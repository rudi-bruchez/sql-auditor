# `sql-auditor observe`, specification

Status: draft, not implemented. Written 27 August 2026, revised the same day
after two reviews, and rewritten on 20 September 2026 after a panel of three
readers and a round of measurement broke the design it then had. The section
"What the measurement changed" records what was wrong and why, because the
design it replaces is the obvious one to propose again.

## What it is for

`collect` reads what the server already knows. There is one class of finding it
cannot reach that way: what the application does during one business operation.
A row-by-row loop, an N+1 pattern, a chatty save, these are recognised by
watching the calls a single user action produces, and the server keeps no record
tying calls to actions.

The Query Store gets close. It knows a statement ran millions of times last week
and that a large share of those fell in one hour, which is enough to say a loop
exists. It cannot say which operation the loop belongs to, or how many rows one
operation writes. That number is the whole argument when the finding goes to an
application vendor, and getting it requires watching while somebody performs the
operation.

`observe` is that watch. It runs an Extended Events session for a bounded time
and leaves a capture file on the instance.

## What the measurement changed

Until 20 September 2026 this document specified a histogram target bucketized on
the `sqlserver.query_hash` action, with the statement text resolved afterwards
from the Query Store and the plan cache. Measured on SQL Server 2025 RTM-CU7
(17.0.4065.4), that design returns one bucket holding the entire workload, with
the value zero:

| Event | `sqlserver.query_hash` action | Events seen |
| --- | --- | --- |
| `rpc_completed` | 0 | 10 |
| `sql_batch_completed` | 0 | 2 |
| `sql_statement_completed` | 4594718534688251656 | 1 |
| `sp_statement_completed` | 4594718534688251656 | 5 |

The traffic was ordinary: a literal batch sent through sqlcmd, and parameterised
calls sent as RPCs by this repository's own driver. The two events this command
is built on fire when the call is finished, at which point there is no current
statement for the action to read, so it reads zero. The events that do carry a
hash are the statement-level ones, which this document rejected by name for
multiplying the event volume by whatever a batch does.

The failure was silent at every check the design had: the session builds, starts,
counts, and returns a well-formed document; the exit code is 0; the total is
right, because every event lands in the single bucket. Only the deliverable is
empty.

Rather than reopen that trade, the capture now keeps the whole event. The two
call-level events carry natively, as their own data columns, everything the
histogram was trying to reconstruct:

| | `rpc_completed` | `sql_batch_completed` |
| --- | --- | --- |
| text | `statement` | `batch_text` |
| time | `duration`, `cpu_time` | `duration`, `cpu_time` |
| I/O | `logical_reads`, `physical_reads`, `page_server_reads`, `writes` | the same four |
| rest | `row_count`, `object_name`, `result` | `row_count`, `spills`, `result` |

No action has to be evaluated for any of it, which is what makes the new design
immune to the defect that killed the old one. What leaves the tool with it: the
hash conversion, the histogram slots and their overflow, the Query Store and
plan cache resolution, the baseline and its subtraction, and the Go decoder for
`target_data`. Roughly half of what this document used to specify is now work
that does not need doing.

The grouping the histogram was performing at capture time is not lost, it moves.
`SQLFerret` ingests `.xel` files, normalises every statement into a fingerprinted
query shape, redacts parameter values, and stores the result in DuckDB. It does
at leisure, with the text in hand, what the histogram was trying to do blind.

## Why it is a separate command

The manifest of every `collect` archive says this, and clients read it:

> The collector issues only read-only SELECT statements against system catalog
> views and dynamic management views, and it does not read any user or
> application table. […] It creates no permanent object: nothing that belongs
> to this server or its databases is created, altered or deleted, and no data
> of yours is written anywhere by this tool.

`CREATE EVENT SESSION` creates a permanent object, and this capture writes a file
on the instance. Both halves of that paragraph would become false. That sentence
is why a DBA runs this tool on production without auditing it line by line, and
it is worth more than any feature. A promise with one exception is not a promise.

The stronger form of the argument is about who approves. The value of that
paragraph is that a DBA can approve it without reading the corpus. An opt-in step
inside `collect` creates two states of the manifest, and every approver then has
to ask which one they are signing. Believing a flag makes that legible is the
weak version of this design.

