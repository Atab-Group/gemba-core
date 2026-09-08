# ATAB WorkPlane adaptor

Read-only projection of GitHub Issues and GitHub Projects v2, shaped
around the way the Atab-Group org runs them. Source lives in
`internal/adapter/atab/`.

GitHub is canonical. The adaptor never writes: `CreateWorkItem` and
`UpdateWorkItem` return a tagged `read_only` error, `AttachEvidence`
returns `capability_denied`, and the manifest sets `read_only: true` so
the SPA hides write controls rather than disabling them. A second writer
against the same issues would produce the drift the board exists to
remove.

Reads go through the authenticated `gh` CLI. The adaptor never handles a
token, never reads one from the environment, and cannot log one.

## Quick start

```bash
# Against live GitHub, using the built-in Atab-Group Project #1 source.
gemba serve --atab

# Against a recorded fixture, with no network and no credential.
gemba serve --atab --atab-fixture internal/adapter/atab/testdata/atab-group-project-1.json

# Against an explicit multi-source allowlist.
gemba serve --atab --atab-sources ~/.config/gemba-atab/sources.json
```

`--atab` selects a WorkPlane on its own. It is mutually exclusive with
`--project-dir`, `--dolt-url` and `--noop`, it skips the whole Beads
startup path (no `bd` probe, no embedded Dolt supervisor, no Beads URL
resolution), and it forces `--orchestration=none` because a read-only
projection of somebody else's tracker has nothing to dispatch.

## What the projection carries

| Board surface       | Where it comes from                                                                                                       |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| Lane placement      | Project #1 `Status`, mapped through the declared `StateMap`                                                               |
| Priority            | Project #1 `Priority` (`P0`–`P3`), as the core integer priority                                                           |
| Area, Estimate      | Project #1 single-select fields, on `Custom`                                                                              |
| Kind                | the `type:` label, falling back to `atab-meta.type`; `epic` renders as the core milestone kind                            |
| Acceptance criteria | the `## Acceptance Criteria` checklist in the issue body                                                                  |
| Autonomy tier       | `atab-meta.autonomy` (`auto`, `pr`, `human`)                                                                              |
| Parent and children | GitHub-native sub-issue links, as `parent_child` edges                                                                    |
| Blockers            | `atab-meta.blocked_by`, inverted into core `blocks` edges                                                                 |
| Provenance          | `atab-meta.discovered_from`, as a `relates_to` edge plus the declared `atab:discovered_from` extension                    |
| Readiness           | derived by the same rules as the org's `ready_select`                                                                     |
| Assignee            | GitHub assignees                                                                                                          |
| Lease               | the newest `<!-- atab-lease ... -->` comment, reported separately from the assignee                                       |
| Evidence            | linked pull requests, their `<!-- atab-local-ci -->` reports, their `<!-- atab-verify -->` verdicts, and their check runs |

### atab-meta

The adaptor reads schema v5 and every earlier shape, which v5 parses
unchanged. It does not read, write or infer a v6, and it never edits an
issue body.

Three reader rules from the schema are implemented verbatim, because
each of them fails safe in a specific direction:

- An absent `autonomy` key reads as `pr`. An out-of-enum value reads as
  `human` with a warning naming the issue. A malformed declaration must
  never grant more autonomy than a person agreed to.
- A block whose YAML does not load is reported as `malformed` and the
  issue is skipped by readiness. It is never guessed at.
- A cross-repo edge naming an org other than the source's own is dropped
  with a warning. Following it would pull an unrelated org's issue into
  the dependency graph.

### Readiness

`atab_readiness` is the projected answer to "could a worker pick this up
right now, and if not, why". `atab_readiness_reason` names the specific
gate. The values, and the order the gates are decided in, are a port of
the org's `ready_select` so the dashboard and the dispatcher agree about
the queue instead of each computing its own idea of ready.

`ready`, `done`, `not_workable`, `parked`, `untracked`, `malformed`,
`in_flight`, `pr_open`, `cycle`, `blocked`, `orphan`, `human_only`,
`reconcile`, `unknown`.

Two of those deserve naming here:

