# Parallel cost from the Query Store

Status: proposed on 4 October 2026; implemented on 6 October 2026 by the plan docs/superpowers/plans/2026-10-06-query-store-parallel-cost.md. The first draft
(`87a8d9f`) put the collector behind an opt-in; it was read the same day by a
panel of five readers, and then the decision changed: the collector runs by
default, with no option. The second version answered both and was read by a
second panel of five the same day. The third version (`760b4bf`) answered that
panel and left one decision to the owner: whether to keep a session marker,
`SET DATEFIRST 3`, that let the file leave sql-auditor's own statements out.
The fourth version (`16c0591`) recorded that decision, taken on 5 October 2026:
the marker was withdrawn, on a measurement of what a default collection leaves
in the stores it audits. A third panel read it and found that measurement false
on client volumes: a default collector compiled a parallel plan over a 200 000
row maintenance log. The contract lint was changed the same night (`aeb4619`,
`a9fe54c`) to require `OPTION (RECOMPILE, MAXDOP 1)` on every statement that
reads rows, not only on result sets. This fifth version rests the property
on that lint rather than on a measurement, says what the lint does not reach,
and answers the rest of the panel. The marker stays withdrawn. What each panel
found, and what became of each finding, is in the three review sections at the
end. No decision is left to the owner before code. Nothing in the tree has
changed yet for 043.

## The question

`80.workload/042.parallel-cost-distribution.sql` puts the cached statements
with the most CPU into bands of estimated cost and says, per band, how many ran
in parallel, how often and with how much CPU. It is the only measurement in the
corpus that can choose a value for `cost threshold for parallelism`, and the
analysis reads it with `seuil_parallelisme.py` in the private repository.

Its limit is the cache. Under memory pressure the cache is emptied
continuously (one client instance held 114 plans in all), and a restart, most
`sp_configure` changes and a flush empty it too. The header of 042 says what
would not depend on that: "THE QUERY STORE IS NOT READ HERE. It keeps what the
cache evicts [...] It runs per database, so its conversions multiply by the
number of databases, and it needs a window and a cap of its own; it is not a
second result set of this file."

This document settles which plans are read, how their cost is found, which
statements are left out, how much a database may cost, the shape of the
output, what changes in the tree, and the tests.

## The decision to run it by default

The first decision of 4 October 2026 put this collector behind an option, off
by default, for its cost per database. The prototype then measured that cost
at 56 to 90 ms per database on the lab (29 to 148 ms in the re-measurement of
"The cost, measured", first cold runs included), and the output carries no text
and no identifier of a query or a plan. The decision was revised the same day
on a general rule: what is useful and raises no confidentiality question runs
by default.

So there is no option. The file declares no `@requires_flag`; `KnownFlags`,
`CostFlags`, `--all`, the wizard's third screen and the README's option tables
do not change. What makes the default acceptable is two properties, each of
which this document turns into a test: nothing that names a query leaves the
server ("The guarantee that no text leaves"), and the cost per database is
reported and limited ("The constants and the stop rules"). The limit is not a
hard ceiling below the file's `@timeout`: the selection is not budgeted, and
the read loop's two budgets are checked between chunks. What each part can
cost, and what reports it, is said there and in "The cost, measured".

## What the store holds, from the documentation and from the lab

From Microsoft Learn:

- `sys.query_store_runtime_stats` carries, per plan, interval and
  `execution_type`, `count_executions`, `avg_cpu_time` (microseconds) and the
  five DOP columns `avg_dop`, `last_dop`, `min_dop`, `max_dop`, `stdev_dop`,
  from SQL Server 2016 (13.x). Its remarks warn that the DOP columns can
  "report large numbers" on systems with many processors, "in scenarios where
  the query uses user defined functions", and call it a reporting issue. From
  SQL Server 2022 (16.x) it also carries `replica_group_id`, "Foreign key to
  sys.query_store_replicas".
  https://learn.microsoft.com/en-us/sql/relational-databases/system-catalog-views/sys-query-store-runtime-stats-transact-sql
- `sys.query_store_plan` carries `query_plan` (`nvarchar(max)`, "Showplan XML
  for the query plan"), `is_parallel_plan` and `last_execution_time`, from
  SQL Server 2016. It has no cost column.
  https://learn.microsoft.com/en-us/sql/relational-databases/system-catalog-views/sys-query-store-plan-transact-sql
- Query Store for readable secondaries: "secondary replicas stream query
  execution information (such as runtime and wait statistics) to the primary
  replica, where the data is persisted in Query Store and made visible across
  all replicas", "aggregated at the role level", told apart by
  `replica_group_id`, which `sys.query_store_replicas` (SQL Server 2025, and
  present on a 2022 CU26 container, measured on 6 October 2026) maps
  to a `role_type` (1 primary, 2 secondary, 3 geo secondary, 4 geo HA
  secondary, 5 or more a named replica). Learn contradicts itself on the
  default: "What's new in SQL Server 2025" and "Persisted statistics for
  readable secondary replicas" say it is on by default; the feature's own page
  gives SQL Server 2025 as "No (can be enabled, per database)", with an
  availability group as a prerequisite, and SQL Server 2022 as a limited
  preview behind trace flag 12606. Either way, a store can hold another
  instance's work.
  https://learn.microsoft.com/en-us/sql/relational-databases/performance/query-store-for-secondary-replicas
  https://learn.microsoft.com/en-us/sql/relational-databases/system-catalog-views/sys-query-store-replicas
- The estimated cost of a statement is the `StatementSubTreeCost` attribute of
  its statement element (`StmtSimple`, `StmtCond`, `StmtCursor`) in the
  showplan schema, the same attribute 042 reads.
  http://schemas.microsoft.com/sqlserver/2004/07/showplan/
- The views require `VIEW DATABASE STATE`, and `VIEW DATABASE PERFORMANCE
  STATE` from SQL Server 2022. `sys.fn_builtin_permissions` on the lab gives
  the chain that makes 029's `VIEW SERVER STATE` enough on every version:
  `VIEW DATABASE PERFORMANCE STATE` is covered at server level by `VIEW SERVER
  PERFORMANCE STATE`, which is covered by `VIEW SERVER STATE`.

Measured on the lab, SQL Server 2025 CU7 (17.0.4065.4, 22 schedulers, Linux,
cost threshold 5, MAXDOP 0), 4 October 2026: in three databases created for it
(`ZzAvgDop` for the first draft, `ZzAvgDop2` for the second version,
`ZzAvgDop3` for the third) with the store in capture mode ALL and one-minute
intervals, dropped afterwards; and in the existing stores of five lab
databases, `LABSTORE_A` (955 plans, 73 MB) and `LABSTORE_B` (126 plans, 24 MB)
among them, read from `master` with three-part names so that the reads were
not captured into those stores. Point 8 has a setup of its own, given there.

1. A stored plan is the plan of one query, and its first costed statement
   element is that query's own. A procedure of two statements (a key lookup,
   then a parallel aggregate) gave two plans, each holding one statement
   element, so the first trap of 042, the batch plan whose first statement is
   a neighbour, does not exist here. The `Statements` element can still hold
   two children: in `LABSTORE_A`, 104 of 955 plans did, all of them
   `INSERT ... EXEC` statements, whose second element carries no cost. Over
   the 955 plans, the string `StatementSubTreeCost="` occurred once in 954
   and never in one (a plan with no statement). No stored plan with two costs
   has been seen; it is checked again by test 3 rather than assumed.
2. The cost found by a text search is the cost of that statement. On the 22
   plans of `ZzAvgDop` the first `StatementSubTreeCost="` of the text and
   `(//StmtSimple/@StatementSubTreeCost)[1]` read as xml agreed 22 times. On
   the plans of `LABSTORE_A`, the text search found the cost of the first
   statement element every time there was one; the `//StmtSimple` path missed
   ten `StmtCond` plans that the text search read correctly. A literal
   `StatementSubTreeCost="123"` written in the query text cannot match, since
   the showplan escapes the quote of `StatementText` as `&quot;` (agy).
3. The cost of a parallel plan is the parallel cost, as in the cache, and the
   boundary is not the threshold. Under a threshold of 5, the scans costing
   under 4.83 stayed serial and the parallel ones began at 6.32 in
   `ZzAvgDop`; a second reader found plans costing 4.577 at DOP 22 and 4.832
   under `MAXDOP 2` that were parallel, and 2.937 serial. A parallel plan's
   own cost can sit under the threshold because the threshold was compared
   with the serial cost.
4. `is_parallel_plan` and `max_dop` disagree, and `max_dop` is the wrong one,
   but only for some statements. In `ZzAvgDop2`, `INSERT INTO #savings EXEC
   sys.sp_estimate_data_compression_savings` gave a plan with
   `is_parallel_plan = 0` and `max_dop` 41, on an instance of 22 schedulers,
   with 1 604 ms of CPU for one execution; in `LABSTORE_A` the same call
   reported 27, and two `INSERT INTO @density` and one `INSERT INTO @props`
   statements reported 2. In `ZzAvgDop3`, with the exact artefacts of test 3
   (041's `#savings`, the procedure called on a heap of 2 000 000 rows), it
   reported 36 from one session and 5 from another, both serial. In the
   collections of point 8, `INSERT INTO @props` and `INSERT INTO @density`
   reported 2 although the statement they execute carries `MAXDOP 1`.
   An ordinary `INSERT INTO #o EXEC dbo.ParAgg`, whose procedure runs a
   parallel aggregate at DOP 22, was stored serial with `max_dop` 1, and so
   were `INSERT INTO @t SELECT ... FROM sys.all_objects` and a procedure
   feeding a temporary table (Claude). So the degree is not simply inherited
   from nested parallel work; what the anomalous statements share is not
   established. A plan that is not parallel cannot be made serial by a
   threshold, so this collector selects on `is_parallel_plan` and never on
   `max_dop`. The converse exists too: in `LABSTORE_A`, executed plans with
   `is_parallel_plan = 1` had runtime rows at `max_dop` 1, one of them with
   54 s of CPU over eleven executions, all at DOP 1.
5. Plans with `is_parallel_plan = 1` and no statement exist: the two
   `SELECT StatMan(...)` queries of an automatic statistics update in
   `ZzAvgDop`, stored with an empty `<Statements/>`. They ran at degree 16.
   They have no cost and belong to the unknown band.
6. A store that is off still returns its data. After `ALTER DATABASE ... SET
   QUERY_STORE = OFF` on `ZzAvgDop2`, `sys.database_query_store_options` read
   `OFF` and the views still returned 42 plans (7 parallel) and 41 runtime
   rows, as they had in Claude's and both codex readers' databases. A rule
   about the state has to be a predicate in the file. On SQL Server 2022 and
   later a new database inherits `model`'s store, which is on: `ZzAvgDop3`
   read `READ_WRITE` straight after `CREATE DATABASE`. The options view
   returned no row only in `master` (and `tempdb`, per Claude).
7. Where the CPU of nested work is counted depends on the kind of object.
   In `ZzAvgDop2`, a scalar function declared `INLINE = OFF` whose body
   aggregates the table used 1 411.6 ms in its own query, and its caller
   1 418.3 ms: the function's CPU is counted twice, as 029 measured. An AFTER
   INSERT trigger running a parallel aggregate used 1 169.1 ms over three
   executions, and the three `INSERT` statements that fired it 15.1 ms in all;
   a multi-statement table-valued function used 490.4 ms in its body and its
   two callers 6.5 ms. Their CPU is counted once, in their own query. The
   trigger case reproduces Claude's (2 086.9 ms against 116.7 ms, and 395 ms
   against 49 ms in the second panel).
