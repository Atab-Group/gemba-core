package atab

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/GembaCore/gemba-core/core"
)

// FixtureFile is the on-disk shape a recorded source replays from. It is
// the same [Issue] shape the live client produces, so a fixture is a
// faithful stand-in rather than a parallel model that can drift.
type FixtureFile struct {
	Source SourceID `json:"source"`
	Issues []Issue  `json:"issues"`
}

// FixtureClient replays a recorded set of issues.
//
// It exists for three callers: the unit and conformance tests, the
// federation tests that need a source to fail on demand, and
// `gemba serve --atab-fixture`, which stands the dashboard up with no
// network and no credential at all.
type FixtureClient struct {
	source SourceID

	mu     sync.Mutex
	issues []Issue
	// failWith, when set, is returned by every read. Tests use it to
	// make one source unreadable and assert the others keep working.
	failWith error
	// partialWith, when set, is returned alongside whatever rows the
	// replay produces, which is the shape a real client uses when part
	// of a source answers and part does not.
	partialWith error
	// calls counts FetchIssues invocations, which is how the cache tests
	// prove a refresh was or was not attempted.
	calls int
	// lastOpts records the options of the most recent fetch, which is
	// how the incremental-refresh test proves a watermark was sent.
	lastOpts FetchOptions
}

var _ Client = (*FixtureClient)(nil)

// NewFixtureClient returns a client replaying issues for source.
func NewFixtureClient(source SourceID, issues []Issue) *FixtureClient {
	return &FixtureClient{source: source, issues: issues}
}

// LoadFixture reads a fixture file from disk.
func LoadFixture(path string) (*FixtureClient, FixtureFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, FixtureFile{}, core.WrapAdaptorError(core.KindValidation, err,
			"atab: could not read fixture %q", path)
	}
	var f FixtureFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, FixtureFile{}, core.WrapAdaptorError(core.KindValidation, err,
			"atab: fixture %q is not valid JSON", path)
	}
	if err := f.Source.Validate(); err != nil {
		return nil, FixtureFile{}, err
	}
	return NewFixtureClient(f.Source, f.Issues), f, nil
}

// SourceID names the source this client replays.
func (f *FixtureClient) SourceID() SourceID { return f.source }

// SetFailure makes every subsequent read return err. Passing nil clears
// it.
func (f *FixtureClient) SetFailure(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failWith = err
}

// SetPartial makes every subsequent FetchIssues return its rows
// alongside err, which is how a client reports that part of a source
// answered and part did not. Passing nil clears it.
func (f *FixtureClient) SetPartial(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.partialWith = err
}

// Replace swaps the replayed issue set, simulating an upstream change
// between two refreshes.
func (f *FixtureClient) Replace(issues []Issue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issues = issues
}

// Calls reports how many times FetchIssues has been called.
func (f *FixtureClient) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// LastOptions returns the options of the most recent FetchIssues call.
func (f *FixtureClient) LastOptions() FetchOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastOpts
}

// FetchIssues replays the recorded set, honouring the same filters the
// live client honours so the cache behaves identically against both.
func (f *FixtureClient) FetchIssues(_ context.Context, opts FetchOptions) ([]Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastOpts = opts
	if f.failWith != nil {
		return nil, f.failWith
	}
	var out []Issue
	for _, is := range f.issues {
		if !opts.IncludeClosed && is.State == "CLOSED" {
			continue
		}
		if !opts.UpdatedSince.IsZero() && is.UpdatedAt.Before(opts.UpdatedSince) {
			continue
		}
		if len(opts.Repos) > 0 && !containsString(opts.Repos, is.Ref.Repo) {
			continue
		}
		out = append(out, is)
		if opts.Limit > 0 && len(out) >= opts.Limit {
			break
		}
	}
	return out, f.partialWith
}

// FetchIssue replays one issue.
func (f *FixtureClient) FetchIssue(_ context.Context, ref IssueRef) (Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return Issue{}, f.failWith
	}
	for _, is := range f.issues {
		if is.Ref == ref {
			return is, nil
		}
	}
	return Issue{}, core.WrapAdaptorError(core.KindSessionNotFound, core.ErrNotFound,
		"atab: fixture has no issue %s", ref)
}

// Ping succeeds unless a failure is staged.
func (f *FixtureClient) Ping(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failWith
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// FixtureNow is the instant the bundled fixtures are written against.
// Tests and the fixture serve mode pin the clock to it so lease
// freshness and staleness are reproducible rather than drifting with the
// wall clock.
var FixtureNow = time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
