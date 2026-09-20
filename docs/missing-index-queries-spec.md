# Which queries wanted a suggested index

Status: draft, not implemented. Written on 20 September 2026, from the answer
recorded in `docs/missing-index-suggestions-spec.md` on the same day, and
revised the same day after a panel of five readers ran it against SQL Server
2025. What the panel changed is at the end.

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

A reviewer showed the second route as well: a `query_hash` reaches the text
where the Query Store holds it, through `sys.query_store_query` and then
`sys.query_store_query_text`. Not directly, as an earlier draft said: the text
view is keyed on `query_text_id` and carries no hash.

So this collector deals in identity, and the line it draws is the corpus's
existing one. Two collectors already publish a hash in every default archive,
behind no flag and with no disclosure token: `80.workload/030.implicit-conversions.sql`
and `060.spills.sql`. A third, `025.query-store-compare.sql`, publishes one
too and is not a precedent for anything here: it sits behind
`--query-store-compare-at` and declares `@discloses: query_text`, because it
publishes the text beside the hash. Statement text is the other side of the
line, where `052.session-text.sql` sits behind `--include-session-text`.

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

The cap is per suggestion, and that is the panel's correction. A single array
capped globally by seeks drops whole suggestions: a reviewer built a database
with two suggestions, one wanted by many statements, and a verbatim
implementation of the first draft returned 500 rows all carrying the same
handle. The second suggestion had three queries; the archive said nothing
about them and nothing about why, with `collected` 1 and `error_number` 0. And
the bias runs the wrong way: ordering by seeks keeps the statement that ran
most and drops the tail, when the tail is exactly what tells one nightly
report from four hundred statements.

So: the top 20 queries of each suggestion, by seeks then scans then
`query_hash` as a tiebreaker, and at most 1,000 rows in the file. The array is
ordered by `group_handle`, then the same three keys, so two collections of the
same instance diff cleanly.

- `group_handle`: the suggestion's handle. It groups the rows of one
  suggestion inside this file and it joins to nothing else: `020` publishes no
  handle, and the number is meaningless outside the instance's current
  lifetime;
- `table`, `equality_columns`, `inequality_columns`, `included_columns`: the
  suggestion itself, spelled exactly as `020.index-usage.sql` spells it,
  because 020's `missing` array carries no handle and this tuple is the only
  join between the two files. Three of the four are nullable and NULL is the
  ordinary case, not the odd one: on the lab fixture every suggestion had a
  NULL among them. The join is therefore on the four fields treated as a
  tuple where NULL equals NULL, which is not what SQL's `=` does and is what
  an analysis in another language has to be told. The DMV's bracketing is
  preserved on both sides, and a separate decided change brings `070` into the
  same convention;
- `queries_for_suggestion`: how many query rows that suggestion has before the
  per-suggestion cap, so a row says whether it is one of twenty out of twenty
  or one of twenty out of six hundred;
- `query_hash`, `query_plan_hash`: hex strings through `CONVERT(varchar(18),
  ..., 1)`, which is the width `binary(8)` needs. `030` uses that width and
  `060` uses 34 over the same type; they emit the same string, and this file
  takes the honest one;
- `user_seeks`, `user_scans`, `last_user_seek`, `last_user_scan`,
  `avg_total_user_cost`, `avg_user_impact_pct`: the per-query figures, with
  the same names and the same rounding as 020's `missing` array, so the two
  read as one document.

The system counters (`system_seeks`, `system_scans` and their companions) are
not projected, for the reason 020 leaves them out of its own `missing` array:
they count the engine's background work, and reporting them beside the user
figures invites the two to be added up. 020 does project them for index usage,
which is a different question about a different population.

### What the array must never carry

By name, and this is the load-bearing instruction of the document:
`last_sql_handle`, `last_statement_sql_handle`, `last_statement_start_offset`,
`last_statement_end_offset`. The first two are what turn a hash into a
statement, and the offsets are what cut the batch down to the exact line. An
implementer who reads only the column list will find a handle whose obvious
sibling is refused by the engine and conclude that projecting it is safe.

The prohibition is on what the file emits, not on what its text contains, and
the difference is not pedantic: a reviewer wrote a body that passes a
name-based test and ships all four columns, by selecting `q.*` into a
temporary table and emitting that. So the guard is on the projected column
list, and a star expansion of the view is refused with it. The test also reads
the body with its comments stripped, because this document asks for a header
comment naming the four columns, and a naive search would then find them.

