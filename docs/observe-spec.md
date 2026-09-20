# `sql-auditor observe`, specification

Status: draft, not implemented. Written 27 August 2026, revised the same day
after two reviews, rewritten on 20 September 2026 after a panel of three readers
and a round of measurement broke the design it then had, and revised again the
same day after a panel of five readers ran the rewrite against SQL Server 2025.
The section "What the measurement changed" records what was wrong with the design
this replaces, because that design is the obvious one to propose again, and "What
the second panel changed" at the end records what was wrong with the replacement.

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
> duration, the CPU time, the reads, the writes and the number of rows. Calls
> your connection pool makes on its own account, to reset a connection before
> handing it to the next user, were excluded, as was this tool's own traffic. If
> the instance was busy enough for the session to drop events rather than slow
> your workload down, the number it dropped is recorded below.
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

`observe status` says whether a session is running, since when, how much it has
written, how many events it has dropped, and how much time is left before its
deadline.

It does not say how many events it has recorded, and that is a correction to an
earlier draft rather than a modesty. For a file target there is no event count in
the dynamic management views: `sys.dm_xe_sessions` has the drops and
`total_bytes_generated`, and the target's own `target_data` reports buffers
written rather than events. The only count is a scan of the capture file, which
is unbounded work on a file that may be a gigabyte, and which would be wrong
anyway, since whatever is still in a buffer is not in the file yet. The old
design could answer this cheaply because a histogram is a list of counts;
this one cannot, and `status` reports the bytes, which is the number the DMV
actually holds.

The state file lives next to the archive directory, where the operator already
looks, and binds server, database, session name and the capture file stem.
`finish` deletes it on success. A `finish` that cannot find it, run from another
machine or after a cleaned working folder, does not refuse: the stem on the
server carries the start time and the maximum, so the window can be reconstructed
from the session itself. What is lost is the operator's intent, and the manifest
says the window was reconstructed rather than declared.

### `observe start --for <minutes>`

A `start` whose deadline is declared at the outset. `status` shows the time left
or the overrun, any later invocation of `observe` stops a session past its
deadline, and a `finish` that arrives late writes the archive with the window it
really measured plus a manifest line saying by how much the capture ran past its
intent. The archive never claims to have measured the window that was asked for.

It does not stop by itself, and an earlier draft that said so was wrong. There is
no server-side timer, and nothing runs between the invocations of a command line
tool. What "declared" buys is that the deadline is written where the next visitor
can read it, which is the file stem, so the enforcement does not depend on the
next visitor's own flags. The section on the maximum lifetime is the whole of it,
and the honest summary is that `--for` bounds the capture at the next invocation
of `observe` by anyone, and not before.

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
`sql-auditor observe`. Any later `observe`, on any machine, finds the session
under the known name, reads its start time and its declared maximum out of the
capture file stem, and stops it if it has outlived it.

Both halves of the deadline come from the stem, and `sys.dm_xe_sessions.create_time`
is not one of them. It is tempting, being the time of `STATE = START` and right
beside the counters, and it is wrong: measured, a `STOP` followed by a `START`
moves it while the stem does not, so a session a DBA restarted by hand has its
deadline slide forward by however long the pause lasted, indefinitely and
invisibly. An earlier draft of this document used `create_time` in one section
and the stem in another without noticing there were two clocks.

The declared maximum is read from the file stem and not from the flags of the
process doing the reading. An earlier draft derived it from the sweeper's own
`--max-minutes`, which gives the wrong answer in both directions: a `--minutes 5`
run whose laptop died runs until minute 60 because nothing recorded the 5, and an
operator running `status` with the default 60 stops and drops a colleague's
capture authorised for 120, abandoning a file that was being filled. Two
deadlines were in the document and neither section said which it meant.

That is weaker than a guarantee and the spec does not pretend otherwise. If the
laptop dies during a `start`, the session keeps running until somebody runs
`observe` again or a DBA notices it. `observe` reduces the window; it does not
close it. And with a file target the residue is no longer a few megabytes of
memory: an orphan goes on writing to disk, which raises the stakes of the sweep
and is the reason the next section exists.

A session that exists in `sys.server_event_sessions` but not in
`sys.dm_xe_sessions` is a third state the previous draft had no rule for, and it
is the ordinary aftermath of a service restart, `STARTUP_STATE = OFF` doing
exactly what it promises. There is no `create_time` for it, so no deadline can be
computed, and a sweep that requires one would leave the name blocked forever. A
stopped session matching the fingerprint is a remnant: it is dropped, whatever
its age, and the run says it did.

