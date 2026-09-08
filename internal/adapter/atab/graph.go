package atab

import (
	"sort"
	"strings"

	"github.com/GembaCore/gemba-core/core"
)

// EdgeKind names one relation in an issue's neighbourhood. The four are
// kept apart rather than folded into one "related" list because they
// answer different questions and fail in different directions: a blocker
// stops work, a dependent is what finishing unblocks, a child is where
// the work actually happens, and provenance only says where a thing came
// from.
type EdgeKind string

const (
	// EdgeKindBlockedBy: declared in this issue's atab-meta blocked_by. The
	// edge is recorded on the blocked issue, pointing upstream.
	EdgeKindBlockedBy EdgeKind = "blocked_by"
	// EdgeKindBlocks: the reverse. Nothing declares it, so it is built by
	// inverting every blocked_by in the source. Without it a blocker
	// cannot say what finishing it would release, which is the question
	// worth asking of a blocker.
	EdgeKindBlocks EdgeKind = "blocks"
	// EdgeKindParent: GitHub's native sub-issue link, upward. Native rather
	// than atab-meta on purpose: the schema deliberately does not carry a
	// parent field, because duplicating GitHub's own link would drift.
	EdgeKindParent EdgeKind = "parent"
	// EdgeKindChild: the same native link, downward.
	EdgeKindChild EdgeKind = "child"
	// EdgeKindDiscoveredFrom: the issue whose work surfaced this one.
	// Provenance, never ordering: it must not be rendered as a blocker or
	// it would hold work that nothing is actually waiting on.
	EdgeKindDiscoveredFrom EdgeKind = "discovered_from"
)

// GraphEdge is one neighbour, resolved as far as this deployment can
// resolve it.
type GraphEdge struct {
	Kind EdgeKind `json:"kind"`
	// ID is the qualified work item id when the target is inside a
	// source this deployment reads. Empty when it is not, because an id
	// this board cannot resolve is not one a caller should follow.
	ID core.WorkItemID `json:"id,omitempty"`
	// Ref is the target in the org's own notation, always present. It is
	// what a person recognises and what the GitHub link is built from.
	Ref string `json:"ref"`
	// Repo and Number carry the same thing in parts, for a renderer that
	// wants to group by repository.
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	// Title is the target's title when it is readable here, empty when
	// it is not. An empty title with State unknown is exactly the case a
	// renderer must show differently.
	Title string `json:"title,omitempty"`
	// Status is the target's projected status when readable.
	Status string `json:"status,omitempty"`
	// State says whether the edge holds. Unknown is the fail-closed
	// value: the target lives somewhere this deployment cannot read, so
	// the edge is treated as holding rather than assumed clear.
	State BlockerState `json:"state"`
	// CrossRepo and CrossSource say why a target could not be read, and
	// let a renderer mark a legitimate cross-boundary edge apart from a
	// broken one.
	CrossRepo   bool `json:"cross_repo,omitempty"`
	CrossSource bool `json:"cross_source,omitempty"`
	// URL is the target's GitHub page. Always constructible from the
	// ref, even when the target is unreadable here, which is the point:
	// an edge this board cannot follow is one a person still can.
	URL string `json:"url"`
}

// Neighbourhood is one issue's immediate graph, grouped by what each
// group means to somebody deciding whether to work on it.
type Neighbourhood struct {
	BlockedBy      []GraphEdge `json:"blocked_by"`
	Blocks         []GraphEdge `json:"blocks"`
	Parent         *GraphEdge  `json:"parent,omitempty"`
	Children       []GraphEdge `json:"children"`
	DiscoveredFrom *GraphEdge  `json:"discovered_from,omitempty"`
	// Unresolved counts edges whose target this deployment cannot read.
	// A caller that renders nothing else can still say "this graph is
	// incomplete", which is the honest thing to say about it.
	Unresolved int `json:"unresolved"`
}

