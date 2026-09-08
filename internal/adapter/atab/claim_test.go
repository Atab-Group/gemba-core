package atab

import (
	"testing"
	"time"
)

// leaseComment builds the canonical claim comment shape:
//
//	<!-- atab-lease holder=<login> instance=<id> expires=<UTC ISO> -->
func leaseComment(id int64, holder, instance, expires string, at time.Time) Comment {
	return Comment{
		ID: id,
		Body: leaseMarker + "holder=" + holder + " instance=" + instance +
			" expires=" + expires + " -->",
		CreatedAt: at,
		UpdatedAt: at,
	}
}

func heartbeatComment(id int64, at time.Time) Comment {
	return Comment{
		ID:        id,
		Body:      heartbeatMarker + "\n🤖 work: claimed, updated " + at.UTC().Format("2006-01-02T15:04:05Z"),
		CreatedAt: at,
		UpdatedAt: at,
	}
}

func claimIssue(comments []Comment, assignees []string, updated time.Time) Issue {
	return Issue{
		Ref:       IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 700},
		Title:     "claim probe",
		State:     "OPEN",
		UpdatedAt: updated,
		Comments:  comments,
		Assignees: assignees,
	}
}

// The protocol elects the lease comment with the highest REST comment
// id, not the newest timestamp. The difference is load-bearing: renewal
// edits a lease in place, so an older lease can carry a newer updatedAt
// than the rival that beat it, and electing on time would hand the issue
// to the loser of a race the protocol already decided.
func TestClaim_ElectsTheHighestCommentIDNotTheNewestTimestamp(t *testing.T) {
	future := testNow.Add(time.Hour).UTC().Format(time.RFC3339)
	// The lower id was edited later, so it looks newer by timestamp.
	loser := leaseComment(100, "bot-a", "inst-a", future, testNow)
	winner := leaseComment(200, "bot-b", "inst-b", future, testNow.Add(-time.Hour))

	claim := ClaimOf(claimIssue([]Comment{loser, winner}, nil, testNow), FreshnessFresh, testNow)
	if claim.Holder != "bot-b" {
		t.Errorf("holder = %q, want bot-b from the highest comment id", claim.Holder)
	}
	if claim.LeaseCommentID != 200 {
		t.Errorf("lease_comment_id = %d, want 200", claim.LeaseCommentID)
	}
	if claim.State != ClaimActive {
		t.Errorf("state = %q, want active", claim.State)
	}
}

// A lease whose expiry is still ahead, with a heartbeat that has not
// gone quiet, is a claim somebody is actually working.
func TestClaim_ActiveCarriesHolderInstanceAndExpiry(t *testing.T) {
	expires := testNow.Add(2 * time.Hour).UTC()
	issue := claimIssue([]Comment{
		leaseComment(10, "atab-bot", "rig-2", expires.Format(time.RFC3339), testNow),
		heartbeatComment(11, testNow.Add(-time.Minute)),
	}, []string{"atab-bot"}, testNow)

	claim := ClaimOf(issue, FreshnessFresh, testNow)
	if claim.State != ClaimActive {
		t.Fatalf("state = %q, want active (%s)", claim.State, claim.Reason)
	}
	if claim.Holder != "atab-bot" || claim.Instance != "rig-2" {
		t.Errorf("holder/instance = %q/%q, want atab-bot/rig-2", claim.Holder, claim.Instance)
	}
	if !claim.Expires.Equal(expires) {
		t.Errorf("expires = %s, want %s", claim.Expires, expires)
	}
	if claim.LastHeartbeat.IsZero() {
		t.Error("last_heartbeat unset; the heartbeat comment was not read")
	}
	if !claim.Held() {
		t.Error("an active claim must read as held")
	}
}

// A lease past its expiry is work about to re-enter the queue, and the
// holder stays on the card: who abandoned it is the useful part.
func TestClaim_ExpiredKeepsTheHolderAndSaysWhy(t *testing.T) {
	issue := claimIssue([]Comment{
		leaseComment(10, "atab-bot", "rig-2",
			testNow.Add(-time.Minute).UTC().Format(time.RFC3339), testNow.Add(-3*time.Hour)),
	}, []string{"atab-bot"}, testNow)

	claim := ClaimOf(issue, FreshnessFresh, testNow)
	if claim.State != ClaimExpired {
		t.Fatalf("state = %q, want expired", claim.State)
	}
	if claim.Holder != "atab-bot" {
		t.Errorf("holder = %q, want the holder that let it lapse", claim.Holder)
	}
	if claim.Reason == "" {
		t.Error("an expired claim gives no reason")
	}
	if claim.Held() {
		t.Error("an expired claim must not read as held")
	}
}

// A holder standing down writes the literal expires=expired. That is a
// release, not an unreadable lease, so it must not read as no claim at
// all: the holder is still the useful fact.
func TestClaim_TheLiteralExpiredValueIsARelease(t *testing.T) {
	issue := claimIssue([]Comment{
		leaseComment(10, "atab-bot", "rig-2", "expired", testNow.Add(-time.Hour)),
	}, nil, testNow)

	claim := ClaimOf(issue, FreshnessFresh, testNow)
	if claim.State != ClaimExpired {
		t.Fatalf("state = %q, want expired", claim.State)
	}
	if claim.Holder != "atab-bot" {
		t.Errorf("holder = %q, want atab-bot; a release still names who released it", claim.Holder)
	}
}

