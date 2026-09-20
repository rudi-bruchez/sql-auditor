# Recording what the collection blocked, and what it cost

Status: draft, not implemented. Written on 20 September 2026, and revised the
same day after a panel of five independent readers ran it against a live SQL
Server 2025. What the panel changed is at the end.

## The question

Two things a run knows about itself and does not write down.

The blocking watch, added in 0.28.0, polls once a second for sessions waiting
on the collector's own session, and cancels the unit when one of them has
waited five seconds. It sees the waiter's session id, wait type, wait duration
and resource description. Today all of that ends as English prose:

- a cancelled unit increments `blocking_watch.cancelled_units` and its error
  reads `cancelled by the blocking watch: session 78 had been waiting on this
  collector for 5.0 s (LCK_M_SCH_M, objectlock objid=1)`;
- a wait that did not reach the limit becomes a warning line with the same
  sentence.

Nobody can count those, group them or feed them to another tool. The archive
of a run that held up production three times says `cancelled_units: 3` and
hides the rest in three sentences.

The performance half is in better shape: `_run.json` already carries `script`,
`scope`, `target`, `output`, `bytes`, `duration_ms` and `status` for every
unit. What is missing is a reader: `MANIFEST.txt` says nothing about where the
time went, so "the audit took eleven minutes" cannot be answered without
opening the JSON and sorting it.

`docs/blocking-watch-spec.md` deferred this on purpose: "It does not record
the blocking chains of the instance. That belongs with the separate request to
log blocking situations in the shared JSON blocking format."

## The shared format, as it stands today

The family of tools (ShareLock, sqltop, sql-auditor, SqlGoPace) shares a
blocking-report contract. Its home is the `schema` module of
`github.com/rudi-bruchez/mssqlkit` (the repository the request calls
`sqlblocking-schema`), and as of 19 September 2026 that module is a stub:
`doc.go` and nothing else. The structure lives in ShareLock's `SPEC01` section
12, where sql-auditor's role is "historical blocking as one audit chapter".

So the contract can be followed in its vocabulary, but nothing can be
validated against it, and no document should claim conformance to a version
that has not shipped. What is added below is therefore not a document of that
contract: it has no `schema_version`, no producer block and no report id, and
a reader must not treat it as interoperable. When the shared module ships, a
converter maps it, along with the instance's own blocking history, which is
the larger chapter left out below.

The rules `doc.go` states, and how this design stands against each:

- `schema_version` leads the document: not applicable, see above;
- three-state booleans stay three-state: followed;
- absence is typed, never a bare null and never a missing key: followed, with
  a status field that names the case;
- truncation is declared, with `truncated` and `omitted_count`: followed;
- units live in field names, `_ms`, `_kb`, `_bytes`, `_us`: followed;
- timestamps are RFC 3339 with an explicit offset, taken from the server: NOT
  followed, and this is the one deliberate departure. Neither the watch query
  nor the enrichment reads the server clock, and adding a read to a poll that
  runs once a second for the length of the run costs more than it settles.
  The timestamps are the collector host's, as every other timestamp in
  `_run.json` already is.

## What is added

### 1. An incident per wait, in `_run.json`

`blocking_watch` gains `incidents`, an object, because a capped collection has
to carry its cap:

```json
"incidents": {
  "items": [
    {
      "unit": {"script": "70.schema/055.page-density.sql", "target": "SALESDB"},
      "first_seen": "2026-09-20T09:14:22.184+02:00",
      "waited_ms": 5031,
      "cancelled": true,
      "waiters_seen": 1,
      "waiter": {
        "session_id": 78,
        "wait_type": "LCK_M_SCH_M",
        "resource_description": "objectlock lockPartition=0 objid=1234 subresource=FULL dbid=7",
        "program_name": "SQLAgent - TSQL JobStep (Job 0x...)",
        "database": "SALESDB",
        "identified": "ok",
        "identified_detail": ""
      }
    }
  ],
  "truncated": false,
  "omitted_count": 0
}
```

One entry per unit the watch saw someone wait on, whether or not it cancelled
it, so the waits that are only a warning today become countable too. The
audit's own session is the root of the chain, and the waiter recorded is the
longest one at the moment of the reading.