### The root

- `database`, `collected_at`;
- `counts.rows`, `counts.suggestions_with_queries`,
  `counts.distinct_query_hashes`: all three BEFORE the cap, all three marked
  as such in the header. The first draft left two of them ambiguous and a
  reviewer read them post-cap, which is the reading that hides the truncation;
- `listing_cap_per_suggestion`: 20, and `listing_cap`: 1000;
- `collected` and `error_number`, flat, in the shape `70.schema/091` and
  `10.system/044` use. Not 020's shape: it guards three areas and so writes
  `collected.usage`, `collected.missing` and their errors, and there is one
  guarded area here.

The pre-cap counts cost a second read of the same three views, since the
capped read cannot produce them. That is the one place this file pays twice,
and it is stated rather than discovered.

### What the header states about the population

The rows are not bounded by the 600 missing-index groups. That bound is on
suggestions; a suggestion can be wanted by many distinct statements, and a
reviewer measured 625 rows for a single group. Saturation is read from
`020.index-usage.sql`, which projects the instance-wide suggestion count, and
this file does not duplicate it.

What the rows survive is what the suggestions survive, which a reviewer
measured in both directions: `DBCC FREEPROCCACHE` leaves them untouched, and
taking the database offline and back online clears them, along with the
suggestions themselves. Clearing the plan cache does break the recovery of
text through the handle, which is a different question and not this file's.

The view is the execution side of a group: it reads zero until the statements
have run. Measured on the lab: three suggestions, four query rows, one
suggestion serving two queries.

`VIEW ANY DEFINITION` is declared for the object name and not for the view. A
reviewer confirmed that a login with `VIEW SERVER STATE` alone reads every
column of `sys.dm_db_missing_index_group_stats_query`, hashes and handles
included; it is `OBJECT_SCHEMA_NAME` and `OBJECT_NAME` that need the other
permission, exactly as in 020.

The body follows the corpus contract like every collector: `SET NOCOUNT ON`,
`READ UNCOMMITTED`, `LOCK_TIMEOUT 10000`, and `OPTION (RECOMPILE, MAXDOP 1)`
on each statement. The header block above is the directives only, and a
reviewer who built the file from it alone hit the contract lint, which is the
lint doing its job.

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
- The forbidden columns, tested on the projection rather than on the file's
  text, with comments stripped, and with a star expansion of the view refused.
  Checked against the body a reviewer wrote to defeat the naive form.
- The cap: a fixture with two suggestions, one of them wanting many queries,
  produces rows for both, and the row of the crowded one says how many queries
  it has beyond the twenty listed.
- CI runs a matrix of 2017 and 2022, and this file exists only on the second.
  The assertion is written to say which leg it is on rather than to guess:
  on 2022 the file is there and its root carries the three pre-cap counts, and
  on 2017 `_run.json` records the skip with the version reason. An assertion
  added to the existing block, which asserts files that exist on both legs,
  would turn the 2017 leg red on the first push.
- On the lab: a suggestion serving two queries appears as two rows carrying
  the same `group_handle` and the same suggestion columns, and the join to
  `020`'s `missing` array on the four suggestion fields, NULLs included,
  matches.

## What the panel changed

Five readers (agy and codex, each with a directive and a neutral prompt, and a
Claude subagent) ran the draft against SQL Server 2025.

- The global cap dropped whole suggestions and biased the answer toward the
  question's wrong side, which the Claude subagent reproduced with a verbatim
  implementation and agy reached by reading. Hence the per-suggestion cap and
  `queries_for_suggestion`.
- The claim that 600 groups bound the rows was false: one group carried 625
  rows.
- A name-based test on the four forbidden columns passes a body that emits all
  four through `q.*`, demonstrated.
- The three join keys are nullable, and NULL is the ordinary case.
- The disclosure paragraph cited `025.query-store-compare.sql` as a precedent
  for publishing a hash with no flag and no disclosure; it is behind a flag
  and declares `query_text`.
- The root shape attributed to 020 is not 020's, the reason given for dropping
  the system counters is contradicted by 020, the hash conversion has two
  widths in the tree, and the pre-cap counts need a second read.
- `VIEW ANY DEFINITION` is not needed for the view, only for the object name.
- The rows survive `DBCC FREEPROCCACHE` and do not survive an offline cycle.
- The Query Store route to the text needs `sys.query_store_query` in the
  middle.