So `observe` is a different command, with a different manifest, requiring a
different consent. `collect` stays restituable without discussion.

This extends one layer down, and the point is easy to lose there. The `observe`
manifest does not belong in `collect/manifest.go`, which is built around one
`Manifest` type whose `Human()` prints the read-only claim above. Putting both in
one type turns the choice an approver makes at the command line into a branch
inside a function, where nobody is signing anything. A separate file and a
separate type, sharing only `Zip` from `collect/archive.go`, which knows nothing
about collection and carries over unchanged.

## What `observe` produces, and what it does not

This is the part that changed most, and it is stated before the command surface
because it decides everything after it.

The capture is a `.xel` file, written by SQL Server, on the instance's own disk.
`observe` chooses where, creates the session, runs it for the window, stops it,
drops it, and tells the operator the path. It does not bring the file back.

That is a deliberate first form and not an oversight. Two ways to pull the file
over the ordinary SQL connection were measured:

| Route | Result | Rights needed |
| --- | --- | --- |
| `sys.fn_xe_file_target_read_file` | works, returns the events as XML rows, not a `.xel` | the ones `observe` already needs |
| `OPENROWSET(BULK …, SINGLE_BLOB)` | returns the file byte for byte, same SHA-256 on both sides | `ADMINISTER BULK OPERATIONS` |

The second works and is verified, but it asks for a right that has nothing to do
with auditing, on an instance where the whole design is an argument about
minimal consent. The first returns XML, which is not what `SQLFerret` ingests.
So the first form of this command asks for neither: the file stays where SQL
Server wrote it, and the operator retrieves it and zips it by whatever means
they already use to move a backup. If that proves to be the friction that stops
people using the command, `OPENROWSET` is the documented way to change it, as an
option that names the extra right in the consent prompt.

The refusal of `OPENROWSET` without the right deserves recording whichever way
that goes, because it is the unpleasant kind: `Msg 4834` is raised at compile
time, it kills the whole batch, and `TRY` does not catch it. A tool that tries
the bulk route opportunistically and falls back does not degrade, it loses the
batch it was in. This is the same shape as the `Msg 17750` failure recorded in
`docs/verification-binary-collation.md`.

What the tool writes is therefore small: an archive holding the manifest, the
exact DDL that ran, the window, the event count, the loss counters, and the path
of the capture file. It is a record of what was done and where the evidence is,
not the evidence.

### What the manifest says

This is the deliverable of the whole design, and it is drafted here rather than
invented at implementation time:

> This capture was taken by creating an Extended Events session on the instance,
> running it for the window recorded below, stopping it, and dropping it. The
> exact statements that created and dropped it are in `_run.json`. The session
> subscribed to two events, `rpc_completed` and `sql_batch_completed`, which fire
> once per call made by a client, and for each call it recorded the text, the
> duration, the CPU time, the reads, the writes and the number of rows.
>
> The capture contains the text of your statements as your application sent
> them, including any literal value written into the SQL by the application, and
> possibly personal data carried in those literals. It was written by SQL Server
> to a file on this server, at the path recorded below, and it is yours. This
> tool counted the events in that file and read nothing else from it: it did not
> copy it, and it is not in this archive.
>
> Nothing else on this instance was modified. The session named below was
> created and removed by this run.

The change of posture is the second paragraph. The previous design could claim
the capture carried no application data, which was its strongest argument on an
instance where a DPO had to be consulted. That claim is gone and must not be
softened into something that sounds like it. The compensation is that the data
never leaves the client's own disk, which is a different and in some rooms a
better answer.

## Command surface

Durations are always in minutes. Seconds invite a value that expires before the
operator has switched windows, and hours invite a session left running over a
weekend. Fractional minutes are refused: an operation shorter than a minute is
better measured with `start` and `finish`, and admitting `0.5` invites `0.01`.

`--max-minutes` caps every mode, not only `start`, and defaults to 60. A
`--minutes 4320` is refused rather than obeyed.

### Timed run

```
sql-auditor observe --minutes 5 --database SALESDB
```

Creates the session, runs it for five minutes, reads the counters, stops the
session, drops it, writes the archive, and prints the capture path. One
invocation, nothing left running, one file to collect.

This is the right mode when the workload is continuous and any five minutes are
representative.

### Start and finish

