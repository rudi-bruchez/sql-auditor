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

## 6. Left open

`70.schema/040.compression.sql` used up its 120-second timeout on the lab
database of 60 249 partitions in the same run, as recorded in
`docs/harm-review-2026-10-28.md` item 7. Unchanged by this release; the timeout
is reported as an error and the run continues.

**Open, as before.**
