package atab

import (
	"testing"
	"time"

	"github.com/GembaCore/gemba-core/core"
)

// demoSnapshot builds a fresh snapshot over the bundled fixture set,
// pinned to a fixed clock so lease freshness and staleness are
// reproducible.
func demoSnapshot(t *testing.T) (*Projector, Snapshot) {
	t.Helper()
	cfg := DemoSourceConfig()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize demo config: %v", err)
	}
	p := NewProjector(cfg)
	p.now = func() time.Time { return testNow }

	issues := make(map[IssueRef]Issue)
	for _, is := range DemoIssues() {
		issues[is.Ref] = is
	}
	return p, Snapshot{
		Source:     cfg.ID,
		ObservedAt: testNow.Add(-time.Minute),
		Freshness:  FreshnessFresh,
		Issues:     issues,
	}
}

func projectByNumber(t *testing.T, items []core.WorkItem, repo string, number int) core.WorkItem {
	t.Helper()
	want := IssueRef{Owner: "Atab-Group", Repo: repo, Number: number}.WorkItemID(DemoSourceID)
	for _, it := range items {
		if it.ID == want {
			return it
		}
	}
	t.Fatalf("no projected item %q", want)
	return core.WorkItem{}
}

func readiness(t *testing.T, it core.WorkItem) Readiness {
	t.Helper()
	v, ok := it.Custom[FieldKeyReadiness].(string)
	if !ok {
		t.Fatalf("item %q carries no readiness field", it.ID)
	}
	return Readiness(v)
}

// Stage 0's acceptance: one representative issue renders onto a complete
// core.WorkItem with the qualified id, the board status, the criteria
// checklist and the atab-meta fields all present.
func TestProjection_RepresentativeIssueRender(t *testing.T) {
	p, snap := demoSnapshot(t)
	items := p.ProjectAll(snap)
	it := projectByNumber(t, items, "Product-Seela", 301)

	if it.ID != "atab-group/Atab-Group~Product-Seela/301" {
		t.Errorf("id = %q", it.ID)
	}
	if it.Title != "Show a price range on the Services card" {
		t.Errorf("title = %q", it.Title)
	}
	if it.Status != StatusTodo || it.StateCategory != core.StateUnstarted {
		t.Errorf("status/category = %q/%q, want Todo/unstarted", it.Status, it.StateCategory)
	}
	if it.Priority == nil || *it.Priority != 1 {
		t.Errorf("priority = %v, want 1 (P1)", it.Priority)
	}
	if it.Kind != "feature" {
		t.Errorf("kind = %q, want feature", it.Kind)
	}
	if it.PrimaryRepositoryID != "Atab-Group~Product-Seela" {
		t.Errorf("repository = %q", it.PrimaryRepositoryID)
	}
	if len(it.RepositoryIDs) != 1 {
		t.Errorf("repository_ids = %v, want the primary promoted into it", it.RepositoryIDs)
	}
	if it.DoD == nil || len(it.DoD.AcceptanceCriteria) != 3 {
		t.Fatalf("dod = %+v, want three criteria", it.DoD)
	}
	if got := it.Custom[FieldKeyAutonomy]; got != string(AutonomyPR) {
		t.Errorf("autonomy = %v, want pr", got)
	}
	if got := it.Custom[FieldKeyArea]; got != "Product" {
		t.Errorf("area = %v, want Product", got)
	}
	if got := it.Custom[FieldKeyFreshness]; got != string(FreshnessFresh) {
		t.Errorf("freshness = %v, want fresh", got)
	}
	if r := readiness(t, it); r != ReadyReady {
		t.Errorf("readiness = %q, want ready", r)
	}
	crit, ok := it.Custom[FieldKeyCriteria].(map[string]any)
	if !ok || crit["total"] != 3 || crit["done"] != 1 {
		t.Errorf("criteria summary = %v, want 1 of 3 done", it.Custom[FieldKeyCriteria])
	}
}

