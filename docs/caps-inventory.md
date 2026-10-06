# Caps inventory, 4 October 2026

Status: an inventory, not a design. No code changes with it. It lists every
place where the collector, by default or under a flag, returns less than the
server holds, so that the decision on which caps to raise is taken against the
whole list and with numbers.

The question behind it: the default collection should carry everything that is
useful and raises no confidentiality concern. The disclosure options stay
closed, and the two cost options (`--estimate-compression`,
`--measure-page-density`) stay opt-in. What remains to decide is the caps.

## Method

The corpus is `queries/**/*.sql` at 0.37.0 (commit `c20b1e4`), read file by
file: every `TOP (n)` above one, every row-bounding `DECLARE`, every `LEFT`,
`SUBSTRING` or `nvarchar(n)` truncation of a value, every window in days, and
`@timeout` only where it bounds a volume. On the Go side: the byte and count
constants in `collect/` (`maxRunBytes`, `maxPlanBytes`, `maxPlansPerQuery`,
`maxModules`, `maxDeadlockGraphs`, `maxBlockedProcessReports`,
`maxPlanCache*`), the configuration defaults, and the watch limits. `TOP (1)`
lookups, number tables, `SET LOCK_TIMEOUT`, the linter's recursion depth and
display widths in the TUI are not caps on what the archive says and are left
out.

For each cap, four sources:

- the header or comment that states its reason, quoted;
- the analysis scripts of the private repository (`analyser-collecte`), read to
  see who consumes the set and whether they say that a count is a floor;
- the archive itself, to see whether it says when the cap was reached;
- twelve real collections taken on eight client instances between August and
  September 2026, by collector 0.18.0 (3), 0.21.0 (5) and 0.23.0 (4, three of
  them under `--profile space`), 27 databases in all. Several are near
  duplicates of the same instance, so a count "per collection" overstates the
  number of distinct servers; the 70.schema counts below use the latest
  collection of each instance. Ten earlier development builds were used only
  where nothing newer carried the set.

Collectors added after 0.23.0 appear in no real collection. Their rows say so,
and their proposals rest on the code and the lab alone.

Costs were measured on the lab instance (SQL Server 2025, Linux container)
where raising had a cost nobody could state. The lab is small: its largest
usable database has 60 tables and 128 statistics, and its plan cache holds
about 2 500 statements. It says how the cost scales, never what a client
server would pay. Where the real collections carry a duration or a byte count
(`_run.json` `results[].duration_ms`, the size of each file), that is the
figure used, and the lab only fills the gap.

Columns of every table:

- Cap: what is bounded, and the value.
- Stated reason: quoted from the file, or "none stated".
- Analysis: which script reads the set, and whether a floor or "cap reached"
  is said.
- Reported: the field in the archive that tells the reader the cap was
  reached, or "no".
- Real collections: how often the cap was hit, and what it cut.
- Proposal: raise to N, remove, or keep, with the measured or estimated cost.

JSON in these archives zips about ten times smaller (four to twenty-two), so
a raw megabyte is about 100 KB in the archive the client sends.

## Caps for archive size or readability

None of these protects anything confidential: the rows past the cap are of the
same kind as the rows before it. They are the candidates for raising. Some sit
inside a collector that is itself behind a disclosure flag; raising them there
widens nothing the flag did not already open.

### 10.system

