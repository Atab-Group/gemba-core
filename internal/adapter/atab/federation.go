package atab

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/GembaCore/gemba-core/core"
)

// Registry is the federated set of work sources one Gemba process reads.
//
// Membership is explicit and closed. A source reaches the board only by
// appearing in the operator's configuration, and a source that is
// configured but unreachable degrades to an unhealthy entry rather than
// disappearing: the board keeps saying it exists and cannot be read,
// which is what stops a silent outage from looking like an empty
// backlog.
//
// Three properties hold across every method, and the Stage 2 tests pin
// each of them:
//
//   - Ids never collide. Every WorkItemID is prefixed with its source id,
//     so two orgs carrying issue #42 in a repo of the same name stay
//     distinct.
//   - An inaccessible source fails closed. Its items are absent, its
//     edges resolve to [BlockerUnknown] (which holds a dependency rather
//     than clearing it), and nothing about it beyond its id and a
//     condition string reaches a caller.
//   - One unhealthy source never takes down a healthy one. Every
//     traversal is per-source, and an error from one is recorded against
//     that source alone.
type Registry struct {
	mu      sync.RWMutex
	store   SnapshotStore
	order   []SourceID
	entries map[SourceID]*sourceEntry
}

type sourceEntry struct {
	cfg       SourceConfig
	cache     *Cache
	projector *Projector
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{entries: make(map[SourceID]*sourceEntry)}
}

// Add allowlists one source and binds the client it is read through.
//
// This is the allowlist: there is no discovery path and no wildcard. A
// duplicate id is refused rather than silently replacing the earlier
// entry, because two configs claiming one id would make the id prefix
// stop meaning what the collision-safety argument needs it to mean.
func (r *Registry) Add(cfg SourceConfig, client Client) error {
	if err := cfg.Normalize(); err != nil {
		return err
	}
	if client == nil {
		return core.NewAdaptorError(core.KindValidation,
			"atab: source %q has no client bound", cfg.ID)
	}
	if got := client.SourceID(); got != cfg.ID {
		return core.NewAdaptorError(core.KindValidation,
			"atab: client for source %q reports source id %q; "+
				"a mismatched binding would file items under the wrong prefix",
			cfg.ID, got)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[cfg.ID]; exists {
		return core.NewAdaptorError(core.KindValidation,
			"atab: source id %q is already registered", cfg.ID)
	}
	cache := NewCache(cfg, client)
	if r.store != nil {
		cache = cache.WithStore(r.store)
	}
	r.entries[cfg.ID] = &sourceEntry{
		cfg:       cfg,
		cache:     cache,
		projector: NewProjector(cfg),
	}
	r.order = append(r.order, cfg.ID)
	return nil
}

// WithStore makes every source added afterwards persist its snapshot,
// and restore it at construction. Call it before Add.
//
// It is registry-wide rather than per-source because the thing it
// protects against is process-wide: a restart, or a rate limit that
// stops every source at once.
func (r *Registry) WithStore(store SnapshotStore) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store = store
	return r
}

// Restored reports, per source, whether it came up holding a stored
// snapshot and how old that snapshot is.
func (r *Registry) Restored() map[SourceID]RestoredSnapshot {
	r.mu.RLock()
	entries := r.snapshotEntries()
	r.mu.RUnlock()

	out := make(map[SourceID]RestoredSnapshot)
	for _, e := range entries {
		if ok, at, items := e.cache.Restored(); ok {
			out[e.cfg.ID] = RestoredSnapshot{ObservedAt: at, Items: items}
		}
	}
	return out
}

// RestoredSnapshot describes a snapshot recovered from the store.
type RestoredSnapshot struct {
	ObservedAt time.Time `json:"observed_at"`
	Items      int       `json:"items"`
}

// Allowed reports whether id names an allowlisted source.
func (r *Registry) Allowed(id SourceID) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.entries[id]
	return ok
}

// Sources returns the configured source ids in registration order.
func (r *Registry) Sources() []SourceID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]SourceID(nil), r.order...)
}

// Config returns the configuration for id.
func (r *Registry) Config(id SourceID) (SourceConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[id]
	if !ok {
		return SourceConfig{}, false
	}
	return e.cfg, true
}

// Health reports every source's condition, in registration order. A
// caller with no access to a source still sees that it exists and how it
// is doing; it never sees anything the source contains.
func (r *Registry) Health() []Health {
	r.mu.RLock()
	entries := r.snapshotEntries()
	r.mu.RUnlock()

	out := make([]Health, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.cache.Health())
	}
	return out
}

// Healthy reports whether every source is currently readable, plus a
// caller-safe summary of the ones that are not.
func (r *Registry) Healthy() (bool, string) {
	var degraded []string
	for _, h := range r.Health() {
		if !h.Healthy {
			degraded = append(degraded, fmt.Sprintf("%s (%s)", h.Source, h.Reason))
		}
	}
	if len(degraded) == 0 {
		return true, ""
	}
	sort.Strings(degraded)
	return false, "unreadable sources: " + joinComma(degraded)
}

// Refresh forces a refresh of every source and returns the errors, keyed
// by source. One source's failure never short-circuits the others: the
// loop runs to completion so a single broken source cannot starve the
// rest of the board of updates.
func (r *Registry) Refresh(ctx context.Context) map[SourceID]error {
	r.mu.RLock()
	entries := r.snapshotEntries()
	r.mu.RUnlock()

	out := make(map[SourceID]error)
	for _, e := range entries {
		if err := e.cache.Refresh(ctx); err != nil {
			out[e.cfg.ID] = err
		}
	}
	return out
}

