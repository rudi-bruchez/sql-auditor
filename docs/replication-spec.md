# Replication collection — specification

Status: draft, not implemented. Written 3 September 2026, rewritten the same
day after a five-reader external review.

The first draft's central mechanism was wrong, and every reader found it. That
draft said a collector could be made harmless on a database it does not apply
to by moving the flag test into a `WHERE` predicate. It cannot: the objects do
not exist there, name binding precedes filtering, and the resulting error is
not catchable in the same batch. The measured replacement is in "How a
collector guards itself", and it is the section to read first if you are
implementing this.

## What it is for

An audit of a server that publishes, distributes or subscribes has one question
behind all the others: **is the topology keeping up, and what is it costing the
publisher?** A log that will not truncate, a Log Reader that has been failing
for three weeks, a publication nobody removed but whose retention still pins
transactions in the distribution database — these are findings, and today the
archive cannot support any of them.

What it holds instead is four flags. `90.availability/040.replication.sql`
reports `is_published`, `is_merge_published`, `is_subscribed` and
`is_distributor` from `sys.databases`, and says in its own header that the
useful metadata is out of reach.

## What is already collected, and stays collected

Three collectors already carry part of the answer and the new files must not
duplicate them:

- `50.agent/010.jobs.sql` reports every SQL Agent job with its category and its
  last outcome. Replication runs as jobs in the `REPL-LogReader`,
  `REPL-Distribution` and `REPL-Snapshot` categories, and a failing agent is
  the most common replication finding in an audit.
- `20.databases/024.log-stats.sql` reports `log_reuse_wait_desc` beside the log
  size and its growth settings. A publisher whose log is held by `REPLICATION`
  is visible there today. It does **not** carry the VLF count, and says so in
  its own header — `023.log-vlf.sql` owns that, because two files reporting the
  same count is how they come to disagree.
- `90.availability/040.replication.sql` reports the flags, which remain the
  cheapest way to see a database that was restored from a publisher and kept a
  flag nobody cleared.

## How a collector guards itself

This section is load-bearing for all three collectors. Everything here was
measured on SQL Server 2022 (16.0.4265.3); the outputs are in the verification
note that accompanies this file.

**The problem.** `dbo.syspublications`, `dbo.sysarticles`,
`dbo.syssubscriptions` exist only in a published database;
`dbo.MSreplication_subscriptions` and `dbo.MSsubscription_agents` only in a
subscribed one; the `MS*` tables only in a distribution database. The
replication tables in `msdb` — `MSdistributiondbs`, `MSdistpublishers` — are
created by `sp_adddistributor` / `sp_adddistributiondb`, not by setup, so on an
instance that was never a distributor they are absent too.

Three measured facts follow:

1. A guard in the `WHERE` clause does not help. `SELECT … FROM
   dbo.syspublications WHERE 1 = 0` raises `Msg 208` on a database without the
   object, because names are bound before predicates are evaluated.
2. `TRY`/`CATCH` does not catch it. Error 208 is raised when the statement is
   compiled, and a handler at the same level never runs; the batch aborts, so
   every statement after it is lost. Result sets emitted *before* it do arrive,
   which is worse than a clean failure: the unit fails halfway.
3. Error 229 — permission denied on an object that *does* exist — **is**
   caught. The two failures the collector must survive are therefore not the
   same kind of failure, and the first draft treated them as one.

**Why the cited precedent does not transfer.** `50.agent/010.jobs.sql` survives
because its protected read is `INSERT INTO @h EXEC msdb.dbo.sp_help_jobhistory`:
the failure happens one execution level down, where it is catchable, and the
rows land in a table variable that exists either way, so the result-set count
never moves. Wrapping a row-returning `SELECT` in `TRY`/`CATCH` fails on both
counts — a caught error skips the remaining `SELECT`s, and the `CATCH`'s own
`SELECT` would add a result set the archive writer did not expect.

**The pattern.** Every collector is built this way, and no variation on it is
allowed without measuring the variation:

```sql
DECLARE @applies bit = 0, @collected bit = 1, @err int = 0, @msg nvarchar(2048) = N'';
SELECT @applies = CONVERT(bit, d.is_published)
FROM sys.databases AS d WHERE d.database_id = DB_ID();

DECLARE @pub TABLE (…);                       -- staging; exists unconditionally

IF @applies = 1
BEGIN
    BEGIN TRY
        INSERT INTO @pub (…)
        EXEC sys.sp_executesql N'SELECT … FROM dbo.syspublications … OPTION (RECOMPILE, MAXDOP 1)';
    END TRY
    BEGIN CATCH
        SELECT @collected = 0, @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH
END

SELECT CONVERT(int, @applies) AS [applies], CONVERT(int, @collected) AS [collected],
       @err AS [error_number], NULLIF(@msg, N'') AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

SELECT … FROM @pub AS p OPTION (RECOMPILE, MAXDOP 1);
```

Four properties, each doing work:

- **The guard is the role flag, and nothing else.** It says what the collector
  is *for*. It deliberately does **not** test whether the object exists: see
  the next paragraph, which is the correction that produced this version of the
  document.
- **`sp_executesql` defers name resolution**, which turns an uncatchable
  compile error into a catchable runtime one. Measured: the same read raises
  208 uncaught when written directly and returns 208 as a caught error inside
  `sp_executesql`. This is the whole safety mechanism; nothing else is needed.
- **`IF` guards only the staging, never a statement that returns rows.** The
  result sets are emitted unconditionally at the end, reading from table
  variables. The declared `@resultsets` count is therefore constant, which is
  what `ReadResultSets` requires — it errors on too few sets and on too many.
- **The `CATCH` assigns variables and returns nothing.** A `SELECT` in a
  `CATCH` is an extra result set and would fail the unit on the very runs the
  degradation exists to rescue.

### Why there is no `OBJECT_ID` test, which there was

An earlier version of this section guarded with
`@applies = 1 AND OBJECT_ID(N'dbo.syspublications', N'U') IS NOT NULL`, on the
reasoning that `OBJECT_ID` answers "does this object exist here" and never
raises. It does not answer that question. **`OBJECT_ID` returns NULL both for
an object that is absent and for one the caller may not see**, and metadata
visibility is exactly what a bare audit login lacks.

Measured: a table created by `sa`, read by a login with a user in the database
and no rights on the table, returns `OBJECT_ID` = NULL, while the same read
sent through `sp_executesql` raises 229. With the test in place the collector
skips the read and reports `applies = 1, collected = 1, no rows` — which this
document's own table calls *"there is genuinely nothing there"*. A publisher
with publications would have been recorded as one with none, silently, on
precisely the login this tool is built for.

Two independent readers found it, and the evidence was already in
`docs/verification-replication-guard.md`: the failure path there was obtained
"with the guard forced open". The note recorded that the mechanism only
produces its error state when the guard is removed, and nobody read it that
way.

Removing the test makes the design smaller and strictly better. The absent
object raises 208, the refused read raises 229, both are caught, and the
difference between them is the information the archive needs.

**Three states, and the archive can tell them apart:**

| `applies` | `collected` | Meaning |
| --- | --- | --- |
| 0 | 1 | The database does not carry the role. Nothing was attempted. |
| 1 | 0 | It does, and the read failed. `error_number` says whether the object was missing (208), the login was refused (229), or something else. |
| 1 | 1, no rows | It does, the read succeeded, and there is genuinely nothing there. |

Measured on a database with no replication: two result sets,
`applies=0, collected=1, error_number=0`, second set empty. On the failure
path: `applies=1, collected=0, error_number=208`, message intact, still two
result sets.

**The state this cannot distinguish**, and it must be said rather than
discovered: if a replication catalog view filters by visibility instead of
raising, a login without rights gets zero rows rather than error 229, and the
archive shows `applies=1, collected=1, no rows` — identical to a database that
genuinely publishes nothing. The repository already has a name for this failure
mode, `NeedsRows` in `collect/preflight.go`: *"a probe whose denial is silent…
the query must be one whose object is never legitimately empty, and an empty
result is the denial."* Whether `syspublications` behaves that way for a bare
reader is unverified and is the first thing the verification run must measure.
Until it is measured, no collector claims to distinguish the two.

## The targeting problem, and the rule that solves it

Publication and subscription metadata lives in the published and subscribed
databases, both already in the selection. Distribution metadata lives in **the
distribution database**, whose name is chosen when replication is configured.
`sp_adddistributiondb` has **no default** for `@database`; `distribution` is
the name the SSMS wizard suggests, and nothing obliges anyone to accept it.