| Collector | Cap | Stated reason | Analysis | Reported | Real collections | Proposal |
| --- | --- | --- | --- | --- | --- | --- |
| `045.default-trace-detail` (opt-in `--include-default-trace`) | 5 000 rows, most recent first | "Five rolling 20 MB files can hold far more than an archive should carry, and the aggregate in 044 is what reports the true totals" | no script reads it | yes: `row_cap`, `capped`, `counts.events`, `window` | hit 3 of 3: 5 000 kept of 30 611, 14 262 and 15 050. Kept window 10 min against 68 min in 044, 24 h against 70 h. In one, class 22 (configuration change) had 21 events in 044 and 7 kept; class 116 (DBCC) 40 and 14 | Keep every row of the rare classes (20, 22, 92 to 95, 104, 105, 108, 115, 116) and cap only the flooding ones (46, 164), or raise to 30 000. About 360 B a row, so 11 MB raw for the largest seen. No server cost: the `ORDER BY StartTime DESC` already reads and sorts every row |
| `040.error-log` `top_messages` | 40 prefixes by count | "TOP 40 by count, and the cut is REPORTED above rather than left implicit" | no script reads it (`mesures.py` reads other sets) | yes: `status.top_messages_kept`, `distinct_message_prefixes` | hit 15 of 15 (development builds included). The 40 covered 54 % to 90 % of lines. On one instance 5 313 prefixes and 11 236 lines fell outside | Raise to 200. About 600 B a row, +100 KB at most. No server cost: the log is already in `#log` and grouped |
| `040.error-log` `notable` | 200 lines, oldest first | "Le cap est de 200 lignes et il est reporté" | no script reads it | no (see the section on stated reasons) | no hit: at most 138 | Report the cap and a total, and order newest first, or remove the cap. No cost |
| `040.error-log` `sample`, `message` | `LEFT(…, 400)` | none stated | none | no | `sample` reached 400 characters once or twice in 14 of 15 collections | Raise to 1 000. A few KB |
| `049.open-transactions` | 50 transactions, user first then oldest | none for the value | no script | yes: `listing_cap`, `counts.*`, `oldest.*` over all rows | not in any collection | Keep, or 200 at about 200 B a row |
| `050.tempdb` `oldest_snapshot_transactions` | 5 | none stated | `mesures.py`, `comparer_instances.py` read 050, not this set | partly: `version_store.active_snapshot_txn_count` | no hit: at most 1 | Keep |
| `060.system-health` deadlocks | 200 timestamps | none for the value | no script (deadlocks are read from 061) | partly: `session.deadlocks` | no hit: the ring held 15 to 46 minutes and no deadlock in 9 of 9 | Remove (25 B a row) |
| `061.deadlock-graphs` (opt-in) | 100 graphs, 1 MiB each (`maxDeadlockGraphs`, `maxDeadlockBytes`) | "TWO CAPS, NEITHER SILENT"; one graph of a megabyte is "the archive rather than the reader that has to be protected" | `deadlocks_resume.py` prints "N écrits sur M dans l'anneau" | yes: `caps.*`, `omissions[]` | no hit in 5: at most 75 graphs, 4.0 MB, largest 97 KB | Keep |
| `063.blocked-process-reports` (opt-in) | 500 reports, one per episode, longest first; 1 MiB each | "It bounds the FILES, not the rows, and not the episodes" | `blocages_resume.py` rebuilds the episodes past the cap and labels them "(hors plafond)" | yes: `caps.*`, `omissions[]`, `counts.in_files` | 1 hit of 6, at 0.18.0 under the old recency rule: 8 095 reports, 7 595 omitted (41 MB in all). The episode rule has not met a real capture since | Keep |
| `064.server-diagnostics` intervals | 400, newest first | "three hundred rows of thirty integers is smaller than one execution plan" | `seuil_parallelisme.py` takes the decision from the root and says the hours are "plafonnés par le collecteur" | yes: `session.intervals` | not in any collection | Keep. The source is the limit, not the cap (see the section on stated reasons) |
| `080.loaded-modules` | 500 non-Microsoft modules | "far beyond what a process loads, and the root says how many there were" | no script | yes: `listing_cap`, `counts.other` | not in any collection | Keep |
| `095.master-user-objects` | 200, newest first | "the newest 200 still name the practice" | no script | yes: `objects_total`, `objects_listed` | no hit | Keep |
| `010.properties` `memory_clerks` | 15 clerks above 100 MB | none stated | not read | no | no hit: at most 7 | Keep |
| `015.buffer-pool` clerks | 25 | none stated | no script | no | hit 9 of 9, but the 25th clerk held 1 to 3 MB | Raise to 50 (a few hundred bytes), or keep |

### 20.databases, 40.security, 50.agent, 60.backup, 90.availability

