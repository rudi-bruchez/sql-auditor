# Adversarial review of `docs/observe-spec.md` (commit a6692b8)

Method: the session the document describes was built verbatim and run on the lab
instance (SQL Server 2025 RTM-CU7, 17.0.4065.4, container `sql2025`), with
traffic driven both by `sqlcmd` (batches) and by a Go program using
`github.com/microsoft/go-mssqldb` v1.9.3 (RPCs), which is the same driver family
the repository uses. Where the document omits a parameter I picked one and said
so rather than guessing what it meant. Everything created carries a `ZzObserve`
prefix and was dropped; the inventory is at the end.

Each finding says whether it was reached by running or by reading.

---

## 1. Blocking

### 1.1 The `@@SPID` exclusion silently empties the capture (measured)

What the document says, under "The session":

> The session also excludes `observe`'s own session id, which means reading
> `@@SPID` before the `CREATE`, so the id appears in the DDL the consent prompt
> shows.

What I did. I built the session exactly as specified, with the two events, the
two `SET` options, the two actions, an `event_file` target, and the predicate
`(sqlserver.database_id = 10 AND sqlserver.session_id <> @@SPID)`, the SPID
being read in the batch that issued the `CREATE`. The creating connection was
SPID 63, so the DDL that ran carried `sqlserver.session_id <> 63`. That
connection then closed, as `observe start` would close it.

I then drove the workload: three `EXEC dbo.ZzObserveP @v=...` RPCs and a
parameterised `SELECT` from the Go driver, plus two `sqlcmd` batches against the
observed database.

What happened. The new connections were assigned SPID 63, the id the dead
creating connection had just freed. The capture file was created, the session
reported `Buffers logged="0"`, and:

```
events_in_file
--------------
0
```

Same session, same DDL, second run a minute later when the driver happened to
get SPID 73 instead:

```
events_in_file
--------------
11
```

A control session identical except for having no `session_id` clause captured 11
events in the run that the specified session captured 0, which isolates the
cause to the exclusion rather than to the database predicate.

Why this matters more than it looks. This is precisely the failure shape the
document says the new design is immune to: "the session builds, starts, counts,
and returns a well-formed document; the exit code is 0 [...] Only the deliverable
is empty." SPID reuse is not exotic. It is what a server does with a freed slot,
and on an instance with few connections the id the tool just released is among
the most likely ids the next connection gets. The `start` / `finish` mode makes
it worse, because `start` deliberately ends its process and frees its SPID before
the workload runs.

The exclusion is also ineffective at the job it was added for. `finish`, `status`
and `stop` run in other processes with other SPIDs, so their own statements are
not excluded by it; only the creating process is.

What it should say instead. Drop the `session_id` clause. The database predicate
already keeps `observe`'s own traffic out as long as the tool connects somewhere
other than the observed database, and the document should state which database
`observe` connects to, because that is the thing doing the work. If self-exclusion
is still wanted, it has to be on something stable for the life of the session, not
on a reusable integer: `sqlserver.client_app_name <> 'sql-auditor'` is server-
verifiable, survives reconnection, and covers `finish` and `status` too, which
`@@SPID` never could.

### 1.2 The ownership fingerprint cannot be evaluated by the invocation that needs it (measured, then read)

What the document says, under "A fixed name is not proof of ownership":

> So the managed session has a server-verifiable fingerprint: its events, its
> actions, its predicate, its target and its target options [...] The sweep acts
> only on a session that matches the fingerprint exactly.

and, decisively:

> Ownership is a property of the session as the server describes it, not of the
> state file, because the state file is the thing most likely to be missing when
> the sweep matters.

What I did. I created the session and read its definition back from
`sys.server_event_session_events`.

What happened:

```
sess            | ev                  | predicate_text
ZzObserveQnSet  | rpc_completed       | ([sqlserver].[database_id]=(10))
ZzObserveQnSet  | sql_batch_completed | ([sqlserver].[database_id]=(10))
```

