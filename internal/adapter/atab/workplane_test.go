package atab

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GembaCore/gemba-core/core"
)

func demoWorkPlane(t *testing.T) *WorkPlane {
	t.Helper()
	reg := NewRegistry()
	if err := reg.Add(DemoSourceConfig(), NewFixtureClient(DemoSourceID, DemoIssues())); err != nil {
		t.Fatalf("add source: %v", err)
	}
	return New(reg, core.TransportAPI)
}

func TestWorkPlane_ManifestIsValidAndReadOnly(t *testing.T) {
	m, err := demoWorkPlane(t).Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("manifest is not valid: %v", err)
	}
	if !m.ReadOnly {
		t.Fatal("the manifest must declare read-only so the SPA hides write controls")
	}
	if m.AdaptorName != AdaptorName {
		t.Errorf("adaptor name = %q", m.AdaptorName)
	}
	if m.ProtocolVersion != core.ProtocolVersion {
		t.Errorf("protocol = %q, want the core's %q", m.ProtocolVersion, core.ProtocolVersion)
	}
	if ok, reasons := m.MinimumBar(); !ok {
		t.Errorf("manifest is below the agentic data-plane bar: %v", reasons)
	}
}

func TestWorkPlane_ManifestRoundTripsThroughJSON(t *testing.T) {
	m := Manifest(core.TransportAPI)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back core.CapabilityManifest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.AdaptorName != m.AdaptorName || back.ReadOnly != m.ReadOnly ||
		len(back.StateMap) != len(m.StateMap) || len(back.FieldExtensions) != len(m.FieldExtensions) {
		t.Fatalf("manifest did not survive a JSON round trip:\n got %+v\nwant %+v", back, m)
	}
}

// Every mutation is refused with a tagged read_only error. GitHub and
// the org board stay canonical; a second writer would create exactly the
// drift the dashboard exists to remove.
func TestWorkPlane_MutationsAreRefused(t *testing.T) {
	wp := demoWorkPlane(t)
	ctx := context.Background()

	checkReadOnly := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s was accepted; the adaptor must be read-only", name)
		}
		ae := core.AsAdaptorError(err)
		if ae == nil {
			t.Fatalf("%s error is untagged: %v", name, err)
		}
		if ae.Kind != core.KindReadOnly {
			t.Errorf("%s error kind = %q, want read_only", name, ae.Kind)
		}
		if ae.Retryable {
			t.Errorf("%s error is marked retryable; retrying a refused write cannot help", name)
		}
	}

	_, err := wp.CreateWorkItem(ctx, core.WorkItem{Title: "nope"})
	checkReadOnly("CreateWorkItem", err)

	_, err = wp.UpdateWorkItem(ctx, "atab-group/Atab-Group~Product-Seela/301", core.WorkItemPatch{})
	checkReadOnly("UpdateWorkItem", err)
}

// AttachEvidence is denied by the manifest rather than by the read-only
// mode. The adaptor synthesises its own evidence, so attach_evidence is
// an op it opts out of, and the contract wants that spelled
// capability_denied.
func TestWorkPlane_AttachEvidenceIsCapabilityDenied(t *testing.T) {
	err := demoWorkPlane(t).AttachEvidence(
		context.Background(), "atab-group/Atab-Group~Product-Seela/301", core.Evidence{})
	if err == nil {
		t.Fatal("AttachEvidence must be refused")
	}
	if err := core.AssertCapabilityDenied(err); err != nil {
		t.Fatalf("AttachEvidence: %v", err)
	}
}

// Sprints and budgets are gated off by the manifest, so the guard has to
// answer capability_denied rather than read_only: the distinction is what
// lets the SPA tell "manifest said no" from "adaptor chose not to".
func TestWorkPlane_GatedCapabilitiesAreDenied(t *testing.T) {
	wp := demoWorkPlane(t)
	ctx := context.Background()

	if _, err := wp.ListSprints(ctx); err == nil {
		t.Error("ListSprints must be denied on a non-sprint adaptor")
	} else if err := core.AssertCapabilityDenied(err); err != nil {
		t.Errorf("ListSprints: %v", err)
	}

	if _, err := wp.ReadBudgetRollup(ctx, "sprint-1"); err == nil {
		t.Error("ReadBudgetRollup must be denied on a non-budget adaptor")
	} else if err := core.AssertCapabilityDenied(err); err != nil {
		t.Errorf("ReadBudgetRollup: %v", err)
	}
}

// Subscribe reports unsupported, which the server's hub pump treats as
// "no events from this plane" and drops silently.
func TestWorkPlane_SubscribeIsUnsupported(t *testing.T) {
	_, err := demoWorkPlane(t).Subscribe(context.Background(), core.WorkPlaneSubscribeFilter{})
	if err == nil {
		t.Fatal("a read-only projection has no mutation to announce")
	}
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("error = %v, want it to satisfy errors.Is(ErrUnsupported)", err)
	}
}

func TestWorkPlane_GetWorkItem(t *testing.T) {
	wp := demoWorkPlane(t)
	it, err := wp.GetWorkItem(context.Background(), "atab-group/Atab-Group~Product-Seela/301")
	if err != nil {
		t.Fatalf("GetWorkItem: %v", err)
	}
	if it.Title != "Show a price range on the Services card" {
		t.Errorf("title = %q", it.Title)
	}
}