### A fixed name is not proof of ownership

The sweep may not stop and drop a session merely because it carries the known
name and an old `create_time`. A DBA can have created that name by hand, reused
it after a previous run, or be looking at a failed one. Stopping it would be an
unauthorised destructive action on a production instance, and with a file target
it also abandons a file somebody was filling.

So the managed session has a server-verifiable fingerprint, and it has to be one
a recovering process can actually evaluate. That rules out the obvious
components. The predicate carries the database id as a literal, read back as
`([database_id]=(10) AND ...)`, and a process that has lost its state file does
not know which database the run was for; requiring the predicate to match would
mean the sweep never fires in exactly the case it exists for. Requiring only the
shape and ignoring the literals would make a DBA's own session on another
database match, which this section forbids.

The fingerprint is therefore, in order of what it proves:

- the target's `filename`, whose stem begins `sql-auditor-observe-`. That prefix
  is what says the session is ours, and the rest of the stem carries the run's
  start time and its declared maximum, which is what lets the sweep decide
  without local state;
- the two events, by name, and the presence of the text option on each;
- the actions, by name;
- the target type.

The predicate is compared and reported but not required to match, since its
literals belong to a run rather than to the tool. A same-named session that fails
the first four is never altered: `observe` refuses, prints what it found and what
it expected, and says the session must be dealt with by hand.

Reading that back needs its own queries and not the ones this document previously
pointed at. `queries/10.system/062.xe-sessions.sql` returns sessions, their
options, one row per event, and targets with their fields. It joins
`sys.server_event_session_actions` nowhere, and it leaves the predicate out
deliberately, saying so in its own header: a filter can carry a literal, and it
is the one part of a session definition that can. Two of the components above are
not read by the file cited as reading them. `062` stays what it is, an inventory;
the fingerprint gets its own reads, of `sys.server_event_sessions`,
`sys.server_event_session_events`, `sys.server_event_session_actions` and
`sys.server_event_session_fields`, all four of which were measured readable by a
login holding only the two rights in the permissions table, with
`VIEW ANY DEFINITION` explicitly denied.

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

That order is for the modes that create something. `status`, `stop` and `finish`
create nothing, so they do not prompt: they validate, connect, describe and
sweep. Writing one order for all five subcommands, which an earlier draft did,
would have had `observe status` printing a `CREATE EVENT SESSION` for approval
before it could tell the operator anything.

`observe status` is still the awkward case and is resolved rather than ignored:
it sweeps like the others, which means it can issue `STOP` and `DROP`. That is
the one documented exception to "no DDL before consent", and it is narrow
because the sweep acts only on a session the fingerprint proved is ours. It
prints what it dropped, and its help text says it can.

One refusal cannot be made to fit before the instance is touched, and it is
listed here rather than pretended away. A capture directory the service account
cannot write to is not detected by `CREATE EVENT SESSION`, which validates no
path and succeeds; the failure arrives at `STATE = START`, `Msg 25602` carrying
the operating system error, with the session already in the catalog. It is
therefore a code 1 and not a code 3: the instance was altered, the compensating
drop runs, and the run says the directory was refused by the server. Where the
file goes explains why the preflight probe that would have made it a 3 was
withdrawn.

### Preflight, and why it departs from the collector's rule

`collect/preflight.go` states its discipline at the top of `Capabilities()`: a
probe reads one row from the cheapest object the real collectors depend on, and
it rejects `HAS_PERMS_BY_NAME` by name, on the grounds that a `SELECT` cannot
disagree with reality.

`ALTER ANY EVENT SESSION` has no such object. The only probe faithful to that
rule is a `CREATE EVENT SESSION`, which is the very thing that must not happen
before consent. So `observe` uses `HAS_PERMS_BY_NAME(NULL, NULL, 'ALTER ANY EVENT
SESSION')`, and this document records that as a deliberate departure rather than
letting an implementer discover the contradiction. It was measured to agree with
reality on this build in both directions: without the grant the function returns
0 and `CREATE` fails with `Msg 15247`; with the grant it returns 1 and `CREATE`
succeeds. That agreement is a measurement on one build, not the property the
collector's rule is protecting, which is why it is written down here.

### Cancellation

Ctrl-C stops the session, drops it, and writes a partial archive whose manifest
records the shortened window and says it was interrupted. Leaving the session
behind is the failure this whole design is nervous about.

`collect/cancel.go` is named in an earlier draft as the discipline to reuse, and
that reference is wrong in a way that would have shipped. Those 65 lines decide
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

