# Statistics usage — specification

**Date:** 21 September 2026, after a second-pass audit whose four requested
subjects included statistics maintenance.
**Status:** implemented on 21 September 2026 as
`80.workload/027.query-store-stats-usage.sql`. Section 7 records what building
it changed; the rest of this document is left as written, because a spec
rewritten to agree with the code it produced stops being evidence of anything.

The audit that produced this could say which statistics were **stale** and
could not say which ones were **used**. Those are different questions and only
the first is collected today. What follows specifies the second, and states
plainly what it will still not answer, because that limit decides how the
result may be read.

## 1. There is no usage DMV, and that is the whole problem

Indexes have `sys.dm_db_index_usage_stats`. Statistics have no equivalent, in
any version. Microsoft's documented answer to "which statistics did the
optimizer use" is to read the execution plan: the `OptimizerStatsUsage` element
carries one `StatisticsInfo` child per statistics object loaded during
compilation.

```xml
<StatisticsInfo
    Database="[SALESDB]"
    Schema="[dbo]"
    Table="[Orders]"
    Statistics="[IX_Orders_CustomerId]"
    ModificationCount="0"
    SamplingPercent="100"
    LastUpdate="2025-09-07T15:32:16.89" />
```

Source: *Determine which statistics the Query Optimizer used*, in the
Statistics article on Microsoft Learn. The same page names four ways to reach
that element, and the fourth is the one that matters here: reading a plan
already stored in the Query Store, through `sys.query_store_plan`.

So the data exists on the instance, in a place the corpus already queries in
six collectors, and nothing reads it.

## 2. What the archive holds today, measured

Measured on one audited instance, September 2026, to size the gap rather than
assume it:

| | |
|---|---|
| `.sqlplan` files written by `021.query-store-detail` and `041.plan-cache-plans` | 169 |
| of those carrying an `OptimizerStatsUsage` element | 52 |
| distinct statistics named across all of them | 84 |
| statistics objects on the instance | 20 724 |

Eighty-four out of twenty thousand. The element is already reaching the archive
by accident, as a by-product of writing plan files, and at a coverage of 0.4 %
it supports no conclusion at all.

The same instance held 6 515 plans in the Query Store of a single database. The
gap is not the data. It is that the corpus rescues fifty plans per database as
files and leaves the rest unread.

## 3. What the collector does

slug: stats-usage-absent

`80.workload/027.query-store-stats-usage.sql`, database scope. The Query Store
family already runs from 020 to 026, so 027 is the next free number — check it
is still free before creating the file, since two of those arrived while this
spec was being written.

It shreds `OptimizerStatsUsage` out of `sys.query_store_plan.query_plan`
server-side with `.nodes()`, and projects one row per distinct statistics
object, aggregated across plans. **It does not return plan XML.** That is the
entire point: the work happens on the instance, and what travels is a few
thousand short rows instead of gigabytes of XML.

```
-- @scope:       database
-- @resultsets:  root:object, statistics_used:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     300
-- @min_version: 14
```

`@min_version: 14`. `OptimizerStatsUsage` appears in showplan from SQL Server
2017 and from 2016 SP2; gating at 14 covers the common case without asserting a
service-pack floor the corpus cannot check. **This has not been measured on a
2016 SP2 instance.** Until it is, a 2016 instance is skipped with a reason
rather than returning a silently empty array, which is the failure mode section
5 of `collection-gaps-spec.md` exists to prevent.

No `@discloses`. The projection carries object names and statistic names, never
query text and never the plan. This is the one Query Store collector that does
not need `@discloses: query_text`, and keeping it that way is what lets it run
on a client that refuses text disclosure.

### The projected row

| Field | Source | Why |
|---|---|---|
| `schema`, `table`, `statistic` | `StatisticsInfo/@Schema`, `@Table`, `@Statistics` | the identity, matching `090.statistics` |
| `plans` | `COUNT(DISTINCT plan_id)` | how many plans loaded it |
| `queries` | `COUNT(DISTINCT query_id)` | the count that matters; one query recompiled fifty times is not fifty users |
| `last_compile` | `MAX(p.last_compile_start_time)` | how recently the optimizer wanted it |
| `min_sampling_percent` | `MIN(@SamplingPercent)` | the compile that saw the worst histogram |
| `max_modification_count` | `MAX(@ModificationCount)` | how stale it was at the worst compile |

The last two are the reason to prefer this over a bare usage flag. They say
that a statistic was **used while stale**, which is a stronger finding than
either fact alone: nobody needs to refresh a stale statistic that no plan
loads, and everybody needs to refresh a stale one that four hundred plans do.