8. What a collection leaves in the store, before and after the lint change.
   On 4 October 2026, around 21:52 UTC, a default collection of `main` over
   the nine lab databases whose store was active left no plan with
   `is_parallel_plan = 1`, 41 to 213 plans per database and 0 to 2 serial
   plans with `max_dop > 1`. The fourth version withdrew the marker on that
   result. It held only because no lab database had a large maintenance log:
   the third panel's Claude reader built a `dbo.CommandLog` of 200 000 rows
   over 23 days and found the unhinted `INSERT INTO #sel` of
   `50.agent/050.commandlog.sql` stored at DOP 22, cost 5.39, inside the
   [5, 25) band. After `aeb4619`, a default collection left no parallel plan
   in any of the nine stores of the lab with that log in place. This version
   measured the same thing in a database of its own, `ZzAvgDop5A` (store in
   capture mode ALL, one-minute intervals; test 3's heaps; a `dbo.CommandLog`
   of Ola Hallengren's shape holding 200 000 rows over 23 days), dropped
   afterwards. Each run was `sql-auditor collect` with `DB_INCLUDE=ZzAvgDop5A`
   and nothing else set; before and after it, `SYSUTCDATETIME()` was read on
   the server; then, from `master` with three-part names, the runtime rows
   whose `last_execution_time` fell between the two instants were joined to
   `sys.query_store_plan`:

   | Binary | Options | Plans | `is_parallel_plan = 1` | serial, `max_dop > 1` |
   | --- | --- | --- | --- | --- |
   | `7984eef`, before the lint change | none | 220 | 1: `INSERT INTO #sel`, cost 5.63, DOP 22, 549 ms | 2 |
   | `e49dafb`, after it | none | 218 | 0 | 2 |
   | `e49dafb` | `--estimate-compression` | 224 | 0 | 3 |

   The first row is the positive control: the same fixture and the same
   reading found the defect, so the zeros below it are not a store that
   captured nothing. The marker measured by the third version, a `SET
   DATEFIRST 3` in the session, is in that version (`760b4bf`) and no longer
   here.
9. The replica groups of a store that has no availability group. In
   `ZzAvgDop3` and in `LABSTORE_A`, every runtime row carried
   `replica_group_id` 1, and `sys.query_store_replicas` held four rows, ids 1
   to 4 against `role_type` 1 to 4. `COL_LENGTH` found the column and
   `OBJECT_ID` the view. No availability group exists on the lab, so the rows
   of a secondary have not been seen.

Point 4 also concerns 042, which calls a statement parallel when
`sys.dm_exec_query_stats.max_dop > 1`. On the same lab, 25 statements of the
cache had `max_dop > 1`; 15 of them had no parallel operator in their plan
fragment (no `Parallel="1"`, the form the cache writes, measured on the
lab on 6 October 2026), carried 351 of the 379 executions and 7.1 of
the 14.3 seconds of "parallel" CPU, and were `INSERT ... EXEC` and
`INSERT INTO @t` statements, most of them sql-auditor's own collectors. It is
outside this document's scope and is listed under the open questions.

## What is read

The plans of the database's Query Store that are parallel (`is_parallel_plan =
1`) and executed inside the window by this replica's role; for each of them,
its estimated cost, read from `query_plan`, and over the window, from
`sys.query_store_runtime_stats`, its executions, its CPU and its degrees.

Serial plans are not read. The question the bands answer is what a higher
threshold would make serial, and only a parallel plan can change; a serial
plan's cost would cost one plan read each and inform nothing
`seuil_parallelisme.py` computes. The serial side is still reported as totals
over the window, taken from the runtime rows alone, which cost no plan read.

Execution types are summed, as in `029.query-store-load-profile.sql`: an
execution stopped by a timeout or an error used its CPU and its workers.

### The aggregation of the runtime rows

The window's runtime rows are aggregated once, per plan, into a temporary
table, `#runtime`: executions, CPU, the CPU and executions of the rows where
`max_dop > 1` and of the rows where `min_dop > 1`, the weighted sum of
`avg_dop`, the highest `max_dop`, and, joined once, `is_parallel_plan` and the
query's object type. The root's totals, the ranking and the bands all come
from that table, so they describe the same snapshot and no total can
disagree with the sum of the bands because a statement ran in between. This is
a grouping by plan, which 029 does not make (029 groups by interval).

The aggregation is one static statement, the same on every version from SQL
Server 2016. Only the rows of other replica roles go through
`sys.sp_executesql`, because the statement that finds them names a column
that does not exist before SQL Server 2022 ("Other replicas"); it stages them
into a second temporary table, `#other_rows`, which the aggregation leaves out
with `NOT EXISTS`. This is the corpus's existing way of reading a
version-dependent column (`20.databases/020.properties.sql`, `COL_LENGTH`
then `sp_executesql`), and a table variable would not be visible inside
`sp_executesql`. Ruled by the owner on 6 October 2026: the fifth version put
the whole aggregation inside the literal, which could not compile on SQL
Server 2016 to 2019 and gave those versions no other path; the
implementation plan found it, and two copies of the aggregation were refused.
`#runtime` is a temporary table and not a table variable for the second
reason as well. And
agy measured an insert of 500 000 aggregated rows at 1.70 s into a table
variable against 0.44 s into a temporary table; that measurement did not use
`MAXDOP 1`, which every statement of this file carries, so it is a reason and
not a figure for 043. The session reset between collectors drops `#runtime`
(dba-guide, "A collector's `#temp` tables do not outlive it").

### Other replicas

A Query Store follows its database, so what it holds was not necessarily run
on the instance being audited, and the output is meant to choose that
instance's threshold. Two cases are told apart.

- On an availability group secondary, the store is the primary's, replicated:
  its rows describe the primary's work, and, with Query Store for secondaries,
  the work of every secondary of the role together. 043 does not read it.
  The test is 050's: the database's `replica_id` joined to
  `sys.dm_hadr_availability_replica_states` with `is_local = 1` and `role =
  2`; a database whose `replica_id` is set and whose replica state cannot be
  read is treated as a secondary, as 050 does. `state.not_read_because`
  says `secondary` or `replica state unreadable`. Ruled by the owner on 6
  October 2026: the unreadable state is reported by that reason in the root
  only, with no `errors.*` key, so it does not mark the run partial (050
  reports through `errors.*`; 043's root keys are the list of "The root",
  which test 2 holds exactly).
- On a primary, or a database outside any availability group, from SQL Server
  2022, the runtime rows whose `replica_group_id` `sys.query_store_replicas`
  maps to a `role_type` other than 1 are left out of everything and counted in
  `excluded.other_replicas_executions` and `excluded.other_replicas_cpu_s`.
  The predicate is negative on purpose: a row whose group the view does not
  list is kept, so a mapping this document has not seen cannot empty the
  bands. Where the column exists and the view does not, nothing is left out
  and the two counts are NULL; before SQL Server 2022 both are NULL as well.
  0 therefore means "looked and found none", and NULL "could not look".
  Amended on 6 October 2026, with the plan's review: this said that SQL
  Server 2022 has the column and not the view; a 2022 CU26 container has both
  (`OBJECT_ID('sys.query_store_replicas')` not NULL, `excluded.*` 0), and
  which 2022 build first shipped the view is not known. The staging of the
  other-role rows and the aggregation are two statements, a few milliseconds
  apart, that do not read one snapshot: a row of another role recorded
  between them is kept (codex, reviewing the plan; not measurable without an
  availability group).
  Amended on 6 October 2026: the staging statement finds the other-role rows
  by semi-join on `sys.query_store_replicas`, a row counting only when its
  group is listed under a non-primary role and never under role 1, because the
  view can hold several rows per group and a join would repeat a runtime row.

What stays mixed: after a failover inside the window, the primary role's rows
from before it were run by the other instance. Nothing in the store dates a
failover; that is a limit of every Query Store collector of the corpus and is
listed under "The limits a reader must keep in view".

### Nested queries

Queries of scalar functions, multi-statement table-valued functions and
triggers (`sys.objects.type` `FN`, `TF`, `TR`, 029's classes) are kept in the
bands, because a parallel plan inside any of them is a plan the threshold acts
on. They are counted apart in the root (`nested.*`).

The totals of the root treat them by what point 7 measured, which is not 029's
rule. A scalar function's CPU is already in its caller, so `window.cpu_s`
leaves the `FN` queries out and `window.scalar_function_cpu_s` says how much
that was. A trigger's or a multi-statement function's CPU is in no caller, so
leaving it out would lose it, and those queries stay in `window.cpu_s`. The
parallel totals (`window.parallel_*`) are over every parallel plan, nested
ones included, because they are the denominator of the bands and the bands
include them. A function body that is parallel is not double counted inside
the bands: a scalar function that is not inlined makes its caller serial
(point 7's caller was stored serial), so the caller is not in a band.

### The audit's own statements

The collector runs inside the database it measures, after every per-database
collector that sorts before `80.workload/043`, and the store captures what
those collectors ran there; earlier audits within the window are in it too.
043 keeps all of them: nothing in the store tells the audit's statements from
the client's, and the marker that did was withdrawn on 5 October 2026 (see the
end of "Second review of 4 October 2026").

Whether they can reach the bands is decided by the contract lint, not by a
measurement. Since `aeb4619` it requires `OPTION (RECOMPILE, MAXDOP 1)` on
every statement that can read rows: a `SELECT` that returns rows or reads a
table (an assignment included), `SELECT ... INTO`, `INSERT ... SELECT`,
`UPDATE`, `DELETE` and `MERGE`, whatever their target, in the file and in
every literal it hands to `EXEC (...)` or `sys.sp_executesql`, which must be a
single literal. A subquery where no statement could carry the hint (`DECLARE
@x = (SELECT ...)`, `SET`, `IF EXISTS`, `WHILE`) is refused. The lint runs on
the embedded corpus in `TestEmbeddedCorpusIsValid` and on a `--queries-dir`
corpus before it runs. A statement compiled under `MAXDOP 1` gets a serial
plan, so whatever the client's data volume, a statement the lint sees cannot
put a parallel plan in the store. Point 8's measurement is the evidence that
the rule holds on the volume that broke the fourth version, not the
guarantee.

What the lint does not reach, and what each part can do:

- `INSERT ... EXEC`. The server refuses an `OPTION` clause on it. Its own
  plan was stored serial in every case measured (point 4; it is where the
  anomalous `max_dop` comes from). What it executes is a literal, linted as
  above when it holds a statement and otherwise a read-only `DBCC` command, of
  which none appeared in the stores of point 8; or one of the four procedures
  the statement lint allows besides `sp_executesql`. Three of them cannot
  touch a store 043 reads: `sp_readerrorlog` and `sp_enumerrorlogs` read
  files, `msdb.dbo.sp_help_jobhistory` compiles in `msdb`, and all three run
  from instance-scoped collectors, while 043 reads user databases only
  (`SelectTargets` leaves `master` and `msdb` out unless a file declares
  `@widened`, and 043 does not). The fourth,
  `sys.sp_estimate_data_compression_savings`, runs per database in
  `70.schema/041.compression-savings.sql`, which only runs under
  `--estimate-compression` (or `--all`). Its internal copy of a sample into
  `#sample_table...` is a statement nobody can hint, and the store keeps it:
  measured serial at cost 37.2 on a 2 000 000-row heap of 108-byte rows and
  38.1 on a 6 000 000-row heap of `bigint`, with no `NonParallelPlanReason`
  in either plan. Its seriality is therefore the optimizer's choice, not a
  rule. Its cost follows the sample, which the procedure caps at 5 000 pages
  per partition (`@pages_to_sample` in its text on this build), so it should
  stay of that order; if it ever compiles parallel, one plan per sampled
  partition enters a band. This is the one path by which a supported run of
  the shipped corpus can put a parallel plan of its own in the bands.
- Functions in an expression (`OBJECT_ID`, `COL_LENGTH`, `HAS_PERMS_BY_NAME`
  and the like), in an `IF` or a `DECLARE`. They read no table through an
  operator the optimizer can run in parallel.
- An XML method on a variable in a `DECLARE` or a `SET`, such as `DECLARE @v
  bigint = @x.value(...)`. It reads a variable, not a table, but it compiles a
  plan the store keeps, and that plan can be costly: measured at 209 on a
  variable of 20 000 elements, stored serial with no `NonParallelPlanReason`.
  No collector of the corpus uses the form today; one that did would be
  outside the lint, and writing it as `SELECT @v = @x.value(...) OPTION
  (RECOMPILE, MAXDOP 1)` brings it inside.
- A statement that opens with a parenthesis. The scanner does not see one
  open, so a statement left without its `;` before it is credited with its
  hint. Measured: `SELECT @n = COUNT_BIG(*) FROM dbo.Ladder7 WHERE id % 7 = 2`
  followed by `(SELECT TOP (0) 1 AS x) EXCEPT (SELECT 1) OPTION (RECOMPILE,
  MAXDOP 1);` passes the lint, and the assignment is stored at DOP 22. The
  parenthesised statement returns a result set nobody declared, so the runner
  refuses the collector at execution ("query returned more result sets than
  the 1 declared"), but only after the parallel statement has run. Such a file
  fails on every run and could not ship unnoticed; closing the form in the
  scanner is a matter for the lint ("Open questions").
- Automatic statistics. A collector's read can make the engine create a
  statistic on a client table: after the collections of point 8, `dbo.CommandLog`
  carried an automatically created statistic on `StartTime`. Its `StatMan`
  query was not in the store there, but point 5 found such queries stored as
  parallel plans with no statement cost. If one is captured, it lands in the
  unknown band (`unknown.no_cost`) and in `window.parallel_*`, never in a
  costed band.
- A Query Store hint. Learn says a Query Store hint overrides a hint written
  in the statement, and it does: with `sys.sp_query_store_set_hints`
  attaching `OPTION (MAXDOP 4)` to a query written with `OPTION (RECOMPILE,
  MAXDOP 1)`, the next execution was stored as a parallel plan at DOP 4. A
  client who sets such a hint on one of the audit's queries makes it
  parallel, and nothing in a collector can prevent it. A forced plan cannot do
  the same, since none of the audit's queries has a parallel plan to force.

The serial plans the audit leaves with `max_dop > 1`, two or three per
database in the runs of point 8, are not banded either, since the selection
reads `is_parallel_plan = 1` and never `max_dop` (point 4). So the bands,
`window.parallel_*` and `nested.*` describe the workload, apart from the cases
above.

The audit does reach what the root counts over every query:
`window.runtime_rows`, `window.executions`, `window.cpu_s`, and
`window.serial_plans_dop_above_1`, which counts the audit's anomalies with the
workload's; and the intervals. The current run is usually the window's last
activity, so `window.newest_interval` is often the interval the audit ran in,
043's own included; and each audit in the window adds the intervals it ran in
to `window.intervals`. On a store that is otherwise quiet, scheduled audits
can make a thin history look fuller and a stopped workload look recent (codex
directive). What one audit adds to `window.cpu_s` was not measured; its
statements being serial, it should be of the order of the time its collectors
spent in the database, against seven days of the client's work, and each
earlier audit in the window adds its own.

The root says nothing about it, since any column would be a guess. The file's
header says it, for whoever reads the output beside the SQL: "The audit's own
statements are in the store, in the serial totals (`window.executions`,
`window.cpu_s`, `window.serial_plans_dop_above_1`) and in the intervals
(`window.intervals`, `window.newest_interval`). Every one of them that reads
rows carries `OPTION (RECOMPILE, MAXDOP 1)`, which the contract lint enforces,
so they do not reach a band, unless a Query Store hint set on one of them
overrides it, or the sample copy that `sp_estimate_data_compression_savings`
runs under `--estimate-compression` compiles parallel." The analysis reads the
bands and `window.parallel_cpu_s`, which those exceptions alone can touch, and
reads the intervals knowing that they include the audit ("What the analysis
does with it").

### How the cost is found

No plan is converted to xml. The cost is found as 042 finds it: the first
`StatementSubTreeCost="` of the text, read up to the next double quote, cast
with `TRY_CAST(... AS float)`, which reads a period whatever the session's
language (agy). Points 1 and 2 are the evidence that this is the query's own
cost.

The text is first copied into a table variable column, one chunk at a time,
and searched there. Measured on `LABSTORE_A` (927 plans, 71.5 MB at the time
of the first draft), twice each:

| Form | Seconds | ms per MB |
| --- | --- | --- |
| `CHARINDEX` on `sys.query_store_plan.query_plan` directly | 4.08 to 4.13 | 57 |
| the same with `COLLATE Latin1_General_BIN2` | 3.66 to 3.69 | 51 |
| `TRY_CAST` to xml into a column, then `.value()` | 3.17 + 0.08, 3.14 + 0.03 | 44 |
| copy into `nvarchar(max)` column, then `CHARINDEX` on it | 0.72 to 0.77, then 0.82 to 0.84 | 22 |

And on `LABSTORE_B` (126 plans, 24.0 MB, 190 KB on average): copy 0.25 to
0.37 s, search 0.19 to 0.20 s, 19 to 24 ms per MB; the cast into an xml
column took 0.89 to 0.91 s on its own, 37 ms per MB. Claude reproduced the
staged form on the same store grown to 955 plans: copy 845 and 771 ms, search
491 and 479 ms, the same cost on 954 of 954 plans as the direct search.

Every reference to `sys.query_store_plan.query_plan` decompresses the plan
again, which is why the direct search, with its three references, is slower
than a conversion. `DATALENGTH(query_plan)` on the view is such a reference:
over the 955 plans of `LABSTORE_A` it took 1 182 ms, where copying the same
plans took 751 ms and `DATALENGTH` on the copied column 4 ms (agy found the
decompression; the second version and Claude measured it). So the bytes are
counted on the staged column, after the copy, and never on the view.

### The selection

The parallel plans of the window are ranked by their parallel CPU in the
window (the CPU of their runtime rows where `max_dop > 1`), highest first,
then by plan id, and the first `@cap + 1` are pinned into a table variable
with their rank `rn`, before any `query_plan` is read,
as 027 does: `TOP (@cap + 1)`, then the evidence that the cap bit is a count
above `@cap`, then the extra row is deleted and never read. The rank by
parallel CPU keeps, when a cap or a budget cuts, the plans that carry the work
a threshold could make serial; a parallel plan that only ever ran at DOP 1 in
the window ranks last, and making it serial would change nothing.

The root then says what share of the window's parallel CPU the bands hold
(`examined.share_of_parallel_cpu_pct`), computed from the bands themselves:
the sum of the seven bands' `parallel_cpu_s` over `window.parallel_cpu_s`,
NULL when that is 0. 042 noted that the band a decision turns on, [5, 25), is
made of cheap statements that a ranking by CPU reaches last; here the ranking
is over parallel plans only, which are few: 12 of 24 plans in `ZzAvgDop`, 3
of 622 in the window of `LABSTORE_A`.

### The window

`window.from` is `DATEADD(day, -@window_days, SYSDATETIMEOFFSET())`. An
interval is in the window when it ends after `window.from` (`end_time >
window.from`), so the interval that straddles the boundary is kept whole
rather than lost whole (codex, both prompts, first panel). The window can
therefore reach back by up to one interval length before `window.from`, a day
at most, and `window.oldest_interval` says how far it did.

Whether the store held seven days is a separate question, and the root gives
evidence rather than proof. `window.store_oldest_interval` is the start of the
oldest interval the store holds at all: history is short when it is later than
`window.from`. When it is earlier, the store reaches back that far, but a store
that spent part of the week OFF or READ_ONLY keeps its older intervals and has
a gap (codex, both prompts). `window.intervals` counts the distinct intervals
of the window that hold a runtime row; against `window.days` and
`state.interval_minutes` it shows a gap, and an idle night looks the same, so
it is a figure for the reader and not a verdict. An hourly store collected at
21:43, whose first interval in the window starts at 21:00 seven days earlier,
is not short.

The window is seven days because a threshold is a property of the workload as
it runs now, and a week holds its weekly cycle (the weekend batch, the Monday
peak) without reaching back past a change of threshold or of MAXDOP made a
fortnight ago. It is not `QUERY_STORE_DAYS`, which configures the extraction
of `--query-store-detail`; tying the two would make a setting chosen for one
change the other.

## The constants and the stop rules

All five are constants declared at the head of the file and projected in the
root, following 027 ("THE CAP AND THE BUDGET ARE CONSTANTS AND NOT OPTIONS").
Tests change them by rewriting the declaration in a copy of the file's text,
as a `--queries-dir` corpus would.

| Constant | Value | Bounds |
| --- | --- | --- |
| `@window_days` | 7 | runtime intervals ending within 7 days of `SYSDATETIMEOFFSET()` |
| `@cap` | 1 000 | parallel plans pinned per database |
| `@chunk` | 100 | plans copied per statement of the read loop |
| `@budget_bytes` | 104 857 600 | bytes of plan text read per database (100 MB) |
| `@budget_ms` | 10 000 | ms of read loop per database |

The work has two parts. The selection (the aggregation into `#runtime`, the
ranking and the pin) reads runtime rows and no plan text, and has no budget of
its own: its cost grows with the runtime rows of the window, which the root
reports (`window.runtime_rows`, `selection.duration_ms`), and only `@timeout`
bounds it (codex, both prompts; agy directive). The read loop is what the
budgets limit.

The read loop takes the pinned plans in rank order, `@chunk` at a time, by
`rn >= @lo AND rn < @lo + @chunk`. Before each chunk it checks, in this order:

1. every pinned plan has been reached: the loop ends;
2. `@budget_ms` or more have passed since the loop began: it stops, and
   `examined.stopped_by` is `time`;
3. `@bytes_read >= @budget_bytes`: it stops, and `examined.stopped_by` is
   `bytes`.

A chunk once started is read whole. Both budgets are therefore checked
between chunks and can be passed by one chunk. For the bytes that is up to
`@chunk` plans of any size: the file does not know a plan's size before
copying it, since asking the view decompresses the plan a second time (027
pays that price to size first; this file does not). On `LABSTORE_A` a hundred
plans averaged 7.7 MB and the largest plan was 0.69 MB, so a chunk there
weighs 69 MB at most; on a store of multi-megabyte plans, which the ranking by
parallel CPU puts first, one chunk can weigh hundreds of megabytes, all
copied into tempdb before the first check (Claude, codex neutral). The root
reports `examined.largest_plan_bytes`, taken on the staged column, so the
magnitude on the client is in the archive. A smaller chunk was measured and
not taken: the copy and search of the 955 plans of `LABSTORE_A` took 2.0 and
2.3 s by chunks of 100, 4.2 and 5.0 s by chunks of 10, and 16.4 and 17.1 s
plan by plan, because each statement pays a fixed price to reach the store's
plans whatever it copies. With either budget at 0 the loop reads nothing,
which is what test 5 uses. The bound that does not move is the file's
`@timeout`; the budgets exist so that a slow instance gets a partial, counted
answer before it is reached, instead of the nothing an expiry returns.

When the loop ends with every pinned plan reached, `examined.stopped_by` is
`cap` if the pin found `@cap + 1` eligible plans (027's evidence rule, not an
equality), and NULL otherwise; the extra pinned plan is never read. A budget
that stopped the loop is named even when the cap also bit, because it is the
one that left pinned plans unread. `truncated` is 1 when `examined.stopped_by`
is not NULL.

The cap is 042's window, 1 000. The byte budget is half of 027's 200 MB,
because the price per MB here is a fifth of 027's (about 20 ms against 94 to
112 ms): 100 MB costs about two seconds on the lab and eight at four times
that rate. The time budget is 10 s and not the first draft's 30 s because the
collector runs on every default run: 10 s is five times the lab's price for
the whole byte budget. It does not change what `check` prints: the planned
duration's ceiling is computed from `@timeout`, 120 s per database, and the
budget is what makes the actual duration far lower (agy directive found the
second version's "33 minutes on 200 databases" read as if it were `check`'s
figure). `@chunk` is 100 for 027's reason: each statement that reads
`sys.query_store_plan` holds a shared QDS lock on the database, and a chunk
releases it, so an option change on the store waits for one chunk and not for
the whole read. A literal `TOP (100)` would break
`TestWorkloadCapsAreDeclaredAppliedAndReported`'s rule against literal caps,
which is the other reason the chunk is a variable.

## Running by default: what changes in the tree

- `queries/80.workload/043.query-store-parallel-cost.sql`, the collector.
  `testdata/corpus.txt` gains its line, with no `@profiles`: the one profile,
  `space`, is about what makes databases larger, and this is not. A run with
  `--profile space` leaves it out.
- `workload_caps_test.go`: the table learns the form a cap is applied in.
  Today every row counts `TOP (@variable)`; 043's pin is `TOP (@cap + 1)` and
  its chunk is a range of ranks, which that count cannot see (Claude ran it:
  with the existing check, the only count that passes is 0, which asserts
  nothing; codex directive by reading). Each row gains the exact strings it
  must find and how many times: for 043, `TOP (@cap + 1)` once, `rn < @lo +
  @chunk` once and `SET @lo = @lo + @chunk` once, with `@cap` projected as
  `cap` and `@chunk` as `chunk`; the existing rows keep `TOP (@variable)` with
  their current counts. 027, which has the same shape and is not in the table
  today for that reason, can join it with the same forms; that is optional and
  not part of this change.
- `docs/dba-guide.md`: the @timeout table's `120 s` row goes from 29 to 30
  (`TestTheGuideCountsTheTimeoutTiersOfTheCorpus` fails until it does); the
  row of "What the default run costs a large instance" given in "The cost,
  measured"; and the illustrative `check` listing may gain
  `80.workload/043.query-store-parallel-cost.sql per database, SQL Server 13+`.
- `docs/caps-inventory.md`: a row for 043's `@cap` and its two budgets.

What does not change, and why:

- No flag: `KnownFlags`, `CostFlags`, `main.go`'s help text ("all eleven
  options", "the two that are off for cost"), the README's option tables, the
  wizard's third screen and its `TestTheWizardOffersEveryOptInTheCommandLineHas`
  stay as they are. `--all` has nothing to turn on.
- `check` needs no new code. It lists 043 among the default per-database
  collectors, and the whole-plan ceiling of its duration lines grows by 120 s
  per database; the costly line, which counts only `CostFlags` collectors, is
  unchanged, and so is `TestPlannedDurationLines`, whose fixture is synthetic.
- `collect/` does not change: the session the runner gives a collector is
  the one it gives today, with no setting added to the reset batch and no new
  warning in `MANIFEST.txt`.
- `QUERY_STORE_DB_INCLUDE` does not narrow 043. Today it narrows the writers
  (021, 022) and 025, the extraction it is documented for ("Narrows which of
  the collected databases the extraction reads"), and none of the default
  Query Store collectors, 020 to 029, whose cost per database is higher than
  this one's (027 up to 20 s). `queryStoreUnits` keeps its condition. That is
  also why `.env.example` is left alone: its Query Store section says the keys
  "only matter with --query-store-detail", which stays true, and naming 043
  there would tell the operator that `QUERY_STORE_DAYS`, `FROM`, `TO` and `TOP`
  shape it, which they do not (Claude). `DB_INCLUDE` and `DB_EXCLUDE` narrow
  it as they narrow every per-database collector.
- No `@discloses`, so `MANIFEST.txt`'s text paragraphs and
  `TestManifestListsTheTextTheDefaultRunCaptures` do not move.
- The other default collectors that read the store's queries and runtime
  rows, `80.workload/` 020, 023, 024, 026, 027, 028 and 029, count the audit's
  statements as 043 does (027 is in the default run and in the `space`
  profile); `20.databases/022.query-store.sql` reads the store's options and
  size, not its queries. 021, under `--query-store-detail`, and 025, under
  `--query-store-compare-at`, extract them. 043 introduces no difference on
  that point between its totals and 029's.

## The guarantee that no text leaves

This is what allows the default, so it is stated as a contract and tested
rather than inferred from the column list.

What leaves the server is the database name (already in every per-database
document), the store's state and settings, timestamps of intervals, counts,
sums, degrees, sizes in bytes, the band boundaries and the stop reason. No
query text, no plan, no query id, plan id, query hash or plan hash, no object
id or name, no replica name. The plan text is read on the instance and
dropped with its chunk, as 042 does with the cache. The replica groups are
read to exclude rows, not projected.

The test (test 2) is on the output, not on the SQL: the set of keys of the
root and of a band row must equal the lists of "The collector", and a query
planted with a distinctive literal must not appear in the document. Column
names are not a test of this: the first draft's test forbade `plan` in a
name, which `examined.plans_read` and five other counts would have failed
(Claude, codex directive). The existing choke point, which latches a
disclosure when a payload carries a Showplan root element and warns when the
script has no `@requires_flag`, stays the last line of defence for a plan
emitted by mistake.

## The collector

`queries/80.workload/043.query-store-parallel-cost.sql`, beside 042 whose
reading it repeats on another source; the 02x range of the Query Store
collectors is full.

```
-- @scope:       database
-- @resultsets:  root:object, bands:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     120
-- @min_version: 13
```

The permissions are 029's, which reads the same views and `sys.objects` for
the same classes of nested queries; `sys.query_store_replicas`,
`sys.dm_hadr_availability_replica_states` and `sys.dm_os_sys_info` need
nothing more (see the permission chain above, and
050, which reads the replica states under the same line). The floor is SQL
Server 2016, where every column read outside the `sp_executesql` branch
exists; it has not been measured on 2016.

Every statement of the file that reads rows carries `OPTION (RECOMPILE,
MAXDOP 1)`: the two result sets, the aggregation, the statement inside the
`sp_executesql` literal, the pin, the `DELETE` of the extra pinned row, each chunk's copy and
search, and every assignment that reads a table. The lint of `aeb4619`
requires it and also shapes the file. The replica state is read by an
assignment (`SELECT @role = ... OPTION (RECOMPILE, MAXDOP 1)`) that an `IF`
then tests, never by `IF EXISTS (SELECT ...)`, and no `DECLARE` or `SET` holds
a subquery. The literal handed to `sys.sp_executesql` holds the one
statement that stages the other replica roles' rows, with its own hint, and
fills `#other_rows` from inside, so no `INSERT ... EXEC` is needed (amended
on 6 October 2026, "The aggregation of the runtime rows"). A cost found in a chunk is inserted into a table
variable of its own rather than written back with an `UPDATE` through an
alias, since the statement lint wants an `UPDATE`'s target named as `@t` or
`#t`. A stub written from this document with those forms lints clean under
`lint()` with `@resultsets: root:object, bands:array`, and each of the
following makes it refuse the file: the `DELETE` or the pin without its hint,
the literal's statement without its hint, `DECLARE @n int = (SELECT COUNT(*)
FROM @pinned)`, `IF EXISTS (SELECT ...)` for the replica test, and a third
hinted `SELECT` that returns rows. A hinted `DELETE` no longer counts as a
result set, which under the fourth version's lint it did (Claude, third
panel).

The root comes from a `LEFT JOIN` from `sys.databases` to
`sys.database_query_store_options`, as in 026 and 029, so a database with no
row in the options view still returns its row. The store is read unless its
`actual_state_desc` is `OFF`, the database has no row there, or the database
is an availability group secondary ("Other replicas"); that is a predicate in
the file (point 6): 021 reports the state and leaves the decision to its
writer, and 043 has no writer. A store in `ERROR` is read like a `READ_ONLY`
one; neither state has been measured here. When the store is not read, the
counts of the root are NULL and `state.not_read_because` says why; a store
that is read and empty gives 0, the convention of 026 and 029, so that a
reader can tell the two apart. The bands are seven rows in every case.

### The root

| Column | Meaning |
| --- | --- |
| `database` | `DB_NAME()` |
| `collected_at` | `SYSDATETIMEOFFSET()` |
| `state.actual`, `state.capture_mode`, `state.interval_minutes` | from `sys.database_query_store_options`, NULL when it has no row |
| `state.not_read_because` | NULL when read; otherwise `off`, `no store`, `secondary` or `replica state unreadable` |
| `schedulers` | `scheduler_count` of `sys.dm_os_sys_info` |
| `window.days`, `window.from` | `@window_days`, and the instant the window starts |
| `window.oldest_interval`, `window.newest_interval` | the start of the oldest and the end of the newest interval read |
| `window.store_oldest_interval` | the start of the oldest interval the store holds |
| `window.intervals` | distinct intervals of the window holding a runtime row |
| `window.runtime_rows` | runtime rows of the window the aggregation read, before any exclusion |
| `window.executions`, `window.cpu_s` | every query, the audit's own included ("The audit's own statements"), except scalar function queries |
| `window.scalar_function_cpu_s` | the CPU of the scalar function queries left out of `window.cpu_s` |
| `window.parallel_plans` | plans with `is_parallel_plan = 1` executed in the window, nested ones included |
| `window.parallel_plan_cpu_s` | all their CPU, at any degree |
| `window.parallel_executions`, `window.parallel_cpu_s` | their runtime rows where `max_dop > 1`: the denominator of the bands' `parallel_*` |
| `window.parallel_executions_min`, `window.parallel_cpu_s_min` | their runtime rows where `min_dop > 1` |
| `window.serial_plans_dop_above_1` | plans with `is_parallel_plan = 0` and a runtime row with `max_dop > 1` (point 4), the audit's included, counted and never banded |
| `window.dop_above_schedulers` | runtime rows of parallel plans with `max_dop` above `schedulers` (the documented anomaly), counted and kept |
| `nested.parallel_plans`, `nested.parallel_cpu_s` | parallel plans of `FN`, `TF` and `TR` queries, kept in the bands, and their CPU where `max_dop > 1` |
| `excluded.other_replicas_executions`, `excluded.other_replicas_cpu_s` | runtime rows of other replica roles, left out of everything above; NULL where they cannot be told apart |
| `cap`, `chunk`, `budget.bytes`, `budget.ms` | the constants |
| `selection.duration_ms` | the time of the aggregation, the ranking and the pin |
| `examined.plans` | plans pinned and eligible to be read, at most `cap` |
| `examined.plans_read` | plans whose text was copied |
| `examined.bytes_read`, `examined.duration_ms` | the read loop's bytes, counted on the staged column, and its time |
| `examined.largest_plan_bytes` | the largest plan copied, on the staged column |
| `examined.share_of_parallel_cpu_pct` | the bands' summed `parallel_cpu_s` over `window.parallel_cpu_s`; NULL when that is 0 |
| `examined.stopped_by` | `cap`, `bytes`, `time`, or NULL |
| `truncated` | 1 when `examined.stopped_by` is not NULL |
| `unknown.no_plan`, `unknown.no_cost` | why the unknown band is unknown: the plan left the store between the pin and its chunk, or its text holds no statement cost |

`examined.share_of_parallel_cpu_pct` is NULL rather than a division by zero
because a store with no parallel CPU in the window is common: the measured
`LABSTORE_A` had none on the day of the second panel, and a store whose
parallel plans all ran at DOP 1 (point 4's converse) has none either. A
division by zero would raise error 8134 and lose the database's whole root
(Claude). It is computed from the bands so that it cannot disagree with them:
the second version computed it from the plans read, while the unknown band
also holds plans that left the store before their chunk, and the two would
then differ (codex neutral).

### The bands

Seven rows always, empty ones included, with 042's boundaries and 042's column
names, so that `seuil_parallelisme.py` reads a band of either file with the
same functions (`tranches`, `serialisable`, `inconnue`, which codex ran on a
band of this shape): `band`, `cost_from`, `cost_to`, `statements`,
`parallel_statements`, `executions`, `parallel_executions`, `cpu_s`,
`parallel_cpu_s`, `max_dop`. Their meaning here:

| Column | Meaning in this file |
| --- | --- |
| `statements` | parallel plans in this band (a plan, not a cache entry: a query with two parallel plans counts twice, each in the band of its own cost) |
| `parallel_statements` | those of them with at least one runtime row in the window where `max_dop > 1` |
| `executions`, `cpu_s` | their executions and CPU in the window, at any degree |
| `parallel_executions`, `parallel_cpu_s` | executions and CPU of their runtime rows where `max_dop > 1`: an upper bound, as in 042 |
| `max_dop` | the highest `max_dop` of the band |

and three columns 042 cannot have:

| Column | Meaning |
| --- | --- |
| `parallel_executions_min`, `parallel_cpu_s_min` | the same over the rows where `min_dop > 1`: every execution of such a row ran parallel, so this is a lower bound |
| `avg_dop` | `SUM(avg_dop * count_executions) / NULLIF(SUM(count_executions), 0)` over the band's rows where `max_dop > 1`, as `float` rounded to two decimals; its weight is therefore the band's `parallel_executions` |

`avg_dop` is a `float` and not the second version's `decimal(9,2)`, because
the documented anomaly has no stated bound and the bands keep it on purpose: a
weighted average above 9 999 999.99 would raise error 8115 and lose the band
result set for the database (Claude). A `float` holds any value the `bigint`
DOP columns can carry.

The two bounds are the point of reading the store rather than the cache. A
runtime row is one plan, one interval of a minute to a day, one execution type;
when `min_dop` and `max_dop` straddle 1 in a row, the store does not say how
many of its executions ran parallel, and the truth lies between the two
columns. 042 can only give the upper one.

The unknown band holds the plans whose chunk the loop reached and whose cost
was not found: those whose text holds no statement cost, and those that left
the store between the pin and their chunk; their runtime figures come from
`#runtime`, so they still count. Pinned plans left unread by a budget are in
no band. The bands therefore hold every plan the loop reached, and
`examined.share_of_parallel_cpu_pct` is their share by construction.

### What the analysis does with it

Out of this repository's scope, stated so the reviewers can check the shape.
`seuil_parallelisme.py` gains a reading of 043 beside 042. Its band functions
are shared; its root reader is not: `lire` reads 042's root (`examined.cap`,
`examined.statements`, `cache.*`), and pointed at 043 it would read 0 and
None without an error (agy directive). 043 gets a reader of its own, written
against the keys of "The root", and `lire` refuses a document that lacks
`examined.statements` rather than print zeros, so that the script as it is
today cannot be pointed at 043 by mistake (DeepSeek neutral).

Over the databases of the archive, it sums each band's counts and CPU, which
share boundaries and add exactly; it takes the maximum of `max_dop`; and it
re-weights `avg_dop` by `parallel_executions`, since an average cannot be
summed (Claude). Its total is `window.parallel_cpu_s` summed over the same
databases, and its coverage is the sum of the bands' `parallel_cpu_s` over
that total.

It qualifies a database rather than rejecting it on `truncated`: a store
whose cap bit holds more than a thousand parallel plans, and the thousand
read carry the share the root states (agy neutral, first panel). It says, per
database, when the store was not read and why, when the history is short
(`window.store_oldest_interval` later than `window.from`) or thin
(`window.intervals`), when other replicas' rows were left out, and when a
budget stopped the read; and it prints both sources side by side. It treats
`window.intervals` and `window.newest_interval` as including the audit's own
activity ("The audit's own statements"): a full-looking count of intervals,
or a newest interval at the time of the collection, is not evidence that the
client's workload ran recently. Its decision rests on the bands and on
`window.parallel_cpu_s`, which the audit does not reach outside the cases that
section names; when `_run.json` shows `estimate_compression` on, it says that
the sample copies of `sp_estimate_data_compression_savings` may be in the
bands. Neither
replaces the other: the cache covers databases whose store is off and
statements of `master`, the store covers what the cache evicted.

## The limits a reader must keep in view

- Only databases whose store is not off, and only what it captured. Under
  capture mode AUTO (the default from SQL Server 2019) a query is captured once
  it passes the store's thresholds of executions or CPU, so a cheap and rare
  parallel query is missing; a cheap and frequent one, the kind the [5, 25)
  band is about, passes them.
- The cost is the parallel plan's, lower than the serial cost the optimizer
  compared with the threshold, as 042 documents: the counts under a candidate
  stay upper bounds of what it would make serial.
- A plan compiled under an earlier threshold and still executed is in the
  window with its old cost. The window shortens the exposure; it does not
  remove it.
- Statements that ran in `master` or in a database outside the selection are
  not counted, while 042 sees the whole instance. The two files describe
  different sets and must not be added together.
- `avg_dop` inherits the documented anomaly. The rows above the scheduler
  count are counted in the root so the reader can tell whether it touched the
  bands.
- The audit's own statements, this run's and those of earlier audits in the
  window, are in the serial totals, in `window.serial_plans_dop_above_1` and
  in the intervals. That they stay out of the bands rests on the contract
  lint, which holds every statement that reads rows to `MAXDOP 1`, in the
  embedded corpus and in a `--queries-dir` one alike. It does not hold where
  the lint cannot reach ("The audit's own statements"): a Query Store hint a
  client set on an audit query, the sample copy of
  `sp_estimate_data_compression_savings` under `--estimate-compression` (the
  run's options are in `_run.json`; an earlier audit's in the window are not),
  and a statement that hides behind a parenthesis, which the runner refuses
  only after it ran.
- The store follows its database across a failover: rows of the primary role
  from before a failover inside the window were run by the other instance,
  under its own threshold and scheduler count. An availability group secondary
  is not read at all.

## The cost, measured

On the lab, 4 October 2026, a prototype of the selection and the read loop
(not the final file: it pins on `is_parallel_plan = 1 OR max_dop > 1`), run
twice per database, the first run cold:

| Store | Plans in window | Pinned | MB read | Selection | Read loop |
| --- | --- | --- | --- | --- | --- |
| `ZzAvgDop` | 24 | 12 | 0.10 | 49 to 62 ms | 16 to 28 ms |
| `ZzAvgDop2` | 41 | 7 | 0.08 | 41 to 99 ms | 16 to 28 ms |
| `LABSTORE_A` | 650 | 3 | 0.05 | 45 to 105 ms | 28 to 37 ms |
| four other lab stores | 95 to 116 | 0 to 4 | up to 0.07 | 29 to 95 ms | 0 to 64 ms |

So 29 to 148 ms per database on the lab, selection and read loop together.
Claude measured the third version's aggregation (runtime rows joined to the
interval, the plan, the query, the context and `sys.objects`, grouped by plan,
`MAXDOP 1`; this version drops the join to the context) at 89 to 90 ms on
`LABSTORE_A`'s 9 086 runtime rows, and 81 to 139 ms on a store of its own.
What a lab cannot say is the number of parallel plans a client store holds in
a week, which decides whether the cap or a budget is
reached, nor the size of the runtime view on a busy store, which decides the
selection's price; `selection.duration_ms`, `window.runtime_rows`,
`examined.duration_ms`, `examined.bytes_read`, `examined.largest_plan_bytes`
and `examined.stopped_by` put the client's own figures in every archive. The
replica filter and the `sp_executesql` around the aggregation have not been
timed; they change the statement's text, not the rows it reads.

The final file, measured on 6 October 2026 after the plan's review, on the
same build: `selection.duration_ms` was 144 to 221 ms on a store of 13 runtime
rows (the first run cold) and 242 to 253 ms on a store of 6 851, four or five
runs each, with the read loop at 57 to 75 ms for 13 plans. The selection
costs more than the prototype's, and most of it is compilation: every
statement compiles under `RECOMPILE`, a part paid by every database whatever
its store holds (Claude, reviewing the plan, measured the compilations by
`SET STATISTICS TIME`: 78 ms for the aggregation, 39 for the staging
literal). The row below gives the final file's figures.

The row that "What the default run costs a large instance" in
`docs/dba-guide.md` gains, as it is to be written there:

| What | Where | What it actually does |
| --- | --- | --- |
| Parallel plan costs read out of the Query Store | `80.workload/043.query-store-parallel-cost.sql` | in each database whose store is not off, and which is not an availability group secondary, two parts. First one aggregation of the last seven days of `sys.query_store_runtime_stats`, grouped by plan into a temporary table; it reads no plan text and has no budget of its own, so its cost grows with the number of runtime rows in the window, over a fixed part that compiling its statements under `RECOMPILE` costs every database: on one 2025 lab build, 144 to 221 ms on a store of 13 runtime rows and 242 to 253 ms on one of 6 851. Then the text of up to 1 000 parallel plans, those with the most parallel CPU first, copied a hundred at a time into a table variable and searched for one attribute, never converted to `xml`: about 20 ms per MB of plan text on one 2025 lab build. The read stops before the next hundred once 100 MB have been read or 10 s have passed, so one hundred plans can pass either, by as much as their size; the 120-second timeout is the hard bound of the whole file. The store's shared lock is released between chunks. The root projects `selection.duration_ms`, `window.runtime_rows`, `examined.duration_ms`, `examined.bytes_read`, `examined.largest_plan_bytes` and `examined.stopped_by`, so the cost on your instance is in the archive. Nothing that names a query leaves the server. |

## What is not in scope

- Correcting 042 for point 4. It is a finding about 042, recorded here
  because this measurement found it.
- Teaching the replica filter to the other Query Store collectors (020 to
  029). They read the secondary's copy of the primary's store and, with Query
  Store for secondaries, mix the roles. That is true today, before 043.
- Correcting 020 and 029, which leave trigger and multi-statement function
  queries out of their totals on the assumption that their CPU is in the
  caller (point 7).
- Serial plans' costs, and any band of serial work.
- A per-collector parameter directive. The constants stay constants.
- Any judgement: no candidate threshold appears in the file.

## Tests

Each test names the change to the future code that must make it fail. A test
whose mutation passes tests nothing, and is to be rewritten, not kept.

1. Corpus lint and caps. `testdata/corpus.txt` lists the new path with no
   profile; the directive lines are exactly the five above, with no
   `@requires_flag`, no `@discloses` and no `@profiles`; the file lints clean,
   contract included. `TestWorkloadCapsAreDeclaredAppliedAndReported`, with
   the forms of "Running by default", finds `TOP (@cap + 1)`, `rn < @lo +
   @chunk` and `SET @lo = @lo + @chunk` once each, and the projections `cap`
   and `chunk`. Mutations: pinning with `TOP (@cap)` or a literal `TOP
   (1001)`; walking the loop with any other variable than `@chunk`; removing
   the hint from the `DELETE` of the extra pinned row, which the contract lint
   refuses (measured on a stub, "The collector"). A static count cannot show
   that the loop reads by the chunk it declares; test 5 does.
2. No text leaves. Live, on the database of test 3, whose workload holds the
   planted query `SELECT COUNT_BIG(*) FROM dbo.Ladder6 WHERE pad <>
   'ZZ043_PLANTED_TEXT' OPTION (MAXDOP 2);`. The test first asserts that
   `sys.query_store_query_text` holds the literal in the text of a query
   whose plan has `is_parallel_plan = 1`, and fails otherwise, since a literal
   the store never kept cannot leak. The `OPTION` clause is what keeps it
   there: written bare on Ladder7, the query was stored by simple
   parameterization as `(@1 varchar(8000))SELECT COUNT_BIG(*) FROM [dbo].[Ladder7] WHERE
   [pad]<>@1`, literal gone, while the hinted form kept it, at DOP 2 and cost
   11.56 (codex neutral). Then the keys of the root and of every band row
   equal the lists of "The collector" exactly, and the literal appears
   nowhere in the document. Mutation: adding `p.plan_id` to the projection.
3. The bands, live, on the lab. A test creates a database with the store in
   capture mode ALL and one-minute intervals. From a connection opened with
   `sql.Open`, it creates seven heaps `dbo.Ladder1` to `dbo.Ladder7` (`id
   bigint`, `pad char(100)`) of 20 000, 50 000, 100 000, 200 000, 500 000,
   1 000 000 and 2 000 000 rows, filled by `INSERT ... WITH (TABLOCK)
   SELECT TOP (@n) ROW_NUMBER() OVER (ORDER BY (SELECT NULL)), 'x'` from
   `sys.all_columns` cross joined three times; runs `SELECT COUNT_BIG(*) FROM
   dbo.LadderN WHERE id % 7 = 3` on each, then the same with `OPTION (MAXDOP
   2)`; runs test 2's planted query; creates and runs `dbo.TwoStatements`,
   whose body is `SELECT pad FROM dbo.Ladder1 WHERE id = 1;` then `SELECT
   COUNT_BIG(*) FROM dbo.Ladder7 WHERE id % 7 = 4;`; and runs, in one batch,
   041's `CREATE TABLE #savings (object_name sysname,
   schema_name sysname, index_id int, partition_number int, size_current_kb
   bigint, size_requested_kb bigint, sample_current_kb bigint,
   sample_requested_kb bigint);` then `INSERT INTO #savings EXEC
   sys.sp_estimate_data_compression_savings @schema_name = N'dbo',
   @object_name = N'Ladder7', @index_id = NULL, @partition_number = NULL,
   @data_compression = N'PAGE';`. Built and run on the lab in `ZzAvgDop5A`,
   that workload gave eight parallel plans: Ladder5 without a hint and under
   `MAXDOP 2` (costs 5.52 and 5.84), Ladder6 likewise (10.99 and 11.56),
   Ladder7 likewise (21.95 and 23.10), the planted query (11.56) and the
   second statement of `dbo.TwoStatements` (21.90). Six of the eight come from
   statements whose serial cost is 12.38 or more (measured under `MAXDOP 1`:
   6.25 for Ladder5, 12.38 for Ladder6, 24.76 for Ladder7), so they stay
   parallel under a threshold up to 12 on this costing, where the fourth
   version's fixture gave exactly five, two of them from a serial cost of 6.25
   (Claude). Ladder1 to Ladder4 stayed serial; the `#savings` call gave the
   anomaly (`is_parallel_plan = 0`, `max_dop` 45). The heaps took 3.7 s to
   build. Then, through `runUnit`, it runs 043. It asserts, in this order:
   the store holds at least five plans with `is_parallel_plan = 1`, and the
   test fails with the instance's threshold and scheduler count in its message
   if not; `window.serial_plans_dop_above_1` is at least 1, and stops there if
   not, since the rest would test nothing;
   `excluded.other_replicas_executions` is 0 and not NULL on SQL Server 2025
   (and NULL below 2022, on a lab that has one); the bands equal those
   computed independently from `sys.query_store_plan` through the xml path on
   the first costed statement element, over the parallel plans of the store;
   no plan of the test database has more than one costed statement element;
   `parallel_executions_min` is at most `parallel_executions` in every band;
   and `examined.share_of_parallel_cpu_pct` equals the bands' summed
   `parallel_cpu_s` over `window.parallel_cpu_s` within rounding. The test
   drops the database. Mutations, each of which must make it fail: selecting
   on `max_dop > 1` instead of `is_parallel_plan = 1`; a `COL_LENGTH` or
   `OBJECT_ID` that never finds the replica column or view (the count turns
   NULL); a replica predicate that keeps the wrong groups (the bands empty
   out); computing the share from anything but the bands.
