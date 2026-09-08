# ATAB WorkPlane: acceptance and recovery

What was verified before this branch was proposed, what the deployment
guarantees when things go wrong, and where the edges are. Companion to
`docs/adaptors/atab.md`, which is the reference; this file is the
evidence.

No credentials appear here or anywhere in the repository. Reads go
through the authenticated `gh` CLI, so the adaptor never handles a
token.

## What it is

A read-only projection of GitHub Issues and Projects v2 onto a Gemba
board, served by `gemba serve --atab` as a `systemd --user` unit that
depends on nothing else on the host.

Read-only is structural, not conventional. `CreateWorkItem` and
`UpdateWorkItem` return a tagged `read_only` error, `AttachEvidence`
returns `capability_denied`, and the manifest sets `read_only: true` so
the SPA hides write controls. Nothing below the client interface can
mutate a GitHub issue, a project row or a comment.

## Verification

| Suite                         | Result                        |
| ----------------------------- | ----------------------------- |
| `go build ./...`              | exit 0                        |
| `go vet ./...`                | exit 0                        |
| `go test ./...`               | exit 0, 127 packages ok       |
| `golangci-lint run ./...`     | exit 0, 0 issues              |
| `gofmt -l`                    | clean                         |
| `go test -race` (atab, server, cli, config) | exit 0           |
| `vitest run` (web)            | exit 0, 124 files, 1119 tests |
| `tsc --noEmit`                | exit 0                        |
| `eslint src --max-warnings=0` | exit 0                        |

The live read is opt-in:
`ATAB_LIVE=1 go test -run TestLiveProject1Smoke` reads one repository,
one page, and asserts every fetched issue projects to exactly one work
item with no duplicate ids and no unmapped statuses. It is not in the
default lane because it spends the org's shared GitHub budget, which
other automation spends at the same time.

## Resilience guarantees

Each of these is a test, not an intention.

**A partial fetch never shrinks the board.** Some repositories answering
and others not is handled apart from both success and failure: it merges
rather than replaces and does not re-anchor, so a repository timing out
cannot delete every item it owns. The source reads degraded until a
clean fetch lands.

**A restart serves the last board it read.** Snapshots persist to
`--atab-state-dir`. Without this a restart starts from an empty cache,
an empty cache always fetches full, and a full fetch is the one thing a
throttled credential cannot do.

**A restored board never claims to be current.** It keeps the instant it
was actually read at, so freshness is computed from that instant and an
old board reads `stale` on every card.

**A restored board is discarded when the source changes shape.** The
fingerprint covers org, board and repository set, because a snapshot
taken before a repository left the source would put that repository's
issues back and nothing later removes them.

**A total failure never overwrites the stored board.** A cache holding
nothing is not written, so the copy that exists to survive a failure
cannot be destroyed by one.

**One source failing does not take another down.** Traversal is
per-source. An unreadable source contributes nothing, its edges resolve
to unknown (which holds a dependency rather than clearing it), and only
a total outage returns an error, because an empty list would render as
"no work".

**Ids never collide.** Every id is `<source>/<owner>~<repo>/<number>`,
so two orgs carrying issue #42 in a repo of the same name stay distinct.

**The order is stable, so paging is safe.** Each source sorts its refs
before projecting and the federated sort is stable, so items sharing an
`updated_at` always land in the same order.

**The health surface answers while a source is unwell.** A refresh does
not hold the state lock across the network call, so a crawl running for
the better part of a minute cannot stall the probe that explains it.

**A bounded history is never rendered as a whole one.** The activity page
reports `has_older` and `at_oldest` separately and the panel's
completeness line reads the second. An empty next cursor is not evidence
that a reader has seen everything, and the twenty-comment tail on the
card is not a history at all.

**A history that could not be read says so.** Three states stay distinct
end to end: an item nobody has touched (`200`, empty array), an adaptor
with no event log (`501 unsupported`), and a backend that refused
(the tagged kind, `rate_limited` most often here). Rendering any of them
as an empty feed would be a claim about the item that the board cannot
support.

