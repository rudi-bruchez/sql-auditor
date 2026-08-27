# `sql-auditor observe` — specification

Status: draft, not implemented. Written 27 August 2026.

## What it is for

`collect` reads what the server already knows. There is one class of finding it
cannot reach that way: **what the application does during one business
operation**. A row-by-row loop, an N+1 pattern, a chatty save — these are
recognised by counting the calls a single user action produces, and the server
keeps no record tying calls to actions.

The Query Store gets close. It knows a statement ran 3.2 million times last week
and that 111,573 of those fell in one hour, which is enough to say a loop
exists. It cannot say *which* operation the loop belongs to, or how many rows
one operation writes. That number is the whole argument when you take the
finding to an application vendor, and getting it requires watching while
somebody performs the operation.

`observe` is that watch. It runs an Extended Events session for a bounded time,
counts the statements, and returns the counts.

## Why it is a separate command

The manifest of every `collect` archive says this, and clients read it:

> The collector issues only read-only SELECT statements against system catalog
> views and dynamic management views. It runs no INSERT, UPDATE, DELETE or DDL,
> and it does not read any user or application table.

`CREATE EVENT SESSION` is DDL. That sentence is why a DBA runs this tool on
production without auditing it line by line, and it is worth more than any
feature. A promise with one exception is not a promise.

So `observe` is a different command, with a different manifest, requiring a
different consent. `collect` stays restituable without discussion.

## Command surface

Durations are always in **minutes**. Seconds invite a value that expires before
the operator has switched windows, and hours invite a session left running over
a weekend.

### Timed run

```
sql-auditor observe --minutes 5 --database LP2
```

Creates the session, runs it for five minutes, reads the result, drops the
session, writes the archive. One invocation, nothing left behind, nothing to
remember.

This is the right mode when the workload is continuous and any five minutes are
representative.

### Start and finish

```
sql-auditor observe start --database LP2
  ... the operator asks a user to run the operation in the application ...
sql-auditor observe finish
```

This is the mode that answers the question the timed run cannot. The operation
you want to measure takes as long as it takes, it happens when somebody presses
a button, and the useful window is exactly the one between "go" and "done".

`start` creates the session, snapshots its counters, writes a small state file
locally, and returns immediately. `finish` reads the counters again, subtracts
the snapshot, drops the session and writes the archive.

`observe status` says whether a session is running, since when, and how many
events it has counted so far. An operator who has walked away needs that.

A **maximum lifetime** applies to `start`, defaulting to 60 minutes and settable
with `--max-minutes`. The session carries it as a `DURATION` on its own stop
mechanism, so a `finish` that never comes does not leave a capture running for a
week. `finish` after the deadline reports what was collected before it expired
rather than failing.

### Reusing a session

```
sql-auditor observe --minutes 5 --session "RBAR watch"
```

Two reasons this matters. A DBA may already have a session tuned for their
instance and may not want a second one. And on a locked-down instance the DDL
may have been done in advance by someone with the rights, leaving the auditor
only the reading to do.

When `--session` names an existing session, `observe` **creates nothing and
drops nothing**. It validates that the session carries the events and the target
it needs, refuses with a specific message if not, snapshots, waits, reads,
subtracts. The archive records that the session was borrowed, so a reader knows
the tool did not shape what it measured.

Without `--session`, the tool creates one named `sql-auditor observe`, a fixed
name so that `status` and `finish` can find it after a crash, and so that a
human finding it in the DMVs knows where it came from.

## What is captured

Two events, `rpc_completed` and `sql_batch_completed`, which between them cover
every statement a client driver can send.

The default target is a **histogram bucketized on `query_hash`**. That decision
carries most of the design:

- It holds one integer per distinct statement shape. A five-minute capture of
  this workload is a few kilobytes, where the same capture with statement text
  would be tens of megabytes.
- It cannot leak application data. No SQL text, no parameter values, no login
  names pass through the capture at all. On an instance where the DPO had to be
  consulted before `collect` ran, that is the difference between a yes and a
  no.
- The text comes back afterwards, resolved server-side by joining the hashes to
  `sys.query_store_query` and `sys.dm_exec_query_stats`. The statements it
  cannot resolve are reported as unresolved hashes rather than dropped.

`--detail` adds an `event_file` target with statement text, for the cases where
the hash is not enough — a statement that has left the plan cache and was never
in the Query Store. It is off by default, it says so in the manifest, and it is
what makes the difference between a capture that carries no application data and
one that might.

The session is filtered to the database under study and excludes the collector's
own session id. `MAX_MEMORY` is set explicitly and `EVENT_RETENTION_MODE` is
`ALLOW_SINGLE_EVENT_LOSS`: on an instance doing 221 batches a second, dropping
an event is preferable to stalling the workload, and the drop count is reported
so the reader knows.

## Getting the result back

This is the part worth thinking through, and the constraint that decides it is
not on the server side.

**There is no Extended Events client library for Go.** Reading a session as it
runs — the "live data" stream that SQL Server Management Studio shows — goes
through `Microsoft.SqlServer.XEvent.Linq`, which is .NET only. There is no
port, and writing one means implementing an undocumented binary protocol
against a moving target. Any design for this command that assumed a live feed
would be a design for a different language.

