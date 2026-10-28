# Harm review, 28 October 2026

Scope: what 0.40.0 adds to 0.39.0.

- `70.schema/046.partitioning.sql`, a new collector that runs by default
  (`docs/partitioning-spec.md`);
- `is_incremental` in `70.schema/090.statistics.sql`;
- `--skip-fragmentation` and the `@skipped_by_flag` directive.

The review asked the two questions of the review of 12 September 2026, in the
same order:

1. What can this change do to a production system, a database, or the
   machine it runs on?
2. What vulnerabilities does the code carry?

Every finding below is either fixed, with the test that fails if it comes
back, or recorded as left open, with the reason.

## The threat model

The one of 12 September: someone points a release at production after reading
`README.md` and `docs/dba-guide.md`. Added for this review: the instance is
the one the collector was written for. It is a data warehouse whose loads run
DDL all day: partition switches, view rewrites, `ALTER TABLE`. Its partitioned
tables hold thousands of partitions, and a schema-modification lock is often
pending on some object. A catalogue reader that is harmless on an OLTP
database is not obviously harmless there.

## 1. FIXED: the join to `sys.allocation_units` ran for minutes on many partitions

The first version of 046 joined `sys.partitions` to `sys.allocation_units` on
`au.container_id = CASE WHEN au.type = 2 THEN p.partition_id ELSE p.hobt_id
END`. A `CASE` in the join condition leaves the engine nested loops only.

Measured on SQL Server 2025 CU5 (17.0.4045.5), on a lab database holding a
15 000-partition table with three indexes (47 402 rows in `sys.partitions`):
323 seconds, and the batch was still running when it was stopped. That is 323
seconds of a collector holding schema-stability locks on catalogue rows, on
the kind of instance where a pending schema change queues behind it.

Fix: two equality joins in a `UNION ALL`, `hobt_id` for in-row and
row-overflow units and `partition_id` for LOB units, as `sys.allocation_units`
documents. Same database, same output: 3.7 and 4.4 seconds through `sqlcmd`,
and 2.6 seconds through the collector. The comment above the statement keeps
both figures.

## 2. FIXED: a `9999-12-31` boundary failed the whole functions area

The step between two temporal boundaries was computed as
`DATEDIFF(minute, '19000101', value) / 1440.0`. `DATEDIFF` returns an `int`,
so it overflows past the year 5983, and `9999-12-31` is the usual end of time
of a warehouse. Reproduced on the lab: `collected.functions` 0,
`errors.functions` 535. Nothing on the server was harmed, but the archive lost
every function and every range on exactly the instances this file was written
for.

Fix: whole days with `DATEDIFF(day, ...)`, plus the fraction of the day in
seconds, which reaches about 2.9 million over the range of `datetime2`. CI
plants a function with a `9999-12-31` boundary and asserts its last boundary
and its step, 2 912 442 days.

## 3. CHECKED: the locks a catalogue reader takes

`READ UNCOMMITTED` does not remove the schema-stability locks a catalogue view
takes on what it describes. On the instance of the threat model, a read of 046
can therefore hold up an `ALTER` that is waiting for the lock, and every
reader queued behind that `ALTER`.

What bounds it, each verified in the code rather than assumed:

- every statement of 046 is a single catalogue read under `LOCK_TIMEOUT 10000`,
  in its own `TRY`, so a statement that waits on the workload gives up after 10
  seconds and the other areas are still collected;
- the blocking watch (`collect/watch.go`) polls once a second for sessions
  waiting on the collection's own session and cancels the running unit once
  one has waited 5 seconds. Its header names this exact case, an `ALTER
  TABLE` queued behind a collector's Sch-S;
- the slowest statement now takes a few seconds at the engine's partition
  maximum, and the client database that motivated the file holds under half
  of that.

The workload can therefore wait on 046 for at most about 5 seconds. A
workload that holds the collector waits up to 10 seconds per statement, and
the collector waits, not the workload.

