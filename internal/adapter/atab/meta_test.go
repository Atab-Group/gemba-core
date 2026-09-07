package atab

import (
	"reflect"
	"testing"
)

func TestParseMeta_FullV5Block(t *testing.T) {
	body := "## What\n\nSomething.\n\n" +
		"<!-- atab-meta: managed by atab-core skills, do not edit by hand -->\n" +
		"```yaml\n" +
		"type: bug\n" +
		"autonomy: auto\n" +
		"blocked_by:\n" +
		"  - 42\n" +
		"  - \"Atab-Group/Product-Snap-a-slip#88\"\n" +
		"discovered_from: 38\n" +
		"requires_env:\n" +
		"  - STRIPE_WEBHOOK_SECRET\n" +
		"  - name: API_KEY\n    from: \"secret:x\"\n    scope: pipeline\n" +
		"scope_clauses: [F-14, R-3]\n" +
		"```\n"

	got := ParseMeta(body, "Atab-Group")
	if got.State != MetaPresent {
		t.Fatalf("state = %q, want present", got.State)
	}
	if got.Type != "bug" {
		t.Errorf("type = %q, want bug", got.Type)
	}
	if got.Autonomy != AutonomyAuto {
		t.Errorf("autonomy = %q, want auto", got.Autonomy)
	}
	wantBlocked := []Edge{
		{Number: 42},
		{Owner: "Atab-Group", Repo: "Product-Snap-a-slip", Number: 88},
	}
	if !reflect.DeepEqual(got.BlockedBy, wantBlocked) {
		t.Errorf("blocked_by = %+v, want %+v", got.BlockedBy, wantBlocked)
	}
	if got.DiscoveredFrom == nil || got.DiscoveredFrom.Number != 38 {
		t.Errorf("discovered_from = %+v, want 38", got.DiscoveredFrom)
	}
	if len(got.RequiresEnv) != 2 {
		t.Fatalf("requires_env = %+v, want 2 entries", got.RequiresEnv)
	}
	if got.RequiresEnv[0].Name != "STRIPE_WEBHOOK_SECRET" || got.RequiresEnv[0].Scope != "deploy" {
		t.Errorf("bare requires_env entry = %+v, want deploy scope", got.RequiresEnv[0])
	}
	if got.RequiresEnv[1].Scope != "pipeline" {
		t.Errorf("mapping requires_env scope = %q, want pipeline", got.RequiresEnv[1].Scope)
	}
	if !reflect.DeepEqual(got.ScopeClauses, []string{"F-14", "R-3"}) {
		t.Errorf("scope_clauses = %v", got.ScopeClauses)
	}
}

func TestParseMeta_AbsentBlockIsUntrackedNotAnError(t *testing.T) {
	got := ParseMeta("Just a plain issue body.", "Atab-Group")
	if got.State != MetaAbsent {
		t.Fatalf("state = %q, want absent", got.State)
	}
}

func TestParseMeta_MalformedYAMLIsMalformed(t *testing.T) {
	body := "<!-- atab-meta: x -->\n```yaml\ntype: [unclosed\n```\n"
	if got := ParseMeta(body, "Atab-Group"); got.State != MetaMalformed {
		t.Fatalf("state = %q, want malformed", got.State)
	}
}

// A missing autonomy key is tier B, and anything outside the enum falls
// to human. The fail-safe direction is what stops a typo granting more
// autonomy than a person ever agreed to.
func TestNormalizeAutonomy_FailsSafeDownward(t *testing.T) {
	cases := []struct {
		raw      string
		want     Autonomy
		wantWarn bool
	}{
		{"", AutonomyPR, false},
		{"auto", AutonomyAuto, false},
		{"pr", AutonomyPR, false},
		{"human", AutonomyHuman, false},
		{"Auto", AutonomyHuman, true},
		{"automatic", AutonomyHuman, true},
		{"true", AutonomyHuman, true},
	}
	for _, tc := range cases {
		got, warn := NormalizeAutonomy(tc.raw)
		if got != tc.want {
			t.Errorf("NormalizeAutonomy(%q) = %q, want %q", tc.raw, got, tc.want)
		}
		if (warn != "") != tc.wantWarn {
			t.Errorf("NormalizeAutonomy(%q) warn = %q, wantWarn %v", tc.raw, warn, tc.wantWarn)
		}
	}
}

func TestParseMeta_InvalidAutonomyWarnsAndReadsAsHuman(t *testing.T) {
	body := "<!-- atab-meta: x -->\n```yaml\ntype: bug\nautonomy: Auto\n```\n"
	got := ParseMeta(body, "Atab-Group")
	if got.Autonomy != AutonomyHuman {
		t.Fatalf("autonomy = %q, want human", got.Autonomy)
	}
	if len(got.Warnings) == 0 {
		t.Fatal("expected a warning naming the invalid autonomy value")
	}
}

// A cross-repo edge naming an org outside the source's own is dropped
// with a warning. Following it would pull an unrelated org's issue into
// the dependency graph.
func TestParseMeta_CrossOrgEdgeIsDropped(t *testing.T) {
	body := "<!-- atab-meta: x -->\n```yaml\ntype: bug\nblocked_by:\n" +
		"  - \"Other-Org/thing#5\"\n  - \"Atab-Group/ok#6\"\n```\n"
	got := ParseMeta(body, "Atab-Group")
	if len(got.BlockedBy) != 1 || got.BlockedBy[0].Owner != "Atab-Group" {
		t.Fatalf("blocked_by = %+v, want only the Atab-Group edge", got.BlockedBy)
	}
	if len(got.Warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one naming the dropped edge", got.Warnings)
	}
}

func TestParseMeta_BlockIsFoundAmongOtherFences(t *testing.T) {
	body := "```yaml\nnot: the block\n```\n\n" +
		"<!-- atab-meta: x -->\n```yaml\ntype: task\n```\n"
	got := ParseMeta(body, "Atab-Group")
	if got.State != MetaPresent || got.Type != "task" {
		t.Fatalf("got %+v, want the marked block", got)
	}
}

func TestEdge_ResolveAndCrossRepo(t *testing.T) {
	self := IssueRef{Owner: "Atab-Group", Repo: "A", Number: 1}
	same := Edge{Number: 9}
	if same.CrossRepo() {
		t.Error("bare int edge must not report cross-repo")
	}
	if got := same.Resolve(self); got != (IssueRef{Owner: "Atab-Group", Repo: "A", Number: 9}) {
		t.Errorf("same-repo resolve = %v", got)
	}
	cross := Edge{Owner: "Atab-Group", Repo: "B", Number: 9}
	if got := cross.Resolve(self); got != (IssueRef{Owner: "Atab-Group", Repo: "B", Number: 9}) {
		t.Errorf("cross-repo resolve = %v", got)
	}
}

func TestParseAcceptanceCriteria(t *testing.T) {
	body := "## What\n\nThing.\n\n" +
		"## Acceptance Criteria\n\n" +
		"- [ ] first check\n" +
		"- [x] second check\n" +
		"* [X] third check\n" +
		"\n## Out of scope\n\n- [ ] not a criterion\n"
	got := ParseAcceptanceCriteria(body)
	want := []Criterion{
		{Text: "first check", Done: false},
		{Text: "second check", Done: true},
		{Text: "third check", Done: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("criteria = %+v, want %+v", got, want)
	}
}

func TestParseAcceptanceCriteria_AbsentSectionIsNil(t *testing.T) {
	if got := ParseAcceptanceCriteria("no criteria here"); got != nil {
		t.Fatalf("criteria = %+v, want nil", got)
	}
}