- `unknown` outranks every other gate. When the snapshot behind an item
  is outside its source's freshness budget, the adaptor declines to
  assert readiness at all. A verdict computed on data it cannot vouch
  for would be worse than admitting it does not know.
- `blocked` covers an edge into a source this deployment cannot read.
  The dependency holds and the reason says so. Assuming an unreadable
  edge is clear would let an unreachable source silently unblock a
  queue.

### Assignee and lease

These are two different facts and the projection keeps them apart. The
assignee says who owns the issue. The lease says whether a worker is
running it right now. A worker that dies leaves its assignee behind
while its lease expires, and that difference is the only thing that
tells a stalled claim from a live one. An expired lease stays visible
for the same reason: it is the evidence the death happened.

### Stale and unknown states

Every item carries `atab_freshness` (`fresh`, `stale`, `unknown`) and
`atab_observed_at`. A board Status the `StateMap` does not know becomes
the `unknown` status token with the raw board value preserved on
`Custom`, rather than being guessed into a lane.

## Federation

`--atab-sources` declares which sources the adaptor may read. There is
no discovery path and no wildcard: a source absent from the file is a
source the adaptor will not read.

```json
{
  "sources": [
    {
      "id": "atab-group",
      "org": "Atab-Group",
      "project_number": 1,
      "display": "Atab Group Tasks",
      "authority": "canonical",
      "refresh_interval": "120s",
      "stale_after": "15m",
      "repos": ["Product-Seela", "Product-Klosa"]
    },
    {
      "id": "partner-org",
      "org": "Partner-Org",
      "project_number": 4,
      "authority": "reference",
      "field_names": { "Status": "State", "Priority": "Urgency" }
    }
  ]
}
```

| Field              | Meaning                                                                              |
| ------------------ | ------------------------------------------------------------------------------------ |
| `id`               | lowercase-hyphen slug; the prefix on every work item id this source produces         |
| `org`              | GitHub organisation login; also org-locks this source's cross-repo `atab-meta` edges |
| `project_number`   | org-level Projects v2 number; `0` means the source has no board                      |
| `repos`            | repositories to read; empty means every repository in the org the credential can see |
| `field_names`      | per-source overrides when a board spells a field differently                         |
| `authority`        | `canonical` or `reference`; see below                                                |
| `refresh_interval` | how often the source may be re-fetched (default `60s`)                               |
| `stale_after`      | age at which a snapshot is marked stale (default `10m`)                              |
| `fixture`          | optional path to a recorded file to replay instead of calling GitHub                 |

Three properties hold across the whole registry, and each has a test
that pins it:

**Ids never collide.** Every work item id is
`<source>/<owner>~<repo>/<number>`. Two orgs carrying issue #42 in a
repo of the same name stay distinct. GitHub forbids `~` in both owner
logins and repository names, so the id splits back apart unambiguously.

**An inaccessible source fails closed.** Its items are absent, its edges
resolve to unknown (which holds a dependency rather than clearing it),
and nothing about it beyond its id and a condition string reaches a
caller. A well-formed id naming a source this process does not carry
returns a plain not-found, without saying whether the source exists
elsewhere, is misconfigured, or is merely unreachable.

**One unhealthy source never takes down a healthy one.** Every traversal
is per-source. A single org going unreadable withholds that org's items
and logs against that org's id; the rest of the board keeps working.
Only a total outage, where every configured source is unreadable,
returns an error, because an empty list would render as "no work".

`authority` decides how far a source's word travels. A `canonical`
source owns its issues and its state can resolve another source's
dependency edge. A `reference` source shows its own issues but never
holds another source's work: an edge into it resolves as unknown. That
is the default for a source the operator has not classified.

## Caching and refresh

Each source holds one snapshot, refreshed no more often than its
`refresh_interval`.

- The first fetch is a full one. Later fetches ask only for issues
  updated since the last success, minus a 90 second overlap so an issue
  updated in the same second as the previous fetch cannot fall through
  the gap between two windows.
- A full re-anchor runs every 30 minutes. An incremental chain sees
  edits but never sees a deletion or a transfer, so the cache would
  drift without it.
