# Missing index suggestions — specification

Status: draft, not implemented. Written 17 September 2026 to answer section
10 bis of [collection-gaps-spec.md](collection-gaps-spec.md), which reserved
this question for a document of its own.

## What section 10 bis says, and what is actually in the tree

Section 10 bis is titled "One gap the review named that this document does not
close", and its first sentence is:

> The missing-index DMVs. `sys.dm_db_missing_index_group_stats` and its siblings
> are absent from the corpus and from this specification.

That sentence is false, and it was false when it was written. The three DMVs are
read by two collectors today, and have been since the corpus was reworked for
client-side JSON:

- `20.databases/020.properties.sql`, result set `missing_indexes`: `TOP (25)`
  ordered by `avg_total_user_cost * avg_user_impact * (user_seeks + user_scans)`,
  projecting that product as `impact_score`, the table, the uses, the average
  impact, and the three column lists.
- `70.schema/020.index-usage.sql`, result set `missing`: no `TOP` and no
  threshold, projecting the table, the three column lists, seeks, scans, average
  cost, average impact, and `last_user_seek` and `last_user_scan`. Its root also
  carries `missing_suggestions`, the count of rows in
  `sys.dm_db_missing_index_details` for the database.

Both are in the `space` profile. The overlap between them is deliberate and the
second file's header states why: the first is a triage view at 25 rows, the
second a baseline that cannot be re-expanded after a restart. That is a
documented decision and this specification does not reopen it.

So the work this document describes is not collection. Everything the optimiser
has to say is already in the archive, twice. What is missing is a contract for
reading it, and one column.

The mistake is worth naming rather than quietly correcting, because it is the
second time this practice has paid for it: the private repository's maintenance
notes record a whole task that shrank to a paragraph when "the notice does not
exist anywhere" turned out to be false. Writing the absence of a thing is an
assertion, and it is the most expensive kind, because nothing in a review
catches it except someone opening the tree.

## Why this needs a contract rather than a collector

A missing index suggestion is not a measurement, and the archive currently
presents it beside measurements without saying so. Four properties make it
different, and all four are why a report must never paste the DDL:

1. **It is per query shape, not per workload.** Each suggestion comes from one
   or more compilations that would have used it. Nothing in it knows what the
   other queries against that table do, so two suggestions on one table are
   routinely near-duplicates differing by a single included column.
2. **It ignores write cost entirely.** The optimiser proposes the index it
   wishes it had for a read. The cost of maintaining it on every insert, update
   and delete is not in `avg_user_impact`, not in the cost, and not anywhere in
   the DMV.
3. **It ignores the indexes that already exist.** A suggestion is emitted even
   when an existing index leads on the same column and would serve the query
   with a different key order or one more included column.
4. **`avg_user_impact` is a percentage of estimated query cost, not of time.**
   An impact of 98 means the optimiser's cost model believed the plan would cost
   two percent of what it did. It does not mean the query will run fifty times
   faster.

`impact_score` in `020.properties` compounds the problem: it is a product of
three numbers with different units, published as a single figure that sorts
well and means nothing on its own. The archive must keep it, because it is the
ordering that made the triage list, and must say what it is.

## What the archive already lets an analysis decide, offline

None of the following needs a new read. Each is computable from files a `space`
archive already carries, and specifying it here is the point of the document.

**Whether an existing index already covers the suggestion.**
`70.schema/070.index-columns.sql` projects, per index, the ordered key list and
the included list. A suggestion whose equality columns are a prefix of an
existing index's keys is not a missing index: it is at most an included-column
change on an index that exists. This comparison is a string one, on data already
side by side in the archive.

**Which suggestions overlap each other.** Two suggestions on the same table
whose equality sets are equal, and whose inequality and included sets differ
only by containment, are one suggestion. Collapsing them is arithmetic on the
three column lists, and Microsoft's own guidance says to do it: "missing index
suggestions should be combined when possible with one another, and with
existing indexes in the current database". How much it reduces a real list is
not stated here, because it has not been measured on one.

**What key order a real index would want.** Section 17 of the gaps
specification was closed by `70.schema/091.statistics-density.sql`, which
carries `all_density_estimate` and `distinct_pct_estimate` per statistic, with
its four caveats attached. The DMV's `equality_columns` is not an ordering
recommendation; the density estimates are what an author uses to choose one,
and they are in the archive.

**Over what period the seeks and scans accumulated.** The suggestions are not
persisted. Microsoft states that they are cleared by instance restarts,
failovers and setting a database offline, and that any metadata change on a
table deletes every suggestion for that table — adding or dropping a column,
creating an index on one, and an `ALTER INDEX` on any index of the table. The
archive can name the first bound exactly: `60.backup/020.restore-history.sql`
carries the last restore per database and is in the `space` profile since
16 September 2026, and `70.schema/020.index-usage.sql` already projects the
instance start time and the seconds since. The second bound, a metadata change
on the table, is not datable from the archive and must be stated as a limit
rather than guessed.

## The engine's own cap, which nothing reports

`sys.dm_db_missing_index_details` and `sys.dm_db_missing_index_groups` are
documented as limited to 600 rows: "If you have more than 600 missing indexes,
you should address the existing missing indexes so you can then view the newer
ones."

So `70.schema/020.index-usage.sql` has a cap after all. Its header calls it a
baseline with no `TOP` and no threshold, and that is true of the collector and
false of the data: at 600 rows the engine has stopped recording new suggestions,
and neither the collector nor the archive says which case a given run is in.
This is the same shape as section 22 of the gaps specification, one level
further out, and it is the second time a bound has been found hiding under a
sentence that said there was none.

