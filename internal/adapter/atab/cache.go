package atab

import (
	"context"
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

	mu            sync.Mutex
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

// Snapshot returns the current snapshot, refreshing first when the
// previous one is older than the source's RefreshInterval.
//
// The returned error is non-nil only when there is nothing to serve at
// all: a refresh failure over a populated cache returns the stale
// snapshot and no error, because the caller can render it and the
// freshness marker says not to trust it as current.
func (c *Cache) Snapshot(ctx context.Context) (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now().UTC()
	if c.refreshDue(now) {
		c.refreshLocked(ctx, now)
	}
	if len(c.issues) == 0 && c.lastSuccess.IsZero() {
		if c.lastErr != nil {
			return Snapshot{Source: c.cfg.ID, Freshness: FreshnessUnknown}, c.lastErr
		}
	}
	return c.snapshotLocked(), nil
}

// Refresh forces a refresh regardless of the interval and reports
// whether it succeeded.
func (c *Cache) Refresh(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshLocked(ctx, c.now().UTC())
	return c.lastErr
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

func (c *Cache) refreshLocked(ctx context.Context, now time.Time) {
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

	fetched, err := c.client.FetchIssues(ctx, opts)
	if err != nil {
		c.lastErr = err
		return
	}

	if full {
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
	c.lastErr = nil
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
