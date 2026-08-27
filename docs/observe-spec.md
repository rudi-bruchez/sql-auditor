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
with `--max-minutes`. How it is enforced is the uncomfortable part of this
design, and the first draft of this spec got it wrong: it said the session
carries the deadline as an option of its own. **It cannot.** `CREATE EVENT
SESSION` takes `MAX_MEMORY`, `EVENT_RETENTION_MODE`, `MAX_DISPATCH_LATENCY`,
`MAX_EVENT_SIZE`, `MEMORY_PARTITION_MODE`, `TRACK_CAUSALITY` and
`STARTUP_STATE`, and nothing that stops it after a while. There is no
server-side timer to lean on.

So the deadline is enforced **by the tool, at its next visit**. `start` records
the expiry in its state file and in the session's name. Any later `observe` —
`status`, `finish`, or a fresh run — finds a session past its expiry under the
name it owns and stops it before doing anything else. Self-healing on the next
pass, not expiring on its own.

That is weaker than it sounds and the spec should not pretend otherwise. If the
laptop dies during a `start`, or during a `--minutes 5` run for that matter, the
session keeps running until somebody runs `observe` again or a DBA notices it.
That is precisely the abandoned-capture scenario the minutes-only rule was
written against, and the honest statement is that `observe` reduces the window
rather than closing it.

Two things make the residue tolerable. The default target holds a bounded amount
of memory and writes nothing, so an orphan costs a few megabytes and some event
dispatch rather than a filling disk. And the fixed session name makes an orphan
findable by anyone, not only by the tool: a DBA who spots it knows what it is
and can drop it in one statement.

A **SQL Agent job** that stops the session at the deadline is the only real
server-side alternative. It is offered as `--agent-stop` rather than made the
default, because it needs the Agent to exist and be running and needs the right
to create a job — a second dependency and a second permission, on exactly the
locked-down instances where `--session` was supposed to help.

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
  `sys.query_store_query` and `sys.dm_exec_query_stats`. Resolution runs at
  `finish`, immediately after the session stops, so the plan cache is as warm as
  it will ever be and the loss is bounded by what was evicted during the window
  itself. The statements it cannot resolve are reported as unresolved hashes
  rather than dropped, and **the manifest must say when the Query Store was off
  for the database**, because resolution then had only the plan cache to work
  from and a large unresolved count means something different than it otherwise
  would.

`--detail` keeps the whole event rather than a bucket, for the cases where the
hash is not enough: a statement that has left the plan cache and was never in the
Query Store, or a question that needs the duration and the row count of each
call rather than how many calls there were. It is off by default, it says so in
the manifest, and it is what makes the difference between a capture carrying no
application data and one that might.

It has two possible targets, and the choice between them is a real one.

A **ring buffer** keeps the events in memory and returns them as XML from
`sys.dm_xe_session_targets.target_data`. Nothing is written to disk, no
directory has to exist, no permission is needed beyond the ones `observe`
already requires, and Go reads it with the standard library. For a short
capture that is plainly the better target, and it is the default for `--detail`.

Its two limits decide when it stops being the better target. It is bounded by
`MAX_MEMORY` and discards the oldest events when full, so a window that outruns
the buffer returns its tail while looking complete — which is why `observe`
must read the target's own `Buffers dropped` count and put it in the manifest
rather than presenting the events as a census. And `target_data` is known to
truncate large payloads, so the XML can come back incomplete for a big buffer
even when the memory holds everything. Verify that ceiling on a real instance
before choosing a default `MAX_MEMORY`, record the build it was measured on
since the behaviour has changed between versions, and choose the default against
the oldest version this tool supports rather than the newest one to hand. Do not
take a number from a blog post.

An **event file** has neither limit and is the target for a long capture. It
costs a directory the SQL Server service account can write to, which
`--detail --file <dir>` must take as an argument rather than guess, and a file
to clean up afterwards.