The remedy needs no new read. `70.schema/020.index-usage.sql` already projects
`missing_suggestions` on its root, the count of rows in
`sys.dm_db_missing_index_details` for the database. What is missing is the
reading of it:

- the analysis treats `missing_suggestions` at or above 600 as a truncated
  list, not a complete one;
- the report says so in the same sentence as the finding, because on such an
  instance the suggestions shown are not the most important ones but the ones
  the engine happened to still be holding;
- and the collector's header stops calling itself unbounded, naming the
  engine's limit instead.

Whether 600 is per database or per instance is not settled by the documentation
quoted above, and the difference matters for a server with forty databases. The
analysis must therefore compare against the count it has, per database, and say
"at or above the engine's limit" rather than compute a percentage of it.

## The one column this specification adds

`sys.dm_db_missing_index_group_stats.unique_compiles`, projected by
`70.schema/020.index-usage.sql` in its `missing` result set, beside the seeks
and scans it already carries.

It is the count of compilations and recompilations that produced this
suggestion, and it separates two situations the archive currently cannot tell
apart: a query shape compiled once, whose suggestion may be a report run by one
person last March, and a shape recompiling thousands of times, where the
suggestion is a standing property of the workload. A suggestion with a high
impact and a single compile is the classic false lead, and today nothing in the
archive marks it as one.

It is one column on a view already read, inside a `TRY/CATCH` that already
exists, in a collector that already has no `TOP`. It adds no permission, no
timeout risk and no new object.

It is deliberately not added to `020.properties`'s triage list, whose 25 rows
are ordered by a score and read by a human deciding what to look at next. A
sixth column there buys less than the width costs.

## What a report may say, and what it may never do

This section is the reason the gaps specification refused to settle the matter
in a line. It binds the analysis and the report, not the collector.

**Never emit `CREATE INDEX` text derived from a suggestion.** Not in an
appendix, not commented out, not "as an illustration". A statement that can be
copied will be copied, and the four properties above are not visible in the
statement once it exists.

**Name the table and the columns, not the index.** The finding is "queries
against `dbo.Commandes` filtered on `ClientID` and `DateCommande` are scanning,
and the optimiser has asked for an index on those columns 40 000 times since the
instance started on 2 September". That sentence is defensible from the archive.
"Create this index" is not.

**Report a family, not its members.** After the collapse described above, a
table carries one entry with the columns its suggestions agree on, and the
number of distinct suggestions it stands for. A list of eleven near-identical
proposals tells a reader the tool cannot count.

**State the window, every time, with its two bounds.** Since the instance
started or the database was restored, whichever is later; and with the caveat
that creating any index on the table resets the counters for it, which the
archive cannot date.

**Say what an existing index already covers, where one does.** A finding that
does not mention the index leading on the same column will be answered by the
DBA who knows it exists, and correctly.

**Never present `impact_score` as a percentage, a time or a cost.** It is an
ordering key. If a number must be shown, show `avg_user_impact` with the words
"of the optimiser's estimated query cost" attached to it.

## What this deliberately does not do

- **It does not deduplicate inside the collector.** The collapse belongs to the
  analysis. A collector that emitted families would make the raw list
  unrecoverable from the archive, and the raw list is exactly what a second
  opinion needs.
- **It does not add a judgement column.** Neither the collector nor the archive
  says whether a suggestion is worth acting on. That decision needs the write
  volume of the table, which no `space` archive carries.
- **It does not raise `020.properties` above 25 rows**, and does not touch the
  overlap between the two collectors.
- **It does not propose an index at all.** The deliverable of this work is a
  paragraph a DBA can act on, and the index that DBA writes is theirs.

## Acceptance criteria

1. `70.schema/020.index-usage.sql` projects `unique_compiles` in `missing`, the
   corpus inventory records the change, and the file still parses under the
   SQL Server 2012 grammar with its result-set count unchanged.
2. Run against an instance where a query shape has compiled more than once, the
   column is greater than one; against a shape compiled once, it is one. Both
   measured, not reasoned about.
3. Section 10 bis of the gaps specification is rewritten to say what is in the
   tree, keeps its original sentence quoted as the error it was, and points
   here.
4. No file in `queries/` emits index DDL, which is already true and stays true.
5. The header of `70.schema/020.index-usage.sql` no longer describes its
   `missing` result set as unbounded, and names the engine's 600-row limit.
6. The analysis marks a database whose `missing_suggestions` is at or above 600
   as truncated, and a report about such a database carries that sentence.

## Open questions for review

- Is `unique_compiles` worth the column, or does `user_seeks + user_scans`
  already separate the one-off report from the standing workload? The claim
  above is that it does not, because a single compile can accumulate many seeks
  across executions of the same cached plan. That claim should be attacked.
- The collapse rule treats two suggestions as one when their equality sets are
  equal and their inequality and included sets differ only by containment. Is
  equality on the equality set too strict, given that `equality_columns` is
  unordered in meaning but delivered as an ordered string?
- Should the report's window sentence be refused outright when
  `60.backup/020.restore-history` is missing from the archive, or stated with
  the instance start alone and a named uncertainty?
- Is the 600-row limit per database or per instance? The documentation quoted
  here does not say, and the specification works around the question rather
  than answering it. Someone should settle it by measurement before the
  work-around becomes the contract.
