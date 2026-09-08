package atab

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GembaCore/gemba-core/core"
)

// The runtime branches on Kind and Retryable, never on the message, so
// each failure shape has to arrive tagged correctly at the boundary.
func TestClassifyGHError(t *testing.T) {
	cases := []struct {
		name      string
		stderr    string
		wantKind  core.ErrorKind
		retryable bool
	}{
		{"throttled", "API rate limit already exceeded for user ID 1", core.KindRateLimited, true},
		{"http 429", "HTTP 429: too many requests", core.KindRateLimited, true},
		{"missing", "gh: Not Found (HTTP 404)", core.KindSessionNotFound, false},
		{"forbidden", "HTTP 403: Resource not accessible by integration", core.KindCapabilityDenied, false},
		{"unauthorised", "gh: This API operation requires authentication", core.KindCapabilityDenied, false},
		{"transport", "dial tcp: connection refused", core.KindRequestFailed, true},
		{"silent", "", core.KindRequestFailed, true},
	}
	for _, tc := range cases {
		err := classifyGHError(errors.New("exit status 1"), tc.stderr)
		ae := core.AsAdaptorError(err)
		if ae == nil {
			t.Errorf("%s: error is untagged: %v", tc.name, err)
			continue
		}
		if ae.Kind != tc.wantKind {
			t.Errorf("%s: kind = %q, want %q", tc.name, ae.Kind, tc.wantKind)
		}
		if ae.Retryable != tc.retryable {
			t.Errorf("%s: retryable = %v, want %v", tc.name, ae.Retryable, tc.retryable)
		}
	}
}

// A permission failure must not echo GitHub's body back to the caller.
// Doing so would put the shape of a source they cannot read into a
// response they can.
func TestClassifyGHError_PermissionFailureIsTerse(t *testing.T) {
	err := classifyGHError(errors.New("exit status 1"),
		"HTTP 403: Resource not accessible. repository Atab-Group/Secret-Client is private")
	if strings.Contains(err.Error(), "Secret-Client") {
		t.Fatalf("the permission error leaked a repository name: %v", err)
	}
}

func TestClassifyGHError_NotFoundMatchesTheSentinel(t *testing.T) {
	err := classifyGHError(errors.New("exit status 1"), "gh: Not Found (HTTP 404)")
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("error = %v, want it to satisfy errors.Is(ErrNotFound)", err)
	}
}

// A whole source throttled has to stay rate_limited, so the runtime
// backs off instead of retrying straight into the same wall.
func TestAggregateFailure_PreservesASharedKind(t *testing.T) {
	throttled := core.NewAdaptorError(core.KindRateLimited, "throttled")
	err := aggregateFailure("atab-group", []string{"a", "b"}, []error{throttled, throttled})

	ae := core.AsAdaptorError(err)
	if ae == nil || ae.Kind != core.KindRateLimited {
		t.Fatalf("error = %v, want a tagged rate_limited", err)
	}
	if !ae.Retryable {
		t.Error("a throttled source is retryable")
	}
	// Identical causes are named once, not once per repository.
	if strings.Count(ae.Message, "throttled") != 1 {
		t.Errorf("message repeats an identical cause: %q", ae.Message)
	}
	for _, repo := range []string{"a", "b"} {
		if !strings.Contains(ae.Message, repo) {
			t.Errorf("message does not name failing repository %q: %s", repo, ae.Message)
		}
	}
}

// Mixed causes have no single honest kind, so the aggregate falls back
// to request_failed rather than picking one of them.
func TestAggregateFailure_MixedKindsFallBack(t *testing.T) {
	err := aggregateFailure("atab-group", []string{"a", "b"}, []error{
		core.NewAdaptorError(core.KindRateLimited, "throttled"),
		core.NewAdaptorError(core.KindCapabilityDenied, "not authorised"),
	})
	ae := core.AsAdaptorError(err)
	if ae == nil || ae.Kind != core.KindRequestFailed {
		t.Fatalf("error = %v, want a tagged request_failed", err)
	}
}