- A refresh failure never empties the cache. The last good snapshot
  keeps serving and flips to stale once it ages past `stale_after`. A
  GitHub outage degrades the board to "here is what we last saw, and it
  is old" rather than to an empty board.
- A source that has never been read successfully has nothing to serve,
  and that is reported as an error.
- A partial refresh, where some repositories in a source answer and
  others do not, merges rather than replaces and does not re-anchor. One
  repository timing out during a full refresh would otherwise delete
  every item it owns from the board with nothing to show it happened.
  The next refresh stays a full one so the repositories that missed out
  are retried whole, and the source reports degraded until a clean fetch
  lands.

The repositories in a source are read four at a time. One at a time does
not finish a multi-repository org inside the API's 30 second request
deadline, and the repositories at the back of the list simply never get
asked; all at once reads as a burst to GitHub's secondary rate limiter,
which costs the whole refresh rather than one repository. Results are
flattened in the order the source declares its repositories, so the
board does not reshuffle when a different repository answers first.

`gemba serve --atab` drives every source itself rather than leaving the
cache to refresh inside whichever request arrives after the interval
lapses. Each source gets its own loop on its own `refresh_interval`,
reading on a context that belongs to the process rather than to a
browser tab. A cold crawl of nine repositories takes about 45 seconds,
which no HTTP request can wait for, so without this the board served
whatever prefix of the org fit inside one request. While the first read
is in flight the board is empty and its freshness reads `unknown`; it
fills when the read lands.

The loop keeps running after a failure. A first read that fails leaves
the cache empty, and an empty cache is exactly the state whose next
refresh is a full crawl, so giving up would hand that crawl back to the
request path where it cannot finish. A throttled source retries on its
next interval and the board fills then.

## The issue graph and the claim

Both are ATAB Core's, read off its own implementation rather than
reasoned out here. A board that disagreed with the dispatcher about who
holds an issue, or about what blocks it, would be worse than one showing
neither.

### Claim

The claim is the answer to "is a worker holding this right now", and it
is kept apart from the GitHub assignee everywhere. The assignee owns the
issue; the claim says whether something is running against it this
minute. A worker that dies leaves its assignee behind while its lease
expires, and that difference is the only thing separating a stalled
claim from a live one.

The authoritative lease is the comment with the highest REST comment id,
which is `claim.py`'s rule. Ids are strictly monotonic; timestamps are
not, because renewal edits a lease in place, so electing on time hands
the issue to the loser of a race the protocol already decided.

| State      | Means                                                               |
| ---------- | ------------------------------------------------------------------- |
| `active`   | lease expiry ahead, heartbeat recent                                |
| `stale`    | lease still held, holder has stopped beating                        |
| `expired`  | expiry passed, or the holder wrote `expires=expired`                |
| `claiming` | assigned with no lease yet, inside the sweep's grace window         |
| `none`     | nothing holds it                                                    |
| `unknown`  | the snapshot is outside its freshness budget, so no holder is named |

`claiming` exists because the pipeline assigns seconds before it writes
the lease; calling that gap unclaimed would invite a second worker into
an issue somebody is taking. `stale` uses the one heartbeat cadence the
contracts declare and three missed beats, which lands well inside the
three-hour lease TTL so it warns ahead of expiry rather than competing
with it.

### Graph

`atab_graph` rides on single-item reads only, because answering "what
would finishing this release" needs the source's `blocked_by` edges
inverted: one pass over the snapshot, cheap for the item a person opened
and quadratic if done for every card.

Blockers come from `blocked_by`, dependents from inverting it, parent
and children from GitHub's native sub-issue link (which the schema
deliberately does not duplicate), and provenance from `discovered_from`.
Provenance is kept out of the blockers, or the board would hold work
nothing is waiting on.

Every edge carries a working GitHub link even when the target is
unreadable here, and an unreadable target fails closed: state `unknown`,
no invented title, counted so the panel can mark it. Reporting it as
resolved would let an unreadable dependency silently unblock a queue.

## Persistence across restarts

`--atab-state-dir` keeps each source's snapshot between processes, one
JSON file per source, written after every read that produced a board and
restored when the source is registered.