The name is not the hard part. The distribution database has a `database_id`
greater than 4, so `CandidateDatabases` already lists it and a database-scoped
collector already runs against it — on a run with no `DB_INCLUDE`. The gap is
narrow: **an operator who narrows the run to the application database loses the
distributor that goes with it.**

So the rule is a second pass over the selection, not a third scope:

> A database carrying `is_distributor = 1` is retained even when `DB_INCLUDE`
> does not match it, provided at least one database **retained after
> filtering** carries `is_published = 1`. `DB_EXCLUDE` still wins, and so do
> the state and access checks.

"Retained after filtering" is exact and decides a case two implementers would
otherwise split on: a publisher the operator **excluded**, or one the login
cannot access, does not trigger retention. Only a publisher that survives into
`Included` does. Both cases get a test.

Three properties make this a rule rather than a convenience. It only fires
where it is needed — with no `DB_INCLUDE` nothing changes. It cannot fire on a
server that is only a distributor, since no local database is published. And
`DB_EXCLUDE` remains an explicit lever for a client who will not have that
database read.

**Two implementation details the first draft glossed.** `SelectTargets` applies
one `switch` in the order state → snapshot → access → include → exclude, so a
database skipped by `DB_INCLUDE` already carries a `SkipReason`; the second
pass must remove that entry, not merely add an `Included` one, or the manifest
lists the database twice with contradictory reasons. And `Selection.Included`
is a `[]string` today: a retention reason beside an included database is a
struct change reaching `check`'s database listing and `writeTargets` in
`collect/manifest.go`, not only `SelectTargets`.

### The widened database is not a general target

A database retained by the second pass is retained **for the replication
collectors only**. `planUnits` pairs it with those collectors and with nothing
else.

**How it knows, which the first version of this section did not say.** Four
readers pointed out the same hole: `planUnits` receives `[]DatabaseFolder`,
which is `{Name, Folder}` and carries no provenance, and `Script` has no field
saying "this is a replication collector". Left unspecified, an implementer
reaches for a hardcoded path prefix in the orchestrator — the silent, unlinted
coupling that `KnownFlags`, `KnownWriters` and `permissionKeys` exist to
prevent, and one that `--queries-dir` would defeat without a word.

So both halves are declared:

- `DatabaseFolder` gains `WidenedFor string`, empty for an ordinarily selected
  database and `"replication"` for one the second pass brought back.
- The three collectors declare `-- @widened: replication`, parsed in
  `collect/queryset.go` into `Script.Widened` against a closed vocabulary, like
  every other directive.

`planUnits` then pairs a widened folder only with a script whose `Widened`
matches. That is a change to `queryset.go`, and the change list below says so;
the earlier claim that nothing there changes was wrong.

**It also costs manifest noise, which is worth pricing.** Roughly thirty
database-scoped collectors exist. Skipping each of them for the retained
database would write thirty "Queries not run" entries into every widened run.
It is therefore not a skip: a widened folder is never offered to those scripts
in the first place, so nothing is recorded as skipped, and the manifest's
retention reason on the database is where a reader learns what happened.

This is not tidiness. Left as an ordinary target, the distribution database
would receive the whole database-scoped corpus, and two collectors there are
actively harmful: `70.schema/041.compression-savings.sql` under
`--estimate-compression` runs `sp_estimate_data_compression_savings` against
`MSrepl_commands`, the largest table on any busy distributor and the one this
specification refuses to so much as count; and `70.schema/080.modules.sql`
under `--include-object-definitions` dumps the source of several hundred
internal replication procedures.

The restriction applies only to the widened case. When the operator did not
narrow the run, the distribution database is an ordinary selected database and
everything runs against it as it does today — this changes no existing
behaviour, it only declines to create a new one.

### The publisher whose distributor is elsewhere

When a database is published and no local database carries `is_distributor`,
the distributor is remote. `sys.servers` carries an `is_distributor` flag, but
the row's `name` is the alias `repl_distributor`, not the server: the instance
is in `data_source`. `040.replication.sql` reports
`COALESCE(NULLIF(data_source, N''), name)` and says which it used.

## The three collectors

One per role, each `@scope: database`, each built on the pattern above.

Directives are part of the specification, because a missing `@timeout` is a
hard lint error, and an unknown directive name is now a lint error too — that
was fixed while this document was under review, so a misspelling fails loudly
rather than shipping ungated.

