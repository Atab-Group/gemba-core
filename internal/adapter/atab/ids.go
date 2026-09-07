package atab

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/GembaCore/gemba-core/core"
)

// SourceID names one federated work source: an org + project + repo set
// the operator has allowlisted. It is the first segment of every
// [core.WorkItemID] this adaptor emits, which is what keeps ids from two
// sources apart even when both surface "Atab-Group/Product-Seela#42".
//
// The grammar is deliberately narrow (lowercase alphanumerics and single
// interior hyphens) so a source id can never contain the "/" or "~"
// separators the id scheme relies on.
type SourceID string

var sourceIDRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Validate reports whether s is a well-formed source id.
func (s SourceID) Validate() error {
	if s == "" {
		return core.NewAdaptorError(core.KindValidation,
			"atab: source id must not be empty")
	}
	if len(s) > 64 {
		return core.NewAdaptorError(core.KindValidation,
			"atab: source id %q exceeds 64 characters", string(s))
	}
	if !sourceIDRe.MatchString(string(s)) {
		return core.NewAdaptorError(core.KindValidation,
			"atab: source id %q must match %s", string(s), sourceIDRe.String())
	}
	return nil
}

// repoSeparator joins owner and repo inside the second segment of a
// WorkItemID. GitHub forbids "~" in both owner logins and repository
// names, so it can never appear inside either half and the split is
// unambiguous in both directions.
const repoSeparator = "~"

// IssueRef identifies one GitHub issue inside one source. It is the
// native key the adaptor works in before ids are qualified.
type IssueRef struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

// String renders the ref in the "owner/repo#number" form atab-meta uses
// for cross-repo edges.
func (r IssueRef) String() string {
	return fmt.Sprintf("%s/%s#%d", r.Owner, r.Repo, r.Number)
}

// Validate rejects a ref that could not name a real GitHub issue.
func (r IssueRef) Validate() error {
	switch {
	case r.Owner == "":
		return core.NewAdaptorError(core.KindValidation, "atab: issue ref owner is empty")
	case r.Repo == "":
		return core.NewAdaptorError(core.KindValidation, "atab: issue ref repo is empty")
	case r.Number <= 0:
		return core.NewAdaptorError(core.KindValidation,
			"atab: issue ref %s/%s has non-positive number %d", r.Owner, r.Repo, r.Number)
	case strings.Contains(r.Owner, repoSeparator) || strings.Contains(r.Repo, repoSeparator):
		return core.NewAdaptorError(core.KindValidation,
			"atab: issue ref %s/%s contains the reserved %q separator",
			r.Owner, r.Repo, repoSeparator)
	case strings.Contains(r.Owner, "/") || strings.Contains(r.Repo, "/"):
		return core.NewAdaptorError(core.KindValidation,
			"atab: issue ref %s/%s contains a literal slash", r.Owner, r.Repo)
	}
	return nil
}

// WorkItemID renders the globally collision-safe id for ref inside
// source. Shape: "<source>/<owner>~<repo>/<number>", which satisfies the
// core "<workspace>/<repo>/<native-id>" contract while keeping the owner
// visible: two sources may both carry a repo called "platform", and two
// orgs may both carry issue #42.
func (r IssueRef) WorkItemID(source SourceID) core.WorkItemID {
	return core.WorkItemID(fmt.Sprintf("%s/%s%s%s/%d",
		source, r.Owner, repoSeparator, r.Repo, r.Number))
}

// ParseWorkItemID splits a qualified id back into its source and ref.
// Every failure is a KindValidation AdaptorError naming the offending
// id, because the only caller that can produce one is a client sending
// an id the adaptor never emitted.
func ParseWorkItemID(id core.WorkItemID) (SourceID, IssueRef, error) {
	parts := strings.Split(string(id), "/")
	if len(parts) != 3 {
		return "", IssueRef{}, core.NewAdaptorError(core.KindValidation,
			"atab: work item id %q must have the form <source>/<owner>~<repo>/<number>", id)
	}
	source := SourceID(parts[0])
	if err := source.Validate(); err != nil {
		return "", IssueRef{}, err
	}
	ownerRepo := strings.Split(parts[1], repoSeparator)
	if len(ownerRepo) != 2 || ownerRepo[0] == "" || ownerRepo[1] == "" {
		return "", IssueRef{}, core.NewAdaptorError(core.KindValidation,
			"atab: work item id %q segment %q must have the form <owner>~<repo>", id, parts[1])
	}
	number, err := strconv.Atoi(parts[2])
	if err != nil || number <= 0 {
		return "", IssueRef{}, core.NewAdaptorError(core.KindValidation,
			"atab: work item id %q segment %q must be a positive issue number", id, parts[2])
	}
	ref := IssueRef{Owner: ownerRepo[0], Repo: ownerRepo[1], Number: number}
	if err := ref.Validate(); err != nil {
		return "", IssueRef{}, err
	}
	return source, ref, nil
}

// RepositoryID is the core repository slug for ref: "owner~repo", the
// same second segment the WorkItemID carries. Keeping the two in lockstep
// means a caller can derive one from the other without a lookup.
func (r IssueRef) RepositoryID() core.RepositoryID {
	return core.RepositoryID(r.Owner + repoSeparator + r.Repo)
}

// AgentID qualifies a GitHub login inside a source so two orgs with the
// same login (a shared bot account, a consultant in both) do not collapse
// into one agent on the board.
func AgentIDFor(source SourceID, login string) core.AgentID {
	return core.AgentID(fmt.Sprintf("%s/github/%s", source, login))
}
