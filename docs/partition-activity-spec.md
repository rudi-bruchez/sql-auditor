# Partition activity — specification

A new per-database collector, `70.schema/047.partition-activity.sql`. Written
29 October 2026.

## 1. The question

The data warehouse that motivated `70.schema/046.partitioning`
(`docs/partitioning-spec.md`) kept monthly filegroups from 2012 to 2019 on a
slow archive volume. `10.system/030.file-io` said that volume was read 46 TB in
eleven days at 501 ms per read, every month of it: 186 of its 192 files had
been read more than 1 GB, and the files of 2018 and 2019 more than twenty times
their size. The application owner believed archive disks were detached every
six months; the archive showed every filegroup since 2012 online.

`046` says how the tables are cut and which months sit on which filegroup. What
a remedy needs next is the list of tables and indexes that do the reading, and
whether the old months are still written:

- an index that scans every partition is the query to rewrite, or the index to
  realign, before any archive can be detached: a filegroup taken offline under
  a query that does not eliminate makes that query fail;
- a filegroup that is never written can be made read only, backed up once and
  checked piece by piece; one that is still written cannot.

## 2. What the archive held

| Fact | Where | Enough? |
| --- | --- | --- |
| reads and stalls per file | `10.system/030.file-io` | per file, no object |
| file to filegroup | `20.databases/020.properties` | yes |
| partition to filegroup | `70.schema/046.partitioning` `ranges` | yes |
| scans, lookups, I/O latch waits per index | `70.schema/030.index-operational` | summed per index, only where locks waited |
| the same per partition, hence per filegroup | — | no |

`sys.dm_db_index_operational_stats` answers per partition; `030` aggregates it
away and keeps only rows that waited on locks.

## 3. The collector

`70.schema/047.partition-activity.sql`, scope database, `@timeout: 120`,
`@permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE`, the same as
`030`, `@profiles: space`, beside `046`. No `@min_version`. It runs by default:
it reads counters held in memory and the catalogue.

The function is read once into a table variable, one row per object, index and
partition (a columnstore's two or three rows summed). `sys.partitions` joined to
the in-row allocation unit (`container_id = hobt_id`, `type = 1`, one equality
join) gives each partition's filegroup and whether its table is partitioned.
Everything after joins the two table variables.

Three result sets.

### root

`database`, `collected_at`, `instance_start`, `seconds_since_instance_start`;
`counts.partition_rows` (rows of `sys.partitions` for user objects),
`counts.partition_rows_reporting` (those the function has a row for),
`counts.indexes` (indexes of partitioned tables), `counts.filegroups`;
`listing_cap.filegroups`, `listing_cap.indexes`; `collected.counters`,
`collected.partitions`, `errors.*`, `error_message`.

### filegroups

Every filegroup, most I/O latch wait first, capped at 2 000: `filegroup`,
`is_read_only`, `partitions`, `partitions_reporting`, `partitions_scanned`,
`partitions_written`, `range_scans`, `singleton_lookups`, `io_latch.waits`,
`io_latch.wait_ms`, `leaf_writes` (inserts, updates and deletes at the leaf).
Empty when either area was refused, since every sum would read zero.

### indexes

Every index of every partitioned table, the non-partitioned ones included, most
I/O latch wait first, capped at 2 000: `table`, `index_name`, `index_id`,
`partitions`, `partitions_reporting`, `partitions_scanned`, `range_scans`,
`range_scans_min` (over non-empty partitions that report), `range_scans_max`,
`singleton_lookups`, `io_latch.waits`, `io_latch.wait_ms`, `leaf_writes`,
`partitions_written`, `first_partition_written`, `last_partition_written`.

`partitions_scanned` equal to `partitions`, with `range_scans_min` close to
`range_scans_max`, is the shape of a predicate that does not eliminate. The
first and last partition written, read against `046` `ranges`, are the months
still modified.

No judgement is applied, for the reasons `046` gives.

## 4. Limits

- The counters start when a partition's metadata is cached and vanish when it
  is evicted, on restart, or when the database closes. `partitions_reporting`
  says how many have a row; a low count may be an eviction, a high one is
  trustworthy.
- `range_scan_count` counts scans started, not pages read. The pages read from
  disk are the I/O latch waits, which is the cost the archive volume imposes.
- Columnstore segments are not represented in the function (measured in `030`).
- No per-partition row: the sums answer the question at a fraction of the size.

## 5. Measured on SQL Server 2025 (17.0.4045.5), 29 October 2026

A database with a monthly `RANGE RIGHT` function over 24 months on four
filegroups, a table with an aligned clustered index, an aligned nonclustered
index and one on `PRIMARY`. The counters reset by taking the database offline
and online, then: 10 scans of one month by its key, 5 scans by `YEAR(d) = 2025`
forced on the clustered index, one update of the last two months.

| Index | partitions_scanned | range_scans | min | max | written |
| --- | --- | --- | --- | --- | --- |
| clustered | 24 of 24 | 130 | 5 | 15 | 23 to 24 |
| aligned nonclustered | 2 of 24 | 6 | 0 | 3 | 23 to 24 |
| on `PRIMARY` | 0 of 1 | 0 | 0 | 0 | 1 to 1 |

130 is 24 × 5 plus 10: the five non-eliminating scans started on every
partition, the ten others on one. The I/O latch waits fell on all four
filegroups, after `DBCC DROPCLEANBUFFERS`.

Behind an open `ALTER TABLE ... ADD` holding a schema-modification lock, both
areas returned 1222 after ten seconds, the document was written, and the
filegroups listing came back empty rather than zeros.

Cost: on 60 249 rows of `sys.partitions` (20 tables of 1 000 partitions with
three indexes each, plus the test table), the file ran in 1.1 to 3.0 s with
`sqlcmd`, three runs, and in 0.14 s per database through the collector on the
lab's other databases.
