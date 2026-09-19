# Blocking watch: a collection must not hold anybody up

Status: implemented on 19 September 2026 (`collect/watch.go`). Written the same
day, and revised after a panel of five independent readers ran it against a
live SQL Server 2025. What the panel changed is listed at the end.

## The problem

`collect` runs every collector on one connection, one after the other. Each
collector sets `READ UNCOMMITTED` and `LOCK_TIMEOUT 10000`, which protects the
collection from waiting behind somebody else. Nothing protects somebody else
from waiting behind the collection.

That is not hypothetical, and `READ UNCOMMITTED` does not prevent it. Measured
on SQL Server 2025 (17.0.4065.4), in a scratch database:

1. session A, shaped like a collector (`READ UNCOMMITTED`, `LOCK_TIMEOUT
   10000`), runs a long `SELECT` that reads `dbo.t`. `sys.dm_tran_locks` shows
   it holding `Sch-S` on `dbo.t` for the whole statement;
2. session B runs `ALTER TABLE dbo.t ADD c int NULL` and waits,
   `LCK_M_SCH_M`, `blocking_session_id` = A;
3. session C runs a plain `SELECT COUNT(*) FROM dbo.t` and may wait behind B,
   `blocking_session_id` = B.

`sys.dm_os_waiting_tasks` showed both links after four seconds:

| session_id | blocking_session_id | wait_type | wait_duration_ms |
| --- | --- | --- | --- |
| B | A | LCK_M_SCH_M | 5007 |
| C | B | LCK_M_SCH_S | 4002 |

Whether C waits depends on lock partitioning, which the engine uses on an
instance with 16 or more schedulers: B takes its `Sch-M` partition by
partition, and a reader landing on a partition B has not reached yet goes
through. A reviewer started six copies of C on a 22-scheduler container; three
waited, on `LCK_M_IS`, and three completed. B waits every time.

A second shape needs no running statement at all. A session whose database
context is a user database holds a shared lock on that database. Measured by a
reviewer: a session that ran `USE` on a scratch database and a `SELECT`, then
sat idle, made `ALTER DATABASE ... SET READ_ONLY` wait `LCK_M_X` on a
`databaselock`, with `blocking_session_id` = the idle session, until that
session changed database. `runUnit` sets the target database with `USE` and
only the next unit's `ResetSession` moves it back, so today the collection
session sits in the database it just read while the document is encoded and
written, and after the last unit it stays there through the manifest and the
archiving, which can be long enough on a large run that the code announces it
as a phase.

On a production instance B is a deployment or a maintenance job, and everything
reading that table queues behind it. The collectors most likely to hold a lock
that long are the ones that read a lot of metadata or scan user tables, which
are also the ones with the longest `@timeout`: 1800 seconds for
`70.schema/041.compression-savings` and `70.schema/055.page-density`, 600 for
`80.workload/022.query-store-profiled`.

The operator running the audit sees none of it. The collector succeeds, slowly.

The comment on `contractLint` in `collect/queryset.go` states that `READ
UNCOMMITTED` "means the collector never waits on, or blocks, a production
workload". The measurements above contradict the "blocks" half, and the
implementation corrects that comment.

## What changes

Two things, the second of which only works because of the first.

### 1. The session leaves the target database as soon as the rows are read

`runUnit` calls `ResetSession` right after `ReadResultSets` returns, before the
document is encoded or written, and again on every path out of `runUnit`,
success or failure, if it has not already run. The reset runs on the run's
context under the usual `deadline`, never on the unit's context, which may be
the one that was just cancelled.

This closes the idle shape above for every unit, including the last one, and
it is also what releases a transaction a collector left open: a cancel stops the statement, and locks the transaction already took stay until `ROLLBACK`, measured by a reviewer with `TABLOCKX`. No collector opens a transaction today, but the statement lint does not forbid it. It
costs one round trip per unit. The reset at the start of the next unit stays:
it is what makes a reconnected session predictable.

### 2. A watch cancels a collector that someone is waiting on

While a unit runs, a second connection looks, once a second, for sessions
waiting on the collection's session. When one has waited 5 seconds, the watch
cancels the unit, and the reset of section 1 follows.

## The watch in detail

### The second connection

A separate `*sql.DB` opened with the same configuration, pinned to one
connection like the first, with one `*sql.Conn` held for the whole run. It
cannot be a second connection of the existing pool: `Open` pins that pool to
one connection on purpose, and the run holds it.