Both set their text option explicitly, `SET collect_statement = 1` on
`rpc_completed` and `SET collect_batch_text = 1` on `sql_batch_completed`. An
earlier draft said these are off by default and warned an implementer against
omitting them. Measured, both default to `true` in `sys.dm_xe_object_columns`,
and a session built with neither `SET` clause captures the text. They are still
set explicitly, for the reason the next section gives about every other option,
which is that an inherited default is a version-dependent default. The warning
was the false part, not the instruction.

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

### The two exclusions, and the one that must not be written the obvious way

`observe` excludes its own traffic on `sqlserver.client_app_name`, the tool
setting `Application Name` in its connection string, and never on a session id.

The name in the predicate is the one the run is actually using and not the
literal `sql-auditor`. `collect/config.go` defines `DefaultAppName` as that
string and then lets `SQL_APPLICATION_NAME` replace it, and `collect/watch.go`
opens a second connection with a suffix appended, so a predicate written against
the constant excludes the wrong connection in both cases.

The limitation that remains is worth stating rather than hiding: an application
that is itself called `sql-auditor` is excluded from its own audit, silently. The
command's help says so, and the end-to-end test that proves the exclusion works
is the same test that would pass if this happened.

An earlier draft specified the session id, read from `@@SPID` before the
`CREATE`. That is the obvious spelling and it is a silent capture killer. The
predicate stores the number as a literal, read back from
`sys.server_event_session_events` as `([database_id]=(10) AND
[sqlserver].[session_id]<>(57))`, and the connection that supplied it then goes
away, `start` being a command that exits. SQL Server reuses a freed session id
immediately: measured, two consecutive client processes were both given id 78.
From that moment the session silently discards every call made by whichever
application connection inherited the id, and the capture comes back short or
empty, with a well formed file and an exit code of zero. The exclusion also
failed at its own job, since `finish`, `status` and `stop` run in other
processes under other ids and were never excluded by it.

The application name is stable for the life of the session, covers every later
invocation of the tool, and is verifiable in the session definition rather than
in a number nobody can check afterwards. Measured on the same fixture, twelve
events kept for the driving application and none for the excluded name.

The second exclusion is `object_name <> N'sp_reset_connection'` on
`rpc_completed`. A pooled client driver issues that procedure every time it hands
a connection to the next user, and it arrives as an ordinary `rpc_completed`:
measured, six of twelve captured events for a workload of five real calls. The
number this command exists to produce is the one that goes to an application
vendor, so half of it being pool housekeeping is not a rounding error.

The spelling matters and the obvious one does not compile. `sqlserver.object_name`
is refused, "the event attribute or predicate source could not be found"; the
bare `object_name` is accepted, the column belonging to the event rather than to
the `sqlserver` package. The clause goes on `rpc_completed` only, that event
being the only one of the two that has the column.

### Options

`MAX_MEMORY` set explicitly. `EVENT_RETENTION_MODE = ALLOW_SINGLE_EVENT_LOSS`:
on a busy instance, dropping an event is preferable to stalling the workload, and
the drop count is reported so the reader knows. `STARTUP_STATE = OFF`, so a
service restart does not resurrect the session.

`MAX_DISPATCH_LATENCY` is set low, and the reason is the opposite of what an
earlier draft of this section said. That draft reported a measurement, true in
itself, that a read of a histogram's `target_data` under a 30 second latency saw
events one second after they fired, and generalised it into "the latency governs
buffering, not what a reader can see". It does not generalise. With a file
target, measured twice, the events are invisible until the buffer is flushed:
under a 30 second latency a read of the file immediately after the workload
returned zero events, and the same file after `STATE = STOP` returned all of
them.

So for this command the latency governs exactly when a reader can see anything,
which makes it the number behind `status` reporting nothing on a capture that is
working, and it governs how much sits in memory rather than on disk when the
process dies. Both arguments point the same way. The value is in the table of
defaults below rather than left to the implementer.

The correction is recorded in full rather than quietly replaced, because the
sentence it replaces was itself written to correct an earlier reviewer, and a
correction made under review pressure is the least reviewed line in a document.

### The file stem, which carries more than a name

The capture file stem is per run and it is built rather than fixed:

```
sql-auditor-observe-<utc yyyyMMddTHHmmss>-<max minutes>.xel
```

Three problems are solved by that one decision, and an earlier draft that left
the filename unspecified had all three.