// The state set is baked into the query text because GraphQL has no
// conditional and gh's flags carry only scalars.
func TestListIssuesQueryFor(t *testing.T) {
	open := listIssuesQueryFor(false)
	all := listIssuesQueryFor(true)
	if !strings.Contains(open, "states: [OPEN]") {
		t.Error("the open-only query does not restrict states")
	}
	if !strings.Contains(all, "states: [OPEN, CLOSED]") {
		t.Error("the include-closed query does not ask for closed issues")
	}
	// Every field the projection derives from has to be asked for. A
	// missing one reads as an absent value rather than as an error, so
	// the query text is pinned here.
	for _, q := range []string{open, all, singleIssueQuery} {
		for _, field := range []string{
			"body", "stateReason", "labels", "assignees",
			"parent", "subIssues", "comments",
			"projectItems", "closedByPullRequestsReferences", "statusCheckRollup",
		} {
			if !strings.Contains(q, field) {
				t.Errorf("query is missing %q", field)
			}
		}
	}
}

func TestGHClient_SourceIDMatchesItsConfig(t *testing.T) {
	cfg := DemoSourceConfig()
	if got := NewGHClient(cfg).SourceID(); got != cfg.ID {
		t.Fatalf("SourceID = %q, want %q", got, cfg.ID)
	}
}

func TestGHClient_FetchIssueRejectsAMalformedRef(t *testing.T) {
	c := NewGHClient(DemoSourceConfig())
	_, err := c.FetchIssue(t.Context(), IssueRef{Owner: "", Repo: "r", Number: 1})
	ae := core.AsAdaptorError(err)
	if ae == nil || ae.Kind != core.KindValidation {
		t.Fatalf("error = %v, want a tagged validation error before any network call", err)
	}
}

// ghShim writes a fake gh that answers for okRepo and fails for every
// other repository, so the partial-fetch path can be exercised without a
// network.
func ghShim(t *testing.T, okRepo string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    repo=` + okRepo + `)
      cat <<'JSON'
{"data":{"repository":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},
"nodes":[{"number":1,"title":"answered","state":"OPEN","createdAt":"2026-09-01T00:00:00Z",
"updatedAt":"2026-09-01T00:00:00Z"}]}}}}
JSON
      exit 0
      ;;
  esac
done
echo "HTTP 500: upstream is unwell" >&2
exit 1
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write gh shim: %v", err)
	}
	return path
}

// One repository failing must not discard the rows another returned, and
// must not be reported as a clean fetch either.
func TestGHClient_PartialFetchReturnsRowsAndAnError(t *testing.T) {
	cfg := DemoSourceConfig()
	cfg.Repos = []string{"Answers", "Fails"}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	client := NewGHClient(cfg).WithBinary(ghShim(t, "Answers"))

	issues, err := client.FetchIssues(t.Context(), FetchOptions{IncludeClosed: true})
	if err == nil {
		t.Fatal("a repository failed; the fetch must not report itself clean")
	}
	if len(issues) != 1 {
		t.Fatalf("issues = %d, want the one row the healthy repository returned", len(issues))
	}
	if issues[0].Ref.Repo != "Answers" {
		t.Errorf("row came from %q", issues[0].Ref.Repo)
	}
	if !strings.Contains(err.Error(), "Fails") {
		t.Errorf("error does not name the failing repository: %v", err)
	}
}

// Every repository failing returns no rows and the tagged error.
func TestGHClient_TotalFailureReturnsNoRows(t *testing.T) {
	cfg := DemoSourceConfig()
	cfg.Repos = []string{"Fails"}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	client := NewGHClient(cfg).WithBinary(ghShim(t, "NothingMatchesThis"))

	issues, err := client.FetchIssues(t.Context(), FetchOptions{IncludeClosed: true})
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %d, want none", len(issues))
	}
	if core.AsAdaptorError(err) == nil {
		t.Errorf("error is untagged: %v", err)
	}
}