With the specified `session_id` clause present the predicate reads
`([sqlserver].[database_id]=(10) AND [sqlserver].[session_id]<>(63))`.

The predicate therefore carries two literals fixed at creation time: the database
id of the database under study, and the SPID of the process that created it. The
target's `filename` likewise carries the directory the first operator chose.

Now take the case the section is written for: a laptop died, a different operator
on a different machine runs `observe status`. That process knows the session name
and nothing else. It cannot know `10`, it cannot know `63`, and it cannot know the
directory. So it cannot decide whether the predicate it reads is the one the tool
would have written. "Matches the fingerprint exactly" is undefined for exactly the
invocation the fingerprint exists to serve, and the document's own sentence ruling
out the state file removes the only place those three values could have come from.

This is not a detail an implementer resolves by being careful. They must choose,
silently, between three incompatible readings: match the predicate's shape and
ignore its literals (then a DBA's hand-made session filtered on a different
database matches, which the section forbids), match the literals (then the sweep
never fires without the state file, which the section forbids), or read the
database name back out of the predicate and re-resolve it (which fails when the
literal is a SPID). Whichever they pick will be defended as "what the spec meant".

What it should say instead. Say which components are matched as literals and which
as shape, and give the sweep something server-side to compare against. The natural
carrier already exists in the design and is currently wasted: the target
`filename`. A stem the tool alone would write, for example
`sql-auditor-observe-<utc timestamp>-<max minutes>.xel`, is readable from
`sys.server_event_session_fields` and from the running target's `target_data`,
is unforgeable enough for this purpose, is predictable in shape without any local
state, and carries the run's own deadline (see 2.5) and its own unique stem (see
1.3). One decision fixes three findings.

### 1.3 Two runs in the same directory report one count for both (measured)

What the document says. The session name is fixed. The directory is the
operator's choice, proposed from `SERVERPROPERTY('ErrorLogFileName')`. The
filename itself is never specified anywhere in the document, and the event count
is obtained with `SELECT COUNT(*) FROM sys.fn_xe_file_target_read_file(...)`,
which needs a wildcard because of the rollover suffix the document itself warns
about.

What I did. Following the document's fixed-name discipline, I configured
`filename = '/var/opt/mssql/log/ZzObserveQnSet.xel'`, ran a capture, stopped and
dropped the session, then created the session again at the same configured path
and ran a second capture, then counted with the stem wildcard the rollover trap
forces.

What happened:

```
run 1, after stop                     : 11 events, 1 file
run 2, same configured filename       : 20 events, 2 files
```

```
-rw-rw----. 1 mssql mssql 24576 ... ZzObserveQnSet_0_134343871931030000.xel
-rw-rw----. 1 mssql mssql 24576 ... ZzObserveQnSet_0_134343872681410000.xel
```

SQL Server does not overwrite: it starts a new `_0_<ticks>.xel`. The second run's
archive therefore reports 20 events for a capture that saw 9, and would go on
accumulating every previous audit taken at that path. The same wildcard is what
the document's rollover-loss check relies on ("the tool records the number of
files present at the end beside the number it expected"), so that check also
counts the previous run's files and will report a phantom rollover.

This is the number the whole command exists to produce. The document's own
example of the deliverable is "during one save order performed at 14:32, the
application issued 4,812 statements". On the second audit of the same instance
that number is the sum of two audits, with nothing on screen to say so.

What it should say instead. Specify a per-run unique filename stem and say that
the count and the file inventory are scoped to it. The timestamped stem proposed
in 1.2 does it. Alternatively, record the file set from the running target's
`target_data` before the stop and count only those files, but the unique stem is
the smaller change and fixes the fingerprint at the same time.

---

## 2. Serious

### 2.1 "Off by default" is false; the warning built on it is false (measured)

What the document says, under "The session":

> Both collect their text, which is off by default in SQL Server and has to be
> asked for: `SET collect_statement = 1` on `rpc_completed` and
> `SET collect_batch_text = 1` on `sql_batch_completed`. An implementer who omits
> them builds a session that works, fires, and records every column except the one
> the capture exists for.