4. The audit's footprint, by the lint. The existing
   `TestEmbeddedCorpusIsValid` lints every file of the embedded corpus with
   the hint rule of `aeb4619`, 043 included, and nothing is added to it.
   Mutations, each of which it must refuse offline: the hint removed from
   `050.commandlog.sql`'s `INSERT INTO #sel` (the statement point 8 found
   parallel; measured refused); the fourth version's mutation, a collector
   that begins with `DECLARE @n bigint; SELECT @n = COUNT_BIG(*) FROM
   dbo.Ladder6 WHERE id % 7 = 5;` (refused: an unhinted assignment that reads
   a table); and `050.commandlog.sql` as it was at `7984eef` (refused, on its
   first unhinted read). The fourth version's live test, which ran every
   per-database collector on test 3's database and read the store, is not
   kept. Its mutation is now refused before anything runs, so it could only
   have been failed by what the lint does not reach; and a live run sees a
   parallel plan only where its fixture holds data large enough to tempt the
   optimizer, which is how it missed `050` on a database with no
   `CommandLog`. A fixture large enough would make it a regression test of one
   collector, which the lint already is. What it would add over the lint is
   the list in "The audit's own statements", and no fixture reaches those
   cases: a Query Store hint is the client's, the compression sample stayed
   serial on both shapes measured, and the parenthesis form fails the
   collector at execution. Point 8's table is the live evidence, measured once
   with a positive control; it is to be measured again, with the same
   protocol, when the scanner of `hintlint.go` changes or a collector that
   reads a client table is added.
