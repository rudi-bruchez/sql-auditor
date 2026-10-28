# Partitioning — specification

A new per-database collector, `70.schema/046.partitioning.sql`, and one new
column in `70.schema/090.statistics.sql`. Written 28 October 2026.

## 1. The question

Found on a client data warehouse in October 2026, SQL Server 2019 Enterprise:
one database of 12.8 TB with 55 partitioned tables, 367 filegroups (one per
month from 2012 on) and 586 data files. Its oldest years sat on a slow archive
volume, and the file statistics (`10.system/030.file-io`) showed each of those
years read 22 to 92 times its own size in eleven days, while the current year
was read 5 times. A reporting workload that reads the periods it asks for does
the opposite.

Three explanations fit that pattern, and the archive could not tell them apart:

1. **Partition elimination fails.** The queries scan every partition because
   their predicate is not on the partitioning column, converts it, or wraps it
   in a function.
2. **The statistics are not incremental.** An automatic update of a statistic
   on a partitioned table samples the whole table, archive included, unless
   the statistic is incremental. The same instance logged 75 hours of
   `WAIT_ON_SYNC_STATISTICS_REFRESH` in eleven days, so updates were frequent.
3. **The queries need the whole history**, cumulative figures for instance, in
   which case there is nothing to eliminate.

The first is answered by the plans and the Query Store, which the corpus
already reads. The second and the facts any remedy depends on were missing:

- whether a statistic is incremental (`sys.stats.is_incremental`);
- the partition functions: key type, `RANGE LEFT` or `RIGHT`, boundaries and
  their grain;
- which partition lands on which filegroup, hence on which volume;
- whether a filegroup is read only, which decides whether the archive can be
  backed up once and checked with `DBCC CHECKFILEGROUP` piece by piece.

## 2. What the archive held, measured on that collection

| Fact | Where | Enough? |
| --- | --- | --- |
| partitions per index | `70.schema/020.index-usage` `size.partitions` | a count only |
| filegroups per index | `70.schema/040.compression` `filegroup_count` | a count only |
| file to filegroup to volume | `20.databases/020.properties` `files[]` | yes |
| reads per file | `10.system/030.file-io` | yes |
| partitioned table count | `70.schema/040.compression` `counts.partitioned_tables` | a count only |
| boundaries, grain, partition to filegroup | — | no |
| `is_read_only` of a filegroup | — | no |
| `is_incremental` of a statistic | — | no: left out of 090 on purpose |

`090.statistics` gave the reason for leaving `is_incremental` out: the column
arrived in SQL Server 2014 and the file's floor is 2012 SP1. The same file
already reads `persisted_sample_percent`, newer still, by testing
`COL_LENGTH` and staging the read through `sp_executesql`, so the reason no
longer holds.

## 3. The collector

`70.schema/046.partitioning.sql`, scope database, `@timeout: 120`,
`@permissions: CONNECT, VIEW ANY DEFINITION`, `@profiles: space`, no
`@min_version` (every view and column it reads predates 2012). It runs by
default: it reads the catalogue only.

Six result sets.

### root

`database`, `collected_at`; `counts.functions`, `counts.schemes`,
`counts.partitioned_tables`, `counts.indexes`, `counts.partitions` (of the
heaps and clustered indexes), `counts.filegroups`,
`counts.read_only_filegroups`, `counts.scheme_slots`, `counts.ranges`;
`listing_cap.*` for each list; `collected.*` and `errors.*` per area
(`functions`, `ranges`, `partitions`, `filegroups`); `error_message`.

### functions

One row per partition function: `function`, `key_type` as written in DDL
(`date`, `datetime2(0)`, `varchar(10)`, `decimal(9,2)`), `range` (`LEFT` or
`RIGHT`), `partitions`, `boundaries`, `boundary_values` (1 when the values are
projected), `first_boundary`, `last_boundary`, `step_unit` (`days` or
`value`), `step_min`, `step_max`, `step_mode` and `step_mode_pct` (the most
frequent step between consecutive boundaries and its share), `schemes`,
`tables`.

The grain is measured, not named. A daily function reads `step_min = step_max
= step_mode = 1`; a monthly one 28 to 31 with 31 as the mode. A temporal value
is converted to days since 1900-01-01 before the difference is taken, so a
six-hour step reads 0.25 whatever the type.

