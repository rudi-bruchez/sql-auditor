# Blocking watch: a collection must not hold anybody up

Status: draft, not implemented. Written 19 September 2026.

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
3. session C runs a plain `SELECT COUNT(*) FROM dbo.t` and waits,
   `LCK_M_SCH_S`, `blocking_session_id` = B.

`sys.dm_os_waiting_tasks` showed both links after four seconds:

| session_id | blocking_session_id | wait_type | wait_duration_ms |
| --- | --- | --- | --- |
| B | A | LCK_M_SCH_M | 5007 |
| C | B | LCK_M_SCH_S | 4002 |

C is an application query that has nothing to do with the audit, and it waits
as long as the collector runs. On a production instance the schema change is a
deployment or an index maintenance job, and everything reading that table queues
behind it. The collectors most likely to hold a lock that long are the ones
that read a lot of metadata or scan physical structures, which are also the
ones with the longest `@timeout`: up to 300 seconds.

The operator running the audit sees none of it. The collector succeeds, slowly.

The code says otherwise today: the comment on `contractLint` in
`collect/queryset.go` states that `READ UNCOMMITTED` "means the collector never
waits on, or blocks, a production workload". The measurement above contradicts
the "blocks" half, and the implementation corrects that comment.

## What the watch does

While a collector runs, a second connection looks, once a second, for sessions
waiting on the collection's session. When one has waited long enough, the watch
cancels the collector, which releases its locks, and the run moves on to the
next unit.

### The second connection

A separate `*sql.DB` opened with the same configuration, pinned to one
connection like the first. It cannot be a second connection of the existing
pool: `Open` pins that pool to one connection on purpose, so that session state
is predictable between scripts, and the run holds that connection for its whole
life.

It is opened after the preflight and before the first unit. If it cannot be
opened, the run goes on without a watch and says so (below). The collection is
the product and the watch is a safeguard; losing the safeguard is a warning, not
a reason to collect nothing.

The watch session uses the same login and application name as the collection.
It will appear in the collectors that list sessions, like the collection
session itself already does.

### What it reads

```sql
SELECT TOP (1) w.session_id, w.wait_type, w.wait_duration_ms,
       w.resource_description
FROM sys.dm_os_waiting_tasks AS w
WHERE w.blocking_session_id = @collector
  AND w.session_id <> @collector
ORDER BY w.wait_duration_ms DESC;
```

`@collector` is `@@SPID` of the collection connection, read once after it
connects and again after every reconnect: a reconnect gets a new session id,
and watching the old one would watch nothing.

`session_id <> @collector` excludes the collector's own parallel tasks. A
parallel plan reports exchange waits (`CXPACKET`, `CXCONSUMER`) whose blocking
session is the same session, and those are not somebody else waiting. The
contract lint requires `OPTION (RECOMPILE, MAXDOP 1)`, but by counting hints
against result sets, not statement by statement, so it does not prove every
statement is serial; the filter costs nothing.

Only direct waiters are read. In the chain above, B waits on the collector and
C waits on B; B alone is enough to decide, since cancelling the collector
releases B and therefore C. The chain would only matter for the message, and
the message names B.

### When it cancels

When the longest direct wait reaches 5 seconds. Polled once a second, so a
waiter is released at most about 6 seconds after it started waiting, plus the
time the cancellation takes.

Five seconds is half the collectors' own `LOCK_TIMEOUT`: the audit gives up
waiting on others after 10 seconds, and it makes others wait on it for less than
that. It is a constant, not a setting. A setting would be read as an invitation
to raise it, and the number exists to bound the harm the audit can do, not to
be tuned per site.

Cancellation is the unit's context being cancelled. Measured with the driver
this repository uses (go-mssqldb 1.10.0) against the same scratch setup:

- the query returned `context canceled` 2 ms after the cancel;
- `sys.dm_tran_locks` went from 2 object locks held by the collector's session
  to 0;
