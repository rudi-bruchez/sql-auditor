# Query Store before and after a change

Status: implemented on 19 September 2026
(`queries/80.workload/025.query-store-compare.sql`). Written the same day,
revised after a panel of five independent readers ran it against a live SQL
Server 2025, and revised again after a sixth reader ran the revision. What the
readers changed is listed at the end.

## The question

A migration, an upgrade, a new application release, a compatibility level
change, an index dropped by a maintenance script: after each of these, the
client asks whether performance changed, and which queries got better or worse.
The Query Store holds the answer, since it keeps per-interval statistics per
plan, but nothing in `collect` reads it that way today.

- `80.workload/020.query-store.sql` ranks the heaviest queries over the whole
  retained history. A regression that started three days ago is averaged into
  thirty days of the old behaviour.
- `80.workload/021.query-store-detail.sql` reads one window. Comparing two runs
  of it, one per window, gives two top-N lists selected independently: a query
  that regressed but was not heavy before is absent from the first list, and
  the comparison has nothing to compare it with.

What is missing is one reading that selects queries by how much they changed
across a given moment, and returns both sides for each of them.

## What is added

One command line option, one collector, no writer.

### The option

```
--query-store-compare-at T    when the change happened, in the SERVER's local
                              time: YYYY-MM-DDTHH:MM for "during that minute",
                              YYYY-MM-DD for "some time that day"
```

Command line only. There is no `.env` or environment key. The window keys live
in `.env` because a window is a standing choice; this value names one event,
and left in a `.env` it would run a comparison around a stale instant on every
later collection, or, typed ahead of a planned migration, make every ordinary
`collect` fail on a moment in the future.

The shape is checked in `buildOptions` (the same two layouts `dateShapeOf`
accepts, refused with exit 2 otherwise). It is turned into an instant after
the probe, with `parseServerLocal` and the same fixed zone as the window,
because the operator types what the client said, which is the server's wall
clock.

### What the typed value means: a span, not an instant

The operator never knows the second. "We deployed at 14:00" means the change
landed at some moment in that minute, or in the next few; a date alone means
some time that day. The typed value is therefore read as a span C:

- `YYYY-MM-DDTHH:MM`: C is `[T, T + 1 minute)`;
- `YYYY-MM-DD`: C is `[T 00:00, T + 1 day)`.

Every Query Store interval that overlaps C is excluded from both sides. It is
the interval in which the change may have happened, and its averages mix the
two behaviours. Measured on SQL Server 2025, with a one-minute interval, an
index dropped at 15:50:13 and a workload running across it: the interval 15:50
to 15:51 held 585 executions of the old plan and 2943 of the new one.

This replaces the first draft's rule, which excluded only an interval strictly
straddling an instant. A reviewer showed that rule failed on the spec's own
example: the option takes minutes, so on a one-minute store the typed instant
always falls on a boundary, nothing straddles it, and the mixed interval went
whole into one side.

An operator unsure of the minute types the date. The price is a day of data
excluded, and the root says so.

### The two sides, computed per database in SQL

The Go side passes two instants, `@qs_change_from` and `@qs_change_to` (the
bounds of C), and `@qs_top`. Everything else is computed by the collector, per
database, because it depends on that database's interval length and on which
intervals exist, which Go cannot know.

In the collector, with m the store's `interval_length_minutes` and N the
server's `SYSDATETIMEOFFSET()`:

1. `excluded_from` = the earliest `start_time` of an interval overlapping C
   (`start_time < @qs_change_to AND end_time > @qs_change_from`); when none
   does, the latest `end_time` at or before `@qs_change_from`; when there is
   none either, `@qs_change_from`. Symmetrically, `excluded_to` = the latest
   `end_time` of an interval overlapping C; when none does, the earliest
   `start_time` at or after `@qs_change_to`; failing that, `@qs_change_to`.
   No row overlapping C means nothing was captured during it, so nothing mixed
   can be kept by accident, and the fallbacks put both bounds on the edge of a
   real interval rather than in the middle of an absent one.