The conversion is whole days plus the fraction of the day in seconds, never
minutes. `DATEDIFF` returns an `int`, so minutes since 1900 overflow after the
year 5983, and a `9999-12-31` boundary, the usual end of time of a warehouse,
failed the whole functions area with error 535 on the lab in the first
version. Days reach about 2.9 million over the range of `datetime2`. CI plants
such a boundary.

### schemes

`scheme`, `function`, `filegroups` (distinct, over the function's partitions),
`read_only_filegroups`, `next_used` (the filegroup the next `SPLIT` puts its
new partition on: the destination numbered `fanout + 1`), `tables`, `indexes`.

### ranges

A scheme's partitions collapsed into runs of consecutive partitions on the same
filegroup: `scheme`, `filegroup`, `first_partition`, `last_partition`,
`partitions`, `from_boundary`, `to_boundary`, `is_read_only`. Partition *n*
lies between boundary *n − 1* and boundary *n*; which side is inclusive is the
function's `range`; NULL is unbounded. A daily function over monthly
filegroups is one row per month. This is the row that, joined to
`20.databases/020.properties` on the filegroup, says which dates live on which
volume.

The runs are found by gaps and islands on `sys.destination_data_spaces`:
within a scheme and a filegroup, consecutive destinations share
`destination_id − ROW_NUMBER()`.

### indexes

Every index of every table whose heap or clustered index is on a partition
scheme, the non-partitioned ones included: an index left on one filegroup
under a partitioned table makes `ALTER TABLE ... SWITCH` refuse. `table`,
`index_name`, `index_id`, `index_type`, `scheme`, `function`,
`partition_column`, `aligned` (on a scheme of the base's function, by the same
column), `partitions`, `empty_partitions`, `rows`, `reserved_mb`,
`largest_partition_rows`, `largest_partition_mb`, `filegroups`,
`read_only_partitions`. Largest first.

Rows come from `sys.partitions.rows`, documented as approximate: it is what the
engine keeps, and enough for "empty or not" and for a share. Pages come from
`sys.allocation_units.total_pages`; the partition's filegroup is its in-row
unit's `data_space_id`.

### filegroups

Every filegroup, partitioned or not: `filegroup`, `type`, `is_read_only`,
`is_default`, `files`, `size_mb`, `reserved_mb` (data held), `objects`,
`partitions` (of heaps and clustered indexes), `scheme_slots`.

## 4. Boundary values are data

A boundary is a value of the partitioning column. Dates and exact numbers say
how the data is cut and nothing about whom it describes. A string boundary can
be a customer name or a country code, and a binary or `uniqueidentifier` one
is a key nobody can read. So values, and the steps computed from them, are
projected for the temporal types (`date`, `datetime`, `datetime2`,
`smalldatetime`, `datetimeoffset`) and the exact numerics (`tinyint` to
`bigint`, `decimal`, `numeric`, `money`, `smallmoney`). For any other type
`boundary_values` is 0 and the value columns are NULL; the counts stay. `time`
is withheld with the others: a time-of-day partitioning is rare and a step in
days would mislead.

Each projected value is written as a reader writes it: a `date` without a
time, a `datetimeoffset` with its offset, an integer without decimals, a
`decimal` at its scale.

## 5. Caps

| List | Cap | Count beside it |
| --- | --- | --- |
| functions | 1 000 | `counts.functions` |
| schemes | 1 000 | `counts.schemes` |
| ranges | 5 000 | `counts.ranges` |
| indexes | 2 000 | `counts.indexes` |
| filegroups | 2 000 | `counts.filegroups` |

A cap bounds output, not server work: every list is cut from table variables
or catalogue views already read whole. The engine allows 15 000 partitions per
function, so a daily function over ten years is about 3 650 partitions and,
over monthly filegroups, 120 ranges. A test keeps each projected cap equal to
its `TOP` and requires the count.

## 6. Cost and locks

Catalogue reads only. `sys.partitions` and `sys.allocation_units` are read
once each, into table variables; nothing reads a data page, nothing needs
`VIEW SERVER STATE`.

**The join between the two views is two equality joins.** `container_id` is
`hobt_id` for in-row and row-overflow units and `partition_id` for LOB units.
The first version joined on a `CASE` choosing between the two, which leaves
the engine nested loops only. Measured on SQL Server 2025 CU5 (17.0.4045.5)
on a lab database holding a 15 000-partition table with three indexes (47 402
rows in `sys.partitions`):

| Form | Time |
| --- | --- |
| `CASE` in the join | 323 s, still under way when stopped |
| two equality joins in `UNION ALL` | 3.7 and 4.4 s through `sqlcmd`, 2.6 s through the collector |

The document was 7.7 KB. The client database of section 1 holds 18 673
storage units, under half the lab's worst case.

**Locks.** Catalogue views take short schema-stability locks on what they
describe, and `READ UNCOMMITTED` does not remove them. On a database whose
loads run DDL, a read can queue behind a schema-modification lock: the client
of section 1 had a view switch waiting minutes behind long reads. The file
sets `LOCK_TIMEOUT 10000` and puts each area in its own `TRY`, so a blocked
area is reported in `errors.*` and the others are still collected. Its reads
take no lock longer than one catalogue statement.

## 7. Permissions, measured

On the lab database, a login holding `CONNECT` and `VIEW ANY DEFINITION`
got the same document as `sa`. Revoking `VIEW ANY DEFINITION` left the
functions, schemes and filegroups visible and the tables and indexes gone,
silently: `counts.partitioned_tables` and `counts.indexes` read 0. That is why
the permission is declared: the preflight checks it and skips the collector
with the reason rather than letting a partial document pass as complete.

## 8. `is_incremental` in 090

`70.schema/090.statistics.sql` stages `sys.stats.is_incremental` into a table
variable through `sp_executesql` when `COL_LENGTH('sys.stats',
'is_incremental')` is not NULL, and both `statistics` and `statistics_all`
project it as `is_incremental`: 1, 0, or NULL where the build cannot say. The
staging read joins `#listed_tables`, so it covers the shared selection and
nothing more; `TestSchemaFilesShareOneTableSelection` counts six joins in 090
instead of five.

Measured on SQL Server 2025: a statistic created `WITH INCREMENTAL = ON` read 1
in both lists and the clustered index's statistic 0. Forcing the static branch
(the condition replaced by `1 = 0`) gave NULL for both and changed no other
value.

## 9. What it does not answer

- **Whether queries eliminate partitions.** That is in the plans: the
  `Partitions Accessed` of a seek or scan, in `80.workload/041` and the Query
  Store plans. This file supplies what a reader needs to judge them: the
  function, the column, the grain.
- **Rows per date.** Rows and pages are per index, with the largest partition
  and the empty count; the ranges carry no row count, because the tables of a
  scheme share its ranges and their rows do not add.
- **Per-partition incremental statistics**
  (`sys.dm_db_incremental_stats_properties`): one row per statistic and per
  partition, thousands per table on a daily function.

## 10. Tests

- `TestEmbeddedCorpusIsValid` lints the file (directives, statement guard,
  `OPTION (RECOMPILE, MAXDOP 1)` on every read) and `testdata/corpus.txt`
  gains its line.
- `TestSchemaCapsAreProjectedWithTheirTotals` gains five rows, one per list.
- `TestSchemaFilesShareOneTableSelection` counts the new join in 090.
- `TestTheGuideCountsTheTimeoutTiersOfTheCorpus`: the 120 s tier of
  `docs/dba-guide.md` goes from 30 to 31.
- CI plants in `ci_probe`, on both legs (2017 and 2022): a monthly `date`
  function `RANGE RIGHT` over three boundaries, its scheme putting the two
  oldest partitions on a filegroup then made read only and the rest on
  `PRIMARY`, a table with a primary key on it holding 89 rows over the first
  three partitions, an index on `PRIMARY` (unaligned), an incremental
  statistic, and a `varchar(10)` function with its table. The assertion
  checks every value the plant decides: steps 28 and 31, the withheld
  boundaries, the two ranges in order with the read-only one first, the
  unaligned index, the empty partition, the read-only partitions, the
  `NEXT USED` filegroup, and `is_incremental` in both lists of 090. The plant
  and the assertion were replayed on the lab before they were committed: both
  `jq` programs returned true, and `030.differential-base`'s assertion still
  held with the added file.

## 11. Harm review

`docs/harm-review-2026-10-28.md` covers this file, the column added to 090 and
`--skip-fragmentation`, released together as 0.40.0.
