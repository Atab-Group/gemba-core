package atab

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GembaCore/gemba-core/core"
)

func node(t *testing.T, v map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal node: %v", err)
	}
	return raw
}

// Every timeline type the query asks for has to arrive as the kind the
// renderer branches on. A type mapped to the wrong kind renders with
// the wrong icon and the wrong sentence, which is a history that lies
// rather than one that is merely incomplete.
func TestConvertTimelineNode_MapsEveryRequestedType(t *testing.T) {
	const at = "2026-09-01T10:00:00Z"
	cases := []struct {
		name        string
		raw         map[string]any
		wantKind    core.ActivityKind
		wantActor   string
		wantSummary string
		wantDetail  map[string]string
	}{
		{
			name: "comment",
			raw: map[string]any{
				"__typename": "IssueComment", "id": "IC_1", "createdAt": at,
				"body": "the body", "author": map[string]any{"login": "nic"},
			},
			wantKind:  core.ActivityComment,
			wantActor: "nic",
		},
		{
			name: "closed",
			raw: map[string]any{
				"__typename": "ClosedEvent", "id": "CE_1", "createdAt": at,
				"actor": map[string]any{"login": "bot"},
			},
			wantKind: core.ActivityClosed, wantActor: "bot", wantSummary: "closed this",
		},
		{
			name: "reopened",
			raw: map[string]any{
				"__typename": "ReopenedEvent", "id": "RE_1", "createdAt": at,
				"actor": map[string]any{"login": "bot"},
			},
			wantKind: core.ActivityReopened, wantSummary: "reopened this",
		},
		{
			name: "labeled",
			raw: map[string]any{
				"__typename": "LabeledEvent", "id": "LE_1", "createdAt": at,
				"actor": map[string]any{"login": "bot"},
				"label": map[string]any{"name": "type:feature"},
			},
			wantKind:   core.ActivityLabeled,
			wantDetail: map[string]string{"label": "type:feature"},
		},
		{
			name: "unlabeled",
			raw: map[string]any{
				"__typename": "UnlabeledEvent", "id": "UL_1", "createdAt": at,
				"label": map[string]any{"name": "blocked"},
			},
			wantKind:   core.ActivityUnlabeled,
			wantDetail: map[string]string{"label": "blocked"},
		},
		{
			name: "assigned",
			raw: map[string]any{
				"__typename": "AssignedEvent", "id": "AE_1", "createdAt": at,
				"assignee": map[string]any{"login": "mike"},
			},
			wantKind:   core.ActivityAssigned,
			wantDetail: map[string]string{"assignee": "mike"},
		},
		{
			name: "unassigned",
			raw: map[string]any{
				"__typename": "UnassignedEvent", "id": "UE_1", "createdAt": at,
				"assignee": map[string]any{"login": "mike"},
			},
			wantKind:   core.ActivityUnassigned,
			wantDetail: map[string]string{"assignee": "mike"},
		},
		{
			name: "renamed",
			raw: map[string]any{
				"__typename": "RenamedTitleEvent", "id": "RT_1", "createdAt": at,
				"previousTitle": "old", "currentTitle": "new",
			},
			wantKind: core.ActivityRenamed,
			wantDetail: map[string]string{
				"previous_title": "old", "current_title": "new",
			},
		},
		{
			name: "cross referenced",
			raw: map[string]any{
				"__typename": "CrossReferencedEvent", "id": "CR_1", "createdAt": at,
				"source": map[string]any{
					"__typename": "Issue", "number": 42, "title": "the other one",
					"url":        "https://github.com/Atab-Group/ATAB-Marketplace/issues/42",
					"repository": map[string]any{"name": "ATAB-Marketplace", "owner": map[string]any{"login": "Atab-Group"}},
				},
			},
			wantKind:   core.ActivityReferenced,
			wantDetail: map[string]string{"target": "Atab-Group/ATAB-Marketplace#42"},
		},
		{
			name: "milestoned",
			raw: map[string]any{
				"__typename": "MilestonedEvent", "id": "ME_1", "createdAt": at,
				"milestoneTitle": "v2",
			},
			wantKind:   core.ActivityMilestoned,
			wantDetail: map[string]string{"milestone": "v2"},
		},
		{
			name: "demilestoned",
			raw: map[string]any{
				"__typename": "DemilestonedEvent", "id": "DE_1", "createdAt": at,
				"milestoneTitle": "v2",
			},
			wantKind:   core.ActivityDemilestoned,
			wantDetail: map[string]string{"milestone": "v2"},
		},
		{
			name: "duplicate",
			raw: map[string]any{
				"__typename": "MarkedAsDuplicateEvent", "id": "MD_1", "createdAt": at,
			},
			wantKind: core.ActivityDuplicate, wantSummary: "marked this as a duplicate",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := convertTimelineNode(node(t, tc.raw))
			if !ok {
				t.Fatalf("node was dropped")
			}
			if ev.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", ev.Kind, tc.wantKind)
			}
			if tc.wantActor != "" && ev.Actor != tc.wantActor {
				t.Errorf("actor = %q, want %q", ev.Actor, tc.wantActor)
			}
			if tc.wantSummary != "" && ev.Summary != tc.wantSummary {
				t.Errorf("summary = %q, want %q", ev.Summary, tc.wantSummary)
			}
			for k, want := range tc.wantDetail {
				if ev.Detail[k] != want {
					t.Errorf("detail[%q] = %q, want %q", k, ev.Detail[k], want)
				}
			}
			if ev.At.IsZero() {
				t.Errorf("event carries no timestamp")
			}
		})
	}
}

