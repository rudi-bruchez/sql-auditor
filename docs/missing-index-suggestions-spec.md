# Missing index suggestions — specification

Status: draft, not implemented. Written 17 September 2026 to answer section
10 bis of [collection-gaps-spec.md](collection-gaps-spec.md), which reserved
this question for a document of its own. Revised the same day after a panel of
five independent readers ran it against a live SQL Server 2025. What the panel
changed is recorded at the end rather than quietly folded in, because the
document's own conclusion moved: it proposed a column and no new read, and it
now proposes a read and no column.

## What section 10 bis says, and what is actually in the tree

Section 10 bis is titled "One gap the review named that this document does not
close", and its first sentence is:

> The missing-index DMVs. `sys.dm_db_missing_index_group_stats` and its siblings
> are absent from the corpus and from this specification.

That sentence is false, and it was false when it was written. The three DMVs are
read by two collectors, and have been since August 2026, while section 10 bis
was written in September:

- `20.databases/020.properties.sql`, result set `missing_indexes`: `TOP (25)`
  ordered by `avg_total_user_cost * avg_user_impact * (user_seeks + user_scans)`,
  projecting that product as `impact_score`, the table, `uses`,
  `avg_impact_pct`, and the three column lists.
- `70.schema/020.index-usage.sql`, result set `missing`: no `TOP` and no
  threshold, projecting the table, the three column lists, `user_seeks`,
  `user_scans`, `avg_total_user_cost`, `avg_user_impact_pct`, `last_user_seek`
  and `last_user_scan`. Its root carries `missing_suggestions`,
  `instance_start` and `seconds_since_instance_start`.

Both are in the `space` profile. The overlap is deliberate and the second file's
header says why: the first is a triage view at 25 rows, the second a baseline
that cannot be re-expanded after a restart. That decision is not reopened here.
All five readers checked this inventory independently and all five agreed.

Note for anything built on top: the two collectors name the same underlying
quantities differently, `avg_impact_pct` against `avg_user_impact_pct`, `uses`
against `user_seeks` and `user_scans`. A report must name which of the two it
reads, because an archive can carry the 25-row triage list for a database whose
baseline read was blocked and came back empty.

The mistake is worth naming rather than quietly correcting, because it is the
second time this practice has paid for it: the private repository's maintenance
notes record a whole task that shrank to a paragraph when "the notice does not
exist anywhere" turned out to be false. Writing the absence of a thing is an
assertion, and it is the most expensive kind, because nothing catches it except
someone opening the tree.

## Why a suggestion is not a measurement

The engine's own documentation lists the limits, and they decide how a report
may speak.

1. **It is per query shape, not per workload.** Microsoft: requests "might offer
   similar variations of indexes on the same table and column(s) across
   queries", and should be combined where possible.
2. **It ignores write cost entirely.** The cost of maintaining the proposed
   index on every insert, update and delete is nowhere in the DMV. A `space`
   archive does carry `user_updates` per EXISTING index, which is activity on
   what exists, not the maintenance cost of what does not; a report must not
   conflate them.
3. **It ignores the indexes that already exist**, never suggests a unique or a
   filtered index, and says nothing at all for a trivial plan.
4. **`avg_user_impact` is a percentage of estimated query cost, not of time**,
   estimated before execution and never revised after it. Microsoft adds that
   cost information "is less accurate for queries involving only inequality
   predicates", which is exactly the shape the first draft's coverage rule
   dismissed.
5. **Key order is not suggested.** The documentation states that key columns are
   suggested but their order is not, and that an author orders them by
   selectivity. The list must never be read as a proposed key order.

`impact_score` in `020.properties` is a product of an estimated cost, a
percentage and an operator count, published as one figure that sorts well. It is
an ordering key. Calling it meaningless, as the first draft did, is too strong
and a reader was right to object: it is a rough accumulated benefit proxy of the
kind Microsoft's own example query uses. What it is not is a percentage, a
duration or a cost, and a report must never print it as one of the three.

## The instance-wide cap, and the one new read

The three DMVs each carry the same note, that the result set is limited to 600
rows. The limitations page says where that limit lives: "Suggestions are
gathered for a maximum of 600 missing index groups. After this threshold is
reached, no more missing index group data is gathered."