- the connection answered a ping afterwards, with the same session id.

That holds for a statement outside any transaction, which is every collector in
the corpus today: none opens one. Nothing enforces it, though. The statement
lint does not forbid `BEGIN TRAN`, and a statement cancelled inside an explicit
transaction keeps the locks the transaction already took. So after a
cancellation the run calls `ResetSession` at once, whose `IF @@TRANCOUNT > 0
ROLLBACK` releases them, rather than leaving that to the start of the next
unit. For the last unit of a run, the connection is closed right after anyway.

### What the run records

A cancelled unit is a failed unit. It wrote nothing, so the manifest carries
an `ErrorEntry` for it and the exit code is the one any failed collector gives.
The message says why, with what a DBA needs to find the other side:

```
cancelled: session 78 had been waiting on this collector for 5.0 s
(LCK_M_SCH_M, objectlock ... objid=1221579390 ... dbid=10)
```

`resource_description` is kept as the engine writes it. It carries object and
database ids, not names, which is enough to find the object and discloses
nothing a collector does not already collect.

It is not a skip. A skip is a decision taken before running, and this one was
taken because of what happened while running. It is not a partial unit either:
a partial unit wrote its document.

A unit that blocked somebody for less than 5 seconds and finished gets a
warning with the longest wait seen and the waiting session, so a collection
that came close is visible too. A wait shorter than the one-second poll can go
unseen; the watch bounds long waits, it does not account for every short one.

The manifest gains a block that says whether the watch ran:

```json
"blocking_watch": {
  "enabled": true,
  "poll_ms": 1000,
  "cancel_after_ms": 5000,
  "cancelled_units": 0
}
```

with `"enabled": false` and a `reason` when it did not. `MANIFEST.txt` prints
one line with the same facts. Without that block, a manifest with no
cancellation cannot tell "nobody was blocked" from "nobody was looking".

### When the watch is off

- `view_server_state` denied at preflight. Without `VIEW SERVER STATE` (from
  SQL Server 2022, `VIEW SERVER PERFORMANCE STATE`) the read fails outright,
  measured: Msg 300, "VIEW SERVER PERFORMANCE STATE permission was denied". The
  watch is not started and the reason says so.
- the second connection cannot be opened.
- the watch query fails during the run. The watch stops for the rest of the
  run, the reason records the error and the time, and the run continues. It
  does not retry: a watch that silently comes and goes would make `enabled`
  mean nothing.

In each case the run's summary line and the progress output say that the watch
is off, once, so the operator knows before reading the manifest.

### What it arms

The watch is armed for the whole of `runUnit`: the `USE`, the query and the
reading of its rows, since locks are held until the rows are read. It is not
armed during the preflight, the identity probe or the database listing, which
are short catalog reads on the same session; widening it to them later is a
matter of arming earlier, not a change of design.

## What this does not do

- It does not look for blocking the collection suffers. That is what
  `LOCK_TIMEOUT` and the guard pattern already handle.
- It does not record the blocking chains of the instance. That belongs with
  the separate request to log blocking situations in the shared JSON blocking
  format, which is broader than the audit's own session.
- It does not retry a cancelled collector. Running it again a minute later
  would block the same deployment again.

## How it is tested

The decision logic (threshold, self-exclusion, warning under the threshold,
stop on error, re-reading the session id after reconnect) is tested with a fake
poll function, no server needed.

The engine behaviour is checked by hand against a local instance, with the
three-session reproduction above, and recorded in the commit that implements
it. CI runs a real collection on SQL Server 2017 and 2022; it proves the watch
starts and does not disturb a run with no blocking, which is the common case.

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

Session C, one second after B:

```sql
SET LOCK_TIMEOUT 60000;
USE WatchLab;
SELECT COUNT(*) FROM dbo.t;
```

Then the watch query above, with `@collector` set to session A. Clean up with
`KILL` on session A and `DROP DATABASE WatchLab`.