func TestWorkPlane_GetWorkItem_NotFoundIsTaggedAndMatchesSentinel(t *testing.T) {
	wp := demoWorkPlane(t)
	_, err := wp.GetWorkItem(context.Background(), "atab-group/Atab-Group~Product-Seela/999999")
	if err == nil {
		t.Fatal("expected not found")
	}
	if ae := core.AsAdaptorError(err); ae == nil {
		t.Fatalf("error is untagged: %v", err)
	}
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("error = %v, want it to satisfy errors.Is(ErrNotFound)", err)
	}
}

func TestWorkPlane_GetWorkItem_MalformedIDIsValidation(t *testing.T) {
	_, err := demoWorkPlane(t).GetWorkItem(context.Background(), "not-an-id")
	ae := core.AsAdaptorError(err)
	if ae == nil || ae.Kind != core.KindValidation {
		t.Fatalf("error = %v, want a tagged validation error", err)
	}
}

func TestWorkPlane_ListFilters(t *testing.T) {
	wp := demoWorkPlane(t)
	ctx := context.Background()

	all, err := wp.ListWorkItems(ctx, core.WorkItemFilter{})
	if err != nil {
		t.Fatalf("ListWorkItems: %v", err)
	}
	if len(all) != len(DemoIssues()) {
		t.Fatalf("unfiltered list = %d, want %d", len(all), len(DemoIssues()))
	}

	byStatus, err := wp.ListWorkItems(ctx, core.WorkItemFilter{Statuses: []string{StatusTodo}})
	if err != nil {
		t.Fatal(err)
	}
	if len(byStatus) == 0 || len(byStatus) >= len(all) {
		t.Fatalf("status filter returned %d of %d", len(byStatus), len(all))
	}
	for _, it := range byStatus {
		if it.Status != StatusTodo {
			t.Errorf("item %q status = %q, want Todo", it.ID, it.Status)
		}
	}

	byCat, err := wp.ListWorkItems(ctx, core.WorkItemFilter{
		StateCategory: []core.StateCategory{core.StateCompleted, core.StateCanceled},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range byCat {
		if it.StateCategory != core.StateCompleted && it.StateCategory != core.StateCanceled {
			t.Errorf("item %q category = %q", it.ID, it.StateCategory)
		}
	}

	byLabel, err := wp.ListWorkItems(ctx, core.WorkItemFilter{Labels: []string{"needs-human"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(byLabel) != 1 {
		t.Fatalf("label filter returned %d, want 1", len(byLabel))
	}

	limited, err := wp.ListWorkItems(ctx, core.WorkItemFilter{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 3 {
		t.Fatalf("limit 3 returned %d", len(limited))
	}

	since := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	recent, err := wp.ListWorkItems(ctx, core.WorkItemFilter{UpdatedSince: &since})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range recent {
		if it.UpdatedAt.Before(since) {
			t.Errorf("item %q updated %v, before the watermark", it.ID, it.UpdatedAt)
		}
	}
}

// No sprint is native here, so a sprint filter matches nothing rather
// than quietly matching everything.
func TestWorkPlane_SprintFilterMatchesNothing(t *testing.T) {
	sprint := "s-1"
	got, err := demoWorkPlane(t).ListWorkItems(
		context.Background(), core.WorkItemFilter{SprintID: &sprint})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("sprint filter returned %d items on an adaptor with no sprints", len(got))
	}
}

func TestWorkPlane_AssigneeFilter(t *testing.T) {
	want := AgentIDFor(DemoSourceID, "niclom12")
	got, err := demoWorkPlane(t).ListWorkItems(
		context.Background(), core.WorkItemFilter{AssigneeID: &want})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("assignee filter returned %d, want the 2 assigned fixtures", len(got))
	}
	for _, it := range got {
		if it.Assignee == nil || it.Assignee.ID != want {
			t.Errorf("item %q assignee = %+v", it.ID, it.Assignee)
		}
	}
}

// A source whose reads have all failed is not the same as a source
// nothing has got to yet, and the probe must not report the first as the
// second. It used to: the never-read branch was decided on the absence
// of a success alone, so a credential that could read nothing at all sat
// behind a green health check for as long as the process ran.
func TestProbe_ASourceWhoseFirstReadFailedIsNotHealthy(t *testing.T) {
	reg := NewRegistry()
	client := NewFixtureClient(DemoSourceID, DemoIssues())
	if err := reg.Add(DemoSourceConfig(), client); err != nil {
		t.Fatalf("add: %v", err)
	}
	wp := New(reg, core.TransportAPI)

	// Nothing has been read yet, and nothing has failed yet.
	ok, reason := wp.Probe(t.Context())
	if !ok {
		t.Errorf("a source nobody has read yet reports unhealthy: %s", reason)
	}
	if !strings.Contains(reason, "not yet read") {
		t.Errorf("reason = %q, want it to say the source has not been read", reason)
	}

	client.SetFailure(core.NewAdaptorError(core.KindRateLimited, "throttled"))
	if err := reg.RefreshSource(t.Context(), DemoSourceID); err == nil {
		t.Fatal("the read was meant to fail")
	}

	ok, reason = wp.Probe(t.Context())
	if ok {
		t.Errorf("a source holding nothing after a failed read reports healthy: %q", reason)
	}
	if !strings.Contains(reason, string(DemoSourceID)) {
		t.Errorf("reason = %q, want it to name %q", reason, DemoSourceID)
	}

	// And it recovers once a read lands.
	client.SetFailure(nil)
	if err := reg.RefreshSource(t.Context(), DemoSourceID); err != nil {
		t.Fatalf("recovery read: %v", err)
	}
	if ok, reason := wp.Probe(t.Context()); !ok {
		t.Errorf("still unhealthy after a clean read: %s", reason)
	}
}
