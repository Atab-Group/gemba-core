package atab

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// MetaSchemaVersion is the atab-meta schema this adaptor reads. v5 is
// purely additive over v1–v4, so an older block parses unchanged. The
// adaptor does not read, write or infer a v6.
const MetaSchemaVersion = 5

// Autonomy is the execution-autonomy tier an issue declares (v3+).
type Autonomy string

const (
	// AutonomyAuto — tier A, the full bot loop may build and merge.
	AutonomyAuto Autonomy = "auto"
	// AutonomyPR — tier B, a bot builds and a human merges. The default
	// for an absent key.
	AutonomyPR Autonomy = "pr"
	// AutonomyHuman — tier C, humans only. Also the fail-safe an
	// out-of-enum value normalises to.
	AutonomyHuman Autonomy = "human"
)

// NormalizeAutonomy applies the schema's two reader rules: an absent key
// reads as "pr", and anything outside the enum reads as "human" with a
// warning. A malformed declaration must never grant more autonomy than a
// human intended, so the fail-safe direction is always downward.
func NormalizeAutonomy(raw string) (Autonomy, string) {
	switch raw {
	case "":
		return AutonomyPR, ""
	case string(AutonomyAuto), string(AutonomyPR), string(AutonomyHuman):
		return Autonomy(raw), ""
	default:
		return AutonomyHuman, fmt.Sprintf(
			"invalid autonomy %q — treating as human", raw)
	}
}

// MetaState classifies what the parser found in an issue body.
type MetaState string

const (
	// MetaPresent — a well-formed block was parsed.
	MetaPresent MetaState = "present"
	// MetaAbsent — no marker at all. The issue is untracked by
	// automation; readiness skips it rather than guessing.
	MetaAbsent MetaState = "absent"
	// MetaMalformed — the marker is there but the YAML behind it does
	// not load, or loads to something other than a mapping. Consumers
	// skip the issue and warn; they never crash.
	MetaMalformed MetaState = "malformed"
)

// EnvRequirement is one entry of the optional requires_env list (v4).
// A bare string entry parses as Name with Scope "deploy".
type EnvRequirement struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
	From  string `json:"from,omitempty"`
	Scope string `json:"scope,omitempty"`
}

// Meta is the parsed atab-meta block.
type Meta struct {
	State     MetaState `json:"state"`
	Type      string    `json:"type,omitempty"`
	Autonomy  Autonomy  `json:"autonomy,omitempty"`
	BlockedBy []Edge    `json:"blocked_by,omitempty"`
	// DiscoveredFrom is provenance, never a blocking edge. Nil when the
	// key is absent.
	DiscoveredFrom *Edge            `json:"discovered_from,omitempty"`
	RequiresEnv    []EnvRequirement `json:"requires_env,omitempty"`
	ScopeClauses   []string         `json:"scope_clauses,omitempty"`
	// Warnings collects non-fatal readings — an invalid autonomy value,
	// a cross-repo edge outside the allowed org. Consumers surface them
	// on the card rather than dropping the issue.
	Warnings []string `json:"warnings,omitempty"`
}

// Edge is one blocked_by or discovered_from target. A same-repo edge
// carries only Number; a cross-repo edge carries Owner and Repo too.
type Edge struct {
	Owner  string `json:"owner,omitempty"`
	Repo   string `json:"repo,omitempty"`
	Number int    `json:"number"`
}

// CrossRepo reports whether the edge names a repo other than the one the
// issue lives in.
func (e Edge) CrossRepo() bool { return e.Owner != "" && e.Repo != "" }

// Resolve returns the absolute IssueRef for the edge, using self as the
// repo for a same-repo (bare int) entry.
func (e Edge) Resolve(self IssueRef) IssueRef {
	if e.CrossRepo() {
		return IssueRef{Owner: e.Owner, Repo: e.Repo, Number: e.Number}
	}
	return IssueRef{Owner: self.Owner, Repo: self.Repo, Number: e.Number}
}

// String renders the edge in its wire form.
func (e Edge) String() string {
	if e.CrossRepo() {
		return fmt.Sprintf("%s/%s#%d", e.Owner, e.Repo, e.Number)
	}
	return strconv.Itoa(e.Number)
}

// metaBlockRe locates the managed block: the HTML comment marker, then
// the yaml fence that immediately follows it. The marker is what a
// parser matches on first; the fence holds the data. Non-greedy so a
// body carrying later fences cannot swallow them into the block.
var metaBlockRe = regexp.MustCompile(
	"(?s)<!--\\s*atab-meta:.*?-->\\s*```yaml\\s*(.*?)\\s*```")

// crossRepoEdgeRe matches the quoted "<owner>/<repo>#<int>" cross-repo
// form. GitHub owner logins and repo names cannot contain "/" or "#",
// which is what makes the split unambiguous.
var crossRepoEdgeRe = regexp.MustCompile(`^([^/\s#]+)/([^/\s#]+)#(\d+)$`)

