package atab

import (
	"testing"
	"time"

	"github.com/GembaCore/gemba-core/core"
)

// The tests in this file pin the Stage 0/1 board-to-core mappings one
// field at a time. The fixture-set tests next door prove the whole
// projection hangs together on realistic issues; these prove each
// individual mapping rule and the direction it fails in, so a regression
// names the rule it broke rather than a fixture that moved.

// mappingIssue is a minimal open issue on the source's own board.
func mappingIssue(fields map[string]string) Issue {
	return Issue{
		Ref:       IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 900},
		Title:     "Mapping probe",
		State:     "OPEN",
		CreatedAt: testNow.Add(-24 * time.Hour),
		UpdatedAt: testNow.Add(-time.Hour),
		ProjectItem: &ProjectItem{
			ProjectNumber: 1,
			Fields:        fields,
		},
	}
}

// projectMappingIssue renders one issue through cfg at the pinned clock.
func projectMappingIssue(t *testing.T, cfg SourceConfig, issue Issue) core.WorkItem {
	t.Helper()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize source config: %v", err)
	}
	p := NewProjector(cfg)
	p.now = func() time.Time { return testNow }
	snap := Snapshot{
		Source:     cfg.ID,
		ObservedAt: testNow.Add(-time.Minute),
		Freshness:  FreshnessFresh,
		Issues:     map[IssueRef]Issue{issue.Ref: issue},
	}
	return p.ProjectOne(snap, issue)
}

// P0 is the org's most urgent band and core reads a lower integer as more
// urgent, so the digit carries straight across. An inverted mapping would
// sort the board's emergencies to the bottom of every queue.
func TestProjection_PriorityDigitMapsStraightThrough(t *testing.T) {
	for raw, want := range map[string]int{"P0": 0, "P1": 1, "P2": 2, "P3": 3} {
		it := projectMappingIssue(t, DemoSourceConfig(),
			mappingIssue(map[string]string{FieldPriority: raw}))
		if it.Priority == nil {
			t.Errorf("%s: priority unset, want %d", raw, want)
			continue
		}
		if *it.Priority != want {
			t.Errorf("%s: priority = %d, want %d", raw, *it.Priority, want)
		}
		if got := it.Custom[FieldKeyPriority]; got != raw {
			t.Errorf("%s: %s = %v, want the raw board option", raw, FieldKeyPriority, got)
		}
	}
}

// A board option outside the P0-P3 shape leaves the core priority unset
// and keeps the raw string on Custom. Coercing it to an integer would
// invent a rank the board never assigned; dropping it entirely would hide
// that the board said something.
func TestProjection_UnmappablePriorityLeavesTheCoreFieldUnset(t *testing.T) {
	for _, raw := range []string{"High", "P", "P10", "Urgent", "0"} {
		it := projectMappingIssue(t, DemoSourceConfig(),
			mappingIssue(map[string]string{FieldPriority: raw}))
		if it.Priority != nil {
			t.Errorf("%q: priority = %d, want unset", raw, *it.Priority)
		}
		if got := it.Custom[FieldKeyPriority]; got != raw {
			t.Errorf("%q: %s = %v, want the raw board option preserved", raw, FieldKeyPriority, got)
		}
	}
}

// An unprioritised row must not read as P0. Defaulting an absent field to
// the zero value would promote every unranked issue to the top of the
// queue, which is the most damaging direction this mapping can fail in.
func TestProjection_AbsentPriorityIsUnsetNotZero(t *testing.T) {
	it := projectMappingIssue(t, DemoSourceConfig(), mappingIssue(nil))
	if it.Priority != nil {
		t.Errorf("priority = %d, want unset for a row with no Priority option", *it.Priority)
	}
	if _, ok := it.Custom[FieldKeyPriority]; ok {
		t.Errorf("%s present for a row with no Priority option", FieldKeyPriority)
	}
}

// The type: label is the org's authoritative statement of kind and wins
// over the atab-meta block, which a skill may have written earlier.
func TestProjection_KindPrefersTheTypeLabelOverMeta(t *testing.T) {
	issue := mappingIssue(nil)
	issue.Labels = []string{"needs-triage", TypeLabelPrefix + "bug"}
	issue.Body = "<!-- atab-meta: managed by atab skills -->\n```yaml\ntype: feature\n```\n"

	it := projectMappingIssue(t, DemoSourceConfig(), issue)
	if it.Kind != "bug" {
		t.Errorf("kind = %q, want bug from the type: label", it.Kind)
	}
	if got := it.Custom[FieldKeyType]; got != "feature" {
		t.Errorf("%s = %v, want the atab-meta type reported as declared", FieldKeyType, got)
	}
}