A fixed stem accumulates. Measured: a session configured at a path, run, stopped
and dropped, then created again at the same configured path, does not overwrite.
SQL Server writes a second `_0_<ticks>.xel` beside the first, and the wildcard
that reads the capture then reads both. A first run of 11 events followed by a
second of 9 reported 20. The number this command exists to produce would have
been the sum of every audit ever taken at that path, with nothing on screen to
say so. It is also what makes the rollover check below work: a count of files is
only meaningful against a stem that belongs to one run.

The deadline has nowhere else to live. The sweep is specified to work from a
machine that has lost every local file, and the only server-side facts it has are
the session name and `create_time`. Deriving the deadline from the sweeping
process's own `--max-minutes` is what an earlier draft did, and it means an
operator running `status` with the default 60 stops a colleague's capture that
was authorised for 120. Putting the run's maximum in the stem makes the deadline
a property of the session, readable by anyone, and removes the second deadline
the document was carrying without noticing.

The fingerprint needs something matchable. The predicate cannot serve: it carries
the database id as a literal, which the recovering process does not know. The
stem is readable from `sys.server_event_session_fields`, it is shaped
predictably, and its prefix is what proves the session is ours.

### The target

`package0.event_file`, with every option explicit, the values being in the table
of defaults below and not left to the implementer.

Rollover deletes. A capture that outruns `max_file_size` times
`max_rollover_files` loses its beginning, silently, and what remains looks like a
complete capture of a shorter window. A count of files at the end does not detect
it: a run that exactly fills its allowance and a run that overflowed by one both
end with the same number of files.

So the loss is detected against an identity rather than a count. Immediately
after the session starts, and before the window is declared open, the tool reads
the running target's current file name from `target_data` and stores it in the
state file and in `_run.json`. Rollover loss is that name being absent from the
final wildcard set. If the name cannot be read at that moment, the run says
rollover is unobservable rather than reporting a clean capture.

That read also removes the string surgery entirely, which matters because the
obvious way to do it is wrong twice over. SQL Server appends `_0_<ticks>.xel` to
the stem of the configured name, so a session configured as
`/var/opt/mssql/log/observe.xel` writes
`/var/opt/mssql/log/observe_0_134343862333780000.xel`. Appending a wildcard to
the configured name, extension included, matches nothing. Measured, that failure
is silent in the common case: a pattern matching no file in a directory that
exists returns zero rows and no error, and only a nonexistent directory raises
`Msg 25718`. So an empty capture is the expected symptom of a wrong pattern, not
an exotic one.

The repository has already shipped that bug once, in
`queries/10.system/063.blocked-process-reports.sql`, where the extension test
searched the reversed name for the extension spelled forwards and so never
matched. It was found when a collection reported an empty capture on an instance
whose ring buffer held two reports and whose files, once read correctly, held
205. Its current code fixes the extension and is still not a pattern to copy
here: it splits the directory with `CHARINDEX('\', REVERSE(...))`, the Windows
separator only, and run against the Linux paths this design uses it produces a
concatenated nonsense path. The lesson to inherit from 063 is that the pattern is
worth measuring, not its expression.

### Where the file goes

The directory is the operator's choice and is never guessed, because it has to be
one the SQL Server service account can write to and one with room. `observe`
proposes the directory holding the error log, and the emphasis is on the
directory: `SERVERPROPERTY('ErrorLogFileName')` returns the path of the log file
itself, `/var/opt/mssql/log/errorlog`, so the proposal is that path with its last
component removed. Using the property's value as a directory yields
`/var/opt/mssql/log/errorlog/observe.xel`, which is a path under a file.

The proposal is a convenience and carries a warning with it: the error log
directory is often on a volume sized for logs. The flag that overrides it is
`--file-dir` and not `--file`, since it takes a directory and the operator does
not choose the file name, which the stem above decides.

A bad directory is not caught where an earlier draft of this document assumed it
was. Measured: `CREATE EVENT SESSION` validates no path at all and succeeds, and
the failure arrives at `STATE = START`, as `Msg 25602` naming the operating
system error, with the session left in the catalog.

That draft answered with a preflight probe: create a throwaway session at the
directory, start it, stop it, drop it, so a bad path could be refused before the
instance was touched. The probe is withdrawn, and the reasons are measurements
rather than second thoughts.

Run against an instance where something already held the managed name, the
probe's four statements refuse the `CREATE` with `Msg 25631` and the `START` with
`Msg 25705`, and then the `STOP` and the `DROP` succeed. It stopped and dropped a
running session it did not own, before consent and before any ownership test.
Giving the probe a name of its own repairs that and not the rest: a probe that
succeeds leaves a `.xel` file, and nothing in this tool's permission set deletes a
file, so the manifest's promise that nothing else was modified stops being true.
And a directory that is merely absent is not refused at all, it is created, two
levels deep if the path says so.