Each file declares `@scope: database`, an explicit `@resultsets` list,
`-- @widened: replication`, and `@permissions: CONNECT, VIEW ANY DEFINITION` —
the rights the corpus already asks for, and no more. See "Permissions" for why
nothing more is asked.

`@timeout` is **120**, not the 60 the first version wrote without measuring it.
`041.connectivity.sql` already declares 120 for decoding a single ring buffer;
`042` aggregates seven days of two history tables with a `PERCENTILE_CONT` sort
under `MAXDOP 1`. A timeout fails the whole unit and the degradation pattern
does not catch it, so the number is the one place here where guessing low costs
the run.

**`042` needs `VIEW SERVER STATE` for one reading and must not pretend
otherwise.** `sys.dm_db_partition_stats`, used for the `MSrepl_commands` row
count, is a database-scoped DMV requiring `VIEW DATABASE STATE` — measured,
`Msg 262` then `Msg 297` under `VIEW ANY DEFINITION` alone, and readable with
`VIEW SERVER STATE`, which carries `VIEW DATABASE STATE` into every database.
Rather than raise the whole file's declared permission and have it skipped on
logins that could collect everything else, the row count moves to its own
statement guarded like the rest: if it fails, `collected = 0` for that section
and the topology still lands.

Every projection is an explicit column list. `MSlogreader_agents` carries
`publisher_password` and `job_password`, and `syspublications` carries
`ftp_password`; they are encrypted blobs, they have no place in an audit
archive, and a projection that drifts toward `SELECT *` puts them there.

### `90.availability/041.replication-publisher.sql`

Guarded on `is_published` and on the presence of `dbo.syspublications`.

- `dbo.syspublications` — name, `status`, `repl_freq`, `sync_method`,
  `retention`, `allow_push`, `allow_pull`, `allow_anonymous`,
  `immediate_sync`, `independent_agent`, `allow_sync_tran`,
  `allow_queued_tran`.
- `dbo.sysarticles` — one row per published article with its destination
  object.
- `dbo.syssubscriptions` — who subscribes, push or pull, and the
  synchronization status.

`immediate_sync` and `allow_anonymous` are why this file exists separately.
Together they make the distribution database keep every command for the full
retention period whether or not a subscriber has taken it, which is the
ordinary explanation for a publisher log that will not truncate. Read beside
`024.log-stats.sql`, they turn `log_reuse_wait = REPLICATION` from a symptom
into a cause.

**Beware the homonyms.** `syspublications`, `sysarticles` and `syssubscriptions`
exist *both* as tables in the publication database and as views with different
column sets in the distribution database. 041 and 042 both run in both places.
Every reference is guarded by `OBJECT_ID` and every projection is written for
the database it is meant for; a column list that happens to compile in the
wrong one is a silent wrong answer.

### `90.availability/042.replication-distribution.sql`

Guarded on `is_distributor` and, separately, on the presence of each object
family it reads — the msdb tables are absent on an instance that was never a
distributor, so they get their own `OBJECT_ID` test rather than riding on the
database flag.

**Configuration**, from `msdb.dbo.MSdistributiondbs`, filtered to
`name = DB_NAME()` because the table holds one row per distribution database
and this collector runs inside one of them: `min_distretention`,
`max_distretention`, `history_retention`. These bound the meaning of everything
else — an agent history reaching back six hours is not evidence of a young
topology if `history_retention` is six hours.

**Topology**: `MSpublications` (`publisher_id`, `publisher_db`, `publication`,
`publication_type`, `retention`, `immediate_sync`), `MSarticles`
(`article`, `source_owner`, `source_object`, `destination_owner`,
`destination_object`), and `MSsubscriptions` for the publication-to-subscriber
mapping. `publication_type` distinguishes transactional from snapshot and
merge, so a merge publication is visible even though its metadata is out of
scope.

`MSpublications` has no `publisher` column — the publisher is `publisher_id`,
and resolving it to a name needs a join whose correct target at the 2012 floor
is unsettled (`MSreplservers` exists only from 2016 SP2 CU3). The collector
reports `publisher_id` and `publisher_db` unconditionally; the resolved name is
added only if the verification run confirms a join that works at the floor.

