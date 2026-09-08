package atab

import (
	"strings"
	"testing"
	"time"
)

func metaBody(lines ...string) string {
	return "<!-- atab-meta: managed by atab-core skills -->\n```yaml\n" +
		strings.Join(lines, "\n") + "\n```\n"
}

func graphIssue(repo string, number int, body string) Issue {
	return Issue{
		Ref:       IssueRef{Owner: "Atab-Group", Repo: repo, Number: number},
		Title:     "issue " + itoa(number),
		State:     "OPEN",
		Body:      body,
		CreatedAt: testNow.Add(-48 * time.Hour),
		UpdatedAt: testNow.Add(-time.Hour),
	}
}

// graphSnapshot builds a snapshot from issues, keyed the way the cache
// keys it.
func graphSnapshot(issues ...Issue) Snapshot {
	m := make(map[IssueRef]Issue, len(issues))
	for _, is := range issues {
		m[is.Ref] = is
	}
	return Snapshot{
		Source:     DemoSourceID,
		ObservedAt: testNow.Add(-time.Minute),
		Freshness:  FreshnessFresh,
		Issues:     m,
	}
}

func graphFor(t *testing.T, snap Snapshot) *GraphBuilder {
	t.Helper()
	cfg := DemoSourceConfig()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return NewGraphBuilder(cfg, snap)
}

func edgeRefs(edges []GraphEdge) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.Ref)
	}
	return out
}

// A bare int in blocked_by is a same-repo blocker, and it resolves
// against the issue's own repository rather than being promoted to a
// qualified ref.
func TestGraph_SameRepoBlockerResolvesInTheIssuesOwnRepo(t *testing.T) {
	blocked := graphIssue("Product-Seela", 10, metaBody("type: bug", "blocked_by:", "  - 9"))
	blocker := graphIssue("Product-Seela", 9, metaBody("type: task"))
	g := graphFor(t, graphSnapshot(blocked, blocker))

	n := g.Neighbourhood(blocked)
	if len(n.BlockedBy) != 1 {
		t.Fatalf("blocked_by = %v, want one edge", edgeRefs(n.BlockedBy))
	}
	e := n.BlockedBy[0]
	// The wire form in atab-meta is the bare int, but a rendered edge is
	// always fully qualified: a consumer holding one edge cannot build a
	// link or an id from "9" without also knowing which repository it was
	// written in. CrossRepo is what says whether a boundary was crossed.
	if e.Ref != "Atab-Group/Product-Seela#9" {
		t.Errorf("ref = %q, want the qualified form", e.Ref)
	}
	if e.CrossRepo {
		t.Error("a same-repo blocker is marked cross-repo")
	}
	if e.Repo != "Atab-Group/Product-Seela" || e.Number != 9 {
		t.Errorf("repo/number = %q/%d, want the issue's own repository", e.Repo, e.Number)
	}
	if e.State != BlockerOpen {
		t.Errorf("state = %q, want open", e.State)
	}
	if e.Title == "" {
		t.Error("a readable blocker carries no title")
	}
	if n.Unresolved != 0 {
		t.Errorf("unresolved = %d, want 0", n.Unresolved)
	}
}

// The quoted "owner/repo#N" form addresses another repository inside the
// same source. It has to resolve against that repository, not against
// the issue's own, or the edge would point at whatever happens to carry
// the same number locally.
func TestGraph_CrossRepoBlockerResolvesAgainstTheNamedRepository(t *testing.T) {
	blocked := graphIssue("Product-Seela", 10,
		metaBody("type: bug", "blocked_by:", `  - "Atab-Group/ATAB-Marketplace#42"`))
	target := graphIssue("ATAB-Marketplace", 42, metaBody("type: task"))
	decoy := graphIssue("Product-Seela", 42, metaBody("type: task"))
	g := graphFor(t, graphSnapshot(blocked, target, decoy))

	n := g.Neighbourhood(blocked)
	if len(n.BlockedBy) != 1 {
		t.Fatalf("blocked_by = %v, want one edge", edgeRefs(n.BlockedBy))
	}
	e := n.BlockedBy[0]
	if e.Ref != "Atab-Group/ATAB-Marketplace#42" {
		t.Errorf("ref = %q, want the qualified cross-repo form", e.Ref)
	}
	if !e.CrossRepo {
		t.Error("a cross-repo blocker is not marked cross-repo")
	}
	if e.Repo != "Atab-Group/ATAB-Marketplace" {
		t.Errorf("repo = %q, want the named repository, not the issue's own", e.Repo)
	}
	if !strings.Contains(e.URL, "ATAB-Marketplace/issues/42") {
		t.Errorf("url = %q, want it to point at the named repository", e.URL)
	}
}