`waited_ms` is the longest wait the watch read for that unit, a lower bound:
the wait is sampled once a second. `waiters_seen` is how many distinct
sessions waited on that unit, because the direct waiter can change between
polls and several can wait at once; the incident describes one of them, and
this says whether there were others. `first_seen` is when the watch first saw
anyone wait on that unit, on the collector host's clock.

The cap is 100 entries. A run that blocks a hundred times has said what it has
to say, but a cancellation must never be the entry that falls off the end: when
the list is full, a cancelled incident replaces the shortest wait that was not
cancelled, and `omitted_count` counts everything dropped either way.

### 2. Who the waiter is

`sys.dm_os_waiting_tasks` gives a session id and nothing about who that is.
One query names the session, on the watch's own connection:

```sql
SELECT s.program_name, DB_NAME(r.database_id)
FROM sys.dm_exec_sessions AS s
LEFT JOIN sys.dm_exec_requests AS r ON r.session_id = s.session_id
WHERE s.session_id = @waiter;
```

No login name and no host name. The first draft argued they were already in
the archive because `10.system/046.local-sessions.sql` collects sessions; the
panel showed the opposite, and 046 says so in its own header: it aggregates,
and "no login name, host name or session id is projected beside it". The
manifest's disclosure paragraph names login, host and program names of live
sessions only under `--include-session-text`. Adding them here by default
would make that paragraph false, which is the one thing the manifest may never
be. A program name is different: 046 already carries it, aggregated, and the
disclosure gains one line when an incident holds one (see below).

No statement text and no input buffer either, for the same reason: the flag
that discloses text turns on a collector the paragraph names, and the watch is
not a collector.

It runs the first time the watch sees a given session waiting on the running
unit, not at the cancel threshold:

- there is no race with the cancel, which releases the waiter. The panel
  measured a waiter still visible in `sys.dm_exec_requests` in the same batch
  as the `KILL` and gone by the next round trip, so a query after the cancel
  would often find nothing;
- the cancel is not delayed. The watch is the bound on the harm the audit can
  do, and no query of its own may sit between the threshold and the cancel;
- a waiter is by definition present when it is waiting, so this is the moment
  the answer exists.

At most three enrichments per unit, which bounds the cost of a convoy. The
result is kept per session id, and attached to the incident only if it is the
session the incident reports; otherwise the incident says so.

`identified` names the case, so an absent field is never a bare null and never
an unexplained missing key:

- `ok`: the query answered, and the fields below it are that session's;
- `not_attempted`: the three-per-unit bound was reached before this session;
- `no_session`: the query answered with no row, so the session had gone;
- `failed`: the query errored or hit its deadline, with the reason in
  `identified_detail`;
- `other_session`: the identity read belongs to a different session than the
  one this incident reports, so it is not shown.

`database` is `null` with `identified: ok` when the session has no request,
which is what a sleeping session with an open transaction looks like. That is
the three-state rule: no request is not the same as an unknown database.

A failed enrichment never stops the watch and never prevents the cancel. A
failed poll does stop it, because a watch that silently comes and goes makes
`enabled` meaningless; an identity is a nicety and its loss costs nothing.

The resource is recorded as the engine wrote it, unresolved. Turning
`objid=1234 dbid=7` into `SALESDB.dbo.Orders` means a query in that database
while it is the object of a schema lock, which is the deadlock-shaped risk the
watch exists to avoid.

### 3. What the manifest says

`MANIFEST.txt` keeps its one `Block watch` line and gains, when there is at
least one incident, the list: unit, waiter, wait type, seconds, and whether it
was cancelled. The warnings stay as they are; they are the sentence a human
reads first.

It gains one line in the disclosure list, and only when an incident carries a
program name: the program name a blocked session reported, which is
client-supplied text. Nothing else about that session enters the archive.

The same section gains the performance reading the file lacks: the collection's
elapsed time, which `RunInfo` already holds, and the five slowest units with
their duration and size. The sum of the unit durations is not the elapsed time
and is not presented as it: preflight, target selection, connection resets,
skipped units, the manifest and the archive all sit outside it. The full list
is already in `_run.json` for anyone who wants to sort it.

