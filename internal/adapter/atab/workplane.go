package atab

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/GembaCore/gemba-core/core"
)

// WorkPlane is the read-only core.WorkPlane over one or more federated
// GitHub / ATAB sources.
//
// Every mutation method returns KindReadOnly. That is not a placeholder
// for write support arriving later: GitHub and the org's project board
// are canonical, and a second writer would create exactly the drift the
// board exists to remove.
type WorkPlane struct {
	registry  *Registry
	transport core.Transport
}

var _ core.WorkPlane = (*WorkPlane)(nil)

// New returns a WorkPlane reading through registry on the given
// transport.
func New(registry *Registry, transport core.Transport) *WorkPlane {
	return &WorkPlane{registry: registry, transport: transport}
}

// Registry exposes the federated source set for the health surface.
func (w *WorkPlane) Registry() *Registry { return w.registry }

// Describe returns the adaptor manifest. Idempotent and side-effect
// free: it reads no source and touches no network.
func (w *WorkPlane) Describe(context.Context) (core.CapabilityManifest, error) {
	return Manifest(w.transport), nil
}

// ListWorkItems projects every allowlisted source and applies filter.
//
// A source that cannot be read contributes nothing and is logged against
// its own id. The healthy sources still return their items, so one
// unreachable org does not blank the board.
func (w *WorkPlane) ListWorkItems(
	ctx context.Context, filter core.WorkItemFilter,
) ([]core.WorkItem, error) {
	results := w.registry.projectAll(ctx)

	var items []core.WorkItem
	var failed int
	for _, res := range results {
		if res.err != nil {
			failed++
			slog.Warn("atab: source unreadable; its items are withheld from this list",
				"source", res.source, "err", res.err)
			continue
		}
		items = append(items, res.items...)
	}
	// Every configured source failed and none has a usable snapshot:
	// that is a real error, not an empty board. Returning an empty slice
	// here would render as "no work", which is the one wrong answer.
	if failed > 0 && len(items) == 0 && failed == len(results) {
		return nil, core.NewAdaptorError(core.KindAdaptorDegraded,
			"atab: all %d configured sources are unreadable", failed)
	}

	items = applyFilter(items, filter)
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items, nil
}

// GetWorkItem resolves one qualified work item id.
func (w *WorkPlane) GetWorkItem(ctx context.Context, id core.WorkItemID) (core.WorkItem, error) {
	return w.registry.getWorkItem(ctx, id)
}

// CreateWorkItem is refused. GitHub is canonical; file the issue there.
func (w *WorkPlane) CreateWorkItem(context.Context, core.WorkItem) (core.WorkItem, error) {
	return core.WorkItem{}, w.readOnly("create_work_item")
}

// UpdateWorkItem is refused. GitHub is canonical; edit the issue there.
func (w *WorkPlane) UpdateWorkItem(
	context.Context, core.WorkItemID, core.WorkItemPatch,
) (core.WorkItem, error) {
	return core.WorkItem{}, w.readOnly("update_work_item")
}

// AttachEvidence is refused as a denied capability rather than as a
// read-only write.
//
// The manifest sets evidence_synthesis_required=false because the
// adaptor builds its own evidence from pull requests, local-CI reports,
// verifier verdicts and check runs. That makes attach_evidence an op the
// manifest opts out of, and the contract requires a manifest opt-out to
// answer capability_denied so the SPA can tell "manifest said no" from
// "adaptor chose not to". The adaptor being read-only is also true, and
// is the weaker of the two statements.
func (w *WorkPlane) AttachEvidence(context.Context, core.WorkItemID, core.Evidence) error {
	return core.EnforceCapability(Manifest(w.transport), core.OpAttachEvidence)
}

// ListSprints returns no sprints. The org tracks iteration on the
// project board rather than as first-class sprint records, so the
// manifest sets sprint_native=false and the UI hides sprint chrome.
func (w *WorkPlane) ListSprints(context.Context) ([]core.Sprint, error) {
	return nil, core.EnforceCapability(Manifest(w.transport), core.OpListSprints)
}

// ReadBudgetRollup is gated off with sprints.
func (w *WorkPlane) ReadBudgetRollup(context.Context, string) (core.BudgetRollup, error) {
	return core.BudgetRollup{}, core.EnforceCapability(
		Manifest(w.transport), core.OpReadBudgetRollup)
}