// issueURL is the target's page on GitHub. It is built from the ref
// rather than read from the issue, so an edge into a repository this
// deployment cannot read still gets a working link.
func issueURL(ref IssueRef) string {
	return "https://github.com/" + ref.Owner + "/" + ref.Repo + "/issues/" +
		itoa(ref.Number)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// GraphBuilder resolves one source's neighbourhood queries.
//
// It holds the reverse blocked_by index, which is the only part of the
// graph that cannot be answered from a single issue: blocked_by is
// recorded on the downstream issue, so "what does finishing this
// release" needs every issue in the source read once.
type GraphBuilder struct {
	cfg    SourceConfig
	snap   Snapshot
	blocks map[IssueRef][]IssueRef
}

// NewGraphBuilder indexes snap. The reverse index is built once per call
// rather than per issue, because building it per issue would make
// rendering a board of N issues an O(N squared) walk of the same edges.
func NewGraphBuilder(cfg SourceConfig, snap Snapshot) *GraphBuilder {
	g := &GraphBuilder{cfg: cfg, snap: snap, blocks: map[IssueRef][]IssueRef{}}
	for ref, issue := range snap.Issues {
		meta := ParseMeta(issue.Body, cfg.Org)
		for _, edge := range meta.BlockedBy {
			target := edge.Resolve(ref)
			g.blocks[target] = append(g.blocks[target], ref)
		}
	}
	for target := range g.blocks {
		sort.Slice(g.blocks[target], func(i, j int) bool {
			return g.blocks[target][i].String() < g.blocks[target][j].String()
		})
	}
	return g
}

// resolve turns one target ref into a rendered edge.
func (g *GraphBuilder) resolve(kind EdgeKind, self, target IssueRef) GraphEdge {
	edge := GraphEdge{
		Kind:      kind,
		Ref:       target.String(),
		Repo:      target.Owner + "/" + target.Repo,
		Number:    target.Number,
		URL:       issueURL(target),
		CrossRepo: target.Owner != self.Owner || target.Repo != self.Repo,
	}

	if issue, ok := g.snap.Issues[target]; ok {
		edge.ID = target.WorkItemID(g.cfg.ID)
		edge.Title = issue.Title
		if strings.EqualFold(issue.State, "CLOSED") {
			edge.State = BlockerResolved
			edge.Status = StatusDone
		} else {
			edge.State = BlockerOpen
			edge.Status = issue.ProjectItem.Field(g.cfg.FieldName(FieldStatus))
			if edge.Status == "" {
				edge.Status = StatusOpen
			}
		}
		return edge
	}

	// Not in this snapshot. Either another source in this deployment can
	// speak for it, or nothing here can and the edge fails closed.
	if g.snap.CrossSource != nil {
		state := g.snap.CrossSource(target)
		edge.State = state
		edge.CrossSource = true
		if state != BlockerUnknown {
			return edge
		}
		return edge
	}
	edge.State = BlockerUnknown
	edge.CrossSource = true
	return edge
}

// Neighbourhood renders the graph around one issue.
func (g *GraphBuilder) Neighbourhood(issue Issue) Neighbourhood {
	self := issue.Ref
	meta := ParseMeta(issue.Body, g.cfg.Org)
	out := Neighbourhood{
		BlockedBy: []GraphEdge{},
		Blocks:    []GraphEdge{},
		Children:  []GraphEdge{},
	}

	for _, e := range meta.BlockedBy {
		out.BlockedBy = append(out.BlockedBy, g.resolve(EdgeKindBlockedBy, self, e.Resolve(self)))
	}
	for _, target := range g.blocks[self] {
		out.Blocks = append(out.Blocks, g.resolve(EdgeKindBlocks, self, target))
	}
	if issue.Parent != nil {
		p := g.resolve(EdgeKindParent, self, issue.Parent.Ref)
		// The native link carries the parent's own title and state, so
		// prefer them: the parent can sit in a repository the source does
		// not declare while GitHub still tells us about it.
		if p.Title == "" {
			p.Title = issue.Parent.Title
		}
		if issue.Parent.State != "" && p.State == BlockerUnknown {
			p.State = closedToState(issue.Parent.State)
		}
		out.Parent = &p
	}
	for _, sub := range issue.SubIssues {
		c := g.resolve(EdgeKindChild, self, sub.Ref)
		if c.Title == "" {
			c.Title = sub.Title
		}
		if sub.State != "" && c.State == BlockerUnknown {
			c.State = closedToState(sub.State)
		}
		out.Children = append(out.Children, c)
	}
	if meta.DiscoveredFrom != nil {
		d := g.resolve(EdgeKindDiscoveredFrom, self, meta.DiscoveredFrom.Resolve(self))
		out.DiscoveredFrom = &d
	}

	for _, group := range [][]GraphEdge{out.BlockedBy, out.Blocks, out.Children} {
		for _, e := range group {
			if e.State == BlockerUnknown {
				out.Unresolved++
			}
		}
	}
	if out.Parent != nil && out.Parent.State == BlockerUnknown {
		out.Unresolved++
	}
	if out.DiscoveredFrom != nil && out.DiscoveredFrom.State == BlockerUnknown {
		out.Unresolved++
	}
	return out
}

// closedToState maps GitHub's own issue state onto the edge vocabulary.
func closedToState(state string) BlockerState {
	if strings.EqualFold(state, "CLOSED") {
		return BlockerResolved
	}
	return BlockerOpen
}