// With no label, the atab-meta type carries the kind.
func TestProjection_KindFallsBackToTheMetaType(t *testing.T) {
	issue := mappingIssue(nil)
	issue.Body = "<!-- atab-meta: managed by atab skills -->\n```yaml\ntype: chore\n```\n"

	if it := projectMappingIssue(t, DemoSourceConfig(), issue); it.Kind != "chore" {
		t.Errorf("kind = %q, want chore from atab-meta", it.Kind)
	}
}

// An epic renders as the core milestone kind so the SPA's milestone
// chrome picks it up. The atab type stays "epic" on Custom, because the
// board's own vocabulary is what a filter written against the org's
// labels will ask for.
func TestProjection_KindEpicBecomesTheCoreMilestone(t *testing.T) {
	issue := mappingIssue(nil)
	issue.Labels = []string{TypeLabelPrefix + EpicType}

	it := projectMappingIssue(t, DemoSourceConfig(), issue)
	if it.Kind != core.KindMilestone {
		t.Errorf("kind = %q, want %q", it.Kind, core.KindMilestone)
	}
}

// An issue that declares no type at all is a task, not an empty kind. A
// blank kind would render as a card with no type chip at all.
func TestProjection_KindDefaultsToTaskWhenNothingDeclaresOne(t *testing.T) {
	if it := projectMappingIssue(t, DemoSourceConfig(), mappingIssue(nil)); it.Kind != "task" {
		t.Errorf("kind = %q, want task", it.Kind)
	}
}

// renamedSource spells all four board fields differently, the way a
// federated source that is not Atab-Group Project #1 may.
func renamedSource() SourceConfig {
	cfg := SecondaryDemoSourceConfig()
	cfg.Org = "Atab-Group"
	cfg.ProjectNumber = 1
	cfg.FieldNames = map[string]string{
		FieldStatus:   "State",
		FieldPriority: "Urgency",
		FieldArea:     "Domain",
		FieldEstimate: "Size",
	}
	return cfg
}

// field_names is the whole reason a second board can join without a code
// change, so every field that reads through FieldName is pinned here
// rather than only Status.
func TestProjection_FieldNameOverridesReadTheRenamedColumns(t *testing.T) {
	issue := mappingIssue(map[string]string{
		"State":   StatusInProgress,
		"Urgency": "P0",
		"Domain":  "Platform",
		"Size":    "L",
	})

	it := projectMappingIssue(t, renamedSource(), issue)
	if it.Status != StatusInProgress {
		t.Errorf("status = %q, want %q through the State override", it.Status, StatusInProgress)
	}
	if it.Priority == nil || *it.Priority != 0 {
		t.Errorf("priority = %v, want 0 through the Urgency override", it.Priority)
	}
	if got := it.Custom[FieldKeyArea]; got != "Platform" {
		t.Errorf("%s = %v, want Platform through the Domain override", FieldKeyArea, got)
	}
	if got := it.Custom[FieldKeyEstimate]; got != "L" {
		t.Errorf("%s = %v, want L through the Size override", FieldKeyEstimate, got)
	}
}

// The mirror of the case above: a source that has not declared the
// overrides reads nothing off a renamed board and says so, rather than
// falling back to some other column that happens to be present.
func TestProjection_DefaultFieldNamesReadNothingFromARenamedBoard(t *testing.T) {
	issue := mappingIssue(map[string]string{
		"State":   StatusInProgress,
		"Urgency": "P0",
		"Domain":  "Platform",
	})

	it := projectMappingIssue(t, DemoSourceConfig(), issue)
	if got := it.Custom[FieldKeyBoardStatus]; got != "" {
		t.Errorf("%s = %v, want empty when the board field is not the declared one", FieldKeyBoardStatus, got)
	}
	if it.Status != StatusOpen {
		t.Errorf("status = %q, want the GitHub state %q when the board says nothing readable", it.Status, StatusOpen)
	}
	if it.Priority != nil {
		t.Errorf("priority = %d, want unset", *it.Priority)
	}
	if _, ok := it.Custom[FieldKeyArea]; ok {
		t.Errorf("%s present, want absent", FieldKeyArea)
	}
}

