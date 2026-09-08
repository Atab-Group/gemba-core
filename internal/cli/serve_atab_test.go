package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GembaCore/gemba-core/core"
	"github.com/GembaCore/gemba-core/internal/adapter/atab"
	"github.com/GembaCore/gemba-core/internal/config"
	"github.com/GembaCore/gemba-core/internal/shader"
)

func writeFixture(t *testing.T, source atab.SourceID, issues []atab.Issue) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.json")
	raw, err := json.Marshal(atab.FixtureFile{Source: source, Issues: issues})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func writeSources(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sources.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write sources: %v", err)
	}
	return path
}

func TestServe_ATABFlagsArePresent(t *testing.T) {
	cmd := newServeCmd(BuildInfo{})
	for _, name := range []string{"atab", "atab-sources", "atab-fixture"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("--%s missing from the serve command", name)
		}
	}
}

// --atab selects a WorkPlane on its own, so it must not trip the "no
// WorkPlane selected" gate, and it must refuse to share the process with
// another selector.
func TestValidateWorkPlaneFlags_ATAB(t *testing.T) {
	if err := (config.ServeConfig{ATAB: true}).ValidateWorkPlaneFlags(); err != nil {
		t.Fatalf("--atab alone: %v", err)
	}
	for name, cfg := range map[string]config.ServeConfig{
		"project-dir": {ATAB: true, ProjectDir: "/tmp/gm"},
		"dolt-url":    {ATAB: true, DoltURL: "mysql://root@127.0.0.1:3307/gemba"},
		"noop":        {ATAB: true, Noop: true},
	} {
		err := cfg.ValidateWorkPlaneFlags()
		if err == nil {
			t.Errorf("--atab with --%s was accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("--atab with --%s error = %q, want it to name the conflict", name, err)
		}
	}
	if err := (config.ServeConfig{
		ATAB: true, ATABFixture: "f.json", ATABSources: "s.json",
	}).ValidateWorkPlaneFlags(); err == nil {
		t.Error("--atab-fixture and --atab-sources must not be combined")
	}
	if err := (config.ServeConfig{ATABSources: "s.json"}).ValidateWorkPlaneFlags(); err == nil {
		t.Error("--atab-sources without --atab must be refused")
	}
}

// The bd probe and the embedded Dolt supervisor both belong to the Beads
// path. An ATAB run must not demand either.
func TestATABModeSkipsTheBeadsStartupPath(t *testing.T) {
	cfg := config.ServeConfig{ATAB: true}
	if shouldProbeBd(cfg) {
		t.Error("an ATAB run must not require the bd CLI on PATH")
	}
	if err := applyBeadsURLDefault(&cfg); err != nil {
		t.Fatalf("applyBeadsURLDefault: %v", err)
	}
	if cfg.DoltURL != "" {
		t.Errorf("an ATAB run resolved a Beads URL %q; it reads GitHub, not Dolt", cfg.DoltURL)
	}
	if cfg.BeadsURLSource != "" {
		t.Errorf("BeadsURLSource = %q, want empty so the cold-start gate stays out of the way",
			cfg.BeadsURLSource)
	}
}

func TestNormalizeServeMode_ATABDisablesOrchestration(t *testing.T) {
	cfg := config.ServeConfig{ATAB: true, Orchestration: "native"}
	normalizeServeMode(&cfg)
	if cfg.Orchestration != "none" {
		t.Fatalf("orchestration = %q, want none; there is nothing to dispatch", cfg.Orchestration)
	}
}

func TestBuildATABRegistry_DefaultsToTheBuiltInSource(t *testing.T) {
	reg, label, err := buildATABRegistry(config.ServeConfig{ATAB: true})
	if err != nil {
		t.Fatalf("buildATABRegistry: %v", err)
	}
	if got := reg.Sources(); len(got) != 1 || got[0] != atab.DemoSourceID {
		t.Fatalf("sources = %v, want the built-in Atab-Group source", got)
	}
	if !strings.Contains(label, "built-in") {
		t.Errorf("label = %q", label)
	}
}

func TestBuildATABRegistry_Fixture(t *testing.T) {
	path := writeFixture(t, atab.DemoSourceID, atab.DemoIssues())
	reg, label, err := buildATABRegistry(config.ServeConfig{ATAB: true, ATABFixture: path})
	if err != nil {
		t.Fatalf("buildATABRegistry: %v", err)
	}
	if !strings.Contains(label, path) {
		t.Errorf("label = %q, want it to name the fixture", label)
	}

	wp := atab.New(reg, core.TransportAPI)
	items, err := wp.ListWorkItems(context.Background(), core.WorkItemFilter{})
	if err != nil {
		t.Fatalf("ListWorkItems: %v", err)
	}
	if len(items) != len(atab.DemoIssues()) {
		t.Fatalf("items = %d, want %d replayed from the fixture", len(items), len(atab.DemoIssues()))
	}
}

func TestLoadATABSources(t *testing.T) {
	path := writeSources(t, `{
  "sources": [
    {"id": "atab-group", "org": "Atab-Group", "project_number": 1,
     "repos": ["Product-Seela"], "authority": "canonical",
     "refresh_interval": "30s", "stale_after": "5m"},
    {"id": "partner-org", "org": "Partner-Org", "project_number": 4,
     "authority": "reference"}
  ]
}`)
	got, fixtures, err := loadATABSources(path)
	if err != nil {
		t.Fatalf("loadATABSources: %v", err)
	}
	if len(got) != 2 || len(fixtures) != 2 {
		t.Fatalf("sources = %d, fixtures = %d, want 2 each", len(got), len(fixtures))
	}
	if got[0].RefreshInterval.String() != "30s" || got[0].StaleAfter.String() != "5m0s" {
		t.Errorf("durations not parsed: %+v", got[0])
	}
	if got[1].Authority != atab.AuthorityReference {
		t.Errorf("authority = %q", got[1].Authority)
	}
}

func TestLoadATABSources_Rejections(t *testing.T) {
	cases := map[string]string{
		"empty list":        `{"sources": []}`,
		"unknown field":     `{"sources": [{"id": "a", "org": "o", "surprise": 1}]}`,
		"bad id":            `{"sources": [{"id": "Atab_Group", "org": "o"}]}`,
		"missing org":       `{"sources": [{"id": "a"}]}`,
		"bad duration":      `{"sources": [{"id": "a", "org": "o", "stale_after": "soon"}]}`,
		"unknown authority": `{"sources": [{"id": "a", "org": "o", "authority": "supreme"}]}`,
		// A stale_after below refresh_interval would make every snapshot
		// born stale.
		"incoherent freshness": `{"sources": [{"id": "a", "org": "o",
			"refresh_interval": "10m", "stale_after": "1m"}]}`,
	}
	for name, body := range cases {
		if _, _, err := loadATABSources(writeSources(t, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, _, err := loadATABSources(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("a missing sources file must be refused")
	}
}

// A duplicate source id would make the id prefix stop being unique, so
// the registry refuses the file rather than silently keeping one entry.
func TestBuildATABRegistry_RejectsDuplicateSourceIDs(t *testing.T) {
	path := writeSources(t, `{
  "sources": [
    {"id": "atab-group", "org": "Atab-Group", "project_number": 1},
    {"id": "atab-group", "org": "Other-Org", "project_number": 2}
  ]
}`)
	if _, _, err := buildATABRegistry(config.ServeConfig{ATAB: true, ATABSources: path}); err == nil {
		t.Fatal("a duplicate source id was accepted")
	}
}

// A source may be pinned to a fixture while its siblings stay live,
// which is how one org is kept offline without taking the board down.
func TestBuildATABRegistry_PerSourceFixture(t *testing.T) {
	fixture := writeFixture(t, "partner-org", atab.SecondaryDemoIssues())
	path := writeSources(t, `{
  "sources": [
    {"id": "atab-group", "org": "Atab-Group", "project_number": 1, "authority": "canonical"},
    {"id": "partner-org", "org": "Partner-Org", "project_number": 4,
     "fixture": "`+fixture+`"}
  ]
}`)
	reg, _, err := buildATABRegistry(config.ServeConfig{ATAB: true, ATABSources: path})
	if err != nil {
		t.Fatalf("buildATABRegistry: %v", err)
	}
	if got := reg.Sources(); len(got) != 2 {
		t.Fatalf("sources = %v, want both", got)
	}
	if !reg.Allowed("partner-org") {
		t.Error("the fixture-backed source is not allowlisted")
	}
}

// The plane the server actually holds is the wrapped one, not the
// adaptor. This asserts the whole seam, because the failure it catches
// is silent: the shader wrapper dropped core.ActivityReader once, and
// the history route answered "this adaptor keeps no history" for an
// adaptor that reads it.
func TestATABWorkPlane_KeepsItsHistorySurfaceThroughTheShader(t *testing.T) {
	path := writeFixture(t, atab.DemoSourceID, atab.DemoIssues())
	reg, _, err := buildATABRegistry(config.ServeConfig{ATAB: true, ATABFixture: path})
	if err != nil {
		t.Fatalf("buildATABRegistry: %v", err)
	}
	wrapped := shader.Wrap(atab.New(reg, core.TransportAPI), nil)
	if _, ok := wrapped.(core.ActivityReader); !ok {
		t.Fatal("the registered plane does not implement core.ActivityReader")
	}
}
