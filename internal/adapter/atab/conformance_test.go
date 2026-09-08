package atab_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/GembaCore/gemba-core/core"
	"github.com/GembaCore/gemba-core/internal/adapter/atab"
	gembatesting "github.com/GembaCore/gemba-core/testing"
)

const (
	seededID   = core.WorkItemID("atab-group/Atab-Group~Product-Seela/302")
	blockerID  = core.WorkItemID("atab-group/Atab-Group~Product-Seela/301")
	inFlightID = core.WorkItemID("atab-group/Atab-Group~Product-Seela/304")
)

// TestATABWorkPlaneConformance runs the shared adaptor contract suite.
//
// It is driven by the fixture client rather than the live gh client on
// purpose: the groups assert contract shape, not GitHub availability,
// and a suite that needs a network and a credential is a suite that
// stops being run.
//
// The two Group J hooks stage their scenarios by changing what the
// fixture source reports, which is exactly where those changes come from
// in production. The adaptor stays read-only throughout; what is being
// tested is that work-item state lives on the issue and therefore
// survives the worker that was holding it.
func TestATABWorkPlaneConformance(t *testing.T) {
	client := atab.NewFixtureClient(atab.DemoSourceID, atab.DemoIssues())
	reg := atab.NewRegistry()
	if err := reg.Add(atab.DemoSourceConfig(), client); err != nil {
		t.Fatalf("add source: %v", err)
	}
	wp := atab.New(reg, core.TransportAPI)

	gembatesting.RunWorkPlaneConformance(t, wp, &gembatesting.WorkPlaneFixture{
		KnownMissingID: core.WorkItemID("atab-group/Atab-Group~Product-Seela/999999"),

		SeedWorkItemWithEdges: func(core.WorkPlane) (core.WorkItemID, []core.Relationship, error) {
			// Fixture #302 already carries every edge shape the adaptor
			// projects: a blocked_by inverted into a core blocks edge,
			// and a discovered_from rendered as relates_to.
			return seededID, []core.Relationship{
				{Kind: core.RelBlocks, From: blockerID, To: seededID},
				{Kind: core.RelRelatesTo, From: blockerID, To: seededID},
			}, nil
		},

		SessionDeathRecovery: func(impl core.WorkPlane) error {
			return sessionDeathRecovery(impl, client, reg)
		},
		WorkPickupBySecondAgent: func(impl core.WorkPlane) error {
			return workPickupBySecondAgent(impl, client, reg)
		},
	})
}

// sessionDeathRecovery asserts R6: a worker dying mid-task loses nothing.
// The issue's own state (its criteria, its board row, its evidence)
// is unchanged by the worker's disappearance, and only the lease lapses.
func sessionDeathRecovery(impl core.WorkPlane, client *atab.FixtureClient, reg *atab.Registry) error {
	ctx := context.Background()
	before, err := impl.GetWorkItem(ctx, inFlightID)
	if err != nil {
		return fmt.Errorf("read the claimed item: %w", err)
	}
	if before.Custom[atab.FieldKeyReadiness] != string(atab.ReadyInFlight) {
		return fmt.Errorf("item %q is not in flight before the simulated death: %v",
			inFlightID, before.Custom[atab.FieldKeyReadiness])
	}

	client.Replace(withExpiredLease(atab.DemoIssues()))
	if errs := reg.Refresh(ctx); len(errs) > 0 {
		return fmt.Errorf("refresh after the simulated death: %v", errs)
	}

	after, err := impl.GetWorkItem(ctx, inFlightID)
	if err != nil {
		return fmt.Errorf("read the item after the worker died: %w", err)
	}
	if after.Title != before.Title {
		return fmt.Errorf("title changed across the death: %q then %q", before.Title, after.Title)
	}
	if before.DoD == nil || after.DoD == nil ||
		len(after.DoD.AcceptanceCriteria) != len(before.DoD.AcceptanceCriteria) {
		return fmt.Errorf("the acceptance criteria did not survive the death: %+v then %+v",
			before.DoD, after.DoD)
	}
	if after.Custom[atab.FieldKeyBoardStatus] != before.Custom[atab.FieldKeyBoardStatus] {
		return fmt.Errorf("the board row changed across the death: %v then %v",
			before.Custom[atab.FieldKeyBoardStatus], after.Custom[atab.FieldKeyBoardStatus])
	}
	lease, ok := after.Custom[atab.FieldKeyLease].(map[string]any)
	if !ok {
		return fmt.Errorf("the dead worker's lease vanished; it is the only trace of the death")
	}
	if lease["fresh"] != false {
		return fmt.Errorf("lease still reads fresh after the worker died: %v", lease)
	}
	return nil
}

// workPickupBySecondAgent asserts the other half of R6: once the first
// worker's lease has lapsed, the issue is offered again with its state
// intact, so a second worker resumes rather than restarts.
func workPickupBySecondAgent(impl core.WorkPlane, client *atab.FixtureClient, reg *atab.Registry) error {
	ctx := context.Background()
	client.Replace(withExpiredLease(atab.DemoIssues()))
	if errs := reg.Refresh(ctx); len(errs) > 0 {
		return fmt.Errorf("refresh: %v", errs)
	}

	item, err := impl.GetWorkItem(ctx, inFlightID)
	if err != nil {
		return fmt.Errorf("read the released item: %w", err)
	}
	// The board still says In Progress, which is the honest state: a
	// human moves it, not this adaptor. What matters for pickup is that
	// the lease has lapsed and is visible as lapsed.
	lease, ok := item.Custom[atab.FieldKeyLease].(map[string]any)
	if !ok || lease["fresh"] != false {
		return fmt.Errorf("a second worker cannot tell the claim is dead: %v",
			item.Custom[atab.FieldKeyLease])
	}
	if lease["holder"] != "claude-1" {
		return fmt.Errorf("the first worker's identity was lost: %v", lease)
	}
	if item.Assignee == nil {
		return fmt.Errorf("the assignee did not survive the first worker's death")
	}
	if item.DoD == nil || len(item.DoD.AcceptanceCriteria) == 0 {
		return fmt.Errorf("the second worker has no criteria to resume against")
	}
	return nil
}

// withExpiredLease returns the fixture set with the in-flight issue's
// lease moved into the past, which is what GitHub shows once a worker
// has stopped renewing.
func withExpiredLease(issues []atab.Issue) []atab.Issue {
	out := make([]atab.Issue, len(issues))
	copy(out, issues)
	for i := range out {
		if out[i].Ref.Number != 304 {
			continue
		}
		comments := make([]atab.Comment, len(out[i].Comments))
		copy(comments, out[i].Comments)
		for j := range comments {
			if len(comments[j].Body) > 16 && comments[j].Body[:16] == "<!-- atab-lease " {
				comments[j].Body = "<!-- atab-lease holder=claude-1 instance=w-7742 " +
					"expires=2026-09-07T11:45:00Z -->"
			}
		}
		out[i].Comments = comments
		out[i].UpdatedAt = time.Now().UTC()
	}
	return out
}