**An optional surface survives the shader.** The decorator between the
server and every adaptor forwards `core.ActivityReader` only when the
inner adaptor has one, and a test asserts the wrapped atab plane still
carries it. This one was found on a deployed binary rather than in a
test: the wrapper dropped the interface and the route answered "this
adaptor keeps no history" against a board that reads it.

## Recovery

The unit restarts on failure and gives up after five failures in five
minutes, so a persistent fault is left visible rather than looping.

```bash
systemctl --user status gemba-atab.service
journalctl --user -u gemba-atab.service -f

curl -s http://127.0.0.1:7676/api/health
curl -s http://127.0.0.1:7676/api/adaptors      # per-source condition
```

`/api/adaptors` is the first thing to read. It names degraded sources
rather than reporting one number for the whole plane, and it never
carries a source's contents, only its condition.

| Symptom                          | Cause and response                                                                                                |
| -------------------------------- | ----------------------------------------------------------------------------------------------------------------- |
| `degraded sources: <id>`         | that source's last read failed; the reason is in the journal. Others are unaffected                               |
| Board is old, cards read `stale` | reads are failing; the stored board is still serving. Check the journal for `rate_limited` or `capability_denied` |
| `not yet read: <id>`             | nothing has read it yet. Only appears before the first attempt; a failed read reports degraded instead            |
| 503 on `/api/work-items`         | every source is unreadable and none has a stored board                                                            |
| Board missing a whole repository | check the source's `repos`; the allowlist is a boundary and there is no discovery                                 |

Rolling back the binary, and purging state:

```bash
# The previous binary is kept beside the running one.
install -m 0755 ~/.local/share/gemba-atab/bin/gemba.prev \
                ~/.local/share/gemba-atab/bin/gemba
systemctl --user restart gemba-atab.service

# Purge stored boards. This is the move when a credential's access was
# withdrawn rather than interrupted; nothing else removes them.
rm -rf ~/.local/share/gemba-atab/snapshots
```

Full removal is in `docs/adaptors/atab.md`. The adaptor writes nothing
to GitHub, so there is no remote state to undo.

## Scope, and how to reconcile the counts

The adaptor is scoped by the source's `repos` allowlist, not by the
project board. Those two produce different totals and the difference is
not a defect:

- A source reads every issue, open and closed, in the repositories it
  declares. Issues in those repositories that are on no board are still
  projected.
- A board carries rows from every repository in the org that anyone
  added to it, including repositories the source does not declare, and
  including rows that are not issues at all.

So a board's item count is larger than what the source sees whenever the
org has more repositories than the source declares. Reconcile by
comparing like with like: count the projected items carrying a row on
the source's own board rather than comparing the board's total against
the source's total.

`GET /api/work-summary` now does that arithmetic. Its `projects` array
splits the board along the project axis, and the entries sum to the
total: on this deployment 1608 on `atab-group#1`, 82 on
`hadedahealth#1`, 1095 on no board, 2785 in all. The unfiled bucket is
the reconciliation gap made countable, and it is the third largest group
on the dashboard.

## Limitations

- **A history page costs GraphQL budget.** Opening an item spends one
  query and each Load older spends another, against the same credential
  budget the board refresh uses. When that budget is exhausted the board
  still serves from its stored snapshot, but the history cannot be read
  at all and says so.
- **A walk is a view, not a transaction.** Offsets index a list that a
  refresh can move underneath them, so an item can be missed or repeated
  across a page boundary. The window is well under a second against a
  refresh interval of minutes.
- **Polling, not events.** The manifest declares no event stream, so the
  SPA polls and freshness is bounded by `refresh_interval`.
- **The rate limit is shared.** The GraphQL budget belongs to the
  credential, not to this process. A full crawl is expensive, and
  restarts used to be the largest consumer until snapshots made the read
  after a restart incremental.
- **Bounded reads.** At most `maxPages` pages per repository and a
  bounded comment tail per issue, so a very large repository or a lease
  buried under many later comments is truncated.
- **No schema v6.** The adaptor reads atab-meta v5 and earlier. It does
  not read, write or infer a v6, and it never edits an issue body.