// An issue can sit on several org boards. Only the source's declared
// board places it in a lane: reading a Status off somebody else's board
// would move the card on a signal this source does not own.
func TestProjection_ARowOnAnotherBoardIsNotThisSourcesStatus(t *testing.T) {
	issue := mappingIssue(map[string]string{FieldStatus: StatusDone})
	issue.ProjectItem.ProjectNumber = 7

	it := projectMappingIssue(t, DemoSourceConfig(), issue)
	if got := it.Custom[FieldKeyBoardStatus]; got != "" {
		t.Errorf("%s = %v, want empty for a row on project 7", FieldKeyBoardStatus, got)
	}
	if it.Status != StatusOpen {
		t.Errorf("status = %q, want %q from the GitHub state", it.Status, StatusOpen)
	}
}

// A source with no board reads every issue from its GitHub state alone,
// which is what project_number 0 declares.
func TestProjection_ASourceWithNoBoardProjectsFromGitHubStateAlone(t *testing.T) {
	cfg := DemoSourceConfig()
	cfg.ProjectNumber = 0

	issue := mappingIssue(map[string]string{FieldStatus: StatusDone})
	issue.ProjectItem.ProjectNumber = 1

	// project_number 0 accepts whatever row it is given, because the
	// source has not declared a board to match against.
	if it := projectMappingIssue(t, cfg, issue); it.Status != StatusDone {
		t.Errorf("status = %q, want %q", it.Status, StatusDone)
	}
}

// Area and Estimate are optional columns. An unset one is absent from
// Custom rather than present and empty, so a renderer can tell "the board
// has no estimate" from "the estimate is blank".
func TestProjection_BlankAreaAndEstimateAreAbsentFromCustom(t *testing.T) {
	it := projectMappingIssue(t, DemoSourceConfig(),
		mappingIssue(map[string]string{FieldStatus: StatusTodo, FieldArea: "", FieldEstimate: ""}))

	if _, ok := it.Custom[FieldKeyArea]; ok {
		t.Errorf("%s present for a blank board option", FieldKeyArea)
	}
	if _, ok := it.Custom[FieldKeyEstimate]; ok {
		t.Errorf("%s present for a blank board option", FieldKeyEstimate)
	}
}

// Every card carries the freshness of the snapshot behind it and the
// instant that snapshot was taken. A card with neither cannot be judged
// at all, so an unset snapshot freshness reads as unknown rather than
// defaulting to fresh.
func TestProjection_FreshnessAndObservedAtAreAlwaysCarried(t *testing.T) {
	cfg := DemoSourceConfig()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	p := NewProjector(cfg)
	p.now = func() time.Time { return testNow }

	issue := mappingIssue(map[string]string{FieldStatus: StatusTodo})
	observed := testNow.Add(-90 * time.Second)
	it := p.ProjectOne(Snapshot{
		Source:     cfg.ID,
		ObservedAt: observed,
		Issues:     map[IssueRef]Issue{issue.Ref: issue},
	}, issue)

	if got := it.Custom[FieldKeyFreshness]; got != string(FreshnessUnknown) {
		t.Errorf("%s = %v, want unknown for a snapshot that declares none", FieldKeyFreshness, got)
	}
	raw, ok := it.Custom[FieldKeyObservedAt].(string)
	if !ok {
		t.Fatalf("%s = %v, want an RFC3339 string", FieldKeyObservedAt, it.Custom[FieldKeyObservedAt])
	}
	got, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("parse %s %q: %v", FieldKeyObservedAt, raw, err)
	}
	if !got.Equal(observed) {
		t.Errorf("%s = %s, want %s", FieldKeyObservedAt, got, observed)
	}
}

// The source id and the org travel on every card. Federation identifies a
// card's origin by these two fields, and a card that cannot name its
// source cannot be attributed or filtered.
func TestProjection_EveryCardNamesItsSourceAndOrg(t *testing.T) {
	it := projectMappingIssue(t, DemoSourceConfig(), mappingIssue(nil))

	if got := it.Custom[FieldKeySource]; got != string(DemoSourceID) {
		t.Errorf("%s = %v, want %q", FieldKeySource, got, DemoSourceID)
	}
	if got := it.Custom[FieldKeySourceOrg]; got != "Atab-Group" {
		t.Errorf("%s = %v, want Atab-Group", FieldKeySourceOrg, got)
	}
	if got := it.Custom[FieldKeyRepo]; got != "Atab-Group/Product-Seela" {
		t.Errorf("%s = %v", FieldKeyRepo, got)
	}
	if got := it.Custom[FieldKeyIssueNumber]; got != 900 {
		t.Errorf("%s = %v, want 900", FieldKeyIssueNumber, got)
	}
}

