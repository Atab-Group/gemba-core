package atab

import "time"

// DemoSourceID is the source id the bundled fixture set is filed under.
const DemoSourceID SourceID = "atab-group"

// SecondaryDemoSourceID is the second source the federation fixtures
// use. Its issue numbers deliberately collide with the primary source's
// so the collision-safety of the id scheme is exercised rather than
// assumed.
const SecondaryDemoSourceID SourceID = "partner-org"

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic("atab: bad fixture timestamp " + s)
	}
	return t.UTC()
}

func metaBlock(yaml string) string {
	return "\n<!-- atab-meta: managed by atab-core skills, do not edit by hand -->\n" +
		"```yaml\n" + yaml + "```\n"
}

// DemoIssues is the representative Atab-Group Project #1 fixture set.
//
// It is not a sample of convenience. Each entry stands for one state the
// projection has to get right, and together they cover every branch the
// readiness rules can take: ready, blocked same-repo, blocked
// cross-source, in flight under a live lease, an expired lease left by a
// dead worker, an epic with open children, a drained epic, a parked
// issue, an untracked issue, a malformed block, a cycle, an orphan, a
// board status the mapping does not know, an issue with full pull
// request evidence, and both closed shapes.
//
// Lease expiries are absolute and far apart on purpose: the live one
// sits in 2030 and the dead one in the past, so the set reads the same
// under a pinned test clock and under the wall clock the demo server
// runs on.
func DemoIssues() []Issue {
	return []Issue{
		// Ready: triaged to Todo, tracked, unblocked, unassigned.
		{
			Ref:   IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 301},
			Title: "Show a price range on the Services card",
			Body: "## What\n\nServices carrying a minimum and a maximum price should " +
				"show the range rather than the minimum alone.\n\n" +
				"## Acceptance Criteria\n\n" +
				"- [ ] A service with a min and a max renders \"R120 to R400\" on the card\n" +
				"- [ ] A service with a single price renders that price unchanged\n" +
				"- [x] The catalogue list and the detail page agree\n" +
				metaBlock("type: feature\nautonomy: pr\n"),
			URL:       "https://github.com/Atab-Group/Product-Seela/issues/301",
			State:     "OPEN",
			Labels:    []string{"type:feature", "priority:p1", "agent:available"},
			Author:    "mike21pentz",
			CreatedAt: ts("2026-08-30T08:00:00Z"),
			UpdatedAt: ts("2026-09-07T09:15:00Z"),
			ProjectItem: &ProjectItem{
				ItemID: "PVTI_ready", ProjectNumber: 1, ProjectTitle: "Atab Group Tasks",
				Fields: map[string]string{
					"Status": StatusTodo, "Priority": "P1", "Area": "Product", "Estimate": "S",
				},
			},
		},
		// Blocked by an open same-repo issue, and carrying provenance.
		{
			Ref:   IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 302},
			Title: "Wire the range formatter into the recommend surface",
			Body: "## What\n\nReuse the formatter #301 introduces.\n\n" +
				"## Acceptance Criteria\n\n" +
				"- [ ] The recommend surface renders ranges through the same formatter\n" +
				metaBlock("type: feature\nautonomy: pr\nblocked_by:\n  - 301\ndiscovered_from: 301\n"),
			URL:       "https://github.com/Atab-Group/Product-Seela/issues/302",
			State:     "OPEN",
			Labels:    []string{"type:feature", "priority:p2"},
			Author:    "mike21pentz",
			CreatedAt: ts("2026-08-30T08:05:00Z"),
			UpdatedAt: ts("2026-09-07T09:10:00Z"),
			ProjectItem: &ProjectItem{
				ItemID: "PVTI_blocked", ProjectNumber: 1,
				Fields: map[string]string{"Status": StatusBacklog, "Priority": "P2", "Area": "Product"},
			},
		},
		// Blocked by a cross-repo edge into a source this deployment may
		// or may not carry. Fails closed when it does not.
		{
			Ref:   IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 303},
			Title: "Migrate the basket totals to the shared money type",
			Body: "## What\n\nOne money type across the app.\n\n" +
				"## Acceptance Criteria\n\n" +
				"- [ ] Basket totals use the shared type\n" +
				"- [ ] Rounding matches the catalogue\n" +
				metaBlock("type: task\nautonomy: auto\nblocked_by:\n  - \"Atab-Group/ATAB-Marketplace#77\"\n"),
			URL:       "https://github.com/Atab-Group/Product-Seela/issues/303",
			State:     "OPEN",
			Labels:    []string{"type:task", "priority:p2"},
			Author:    "niclom12",
			CreatedAt: ts("2026-08-31T10:00:00Z"),
			UpdatedAt: ts("2026-09-07T08:00:00Z"),
			Parent: &ParentRef{
				Ref:   IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 310},
				Title: "Epic: unified money handling", State: "OPEN",
			},
			ProjectItem: &ProjectItem{
				ItemID: "PVTI_crossrepo", ProjectNumber: 1,
				Fields: map[string]string{"Status": StatusTodo, "Priority": "P2", "Area": "Product"},
			},
		},
		// In flight: board says In Progress, a live lease is held by a
		// worker, and the GitHub assignee is a different identity.
		{
			Ref:   IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 304},
			Title: "Rebuild the checkout summary panel",
			Body: "## What\n\nOne-page checkout summary.\n\n" +
				"## Acceptance Criteria\n\n" +
				"- [x] The panel lists every line item\n" +
				"- [ ] The totals row matches the basket\n" +
				metaBlock("type: feature\nautonomy: pr\n"),
			URL:       "https://github.com/Atab-Group/Product-Seela/issues/304",
			State:     "OPEN",
			Labels:    []string{"type:feature", "priority:p0", "agent:claimed"},
			Assignees: []string{"niclom12"},
			Author:    "mike21pentz",
			CreatedAt: ts("2026-09-01T07:00:00Z"),
			UpdatedAt: ts("2026-09-07T11:40:00Z"),
			Comments: []Comment{
				{
					Body: "Claimed by claude-1 (operating as @niclom12).", Author: "niclom12",
					CreatedAt: ts("2026-09-07T11:30:00Z"), UpdatedAt: ts("2026-09-07T11:30:00Z"),
				},
				{
					Body:      "<!-- atab-lease holder=claude-1 instance=w-7742 expires=2030-01-01T00:00:00Z -->",
					Author:    "niclom12",
					CreatedAt: ts("2026-09-07T11:31:00Z"), UpdatedAt: ts("2026-09-07T11:31:00Z"),
				},
			},
			ProjectItem: &ProjectItem{
				ItemID: "PVTI_inflight", ProjectNumber: 1,
				Fields: map[string]string{
					"Status": StatusInProgress, "Priority": "P0", "Area": "Product", "Estimate": "M",
				},
			},
		},
		// An assignee whose lease has expired: the worker died holding
		// the issue. Pickable again, and the dead lease stays visible.
		{
			Ref:   IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 305},
			Title: "Backfill the supplier import audit trail",
			Body: "## What\n\nEvery import writes an audit row.\n\n" +
				"## Acceptance Criteria\n\n" +
				"- [ ] An import writes one audit row per file\n" +
				metaBlock("type: chore\nautonomy: pr\n"),
			URL:       "https://github.com/Atab-Group/Product-Seela/issues/305",
			State:     "OPEN",
			Labels:    []string{"type:chore", "priority:p3"},
			Assignees: []string{"niclom12"},
			Author:    "niclom12",
			CreatedAt: ts("2026-09-02T07:00:00Z"),
			UpdatedAt: ts("2026-09-06T18:00:00Z"),
			Comments: []Comment{{
				Body:      "<!-- atab-lease holder=claude-3 instance=w-6001 expires=2026-09-06T19:00:00Z -->",
				Author:    "niclom12",
				CreatedAt: ts("2026-09-06T18:00:00Z"), UpdatedAt: ts("2026-09-06T18:00:00Z"),
			}},
			ProjectItem: &ProjectItem{
				ItemID: "PVTI_expiredlease", ProjectNumber: 1,
				Fields: map[string]string{"Status": StatusTodo, "Priority": "P3", "Area": "Product"},
			},
		},
		// Already built, and the board has not caught up: an open pull
		// request links the issue while Status still reads Todo. This is
		// the drift the pr_open gate exists for, and it carries the full
		// evidence set: local CI, a verifier verdict and check runs.
		{
			Ref:   IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 306},
			Title: "Add a currency formatter helper",
			Body: "## What\n\nOne helper, used everywhere.\n\n" +
				"## Acceptance Criteria\n\n" +
				"- [x] The helper formats cents to a display string\n" +
				metaBlock("type: task\nautonomy: pr\n"),
			URL:       "https://github.com/Atab-Group/Product-Seela/issues/306",
			State:     "OPEN",
			Labels:    []string{"type:task", "priority:p2"},
			Author:    "niclom12",
			CreatedAt: ts("2026-09-04T07:00:00Z"),
			UpdatedAt: ts("2026-09-07T10:05:00Z"),
			ProjectItem: &ProjectItem{
				ItemID: "PVTI_propen", ProjectNumber: 1,
				Fields: map[string]string{"Status": StatusTodo, "Priority": "P2", "Area": "Product"},
			},
			LinkedPRs: []PullRequest{{
				Ref:         IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 307},
				Title:       "feat(money): add a currency formatter helper",
				URL:         "https://github.com/Atab-Group/Product-Seela/pull/307",
				State:       "OPEN",
				HeadSHA:     "9f1c2d4a6b8e0f2a4c6e8a0b2d4f6a8c0e2a4c6e",
				CheckRollup: "SUCCESS",
				UpdatedAt:   ts("2026-09-07T10:05:00Z"),
				Checks: []CheckRun{
					{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS",
						URL: "https://github.com/Atab-Group/Product-Seela/actions/runs/1"},
					{Name: "lint", Status: "COMPLETED", Conclusion: "SUCCESS",
						URL: "https://github.com/Atab-Group/Product-Seela/actions/runs/2"},
				},
				Comments: []Comment{
					{
						Body: "<!-- atab-local-ci -->\n```json\n{\"head_sha\":" +
							"\"9f1c2d4a6b8e0f2a4c6e8a0b2d4f6a8c0e2a4c6e\",\"all_green\":true," +
							"\"source\":\"ci.json\",\"run_by\":\"work\",\"commands\":[" +
							"{\"name\":\"test\",\"cmd\":\"npm test\",\"exit\":0,\"duration_s\":41}," +
							"{\"name\":\"lint\",\"cmd\":\"npm run lint\",\"exit\":0,\"duration_s\":6}]}\n```",
						Author:    "niclom12",
						CreatedAt: ts("2026-09-07T09:50:00Z"), UpdatedAt: ts("2026-09-07T09:50:00Z"),
					},
					{
						Body: "<!-- atab-verify -->\n```json\n{\"verifier\":\"verifier\"," +
							"\"task_id\":\"Product-Seela#306\",\"verified_at\":\"2026-09-07T10:00:00Z\"," +
							"\"overall_verdict\":\"PASS\",\"bounce_back_message\":\"\"," +
							"\"criteria\":[{\"id\":\"AC-1\",\"criterion\":\"The helper formats cents " +
							"to a display string\",\"verdict\":\"PASS\",\"evidence\":[]}]}\n```",
						Author:    "niclom12",
						CreatedAt: ts("2026-09-07T10:00:00Z"), UpdatedAt: ts("2026-09-07T10:00:00Z"),
					},
				},
			}},
		},
		// A board status this adaptor's mapping does not know. Reported
		// as unknown rather than guessed into a lane.
		{
			Ref:   IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 308},
			Title: "Investigate the intermittent search timeout",
			Body: "## What\n\nSearch times out about once a day.\n\n" +
				"## Acceptance Criteria\n\n" +
				"- [ ] The cause is named with a trace\n" +
				metaBlock("type: research\nautonomy: pr\n"),
			URL:       "https://github.com/Atab-Group/Product-Seela/issues/308",
			State:     "OPEN",
			Labels:    []string{"type:research", "priority:p2"},
			Author:    "niclom12",
			CreatedAt: ts("2026-09-05T07:00:00Z"),
			UpdatedAt: ts("2026-09-07T07:30:00Z"),
			ProjectItem: &ProjectItem{
				ItemID: "PVTI_unknownstatus", ProjectNumber: 1,
				Fields: map[string]string{"Status": "Parking Lot", "Priority": "P2", "Area": "Product"},
			},
		},
		// Parked by a needs-human label.
		{
			Ref:   IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 309},
			Title: "Decide the refund policy copy",
			Body: "## What\n\nLegal wording.\n\n" +
				"## Acceptance Criteria\n\n" +
				"- [ ] The copy is signed off\n" +
				metaBlock("type: task\nautonomy: pr\n"),
			URL:       "https://github.com/Atab-Group/Product-Seela/issues/309",
			State:     "OPEN",
			Labels:    []string{"type:task", "priority:p2", "needs-human"},
			Author:    "niclom12",
			CreatedAt: ts("2026-09-05T08:00:00Z"),
			UpdatedAt: ts("2026-09-07T07:00:00Z"),
			ProjectItem: &ProjectItem{
				ItemID: "PVTI_parked", ProjectNumber: 1,
				Fields: map[string]string{"Status": StatusTodo, "Priority": "P2", "Area": "Product"},
			},
		},
		// Epic with an open child: tracker-only.
		{
			Ref:       IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 310},
			Title:     "Epic: unified money handling",
			Body:      "Tracker only." + metaBlock("type: epic\nautonomy: pr\n"),
			URL:       "https://github.com/Atab-Group/Product-Seela/issues/310",
			State:     "OPEN",
			Labels:    []string{"type:epic"},
			Author:    "niclom12",
			CreatedAt: ts("2026-08-20T07:00:00Z"),
			UpdatedAt: ts("2026-09-07T07:00:00Z"),
			SubIssues: []SubIssueRef{
				{Ref: IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 303},
					Title: "Migrate the basket totals to the shared money type", State: "OPEN"},
				{Ref: IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 311},
					Title: "Round consistently in the catalogue", State: "CLOSED"},
			},
			ProjectItem: &ProjectItem{
				ItemID: "PVTI_epic", ProjectNumber: 1,
				Fields: map[string]string{"Status": StatusBacklog, "Priority": "P2", "Area": "Product"},
			},
		},
		// Drained epic: every child closed, nobody assigned. Reconcile.
		{
			Ref:   IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 312},
			Title: "Epic: WhatsApp integration",
			Body: "Tracker only.\n\n## Acceptance Criteria\n\n" +
				"- [x] Inbound messages land\n- [x] Outbound messages send\n" +
				metaBlock("type: epic\nautonomy: pr\n"),
			URL:       "https://github.com/Atab-Group/Product-Seela/issues/312",
			State:     "OPEN",
			Labels:    []string{"type:epic"},
			Author:    "niclom12",
			CreatedAt: ts("2026-08-10T07:00:00Z"),
			UpdatedAt: ts("2026-09-05T07:00:00Z"),
			SubIssues: []SubIssueRef{
				{Ref: IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 313},
					Title: "Inbound webhook", State: "CLOSED"},
				{Ref: IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 314},
					Title: "Outbound sender", State: "CLOSED"},
			},
			ProjectItem: &ProjectItem{
				ItemID: "PVTI_drained", ProjectNumber: 1,
				Fields: map[string]string{"Status": StatusTodo, "Priority": "P2", "Area": "Product"},
			},
		},
		// Orphan: the native parent is closed.
		{
			Ref:   IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 315},
			Title: "Drop the legacy webhook shim",
			Body: "## What\n\nDelete the shim.\n\n## Acceptance Criteria\n\n" +
				"- [ ] The shim is gone and nothing imports it\n" +
				metaBlock("type: chore\nautonomy: pr\n"),
			URL:       "https://github.com/Atab-Group/Product-Seela/issues/315",
			State:     "OPEN",
			Labels:    []string{"type:chore", "priority:p3"},
			Author:    "niclom12",
			CreatedAt: ts("2026-08-15T07:00:00Z"),
			UpdatedAt: ts("2026-09-04T07:00:00Z"),
			Parent: &ParentRef{
				Ref:   IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 316},
				Title: "Epic: retire the legacy gateway", State: "CLOSED",
			},
			ProjectItem: &ProjectItem{
				ItemID: "PVTI_orphan", ProjectNumber: 1,
				Fields: map[string]string{"Status": StatusTodo, "Priority": "P3", "Area": "Product"},
			},
		},
		// Untracked: no atab-meta block at all.
		{
			Ref:       IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 317},
			Title:     "Typo on the pricing page",
			Body:      "Small typo in the heading.",
			URL:       "https://github.com/Atab-Group/Product-Seela/issues/317",
			State:     "OPEN",
			Labels:    []string{"type:bug", "priority:p3"},
			Author:    "mike21pentz",
			CreatedAt: ts("2026-09-06T07:00:00Z"),
			UpdatedAt: ts("2026-09-06T07:30:00Z"),
		},
		// Malformed block: the marker is present, the YAML is not.
		{
			Ref:   IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 318},
			Title: "Tighten the import validation",
			Body: "## What\n\nValidate harder.\n" +
				"\n<!-- atab-meta: managed by atab-core skills, do not edit by hand -->\n" +
				"```yaml\ntype: [unclosed\n```\n",
			URL:       "https://github.com/Atab-Group/Product-Seela/issues/318",
			State:     "OPEN",
			Labels:    []string{"type:task", "priority:p3"},
			Author:    "niclom12",
			CreatedAt: ts("2026-09-06T08:00:00Z"),
			UpdatedAt: ts("2026-09-06T08:30:00Z"),
			ProjectItem: &ProjectItem{
				ItemID: "PVTI_malformed", ProjectNumber: 1,
				Fields: map[string]string{"Status": StatusTodo, "Priority": "P3", "Area": "Product"},
			},
		},
		// A blocked_by cycle: 320 and 321 block each other.
		{
			Ref:   IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 320},
			Title: "Split the settlement job",
			Body: "## Acceptance Criteria\n\n- [ ] The job splits cleanly\n" +
				metaBlock("type: task\nautonomy: pr\nblocked_by:\n  - 321\n"),
			URL:       "https://github.com/Atab-Group/Product-Seela/issues/320",
			State:     "OPEN",
			Labels:    []string{"type:task", "priority:p2"},
			Author:    "niclom12",
			CreatedAt: ts("2026-09-06T09:00:00Z"),
			UpdatedAt: ts("2026-09-06T09:30:00Z"),
			ProjectItem: &ProjectItem{
				ItemID: "PVTI_cycle_a", ProjectNumber: 1,
				Fields: map[string]string{"Status": StatusTodo, "Priority": "P2", "Area": "Internal"},
			},
		},
		{
			Ref:   IssueRef{Owner: "Atab-Group", Repo: "Product-Seela", Number: 321},
			Title: "Rewrite the settlement reconciler",
			Body: "## Acceptance Criteria\n\n- [ ] Reconciliation matches the ledger\n" +
				metaBlock("type: task\nautonomy: pr\nblocked_by:\n  - 320\n"),
			URL:       "https://github.com/Atab-Group/Product-Seela/issues/321",
			State:     "OPEN",
			Labels:    []string{"type:task", "priority:p2"},
			Author:    "niclom12",
			CreatedAt: ts("2026-09-06T09:05:00Z"),
			UpdatedAt: ts("2026-09-06T09:35:00Z"),
			ProjectItem: &ProjectItem{
				ItemID: "PVTI_cycle_b", ProjectNumber: 1,
				Fields: map[string]string{"Status": StatusTodo, "Priority": "P2", "Area": "Internal"},
			},
		},
		// Human-only autonomy, plus a requires_env declaration.
		{
			Ref:   IssueRef{Owner: "Atab-Group", Repo: "ATAB-Marketplace", Number: 401},
			Title: "Rotate the payment webhook secret",
			Body: "## What\n\nRotate and re-verify.\n\n## Acceptance Criteria\n\n" +
				"- [ ] The webhook verifies against the new secret\n" +
				metaBlock("type: task\nautonomy: human\nrequires_env:\n"+
					"  - STRIPE_WEBHOOK_SECRET\n"+
					"  - name: STRIPE_API_KEY\n    from: \"secret:stripe/api-key\"\n    scope: both\n"),
			URL:       "https://github.com/Atab-Group/ATAB-Marketplace/issues/401",
			State:     "OPEN",
			Labels:    []string{"type:task", "priority:p1"},
			Author:    "niclom12",
			CreatedAt: ts("2026-09-03T07:00:00Z"),
			UpdatedAt: ts("2026-09-07T06:00:00Z"),
			ProjectItem: &ProjectItem{
				ItemID: "PVTI_human", ProjectNumber: 1,
				Fields: map[string]string{"Status": StatusTodo, "Priority": "P1", "Area": "Internal"},
			},
		},
		// Done, and cancelled: the two closed shapes, which must land in
		// different core buckets.
		{
			Ref:         IssueRef{Owner: "Atab-Group", Repo: "ATAB-Marketplace", Number: 402},
			Title:       "Ship the supplier onboarding email",
			Body:        "## Acceptance Criteria\n\n- [x] The email sends on approval\n" + metaBlock("type: feature\nautonomy: pr\n"),
			URL:         "https://github.com/Atab-Group/ATAB-Marketplace/issues/402",
			State:       "CLOSED",
			StateReason: "COMPLETED",
			Labels:      []string{"type:feature", "priority:p1"},
			Author:      "niclom12",
			CreatedAt:   ts("2026-08-01T07:00:00Z"),
			UpdatedAt:   ts("2026-09-02T07:00:00Z"),
			ProjectItem: &ProjectItem{
				ItemID: "PVTI_done", ProjectNumber: 1,
				Fields: map[string]string{"Status": StatusDone, "Priority": "P1", "Area": "Internal"},
			},
		},
		{
			Ref:         IssueRef{Owner: "Atab-Group", Repo: "ATAB-Marketplace", Number: 403},
			Title:       "Consider native price ranges on Services",
			Body:        "Superseded.",
			URL:         "https://github.com/Atab-Group/ATAB-Marketplace/issues/403",
			State:       "CLOSED",
			StateReason: "NOT_PLANNED",
			Labels:      []string{"type:feature", "priority:p3", "stale"},
			Author:      "mike21pentz",
			CreatedAt:   ts("2026-05-24T16:50:23Z"),
			UpdatedAt:   ts("2026-09-07T05:24:59Z"),
		},
	}
}