### The root object, which is the honest half

| Field | Why |
|---|---|
| `plans_total` | plans in the Query Store for this database |
| `plans_examined` | plans actually shredded, after the cap |
| `plans_with_usage` | of those, how many carried the element |
| `statistics_named` | distinct objects found |
| `truncated` | whether the cap bit |

`plans_examined` against `plans_total` is what stops the analysis over-reading
the result. Without it a capped scan reads as a complete one, which is the
defect recorded as section 22 of `collection-gaps-spec.md` and there is no
excuse for rebuilding it here.

### The cap, and why it is on plans and not on rows

`SELECT TOP (@cap)` over plans ordered by `last_execution_time DESC`, default
5 000, exposed as an option. XML shredding is CPU-bound on the client's
instance and this is an audit collector, so it must be boring: a fixed budget,
the most recently executed plans first, and the truncation declared.

Ordering by recency rather than by cost is deliberate. A statistic loaded by an
expensive plan last February matters less to a cleanup decision than one loaded
by a cheap plan last night, because the question this feeds is "may I drop
this", and recency is the better guard.

## 4. What it will still not answer

**Presence proves use. Absence proves nothing.** A statistics object missing
from the result may simply belong to a query whose plan left the Query Store,
or to a database where the Query Store is off, or to a workload that has not
run inside the retention window. On the audited instance the Query Store was
enabled on two databases out of ten.

This asymmetry is not a defect to be fixed later, it is a property of the
source, and it decides the only correct use of the output:

> The list is a veto, never a hit list. A cleanup decides what to drop on other
> grounds, and this collector refuses any drop it contradicts.

An implementation that presents the result as "statistics never used" has
inverted it, and will one day recommend dropping the one histogram that a
quarterly close depends on. Write the sentence above into the collector header.

## 5. What already answers the other half, and needs no collector

Worth stating so it is not built twice. Redundancy between an auto-created
statistic and an index statistic is **already fully computable from the
archive**, and it needs neither this collector nor any new query.

`70.schema/091.statistics-density.json` projects `leading_column`,
`is_auto_created`, `is_index_statistic` and `has_filter` per statistic. SQL
Server builds a histogram on the first key column only, so an auto-created
statistic is redundant with an index statistic when, and only when, both lead
on the same column of the same table and neither is filtered. That is a join on
data already collected.

Measured on the audited instance: 194 redundant auto-created statistics out of
11 105, 1.7 %. The analysis belongs in the private repository, not here.

Two bounds that apply to any such count, and that the analysis must publish
rather than swallow:

- `090` and `091` cap their listing at 200 tables. One database declared 13 012
  statistics and listed 5 583, so every count derived from these files is a
  floor.
- Dropping a redundant auto-created statistic frees metadata, not space, and
  the engine recreates it on the next compile that needs it. The measurable
  gain is the duration of the statistics maintenance job. On 194 objects that
  is small, and saying so is part of the finding.

## 6. Three steps, in order, when it is built

The repository rule for a new collector, unchanged:

1. The collector enters `queries/`, and `testdata/corpus.txt` is regenerated.
2. The private inventory is regenerated from it, and the diff is read.
3. The fixture generator gains the file, with a rendering test.

And one check that belongs to this collector in particular: run it against a
database whose Query Store is **off**. It must return an empty array with
`plans_total = 0`, not an error, because eight databases out of ten on a real
estate are in that state and a collector that errors there will be read as a
fault on the client's instance.

## 7. What building it changed, and why

Six things. Five were found by running the collector, not by reading it, which
is the usual proportion and the reason the spec was not treated as the answer.

### The XML has to become a column before an attribute is read

The spec said "shreds with `.nodes()` server-side" and stopped there, which is
where the whole cost turned out to live. Applied to an XML expression (a
`TRY_CAST` in a derived table), every `.value()` call re-parses the entire
plan, so six attributes per element cost six parses of a seventy-kilobyte
document. Measured on a lab instance, 273 plans: 116 seconds. The same shred
reading from a stored `xml` column, after one `.query('//OptimizerStatsUsage')`
per plan extracted the fragment: 3.3 seconds, with 738 KB of fragment
materialised in place of 14 MB of plan.

At the spec's cap of 5 000 plans the first form would have taken around
thirty-five minutes per database, against a declared timeout of 300 seconds. A
collector that cannot finish is a collector that returns nothing.

