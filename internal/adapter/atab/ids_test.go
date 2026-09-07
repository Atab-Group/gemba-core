package atab

import (
	"errors"
	"testing"

	"github.com/GembaCore/gemba-core/core"
)

func TestWorkItemID_RoundTrip(t *testing.T) {
	ref := IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 301}
	id := ref.WorkItemID("atab-group")
	if want := core.WorkItemID("atab-group/Atab-Group~Product-Seela/301"); id != want {
		t.Fatalf("id = %q, want %q", id, want)
	}
	gotSource, gotRef, err := ParseWorkItemID(id)
	if err != nil {
		t.Fatalf("ParseWorkItemID: %v", err)
	}
	if gotSource != "atab-group" || gotRef != ref {
		t.Fatalf("round trip = %q %v, want atab-group %v", gotSource, gotRef, ref)
	}
}

// Two sources carrying the same owner, repo and issue number must not
// collapse onto one id. This is the property the whole federation layer
// rests on, so it is asserted directly rather than inferred.
func TestWorkItemID_IsCollisionSafeAcrossSources(t *testing.T) {
	ref := IssueRef{Owner: "Product-Seela", Repo: "Product-Seela", Number: 301}
	a := ref.WorkItemID("atab-group")
	b := ref.WorkItemID("partner-org")
	if a == b {
		t.Fatalf("ids collided across sources: %q", a)
	}
}

// The separator has to survive an owner or repo that looks like the
// separator's neighbours. GitHub forbids "~" in both, so an id can be
// split back apart unambiguously.
func TestWorkItemID_SeparatorIsUnambiguous(t *testing.T) {
	ref := IssueRef{Owner: "a-b-c", Repo: "d-e~f", Number: 1}
	if err := ref.Validate(); err == nil {
		t.Fatal("a repo containing the reserved separator must be refused")
	}
}

func TestParseWorkItemID_Rejections(t *testing.T) {
	bad := []core.WorkItemID{
		"",
		"atab-group",
		"atab-group/Atab-Group~Product-Seela",
		"atab-group/Atab-Group/301",
		"atab-group/Atab-Group~Product-Seela/0",
		"atab-group/Atab-Group~Product-Seela/-3",
		"atab-group/Atab-Group~Product-Seela/notanumber",
		"ATAB/Atab-Group~Product-Seela/1",
		"/Atab-Group~Product-Seela/1",
	}
	for _, id := range bad {
		if _, _, err := ParseWorkItemID(id); err == nil {
			t.Errorf("ParseWorkItemID(%q) accepted a malformed id", id)
		} else if ae := core.AsAdaptorError(err); ae == nil || ae.Kind != core.KindValidation {
			t.Errorf("ParseWorkItemID(%q) error = %v, want a tagged validation error", id, err)
		}
	}
}

func TestSourceID_Validate(t *testing.T) {
	ok := []SourceID{"a", "atab-group", "a1", "partner-org-2"}
	for _, s := range ok {
		if err := s.Validate(); err != nil {
			t.Errorf("SourceID(%q).Validate() = %v, want nil", s, err)
		}
	}
	bad := []SourceID{"", "Atab-Group", "atab_group", "-lead", "trail-", "a--b", "a/b", "a~b"}
	for _, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("SourceID(%q).Validate() accepted an invalid id", s)
		}
	}
}

func TestIssueRef_RepositoryIDMatchesIDSegment(t *testing.T) {
	ref := IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 7}
	if got := ref.RepositoryID(); got != "Atab-Group~Product-Seela" {
		t.Fatalf("RepositoryID = %q", got)
	}
	_, parsed, err := ParseWorkItemID(ref.WorkItemID("s"))
	if err != nil || parsed.RepositoryID() != ref.RepositoryID() {
		t.Fatalf("repository id does not survive the id round trip: %v", err)
	}
}

func TestIssueRef_ValidateRejectsEmptyParts(t *testing.T) {
	for _, ref := range []IssueRef{
		{Repo: "r", Number: 1},
		{Owner: "o", Number: 1},
		{Owner: "o", Repo: "r"},
		{Owner: "o/x", Repo: "r", Number: 1},
	} {
		err := ref.Validate()
		if err == nil {
			t.Errorf("IssueRef%+v passed validation", ref)
			continue
		}
		if !errors.As(err, new(*core.AdaptorError)) {
			t.Errorf("IssueRef%+v error is untagged: %v", ref, err)
		}
	}
}
