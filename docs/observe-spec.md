# `sql-auditor observe` — specification

Status: draft, not implemented. Written 27 August 2026, revised the same day
after two reviews.

## What it is for

`collect` reads what the server already knows. There is one class of finding it
cannot reach that way: **what the application does during one business
operation**. A row-by-row loop, an N+1 pattern, a chatty save — these are
recognised by counting the calls a single user action produces, and the server
keeps no record tying calls to actions.

The Query Store gets close. It knows a statement ran millions of times last week
and that a large share of those fell in one hour, which is enough to say a loop
exists. It cannot say *which* operation the loop belongs to, or how many rows
one operation writes. That number is the whole argument when the finding goes to
an application vendor, and getting it requires watching while somebody performs
the operation.

`observe` is that watch. It runs an Extended Events session for a bounded time,
counts the statements, and returns the counts.

## Why it is a separate command

The manifest of every `collect` archive says this, and clients read it:

> The collector issues only read-only SELECT statements against system catalog
> views and dynamic management views, and it does not read any user or
> application table. […] It creates no permanent object: nothing that belongs
> to this server or its databases is created, altered or deleted, and no data
> of yours is written anywhere by this tool.

`CREATE EVENT SESSION` creates a permanent object. That sentence is why a DBA runs this tool on
production without auditing it line by line, and it is worth more than any
feature. A promise with one exception is not a promise.

The stronger form of the argument is about who approves. The value of that
paragraph is that a DBA can approve it **without reading the corpus**. An opt-in
step inside `collect` creates two states of the manifest, and every approver
then has to ask which one they are signing. Believing a flag makes that legible
is the weak version of this design.

So `observe` is a different command, with a different manifest, requiring a
different consent. `collect` stays restituable without discussion.

### What `observe`'s own manifest says

This is the deliverable of the whole design, and it is drafted here rather than
invented at implementation time:

> This capture was taken by creating an Extended Events session on the instance,
> running it for the window recorded below, reading what it counted, and
> dropping it. The exact statements that created and dropped it are in
> `_run.json`. The session subscribed to two events, `rpc_completed` and
> `sql_batch_completed`, which fire once per call made by a client. It counted
> those calls per statement shape; it did not capture the text of any statement,
> nor any parameter value, nor any login or host name.
>
> The statement text in `statements.json` was resolved afterwards, from this
> instance's Query Store and plan cache, by matching the shapes that were
> counted. Text that comes from the plan cache can contain literal values
> written into the SQL by the application, so treat this file as potentially
> carrying data from your tables. Text that comes from the Query Store is
> parameterised and does not.
>
> Nothing was modified on this instance apart from the session named below,
> which was created and removed by this run.

## Command surface

Durations are always in **minutes**. Seconds invite a value that expires before
the operator has switched windows, and hours invite a session left running over
a weekend.

`--max-minutes` caps every mode, not only `start`, and defaults to 60. A
`--minutes 4320` is refused rather than obeyed.

### Timed run

```
sql-auditor observe --minutes 5 --database SALESDB
```

Creates the session, runs it for five minutes, reads the result, drops the
session, writes the archive. One invocation, nothing left behind, nothing to
remember.

This is the right mode when the workload is continuous and any five minutes are
representative.

**Ctrl-C stops the session, drops it, and writes a partial archive** whose
manifest records the shortened window and says it was interrupted. An
interrupted run is the commonest way to abandon a capture, and leaving the
session behind is the failure this whole design is nervous about. `collect`
already has this discipline in `collect/cancel.go`; `observe` uses the same one.

### Start and finish

```
sql-auditor observe start --database SALESDB
  ... the operator asks a user to run the operation in the application ...
sql-auditor observe finish
```

This is the mode the timed run cannot replace. The operation you want to measure
takes as long as it takes, it happens when somebody presses a button, and the
useful window is exactly the one between "go" and "done".

`start` creates the session, snapshots its counters, writes a state file, and
returns. `finish` reads the counters again, subtracts the snapshot, drops the
session and writes the archive.

`observe status` says whether a session is running, since when, and how many
events it has counted so far — which is the sum of the histogram's buckets, not
a live count.

#### The maximum lifetime, and how it is really enforced

