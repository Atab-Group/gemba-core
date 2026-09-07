package atab

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/GembaCore/gemba-core/core"
)

func twoSourceRegistry(t *testing.T) (*Registry, *FixtureClient, *FixtureClient) {
	t.Helper()
	primary := NewFixtureClient(DemoSourceID, DemoIssues())
	secondary := NewFixtureClient(SecondaryDemoSourceID, SecondaryDemoIssues())

	reg := NewRegistry()
	if err := reg.Add(DemoSourceConfig(), primary); err != nil {
		t.Fatalf("add primary: %v", err)
	}
	if err := reg.Add(SecondaryDemoSourceConfig(), secondary); err != nil {
		t.Fatalf("add secondary: %v", err)
	}
	return reg, primary, secondary
}

// Membership is closed. A source reaches the board only by being written
// down, and there is no discovery path that could add one.
func TestRegistry_AllowlistIsExplicit(t *testing.T) {
	reg, _, _ := twoSourceRegistry(t)
	if !reg.Allowed(DemoSourceID) || !reg.Allowed(SecondaryDemoSourceID) {
		t.Fatal("both configured sources must be allowlisted")
	}
	if reg.Allowed("some-other-org") {
		t.Fatal("an unconfigured source must not be allowed")
	}
}

func TestRegistry_RejectsDuplicateAndMismatchedSources(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Add(DemoSourceConfig(), NewFixtureClient(DemoSourceID, nil)); err != nil {
		t.Fatalf("first add: %v", err)
	}
	// A duplicate id would make the id prefix stop being unique, which
	// is the property the whole scheme rests on.
	if err := reg.Add(DemoSourceConfig(), NewFixtureClient(DemoSourceID, nil)); err == nil {
		t.Error("a duplicate source id must be refused")
	}
	// A client bound to a different source would file items under the
	// wrong prefix.
	cfg := SecondaryDemoSourceConfig()
	if err := reg.Add(cfg, NewFixtureClient(DemoSourceID, nil)); err == nil {
		t.Error("a client whose source id does not match its config must be refused")
	}
	if err := reg.Add(cfg, nil); err == nil {
		t.Error("a source with no client must be refused")
	}
}

// Two sources carrying Product-Seela#301 must both appear, under
// distinct ids. This is the collision the qualified id scheme exists to
// survive, so it is exercised rather than assumed.
func TestRegistry_IDsStayCollisionSafeAcrossSources(t *testing.T) {
	reg, _, _ := twoSourceRegistry(t)
	wp := New(reg, core.TransportAPI)

	items, err := wp.ListWorkItems(context.Background(), core.WorkItemFilter{})
	if err != nil {
		t.Fatalf("ListWorkItems: %v", err)
	}
	seen := map[core.WorkItemID]int{}
	for _, it := range items {
		seen[it.ID]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("id %q appeared %d times", id, n)
		}
	}
	for _, want := range []core.WorkItemID{
		"atab-group/Atab-Group~Product-Seela/301",
		"partner-org/Partner-Org~Product-Seela/301",
	} {
		if seen[want] != 1 {
			t.Errorf("expected exactly one %q, got %d", want, seen[want])
		}
	}
}