Its application name is the configured one with ` (blocking watch)` appended.
The corpus already accounts for its own session: `10.system/042` and `046` mark
the group holding `@@SPID` with `contains_collector_session`. The watch has
another session id and is not marked. With its own application name it forms
its own group in `046`, which groups by `program_name`, and reads as what it
is. In `042`, which groups by transport and encryption, it is one more
connection in the collector's group; the `dba-guide` says so.

### Starting it

After the preflight and before the first unit, in this order:

1. if `view_server_state` is in `DeniedCapabilities(m.Preflight)`, the watch is
   off. `DeniedCapabilities` counts anything that is not `ok`, which includes
   the `not_needed` that `ProfileChecks` writes over a denied capability no
   collector of the profile declares; testing for `denied` alone would miss it;
2. open the connection. A failure turns the watch off;
3. run the watch query once, with the collection's session id. A failure turns
   the watch off. This is the probe that counts: the preflight probes
   `sys.dm_os_wait_stats`, and a `DENY SELECT` on `sys.dm_os_waiting_tasks`
   alone would pass the preflight and fail here.

A watch that is off is a warning, never a reason to collect nothing. The
collection is the product and the watch is a safeguard.

### What it reads

The query, sent as a parameterised statement with the session id bound as
`sql.Named("collector", spid)`, an `int`:

```sql
SELECT TOP (1) w.session_id, w.wait_type, w.wait_duration_ms,
       w.resource_description
FROM sys.dm_os_waiting_tasks AS w
WHERE w.blocking_session_id = @collector
  AND w.session_id <> @collector
ORDER BY w.wait_duration_ms DESC;
```

To run it by hand, prepend `DECLARE @collector int = <session id>;`.

The session id is `@@SPID` of the collection connection, read by `Run` after it
connects and again after every reconnect, and passed to the watch when a unit
is armed. A reconnect gets a new session id; watching the old one would watch
nothing.

`session_id <> @collector` excludes the collector's own parallel tasks, whose
exchange waits name their own session as the blocker. The contract lint
requires `OPTION (RECOMPILE, MAXDOP 1)`, but by counting hints against result
sets, not statement by statement, so it does not prove every statement is
serial; the filter costs nothing. Negative `blocking_session_id` values (-2 to
-5) name orphaned, deferred or latch blockers, never a positive session id, so
they cannot match.

Only direct waiters are read. In the chain above, cancelling A releases B and
therefore whatever waits on B.

Each poll runs under a 2-second deadline. A poll that times out is a failure.
A half-open socket does not fail, it hangs, and a watch that hangs protects
nothing while the manifest says it ran.

### When it cancels, and what "5 seconds" means

When the longest direct wait read in one poll reaches 5000 ms. Polled once a
second, so a waiter is released about 6 seconds after it started waiting, plus
the cancellation itself: measured at 1 to 2 ms for a statement still
executing, and 906 ms for one whose rows were streaming, while the driver
drained them.

`wait_duration_ms` is the time spent in the task's current wait. The bound
therefore holds for one continuous wait, which is what a lock wait on the
collector is: B waits on A's lock until A lets go. A waiter that changes wait
type or resource starts again from zero, and the watch does not stitch waits
together; it bounds the waits that matter, it does not account for every
second anyone spent near the collector.

Five seconds is half the collectors' own `LOCK_TIMEOUT`: the audit gives up
waiting on others after 10 seconds, and it makes others wait on it for less
than that. It is a constant, not a setting. A setting would be read as an
invitation to raise it, and the number exists to bound the harm the audit can
do, not to be tuned per site.

### How the cancel reaches the unit

The watch goroutine never touches the manifest, the observer or the collection
connection. Its only effect on the run is a function call:

- `runUnit` creates the unit's context with `context.WithCancelCause` under the
  run's context, and the query context under that with the collector's
  timeout. It arms the watch with the session id and the cancel function, and
  disarms it on every path out;
- the watch, when it fires, calls that function with a `*blockedError` carrying
  the sample: waiting session, wait type, duration, `resource_description`;
- disarming returns the longest sample seen while armed, whether or not it
  fired. Arm, disarm and the watch's reading of the armed state go through one
  mutex; a cancel that arrives after disarming is dropped, so it can never
  land on the next unit.