5. The stop rules, live, on the store of test 3 with `@chunk` rewritten to 2.
   With `@budget_bytes` at 1, `examined.stopped_by` is `bytes` and
   `examined.plans_read` is 2, the first chunk, which are the two plans of
   highest parallel CPU, and `examined.largest_plan_bytes` equals the larger
   of their two `DATALENGTH`s read by the test. With `@budget_bytes` at 0, or
   `@budget_ms` at 0, nothing is read and `stopped_by` names that budget; with
   both at 0 it is `time`. With `@cap` rewritten to 2 on a store of more
   parallel plans than that and budgets left as they are, `stopped_by` is
   `cap` and `examined.plans_read` is 2; with `@cap` at the number of parallel
   plans it is NULL. A test that finds fewer than five parallel plans in the
   store fails rather than passes, since it could not tell the rules apart.
   Mutations: a loop that ignores `@chunk` (the byte case reads more than 2);
   `TOP (@cap)` without the `+ 1` (`cap` is never named); checking bytes
   before time.
6. Stores that are not read, and a store with no parallel work. Through
   `runUnit`: in `master`, which has no row in the options view, one root row
   with `state.actual` NULL, `state.not_read_because` `no store`, NULL counts
   and seven empty bands; in a database whose store held parallel plans and
   was then set OFF, the same with `state.actual` `OFF` and `off`; in a
   database whose store is on and holds only serial work in the window, the
   parallel counts at 0 and not NULL (`window.parallel_plans`,
   `window.parallel_plan_cpu_s`, `window.parallel_executions`,
   `window.parallel_cpu_s`, their `_min` pair, `nested.parallel_plans`,
   `nested.parallel_cpu_s`, `examined.plans`, `examined.plans_read`,
   `examined.bytes_read`), `examined.share_of_parallel_cpu_pct` NULL, seven
   bands of zeros, and `window.executions` and `window.cpu_s` above 0, since
   that serial work and 043's own statements are in them (Claude). None of
   the three
   raises an error. The "never enabled" case of the second version cannot be
   built on SQL Server 2022 and later, where a new database inherits
   `model`'s store (Claude, point 6), so `master` stands for it. Mutations:
   reading the OFF store into the bands (which is why it must hold parallel
   plans before it is switched off); dividing without `NULLIF` (error 8134
   fails the third case).