```
sql-auditor observe start --database SALESDB
  ... the operator asks a user to run the operation in the application ...
sql-auditor observe finish
```

This is the mode the timed run cannot replace. The operation you want to measure
takes as long as it takes, it happens when somebody presses a button, and the
useful window is exactly the one between "go" and "done".

`start` creates the session, starts it, writes a state file, and returns.
`finish` reads the counters, stops the session, drops it, and writes the archive.

`observe status` says whether a session is running, since when, how many events
it has recorded, how many it has dropped, and how much time is left before its
deadline.

### `observe start --for <minutes>`

A `start` that stops by itself at a declared deadline, in the shape chosen after
the trade was explained. The deadline is declared and stored, `status` shows the
time left or the overrun, any later invocation of `observe` stops a session past
its deadline, and a `finish` that arrives late writes the archive with the window
it really measured plus a manifest line saying by how much the capture ran past
its intent. The archive never claims to have measured the window that was asked
for.

The honest part of that sentence is "any later invocation". There is no
server-side timer, which the next section is about.

### `observe stop`

Stops and drops the session without writing an archive, and refuses nothing
except a session that is not ours.

It exists because without it the design locks the operator out. A `start --for
60` whose process then dies leaves a session that every later `start` refuses to
replace, since it is within its deadline, for up to an hour. That is the one
finding all three reviewers reached independently, and a command that can undo
what a command did is a smaller remedy than a `--force` flag on `start`, which
would have to decide the ownership question in the middle of doing something
else.

### The maximum lifetime, and how it is really enforced

An earlier draft of this spec said the session carries its deadline as an option
of its own. It cannot. `CREATE EVENT SESSION` takes `MAX_MEMORY`,
`EVENT_RETENTION_MODE`, `MAX_DISPATCH_LATENCY`, `MAX_EVENT_SIZE`,
`MEMORY_PARTITION_MODE`, `TRACK_CAUSALITY` and `STARTUP_STATE`, and nothing that
stops it after a while. There is no server-side timer to lean on.

So the deadline is enforced by the tool, at its next visit, and the mechanism has
to survive the loss of everything local. The session name is fixed,
`sql-auditor observe`, and the deadline is derived from the session's own start
time, `sys.dm_xe_sessions.create_time`, plus `--max-minutes`. Any later
`observe`, on any machine, finds the session under the known name, reads when it
started, and stops it if it has outlived its maximum.

Deriving the deadline rather than storing it is what makes the fixed name and the
self-healing consistent. A name carrying an expiry is not a fixed name, and a
`status` that has lost its state file could not construct it.

That is weaker than a guarantee and the spec does not pretend otherwise. If the
laptop dies during a `start`, or during a `--minutes 5` run, the session keeps
running until somebody runs `observe` again or a DBA notices it. `observe`
reduces the window; it does not close it. And with a file target the residue is
no longer a few megabytes of memory: an orphan goes on writing to disk, which
raises the stakes of the sweep and is the reason the next section exists.

### A fixed name is not proof of ownership

The sweep may not stop and drop a session merely because it carries the known
name and an old `create_time`. A DBA can have created that name by hand, reused
it after a previous run, or be looking at a failed one. Stopping it would be an
unauthorised destructive action on a production instance, and with a file target
it also abandons a file somebody was filling.

So the managed session has a server-verifiable fingerprint: its events, its
actions, its predicate, its target and its target options, read back from
`sys.server_event_sessions` and its companions the way
`queries/10.system/062.xe-sessions.sql` already reads them. The sweep acts only
on a session that matches the fingerprint exactly. A same-named session that does
not match is never altered: `observe` refuses, prints what it found and what it
expected, and says the session must be dealt with by hand.

Ownership is a property of the session as the server describes it, not of the
state file, because the state file is the thing most likely to be missing when
the sweep matters.

### Two operators can still collide

One `observe` at a time per instance. A fixed name means a second operator, or
the same operator on a second database, collides, and the check is not an atomic
lock: two `start` commands can both see nothing, both pass consent, and both
reach `CREATE EVENT SESSION`. One of them loses.

Already-exists at create is therefore an ordinary refusal and not an error: the
loser re-reads the session, describes the winner, and stops nothing. If the
re-read fails, or the session it finds does not match the fingerprint, it refuses
with that fact rather than guessing.