The driver does not carry the cause: it returns a bare `context canceled`
whatever was passed, verified by two reviewers. So on a query or read error
`runUnit` asks `context.Cause` of the unit context, and when it is a
`*blockedError`, returns that instead of the driver's error. `outOfTime` is not
involved: it only relabels a `DeadlineExceeded` of the query context.

### What the run records

Three outcomes, decided by `runUnit` on the main goroutine from what disarming
returned and from its own error:

- the unit returned a `*blockedError`: it is a failed unit. It wrote nothing,
  the manifest carries an `ErrorEntry`, and the exit code is the one any failed
  collector gives. The message:

  ```
  cancelled by the blocking watch: session 78 had been waiting on this
  collector for 5.0 s (LCK_M_SCH_M, objectlock ... objid=1221579390 ... dbid=10)
  ```

  `resource_description` is kept as the engine writes it: object and database
  ids, which are enough to find the object and disclose nothing a collector
  does not already collect;
- the watch fired but the unit had already finished reading its rows, so the
  cancel changed nothing and the reset of section 1 released the waiter: the
  unit succeeded, and it gets a warning saying so with the sample;
- the watch saw a wait under 5 seconds: the unit succeeded, with a warning
  naming the longest wait seen. A wait shorter than the one-second poll can go
  unseen.

It is not a skip: a skip is decided before running. It is not a partial unit
either: a partial unit wrote its document.

Ctrl-C at the same moment is an operator stop, as it is today.
`recordUnitFailure` asks the run's context first, and the watch never cancels
that one. If the stop lands after the watch's `ErrorEntry` was written, the
entry stays: the unit really was cancelled for blocking before the operator
stopped the run.

### After a cancellation: leave that database alone

The 5-second bound is per wait. A deployment is many statements, and the next
collector on the same database can hold a lock the next statement needs. So
once a unit on database D was cancelled by the watch, every later unit
targeting D is skipped, with the reason `skipped: the blocking watch cancelled
<script> on this database`. Skipped, because this one is decided before
running. Instance-scope units carry on; each is bounded by the watch on its
own.

### What the cancel costs the audit

The watch cancels the collectors most expensive to lose. `055.page-density`
documents in its header that a cancelled batch loses its whole document, after
the buffer pool was already evicted, and `041.compression-savings` is the same
kind of scan. Both are exactly the collectors likely to hold a lock on a user
table for minutes. The trade is taken deliberately: a missing section in an
audit is a line in the manifest and a question to ask; a deployment held up
for twenty minutes by the audit is an incident caused by the auditor. The
cancelled collector is not retried, for the reason the previous section gives.

### The manifest

A block that says whether the watch ran:

```json
"blocking_watch": {
  "enabled": true,
  "poll_ms": 1000,
  "cancel_after_ms": 5000,
  "cancelled_units": 0,
  "stopped": ""
}
```

- `enabled` is false with a `reason` when the watch never started;
- `stopped` is empty while it ran to the end, and otherwise carries the time
  and the error that stopped it mid-run. `enabled: true` with a non-empty
  `stopped` means the units before that time were watched and the ones after
  were not;
- `cancelled_units` counts the `*blockedError` outcomes only.

`MANIFEST.txt` prints one line with the same facts. Without the block, a
manifest with no cancellation cannot tell "nobody was blocked" from "nobody was
looking". Every reader of `_run.json` in this repository decodes with
`encoding/json`, which ignores an unknown field; the private analysis reads the
manifest the same way and is not affected by an added block.

### When the watch is off, or stops

The three start failures above, and a failed or timed-out poll during the run.
After a failed poll the watch stops for the rest of the run and does not retry:
a watch that silently comes and goes would make `enabled` mean nothing.

The first time the watch is off or stops, one line goes to the progress
writer, `o.progress()`, the same channel as "connection lost; attempting one
reconnect", so the operator knows before reading the manifest. No change to
the `Observer` interface.

## What this does not do

- It does not look for blocking the collection suffers. That is what
  `LOCK_TIMEOUT` and the guard pattern already handle.
- It does not record the blocking chains of the instance. That belongs with
  the separate request to log blocking situations in the shared JSON blocking
  format, which is broader than the audit's own session.
- It does not retry a cancelled collector.

## How it is tested

Without a server:

- the watch's decision against a fake poll function: threshold, the sample
  returned on disarm, a fire after disarm dropped, stop on a poll error and on
  a poll that exceeds its deadline;
- `runUnit`'s classification: a unit context cancelled with a `*blockedError`
  gives that error, not `context canceled`;