// One unreadable source must not take down a healthy one. The healthy
// source keeps returning its items and the failure is recorded against
// the source that failed.
func TestRegistry_OneBrokenSourceDoesNotBreakTheOthers(t *testing.T) {
	reg, _, secondary := twoSourceRegistry(t)
	wp := New(reg, core.TransportAPI)
	ctx := context.Background()

	secondary.SetFailure(core.NewAdaptorError(core.KindCapabilityDenied,
		"this credential is not authorised for the requested source"))

	items, err := wp.ListWorkItems(ctx, core.WorkItemFilter{})
	if err != nil {
		t.Fatalf("ListWorkItems: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("the healthy source returned nothing")
	}
	for _, it := range items {
		if strings.HasPrefix(string(it.ID), string(SecondaryDemoSourceID)+"/") {
			t.Fatalf("item %q leaked from the unreadable source", it.ID)
		}
	}

	healthy, reason := reg.Healthy()
	if healthy {
		t.Error("the registry must report itself degraded while a source is unreadable")
	}
	if !strings.Contains(reason, string(SecondaryDemoSourceID)) {
		t.Errorf("reason = %q, want it to name the failing source", reason)
	}
}

// A source that cannot be read contributes nothing beyond its id and a
// condition string. Nothing about its contents may reach a caller.
func TestRegistry_InaccessibleSourceLeaksNoMetadata(t *testing.T) {
	reg, _, secondary := twoSourceRegistry(t)
	ctx := context.Background()

	// Prime nothing: the source fails on its very first read, so the
	// registry has never seen its contents.
	secondary.SetFailure(core.NewAdaptorError(core.KindCapabilityDenied,
		"this credential is not authorised for the requested source"))

	for _, h := range reg.Health() {
		if h.Source != SecondaryDemoSourceID {
			continue
		}
		if h.Items != 0 {
			t.Errorf("health reports %d items for an unreadable source", h.Items)
		}
		for _, secret := range []string{"Partner-Org", "reconcile the nightly export", "PVTI_"} {
			if strings.Contains(h.Reason, secret) {
				t.Errorf("health reason leaked %q: %s", secret, h.Reason)
			}
		}
	}

	// A well-formed id into the unreadable source must not confirm or
	// deny anything about it beyond the failure to read.
	_, err := reg.getWorkItem(ctx, "partner-org/Partner-Org~Product-Seela/301")
	if err == nil {
		t.Fatal("reading into an unreadable source must fail")
	}
	if strings.Contains(err.Error(), "reconcile the nightly export") {
		t.Errorf("error leaked issue content: %v", err)
	}
}

// An id naming a source this process does not carry is simply not found.
// Saying anything more would describe a deployment the caller was not
// granted.
func TestRegistry_UnknownSourceIsPlainNotFound(t *testing.T) {
	reg, _, _ := twoSourceRegistry(t)
	_, err := reg.getWorkItem(context.Background(), "not-configured/Some-Org~Repo/1")
	if err == nil {
		t.Fatal("expected an error")
	}
	if ae := core.AsAdaptorError(err); ae == nil || ae.Kind != core.KindSessionNotFound {
		t.Fatalf("error = %v, want a tagged session_not_found", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "not configured") ||
		strings.Contains(strings.ToLower(err.Error()), "unreachable") {
		t.Errorf("error distinguishes an unconfigured source from a missing item: %v", err)
	}
}

// An edge into a source this deployment cannot resolve holds the
// dependent work. Assuming it clear would let an unreachable source
// silently unblock a queue.
func TestRegistry_CrossSourceDependencyFailsClosed(t *testing.T) {
	reg, _, _ := twoSourceRegistry(t)
	wp := New(reg, core.TransportAPI)

	items, err := wp.ListWorkItems(context.Background(), core.WorkItemFilter{
		IDs: []core.WorkItemID{"atab-group/Atab-Group~Product-Seela/303"},
	})
	if err != nil {
		t.Fatalf("ListWorkItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if got := items[0].Custom[FieldKeyReadiness]; got != string(ReadyBlocked) {
		t.Fatalf("readiness = %v, want blocked; the cross-repo target is unresolvable", got)
	}
	reason, _ := items[0].Custom[FieldKeyReadyReason].(string)
	if !strings.Contains(reason, "cannot read") {
		t.Errorf("reason = %q, want it to say the edge is held rather than assumed clear", reason)
	}
}

// A reference-authority source shows its own issues but never holds
// another source's work. Only a canonical source resolves a foreign edge.
func TestRegistry_ReferenceAuthorityDoesNotResolveForeignEdges(t *testing.T) {
	primary := NewFixtureClient(DemoSourceID, []Issue{{
		Ref:   IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 500},
		Title: "Depends on the partner board",
		Body: "## Acceptance Criteria\n\n- [ ] done\n" +
			"\n<!-- atab-meta: x -->\n```yaml\ntype: task\nautonomy: pr\n" +
			"blocked_by:\n  - \"Atab-Group/Partner#302\"\n```\n",
		State:     "OPEN",
		Labels:    []string{"type:task"},
		UpdatedAt: testNow,
	}})
	// The blocker exists, closed, inside the second source.
	secondary := NewFixtureClient(SecondaryDemoSourceID, []Issue{{
		Ref:         IssueRef{Owner: "Atab-Group", Repo: "Partner", Number: 302},
		Title:       "Closed upstream",
		State:       "CLOSED",
		StateReason: "COMPLETED",
		UpdatedAt:   testNow,
	}})

	build := func(secondaryAuthority Authority) *Registry {
		reg := NewRegistry()
		primaryCfg := DemoSourceConfig()
		primaryCfg.Repos = []string{"Product-Seela"}
		if err := reg.Add(primaryCfg, primary); err != nil {
			t.Fatalf("add primary: %v", err)
		}
		secCfg := SecondaryDemoSourceConfig()
		secCfg.Org = "Atab-Group"
		secCfg.Repos = []string{"Partner"}
		secCfg.Authority = secondaryAuthority
		if err := reg.Add(secCfg, secondary); err != nil {
			t.Fatalf("add secondary: %v", err)
		}
		return reg
	}

	ctx := context.Background()
	id := core.WorkItemID("atab-group/Atab-Group~Product-Seela/500")

	refItem, err := build(AuthorityReference).getWorkItem(ctx, id)
	if err != nil {
		t.Fatalf("reference read: %v", err)
	}
	if got := refItem.Custom[FieldKeyReadiness]; got != string(ReadyBlocked) {
		t.Errorf("readiness against a reference source = %v, want blocked", got)
	}

	canonItem, err := build(AuthorityCanonical).getWorkItem(ctx, id)
	if err != nil {
		t.Fatalf("canonical read: %v", err)
	}
	if got := canonItem.Custom[FieldKeyReadiness]; got != string(ReadyReady) {
		t.Errorf("readiness against a canonical source = %v (%v), want ready",
			got, canonItem.Custom[FieldKeyReadyReason])
	}
}

// Every source failing is a real error, not an empty board. An empty
// list would render as "no work", which is the one wrong answer.
func TestWorkPlane_AllSourcesDownIsAnError(t *testing.T) {
	reg, primary, secondary := twoSourceRegistry(t)
	down := core.NewAdaptorError(core.KindRequestFailed, "github is unreachable")
	primary.SetFailure(down)
	secondary.SetFailure(down)

	if _, err := New(reg, core.TransportAPI).ListWorkItems(
		context.Background(), core.WorkItemFilter{}); err == nil {
		t.Fatal("a total outage must surface as an error, never as an empty board")
	}
}

func TestRegistry_RefreshReportsPerSourceErrors(t *testing.T) {
	reg, _, secondary := twoSourceRegistry(t)
	secondary.SetFailure(core.NewAdaptorError(core.KindRequestFailed, "down"))

	errs := reg.Refresh(context.Background())
	if _, bad := errs[SecondaryDemoSourceID]; !bad {
		t.Error("the failing source is missing from the error map")
	}
	if _, bad := errs[DemoSourceID]; bad {
		t.Error("the healthy source must not be reported as failing")
	}
}

func TestWorkPlane_ProbeReportsDegradedSources(t *testing.T) {
	reg, _, secondary := twoSourceRegistry(t)
	wp := New(reg, core.TransportAPI)
	ctx := context.Background()

	if _, err := wp.ListWorkItems(ctx, core.WorkItemFilter{}); err != nil {
		t.Fatal(err)
	}
	if ok, reason := wp.Probe(ctx); !ok {
		t.Fatalf("probe = false (%s), want healthy", reason)
	}

	secondary.SetFailure(core.NewAdaptorError(core.KindRequestFailed, "down"))
	if err := reg.Refresh(ctx); len(err) == 0 {
		t.Fatal("expected the forced refresh to fail for the broken source")
	}
	ok, reason := wp.Probe(ctx)
	if ok {
		t.Fatal("probe must report degraded while a source is unreadable")
	}
	if !strings.Contains(reason, string(SecondaryDemoSourceID)) {
		t.Errorf("probe reason = %q, want it to name the source", reason)
	}
}

func TestRegistry_EmptyRegistryProbesUnhealthy(t *testing.T) {
	wp := New(NewRegistry(), core.TransportAPI)
	if ok, reason := wp.Probe(context.Background()); ok || reason == "" {
		t.Fatalf("probe = %v %q, want an unhealthy verdict with a reason", ok, reason)
	}
}

func TestRegistry_SourcesAndConfigAreReadable(t *testing.T) {
	reg, _, _ := twoSourceRegistry(t)
	got := reg.Sources()
	if len(got) != 2 || got[0] != DemoSourceID || got[1] != SecondaryDemoSourceID {
		t.Fatalf("sources = %v, want registration order", got)
	}
	cfg, ok := reg.Config(DemoSourceID)
	if !ok || cfg.ProjectNumber != 1 {
		t.Fatalf("config = %+v %v", cfg, ok)
	}
	if _, ok := reg.Config("nope"); ok {
		t.Error("an unconfigured source must not resolve")
	}
}

func TestRegistry_HealthFreshnessTracksTheBudget(t *testing.T) {
	reg, _, _ := twoSourceRegistry(t)
	ctx := context.Background()
	if err := reg.Refresh(ctx); len(err) != 0 {
		t.Fatalf("refresh: %v", err)
	}
	for _, h := range reg.Health() {
		if h.Freshness != FreshnessFresh {
			t.Errorf("source %q freshness = %q immediately after a refresh, want fresh",
				h.Source, h.Freshness)
		}
		if h.LastSuccess.IsZero() || time.Since(h.LastSuccess) > time.Minute {
			t.Errorf("source %q last success = %v", h.Source, h.LastSuccess)
		}
	}
}