**The cap is on the instance, not on the database, and three readers measured
it independently.** The clearest run: two databases were each driven with three
hundred distinct qualifying query shapes. The first recorded 138 suggestions,
the second recorded zero, and the instance count stood at exactly 600 both
times. The distribution across the whole box at that moment was
`225, 222, 138, 7, 5, 3`: no database anywhere near 600, every database
truncated. Another reader reproduced the shape from the other end, freeing
capacity by dropping tables and watching a previously silent query start
recording suggestions again as the instance fell to 481.

Running the unchanged collector against a saturated database returns
`missing_suggestions = 0`, every `collected` flag at 1 and every error number at
0. A clean, complete-looking, entirely empty answer, indistinguishable from a
database that has nothing to suggest.

So the first draft was wrong twice: a per-database count cannot detect the
condition, and the claim that the fix needed no new read does not survive. The
read that closes it is the same `COUNT(*)` the collector already issues, with
its `WHERE mid.database_id = DB_ID()` removed. Same DMV, same permission, same
`TRY` block, one more scalar on the root of `70.schema/020.index-usage.sql`:

    SELECT @missing_suggestions_instance = COUNT(*)
    FROM sys.dm_db_missing_index_details
    OPTION (RECOMPILE, MAXDOP 1);

It is read once per database rather than once per instance, which is redundant
on a multi-database run and is the price of not inventing a collector for one
scalar. The rule it enables is blunt: when that count is at the engine's
threshold, no statement about missing indexes on any database of that instance
can claim to be complete, and the report says so instead of listing.

One claim that arrived with the panel is NOT adopted: that the limit was 500
rather than 600 before SQL Server 2019. No source was offered and no measurement
was made, and the only instance available here is a 2025. Hardcoding an
unverified threshold would silently stop detecting the very condition this
section exists for, so the collected count is reported raw and the threshold
lives in the analysis, where it can be corrected without a corpus change.

## What the archive lets an analysis decide, and what it does not

**Coverage: the existing-index test establishes possible overlap, never
coverage.** The first draft said a suggestion whose equality columns are a
prefix of an existing index's keys "is not a missing index: it is at most an
included-column change". The panel broke that four ways, three of them measured.

A suggestion carrying an inequality column needs that column IN THE KEY.
Measured against an index on `(A, C) INCLUDE (B)` with a predicate
`A = 1 AND B > 900`: the plan seeks on `A` and applies a residual predicate on
`B` across every row that matches. The index the suggestion wants is `(A, B)`,
and no included column substitutes for it.

An index that matches may be unusable. A disabled index was created, the prefix
held, the suggestion was still emitted, and forcing that index returned
`Msg 315: Index ... is disabled`. The archive already carries what is needed to
see this: `070.index-columns.sql` projects `is_disabled` and `filter_definition`,
`020.index-usage.sql` projects `hypothetical`. A coverage rule that does not
consult all three is wrong on data it already has.

It is not a prefix test at all. `equality_columns` is an unordered set delivered
as a string; the index keys are ordered. The correct test is set containment
against the leading *k* keys, and writing it as a prefix would miss every
covered suggestion whose columns the DMV happened to list in a different order
from the index. The two orderings genuinely differ: the DMV emits columns in
physical `column_id` order, measured by three readers, and an index's key order
is whatever its author chose.

And the comparison cannot be a naive string one, because the two sides are in
different notations. `070.index-columns.sql` concatenates bare names with `", "`
and glues a `" DESC"` suffix into the same string, while the DMV emits
bracket-quoted identifiers: `[x]` against `y, x DESC` matches nothing. Worse,
the collector does not escape, so a table carrying a column literally named
`a, b` alongside columns `a` and `b` produced two indexes with different leading
columns, different key counts and the identical `keys` string. The rule is
therefore: parse both sides into lists of identifiers, drop the direction marker
for equality columns and keep it for inequality ones, and where the collector's
string cannot be parsed unambiguously, ABSTAIN and say so rather than compare
and be silently wrong.

One more asymmetry, cheap to state and cheap to fix later:
`070.index-columns.sql` filters `is_ms_shipped = 0` and the `missing` result set
filters nothing, so a suggestion on a shipped table has no counterpart to
compare against. The abstain rule covers it.