### Ordering, which decides whether exit code 3 tells the truth

Exit code 3 means refused before touching the instance. A sweep that runs first
and drops an orphan, and then refuses for a missing permission, exits 3 having
altered the instance.

The order is therefore: validate the command line, connect, identify any session
under the known name without altering it, run preflight for the permissions the
chosen mode needs, obtain consent, and only then sweep, create or alter. The
sweep needs `ALTER ANY EVENT SESSION` like everything else, so putting preflight
before it is also what turns a raw SQL error into the designed refusal.

`observe status` is the awkward case and is resolved rather than ignored: it
sweeps like every other entry point, which means it can issue DDL. It prints
what it dropped, and its help text says it can.

### Cancellation

Ctrl-C stops the session, drops it, and writes a partial archive whose manifest
records the shortened window and says it was interrupted. Leaving the session
behind is the failure this whole design is nervous about.

`collect/cancel.go` is named in an earlier draft as the discipline to reuse, and
that reference is wrong in a way that would have shipped. Those 63 lines decide
that a dead context means the operator stopped the run, so a cancellation is not
filed as a network fault. They handle no signal, write no archive and touch no
server, and they are enough for `collect` because `collect` creates nothing on
the instance. `observe` does.

Two things follow that the implementation owes:

- the teardown cannot run on the cancelled context. Once the signal handler
  cancels, every query on that context fails immediately, the read of the
  counters and the `DROP EVENT SESSION` included. The teardown runs on a fresh
  context derived from `context.Background()`, with its own short timeout;
- the second Ctrl-C kills the process by design, since the handler calls
  `signal.Stop` before cancelling. For `collect` that is right, the residue being
  local files. Here it leaves a session running and a file growing. The trade may
  still be the right one, but it is made deliberately: the first Ctrl-C message
  says what a second one will leave behind, and the session name and the capture
  path are on screen before the wait starts, not only in the archive.

Every transition between `CREATE`, `START`, the write of the state file, the
read, the `STOP`, the `DROP` and the archive has a compensating action. Creation
is followed by a best-effort drop on any failure before durable state exists, and
the state file is written atomically only once the session is known to be
running.

## The session

Two events, `rpc_completed` and `sql_batch_completed`, which between them cover
every call a client driver makes.

Both collect their text, which is off by default in SQL Server and has to be
asked for: `SET collect_statement = 1` on `rpc_completed` and
`SET collect_batch_text = 1` on `sql_batch_completed`. An implementer who omits
them builds a session that works, fires, and records every column except the one
the capture exists for.

The actions are the context the events do not carry and `SQLFerret` can use:
`sqlserver.database_name`, `sqlserver.client_app_name`. Nothing that identifies a
person is collected, so no `username`, no `client_hostname`, no `nt_username`.
That is a default and not a law: an audit that needs to tell two application
servers apart can ask for more, and the manifest then says so.

The session is filtered to the database under study, on `sqlserver.database_id`,
resolved from the name when the session is created. That attributes a call to the
database the session is in, not to the database whose objects it touches: a
three-part-name query issued from another context is not captured. For counting
one application's calls during one operation that is the right filter, and it is
stated here because a reader comparing these numbers with the Query Store's will
otherwise find a discrepancy with no explanation.

The session also excludes `observe`'s own session id, which means reading
`@@SPID` before the `CREATE`, so the id appears in the DDL the consent prompt
shows.

### Options

`MAX_MEMORY` set explicitly. `EVENT_RETENTION_MODE = ALLOW_SINGLE_EVENT_LOSS`:
on a busy instance, dropping an event is preferable to stalling the workload, and
the drop count is reported so the reader knows. `STARTUP_STATE = OFF`, so a
service restart does not resurrect the session.

`MAX_DISPATCH_LATENCY` matters less here than an earlier draft feared, and the
correction is worth recording because it was reached by removing a variable
rather than by reading. Under a session declared with a 30 second latency, a read
of the histogram's `target_data` saw events one second after they fired, and the
control run, driving and reading on a single connection with no disconnection in
between, saw the count rise from 20 to 40 in the same batch. The latency governs
buffering, not what a reader can see. It is still set low here, because with a
file target it governs how much sits in memory rather than on disk when the
process dies.

### The target, and the trap in its file names

