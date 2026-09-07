package atab

import (
	"context"
	"testing"
	"time"

	"github.com/GembaCore/gemba-core/core"
)

func testCache(t *testing.T, issues []Issue) (*Cache, *FixtureClient, *time.Time) {
	t.Helper()
	cfg := DemoSourceConfig()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	client := NewFixtureClient(cfg.ID, issues)
	c := NewCache(cfg, client)
	clock := testNow
	c.now = func() time.Time { return clock }
	return c, client, &clock
}

func TestCache_FirstReadIsAFullFetch(t *testing.T) {
	c, client, _ := testCache(t, DemoIssues())
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Issues) != len(DemoIssues()) {
		t.Errorf("issues = %d, want %d", len(snap.Issues), len(DemoIssues()))
	}
	if snap.Freshness != FreshnessFresh {
		t.Errorf("freshness = %q, want fresh", snap.Freshness)
	}
	if got := client.LastOptions(); !got.UpdatedSince.IsZero() {
		t.Errorf("first fetch sent a watermark %v; it must be a full fetch", got.UpdatedSince)
	}
	if client.Calls() != 1 {
		t.Errorf("calls = %d, want 1", client.Calls())
	}
}

// A second read inside the refresh interval must not hit the source. The
// interval is the whole reason the board can be opened repeatedly without
// spending the rate-limit budget.
func TestCache_HonoursTheRefreshInterval(t *testing.T) {
	c, client, clock := testCache(t, DemoIssues())
	ctx := context.Background()
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(10 * time.Second)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if client.Calls() != 1 {
		t.Fatalf("calls = %d, want 1 — the second read was inside the interval", client.Calls())
	}
	*clock = clock.Add(DefaultRefreshInterval)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if client.Calls() != 2 {
		t.Fatalf("calls = %d, want 2 — the interval had elapsed", client.Calls())
	}
}

// The second fetch asks only for what changed, with an overlap so an
// issue updated in the same second as the last fetch cannot fall through
// the gap between two windows.
func TestCache_RefreshIsIncrementalWithOverlap(t *testing.T) {
	c, client, clock := testCache(t, DemoIssues())
	ctx := context.Background()
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	firstSuccess := *clock

	*clock = clock.Add(DefaultRefreshInterval)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	got := client.LastOptions().UpdatedSince
	if got.IsZero() {
		t.Fatal("the second fetch sent no watermark; it would be a full scan every minute")
	}
	want := firstSuccess.Add(-refreshOverlap)
	if !got.Equal(want) {
		t.Fatalf("watermark = %v, want %v (last success minus the overlap)", got, want)
	}
}

// An incremental fetch merges rather than replaces, so an issue that did
// not change in this window keeps its last known values.
func TestCache_IncrementalFetchMergesRatherThanReplaces(t *testing.T) {
	c, client, clock := testCache(t, DemoIssues())
	ctx := context.Background()
	snap, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := len(snap.Issues)

	updated := Issue{
		Ref:       IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 301},
		Title:     "Retitled upstream",
		State:     "OPEN",
		UpdatedAt: clock.Add(2 * time.Minute),
	}
	client.Replace([]Issue{updated})

	*clock = clock.Add(DefaultRefreshInterval)
	snap, err = c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Issues) != before {
		t.Fatalf("issues = %d after an incremental refresh, want %d kept", len(snap.Issues), before)
	}
	if got := snap.Issues[updated.Ref].Title; got != "Retitled upstream" {
		t.Fatalf("updated issue title = %q, want the refreshed value", got)
	}
}

// A full re-anchor replaces the map, which is how an issue that was
// deleted, transferred or moved off the board leaves the board. An
// incremental chain alone would never notice.
func TestCache_FullRefreshReanchorsAndDropsRemovedIssues(t *testing.T) {
	c, client, clock := testCache(t, DemoIssues())
	ctx := context.Background()
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	kept := DemoIssues()[0]
	kept.UpdatedAt = clock.Add(fullRefreshEvery)
	client.Replace([]Issue{kept})

	*clock = clock.Add(fullRefreshEvery)
	snap, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Issues) != 1 {
		t.Fatalf("issues = %d after a full re-anchor, want 1", len(snap.Issues))
	}
	if !client.LastOptions().UpdatedSince.IsZero() {
		t.Error("a re-anchor must be a full fetch, not an incremental one")
	}
}

// A refresh failure over a populated cache keeps serving the last good
// snapshot. An empty board would read as "no work", which is the one
// wrong answer during an outage.
func TestCache_RefreshFailureKeepsTheLastGoodSnapshot(t *testing.T) {
	c, client, clock := testCache(t, DemoIssues())
	ctx := context.Background()
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	client.SetFailure(core.NewAdaptorError(core.KindRequestFailed, "github is down"))

	*clock = clock.Add(DefaultRefreshInterval)
	snap, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot returned an error over a populated cache: %v", err)
	}
	if len(snap.Issues) == 0 {
		t.Fatal("the last good snapshot was dropped on a refresh failure")
	}
	if h := c.Health(); h.Healthy {
		t.Error("health must report the source as unhealthy after a failed refresh")
	}
}

// Past the freshness budget the snapshot is served with an explicit
// stale marker rather than silently presented as current.
func TestCache_GoesStalePastTheBudget(t *testing.T) {
	c, client, clock := testCache(t, DemoIssues())
	ctx := context.Background()
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	client.SetFailure(core.NewAdaptorError(core.KindRequestFailed, "still down"))

	*clock = clock.Add(DefaultStaleAfter + time.Minute)
	snap, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Freshness != FreshnessStale {
		t.Fatalf("freshness = %q, want stale", snap.Freshness)
	}
	if len(snap.Issues) == 0 {
		t.Error("a stale snapshot still carries its last known items")
	}
}

// A source that has never been read successfully has nothing to serve,
// and that is a real error rather than an empty board.
func TestCache_NeverReadSurfacesTheError(t *testing.T) {
	c, client, _ := testCache(t, DemoIssues())
	client.SetFailure(core.NewAdaptorError(core.KindCapabilityDenied, "no access"))
	if _, err := c.Snapshot(context.Background()); err == nil {
		t.Fatal("a source that has never been read must not return an empty snapshot")
	}
	h := c.Health()
	if h.Healthy || h.Freshness != FreshnessUnknown {
		t.Fatalf("health = %+v, want unhealthy with unknown freshness", h)
	}
}

func TestSourceConfig_NormalizeRejectsIncoherentFreshness(t *testing.T) {
	cfg := SourceConfig{
		ID: "s", Org: "o",
		RefreshInterval: 10 * time.Minute,
		StaleAfter:      time.Minute,
	}
	if err := cfg.Normalize(); err == nil {
		t.Fatal("a stale_after below refresh_interval would make every snapshot born stale")
	}
}

func TestSourceConfig_NormalizeDefaults(t *testing.T) {
	cfg := SourceConfig{ID: "s", Org: "Atab-Group"}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if cfg.Authority != AuthorityReference {
		t.Errorf("authority = %q, want the reference default", cfg.Authority)
	}
	if cfg.RefreshInterval != DefaultRefreshInterval || cfg.StaleAfter != DefaultStaleAfter {
		t.Errorf("freshness defaults not applied: %+v", cfg)
	}
	if cfg.Display != "Atab-Group" {
		t.Errorf("display = %q, want the org name", cfg.Display)
	}
}