Without it a restart starts from an empty cache, an empty cache always
fetches full, and a full fetch is the one thing a throttled credential
cannot do, so a restart during a rate limit serves nothing at all until
the budget returns. That is the moment the previous answer is most worth
having.

Three things keep a restored board honest:

- It keeps the instant it was actually read at. Freshness is computed
  from that instant, so a restored board reads `stale` as soon as it is
  older than the source's budget and every card says so.
- It is discarded when the source's shape changes. The fingerprint
  covers org, board and repository set, because a snapshot taken before
  a repository left the source would put that repository's issues back
  and nothing later removes them.
- A cache holding nothing is never written, so a total failure cannot
  overwrite the stored board with an empty one.

The stored watermark survives with the issues, so the read after a
restart is incremental rather than a full crawl.

### Ordering, and why paging is safe

`ListWorkItems` returns items newest-updated first. The ordering is a
property of the data rather than of a map walk, which is what makes
`?offset=` safe to slice it with: each source sorts its refs before
projecting, the federated sort is stable, and sources concatenate in
registration order, so two items sharing an `updated_at` always land in
the same order. GitHub stamps with second resolution, so ties are
ordinary rather than rare, and an ordering that shuffled on ties would
make a caller walking the pages skip and duplicate items with nothing
reporting an error.

The one case paging cannot cover is a refresh landing between two page
requests. Offsets index a list that has moved, so an item can be missed
or repeated across the boundary. A walk takes well under a second and
`refresh_interval` is minutes, so the window is small, but it is real:
treat a walk as a view of the board, not as a transaction over it.

### A source the credential can no longer read

A stored snapshot keeps serving when a refresh fails, and that holds for
a withdrawn permission exactly as it does for a network blip. The
adaptor never presents it as current: the snapshot reads `stale` and the
health surface reports the source degraded with the reason.

Purging is deleting the state directory. That is the operator's move
when access was withdrawn rather than interrupted, and nothing else
removes a stored board.

## The project axis

A source is an org plus a credential plus a repository allowlist. A
project is one Projects v2 board inside it. The two are different
questions and the board asks both, on two filter rows.

That distinction is not pedantry here. `Atab-Group` declares nine
repositories holding 2539 issues; its project #1 carries 1608 of them and
1095 sit on no board at all. The unfiled bucket is the third largest
thing on the dashboard, and a filter that only knew about orgs could not
show it. Until this landed, the row labelled "Project" was filtering by
source, so it offered one button per org and answered a question nobody
was asking.

The projection carries three fields:

| Field | What it holds |
| --- | --- |
| `atab_project` | The primary board, source-qualified: `atab-group#1`. Absent when the issue is on no board. |
| `atab_project_title` | The board's own name, as GitHub spells it. |
| `atab_projects` | Every board the issue was observed on, each with `id`, `number` and `title`. |

Ids are source-qualified for the same reason work item ids are: two orgs
both run a project #1, and an unqualified `#1` would merge two boards
into one filter button.

The configured board leads the list, because that is the board whose
`Status` and `Priority` the card already shows. The rest follow in the
order GitHub returned them. An issue on two boards is reachable from
either, which is why the per-board counts can sum to more than the total
and why `All` carries its own count rather than a sum.

`GET /api/work-summary` reports the same axis under `projects`, with the
unfiled bucket last and unattributed: it spans every source, so crediting
it to one would be a lie.

## Issue history

`GET /api/work-items/{id}/activity` returns one backwards page of the
item's real GitHub timeline: comments, closes and reopens, label and
assignee changes, renames, cross-references and milestone moves, each
with its actor, its instant and a link to its own record on GitHub.

It is a different read from everything else here. The board is a
snapshot on a timer; a history is unbounded and nobody needs one until
they open an item. So it is fetched on demand, paged, and never cached
into the snapshot.

The bounded twenty-comment tail the projection carries exists for lease
and evidence detection. **It is not a history.** The response reports
`has_older` and `at_oldest` separately so that distinction survives to
the screen: only `at_oldest` licenses a reader to treat what they are
looking at as the whole record. The panel prints one of two sentences
accordingly, and never infers completeness from an empty cursor.