That is not a limitation to work around, it is a constraint that picks the right
answer. Everything below reads the capture **through the same T-SQL connection
as the rest of the tool**, after the fact rather than as it happens. Which means
the session has to accumulate into something a `SELECT` can read, and that
decides the target before anything else does.

Three consequences worth stating, because they will look like omissions
otherwise. There is no live view: `observe status` reports counts, not a stream.
There is no per-event callback, so filtering happens in the session definition
where it belongs rather than in the client. And the ring buffer target, which
looks tempting because it is a DMV read, is the wrong choice anyway: it holds
recent events and silently discards older ones, so a five-minute window on a
busy instance would return its last few seconds and call it a capture.

**The histogram target needs no file and no library.** Its contents are in
`sys.dm_xe_session_targets.target_data`, an XML document read over the same
connection as everything else and parsed with the standard library. No path, no
share, no permission on the server's file system, nothing to clean up, and
nothing that Go cannot do. This is the main reason it is the default rather than
the event file, and it is why the histogram survives the absence of a client
library where a live stream does not.

**The event file, when `--detail` is passed, comes back the way the blocked
process reports already do**: `sys.fn_xe_file_target_read_file` reads it
server-side and returns it as rows. The collector already does exactly this in
`10.system/063.blocked-process-reports.sql`, including the rollover-file
pattern, so the mechanism is proven rather than new. It is also the only way to
read a `.xel` from Go: the file format is undocumented and the parsing happens
inside SQL Server, which hands back rows. What it requires is a directory the
SQL Server service account can write to, which `--detail` must take as an
argument rather than guess.

The rollover-file pattern deserves one line of warning, since this command would
inherit it. SQL Server appends `_0_<ticks>.xel` to the **stem** of the configured
name, so a session configured as `D:\watch\observe.xel` writes
`D:\watch\observe_0_133000000000000000.xel`. Building the search pattern by
appending a wildcard to the configured name, extension included, matches nothing
and returns an empty capture with no error. That exact mistake shipped in 063
and was found only when an instance that had 205 reports was reported as having
none.

**Cumulative counters, again.** A histogram target counts from the moment the
session started, not from the moment `observe` attached to it. When the session
is borrowed with `--session` it may have been running for a week. So `start`
snapshots and `finish` subtracts, exactly as the audit reads two collections and
subtracts. Reporting the raw histogram of a borrowed session would produce the
same class of error the rest of this tool exists to avoid.

**The archive** is a zip beside `collect`'s, with the same shape: a
`MANIFEST.txt` in prose, a `_run.json` for machines, and the results. It records
what was created and what was dropped, whether the session was borrowed, how
long the window was, how many events were lost, and whether `--detail` was on.
Somebody reading the archive in six months must be able to tell what the tool
did to the server.

## Output

```
observe-<server>-<date>-<time>.zip
  MANIFEST.txt
  _run.json
  statements.json     one row per query_hash: executions, resolved text,
                      first and last seen, source of the resolution
  unresolved.json     hashes the server could no longer name, with counts
  detail/             only with --detail: the event file rows
```

`statements.json` is the deliverable. Sorted by execution count, it is the
answer to "how many calls did that operation make", and the second column is the
statement making them.

## Consent and safety

`observe` prints the exact DDL it is about to run and waits for confirmation,
unless `--yes` is passed. The prompt names the session, the events, the target,
the database filter and the duration. An operator should be able to paste that
DDL into a ticket.

Guarantees the implementation owes:

- **Nothing survives a crash.** The session is created with `STARTUP_STATE = OFF`
  so a service restart does not resurrect it, and with its own duration cap so
  an abandoned `start` expires. `observe status` finds an orphan by its fixed
  name and `observe finish --drop` removes it.
- **A borrowed session is never dropped, never altered.** Not even to add an
  event it is missing; that is a refusal with a message, not a repair.
- **The tool refuses rather than degrades.** No permission, no Query Store for
  the resolution step, a session that already exists under the name it wanted:
  each is a distinct exit code and a sentence saying what to do.
- **The cost is measured before it is paid.** `observe` reads
  `batch_requests/sec` first and says what event rate to expect. At 221 batches a
  second a five-minute histogram capture is around 66,000 events into a few
  kilobytes of buckets; the same capture with `--detail` is a file the operator
  should be told about before it is written.

## What it does not do

It does not tie statements to a business operation on its own. It counts what
happened between two moments; the operator supplies the meaning by choosing
those moments. The report should say "during one *save order* performed at
14:32, the application issued 4,812 statements", and the tool provides the
4,812, not the *save order*.

It does not replace the Query Store analysis. `023` and `024` find the loops
across a month without touching the server. `observe` is for attributing one of
them to an operation, which is a smaller and later question.

It captures no execution plans and no wait information. Both exist elsewhere in
the tool, and adding them here would double the cost of the capture for facts
already available.

## Open questions

Whether `finish` should also snapshot `sys.dm_exec_query_stats` for the same
window, giving rows and duration per statement rather than counts alone. It is
cheap and it uses the subtract-two-snapshots discipline already in place. The
argument against is scope: the command would stop being "count the calls".

Whether the state file that `start` leaves behind belongs next to the archive or
in the user's config directory. Next to the archive is discoverable; in the
config directory survives someone cleaning out a working folder.

Whether `--minutes` should accept a fraction for a very short operation. Probably
not: an operation shorter than a minute is better measured with `start` and
`finish`, and admitting `0.5` invites `0.01`.