`package0.event_file`, with `filename`, `max_file_size` and `max_rollover_files`
all set explicitly. Inheriting version-dependent defaults is how two audits of
two builds come back incomparable.

Rollover deletes. A capture that outruns `max_file_size` times
`max_rollover_files` loses its beginning, silently, and what remains looks like a
complete capture of a shorter window. That is the answer-that-looks-like-an-answer
this document objects to everywhere else, so the loss has to be visible: the tool
records the number of files present at the end beside the number it expected, and
says in the manifest when the earliest file is not the one the session started
with.

The rollover naming deserves its own warning, since this command would inherit a
mistake this repository has already made. SQL Server appends `_0_<ticks>.xel` to
the stem of the configured name, so a session configured as
`/var/opt/mssql/log/observe.xel` writes
`/var/opt/mssql/log/observe_0_134343862333780000.xel`. Building the pattern by
appending a wildcard to the configured name, extension included, matches nothing
and returns an empty capture with no error. That shipped in
`queries/10.system/063.blocked-process-reports.sql`, where the extension test
searched the reversed name for the extension spelled forwards and so never
matched. It was found when a collection reported an empty capture on an instance
whose ring buffer held two reports and whose files, once read correctly, held
205.

### Where the file goes

The directory is the operator's choice and is never guessed, because it has to be
one the SQL Server service account can write to and one with room. `observe`
proposes the directory holding the error log, read from
`SERVERPROPERTY('ErrorLogFileName')`, which is writable by that account by
definition, and requires it to be confirmed or replaced with `--file <dir>`.

The proposal is a convenience and carries a warning with it: the error log
directory is often on a volume sized for logs. The consent prompt states the
expected size, from the measured batch rate times the window, beside the path.

### Reading the counters before stopping

Measured: stopping a session removes it from `sys.dm_xe_sessions` and from
`sys.dm_xe_session_targets` entirely, one row to zero. A read after the stop
returns no row, which a careless caller turns into "no events", which is an
archive saying the window saw nothing.

With a file target the events themselves are safe on disk, so this is no longer
the correctness trap it was for an in-memory target. What is lost is everything
the session knows about itself, and those are exactly the numbers the archive
needs: `dropped_event_count`, `dropped_buffer_count`,
`largest_event_dropped_size`, `total_bytes_generated` and `create_time`. They are
read while the session is running, before the stop, and not after.

The event count in the file is read afterwards, with
`SELECT COUNT(*) FROM sys.fn_xe_file_target_read_file(...)`, which returns a
number and brings no statement text back to the tool. Measured with a login
holding only `VIEW SERVER STATE` and `ALTER ANY EVENT SESSION`.

### Measuring the cost before paying it

`observe` reports the expected event rate before creating anything. Batch
Requests/sec in `sys.dm_os_performance_counters` is a cumulative count, not a
rate: one read gives the average since the last service restart, which on an
instance up for months is far below the rate during the window. Two reads a few
seconds apart, subtracted.

This is the same trap the corpus documents elsewhere, and getting it wrong here
would contradict the discipline this section is built on. It matters more than it
did: the number now predicts how much disk the capture will take.

## Permissions

For a command whose whole frame is consent, the first question is what rights it
needs:

| Right | Scope | Needed for |
| --- | --- | --- |
| `ALTER ANY EVENT SESSION` | server | creating, starting, stopping, dropping the session |
| `VIEW SERVER STATE` | server | `sys.dm_xe_sessions`, `sys.dm_xe_session_targets`, `sys.fn_xe_file_target_read_file` |

That is the whole list, and it was measured rather than derived: a login holding
those two read the capture file's event count and saw the session's counters,
with no database-level right and no membership of any server role. The Query
Store and plan cache rights the previous design needed are gone with the
resolution step.

The consent prompt prints the permissions next to the DDL. Preflight checks them
before the session is created, the way `collect/preflight.go` already does for the
collector, and `collect/grants.go` grows the corresponding `GRANT`. Without that,
"the tool refuses rather than degrades" is a promise with nothing behind it.

Version floor: SQL Server 2012, the same as the corpus. The floor is a property
of Extended Events rather than a convention: the session syntax changed
substantially across 2008 and 2008 R2 and settled at 2012, so one DDL cannot
cover the older builds. The measurements in this document were taken on SQL
Server 2025 and are not a substitute for taking them on 2012; that is the first
open question below.

