package atab

import "github.com/GembaCore/gemba-core/core"

// AdaptorName is the manifest identity the SPA keys extension renderers
// off (web/src/extensions/<adaptor-id>/).
const AdaptorName = "atab-github"

// AdaptorVersion tracks this adaptor's own cadence, independent of the
// core protocol version.
const AdaptorVersion = "0.3.0"

// Native status tokens this adaptor emits. The first five are the
// Atab-Group Project #1 Status options verbatim; the rest cover issues
// the board does not carry and the two explicit degraded states.
const (
	StatusBacklog    = "Backlog"
	StatusTodo       = "Todo"
	StatusInProgress = "In Progress"
	StatusInReview   = "In Review"
	StatusDone       = "Done"

	// StatusOpen: an open issue with no row on the board. Real and
	// common: an issue filed but not yet triaged onto the project.
	StatusOpen = "open"
	// StatusClosedCompleted: closed with GitHub state_reason
	// COMPLETED, and not on the board.
	StatusClosedCompleted = "closed"
	// StatusClosedNotPlanned: closed with state_reason NOT_PLANNED.
	// This is the one token that maps to the canceled bucket.
	StatusClosedNotPlanned = "not_planned"

	// StatusUnknown: the projection could not determine a status. The
	// board row is missing a Status value the source's mapping knows,
	// or GitHub returned a state token this adaptor does not recognise.
	// Emitted rather than guessed: a card in the wrong lane is worse
	// than a card that says it does not know.
	StatusUnknown = "unknown"
	// StatusStale: the source could not be refreshed inside its
	// freshness budget and no prior status was ever observed for this
	// item. An item with a prior status keeps it and is flagged through
	// the atab_freshness field instead.
	StatusStale = "stale"
)

// StateMap is the declared translation from every native status above to
// a core bucket. Every token the adaptor can emit appears here; a gap
// would be a conformance failure and would push the SPA onto its
// unknown-state fallback.
var StateMap = core.StateMap{
	StatusBacklog:    core.StateBacklog,
	StatusTodo:       core.StateUnstarted,
	StatusInProgress: core.StateStarted,
	StatusInReview:   core.StateStarted,
	StatusDone:       core.StateCompleted,

	StatusOpen:             core.StateBacklog,
	StatusClosedCompleted:  core.StateCompleted,
	StatusClosedNotPlanned: core.StateCanceled,

	StatusUnknown: core.StateBacklog,
	StatusStale:   core.StateBacklog,
}

// Custom field keys the adaptor writes onto WorkItem.Custom. Each one is
// declared as a FieldExtension so the SPA knows it exists and which
// renderer to reach for.
const (
	FieldKeySource      = "atab_source"
	FieldKeySourceOrg   = "atab_source_org"
	FieldKeyRepo        = "atab_repo"
	FieldKeyIssueNumber = "atab_issue_number"
	FieldKeyType        = "atab_type"
	FieldKeyAutonomy    = "atab_autonomy"
	FieldKeyCriteria    = "atab_acceptance_criteria"
	FieldKeyReadiness   = "atab_readiness"
	FieldKeyReadyReason = "atab_readiness_reason"
	FieldKeyLease       = "atab_lease"
	// FieldKeyClaim carries the whole claim protocol answer: state,
	// holder, instance, expiry and last heartbeat. It is separate from
	// the GitHub assignee, which stays on the core Assignee field.
	FieldKeyClaim = "atab_claim"
	// FieldKeyGraph carries the issue's immediate neighbourhood:
	// blockers, dependents, parent, children and provenance, each with
	// the state of the edge. Present on a single-item read only.
	FieldKeyGraph = "atab_graph"
	// FieldKeyClaimState is the same answer reduced to one token, for a
	// card or a table column that has room for a chip rather than a
	// struct.
	FieldKeyClaimState = "atab_claim_state"
	// FieldKeyClaimedBy names the holder, or is absent when nothing holds
	// the issue. A column bound to this reads as "Claimed by".
	FieldKeyClaimedBy = "atab_claimed_by"
	// FieldKeyProject is the qualified identity of the Projects v2 board
	// this issue sits on: "<source>#<number>". It is the project axis,
	// and it is deliberately not the source: a source is an org plus a
	// credential and a repository allowlist, while a project is one board
	// inside it. Absent when the issue is on no board, which is a real
	// and common answer rather than a gap.
	FieldKeyProject = "atab_project"
	// FieldKeyProjectTitle is the board's own name, as GitHub spells it.
	FieldKeyProjectTitle = "atab_project_title"
	// FieldKeyProjects lists every board the issue was observed on, not
	// only the source's configured one. An issue can sit on a team board
	// and a portfolio board at once, and a filter that knew only about
	// the configured board would call such an item unfiled.
	FieldKeyProjects     = "atab_projects"
	FieldKeyBoardStatus  = "atab_board_status"
	FieldKeyPriority     = "atab_priority"
	FieldKeyArea         = "atab_area"
	FieldKeyEstimate     = "atab_estimate"
	FieldKeyFreshness    = "atab_freshness"
	FieldKeyObservedAt   = "atab_observed_at"
	FieldKeyMetaState    = "atab_meta_state"
	FieldKeyWarnings     = "atab_warnings"
	FieldKeyRequiresEnv  = "atab_requires_env"
	FieldKeyScopeClauses = "atab_scope_clauses"
	FieldKeySubIssues    = "atab_sub_issue_progress"
)

