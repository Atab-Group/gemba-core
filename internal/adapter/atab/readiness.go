package atab

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Readiness is the projected answer to "could a worker pick this up right
// now, and if not, why not". The values and the order they are decided in
// are a faithful port of the org's ready_select rules, so the dashboard
// and the dispatcher agree about the queue instead of each computing its
// own idea of ready.
type Readiness string

const (
	// ReadyReady: every gate passed. The item is pickable.
	ReadyReady Readiness = "ready"
	// ReadyDone: closed, or sitting in the board's Done column.
	ReadyDone Readiness = "done"
	// ReadyNotWorkable: the type is not workable: an epic that still
	// has open children, or a type the board does not mark workable.
	ReadyNotWorkable Readiness = "not_workable"
	// ReadyParked: a needs-human-class label parks the item on the
	// human side until a person clears it.
	ReadyParked Readiness = "parked"
	// ReadyUntracked: no atab-meta block, so automation does not track
	// it. Shown, never dispatched.
	ReadyUntracked Readiness = "untracked"
	// ReadyMalformed: the atab-meta block does not parse.
	ReadyMalformed Readiness = "malformed"
	// ReadyInFlight: already being worked: the board says In Progress
	// or In Review, or a worker holds a fresh lease.
	ReadyInFlight Readiness = "in_flight"
	// ReadyPROpen: already built; an open pull request links the issue
	// and it is waiting to merge.
	ReadyPROpen Readiness = "pr_open"
	// ReadyCycle: the item sits in a blocked_by cycle. Reported apart
	// from plain blocked because a cycle needs a human to break it.
	ReadyCycle Readiness = "cycle"
	// ReadyBlocked: at least one blocked_by edge is unresolved.
	ReadyBlocked Readiness = "blocked"
	// ReadyOrphan: the native parent issue is closed, so the item's
	// context is gone.
	ReadyOrphan Readiness = "orphan"
	// ReadyHumanOnly: autonomy is human, so no bot may claim it. It is
	// still ready work for a person, and is reported as its own state
	// rather than folded into blocked.
	ReadyHumanOnly Readiness = "human_only"
	// ReadyReconcile: a drained epic: open, type epic, unassigned, and
	// every native sub-issue closed. Its work is reconciliation.
	ReadyReconcile Readiness = "reconcile"
	// ReadyUnknown: the projection could not decide, because the
	// snapshot behind it is stale or the source is unhealthy. Never a
	// silent "ready".
	ReadyUnknown Readiness = "unknown"
)

// ParkedLabels park an item on the human side. Clearing the label puts
// the item straight back in the queue with no other action.
var ParkedLabels = map[string]struct{}{
	"needs-human":  {},
	"awaiting-env": {},
	"escalated":    {},
}

// WorkableTypes are the atab issue types a worker may pick up. An epic
// is tracker-only unless it has drained.
var WorkableTypes = map[string]struct{}{
	"bug":      {},
	"feature":  {},
	"task":     {},
	"chore":    {},
	"research": {},
}

// EpicType is the one non-workable type with special handling.
const EpicType = "epic"

// TypeLabelPrefix is how the org spells an issue type on a label.
const TypeLabelPrefix = "type:"

// TypeOf resolves an issue's type, preferring the type label over the
// atab-meta declaration.
//
// The label is the org's own authority for workability and it survives a
// block that does not parse, which is what lets a malformed issue still
// be reported as malformed instead of as an unknown type.
func TypeOf(labels []string, meta Meta) string {
	for _, l := range labels {
		if strings.HasPrefix(l, TypeLabelPrefix) {
			if t := strings.TrimPrefix(l, TypeLabelPrefix); t != "" {
				return t
			}
		}
	}
	return meta.Type
}

// BlockerState is what a blocker resolver reports about one edge target.
type BlockerState string

const (
	// BlockerOpen: the target exists and is still open: the edge holds.
	BlockerOpen BlockerState = "open"
	// BlockerResolved: the target is closed, is in the board's Done
	// column, or does not exist. A typo must not deadlock the queue, so
	// an absent target resolves.
	BlockerResolved BlockerState = "resolved"
	// BlockerUnknown: the target lives in a source this deployment
	// cannot read. The edge is treated as holding and the item is
	// reported blocked with an explicit reason, never quietly ready.
	// This is the fail-closed direction Stage 2 depends on.
	BlockerUnknown BlockerState = "unknown"
)