// A comment's body belongs to the comment kind alone. A state event
// carrying a body would render the wrong thing in the wrong place.
func TestConvertTimelineNode_OnlyCommentsCarryABody(t *testing.T) {
	ev, _ := convertTimelineNode(node(t, map[string]any{
		"__typename": "ClosedEvent", "id": "CE_1",
		"createdAt": "2026-09-01T10:00:00Z", "body": "should not appear",
	}))
	if ev.Body != "" {
		t.Fatalf("body = %q on a %s, want empty", ev.Body, ev.Kind)
	}
}

// An event with no timestamp sorts to the top of a chronological list
// and reads as the most recent thing that happened, so it is dropped
// rather than rendered at the zero time.
func TestConvertTimelineNode_DropsAnEventWithNoTimestamp(t *testing.T) {
	if _, ok := convertTimelineNode(node(t, map[string]any{
		"__typename": "ClosedEvent", "id": "CE_1",
	})); ok {
		t.Fatal("an event with no createdAt was kept")
	}
}

// A type this build did not ask for still renders, saying what it was.
// Dropping it silently would put a gap in a history whose whole value is
// that it has none.
func TestConvertTimelineNode_UnknownTypeRendersHonestly(t *testing.T) {
	ev, ok := convertTimelineNode(node(t, map[string]any{
		"__typename": "SubIssueAddedEvent", "id": "SA_1",
		"createdAt": "2026-09-01T10:00:00Z",
	}))
	if !ok {
		t.Fatal("an unfamiliar event was dropped")
	}
	if ev.Kind != core.ActivityOther {
		t.Errorf("kind = %q, want %q", ev.Kind, core.ActivityOther)
	}
	if ev.Summary != "sub issue added" {
		t.Errorf("summary = %q, want %q", ev.Summary, "sub issue added")
	}
}