// Subscribe reports that this adaptor emits no events.
//
// A read-only projection has no mutation to announce, and polling GitHub
// to synthesise change events would be a different component with a
// different rate-limit budget. The server's hub pump treats
// KindUnsupported as "no events from this plane" and drops it silently.
func (w *WorkPlane) Subscribe(
	context.Context, core.WorkPlaneSubscribeFilter,
) (<-chan core.WorkPlaneEvent, error) {
	return nil, core.NewAdaptorError(core.KindUnsupported,
		"atab: read-only projection emits no work-plane events")
}

func (w *WorkPlane) readOnly(op string) error {
	return core.NewAdaptorError(core.KindReadOnly,
		"atab: %s is refused — GitHub and the org project board are canonical "+
			"and this adaptor is a read-only projection", op)
}

// applyFilter narrows items by the core filter. Predicates intersect;
// a zero-valued field is not a filter.
//
// GitHub cannot express most of this server-side across a federated set,
// so it runs in process against the snapshot. The snapshot is already
// bounded by the source config, so the cost is bounded with it.
func applyFilter(items []core.WorkItem, f core.WorkItemFilter) []core.WorkItem {
	if isZeroFilter(f) {
		return items
	}
	ids := toSet(stringsOf(f.IDs))
	kinds := toSet(f.Kinds)
	statuses := toSet(f.Statuses)
	cats := make(map[core.StateCategory]struct{}, len(f.StateCategory))
	for _, c := range f.StateCategory {
		cats[c] = struct{}{}
	}
	labels := toSet(f.Labels)

	out := items[:0:0]
	for _, it := range items {
		if len(ids) > 0 {
			if _, ok := ids[string(it.ID)]; !ok {
				continue
			}
		}
		if len(kinds) > 0 {
			if _, ok := kinds[it.Kind]; !ok {
				continue
			}
		}
		if len(statuses) > 0 {
			if _, ok := statuses[it.Status]; !ok {
				continue
			}
		}
		if len(cats) > 0 {
			if _, ok := cats[it.StateCategory]; !ok {
				continue
			}
		}
		if len(labels) > 0 && !hasAnyLabel(it.Labels, labels) {
			continue
		}
		if f.AssigneeID != nil {
			if it.Assignee == nil || it.Assignee.ID != *f.AssigneeID {
				continue
			}
		}
		if f.UpdatedSince != nil && it.UpdatedAt.Before(*f.UpdatedSince) {
			continue
		}
		if f.CreatedSince != nil && it.CreatedAt.Before(*f.CreatedSince) {
			continue
		}
		if f.SprintID != nil {
			// No sprint is native here, so a sprint filter matches
			// nothing rather than matching everything.
			continue
		}
		out = append(out, it)
	}
	return out
}

func isZeroFilter(f core.WorkItemFilter) bool {
	return len(f.IDs) == 0 && len(f.Kinds) == 0 && len(f.Statuses) == 0 &&
		len(f.StateCategory) == 0 && len(f.Labels) == 0 &&
		f.AssigneeID == nil && f.SprintID == nil &&
		f.UpdatedSince == nil && f.CreatedSince == nil
}

func hasAnyLabel(have []string, want map[string]struct{}) bool {
	for _, l := range have {
		if _, ok := want[l]; ok {
			return true
		}
	}
	return false
}

func stringsOf(ids []core.WorkItemID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

func toSet(in []string) map[string]struct{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(in))
	for _, s := range in {
		out[s] = struct{}{}
	}
	return out
}

// ProbeTimeout bounds the health probe so a hung source cannot stall the
// adaptor health surface.
const ProbeTimeout = 5 * time.Second

// Probe reports the registry's health as a single line for the /api/adaptors
// surface.
func (w *WorkPlane) Probe(ctx context.Context) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()

	healths := w.registry.Health()
	if len(healths) == 0 {
		return false, "no sources are allowlisted"
	}
	var degraded []string
	var never []string
	for _, h := range healths {
		switch {
		case h.LastSuccess.IsZero():
			never = append(never, string(h.Source))
		case !h.Healthy:
			degraded = append(degraded, string(h.Source))
		case h.Freshness == FreshnessStale:
			degraded = append(degraded, string(h.Source)+" (stale)")
		}
	}
	// A source that has never been read yet is not a failure on its own:
	// the first Snapshot call populates it. Report it, but do not call
	// the plane unhealthy until a read has actually failed.
	if len(degraded) == 0 {
		if len(never) > 0 {
			return true, "not yet read: " + strings.Join(never, ", ")
		}
		return true, ""
	}
	sort.Strings(degraded)
	return false, "degraded sources: " + strings.Join(degraded, ", ")
}
