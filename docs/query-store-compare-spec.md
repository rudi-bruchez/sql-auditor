# Query Store before and after a change

Status: draft, not implemented. Written on 19 September 2026.

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
across a given instant, and returns both sides for each of them.

## What is added

One command line option, one collector, no writer.

### The option

```
--query-store-compare-at T    the instant of the change, YYYY-MM-DDTHH:MM or
                              YYYY-MM-DD, in the SERVER's local time
```

Environment key `QUERY_STORE_COMPARE_AT`, with the same precedence as every
other key (flag, then `.env`, then the process environment). The value is
checked for shape in `Resolve`, with the `dateShapeOf` helper the window bounds
already use, and turned into an instant after the probe with `parseServerLocal`
and the same fixed zone as the window. Same reason as the window: the operator
types what the client said, which is the server's wall clock.

The option sets an internal flag, `query_store_compare`, true exactly when the
value is not empty. The collector is gated on that flag with `@requires_flag`.
`--all` does not set it: `--all` turns on opt-ins that need nothing else, and
this one needs an instant nobody can default. A run with `--all` and no instant
reports the collector as skipped, with the reason naming the option, as every
other gated collector does.

`KnownFlags["query_store_compare"]` is `--query-store-compare-at`, so the
skip reason and `check` name the option to type.

### The two sides

Let P be the instant, and N the moment of collection (the server's clock, as
`serverNow` already gives it). Each side has length

```
L = min(N - P, 7 days)
```

- before: `[P - L, P)`
- after: `[P, P + L)`

The sides have the same requested length, so a count of executions on one side
can be set against the other without a correction. Seven days is the cap
because a week covers the weekly cycle of most workloads (month-end jobs
excepted, which no default can cover), and it is also the default of
`QUERY_STORE_DAYS`. There is no option to change it. If one is needed later,
it is a second key, not a reason to delay this one.

Refusals, fatal only when the flag is on, in the same function that prices the
window's refusals (`windowForRun`, or a sibling of it):

- P not before N: nothing is after it yet;
- `N - P` under one hour: most stores aggregate by the hour, and the interval
  that contains P is excluded (below), so the after side would hold nothing.

With the flag off, a malformed `QUERY_STORE_COMPARE_AT` is already refused by
`Resolve` for shape, like the window bounds; nothing else reads it.

Known limit, shared with the existing window: the server's UTC offset is the
one the probe reported at collection time. A P on the other side of a
daylight saving change is off by one hour. The Query Store's own interval
bounds carry their offset, so this is a limit of how P is typed, not of the
data.

### Assigning intervals to a side

Measured on SQL Server 2025 with a one-minute interval, an index dropped at
15:50:13 and a workload running across it: the interval 15:50 to 15:51 held 585
executions of the old plan and 2943 of the new one. Its average is neither
side.

So an interval belongs to a side only if it lies entirely within it:

- before: `start_time >= P - L AND end_time <= P`
- after: `start_time >= P AND start_time < P + L`

The after condition tests the start only, so the interval open at collection
time is kept; its statistics are partial, which is true of any live reading.
An interval with `start_time < P AND end_time > P` belongs to neither side. It
is reported in the root (bounds and execution count), never merged.

Because intervals are aligned on the hour by default, P = 14:20 loses the whole
14:00 to 15:00 hour. That is the price of not mixing the two sides, and the
root says so by giving the excluded interval's bounds.

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

It is not a writer: its output is a JSON file like any other collector's. It
receives four named parameters, `@qs_pivot`, `@qs_before_from`,
`@qs_after_to` and `@qs_top`. `queryStoreArgs` today switches on `s.Writer`;
it gains a case for this collector keyed on its flag, so no other collector
moves to `sp_executesql`. `QUERY_STORE_DB_INCLUDE` applies to it as it applies
to 021: `queryStoreUnits` tests `s.Writer == ""` and has to test the flag too.

`@qs_top` is `QUERY_STORE_TOP` (default 50). One setting for "how many queries
per database" is enough; the two collectors answer different questions but
cost the same kind of thing.

The root, like 021's, is a `LEFT JOIN` from `sys.databases`, so a database
with the store off still produces a row saying so. It carries:

- `database`, `collected_at`, `state.actual`, `state.desired`,
  `state.readonly_reason`, `state.capture_mode`;
- `pivot`, and `excluded_interval.start`, `excluded_interval.end`,
  `excluded_interval.executions` (null when P falls on a boundary);
- for each side, `before.*` and `after.*`: `requested_from`, `requested_to`,
  `effective_from`, `effective_to` (clamped to what the store holds, as in
  021), `intervals` (the count of intervals assigned to that side);
- `interval_minutes`, `selection.cap`.

A side with zero intervals is a side the store cannot speak for, and it looks
exactly like a side where nothing ran. The count is what tells them apart,
and it is the first thing the reader checks.

### The unit of comparison

Measured on the same lab database:

| Change between the sides | `query_id` | `query_hash` | `query_text_id` |
| --- | --- | --- | --- |
| an index dropped | same | same | same |
| compatibility level 170 to 130 | same | same | same |
| `SET ANSI_NULLS OFF`, `DATEFORMAT dmy` | new | new | same |

A change of SET options, which is what a new driver or a new connection
library brings with it, and a migration often brings a new driver, splits one
statement into two `query_id`s, one on each side. `query_hash` does not bridge
them either. `query_text_id` does.

So the row is one `query_id`, as everywhere else in the corpus, and it carries
`query_text_id`, `context_settings_id` and the `set_options` of its context
settings, rendered as hex. A query present only after P whose text is shared
by a query present only before P is the same statement under other settings,
and the reader can see it by joining on `query_text_id`. The collector does
not merge them: two settings can give two plans, and that difference may be
the finding.

A plan's `compatibility_level` in `sys.query_store_plan` is the level of its
last compilation, not of its first. Measured: after the level went from 170 to
130, a plan compiled at 170 and recompiled to the same shape reported 130.
So the plan rows carry it, named `last_compile_compatibility_level`, and the
spec does not claim it tells the sides apart. `engine_version`, which moves on
an upgrade, has the same caveat and the same naming.

### Selection

Candidates: every query with at least one execution on either side.

For each candidate and each of two metrics, CPU time and duration, an impact
in microseconds:

- on both sides: `|avg_after - avg_before| * executions_after`, the cost of the
  change in per-execution cost at the after side's volume;
- after only: `total_after`;
- before only: `total_before`.

The first form attributes to the change only the per-execution difference. A
query that simply ran ten times more often keeps its per-execution cost and
does not rank; its execution counts are in the row, and volume is a different
question from regression. That is a choice, and it is written down so it can
be argued with.

The selection is a round robin over the two rankings, deduplicated, up to
`@qs_top` queries, the same shape as 021's four-metric round robin. Then,
outside the cap:

- every other query sharing a `query_text_id` with a selected query, so the
  statement split by a SET change is never selected on one side only;
- every query with a forced plan that executed on either side.

Each row says which ranking or which rule selected it (`selected_by`), and its
rank in each ranking.

### Rows

`queries`, one row per selected `query_id`:

- `query_id`, `query_text_id`, `query_hash`, `context_settings_id`,
  `set_options`, `object` (schema.name, null for ad hoc), `selected_by`,
  `rank.cpu`, `rank.duration`;
- for each side, `before.*` and `after.*`: `executions`, `plans` (distinct
  plans that executed on that side), and per execution `duration_ms`,
  `cpu_ms`, `logical_reads`, `physical_reads`, `logical_writes`,
  `max_used_memory_kb`, `rowcount`;
- `text`: the first 500 characters, as 020 does.

Averages are weighted by executions (`SUM(avg * count) / SUM(count)`), as in
020 and 021, and converted from microseconds to milliseconds.

`plans`, one row per plan of a selected query that executed on either side:

- `query_id`, `plan_id`, `query_plan_hash`, `is_forced_plan`,
  `last_compile_compatibility_level`, `last_compile_engine_version`,
  `first_execution_time`, `last_execution_time`;
- per side, `executions` and the same per-execution averages.

`query_plan_hash` rather than `plan_id` is what says two plans have the same
shape. No plan XML: that is `--query-store-detail`'s disclosure, and an
operator who wants the plans of the queries this collector selected turns it
on. The two selections are independent, and the spec does not try to join them
in `collect`.

### No verdict

Nothing is labelled a regression or an improvement. The ranking decides what
is returned, not what it means. Whether a query that got slower after P got
slower because of what happened at P, the data volume, a plan that would have
changed anyway, or a quiet week before a busy one, needs the deployment
calendar and the workload, and that is analysis. The same principle as 021's
header, and the same place for the judgement: outside this repository.

## What is not in scope

- The wizard. The option exists on the command line and in `.env`; the TUI can
  offer it later.
- Wait statistics per side (`sys.query_store_wait_stats`, 2017 and later). A
  second result set would be the natural extension; it is left out because
  the floor of the collector would then be two versions.
- Anything outside the Query Store: wait stats, counters and file latency
  have no history before the collection, so they cannot be split at P.
- Private analysis of the output.

## Tests

- `Resolve`: the key's shape is checked; the flag is set exactly when the
  value is not empty.
- The side computation: L capped at seven days, the two refusals, the flag-off
  path that ignores a bad instant with a warning.
- `queryStoreArgs`: the four parameters reach this collector and no other.
- `queryStoreUnits`: `QUERY_STORE_DB_INCLUDE` narrows this collector.
- The corpus inventory gains one entry (`testdata/corpus.txt`, regenerated).
- CI: a run with `--query-store-compare-at` against the CI instance, asserting
  the collector ran and its root carries both sides.
- On the lab: the scenario above (an index dropped mid-workload, and a SET
  change) must select both statements, show the plan change on the first and
  the `query_text_id` pair on the second.