// Edge extension names for the two atab-native relationships that have
// no core equivalent. blocked_by and the native parent link both map
// onto core edges and so are not extensions.
const (
	// EdgeDiscoveredFrom is provenance: the issue whose work surfaced
	// this one. Directed, no inverse, never blocking.
	EdgeDiscoveredFrom = "atab:discovered_from"
)

// Manifest returns the adaptor's declared capabilities.
//
// ReadOnly is true and never configurable. GitHub and the org's project
// board stay canonical; this adaptor is a projection, so every mutation
// method fails with KindReadOnly and the SPA hides write controls.
func Manifest(transport core.Transport) core.CapabilityManifest {
	return core.CapabilityManifest{
		AdaptorName:     AdaptorName,
		AdaptorVersion:  AdaptorVersion,
		ProtocolVersion: core.ProtocolVersion,
		Transport:       transport,
		StateMap:        StateMap,

		ReadOnly:          true,
		DescriptionFormat: core.DescriptionFormatMarkdown,

		SprintNative:              false,
		TokenBudgetEnforced:       false,
		EvidenceSynthesisRequired: false,

		EdgeExtensions: []core.EdgeExtension{{
			Name:     EdgeDiscoveredFrom,
			Directed: true,
			Description: "Provenance edge from atab-meta: the issue whose work " +
				"surfaced this one. Never blocking.",
		}},

		FieldExtensions: []core.FieldExtension{
			{Name: FieldKeySource, Type: "string", Description: "Federated source id."},
			{Name: FieldKeySourceOrg, Type: "string", Description: "GitHub organisation login."},
			{Name: FieldKeyRepo, Type: "string", Description: "owner/repo the issue lives in."},
			{Name: FieldKeyIssueNumber, Type: "number", Description: "GitHub issue number."},
			{Name: FieldKeyType, Type: "string", Description: "atab-meta type."},
			{Name: FieldKeyAutonomy, Type: "string", Description: "atab-meta autonomy tier: auto, pr or human."},
			{Name: FieldKeyCriteria, Type: "checklist", Description: "Acceptance-criteria checklist parsed from the issue body."},
			{Name: FieldKeyReadiness, Type: "string", Description: "Readiness state derived by the ready_select rules."},
			{Name: FieldKeyReadyReason, Type: "string", Description: "Why the item holds its readiness state."},
			{Name: FieldKeyLease, Type: "string", Description: "Active atab-lease holder, distinct from the GitHub assignee."},
			{Name: FieldKeyClaim, Type: "object", Description: "Claim protocol state: holder, instance, expiry, last heartbeat."},
			{Name: FieldKeyGraph, Type: "object", Description: "Immediate graph: blockers, dependents, parent, children, provenance. Single-item reads only."},
			{Name: FieldKeyClaimState, Type: "string", Description: "One of active, stale, expired, claiming, none, unknown."},
			{Name: FieldKeyClaimedBy, Type: "string", Description: "Claim holder login. Absent when nothing holds the issue."},
			{Name: FieldKeyProject, Type: "string", Description: "Qualified Projects v2 board identity, <source>#<number>. Absent when the issue is on no board."},
			{Name: FieldKeyProjectTitle, Type: "string", Description: "Projects v2 board title as GitHub spells it."},
			{Name: FieldKeyProjects, Type: "object", Description: "Every Projects v2 board the issue was observed on, each with id, number and title."},
			{Name: FieldKeyBoardStatus, Type: "string", Description: "Raw project-board Status option."},
			{Name: FieldKeyPriority, Type: "string", Description: "Project-board Priority option."},
			{Name: FieldKeyArea, Type: "string", Description: "Project-board Area option."},
			{Name: FieldKeyEstimate, Type: "string", Description: "Project-board Estimate option."},
			{Name: FieldKeyFreshness, Type: "string", Description: "fresh, stale or unknown for the snapshot behind this card."},
			{Name: FieldKeyObservedAt, Type: "string", Description: "RFC3339 instant the source was last read."},
			{Name: FieldKeyMetaState, Type: "string", Description: "present, absent or malformed for the atab-meta block."},
			{Name: FieldKeyWarnings, Type: "string", Description: "Non-fatal projection warnings."},
			{Name: FieldKeyRequiresEnv, Type: "string", Description: "atab-meta requires_env entries."},
			{Name: FieldKeyScopeClauses, Type: "string", Description: "atab-meta scope_clauses citations."},
			{Name: FieldKeySubIssues, Type: "string", Description: "Closed / total native sub-issues."},
		},

		// R1–R8 agentic-data-plane declarations. GitHub enforces the
		// issue schema natively, models the sub-issue and closing-
		// reference graphs as first-class edges, and survives an agent
		// session's death because state lives on the issue, not in the
		// runner. It has no versioned cross-instance transport and no
		// ready-set query of its own: readiness here is derived by this
		// adaptor from the atab-meta graph, which is why ReadySetQuery
		// stays false.
		SchemaEnforcement:     core.SchemaNative,
		QueryLanguages:        []core.QueryLanguage{core.QueryFilterOnly, core.QueryGraphQL},
		DependencyGraphNative: true,
		ReadySetQuery:         false,
		VersioningTransport:   []core.VersioningTransport{core.VersioningNone},
		// Read-only: the adaptor never writes, so it never races another
		// writer. Optimistic is the honest declaration: GitHub itself
		// resolves concurrent writes that way for the writers upstream.
		ConcurrencyModel:       core.ConcurrencyOptimistic,
		AgentSessionDecoupling: true,
		AgentNativeAPI:         core.AgentAPICLI,
		OrchestratorHooks:      nil,
	}
}