Without a probe the same failure is cheaper. `Msg 25602` is catchable in `TRY`,
the compensating `DROP` leaves no session, and no file exists because the file
could not be written. So a bad capture directory is an exit code 1 after consent,
not a 3 before it, and the only guard against a typo is that the consent prompt
prints the path. The prompt also says the directory will be created if it is
absent, since that is what the server does.

### The session, rendered

Every option this document insists be explicit is worthless if the document does
not say what it is. Four of the five readers of the previous draft stopped at the
same place: there was no artefact to run.

```sql
CREATE EVENT SESSION [sql-auditor observe] ON SERVER
  ADD EVENT sqlserver.rpc_completed (
      SET collect_statement = 1
      ACTION (sqlserver.database_name, sqlserver.client_app_name)
      WHERE database_id = @database_id
        AND sqlserver.client_app_name <> N'sql-auditor'
        AND object_name <> N'sp_reset_connection'),
  ADD EVENT sqlserver.sql_batch_completed (
      SET collect_batch_text = 1
      ACTION (sqlserver.database_name, sqlserver.client_app_name)
      WHERE database_id = @database_id
        AND sqlserver.client_app_name <> N'sql-auditor')
  ADD TARGET package0.event_file (
      SET filename = @stem,
          max_file_size = @max_file_size_mb,
          max_rollover_files = @max_rollover_files)
  WITH (MAX_MEMORY = @max_memory_kb KB,
        EVENT_RETENTION_MODE = ALLOW_SINGLE_EVENT_LOSS,
        MAX_DISPATCH_LATENCY = @dispatch_latency_s SECONDS,
        STARTUP_STATE = OFF);
```

Two spellings in there are not free choices and were established by running the
alternatives. There is no comma between the last `ADD EVENT` and `ADD TARGET`,
the plausible one returning `Msg 102`. And the pool exclusion is `object_name`
and not `sqlserver.object_name`, which is refused.

| Parameter | Default | Why, and what bounds it |
| --- | --- | --- |
| `@max_memory_kb` | 4096 | four buffers of a megabyte, the smallest that does not force partitioning decisions |
| `@dispatch_latency_s` | 3 | the delay before `status` and a `finish` can see anything, see Options |
| `@max_file_size_mb` | 128 | one file per few minutes at a few hundred calls a second |
| `@max_rollover_files` | 8 | a gigabyte of capture before the beginning is at risk |

These are defaults with reasons, not measurements, and they are the subject of
open question 3. What matters for comparability is that two implementers reading
this document produce the same session, which was not true of the previous draft.

The rendered statement, with every parameter substituted, is what the consent
prompt shows and what `_run.json` records, captured at execution rather than
recomposed.

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
would contradict the discipline this section is built on.

The rate answers how many calls, and the consent prompt needs bytes. Those are
not the same question and an earlier draft slid between them: two workloads at
the same rate differ by orders of magnitude in capture size, a chatty
parameterised application against one that sends thousand-line generated
statements. So the prompt states the expected call count, which is measured, and
a size that is explicitly labelled an estimate, from the call count times a
bytes-per-event figure whose source and window are named beside it. Open question
3 is what turns that figure into a measurement.

Batch Requests/sec was itself checked rather than assumed, since a counter that
ignored RPCs would understate exactly the parameterised applications this command
targets. It does not: fifty procedure calls sent over RPC moved the counter by
fifty-one, the extra being the reading batch.

## Permissions

For a command whose whole frame is consent, the first question is what rights it
needs:

| Right | Scope | Needed for |
| --- | --- | --- |
| `ALTER ANY EVENT SESSION` | server | creating, starting, stopping, dropping the session |
| `VIEW SERVER STATE` | server | `sys.dm_xe_sessions`, `sys.dm_xe_session_targets`, `sys.fn_xe_file_target_read_file` |

That is the whole list for the SQL Server the measurement was taken on, and it
was measured rather than derived: a login holding those two, with no database
mapping and no server role, created, started, stopped and dropped a file-target
session, read the session counters, read the four catalog views the fingerprint
needs with `VIEW ANY DEFINITION` explicitly denied, resolved
`SERVERPROPERTY('ErrorLogFileName')` and counted the capture file's events. The
Query Store and plan cache rights the previous design needed are gone with the
resolution step.