An earlier draft of this spec said the session carries its deadline as an option
of its own. **It cannot.** `CREATE EVENT SESSION` takes `MAX_MEMORY`,
`EVENT_RETENTION_MODE`, `MAX_DISPATCH_LATENCY`, `MAX_EVENT_SIZE`,
`MEMORY_PARTITION_MODE`, `TRACK_CAUSALITY` and `STARTUP_STATE`, and nothing that
stops it after a while. There is no server-side timer to lean on.

So the deadline is enforced **by the tool, at its next visit**, and the mechanism
has to survive the loss of everything local. The session name is fixed —
`sql-auditor observe` — and the deadline is **derived from the session's own
start time**, `sys.dm_xe_sessions.create_time`, plus `--max-minutes`. Any later
`observe`, on any machine, finds the session under the known name, reads when it
started, and stops it if it has outlived its maximum before doing anything else.

Deriving the deadline rather than storing it is what makes the fixed name and
the self-healing consistent. A name carrying an expiry is not a fixed name, and
a `status` that has lost its state file could not construct it.

That is weaker than a guarantee and the spec does not pretend otherwise. If the
laptop dies during a `start` — or during a `--minutes 5` run — the session keeps
running until somebody runs `observe` again or a DBA notices it. `observe`
reduces the window; it does not close it.

Two things make the residue tolerable. The default target holds a bounded amount
of memory and writes nothing, so an orphan costs a few megabytes and some event
dispatch rather than a filling disk. And the fixed name makes an orphan findable
by anyone: a DBA who spots it knows what it is and can drop it in one statement.

**One `observe` at a time per instance.** A fixed name means a second operator,
or the same operator on a second database, collides. `start` finding a session
under its name has exactly two branches, and they are the same check: past its
deadline, stop it and proceed; within it, refuse and say who is running what
since when.

A **SQL Agent job** stopping the session at the deadline is the only real
server-side alternative. It is `--agent-stop` rather than the default, because
it needs the Agent to exist and be running and the right to create a job — a
second dependency and a second permission, on exactly the locked-down instances
where `--session` was supposed to help. The job deletes itself after stopping
the session, and the self-healing sweep removes expired jobs alongside expired
sessions, because a job left behind is a second class of orphan.

### Reusing a session

```
sql-auditor observe --minutes 5 --session "RBAR watch"
```

A DBA may already have a session tuned for their instance. And on a locked-down
instance the DDL may have been done in advance by someone with the rights,
leaving the auditor only the reading.

When `--session` names an existing session, `observe` **creates nothing, alters
nothing and drops nothing** — including at `finish`, which otherwise drops. It
validates and refuses rather than repairs, and the refusal lists the events and
the target it expected, so the session can be corrected by hand in one pass.
What it validates, reading the same DMVs as
`queries/10.system/062.xe-sessions.sql`:

- the events it needs are subscribed;
- the target it needs exists;
- **the session is running.** Snapshotting a stopped session, waiting, and
  subtracting yields a zero delta indistinguishable from "the operation issued
  no statements", which is precisely the answer-that-looks-like-an-answer this
  document objects to everywhere else. Starting it would be an alteration, so
  the answer is to refuse.

The archive records that the session was borrowed, so a reader knows the tool
did not shape what it measured.

### Permissions

For a command whose whole frame is consent, the first question is what rights it
needs, and the answer differs by mode:

| Right | Scope | Needed for |
| --- | --- | --- |
| `ALTER ANY EVENT SESSION` | server | creating, starting, stopping, dropping the session |
| `VIEW SERVER STATE` | server | `sys.dm_xe_sessions`, `sys.dm_xe_session_targets`, the plan cache |
| read access in the target database | database | resolving hashes against its Query Store |

With `--session`, the first line is not required: borrowing a session needs only
the reading rights. That is the point of the mode, and it belongs where a DBA
deciding what to grant will read it.

The consent prompt prints the permissions next to the DDL. Preflight checks them
before the session is created, the way `collect/preflight.go` already does for
the collector, and `collect/grants.go` grows the corresponding `GRANT`. Without
that, "the tool refuses rather than degrades" is a promise with nothing behind
it.

Version floor: SQL Server 2012, the same as the corpus, since the histogram
target, the ring buffer target and both events predate it. Verified claims about
older builds belong in `docs/verification-2012.md` when the time comes.

## What is captured

Two events, `rpc_completed` and `sql_batch_completed`, which between them cover
every statement a client driver sends.

### The histogram, and the two things that must be set explicitly

