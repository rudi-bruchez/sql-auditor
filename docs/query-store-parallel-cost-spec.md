# Parallel cost from the Query Store

Status: proposed on 4 October 2026, not implemented. Written on 4 October 2026
from the decision taken that day to read the parallel costs out of the Query
Store behind an opt-in, with a window and caps of its own, and before the panel
of five readers that every specification of this repository goes through.
Nothing in the tree has changed yet.

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

The decision of 4 October 2026: yes, as a collector of its own, behind an
option that is off by default, with its own window and its own caps. This
document settles which plans are read, how their cost is found, how much a
database may cost, the option, the shape of the output, and the tests.

## What the store holds, from the documentation and from the lab

From Microsoft Learn:

- `sys.query_store_runtime_stats` carries, per plan, interval and
  `execution_type`, `count_executions`, `avg_cpu_time` (microseconds) and the
  five DOP columns `avg_dop`, `last_dop`, `min_dop`, `max_dop`, `stdev_dop`,
  from SQL Server 2016 (13.x). Its remarks warn that the DOP columns can
  "report large numbers" on systems with many processors, "in scenarios where
  the query uses user defined functions", and call it a reporting issue.
  https://learn.microsoft.com/en-us/sql/relational-databases/system-catalog-views/sys-query-store-runtime-stats-transact-sql
