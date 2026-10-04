# Parallel cost from the Query Store

Status: proposed on 4 October 2026, not implemented. The first draft
(`87a8d9f`) put the collector behind an opt-in; it was read the same day by a
panel of five readers, and then the decision changed: the collector runs by
default, with no option. The second version answered both and was read by a
second panel of five the same day. This third version answers that panel. What
each panel found, and what became of each finding, is in "Review of 4 October
2026" and "Second review of 4 October 2026" at the end. One decision is left to
the owner before code: whether the session marker of "The statements
sql-auditor emitted" is kept, or replaced by the alternative given in "Open
questions". Nothing in the tree has changed yet.

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
- `sys.query_context_settings` carries, per context of compilation, the
  `set_options` mask, `language_id`, `date_format` and `date_first`; every
  query of the store points to one through `context_settings_id`.
  https://learn.microsoft.com/en-us/sql/relational-databases/system-catalog-views/sys-query-context-settings-transact-sql
- Query Store for readable secondaries: "secondary replicas stream query
  execution information (such as runtime and wait statistics) to the primary
  replica, where the data is persisted in Query Store and made visible across
  all replicas", "aggregated at the role level", told apart by
  `replica_group_id`, which `sys.query_store_replicas` (SQL Server 2025) maps
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
`ZzAvgDop3` for this one) with the store in capture mode ALL and one-minute
intervals, dropped afterwards; and in the existing stores of five lab
databases, `LABSTORE_A` (955 plans, 73 MB) and `LABSTORE_B` (126 plans, 24 MB)
among them, read from `master` with three-part names so that the reads were
not captured into those stores.

