package atab

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GembaCore/gemba-core/core"
)

// Freshness says how much the projection is willing to vouch for.
type Freshness string

const (
	// FreshnessFresh — the snapshot is inside the source's budget.
	FreshnessFresh Freshness = "fresh"
	// FreshnessStale — the snapshot is past its budget and a refresh
	// has not succeeded. Values are last-known, not current.
	FreshnessStale Freshness = "stale"
	// FreshnessUnknown — nothing has ever been observed for the source.
	FreshnessUnknown Freshness = "unknown"
)

// Snapshot is one source's observed state at one instant. Everything the
// projection needs beyond a single issue lives here, so projecting an
// issue never reaches back to the network.
type Snapshot struct {
	Source     SourceID
	ObservedAt time.Time
	Freshness  Freshness
	// Issues is the observed set, keyed by ref.
	Issues map[IssueRef]Issue
	// CrossSource resolves a blocker that lives outside this snapshot.
	// Nil means every out-of-snapshot blocker reads as
	// [BlockerUnknown], which is the fail-closed direction.
	CrossSource func(IssueRef) BlockerState
}

// Projector turns raw GitHub issues into core WorkItems for one source.
type Projector struct {
	cfg SourceConfig
	// now is injectable so lease freshness and staleness are
	// reproducible under test.
	now func() time.Time
}

// NewProjector returns a projector for cfg. cfg must already be
// normalised.
func NewProjector(cfg SourceConfig) *Projector {
	return &Projector{cfg: cfg, now: time.Now}
}