| Collector | Cap | Stated reason | Analysis | Reported | Real collections | Proposal |
| --- | --- | --- | --- | --- | --- | --- |
| `60.backup/010.history` `recent` | 200 backups, instance-wide | "says what happened last night"; the comment on `fulls` admits "200 rows cover the last few hours" | no script reads `recent` | implicitly: `window.backups_in_window` against 200 | hit 6 of 12. 200 rows covered 9 h of a 30-day window on the busiest instance (15 183 backups), 32 h and 37 h on two others, 3, 8 and 16 days elsewhere | Raise to 2 000, or to the whole window (4 MB raw on the largest seen). About 260 B a row |
| `60.backup/010.history` window | 30 days | "THIRTY DAYS IS A WINDOW, NOT A RETENTION" | `mesures.py` | yes: `window.backups_older`, `window.oldest_record` | n/a | Keep |
| `60.backup/010.history` `fulls` | 10 per database | "covers more than a week of nightly fulls" | `differentielle.py` says when the base is beyond the cap ("plafond") | implicitly | not in any collection | Keep |
| `50.agent/010.jobs` history window | 30 days | "An unbounded sp_help_jobhistory is unpredictable in size, and a run count with no window is unreadable" | `supervision.py`, `mesures.py` (requested window against real one) | yes: `window.days_requested`, `observed.oldest_run_date` | the window was the limit in 3 of 12; msdb retention was shorter in the other 9 | Raise to 90. The output is one row per job, so no archive cost; the server reads at most what msdb keeps |
| `20.databases/020.properties` `largest_objects` | 20 | none stated | no script; 70.schema/010 carries every size | no | 9 of 30 database units hit | Keep (a triage view of data that is complete elsewhere) |
| `20.databases/020.properties` `unused_indexes` | 25 | 70.schema/020 calls it a "triage view" | no script; 70.schema/020 has every index | no | 12 of 30 hit | Keep |
| `20.databases/020.properties` `missing_indexes` | 25 | same | `index_chevauchement.py` uses it only as a fallback and says "la vue de tri à 25 lignes" | no | 8 of 30 hit | Keep |
| `20.databases/025.fragmentation` output | 25 of at most 100 measured | none stated | no script | no count above the floor before the TOP | older builds (same TOP inside 020): 5 of 30 hit | Remove. The measurement cap already bounds it at 100 rows; about 150 B a row, no server cost |
| `20.databases/028.change-tracking` `internal_tables` | 200 | none stated | no script | partly: `counts.internal_tables_all` includes empty tables | not in any collection | Remove, or keep and add an exact count. Tens of rows in practice |
| `50.agent/050.commandlog` listings | 100 open or failed, 25 longest per type | none stated beyond the description | `mesures.py` | yes for the first (`counts.*` against `caps.open_or_failed`), partly for the second | not in any collection | Keep |
| `60.backup/020.restore-history` | 200 | "an archive nobody can read is not evidence" | `index_chevauchement.py` reads the uncapped `per_database` | implicitly: `counts.total` | no restore in 9 of 9 | Keep |
| `90.availability/030.log-shipping` errors | 200 | "a chain failing for months would otherwise fill the archive with one message" | no script | yes: `errors_listing_cap`, `counts.recorded_errors` | no error in 7 of 7 | Keep |
| `90.availability/042.replication-distribution` `repl_errors` | 50 rows, `LEFT(error_text, 512)` | none stated | `mesures.py` prints "{n} erreur(s) sur {fenêtre} jours", a count that would be a floor at 50 | no total | no error in 3 of 3 | Keep 50 and add the count over the window |
| `40.security/032.server-trigger-definitions` (opt-in) | 1 MiB per definition | "the cap 080.modules.sql uses; the row survives with its length" | none | yes: `source_state = 'above_cap'` | not in any collection | Keep |

### 70.schema