Two things the table does not cover, and saying so is what keeps it honest. The
SQL Server service account needs write access to the capture directory, which is
not a permission of the connecting login and is probed separately. And the same
list has not been established on the version floor below.

The consent prompt prints the permissions next to the DDL. Preflight checks them
before the session is created, the way `collect/preflight.go` already does for the
collector, and `collect/grants.go` grows the corresponding `GRANT`. Without that,
"the tool refuses rather than degrades" is a promise with nothing behind it.

Version floor: SQL Server 2012 is the intended floor, the same as the corpus, and
it is not yet a promise. The intent is a property of Extended Events rather than
a convention: the session syntax changed substantially across 2008 and 2008 R2
and settled at 2012, so no single DDL can cover the older builds, and 2012 is the
oldest that one can.

Every measurement in this document was taken on SQL Server 2025 RTM-CU7. Until
the rendered session above, the permission list and
`sys.fn_xe_file_target_read_file` have been run on a 2012 instance, the document
says verified on 2025 and claims nothing older. Two columns in the capability
table are already suspect on that floor, `page_server_reads` and `spills` being
later additions. Promoting 2012 from intent to floor is open question 1, and
until it happens the command refuses a build below the one it was measured on
rather than degrading on it.

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
| 3 | refused before touching the instance: missing permission, a session already running under the name, a same-named session that is not ours, a command line that does not parse |

They are the interface a script uses, so they are listed rather than promised.

A capture directory the server refuses is a 1 and not a 3. Two earlier drafts got
this wrong in opposite directions: the first listed "a bad path" under 3 without
noticing that no path is validated at `CREATE`, and the second invented a
preflight probe to make the 3 true, at the price described above. The instance is
altered by the time the directory is known to be bad, and the exit code says so.

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
5. An actual rollover, driven hard enough to delete a file, so that the loss
   detection above is a measured procedure rather than a described one.

None of these changes the design. All of them are details this document would
otherwise state with more confidence than it has earned.

## What the second panel changed

Five readers ran the rewrite against SQL Server 2025: agy and codex, each with a
directive and a neutral prompt, and a Claude subagent on the neutral one. The
overlap between any two was small and each found something no other did, which is
the argument for running all five rather than the best two.

- The `@@SPID` exclusion emptied the capture. The predicate stores the id as a
  literal, the connection that supplied it exits, and the server hands the freed
  id to the next client: measured, two consecutive processes both got id 78. The
  exclusion is now on the application name, verified in both directions.
- Connection-pool resets were half the capture. Six of twelve events for a
  workload of five real calls, and the number the whole command exists to produce
  is the one that goes to a vendor.
- The proposed fix for that did not compile. `sqlserver.object_name` is refused
  and the bare `object_name` is accepted, which is a reminder that a reviewer's
  correction deserves the same check as a reviewer's finding.
- There was no session to run. Four of the five stopped at the same place: the
  document demanded that every option be explicit and gave no values, no file
  name and no rendered DDL, so a verbatim execution was impossible and two
  implementers would have produced two incomparable sessions.
- The text options are not off by default, contradicting a product fact this
  document asserted from memory in a section teaching an implementer what not to
  get wrong.
- The dispatch latency paragraph, itself written to correct an earlier reviewer,
  generalised a histogram measurement to a file target where it is false. Events
  are invisible until flushed, measured twice.
- There were two deadlines and no section said which it meant, so an operator
  running `status` with the default maximum would stop a colleague's authorised
  capture. The maximum now travels in the file stem.
- A fixed file name accumulates captures across runs, so the second audit of an
  instance reported the sum of both.
- Rollover loss cannot be detected by counting files, since a run that exactly
  fills its allowance and one that overflowed look identical.
- `queries/10.system/062.xe-sessions.sql` was cited as reading the fingerprint
  and reads neither the actions nor the predicate, deliberately in the second
  case.
- `collect/preflight.go` rejects `HAS_PERMS_BY_NAME` by name, so the reuse this
  document claimed argues against the only available probe.
- A bad capture directory is not caught at `CREATE`, which validates no path, so
  exit code 3 could be issued after the instance had been altered.
- `SERVERPROPERTY('ErrorLogFileName')` returns a file and not a directory.
- 063's rollover fix splits the directory on the Windows separator only and
  produces nonsense on the paths this design uses.
- A rate in calls per second does not estimate bytes.
- A session stopped but still in the catalog, which is what a service restart
  leaves, had no rule and would have blocked the name forever.