## Output

```
observe-<server>-<date>-<time>.zip
  MANIFEST.txt
  _run.json      the exact statements that ran, the window, the session
                 counters, the exit classification
  capture.json   where the capture file is, how many files, how many events,
                 what was dropped, and how to retrieve it
```

`_run.json` records the DDL as it was executed, captured at execution and not
recomposed afterwards from the same builder. A recomposition proves the builder
is deterministic, which nobody doubted, and not that the consent prompt told the
truth. That file is the promise the manifest makes in writing, and it is what
lets an operator paste the DDL into a ticket after the fact.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | the capture completed, the file is on the instance, the archive was written |
| 1 | the instance could not be reached, or the archive could not be written |
| 2 | partial: the capture ran but something is missing, dropped events, rollover loss, a session that stopped on its own |
| 3 | refused before touching the instance: missing permission, a session already running under the name, a same-named session that is not ours, a bad path |

They are the interface a script uses, so they are listed rather than promised.

## Consent and safety

`observe` prints the exact DDL it is about to run, the permissions it requires,
the path it will write to and the size it expects, and waits for confirmation
unless `--yes` is passed. The prompt names the session, the events, the actions,
the target and its options, the database filter and the duration.

Guarantees the implementation owes:

- nothing survives a service restart, and an orphan is findable.
  `STARTUP_STATE = OFF` means restarting SQL Server does not bring the session
  back. What it does not guarantee is that nothing survives a crash of the tool:
  there is no server-side timer, so an orphan runs until the next `observe` stops
  it or a human does. Stating this as a guarantee, which an earlier draft did,
  would have been the comfortable sentence and the false one;
- a session that does not match the managed fingerprint is never stopped, never
  altered, never dropped, whatever its name;
- the tool refuses rather than degrades, and preflight is what makes that true
  rather than aspirational;
- the cost is measured before it is paid, from two reads of a cumulative counter
  rather than one;
- the capture file is never read for its content, only counted, and never copied.

## What it does not do

It does not tie statements to a business operation on its own. It records what
happened between two moments; the operator supplies the meaning by choosing those
moments. The report should say "during one save order performed at 14:32, the
application issued 4,812 statements", and the tool provides the 4,812, not the
save order.

It does not see inside a batch. `rpc_completed` and `sql_batch_completed` fire
once per call from the client, so a loop written in T-SQL, a `WHILE`, a cursor, a
procedure iterating a set, arrives as one event however many statements it runs.
That is consistent with what the command is for, and it is worth stating because
a reader coming from `queries/80.workload/021.query-store-detail.sql` will have
seen per-statement figures and expect them here.

It does not analyse the capture. No normalisation, no ranking, no grouping by
query shape. That is `SQLFerret`'s job and it already does it, and a second
implementation in this repository would be a worse one competing with it.

It does not move the capture file, for the reasons given above, and it does not
delete it. The file belongs to the client from the moment SQL Server writes it,
and a tool that deleted evidence it did not collect would be making a decision
that is not its own.

It does not replace the Query Store analysis. `023` and `024` find the loops
across a month without touching the server. `observe` attributes one of them to
an operation, which is a smaller and later question.

It captures no execution plans and no wait information. Both exist elsewhere in
the tool, and adding them here would double the cost of the capture for facts
already available.

## Open questions, to be measured before implementation

1. The whole session on SQL Server 2012: the DDL as written here, the two events
   and their text options, the `event_file` target options, and
   `sys.fn_xe_file_target_read_file`. Every measurement in this document was
   taken on SQL Server 2025, and a 2012 floor that has never been run on 2012 is
   a claim rather than a floor.
2. `max_rollover_files` and its zero value, and what the session does when the
   volume fills. A capture that stops writing because there is no room must not
   look like a capture of a quiet window.
3. The size a real workload produces per minute, on at least two instances, so
   the consent prompt's estimate is a measurement rather than an arithmetic
   guess.
4. Whether `SQLFerret` ingests a rolled-over set of files as one capture, and what
   it does with a set whose first file was deleted by rollover. The handoff is
   only as good as its worst case.

None of these changes the design. All of them are details this document would
otherwise state with more confidence than it has earned.
