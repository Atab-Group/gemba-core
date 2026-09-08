package atab

import (
	"strings"
	"time"
)

// The claim protocol is ATAB Core's, not this adaptor's. Every constant
// and rule below is read off the canonical implementation
// (plugins/atab-core: scripts/claim.py, scripts/lease_sweep.py,
// references/pipeline-contracts.json) rather than chosen here, because a
// dashboard that disagreed with the dispatcher about who holds an issue
// would be worse than one that showed nothing.
const (
	// heartbeatMarker prefixes the worker's liveness comment.
	heartbeatMarker = "<!-- atab-heartbeat -->"
	// sweepMarker prefixes the record the sweep leaves when it releases
	// an expired claim.
	sweepMarker = "<!-- atab-lease-sweep -->"

	// claimGrace is the sweep's grace window for an assignee that has no
	// lease yet. The pipeline assigns the issue seconds before it writes
	// the lease, so an assigned issue touched inside this window is a
	// claim in flight rather than a claim that failed.
	claimGrace = 15 * time.Minute

	// heartbeatInterval is the only heartbeat cadence the contracts
	// declare (lease.marshal_heartbeat_seconds). Issue work upserts its
	// heartbeat comment without declaring a period of its own, so this is
	// the interval a quiet worker is measured against.
	heartbeatInterval = 10 * time.Minute
	// heartbeatsBeforeStale is how many missed beats read as stale. Three
	// is far enough out to not flag ordinary jitter, and it sits well
	// inside the three-hour lease TTL, so stale is a warning that arrives
	// before expiry rather than a verdict competing with it.
	heartbeatsBeforeStale = 3
)

// ClaimState is what the claim protocol says about an issue right now.
type ClaimState string

const (
	// ClaimNone: nothing holds it.
	ClaimNone ClaimState = "none"
	// ClaimActive: a lease whose expiry is still in the future, with a
	// heartbeat that has not gone quiet.
	ClaimActive ClaimState = "active"
	// ClaimStale: the lease has not expired, but the holder has stopped
	// beating. The claim is still formally held; the worker behind it
	// probably is not running. It is reported apart from active because
	// those are different things to a person deciding whether to step in.
	ClaimStale ClaimState = "stale"
	// ClaimExpired: the lease's expiry has passed, or the holder released
	// it by writing expires=expired. The sweep releases these, so an
	// expired claim is work about to re-enter the queue.
	ClaimExpired ClaimState = "expired"
	// ClaimPending: assigned with no lease yet, inside the sweep's grace
	// window. A claim in flight, not a failed one.
	ClaimPending ClaimState = "claiming"
	// ClaimUnknown: the snapshot behind the issue is outside its source's
	// freshness budget, so the adaptor declines to assert who holds it.
	// Asserting a holder from data it cannot vouch for would be worse
	// than admitting it does not know.
	ClaimUnknown ClaimState = "unknown"
)

// Claim is the projected answer to "is anything working on this right
// now, and can I trust that".
//
// It is kept apart from the GitHub assignee throughout. The assignee
// says who owns the issue; the claim says whether a worker is holding it
// this minute. A worker that dies leaves its assignee behind while its
// lease expires, and that difference is the only thing that separates a
// stalled claim from a live one.
type Claim struct {
	State    ClaimState `json:"state"`
	Holder   string     `json:"holder,omitempty"`
	Instance string     `json:"instance,omitempty"`
	// Expires is the lease's declared expiry. Zero when the holder
	// released the lease with the literal expires=expired.
	Expires time.Time `json:"expires,omitempty"`
	// LastHeartbeat is when the holder last said it was alive. Zero when
	// it never has, which is not itself a fault: the heartbeat comment is
	// written just after the lease.
	LastHeartbeat time.Time `json:"last_heartbeat,omitempty"`
	// LeaseCommentID is the comment the claim was read from. It is the
	// tiebreak the protocol uses, so reporting it makes a contested claim
	// auditable against the issue itself.
	LeaseCommentID int64 `json:"lease_comment_id,omitempty"`
	// Released is true when the sweep has recorded a release on this
	// issue, which is how a crash-cycle shows up from the outside.
	Released bool `json:"released,omitempty"`
	// Reason names the state in words, for a card that has room for one
	// line rather than a struct.
	Reason string `json:"reason,omitempty"`
}

// Held reports whether anything is holding the issue in a way that
// should stop somebody else picking it up.
func (c Claim) Held() bool {
	return c.State == ClaimActive || c.State == ClaimStale || c.State == ClaimPending
}