**Agents**: `MSdistribution_agents` (name, publication, `subscriber_id`,
`subscriber_db`, `job_id`), `MSlogreader_agents` and `MSsnapshot_agents` (name,
`publisher_db`, publication, `job_id`, `local_job`). Neither of the last two has
a subscriber column, and correctly so — a Log Reader reads a publisher's log
and a Snapshot Agent writes files; neither talks to a subscriber.

**Profiles**, from `msdb.dbo.MSagent_profiles` and
`msdb.dbo.MSagent_profile_parameters`: the batch and polling parameters
(`-CommitBatchSize`, `-ReadBatchSize`, `-PollingInterval`,
`-SubscriptionStreams`) that explain more latency than any single failure does.
Lightweight catalog tables, guarded like the rest.

**Latency**, aggregated over a bounded window from `MSdistribution_history` and
`MSlogreader_history`. Both carry `agent_id`, `runstatus`, `start_time`, `time`,
`duration`, `comments`, `delivery_latency`, `delivered_commands`, `error_id`.
The two latencies measure different legs and the archive must not merge them:
in `MSlogreader_history`, `delivery_latency` is the milliseconds between a
command committing in the published database and arriving in the distribution
database; in `MSdistribution_history` it is the milliseconds between the
distribution database and the subscriber. A topology that is behind is behind
on one leg or the other, and which one decides where to look.

Per agent: the most recent session with its `runstatus` and time, the latest
`delivery_latency`, the maximum over the window, and the median. The median is
not `MEDIAN()`, which does not exist in T-SQL, and it is not
`PERCENTILE_CONT(0.5) WITHIN GROUP (…)` used as an aggregate, which exists in
Azure SQL and Fabric and on **no** version of SQL Server — measured,
`Msg 10753, the function 'PERCENTILE_CONT' must have an OVER clause`, on 2022.
At the 2012 floor and everywhere else it is the analytic form,
`PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY delivery_latency)
OVER (PARTITION BY agent_id)`, computed in a CTE and collapsed by a grouping
outside it. The file is parsed under the 2012 grammar by
`tools/verify-corpus-grammar.ps1` before it is committed.

`runstatus` is stored as its code and its label together — 1 start, 2 succeed,
3 in progress, 4 idle, 5 retry, 6 fail — because a code alone sends the reader
to the documentation and a label alone cannot be matched on.

**Cleanup throughput.** The distribution cleanup agents write to the same
history tables, and their rows are collected under the same window. This closes
a loop nothing else does: the specification's answer to "is the distribution
database growing" is the size of `MSrepl_commands` from
`sys.dm_db_partition_stats`, and a growing `MSrepl_commands` means cleanup is
not keeping up. `010.jobs.sql` gives the cleanup job's last outcome, but an
outcome is not a throughput — a job that succeeds nightly while deleting
nothing looks identical to one that is working.

**Errors**, from `MSrepl_errors`: the last 50 rows in the window, message
truncated to 512 characters. `error_id` in the history tables joins to it, so a
failing agent's own error is retrievable rather than merely counted. Fifty is
chosen so a repeating failure reads as a repetition; the verification run
confirms or changes it.

**Tracer tokens**, from `MStracer_history` and `MStracer_tokens`, joined on
`parent_tracer_id = tracer_id`. They are read like everything else — always
queried, returning an empty set when there are none. The first draft said "read
only if they contain anything", which contradicts the fixed result-set count in
the same document. Note for the implementer: these latencies are `datetime`
deltas across the two tables, not the stored milliseconds of the columns beside
them.

### `90.availability/043.replication-subscriber.sql`

Guarded on `is_subscribed` and on object presence. Reads
`dbo.MSreplication_subscriptions` — publisher, publisher database, publication,
`subscription_type` (0 push, 1 pull, 2 anonymous), `distribution_agent`, and
`Time`, the last update the Distribution Agent made — and
`dbo.MSsubscription_agents`.

One reviewer placed `distribution_agent` in `dbo.MSsubscription_properties`
instead. Microsoft's documentation lists it on `MSreplication_subscriptions`;
both tables exist on a subscriber and the verification run settles which
carries it in practice. The collector reads `MSreplication_subscriptions` and,
if the column is absent there, the guarded pattern records the error rather
than failing the unit.

On a pull subscription this file is the only place the topology is visible at
all: the agent runs on the subscriber and its history lives on the distributor,
which may be a server the audit never touches. `Time` going stale is then the
whole signal.

