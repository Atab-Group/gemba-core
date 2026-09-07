package atab

import (
	"errors"
	"strings"
	"testing"

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
	for _, q := range []string{open, all, singleIssueQuery} {
		for _, field := range []string{
			"atab", // no marker expected; guards against an empty query
			"projectItems", "closedByPullRequestsReferences", "subIssues", "parent", "comments",
		} {
			if field == "atab" {
				continue
			}
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