// latestLeaseComment returns the lease comment the protocol treats as
// authoritative: the one with the highest REST comment id.
//
// Highest id rather than newest timestamp is the canonical rule and the
// difference is load-bearing. Two workers racing both write a lease, and
// the renewal path edits its own comment in place, so an older lease can
// carry a newer updatedAt than the rival that beat it. Comment ids are
// strictly monotonic, which is why the protocol elects on them.
func latestLeaseComment(comments []Comment) (Comment, bool) {
	var best Comment
	var found bool
	for _, c := range comments {
		if !strings.HasPrefix(c.Body, leaseMarker) {
			continue
		}
		switch {
		case !found:
			best, found = c, true
		case c.ID > best.ID:
			best = c
		case c.ID == 0 && best.ID == 0 && c.at().After(best.at()):
			// Neither comment carries an id, which happens only for a
			// client that cannot supply one. Fall back to time rather
			// than to nothing.
			best = c
		}
	}
	return best, found
}

// at is the comment's own instant, preferring the edit over the write.
func (c Comment) at() time.Time {
	if !c.UpdatedAt.IsZero() {
		return c.UpdatedAt
	}
	return c.CreatedAt
}

// lastHeartbeat returns the newest heartbeat comment's instant.
//
// The body carries a timestamp too, but the comment's own updatedAt is
// what the upsert moves and is not subject to the body's formatting, so
// it is the one read here. The body timestamp is parsed as a fallback
// for a heartbeat whose comment metadata did not survive the fetch.
func lastHeartbeat(comments []Comment) time.Time {
	var best time.Time
	for _, c := range comments {
		if !strings.HasPrefix(c.Body, heartbeatMarker) {
			continue
		}
		when := c.at()
		if when.IsZero() {
			when = parseHeartbeatBody(c.Body)
		}
		if when.After(best) {
			best = when
		}
	}
	return best
}

// parseHeartbeatBody reads the instant out of the heartbeat comment's
// text, which the writer formats as "... updated 2026-09-08T01:02:03Z".
func parseHeartbeatBody(body string) time.Time {
	const key = "updated "
	i := strings.LastIndex(body, key)
	if i < 0 {
		return time.Time{}
	}
	field := strings.Fields(body[i+len(key):])
	if len(field) == 0 {
		return time.Time{}
	}
	t, err := time.Parse("2006-01-02T15:04:05Z", field[0])
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// released reports whether the sweep has recorded a release here.
func released(comments []Comment) bool {
	for _, c := range comments {
		if strings.HasPrefix(c.Body, sweepMarker) {
			return true
		}
	}
	return false
}

// ClaimOf reads the claim protocol off one issue.
//
// freshness gates the whole answer. When the snapshot the issue came
// from is outside its source's budget, every field below describes a
// moment the adaptor cannot vouch for, and reporting a holder from it
// would be a confident answer built on data known to be old.
func ClaimOf(issue Issue, freshness Freshness, now time.Time) Claim {
	if freshness == FreshnessUnknown || freshness == FreshnessStale {
		return Claim{
			State:  ClaimUnknown,
			Reason: "the snapshot behind this item is outside its freshness budget",
		}
	}

	claim := Claim{Released: released(issue.Comments)}
	beat := lastHeartbeat(issue.Comments)
	claim.LastHeartbeat = beat

	comment, ok := latestLeaseComment(issue.Comments)
	if !ok {
		// No lease. An assignee inside the sweep's grace window is a
		// claim being taken right now: the pipeline assigns before it
		// writes the lease, and calling that gap "unclaimed" would invite
		// a second worker into the same issue.
		if len(issue.Assignees) > 0 && now.Sub(issue.UpdatedAt) < claimGrace {
			claim.State = ClaimPending
			claim.Holder = issue.Assignees[0]
			claim.Reason = "assigned moments ago; the lease has not landed yet"
			return claim
		}
		claim.State = ClaimNone
		if claim.Released {
			claim.Reason = "released by the sweep after its claim expired"
		}
		return claim
	}

	lease, parsed := parseLeaseComment(comment)
	claim.Holder = lease.Holder
	claim.Instance = lease.Instance
	claim.LeaseCommentID = comment.ID

	// A lease naming no expiry, or the literal expires=expired, is a
	// holder standing down rather than a holder nobody can read.
	if !parsed || lease.Expires.IsZero() {
		claim.State = ClaimExpired
		claim.Reason = "the holder released this lease"
		return claim
	}
	claim.Expires = lease.Expires

	if !lease.Expires.After(now) {
		claim.State = ClaimExpired
		claim.Reason = "the lease expired; the sweep releases it on its next pass"
		return claim
	}

	if !beat.IsZero() && now.Sub(beat) > heartbeatsBeforeStale*heartbeatInterval {
		claim.State = ClaimStale
		claim.Reason = "the lease is still held, but the holder has stopped reporting"
		return claim
	}

	claim.State = ClaimActive
	claim.Reason = "held by a running worker"
	return claim
}