### Two additions to `040.replication.sql`

Both are instance-scoped, need no right the corpus does not already hold, and
belong beside the flags rather than in a new file:

- **Replication performance counters**, from
  `sys.dm_os_performance_counters` (`SQLServer:Replication Logreader`,
  `SQLServer:Replication Dist.`). Live throughput without touching a history
  table.

**And one thing that was here and cannot be done.** An earlier version of this
section proposed reading `sys.dm_tran_database_transactions` to name "the
oldest un-replicated transaction" when `log_reuse_wait_desc` is `REPLICATION`.
That view lists only transactions that are still **active**. When the log is
held by replication, the transactions holding it have *committed* and are
waiting for the Log Reader; they left that view at the instant of commit —
measured, the count drops to zero on `COMMIT`. On the three-week-old failure
this document opens with, the query returns nothing. Naming the watermark needs
`DBCC OPENTRAN` or a comparison of `MSrepl_commands.xact_seqno` against the
publisher's LSN, neither of which is proposed here. The gap is real and stays
open rather than being closed by a query that cannot answer it.

**The permission changes, and that is not free.** Today `040.replication.sql`
declares `CONNECT, VIEW ANY DEFINITION` and succeeds on a login holding exactly
that. `sys.dm_os_performance_counters` needs `VIEW SERVER STATE` — measured,
`Msg 300` then `Msg 297` without it. Since `@permissions` drives the skip gate,
the file must declare `VIEW SERVER STATE`, and a login that lacks it then loses
the four replication flags it collects today.

That trade is not worth making in one file. The counters go in their own file,
`90.availability/044.replication-counters.sql` — 041, 042 and 043 are taken by
the collectors above — declaring `VIEW SERVER STATE`, so a login without that
right loses the counters and keeps the flags. The
existing `040` keeps its permissions and gains only the `sys.servers` reading,
which needs nothing new.

The file's header is rewritten. Most of it argues that distribution metadata is
unreachable, and that argument stops being true here. What stays: the flags are
flags, not proof of activity, and a database restored from a publisher keeps
them.

## Permissions

**Nothing is required, and nothing is requested.** The collectors read what the
audit login already reaches and record the refusal when it does not. The DBA
guide asks for no new grant; in particular it never asks for `db_owner` on a
client's application database, which is what reading `syspublications` may
require and which would contradict the promise that makes this tool acceptable
at all: the audit login cannot read application data.

Where a client's login happens to hold more — a DBA running it themselves, a
service account already in `db_owner` — the collectors read more, and the
archive says so. That is the whole posture, and it costs no code: a refusal is
error 229, which the pattern above catches and records as
`collected = 0` with the number and the message.

This is a deliberate reversal of the first draft, which promised a `@permissions`
key, a `check` probe and a `--grant-script` section for a replication right.
Each would have opened a closed vocabulary — `permissionKeys` in
`collect/queryset.go`, `Capabilities()` in `collect/preflight.go`,
`BuildGrantScript` in `collect/grants.go` — and the preflight one is not even
shaped for the job: its probes are instance-level and run before the database
list exists, while the right in question lives inside a database whose name is
learned later. Requiring nothing removes all three changes.

The cost is honest and stated, and the first version stated it wrongly. It said
that on a bare audit login `041` would record `collected = 0` with error 229.
It would not: `@permissions` drives the skip gate, so a login that lacks
`VIEW ANY DEFINITION` never runs the file at all — it lands under "Queries not
run" in the manifest, which is a different and better-labelled outcome. The 229
path is reached only by a login that holds `VIEW ANY DEFINITION` and still
cannot read `syspublications`, which is the ordinary audit login this practice
is given.

Either way the publisher side of the picture is thinner than the distributor
side, and the archive says which of the two happened. That is worth more than a
grant nobody should give.

## The read-only promise is a design constraint

The manifest of every archive says the collector reads: only SELECT statements
against catalog and dynamic management views, no user or application table, and
— the sentence that matters — **it creates no permanent object; nothing that
belongs to the server or its databases is created, altered or deleted.**
`observe` exists as a separate command because `CREATE EVENT SESSION` creates
one, and a promise with one exception is not a promise.