func TestClampActivityLimit(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, defaultActivityPage},
		{-5, defaultActivityPage},
		{10, 10},
		{maxActivityPage, maxActivityPage},
		{maxActivityPage + 1, maxActivityPage},
		{100000, maxActivityPage},
	}
	for _, tc := range cases {
		if got := clampActivityLimit(tc.in); got != tc.want {
			t.Errorf("clampActivityLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// activityShim answers the timeline query with a fixed page. It records
// the arguments so the test can assert the client paged backwards.
func activityShim(t *testing.T, body string) (bin, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "gh")
	argsFile = filepath.Join(dir, "args")
	script := `#!/bin/sh
printf '%s\n' "$@" > ` + argsFile + `
cat <<'JSON'
` + body + `
JSON
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write gh shim: %v", err)
	}
	return bin, argsFile
}

const twoEventPage = `{"data":{"repository":{"issue":{"timelineItems":{
  "totalCount": 7,
  "pageInfo": {"hasPreviousPage": true, "startCursor": "CURSOR_OLDER"},
  "nodes": [
    {"__typename":"IssueComment","id":"IC_1","createdAt":"2026-09-01T09:00:00Z",
     "body":"first","author":{"login":"nic"},
     "url":"https://github.com/o/r/issues/1#issuecomment-1"},
    {"__typename":"ClosedEvent","id":"CE_1","createdAt":"2026-09-01T10:00:00Z",
     "actor":{"login":"bot"}}
  ]}}}}}`

func TestFetchActivity_PagesBackwardsAndReportsMoreHistory(t *testing.T) {
	bin, argsFile := activityShim(t, twoEventPage)
	cfg := SourceConfig{ID: "atab-group", Org: "Atab-Group", ProjectNumber: 1}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	client := NewGHClient(cfg).WithBinary(bin)

	page, err := client.FetchActivity(context.Background(),
		IssueRef{Owner: "Atab-Group", Repo: "ATAB-Marketplace", Number: 1},
		core.ActivityQuery{Limit: 2})
	if err != nil {
		t.Fatalf("FetchActivity: %v", err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(page.Events))
	}
	// Within a page the events keep GitHub's chronological order, so a
	// renderer can lay them out without re-sorting.
	if !page.Events[0].At.Before(page.Events[1].At) {
		t.Errorf("page is not in chronological order: %v then %v",
			page.Events[0].At, page.Events[1].At)
	}
	if !page.HasOlder {
		t.Error("has_older = false, but the connection reported a previous page")
	}
	// The two are separate answers on purpose: a page that reaches the
	// beginning of the history is the only one a reader may treat as
	// complete.
	if page.AtOldest {
		t.Error("at_oldest = true on a page that is not the beginning of the history")
	}
	if page.OlderCursor != "CURSOR_OLDER" {
		t.Errorf("older_cursor = %q, want %q", page.OlderCursor, "CURSOR_OLDER")
	}
	if page.Total != 7 {
		t.Errorf("total = %d, want the backend's 7 rather than the page length", page.Total)
	}

	// The cursor has to travel as `before` with `last`, or the client
	// would page forward from the oldest entry, which is the wrong end.
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read shim args: %v", err)
	}
	if !strings.Contains(string(args), "last:$n") ||
		!strings.Contains(string(args), "before:$before") {
		t.Errorf("the query does not page backwards:\n%s", args)
	}
}

func TestFetchActivity_ForwardsTheCursor(t *testing.T) {
	bin, argsFile := activityShim(t, twoEventPage)
	cfg := SourceConfig{ID: "atab-group", Org: "Atab-Group"}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	client := NewGHClient(cfg).WithBinary(bin)

	if _, err := client.FetchActivity(context.Background(),
		IssueRef{Owner: "Atab-Group", Repo: "r", Number: 3},
		core.ActivityQuery{Before: "CURSOR_OLDER", Limit: 5}); err != nil {
		t.Fatalf("FetchActivity: %v", err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read shim args: %v", err)
	}
	if !strings.Contains(string(args), "before=CURSOR_OLDER") {
		t.Errorf("the cursor was not sent:\n%s", args)
	}
	if !strings.Contains(string(args), "n=5") {
		t.Errorf("the page size was not sent:\n%s", args)
	}
}

// A page with no previous page is the beginning of the history, and is
// the only case a reader may read as complete.
func TestFetchActivity_ReportsTheBeginningOfHistory(t *testing.T) {
	bin, _ := activityShim(t, `{"data":{"repository":{"issue":{"timelineItems":{
  "totalCount": 1,
  "pageInfo": {"hasPreviousPage": false, "startCursor": "C0"},
  "nodes": [{"__typename":"IssueComment","id":"IC_1",
    "createdAt":"2026-09-01T09:00:00Z","body":"only","author":{"login":"nic"}}]}}}}}`)
	cfg := SourceConfig{ID: "atab-group", Org: "Atab-Group"}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	page, err := NewGHClient(cfg).WithBinary(bin).FetchActivity(context.Background(),
		IssueRef{Owner: "Atab-Group", Repo: "r", Number: 1}, core.ActivityQuery{})
	if err != nil {
		t.Fatalf("FetchActivity: %v", err)
	}
	if !page.AtOldest || page.HasOlder {
		t.Errorf("at_oldest = %v, has_older = %v; want true, false", page.AtOldest, page.HasOlder)
	}
	if page.OlderCursor != "" {
		t.Errorf("older_cursor = %q on a page with nothing older", page.OlderCursor)
	}
}

func TestFetchActivity_MissingIssueIsNotFound(t *testing.T) {
	bin, _ := activityShim(t, `{"data":{"repository":{"issue":null}}}`)
	cfg := SourceConfig{ID: "atab-group", Org: "Atab-Group"}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	_, err := NewGHClient(cfg).WithBinary(bin).FetchActivity(context.Background(),
		IssueRef{Owner: "Atab-Group", Repo: "r", Number: 9}, core.ActivityQuery{})
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("err = %v, want it to satisfy errors.Is(ErrNotFound)", err)
	}
}

// --- WorkPlane.ReadActivity -----------------------------------------

// activityFixtureClient is a fixture client that can also answer for
// history, which the plain fixture client deliberately cannot.
type activityFixtureClient struct {
	*FixtureClient
	page core.ActivityPage
	err  error
	last core.ActivityQuery
}

func (c *activityFixtureClient) FetchActivity(
	_ context.Context, _ IssueRef, q core.ActivityQuery,
) (core.ActivityPage, error) {
	c.last = q
	return c.page, c.err
}

func activityPlane(t *testing.T, client Client) *WorkPlane {
	t.Helper()
	cfg := SourceConfig{ID: "atab-group", Org: "Atab-Group", ProjectNumber: 1}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	reg := NewRegistry()
	if err := reg.Add(cfg, client); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return New(reg, core.TransportAPI)
}

// A well-formed id naming a source this deployment does not carry
// answers not-found with nothing beyond the id the caller already had.
// Saying "that source exists but you may not read it" would confirm the
// shape of a deployment they were never granted.
func TestReadActivity_UnknownSourceFailsClosed(t *testing.T) {
	plane := activityPlane(t, &activityFixtureClient{FixtureClient: NewFixtureClient("atab-group", nil)})
	_, err := plane.ReadActivity(context.Background(),
		"someotherorg/Other~Repo/1", core.ActivityQuery{})
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("err = %v, want not-found", err)
	}
	if strings.Contains(err.Error(), "atab-group") {
		t.Errorf("the error names a source the caller did not ask about: %v", err)
	}
}

// A client that cannot read history says so, rather than returning an
// empty page. An empty history and a backend with no history are
// different facts and a reader has to be able to tell them apart.
func TestReadActivity_ClientWithoutHistoryIsUnsupported(t *testing.T) {
	plane := activityPlane(t, NewFixtureClient("atab-group", nil))
	_, err := plane.ReadActivity(context.Background(),
		"atab-group/Atab-Group~ATAB-Marketplace/1", core.ActivityQuery{})
	ae := core.AsAdaptorError(err)
	if ae == nil || ae.Kind != core.KindUnsupported {
		t.Fatalf("err = %v, want a tagged %s", err, core.KindUnsupported)
	}
}

// The page is stamped with its source and with the surrounding board's
// freshness, so a reader looking at a live history beside a stale card
// can see which is which.
func TestReadActivity_StampsSourceAndFreshness(t *testing.T) {
	client := &activityFixtureClient{
		FixtureClient: NewFixtureClient("atab-group", nil),
		page: core.ActivityPage{
			Events:   []core.ActivityEvent{{ID: "IC_1", Kind: core.ActivityComment, At: time.Now().UTC()}},
			AtOldest: true,
		},
	}
	plane := activityPlane(t, client)
	page, err := plane.ReadActivity(context.Background(),
		"atab-group/Atab-Group~ATAB-Marketplace/1", core.ActivityQuery{})
	if err != nil {
		t.Fatalf("ReadActivity: %v", err)
	}
	if page.Source != "atab-group" {
		t.Errorf("source = %q, want %q", page.Source, "atab-group")
	}
	// Nothing has been read into this cache, so the board beside the
	// history is unknown rather than fresh.
	if page.Freshness != string(FreshnessUnknown) {
		t.Errorf("freshness = %q, want %q", page.Freshness, FreshnessUnknown)
	}
}

// An unbounded page request would spend the GraphQL budget the board
// needs, so the limit is clamped before it reaches the client.
func TestReadActivity_ClampsTheLimitBeforeTheClientSeesIt(t *testing.T) {
	client := &activityFixtureClient{FixtureClient: NewFixtureClient("atab-group", nil)}
	plane := activityPlane(t, client)
	if _, err := plane.ReadActivity(context.Background(),
		"atab-group/Atab-Group~ATAB-Marketplace/1",
		core.ActivityQuery{Limit: 100000}); err != nil {
		t.Fatalf("ReadActivity: %v", err)
	}
	if client.last.Limit != maxActivityPage {
		t.Errorf("client saw limit %d, want it clamped to %d", client.last.Limit, maxActivityPage)
	}
}
