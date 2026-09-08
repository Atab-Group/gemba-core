package atab

import (
	"testing"
	"time"

	"github.com/GembaCore/gemba-core/core"
)

func projectProjector(t *testing.T, projectNumber int) *Projector {
	t.Helper()
	cfg := SourceConfig{ID: "atab-group", Org: "Atab-Group", ProjectNumber: projectNumber}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return NewProjector(cfg)
}

func projectIssue(projects []ProjectRef, item *ProjectItem) Issue {
	return Issue{
		Ref:       IssueRef{Owner: "Atab-Group", Repo: "ATAB-Marketplace", Number: 7},
		Title:     "a project-filtered issue",
		State:     "OPEN",
		CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Projects:  projects,

		ProjectItem: item,
	}
}

func projectSnapshot(issue Issue) Snapshot {
	return Snapshot{
		Source:     "atab-group",
		ObservedAt: time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC),
		Freshness:  FreshnessFresh,
		Issues:     map[IssueRef]Issue{issue.Ref: issue},
	}
}

// The project axis is qualified by source for the same reason work item
// ids are: two orgs both run a project #1, and an unqualified "#1" would
// merge two boards into one filter button.
func TestProjection_ProjectIdentityIsSourceQualified(t *testing.T) {
	issue := projectIssue(
		[]ProjectRef{{Number: 1, Title: "Atab Group Tasks"}},
		&ProjectItem{ProjectNumber: 1, ProjectTitle: "Atab Group Tasks"},
	)
	item := projectProjector(t, 1).ProjectOne(projectSnapshot(issue), issue)

	if got := item.Custom[FieldKeyProject]; got != "atab-group#1" {
		t.Errorf("%s = %v, want %q", FieldKeyProject, got, "atab-group#1")
	}
	if got := item.Custom[FieldKeyProjectTitle]; got != "Atab Group Tasks" {
		t.Errorf("%s = %v, want the board's own title", FieldKeyProjectTitle, got)
	}
}

// An issue on no board carries no project. It is a real answer, and the
// filter shows it as its own bucket rather than inventing a board.
func TestProjection_AnIssueOnNoBoardCarriesNoProject(t *testing.T) {
	issue := projectIssue(nil, nil)
	item := projectProjector(t, 1).ProjectOne(projectSnapshot(issue), issue)

	if _, ok := item.Custom[FieldKeyProject]; ok {
		t.Errorf("%s is present on an issue that is on no board: %v",
			FieldKeyProject, item.Custom[FieldKeyProject])
	}
	if _, ok := item.Custom[FieldKeyProjects]; ok {
		t.Errorf("%s is present on an issue that is on no board", FieldKeyProjects)
	}
}

// An issue on a second board is not unfiled. The configured board leads,
// because that is where the card's Status and Priority come from, but
// the others still travel so the filter can offer them.
func TestProjection_SecondBoardTravelsWithTheConfiguredOneFirst(t *testing.T) {
	issue := projectIssue([]ProjectRef{
		{Number: 4, Title: "Portfolio"},
		{Number: 1, Title: "Atab Group Tasks"},
	}, &ProjectItem{ProjectNumber: 1, ProjectTitle: "Atab Group Tasks"})
	item := projectProjector(t, 1).ProjectOne(projectSnapshot(issue), issue)

	if got := item.Custom[FieldKeyProject]; got != "atab-group#1" {
		t.Errorf("%s = %v, want the configured board to lead", FieldKeyProject, got)
	}
	projects, ok := item.Custom[FieldKeyProjects].([]map[string]any)
	if !ok {
		t.Fatalf("%s = %T, want a list", FieldKeyProjects, item.Custom[FieldKeyProjects])
	}
	if len(projects) != 2 {
		t.Fatalf("projects = %d, want both boards", len(projects))
	}
	if projects[0]["id"] != "atab-group#1" || projects[1]["id"] != "atab-group#4" {
		t.Errorf("project order = %v, want the configured board first", projects)
	}
	if projects[1]["title"] != "Portfolio" {
		t.Errorf("the second board lost its title: %v", projects[1])
	}
}

// A restored snapshot written before the project axis existed carries
// only the configured row. Deriving the list from it keeps that board
// filterable instead of showing every card as unfiled until the next
// full fetch.
func TestProjection_DerivesTheProjectFromAnOlderSnapshot(t *testing.T) {
	issue := projectIssue(nil, &ProjectItem{ProjectNumber: 1, ProjectTitle: "Atab Group Tasks"})
	item := projectProjector(t, 1).ProjectOne(projectSnapshot(issue), issue)

	if got := item.Custom[FieldKeyProject]; got != "atab-group#1" {
		t.Errorf("%s = %v, want it derived from the stored row", FieldKeyProject, got)
	}
}

// Two rows on one board are one project, not two filter buttons.
func TestProjection_DeduplicatesRepeatedBoards(t *testing.T) {
	issue := projectIssue([]ProjectRef{
		{Number: 1, Title: "Atab Group Tasks"},
		{Number: 1, Title: "Atab Group Tasks"},
	}, nil)
	item := projectProjector(t, 1).ProjectOne(projectSnapshot(issue), issue)

	projects, _ := item.Custom[FieldKeyProjects].([]map[string]any)
	if len(projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(projects))
	}
}

// The project axis and the source axis are different questions. A source
// with no configured board still reports the boards its issues sit on,
// which is what makes "project" mean the board rather than the org.
func TestProjection_ProjectIsNotTheSource(t *testing.T) {
	issue := projectIssue([]ProjectRef{{Number: 3, Title: "Delivery"}}, nil)
	item := projectProjector(t, 0).ProjectOne(projectSnapshot(issue), issue)

	if got := item.Custom[FieldKeyProject]; got != "atab-group#3" {
		t.Errorf("%s = %v, want the observed board", FieldKeyProject, got)
	}
	if got := item.Custom[FieldKeySource]; got != "atab-group" {
		t.Errorf("%s = %v, want the source to stay its own field", FieldKeySource, got)
	}
	if item.Custom[FieldKeyProject] == item.Custom[FieldKeySource] {
		t.Error("the project axis collapsed into the source axis")
	}
}

// The manifest has to declare every field the projection writes, or the
// SPA has no way to know the field exists.
func TestManifest_DeclaresTheProjectFields(t *testing.T) {
	declared := map[string]bool{}
	for _, f := range Manifest(core.TransportAPI).FieldExtensions {
		declared[f.Name] = true
	}
	for _, key := range []string{FieldKeyProject, FieldKeyProjectTitle, FieldKeyProjects} {
		if !declared[key] {
			t.Errorf("the manifest does not declare %q", key)
		}
	}
}