7. The guide. `TestTheGuideCountsTheTimeoutTiersOfTheCorpus` passes with the
   `120 s` row at 30; it is the existing test, run after the guide is edited.

What no test here covers, said so that nobody believes otherwise: the
secondary branch and the rows of another replica role (the lab has no
availability group); `avg_dop` above the range of a `decimal` (the anomaly
cannot be produced); a plan leaving the store between the pin and its chunk;
the selection's cost on a large runtime view; a parallel plan whose
executions ran at a degree of 1 (point 4 saw one in a lab store; no fixture
reproduces it, so the upper bounds equal the totals in every test, and a
band or root that took one for the other would pass; added on 6 October
2026 with the plan's review); the audit's CPU in
`window.cpu_s`, which no test measures; the audit's footprint on a live store,
measured once (point 8) and held by the lint, not by a live test; and the
cases the lint does not reach ("The audit's own statements").

The live tests create their databases under a prefix of their own, and the
existing `TestLive...` suite's `ZzDroppedDuringRun` collides on a shared
instance (codex neutral, first panel); that is a matter for the suite, not for
this file. The tests are numbered for this version; the reviews below use the
numbers of the version they read.

## Open questions

- Is seven days the right window, or thirty, the store's default retention,
  at the price of mixing plans compiled under a threshold since changed?
- Is 10 s the right time budget for a collector that runs on every default
  run, and should the selection, which has no budget, get one, for example
  a count of runtime rows above which the store is reported and not read?
- Should 042 be corrected for serial statements whose `max_dop` exceeds 1, so
  that both files define parallel the same way? Answered on 6 October 2026:
  042 now calls a statement parallel when its cached plan fragment holds an
  operator with `Parallel="1"` (the form the cache writes, not
  `Parallel="true"`), and projects `examined.serial_plans_dop_above_1`.
- Should the other Query Store collectors (020 to 029) learn the replica
  filter, and skip availability group secondaries?
- Should 020 and 029 keep trigger and multi-statement function queries in
  their totals, after point 7?
- Is 043 the right number, or should the Query Store family keep a range of
  its own and the file be renumbered there?
- Should serial plans above the current threshold be counted (they cannot
  exist without a hint or a construct that forbids parallelism), as the
  signature of the opposite misconfiguration?
- Should the contract lint's scanner refuse a statement that opens with a
  parenthesis, or a data statement left without its `;` before one? Today
  such a file passes the lint, runs its unhinted statement, and is refused by
  the runner only afterwards ("The audit's own statements"). It is a change to
  `hintlint.go`, not to 043.
- Should `041.compression-savings.sql` say in its header that the sample copy
  of `sp_estimate_data_compression_savings` lands, unhinted, in the audited
  database's Query Store?

The first draft's questions on a `.env` key and on `--all` are closed by the
decision to run by default, and the third version's question on the session
marker by the decision of 5 October 2026.

## Review of 4 October 2026

Five readers ran the first draft (`87a8d9f`) against the tree of the day and
the lab's SQL Server 2025: agy with the directive and the neutral prompts,
codex with the directive and the neutral prompts, and a Claude subagent with
the neutral prompt. Claude built a database and a stub collector and measured;
codex ran the suite, the consumer's functions and the lab stores; agy ran
probes on the lab. Then the decision to run by default came, and the second
version re-measured what the findings turned on, in `ZzAvgDop2` and in the lab
stores, before rewriting the body. The union of what they found, and what
became of each:

1. Test 5 tested nothing: an ordinary `INSERT ... EXEC` is stored serial with
   `max_dop` 1, so the mutation it was meant to catch passes (Claude, measured;
   reproduced). Taken: point 4, and test 5 uses
   `sp_estimate_data_compression_savings` and asserts the anomaly is present
   before anything else.
2. A store that is OFF still returns its data, and 021, the cited rule, does
   not filter in SQL (Claude, measured; reproduced). Taken: point 6, the
   predicate in "The collector", test 7.
3. Test 6 could not run on test 5's store: a budget in whole MB, chunks of
   100, and two readings of the rule at 0 (Claude, measured; codex directive
   by reading). Taken: "The constants and the stop rules" (`@budget_bytes`,
   `@chunk`, the order of the checks), test 6.
4. The CPU of a parallel trigger is not in its caller (Claude, measured;
   reproduced, and a multi-statement table-valued function behaves the
   same). Taken: point 7 and "Nested queries"; the finding about 020 and 029
   is out of scope and an open question.
5. The audit's own statements land in the store it measures, and 041 runs
   before 043 (Claude, by measurement on `LABSTORE_A`). Taken: point 8 and
   "The statements sql-auditor emitted", tests 2, 4 and 5.
6. Test 3 forbade `plan` in column names that the root itself uses (Claude,
   codex directive). Taken: "The guarantee that no text leaves", test 3 on
   the output.
7. Tests and documents the first draft did not list:
   `TestTheGuideCountsTheTimeoutTiersOfTheCorpus`,
   `TestPlannedDurationLines`, the wizard's text, `main.go`'s help, the
   guide's duration section, and `check`'s "N databases, N units" when system
   databases are widened (Claude, by running a copy of the tree). Taken for
   the timeout table (test 8, "Running by default"); the others belonged to
   the opt-in and lapse with it, as that section says for each.
8. `.env.example` and the wizard would have misled: the Query Store section
   of `.env.example` holds keys that do not shape 043, and the wizard row
   would have sat under a "Query Store window" line it ignores (Claude).
   Taken: 043 is not narrowed by `QUERY_STORE_DB_INCLUDE`, `.env.example` is
   unchanged, and there is no wizard row.
9. The bands are not summable across databases for `max_dop` and `avg_dop`
   (Claude). Taken: "What the analysis does with it", and `avg_dop`'s weight
   is stated in the bands.
10. Zero counts for a store that was never enabled broke the convention of
    026 and 029 (Claude). Taken: NULL when not read, 0 when read and empty.
11. `CostFlags` was out of proportion to a collector measured at under 100 ms
    per database (Claude). Superseded by the decision to run by default.
12. The denominator: `window.parallel_cpu_s` included executions at DOP 1
    that the bands exclude, 151.0 s against 43.3 s on `LABSTORE_A`'s history
    (codex directive, measured; agy neutral, by reading). Taken: the root's
    `window.parallel_*` share the bands' predicate, and the ranking uses it
    too.
13. "The stored plan is the plan of one statement" was false as written: 104
    plans of `LABSTORE_A` hold two statement elements (codex directive,
    measured; reproduced, all `INSERT ... EXEC`, one cost each). Taken:
    point 1 rewritten, and test 5 checks the premise on its store.
14. The converse of point 4: parallel plans whose runtime rows are all at
    `max_dop` 1 (codex directive). Taken: point 4, the ranking by parallel
    CPU, and `window.parallel_plan_cpu_s`.
15. The permissions would fail on SQL Server 2022 and later (codex directive,
    hypothesis). Rejected: `sys.fn_builtin_permissions` gives `VIEW SERVER
    STATE` covering `VIEW SERVER PERFORMANCE STATE`, which covers `VIEW
    DATABASE PERFORMANCE STATE`, and 029 already runs by default with the same
    line.
16. The time and byte budgets are soft, and "caps it at thirty on any
    instance" was not true (codex, both prompts). Taken: the stop rules say
    what is checked and when, the guide's row says a chunk can pass a budget,
    and `@timeout` is named as the hard bound.
17. The seven-day predicate dropped the interval straddling the boundary, and
    the completeness test would have called complete hourly stores short
    (codex, both prompts). Taken: "The window", with
    `window.store_oldest_interval`.
18. `window.cpu_s` double counted nested CPU (codex neutral). Taken with 4,
    by kind of object.
19. "At most 100 MB" in the option's description was not a cap on bytes read
    (codex neutral). Taken with 16; the option's description is gone with the
    option.
20. `DATALENGTH` on the view decompresses the plan a second time (agy
    neutral). Taken after measuring it at 1 182 ms against 751 ms for the
    copy: the bytes are counted on the staged column.
21. Rejecting a database on `truncated` reversed 042's thin-cache logic
    (agy neutral). Taken: the analysis qualifies by share.
22. The comparison with 029's single pass was wrong in grain (agy neutral).
    Taken: "The aggregation of the runtime rows".
23. The parallel boundary measured by Claude (4.577 parallel) differs from
    the first draft's 4.83 and 6.32 (Claude). Taken: point 3 gives both.
24. The existing live suite creates `ZzDroppedDuringRun` and collides on a
    shared instance (codex neutral). Out of scope; noted under "Tests".

Confirmed and not a problem: selecting on `is_parallel_plan` (all five); the
text search cannot be fooled by a literal in the query text, and `TRY_CAST` to
float reads a period under any language (agy, both prompts); the staged
search reproduces (Claude); the lock is released between chunks (agy by
reading, codex found no retained lock); the consumer's three functions accept
the band shape (codex, both prompts); the `queryStoreUnits` and `KnownFlags`
mechanics held for the opt-in (Claude, codex neutral).

## Second review of 4 October 2026

Five readers ran the second version (`7bd22c1`) against the tree and the lab:
agy with both prompts, codex with both prompts, a Claude subagent with the
neutral prompt. Claude built a driver that runs batches on one pooled
connection and recycles it as `recycleConn` does, read go-mssqldb v1.10.0, and
measured in three databases of its own; codex ran the suite and the live
suite, and probes in two databases; agy ran probes on the lab, and its neutral
reading found nothing. The third version then measured, in `ZzAvgDop3`, the
marker placed in the reset batch across a recycle and a `SET LANGUAGE`, test 5's
`#savings` artefacts from both kinds of session, the replica groups of a
store without an availability group, and the copy and search of
`LABSTORE_A` by chunks of 100, 10 and 1; and read Microsoft Learn on Query
Store for secondaries. The union, and what became of each finding:

1. The cap test cannot check 043's caps: it counts `TOP (@variable)`, while
   the pin is `TOP (@cap + 1)` and the chunk a range of ranks, so the only
   passing count is 0, which checks nothing (Claude, measured on a stub; codex
   directive by reading). Taken: `workload_caps_test.go` learns the applied
   form per row, test 1 names the forms, and test 6 is said to be what proves
   the loop reads by its chunk.
2. `SET LANGUAGE` cancels the marker for the rest of the collector, and test
   2 looked for the readers of `DATEFIRST`, not its writers (Claude, measured;
   reproduced). Taken: point 8, "The statements sql-auditor emitted", test 2
   scans for writers as well as readers, test 4 pins the behaviour, and a
   `--queries-dir` script with a writer gets a warning.
3. The "never enabled" case of test 7 cannot be built on SQL Server 2022 and
   later: a new database inherits `model`'s store, which is on (Claude,
   measured; reproduced). Taken: point 6, and test 7 reads `master`, the one
   database the lab showed without a row.