// ReadinessInput is everything the rules read. Assembling it explicitly
// keeps the decision a pure function of observable facts, which is what
// makes it testable against fixtures without a network.
type ReadinessInput struct {
	Self IssueRef

	// Closed is GitHub's own state, which outranks the board.
	Closed      bool
	StateReason string

	Labels      []string
	Assignees   []string
	BoardStatus string
	Meta        Meta

	// Lease is the newest lease comment, when one exists.
	Lease    Lease
	HasLease bool

	// Parent is the native parent link, when one exists.
	Parent *ParentRef
	// SubIssues are the native children.
	SubIssues []SubIssueRef

	// OpenLinkedPRs counts pull requests that link the issue and are
	// still open.
	OpenLinkedPRs int

	// InCycle marks membership of a blocked_by cycle.
	InCycle bool

	// Fresh reports whether the snapshot behind this item is inside its
	// source's freshness budget.
	Fresh bool

	// Now is the instant the decision is made against, so lease freshness
	// is reproducible in tests.
	Now time.Time

	// ResolveBlocker answers the state of one blocked_by target. Nil is
	// treated as "every blocker is unknown", which fails closed.
	ResolveBlocker func(IssueRef) BlockerState
}

// ReadinessResult carries the state plus a caller-safe reason. The reason
// is what the card renders, so it names the specific gate rather than
// restating the state.
type ReadinessResult struct {
	State  Readiness `json:"state"`
	Reason string    `json:"reason,omitempty"`
	// BlockedBy lists the unresolved blockers when State is blocked.
	BlockedBy []IssueRef `json:"blocked_by,omitempty"`
}

// EvaluateReadiness applies the gates in the order the org's selector
// applies them. Order matters: a cycle outranks plain blocked, a park
// outranks the meta check, and an unknown snapshot outranks everything
// because a decision made on data the adaptor cannot vouch for is worse
// than an honest "unknown".
func EvaluateReadiness(in ReadinessInput) ReadinessResult {
	if !in.Fresh {
		return ReadinessResult{
			State: ReadyUnknown,
			Reason: "the snapshot behind this item is outside its source's " +
				"freshness budget; readiness is not being asserted",
		}
	}

	if in.Closed {
		reason := "issue is closed"
		if in.StateReason != "" {
			reason = fmt.Sprintf("issue is closed (%s)", strings.ToLower(in.StateReason))
		}
		return ReadinessResult{State: ReadyDone, Reason: reason}
	}
	if in.BoardStatus == StatusDone {
		return ReadinessResult{State: ReadyDone, Reason: "board status is Done"}
	}

	labels := labelSet(in.Labels)
	if parked := parkedLabel(labels); parked != "" {
		return ReadinessResult{
			State:  ReadyParked,
			Reason: fmt.Sprintf("parked by the %q label until a human clears it", parked),
		}
	}

	// Workability is decided from the type LABEL first, exactly as the
	// org's selector does. Reading it from the atab-meta block instead
	// would make every malformed block look like an unknown type, and a
	// malformed block is a state a human has to see as itself.
	itemType := TypeOf(in.Labels, in.Meta)
	drained := isDrainedEpic(itemType, in.Assignees, in.SubIssues)
	if _, workable := WorkableTypes[itemType]; !workable && !drained {
		reason := fmt.Sprintf("type %q is not workable", itemType)
		switch itemType {
		case "":
			reason = "no issue type declared"
		case EpicType:
			reason = "epic with open sub-issues; work its children instead"
		}
		if in.Meta.State == MetaAbsent {
			return ReadinessResult{
				State:  ReadyUntracked,
				Reason: "no atab-meta block; not tracked by automation",
			}
		}
		return ReadinessResult{State: ReadyNotWorkable, Reason: reason}
	}

	switch in.Meta.State {
	case MetaAbsent:
		return ReadinessResult{
			State:  ReadyUntracked,
			Reason: "no atab-meta block; not tracked by automation",
		}
	case MetaMalformed:
		return ReadinessResult{
			State:  ReadyMalformed,
			Reason: "the atab-meta block does not parse; skipped rather than guessed",
		}
	}

	if in.BoardStatus == StatusInProgress || in.BoardStatus == StatusInReview {
		return ReadinessResult{
			State:  ReadyInFlight,
			Reason: fmt.Sprintf("board status is %s", in.BoardStatus),
		}
	}
	if in.HasLease && in.Lease.Fresh(in.Now) {
		return ReadinessResult{
			State: ReadyInFlight,
			Reason: fmt.Sprintf("held by a live lease (%s, expires %s)",
				in.Lease.Holder, in.Lease.Expires.UTC().Format(time.RFC3339)),
		}
	}

	if in.OpenLinkedPRs > 0 {
		return ReadinessResult{
			State: ReadyPROpen,
			Reason: fmt.Sprintf("%d open pull request(s) already link this issue",
				in.OpenLinkedPRs),
		}
	}

	if in.InCycle {
		return ReadinessResult{
			State:  ReadyCycle,
			Reason: "sits in a blocked_by cycle; a human has to break it",
		}
	}

	if in.Parent != nil && strings.EqualFold(in.Parent.State, "CLOSED") {
		return ReadinessResult{
			State:  ReadyOrphan,
			Reason: fmt.Sprintf("parent %s is closed", in.Parent.Ref),
		}
	}

	if unresolved, reason := unresolvedBlockers(in); len(unresolved) > 0 {
		return ReadinessResult{State: ReadyBlocked, Reason: reason, BlockedBy: unresolved}
	}

	if in.Meta.Autonomy == AutonomyHuman {
		return ReadinessResult{
			State:  ReadyHumanOnly,
			Reason: "autonomy is human; ready for a person, never for a bot",
		}
	}

	if drained {
		return ReadinessResult{
			State:  ReadyReconcile,
			Reason: "drained epic: every sub-issue is closed; the work is reconciliation",
		}
	}

	return ReadinessResult{State: ReadyReady}
}

