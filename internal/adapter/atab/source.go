package atab

import (
	"context"
	"time"
)

// Issue is the raw GitHub projection one Client returns. It is
// deliberately close to the GraphQL shape: every field the adaptor
// derives from is visible here, so a fixture is a faithful stand-in for
// a live fetch and the projection logic can be tested without a network.
type Issue struct {
	Ref IssueRef `json:"ref"`

	NodeID string `json:"node_id,omitempty"`
	Title  string `json:"title"`
	Body   string `json:"body,omitempty"`
	URL    string `json:"url,omitempty"`

	// State is GitHub's issue state: "OPEN" or "CLOSED".
	State string `json:"state"`
	// StateReason is GitHub's close reason: "COMPLETED", "NOT_PLANNED",
	// "REOPENED", or empty. It is what separates a finished issue from a
	// cancelled one on the board.
	StateReason string `json:"state_reason,omitempty"`

	Labels    []string `json:"labels,omitempty"`
	Assignees []string `json:"assignees,omitempty"`
	Author    string   `json:"author,omitempty"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`

	// Parent is GitHub's native sub-issue link to this issue's parent.
	// The atab-meta block deliberately does not duplicate it.
	Parent *ParentRef `json:"parent,omitempty"`
	// SubIssues are the native children, in GitHub's order.
	SubIssues []SubIssueRef `json:"sub_issues,omitempty"`

	// Comments carries the recent comments the adaptor needs for lease
	// detection. Clients fetch a bounded tail, newest last.
	Comments []Comment `json:"comments,omitempty"`

	// ProjectItem is this issue's row on the source's project board, or
	// nil when the issue is not on the board.
	ProjectItem *ProjectItem `json:"project_item,omitempty"`

	// LinkedPRs are pull requests that reference the issue via a closing
	// keyword, with their check state attached.
	LinkedPRs []PullRequest `json:"linked_prs,omitempty"`
}

// ParentRef is the native parent link plus the state the readiness rules
// need: an issue whose parent is closed is an orphan and drops out.
type ParentRef struct {
	Ref   IssueRef `json:"ref"`
	Title string   `json:"title,omitempty"`
	State string   `json:"state"`
	URL   string   `json:"url,omitempty"`
}

// SubIssueRef is one native child link.
type SubIssueRef struct {
	Ref   IssueRef `json:"ref"`
	Title string   `json:"title,omitempty"`
	State string   `json:"state"`
	URL   string   `json:"url,omitempty"`
}

// Comment is one issue comment, reduced to what lease detection reads.
type Comment struct {
	Body      string    `json:"body"`
	Author    string    `json:"author,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ProjectItem is the issue's row on a Projects v2 board. Fields carries
// every single-select, text, number, date and iteration value the board
// declares, keyed by field name as the board spells it ("Status",
// "Priority", "Area", "Estimate"). Keeping it a map rather than a fixed
// struct is what lets a second federated source declare different fields
// without a code change.
type ProjectItem struct {
	ItemID        string            `json:"item_id,omitempty"`
	ProjectNumber int               `json:"project_number"`
	ProjectTitle  string            `json:"project_title,omitempty"`
	Fields        map[string]string `json:"fields,omitempty"`
}

// Field returns the board value for name, or "" when the board has no
// such field or the row leaves it unset.
func (p *ProjectItem) Field(name string) string {
	if p == nil || p.Fields == nil {
		return ""
	}
	return p.Fields[name]
}

// PullRequest is a pull request linked to an issue by a closing
// reference, carrying the check evidence the board renders.
type PullRequest struct {
	Ref    IssueRef `json:"ref"`
	Title  string   `json:"title,omitempty"`
	URL    string   `json:"url,omitempty"`
	State  string   `json:"state"` // OPEN | CLOSED | MERGED
	Merged bool     `json:"merged"`
	Draft  bool     `json:"draft"`

	// HeadSHA is the commit the checks ran against.
	HeadSHA string `json:"head_sha,omitempty"`
	// CheckRollup is GitHub's aggregate status: SUCCESS, FAILURE,
	// PENDING, ERROR, EXPECTED, or empty when no checks reported.
	CheckRollup string `json:"check_rollup,omitempty"`
	// Checks are the individual runs behind the rollup.
	Checks []CheckRun `json:"checks,omitempty"`

	// Comments on the PR, used to recover local-CI and verifier
	// verdicts the pipeline posts there.
	Comments []Comment `json:"comments,omitempty"`

	UpdatedAt time.Time `json:"updated_at"`
}

// CheckRun is one check on a pull request head.
type CheckRun struct {
	Name        string     `json:"name"`
	Status      string     `json:"status,omitempty"`     // QUEUED | IN_PROGRESS | COMPLETED
	Conclusion  string     `json:"conclusion,omitempty"` // SUCCESS | FAILURE | ...
	URL         string     `json:"url,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// FetchOptions narrows a Client fetch.
type FetchOptions struct {
	// Repos restricts the fetch to these repositories. Empty means
	// "every repository the source declares".
	Repos []string
	// UpdatedSince asks the client for issues touched at or after this
	// instant. The zero value means a full fetch. This is what makes
	// refresh incremental rather than a repeated full scan.
	UpdatedSince time.Time
	// Limit caps the number of issues returned. 0 means the client's
	// own default.
	Limit int
	// IncludeClosed pulls closed issues too. Readiness never needs them,
	// but the board's Done column and the blocker resolver do.
	IncludeClosed bool
}

// Client is the port the adaptor reads GitHub through. One
// implementation talks to the real API; the fixture implementation in
// the tests replays recorded payloads. Nothing below this interface is
// allowed to mutate anything: the adaptor is read-only by construction,
// not by convention.
type Client interface {
	// SourceID names the source this client reads.
	SourceID() SourceID

	// FetchIssues returns issues matching opts. Implementations return a
	// tagged *core.AdaptorError on failure: KindRequestFailed for
	// transport trouble, KindRateLimited when GitHub throttles,
	// KindCapabilityDenied when the token cannot see the source.
	//
	// A non-empty slice alongside a non-nil error means a partial fetch:
	// part of the source answered and part did not. Callers keep the rows
	// and treat the source as degraded. Returning only the rows would
	// present an incomplete source as a complete one, and returning only
	// the error would throw away work that was fetched successfully.
	FetchIssues(ctx context.Context, opts FetchOptions) ([]Issue, error)

	// FetchIssue returns one issue by ref, or a KindSessionNotFound
	// error that also satisfies errors.Is(err, core.ErrNotFound).
	FetchIssue(ctx context.Context, ref IssueRef) (Issue, error)

	// Ping is a cheap bounded liveness probe used by the health surface.
	// It must not depend on the size of the source.
	Ping(ctx context.Context) error
}
