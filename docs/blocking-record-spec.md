# Recording what the collection blocked, and what it cost

Status: draft, not implemented. Written on 20 September 2026.

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
`sqlblocking-schema`), and as of 19 September 2026 **that module is a stub**:
`doc.go` and nothing else. The structure lives in ShareLock's `SPEC01` section
12, where sql-auditor's role is "historical blocking as one audit chapter".

So the contract can be followed, but nothing can be validated against it, and
no document should claim conformance to a version that has not shipped. The
rules `doc.go` states, which this design follows:

- `schema_version` leads the document; additive changes bump minor;
- three-state booleans stay three-state;
- absence is typed: an omission carries a reason, never a bare null;
- truncation is declared, with `truncated` and `omitted_count`;
- units live in field names: `_ms`, `_kb`, `_bytes`, `_us`;
- timestamps are RFC 3339 with an explicit offset, taken from the server.

## What is added

### 1. An incident per wait, in `_run.json`

`blocking_watch` gains `incidents`, an array. One entry per unit the watch saw
someone wait on, whether or not it cancelled it. The shape follows section 12's
vocabulary without pretending to be one of its documents: the audit's own
session is the root of the chain, and there is only ever one waiter recorded,
the longest at the moment of the reading.

```json
{
  "unit": {"script": "70.schema/055.page-density.sql", "target": "SALESDB"},
  "first_seen": "2026-09-20T09:14:22.184+02:00",
  "waited_ms": 5031,
  "cancelled": true,
  "waiter": {
    "session_id": 78,
    "wait_type": "LCK_M_SCH_M",
    "resource_description": "objectlock lockPartition=0 objid=1234 subresource=FULL dbid=7",
    "login_name": "deploy_svc",
    "host_name": "APP07",
    "program_name": ".Net SqlClient Data Provider",
    "database": "SALESDB",
    "enrichment": "ok"
  }
}
```

`waited_ms` is the longest wait the watch read for that unit, which is a lower
bound: the wait is sampled once a second. `cancelled` says whether that unit
was stopped. The array is capped at 100 entries and carries `truncated` and
`omitted_count` beside it, per the contract rule; a run that blocks a hundred
times has said everything it has to say.

### 2. Enrichment of the waiter, once

`sys.dm_os_waiting_tasks` gives a session id and nothing about who that is.
The watch runs one enrichment query on its own connection, the first time a
wait on the running unit crosses the cancel threshold, so at most once per
unit, and only in the case that is about to become an incident worth reading:

```sql
SELECT s.login_name, s.host_name, s.program_name, DB_NAME(r.database_id)
FROM sys.dm_exec_sessions AS s
LEFT JOIN sys.dm_exec_requests AS r ON r.session_id = s.session_id
WHERE s.session_id = @waiter;
```

It runs under the watch's own `watchPollDeadline`, and it runs before the
cancel rather than after: cancelling releases the waiter, which then leaves
`sys.dm_exec_requests` at its own pace, and enriching afterwards would be a
race the watch would often lose. `enrichment` says what happened, which is the
typed absence the contract asks for, and the identity fields are there only
when it reads `ok`:

- `ok`: the query answered;
- `not_attempted`: the wait never reached the cancel threshold, so nothing was
  tried. That is every incident which is only a warning today;
- `no_session`: the query answered with no row, so the waiter had gone;
- `failed: <reason>`: the query errored or hit its deadline.

No statement text, and no input buffer. `10.system/046.local-sessions.sql`
already puts login, host and program of local sessions in every archive, so
enrichment adds no kind of fact the default archive did not hold. Statement
text is different: `10.system/052.session-text.sql` is the one collector whose
output is not purely structural, and it sits behind `--include-session-text`.
An incident does not carry text even with that flag on, because the flag turns
on a collector the disclosure paragraph names, and the watch is not a
collector; adding text here would make that paragraph untrue.

The resource is recorded as the engine wrote it, unresolved. Turning
`objid=1234 dbid=7` into `SALESDB.dbo.Orders` means a query in that database
while it is the object of a schema lock, which is the deadlock-shaped risk the
watch exists to avoid.

### 3. What the manifest says

`MANIFEST.txt` keeps its one `Block watch` line and gains, when there is at
least one incident, the list: unit, waiter, wait type, seconds, and whether it
was cancelled. The warnings stay as they are; they are the sentence a human
reads first.

The same section gains the performance reading the file lacks: the total
elapsed time of the collection, and the five slowest units with their duration
and size. Five, and a total, is what answers "where did the eleven minutes
go"; the full list is already in `_run.json` for anyone who wants to sort it.

## What is not in scope

- **The blocking history of the instance in the shared format.** The archive
  already holds it: `10.system/063.blocked-process-reports.sql`,
  `061.deadlock-graphs.sql` and `060.system-health.sql`. Rendering those into
  a section 12 document is a converter over collected data, it is the "one
  audit chapter" the ecosystem gives sql-auditor, and it should be written
  against the shared module rather than against a copy of the structure. It
  waits for that module to exist.
- **Blocking the collection suffers**, which `LOCK_TIMEOUT` handles.
- **A chain deeper than one waiter.** The watch reads the longest direct
  waiter; cancelling the collector releases whatever queued behind it.
- **A second sample of the same waiter** to show a wait growing.

## Tests

Without a server:

- the incident built from a scripted poll: one per unit, the longest wait
  kept, `cancelled` true only when the watch fired, and no incident when
  nothing waited;
- the cap: 101 incidents give 100 entries, `truncated: true`,
  `omitted_count: 1`;
- enrichment failure leaves `enrichment` at `no_session` or `failed`, no identity fields, and the
  incident is still written;
- `MANIFEST.txt` for a run with no incident, one incident and a truncated
  list; the slowest-units block for a run with fewer than five units.

Against a server:

- CI asserts `blocking_watch.incidents` exists and is empty on a run where
  nothing blocks, which is what distinguishes "nobody waited" from "the field
  was never written";
- the reproduction of `docs/blocking-watch-spec.md`, by hand: a session
  holding a schema lock, the collector queued behind it, and the incident read
  from `_run.json` with the waiter's login and host in it.