What I did. Two things. First, read the defaults:

```sql
SELECT o.name, c.name, c.column_type, CAST(c.column_value AS varchar(40))
FROM sys.dm_xe_objects o
JOIN sys.dm_xe_object_columns c ON c.object_name=o.name AND c.object_package_guid=o.package_guid
WHERE o.name IN ('rpc_completed','sql_batch_completed') AND c.column_type='customizable';
```

```
rpc_completed       | collect_statement  | customizable | true
rpc_completed       | collect_data_stream| customizable | false
rpc_completed       | collect_output_parameters | customizable | false
sql_batch_completed | collect_batch_text | customizable | true
```

Second, built two sessions side by side, identical but for the `SET` clauses, and
drove the same workload through both. Both captured 11 events. The session with
no `SET` clause at all:

```
ev                  | db_act      | app_act      | rc | txt
rpc_completed       | ZzObserveDB | ZzObserveApp | 2  | exec dbo.ZzObserveP @v=N'row0'
sql_batch_completed | ZzObserveDB | SQLCMD       | 2  | SELECT @@SPID AS driving_spid; INSERT dbo.ZzObserveT...
```

The text is there. Both options default to on, on this build.

This contradicts a statement the document presents as a fact about the product,
in the one section where it is teaching the implementer what not to get wrong.
Setting them explicitly is still right, for the same reason the target options are
set explicitly, and that reason is already written two sections later ("Inheriting
version-dependent defaults is how two audits of two builds come back
incomparable"). The justification in the text is what is wrong.

What it should say instead. "Both carry their text by default on the builds
measured; set them explicitly anyway, because an inherited default is a
version-dependent default and this is the column the capture exists for." Do not
keep the sentence about the implementer who omits them, because it describes a
failure that does not happen and will be quoted back as measured fact.

### 2.2 Exit code 3 cannot cover a bad path (measured)

What the document says. The exit code table: "3 | refused before touching the
instance: missing permission, a session already running under the name, a
same-named session that is not ours, a bad path". And, under "Ordering": "Exit
code 3 means refused before touching the instance."

What I did. Created the session with a target directory the service account
cannot write to, then started it.

What happened:

```
CREATE succeeded
Msg 25602, Level 17, State 1
The target, "...package0.event_file", encountered a configuration error during
initialization. Object cannot be added to the event session. The operating system
returned error 5: 'Access is denied.' while creating the file
'/root/ZzObserveBadPath_0_134343873226820000.xel'.
```

and afterwards:

```
where_  | name
catalog | ZzObserveBadPath
```

`CREATE EVENT SESSION` validates no path. The path is only tested at `START`, by
which point the instance has been altered and a session exists in the catalog. So
either the tool exits 3 having created an object, which is the exact dishonesty
the "Ordering" section was written to prevent, or a bad path is not a code 3.

The document's own compensating-action rule covers the residue ("Creation is
followed by a best-effort drop on any failure before durable state exists"), so
the defect is confined to the exit code contract, which is listed "rather than
promised" because scripts consume it.

What it should say instead. Move "a bad path" out of code 3, and say which code a
failed `START` on a valid-looking path produces (it is neither 0 nor 2 as they are
currently worded). If a pre-create path check is wanted, say what it is; there is
no cheap one, since the question is whether the service account can write there,
which only the write answers.

### 2.3 The fingerprint is said to be read "the way 062 already reads them", and 062 reads neither actions nor the predicate (read)

What the document says:

> read back from `sys.server_event_sessions` and its companions the way
> `queries/10.system/062.xe-sessions.sql` already reads them.

What I did. Read `queries/10.system/062.xe-sessions.sql`.

What happened. 062 projects three result sets: the session list from
`sys.server_event_sessions`, one row per event from
`sys.server_event_session_events`, and targets with their fields. It never joins
`sys.server_event_session_actions`, so it reads no actions. And it deliberately
excludes the predicate, in its own words:

> The predicate text is deliberately left out: a filter can carry a literal, and
> it is the one part of a session definition that can.

Two of the five fingerprint components are not read by the file cited as reading
them, and one of them is excluded there on purpose for a reason that bears
directly on `observe`: the predicate carries literals, which is the same fact that
makes 1.2 a problem. An implementer following the pointer will write the query,
find two components missing, and either invent them or conclude the fingerprint is
smaller than specified.

I did verify the query is writable and that the rights suffice: with only
`ALTER ANY EVENT SESSION` and `VIEW SERVER STATE`, and an explicit
`DENY VIEW ANY DEFINITION`, a login read `sys.server_event_sessions` (9 rows),
`..._events` (160), `..._fields` (32) and `..._actions` (49). The pointer is
wrong, not the plan.

What it should say instead. Say that the fingerprint needs a read 062 does not
perform, name `sys.server_event_session_actions` explicitly, and acknowledge that
062 excludes the predicate on purpose, so `observe` is choosing to read something
the corpus refuses to collect, and why that is safe here (it reads its own
predicate, and does not put it in an archive).

### 2.4 Preflight cannot be done "the way `collect/preflight.go` already does" (read, then measured)

What the document says:

> Preflight checks them before the session is created, the way
> `collect/preflight.go` already does for the collector.

What I did. Read `collect/preflight.go`. Its discipline is stated at the top of
`Capabilities()` and it is the opposite of what `observe` needs:

> HAS_PERMS_BY_NAME: a permission can arrive through role membership, be denied at
> a lower scope, or be shadowed by a database setting, and every such case produces
> a preflight that disagrees with what the run actually does. A SELECT cannot
> disagree with reality.
>
> Each probe reads one row from the cheapest object that the real collectors
> depend on, so the probe fails in exactly the cases they would.

`VIEW SERVER STATE` has such an object; `ALTER ANY EVENT SESSION` has none. The
only probe that "fails in exactly the cases the real thing would" is a
`CREATE EVENT SESSION`, which must not happen before consent. So the reuse named
in the document is not available, and the file it points at argues by name against
the only mechanism that is.

I measured the mechanism anyway, because if it disagreed with reality the finding
would be larger. It does not:

```
login without the grant: HAS_PERMS_BY_NAME(NULL,NULL,'ALTER ANY EVENT SESSION') = 0
                         CREATE EVENT SESSION -> Msg 15247, User does not have
                         permission to perform this action.
login with the grant   : HAS_PERMS_BY_NAME(...) = 1, CREATE succeeds.
```

What it should say instead. Say that `ALTER ANY EVENT SESSION` is probed with
`HAS_PERMS_BY_NAME(NULL, NULL, 'ALTER ANY EVENT SESSION')`, that this is a
departure from the probe-by-SELECT rule `preflight.go` states, and why the
departure is forced. Leaving the reader to discover the conflict is how the rule
in `preflight.go` gets quietly eroded by the second command that needed an
exception.

While measuring this I did confirm the document's permission table in full, with a
login holding only those two server rights and no database mapping and no role:
`SERVERPROPERTY('ErrorLogFileName')` returned `/var/opt/mssql/log/errorlog`,
`DB_ID('ZzObserveDB')` resolved, `CREATE` / `ALTER ... STATE=START` worked, the
session counters were readable from `sys.dm_xe_sessions`, and
`sys.fn_xe_file_target_read_file` returned a count. The table is right.

### 2.5 The enforced deadline is the sweeper's flag, not the run's intent (read)

What the document says. Three statements that do not compose:

> `--max-minutes` caps every mode, not only `start`, and defaults to 60.

> the deadline is derived from the session's own start time,
> `sys.dm_xe_sessions.create_time`, plus `--max-minutes`.

> [of `start --for`] The deadline is declared and stored, `status` shows the time
> left or the overrun, any later invocation of `observe` stops a session past its
> deadline.

There are two deadlines, the stored `--for` one and the derived one, and "past its
deadline" is not told which it means. Worse, the derived one is computed from a
flag belonging to the process doing the sweeping, not to the process that created
the session. Two consequences, both against the design's own stated goal of
shrinking the orphan window:

- a `--minutes 5` run whose laptop dies at minute one leaves a session that no
  later `observe` will stop until minute 60, because 5 was never recorded
  anywhere the server can see. The document says "`observe` reduces the window; it
  does not close it", which a reader will take to mean reduced to the requested
  window. It is reduced to twelve times it.
- an operator who runs `observe status` with the default `--max-minutes` while a
  colleague's `--max-minutes 120` capture is at minute 70 stops and drops a live
  capture and abandons its file. The document forbids exactly this outcome one
  section earlier ("with a file target it also abandons a file somebody was
  filling"), for the case of a hand-made session, but the sweep as specified
  produces it for its own.

I verified the one server-side fact the mechanism rests on, and it holds:
`sys.dm_xe_sessions.create_time` is the time of `STATE = START`, not of the
`CREATE` (`CREATE` at 14:18:34, `START` at 14:18:39, `create_time` 14:18:39.017),
so deriving from it measures the capture and not the definition.

What it should say instead. Put the run's own deadline somewhere the server holds
it, so any later `observe` reads the intent rather than substituting its own. The
timestamped filename stem from 1.2 carries it at no extra cost. Failing that, state
plainly that the enforced maximum is the sweeper's `--max-minutes` and that a
capture can therefore be cut short by an unrelated invocation, and say which of the
two deadlines `status` displays.

### 2.6 Connection-pool housekeeping is in the capture and in the count (measured)

What the document says. The headline deliverable is a count of calls: "the report
should say 'during one save order performed at 14:32, the application issued 4,812
statements', and the tool provides the 4,812". The manifest draft says the two
events "fire once per call made by a client".

What I did. Read back the events captured from an ordinary Go client doing one
parameterised query and three procedure calls.

What happened, verbatim from the capture:

```
rpc_completed | ZzObserveDB | ZzObserveApp | 1 | sp_executesql        | exec sp_executesql N'SELECT COUNT(*)...'
rpc_completed | ZzObserveDB | ZzObserveApp | 0 | sp_reset_connection  | exec sp_reset_connection
rpc_completed | ZzObserveDB | ZzObserveApp | 0 | sp_reset_connection  | exec sp_reset_connection
rpc_completed | ZzObserveDB | ZzObserveApp | 0 | sp_reset_connection  | exec sp_reset_connection
rpc_completed | ZzObserveDB | ZzObserveApp | 2 | ZzObserveP           | exec dbo.ZzObserveP @v=N'row0'
...
```

Four of eleven events were `sp_reset_connection`, which is the pool handing a
connection to the next user and not a call the application made. On a chatty
application with a pool the proportion is not small, and it scales with the
connection churn rather than with the work.

The number in the archive is the one that goes to the application vendor, and the
document is emphatic that it is the whole argument. It should not silently include
the driver clearing its own connection.

What it should say instead. Either exclude it in the predicate
(`sqlserver.object_name <> 'sp_reset_connection'` is available on `rpc_completed`
and I saw it populated), or report two counts, events captured and calls
attributable to the application, and say which one the report should quote.
Whichever is chosen, the manifest's "fire once per call made by a client" needs
the qualification, because a reader checking the number against their own
application log will find the discrepancy and lose confidence in the rest.

### 2.7 The rollover-naming lesson is pointed at a fix that is Windows-only (measured)

What the document says:

> That shipped in `queries/10.system/063.blocked-process-reports.sql`, where the
> extension test searched the reversed name for the extension spelled forwards and
> so never matched.

and the example it gives of the right shape uses a Linux path:
`/var/opt/mssql/log/observe.xel`.

What I did. The 063 story is true and its current code does fix the extension
test. But it builds the directory prefix with `CHARINDEX('\', REVERSE(...))`,
splitting on the Windows separator only. I ran 063's exact expression against the
Linux paths this instance actually produces.

What happened:

```
path_063_builds_on_linux
/var/opt/mssql/log/ZzObserveQnSet_0_134343872681410000.xel/var/opt/mssql/log/ZzObserveQnSet*.xel

Msg 25718, Level 16, State 3
The log file name "...xel/var/opt/mssql/log/ZzObserveQnSet*.xel" is invalid.
```

With no `\` found, both the stem extraction and the directory extraction degrade,
the path is concatenated onto itself, and nothing matches. An implementer told to
inherit the 063 fix and writing for the instance in the document's own example
reproduces the bug the section warns about, one separator further along.

I also measured the two failure modes of a wrong pattern, because they are not the
same and the document treats them as one. A wildcard in a directory that exists,
matching no file, returns zero rows and no error:

```
SELECT COUNT(*) FROM sys.fn_xe_file_target_read_file('/var/opt/mssql/log/ZzObserveNothingHere*.xel',NULL,NULL,NULL)
--> 0
```

The naive pattern the document warns about, `<name>.xel*.xel`, likewise returns 0
and no error. Only a nonexistent directory raises `Msg 25718`. So "empty capture
with no error" is exactly right, and it is the common case, not the exotic one.

What it should say instead. Say that the separator is `\` or `/` depending on the
platform, and that 063 currently handles only the first. Better, do not build the
pattern at all: the running target's `target_data` gives the real file name
directly, and 062's own comment says so ("it is projected because it is the only
place the real path appears"). Reading the file set from `target_data` before the
stop removes the string surgery, removes the platform question, and pairs with 1.3
by naming the files of this run and no other.

---

## 3. Smaller

### 3.1 Every numeric option the document insists be explicit is left unstated

"`MAX_MEMORY` set explicitly", "`max_file_size` and `max_rollover_files` all set
explicitly", `MAX_DISPATCH_LATENCY` "set low". No value is given for any of them,
and the reason given for setting them explicitly is that inherited defaults make
two audits incomparable. Two implementers reading this produce two incomparable
audits from the same document. I used 4096 KB, 10 MB, 5 files and 1 second, and
those choices are mine, not the document's. Give the numbers, or say they are the
implementer's and that `_run.json` records them.

### 3.2 `--file <dir>` takes a directory

"requires it to be confirmed or replaced with `--file <dir>`". The flag is named
for a file and documented as taking a directory. `--directory` or `--capture-dir`
costs nothing now and a deprecation cycle later. This is also the only place the
document comes near specifying the filename, which is the gap behind 1.3.

### 3.3 The state file has no stated location, and no stated scope

`start` writes it, `finish` and `status` read it, and the document never says
where it lives. It matters more than a path usually does, because the document
argues at length about what happens when it is missing, and because `finish` run
from a second machine will always be missing it. Say where it is, and say what
`finish` does when there is none but a matching session is running.

### 3.4 `status` claims a number the design reads only after the stop

"`observe status` says whether a session is running, since when, how many events
it has recorded". The event count is specified elsewhere as read from the file
after the stop. While running, the file is missing whatever is still in buffers
(`MAX_DISPATCH_LATENCY`), so `status` either reports a number that is quietly
short or reports `total_bytes_generated` under a different name. Say which.

### 3.5 The version floor and the column table

The capability table lists `page_server_reads` for both events and `spills` for
`sql_batch_completed`. Both are present on 2025, which I confirmed from
`sys.dm_xe_object_columns`; both are later additions than the stated 2012 floor.
The table reads as a contract about what the capture carries, and on the floor it
carries less. Open question 1 covers the DDL on 2012 but not this table. One
sentence saying the table describes 2016 and later, or naming the columns that
drop off, would close it.

### 3.6 The manifest overclaims under `ALLOW_SINGLE_EVENT_LOSS`

The manifest draft says the events "fire once per call made by a client, and for
each call it recorded the text, the duration, the CPU time, the reads, the writes
and the number of rows". The session is configured to drop events under pressure,
and the archive reports the drop count. "For each call" and "dropped 4,000 events"
in the same archive is a contradiction a client will find. Qualify the manifest
sentence rather than relying on the counter elsewhere to correct it.

### 3.7 `collect/cancel.go` is 65 lines, not 63

The argument built on it is correct and I checked it: those lines classify a dead
context as an operator cancellation, handle no signal, write no archive and touch
no server. Only the count is off. Cite the file, not the length, since the length
will drift.

---

## Things I tested that turned out to support the document

Stated because a review that reports only misses is not telling you where the
budget went, and because one of these was a hypothesis I expected to confirm and
had to drop.

- The `sqlserver.database_id` predicate does evaluate correctly on
  `rpc_completed` and `sql_batch_completed`. This was the first thing I tested,
  since the design's predecessor died of an action that read zero at completion
  time and a predicate source is evaluated at the same point. It works: traffic in
  the observed database was captured, a batch run in `master` on the same
  connection was not. The `sqlserver.database_name` and `sqlserver.client_app_name`
  actions are likewise populated, showing `ZzObserveDB` and the real application
  name.
- Stopping a session removes it from both `sys.dm_xe_sessions` and
  `sys.dm_xe_session_targets` (1 row to 0 for each) while the catalog row remains.
  Measured exactly as the document states. This also means exit code 2's "a session
  that stopped on its own" is distinguishable after the fact, catalog row present
  and no DMV row, which the document does not say but which holds.
- The counters the archive needs, `dropped_event_count`, `dropped_buffer_count`,
  `largest_event_dropped_size`, `total_bytes_generated`, `create_time`, are all
  present on `sys.dm_xe_sessions` and readable by the minimal login.
- A duplicate `CREATE` raises `Msg 25631` and is catchable in `TRY`, so the
  collision section's "ordinary refusal and not an error" is implementable as
  described.
- A session name containing a space and a hyphen, `sql-auditor observe`, creates,
  starts, stops and drops without trouble when bracketed.
- I expected, and tested, that `Batch Requests/sec` would ignore RPCs, which would
  have made the consent prompt's size estimate systematically short on exactly the
  parameterised applications this command targets. It does not. Fifty RPC procedure
  calls plus two counter reads moved the counter by 51. The estimator's basis is
  sound; the residual inaccuracy is only that the counter is instance-wide while
  the session is filtered to one database, which errs on the safe side. I am
  reporting the rejected hypothesis rather than reshaping it into a finding.

---

## What I created and removed

On `sql2025`, all dropped and verified gone (`sys.server_event_sessions`,
`sys.server_principals`, `sys.databases` all return no `ZzObserve%` row, and
`/var/opt/mssql/log` holds no `ZzObserve*.xel`):

- event sessions `ZzObserve sql-auditor observe`, `ZzObserve dbidonly`,
  `ZzObserveQnSet`, `ZzObserveQnNoSet`, `ZzObserveBadPath`, `ZzObserveMinRights`,
  `ZzObserveTiming`;
- logins `ZzObserveLogin`, `ZzObserveLogin2`, `ZzObserveLogin3`;
- database `ZzObserveDB` with `dbo.ZzObserveT` and `dbo.ZzObserveP`;
- capture files `/var/opt/mssql/log/ZzObserve*.xel` and the scratch scripts under
  `/tmp` inside the container.

No client identifier appears above; names are the lab's own or the repository's
conventional placeholders. Nothing was written into the repository working tree,
and the throwaway Go module lives in the scratchpad.

One note for the controller: another reviewer was working on the same container
with the same `ZzObserve` prefix during this run, and their cleanup dropped two of
my sessions and their files mid-measurement, which is what produced a count that
went 11, then 0, then 11 again for a moment. I re-ran everything on uniquely
prefixed objects afterwards, and every number in this report comes from the re-run.
Sessions named `ZzObserve_Size0`, `ZzObserve_Size1`, `ZzObserve_Size2`,
`ZzObserve_Latency`, `ZzObserve_Rollover`, `ZzObserve_Test` and `ZzObserveReview`
were theirs, not mine; they were gone when I finished. A shared instance and a
shared naming convention is worth changing for the next panel.
