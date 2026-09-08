package atab

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/GembaCore/gemba-core/core"
)

var updateFixtures = flag.Bool("update-fixtures", false,
	"rewrite the checked-in fixture files from DemoIssues")

const (
	primaryFixturePath   = "testdata/atab-group-project-1.json"
	secondaryFixturePath = "testdata/partner-org.json"
)

// TestFixtureFilesMatchTheCode keeps the checked-in fixture JSON in step
// with the Go fixture set.
//
// The JSON is a real artifact, not a test byproduct: it is what
// `gemba serve --atab --atab-fixture` replays to stand the dashboard up
// with no network and no credential. Keeping one definition and
// generating the other stops the two drifting into disagreement about
// what the board is supposed to show.
//
// Run `go test ./internal/adapter/atab -update-fixtures` to regenerate.
func TestFixtureFilesMatchTheCode(t *testing.T) {
	cases := []struct {
		path   string
		source SourceID
		issues []Issue
	}{
		{primaryFixturePath, DemoSourceID, DemoIssues()},
		{secondaryFixturePath, SecondaryDemoSourceID, SecondaryDemoIssues()},
	}

	for _, tc := range cases {
		want, err := json.MarshalIndent(FixtureFile{Source: tc.source, Issues: tc.issues}, "", "  ")
		if err != nil {
			t.Fatalf("marshal %s: %v", tc.path, err)
		}
		want = append(want, '\n')

		if *updateFixtures {
			if err := os.MkdirAll(filepath.Dir(tc.path), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(tc.path, want, 0o644); err != nil {
				t.Fatalf("write %s: %v", tc.path, err)
			}
			continue
		}

		got, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("read %s (run with -update-fixtures to create it): %v", tc.path, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s is out of date; run "+
				"`go test ./internal/adapter/atab -update-fixtures`", tc.path)
		}
	}
}

// The checked-in file has to load back through the same path the serve
// mode uses, or the offline demo breaks without any test noticing.
func TestFixtureFileLoadsAndProjects(t *testing.T) {
	if *updateFixtures {
		t.Skip("regenerating fixtures")
	}
	client, file, err := LoadFixture(primaryFixturePath)
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}
	if file.Source != DemoSourceID {
		t.Fatalf("source = %q", file.Source)
	}
	reg := NewRegistry()
	if err := reg.Add(DemoSourceConfig(), client); err != nil {
		t.Fatalf("add: %v", err)
	}
	items, err := New(reg, core.TransportAPI).ListWorkItems(context.Background(), core.WorkItemFilter{})
	if err != nil {
		t.Fatalf("ListWorkItems: %v", err)
	}
	if len(items) != len(DemoIssues()) {
		t.Fatalf("items = %d, want %d", len(items), len(DemoIssues()))
	}
}