That wording is new, and this document is part of why. The sentence used to
read "runs no INSERT, UPDATE, DELETE or DDL", which the guard pattern above
contradicts in its first line: `INSERT INTO @pub … EXEC sp_executesql` is an
INSERT. So were two collectors already shipped, which capture `sp_readerrorlog`
and `sp_estimate_data_compression_savings` the same way. An external reader
found the contradiction here, in the section that quotes the promise as
binding. The sentence was corrected rather than the corpus, because what a
security officer needs to know is not which SQL verb is used but what the
server keeps afterwards — nothing — and the staging happens in tempdb.

The same reasoning binds the rest of the design:

**No `sp_posttracertoken`.** Posting a tracer token is the sanctioned way to
measure end-to-end latency, and it is a write. Where a client already posts
tokens, `MStracer_history` holds the measurements and they are collected for
free; where nobody does, the archive says the history is empty rather than
manufacturing a number.

**Catalog tables and views, not `sp_replmonitor*` procedures.** A procedure's
result-set shape is not a contract, several of them require `replmonitor`, and
some build temporary tables.

**No `MSdistribution_status`.** The view exposes `UndelivCmdsInDistDB`, the
count of commands pending delivery, and it is the number that most directly
explains a growing distribution database. It aggregates `MSrepl_commands`, the
largest table on any busy distributor, and an audit collector must not be the
reason a distributor stalls. `sys.dm_db_partition_stats` gives that table's row
count for nothing, and the cleanup-agent history above says whether the number
is being worked down.

## Cost, and the window

The history reads are bounded to seven days by a literal in the SQL —
`DECLARE @window_days int = 7;` — exactly as `50.agent/010.jobs.sql` bounds its
own history at thirty.

It is **not** configurable, and the first draft's "the only tunable, on the
model of `QUERY_STORE_DAYS`" was wrong twice over. Configuration keys are a
closed set in `collect/config.go`, and a configured value reaches SQL only
through `queryStoreArgs`, which serves the two `@writer` scripts and returns
nothing for everyone else — its own comment refuses to generalise, because
doing so "would switch all twenty-eight to `sp_executesql` for the sake of
two". Making the window configurable is that change, and this specification
does not ask for it.

Every statement carries `OPTION (RECOMPILE, MAXDOP 1)` and
`SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED`, including the statements
inside `sp_executesql` strings — `StripSQLComments` tracks string literals
rather than discarding them, so the contract lint sees the hints there. No
`COUNT(*)` runs against `MSrepl_commands`.

## Changes to existing code

The first draft's list was short because it was incomplete. This one is the
whole of it:

- **`collect.DatabaseInfo`** gains `IsPublished`, `IsSubscribed` and
  `IsDistributor`; `CandidateDatabases` projects them from the same
  `sys.databases` read.
- **`collect.SelectTargets`** gains the second pass, including removal of the
  superseded `SkipReason`.
- **`collect.Selection`** gains a retention reason for an included database,
  which `check`'s listing and `manifest.writeTargets` render. `writeTargets`
  iterates `m.Targets.Databases`, which is `[]DatabaseFolder`, so the reason
  travels on `DatabaseFolder` rather than on `Selection.Included` — the first
  version named the wrong struct.
- **`collect.DatabaseFolder`** gains `WidenedFor string`.
- **`collect.queryset`** gains the `@widened` directive and its closed
  vocabulary, and `Script` gains the field. The first version claimed nothing
  here changed; that was only true of the permissions work.
- **`collect.planUnits`** pairs a widened folder only with a script whose
  `@widened` matches, and offers it to no other `@scope: database` collector.
- **`queries/90.availability/040.replication.sql`** — header rewritten, plus
  the two instance-scoped additions.
- **`docs/dba-guide.md`** — a paragraph saying that no new grant is asked for,
  what is thinner without one, and that a narrowed run may collect a
  distribution database the operator did not name.

Nothing in `queryset.go`, `preflight.go` or `grants.go` changes.

## What this does not do

**Merge replication.** A different metadata model and a different set of
questions. `MSpublications.publication_type = 2` makes a merge publication
visible on the distributor, which is enough to say it exists and that the audit
did not examine it.

**Peer-to-peer and updatable subscriptions.** `allow_sync_tran` and
`allow_queued_tran` are read where they sit in `syspublications`, but no
collector is written for the Queue Reader.

**Any judgement.** No latency is called high, no retention wrong, no failing
agent a problem. A Log Reader failing for a month may be a decommissioned
publication nobody removed, and the archive is not the place that decides.