4. Query Store for readable secondaries puts other instances' work into the
   primary's store (Claude, by reading Learn). Verified on Learn, which also
   contradicts itself on whether it is on by default in SQL Server 2025; the
   lab has no availability group, and its stores carry group 1 only (point
   9). Taken, as filter and refusal: on a secondary 043 does not read the
   store; on a primary, rows of other roles are left out and counted in
   `excluded.other_replicas_*`, NULL where they cannot be told apart
   ("Other replicas"). The filter needs a version-dependent column, hence
   `sp_executesql` and a temporary table. No live test can build the case.
5. `examined.share_of_parallel_cpu_pct` divided by zero when nothing in the
   window is parallel, the common case (Claude). Taken: NULL through
   `NULLIF`, test 7's third case.
6. Test 2 could not read `SessionInitSQL` statically, since `Open` returns a
   `*sql.DB`; and the "once after the login" setting was redundant, because
   the driver's `Connect` already runs `SessionInitSQL` (Claude, reading the
   driver). Superseded: the marker moved into `ResetSession`'s batch, which a
   function builds and a test reads; there is no `SessionInitSQL`.
7. The marker added a round trip per collector, and made `recycleConn`'s
   comment and the guide's "costs one round trip" false (Claude, reading).
   Taken with 6: the marker rides on a batch the run already sends, and both
   sentences stay true.
