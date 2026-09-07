package atab

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GembaCore/gemba-core/core"
)

// TestLiveProject1Smoke reads the real Atab-Group Project #1 through the
// gh CLI and asserts the projection holds against live data.
//
// It is opt-in: set ATAB_LIVE=1 to run it. Two reasons, both practical.
// It needs an authenticated gh and a network, so it cannot be part of
// the default lane. And it spends the org's shared GitHub budget, which
// other automation in the same org is spending at the same time — a
// suite that quietly competes for that budget is a suite that makes
// other things fail.
//
// The read is bounded on purpose: one repository, one page. The point is
// to prove the query shape, the projection and the id scheme survive
// real data, not to mirror the whole org.
func TestLiveProject1Smoke(t *testing.T) {
	if os.Getenv("ATAB_LIVE") != "1" {
		t.Skip("set ATAB_LIVE=1 to run the live Project #1 smoke")
	}
	repo := envOr("ATAB_LIVE_REPO", "Product-Seela")

	cfg := DemoSourceConfig()
	cfg.Repos = []string{repo}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}

	client := NewGHClient(cfg).WithPaging(25, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	issues, err := client.FetchIssues(ctx, FetchOptions{
		Repos:         []string{repo},
		IncludeClosed: true,
		Limit:         25,
	})
	if err != nil {
		t.Fatalf("FetchIssues: %v", err)
	}
	if len(issues) == 0 {
		t.Fatalf("no issues returned from %s/%s", cfg.Org, repo)
	}
	t.Logf("fetched %d issues from %s/%s", len(issues), cfg.Org, repo)

	reg := NewRegistry()
	if err := reg.Add(cfg, NewFixtureClient(cfg.ID, issues)); err != nil {
		t.Fatalf("add source: %v", err)
	}
	items, err := New(reg, core.TransportAPI).ListWorkItems(ctx, core.WorkItemFilter{})
	if err != nil {
		t.Fatalf("ListWorkItems: %v", err)
	}

	// Reconciliation: every fetched issue projects to exactly one work
	// item, and no two share an id. A silent drop here would show up as
	// a board that is quietly missing work.
	if len(items) != len(issues) {
		t.Fatalf("projected %d items from %d issues; the projection dropped work",
			len(items), len(issues))
	}
	seen := map[core.WorkItemID]bool{}
	var onBoard, tracked int
	statuses := map[string]int{}
	readiness := map[string]int{}
	for _, it := range items {
		if seen[it.ID] {
			t.Errorf("duplicate id %q", it.ID)
		}
		seen[it.ID] = true

		if _, ok := StateMap[it.Status]; !ok {
			t.Errorf("item %q emitted unmapped status %q", it.ID, it.Status)
		}
		statuses[it.Status]++
		if r, ok := it.Custom[FieldKeyReadiness].(string); ok {
			readiness[r]++
		}
		if s, _ := it.Custom[FieldKeyBoardStatus].(string); s != "" {
			onBoard++
		}
		if it.Custom[FieldKeyMetaState] == string(MetaPresent) {
			tracked++
		}
		if !strings.HasPrefix(string(it.ID), string(cfg.ID)+"/") {
			t.Errorf("item %q is not qualified by its source", it.ID)
		}
	}
	t.Logf("statuses: %v", statuses)
	t.Logf("readiness: %v", readiness)
	t.Logf("on Project #1: %d of %d; carrying an atab-meta block: %d",
		onBoard, len(items), tracked)

	// One live item read back by id has to match the one from the list.
	first := items[0]
	got, err := New(reg, core.TransportAPI).GetWorkItem(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetWorkItem(%q): %v", first.ID, err)
	}
	if got.Title != first.Title || got.Status != first.Status {
		t.Errorf("list and get disagree for %q: %q/%q vs %q/%q",
			first.ID, first.Title, first.Status, got.Title, got.Status)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