**Families: group by equality set, keep every maximal element.** Two suggestions
belong to the same family when their equality sets are equal. Within a family,
an entry whose inequality and included sets are contained in another's is
absorbed by it. Pairwise containment is not transitive, so the rule cannot be
"two suggestions are one": with inequality sets `{x}`, `{x,y}` and `{y}`, the
first absorbs into the second and the third into nothing, and a greedy
implementation would answer differently depending on read order. Group, keep
maximal elements, and report how many suggestions each stands for. A table
carries as many families as it has distinct equality sets; the first draft's
"a table carries one entry" was wrong, since two suggestions on one table need
not share a single equality column.

The equality set may be compared on the DMV's own string, and this was measured
rather than assumed. A table with columns declared `z`, `y`, `x` was queried as
`WHERE x = 42 AND y = 42` and as `WHERE y = 7 AND x = 7`; both produced the
single entry `[y], [x]`, the physical order. Two readers and the author measured
this independently. The one reader who claimed the opposite offered no output.

**Key order: the archive usually cannot say.** `70.schema/091.statistics-density.sql`
carries `all_density_estimate` and `distinct_pct_estimate`, and Microsoft's
guidance is to order equality columns by selectivity. But it is not in the
`space` profile, it is gated at `@min_version: 13.0.4422`, it describes only the
FIRST column of a statistic, and it selects only the largest 200 tables. So no
`space` archive can order columns, no archive of a 2012 or 2014 instance can,
and a full archive of a modern one can only when a separate applicable statistic
exists for each column it would order. The rule follows the evidence and not the
profile: order only when applicable density rows exist for every column
concerned, otherwise name the columns without ordering them and say why.

**Window: a ceiling, never a measurement.** The suggestions are not persisted.
They are cleared by instance restarts, failovers and setting a database offline,
and any metadata change on a table deletes every suggestion for that table.
Three refinements, all measured:

- Taking a database offline and back online cleared its suggestions, 225 to 0,
  with no restore and no metadata change. The collector then reported an
  instance start nearly three days earlier for counters seconds old.
- `ALTER INDEX ... REORGANIZE` did NOT clear a table's suggestions and
  `ALTER INDEX ... REBUILD` did. Microsoft's wording covers any `ALTER INDEX`;
  on the tested build only the rebuild behaves that way. Measured by a reader
  and reproduced by the author.
- Creating an index on the table did clear them, as documented.

So the sentence is a ceiling: the counters cover at most the period since the
latest of the instance start, the last recorded restore, the last time the
database came online, and the last metadata change on the table. The archive can
date only the first two, and the second only when `msdb` history survives.

That last caveat has a detectable failure the first draft missed. The common
case is not a missing collector but a present collector with no row for the
database, because `sp_delete_backuphistory` is in every stock maintenance plan:
"restored last week, history pruned on Sunday" and "never restored" are the same
absence, and the fallback to instance start then overstates the window in the
direction that flatters the finding. The root of `60.backup/020.restore-history`
carries `counts.total`, `counts.last_30_days` and `counts.oldest_record`; an
analysis that finds no row for its database and an `oldest_record` more recent
than the instance start is looking at a pruned history and must say so.

**Query identity: possible from SQL Server 2019, and nothing reads it.** Two
readers objected, correctly, that the `missing` result set has no statement
identity: `user_seeks` counts operators that could have used the proposed index,
across queries the archive cannot name, so a report saying "these queries are
scanning" asserts more than the evidence carries. Pushing one question past
where the panel stopped: the engine answers it above 15.x.
`sys.dm_db_missing_index_group_stats_query` returns the queries that needed a
missing index, joined on the group handle, and no collector reads it. Collecting
it reaches query identity and therefore carries a disclosure question, which the
spills collector's history shows is not a formality. It is an open question
below, not a decision here. Below 2019 the objection stands with no remedy, and
the report language is written for that case.

## The column this document proposed, and why it is withdrawn

The first draft added `sys.dm_db_missing_index_group_stats.unique_compiles`,
claiming it separates "a query shape compiled once, whose suggestion may be a
report run by one person last March" from "a shape recompiling thousands of
times".