// ParseMeta reads the atab-meta block out of an issue body.
//
// The three outcomes match the schema's edge cases exactly: no marker is
// [MetaAbsent] (untracked, not an error), a marker whose YAML does not
// load to a mapping is [MetaMalformed] (skip and warn), and anything
// else is [MetaPresent] with the fields normalised.
//
// allowedOrg, when non-empty, org-locks cross-repo edges: an edge naming
// a different org is dropped with a warning rather than followed, which
// is what keeps a leaked legacy reference from pulling an unrelated org's
// issue into the graph.
func ParseMeta(body, allowedOrg string) Meta {
	m := metaBlockRe.FindStringSubmatch(body)
	if m == nil {
		return Meta{State: MetaAbsent}
	}
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(m[1]), &raw); err != nil || raw == nil {
		return Meta{State: MetaMalformed}
	}

	out := Meta{State: MetaPresent}
	if s, ok := raw["type"].(string); ok {
		out.Type = s
	}

	autonomyRaw := ""
	if v, present := raw["autonomy"]; present {
		// A non-string autonomy (a bare YAML bool from `autonomy: yes`)
		// is still out-of-enum, so render it and let the normaliser
		// take it to human rather than silently reading it as absent.
		autonomyRaw = fmt.Sprint(v)
	}
	autonomy, warn := NormalizeAutonomy(autonomyRaw)
	out.Autonomy = autonomy
	if warn != "" {
		out.Warnings = append(out.Warnings, warn)
	}

	for _, entry := range asSlice(raw["blocked_by"]) {
		edge, warn := parseEdge(entry, allowedOrg)
		if warn != "" {
			out.Warnings = append(out.Warnings, warn)
			continue
		}
		out.BlockedBy = append(out.BlockedBy, edge)
	}

	if v, present := raw["discovered_from"]; present && v != nil {
		edge, warn := parseEdge(v, allowedOrg)
		if warn != "" {
			out.Warnings = append(out.Warnings, warn)
		} else {
			out.DiscoveredFrom = &edge
		}
	}

	for _, entry := range asSlice(raw["requires_env"]) {
		if req, ok := parseEnvRequirement(entry); ok {
			out.RequiresEnv = append(out.RequiresEnv, req)
		}
	}

	for _, entry := range asSlice(raw["scope_clauses"]) {
		if s, ok := entry.(string); ok && s != "" {
			out.ScopeClauses = append(out.ScopeClauses, s)
		}
	}

	return out
}

// parseEdge reads one blocked_by / discovered_from entry in either of
// the schema's two forms. The warning string is non-empty when the entry
// must be dropped; the schema's rule is that a bad edge warns and is
// skipped, never that it deadlocks the queue.
func parseEdge(v any, allowedOrg string) (Edge, string) {
	switch t := v.(type) {
	case int:
		if t <= 0 {
			return Edge{}, fmt.Sprintf("edge %d is not a positive issue number", t)
		}
		return Edge{Number: t}, ""
	case string:
		m := crossRepoEdgeRe.FindStringSubmatch(strings.TrimSpace(t))
		if m == nil {
			return Edge{}, fmt.Sprintf("edge %q is not in <owner>/<repo>#<int> form", t)
		}
		if allowedOrg != "" && !strings.EqualFold(m[1], allowedOrg) {
			return Edge{}, fmt.Sprintf(
				"cross-repo edge %q names org %q outside the allowed org %q — skipped",
				t, m[1], allowedOrg)
		}
		n, err := strconv.Atoi(m[3])
		if err != nil || n <= 0 {
			return Edge{}, fmt.Sprintf("edge %q has a non-positive issue number", t)
		}
		return Edge{Owner: m[1], Repo: m[2], Number: n}, ""
	default:
		return Edge{}, fmt.Sprintf("edge %v has unsupported type %T", v, v)
	}
}

func parseEnvRequirement(v any) (EnvRequirement, bool) {
	switch t := v.(type) {
	case string:
		name, scope := splitEnvScope(t)
		if name == "" {
			return EnvRequirement{}, false
		}
		return EnvRequirement{Name: name, Scope: scope}, true
	case map[string]any:
		req := EnvRequirement{Scope: "deploy"}
		if s, ok := t["name"].(string); ok {
			req.Name = s
		}
		if req.Name == "" {
			return EnvRequirement{}, false
		}
		if s, ok := t["value"].(string); ok {
			req.Value = s
		}
		if s, ok := t["from"].(string); ok {
			req.From = s
		}
		if s, ok := t["scope"].(string); ok && s != "" {
			req.Scope = s
		}
		return req, true
	default:
		return EnvRequirement{}, false
	}
}

// splitEnvScope reads the "NAME@pipeline" / "NAME@both" shorthand. A
// bare name is deploy-scoped, which is the schema's stated default.
func splitEnvScope(s string) (name, scope string) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "@"); i > 0 {
		candidate := s[i+1:]
		switch candidate {
		case "deploy", "pipeline", "both":
			return s[:i], candidate
		}
	}
	return s, "deploy"
}

func asSlice(v any) []any {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		return t
	default:
		return []any{t}
	}
}

// --- acceptance criteria ---------------------------------------------

// Criterion is one acceptance-criteria checklist item. The criteria live
// in a plain "## Acceptance Criteria" markdown checklist in the issue
// body, not in the atab-meta block, and that list is the work
// definition's source of truth.
type Criterion struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

var (
	criteriaHeadingRe = regexp.MustCompile(`(?i)^#{1,6}\s*acceptance\s+criteria\s*$`)
	anyHeadingRe      = regexp.MustCompile(`^#{1,6}\s+\S`)
	checklistItemRe   = regexp.MustCompile(`^\s*[-*+]\s+\[([ xX])\]\s+(.*\S)\s*$`)
)

// ParseAcceptanceCriteria extracts the checklist under the body's
// "## Acceptance Criteria" heading. Returns nil when the section is
// absent or holds no checklist items — an issue with no criteria is a
// real state the board must show, not a parse failure.
func ParseAcceptanceCriteria(body string) []Criterion {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	inSection := false
	var out []Criterion
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if criteriaHeadingRe.MatchString(trimmed) {
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		if anyHeadingRe.MatchString(trimmed) {
			break // the next section ends the checklist
		}
		if m := checklistItemRe.FindStringSubmatch(line); m != nil {
			out = append(out, Criterion{
				Text: m[2],
				Done: m[1] != " ",
			})
		}
	}
	return out
}
