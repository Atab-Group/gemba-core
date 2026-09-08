package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GembaCore/gemba-core/core"
	"github.com/GembaCore/gemba-core/internal/config"
	"github.com/GembaCore/gemba-core/internal/transport/api"
	"github.com/GembaCore/gemba-core/internal/transport/testadaptors"
)

// activityPlane is a fake WorkPlane that also implements the optional
// core.ActivityReader surface.
type activityPlane struct {
	*testadaptors.FakeWorkPlane
	page core.ActivityPage
	err  error
	last core.ActivityQuery
}

func (p *activityPlane) ReadActivity(
	_ context.Context, _ core.WorkItemID, q core.ActivityQuery,
) (core.ActivityPage, error) {
	p.last = q
	return p.page, p.err
}

func activityRouter(t *testing.T, plane core.WorkPlane) http.Handler {
	t.Helper()
	host := api.New()
	if _, err := host.RegisterWorkPlane(context.Background(), plane); err != nil {
		t.Fatalf("RegisterWorkPlane: %v", err)
	}
	return NewRouter(config.ServeConfig{}, fakeSPA(), host)
}

func getActivity(t *testing.T, h http.Handler, path string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// A plane that keeps no history says so. Answering 200 with an empty
// page would tell a reader the item has no history, which is a different
// fact and not one this deployment knows.
func TestWorkItemActivity_UnsupportedPlaneIs501(t *testing.T) {
	h := activityRouter(t, testadaptors.NewFakeWorkPlane(core.TransportAPI))
	code, body := getActivity(t, h, "/api/work-items/gm-foo/activity")
	if code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", code, body)
	}
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error != "unsupported" {
		t.Errorf("error = %q, want %q", env.Error, "unsupported")
	}
}

func TestWorkItemActivity_ReturnsThePage(t *testing.T) {
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	plane := &activityPlane{
		FakeWorkPlane: testadaptors.NewFakeWorkPlane(core.TransportAPI),
		page: core.ActivityPage{
			Events: []core.ActivityEvent{
				{ID: "IC_1", Kind: core.ActivityComment, Actor: "nic", At: at, Body: "hello"},
				{ID: "CE_1", Kind: core.ActivityClosed, Actor: "bot", At: at, Summary: "closed this"},
			},
			OlderCursor: "CURSOR",
			HasOlder:    true,
			Total:       9,
			Source:      "atab-group",
			Freshness:   "fresh",
		},
	}
	h := activityRouter(t, plane)
	code, body := getActivity(t, h, "/api/work-items/atab-group%2FAtab-Group~Repo%2F1/activity")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", code, body)
	}
	var got core.ActivityPage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(got.Events))
	}
	if got.Events[0].Body != "hello" || got.Events[1].Summary != "closed this" {
		t.Errorf("events did not survive the wire: %+v", got.Events)
	}
	// has_older and at_oldest are separate answers, and both have to
	// reach the renderer: a bounded page must never read as complete.
	if !got.HasOlder || got.AtOldest {
		t.Errorf("has_older = %v, at_oldest = %v; want true, false", got.HasOlder, got.AtOldest)
	}
	if got.OlderCursor != "CURSOR" || got.Total != 9 {
		t.Errorf("cursor = %q, total = %d", got.OlderCursor, got.Total)
	}
}

// An item with nothing in its history returns an empty array, never
// null: a renderer branching on null is a bug waiting for the first
// item nobody has touched.
func TestWorkItemActivity_EmptyHistoryIsAnArray(t *testing.T) {
	plane := &activityPlane{FakeWorkPlane: testadaptors.NewFakeWorkPlane(core.TransportAPI)}
	h := activityRouter(t, plane)
	code, body := getActivity(t, h, "/api/work-items/gm-foo/activity")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", code, body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(raw["events"]) != "[]" {
		t.Errorf("events = %s, want []", raw["events"])
	}
}

func TestWorkItemActivity_ForwardsCursorAndLimit(t *testing.T) {
	plane := &activityPlane{FakeWorkPlane: testadaptors.NewFakeWorkPlane(core.TransportAPI)}
	h := activityRouter(t, plane)
	if code, body := getActivity(t, h,
		"/api/work-items/gm-foo/activity?before=CURSOR&limit=7"); code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	if plane.last.Before != "CURSOR" || plane.last.Limit != 7 {
		t.Errorf("plane saw %+v, want before=CURSOR limit=7", plane.last)
	}
}

func TestWorkItemActivity_ClampsAnAbsurdLimit(t *testing.T) {
	plane := &activityPlane{FakeWorkPlane: testadaptors.NewFakeWorkPlane(core.TransportAPI)}
	h := activityRouter(t, plane)
	if code, _ := getActivity(t, h, "/api/work-items/gm-foo/activity?limit=100000"); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if plane.last.Limit != maxActivityLimit {
		t.Errorf("plane saw limit %d, want it clamped to %d", plane.last.Limit, maxActivityLimit)
	}
}

func TestWorkItemActivity_RejectsANonPositiveLimit(t *testing.T) {
	plane := &activityPlane{FakeWorkPlane: testadaptors.NewFakeWorkPlane(core.TransportAPI)}
	h := activityRouter(t, plane)
	for _, v := range []string{"0", "-1", "many"} {
		if code, _ := getActivity(t, h, "/api/work-items/gm-foo/activity?limit="+v); code != http.StatusBadRequest {
			t.Errorf("limit=%s: status = %d, want 400", v, code)
		}
	}
}

// A throttled backend has to surface as a throttle. Rendering it as an
// empty history would tell a reader the item has no activity when the
// truth is that GitHub would not answer.
func TestWorkItemActivity_BackendFailureIsNotAnEmptyHistory(t *testing.T) {
	plane := &activityPlane{
		FakeWorkPlane: testadaptors.NewFakeWorkPlane(core.TransportAPI),
		err:           core.NewAdaptorError(core.KindRateLimited, "atab: GitHub throttled the request"),
	}
	h := activityRouter(t, plane)
	code, body := getActivity(t, h, "/api/work-items/gm-foo/activity")
	if code == http.StatusOK {
		t.Fatalf("a throttled backend returned 200: %s", body)
	}
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error != string(core.KindRateLimited) {
		t.Errorf("error = %q, want %q", env.Error, core.KindRateLimited)
	}
}

func TestWorkItemActivity_NotFoundIs404(t *testing.T) {
	plane := &activityPlane{
		FakeWorkPlane: testadaptors.NewFakeWorkPlane(core.TransportAPI),
		err: core.WrapAdaptorError(core.KindSessionNotFound, core.ErrNotFound,
			"atab: no work item %q", "gm-foo"),
	}
	h := activityRouter(t, plane)
	if code, body := getActivity(t, h, "/api/work-items/gm-foo/activity"); code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", code, body)
	}
}