| Collector | Cap | Stated reason | Analysis | Reported | Real collections | Proposal |
| --- | --- | --- | --- | --- | --- | --- |
| `060.columns` | columns of the same tables as 010 (TOP 200 by rows union TOP 50 by reserved pages) | "Two different selections in one directory would be a trap"; the size reason is 010's | `conversion_index.py` returns NON_JUGE "table absente de 060 (N tables listées sur M)" and says the cap | yes: `tables_covered`/`tables_total`, `columns_listed`/`columns_total` | hit in 7 of 27 databases (207 to 846 tables). Columns listed: 97 %, 83 %, 48 %, 46 %, 45 %, 40 %, 35 %; 37 708 columns missing in all, 8 572 of 24 823 at worst | Remove, with 010. About 465 B a column: the worst database goes from 4.0 to about 11.5 MB raw (about 0.8 MB zipped). Real time today 6.9 s there, mostly catalog work that already counts every column |
| `010.objects` `tables` | TOP 200 by rows union TOP 50 by reserved pages | "a database with 5 000 tables would otherwise produce an archive nobody opens, and the tail of that list is empty tables" | no `analyser-collecte` script reads it; the space note's `plafonds.py` leaves its completeness "indéterminée" | yes: `listing_cap`, `listing_cap_by_size`, `counts.tables` | same 7 databases; 7 to 646 tables cut. The 50-by-size branch never added a table. In 3 of the 7 the 200th table held 1 095, 3 214 and 472 542 rows | Remove. About 300 B a table, +200 KB raw at worst; row and page counts are already read to rank |
| `010.objects` `untrusted_constraints`, `deprecated_types` | 200 each | "capped by the same reasoning" | `deprecated_types` feeds 4 pieces of evidence in the space note | yes: `counts.*` beside the lists | no hit; largest 160 and 173 | Remove. Cheap, and the second came close |
| `090.statistics` `statistics` | TOP 200 tables by rows | "THE FULL DETAIL COVERS THE SAME 200 TABLES AS 010.objects.sql" (false, see below); `statistics_all` is uncapped since 0.36 | `statistiques_inutiles.py` reads only the capped set and prints "tout compte ci-dessous est un plancher"; `mesures.py` uses `statistics_all` when present | yes: `listing_cap`, `statistics_listed`/`statistics_total` | hit in 6 databases: 1 183 of 1 193, 637 of 1 001, 1 300 of 1 730, 1 109 of 1 674, 2 207 of 2 439, 5 617 of 12 996 | Remove. Since 0.36 the server already reads every statistic; only output grows, about 420 B a row, +3.1 MB raw at worst |
| `091.statistics-density` | TOP 200 tables by rows | "THE 200-TABLE CAP STAYS … If audits start meeting such candidates, the cap is the thing to lift" | `index_chevauchement.py` says "091 ne lit que les 200 plus grosses tables" | partly: `listing_cap`, no total | collected in 3; hit in 1: 1 109 of 1 674 statistics | Remove. Lab: 0.27 ms a statistic, so about +3.5 s and +4.3 MB raw for 13 000 statistics |
| `040.compression` `largest_uncompressed` | 200 (table, index) groups | none stated | the space note says 040 carries no eligible count | no total of groups | hit in 9 of 27: up to 1 617 uncompressed units against 200 | Raise to 2 000 and add a total. About 165 B a row, +230 KB at worst. The per-row filegroup lookup is unmeasured at that size |
| `041.compression-savings` `not_estimated` | 100 | "THE LIST STAYS CAPPED AND THAT IS RIGHT" | `plafonds.py` says "au moins" when the array is full | yes: `not_estimated_objects` | full in 6 collections | Raise to 1 000 (under 150 KB). Low value |
| `030.index-operational` `heaps` | 200 | "the same cap as the two others" | not read by the scripts | no heap total (010 has `counts.heaps`) | hit in 4 databases (up to 810 heaps) | Remove. The DMV is already read in full; about 200 B a row |
| `030.index-operational` `contention`, `page_compression` | 200 each | none, and "the same cap as the two others" | `contention` feeds 2 pieces of evidence | no total for `contention`; `page_partitions.reporting` for the other | no hit (at most 79; 0) | Keep, or remove for free |
| `045.columnstore` partitions, trim reasons | 200 and 400 | none stated | not read | no | not in any collection | Raise to 2 000 and add a partition count |
| `021.missing-index-queries` | 20 queries per suggestion, 1 000 rows in all | "THE CAP IS PER SUGGESTION … A single list capped by seeks drops whole suggestions"; none for the 1 000 | not read | yes: `counts.*_before_cap`, `listing_cap*` | not in any collection | Raise 1 000 to 12 000 (the engine's 600 groups times 20). About 300 B a row, 3.6 MB at most |
| `080.modules` (opt-in `--include-object-definitions`) | 2 000 modules, 1 MiB each (`maxModules`, `maxModuleBytes`) | "a single database filling the run budget would starve every collector after it" | `mesures.py`, `candidats_tsql.py`, `blocages_resume.py` say "au-delà d'un plafond" | yes: rank, count, omissions | at most 1 187 modules in 13 databases; no omission | Keep |

### 80.workload

| Collector | Cap | Stated reason | Analysis | Reported | Real collections | Proposal |
| --- | --- | --- | --- | --- | --- | --- |
| `020.query-store` `top_queries` | 50, round robin over duration, CPU and reads | none for the number; the totals "say nothing about the share of the whole it represents" | no script reads the list | indirectly: `listing_cap` against `counts.queries` | hit in 9 of 11 non-empty stores (238 to 42 630 queries) | Raise to 200. No server cost (the whole store is already ranked); 30 to 43 KB per 50 rows, so +100 to 130 KB a database |
| `020.query-store` `by_query_hash` | 50 | "the same round robin with the hash as the key" | no script | `listing_cap` | not in any collection | Raise to 200 |
| `023.query-store-most-executed` | 50 by call count, twice | "this is the RBAR-hunting ranking" (the order, not the number) | no script | no | hit in the same 9 of 11; `by_object` is uncapped (up to 337) | Raise to 200. Server cost unchanged (42 to 70 s already paid for the full aggregation); +100 KB a database |
| `024.query-store-rowcount` | 50 | "like 023, so the two files rank the same population" | no script | no | 1 of 1 hit | Raise with 023 (they must stay equal) |
| `026.query-store-interrupted` | 50 per listing | none stated | no script | no | not in any collection | Raise to 200 |
| `028.query-store-resources` | 50 over four measures | none stated | no script | no total | not in any collection | Raise to 200 (each measure reaches about 12 deep today) |
| `025.query-store-compare` | `QUERY_STORE_TOP` | inherits 021 | no script | `selection.cap` | not in any collection | Keep tied to `QUERY_STORE_TOP` |
| `029.query-store-load-profile` | 1 488 intervals | "62 days of hourly intervals … at most some 500 KB of archive" | `profil_charge.py` prints "liste coupée à N intervalles sur M" | yes | not in any collection | Keep |
| `021.query-store-detail` plans per query | 3 with XML (`maxPlansPerQuery`) | "Every recompile adds a plan … fully materialised in memory" | rows kept, XML named in omissions | yes: `selection.plans_beyond_cap`, omissions | hit in 3 of 6 units: 2 816 and 2 927 plans beyond the cap over 27 queries, 1.5 GB of XML by `bytes`; 4 plans elsewhere | Keep. Raising it would multiply a unit that already writes 47 MB |
| `021`, `022` plan size | 8 MiB (`maxPlanBytes`) | "an operator has no way to pick a good value before seeing the plans" | named omission with size | yes | no hit | Keep |
| `041.plan-cache-plans` per ranking | 25 (`maxPlanCachePerMetric`) | "'the top 25 by four measures' and 'the top 100 by CPU' are different selections" | `query_store_fenetre.py` only points to it | yes: `counts.cap_per_metric`, `counts.selected` | 41, 41 and 56 plans selected, 2.6 to 4.6 MB | Raise to 50 (about 5 to 9 MB) |
| `041.plan-cache-plans` total and size | 100 plans (`maxPlanCachePlans`), 4 MiB a plan | "the hundred is a ceiling rather than a quota" | as above | yes | the 100 cannot bind (4 × 25); the 3 null plans seen were the engine's, not the size cap | Keep, tied to four times the per-ranking cap |
| `060.spills` | 50 by pages spilled | "the list is read to decide where to look first" | no script | partly: `statements.spilling` | not in any collection | Raise to 200. One scan either way |

### Go side

| Constant | Cap | Stated reason | Reported | Real collections | Proposal |
| --- | --- | --- | --- | --- | --- |
| `maxRunBytes` (`collect/runfile.go`) | 256 MiB of payload per run, refused writes named by `budgetReason` | "stayed generous on purpose rather than being retuned down" | yes: omission "the run reached the N MiB cap" | never reached. Largest raw run 60 MB (23 %); no budget omission in any `_index.json` | Keep. It is the guard that makes the raises above safe |
| `maxBlockedWaits` (`collect/manifest.go`) | 100 waits of the collector's own sessions | "A run that blocks a hundred times has said what it has to say" | yes: `truncated`, `omitted_count` | not counted (the watch postdates most collections) | Keep |

## Caps for server cost

These bound work on the client's server. Raising them costs time or buffer pool
there, so each proposal names the cost.

| Collector | Cap | Stated reason | Analysis | Reported | Real collections | Proposal |
| --- | --- | --- | --- | --- | --- | --- |
| `80.workload/030.implicit-conversions` candidates | 200 statements kept after the text prefilter | "only after it has taken one of the two hundred places, which matters only when more than two hundred statements match" | `conversion_index.py` cannot know | no: no `bounds.candidates`, the cut is silent | not visible. Lab: 466 statements matched, 200 kept; at 1 000, 18 conversions found instead of 15, 7.6 s instead of 5.9 s | Raise to 1 000 (equal to `@examined`) and add `bounds.candidates` |
| `80.workload/030.implicit-conversions` `@examined` | 1 000 statements by logical reads | "Rendering a plan as text costs CPU in proportion to its size" | `conversion_index.py` prints "parmi les N plus lourdes … sur M du cache" | yes: `bounds.statements_examined` against `bounds.plans_in_cache` | old form (200 plans): 9 of 9 hit, 200 of 1 637 to 156 845 in cache, 0.7 to 4.4 s | Raise to 2 500. Lab: whole cache 6.2 s against 5.9 s. On a client the cost follows plan bytes: the header's 1.2 s for 66 MB puts a 500 MB window near 10 s |
| `80.workload/053.plan-warnings` candidates | 200 after the prefilter | "at the price of one of the two hundred places" | no script | partly: `bounds.candidates` = 200 shows the cut, not its size | not in any collection. Lab: 894 matched, 200 kept; at 1 000, missing join predicates 16 to 38, plan-affecting converts 86 to 209, optimizer timeouts 50 to 157, for 9 s to 27 s | Raise to 500 and add `bounds.matched` |
| `80.workload/053.plan-warnings` `@examined` | 1 000 by CPU | same text as 030 | no script | yes | not in any collection | Keep (the candidate cap binds first) |
| `80.workload/042.parallel-cost-distribution` `@examined` | 500 by CPU | "the 500 fragments are bounded in number, not in bytes" | `seuil_parallelisme.py` says "cache maigre" below the cap | yes: `examined.*`, including `share_of_cache_cpu_pct`, `plan_kb`, `duration_ms` | not in any collection | Raise to 1 000. Lab: 500 = 99 % of cache CPU, 2.2 s; whole cache 5.4 s. On a client the archive's own `plan_kb` and `duration_ms` give the price afterwards |
| `80.workload/021.query-store-detail` | `QUERY_STORE_TOP`, default 50, no upper bound | "fifty slots reach deeper than fifty divided by four" | `query_store_fenetre.py` reads the window only | yes: `selection.cap`, `selection.ranked` | hit in 4 of 6 units; one wrote 47 MB in 248 s and 295 s against a 300 s timeout | Keep 50 as the default; the operator can raise it |
| `80.workload/027.query-store-stats-usage` | 2 000 plans, 200 MB of plan text per database | "5 000 plans is one to one and a half minutes of client CPU per database"; "to keep the cost under the 300 s timeout" | `statistiques_inutiles.py` marks "(plancher)", `index_chevauchement.py` says "tronqué au plafond" | yes: `truncated`, `cap`, `plans_total`, `budget.*` | not in any collection | Keep |
| `80.workload/043.query-store-parallel-cost` | 1 000 parallel plans per database by parallel CPU, read 100 at a time; no new hundred is begun past 100 MB of plan text or 10 s, so one hundred can pass either | "THE CAP AND THE BUDGETS ARE CONSTANTS AND NOT OPTIONS, as in 027" | `seuil_parallelisme.py` qualifies a database by `examined.share_of_parallel_cpu_pct` rather than rejecting it | yes: `cap`, `chunk`, `budget.*`, `examined.*`, `truncated` | not in any collection (added 6 October 2026) | Keep. The archive's own `selection.duration_ms`, `examined.duration_ms` and `examined.bytes_read` give the price |
| `70.schema/041.compression-savings` (opt-in `--estimate-compression`) | 20 objects of 100 MB or more | "the estimate cost is roughly proportional to size and the tail is where the cost/benefit inverts" | `plafonds.py` computes from `not_estimated` | yes: `bounds.*`, `attempted_objects`, `not_estimated_objects` | hit in 3 of 17; 154, 126, 77 and 53 s for 20 objects | Keep, or 40 after adding a time budget like 055's (estimated 300 s) |
| `70.schema/055.page-density` (opt-in `--measure-page-density`) | 50 partitions, 1 500 s | "tens of gigabytes pulled into the buffer pool" | `plafonds.py` compares measured with eligible | yes: `counts.eligible_partitions`, `sample.measured_partitions` | hit 2 of 2: 146 and 313 eligible; 720 and 659 s | Keep, or 150 with the time budget as the bound (up to +780 s) |
| `70.schema/050.heaps` | 50 heaps, 200 000 pages, 240 s | "The cap of 50 heaps is a count, and a count bounds nothing … each candidate is priced" | no eligible count for the space note | partly: `sample.*`, `skipped_budget`, no eligible total | 0.18.0: 4 databases at 50 (108 to 810 heaps); since 0.21 no hit | Raise to 100 and let the page and time budgets bound it; add the eligible count |
| `20.databases/025.fragmentation` | 100 largest partitions, 150 s budget | "the budget bounds the number of calls, not the length of one" | no script | yes: `eligible_partitions`, `measured_partitions` | not in any collection | Keep. The lab cannot say what a raise would cost |
| `50.agent/050.commandlog` | 100 000 rows within 30 days | measured "36 029 pages … 1.2 s" | `mesures.py` | yes: `caps.rows_reached` | not in any collection | Keep |
| `90.availability/042.replication-distribution` | 7-day window, comments cut to 512 in the CTE | "a window sort of a week of history … spills"; full width "turns a diagnostic into a memory grant that spills" | `mesures.py` | yes: `window_days` (comments: no) | no hit measurable | Keep |
| `10.system/040.error-log` size guard | log above 50 MB not read | "the copy costs the instance more than the summary is worth" | `mesures.py` says the log was not read | yes: `skipped_for_size`, `log_size_bytes` | no hit; largest 107 829 lines | Keep |
| `watchIdentifyPerUnit` (`collect/watch.go`) | 3 identity reads per unit | "so a convoy cannot turn the watch into a querying loop of its own" | none | no | n/a | Keep |

## Caps for disclosure

Kept by decision. Named here so that the list is complete.

| Collector | Cap | Stated reason | Real collections |
| --- | --- | --- | --- |
| `50.agent/020.job-steps` | `LEFT(command, 200)` for T-SQL steps, other subsystems not projected | "a T-SQL step whose first 200 characters contain a literal password would carry it" | 16 of 255 T-SQL steps cut, all at 0.23.0 (longest 46 438 characters). The full text exists behind `--include-job-step-commands` (021) |
| `50.agent/010.jobs` `last_run.message` | `LEFT(…, 512)` | "written by whatever the job runs" | none of 8 messages reached 512 |
| `80.workload/020`, `023`, `024`, `025`, `026`, `028` | `LEFT(query_sql_text, 500)` | "application SQL and can contain literal values from the workload, which is a disclosure decision" | 54 % of 020's listed texts and 33 % of 023's reached 500. The full text is in 021 behind its flag. No per-row flag says a text was cut |
| `10.system/042.connection-security` pools | 200 groups of host, program, login | "THE LIST IS CAPPED AT 200 GROUPS, largest first, and the root carries the total" | not in any collection |
| `10.system/052.session-text` (opt-in) | 5 transactions with text | "Same TOP (5) and same ORDER BY as 050.tempdb.sql, so the rows correspond" | no hit |

## Other limits

Not caps on volume for a reason of size, cost or disclosure, listed so that
nobody looks for them again.

| Where | Limit | Why |
| --- | --- | --- |
| `10.system/040.error-log` | grouping on `LEFT(Txt, 80)` | the log is localised; "A prefix works in every language". Reported as `grouping_prefix_length` |
| `10.system/062.xe-sessions` | setting values as `nvarchar(400)` | none stated; longest seen 88 characters |
| `10.system/020`, `025` | registry values as `nvarchar(1024)` | none stated; a startup parameter is short |
| `20.databases/025.fragmentation` | partitions above 1 000 pages and 10 % | an eligibility floor, not a cut |
| `60.backup/010.history` `devices` | the path cut at the last backslash, to group by directory | grouping key; see the stated reasons below, it does not group virtual devices |
| `70.schema/020.index-usage` | 600 missing-index groups | the engine's limit, reported as `missing_suggestions_instance` |
| `QUERY_STORE_DAYS` | 7 days by default | a default window the operator sets; `maxQueryStoreDays` (3 650) guards an int64 overflow, not a volume |
| `collect/collect.go` | 64 MiB when reading a previous manifest | "A manifest is kilobytes. The bound is against an archive that is not one of ours" |

## Stated reasons that are false or stale

Each was checked against the code; the archive counts come from the real
collections.

- `70.schema/090.statistics.sql`, line 31: "THE FULL DETAIL COVERS THE SAME
  200 TABLES AS 010.objects.sql, by the same ordering and the same tie-break."
  False. 010 and 060 take the union of the 200 tables with the most rows and
  the 50 with the most reserved pages; 090 (two places) and 091 take only the
  first 200 by rows. A large LOB table with few rows is in 010 and 060 and in
  neither statistics file. `TestObjectsAndColumnsSelectTheSameTables` checks
  010 against 060 only. It has not shown in the real collections, where the
  size branch never added a table.
- `70.schema/010.objects.sql`: "the tail of that list is empty tables". True in
  4 of the 7 databases that hit the cap, false in 3, where the 200th table
  held 1 095, 3 214 and 472 542 rows and 52, 382 and 646 non-empty tables were
  cut.
- `70.schema/091.statistics-density.sql`: "If audits start meeting such
  candidates, the cap is the thing to lift." The condition is met: on one real
  database 51 of 206 missing-index suggestions sit on tables outside the 200.
- `10.system/040.error-log.sql`, set `notable`: "Le cap est de 200 lignes et
  il est reporté." Nothing reports it: the status object carries
  `top_messages_kept` and `grouping_prefix_length`, no notable cap and no
  notable total. The set is also ordered by `LogDate` ascending, so a cut keeps
  the 200 oldest lines and drops the most recent ones without a word.
- `10.system/064.server-diagnostics.sql`: "the window is about a day". The file
  reads only the system_health ring buffer, which spanned 15 to 46 minutes on
  all 9 real instances that carry 060, that is 3 to 9 rounds. The 400 never
  binds; the day holds only in the lab, whose event file (not read by 064) kept
  about 143 rounds over ten days.
- `10.system/045.default-trace-detail.sql`: "this file exists to show what the
  events WERE, not to count them" assumes that the 5 000 most recent rows are a
  fair sample. On one real instance 14 of 21 configuration changes were pushed
  out by a flood of class 164.
- `50.agent/020.job-steps.sql`, line 48: a collector projecting full command
  bodies "belongs behind an opt-in flag, like session text, and does not exist
  yet." Stale: `50.agent/021.job-step-commands.sql` exists behind
  `job_step_commands`.
- `50.agent/010.jobs.sql`: the 30-day window is justified by size, but the
  output is one row per job whatever the window; the window bounds only the
  server-side work, and msdb retention was the real limit in 9 of 12.
- `60.backup/010.history.sql`, set `devices`: "Grouped by device rather than
  listed per backup … The directory, not the file." False for virtual devices
  (VSS or third-party agents), whose names carry no backslash: in 7 of 12 real
  collections `devices` has one row per backup, up to 15 183 rows. In one
  file that is 2.4 MB, 97 % of it, against 55 KB for the capped `recent`. It is
  an unbounded set whose stated bound does not hold, and it inflates the
  number of backup targets `mesures.py` reports.
- `80.workload/027.query-store-stats-usage.sql`: "the collectors that bound
  themselves (80.workload/030.implicit-conversions and 70.schema/090.statistics)
  do it with a DECLARE". Stale for 090, which uses a literal `TOP (200)`. The
  same header's "12 to 17 ms per plan" sits beside a later measurement of 56 ms
  a plan, and no longer prices the 2 000.
- `80.workload/030.implicit-conversions.sql`: "Twelve rows of roughly 100 KB is
  a megabyte of table variable." Stale: the candidate set now holds up to 200
  fragments, and the lab filled it. The file reports `statements_examined` but
  no candidate count, so a cut by its `TOP (200)` is silent, against its own
  rule that a result "means nothing without knowing how much of the cache was
  looked at".
- `80.workload/041.plan-cache-plans.sql`: "eight hundred procedures whose plans
  run from 0.5 to 2 MB would produce a multi-gigabyte archive." Overstated:
  800 × 2 MB is 1.6 GB, and the 256 MiB run budget stops it first.
- `70.schema/021.missing-index-queries.sql`: the per-suggestion cap exists so
  that whole suggestions are never dropped, but the outer `TOP (1000) ORDER BY
  group_handle` drops them once 50 suggestions carry 20 queries each, in
  handle order. No reason is given for the 1 000.

On the analysis side, two readers lag the collector. `conversion_index.py`
reads `bounds.statements_examined`, which no real collection carries (0.18.0
to 0.23.0 write `plans_examined`), so its header line prints an empty count on
them. `statistiques_inutiles.py` reads only the capped `statistics` set and
will keep calling its counts a floor on archives that carry the uncapped
`statistics_all` (0.36 and later).

## Proposals

Sorted by value for the audit. Each one is a default change; none opens a
disclosure.

1. `70.schema/060.columns` and `010.objects` `tables`: remove the cap. Hit in 7 of 27 databases, up to 65 % of columns missing, 27 pieces of evidence rest on it; +7.6 MB raw (under 1 MB zipped) at worst.
2. `70.schema/090.statistics` `statistics`: remove the cap. Hit in 6 databases (5 617 of 12 996 at worst), redundancy counts stay a floor; output only, +3.1 MB raw at worst, and it ends the 090/010 selection mismatch.
3. `70.schema/091.statistics-density`: remove the cap. The condition its own header sets for lifting it is met; about 0.27 ms and 327 B a statistic.
4. `80.workload/030.implicit-conversions`: candidates 200 to 1 000 with `bounds.candidates`, `@examined` 1 000 to 2 500. The cut is silent today; lab +1.7 s, client cost proportional to plan bytes (about 10 s for 500 MB).
5. `80.workload/053.plan-warnings` candidates: 200 to 500 with a matched count. The lab found two to three times more warnings uncapped; 9 s to 27 s there at 1 000.
6. `80.workload/020`, `023`, `024` listings: 50 to 200. Hit in 9 of 11 real stores; no server cost, about +100 KB a database each.
7. `60.backup/010.history` `recent`: 200 to 2 000 or the whole window. Hit in 6 of 12, 9 h of 30 days on the busiest instance; fix the `devices` grouping in the same change, which saves more than this costs.
8. `70.schema/040.compression` `largest_uncompressed`: 200 to 2 000 with a total. Hit in 9 of 27; +230 KB at worst.
9. `10.system/045.default-trace-detail`: rank per event class, or 5 000 to 30 000. Hit 3 of 3, two thirds of configuration changes lost once; opt-in, 11 MB raw at most.
10. `10.system/040.error-log`: `top_messages` 40 to 200 (hit 15 of 15, +100 KB), `notable` reported and newest first, `LEFT` 400 to 1 000.
11. `70.schema/030.index-operational` `heaps`: remove the cap. Hit in 4 databases, DMV already read in full; +120 KB.
12. `50.agent/010.jobs` window: 30 to 90 days. Limit in 3 of 12; no archive cost.
13. `80.workload/042.parallel-cost-distribution` `@examined`: 500 to 1 000. Its own fields price it afterwards.
14. `80.workload/041.plan-cache-plans`: 25 to 50 per ranking; about 5 to 9 MB.
15. `70.schema/050.heaps`: 50 to 100 under the existing page and time budgets, with an eligible count.
16. `80.workload/026`, `028`, `060`: 50 to 200. Already paid for by the aggregation; not yet seen in a real collection.
17. `70.schema/021.missing-index-queries`: 1 000 to 12 000; `045.columnstore`: 200 and 400 to 2 000 with a count.
18. `20.databases/025.fragmentation` output: remove the `TOP (25)`; already bounded at 100 measured.
19. `10.system/060.system-health` deadlock timestamps, `010.objects` constraint and type lists, `028.change-tracking` internal tables: remove; cheap and rarely reached.
20. `90.availability/042` `repl_errors`: keep 50, add the window's error count so that 50 cannot read as a total.