8. `avg_dop` as `decimal(9,2)` could overflow on the documented anomaly
   (Claude). Taken: a `float`, and the reason stated in "The bands". No test
   can produce the value.
9. The six other default Query Store collectors still count the audit, and
   025 shows its statements as separate query ids (Claude). Left: stated in
   "Running by default" and "What is not in scope", and an open question;
   043's `excluded.audit_cpu_s` is the difference between its total and 029's.
10. The byte overshoot is bounded by the size of a chunk's plans, not by the
    lab's average, and the ranking puts the largest first (Claude; codex
    neutral, as a hypothesis on the default guarantee). Taken: the bound is
    stated, `examined.largest_plan_bytes` reports it, and smaller chunks were
    measured and refused (chunks of 10 cost twice as much, plan by plan eight
    times). "The decision to run it by default" no longer calls the bytes
    bounded.
11. The selection is not covered by either budget, and the guide's "CPU,
    proportional to the bytes read" hid it (codex, both prompts; agy
    directive). Taken: `selection.duration_ms` and `window.runtime_rows`, the
    guide row prices the two parts apart, and whether the selection needs a
    budget is an open question.
12. The audit marker deletes a client session that sets `DATEFIRST 3` from
    every total and counts it as the audit's (codex, both prompts, measured).
    Not changed: it was stated as a limit in the second version and is the
    price of the marker; it is now in "The limits a reader must keep in view"
    and in the open question put to the owner.
13. The marker changes the answers of a foreign `--queries-dir` corpus, and
    the guide's sentence said only "days of the week", while week numbers and
    `DATETRUNC(week)` move too (codex, both prompts, measured). Taken: the
    functions are named in this document and in the guide's paragraph, and
    such a script gets a warning. The change itself stays, with the marker.
14. Test 5's workload could not run as written: `#savings` was not defined
    and the procedure's arguments were missing (codex directive, measured).
    Taken: test 5 spells out the table, the arguments, the ladder and the
    procedure; run in `ZzAvgDop3`, they gave the anomaly from both sessions
    (point 4).
15. The oldest interval does not prove seven days of capture: a store OFF or
    READ_ONLY for part of the week keeps its older intervals (codex, both
    prompts; the OFF premise measured by codex directive). Taken: "The
    window" calls it evidence, adds `window.intervals`, and the analysis
    reports a thin history rather than concluding.
16. A plan that left the store before its chunk is in the unknown band but
    not in the share's numerator, so the stated identity failed (codex
    neutral, by reading). Taken: the share is computed from the bands.
17. The aggregation into a table variable is slow on large stores (agy
    directive, measured at 1.70 s against 0.44 s for 500 000 rows, without
    `MAXDOP 1`). Taken for another reason as well: `#runtime`, required by
    `sp_executesql`.
18. `seuil_parallelisme.py` reads 042's root keys, and would read 0 from 043
    (agy directive, by reading; checked: `lire` reads `examined.statements`,
    `examined.cap` and `cache.*`). Taken: "What the analysis does with it"
    gives 043 a root reader of its own.
19. "33 minutes on 200 databases" read as `check`'s figure, while `check`
    computes from `@timeout` (agy directive). Taken: the sentence now says
    the budget does not change `check`'s ceiling.

Confirmed and not a problem: the marker reaches every kind of statement the
audit causes, `sp_executesql`, system procedure internals, trigger and
function bodies and `StatMan` included, and the same statements from an
unmarked session are stored under 7 (Claude); `SessionInitSQL` and the pool's
reset behaved as the second version said (Claude), which no longer matters;
the anomaly of test 5 reproduces from both sessions (Claude, and the third
version); no language has `datefirst` 3 and no system procedure the corpus
calls reads it (agy neutral, Claude); the `120 s` tier is 29 today; `--profile space`
excludes a file with no `@profiles`; the OFF predicate and the denominator
fix hold (codex, both prompts); the suite and the live suite pass on the
untouched tree (codex, both prompts, Claude); and the aggregation's join to
the plan view costs 2 to 14 ms, so it does not decompress plans (Claude).

The decision left to the owner by the third version was taken on 5 October
2026: the session marker is withdrawn. It rested on a measurement made on 4
October 2026 around 21:52 UTC, a complete default collection on the lab
followed, in each of the nine databases with an active store, by a read of the
runtime rows executed since it began: no parallel plan in any of the nine, 41
to 213 plans per database, 0 to 2 serial plans with `max_dop > 1` (point 8).
The audit therefore leaves nothing for the bands to exclude, the selection on
`is_parallel_plan = 1` already sets its anomalies aside, and its share is in
the serial totals only ("The audit's own statements"). That supersedes finding
5 of the first review and findings 2, 6, 7, 12 and 13 of this one, which
concerned the marker; finding 9 stands, without the `excluded.audit_cpu_s`
it pointed to, since 043 and 029 now count the audit alike. The marker's
context-setting measurements, its two tests, the `excluded.audit_*` columns,
the reset batch and the `--queries-dir` warnings left with it; they are in
`760b4bf`. Test 4 now runs the embedded corpus against a store and fails if
the audit leaves a parallel plan.