The default target is a **histogram bucketized on the `sqlserver.query_hash`
action**. Both halves of that sentence are load-bearing.

**It is an action, not a column.** Neither event has a `query_hash` column, so
the session must add `sqlserver.query_hash` as an action and the target must be
told `source_type = 1`. This is not a footnote: with the default
`source_type = 0`, the target requires `filtering_event_name`, which means one
histogram bucketizes exactly **one** event. Bucketizing on the action is what
makes "two events, one histogram" possible at all. An implementer who takes the
column path will build the session, find it covers half the workload, and
redesign.

**`slots` must be set.** It defaults to 256, rounded up to a power of two, and a
real application workload passes 256 distinct statement shapes in seconds. Past
that the target does not drop events: it folds different statements into one
bucket, which corrupts the single number this command exists to produce. Set it
explicitly, sized for the instance, and put the target's overflow attributes in
the manifest beside the drop count.

What the histogram buys:

- One integer per distinct statement shape. A five-minute capture at a few
  hundred batches a second is a few kilobytes, where the same capture with
  statement text is tens of megabytes.
- **The capture carries no application data.** No SQL text, no parameter values,
  no login or host names pass through it. On an instance where a DPO had to be
  consulted before `collect` ran, that is the difference between a yes and a no.

That property belongs to the capture and not to the archive, and an earlier
draft overstated it. The deliverable carries resolved statement text, and text
resolved from the plan cache can embed literal values the application wrote into
the SQL. Query Store text is parameterised and does not. The manifest says so,
the consent prompt says so, and the per-row source label lets a reader tell
which is which.

### `--detail`

Keeps the whole event rather than a bucket, for the cases where the hash is not
enough: a statement that has left the plan cache and was never in the Query
Store, or a question that needs the duration and the row count of each call
rather than how many calls there were. It is off by default and says so in the
manifest.

Two possible targets, and the choice between them is a real one.

A **ring buffer** keeps events in memory and returns them as XML from
`sys.dm_xe_session_targets.target_data`. Nothing is written to disk, no
directory has to exist, no permission is needed beyond the ones `observe`
already requires, and Go reads it with the standard library. For a short capture
that is the better target, and it is the default for `--detail`.

Its limits decide when it stops being so, and **the target's own options are not
the session's**: a ring buffer has its own `max_memory` and its own
`max_events_limit`, separate from the session-level `MAX_MEMORY`, and both must
be set explicitly. Inheriting version-dependent defaults is how two audits of
two builds come back incomparable. It discards the oldest events when full, so a
window that outruns the buffer returns its tail while looking complete. And
`target_data` is known to truncate large payloads, so the XML can come back
incomplete even when memory held everything.

An **event file** has neither limit and is the target for a long capture. It
costs a directory the SQL Server service account can write to, taken as
`--detail --file <dir>` rather than guessed, and a file to clean up.

The rule between them: ring buffer up to a window and a rate the buffer can
hold, event file beyond it. `observe` measures the batch rate first, computes
what the window will produce, and says which target it is about to use and why.

### Session options

`MAX_MEMORY` set explicitly. `EVENT_RETENTION_MODE = ALLOW_SINGLE_EVENT_LOSS`:
on a busy instance, dropping an event is preferable to stalling the workload,
and the drop count is reported so the reader knows. `STARTUP_STATE = OFF`, so a
service restart does not resurrect the session.

`MAX_DISPATCH_LATENCY` deserves a line of its own, because it will otherwise
produce the first support question. It defaults to 30 seconds: events sit in
their buffer until that latency elapses or the buffer fills, so `observe status`
can report zero events for the first half-minute of a capture that is working
perfectly. Stopping the session flushes the buffers, which is why `finish`
always sees them and a mid-flight read may not. Either set it low and say so, or
say so in the output of `status`.

The session is filtered to the database under study, on `sqlserver.database_id`,
resolved from the name when the session is created. That attributes a statement
to the database the **session** is in, not to the database whose objects it
touches: a three-part-name query issued from another context is not counted. For
counting one application's calls during one operation that is the right filter,
and it is stated here because a reader comparing these numbers with the Query
Store's will otherwise find a discrepancy with no explanation.

The session also excludes `observe`'s own session id, which means reading
`@@SPID` before the `CREATE`, so the id appears in the DDL the consent prompt
shows.

## Getting the result back

