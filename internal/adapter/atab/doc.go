// Package atab implements a read-only core.WorkPlane over GitHub Issues
// and GitHub Projects v2 as the Atab-Group org runs them.
//
// GitHub is canonical. This adaptor never writes: every mutation method
// returns a tagged core.AdaptorError with KindReadOnly, and the manifest
// sets ReadOnly=true so the SPA hides write controls rather than
// disabling them. The dashboard is a projection; the org's issues,
// project board and pull requests stay the single source of truth and
// are only ever read.
//
// The projection has three layers, built in three stages:
//
//	Stage 0: the WorkPlane contract itself: capability manifest, the
//	  qualified WorkItemID scheme, the atab-meta block parser, and a
//	  single issue rendered onto core.WorkItem.
//	Stage 1: the full Atab-Group Project #1 projection: board Status
//	  and Priority, atab-meta type/autonomy plus the acceptance-criteria
//	  checklist, GitHub-native parent/sub-issue edges, blocked_by and
//	  discovered_from edges, readiness derived from the same rules as
//	  ready_select.py, an assignee that stays distinct from an issue
//	  lease, evidence drawn from pull requests, local-CI runs, verifier
//	  verdicts and check runs, explicit stale and unknown states, and a
//	  snapshot cache with incremental refresh.
//	Stage 2: federation across several org / project / repo sources,
//	  each with its own mapping, authority, health and freshness, behind
//	  an explicit allowlist. See package federation.
//
// The adaptor reads atab-meta schema v5 (and every earlier shape, which
// v5 parses unchanged). It neither implements nor emits a v6.
package atab