1. A stored plan is the plan of one query, and its first costed statement
   element is that query's own. A procedure of two statements (a key lookup,
   then a parallel aggregate) gave two plans, each holding one statement
   element, so the first trap of 042, the batch plan whose first statement is
   a neighbour, does not exist here. The `Statements` element can still hold
   two children: in `LABSTORE_A`, 104 of 955 plans did, all of them
   `INSERT ... EXEC` statements, whose second element carries no cost. Over
   the 955 plans, the string `StatementSubTreeCost="` occurred once in 954
   and never in one (a plan with no statement). No stored plan with two costs
   has been seen; it is checked again by test 5 rather than assumed.
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
   statements reported 2. In `ZzAvgDop3`, with the exact artefacts of test 5
   (041's `#savings`, the procedure called on a heap of 2 000 000 rows), it
   reported 36 from an unmarked session and 5 from a marked one, both serial.
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
8. A context setting marks a session's statements. A session that ran `SET
   DATEFIRST 3` in a batch of its own and then a parallel aggregate in the
   next batch stored that aggregate under a context of its own,
   `date_first` 3, with an unchanged `set_options` mask (`0x000000FB`); no
   copy of the query appeared under the default context. When the `SET` and
   the query were in the same batch, the query was also compiled once under
   the default context, before the `SET` ran, and that plan has no runtime
   row. The languages of the lab instance have `datefirst` 1 (26 of them) or
   7 (8); none has 3. In `ZzAvgDop3`, the batch the run already sends before
   every collector, with the `SET` put first (`SET DATEFIRST 3; IF @@TRANCOUNT
   > 0 ROLLBACK; USE [master];`), then `USE` of the database, then the
   collector's batch, stored the collector's query under `date_first` 3, and
   did so again after the connection was handed back to the pool and taken
   out (the pool's reset put `@@DATEFIRST` back to 7, the batch to 3). A
   collector batch that began with `SET LANGUAGE French` read `@@DATEFIRST` 1
   and stored its query under `date_first` 1, `language_id` 2: a language
   change replaces the marker until the next reset (Claude found it; measured
   again here). The second panel also found the marker on the internal
   statements of `sp_estimate_data_compression_savings`, on `sp_executesql`,
   on trigger and scalar function bodies and on the `StatMan` queries the
   audit's metadata access caused (Claude).
9. The replica groups of a store that has no availability group. In
   `ZzAvgDop3` and in `LABSTORE_A`, every runtime row carried
   `replica_group_id` 1, and `sys.query_store_replicas` held four rows, ids 1
   to 4 against `role_type` 1 to 4. `COL_LENGTH` found the column and
   `OBJECT_ID` the view. No availability group exists on the lab, so the rows
   of a secondary have not been seen.

Point 4 also concerns 042, which calls a statement parallel when
`sys.dm_exec_query_stats.max_dop > 1`. On the same lab, 25 statements of the
cache had `max_dop > 1`; 15 of them had no parallel operator in their plan
fragment (no `Parallel="true"`), carried 351 of the 379 executions and 7.1 of
the 14.3 seconds of "parallel" CPU, and were `INSERT ... EXEC` and
`INSERT INTO @t` statements, most of them sql-auditor's own collectors. It is
outside this document's scope and is listed under the open questions.

## What is read

The plans of the database's Query Store that are parallel (`is_parallel_plan =
1`), executed inside the window by this replica's role, and not emitted by
sql-auditor; for each of them, its estimated cost, read from `query_plan`, and
over the window, from `sys.query_store_runtime_stats`, its executions, its CPU
and its degrees.

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
`avg_dop`, the highest `max_dop`, and, joined once, `is_parallel_plan`, the
query's object type and the query's context. The root's totals, the ranking
and the bands all come from that table, so they describe the same snapshot and
no total can disagree with the sum of the bands because a statement ran in
between. This is a grouping by plan, which 029 does not make (029 groups by
interval).

It is a temporary table and not a table variable for two reasons. The
statement that fills it is run through `sys.sp_executesql`, because its replica
predicate names a column that does not exist before SQL Server 2022 ("Other
replicas"), and a table variable is not visible inside `sp_executesql`; this
is the corpus's existing way of reading a version-dependent column
(`20.databases/020.properties.sql`, `COL_LENGTH` then `sp_executesql`). And
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
  read is treated as a secondary and reported as an error, as 050 does.
  `state.not_read_because` says `secondary` or `replica state unreadable`.
- On a primary, or a database outside any availability group, from SQL Server
  2022, the runtime rows whose `replica_group_id` `sys.query_store_replicas`
  maps to a `role_type` other than 1 are left out of everything and counted in
  `excluded.other_replicas_executions` and `excluded.other_replicas_cpu_s`.
  The predicate is negative on purpose: a row whose group the view does not
  list is kept, so a mapping this document has not seen cannot empty the
  bands. Where the column exists and the view does not (SQL Server 2022, where
  the feature needs trace flag 12606), nothing is left out and the two counts
  are NULL; before SQL Server 2022 both are NULL as well. 0 therefore means
  "looked and found none", and NULL "could not look".

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

### The statements sql-auditor emitted

The collector runs inside the database it measures, after every per-database
collector that sorts before `80.workload/043`, and the store captures what
those collectors ran there: `70.schema/041.compression-savings.sql` calls the
very `sp_estimate_data_compression_savings` of point 4, and 027 alone can
spend up to 20 s per database. In `LABSTORE_A`, the four serial plans with
`max_dop > 1` were all sql-auditor's own statements, the newest executed by an
audit the same evening. Earlier audits within the window are in it too.

They are recognised by a context setting the runner gives its session, and
left out of everything: totals, ranking and bands.

- The marker rides on the batch the runner already sends before every
  collector, after every reconnect and when a collector leaves its database:
  `ResetSession`'s `IF @@TRANCOUNT > 0 ROLLBACK; USE [db];` becomes `SET
  DATEFIRST 3; IF @@TRANCOUNT > 0 ROLLBACK; USE [db];`. The batch is built by
  a function of its own so that a test can read it. That batch runs after the
  pool's reset (which restores the login's settings) and before the
  collector's own batch, so every collector runs marked (point 8). It adds no
  statement on the wire: the second version used the driver's
  `SessionInitSQL`, which go-mssqldb runs as an extra batch on every reset of
  a pooled connection (Claude, reading v1.10.0), and which would have made
  `recycleConn`'s comment ("the reset costs nothing on the wire") and the
  guide's "costs one round trip" false. Both stay true.
- The value is a Go constant, `collect.AuditDateFirst`, and the file declares
  `@audit_datefirst` with the same value; a test compares them.
- The file leaves out every query whose `context_settings_id` has
  `date_first = @audit_datefirst` in `sys.query_context_settings`, and counts
  what it left out in `excluded.audit_plans` and `excluded.audit_cpu_s`.
- What can cancel the marker inside a collector: a statement that writes
  `DATEFIRST`, which is `SET DATEFIRST` and `SET LANGUAGE` (point 8). The rest
  of that collector's batch then runs under the other value and is counted as
  workload, until the next collector's reset batch restores the marker. A
  `SET` inside a stored procedure is undone when it returns, so a procedure the
  audit calls cannot do it. The embedded corpus has no writer today, and test
  2 keeps it so. A script of a `--queries-dir` corpus that has one gets a
  warning in `MANIFEST.txt` naming it, by the route the existing warnings take.
- What the marker changes for the corpus: `DATEPART` with `weekday`/`dw` and
  `week`/`wk`/`ww`, `DATETRUNC(week, ...)` and `@@DATEFIRST` give different
  answers (agy and codex measured `7, 1, 41` against `3, 5, 40`, and
  `DATETRUNC(week)` moved from 4 October to 30 September); `DATENAME(weekday)`,
  `DATEDIFF(week)`, `DATE_BUCKET(week)` and `FORMAT(..., 'dddd')` do not
  (Claude). The embedded corpus reads none of the first group, and test 2
  keeps it so; none of the system procedures it calls references `DATEFIRST`
  or a week datepart (Claude). A `--queries-dir` script that reads one gets
  the same kind of warning, and `docs/dba-guide.md` says what moves.

What it cannot recognise: statements of an sql-auditor build older than this
change; statements of any other tool; the few statements the run sends outside
a collector (the probe, the preflight, the session id reads), which land in the
store only when `SQL_DATABASE` names a user database and are serial; and a
client session that sets `DATEFIRST 3` itself, which is left out and counted as
the audit's (both codex readers ran it: the same query under 7 and under 3 gave
two query ids, and the predicate takes the second). The first three stay in
the window as ordinary workload, and the root does not pretend otherwise. The
last is the price of the marker, and the decision whether to pay it is in
"Open questions".

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

The parallel plans of the window that sql-auditor did not emit are ranked by
their parallel CPU in the window (the CPU of their runtime rows where `max_dop
> 1`), highest first, then by plan id, and the first `@cap + 1` are pinned
into a table variable with their rank `rn`, before any `query_plan` is read,
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

All six are constants declared at the head of the file and projected in the
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
| `@audit_datefirst` | 3 | the context setting of sql-auditor's sessions |

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
which is what test 6 uses. The bound that does not move is the file's
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
- `collect/runner.go`: `ResetSession`'s batch gains `SET DATEFIRST` with
  `collect.AuditDateFirst` at its head, and is built by a function a test can
  call (for example `resetBatch(defaultDB string) string`). `Open` and the
  connector do not change; no `SessionInitSQL`.
- `collect/queryset.go` or the runner: a warning for a script that writes
  `DATEFIRST` (`SET DATEFIRST`, `SET LANGUAGE`) or reads it (`DATEPART` with a
  weekday or week datepart, `DATETRUNC(week, ...)`, `@@DATEFIRST`), by the
  route `MANIFEST.txt`'s existing warnings take. The embedded corpus never
  triggers it (test 2).
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
  measured"; beside the paragraph on the session reset, a paragraph on the
  session's `DATEFIRST`: its value, why, what it changes (the functions of
  "The statements sql-auditor emitted"), that a `SET LANGUAGE` or `SET
  DATEFIRST` in a `--queries-dir` script ends the marking for the rest of that
  script, and that a client session using `DATEFIRST 3` is counted as the
  audit's by 043; and the illustrative `check` listing may gain
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
- `recycleConn`'s comment and the guide's "costs one round trip" stay true,
  since the marker adds no batch.
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
- The six other default Query Store collectors (020, 023, 024, 026, 028, 029)
  go on counting the audit's statements, and 025, under
  `--query-store-detail`, shows them as separate query ids with the same
  `set_options` (Claude). That is left as it is in this change: teaching them
  the marker is a change to six files with tests of their own, and 043's root
  gives `excluded.audit_cpu_s`, which is the difference a reader comparing
  043's total with 029's has to account for. It is an open question.

## The guarantee that no text leaves

This is what allows the default, so it is stated as a contract and tested
rather than inferred from the column list.

What leaves the server is the database name (already in every per-database
document), the store's state and settings, timestamps of intervals, counts,
sums, degrees, sizes in bytes, the band boundaries and the stop reason. No
query text, no plan, no query id, plan id, query hash or plan hash, no object
id or name, no context settings id, no replica name. The plan text is read on
the instance and dropped with its chunk, as 042 does with the cache. The
context settings and the replica groups are read to exclude rows, not
projected.

The test (test 3) is on the output, not on the SQL: the set of keys of the
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
the same classes of nested queries; `sys.query_context_settings`,
`sys.query_store_replicas`, `sys.dm_hadr_availability_replica_states` and
`sys.dm_os_sys_info` need nothing more (see the permission chain above, and
050, which reads the replica states under the same line). The floor is SQL
Server 2016, where every column read outside the `sp_executesql` branch
exists; it has not been measured on 2016. Each of the two result sets carries
`OPTION (RECOMPILE, MAXDOP 1)`, as the contract lint requires, and so does
every statement of the file.

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
| `window.executions`, `window.cpu_s` | every query not emitted by sql-auditor, except scalar function queries |
| `window.scalar_function_cpu_s` | the CPU of the scalar function queries left out of `window.cpu_s` |
| `window.parallel_plans` | plans with `is_parallel_plan = 1` executed in the window, nested ones included |
| `window.parallel_plan_cpu_s` | all their CPU, at any degree |
| `window.parallel_executions`, `window.parallel_cpu_s` | their runtime rows where `max_dop > 1`: the denominator of the bands' `parallel_*` |
| `window.parallel_executions_min`, `window.parallel_cpu_s_min` | their runtime rows where `min_dop > 1` |
| `window.serial_plans_dop_above_1` | plans with `is_parallel_plan = 0` and a runtime row with `max_dop > 1` (point 4), counted and never banded |
| `window.dop_above_schedulers` | runtime rows of parallel plans with `max_dop` above `schedulers` (the documented anomaly), counted and kept |
| `nested.parallel_plans`, `nested.parallel_cpu_s` | parallel plans of `FN`, `TF` and `TR` queries, kept in the bands, and their CPU where `max_dop > 1` |
| `excluded.audit_plans`, `excluded.audit_cpu_s` | plans executed in the window under `@audit_datefirst`, and their CPU, left out of everything above |
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
against the keys of "The root".

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
budget stopped the read; and it prints both sources side by side. Neither
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
- The audit's own statements are left out only when an sql-auditor build with
  the session marker ran them. After an upgrade, a week of older audits can
  remain in the window, and nothing distinguishes them from the workload. A
  client session that uses `DATEFIRST 3` is left out as if it were the audit.
- The store follows its database across a failover: rows of the primary role
  from before a failover inside the window were run by the other instance,
  under its own threshold and scheduler count. An availability group secondary
  is not read at all.

## The cost, measured

On the lab, 4 October 2026, a prototype of the selection and the read loop
(not the final file: it pins on `is_parallel_plan = 1 OR max_dop > 1` and
does not exclude the audit), run twice per database, the first run cold:

| Store | Plans in window | Pinned | MB read | Selection | Read loop |
| --- | --- | --- | --- | --- | --- |
| `ZzAvgDop` | 24 | 12 | 0.10 | 49 to 62 ms | 16 to 28 ms |
| `ZzAvgDop2` | 41 | 7 | 0.08 | 41 to 99 ms | 16 to 28 ms |
| `LABSTORE_A` | 650 | 3 | 0.05 | 45 to 105 ms | 28 to 37 ms |
| four other lab stores | 95 to 116 | 0 to 4 | up to 0.07 | 29 to 95 ms | 0 to 64 ms |

So 29 to 148 ms per database on the lab, selection and read loop together.
Claude measured the final shape of the aggregation (runtime rows joined to the
interval, the plan, the query, the context and `sys.objects`, grouped by plan,
`MAXDOP 1`) at 89 to 90 ms on `LABSTORE_A`'s 9 086 runtime rows, and 81 to
139 ms on a store of its own. What a lab cannot say is the number of parallel plans
a client store holds in a week, which decides whether the cap or a budget is
reached, nor the size of the runtime view on a busy store, which decides the
selection's price; `selection.duration_ms`, `window.runtime_rows`,
`examined.duration_ms`, `examined.bytes_read`, `examined.largest_plan_bytes`
and `examined.stopped_by` put the client's own figures in every archive. The
replica filter and the `sp_executesql` around the aggregation have not been
timed; they change the statement's text, not the rows it reads.

The row that "What the default run costs a large instance" in
`docs/dba-guide.md` gains, as it is to be written there:

| What | Where | What it actually does |
| --- | --- | --- |
| Parallel plan costs read out of the Query Store | `80.workload/043.query-store-parallel-cost.sql` | in each database whose store is not off, and which is not an availability group secondary, two parts. First one aggregation of the last seven days of `sys.query_store_runtime_stats`, grouped by plan into a temporary table; it reads no plan text and has no budget of its own, so its cost follows the number of runtime rows in the window: 29 to 105 ms on six lab stores of up to 9 086 rows. Then the text of up to 1 000 parallel plans, those with the most parallel CPU first, copied a hundred at a time into a table variable and searched for one attribute, never converted to `xml`: about 20 ms per MB of plan text on one 2025 lab build. The read stops before the next hundred once 100 MB have been read or 10 s have passed, so one hundred plans can pass either, by as much as their size; the 120-second timeout is the hard bound of the whole file. The store's shared lock is released between chunks. The root projects `selection.duration_ms`, `window.runtime_rows`, `examined.duration_ms`, `examined.bytes_read`, `examined.largest_plan_bytes` and `examined.stopped_by`, so the cost on your instance is in the archive. Nothing that names a query leaves the server. |

## What is not in scope

- Correcting 042 for point 4, and teaching 042 the session marker. Both are
  findings about 042, recorded here because this measurement found them.
- Teaching the marker, and the replica filter, to the other Query Store
  collectors (020 to 029). They count the audit's statements, read the
  secondary's copy of the primary's store, and, with Query Store for
  secondaries, mix the roles. That is true today, before 043.
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
   (1001)`; walking the loop with any other variable than `@chunk`. A static
   count cannot show that the loop reads by the chunk it declares; test 6 does.
2. The marker, statically. `@audit_datefirst` in 043 equals
   `collect.AuditDateFirst`, and the reset batch built by the runner's
   function begins with `SET DATEFIRST` followed by that value. No file of the
   embedded corpus, comments stripped, writes `DATEFIRST` (`SET LANGUAGE`,
   `SET DATEFIRST`) or reads it (`DATEPART` or `DATENAME` with `week`, `wk`,
   `ww`, `weekday` or `dw`, `DATETRUNC` with `week`, `@@DATEFIRST`); and a
   script written to a temporary `--queries-dir` with one writer and one
   reader gets the manifest warning, naming it. Mutations: changing either
   constant alone; dropping the `SET` from the batch; adding `SET LANGUAGE
   French;` to any collector (the writer, which breaks 043's exclusion);
   adding `DATEPART(dw, GETDATE())` to any collector (the reader, which
   changes that collector's answer); removing the warning.
3. No text leaves. Live, on the database of test 5: the keys of the root and
   of every band row equal the lists of "The collector" exactly, and the
   literal `ZZ043_PLANTED_TEXT`, written into a parallel query of the planted
   workload, appears nowhere in the document. Mutation: adding `p.plan_id` to
   the projection.
4. The session marker, live, through the runner's own path. With `runUnit`
   and `recycleConn`, as `TestLiveAUnitLeavesNoTempTable` uses them, four
   units in a row on one connection, each a script whose root is `SELECT
   @@DATEFIRST AS df`, the third preceded in its own batch text by `SET
   LANGUAGE French;`, and `recycleConn` after each: the roots read 3, 3, 1
   and 3. Mutations: dropping the `SET` from the reset batch (all four read
   the login's value, 7 for `us_english`); setting it once after the login
   instead of in the reset batch (the second and fourth read 7, since the
   pool's reset restores the login's settings). The third value asserts the
   limit this document states; if the engine stops resetting `DATEFIRST` on
   a language change, the test says the limit has gone.
5. The bands, live, on the lab. A test creates a database with the store in
   capture mode ALL and one-minute intervals. From a connection opened with
   `sql.Open`, which never runs `ResetSession` and so is unmarked, it creates
   six heaps `dbo.Ladder1` to `dbo.Ladder6` (`id bigint`, `pad char(100)`) of
   20 000, 50 000, 100 000, 200 000, 500 000 and 2 000 000 rows, runs `SELECT
   COUNT_BIG(*) FROM dbo.LadderN WHERE id % 7 = 3` on each, then the same
   with `OPTION (MAXDOP 2)`; creates and runs `dbo.TwoStatements`, whose body
   is `SELECT pad FROM dbo.Ladder1 WHERE id = 1;` then `SELECT COUNT_BIG(*)
   FROM dbo.Ladder6 WHERE id % 7 = 4;`; and runs, in one batch, 041's
   `CREATE TABLE #savings (object_name sysname, schema_name sysname, index_id
   int, partition_number int, size_current_kb bigint, size_requested_kb
   bigint, sample_current_kb bigint, sample_requested_kb bigint);` then
   `INSERT INTO #savings EXEC sys.sp_estimate_data_compression_savings
   @schema_name = N'dbo', @object_name = N'Ladder6', @index_id = NULL,
   @partition_number = NULL, @data_compression = N'PAGE';`. Then, through
   `runUnit`, it runs a unit made of the parallel aggregate on `dbo.Ladder6`
   and the same `#savings` batch, then 043. It asserts, in this order:
   `window.serial_plans_dop_above_1` is at least 1, and stops there if not,
   since the rest would test nothing; that count equals the number of serial
   plans with `max_dop > 1` the test finds itself under the default context,
   so the audit's call is not in it; `excluded.audit_plans` is at least 2;
   `excluded.other_replicas_executions` is 0 and not NULL on SQL Server 2025
   (and NULL below 2022, on a lab that has one); the bands equal those
   computed independently from `sys.query_store_plan` through the xml path on
   the first costed statement element, over the parallel plans of the default
   context; no plan of the test database has more than one costed statement
   element; `parallel_executions_min` is at most `parallel_executions` in
   every band; and `examined.share_of_parallel_cpu_pct` equals the bands'
   summed `parallel_cpu_s` over `window.parallel_cpu_s` within rounding. The
   test drops the database. Mutations, each of which must make it fail:
   selecting on `max_dop > 1` instead of `is_parallel_plan = 1`; removing the
   exclusion of `@audit_datefirst`; a `COL_LENGTH` or `OBJECT_ID` that never
   finds the replica column or view (the count turns NULL); a replica
   predicate that keeps the wrong groups (the bands empty out); computing the
   share from anything but the bands.
6. The stop rules, live, on the store of test 5 with `@chunk` rewritten to 2.
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
7. Stores that are not read, and a store with no parallel work. Through
   `runUnit`: in `master`, which has no row in the options view, one root row
   with `state.actual` NULL, `state.not_read_because` `no store`, NULL counts
   and seven empty bands; in a database whose store held parallel plans and
   was then set OFF, the same with `state.actual` `OFF` and `off`; in a
   database whose store is on and holds only serial work in the window, 0
   counts and `examined.share_of_parallel_cpu_pct` NULL. None of the three
   raises an error. The "never enabled" case of the second version cannot be
   built on SQL Server 2022 and later, where a new database inherits
   `model`'s store (Claude, point 6), so `master` stands for it. Mutations:
   reading the OFF store into the bands (which is why it must hold parallel
   plans before it is switched off); dividing without `NULLIF` (error 8134
   fails the third case).
8. The guide. `TestTheGuideCountsTheTimeoutTiersOfTheCorpus` passes with the
   `120 s` row at 30; it is the existing test, run after the guide is edited.

What no test here covers, said so that nobody believes otherwise: the
secondary branch and the rows of another replica role (the lab has no
availability group); `avg_dop` above the range of a `decimal` (the anomaly
cannot be produced); a plan leaving the store between the pin and its chunk;
and the selection's cost on a large runtime view.

The live tests create their databases under a prefix of their own, and the
existing `TestLive...` suite's `ZzDroppedDuringRun` collides on a shared
instance (codex neutral, first panel); that is a matter for the suite, not for
this file.

## Open questions

- Is the session marker worth what it changes? It is the only marker found
  that reaches nested work (system procedures, triggers, functions, the
  statistics queries the audit causes), and in this version it costs no round
  trip. Its price: every collector of every run runs under `DATEFIRST 3`; a
  `--queries-dir` script that reads the week computes it from Wednesday, and
  one that sets a language stops being recognised for the rest of its batch,
  both with a warning; a client session that uses `DATEFIRST 3` is counted
  as the audit's. The alternative is to have no marker and to keep the audit's
  statements in the window: every result set of the corpus carries `MAXDOP 1`,
  so most of them are serial and never reach a band; what would remain are
  the parallel plans the audit causes without writing them, such as the
  internal statements of `sp_estimate_data_compression_savings`, and the
  serial anomalies of point 4, which `window.serial_plans_dop_above_1` would
  then count with the workload's. How much of a band that residue weighs has
  not been measured; a lab run of the whole corpus followed by 043 without
  the exclusion would say. The other alternative found, excluding only the
  intervals of the current run, loses the client's workload of those minutes
  and keeps every earlier audit.
- Is seven days the right window, or thirty, the store's default retention,
  at the price of mixing plans compiled under a threshold since changed?
- Is 10 s the right time budget for a collector that runs on every default
  run, and should the selection, which has no budget, get one, for example
  a count of runtime rows above which the store is reported and not read?
- Should 042 be corrected for serial statements whose `max_dop` exceeds 1, and
  taught the session marker, so that both files define parallel and the
  workload the same way?
- Should the other Query Store collectors (020 to 029) learn the marker and
  the replica filter, and skip availability group secondaries?
- Should 020 and 029 keep trigger and multi-statement function queries in
  their totals, after point 7?
- Is 043 the right number, or should the Query Store family keep a range of
  its own and the file be renumbered there?
- Should serial plans above the current threshold be counted (they cannot
  exist without a hint or a construct that forbids parallelism), as the
  signature of the opposite misconfiguration?

The first draft's questions on a `.env` key and on `--all` are closed by the
decision to run by default.

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
reading found nothing. This version then measured, in `ZzAvgDop3`, the marker
placed in the reset batch across a recycle and a `SET LANGUAGE`, test 5's
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
the anomaly of test 5 reproduces from both sessions (Claude, and here); no
language has `datefirst` 3 and no system procedure the corpus calls reads it
(agy neutral, Claude); the `120 s` tier is 29 today; `--profile space`
excludes a file with no `@profiles`; the OFF predicate and the denominator
fix hold (codex, both prompts); the suite and the live suite pass on the
untouched tree (codex, both prompts, Claude); and the aggregation's join to
the plan view costs 2 to 14 ms, so it does not decompress plans (Claude).
