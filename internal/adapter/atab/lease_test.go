package atab

import (
	"testing"
	"time"
)

func TestLatestLease_PicksTheNewest(t *testing.T) {
	comments := []Comment{
		{
			Body:      "<!-- atab-lease holder=old instance=w-1 expires=2026-09-07T10:00:00Z -->",
			UpdatedAt: time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC),
		},
		{Body: "just a comment", UpdatedAt: time.Date(2026, 9, 7, 9, 30, 0, 0, time.UTC)},
		{
			Body:      "<!-- atab-lease holder=new instance=w-2 expires=2026-09-07T13:00:00Z -->",
			UpdatedAt: time.Date(2026, 9, 7, 11, 0, 0, 0, time.UTC),
		},
	}
	lease, ok := LatestLease(comments)
	if !ok {
		t.Fatal("expected a lease")
	}
	if lease.Holder != "new" || lease.Instance != "w-2" {
		t.Fatalf("lease = %+v, want the newest comment's", lease)
	}
	if !lease.Fresh(testNow) {
		t.Error("a lease expiring at 13:00 must be fresh at 12:00")
	}
}

func TestLatestLease_ExpiredIsStillReported(t *testing.T) {
	lease, ok := LatestLease([]Comment{{
		Body:      "<!-- atab-lease holder=dead instance=w-9 expires=2026-09-06T19:00:00Z -->",
		UpdatedAt: time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC),
	}})
	if !ok {
		t.Fatal("an expired lease is still a lease; it is the trace a worker died holding the issue")
	}
	if lease.Fresh(testNow) {
		t.Error("a lease that expired yesterday must not read as fresh")
	}
}

func TestLatestLease_NoLease(t *testing.T) {
	if _, ok := LatestLease([]Comment{{Body: "Claimed by claude-1."}}); ok {
		t.Fatal("a plain comment is not a lease")
	}
	if _, ok := LatestLease(nil); ok {
		t.Fatal("no comments means no lease")
	}
}

// An unparseable lease reads as no lease, never as a live claim. Reading
// it the other way would strand work behind a claim nobody holds.
func TestLatestLease_UnreadableLeaseReadsAsUnclaimed(t *testing.T) {
	if _, ok := LatestLease([]Comment{
		{Body: "<!-- atab-lease holder=x instance=y -->"},
		{Body: "<!-- atab-lease holder=x expires=not-a-time -->"},
	}); ok {
		t.Fatal("a lease with no readable expiry must not read as a claim")
	}
}

func TestLatestLease_FallsBackToCreatedAt(t *testing.T) {
	lease, ok := LatestLease([]Comment{{
		Body:      "<!-- atab-lease holder=a instance=b expires=2030-01-01T00:00:00Z -->",
		CreatedAt: time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC),
	}})
	if !ok || lease.ObservedAt.IsZero() {
		t.Fatalf("lease = %+v, want createdAt used when updatedAt is absent", lease)
	}
}