The constraint that decides this is not on the server side.

**There is no Extended Events client library for Go.** Reading a session as it
runs — the live data stream that Management Studio shows — goes through
`Microsoft.SqlServer.XEvent.Linq`, which is .NET only. There is no port, and
writing one means implementing an undocumented binary protocol against a moving
target. Any design assuming a live feed is a design for a different language.

That constraint picks the right answer rather than obstructing it. Everything is
read **through the same T-SQL connection as the rest of the tool**, after the
fact. Which means the session must accumulate into something a `SELECT` can
read, and that decides the target before anything else does.

Two consequences worth stating, because they will look like omissions otherwise.
There is no live view: `status` reports counts, not a stream. And there is no
per-event callback, so filtering happens in the session definition where it
belongs rather than in the client.

What the missing library does **not** rule out is reading a target. Both
in-memory targets hand back XML over a normal query, and `encoding/xml` parses
it. The absence of a client library costs the stream, not the data.

**The histogram needs no file and no library.** Its contents are in
`sys.dm_xe_session_targets.target_data`, read over the same connection as
everything else and parsed with the standard library.

**The ring buffer needs no file either.** Same DMV, same XML, same parser; only
the shape of the document differs, one element per event instead of one per
bucket. That is what makes `--detail` usable where nobody will grant a
directory.

**The event file comes back the way the blocked process reports already do**:
`sys.fn_xe_file_target_read_file` reads it server-side and returns rows. It is
also the only way to read a `.xel` from Go, the format being undocumented and
the parsing happening inside SQL Server.

The rollover-file pattern deserves a warning, since this command would inherit
it. SQL Server appends `_0_<ticks>.xel` to the **stem** of the configured name,
so a session configured as `D:\watch\observe.xel` writes
`D:\watch\observe_0_133000000000000000.xel`. Building the pattern by appending a
wildcard to the configured name, extension included, matches nothing and returns
an empty capture with no error. That mistake shipped in
`queries/10.system/063.blocked-process-reports.sql`, where the extension test
searched the reversed name for the extension spelled forwards and so never
matched. It was found when a collection reported an empty capture on an instance
whose ring buffer held two reports and whose files, once read correctly, held
205.

### Subtracting, and what invalidates it

A histogram counts from the moment the session started, not from the moment
`observe` attached to it. When a session is borrowed it may have been running
for a week. So `start` snapshots and `finish` subtracts, exactly as the audit
reads two collections and subtracts.

**A session restarted between the two reads breaks that arithmetic silently.** A
DBA, a monitoring tool or a failover stops and starts it; the histogram resets;
the subtraction yields negative or nonsensical per-bucket deltas that still look
like data. `finish` records `sys.dm_xe_sessions.create_time` at both ends and
refuses if it moved, and refuses on any negative bucket delta as a backstop. The
audit's version of this discipline compares two readings taken by the same tool;
here the tool is subtracting two reads of something somebody else owns.

### The state file

`start` writes it next to the archive directory, where the operator already
looks. `finish` deletes it on success.

It binds **server, database and session name**, not the name alone. `finish`
typed against the wrong connection string would otherwise find the fixed name on
a different instance and subtract a stranger's snapshot, or drop a stranger's
session.

If it is lost — `finish` run from another machine, a working folder cleaned —
the baseline is gone and subtraction is impossible. `observe` refuses, which is
the answer consistent with the rest of this design. `--no-baseline` reports the
raw counters with a manifest note saying they are cumulative since the session
started and not scoped to any window.

### Measuring the cost before paying it

`observe` reports the expected event rate before creating anything. **Batch
Requests/sec in `sys.dm_os_performance_counters` is a cumulative count**, not a
rate: one read gives the average since the last service restart, which on an
instance up for months is far below the rate during the window. Two reads a few
seconds apart, subtracted.

This is the same trap the corpus documents elsewhere, and getting it wrong here
would contradict the discipline the section above is built on.

## The decoding layer

**`collect/xevents`.** One job: turn a `target_data` document into typed Go
values. Two shapes to decode, matching the two in-memory targets.

- A **histogram** is a list of buckets, each a slot value and a count, plus the
  target's own attributes: how many slots it holds, whether it overflowed, and
  how many events went into the overflow rather than into a bucket.