// blocked_by is recorded on the blocked issue, but the core "blocks"
// edge runs from blocker to blocked. Getting that inversion wrong would
// reverse every dependency arrow on the board.
func TestProjection_BlockedByEdgeIsInverted(t *testing.T) {
	p, snap := demoSnapshot(t)
	it := projectByNumber(t, p.ProjectAll(snap), "Product-Seela", 302)

	self := core.WorkItemID("atab-group/Atab-Group~Product-Seela/302")
	blocker := core.WorkItemID("atab-group/Atab-Group~Product-Seela/301")
	var found bool
	for _, rel := range it.Relationships {
		if rel.Kind == core.RelBlocks {
			if rel.From != blocker || rel.To != self {
				t.Fatalf("blocks edge = %v -> %v, want %v -> %v", rel.From, rel.To, blocker, self)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no blocks edge projected")
	}
	if r := readiness(t, it); r != ReadyBlocked {
		t.Errorf("readiness = %q, want blocked", r)
	}
}

func TestProjection_DiscoveredFromIsProvenanceNotBlocking(t *testing.T) {
	p, snap := demoSnapshot(t)
	it := projectByNumber(t, p.ProjectAll(snap), "Product-Seela", 302)

	var relates int
	for _, rel := range it.Relationships {
		if rel.Kind == core.RelRelatesTo {
			relates++
		}
	}
	if relates != 1 {
		t.Errorf("relates_to edges = %d, want 1 for discovered_from", relates)
	}
	if got := it.Custom["atab_discovered_from"]; got != "Atab-Group/Product-Seela#301" {
		t.Errorf("discovered_from = %v", got)
	}
}

func TestProjection_NativeParentAndSubIssueEdges(t *testing.T) {
	p, snap := demoSnapshot(t)
	items := p.ProjectAll(snap)

	child := projectByNumber(t, items, "Product-Seela", 303)
	var parentEdge bool
	for _, rel := range child.Relationships {
		if rel.Kind == core.RelParentChild &&
			rel.From == "atab-group/Atab-Group~Product-Seela/310" &&
			rel.To == child.ID {
			parentEdge = true
		}
	}
	if !parentEdge {
		t.Error("child does not carry its native parent edge")
	}

	epic := projectByNumber(t, items, "Product-Seela", 310)
	var children int
	for _, rel := range epic.Relationships {
		if rel.Kind == core.RelParentChild && rel.From == epic.ID {
			children++
		}
	}
	if children != 2 {
		t.Errorf("epic sub-issue edges = %d, want 2", children)
	}
	if got := epic.Custom[FieldKeySubIssues]; got != "1/2" {
		t.Errorf("sub-issue progress = %v, want 1/2", got)
	}
	if epic.Kind != core.KindMilestone {
		t.Errorf("epic kind = %q, want the core milestone kind", epic.Kind)
	}
}

// The assignee says who owns the issue; the lease says whether a worker
// is running it now. Conflating them would show a crashed worker as the
// owner, so the projection keeps them in separate fields.
func TestProjection_AssigneeIsDistinctFromLease(t *testing.T) {
	p, snap := demoSnapshot(t)
	items := p.ProjectAll(snap)

	live := projectByNumber(t, items, "Product-Seela", 304)
	if live.Assignee == nil || live.Assignee.Name != "niclom12" {
		t.Fatalf("assignee = %+v, want niclom12", live.Assignee)
	}
	lease, ok := live.Custom[FieldKeyLease].(map[string]any)
	if !ok {
		t.Fatal("no lease field on an issue holding a live lease")
	}
	if lease["holder"] != "claude-1" {
		t.Errorf("lease holder = %v, want claude-1 (distinct from the assignee)", lease["holder"])
	}
	if lease["fresh"] != true {
		t.Errorf("lease fresh = %v, want true", lease["fresh"])
	}
	if r := readiness(t, live); r != ReadyInFlight {
		t.Errorf("readiness = %q, want in_flight", r)
	}

	dead := projectByNumber(t, items, "Product-Seela", 305)
	deadLease, ok := dead.Custom[FieldKeyLease].(map[string]any)
	if !ok {
		t.Fatal("an expired lease must stay visible: it is the evidence a worker died holding the issue")
	}
	if deadLease["fresh"] != false {
		t.Errorf("expired lease fresh = %v, want false", deadLease["fresh"])
	}
	if dead.Assignee == nil {
		t.Error("the assignee survives the lease expiring")
	}
	if r := readiness(t, dead); r != ReadyReady {
		t.Errorf("readiness = %q, want ready; an expired lease releases the issue", r)
	}
}

func TestProjection_EvidenceFromPRLocalCIVerifierAndChecks(t *testing.T) {
	p, snap := demoSnapshot(t)
	it := projectByNumber(t, p.ProjectAll(snap), "Product-Seela", 306)

	bySource := map[string]core.Evidence{}
	for _, ev := range it.Evidence {
		bySource[ev.Source] = ev
	}
	for _, want := range []string{
		EvidenceSourcePR, EvidenceSourceLocalCI, EvidenceSourceVerifier, EvidenceSourceCheck,
	} {
		if _, ok := bySource[want]; !ok {
			t.Errorf("no evidence from %q; got %v", want, keysOf(bySource))
		}
	}
	if ci := bySource[EvidenceSourceLocalCI]; ci.Payload["all_green"] != true {
		t.Errorf("local CI payload = %v, want all_green", ci.Payload)
	}
	if ci := bySource[EvidenceSourceLocalCI]; ci.Payload["stale"] != false {
		t.Errorf("local CI stale = %v, want false; the report matches the head", ci.Payload["stale"])
	}
	if v := bySource[EvidenceSourceVerifier]; v.Payload["overall_verdict"] != "PASS" {
		t.Errorf("verifier verdict = %v, want PASS", v.Payload["overall_verdict"])
	}
	if r := readiness(t, it); r != ReadyPROpen {
		t.Errorf("readiness = %q, want pr_open", r)
	}
}

// A local-CI report stamped against a commit that is no longer the head
// ran, but not against what would merge. It must not present as current.
func TestCollectEvidence_StaleLocalCIReportIsFlagged(t *testing.T) {
	issue := Issue{
		Ref: IssueRef{Owner: "Atab-Group", Repo: "R", Number: 1},
		LinkedPRs: []PullRequest{{
			Ref:     IssueRef{Owner: "Atab-Group", Repo: "R", Number: 2},
			State:   "OPEN",
			HeadSHA: "bbbbbbbbbb",
			Comments: []Comment{{
				Body: "<!-- atab-local-ci -->\n```json\n{\"head_sha\":\"aaaaaaaaaa\"," +
					"\"all_green\":true,\"commands\":[]}\n```",
				UpdatedAt: testNow,
			}},
		}},
	}
	for _, ev := range CollectEvidence("s", issue) {
		if ev.Source != EvidenceSourceLocalCI {
			continue
		}
		if ev.Payload["stale"] != true {
			t.Fatalf("stale = %v, want true", ev.Payload["stale"])
		}
		return
	}
	t.Fatal("no local-CI evidence produced")
}

// Several reports may sit on one pull request, one per push. The
// contract is always the newest.
func TestCollectEvidence_TakesTheNewestReport(t *testing.T) {
	issue := Issue{
		Ref: IssueRef{Owner: "o", Repo: "r", Number: 1},
		LinkedPRs: []PullRequest{{
			Ref:     IssueRef{Owner: "o", Repo: "r", Number: 2},
			State:   "OPEN",
			HeadSHA: "sha-new",
			Comments: []Comment{
				{
					Body:      "<!-- atab-local-ci -->\n```json\n{\"head_sha\":\"sha-old\",\"all_green\":false}\n```",
					UpdatedAt: testNow.Add(-2 * time.Hour),
				},
				{
					Body:      "<!-- atab-local-ci -->\n```json\n{\"head_sha\":\"sha-new\",\"all_green\":true}\n```",
					UpdatedAt: testNow.Add(-time.Hour),
				},
			},
		}},
	}
	for _, ev := range CollectEvidence("s", issue) {
		if ev.Source == EvidenceSourceLocalCI {
			if ev.Payload["all_green"] != true || ev.Ref != "sha-new" {
				t.Fatalf("evidence = %+v, want the newest report", ev)
			}
			return
		}
	}
	t.Fatal("no local-CI evidence produced")
}

func TestProjection_ClosedShapesLandInDifferentBuckets(t *testing.T) {
	p, snap := demoSnapshot(t)
	items := p.ProjectAll(snap)

	done := projectByNumber(t, items, "ATAB-Marketplace", 402)
	if done.StateCategory != core.StateCompleted {
		t.Errorf("completed issue category = %q, want completed", done.StateCategory)
	}
	cancelled := projectByNumber(t, items, "ATAB-Marketplace", 403)
	if cancelled.Status != StatusClosedNotPlanned || cancelled.StateCategory != core.StateCanceled {
		t.Errorf("not-planned issue = %q/%q, want not_planned/canceled",
			cancelled.Status, cancelled.StateCategory)
	}
}

// A board Status the mapping does not know becomes an explicit unknown
// rather than a guess. A card in the wrong lane is worse than a card
// that says it does not know.
func TestProjection_UnknownBoardStatusIsExplicit(t *testing.T) {
	p, snap := demoSnapshot(t)
	it := projectByNumber(t, p.ProjectAll(snap), "Product-Seela", 308)
	if it.Status != StatusUnknown {
		t.Fatalf("status = %q, want unknown", it.Status)
	}
	if it.Custom[FieldKeyBoardStatus] != "Parking Lot" {
		t.Errorf("raw board status = %v, want it preserved", it.Custom[FieldKeyBoardStatus])
	}
	if _, ok := StateMap[it.Status]; !ok {
		t.Error("every emitted status must appear in the declared StateMap")
	}
}

// The StateMap has to cover every token the projection can emit, or the
// SPA falls through to its unknown-state fallback.
func TestProjection_EveryEmittedStatusIsMapped(t *testing.T) {
	p, snap := demoSnapshot(t)
	for _, it := range p.ProjectAll(snap) {
		if _, ok := StateMap[it.Status]; !ok {
			t.Errorf("item %q emitted unmapped status %q", it.ID, it.Status)
		}
	}
}

func TestProjection_StaleSnapshotMarksEveryItem(t *testing.T) {
	p, snap := demoSnapshot(t)
	snap.Freshness = FreshnessStale
	for _, it := range p.ProjectAll(snap) {
		if it.Custom[FieldKeyFreshness] != string(FreshnessStale) {
			t.Fatalf("item %q freshness = %v, want stale", it.ID, it.Custom[FieldKeyFreshness])
		}
		if readiness(t, it) != ReadyUnknown {
			t.Fatalf("item %q readiness = %v on a stale snapshot, want unknown",
				it.ID, it.Custom[FieldKeyReadiness])
		}
	}
}

func TestProjection_ReadinessAcrossTheFixtureSet(t *testing.T) {
	p, snap := demoSnapshot(t)
	items := p.ProjectAll(snap)

	cases := []struct {
		repo   string
		number int
		want   Readiness
	}{
		{"Product-Seela", 301, ReadyReady},
		{"Product-Seela", 302, ReadyBlocked},
		{"Product-Seela", 303, ReadyBlocked}, // cross-repo target absent from this snapshot
		{"Product-Seela", 304, ReadyInFlight},
		{"Product-Seela", 305, ReadyReady},
		{"Product-Seela", 306, ReadyPROpen},
		{"Product-Seela", 308, ReadyReady},
		{"Product-Seela", 309, ReadyParked},
		{"Product-Seela", 310, ReadyNotWorkable},
		{"Product-Seela", 312, ReadyReconcile},
		{"Product-Seela", 315, ReadyOrphan},
		{"Product-Seela", 317, ReadyUntracked},
		{"Product-Seela", 318, ReadyMalformed},
		{"Product-Seela", 320, ReadyCycle},
		{"Product-Seela", 321, ReadyCycle},
		{"ATAB-Marketplace", 401, ReadyHumanOnly},
		{"ATAB-Marketplace", 402, ReadyDone},
		{"ATAB-Marketplace", 403, ReadyDone},
	}
	for _, tc := range cases {
		it := projectByNumber(t, items, tc.repo, tc.number)
		if got := readiness(t, it); got != tc.want {
			t.Errorf("%s#%d readiness = %q, want %q (%v)",
				tc.repo, tc.number, got, tc.want, it.Custom[FieldKeyReadyReason])
		}
	}
}

func TestProjection_RequiresEnvAndWarningsSurface(t *testing.T) {
	p, snap := demoSnapshot(t)
	it := projectByNumber(t, p.ProjectAll(snap), "ATAB-Marketplace", 401)
	got, _ := it.Custom[FieldKeyRequiresEnv].(string)
	if got != "STRIPE_WEBHOOK_SECRET@deploy, STRIPE_API_KEY@both" {
		t.Errorf("requires_env = %q", got)
	}
}

func TestProjection_SortedByUpdatedAtDescending(t *testing.T) {
	p, snap := demoSnapshot(t)
	items := p.ProjectAll(snap)
	for i := 1; i < len(items); i++ {
		if items[i-1].UpdatedAt.Before(items[i].UpdatedAt) {
			t.Fatalf("items are not newest-first at index %d", i)
		}
	}
}

func keysOf(m map[string]core.Evidence) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