2. L = the shortest of: the time from `excluded_to` to N; the time from the
   oldest interval the store holds to `excluded_from`; seven days. Rounded
   down to a whole multiple of m, and zero when `excluded_to` is after N (the
   interval overlapping C is still open) or the store holds nothing older than
   `excluded_from`.
3. before = the intervals with `start_time >= excluded_from - L` and
   `end_time <= excluded_from`; after = the intervals with
   `start_time >= excluded_to` and `end_time <= excluded_to + L`.

The retention bound matters as much as the other two. Without it, a store
enabled or cleared shortly before a migration, which is the documented way to
prepare one, would compare a few hours of before with a week of after, and the
total ranking below would measure retention rather than the change. A reviewer
showed it on the lab: a nominal 31 minutes on each side held 6 intervals
before and 12 after, and an unchanged query ranked first on total CPU.

Both sides have the same length, L, and both are made of whole, closed
intervals: the interval still open at collection time is never on a side,
since it ends after N and `excluded_to + L <= N`. The first draft's sides had
asymmetric bounds (the before side dropped the interval containing its start,
the after side kept the one containing its end); measured by a reviewer, 59
intervals against 60 on a one-hour comparison.

Seven days is the cap because a week covers the weekly cycle of most workloads
(month-end jobs excepted, which no default covers) and matches the default of
`QUERY_STORE_DAYS`. It is a multiple of every interval length the Query Store
accepts (1, 5, 10, 15, 30, 60 and 1440 minutes), so the rounding never
shortens it.

L can be zero: a daily store and a change six hours ago, or an hourly store
and a change forty minutes ago. That is a per-database fact, reported in the
root, not a refusal: another database of the same run may have a one-minute
store and a usable comparison. The first draft refused anything under an hour
in Go, which a reviewer showed let a daily store through with both sides
empty, and refused a one-minute store that had enough data.

