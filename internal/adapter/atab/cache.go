package atab

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// refreshOverlap is subtracted from the incremental watermark so an
// issue updated in the same second as the last fetch cannot fall through
// the gap between two windows. GitHub's updatedAt has second resolution
// and its search index lags writes slightly; a small overlap costs a few
// redundant rows and closes a class of silently-missing updates.
const refreshOverlap = 90 * time.Second

// fullRefreshEvery bounds how long an incremental chain runs before a
// full fetch re-anchors it. Incremental fetches see edits, but never see
// a deletion or a transfer, so the cache would drift without this.
const fullRefreshEvery = 30 * time.Minute

// Health is one source's observed condition. It is what the federation
// registry aggregates and what the /api/adaptors probe reports.
type Health struct {
	Source SourceID `json:"source"`
	// Healthy is false when the last refresh attempt failed.
	Healthy bool `json:"healthy"`
	// Reason names the failure in caller-safe terms. It never carries
	// the source's contents, only its condition.
	Reason string `json:"reason,omitempty"`
	// LastSuccess is when the source was last read successfully.
	LastSuccess time.Time `json:"last_success"`
	// LastAttempt is when a refresh was last tried.
	LastAttempt time.Time `json:"last_attempt"`
	// Freshness classifies the snapshot currently being served.
	Freshness Freshness `json:"freshness"`
	// Items is how many issues the snapshot holds.
	Items int `json:"items"`
}

// Cache holds one source's snapshot and refreshes it on demand, no more
// often than the source's RefreshInterval.
//
// A refresh failure never empties the cache. The last good snapshot keeps
// serving, flipped to stale once it ages past the source's budget, so a
// GitHub outage degrades the board to "here is what we last saw, and it
// is old" instead of to an empty board.
type Cache struct {
	cfg    SourceConfig
	client Client
	now    func() time.Time

	// refreshMu serialises refreshes. It is separate from mu because a
	// refresh spends most of its time inside the client, and holding the
	// state lock across that call would make every reader wait out the
	// network: a 45 second crawl would stall the health surface, which
	// exists precisely to be answerable while a source is unwell.
	refreshMu sync.Mutex

	mu            sync.Mutex
	store         SnapshotStore
	issues        map[IssueRef]Issue
	lastSuccess   time.Time
	lastAttempt   time.Time
	lastFullFetch time.Time
	lastErr       error
}

// NewCache returns a cache for cfg reading through client. cfg must
// already be normalised.
func NewCache(cfg SourceConfig, client Client) *Cache {
	return &Cache{
		cfg:    cfg,
		client: client,
		now:    time.Now,
		issues: make(map[IssueRef]Issue),
	}
}

// WithStore attaches a snapshot store and restores whatever it holds for
// this source, so the cache starts a process with the board it ended the
// last one with rather than with nothing.
//
// The restored snapshot keeps the instant it was actually read at, which
// is the whole point: freshness is computed from that instant, so a
// restored board reads `stale` the moment it is older than the source's
// budget and says so on every card. Restoring it as `fresh` would
// present a month-old board as current, which is worse than an empty
// one.
//
// lastAttempt is deliberately left zero. A restore is not a read, so the
// scheduler still reads immediately at boot; the restored snapshot is
// what serves until that read lands.
func (c *Cache) WithStore(store SnapshotStore) *Cache {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.store = store
	if store == nil {
		return c
	}
	saved, ok, err := store.Load(c.cfg.ID)
	if err != nil || !ok {
		return c
	}
	if saved.Fingerprint != c.cfg.Fingerprint() {
		// The stored board answers a different question: a different
		// org, board or repository set under the same id. Merging it
		// would put issues on the board that this source no longer
		// claims, and nothing later removes them.
		return c
	}
	for _, is := range saved.Issues {
		c.issues[is.Ref] = is
	}
	c.lastSuccess = saved.LastSuccess
	c.lastFullFetch = saved.LastFullFetch
	return c
}

// Restored reports whether the cache came up holding a stored snapshot,
// and how old that snapshot is. It is for the operator log at boot: a
// board serving restored data looks identical to one serving fresh data
// unless something says so.
func (c *Cache) Restored() (bool, time.Time, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastSuccess.IsZero() || len(c.issues) == 0 {
		return false, time.Time{}, 0
	}
	return true, c.lastSuccess, len(c.issues)
}

// Snapshot returns the current snapshot, refreshing first when the
// previous one is older than the source's RefreshInterval.
//
// The returned error is non-nil only when there is nothing to serve at
// all: a refresh failure over a populated cache returns the stale
// snapshot and no error, because the caller can render it and the
// freshness marker says not to trust it as current.
func (c *Cache) Snapshot(ctx context.Context) (Snapshot, error) {
	c.refreshIfDue(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.issues) == 0 && c.lastSuccess.IsZero() {
		if c.lastErr != nil {
			return Snapshot{Source: c.cfg.ID, Freshness: FreshnessUnknown}, c.lastErr
		}
	}
	return c.snapshotLocked(), nil
}

// refreshIfDue refreshes when the previous attempt is older than the
// source's RefreshInterval. The due check is repeated under refreshMu so
// that two callers arriving together produce one fetch rather than two.
func (c *Cache) refreshIfDue(ctx context.Context) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	c.mu.Lock()
	due := c.refreshDue(c.now().UTC())
	c.mu.Unlock()
	if !due {
		return
	}
	// The error is recorded on the cache and read back by the caller
	// through the snapshot and the health surface. A refresh failure is
	// not a Snapshot failure: the last good snapshot keeps serving.
	_ = c.refresh(ctx, c.now().UTC())
}