It does the opposite, and that was measured on a table with statistics
pre-created and auto-creation off, so that nothing recompiled by accident:

| workload | `unique_compiles` | `user_seeks` |
|---|---|---|
| one procedure, one cached plan, 81 executions | 2 | 81 |
| fifteen one-off ad hoc statements differing only by a literal | 15 | 15 |

The fifteen one-off statements are precisely the false lead the column was meant
to mark, and they score higher than the standing workload. Microsoft's own
description says why: it is the "number of compilations and recompilations that
would benefit from this missing index group", and on any workload that is not
fully parameterised an unparameterised family produces one compilation per
literal. Two other readers reached the same conclusion from the other side, each
measuring a cached plan at one compile against a hundred seeks.

The first draft's own open question asked whether `user_seeks + user_scans`
already separates the two cases. On that measurement it does, 81 against 15,
and `unique_compiles` gets it backwards.

So the column is withdrawn. It would have carried a real quantity, compilation
pressure attributable to a suggestion, but this document has no use for that
quantity that survives measurement, and a column added without a rule that
survives is a column someone will invent a rule for later. If a future document
needs compilation pressure, it can argue for it on its own evidence.

The withdrawal also removes an acceptance criterion the panel showed was not
reproducible: the first draft asked an implementer to see `unique_compiles = 1`
for a shape compiled once, and a stored procedure compiled exactly once and
executed forty times reported 2, still 2 after eighty-one executions, with
`sys.dm_exec_procedure_stats` confirming a single plan. The value 1 appeared only
for a single ad hoc statement after clearing the procedure cache. An implementer
handed that criterion would have reported the brief as wrong, or quietly
reshaped the test until it agreed.

## What a report may say, and what it may never do

This section binds the analysis and the report, not the collector.

**Never generate `CREATE INDEX` text from a suggestion.** Not in an appendix,
not commented out, not as an illustration. A statement that can be copied will
be copied, and none of the limits above is visible in the statement once it
exists.

The prohibition is on GENERATING a recommendation, and must be worded that way,
because the corpus legitimately preserves index DDL as collected evidence:
`70.schema/080.modules.sql` returns module definitions verbatim, so a procedure
whose body contains a `CREATE INDEX` string arrives in the archive with that
string intact. A reader demonstrated it. Preserving what exists on the server is
what this tool is for; proposing what does not is what this rule forbids.

**Name the table and the columns, and do not claim an access method.** Without
the query identity the archive lacks below 2019, the defensible sentence is of
the form: "the optimiser recorded 40 000 opportunities to use an index on
`ClientID` and `DateCommande` of `dbo.Commandes`, over at most the period since
2 September". The first draft's example said those queries "are scanning", which
the evidence does not support.

**Report a family, not its members**, with the count it stands for, and allow a
table to carry several families.

**State the window as a ceiling**, with what can and cannot be dated, and say
when the restore history looks pruned.

**Say what an existing index already covers, where one does**, and where the
coverage test abstained, say that too rather than omitting the finding or
implying coverage.

**Name which collector the numbers came from**, since the two spell the same
quantities differently and one can be empty while the other is not.

**Never present `impact_score` as a percentage, a duration or a cost.** If a
number must be shown, show `avg_user_impact_pct` with the words "of the
optimiser's estimated query cost" attached.

## What this deliberately does not do

- **It does not deduplicate inside the collector.** Family formation belongs to
  the analysis; a collector that emitted families would make the raw list
  unrecoverable, and the raw list is what a second opinion needs.
- **It does not add a judgement column**, and no longer adds a column at all.
- **It does not raise `020.properties` above 25 rows**, and does not touch the
  deliberate overlap between the two collectors.
- **It does not propose an index.** The deliverable is a paragraph a DBA can act
  on, and the index that DBA writes is theirs.

## Where each part is accepted, and how

The analysis and the report do not live in this repository: `CLAUDE.md` places
derived analysis outside it and `README.md` says this tool collects and does not
judge. The criteria split accordingly, and saying so is part of the contract.

In this repository:

1. `70.schema/020.index-usage.sql` projects the instance-wide count of
   `sys.dm_db_missing_index_details` on its root, inside the existing `TRY`
   block, and `go test ./...` stays green.