// blocked_by is recorded on the downstream issue, so a blocker has no
// record of what it blocks. Inverting the source's edges is the only way
// to answer "what would finishing this release", which is the question
// worth asking of a blocker.
func TestGraph_DependentsAreBuiltByInvertingBlockedBy(t *testing.T) {
	blocker := graphIssue("Product-Seela", 9, metaBody("type: task"))
	first := graphIssue("Product-Seela", 10, metaBody("type: bug", "blocked_by:", "  - 9"))
	second := graphIssue("Product-Seela", 11, metaBody("type: bug", "blocked_by:", "  - 9"))
	elsewhere := graphIssue("ATAB-Marketplace", 5,
		metaBody("type: bug", "blocked_by:", `  - "Atab-Group/Product-Seela#9"`))
	g := graphFor(t, graphSnapshot(blocker, first, second, elsewhere))

	n := g.Neighbourhood(blocker)
	if len(n.Blocks) != 3 {
		t.Fatalf("blocks = %v, want three dependents", edgeRefs(n.Blocks))
	}
	// Deterministic order, so the panel does not reshuffle between reads.
	got := edgeRefs(n.Blocks)
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Fatalf("dependents are not in a stable order: %v", got)
		}
	}
	if len(n.BlockedBy) != 0 {
		t.Errorf("the blocker itself reports blockers: %v", edgeRefs(n.BlockedBy))
	}
}

// The parent link is GitHub's native sub-issue relation, which the
// atab-meta schema deliberately does not duplicate. Both directions have
// to render from it.
func TestGraph_ParentAndChildrenComeFromTheNativeLink(t *testing.T) {
	parentRef := IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 1}
	childRef := IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 3}

	epic := graphIssue("Product-Seela", 1, metaBody("type: epic"))
	epic.SubIssues = []SubIssueRef{{Ref: childRef, Title: "the child", State: "OPEN"}}

	child := graphIssue("Product-Seela", 3, metaBody("type: task"))
	child.Parent = &ParentRef{Ref: parentRef, Title: "the epic", State: "OPEN"}

	g := graphFor(t, graphSnapshot(epic, child))

	up := g.Neighbourhood(child)
	if up.Parent == nil {
		t.Fatal("child reports no parent")
	}
	if up.Parent.Number != 1 || up.Parent.Kind != EdgeKindParent {
		t.Errorf("parent = %+v, want issue 1 as a parent edge", up.Parent)
	}

	down := g.Neighbourhood(epic)
	if len(down.Children) != 1 {
		t.Fatalf("children = %v, want one", edgeRefs(down.Children))
	}
	if down.Children[0].Number != 3 || down.Children[0].Kind != EdgeKindChild {
		t.Errorf("child = %+v, want issue 3 as a child edge", down.Children[0])
	}
}

// discovered_from is provenance and never ordering. It must not appear
// among the blockers, or the board would hold work that nothing is
// actually waiting on.
func TestGraph_DiscoveredFromIsProvenanceNotABlocker(t *testing.T) {
	found := graphIssue("Product-Seela", 20,
		metaBody("type: bug", "discovered_from: 12"))
	origin := graphIssue("Product-Seela", 12, metaBody("type: feature"))
	g := graphFor(t, graphSnapshot(found, origin))

	n := g.Neighbourhood(found)
	if n.DiscoveredFrom == nil {
		t.Fatal("discovered_from was not resolved")
	}
	if n.DiscoveredFrom.Number != 12 {
		t.Errorf("discovered_from = %d, want 12", n.DiscoveredFrom.Number)
	}
	if len(n.BlockedBy) != 0 {
		t.Errorf("provenance leaked into the blockers: %v", edgeRefs(n.BlockedBy))
	}
	if n.Unresolved != 0 {
		t.Errorf("unresolved = %d, want 0", n.Unresolved)
	}
}

