package atab

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GembaCore/gemba-core/core"
)

func testStore(t *testing.T) *FileStore {
	t.Helper()
	store, err := NewFileStore(filepath.Join(t.TempDir(), "snapshots"))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return store
}

// storedCache builds a cache over store at a pinned clock.
func storedCache(t *testing.T, store SnapshotStore, at time.Time, issues []Issue) (*Cache, *FixtureClient) {
	t.Helper()
	cfg := DemoSourceConfig()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	client := NewFixtureClient(cfg.ID, issues)
	c := NewCache(cfg, client).WithStore(store)
	c.now = func() time.Time { return at }
	return c, client
}

// The point of the store: a process that comes up while GitHub is
// refusing to answer serves the board it last read rather than nothing.
// An empty board and an old board are not equally useful, and the whole
// reason a rate limit is survivable is that the previous answer is still
// broadly true.
func TestStore_ARestartServesTheLastBoardWhenTheSourceIsUnreadable(t *testing.T) {
	store := testStore(t)

	first, _ := storedCache(t, store, testNow, DemoIssues())
	if _, err := first.Snapshot(context.Background()); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}

	// A new process, an hour later, with GitHub throttling it.
	later := testNow.Add(time.Hour)
	second, client := storedCache(t, store, later, nil)
	client.SetFailure(core.NewAdaptorError(core.KindRateLimited, "throttled"))

	snap, err := second.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("a restored cache must serve rather than error: %v", err)
	}
	if len(snap.Issues) != len(DemoIssues()) {
		t.Fatalf("issues = %d, want the %d that were stored",
			len(snap.Issues), len(DemoIssues()))
	}
	if !snap.ObservedAt.Equal(testNow) {
		t.Errorf("observed_at = %s, want the instant the board was actually read (%s)",
			snap.ObservedAt, testNow)
	}
}

// A restored board must never claim to be current. Freshness is computed
// from the instant the board was actually read, so one older than the
// source's budget reads stale and every card says so. Restoring it as
// fresh would present an old board as a live one, which is worse than
// serving nothing at all.
func TestStore_ARestoredBoardIsStaleNotFresh(t *testing.T) {
	store := testStore(t)
	first, _ := storedCache(t, store, testNow, DemoIssues())
	if _, err := first.Snapshot(context.Background()); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}

	cfg := DemoSourceConfig()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	// Well past the source's stale_after.
	later := testNow.Add(cfg.StaleAfter + time.Hour)

	second, client := storedCache(t, store, later, nil)
	client.SetFailure(core.NewAdaptorError(core.KindRateLimited, "throttled"))

	snap, err := second.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Freshness != FreshnessStale {
		t.Errorf("freshness = %q, want stale for a board read %s ago",
			snap.Freshness, later.Sub(testNow))
	}
}

// A restore is not a read. The scheduler must still go to GitHub
// immediately at boot, or a restored board would sit unrefreshed for a
// whole interval while the source was perfectly readable.
func TestStore_ARestoreDoesNotCountAsAnAttempt(t *testing.T) {
	store := testStore(t)
	first, _ := storedCache(t, store, testNow, DemoIssues())
	if _, err := first.Snapshot(context.Background()); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}

	second, client := storedCache(t, store, testNow.Add(time.Minute), DemoIssues())
	if _, err := second.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if client.Calls() != 1 {
		t.Errorf("calls = %d, want 1; a restored cache still owes a read", client.Calls())
	}
}

// A restart soon after a full crawl must not pay for another one. The
// stored watermark is what makes the next fetch incremental, and a full
// crawl of a large source is both the slowest and the most expensive
// thing this adaptor does.
func TestStore_ARestartAfterAFullCrawlFetchesIncrementally(t *testing.T) {
	store := testStore(t)
	first, _ := storedCache(t, store, testNow, DemoIssues())
	if _, err := first.Snapshot(context.Background()); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}

	second, client := storedCache(t, store, testNow.Add(time.Minute), DemoIssues())
	if _, err := second.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := client.LastOptions(); got.UpdatedSince.IsZero() {
		t.Error("the read after a restart was a full fetch; the stored watermark was not used")
	}
}

// A stored board describes the source it was read from. Restoring one
// taken before a repository left the source would put that repository's
// issues back, and nothing later takes them off: an incremental fetch
// never removes anything and a full one only replaces what it asked for.
func TestStore_ASnapshotOfADifferentSourceShapeIsDiscarded(t *testing.T) {
	store := testStore(t)
	first, _ := storedCache(t, store, testNow, DemoIssues())
	if _, err := first.Snapshot(context.Background()); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}

	// Same id, a repository removed from the source.
	cfg := DemoSourceConfig()
	cfg.Repos = []string{"Only-One-Repo"}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	client := NewFixtureClient(cfg.ID, nil)
	client.SetFailure(core.NewAdaptorError(core.KindRateLimited, "throttled"))
	c := NewCache(cfg, client).WithStore(store)
	c.now = func() time.Time { return testNow.Add(time.Minute) }

	if ok, _, _ := c.Restored(); ok {
		t.Fatal("a snapshot of a differently-shaped source was restored")
	}
}

// A total failure must not overwrite the stored board with nothing. The
// stored copy exists precisely to survive the case where the source
// cannot be read, so letting that case destroy it would defeat it.
func TestStore_ATotalFailureDoesNotOverwriteTheStoredBoard(t *testing.T) {
	store := testStore(t)
	first, _ := storedCache(t, store, testNow, DemoIssues())
	if _, err := first.Snapshot(context.Background()); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}

	failing, client := storedCache(t, store, testNow.Add(time.Hour), nil)
	client.SetFailure(core.NewAdaptorError(core.KindRateLimited, "throttled"))
	if err := failing.Refresh(context.Background()); err == nil {
		t.Fatal("the read was meant to fail")
	}

	saved, ok, err := store.Load(DemoSourceID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !ok {
		t.Fatal("the stored board was deleted by a failed read")
	}
	if len(saved.Issues) != len(DemoIssues()) {
		t.Errorf("stored issues = %d, want the %d from before the failure",
			len(saved.Issues), len(DemoIssues()))
	}
}