// slowGHShim writes a fake gh that answers for every repository after
// pause, so the cost of reading a source scales with how the fetch is
// scheduled rather than with the network.
func slowGHShim(t *testing.T, pause string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
sleep ` + pause + `
cat <<'JSON'
{"data":{"repository":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},
"nodes":[{"number":1,"title":"answered","state":"OPEN","createdAt":"2026-09-01T00:00:00Z",
"updatedAt":"2026-09-01T00:00:00Z"}]}}}}
JSON
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write gh shim: %v", err)
	}
	return path
}

func repoNames(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, fmt.Sprintf("Repo%02d", i))
	}
	return out
}

// A source is read repository-by-repository, and the caller's context is
// usually an HTTP request deadline. Read one at a time, the repositories
// at the back of a nine-repository org never get asked inside that
// deadline, and the board renders as whichever prefix fit while
// reporting itself merely degraded.
//
// The bound here is deliberately loose. It is not asserting a latency
// target: it is asserting that the total is a function of the slowest
// few repositories rather than the sum of all of them, which is the
// difference a sequential fetch cannot produce.
func TestGHClient_RepositoriesAreFetchedConcurrently(t *testing.T) {
	const (
		repos = 8
		pause = 400 * time.Millisecond
	)
	cfg := DemoSourceConfig()
	cfg.Repos = repoNames(repos)
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	client := NewGHClient(cfg).WithBinary(slowGHShim(t, "0.4"))

	started := time.Now()
	issues, err := client.FetchIssues(t.Context(), FetchOptions{IncludeClosed: true})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("FetchIssues: %v", err)
	}
	if len(issues) != repos {
		t.Fatalf("issues = %d, want one per repository (%d)", len(issues), repos)
	}

	sequential := repos * pause
	waves := (repos + fetchConcurrency - 1) / fetchConcurrency
	budget := time.Duration(waves)*pause + sequential/2
	if elapsed >= budget {
		t.Errorf("fetching %d repositories took %s; a concurrent fetch should "+
			"cost about %d waves of %s, not the %s a sequential one does",
			repos, elapsed.Round(time.Millisecond), waves, pause, sequential)
	}
}

// Concurrency must not make the board's order depend on which repository
// answered first. A source that reshuffles between refreshes is one a
// reader cannot scan, and a Limit applied to arrival order would return
// a different subset of the same board on every call.
func TestGHClient_ResultsFollowDeclarationOrderNotArrivalOrder(t *testing.T) {
	cfg := DemoSourceConfig()
	cfg.Repos = repoNames(8)
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	client := NewGHClient(cfg).WithBinary(slowGHShim(t, "0.05"))

	for attempt := range 3 {
		issues, err := client.FetchIssues(t.Context(), FetchOptions{IncludeClosed: true})
		if err != nil {
			t.Fatalf("attempt %d: FetchIssues: %v", attempt, err)
		}
		for i, want := range cfg.Repos {
			if issues[i].Ref.Repo != want {
				t.Fatalf("attempt %d: issues[%d] came from %q, want %q",
					attempt, i, issues[i].Ref.Repo, want)
			}
		}
	}
}

// Limit truncates that stable order, so a bounded fetch returns the same
// rows every time rather than a race's worth of them.
func TestGHClient_LimitTruncatesInDeclarationOrder(t *testing.T) {
	cfg := DemoSourceConfig()
	cfg.Repos = repoNames(8)
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	client := NewGHClient(cfg).WithBinary(slowGHShim(t, "0.05"))

	issues, err := client.FetchIssues(t.Context(), FetchOptions{IncludeClosed: true, Limit: 3})
	if err != nil {
		t.Fatalf("FetchIssues: %v", err)
	}
	if len(issues) != 3 {
		t.Fatalf("issues = %d, want 3", len(issues))
	}
	for i, want := range cfg.Repos[:3] {
		if issues[i].Ref.Repo != want {
			t.Errorf("issues[%d] came from %q, want %q", i, issues[i].Ref.Repo, want)
		}
	}
}