// RefreshSource forces a refresh of one source.
//
// It exists so a caller driving sources on a schedule can give each one
// the interval it declared, rather than reading every source on the
// shortest interval any of them asked for. An unknown id is a caller
// error, not a source failure, so it is tagged as validation.
func (r *Registry) RefreshSource(ctx context.Context, id SourceID) error {
	e := r.entry(id)
	if e == nil {
		return core.NewAdaptorError(core.KindValidation,
			"atab: no source %q is allowlisted", id)
	}
	return e.cache.Refresh(ctx)
}

// snapshotEntries copies the entry pointers in registration order.
// Callers must hold at least a read lock.
func (r *Registry) snapshotEntries() []*sourceEntry {
	out := make([]*sourceEntry, 0, len(r.order))
	for _, id := range r.order {
		if e, ok := r.entries[id]; ok {
			out = append(out, e)
		}
	}
	return out
}

// federatedResult is one source's contribution to a whole-registry read.
type federatedResult struct {
	source SourceID
	items  []core.WorkItem
	err    error
}

// projectAll reads and projects every source.
//
// Cross-source blocker resolution runs against the union of the healthy
// snapshots, and only for sources whose authority is canonical. A
// reference-authority source can show its own issues without being able
// to hold another source's work: an edge into it resolves as unknown,
// which the readiness rules treat as still blocking and label as such.
func (r *Registry) projectAll(ctx context.Context) []federatedResult {
	r.mu.RLock()
	entries := r.snapshotEntries()
	r.mu.RUnlock()

	snaps := make(map[SourceID]Snapshot, len(entries))
	results := make([]federatedResult, 0, len(entries))
	for _, e := range entries {
		snap, err := e.cache.Snapshot(ctx)
		if err != nil {
			results = append(results, federatedResult{source: e.cfg.ID, err: err})
			continue
		}
		snaps[e.cfg.ID] = snap
		results = append(results, federatedResult{source: e.cfg.ID})
	}

	resolver := r.crossSourceResolver(entries, snaps)
	for i := range results {
		if results[i].err != nil {
			continue
		}
		e := r.entry(results[i].source)
		if e == nil {
			continue
		}
		snap := snaps[results[i].source]
		snap.CrossSource = resolver
		results[i].items = e.projector.ProjectAll(snap)
	}
	return results
}

// crossSourceResolver answers a blocker that is not in the asking
// source's own snapshot.
//
// It searches only canonical sources, and only sources whose snapshot
// was read successfully. Anything it cannot find in that set is
// [BlockerUnknown], never [BlockerResolved]: an edge this deployment
// cannot see must hold the dependent work, because assuming it clear
// would let an unreachable source silently unblock a queue.
func (r *Registry) crossSourceResolver(
	entries []*sourceEntry, snaps map[SourceID]Snapshot,
) func(IssueRef) BlockerState {
	type lookup struct {
		cfg  SourceConfig
		snap Snapshot
	}
	var canonical []lookup
	for _, e := range entries {
		snap, ok := snaps[e.cfg.ID]
		if !ok || e.cfg.Authority != AuthorityCanonical {
			continue
		}
		canonical = append(canonical, lookup{cfg: e.cfg, snap: snap})
	}
	return func(target IssueRef) BlockerState {
		for _, l := range canonical {
			issue, ok := l.snap.Issues[target]
			if !ok {
				continue
			}
			if issue.State == "" {
				return BlockerUnknown
			}
			if equalFold(issue.State, "CLOSED") {
				return BlockerResolved
			}
			return BlockerOpen
		}
		return BlockerUnknown
	}
}

func (r *Registry) entry(id SourceID) *sourceEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.entries[id]
}

// getWorkItem resolves one qualified id.
//
// A well-formed id naming a source this process does not carry returns
// not-found with no detail beyond the id the caller already supplied.
// The registry does not say whether the source exists elsewhere, is
// misconfigured, or is merely unreachable, because each of those answers
// would leak the shape of a deployment the caller was not granted.
func (r *Registry) getWorkItem(ctx context.Context, id core.WorkItemID) (core.WorkItem, error) {
	source, ref, err := ParseWorkItemID(id)
	if err != nil {
		return core.WorkItem{}, err
	}
	e := r.entry(source)
	if e == nil {
		return core.WorkItem{}, core.WrapAdaptorError(core.KindSessionNotFound,
			core.ErrNotFound, "atab: no work item %q", id)
	}
	snap, snapErr := e.cache.Snapshot(ctx)
	if snapErr != nil {
		return core.WorkItem{}, snapErr
	}
	issue, ok := snap.Issues[ref]
	if !ok {
		// Fall back to a direct read: the snapshot is a bounded window
		// and an older issue may sit outside it without being absent.
		fetched, ferr := e.cache.client.FetchIssue(ctx, ref)
		if ferr != nil {
			if errors.Is(ferr, core.ErrNotFound) {
				return core.WorkItem{}, core.WrapAdaptorError(core.KindSessionNotFound,
					core.ErrNotFound, "atab: no work item %q", id)
			}
			return core.WorkItem{}, ferr
		}
		issue = fetched
		snap.Issues[ref] = fetched
	}
	r.mu.RLock()
	entries := r.snapshotEntries()
	r.mu.RUnlock()
	snaps := map[SourceID]Snapshot{source: snap}
	for _, other := range entries {
		if other.cfg.ID == source {
			continue
		}
		if s, err := other.cache.Snapshot(ctx); err == nil {
			snaps[other.cfg.ID] = s
		}
	}
	snap.CrossSource = r.crossSourceResolver(entries, snaps)
	return e.projector.ProjectOne(snap, issue), nil
}

func joinComma(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