func unresolvedBlockers(in ReadinessInput) ([]IssueRef, string) {
	var unresolved []IssueRef
	var unknown []IssueRef
	for _, edge := range in.Meta.BlockedBy {
		target := edge.Resolve(in.Self)
		state := BlockerUnknown
		if in.ResolveBlocker != nil {
			state = in.ResolveBlocker(target)
		}
		switch state {
		case BlockerResolved:
			continue
		case BlockerUnknown:
			unknown = append(unknown, target)
			unresolved = append(unresolved, target)
		default:
			unresolved = append(unresolved, target)
		}
	}
	if len(unresolved) == 0 {
		return nil, ""
	}
	if len(unknown) > 0 {
		return unresolved, fmt.Sprintf(
			"blocked by %s; %d of them are in a source this deployment cannot read, "+
				"so the edge is held rather than assumed clear",
			renderRefs(unresolved), len(unknown))
	}
	return unresolved, fmt.Sprintf("blocked by %s", renderRefs(unresolved))
}

// isDrainedEpic reports the reconcile case: an open, unassigned epic
// whose every native sub-issue is closed. An epic with no sub-issues at
// all has not drained; it was never filled.
func isDrainedEpic(itemType string, assignees []string, subs []SubIssueRef) bool {
	if itemType != EpicType || len(assignees) > 0 || len(subs) == 0 {
		return false
	}
	for _, s := range subs {
		if !strings.EqualFold(s.State, "CLOSED") {
			return false
		}
	}
	return true
}

func labelSet(labels []string) map[string]struct{} {
	out := make(map[string]struct{}, len(labels))
	for _, l := range labels {
		out[l] = struct{}{}
	}
	return out
}

func parkedLabel(labels map[string]struct{}) string {
	var hits []string
	for l := range labels {
		if _, ok := ParkedLabels[l]; ok {
			hits = append(hits, l)
		}
	}
	if len(hits) == 0 {
		return ""
	}
	sort.Strings(hits)
	return hits[0]
}

func renderRefs(refs []IssueRef) string {
	parts := make([]string, 0, len(refs))
	for _, r := range refs {
		parts = append(parts, r.String())
	}
	return strings.Join(parts, ", ")
}

// DetectCycles returns the set of issues that participate in a
// blocked_by cycle. Nodes are opaque refs, so a cycle that runs through
// a cross-repo edge is found the same way a same-repo one is.
//
// The walk is an iterative colouring DFS: white unvisited, grey on the
// current path, black finished. Reaching a grey node closes a cycle, and
// every node currently on the path is part of it.
func DetectCycles(edges map[IssueRef][]IssueRef) map[IssueRef]bool {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := make(map[IssueRef]int, len(edges))
	inCycle := make(map[IssueRef]bool)

	nodes := make([]IssueRef, 0, len(edges))
	for n := range edges {
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].String() < nodes[j].String() })

	for _, start := range nodes {
		if color[start] != white {
			continue
		}
		var path []IssueRef
		var walk func(IssueRef)
		walk = func(n IssueRef) {
			color[n] = grey
			path = append(path, n)
			for _, next := range edges[n] {
				switch color[next] {
				case white:
					if _, known := edges[next]; known {
						walk(next)
					} else {
						color[next] = black
					}
				case grey:
					// Everything from next to the top of the path is on
					// the cycle.
					for i := len(path) - 1; i >= 0; i-- {
						inCycle[path[i]] = true
						if path[i] == next {
							break
						}
					}
				}
			}
			path = path[:len(path)-1]
			color[n] = black
		}
		walk(start)
	}
	return inCycle
}
