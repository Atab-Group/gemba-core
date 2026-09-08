package atab

import (
	"testing"
	"time"
)

var testNow = time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)

func baseInput() ReadinessInput {
	return ReadinessInput{
		Self:        IssueRef{Owner: "Atab-Group", Repo: "R", Number: 1},
		BoardStatus: StatusTodo,
		Meta:        Meta{State: MetaPresent, Type: "task", Autonomy: AutonomyPR},
		Fresh:       true,
		Now:         testNow,
		ResolveBlocker: func(IssueRef) BlockerState {
			return BlockerResolved
		},
	}
}

func TestEvaluateReadiness_Ready(t *testing.T) {
	if got := EvaluateReadiness(baseInput()); got.State != ReadyReady {
		t.Fatalf("state = %q (%s), want ready", got.State, got.Reason)
	}
}

// A snapshot outside its freshness budget outranks every other gate. A
// readiness verdict computed on data the adaptor cannot vouch for would
// be worse than admitting it does not know.
func TestEvaluateReadiness_StaleSnapshotIsUnknown(t *testing.T) {
	in := baseInput()
	in.Fresh = false
	got := EvaluateReadiness(in)
	if got.State != ReadyUnknown {
		t.Fatalf("state = %q, want unknown", got.State)
	}
	if got.Reason == "" {
		t.Error("an unknown verdict must say why")
	}
}

func TestEvaluateReadiness_ClosedAndDone(t *testing.T) {
	in := baseInput()
	in.Closed = true
	in.StateReason = "COMPLETED"
	if got := EvaluateReadiness(in); got.State != ReadyDone {
		t.Errorf("closed issue state = %q, want done", got.State)
	}

	in = baseInput()
	in.BoardStatus = StatusDone
	if got := EvaluateReadiness(in); got.State != ReadyDone {
		t.Errorf("board Done state = %q, want done", got.State)
	}
}

func TestEvaluateReadiness_ParkedLabels(t *testing.T) {
	for label := range ParkedLabels {
		in := baseInput()
		in.Labels = []string{"type:task", label}
		got := EvaluateReadiness(in)
		if got.State != ReadyParked {
			t.Errorf("label %q state = %q, want parked", label, got.State)
		}
	}
}

func TestEvaluateReadiness_UntrackedAndMalformed(t *testing.T) {
	in := baseInput()
	in.Meta = Meta{State: MetaAbsent}
	if got := EvaluateReadiness(in); got.State != ReadyUntracked {
		t.Errorf("absent meta state = %q, want untracked", got.State)
	}

	in = baseInput()
	in.Meta = Meta{State: MetaMalformed, Type: "task"}
	if got := EvaluateReadiness(in); got.State != ReadyMalformed {
		t.Errorf("malformed meta state = %q, want malformed", got.State)
	}
}

func TestEvaluateReadiness_InFlightByBoardOrLease(t *testing.T) {
	for _, status := range []string{StatusInProgress, StatusInReview} {
		in := baseInput()
		in.BoardStatus = status
		if got := EvaluateReadiness(in); got.State != ReadyInFlight {
			t.Errorf("status %q state = %q, want in_flight", status, got.State)
		}
	}

	in := baseInput()
	in.HasLease = true
	in.Lease = Lease{Holder: "claude-1", Expires: testNow.Add(time.Hour)}
	got := EvaluateReadiness(in)
	if got.State != ReadyInFlight {
		t.Fatalf("live lease state = %q, want in_flight", got.State)
	}
}

// An expired lease is a crashed worker, not a live claim. The issue goes
// back in the queue; recovery depends on that.
func TestEvaluateReadiness_ExpiredLeaseIsPickableAgain(t *testing.T) {
	in := baseInput()
	in.Assignees = []string{"niclom12"}
	in.HasLease = true
	in.Lease = Lease{Holder: "claude-3", Expires: testNow.Add(-time.Hour)}
	if got := EvaluateReadiness(in); got.State != ReadyReady {
		t.Fatalf("expired lease state = %q (%s), want ready", got.State, got.Reason)
	}
}

func TestEvaluateReadiness_OpenPRHoldsIt(t *testing.T) {
	in := baseInput()
	in.OpenLinkedPRs = 1
	if got := EvaluateReadiness(in); got.State != ReadyPROpen {
		t.Fatalf("state = %q, want pr_open", got.State)
	}
}

// A cycle is reported apart from plain blocked, and outranks it: a
// cycle needs a person to break it, where a blocker just needs waiting.
func TestEvaluateReadiness_CycleOutranksBlocked(t *testing.T) {
	in := baseInput()
	in.InCycle = true
	in.Meta.BlockedBy = []Edge{{Number: 2}}
	in.ResolveBlocker = func(IssueRef) BlockerState { return BlockerOpen }
	if got := EvaluateReadiness(in); got.State != ReadyCycle {
		t.Fatalf("state = %q, want cycle", got.State)
	}
}

func TestEvaluateReadiness_OrphanWhenParentClosed(t *testing.T) {
	in := baseInput()
	in.Parent = &ParentRef{Ref: IssueRef{Owner: "Atab-Group", Repo: "R", Number: 9}, State: "CLOSED"}
	if got := EvaluateReadiness(in); got.State != ReadyOrphan {
		t.Fatalf("state = %q, want orphan", got.State)
	}
}