2. A collector test asserts that the new scalar is present and is not the
   database-filtered count. The corpus inventory will NOT change: `inventoryLine`
   emits the path and the sorted profiles and nothing else, which a reader
   verified by running the guard with `-update` and finding the file's checksum
   unmoved. The first draft asked the golden file for something it does not
   represent, and told an implementer to run `refresh-corpus.ps1` for a change
   it cannot record.
3. The grammar tool parses the changed file and its result-set count is
   unchanged at three. Noted with its limit: a parser checks syntax, never that
   a catalog column exists on a version, so it is not evidence for the 2012
   floor. The documentation is.
4. Section 10 bis of the gaps specification is rewritten to say what is in the
   tree, keeps its original sentence quoted as the error it was, and points
   here. Its status line at the head of that document changes with it.
5. No collector GENERATES index DDL. Already true and already enforced twice,
   by the absence of any `CREATE INDEX` in `queries/` and by
   `collect/statementlint.go`, which refuses a `CREATE` that is not scoped to a
   variable or a temporary object. It is a regression guard, not work, and it is
   worded to exclude collected module text.

Outside this repository, in the analysis that consumes the archive: saturation
handling driven by the instance-wide count; family formation with several
families per table and maximal elements preserved; the coverage test consulting
`is_disabled`, `filter_definition` and `hypothetical`, testing set containment
rather than a prefix, and abstaining on ambiguous key strings; ordering only on
applicable density rows; the window stated as a ceiling with pruned-history
detection; and the report language above. Each needs a fixture and a named owner
there, and this document does not pretend they can be accepted here.

## Open questions

- Was the limit 500 on builds before SQL Server 2019? Claimed by one reader
  without a source or a measurement, and not adopted. Someone should settle it
  against a 2016 or 2017 instance before any threshold is hardcoded anywhere.
- Should `070.index-columns.sql` emit escaped identifiers, so the coverage test
  stops abstaining on tables whose column names contain a comma? A corpus change
  and a golden diff, against a case that is rare and that the abstain rule
  already handles safely.
- Should `sys.dm_db_missing_index_group_stats_query` be collected above 15.x, so
  a report can name the queries a suggestion came from? It reaches query
  identity and carries a disclosure question.
- Is adding `70.schema/091.statistics-density.sql` to the `space` profile worth
  its cost, given that the ordering rule now depends on applicable density rows
  rather than on the profile, and that its own single-leading-column limit means
  the profile change would often still not deliver an ordering?

## What the panel changed

Five readers, two prompts, on commit `1d6ef59` against a live SQL Server 2025:
agy twice, codex twice, one Claude subagent. The union was large and the overlap
between any two readers was small, as the method predicts. Every reader found at
least one thing no other found.

Withdrawn: `unique_compiles`, the document's only proposed column, measured to
classify workloads in the opposite direction from the claim that justified it.

Refuted by measurement and rewritten: the per-database truncation check, which
cannot see an instance-wide cap; the equality-prefix coverage rule, which fails
on inequality columns, on disabled indexes, on the unordered nature of
`equality_columns` and on ambiguous key strings; the blanket claim that any
`ALTER INDEX` clears suggestions; and the acceptance criteria, of which one was
a no-op, one was not reproducible, and one asked for a guard that already
exists.

Corrected from the documentation: `impact_score` called meaningless, where it is
a benefit proxy that must simply never be printed as a percentage; and the
window sentence, which omitted offline resets and pruned restore history.

Rejected: that `equality_columns` can arrive in query order. Two readers and the
author measured the opposite, and the reader who claimed it offered no output.

Not adopted pending evidence: the 500-row limit before 2019.

Added by the author after the panel:
`sys.dm_db_missing_index_group_stats_query`, which answers above SQL Server 2019
the query-identity objection two readers raised and neither resolved.

And two facts about running panels rather than about this document. Three of the
five readers reported that they had cleaned up after themselves; thirteen
`mireview_*` databases and eight scripts were still on the container afterwards.
One reader implemented the proposed column in the live working tree despite
having its own worktree. Both were cleaned up by hand, and both are exactly what
the review skill warns about.