Paging runs backwards, newest first, because that is the end a reader
opens a history at. GitHub's `timelineItems` connection pages forward
from the beginning, so the query uses `last:` with `before:`.

```
GET /api/work-items/{id}/activity?limit=30
GET /api/work-items/{id}/activity?before=<older_cursor>&limit=30
```

The route is offered through the optional `core.ActivityReader`
interface rather than through `WorkPlane`. A history is expensive and
most backends keep no event log, so requiring it of every adaptor would
hand each one a method it could not implement. A plane that does not
implement it gets `501 unsupported`, which a reader can tell apart from
an item nobody has touched.

Three failure states are distinct on the wire and on screen, because
rendering any of them as an empty feed would be a lie about the item:

| State | Wire | What the panel says |
| --- | --- | --- |
| Nothing has happened | `200`, `events: []` | Nothing has happened since it was opened |
| The adaptor keeps no history | `501 unsupported` | This board's adaptor does not fetch history |
| GitHub would not answer | the tagged kind, e.g. `rate_limited` | Names the cause; the throttle case says it will answer again |

A throttled read surfaces as `rate_limited`. The shared error mapper
gives every read surface `500` for that kind, so the status code is
`500` while the envelope's `error` is the useful part; the panel branches
on the code.

## A summary for an external widget

`GET /api/work-summary` is a fixed-size read-only surface: totals by
status, readiness and claim state, the same per source with its
freshness, and a capped list of what is being worked. Everything in it
is a count except that list, so the response does not grow with the
board. The alternative for a status widget is fetching every page and
counting client-side, which on this deployment is several megabytes per
poll to render a handful of numbers.

It is composed from the WorkPlane interface, so it answers for whichever
adaptor is bound; an adaptor carrying none of the `atab_*` fields still
gets a usable answer. Sources and held work are sorted, so a widget
polling it does not reshuffle between two identical answers.

Cross-origin access is off unless `--cors-allowed-origins` names an
exact origin. There is no wildcard, and the flag is the only way in.

## Running it as a service

The unit below runs the dashboard independently of any other service on
the host. It does not depend on, start, or stop anything else.

`~/.config/systemd/user/gemba-atab.service`:

```ini
[Unit]
Description=Gemba ATAB dashboard
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%h/.local/share/gemba-atab
Environment=PATH=/usr/local/bin:/usr/bin:/bin:%h/.local/bin
ExecStart=%h/.local/share/gemba-atab/bin/gemba serve \
  --atab \
  --atab-sources %h/.config/gemba-atab/sources.json \
  --listen 127.0.0.1 --port 7676 --quiet
Restart=on-failure
RestartSec=10s
StartLimitIntervalSec=300
StartLimitBurst=5
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=%h/.local/share/gemba-atab
NoNewPrivileges=true

[Install]
WantedBy=default.target
```

`gh` must be on the unit's PATH; the adaptor shells out to it for every
read.

```bash
systemctl --user daemon-reload
systemctl --user enable --now gemba-atab.service

systemctl --user status gemba-atab.service
journalctl --user -u gemba-atab.service -f

curl -s http://127.0.0.1:7676/api/health
curl -s http://127.0.0.1:7676/api/adaptors
curl -s http://127.0.0.1:7676/api/work-items | head -c 400
```

Stop and disable:

```bash
systemctl --user stop gemba-atab.service
systemctl --user disable gemba-atab.service
```

Roll back completely:

```bash
systemctl --user disable --now gemba-atab.service
rm ~/.config/systemd/user/gemba-atab.service
systemctl --user daemon-reload
rm -rf ~/.local/share/gemba-atab ~/.config/gemba-atab
```

Nothing outside those paths is touched. The adaptor writes nothing to
GitHub, so there is no remote state to undo.

To roll back only the binary, replace
`~/.local/share/gemba-atab/bin/gemba` with the previous build and
restart the unit. To take one source offline without stopping the board,
remove its entry from `sources.json` and restart, or point it at a
`fixture` file.

## Health and observability

