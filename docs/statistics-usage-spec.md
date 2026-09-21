# Statistics usage — specification

**Date:** 21 September 2026, after a second-pass audit whose four requested
subjects included statistics maintenance.
**Status:** specified, not implemented. One collector, `80.workload/027.query-store-stats-usage.sql`.

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