## What is not in scope

- The blocking history of the instance in the shared format. The archive
  already holds it: `10.system/063.blocked-process-reports.sql`,
  `061.deadlock-graphs.sql` and `060.system-health.sql`. Rendering those into
  a section 12 document is a converter over collected data, it is the "one
  audit chapter" the ecosystem gives sql-auditor, and it should be written
  against the shared module rather than against a copy of the structure. It
  waits for that module to exist.
- Blocking the collection suffers, which `LOCK_TIMEOUT` handles.
- A chain deeper than one waiter: the watch reads the longest direct
  waiter; cancelling the collector releases whatever queued behind it.
- A second sample of the same waiter, to show a wait growing.

## Tests

Without a server:

- the incident built from a scripted poll: one per unit, the longest wait
  kept, `cancelled` true only when the watch fired, `waiters_seen` counting
  distinct sessions, and no incident when nobody waited;
- two waiters trading the maximum across polls: the incident reports the
  session of the longest wait, and an identity read for the other session is
  reported as `other_session` rather than attached to it;
- the cap: 101 waits give 100 items, `truncated: true`, `omitted_count: 1`;
  and a cancelled wait arriving into a full list of warnings is kept, while
  the shortest warning is dropped;
- an enrichment that fails, returns no row, or hangs past its deadline: the
  incident is still written, `identified` says which case, the watch is not
  stopped, and, for the hang, the cancel still happens at the threshold
  (measured on the fake clock, not on wall time);
- a session with no request gives `database: null` with `identified: ok`;
- `MANIFEST.txt` for a run with no incident, one incident and a truncated
  list; the disclosure line appearing only when an incident carries a program
  name; the slowest-units block for a run with fewer than five units.

Against a server:

- CI asserts that `blocking_watch.incidents` is present with an empty `items`
  and `truncated: false` on a run where nothing blocks, which is what
  distinguishes "nobody waited" from "the field was never written";
- the reproduction of `docs/blocking-watch-spec.md`, by hand, in the direction
  the watch actually looks: a collector-shaped reader holds `Sch-S` on a
  table of a scratch database and the audit runs behind it, so that a schema
  change queues on the audit's own session. The first draft described the
  opposite arrangement, with the collector queued behind a schema lock; the
  panel ran it and the watch query returned nothing, because that is blocking
  the collection suffers, which is out of scope here and handled by
  `LOCK_TIMEOUT`.

## What the panel changed

Five readers (agy and codex, each with a directive and a neutral prompt, and a
Claude subagent) ran the first draft against SQL Server 2025.

- The disclosure argument was false. 046 collects no login name, host name or
  session id, and says so in its header; the manifest promises those only
  under `--include-session-text` (agy both seats, codex both seats, verified
  by the author). The incident now carries neither, and the program name it
  does carry adds a disclosure line.
- The enrichment moved from the cancel threshold to the first sighting of a
  waiter: at the threshold it sits between the wait and the cancel, on the
  watch's own connection and its deadline, which is exactly where the design
  may not add a delay, and the failure policy was unspecified (codex
  directive, agy directive).
- The watch keeps one worst sample and no identity history, so an incident
  could pair one session's identity with another's wait (codex directive).
  Hence `waiters_seen`, the per-session identity and `other_session`.
- The cap could drop a cancellation in favour of harmless sub-second waits
  (agy neutral).
- An array cannot carry `truncated` and `omitted_count`; the incidents are an
  object (codex directive).
- The timestamp rule of the shared contract cannot be met by the artefacts the
  document specifies (codex neutral), so the departure is now stated rather
  than claimed away. Likewise the claim of conformance: the fragment is not a
  document of that contract.
- `DB_NAME(r.database_id)` is null for a session without a request, and is the
  waiter's connection context, not the locked database (agy neutral).
- The manual reproduction was written in the direction the watch does not look
  (codex neutral).
- The sum of unit durations is not the run's elapsed time (agy directive,
  codex directive).
