package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GembaCore/gemba-core/core"
	"github.com/GembaCore/gemba-core/internal/config"
)

// summaryItem builds a projected item carrying the atab_* fields the
// summary reads.
func summaryItem(id, status, source, repo, readiness, claimState, holder string) core.WorkItem {
	return core.WorkItem{
		ID:            core.WorkItemID(id),
		Kind:          "task",
		Title:         "item " + id,
		Status:        status,
		StateCategory: core.StateBacklog,
		Custom: map[string]any{
			"atab_source":      source,
			"atab_repo":        repo,
			"atab_readiness":   readiness,
			"atab_claim_state": claimState,
			"atab_claimed_by":  holder,
			"atab_freshness":   "fresh",
			"atab_claim":       map[string]any{"expires": "2026-09-08T12:00:00Z"},
		},
	}
}

func summaryRouter(t *testing.T, items []core.WorkItem) http.Handler {
	t.Helper()
	host := newProgrammableHostFull(t, nil,
		func(context.Context, core.WorkItemFilter) ([]core.WorkItem, error) {
			return items, nil
		})
	return NewRouter(config.ServeConfig{}, fakeSPA(), host)
}

func getSummary(t *testing.T, h http.Handler) workSummary {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/work-summary", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out workSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// The whole point of the endpoint: a widget gets its numbers without
// pulling the board. The counts have to be of everything, not of the
// first page, or the summary would disagree with the board it summarises.
func TestWorkSummary_CountsEveryItemGroupedBySource(t *testing.T) {
	items := []core.WorkItem{
		summaryItem("a/1", "Todo", "atab-group", "Atab-Group/One", "ready", "none", ""),
		summaryItem("a/2", "Done", "atab-group", "Atab-Group/One", "done", "none", ""),
		summaryItem("a/3", "In Progress", "atab-group", "Atab-Group/Two", "in_flight", "active", "bot-a"),
		summaryItem("b/1", "Todo", "hadedahealth", "hadedahealth/Repo", "ready", "none", ""),
	}
	out := getSummary(t, summaryRouter(t, items))

	if out.Total != 4 {
		t.Errorf("total = %d, want 4", out.Total)
	}
	if len(out.Sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(out.Sources))
	}
	// Sorted, so a polling widget does not reshuffle its rows between
	// two identical answers.
	if out.Sources[0].ID != "atab-group" || out.Sources[1].ID != "hadedahealth" {
		t.Errorf("sources are not in a stable order: %+v", out.Sources)
	}
	if out.Sources[0].Items != 3 {
		t.Errorf("atab-group items = %d, want 3", out.Sources[0].Items)
	}
	if out.Sources[0].Repos["Atab-Group/One"] != 2 {
		t.Errorf("repo counts wrong: %+v", out.Sources[0].Repos)
	}
	if out.ByStatus["Todo"] != 2 || out.ByStatus["Done"] != 1 {
		t.Errorf("by_status = %+v", out.ByStatus)
	}
	if out.ByReadiness["ready"] != 2 {
		t.Errorf("by_readiness = %+v", out.ByReadiness)
	}
}

// A widget shows what is running now. Held work is listed; expired and
// released claims are counted but not listed, because a list of work
// nobody is doing is not what the surface is for.
func TestWorkSummary_ListsHeldWorkAndOnlyCountsTheRest(t *testing.T) {
	items := []core.WorkItem{
		summaryItem("a/1", "In Progress", "s", "r", "in_flight", "active", "bot-a"),
		summaryItem("a/2", "In Progress", "s", "r", "in_flight", "stale", "bot-b"),
		summaryItem("a/3", "Todo", "s", "r", "ready", "claiming", "bot-c"),
		summaryItem("a/4", "Todo", "s", "r", "ready", "expired", "bot-d"),
		summaryItem("a/5", "Todo", "s", "r", "ready", "none", ""),
	}
	out := getSummary(t, summaryRouter(t, items))

	if len(out.Claimed) != 3 {
		t.Fatalf("claimed = %d, want the three that are held: %+v", len(out.Claimed), out.Claimed)
	}
	for _, c := range out.Claimed {
		if c.State == "expired" || c.State == "none" {
			t.Errorf("unheld work appeared in the claimed list: %+v", c)
		}
		if c.Holder == "" {
			t.Errorf("a held item names no holder: %+v", c)
		}
	}
	if out.ByClaim["expired"] != 1 || out.ByClaim["none"] != 1 {
		t.Errorf("by_claim lost the unheld states: %+v", out.ByClaim)
	}
}

// The response must not grow with the board. A deployment with more
// simultaneous claims than the cap gets the cap plus a count of what was
// left out, rather than a second copy of the board.
func TestWorkSummary_TheHeldListIsCappedAndSaysSo(t *testing.T) {
	var items []core.WorkItem
	for i := range summaryClaimCap + 7 {
		items = append(items, summaryItem(
			"a/"+string(rune('a'+i%26))+string(rune('a'+i/26)),
			"In Progress", "s", "r", "in_flight", "active", "bot"))
	}
	out := getSummary(t, summaryRouter(t, items))

	if len(out.Claimed) != summaryClaimCap {
		t.Errorf("claimed = %d, want the cap %d", len(out.Claimed), summaryClaimCap)
	}
	if out.ClaimedTruncated != 7 {
		t.Errorf("claimed_truncated = %d, want 7", out.ClaimedTruncated)
	}
}