The rule between them: ring buffer up to a window and a rate the buffer can
hold, event file beyond it. `observe` should measure `batch_requests/sec` first,
compute what the window will produce, and say which target it is about to use
and why.

`MAX_DISPATCH_LATENCY` deserves a line of its own, because it will otherwise
produce the first support question. It defaults to 30 seconds: events sit in
their buffer until that latency elapses or the buffer fills, so `observe status`
can report zero events for the first half-minute of a capture that is working
perfectly. Stopping the session flushes the buffers, which is why `finish`
always sees them and a mid-flight read may not. Either set it low and say so, or
say so in the output of `status`; saying nothing means the first user concludes
the capture is broken.

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

Two consequences worth stating, because they will look like omissions
otherwise. There is no live view: `observe status` reports counts, not a stream.
And there is no per-event callback, so filtering happens in the session
definition where it belongs rather than in the client.

What the missing library does **not** rule out is reading a target. Both targets
below hand back XML over a normal query, and `encoding/xml` parses it. The
absence of a client library costs the stream, not the data.

**The histogram target needs no file and no library.** Its contents are in
`sys.dm_xe_session_targets.target_data`, an XML document read over the same
connection as everything else and parsed with the standard library. No path, no
share, no permission on the server's file system, nothing to clean up, and
nothing that Go cannot do. This is the main reason it is the default rather than
the event file, and it is why the histogram survives the absence of a client
library where a live stream does not.

**The ring buffer needs no file either.** Same DMV, same XML, same parser as the
histogram; only the shape of the document differs, one element per event instead
of one per bucket. This is what makes `--detail` usable on an instance where
nobody will grant a directory.

**The event file, when the window is too long for a buffer, comes back the way
the blocked process reports already do**: `sys.fn_xe_file_target_read_file` reads it
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

## The decoding layer

Everything above says what to read. This says who reads it, because the answer
is a package that does not exist yet and that `observe` cannot be built without.

**`collect/xevents`.** One job: turn a `target_data` document into typed Go
values. Two shapes to decode, matching the two targets that stay in memory.

- A **histogram** is a list of buckets, each a slot value and a count, plus the
  target's own attributes: how many buckets it holds, whether it overflowed, and
  how many events were not counted because it did.
- A **ring buffer** is a list of events, each with a name, a timestamp, its data
  fields and its actions, plus the target's dropped count.

It does not connect to SQL Server. It takes a string of XML and returns values.
That separation is the whole point: the SQL lives in the corpus where every
other query lives, the transport lives in the collector, and the parsing is a
pure function with no I/O — which is what makes it testable at all.

**It is tested against captures taken from real sessions**, committed as
fixtures rather than hand-written. A document invented to match the parser
proves the parser matches the invention. A document produced by an actual
`CREATE EVENT SESSION` on an actual instance, saved once and replayed forever,
proves something. The fixtures must be scrubbed before they are committed: a
ring buffer capture carries statement text, and the rule in `CLAUDE.md` applies
to test data exactly as it applies to code.

Three behaviours the package owes its caller, all three learned from mistakes
already made elsewhere in this repository.

**It reports what it could not read rather than returning less.** `target_data`
truncates large payloads, and truncated XML either fails to parse or — worse —
parses into a shorter list that looks complete. The parser must distinguish
"this document ended cleanly" from "this document was cut", and the caller must
put the difference in the manifest. Silently returning half a capture is the
same class of defect as the `.xel` pattern bug: an answer that looks like an
answer.

**It carries the drop count out with the data.** The count belongs to the
target, not to the events, so a caller that only asks for the events will never
see it. The function signature should make that impossible: return the counters
and the payload together, or return a struct that holds both.

**It does not guess at types it does not know.** An event field whose type the
package has no mapping for comes back as its string form, labelled as
unconverted. Guessing produces a number that is wrong in a way nothing
downstream can detect.