- `sys.query_store_plan` carries `query_plan` (`nvarchar(max)`, "Showplan XML
  for the query plan"), `is_parallel_plan` and `last_execution_time`, from
  SQL Server 2016. It has no cost column.
  https://learn.microsoft.com/en-us/sql/relational-databases/system-catalog-views/sys-query-store-plan-transact-sql
- The estimated cost of a statement is the `StatementSubTreeCost` attribute of
  its statement element (`StmtSimple`, `StmtCond`, `StmtCursor`) in the
  showplan schema, the same attribute 042 reads.
  http://schemas.microsoft.com/sqlserver/2004/07/showplan/
- Both views require `VIEW DATABASE STATE`, and `VIEW DATABASE PERFORMANCE
  STATE` from SQL Server 2022.

Measured on the lab, SQL Server 2025 CU7 (17.0.4065.4, 22 schedulers, Linux,
cost threshold 5, MAXDOP 0), 4 October 2026, in a database `ZzAvgDop` created
for it with the Query Store in capture mode ALL and dropped afterwards, and in
the existing store of a lab database called `LABSTORE_A` here, read from `master` with three-part
names so that the reads were not captured into that store:

1. The stored plan is the plan of one statement. A procedure of two statements
   (a key lookup, then a parallel aggregate) gave two plans, each holding one
   statement element. The first trap of 042, the batch plan whose first
   statement is a neighbour, does not exist here.
2. The cost found by a text search is the cost of that statement. On the 22
   plans of `ZzAvgDop` the first `StatementSubTreeCost="` of the text and
   `(//StmtSimple/@StatementSubTreeCost)[1]` read as xml agreed 22 times. On
   the 927 plans of `LABSTORE_A`, the text search found the cost of the
   first statement element 926 times and nothing once (a plan with no
   statement); the `//StmtSimple` path missed ten `StmtCond` plans that the
   text search read correctly. That is the result 042 recorded on the cache.
3. The cost of a parallel plan is the parallel cost, as in the cache: in
   `ZzAvgDop`, under a threshold of 5, the scans under 4.83 stayed serial and
   the parallel ones began at 6.32.
4. `is_parallel_plan` and `max_dop` disagree, and `max_dop` is the wrong one.
   In `LABSTORE_A`, six plans with `max_dop > 1` in their runtime rows
   had `is_parallel_plan = 0` and a cost of 0.0133. They are `INSERT ... EXEC`
   statements (one of them `INSERT INTO #savings EXEC
   sys.sp_estimate_data_compression_savings`) and `INSERT INTO @t` fed by
   catalog functions, and the degree they report is the degree of the work
   nested under them: 26 and 27, on an instance of 22 schedulers. This is the
   anomaly the documentation describes. A plan that is not parallel cannot be
   made serial by a threshold, so this collector selects on
   `is_parallel_plan` and never on `max_dop`.
5. Plans with `is_parallel_plan = 1` and no statement exist: the two
   `SELECT StatMan(...)` queries of an automatic statistics update in
   `ZzAvgDop`, stored with an empty `<Statements/>`. They ran at degree 16.
   They have no cost and belong to the unknown band.

Point 4 also concerns 042, which calls a statement parallel when
`sys.dm_exec_query_stats.max_dop > 1`. The same day, on the same lab, 25
statements of the cache had `max_dop > 1`; 15 of them had no parallel operator
in their plan fragment (no `Parallel="true"`), carried 351 of the 379
executions and 7.1 of the 14.3 seconds of "parallel" CPU, and are the same
`INSERT ... EXEC` and `INSERT INTO @t` statements, most of them sql-auditor's
own collectors. 042's header explains a parallel statement under the current
threshold by the parallel cost being lower than the serial one, or by a plan
compiled under a lower threshold; this is a third cause, and on the lab the
largest. It is outside this document's scope and is listed under the open
questions.

## What is read

The plans of the database's Query Store that are parallel (`is_parallel_plan =
1`) and executed inside the window, and for each of them: its estimated cost,
read from `query_plan`; and over the window, from
`sys.query_store_runtime_stats`, its executions, its CPU, and its degrees.

Serial plans are not read. The question the bands answer is what a higher
threshold would make serial, and only a parallel plan can change; a serial
plan's cost would cost one plan read each and inform nothing
`seuil_parallelisme.py` computes, since every figure it derives is a sum of the
`parallel_*` columns. The serial side is still reported, as totals over the
window taken from the runtime rows alone (executions and CPU of every plan,
then of the parallel plans), which cost no plan read: that is what the reader
needs to know what share of the database's CPU the bands describe.

Execution types are summed, as in `029.query-store-load-profile.sql`: an
execution stopped by a timeout or an error used its CPU and its workers.

Nested queries (a query whose `object_id` is a scalar or table-valued function
or a trigger) are kept in the bands, because a parallel plan inside a trigger
is a plan the threshold acts on, and counted apart in the root with their CPU,
because 029 measured that their CPU is also counted in the calling statement.
The predicate is 029's, verbatim.

### The cost, found as text after the plan is staged in a column

No plan is converted to xml. The cost is found as 042 finds it: the first
`StatementSubTreeCost="` of the text, read up to the next double quote, cast
with `TRY_CAST(... AS float)`. Point 2 above is the evidence that this is the
measured statement's own cost.

The text is first copied into a table variable column, one chunk at a time,
and searched there. Measured on `LABSTORE_A` (927 plans, 71.5 MB of
plan text, 77 KB on average), twice each:

| Form | Seconds | ms per MB |
| --- | --- | --- |
| `CHARINDEX` on `sys.query_store_plan.query_plan` directly | 4.08 to 4.13 | 57 |
| the same with `COLLATE Latin1_General_BIN2` | 3.66 to 3.69 | 51 |
| `TRY_CAST` to xml into a column, then `.value()` | 3.17 + 0.08, 3.14 + 0.03 | 44 |
| copy into `nvarchar(max)` column, then `CHARINDEX` on it | 0.72 to 0.77, then 0.82 to 0.84 | 22 |

And on `LABSTORE_B` (126 plans, 24.0 MB, 190 KB on average): copy 0.25 to
0.37 s, search 0.19 to 0.20 s, 19 to 24 ms per MB; the cast into an xml
column took 0.89 to 0.91 s on its own, 37 ms per MB.

The search on the view's column reads the store once per reference to
`query_plan` (two `CHARINDEX` and a `SUBSTRING`), which is why it is slower
than a conversion. Staged, it is half the cost of the cast, and it has the
property that made 042 drop the cast: a plan nested deeper than the 128 levels
of the xml type is searched like any other.

So the cost to bound is the copy and the search, about 20 ms per MB of plan on
the lab, and not a conversion. The brief of this document assumed one
conversion per plan; the measurement removes it.

### The selection, pinned before any plan is read

The parallel plans executed in the window are ranked by their CPU in the
window, highest first, and the first `@cap` are pinned into a table variable
before any `query_plan` is read, as 027 and 042 do. The rank by CPU keeps,
when a cap or a budget cuts, the plans that carry the parallel work; the root
then says what share of the parallel CPU of the window the plans read carry,
a figure computed from the runtime rows alone and therefore exact whatever was
cut.

042 noted that the band a decision turns on, [5, 25), is made of cheap
statements that a ranking by CPU reaches last. Here the ranking is over
parallel plans only, which are few: 12 of 24 plans in `ZzAvgDop`, 3 of 622 in
the window of `LABSTORE_A`. The cap is there for the store that holds
thousands of them, and `truncated` says when it bit.

## The window and the caps

All four are constants declared at the head of the file and projected in the
root, following 027 ("THE CAP AND THE BUDGET ARE CONSTANTS AND NOT OPTIONS"):
the corpus has no directive for a per-collector parameter, and none of these
numbers has yet been asked to change.

| Constant | Value | Bounds |
| --- | --- | --- |
| `@window_days` | 7 | runtime intervals whose `start_time` is within 7 days of `SYSDATETIMEOFFSET()` |
| `@cap` | 1 000 | parallel plans pinned per database |
| `@budget_mb` | 100 | MB of plan text read per database |
| `@budget_ms` | 30 000 | ms spent in the read loop per database |

The window is seven days because a threshold is a property of the workload as
it runs now, and a week holds its weekly cycle (the weekend batch, the Monday
peak) without reaching back past a change of threshold or of MAXDOP made a
fortnight ago, whose plans would otherwise sit in the bands beside the current
ones. It is not `QUERY_STORE_DAYS`, which configures the extraction of
`--query-store-detail` and whose `.env.example` paragraph says it matters only
there; tying the two would make a setting chosen for one change the other. The
store keeps thirty days by default, so a week is inside it; when the store
holds less, `window.oldest_interval` says from when the bands really start.

The cap is 042's window, 1 000.

The byte budget is 100 MB, half of 027's 200 MB, because the price per MB here
is a fifth of 027's (about 20 ms against 94 to 112 ms): 100 MB costs about two
seconds on the lab, eight at four times that rate, and the budget is not what
is expected to stop a read. It stops the read as 027's does: plans are taken in
rank order, each one's `DATALENGTH` is added, and the loop stops at the first
plan that would start past the budget, so the plans read are the top of the
ranking without a gap; the plan that crosses the line is read whole.

The time budget is new, and the decision asked for it ("temps par base").
027 has none and relies on its `@timeout`, which, when it expires, returns
nothing for the database. Here the loop does not start a new chunk once
`@budget_ms` has passed since it began, so a slow instance gets a partial,
counted answer instead of an empty one. Thirty seconds is a quarter of the
`@timeout` proposed below, leaving the selection, the bands and a slower
instance room inside it.

The read is in chunks of 100 plans, for 027's reason: each statement that
reads `sys.query_store_plan` holds a shared QDS lock on the database, and a
chunk releases it, so an option change on the store waits seconds and not the
length of the read.

## The option

`--query-store-parallel-cost`, flag `query_store_parallel_cost`
(`FlagQueryStoreParallelCost` in `collect/collect.go`), entered in
`KnownFlags` and in `CostFlags`.

A cost option and not a disclosure option. What leaves the server is the
database name, counts, sums, degrees and the band boundaries; no query text,
no plan, no query or plan id, no object name. The plan text is read on the
instance and dropped with its chunk, as 042 does with the cache. The file
declares no `@discloses`. The option exists because of what the read costs the
instance per database, which is what `CostFlags` means ("off by default for
cost rather than for disclosure").

No environment variable and no `.env` key. None of the twelve opt-ins has one:
`knownKeys` in `collect/config.go` is a closed set of connection, path,
selection and Query Store window settings, and the opt-ins are command-line
flags, the wizard's third screen, and `--all`. An option that alone could be
left on in a file would break that symmetry for no reason the decision gave.
This departs from the brief, which asked for one; the open questions keep it.

Consequences, each a place the implementation changes:

- `cmd/sql-auditor/main.go`: the flag, its help line, and
  `collect.FlagQueryStoreParallelCost: c.all || c.queryStoreParallelCost` in
  the `Flags` map. `--all` turns it on, as it turns on the two other cost
  options; the README's "all eleven options" becomes twelve, "the two off for
  cost" three.
- `tui/state.go` `flagOrder` and `tui/render.go` options: a row "query store
  parallel cost", described "reads the plan text of up to 1 000 parallel
  plans per database, at most 100 MB or 30 s each", after page density.
- `collect/collect.go` `queryStoreUnits`: `QUERY_STORE_DB_INCLUDE` narrows
  this collector as it narrows the writers and `025`. The multiplication by the
  number of databases is the nuisance the decision named, and the operator
  already has a setting that says which databases' stores may be read deeply.
  The condition becomes `s.Writer == "" && s.RequiresFlag !=
  FlagQueryStoreCompare && s.RequiresFlag != FlagQueryStoreParallelCost`, and
  `.env.example`'s "These only matter with --query-store-detail" names this
  option too.
- The manifest's `config` gains `query_store_parallel_cost` through the
  existing loop over `KnownFlags`.

What `check` announces needs no new code: `PlannedDuration` counts a unit as
costly when its script's `RequiresFlag` is in `CostFlags`, so the line becomes,
for eight databases with only this option on:

```
Duration, a ceiling and not an estimate:
  8 databases; costly collectors on: 80.workload/043.query-store-parallel-cost.sql;
    at most 16m00s (960 s) if every one of their 8 units runs to its @timeout
```

and without it, the parenthesis lists three options instead of two.

## The collector

`queries/80.workload/043.query-store-parallel-cost.sql`, beside 042 whose
reading it repeats on another source; the 02x range of the Query Store
collectors is full.

```
-- @scope:         database
-- @resultsets:    root:object, bands:array
-- @permissions:   CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:       120
-- @min_version:   13
-- @requires_flag: query_store_parallel_cost
```

The permissions are 029's, which reads the same views and `sys.objects` for the
same nested-query predicate. The floor is SQL Server 2016, where every column
read exists; it has not been measured on 2016.

The root comes from a `LEFT JOIN` from `sys.databases` to
`sys.database_query_store_options`, as in 026 and 029, so a database whose
store was never enabled still returns its row. Every state but OFF is read, as
in 021: a READ_ONLY store still holds its history.

### The root

| Column | Meaning |
| --- | --- |
| `database` | `DB_NAME()` |
| `collected_at` | `SYSDATETIMEOFFSET()` |
| `state.actual`, `state.capture_mode` | from `sys.database_query_store_options` |
| `window.days` | `@window_days` |
| `window.oldest_interval`, `window.newest_interval` | the intervals actually inside the window |
| `window.executions`, `window.cpu_s` | every plan, every execution type, inside the window |
| `window.parallel_plans` | plans with `is_parallel_plan = 1` executed in the window |
| `window.parallel_executions`, `window.parallel_cpu_s` | the same plans' executions and CPU |
| `window.serial_plans_dop_above_1` | plans with `is_parallel_plan = 0` and a runtime row with `max_dop > 1` (point 4), counted and never banded |
| `window.dop_above_schedulers` | runtime rows of parallel plans with `max_dop` above `scheduler_count` of `sys.dm_os_sys_info` (the documented anomaly), counted and kept |
| `nested.parallel_plans`, `nested.parallel_cpu_s` | parallel plans of functions and triggers, kept in the bands |
| `cap`, `budget.bytes`, `budget.ms` | the constants |
| `examined.plans` | plans pinned |
| `examined.plans_read` | plans whose text was read |
| `examined.bytes_read`, `examined.duration_ms` | the read loop's bytes and time |
| `examined.share_of_parallel_cpu_pct` | CPU of the plans read over `window.parallel_cpu_s` |
| `examined.stopped_by` | `cap`, `bytes`, `time`, or NULL when every eligible plan was read |
| `truncated` | 1 when `examined.stopped_by` is not NULL |
| `unknown.no_plan`, `unknown.no_cost` | why the unknown band is unknown: the plan left the store between the pin and its chunk, or its text holds no statement cost |

`examined.stopped_by` is `cap` when the pin found `@cap + 1` eligible plans
(027's evidence rule, not an equality), and otherwise `bytes` or `time` when the
loop stopped with plans of the pin unread; when both a budget and the cap bite,
the budget is named, because it is the one that left pinned plans unread.

### The bands

Seven rows always, empty ones included, with 042's boundaries and 042's column
names, so that `seuil_parallelisme.py` reads a band of either file with the
same functions (`tranches`, `serialisable`, `inconnue`): `band`, `cost_from`,
`cost_to`, `statements`, `parallel_statements`, `executions`,
`parallel_executions`, `cpu_s`, `parallel_cpu_s`, `max_dop`. Their meaning
here:

| Column | Meaning in this file |
| --- | --- |
| `statements` | parallel plans read in this band (a plan, not a cache entry: a query with two parallel plans counts twice, each in the band of its own cost) |
| `parallel_statements` | those of them with at least one runtime row in the window where `max_dop > 1` |
| `executions`, `cpu_s` | their executions and CPU in the window |
| `parallel_executions`, `parallel_cpu_s` | executions and CPU of the runtime rows where `max_dop > 1`: an upper bound, as in 042 |
| `max_dop` | the highest `max_dop` of the band |

and three columns 042 cannot have:

| Column | Meaning |
| --- | --- |
| `parallel_executions_min`, `parallel_cpu_s_min` | the same over the rows where `min_dop > 1`: every execution of such a row ran parallel, so this is a lower bound |
| `avg_dop` | `SUM(avg_dop * count_executions) / SUM(count_executions)` over the band's rows where `max_dop > 1`, decimal(9,2) |

The two bounds are the point of reading the store rather than the cache. A
runtime row is one plan, one interval of a minute to a day, one execution type;
when `min_dop` and `max_dop` straddle 1 in a row, the store does not say how
many of its executions ran parallel, and the truth lies between the two
columns. 042 can only give the upper one.

### What the analysis does with it

Out of this repository's scope, stated so the reviewers can check the shape.
`seuil_parallelisme.py` gains a reading of 043 beside 042: it sums each band
over the databases of the archive (the per-database files share boundaries,
so the sum is exact), takes `window.parallel_cpu_s` summed as its total, and
replaces the thin-cache test (`examined.statements < examined.cap`) with the
store's own: no store, a store off, a window shorter than `window.days`, or
`truncated`. It prints both sources side by side; neither replaces the other,
since the cache covers databases whose store is off and the store covers what
the cache evicted.

## The limits a reader must keep in view

- Only databases whose store is on, and only what it captured. Under capture
  mode AUTO (the default from SQL Server 2019) a query is captured once it
  passes the store's thresholds of executions or CPU, so a cheap and rare
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

## The cost, measured

On the lab, 4 October 2026, a prototype of the selection and the read loop
(not the final file), run twice per database:

| Database | Plans in window | Parallel plans pinned | MB read | Selection | Read loop |
| --- | --- | --- | --- | --- | --- |
| `ZzAvgDop` | 24 | 12 | 0.10 | 49 to 62 ms | 16 to 28 ms |
| `LABSTORE_A` | 622 | 3 | 0.05 | 40 to 45 ms | 16 to 24 ms |

The selection is one aggregation of `sys.query_store_runtime_stats` over the
window, the same pass 029 makes over the whole history (270 ms for 41 052 rows
in its own measurement). The read loop is bounded by the budgets above; its
worst case on the lab rate is the 100 MB budget, about two seconds, and the
time budget caps it at thirty on any instance. `examined.duration_ms` and
`examined.bytes_read` put the client's own rate in every archive.

What a lab cannot say is the number of parallel plans a client store holds in
a week, which is what decides whether the cap or a budget is ever reached; the
first client archives answer it through `examined.stopped_by`.

## What is not in scope

- Correcting 042 for point 4. It is a finding about 042, recorded here because
  this measurement found it; whether 042 should require a parallel operator in
  the fragment is a decision of its own.
- Serial plans' costs, and any band of serial work.
- A per-collector parameter directive. The constants stay constants.
- Any judgement: no candidate threshold appears in the file.

## Tests

1. Corpus lint. `testdata/corpus.txt` lists the new path; the directive lines
   are exactly the six above; `@requires_flag` names a flag `KnownFlags`
   knows. Removing the flag from `KnownFlags` makes the lint fail on that file.
2. Cost classification. `CostFlags[FlagQueryStoreParallelCost]` is true, and
   `PlannedDuration` with only this flag on and three databases reports
   `CostlyUnits` 3 and `CostlyCeiling` 360 s. `TestAllTurnsOnEveryOptIn` (or
   its equivalent) passes with the flag added and fails without the
   `c.all ||`.
3. No disclosure. The file's projected column names contain no `text`,
   `plan`, `query_id` or `plan_id`, checked by the existing disclosure guard
   or by a test that runs the file live and asserts that no value of either
   result set is longer than 128 characters except `database`.
4. `QUERY_STORE_DB_INCLUDE`. With the setting naming one of two databases,
   the other gets a skip with `skipNotInQueryStoreInclude` for this script.
5. Live, on the lab. A test creates a database with the store in capture mode
   ALL, runs a ladder of aggregates of known cost (as `ZzAvgDop` did: scans
   from 20 000 to 2 000 000 rows, half under `OPTION (MAXDOP 2)`), a
   procedure whose second statement is parallel, and one `INSERT ... EXEC`,
   then runs the collector and asserts: the bands equal those computed
   independently from `sys.query_store_plan` through the xml path on the
   first statement element; the `INSERT ... EXEC` plan is counted in
   `window.serial_plans_dop_above_1` and in no band; `parallel_executions_min`
   is at most `parallel_executions` in every band; and the test drops the
   database. Making the selection read `max_dop > 1` instead of
   `is_parallel_plan = 1` must make this test fail.
6. The budgets. With `@budget_mb` lowered by the test to cut inside the pin,
   `examined.stopped_by` is `bytes`, `examined.plans_read` is the expected
   count computed from `DATALENGTH` in rank order, and the plans read are the
   top of the ranking. With `@budget_ms` at 0, `stopped_by` is `time` after
   the first chunk. With `@cap` at 2 on a store of three parallel plans,
   `stopped_by` is `cap` and with `@cap` at 3 it is NULL.
7. A store that is off, and a database whose store was never enabled, each
   return one root row with zero counts and seven empty bands, and no error.

## Open questions

- Should the option have a `.env` key after all, as the brief asked, which
  would make it the only opt-in with one?
- Is seven days the right window, or thirty, the store's default retention,
  at the price of mixing plans compiled under a threshold since changed?
- Should 042 be corrected for serial statements whose `max_dop` exceeds 1
  (point 4), and should that wait for this collector, so that both files
  define parallel the same way?
- Should `--all` turn this option on, as it does the two other cost options,
  given that it multiplies by the number of databases?
- Is 043 the right number, or should the Query Store family keep a range of
  its own and the file be renumbered there?
- Should serial plans above the current threshold be counted (they cannot
  exist without a hint or a construct that forbids parallelism), as the
  signature of the opposite misconfiguration?