## Review of 5 October 2026

Five readers ran the fourth version (`16c0591`) against the tree and the lab:
codex with the directive and the neutral prompts, DeepSeek V4 Pro with both in
agy's two seats (agy's quota was spent), and a Claude subagent with the neutral
prompt. Claude built a copy of the tree, the real binary and two databases,
and ran a default collection over a `dbo.CommandLog` of 200 000 rows; codex ran
the suite, `check`, part of the live suite and the consumer's functions;
DeepSeek ran probes in databases of its own. The decisive finding was fixed in
the tree before this version (`aeb4619`, `a9fe54c`). This version then
measured in `ZzAvgDop5A`, dropped afterwards: test 3's workload with a seventh
heap, the collection of point 8 before and after the lint change and with
`--estimate-compression`, the procedure's sample copy on a second shape, a
Query Store hint on a statement written with `MAXDOP 1`, the parenthesis form
and an XML method through the real runner; and it ran `lint()` on a stub of
043 and on mutated copies of collectors. The union, and what became of each
finding:

1. A default collector left a parallel plan in the [5, 25) band: the unhinted
   `INSERT INTO #sel` of `50.agent/050.commandlog.sql`, DOP 22, cost 5.39,
   over a `CommandLog` of 200 000 rows, which test 4's database did not have
   (Claude, measured; reproduced with the binary of `7984eef`: cost 5.63, DOP
   22). DeepSeek directive reached the same class by reading: the lint
   exempted assignments and buffering statements, and `050`'s dynamic
   `UPDATE` had no hint. Taken, in the lint first: every statement that reads
   rows carries the hint since `aeb4619`, and the nineteen files that did not
   were rewritten. In this document: point 8, "The audit's own statements",
   the limits and test 4 rest the property on the lint, with point 8's table
   as its evidence.
2. A hinted `DELETE` or `UPDATE` failed the lint as a result set too many, and
   a subquery in `SET` or `DECLARE` cannot carry an `OPTION` clause, so "every
   statement carries the hint" could not be written (Claude, measured on a
   stub). Taken by the same change; verified on a stub of 043, which lints
   clean with its `DELETE` hinted and is refused without it, and "The
   collector" names the forms the file must use. Found while verifying: the
   statement lint refuses `UPDATE k ... FROM @pinned AS k` with a message that
   names `CREATE`, the first scoped keyword of the file, rather than the
   `UPDATE`. 043 avoids the form; the message is a matter for the lint.
3. Test 3's workload gave exactly the five parallel plans test 5 needs, two of
   them from a statement whose serial cost sits just above the threshold
   (Claude, measured; reproduced: serial cost 6.25, parallel 5.52 and 5.84).
   Taken: a heap of 1 000 000 rows and the planted query bring the lab to
   eight, six of them from serial costs of 12.38 or more, and test 3 asserts
   the count before anything else, with the threshold and scheduler count in
   its failure.
4. The opt-in collectors were outside the claim, and 041 was cited as part of
   the default footprint while it runs only under `--estimate-compression`
   (Claude, by reading; codex directive, by running `check` with and without
   `--all`). Verified: with `--estimate-compression` no parallel plan was
   left, and the procedure's sample copy was stored serial at 37.2, and at
   38.1 on a second heap. Taken: "The audit's own statements" names it as the
   one path by which the shipped corpus can reach a band, and the analysis
   reads `_run.json` for it.
5. `027`'s `DECLARE @plans_total bigint = (SELECT COUNT_BIG(*) FROM
   sys.query_store_plan)` and the corpus's other unhinted reads grow with the
   client's store (Claude, hypothesis). Taken by `aeb4619`, which rewrote 027;
   checked: the declaration is gone and the file lints clean.
6. Test 6's third case asked for "0 counts" where `window.executions` and
   `window.cpu_s` cannot be 0 (Claude, by reading). Taken: the case names the
   keys at 0 and the two above 0.
7. Test 4 did not say whether its instant was taken on the server (Claude, by
   reading). Moot for a test that is no longer live; point 8's protocol reads
   `SYSUTCDATETIME()` on the server.
8. "The six other default Query Store collectors" left out 027 and
   `20.databases/022.query-store.sql` (Claude, by reading). Verified: both run
   by default, `@profiles: space` adding a profile and removing none. Taken;
   found while verifying, the same sentence put 025 under
   `--query-store-detail`, which gates 021, while 025 needs
   `--query-store-compare-at`.
9. Point 8's "0 to 2" serial plans with `max_dop > 1`: one database had 3
   (Claude, measured). Verified: 2 in a default run here and 3 with
   `--estimate-compression`. Taken: the text says two or three in the runs
   measured, and the file's header carries no range.
10. Point 8's measurement could not be repeated: no command, no instant, no
    database names, no predicate, on an instance where `check` listed eleven
    databases (codex directive, by running). Taken: point 8's table gives the
    database, the fixture, the command, the server instants, the predicate and
    a positive control; the 21:52 measurement stays as dated history.
11. Test 4 had no positive control and could pass on a store that had not yet
    shown the audit's rows (codex directive, hypothesis citing Learn on view
    latency). Moot for test 4, which is now offline; point 8's control is the
    run before the lint change on the same fixture, which found the plan.
12. The audit's serial activity fills intervals, so `window.intervals` and
    `window.newest_interval` can make a thin or stopped history look current
    (codex directive, hypothesis; DeepSeek directive, by reading, for 043's
    own interval). Taken in "The audit's own statements" and in the analysis,
    which must not read either as evidence of the client's workload. No column
    is added: counting the intervals of parallel work apart would need a
    second pass over the runtime rows, which `#runtime`, grouped by plan, does
    not keep.
13. A Query Store hint overrides a hint written in the statement, so a client
    can make an audit query parallel (codex, both prompts, from Learn).
    Verified on the lab: `OPTION (MAXDOP 4)` set with
    `sys.sp_query_store_set_hints` on a query written with `OPTION (RECOMPILE,
    MAXDOP 1)` gave a parallel plan at DOP 4. Taken as a limit, in the
    header's sentence, the section and "The limits a reader must keep in view".
14. Test 2's planted literal was in no query of test 3's workload (codex
    neutral, by reading). Taken, and found while verifying that the bare form
    loses the literal to simple parameterization: the planted query carries
    `OPTION (MAXDOP 2)`, and test 2 first asserts the store kept the literal
    in a parallel plan's query.
15. Test 4's mutation relied on a scalar `COUNT_BIG(*)` that another instance
    might run serial, and a `GROUP BY` would be more robust (DeepSeek
    directive, measured parallel on 2 000 000 rows). Moot: the mutation is now
    refused by the lint before anything runs.
16. Point 8 was framed as a fact about the engine while it was one run on one
    build (DeepSeek directive, by reading, findings 6 and 7). Taken with 1 and
    10: the guarantee is the lint, and the run is evidence.
17. `seuil_parallelisme.py`, pointed at 043 today, prints zeros rather than
    failing (DeepSeek neutral, by reading). Known from the second review,
    finding 18; taken further: `lire` refuses a document without
    `examined.statements` ("What the analysis does with it").

Found while revising, by running:

18. A data statement left without its `;` before a statement that opens with
    a parenthesis passes the lint unhinted, and its plan is stored parallel
    (DOP 22 on 2 000 000 rows); the runner refuses the collector at execution,
    after the statement ran. Recorded in "The audit's own statements" and as
    an open question for the lint.
19. A collection's read created a statistic on a client table: after the runs
    of point 8, `dbo.CommandLog` carried an automatically created statistic on
    `StartTime`. Its `StatMan` query was not in the store, but such queries
    can be stored parallel with no cost (point 5); recorded among the cases
    the lint does not reach, and as outside the costed bands.
20. `DECLARE @v bigint = @x.value(...)` on a variable of 20 000 elements
    compiled a plan the store kept, at cost 209, serial. No collector uses the
    form; it is listed among the cases the lint does not reach.

Rejected:

- Test 3's ladder cannot go parallel at a threshold of 5, since a 1 500 000
  row `COUNT_BIG(*)` cost under 0.01 (DeepSeek neutral, measured, findings 3
  and 7). Rejected by measurement: on the same lab and build the 1 000 000 and
  2 000 000 row heaps gave parallel plans at 10.99 and 21.95, and Claude's
  reading of the same fixture found five. A cost under 0.01 is not the cost of
  scanning 1 500 000 rows, and the report does not give the table's
  definition or how it was filled.
  The concern behind it, a test passing on a store with nothing parallel, is
  answered by test 3's first assertion; the mutation it names, selecting on
  `max_dop > 1`, would fail anyway, since the asserted anomaly would then
  enter the bands.
- `nested.*` should have an any-degree CPU column like
  `window.parallel_plan_cpu_s` (DeepSeek neutral, finding 9). Rejected: no
  computation of the analysis uses it; the window's any-degree column exists
  to show what the denominator leaves out, and `nested.*` only says how much
  of the bands is nested work.
- The selection on `is_parallel_plan = 1` sets aside a parallel buffering
  `INSERT` that the old lint allowed, and should be presented as the guard
  (DeepSeek directive, finding 6). Rejected as stated: a parallel buffering
  `INSERT` is a parallel plan and is selected, as `050`'s was
  (`is_parallel_plan = 1`). The selection guards against the serial anomaly
  only, which "The audit's own statements" says.

Confirmed and not a problem: `master` and `tempdb` have no row in the options
view, `model` is `READ_WRITE`, and a new database inherits it (Claude,
DeepSeek neutral); a store set OFF still returns its data (DeepSeek neutral);
the replica column and view exist, and a store without an availability group
maps ids 1 to 4 to `role_type` 1 to 4 (Claude, DeepSeek neutral); the `#savings`
anomaly reproduces (Claude, DeepSeek directive, and this version at
`max_dop` 45); the `120 s` tier is 29 and the cited tests exist (Claude); the
caps test as described catches its mutations (Claude, DeepSeek neutral); the
consumer's band functions read 043's band shape and `lire` misreads its root
(codex neutral, DeepSeek); the stop rules' order gives test 5's outcomes
(Claude, DeepSeek directive); no live text of the body still names the marker,
and the tests are numbered consistently (codex directive, DeepSeek directive);
the suite passes on an untouched tree (codex, both prompts, Claude); and the
live suite's `ZzDroppedDuringRun` collides again on a shared instance (codex,
both prompts), as the first review recorded.

The rules this version is least sure of, its own new ones first:

- That the lint is the guarantee. `hintlint.go` is a scanner, not a parser:
  the parenthesis form passes it today, and a construct it has never met
  could pass it tomorrow. A file that hides a statement this way is refused
  at execution, which is loud but late.
- Dropping the live footprint test. Point 8's table is measured once, and "to
  be measured again when the scanner changes or a collector reads a client
  table" is a rule nothing enforces.
- The sample copy of `sp_estimate_data_compression_savings`. Its plan was
  serial on two shapes with no `NonParallelPlanReason`, so its seriality is a
  cost decision of the optimizer, and its cost, bounded by a 5 000-page
  sample, was read from one build's procedure text.
- The fixture's margin. Eight parallel plans on a lab of 22 schedulers at a
  threshold of 5, six of them up to a threshold of 12; another instance's
  costing may give fewer, and test 3 then fails rather than passes.
- The cases outside the lint that were not observed: an automatic `StatMan`
  stored parallel after an audit's read, and an XML method in a `DECLARE`
  that the corpus does not use. Both are argued from other measurements.
- That the three procedures besides the compression estimate cannot reach a
  store 043 reads. It holds for the default `SQL_DATABASE`, `master`; with
  another database there, the instance-scoped collectors run in it, and the
  argument for `sp_readerrorlog` and `sp_enumerrorlogs` then rests on their
  reading files.
- Leaving the interval figures unchanged and relying on the analysis to read
  them with care (finding 12).