- A **ring buffer** is a list of events, each with a name, a timestamp, its data
  fields and its actions, plus **both** of its loss counters. `droppedCount` and
  `droppedBuffers` mean different things — events lost singly, versus whole
  buffers discarded — and a capture that lost three events is not a capture that
  lost forty buffers. Read both, report both.

It does not connect to SQL Server. It takes a string of XML and returns values.
That separation is the point: the SQL lives in the corpus where every other
query lives, the transport lives in the collector, and the parsing is a pure
function with no I/O, which is what makes it testable at all.

**It is tested against captures taken from real sessions**, committed as
fixtures rather than hand-written. A document invented to match the parser
proves the parser matches the invention. The fixtures must be scrubbed before
they are committed: a ring buffer capture carries statement text, and the rule
in `CLAUDE.md` applies to test data exactly as it applies to code.

Three behaviours the package owes its caller, all three learned from mistakes
already made in this repository.

**It reports what it could not read rather than returning less.** `target_data`
truncates large payloads, and truncated XML either fails to parse or — worse —
parses into a shorter list that looks complete. The parser distinguishes "this
document ended cleanly" from "this document was cut", and the caller puts the
difference in the manifest. Silently returning half a capture is the same class
of defect as the `.xel` pattern bug: an answer that looks like an answer.

**It carries the counters out with the data.** Drops, overflow, truncation: they
belong to the target, not to the events, so a caller that only asks for the
payload never sees them. The signature makes that impossible — counters and
payload return together.

**It does not guess at types it does not know.** An event field whose type the
package has no mapping for comes back as its string form, labelled as
unconverted. Guessing produces a number wrong in a way nothing downstream can
detect.

Decoding is **eager**, with the truncation check run before any of it. A lazy
design keeps the XML alive for marginal benefit and complicates the truncation
guarantee the parser contract depends on.

**Why it lives here and not in its own repository, for now.** It has one
consumer. An interface extracted for a single caller is shaped by that caller's
accidents and freezes before anyone has learned what the second needs. When a
second appears, extraction is a rename.

**Its explicit non-goal is the `.xel` binary format.** Nothing in this package
opens a file. Reading a `.xel` without SQL Server means reverse-engineering an
undocumented format, which is a project with its own scope, risks and audience —
an auditor handed a client's capture files and no instance to read them on. It
should not be smuggled in as a package of this one.

The name sits awkwardly beside `collect/observer.go`, which defines the
`Observer` interface watching a run. Two unrelated things spelled almost the
same, in one package tree. Worth renaming one of them now rather than at review.

## Output

```
observe-<server>-<date>-<time>.zip
  MANIFEST.txt
  _run.json
  statements.json     one row per query_hash: executions, resolved text,
                      the source the text came from
  unresolved.json     hashes the server could no longer name, with counts
  events.json         only with --detail: one row per captured event, with
                      the target it was read from and both loss counters
```

`statements.json` is the deliverable. Sorted by execution count, it answers "how
many calls did that operation make", and the second column is the statement
making them.

It carries **no first-seen or last-seen timestamps**. An earlier draft promised
them, and the default capture cannot supply them: a histogram is one integer per
slot and records no time at all. `sys.dm_exec_query_stats` has `creation_time`
and `last_execution_time`, but those are plan-cache lifetimes rather than the
observation window, and attaching them to a window-scoped count invites exactly
the misreading this tool exists to prevent. With `--detail` the events carry
their own timestamps, and `events.json` has them.

### Resolution, and its two ambiguities

Resolution runs at `finish`, after the session has stopped, so the plan cache is
as warm as it will ever be and the loss is bounded by what was evicted during
the window itself.

**Query Store first, in the context of the target database** — its views are
per-database and the query has to run there — then the plan cache. The order
matters for more than freshness: `sys.dm_exec_query_stats` is instance-wide, so
a `query_hash` shared with a statement from another database, which is common
across copies of one schema, resolves to the wrong text or to an arbitrary one
of several. Query Store text is also parameterised, where plan-cache text may
carry literals.

Each row is labelled with the source its text came from. The manifest says when
the Query Store was off for the database, because resolution then had only the
plan cache and a large unresolved count means something different than it
otherwise would; and it says that plan-cache resolution is ambiguous across
databases.