// An edge into something this deployment cannot read fails closed: the
// state is unknown, the edge is counted as unresolved so a renderer can
// mark it, and no title is invented for it. Reporting it as resolved
// would let an unreadable dependency silently unblock a queue.
func TestGraph_AnUnreadableTargetFailsClosedAndIsCounted(t *testing.T) {
	blocked := graphIssue("Product-Seela", 10,
		metaBody("type: bug", "blocked_by:", `  - "Atab-Group/Not-Configured#7"`))
	g := graphFor(t, graphSnapshot(blocked))

	n := g.Neighbourhood(blocked)
	if len(n.BlockedBy) != 1 {
		t.Fatalf("blocked_by = %v, want one edge", edgeRefs(n.BlockedBy))
	}
	e := n.BlockedBy[0]
	if e.State != BlockerUnknown {
		t.Errorf("state = %q, want unknown; an unreadable edge must not read as clear", e.State)
	}
	if e.Title != "" {
		t.Errorf("title = %q, want empty for a target that cannot be read", e.Title)
	}
	if e.ID != "" {
		t.Errorf("id = %q, want empty; this board cannot resolve that target", e.ID)
	}
	if n.Unresolved != 1 {
		t.Errorf("unresolved = %d, want 1", n.Unresolved)
	}
	// The link still works even though the board cannot follow it.
	if !strings.Contains(e.URL, "Not-Configured/issues/7") {
		t.Errorf("url = %q, want a usable GitHub link", e.URL)
	}
}

// A closed blocker no longer holds, which is what lets a queue drain.
func TestGraph_AClosedBlockerResolves(t *testing.T) {
	blocked := graphIssue("Product-Seela", 10, metaBody("type: bug", "blocked_by:", "  - 9"))
	blocker := graphIssue("Product-Seela", 9, metaBody("type: task"))
	blocker.State = "CLOSED"
	g := graphFor(t, graphSnapshot(blocked, blocker))

	n := g.Neighbourhood(blocked)
	if n.BlockedBy[0].State != BlockerResolved {
		t.Errorf("state = %q, want resolved for a closed blocker", n.BlockedBy[0].State)
	}
	if n.Unresolved != 0 {
		t.Errorf("unresolved = %d, want 0; a closed blocker is resolved, not unknown", n.Unresolved)
	}
}

// A source with no edges at all still answers with empty groups rather
// than nulls, so a renderer never has to special-case the ordinary case.
func TestGraph_AnIssueWithNoEdgesHasEmptyGroupsNotNulls(t *testing.T) {
	lonely := graphIssue("Product-Seela", 1, metaBody("type: task"))
	n := graphFor(t, graphSnapshot(lonely)).Neighbourhood(lonely)

	if n.BlockedBy == nil || n.Blocks == nil || n.Children == nil {
		t.Fatalf("a group is nil: %+v", n)
	}
	if len(n.BlockedBy)+len(n.Blocks)+len(n.Children) != 0 {
		t.Errorf("edges appeared from nowhere: %+v", n)
	}
	if n.Parent != nil || n.DiscoveredFrom != nil {
		t.Error("parent or provenance invented for an issue that declares neither")
	}
}

// An issue with no atab-meta block at all is untracked, not broken. Its
// native parent and child links still render, because those are
// GitHub's and do not depend on the block.
func TestGraph_NativeLinksSurviveAMissingMetaBlock(t *testing.T) {
	child := graphIssue("Product-Seela", 3, "no managed block here")
	child.Parent = &ParentRef{
		Ref:   IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 1},
		Title: "the epic", State: "OPEN",
	}
	g := graphFor(t, graphSnapshot(child))

	n := g.Neighbourhood(child)
	if n.Parent == nil {
		t.Fatal("the native parent link was lost with the meta block")
	}
	if n.Parent.Title != "the epic" {
		t.Errorf("parent title = %q, want the title GitHub supplied", n.Parent.Title)
	}
}