func TestEvaluateReadiness_BlockedByOpenTarget(t *testing.T) {
	in := baseInput()
	in.Meta.BlockedBy = []Edge{{Number: 2}}
	in.ResolveBlocker = func(IssueRef) BlockerState { return BlockerOpen }
	got := EvaluateReadiness(in)
	if got.State != ReadyBlocked {
		t.Fatalf("state = %q, want blocked", got.State)
	}
	if len(got.BlockedBy) != 1 || got.BlockedBy[0].Number != 2 {
		t.Errorf("blocked_by = %v, want the open target", got.BlockedBy)
	}
}

// A blocker pointing at a typo resolves rather than deadlocking the
// queue, which is the schema's stated rule.
func TestEvaluateReadiness_AbsentBlockerResolves(t *testing.T) {
	in := baseInput()
	in.Meta.BlockedBy = []Edge{{Number: 999}}
	in.ResolveBlocker = func(IssueRef) BlockerState { return BlockerResolved }
	if got := EvaluateReadiness(in); got.State != ReadyReady {
		t.Fatalf("state = %q, want ready", got.State)
	}
}

// An edge into a source this deployment cannot read holds the work and
// says so. Assuming it clear would let an unreachable source silently
// unblock a queue, which is the failure Stage 2 has to rule out.
func TestEvaluateReadiness_UnknownBlockerFailsClosed(t *testing.T) {
	in := baseInput()
	in.Meta.BlockedBy = []Edge{{Owner: "Atab-Group", Repo: "Other", Number: 77}}
	in.ResolveBlocker = func(IssueRef) BlockerState { return BlockerUnknown }
	got := EvaluateReadiness(in)
	if got.State != ReadyBlocked {
		t.Fatalf("state = %q, want blocked", got.State)
	}
	if got.Reason == "" || !contains(got.Reason, "cannot read") {
		t.Errorf("reason = %q, want it to name the unreadable source", got.Reason)
	}
}

// A nil resolver means the adaptor knows nothing about any blocker. That
// must fail closed too, not pass by omission.
func TestEvaluateReadiness_NilResolverFailsClosed(t *testing.T) {
	in := baseInput()
	in.Meta.BlockedBy = []Edge{{Number: 2}}
	in.ResolveBlocker = nil
	if got := EvaluateReadiness(in); got.State != ReadyBlocked {
		t.Fatalf("state = %q, want blocked", got.State)
	}
}

func TestEvaluateReadiness_HumanAutonomyIsItsOwnState(t *testing.T) {
	in := baseInput()
	in.Meta.Autonomy = AutonomyHuman
	if got := EvaluateReadiness(in); got.State != ReadyHumanOnly {
		t.Fatalf("state = %q, want human_only", got.State)
	}
}

func TestEvaluateReadiness_EpicStates(t *testing.T) {
	open := baseInput()
	open.Meta.Type = EpicType
	open.SubIssues = []SubIssueRef{{State: "OPEN"}, {State: "CLOSED"}}
	if got := EvaluateReadiness(open); got.State != ReadyNotWorkable {
		t.Errorf("epic with an open child state = %q, want not_workable", got.State)
	}

	drained := baseInput()
	drained.Meta.Type = EpicType
	drained.SubIssues = []SubIssueRef{{State: "CLOSED"}, {State: "CLOSED"}}
	if got := EvaluateReadiness(drained); got.State != ReadyReconcile {
		t.Errorf("drained epic state = %q, want reconcile", got.State)
	}

	// An assigned epic is a person's, never a reconcile candidate.
	claimed := drained
	claimed.Assignees = []string{"niclom12"}
	if got := EvaluateReadiness(claimed); got.State != ReadyNotWorkable {
		t.Errorf("assigned drained epic state = %q, want not_workable", got.State)
	}

	// An epic with no children at all was never filled, so it has not
	// drained.
	empty := baseInput()
	empty.Meta.Type = EpicType
	if got := EvaluateReadiness(empty); got.State != ReadyNotWorkable {
		t.Errorf("childless epic state = %q, want not_workable", got.State)
	}
}

func TestDetectCycles(t *testing.T) {
	a := IssueRef{Owner: "o", Repo: "r", Number: 1}
	b := IssueRef{Owner: "o", Repo: "r", Number: 2}
	c := IssueRef{Owner: "o", Repo: "r", Number: 3}
	free := IssueRef{Owner: "o", Repo: "r", Number: 4}

	got := DetectCycles(map[IssueRef][]IssueRef{
		a:    {b},
		b:    {c},
		c:    {a},
		free: {a},
	})
	for _, ref := range []IssueRef{a, b, c} {
		if !got[ref] {
			t.Errorf("%v should be reported in the cycle", ref)
		}
	}
	if got[free] {
		t.Errorf("%v points into a cycle but is not in one", free)
	}
}

// A cycle running through a cross-repo edge is found the same way a
// same-repo one is: nodes are opaque refs to the detector.
func TestDetectCycles_CrossRepo(t *testing.T) {
	a := IssueRef{Owner: "o", Repo: "one", Number: 1}
	b := IssueRef{Owner: "o", Repo: "two", Number: 1}
	got := DetectCycles(map[IssueRef][]IssueRef{a: {b}, b: {a}})
	if !got[a] || !got[b] {
		t.Fatalf("cross-repo cycle not detected: %v", got)
	}
}

func TestDetectCycles_SelfEdge(t *testing.T) {
	a := IssueRef{Owner: "o", Repo: "r", Number: 1}
	if got := DetectCycles(map[IssueRef][]IssueRef{a: {a}}); !got[a] {
		t.Fatal("a self-blocking issue is a cycle of one")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