**The hash conversion has to be written down before implementation.** The
histogram returns the slot value as a decimal, while
`sys.query_store_query.query_hash` and `sys.dm_exec_query_stats.query_hash` are
`binary(8)`. The conversion is signed-versus-unsigned and byte order both. Get
it wrong and resolution matches nothing and reports every hash as unresolved,
which looks exactly like a cold plan cache and will be diagnosed as one.

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | the capture completed and the archive was written |
| 1 | the instance could not be reached, or the archive could not be written |
| 2 | partial: the capture ran but something is missing — overflow, truncation, dropped events, unresolved hashes above a stated share |
| 3 | refused before touching the instance: missing permission, a session already running under the name, a borrowed session that does not qualify, a lost state file |

They are the interface a script uses, so they are listed rather than promised.

## Consent and safety

`observe` prints the exact DDL it is about to run, and the permissions it
requires, and waits for confirmation unless `--yes` is passed. The prompt names
the session, the events, the actions, the target and its options, the database
filter and the duration. An operator should be able to paste that DDL into a
ticket.

Guarantees the implementation owes:

- **Nothing survives a service restart, and an orphan is findable.**
  `STARTUP_STATE = OFF` means restarting SQL Server does not bring the session
  back. What it does **not** guarantee is that nothing survives a crash of the
  tool: there is no server-side timer, so an orphan runs until the next
  `observe` stops it or a human does. Stating this as a guarantee, which an
  earlier draft did, would have been the comfortable sentence and the false one.
- **A borrowed session is never dropped, never altered, never started.** Not
  even to add an event it is missing; that is a refusal with a message listing
  what was expected, not a repair.
- **The tool refuses rather than degrades**, and preflight is what makes that
  true rather than aspirational.
- **The cost is measured before it is paid**, from two reads of a cumulative
  counter rather than one.

## What it does not do

It does not tie statements to a business operation on its own. It counts what
happened between two moments; the operator supplies the meaning by choosing
those moments. The report should say "during one *save order* performed at
14:32, the application issued 4,812 statements", and the tool provides the
4,812, not the *save order*.

It does not see inside a batch. `rpc_completed` and `sql_batch_completed` fire
once per call from the client, so a loop written in T-SQL — a `WHILE`, a cursor,
a procedure iterating a set — arrives as one event however many statements it
runs. That is consistent with what the command is for, and it is worth stating
because a reader coming from `queries/80.workload/021.query-store-detail.sql`
will have seen per-statement figures and expect them here. Capturing
`sql_statement_completed` and `sp_statement_completed` would see inside the
batch and multiply the event volume by whatever the batch does, which is the
trade this command declines by default.

It does not replace the Query Store analysis. `023` and `024` find the loops
across a month without touching the server. `observe` attributes one of them to
an operation, which is a smaller and later question.

It captures no execution plans and no wait information. Both exist elsewhere in
the tool, and adding them here would double the cost of the capture for facts
already available.

## Open questions, and the ones now closed

**Closed: snapshot `sys.dm_exec_query_stats` at `finish`?** Yes, as its own
section of the archive rather than a widening of the headline number. It is the
same subtract-two-reads discipline, it is cheap, and rows plus duration per
statement is what turns "4,812 calls" into a vendor conversation. The manifest
records that plan-cache deltas are instance-wide.

**Closed: eager or lazy decoding?** Eager, truncation check first.

**Closed: where does the state file live?** Next to the archive. The config
directory invites cross-server collisions, and "survives someone cleaning a
working folder" is an argument for the refusal path rather than for hiding the
file.

**Closed: fractional minutes?** No. An operation shorter than a minute is better
measured with `start` and `finish`, and admitting `0.5` invites `0.01`.

**Open, and to be measured before the first implementation**, in order:

1. The histogram XML shape on the oldest supported build: the slot value format,
   the overflow attributes, the dropped count. The scrubbed captures become the
   first `collect/xevents` fixtures.
2. The `target_data` truncation ceiling for a ring buffer, and the session
   `MAX_MEMORY` and target `max_memory` needed for five minutes at a few hundred
   events a second. Record the build each figure was measured on, and choose
   defaults against the oldest supported version rather than the newest to hand.
3. The permission set per mode on a locked-down instance, which doubles as the
   `--session` validation test cases.
4. Whether `query_hash` from the two events joins cleanly to
   `sys.query_store_query.query_hash` for both RPC and batch workloads. The
   whole resolution step rests on it.

None of these changes the design. All of them are details this document would
otherwise state with more confidence than it has earned.