- the skip of later units on a database where a unit was cancelled;
- the manifest block in `_run.json` and its line in `MANIFEST.txt`, for the
  three states: never started, ran to the end, stopped mid-run.

Against a server:

- CI runs a real collection on SQL Server 2017 and 2022. That run executes the
  watch query, bound as specified, once a second throughout; the CI job asserts
  that `_run.json` says `enabled: true` with an empty `stopped`. A query that
  does not bind or does not parse fails there;
- the cancellation itself is checked by hand with the reproduction below,
  pointing `sql-auditor collect` at the scratch database while B waits, and
  recorded in the commit that implements it.

## Appendix: the reproduction

Run as a sysadmin on a disposable instance. Setup:

```sql
CREATE DATABASE WatchLab;
GO
USE WatchLab;
CREATE TABLE dbo.t (id int IDENTITY PRIMARY KEY, pad char(200) NOT NULL DEFAULT 'x');
INSERT dbo.t DEFAULT VALUES;
```

Session A, the collector. The `CROSS APPLY` makes it reread `dbo.t` for the
whole statement, so it keeps its `Sch-S`; a query that reads the table once and
then spends its time elsewhere finished in under two seconds on the test
container and blocked nothing, which is how the first two attempts failed.

```sql
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;
USE WatchLab;
SELECT COUNT_BIG(x.id)
FROM (SELECT TOP (3000000000) a.object_id
      FROM sys.all_objects a CROSS JOIN sys.all_objects b CROSS JOIN sys.all_objects c) o
CROSS APPLY (SELECT TOP (1) t.id FROM dbo.t AS t
             WHERE t.id >= ABS(CHECKSUM(NEWID())) % 2) x
OPTION (MAXDOP 1);
```

Session B, two seconds later:

```sql
USE WatchLab;
SET LOCK_TIMEOUT 60000;
ALTER TABLE dbo.t ADD c int NULL;
ALTER TABLE dbo.t DROP COLUMN c;
```

Session C, one second after B. On 16 schedulers or more it may complete
instead of waiting, as explained above:

```sql
SET LOCK_TIMEOUT 60000;
USE WatchLab;
SELECT COUNT(*) FROM dbo.t;
```

Then the watch query with `DECLARE @collector int = <session A>;` prepended.
Clean up with `KILL` on session A and `DROP DATABASE WatchLab`.

The idle shape: in session A, run `USE WatchLab; SELECT COUNT(*) FROM dbo.t;`
and leave the session open; in session B, `SET LOCK_TIMEOUT 20000; ALTER
DATABASE WatchLab SET READ_ONLY;`. B waits `LCK_M_X` on the database until A
runs `USE master`.

## What the panel changed

Five readers: agy with a directive and a neutral prompt, codex with the same
two, and a fresh Claude subagent with the neutral one.

- The idle shape, found by one reader by running it, and the false sentence of
  the first draft that the last unit's connection "is closed right after
  anyway": it is closed when `Run` returns, after the manifest and the zip.
  This added section 1, which is now half of the design.
- The driver drops the cancellation cause (two readers, verified). Added
  `context.Cause` and the `*blockedError`.
- The watch query did not say how `@collector` is bound (two readers). Now it
  does, and CI executes it.
- The first draft's "the run calls `ResetSession` at once" had no place in the
  code to happen (three readers). It is now the reset of section 1, on the run's
  context.
- `ProfileChecks` can turn a denial into `not_needed` (two readers), and an
  object-level `DENY` on the DMV passes the preflight (one reader). Now the
  gate is `DeniedCapabilities` plus one real poll.
- The per-wait bound let the next unit block the same deployment again. Added
  the skip of the rest of that database.
- The `@timeout` ceiling was 1800 s, not 300, and the collectors the watch will
  cancel are the ones that lose most. The trade is now argued.
- No deadline on the poll; no value for `enabled` after a mid-run stop; the
  watch goroutine writing the manifest concurrently; the watch session unmarked
  in `042` and `046`; Ctrl-C precedence; the reproduction's third leg depending
  on lock partitioning. Each is settled above.

One claim was checked and kept: a reader found that `Sch-S` taken inside an
explicit transaction is released at the end of the statement, not of the
transaction. That is true of `Sch-S`, and another reader measured that locks a
transaction took, `TABLOCKX` or an `UPDATE`, stay after the cancel until
`ROLLBACK`. The reset of section 1 covers both.