// Refresh forces a refresh regardless of the interval and reports
// whether it succeeded.
func (c *Cache) Refresh(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	return c.refresh(ctx, c.now().UTC())
}

// refresh reads the source and applies the result. The caller holds
// refreshMu; the state lock is taken twice, around the client call and
// never across it.
func (c *Cache) refresh(ctx context.Context, now time.Time) error {
	c.mu.Lock()
	c.lastAttempt = now
	full := c.lastFullFetch.IsZero() ||
		len(c.issues) == 0 ||
		now.Sub(c.lastFullFetch) >= fullRefreshEvery
	opts := FetchOptions{
		Repos:         c.cfg.Repos,
		IncludeClosed: true,
	}
	if !full {
		opts.UpdatedSince = c.lastSuccess.Add(-refreshOverlap)
	}
	c.mu.Unlock()

	fetched, err := c.client.FetchIssues(ctx, opts)

	c.mu.Lock()
	c.applyLocked(fetched, err, full, now)
	outcome := c.lastErr
	toPersist := c.persistableLocked()
	c.mu.Unlock()

	// Persisting happens outside the state lock. A snapshot of a large
	// source is several megabytes, and holding the lock across that write
	// would put a disk flush on the path of every reader for the same
	// reason the network call is not held across it either.
	if toPersist != nil {
		if err := c.store.Save(*toPersist); err != nil {
			// A board that cannot be saved is still a board. Losing the
			// stored copy costs one cold crawl after the next restart,
			// which is not worth failing a refresh that worked.
			slog.Warn("atab: could not persist the snapshot",
				"source", string(c.cfg.ID), "err", err)
		}
	}
	return outcome
}

// persistableLocked returns the snapshot to write, or nil when there is
// no store or nothing worth storing. The caller holds mu.
//
// A cache holding nothing is never written. Persisting an empty board
// over a good stored one would let a single total failure destroy the
// copy that exists to survive exactly that.
func (c *Cache) persistableLocked() *PersistedSnapshot {
	if c.store == nil || len(c.issues) == 0 || c.lastSuccess.IsZero() {
		return nil
	}
	issues := make([]Issue, 0, len(c.issues))
	for _, is := range c.issues {
		issues = append(issues, is)
	}
	return &PersistedSnapshot{
		Source:        c.cfg.ID,
		Fingerprint:   c.cfg.Fingerprint(),
		LastSuccess:   c.lastSuccess,
		LastFullFetch: c.lastFullFetch,
		Issues:        issues,
	}
}

// Health reports the source's condition without touching the network.
func (c *Cache) Health() Health {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := Health{
		Source:      c.cfg.ID,
		Healthy:     c.lastErr == nil && !c.lastSuccess.IsZero(),
		LastSuccess: c.lastSuccess,
		LastAttempt: c.lastAttempt,
		Freshness:   c.freshnessLocked(c.now().UTC()),
		Items:       len(c.issues),
	}
	if c.lastErr != nil {
		h.Reason = c.lastErr.Error()
	} else if c.lastSuccess.IsZero() {
		h.Reason = "source has never been read"
	}
	return h
}

func (c *Cache) refreshDue(now time.Time) bool {
	if c.lastAttempt.IsZero() {
		return true
	}
	return now.Sub(c.lastAttempt) >= c.cfg.RefreshInterval
}

// applyLocked folds one fetch's outcome into the cache. The caller holds
// mu and decided full before the fetch ran.
func (c *Cache) applyLocked(fetched []Issue, err error, full bool, now time.Time) {
	// A partial fetch is a non-nil error alongside a non-empty result:
	// some repositories in the source answered and others did not. It is
	// handled apart from both success and failure because treating it as
	// either one loses work.
	//
	// Treating it as success would let a full re-anchor replace the map
	// with only what answered, so one repository timing out would delete
	// every item it owns from the board. Treating it as failure would
	// throw away rows that were fetched successfully, and on a first load
	// large enough to time out the source would never populate at all.
	//
	// So a partial merges, never replaces, and it does not re-anchor.
	// Leaving lastFullFetch alone keeps the next refresh a full one, so
	// the repositories that missed out are retried whole rather than
	// crawling forward from an incremental watermark they never reached.
	// The rows that did arrive are current, so the success watermark does
	// move: withholding it would make the board report every fetched item
	// as unknown because a ninth repository timed out, which is less
	// truthful about those items, not more. What records the shortfall is
	// lastErr, which the health surface and the probe read.
	partial := err != nil && len(fetched) > 0
	if err != nil && !partial {
		c.lastErr = err
		return
	}

	if full && !partial {
		// A full fetch replaces the map, which is how an issue that was
		// deleted, transferred or moved off the board leaves the board.
		next := make(map[IssueRef]Issue, len(fetched))
		for _, is := range fetched {
			next[is.Ref] = is
		}
		c.issues = next
		c.lastFullFetch = now
	} else {
		for _, is := range fetched {
			c.issues[is.Ref] = is
		}
	}

	c.lastErr = err // nil on a clean fetch, the shortfall on a partial
	c.lastSuccess = now
}

func (c *Cache) snapshotLocked() Snapshot {
	issues := make(map[IssueRef]Issue, len(c.issues))
	for k, v := range c.issues {
		issues[k] = v
	}
	return Snapshot{
		Source:     c.cfg.ID,
		ObservedAt: c.lastSuccess,
		Freshness:  c.freshnessLocked(c.now().UTC()),
		Issues:     issues,
	}
}

func (c *Cache) freshnessLocked(now time.Time) Freshness {
	switch {
	case c.lastSuccess.IsZero():
		return FreshnessUnknown
	case now.Sub(c.lastSuccess) > c.cfg.StaleAfter:
		return FreshnessStale
	default:
		return FreshnessFresh
	}
}