// SecondaryDemoIssues is the second federated source's fixture set.
//
// Its issue numbers repeat the primary source's on purpose. Two sources
// carrying "Product-Seela#301" is exactly the collision the qualified id
// scheme has to survive, so the fixtures create it rather than avoiding
// it.
func SecondaryDemoIssues() []Issue {
	return []Issue{
		{
			Ref:   IssueRef{Owner: "Partner-Org", Repo: "Product-Seela", Number: 301},
			Title: "Partner: reconcile the nightly export",
			Body: "## Acceptance Criteria\n\n- [ ] The export reconciles to the ledger\n" +
				metaBlock("type: task\nautonomy: pr\n"),
			URL:       "https://github.com/Partner-Org/Product-Seela/issues/301",
			State:     "OPEN",
			Labels:    []string{"type:task", "priority:p2"},
			Author:    "partner-dev",
			CreatedAt: ts("2026-09-01T07:00:00Z"),
			UpdatedAt: ts("2026-09-07T09:00:00Z"),
			ProjectItem: &ProjectItem{
				ItemID: "PVTI_partner_ready", ProjectNumber: 4,
				Fields: map[string]string{"Status": StatusTodo, "Priority": "P2"},
			},
		},
		{
			Ref:         IssueRef{Owner: "Partner-Org", Repo: "Product-Seela", Number: 302},
			Title:       "Partner: retire the v1 export format",
			Body:        "Done." + metaBlock("type: chore\nautonomy: pr\n"),
			URL:         "https://github.com/Partner-Org/Product-Seela/issues/302",
			State:       "CLOSED",
			StateReason: "COMPLETED",
			Labels:      []string{"type:chore"},
			Author:      "partner-dev",
			CreatedAt:   ts("2026-08-01T07:00:00Z"),
			UpdatedAt:   ts("2026-09-01T07:00:00Z"),
		},
	}
}

// DemoSourceConfig is the primary source's configuration, matching
// Atab-Group Project #1.
func DemoSourceConfig() SourceConfig {
	return SourceConfig{
		ID:            DemoSourceID,
		Org:           "Atab-Group",
		ProjectNumber: 1,
		Repos:         []string{"Product-Seela", "ATAB-Marketplace"},
		Display:       "Atab Group Tasks",
		Authority:     AuthorityCanonical,
	}
}

// SecondaryDemoSourceConfig is the second source's configuration.
func SecondaryDemoSourceConfig() SourceConfig {
	return SourceConfig{
		ID:            SecondaryDemoSourceID,
		Org:           "Partner-Org",
		ProjectNumber: 4,
		Repos:         []string{"Product-Seela"},
		Display:       "Partner delivery board",
		Authority:     AuthorityReference,
	}
}