// The endpoint is composed from the WorkPlane interface, so an adaptor
// that carries none of the atab fields still gets a usable answer rather
// than an error or a crash.
func TestWorkSummary_AnAdaptorWithoutTheAtabFieldsStillSummarises(t *testing.T) {
	items := []core.WorkItem{
		{ID: "gm-1", Kind: "task", Title: "plain", Status: "open", StateCategory: core.StateBacklog},
		{ID: "gm-2", Kind: "task", Title: "plain", Status: "open", StateCategory: core.StateBacklog},
	}
	out := getSummary(t, summaryRouter(t, items))

	if out.Total != 2 {
		t.Errorf("total = %d, want 2", out.Total)
	}
	if out.ByStatus["open"] != 2 {
		t.Errorf("by_status = %+v", out.ByStatus)
	}
	if len(out.Claimed) != 0 {
		t.Errorf("claims invented for an adaptor that reports none: %+v", out.Claimed)
	}
	if len(out.Sources) != 1 || out.Sources[0].ID != "default" {
		t.Errorf("sources = %+v, want a single default bucket", out.Sources)
	}
}

// With no adaptor bound the surface says so rather than reporting an
// empty board, which would read as "no work" to a widget.
func TestWorkSummary_NoAdaptorIsUnavailableNotEmpty(t *testing.T) {
	h := NewRouter(config.ServeConfig{}, fakeSPA(), nil)
	req := httptest.NewRequest(http.MethodGet, "/api/work-summary", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
}

// The wire shape matters more than the decoded one: a nil slice
// marshals to null, and a widget that has to handle both null and []
// for "nothing is running" will eventually handle only one of them.
// Decoding into a Go slice hides this, so the raw JSON is asserted.
func TestWorkSummary_EmptyGroupsAreArraysNotNull(t *testing.T) {
	h := summaryRouter(t, []core.WorkItem{
		{ID: "gm-1", Kind: "task", Title: "plain", Status: "open", StateCategory: core.StateBacklog},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/work-summary", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"claimed", "sources", "adaptors"} {
		got := string(raw[key])
		if got == "null" || got == "" {
			t.Errorf("%s = %q, want a JSON array", key, got)
		}
	}
}

// projectItem builds a projected item carrying the project axis.
func projectItem(id, source, project, title string) core.WorkItem {
	custom := map[string]any{
		"atab_source":    source,
		"atab_freshness": "fresh",
	}
	if project != "" {
		custom["atab_project"] = project
		custom["atab_project_title"] = title
	}
	return core.WorkItem{
		ID:            core.WorkItemID(id),
		Kind:          "task",
		Title:         "item " + id,
		Status:        "Todo",
		StateCategory: core.StateBacklog,
		Custom:        custom,
	}
}

// The project axis is not the source axis. Two boards inside one org
// have to count separately, or "project" would mean "org" and the filter
// would answer a question nobody asked.
func TestWorkSummary_CountsProjectsSeparatelyFromSources(t *testing.T) {
	items := []core.WorkItem{
		projectItem("a/1", "atab-group", "atab-group#1", "Atab Group Tasks"),
		projectItem("a/2", "atab-group", "atab-group#1", "Atab Group Tasks"),
		projectItem("a/3", "atab-group", "atab-group#4", "Portfolio"),
		projectItem("b/1", "hadedahealth", "hadedahealth#1", "HadedaHealth Build"),
	}
	out := getSummary(t, summaryRouter(t, items))

	if len(out.Sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(out.Sources))
	}
	if len(out.Projects) != 3 {
		t.Fatalf("projects = %d, want 3 boards across 2 sources: %+v", len(out.Projects), out.Projects)
	}
	byID := map[string]summaryProject{}
	for _, p := range out.Projects {
		byID[p.ID] = p
	}
	if byID["atab-group#1"].Items != 2 {
		t.Errorf("atab-group#1 = %d items, want 2", byID["atab-group#1"].Items)
	}
	if byID["atab-group#1"].Title != "Atab Group Tasks" {
		t.Errorf("atab-group#1 title = %q", byID["atab-group#1"].Title)
	}
	if byID["atab-group#4"].Source != "atab-group" {
		t.Errorf("atab-group#4 source = %q, want atab-group", byID["atab-group#4"].Source)
	}
}

// Work on no board is a real answer and gets its own bucket. Leaving it
// out would make the project counts disagree with the board total.
func TestWorkSummary_UnfiledWorkIsItsOwnBucketAndSortsLast(t *testing.T) {
	items := []core.WorkItem{
		projectItem("a/1", "atab-group", "atab-group#1", "Atab Group Tasks"),
		projectItem("a/2", "atab-group", "", ""),
		projectItem("b/1", "hadedahealth", "", ""),
	}
	out := getSummary(t, summaryRouter(t, items))

	if len(out.Projects) != 2 {
		t.Fatalf("projects = %d, want the board plus the unfiled bucket", len(out.Projects))
	}
	last := out.Projects[len(out.Projects)-1]
	if last.ID != "" {
		t.Errorf("the unfiled bucket is not last: %+v", out.Projects)
	}
	if last.Items != 2 {
		t.Errorf("unfiled = %d items, want 2 across both sources", last.Items)
	}
	// The bucket spans sources, so crediting it to one would be a lie.
	if last.Source != "" {
		t.Errorf("the unfiled bucket claims source %q", last.Source)
	}

	total := 0
	for _, p := range out.Projects {
		total += p.Items
	}
	if total != out.Total {
		t.Errorf("project counts sum to %d, but the board holds %d", total, out.Total)
	}
}