The only refusal is in Go, before anything runs: C must end before the
collection starts (the probe's `serverNow`). Otherwise there is no after at
all, and a C in the future is almost always a time typed in the wrong zone,
the same reasoning as the window's future-bound refusal.

On a daily store, a date typed on a server whose offset is not zero can
overlap two daily intervals, and both are excluded: the store's days and the
server's local days do not have to coincide. The root's excluded bounds show
it; the containers used here run at UTC, so it was not measured.

Known limit, shared with the existing window: the server's UTC offset is the
one the probe reported at collection time. A T on the other side of a daylight
saving change is one hour off. A reviewer confirmed that the driver sends a
`time.Time` as `datetimeoffset(7)`, so the comparisons themselves are made on
the UTC instant; the limit is only in how T is typed.

The bounds come from the interval rows rather than from arithmetic on m,
because interval boundaries are not something the collector can compute
reliably. Measured on SQL Server 2025: a store at 15 minutes had an interval
16:15 to 16:30; after `INTERVAL_LENGTH_MINUTES = 1440`, that same interval
became 16:15 to 00:00. Boundaries follow the clock of the current length, and
the interval open at the change straddles both.

When the store's interval length changed within the compared range, older
intervals are not aligned on the current m. They still belong to a side only
when they lie entirely within it, so nothing is mixed; a side may then hold
fewer intervals than L / m, and the root's counts show it.

### What the counts do and do not say

Interval rows exist only for periods in which the store captured something.
Measured by a reviewer: the lab database has no interval rows between 15:54
and 16:05 with the store in READ_WRITE, and an idle scratch database had
intervals filled by the instance's own background queries. So the number of
intervals on a side proves neither that the workload ran nor that it did not.
The root reports it, beside L and the state, and the spec does not claim it
separates "the store could not speak" from "nothing ran". The state
(`actual`, `readonly_reason`) is what says whether the store was recording at
collection time.

### The collector

`queries/80.workload/025.query-store-compare.sql`:

```
-- @scope:         database
-- @resultsets:    root:object, queries:array, plans:array
-- @permissions:   CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:       300
-- @min_version:   13
-- @requires_flag: query_store_compare
-- @discloses:     query_text
```

It is not a writer: its output is one JSON file like any other collector's.
`runUnit` already passes arguments to a collector without a writer; only
`queryStoreArgs` has to learn this one. It switches on `s.Writer` today and
gains a case keyed on `s.RequiresFlag == FlagQueryStoreCompare`, so no other
collector moves to `sp_executesql`.

`QUERY_STORE_DB_INCLUDE` applies to it as it applies to 021.
`queryStoreUnits` returns early on `s.Writer == ""`; that condition becomes
"neither a writer nor this collector", and a test checks that ordinary
collectors are still not narrowed.

`@qs_top` is `QUERY_STORE_TOP` (default 50). One setting for "how many queries
per database" is enough.

### The flag is not an opt-in

`--all` and the wizard's third screen turn on every entry of `KnownFlags`, and
two tests hold them to it (`TestAllTurnsOnEveryOptIn` and
`TestTheWizardOffersEveryOptInTheCommandLineHas`). This flag cannot be one of
them: `--all` has no instant to give, and screen 3 holds boolean toggles.

So it goes into a second map, `ValueFlags`, beside `KnownFlags`: flags that an
option carrying a value sets, true exactly when the value was given.
`@requires_flag` accepts a name from either map; the skip reason and `check`
look the option up in either. `--all` and the wizard keep iterating
`KnownFlags` only. The wizard test stays as it is; `TestAllTurnsOnEveryOptIn`
reads the `Flags` map `buildOptions` returns, which carries this flag too, so
it learns that a `ValueFlags` entry is decided but must stay off under `--all`.
A test checks that the two maps are disjoint, and that every entry of
`ValueFlags` is decided by `buildOptions`.

A run with `--all` and no `--query-store-compare-at` reports the collector as
skipped with the reason naming the option, as every other gated collector
does.

The manifest records the value as typed (`query_store_compare_at_requested`)
and as resolved in the server's zone (`query_store_compare_at`), like the
window's pair.

### The root

A `LEFT JOIN` from `sys.databases`, as in 021, so a database with the store
off still produces a row saying so:

- `database`, `collected_at`, `state.actual`, `state.desired`,
  `state.readonly_reason`, `state.capture_mode`, `interval_minutes`;
- `change.from`, `change.to` (C, as received);
- `excluded.from`, `excluded.to`, `excluded.still_open` (the interval
  overlapping C had not closed at collection time, so its executions are
  partial and there is no after side yet), `excluded.intervals`,
  `excluded.executions`;
- `oldest_interval`, the start of the store's retained history;
- `side_minutes` (L);
- `before.from`, `before.to`, `before.intervals`, and the same for `after`;
- `selection.cap`, `selection.statements`, `selection.queries`.

### The unit of comparison

Measured on the lab database, with one reviewer's refinement:

| Change between the sides | `query_id` | `query_hash` | `query_text_id` |
| --- | --- | --- | --- |
| an index dropped | same | same | same |
| compatibility level 170 to 130 | same | same | same |
| `SET ARITHABORT OFF` or `DATEFORMAT dmy` | new | same | same |
| `SET ANSI_NULLS OFF` | new | new | same |

A change of SET options, which a new driver or connection library brings with
it, and a migration often brings a new driver, splits one statement into two
`query_id`s, one on each side.

`query_text_id` alone does not identify that statement. Two reviewers showed
independently that two stored procedures holding the same statement share one
`query_text_id` under two `query_id`s. The pair that identifies "the same
statement under other settings" is (`query_text_id`, `object_id`), with a
different `context_settings_id`.

So the unit of SELECTION is the statement, (`query_text_id`, `object_id`), and
the unit of the ROWS stays the `query_id`, as everywhere else in the corpus.
Ranked by `query_id`, a statement split by a driver change would sit on one
side per `query_id`; the per-execution rankings accept only queries present on
both sides, so neither half could rank there, and a split statement that
regressed would lose its place to unchanged heavy ones. A reviewer built that
case (three statements, two of them switched to other SET options when an
index was dropped) and the split regression was not selected. Ranked as a
statement, it is: measured with a cap of three, the split regression came
second on per-execution CPU, both of its `query_id`s returned.

Each row carries `query_text_id`, `object_id`, `context_settings_id`, and the
`set_options` of its context settings rendered as hex. The collector does not
merge the `query_id`s of a statement: two settings can give two plans, and
that difference may be the finding.

A plan's `compatibility_level` in `sys.query_store_plan` is that of its last
compilation. Measured: after the level went from 170 to 130, a plan compiled
at 170 and recompiled to the same shape reported 130. The plan rows name it
`last_compile_compatibility_level`, and `engine_version` likewise
`last_compile_engine_version`; neither is claimed to tell the sides apart.

### Selection

Candidates: every statement with at least one execution on either side, all
its `query_id`s summed.

Three rankings over statements:

1. `cpu_per_execution`: statements on both sides only, by
   `|avg_cpu_after - avg_cpu_before| * executions_after`. The change in
   per-execution cost, at the after side's volume.
2. `duration_per_execution`: the same with duration.
3. `cpu_total`: every statement, by `|total_cpu_after - total_cpu_before|`,
   a missing side counting as zero.

The sides have the same length, are made of whole intervals, and the before
side is never longer than the history behind it, so the totals can be compared
directly. The third ranking is what catches a statement that appeared,
disappeared, or changed volume. The first draft ranked a
one-side query by its total and a two-side query by its per-execution change,
which a reviewer showed was discontinuous: a query falling to zero executions
ranked first, the same query falling to one execution ranked near last.

The selection is a round robin over the three rankings, deduplicated, up to
`@qs_top` statements, the same shape as 021's round robin; ties break on the
statement's lowest `query_id`, so two collections of an unchanged store select
the same statements. Then, outside the cap, the statement of every query that
executed on either side and has a plan with `is_forced_plan = 1` or
`force_failure_count > 0`, as in 020, since a forcing that fails is the case
that goes unnoticed.

Every `query_id` of a selected statement that executed on either side gets a
row. How many there are is bounded by the SET combinations, parameterization
types and batches the statement ran under, not by how often its text is
repeated across procedures, since `object_id` is part of the key.

Each row gives its statement's `selected_by` and ranks.

### Rows

`queries`, one row per selected `query_id`:

- `query_id`, `query_text_id`, `object_id`, `object` (schema.name, null for
  ad hoc), `query_hash`, `context_settings_id`, `set_options`,
  `selected_by`, `rank.cpu_per_execution`, `rank.duration_per_execution`,
  `rank.cpu_total`;
- for each side, `before.*` and `after.*`: `executions`, `plans` (distinct
  plans that executed on that side), and per execution `duration_ms`,
  `cpu_ms`, `logical_reads`, `physical_reads`, `logical_writes`,
  `max_used_memory_pages` (the source column counts 8 KB pages, and 021
  already names its column that way), `rowcount`;
- `text`: the first 500 characters, as 020 does.

Averages are weighted by executions (`SUM(avg * count) / SUM(count)`), as in
020 and 021, and durations converted from microseconds to milliseconds.

`plans`, one row per plan of a selected query that executed on either side:

- `query_id`, `plan_id`, `query_plan_hash`, `is_forced_plan`,
  `force_failure_count`, `last_compile_compatibility_level`,
  `last_compile_engine_version`;
- per side, `executions`, `first_execution` and `last_execution` (the minimum
  and maximum of `sys.query_store_runtime_stats.first_execution_time` and
  `last_execution_time` over that side's intervals; the plan view has no such
  column, a reviewer checked), and the same per-execution averages.

`query_plan_hash` groups plans of the same shape. It is a hash, and 022
already treats a match on it as a match on a hash rather than as identity; the
rows here are named the same way and no text claims more.

No plan XML: that is `--query-store-detail`'s disclosure, and an operator who
wants the plans of the queries this collector selected turns it on.

### No verdict

Nothing is labelled a regression or an improvement. The ranking decides what
is returned, not what it means. Whether a query that got slower after the
change got slower because of it, the data volume, a plan that would have
changed anyway, or a quiet week before a busy one, needs the deployment
calendar and the workload, and that is analysis. The same principle as 021's
header, and the same place for the judgement: outside this repository.

## What is not in scope

- The wizard. It cannot take an instant on screen 3; offering the comparison
  there is a separate screen, later.
- Wait statistics per side (`sys.query_store_wait_stats`, 2017 and later). A
  second result set would be the natural extension; it would raise the
  collector's floor.
- Anything outside the Query Store: wait stats, counters and file latency have
  no history before the collection, so they cannot be split.
- Private analysis of the output.

## Tests

- `buildOptions`: the two shapes accepted, anything else refused with exit 2;
  the flag set exactly when the option was given; `--all` alone leaves it off.
- `ValueFlags` and `KnownFlags` disjoint; `@requires_flag` accepts a name from
  either; the skip reason names `--query-store-compare-at`.
- The span: a minute and a day, the future refusal against the server's clock.
- `queryStoreArgs`: the three parameters reach this collector and no other.
- `queryStoreUnits`: `QUERY_STORE_DB_INCLUDE` narrows this collector, and
  still does not narrow an ordinary one.
- The manifest records the typed and the resolved value.
- The corpus inventory gains one entry (`testdata/corpus.txt`, regenerated).
- On the lab, all of: the change typed on its minute excludes the 15:50
  interval and nothing else; the dropped index shows as a plan change; a
  statement split by a SET change across the change is ranked as one and both
  its `query_id`s are returned; a store whose history starts shortly before
  the change gives two sides of the same number of intervals. A date typed on
  the day of collection is refused in Go, since the day is not over; a past
  date excludes that whole day.
- Cost, measured before merging: the collector on a store of at least a
  hundred thousand `runtime_stats` rows. Measured on SQL Server 2025 with
  100,892 rows (10,002 queries, one-minute intervals), through the binary with
  the default cap of 50: 757 ms. The same store also showed why the interval
  counts are reported rather than trusted: under load it went `READ_ONLY`
  (`readonly_reason` 262144, in-memory limit) and captured one minute in two,
  so the after side held 5 intervals for 6 minutes of time.
- CI: a run with `--query-store-compare-at` against the CI instance, asserting
  the collector ran and its root carries both sides.

## What the panel changed

Five readers (agy and codex, each with a directive and a neutral prompt, and a
Claude subagent) ran the first draft against SQL Server 2025.

- The typed value became a span, and the excluded intervals are those
  overlapping it (three readers: the boundary case, the empty sides, the
  asymmetric sides).
- The sides are computed in SQL per database, of equal length in whole
  intervals, and an empty side is reported rather than refused.
- The sibling rule joins on `query_text_id` and `object_id` (two readers,
  measured with two procedures).
- The flag moved to `ValueFlags` (the `--all` and wizard tests fail
  otherwise), and the option lost its `.env` key.
- The ranking gained a total ranking and lost the discontinuity at zero
  executions.
- `first_execution_time` comes from `runtime_stats`; memory is in pages;
  forced-plan selection includes forcing failures; the manifest records the
  value; the `query_hash` row of the table was corrected.
- The claim that the interval count tells an idle side from an unrecorded one
  was withdrawn.

A sixth reader ran the revision, and changed it again:

- L is also bounded by the store's retained history, so a before side cut
  short by retention no longer passes for a week of data.
- Selection is by statement rather than by `query_id`, which replaces the
  sibling rule: a statement split by a SET change ranks as one.
- The root says when the excluded interval is still open, and gives the
  store's oldest interval.
- The date test contradicted the Go refusal and was rewritten; the option
  names come from one helper, which also corrected the grant script's
  `(only with ...)` notes that spelled some options wrongly.