// malformedMetaBody is a marker whose YAML does not load. The marker
// itself must carry the colon: the org's canonical parser matches
// "<!-- atab-meta: ... -->", and a hand-written block missing it is not
// a managed block at all.
const malformedMetaBody = "<!-- atab-meta: managed by atab skills -->\n" +
	"```yaml\ntype: [unclosed\n```\n"

// A malformed atab-meta block is reported as malformed and never guessed
// at. The issue still projects: withholding the card would hide the
// broken block instead of showing it.
func TestProjection_MalformedMetaStillProjectsAndSaysSo(t *testing.T) {
	issue := mappingIssue(map[string]string{FieldStatus: StatusTodo})
	// A workable type label, because the label gate is decided before
	// the block is parsed and would otherwise answer first.
	issue.Labels = []string{TypeLabelPrefix + "bug"}
	issue.Body = malformedMetaBody

	it := projectMappingIssue(t, DemoSourceConfig(), issue)
	if got := it.Custom[FieldKeyMetaState]; got != string(MetaMalformed) {
		t.Errorf("%s = %v, want malformed", FieldKeyMetaState, got)
	}
	if r := readiness(t, it); r != ReadyMalformed {
		t.Errorf("readiness = %q, want malformed", r)
	}
	if it.Title == "" {
		t.Error("title empty; a malformed block must not suppress the card")
	}
}

// Workability is read off the type label before the block is parsed, so
// an issue that declares no type reports the missing type rather than
// the broken block. This is the org's own selector order: reading the
// type out of atab-meta instead would make every malformed block look
// like an untyped issue and hide the state a human has to fix.
func TestReadiness_TheTypeLabelGateIsDecidedBeforeTheMetaBlock(t *testing.T) {
	issue := mappingIssue(map[string]string{FieldStatus: StatusTodo})
	issue.Body = malformedMetaBody

	it := projectMappingIssue(t, DemoSourceConfig(), issue)
	if r := readiness(t, it); r != ReadyNotWorkable {
		t.Errorf("readiness = %q, want %q for an untyped issue", r, ReadyNotWorkable)
	}
	// The malformed block is still reported on the card, so the reason
	// the type could not be read is visible even when it lost the gate.
	if got := it.Custom[FieldKeyMetaState]; got != string(MetaMalformed) {
		t.Errorf("%s = %v, want malformed", FieldKeyMetaState, got)
	}
}

// A marker without the colon is not the canonical managed block, so it
// reads as absent rather than malformed or present. Accepting the looser
// form would let a hand-typed block silently drive automation.
func TestProjection_ANonCanonicalMarkerIsNotAManagedBlock(t *testing.T) {
	issue := mappingIssue(map[string]string{FieldStatus: StatusTodo})
	issue.Body = "<!-- atab-meta -->\n```yaml\ntype: bug\nautonomy: auto\n```\n"

	it := projectMappingIssue(t, DemoSourceConfig(), issue)
	if got := it.Custom[FieldKeyMetaState]; got != string(MetaAbsent) {
		t.Errorf("%s = %v, want absent", FieldKeyMetaState, got)
	}
	if got := it.Custom[FieldKeyType]; got != "" {
		t.Errorf("%s = %v, want empty; the block is not managed", FieldKeyType, got)
	}
}

// An out-of-enum autonomy reads as human and warns. A malformed
// declaration must never grant more autonomy than a person agreed to, so
// this is the one mapping that is required to fail downward.
func TestProjection_OutOfEnumAutonomyReadsAsHumanAndWarns(t *testing.T) {
	issue := mappingIssue(map[string]string{FieldStatus: StatusTodo})
	issue.Body = "<!-- atab-meta: managed by atab skills -->\n```yaml\ntype: bug\nautonomy: yolo\n```\n"

	it := projectMappingIssue(t, DemoSourceConfig(), issue)
	if got := it.Custom[FieldKeyAutonomy]; got != string(AutonomyHuman) {
		t.Errorf("%s = %v, want human", FieldKeyAutonomy, got)
	}
	if got, _ := it.Custom[FieldKeyWarnings].(string); got == "" {
		t.Errorf("%s empty, want a warning naming the issue", FieldKeyWarnings)
	}
}
