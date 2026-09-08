package atab

import (
	"strings"
	"time"
)

// leaseMarker is the one spelling of a lease comment:
//
//	<!-- atab-lease holder=<login> instance=<id> expires=<UTC ISO> -->
const leaseMarker = "<!-- atab-lease "

// Lease is an issue claim held by a running worker. It is deliberately
// separate from the GitHub assignee: the assignee says who owns the
// issue, the lease says whether a worker is holding it right now. A
// dead worker leaves an assignee behind but its lease expires, and that
// difference is exactly what tells a stalled claim from a live one.
type Lease struct {
	Holder   string    `json:"holder,omitempty"`
	Instance string    `json:"instance,omitempty"`
	Expires  time.Time `json:"expires"`
	// ObservedAt is the comment's own timestamp, used to pick the
	// newest lease when an issue carries several.
	ObservedAt time.Time `json:"observed_at"`
}

// Fresh reports whether the lease is still live at now.
func (l Lease) Fresh(now time.Time) bool {
	return !l.Expires.IsZero() && l.Expires.After(now)
}

// LatestLease returns the lease the claim protocol treats as
// authoritative, if the issue carries one.
//
// Election is by highest REST comment id, which is the canonical rule:
// ids are strictly monotonic, while a lease renewed in place carries a
// newer updatedAt than a rival written after it. Only a lease with a
// readable future-or-past expiry is returned here; a holder standing
// down with expires=expired is a claim state rather than a lease, and
// [ClaimOf] is what reports it.
func LatestLease(comments []Comment) (Lease, bool) {
	comment, ok := latestLeaseComment(comments)
	if !ok {
		return Lease{}, false
	}
	lease, parsed := parseLeaseComment(comment)
	if !parsed {
		return Lease{}, false
	}
	return lease, true
}

// parseLeaseComment reads holder, instance and expiry out of one lease
// comment. The bool reports whether a usable expiry was found: a lease
// whose expiry is the literal "expired" parses its holder fine but
// cannot be judged fresh, which is the difference between a released
// claim and an unreadable one.
func parseLeaseComment(c Comment) (Lease, bool) {
	lease := Lease{ObservedAt: c.UpdatedAt}
	if lease.ObservedAt.IsZero() {
		lease.ObservedAt = c.CreatedAt
	}
	body := strings.TrimSuffix(strings.TrimSpace(c.Body), "-->")
	for _, tok := range strings.Fields(body) {
		key, value, ok := strings.Cut(tok, "=")
		if !ok {
			continue
		}
		switch key {
		case "holder":
			lease.Holder = value
		case "instance":
			lease.Instance = value
		case "expires":
			if t, err := time.Parse(time.RFC3339, value); err == nil {
				lease.Expires = t.UTC()
			}
		}
	}
	// An expiry that did not parse leaves the holder readable and the
	// lease unjudgeable, which the caller distinguishes on the bool.
	return lease, !lease.Expires.IsZero()
}
