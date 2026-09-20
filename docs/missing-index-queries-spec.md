# Which queries wanted a suggested index

Status: draft, not implemented. Written on 20 September 2026, from the answer
recorded in `docs/missing-index-suggestions-spec.md` on the same day.

## The question

The archive carries the optimizer's missing-index suggestions, in
`70.schema/020.index-usage.sql`: a table, the equality, inequality and
included columns, and how often the suggestion was met. It does not carry what
wanted them. A report can say "the optimizer asked for an index on
(client_id, order_date) INCLUDE (total) 4,000 times" and cannot say whether
that is one nightly report or four hundred different statements, which is the
first thing anyone asks before building the index.

`sys.dm_db_missing_index_group_stats_query`, from SQL Server 2019, answers it.
One row per suggestion and query, with that query's seeks, scans, cost and
impact.

## What it exposes, measured rather than assumed

The view carries two statement handles, and the first answer written about it
tested only one of them. `last_statement_sql_handle` is refused by
`sys.dm_exec_sql_text`, with `Msg 12413` redirecting to the Query Store.
`last_sql_handle` is an ordinary handle: with `last_statement_start_offset`
and `last_statement_end_offset` beside it, the statement comes back whole,
literals included, as long as the plan is in cache. Verified on 2022:

```
SELECT @p = MAX(pad) FROM dbo.Txt WHERE ssn = 4242
```

A reviewer showed the second route as well: `query_hash` joined to
`sys.query_store_query_text` returns the text where the Query Store holds it.

So this collector deals in identity, and the line it draws is the corpus's
existing one. Hashes are already in the archive three times over
(`80.workload/030.implicit-conversions.sql`, `060.spills.sql`,
`025.query-store-compare.sql`), none of them behind a flag, and none of them
disclosing text. Statement text is the other side of that line, where
`052.session-text.sql` sits behind `--include-session-text`.

## What is added

One collector, `queries/70.schema/021.missing-index-queries.sql`:

```
-- @scope:       database
-- @resultsets:  root:object, queries:array
-- @permissions: CONNECT, VIEW SERVER STATE, VIEW ANY DEFINITION
-- @timeout:     120
-- @min_version: 15
```

A second file rather than a third result set on `020.index-usage.sql`: 020 is
298 lines, it is in the `space` profile, and it answers a question this one
does not. Its cost is one more pass over the same three DMVs, which is what a
separate file costs anyone who wants both.

Not in the `space` profile. That profile answers what makes the databases big
and what can be dropped; which statements wanted an index is tuning, and a
profile is the promise that its members earn their place.

### What the array carries

One row per suggestion and query, capped at 500 by seeks then scans, with the
cap in the root as every capped listing in this corpus has it:

- `group_handle`: the suggestion's handle, a compact key within this archive
  and meaningless outside the instance's current lifetime;
- `table`, `equality_columns`, `inequality_columns`, `included_columns`: the
  suggestion itself, spelled exactly as `020.index-usage.sql` spells it,
  because 020's `missing` array carries no handle and this tuple is the only
  join between the two files. The DMV's own bracketing is preserved on both
  sides, which is the subject of a separate, decided change to `070`;
- `query_hash`, `query_plan_hash`: hex strings, in the form `030` and `060`
  already use;
- `user_seeks`, `user_scans`, `last_user_seek`, `last_user_scan`,
  `avg_total_user_cost`, `avg_user_impact_pct`: the per-query figures, with
  the same names and the same rounding as 020's `missing` array, so the two
  read as one document.

The system counters (`system_seeks`, `system_scans` and their companions) are
not projected, for the reason 020 does not project them: they count the
engine's own background work, and an audit that reported them beside the user
figures would invite the two to be added up.

### What the array must never carry

By name, and this is the load-bearing instruction of the document:
`last_sql_handle`, `last_statement_sql_handle`, `last_statement_start_offset`,
`last_statement_end_offset`. The first three are what turn a hash into a
statement, and the offsets are what turn a batch into the exact line. An
implementer who reads only the column list will find a handle whose obvious
sibling is refused by the engine and conclude that projecting it is safe.

### The root

- `database`, `collected_at`;
- `counts.suggestions_with_queries`: distinct suggestions the array covers;
- `counts.rows`: rows before the cap;
- `counts.distinct_query_hashes`;
- `listing_cap`: 500;
- `collected` and `error_number` in the shape 020 uses, since the same
  `TRY`/`CATCH` and the same guard apply.

### Two limits the header states

The rows are bounded by the same instance-wide cap as the suggestions
themselves: 600 groups, measured on 2017, 2019, 2022 and 2025, and trimmed to
480 under memory pressure. `020.index-usage.sql` already projects the
instance-wide count for that reason, and this file does not duplicate it: a
reader holding both files reads saturation from the one that measures it.

And the view is the execution side of a group, so it reads zero until the
statements have run, even where the suggestion exists. Measured: a database
with three suggestions and four query rows, one suggestion serving two
queries.

## What is not in scope

- Any resolution of a hash to a statement, here or in the analysis. That is
  the Query Store's job, in the files that already carry it.
- Ordering or scoring the suggestions. `020.index-usage.sql` refuses to
  compute an impact score and this file refuses the same way.
- SQL Server 2017 and below, where the view does not exist. The runner skips
  the file on `@min_version` and records the skip.

## Tests

- The corpus inventory gains one entry (`testdata/corpus.txt`, regenerated);
  `TestEmbeddedCorpusIsValid` lints the header and the body.
- A test that the body projects none of the four forbidden columns, by name.
  It is the one rule here that a future edit could break silently, and a
  reviewer reading a diff would not necessarily know the column list.
- CI asserts, on 2022, that the file runs and that its root has the counts;
  the CI fixture already drives a workload, and a database with no suggestion
  produces an empty array with a root, which is the shape to assert.
- On the lab: a suggestion serving two queries appears as two rows carrying
  the same `group_handle` and the same suggestion columns, and the join to
  `020`'s `missing` array on the four suggestion fields matches.