## 4. CHECKED: what leaves the server

046 projects object, index, scheme, function and filegroup names, which the
corpus already collects elsewhere, and partition boundaries, which it did not
collect before. A boundary is a value of the partitioning column, written into
DDL rather than read from a table.

- Values are projected for the temporal and exact numeric types only. A
  character, binary or `uniqueidentifier` boundary can be a customer name or
  a key, and is withheld: `boundary_values` is 0 and the values and steps are
  NULL. CI plants a `varchar(10)` function and asserts the withholding.
- An exact numeric boundary can still be a business number, a range of
  customer numbers for instance. It is projected on purpose: a number without
  a name identifies nobody, and the grain of a numeric partitioning is
  unreadable without it. The spec states the decision.
- No row is read from any user table, so the sentence of `MANIFEST.txt` saying
  so still holds.

## 5. CHECKED: the statements the corpus guard sees

`TestEmbeddedCorpusIsValid` runs the statement lint over 046 and over the new
`sp_executesql` literal of 090. Since the review of 12 September, the lint
descends into the literals of `EXEC` and `sp_executesql`. Both are
`SELECT`s into table variables, and every read carries `OPTION (RECOMPILE,
MAXDOP 1)`: the lint refused the first draft of 046 for one statement that
did not, and the draft was fixed before it was committed.

The new literal in 090 is compiled only when `COL_LENGTH('sys.stats',
'is_incremental')` is not NULL, so a 2012 instance never compiles a column it
lacks. The static branch was forced on the lab: NULL in both lists and no
other value changed.

## 6. CHECKED: `--skip-fragmentation` can only remove

The option can only switch one default collector off. It cannot run anything
the default run does not, and it cannot remove a collector that does not
declare it, because `@skipped_by_flag` accepts only the closed set
`SkipFlags`, and a misspelt name fails the lint. `--all` never sets it, which
`TestAllTurnsOnEveryOptIn` asserts. A collector left out is recorded in
`skipped_scripts` as "left out by --skip-fragmentation", the setting is in
`_run.json`, and a same-day rerun that loses the collector says so once. The
private analysis reads the reason as "not requested: left out", not as a
missing file.

## 7. OPEN: `70.schema/040.compression` exceeds its timeout on many partitions

Seen during this review rather than caused by it: on the lab database of
finding 1, `70.schema/040.compression.sql` ran out of its 120-second
`@timeout` and was cancelled. It already joins `sys.allocation_units` by
equality, so its cost is elsewhere, and it is not part of this change. The
harm is bounded by the timeout, 120 seconds of catalogue reading per such
database, under the same lock timeout and blocking watch. Its two join
conditions also swap `hobt_id` and `partition_id` with respect to the
documentation, which gives the same result while the two are equal. Recorded
in the project backlog for its own measurement.

## 8. Vulnerabilities

`govulncheck ./...` with the local toolchain (go1.27.0) reports two standard
library vulnerabilities reachable from the code, both through the TLS
connection to SQL Server: GO-2026-6607 (`crypto/tls`, malformed ECH outer
extension references) and GO-2026-6809 (`crypto/x509`, distrust of
compromised intermediate certificates). The release is not built with the
local toolchain, so what matters is the release's:

- the release workflow sets Go up from `go.mod`, which says `go 1.26`, and
  the 0.39.0 build log reads `go version go1.26.9`;
- on the 1.26 branch, GO-2026-6607 is fixed in 1.26.9 and GO-2026-6809 in
  1.26.10, according to the Go vulnerability database.

**0.39.0 shipped with GO-2026-6809.** `go.mod` now carries
`toolchain go1.26.10`, a floor that `setup-go` reads, so a release can no
longer be built below the fixed patch whatever the runner caches. The build
log of 0.40.0 has to show 1.26.10 or later. It is checked there, not assumed.

No new dependency is added. The Go code changes are the flag, its closed set
and the skip reason, with no new input from the network or the file system.