**Why it lives here and not in its own repository, for now.** It has one
consumer. An interface extracted for a single caller is an interface shaped by
that caller's accidents, and it freezes before anyone has learned what the
second caller needs. When a second consumer appears, extraction is a rename.

**Its explicit non-goal is the `.xel` binary format.** Nothing in this package
opens a file. Reading a `.xel` without SQL Server means reverse-engineering an
undocumented binary format, which is a project with its own scope, its own
risks and its own audience — an auditor handed a client's capture files and no
instance to read them on. That project should not be smuggled in as a package
of this one.

## Output

```
observe-<server>-<date>-<time>.zip
  MANIFEST.txt
  _run.json
  statements.json     one row per query_hash: executions, resolved text,
                      first and last seen, source of the resolution
  unresolved.json     hashes the server could no longer name, with counts
  events.json         only with --detail: one row per captured event, with
                      the target it was read from and the dropped count
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

- **Nothing survives a service restart, and an orphan is findable.** The session
  is created with `STARTUP_STATE = OFF`, so restarting SQL Server does not bring
  it back. What it does **not** guarantee is that nothing survives a crash of the
  tool: as the command surface section says at length, there is no server-side
  timer, so an orphaned session runs until the next `observe` stops it or a human
  does. `observe status` finds it by its fixed name and `observe finish --drop`
  removes it. Stating this as a guarantee, which an earlier draft did, would have
  been the more comfortable sentence and the false one.
- **A borrowed session is never dropped, never altered.** Not even to add an
  event it is missing; that is a refusal with a message, not a repair.
- **The tool refuses rather than degrades.** No permission, no Query Store for
  the resolution step, a session that already exists under the name it wanted:
  each is a distinct exit code and a sentence saying what to do.
- **The cost is measured before it is paid.** `observe` reads
  `batch_requests/sec` first and says what event rate to expect. At 221 batches a
  second a five-minute histogram capture is around 66,000 events into a few
  kilobytes of buckets; the same capture with `--detail` is a buffer, or a file,
  whose size the operator should be told before it is filled.

## What it does not do

It does not tie statements to a business operation on its own. It counts what
happened between two moments; the operator supplies the meaning by choosing
those moments. The report should say "during one *save order* performed at
14:32, the application issued 4,812 statements", and the tool provides the
4,812, not the *save order*.

It does not see inside a batch. `rpc_completed` and `sql_batch_completed` fire
once per call from the client, so a loop written in T-SQL — a `WHILE`, a cursor,
a procedure iterating a set — arrives as one event however many statements it
runs. That is consistent with what the command is for, which is the loop on the
application side, and it is worth stating because a reader coming from
`021.query-store-detail.sql` will have seen per-statement figures and expect
them here. Capturing `sql_statement_completed` and `sp_statement_completed`
instead would see inside the batch and multiply the event volume by whatever the
batch does, which is the trade this command declines to make by default.

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
argument against is scope: the command would stop being "count the calls". It is
also partly answered by `--detail` on a ring buffer, which carries duration and
row count per event without a second source.

What `MAX_MEMORY` a ring buffer needs for a five-minute window at a few hundred
statements a second, and at what size `target_data` starts truncating. Both are
measurable in an afternoon against a real instance, and both should be measured
rather than assumed: the ring buffer is the target that makes `--detail` work
without asking anyone for a directory, so its ceiling decides how much of the
command's usefulness survives a locked-down server.

Whether `collect/xevents` should decode the ring buffer's event fields eagerly
or lazily. Eager is simpler and costs memory on a large buffer; lazy keeps the
XML around, which is the thing that may be truncated. Probably eager, with the
truncation check done before any decoding starts.

Whether the state file that `start` leaves behind belongs next to the archive or
in the user's config directory. Next to the archive is discoverable; in the
config directory survives someone cleaning out a working folder.

Whether `--minutes` should accept a fraction for a very short operation. Probably
not: an operation shorter than a minute is better measured with `start` and
`finish`, and admitting `0.5` invites `0.01`.