**Replication on Azure SQL Database and managed instances.**

## Tests and verification

Go tests cover what Go does: the second pass in `SelectTargets` — retained when
a published database survives filtering, not retained when the only publisher
was excluded or inaccessible, excluded when `DB_EXCLUDE` names it, and the
superseded skip entry removed — the `planUnits` restriction, and the manifest
rendering of the retention reason.

The SQL is verified manually against a real topology and recorded on the model
of `docs/verification-2012.md`: the statement, the server version, the result,
and what was refused, with no proper nouns. `compose.yaml` runs a single
container and configuring a publisher, distributor and subscriber inside it is
a substantial piece of work whose return is one integration test; that work is
not proposed here, and the specification does not pretend the SQL is covered by
`go test`.

The verification run answers, in this order, the questions this document could
not:

1. Does a bare reader get error 229 from `syspublications`, or zero rows? The
   whole "three states" table depends on the answer.
2. Does `replmonitor` grant direct `SELECT` on the distribution tables, and do
   those tables already grant to `public`? The permissions posture asks for
   nothing either way, but the guide's description of what is thinner without a
   grant should be true.
3. Which join resolves `MSpublications.publisher_id` to a name at the 2012
   floor.
4. Whether `distribution_agent` is on `MSreplication_subscriptions`,
   `MSsubscription_properties`, or both.
5. Whether 50 rows of `MSrepl_errors` is the right number.

The floor stays SQL Server 2012, spelled `@min_version` where a file needs it.
No file is expected to need it, and `tools/verify-corpus-grammar.ps1` is what
confirms that rather than this paragraph.

## A defect this uncovered, since fixed

`parseScript` switched on directive names with no default case, so a name it
did not recognise was silently ignored. A file carrying `-- @minversion:` — the
misspelling the first draft of this document itself used — would have been
ungated on every version with nothing anywhere saying so.

It is fixed: an unknown directive is now a lint error. Writing the test first
turned up three header lines in the shipped corpus that opened with an `@` word
in prose, which the parser had been reading as directives all along; all three
are reworded. The rule that follows is worth carrying into the three new files:
**a header line never begins with an `@` word, even in prose.**

This mattered here beyond tidiness. It is what makes the `@widened` directive
above safe to introduce — a misspelling of it now fails loudly instead of
quietly widening nothing.

## What a competent audit will still ask for, and this does not collect

Three gaps a reviewer named, kept here rather than quietly widened into the
scope:

**Article filters.** `041` collects article names and destination objects but
not `sysarticles.filter` and `filter_clause`, nor the column list from
`sysarticlecolumns`. When a subscriber is missing rows, a horizontal row filter
or a dropped column is the first cause to check, and the archive would not
show it. This is cheap to add and the only reason it is not in the projection
is that nobody has yet said which of the two matters more often.

**The un-replicated watermark.** With the `sys.dm_tran_database_transactions`
idea withdrawn, nothing says how far behind the Log Reader is in log terms.
The real answer compares `MSrepl_commands.xact_seqno` on the distributor with
the publisher's current LSN, which crosses the two databases this design keeps
apart. It is the most valuable thing missing.

**Retention actually honoured.** `MSdistributiondbs` gives the configured
retention; nothing checks whether `MSrepl_commands` holds transactions older
than `max_distretention`, which is the signature of a cleanup job that runs and
deletes nothing. The cleanup-agent history above says whether cleanup ran; this
would say whether it worked.

## Open questions

**Should the widening extend to `is_merge_published`?** Today it does not,
because merge is out of scope. If a merge collector is ever written, the rule
needs one word changed, and the decision belongs to that moment.

**A stale flag widens the run.** `is_published` survives a restore from a
publisher — this document says so itself, twice — so a leftover flag on an
included database retains the distribution database into a narrowed run. The
guards make it harmless: `042` finds nothing and records that it found nothing.
But an operator will see a database they did not name, and the manifest's
retention reason is where they will learn why. That is acceptable, and it is
written down here so that the first person it surprises can find it. It gets a
test fixture with a stale flag.

**Does the `planUnits` restriction want an escape?** An auditor who genuinely
wants the distribution database inventoried can name it in `DB_INCLUDE`, at
which point it is an ordinary target and the restriction does not apply. That
seems sufficient, but it is untested against a real engagement.
