// Package core: see doc.go for the overview.
//
// activity.go carries the optional work-item history surface.
//
// It is deliberately not part of [WorkPlane]. A work item's history is
// unbounded, it is expensive to fetch, and most backends cannot answer
// for it at all, so folding it into the required port would force every
// adaptor to carry a method it cannot implement. An adaptor that can
// answer implements [ActivityReader] instead, and the server offers the
// route only when the bound plane does.
package core

import (
	"context"
	"time"
)

// ActivityKind names one entry in a work item's history.
//
// The set is closed on purpose: a renderer decides an icon and a
// sentence from this token, and an open set would mean either a generic
// fallback for everything unfamiliar or a renderer that drifts from its
// adaptors. A backend event with no row here arrives as [ActivityOther]
// carrying its own Summary, which renders honestly without pretending to
// be a kind the UI understands.
type ActivityKind string

const (
	// ActivityComment is a human or agent comment. Body carries it.
	ActivityComment ActivityKind = "comment"
	// ActivityClosed and ActivityReopened are the state transitions.
	ActivityClosed   ActivityKind = "closed"
	ActivityReopened ActivityKind = "reopened"
	// ActivityLabeled and ActivityUnlabeled name the label in Detail.
	ActivityLabeled   ActivityKind = "labeled"
	ActivityUnlabeled ActivityKind = "unlabeled"
	// ActivityAssigned and ActivityUnassigned name the assignee in
	// Detail. They are about the assignee and never about a lease: a
	// claim is its own protocol and its own field.
	ActivityAssigned   ActivityKind = "assigned"
	ActivityUnassigned ActivityKind = "unassigned"
	// ActivityRenamed carries the old and new title in Detail.
	ActivityRenamed ActivityKind = "renamed"
	// ActivityReferenced is another item pointing at this one.
	ActivityReferenced ActivityKind = "referenced"
	// ActivityMilestoned and ActivityDemilestoned name the milestone.
	ActivityMilestoned   ActivityKind = "milestoned"
	ActivityDemilestoned ActivityKind = "demilestoned"
	// ActivityDuplicate marks the item as a duplicate of another.
	ActivityDuplicate ActivityKind = "duplicate"
	// ActivityOther is a backend event with no row above. Summary says
	// what it was; the renderer shows it plainly rather than guessing.
	ActivityOther ActivityKind = "other"
)

// ActivityEvent is one entry in a work item's history.
type ActivityEvent struct {
	// ID is stable for this event within its backend, so a renderer can
	// key on it across refreshes.
	ID string `json:"id"`
	// Kind decides how the event renders.
	Kind ActivityKind `json:"kind"`
	// Actor is who did it, as the backend names them. Empty when the
	// backend withheld the actor (a deleted account, an app the
	// credential cannot resolve).
	Actor string `json:"actor,omitempty"`
	// At is when it happened.
	At time.Time `json:"at"`
	// Summary is a one-line description for a non-comment event. It is
	// filled by the adaptor rather than the renderer so a backend can
	// describe an event the renderer has no row for.
	Summary string `json:"summary,omitempty"`
	// Body is the comment text, and empty for every other kind.
	Body string `json:"body,omitempty"`
	// URL points at the event on the backend, so a reader can always
	// leave for the canonical record.
	URL string `json:"url,omitempty"`
	// Detail carries the kind's own fields: label, assignee, milestone,
	// previous_title, current_title, target.
	Detail map[string]string `json:"detail,omitempty"`
}

// ActivityQuery bounds one read of a work item's history.
//
// Paging runs backwards, newest first, because that is the end a reader
// opens a history at. Before is a cursor from a previous page's
// OlderCursor; empty means "start at the newest".
type ActivityQuery struct {
	Before string `json:"before,omitempty"`
	// Limit caps the page. 0 means the adaptor's default. Adaptors clamp
	// it to their own maximum rather than honouring an unbounded request.
	Limit int `json:"limit,omitempty"`
}

// ActivityPage is one page of history, oldest-first within the page.
//
// The page never claims to be the whole history. HasOlder and
// AtOldest are separate and both explicit, because the failure this
// surface exists to prevent is a bounded read being read as complete:
// a projection that carries the last twenty comments is not a history,
// and a renderer must be able to say so.
type ActivityPage struct {
	Events []ActivityEvent `json:"events"`
	// OlderCursor is passed back as ActivityQuery.Before to fetch the
	// page immediately older than this one. Empty when there is none.
	OlderCursor string `json:"older_cursor,omitempty"`
	// HasOlder says more history exists before this page.
	HasOlder bool `json:"has_older"`
	// AtOldest says this page reaches the beginning of the item's
	// history, so a reader looking at it is looking at everything.
	AtOldest bool `json:"at_oldest"`
	// Total is the backend's own count of matching events when it
	// reports one, and 0 when it does not. It is not derived from the
	// page: a page length is never a total.
	Total int `json:"total,omitempty"`
	// Source names the configured source the item came from, so a
	// federated deployment can say which one answered.
	Source string `json:"source,omitempty"`
	// Freshness mirrors the source's own freshness at the time of the
	// read. History is fetched live rather than from the snapshot, so
	// this says how current the surrounding projection is, not the page.
	Freshness string `json:"freshness,omitempty"`
}

// ActivityReader is the optional history surface. A [WorkPlane] that can
// answer for a work item's history implements it; the server type-asserts
// for it and answers 501 when the bound plane does not.
//
// Implementations MUST fail closed on a work item the caller's
// deployment does not carry: return a not-found error rather than an
// empty page, and never distinguish "absent here" from "exists but
// unreadable" in the message, for the same reason GetWorkItem does not.
type ActivityReader interface {
	ReadActivity(ctx context.Context, id WorkItemID, q ActivityQuery) (ActivityPage, error)
}