`GET /api/adaptors` reports the plane's health. The probe reports
degraded when a source has failed a read or has gone stale, and reports
healthy with a note when a source simply has not been read yet, because
the first query populates it.

Per-source condition (last success, last attempt, freshness, item count)
is available through the registry and is what the probe aggregates. A
health reason never carries a source's contents, only its condition.

## Testing

```bash
go test ./internal/adapter/atab/...          # unit, projection, federation, conformance
ATAB_LIVE=1 go test ./internal/adapter/atab/ -run TestLiveProject1Smoke -v
```

The default lane is offline. It runs against the checked-in fixture set,
which covers every readiness branch: ready, blocked same-repo, blocked
cross-source, in flight under a live lease, an expired lease left by a
dead worker, an epic with open children, a drained epic, a parked issue,
an untracked issue, a malformed block, a cycle, an orphan, a board
status the mapping does not know, an issue with full pull request
evidence, and both closed shapes.

The live smoke is opt-in because it needs an authenticated `gh` and
spends the org's shared GitHub budget, which other automation in the
same org is spending at the same time.

The fixture JSON under `testdata/` is generated from the Go fixture set;
`go test ./internal/adapter/atab -update-fixtures` regenerates it and a
test fails when the two drift.

## Limitations

- **No events.** `Subscribe` returns `unsupported`, so the SPA polls
  rather than receiving pushed updates. Synthesising change events would
  mean polling GitHub on a second schedule with its own rate-limit
  budget. State freshness is bounded by `refresh_interval`, not by the
  500ms bar an event-emitting adaptor meets.
- **No sprints and no token budgets.** The org tracks iteration on the
  project board rather than as first-class sprint records, so
  `sprint_native` and `token_budget_enforced` are both false and the UI
  hides that chrome. A sprint filter matches nothing rather than
  matching everything.
- **Rate limits are shared.** The GraphQL budget belongs to the
  credential, not to this process. Other automation in the same org
  spends it too, and an exhausted budget surfaces as a `rate_limited`
  error and a stale board. Raising `refresh_interval` is the lever.
- **Bounded reads.** A fetch reads at most `maxPages` pages per
  repository (20 pages of 50 by default). A repository with more issues
  than that in one window is truncated at the newest end.
- **A page of the list is capped, and the board walks the pages.**
  `GET /api/work-items` still applies its server-wide default limit, but
  it now takes `?offset=` and answers with `has_more`, and the SPA walks
  until that clears. The live Atab-Group source projects around 2500
  items once closed issues are counted; before the walk existed, the
  board showed the first 1000 in repository-declaration order and the
  repositories at the end of the source's list were simply missing.
- **Comment tails are bounded.** Lease detection reads the last 20
  comments on an issue and evidence reads the last 20 on a pull request.
  A lease buried under more than 20 later comments is not seen. This is
  the tail on the card and is not the history: the Activity panel reads
  the timeline live, and says which of the two a reader is looking at.
- **A history page costs GraphQL budget.** Opening an item spends one
  query, and each Load older spends another, against the same credential
  budget the board refresh uses. Pages are capped at 100 events and
  default to 30 for that reason.
- **The timeline is filtered.** An unfiltered GitHub timeline includes
  subscription, mention and deployment events that would fill every page
  before a human comment appeared, so the query names its item types. An
  event of a type this build did not ask for still renders, saying what
  it was, rather than leaving a gap.
- **Comment bodies render as text.** They are arbitrary content anybody
  in the org can write, and the history panel is not the place to add an
  HTML sink, so markdown is not rendered there.
- **The verifier marker is provisional.** Verdicts are read from a
  `<!-- atab-verify -->` comment. The org's current Land gate reads a
  self-check block in the PR body instead, so a PR without that comment
  contributes pull request, local-CI and check evidence but no verifier
  verdict.
- **Cross-repo parent links are not read.** GitHub's cross-repo
  sub-issue support is plan-gated, and the org's own schema keeps parent
  links same-repo.
- **`requires_env` and `scope_clauses` are surfaced, not enforced.**
  They appear on the card as declared; nothing here checks an
  environment or resolves a clause.
