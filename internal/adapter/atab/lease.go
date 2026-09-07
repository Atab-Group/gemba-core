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

// LatestLease returns the newest lease comment in comments, if any.
//
// A comment that does not parse is skipped rather than treated as a
// claim. That direction is the safe one: an unreadable comment read as a
// live lease would strand real work behind a claim nobody holds, while
// reading it as no-lease at worst offers work that a second check at
// claim time will refuse.
func LatestLease(comments []Comment) (Lease, bool) {
	var best Lease
	var found bool
	for _, c := range comments {
		if !strings.HasPrefix(c.Body, leaseMarker) {
			continue
		}
		lease, ok := parseLease(c)
		if !ok {
			continue
		}
		if !found || !lease.ObservedAt.Before(best.ObservedAt) {
			best = lease
			found = true
		}
	}
	return best, found
}

func parseLease(c Comment) (Lease, bool) {
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
	// A lease with no expiry cannot be judged fresh or stale, so it is
	// not a lease this adaptor will report.
	if lease.Expires.IsZero() {
		return Lease{}, false
	}
	return lease, true
}