// A corrupt or truncated file is a cache that cannot be used, not a
// reason to refuse to start. Failing the boot on it would turn a
// recoverable annoyance into an outage.
func TestStore_ACorruptFileReadsAsNothingStored(t *testing.T) {
	store := testStore(t)
	path := store.path(DemoSourceID)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, ok, err := store.Load(DemoSourceID)
	if err != nil {
		t.Fatalf("a corrupt file must not be an error: %v", err)
	}
	if ok {
		t.Error("a corrupt file reported a usable snapshot")
	}
}

// A snapshot of a large source is several megabytes, so a process killed
// mid-write must not leave a truncated file that the next boot reads as
// a source holding a handful of issues.
func TestStore_SaveLeavesNoPartialFile(t *testing.T) {
	store := testStore(t)
	if err := store.Save(PersistedSnapshot{
		Source:      DemoSourceID,
		Fingerprint: DemoSourceConfig().Fingerprint(),
		LastSuccess: testNow,
		Issues:      DemoIssues(),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, err := os.ReadDir(store.dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only the final file", names)
	}
}

// The fingerprint has to move when any part of the question moves, and
// stay put when nothing does. A fingerprint that ignored the repository
// set would restore a board from a source that no longer exists in that
// shape; one that changed spuriously would throw away a good board on
// every boot and make the store pointless.
func TestFingerprint_TracksTheSourceShape(t *testing.T) {
	base := DemoSourceConfig()
	if err := base.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want := base.Fingerprint()

	same := DemoSourceConfig()
	if err := same.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got := same.Fingerprint(); got != want {
		t.Errorf("an identical config fingerprints differently: %s vs %s", got, want)
	}

	// Repository order is not part of the question, only membership.
	reordered := DemoSourceConfig()
	reordered.Repos = append([]string(nil), base.Repos...)
	if len(reordered.Repos) > 1 {
		reordered.Repos[0], reordered.Repos[1] = reordered.Repos[1], reordered.Repos[0]
		if err := reordered.Normalize(); err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if got := reordered.Fingerprint(); got != want {
			t.Errorf("reordering the repository list changed the fingerprint")
		}
	}

	for name, mutate := range map[string]func(*SourceConfig){
		"org":            func(c *SourceConfig) { c.Org = "Someone-Else" },
		"project number": func(c *SourceConfig) { c.ProjectNumber = 99 },
		"repos":          func(c *SourceConfig) { c.Repos = []string{"Only-One-Repo"} },
		"field names": func(c *SourceConfig) {
			c.FieldNames = map[string]string{FieldStatus: "State"}
		},
	} {
		cfg := DemoSourceConfig()
		mutate(&cfg)
		if err := cfg.Normalize(); err != nil {
			t.Fatalf("%s: normalize: %v", name, err)
		}
		if got := cfg.Fingerprint(); got == want {
			t.Errorf("changing the %s did not change the fingerprint", name)
		}
	}
}

// Persistence changes what "unreadable" means for a source that has a
// stored board: it keeps serving that board instead of going blank. The
// behaviour is deliberate and is pinned here rather than left to be
// discovered, because it holds for a revoked credential exactly as it
// does for a network blip, and the two deserve different reactions from
// an operator.
//
// What the adaptor guarantees is that it never presents the stored board
// as current and never hides the failure: the snapshot reads stale, and
// health reports the source degraded with the reason. Purging genuinely
// requires deleting the state directory, which is what the operator does
// when access was withdrawn rather than interrupted.
func TestStore_ARestrictedSourceKeepsServingItsStoredBoardAndSaysSo(t *testing.T) {
	store := testStore(t)
	first, _ := storedCache(t, store, testNow, DemoIssues())
	if _, err := first.Snapshot(context.Background()); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}

	cfg := DemoSourceConfig()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	later := testNow.Add(cfg.StaleAfter + time.Minute)

	restricted, client := storedCache(t, store, later, nil)
	client.SetFailure(core.NewAdaptorError(core.KindCapabilityDenied,
		"this credential is not authorised for the requested source"))

	snap, err := restricted.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("a restored board still serves: %v", err)
	}
	if len(snap.Issues) != len(DemoIssues()) {
		t.Errorf("issues = %d, want the stored %d", len(snap.Issues), len(DemoIssues()))
	}
	if snap.Freshness != FreshnessStale {
		t.Errorf("freshness = %q, want stale; a stored board must never read current",
			snap.Freshness)
	}

	h := restricted.Health()
	if h.Healthy {
		t.Error("a source the credential can no longer read reports healthy")
	}
	if h.Reason == "" {
		t.Error("health gives no reason for a source that cannot be read")
	}
	// Deleting the state directory is the purge, and it has to actually
	// purge: a fresh cache over an empty store holds nothing.
	if err := os.Remove(store.path(DemoSourceID)); err != nil {
		t.Fatalf("remove stored snapshot: %v", err)
	}
	purged, purgedClient := storedCache(t, store, later, nil)
	purgedClient.SetFailure(core.NewAdaptorError(core.KindCapabilityDenied, "still denied"))
	if ok, _, items := purged.Restored(); ok {
		t.Errorf("a purged store still restored %d items", items)
	}
}