// ProjectAll renders every issue in snap, plus the derived readiness that
// depends on the whole set (cycles, blocker resolution). Results are
// sorted by UpdatedAt descending, which is the adaptor's natural order.
func (p *Projector) ProjectAll(snap Snapshot) []core.WorkItem {
	now := p.now().UTC()
	edges := p.blockedByEdges(snap)
	cycles := DetectCycles(edges)

	out := make([]core.WorkItem, 0, len(snap.Issues))
	refs := make([]IssueRef, 0, len(snap.Issues))
	for ref := range snap.Issues {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })

	for _, ref := range refs {
		out = append(out, p.project(snap, snap.Issues[ref], cycles[ref], now))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

// ProjectOne renders a single issue. Cycle membership needs the whole
// graph, so a single-issue projection resolves blockers but reports
// cycle membership only when snap already carries the neighbourhood.
func (p *Projector) ProjectOne(snap Snapshot, issue Issue) core.WorkItem {
	cycles := DetectCycles(p.blockedByEdges(snap))
	return p.project(snap, issue, cycles[issue.Ref], p.now().UTC())
}

// blockedByEdges builds the directed blocked_by graph over the snapshot.
// Cross-repo edges are opaque refs like every other node, so a cycle
// running through one is found the same way.
func (p *Projector) blockedByEdges(snap Snapshot) map[IssueRef][]IssueRef {
	edges := make(map[IssueRef][]IssueRef, len(snap.Issues))
	for ref, issue := range snap.Issues {
		meta := ParseMeta(issue.Body, p.cfg.Org)
		targets := make([]IssueRef, 0, len(meta.BlockedBy))
		for _, e := range meta.BlockedBy {
			targets = append(targets, e.Resolve(ref))
		}
		edges[ref] = targets
	}
	return edges
}

func (p *Projector) project(snap Snapshot, issue Issue, inCycle bool, now time.Time) core.WorkItem {
	meta := ParseMeta(issue.Body, p.cfg.Org)
	criteria := ParseAcceptanceCriteria(issue.Body)
	lease, hasLease := LatestLease(issue.Comments)

	boardStatus := p.boardStatus(issue)
	status, stateCategory := p.status(issue, boardStatus, snap.Freshness)

	openPRs := 0
	for _, pr := range issue.LinkedPRs {
		if strings.EqualFold(pr.State, "OPEN") && !pr.Merged {
			openPRs++
		}
	}

	readiness := EvaluateReadiness(ReadinessInput{
		Self:           issue.Ref,
		Closed:         strings.EqualFold(issue.State, "CLOSED"),
		StateReason:    issue.StateReason,
		Labels:         issue.Labels,
		Assignees:      issue.Assignees,
		BoardStatus:    boardStatus,
		Meta:           meta,
		Lease:          lease,
		HasLease:       hasLease,
		Parent:         issue.Parent,
		SubIssues:      issue.SubIssues,
		OpenLinkedPRs:  openPRs,
		InCycle:        inCycle,
		Fresh:          snap.Freshness == FreshnessFresh,
		Now:            now,
		ResolveBlocker: p.blockerResolver(snap),
	})

	wi := core.WorkItem{
		ID:                  issue.Ref.WorkItemID(p.cfg.ID),
		PrimaryRepositoryID: issue.Ref.RepositoryID(),
		Kind:                p.kind(meta, issue.Labels),
		Title:               issue.Title,
		Description:         issue.Body,
		Status:              status,
		StateCategory:       stateCategory,
		Labels:              append([]string(nil), issue.Labels...),
		CreatedAt:           issue.CreatedAt,
		UpdatedAt:           issue.UpdatedAt,
		Relationships:       p.relationships(issue, meta),
		Evidence:            CollectEvidence(p.cfg.ID, issue),
		DoD:                 dodFrom(criteria),
		Custom:              p.custom(snap, issue, meta, criteria, readiness, boardStatus, lease, hasLease),
	}
	wi.NormalizeRepositories()

	if pr := p.priority(issue); pr != nil {
		wi.Priority = pr
	}
	// The GitHub assignee is who owns the issue. It is set from the
	// assignee list alone and never inferred from a lease: a worker
	// holding a lease is running the work, which the lease field says,
	// and conflating the two would show a crashed worker as the owner.
	if len(issue.Assignees) > 0 {
		wi.Assignee = &core.AgentRef{
			ID:        AgentIDFor(p.cfg.ID, issue.Assignees[0]),
			Name:      issue.Assignees[0],
			Kind:      core.AgentKindHuman,
			Workspace: string(p.cfg.ID),
		}
	}
	if issue.Author != "" {
		wi.Owner = &core.AgentRef{
			ID:        AgentIDFor(p.cfg.ID, issue.Author),
			Name:      issue.Author,
			Kind:      core.AgentKindHuman,
			Workspace: string(p.cfg.ID),
		}
	}
	return wi
}

// blockerResolver answers a blocked_by target from the snapshot, then
// from the federated resolver, and finally reports unknown. Unknown is
// the fail-closed answer: an edge this deployment cannot see holds.
func (p *Projector) blockerResolver(snap Snapshot) func(IssueRef) BlockerState {
	return func(target IssueRef) BlockerState {
		if issue, ok := snap.Issues[target]; ok {
			if strings.EqualFold(issue.State, "CLOSED") {
				return BlockerResolved
			}
			if p.boardStatus(issue) == StatusDone {
				return BlockerResolved
			}
			return BlockerOpen
		}
		if snap.CrossSource != nil {
			return snap.CrossSource(target)
		}
		return BlockerUnknown
	}
}

// boardStatus reads the source's Status field off the project row, and
// returns "" when the issue is not on the board.
func (p *Projector) boardStatus(issue Issue) string {
	if issue.ProjectItem == nil {
		return ""
	}
	if p.cfg.ProjectNumber != 0 && issue.ProjectItem.ProjectNumber != p.cfg.ProjectNumber {
		// A row on some other board is not this source's board.
		return ""
	}
	return issue.ProjectItem.Field(p.cfg.FieldName(FieldStatus))
}

// status resolves the native status token and its core bucket.
//
// The board wins when it carries a Status this adaptor's StateMap knows.
// A board value the map does not know becomes StatusUnknown rather than
// a guess, because a card placed in the wrong lane is a worse failure
// than a card that admits it does not know.
func (p *Projector) status(issue Issue, boardStatus string, fresh Freshness) (string, core.StateCategory) {
	if boardStatus != "" {
		if cat, ok := StateMap[boardStatus]; ok {
			return boardStatus, cat
		}
		return StatusUnknown, StateMap[StatusUnknown]
	}
	switch {
	case strings.EqualFold(issue.State, "CLOSED"):
		if strings.EqualFold(issue.StateReason, "NOT_PLANNED") {
			return StatusClosedNotPlanned, StateMap[StatusClosedNotPlanned]
		}
		return StatusClosedCompleted, StateMap[StatusClosedCompleted]
	case strings.EqualFold(issue.State, "OPEN"):
		return StatusOpen, StateMap[StatusOpen]
	case fresh != FreshnessFresh:
		// Nothing was ever observed for this item and the source is not
		// current: say stale rather than inventing an open issue.
		return StatusStale, StateMap[StatusStale]
	default:
		return StatusUnknown, StateMap[StatusUnknown]
	}
}

// kind maps the atab-meta type onto the core work-item kind. A type the
// board declares as an epic becomes the core "milestone" kind so the
// SPA's milestone chrome picks it up; everything else passes through.
func (p *Projector) kind(meta Meta, labels []string) string {
	t := TypeOf(labels, meta)
	if t == EpicType {
		return core.KindMilestone
	}
	if t == "" {
		return "task"
	}
	return t
}

// relationships emits the three core edges plus the atab-native
// provenance edge.
//
// blocked_by is recorded on the blocked issue but the core "blocks" edge
// runs the other way, so the projection inverts it: the blocker is From,
// this issue is To. Getting that backwards would reverse every
// dependency arrow on the board.
func (p *Projector) relationships(issue Issue, meta Meta) []core.Relationship {
	self := issue.Ref.WorkItemID(p.cfg.ID)
	var out []core.Relationship

	if issue.Parent != nil {
		out = append(out, core.Relationship{
			Kind: core.RelParentChild,
			From: issue.Parent.Ref.WorkItemID(p.cfg.ID),
			To:   self,
		})
	}
	for _, sub := range issue.SubIssues {
		out = append(out, core.Relationship{
			Kind: core.RelParentChild,
			From: self,
			To:   sub.Ref.WorkItemID(p.cfg.ID),
		})
	}
	for _, edge := range meta.BlockedBy {
		out = append(out, core.Relationship{
			Kind: core.RelBlocks,
			From: edge.Resolve(issue.Ref).WorkItemID(p.cfg.ID),
			To:   self,
		})
	}
	if meta.DiscoveredFrom != nil {
		// Provenance, not ordering. The core has no discovery edge, so
		// it renders as relates_to unless the atab extension renderer is
		// loaded; the declared EdgeDiscoveredFrom extension is what tells
		// the SPA the richer name.
		out = append(out, core.Relationship{
			Kind: core.RelRelatesTo,
			From: meta.DiscoveredFrom.Resolve(issue.Ref).WorkItemID(p.cfg.ID),
			To:   self,
		})
	}
	return out
}

func (p *Projector) priority(issue Issue) *int {
	raw := issue.ProjectItem.Field(p.cfg.FieldName(FieldPriority))
	if raw == "" {
		return nil
	}
	// P0 is the most urgent, and core treats a lower Priority int as
	// more urgent, so the digit maps straight through.
	if len(raw) == 2 && (raw[0] == 'P' || raw[0] == 'p') {
		if n, err := strconv.Atoi(raw[1:]); err == nil {
			return &n
		}
	}
	return nil
}

// dodFrom turns the acceptance-criteria checklist into the core
// definition-of-done record. DoD is informational: core never blocks a
// transition on it, and neither does this adaptor.
//
// core.DefinitionOfDone carries criteria as plain strings, so the ticked
// state travels in the marker each line keeps. The structured checklist
// with its per-item boolean is still available on Custom under
// atab_acceptance_criteria for a renderer that wants checkboxes.
func dodFrom(criteria []Criterion) *core.DefinitionOfDone {
	if len(criteria) == 0 {
		return nil
	}
	dod := &core.DefinitionOfDone{Version: fmt.Sprintf("atab-meta-v%d", MetaSchemaVersion)}
	for _, c := range criteria {
		marker := "[ ] "
		if c.Done {
			marker = "[x] "
		}
		dod.AcceptanceCriteria = append(dod.AcceptanceCriteria, marker+c.Text)
	}
	return dod
}

func (p *Projector) custom(
	snap Snapshot,
	issue Issue,
	meta Meta,
	criteria []Criterion,
	readiness ReadinessResult,
	boardStatus string,
	lease Lease,
	hasLease bool,
) map[string]any {
	freshness := snap.Freshness
	if freshness == "" {
		freshness = FreshnessUnknown
	}
	done := 0
	for _, c := range criteria {
		if c.Done {
			done++
		}
	}
	subsClosed := 0
	for _, s := range issue.SubIssues {
		if strings.EqualFold(s.State, "CLOSED") {
			subsClosed++
		}
	}

	out := map[string]any{
		FieldKeySource:      string(p.cfg.ID),
		FieldKeySourceOrg:   p.cfg.Org,
		FieldKeyRepo:        issue.Ref.Owner + "/" + issue.Ref.Repo,
		FieldKeyIssueNumber: issue.Ref.Number,
		FieldKeyType:        meta.Type,
		FieldKeyAutonomy:    string(meta.Autonomy),
		FieldKeyReadiness:   string(readiness.State),
		FieldKeyMetaState:   string(meta.State),
		FieldKeyFreshness:   string(freshness),
		FieldKeyObservedAt:  snap.ObservedAt.UTC().Format(time.RFC3339),
		FieldKeyBoardStatus: boardStatus,
		FieldKeyCriteria: map[string]any{
			"total": len(criteria),
			"done":  done,
			"items": criteria,
		},
	}
	if readiness.Reason != "" {
		out[FieldKeyReadyReason] = readiness.Reason
	}
	if len(readiness.BlockedBy) > 0 {
		out["atab_blocked_by"] = renderRefs(readiness.BlockedBy)
	}
	if meta.DiscoveredFrom != nil {
		out["atab_discovered_from"] = meta.DiscoveredFrom.Resolve(issue.Ref).String()
	}
	if len(issue.SubIssues) > 0 {
		out[FieldKeySubIssues] = fmt.Sprintf("%d/%d", subsClosed, len(issue.SubIssues))
	}
	if v := issue.ProjectItem.Field(p.cfg.FieldName(FieldPriority)); v != "" {
		out[FieldKeyPriority] = v
	}
	if v := issue.ProjectItem.Field(p.cfg.FieldName(FieldArea)); v != "" {
		out[FieldKeyArea] = v
	}
	if v := issue.ProjectItem.Field(p.cfg.FieldName(FieldEstimate)); v != "" {
		out[FieldKeyEstimate] = v
	}
	// The lease is reported whether or not it is fresh. An expired lease
	// is the evidence that a worker died holding the issue, and hiding it
	// would erase the only trace of that.
	if hasLease {
		out[FieldKeyLease] = map[string]any{
			"holder":   lease.Holder,
			"instance": lease.Instance,
			"expires":  lease.Expires.UTC().Format(time.RFC3339),
			"fresh":    lease.Fresh(p.now().UTC()),
		}
	}
	if len(meta.RequiresEnv) > 0 {
		names := make([]string, 0, len(meta.RequiresEnv))
		for _, r := range meta.RequiresEnv {
			names = append(names, r.Name+"@"+r.Scope)
		}
		out[FieldKeyRequiresEnv] = strings.Join(names, ", ")
	}
	if len(meta.ScopeClauses) > 0 {
		out[FieldKeyScopeClauses] = strings.Join(meta.ScopeClauses, ", ")
	}
	if len(meta.Warnings) > 0 {
		out[FieldKeyWarnings] = strings.Join(meta.Warnings, "; ")
	}
	if issue.URL != "" {
		out["atab_url"] = issue.URL
	}
	return out
}