A warning for anyone re-measuring this. A benchmark of the form
`SELECT COUNT(*) FROM (SELECT x.value(...), ...) AS z` measures nothing: the
optimizer eliminates `.value()` calls whose results are never read, and it
reported 1.5 seconds for the form that actually takes 116. The rows have to be
inserted somewhere.

### The scan is chunked, because of the lock and not the memory

Reading `sys.query_store_plan` takes a shared QDS database lock, held for the
length of the statement. The first version held it across the whole scan and
was cancelled by this tool's own blocking watch, after another session had
waited 5.2 seconds for `LCK_M_X` on it. A statement per hundred plans measures
about 1.2 seconds, so the lock is released roughly that often. Total elapsed
time is unchanged; what changes is how long anyone else is stopped.

This is the one finding with no trace in the archive to check it against: it
showed up as a cancelled collector on a lab instance with two databases. On a
busy production store it would be more likely, not less.

### `plans_examined` and `plans_total` cannot be two separate counts

The first run reported `plans_examined` 116 against `plans_total` 115. Under
`QUERY_CAPTURE_MODE ALL` the collector's own statements are captured into the
store it is reading, so two counts a second apart disagree, and an impossible
pair costs more trust than the number was ever worth. The plan ids are now
pinned into a table variable first, `plans_examined` is that set, and
`truncated` is whether the pin reached the cap rather than an arithmetic guess.
`plans_total` remains a second snapshot, taken after the pin so that drift
appears as headroom.

### Catalog statistics are excluded, and counted

Section 3 did not anticipate them. On the first run, 110 of the 113 objects
named were statistics on `sys` tables, in `mssqlsystemresource`, `master` and
`tempdb` as well as in the database being read, loaded by the audit's own
catalog queries and by the Query Store's internal ones. None is a drop
candidate, and `70.schema/091.statistics-density`, which this file exists to be
joined against, lists only `is_ms_shipped = 0` tables and can never match one.
They are dropped from the array and counted in root as
`catalog_statistics_excluded`, because a count is what tells a reader the rows
were excluded rather than never found.

That count also turned out to be the only assertion CI can make about the
shred: the CI probe database has no user tables, so `statistics_used` is
legitimately empty there and only the excluded count proves the XML parsed.

### The database is projected, and the table is projected in one column

`StatisticsInfo/@Database` exists because a plan compiled in one database can
load statistics in another, and the corpus files this collector's output under
the database whose Query Store held the plan. Without the attribute, `OTHERDB`'s
statistic is filed under `SALESDB`. The first run confirmed it is populated and
varies.

Section 3 named `schema` and `table` as separate fields, "matching
`090.statistics`". `090.statistics` does not do that: it emits one `[table]`
column holding `schema.table`. The collector follows the file rather than the
sentence, so the two join on `(database, table, statistic)` with nothing to
reassemble. Showplan quotes these attributes (`Table="[Orders]"`), and the
outer brackets are stripped, because a join against `dbo.Orders` fails silently
on them.

### The cap is 2 000 and not 5 000

Section 3 chose 5 000 to cover the largest store measured on a real estate,
6 515 plans in one database. At 12 to 17 ms per plan that is one to one and a
half minutes of the client's CPU per database, and the corpus runs this against
every database that has a store.

Covering the largest store is not what the file is for. The question it feeds
is whether a given statistic may be dropped, the scan is ordered by recency
precisely because recent use is the better guard, and the plans that answer the
question are therefore at the top of that order. Two thousand recent plans
answer it for a third of the cost, and `truncated` says when the cap bit, so a
partial scan cannot be read as a complete one.

### The cap is a constant, not an option

Section 3 said "exposed as an option". The corpus has no directive for a
per-collector parameter, and the collectors that bound themselves do it with a
`DECLARE` and report the value they used. A command-line flag for a number
nobody has yet asked to change would have been the first of its kind. The value
travels in root either way.

## 8. What is still unverified

- SQL Server 2016 SP2. The gate is `@min_version: 14` and the reason is
  unchanged: the element exists from 2016 SP2, the corpus cannot check a
  service pack, and a 2016 instance is skipped with a reason rather than
  returning a silently empty array. Nothing has been measured there.
- A store at the cap. Every measurement above is on a store of a few
  hundred plans. The rate observed is roughly 12 to 17 ms per plan, which puts
  the cap of 2 000 at twenty-five to thirty-five seconds per database, inside
  the declared timeout, but never actually run against a store that large.
- The Query Store off case in CI. Verified by hand on a lab database whose
  store is `OFF`: `plans_total = 0`, an empty array, no error. Making that a
  permanent guard needs a second CI database, which this change did not add.
