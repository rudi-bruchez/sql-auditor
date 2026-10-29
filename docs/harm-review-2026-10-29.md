# Harm review — 0.41.0, 29 October 2026

What 0.41.0 changes on a client instance and in an archive, checked before the
release. The specification is `docs/partition-activity-spec.md`.

## 1. Locks on a database that runs DDL

The function and the catalogue views take short schema-stability locks.
Behind a schema-modification lock the file waits at most 10 seconds per area
(`LOCK_TIMEOUT 10000`), as `030` and `046` do. Measured behind an open
`ALTER TABLE ... ADD`: both areas returned 1222, the document was written,
and the filegroups listing came back empty instead of zeros.

The other direction matters more on the warehouse that motivated the file,
where `ALTER VIEW` built a lock convoy: a Sch-S held by this file while a DDL
waits makes every later request queue behind the DDL. The window is the
duration of two reads, 0.14 s per database on the lab and 3 s at most on
60 249 partitions, and the collector's blocking watch cancels a collector that
blocks another session for 5 seconds. Same exposure as `030`, which reads the
same function seven times per database where this file reads it once.

**Checked.**

## 2. Cost

No data page is read: the function returns counters held in memory, and
`sys.partitions` with `sys.allocation_units` is one equality join, not the
CASE join that cost 323 s in `046`'s first version. The result is two lists
capped at 2 000 rows. Measured: 1.1 to 3.0 s on 60 249 partition rows.

**Checked.**

## 3. Disclosure

Schema, table, index and filegroup names, and counts. Nothing that
`030.index-operational` and `046.partitioning` do not already project. No
boundary value, no data.

**Checked.**

## 4. Permissions

`VIEW SERVER STATE`, as `030`. Without it the collector is skipped by the
preflight like `030`, and the rest of the run is unchanged.

**Checked.**

## 5. Reading the result wrong

Two readings the file could invite and does not support, both written in its
header and in the spec:

- a low count proves nothing, the counters vanish with the metadata; a high one
  is trustworthy;
- every partition scanned is not a defect when the query needs the whole
  history.

The listing carries `partitions_reporting` beside `partitions` so the first is
visible in the data, not only in the documentation.

**Checked.**

## 6. `--skip-parallel-cost-distribution` can only remove

Built on the opt-out mechanism of 0.40.0, unchanged: an entry in `SkipFlags`,
a `@skipped_by_flag` directive on `042`, and the option. Off, the run is
identical to 0.40.0's. On, `042` is listed in `skipped_scripts` with the
option named, `_run.json` records the setting, and a rerun comparison names the
loss once. `--all` does not set it. The test that each `--skip-x` option sets
its own flag and nothing else now covers both options.

**Checked.**

## 7. External review, 29 October 2026

Two readers with disjoint scopes, on commit dac0354: codex (GPT-6) on
conformance and harm, a fresh Claude subagent on breaking the guards (the
opencode seat ran out of credits after reading the brief, agy out of quota).
Both ran against the SQL Server 2025 lab. Every finding below was checked
before it was acted on.

Accepted and fixed:

- An even spread of range scans was presented as proof that queries fail to
  eliminate. codex measured three queries, each eliminating to a different
  partition, giving 2 and 2 on 3 partitions of 3. The header, the spec and the
  changelog now say it is what a non-eliminating predicate leaves, not proof of
  one.
- `first_partition_written` and `last_partition_written` are the index's own
  partition numbers, so on an unaligned index they are not the table's months
  (the lab already showed `1` to `1` on the index on `PRIMARY`). Now said, with
  `046 indexes.aligned` as the condition for reading them against `046` ranges.
- The `indexes` listing was not emptied when the counters were refused, unlike
  `filegroups`, and its rows read idle. Now emptied, and a test holds both
  guards.
- Removing the allocation-unit type filter from 047 fails the partitions area
  with error 2627 on any partitioned table with a LOB column, and CI passed,
  its probe having none. Measured on the lab; the CI probe now carries a
  `varchar(max)` column.
- The cap test matched each `DECLARE` and never the `TOP` of its list, so a
  removed `TOP` or two swapped caps passed, in 046 as in 047. Each `TOP` is now
  matched on the first columns of its own list; a swap fails the test.
- Losing the `@skipped_by_flag` line from 042, or pointing it at the other
  flag, passed every test while the option still parsed and was recorded as
  on. A test now ties each skip flag to exactly the collector it leaves out.

Left open:

- The counters of 047 (`partitions_reporting`, `range_scans_min`,
  `leaf_writes`) are asserted by no test. CI cannot assert them, the read-only
  step may reload the metadata; the lab measurement in the spec is the
  evidence.
- `60.backup/010.history.sql` `history_by_month` is asserted only by its
  declaration: grouping by year passed every test. And its header says the
  shape tells a working purge from a stopped one, which does not hold when the
  retention is longer than the history msdb holds; its full read of
  `backupset` was not measured on a large msdb. Raised for the author of that
  commit.

Rejected:

- Three root-package tests failing in both readers' copies: the documented
  Windows CRLF failures of a checkout, present before any change and absent on
  the CI's LF checkout.

**Checked.**

## 8. Left open

`70.schema/040.compression.sql` used up its 120-second timeout on the lab
database of 60 249 partitions in the same run, as recorded in
`docs/harm-review-2026-10-28.md` item 7. Unchanged by this release; the timeout
is reported as an error and the run continues.

**Open, as before.**