// The lease is still inside its TTL but the holder has stopped beating.
// The claim is formally held and the worker behind it probably is not
// running, which is a different thing to tell an operator than either
// active or expired.
func TestClaim_AQuietHolderReadsStaleNotActive(t *testing.T) {
	issue := claimIssue([]Comment{
		leaseComment(10, "atab-bot", "rig-2",
			testNow.Add(2*time.Hour).UTC().Format(time.RFC3339), testNow.Add(-2*time.Hour)),
		heartbeatComment(11, testNow.Add(-(heartbeatsBeforeStale*heartbeatInterval + time.Minute))),
	}, []string{"atab-bot"}, testNow)

	claim := ClaimOf(issue, FreshnessFresh, testNow)
	if claim.State != ClaimStale {
		t.Fatalf("state = %q, want stale (%s)", claim.State, claim.Reason)
	}
	if !claim.Held() {
		t.Error("a stale claim is still formally held")
	}
}

// The pipeline assigns the issue seconds before it writes the lease.
// Reading that gap as unclaimed would invite a second worker into an
// issue somebody is in the middle of taking.
func TestClaim_AnAssigneeInsideTheGraceWindowIsAClaimInFlight(t *testing.T) {
	issue := claimIssue(nil, []string{"atab-bot"}, testNow.Add(-time.Minute))

	claim := ClaimOf(issue, FreshnessFresh, testNow)
	if claim.State != ClaimPending {
		t.Fatalf("state = %q, want claiming", claim.State)
	}
	if claim.Holder != "atab-bot" {
		t.Errorf("holder = %q, want the assignee", claim.Holder)
	}
	if !claim.Held() {
		t.Error("a claim in flight must read as held")
	}
}

// Past the grace window an assignee with no lease is just an assignee.
// Holding it as claimed forever would strand the issue behind a claim
// nobody took.
func TestClaim_AnAssigneePastGraceIsNotAClaim(t *testing.T) {
	issue := claimIssue(nil, []string{"someone"}, testNow.Add(-claimGrace-time.Minute))

	if claim := ClaimOf(issue, FreshnessFresh, testNow); claim.State != ClaimNone {
		t.Fatalf("state = %q, want none", claim.State)
	}
}

// The sweep's release record is surfaced, because a crash cycle is only
// visible from the outside as repeated releases.
func TestClaim_ASweptIssueReportsThatItWasReleased(t *testing.T) {
	issue := claimIssue([]Comment{
		{ID: 5, Body: sweepMarker + " released expired claim", CreatedAt: testNow.Add(-time.Hour)},
	}, nil, testNow)

	claim := ClaimOf(issue, FreshnessFresh, testNow)
	if claim.State != ClaimNone {
		t.Fatalf("state = %q, want none", claim.State)
	}
	if !claim.Released {
		t.Error("the sweep's release record was not reported")
	}
}

// An issue nothing has ever claimed is the ordinary case and must read
// as none, with no holder invented for it.
func TestClaim_NoLeaseAndNoAssigneeIsNoClaim(t *testing.T) {
	claim := ClaimOf(claimIssue(nil, nil, testNow.Add(-time.Hour)), FreshnessFresh, testNow)
	if claim.State != ClaimNone {
		t.Fatalf("state = %q, want none", claim.State)
	}
	if claim.Holder != "" {
		t.Errorf("holder = %q, want empty", claim.Holder)
	}
	if claim.Held() {
		t.Error("no claim must not read as held")
	}
}

// A snapshot outside its freshness budget cannot support a statement
// about who is working right now. Naming a holder from data known to be
// old would be a confident answer built on nothing.
func TestClaim_AStaleSnapshotDeclinesToNameAHolder(t *testing.T) {
	issue := claimIssue([]Comment{
		leaseComment(10, "atab-bot", "rig-2",
			testNow.Add(time.Hour).UTC().Format(time.RFC3339), testNow),
	}, []string{"atab-bot"}, testNow)

	for _, f := range []Freshness{FreshnessStale, FreshnessUnknown} {
		claim := ClaimOf(issue, f, testNow)
		if claim.State != ClaimUnknown {
			t.Errorf("freshness %q: state = %q, want unknown", f, claim.State)
		}
		if claim.Holder != "" {
			t.Errorf("freshness %q: holder = %q, want empty", f, claim.Holder)
		}
	}
}

// The GitHub assignee and the claim are different facts and the
// projection must not collapse them. An issue assigned to a person with
// an expired bot lease is the exact case that separates them.
func TestClaim_TheAssigneeIsNotTheHolder(t *testing.T) {
	issue := claimIssue([]Comment{
		leaseComment(10, "atab-bot", "rig-2",
			testNow.Add(-time.Hour).UTC().Format(time.RFC3339), testNow.Add(-4*time.Hour)),
	}, []string{"a-person"}, testNow)

	claim := ClaimOf(issue, FreshnessFresh, testNow)
	if claim.Holder != "atab-bot" {
		t.Errorf("holder = %q, want the lease holder rather than the assignee", claim.Holder)
	}
	if claim.State != ClaimExpired {
		t.Errorf("state = %q, want expired", claim.State)
	}
}

// A malformed lease comment is skipped rather than read as a claim.
// Reading an unparseable comment as a live lease would strand real work
// behind a claim nobody holds.
func TestClaim_AMalformedLeaseCommentIsNotAClaim(t *testing.T) {
	issue := claimIssue([]Comment{
		{ID: 10, Body: leaseMarker + "this is not the shape at all -->", CreatedAt: testNow},
	}, nil, testNow.Add(-time.Hour))

	claim := ClaimOf(issue, FreshnessFresh, testNow)
	// It parses to a holder-less lease with no expiry, which is a
	// release rather than an active hold. What must never happen is
	// active.
	if claim.State == ClaimActive {
		t.Fatalf("a malformed lease read as an active claim: %+v", claim)
	}
	if claim.Held() {
		t.Error("a malformed lease must not hold the issue")
	}
}
