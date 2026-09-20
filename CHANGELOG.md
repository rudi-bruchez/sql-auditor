# Changelog

Notable changes to sql-auditor. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html) with the caveat that
it is pre-1.0: the minor version moves for features and for behaviour changes
alike. The command surface is still settling, and 1.0 would promise a stability
this tool is not yet in a position to promise.

This file starts at 0.21.0, the first *published* release. Everything before it
is in the git history, which is the honest record of it; cutting that history
into per-release entries after the fact would mean inventing boundaries the
repository never had.

The version is stamped into every binary and recorded in the `MANIFEST.txt` of
every archive, so a collection can always name the build that produced it. The
release workflow refuses a tag that disagrees with either this file or
`cmd/sql-auditor/main.go`.

## [Unreleased]

### Added

- `10.system/078.performance-counters.sql` archives the content of
  `sys.dm_os_performance_counters`, one snapshot, except the deprecated
  features 075 already reads: `Access Methods`, `Locks`, `Latches`,
  `SQL Errors`, `Plan Cache`, `Query Store`, `Columnstore`, resource pools
  and the per-database counters had reached no archive. Each row carries the
  type the engine declares, which does not say how to read it (some plain
  values are counts since start), so nothing is computed. Root and array
  come from one read of the view. Design in
  `docs/performance-counters-spec.md`.
- `80.workload/026.query-store-interrupted.sql` lists, per database, the
  queries whose executions did not finish: stopped by the client (`Aborted`,
  where query timeouts land) or by an error (`Exception`), with the
  duration and CPU of those executions beside the query's finished ones. A
  timeout leaves no error on the server and `sys.dm_exec_query_stats` does not
  count it. Under the
  default capture mode `AUTO`, a rarely run query that times out while blocked
  is never captured, so the listing is a lower bound there; the root gives the
  capture mode, and a blocked timeout past 30 seconds may also be in
  `system_health`'s `wait_info` events, which `10.system/060` counts. Design
  in `docs/query-store-interrupted-spec.md`.

### Fixed

- `80.workload/025.query-store-compare.sql` labelled a dropped object and an
  ad hoc batch both as `null`; it now uses 023's labels, `(ad hoc)` and
  `(dropped object, object_id N)`.

## [0.28.0] - 2026-09-19

A collection could hold up the server it was auditing: `READ UNCOMMITTED` keeps
a collector from waiting on the workload, not the workload from waiting on the
collector. A blocking watch now cancels a collector someone has waited on for
5 seconds. The release also adds a way to compare the Query Store on either
side of a change, and two collectors.

### Added

- `--query-store-compare-at` and `80.workload/025.query-store-compare.sql`
  compare the Query Store on either side of a change (a migration, a release,
  a compatibility level, an index dropped). The value names the minute or the
  day the change happened, in the server's local time, and every interval
  overlapping it is left out of both sides, since its averages mix the two
  behaviours. The sides are computed per database, of equal length in whole
  intervals, up to seven days. Statements (the same text in the same
  module) are selected by the change in their per-execution CPU and duration
  and in their total CPU, so a statement split into two `query_id`s by a
  change of SET options still ranks as one; each `query_id` keeps its own row.
  Forced plans are added outside the cap, and nothing is labelled a
  regression. The before side is never longer than the history the store
  still holds. Command line only, and `--all` does not
  turn it on. Design in `docs/query-store-compare-spec.md`.
- A blocking watch. `collect` opens a second connection that reads
  `sys.dm_os_waiting_tasks` once a second while each collector runs, and
  cancels a collector that another session has waited on for 5 seconds; the
  remaining collectors on that database are skipped. `READ UNCOMMITTED` keeps
  the collection from waiting on the workload but not the workload from
  waiting on the collection: a collector's Sch-S held a schema change, and
  every reader behind it, for as long as the collector ran. The manifest
  records the watch in a `blocking_watch` block and a `Block watch` line,
  including when it was off and why. Design in `docs/blocking-watch-spec.md`.
- `80.workload/011.batch-response-times.sql` collects the 'Batch Resp
  Statistics' counters as a histogram of batch durations since the last
  restart, one row per bucket with elapsed and CPU counts and totals, and in
  the root how many batches took a second or more, and ten seconds or more.
  The elapsed and CPU histograms are independent: a batch is filed once by
  each, so the two columns of one row do not describe the same batches.
- `20.databases/027.resumable-operations.sql` lists the resumable index
  operations each database holds, running or paused, from SQL Server 2017 on:
  table, index, state, progress, the pages already written, and how long a
  paused one has been waiting. A paused operation blocks other index DDL on its
  table (Msg 10637), and a paused `CREATE INDEX` has no row in `sys.indexes`,
  so no other collector could see it. `index_exists` tells a rebuild from a
  creation. The statement text is not collected. Without `VIEW ANY
  DEFINITION` the view comes back empty rather than failing, which reads like a
  database with nothing paused.

### Changed

- The collection session goes back to the default database as soon as a
  collector's rows are read, instead of when the next collector starts. A
  session left in a user database holds a lock on it that an `ALTER DATABASE`
  waits on, and after the last collector it used to stay there through the
  writing of the manifest and the archive.

## [0.27.0] - 2026-09-19

One collector held a cheap question and an expensive one in the same batch, and
a timeout on the second cost the answer to the first. Fragmentation now has a
file of its own, so a large database that runs out of time loses fragmentation
and nothing else.

### Changed

- The fragmentation read has left `20.databases/020.properties.sql` for a
  collector of its own, `20.databases/025.fragmentation.sql`. It was the only
  expensive area of that file, and a timeout cancels the whole batch: on a
  2.4 TB database the files, their autogrowth settings and the largest objects
  were lost with it, although they are catalog reads that answer in
  milliseconds. The per-partition budget of 0.24.0 made that rarer but could
  not rule it out, since it bounds the number of calls and not the length of
  one. `025.fragmentation.json` carries the same `fragmentation` array and the
  same `fragmentation_sample`, `collected.fragmentation` and
  `errors.fragmentation` fields that `020.properties.json` did, and is in the
  `space` profile like its parent. `020.properties.json` no longer carries
  them, so a reader has to look in the new file, and fall back to the old one
  for an archive collected before this change.

## [0.26.0] - 2026-09-18

Three collectors said less than they appeared to. Two of them failed in the
field on real instances during the week, and the third was answering a question
with a guess. The shape they share is worth naming: none of the three reported
an error, and each left a reader with a document that looked complete. A batch
cancelled on timeout returns nothing at all, a version gate set one branch too
low aborts on a column that is not there, and a hand-written list of wait types
returns rows for every name in it and stays silent about the one that mattered.

### Changed

- `10.system/010.properties.sql` matches lock waits as a family, `LCK[_]M[_]%`,
  rather than by naming two of them. Which lock mode dominates is a property of
  the workload, so a list of names encodes a guess about the instance instead
  of measuring it. Found in the field: the block named `LCK_M_S` and `LCK_M_X`
  while `LCK_M_U` had accumulated twenty times the wait time of `LCK_M_S` and
  was the fourth wait of the instance, absent from this summary entirely, so a
  reader who stopped here concluded the instance had no locking problem. The
  family predicate matches 72 wait types in the catalogue and the existing
  `waiting_tasks_count` filter left five of them on a test instance, one being
  `LCK_M_SCH_S`, its largest single wait, which the old list did not name
  either. The rows stay bounded because modes that never waited are dropped,
  not because the list is short. `collect/blocking.go` already matched the
  family this way.

### Fixed

- `80.workload/060.spills.sql` was gated at 13.0.5026 and aborted the batch on
  SQL Server 2017 RTM with `Invalid column name 'total_spills'`. `total_spills`
  and `last_spills` arrived in 2016 SP2 and in 2017 CU3, which is two floors on
  two branches rather than a range, and every 2017 build below 14.0.3015 clears
  the 2016 one. The gate now carries the later floor, as
  `10.system/013.memory-model.sql` already did for the same shape of problem:
  2016 SP2 and SP3 instances no longer run this collector although they have
  the columns, which is the safe direction to be wrong in.

- `70.schema/055.page-density.sql` read its 50 partitions in one statement, so
  running out of its 1800 seconds lost the whole document, summary included.
  Measured on a collection taken in the field under the space profile: the two
  largest databases of one instance timed out and neither left any trace of
  what had been measured, on the one collector that says whether a rebuild
  would give space back. It now measures one partition per call, largest first,
  and starts no new call after 1500 seconds. The root gains
  `sample.measured_partitions` and `sample.budget_sec`, so a list cut short is
  no longer read as a database whose pages are all full.

## [0.25.0] - 2026-09-17

Six things an archive could not say, and did not say it could not say. An
object's own room can now be sized, because the file rows carry their filegroup
and the object rows carry theirs: a space simulation that pooled free space
across filegroups which do not lend to each other was answering "enough room"
for operations the engine then refused. A heap says how many partitions it has,
which decides whether a rebuild may be written without a `PARTITION` clause,
where omitting it on a heap of forty rebuilds all forty in silence. The restore
history joins the `space` profile and gains a per-database row, so an index
called unread has a period to be unread over. The compression estimate publishes
the exact number of objects its bounds excluded rather than the hundred it
lists. A new collector names the statements that spilled to `tempdb` and whether
the grant or the estimate was to blame. And the index usage collector counts the
missing-index suggestions of the whole instance, because the engine's limit is
per instance and a database crowded out by its neighbours was indistinguishable
from a database with nothing to suggest.

Every one of them was executed against a real SQL Server before it was
committed, on databases built for the case in question; the measurements are in
the collector headers.

### Added

- Every data file row of `20.databases/020.properties` now carries the
  filegroup it backs, and the object rows of `70.schema/040.compression`,
  `70.schema/041.compression-savings` and `70.schema/050.heaps` carry the
  filegroup the object sits on. Neither half was worth much alone: SQL Server
  allocates per filegroup, so an object in a filegroup capped by `MAXSIZE`
  borrows nothing from a neighbour that can still grow, and a space simulation
  that pooled every data file answered "enough room" for operations the engine
  then refused at run time. A log file's filegroup is NULL, which is the fact
  and not a gap. Where a row aggregates several partitions, `filegroup` is the
  name only when there is exactly one and `filegroup_count` says so; the rows
  that are one partition carry the name outright. This closes gap 21 of
  `docs/collection-gaps-spec.md`.
- `70.schema/050.heaps` now carries `partition_count` per heap. The list is the
  fifty largest heaps and, within those, only the partitions that passed its
  page-count filter, so a single row used to mean either a heap with one
  partition or one whose siblings did not make the cut, and nothing told them
  apart. It decides whether a rebuild may be written without a `PARTITION`
  clause, where the two mistakes are not symmetrical: naming a partition on a
  heap that has one fails loudly and changes nothing, omitting it on a heap
  that has forty rebuilds all forty in silence. This closes gap 19.
- `60.backup/020.restore-history` gains a `per_database` result set, one row per
  destination database with the last and first restore recorded and how many
  there are, and joins the `space` profile. A restore resets
  `sys.dm_db_index_usage_stats` exactly as a restart does, so calling an index
  unread means naming the period it was not read over, and a `space` archive
  carried nothing to date that period. The collector existed but belonged to no
  profile, and its only listing is the 200 most recent restores of the whole
  instance, which on an instance that restores nightly drops the last restore of
  a quiet database. The new result set is bounded by the number of databases
  instead. A missing row still proves nothing, since `msdb` history is prunable.
  This closes gap 20, and takes the space profile from 21 collectors to 22.
- The root of `70.schema/041.compression-savings` now carries
  `not_estimated_objects`, the exact number of uncompressed objects its bounds
  excluded. The file publishes that exclusion as a list so that "we estimated
  the savings" cannot quietly mean "we estimated some", but the list itself
  stopped at a hundred rows and said nothing about stopping. A consumer that
  derived the eligible population from the array understated it on any database
  with more than about a hundred and twenty large uncompressed objects, and so
  reported better coverage than it had. The list stays capped, which is right;
  the number it stands for is now exact. This closes gap 22.
- `80.workload/060.spills.sql`, a new collector gated at build 13.0.5026, names
  the statements that spilled to `tempdb` with the pages they spilled and the
  memory they were granted, used and ideally wanted. An audit that names a slow
  procedure is expected to say where its time went, and a hash aggregate
  spilling twice for ten seconds each was invisible to every archive: the
  diagnosis needed a post-execution plan the client had to be asked for. It
  reads no statement text, taking the database and the object from
  `sys.dm_exec_plan_attributes` and resolving the object name only for a
  compiled module. Below the floor the columns do not exist and there is still
  no path to a spill except a plan, which is a fact about the build rather than
  a gap in this corpus. This closes gap 18 above that floor, and takes the
  corpus from 84 collectors to 85.
- The root of `70.schema/020.index-usage` now carries
  `missing_suggestions_instance` beside the database's own count. The engine
  gathers missing-index suggestions for at most 600 groups across the whole
  instance and then stops, so a database whose suggestions were crowded out by a
  busy neighbour came back with an empty list, a count of zero, every collected
  flag at 1 and no error: indistinguishable from a database with nothing to
  suggest. Measured on SQL Server 2025, two databases driven with three hundred
  query shapes each recorded 138 and zero while the instance stood at exactly
  600. The collector reports the number and applies no threshold, because the
  documented limit belongs to the builds it has been checked against and a
  constant in the corpus would need a corpus change the day it moves.

## [0.24.0] - 2026-09-17

Nine corrections, of which three change what the tool does rather than what it
says, and those three are why the minor version moves. A stopped or failed
rerun no longer deletes the complete archive it was replacing. A collection
stopped with `ctrl-c` exits `2` instead of `0`, so a scheduler that recorded a
success now records a partial run. And `check --grant-script` refuses to
overwrite an existing file without `--force`, as `env init` and
`queries export` already did.

The rest close ways the tool could mislead in silence: a `.env` that could be
left empty by a failed save, opt-ins missing from the run's own record of
itself, and three messages that described behaviour the code did not have.

### Fixed

- A same-day rerun that was stopped, or that ended with a collector failing,
  deleted the complete archive of the run it replaced and left a partial one in
  its place. The earlier run is now deleted only after a run that exits `0`;
  otherwise it stays beside the new one, and the collection says where.
- A collection stopped with `ctrl-c` or `SIGTERM` on the command line exited
  `0`, so a scheduler or a CI job that stopped it recorded a success. It exits
  `2`, the code for a partial run, and its summary line says `cancelled`.
- On `ctrl-c`, the command line printed `stopping: finishing what is in flight`
  and the README said the same, but the collector running at that moment is
  abandoned and its output lost. The message and the README now say so, and
  the README says what a second `ctrl-c` leaves behind: the files written so
  far, no manifest, no archive, and a `.lock` file to delete.
- A collection stopped with `ctrl-c` while it was connecting, before its first
  collector, exited `1`, the code for an instance that could not be reached,
  and its manifest carried `context canceled` as an error with no `cancelled`
  flag. It now exits `2`, the manifest says `cancelled` with no error, and the
  command line says nothing was collected.
- `check --grant-script FILE` replaced an existing `FILE` without a word, a
  reviewed script or a mistyped path alike. It now refuses, before connecting,
  unless `--force` is given, as `env init` and `queries export` already did.
- The `config` block of `_run.json` did not record `estimate_compression` or
  `blocked_process_reports`, so an archive taken with either option did not say
  so. It now records every opt-in, on or off.
- Saving the server and the login from the wizard rewrote `.env` in place, and
  a write that failed after the truncation (a full disk, a quota) left it empty,
  password and hand-written settings included. The original is now copied to
  `.env.sql-auditor-backup` first and stays there if the rewrite fails; the
  error names it. A backup left by an earlier failure is never replaced.
- The README described `--measure-page-density` and `--estimate-compression`
  as if they ran once for the instance. Both run in every collected database,
  each with an 1800-second timeout, and nothing bounds the run as a whole; the
  table now says so, as `docs/dba-guide.md` already did.
- `20.databases/020.properties.sql` ran out of its 300 seconds on databases of
  a few hundred GB and up, and a timeout returns none of its seven result sets:
  the files, the space and the creation date were lost with the fragmentation
  read that caused it. That read now measures the 100 largest partitions one at
  a time and starts no new one after 150 seconds. The root object gains
  `fragmentation_sample.eligible_partitions`, `.measured_partitions` and
  `.budget_sec`, so a list cut short is not read as complete. The
  `fragmentation` array keeps its shape and its 25 rows, now the most
  fragmented of the partitions measured rather than of every partition.

## [0.23.0] - 2026-09-15

A question about disk space can now be asked on its own: `--profile space` runs
the collectors that answer it and nothing else. The wizard also asks for the
connection when a double-clicked binary finds no `.env`, and the statement lint
applied to a `--queries-dir` corpus refuses five more ways to change the server.

### Added

- `--profile space` on `check`, `collect` and the wizard: 21 of the 84
  collectors answer what makes the databases of an instance larger than they
  need to be, and the profile narrows a run to them. The archive names the
  profile, the run folder carries it, and rights the profile does not need
  are reported `not needed`.
- `70.schema/055.page-density.sql`, behind `--measure-page-density`: page
  fullness of the 50 largest rowstore index partitions, indexed views included.
  84 collectors.
- `--profile` is refused, with exit code 2 and before anything is collected,
  wherever it would not narrow what it was asked to narrow: beside `--all`,
  with an empty name, with an option that has no collector in the profile, on
  a `--queries-dir` corpus that declares no profile, and on any command other
  than `check` and `collect`.
- The first screen of the wizard asks for the server and the login when no
  `.env` provides them, and can save both to `.env`. The password is never
  saved. A binary started without arguments, by a double-click for instance,
  keeps its console open when it stops before the wizard starts, so the
  message can be read.

### Changed

- `70.schema/010.objects.sql` and `70.schema/060.columns.sql` list the union of
  the 200 tables with the most rows and the 50 with the most reserved pages, so
  a large LOB table with few rows is no longer left out.
- The replication widening brings the distribution database into a narrowed run
  only when a collector that will run reads it. On an instance where the
  replication collectors are gated off, it is no longer listed as covered.
- `--all` turns on ten options.
- An empty `SQL_SERVER`, or a login with no password, is refused by `check` and
  `collect` once the configuration is resolved, with the same message and exit
  code 2 as before; the wizard refuses them when its first screen is submitted.
- `ci.yml` pins its actions to full commit SHAs, as `release.yml` already did.

### Fixed

- `docs/dba-guide.md` stated that the heap scan reads about 1 % of the pages.
  Measured, it brings 8 to 12 % of a large heap into the buffer pool and all of
  a small one.
- The README says to unpack the Windows archive with `tar`, not through
  Explorer, whose extractor copies the zip's download mark onto the binary and
  gets it stopped by SmartScreen.

### Security

- The statement lint applied to a `--queries-dir` corpus refuses
  `sp_msforeachdb`, `sp_msforeachtable` and `sp_MSforeach_worker`, which run a
  string against every database; `DBCC SQLPERF` with `CLEAR`, which resets the
  wait statistics this tool exists to collect; and `sp_updatestats`,
  `sp_recompile`, `sp_cycle_errorlog`, `sp_trace_setstatus` and `CHECKPOINT`.
  The embedded corpus was not affected. The guard stops an accident, not an
  author.

## [0.22.0] - 2026-09-06

The corpus goes from 62 collectors to 83, and every gap
[docs/collection-gaps-spec.md](docs/collection-gaps-spec.md) records is closed
except three it deliberately leaves open. The bar for entry there is that an
audit needed the answer, could not find it in an archive, and had to go back to
the client for it.

Two new opt-ins and one new permission come with that, and all three are off or
unasked by default.

### Added

- **New keys in existing collectors, non-breaking.** Archives produced by older
  builds simply lack them.
  `10.system/010.properties.sql` gains `memory.total_page_file_mb` and
  `memory.available_page_file_mb` (OS page file from `sys.dm_os_sys_memory`) and
  the scheduler gauges `schedulers.visible_count`, `schedulers.runnable_tasks`,
  `schedulers.runnable_tasks_max` and `schedulers.work_queue` (instantaneous,
  `VISIBLE ONLINE` schedulers only, so the average per scheduler and the local
  peak are both computable). `20.databases/010.all-databases.sql` gains
  `parameterization_forced`. `20.databases/020.properties.sql` gains `is_sparse`
  per file, which the size columns cannot reveal.
  `20.databases/010.all-databases.sql` also gains `last_good_checkdb` from
  `DATABASEPROPERTYEX(db, 'LastGoodCheckDbTime')`, one line per database in the
  instance list because the per-database collectors never run against master,
  model or msdb — the databases whose integrity history matters most. It reads
  `1900-01-01` for a database that never had a successful CHECKDB and NULL on
  builds older than 2016 SP2, where the property is unknown.
  `70.schema/020.index-usage.sql` gains `hypothetical` on the usage result set,
  because a hypothetical index shows the same zero counters as a dead one and
  the two are otherwise indistinguishable in a baseline.
- **Two more instance- and schema-scoped probes for assessment rules the
  archive could not answer** (`10.system/095.master-user-objects.sql` and
  `70.schema/075.foreign-keys.sql`). User objects in master are read from the
  instance scope, under the three-part name `master.sys.objects`, because the
  database-scoped collectors never run against master and nothing else in the
  archive could see a table parked there; the newest 200 are listed with the
  true total beside the cap, and an empty list is the healthy answer, not a
  failed read. Foreign keys are projected one row per (key, column) with the
  referenced table and column, so the archive can join them offline to the
  index key columns of `070.index-columns.sql` — whether a foreign key is
  supported by an index is a judgement this file deliberately does not make.
- **Four instance probes for assessment rules the archive could not answer**
  (`10.system/075.deprecated-features.sql`, `10.system/076.pending-io.sql`,
  `10.system/077.fulltext.sql` and `80.workload/052.optimizer-hints.sql`).
  Deprecated features keep only the counters above zero — the filter the
  Microsoft probe itself applies, and the difference between a signal and the
  ~250 zero counters an old instance carries — with RTRIM on the padded nchar
  feature names, and every counter bounded by the last restart, which the root
  object states beside the numbers. Pending I/O is attributed per database and
  file through the `io_handle` = `file_handle` join on
  `sys.dm_io_virtual_file_stats`, because the pending-requests DMV names
  neither database nor file; an empty array is the expected reading of a quiet
  instant, not a failure. Optimizer hints are reported next to the total
  optimization count, because a bare hint count confuses "few queries" with
  "few hints". The Full-Text service answers installed or not, with its two
  security properties NULL when it is not — a success by design, not a
  collection failure.
- **An exclusive version ceiling, `@max_version`**, the mirror of
  `@min_version`: a script declaring it does not run on the named version nor
  above. It exists because the corpus knew floors only, and one window can
  only be covered without overlap once a ceiling exists: the floor of
  `10.system/020.host-services.sql` comes from a single services-view column
  (`instant_file_initialization_enabled`, 2016 SP1), not from the view that
  carries the startup parameters.
- **The persisted startup parameters for 2012 to 2016 RTM**
  (`10.system/025.startup-parameters-2012.sql`). It projects exactly the
  `startup_parameters` result set of `020.host-services.sql` — the trace flags
  that survive a restart — and mutes itself from 2016 SP1 up, where 020 runs
  instead. The window it serves is Windows by construction: SQL Server on
  Linux starts with 2017, so `sys.dm_server_registry` is reliable on every
  version the ceiling lets it reach.
- **The host and its operating system** (`10.system/021.host-info.sql`). The
  archive said nothing about the machine, so three questions an audit is
  routinely asked — is the host still supported, does a known fix apply, does
  the memory configuration make sense against the hardware — had no answer in
  it. One file reads whichever of the two views this build has, and reports the
  raw release number rather than mapping it to a product name it cannot know.
- **Transport, authentication and encryption in transit**
  (`10.system/042.connection-security.sql`), aggregated and never per session.
  It carries what the sessions do and, separately, what the server demands:
  forced and encrypted is a configuration, unforced and encrypted is a
  coincidence, and only the pair is a finding.
- **What else is running on this machine** (`10.system/043.cpu-neighbours.sql`
  and `046.local-sessions.sql`): the CPU and memory a neighbour is taking, and
  the sessions that originate on the server itself. Nothing inside SQL Server
  can enumerate a host's processes, so this is how much it is not getting and
  who connects locally — not a process list.
- **The default trace, in aggregate** (`10.system/044.default-trace.sql`). It is
  the only free record of what was done to an instance, with timestamps, and
  nothing read it. Autogrow events carry their duration, because "the log grew
  180 times" is a curiosity and "the slowest took 41 seconds" is the report.
- **Two more ring buffers** decoded (`10.system/047.resource-pressure.sql` and
  `048.security-errors.sql`), plus the exception buffer folded into
  `041.connectivity.sql` as a fourth result set. Resource pressure is the only
  history of memory pressure that covers the whole supported range; the security
  buffer says whether a failed login was a wrong password or a broken SPN.
- **Enterprise-era features persisted in a database**
  (`20.databases/026.persisted-sku-features.sql`), with the edition boundary
  left to the analysis step — since SQL Server 2016 SP1 the presence of these
  features is a licensing conversation and not a defect.
- **Column distribution** (`70.schema/091.statistics-density.sql`), so an index
  key order can be argued from the archive. It estimates the leading column's
  density from the histogram and says so in the column names: the density vector
  itself needs one `DBCC SHOW_STATISTICS` call per statistic, built from
  variables, which the corpus's read-only statement lint refuses by design.
- **`schema_option` on replication articles**, raw and with six bits decoded.
  Without it nothing separated an index that came from replication from one made
  by hand — and with nonclustered index copying off, a reinitialisation drops
  every index on the subscriber. That answer had to be got by mail.

- **Database principals, and who is told when the instance breaks**
  (`40.security/020.database-principals.sql` and `50.agent/030.alerts.sql`).
  The security section of a report could only speak about server-level sysadmin
  membership, and nothing said whether an instance raises an alert on a
  severity 19 to 25 error or an I/O error — which is not the same finding as
  raising one nobody is notified of. The alerts collector needs a permission
  neither `MSDB READ` nor `SQLAgentReaderRole` grants, so **`AGENT ALERTS` is a
  new capability**: it is probed, it appears in `check`, the grant script writes
  it, and `docs/dba-guide.md` lists it. No operator address is collected, only
  whether one is configured.
- **What the maintenance plans actually do** (`50.agent/040.maintenance-plans.sql`),
  task by task. Until now the archive could say a maintenance plan exists —
  the Agent job step says only "Subplan_1" — and nothing about what it does.
  Each plan stores its tasks as SSIS packages in `msdb.dbo.sysssispackages`,
  and the collector reads the task name and the immutable task type
  (`DbMaintenanceShrinkTask` and so on) out of the package XML, never the
  package body, which can carry connection strings. An encrypted or unreadable
  plan still appears, as a row with a null task. No fixed role reads that
  table — the `db_ssis*` roles are deliberately not offered, `db_ssisoperator`
  being execution rights on every package and `db_ssisadmin` a documented
  escalation path — so **`MAINTENANCE PLANS` is a new capability**: probed,
  shown in `check`, and granted as `SELECT` on that one table. Verified
  against a SQL Server 2022 instance with a login holding nothing else.
- **Execution plans when the Query Store is off** (`--plan-cache-plans`). Until
  now an instance without the Query Store contributed no plan at all, and the
  analysis had aggregate counters with no way to see a plan shape. This keeps up
  to a hundred plans from the cache as `.sqlplan` files with an index, chosen by
  four definitions of mattering. Off by default: a plan carries compiled
  parameter values and literal predicates, and it discloses that under its own
  entry in `MANIFEST.txt` rather than borrowing the Query Store's.
- **The retained rows of the default trace** (`--include-default-trace`),
  alongside the aggregate that now always runs. Off by default, and disclosed
  under the same wording the error log collector uses, because that is what the
  rows carry.

- **`MANIFEST.txt` now records how the connection was secured**, as a
  `Connection` line beside the authentication and a `transport` block in the
  JSON. Both halves are kept, because neither answers the question alone:
  encryption without validation stops an eavesdropper and does not stop a
  machine-in-the-middle, which terminates the TLS itself and presents whatever
  certificate it likes. The terminal note that said so scrolled away; the
  question "was this archive gathered over a channel whose far end was
  verified?" is asked months later by someone holding the archive and not the
  `.env` it was run from.

### Changed

- **`20.databases/010.all-databases.sql` (and its 011/012 companions) no longer
  exclude the system databases.** `WHERE d.database_id > 4` is gone, so
  `$.databases[*]` gains four rows per archive — master, tempdb, model, msdb.
  This is a breaking change for anything diffing archives across the boundary.
  The reason is model: `auto_shrink` on it is copied to every database created
  afterwards, and a collector that cannot see model blinds the audit rules
  that read it. tempdb has no backups at all and master never has a log
  backup, so their backup columns are NULL and the analysis layer must
  exclude them by name before judging staleness.

- **The corpus inventory is `testdata/corpus.txt`, not a number in a test.**
  `TestEmbeddedCorpusIsValid` hardcoded how many collectors there are and
  aborted on a mismatch, so adding one failed twice: once on the count, and
  again on the lint the count had prevented from running. The inventory is now
  a golden file regenerated with `go test . -run TestEmbeddedCorpusIsValid
  -update`, or with `tools/refresh-corpus.ps1` alongside the other checks, and
  the mismatch reports with `Errorf` so the lint runs in the same pass. A list
  names the file that arrived or vanished and catches a rename, neither of which
  a total can do. CI never regenerates it: the diff is the guard.
- **Two other hardcoded sizes are derived instead.** `--all` is checked against
  `collect.KnownFlags` rather than a count, which is the stronger test — the two
  sets are decided in different places — and names the flag that drifted. The
  verification screen's granted-over-total is taken from its own fixture, where
  the comment above it already claimed the total was never written down.
- **`20.databases/023.log-vlf.sql` no longer carries a version floor.** The
  condition was never which build this is but whether `sys.dm_db_log_info`
  exists, so the file asks that directly and falls back to `DBCC LOGINFO`,
  naming the mechanism that answered and recording a refusal rather than
  pretending the question was never asked. The old 13.0.5026 gate denied a VLF
  count to every instance below SQL Server 2016 SP2 — which is exactly the
  population whose logs have been growing by percentage increments for years.

### Fixed

- **`50.agent/020.job-steps.sql` now declares the `AGENT JOBS` permission it
  already used.** The collector joins `sysjobs_view` for job names and
  `sysproxies` for proxy names, but only `AGENT JOB STEPS` was declared — so
  a login granted SELECT on `sysjobsteps` without SQLAgentReaderRole failed
  at run time instead of being reported up front. The manifest now conditions
  the collector on both capabilities and the grant script lists it under
  both sections.
- **Two collections of the same instance on the same day ran into each other.**
  Nothing prevented it: both renamed the same predecessor aside, both wrote into
  the same folder, and both exited 0 printing the same archive path, so the
  operator was handed one archive that was two runs interleaved with a manifest
  describing whichever finished last. A run now claims its name with an `O_EXCL`
  lock file beside the folder, the way the grant script and `env init` already
  claim theirs. A stale lock is deliberately not cleaned up — a process killed
  mid-run leaves evidence worth looking at, and guessing by age or by PID would
  delete it — so the refusal names the file and says what to do.
- **`ctrl-c` on the command line wrote no manifest and no archive.** The README
  promise held only in the wizard; the subcommand ran on `context.Background()`
  with no handler, on exactly the path a scheduled task, a remote shell and a
  runbook use. A second `ctrl-c` still abandons the run, so a collection that
  will not wind down can be stopped.
- **Control characters from the server reached the terminal.** A database name,
  a server name and a login are chosen on the far side of the connection, and
  an ESC in one of them can repaint the screen — including the archive path the
  wizard asks the operator to copy. `SafeForTerminal` is the display
  counterpart of the `SafeFolderName` that already existed for the filesystem.
- **The `--queries-dir` statement lint could be walked past two ways.**
  *Concatenation:* only the literal that opens a dynamic-SQL argument is
  recognised as executed, so in `EXEC('DR' + 'OP DATABASE x')` the first
  fragment was linted, found harmless, and the rest was never read. It defeated
  every rule in the file, the `xp_` blocklist included, and `sp_executesql` took
  it too. A `+` in the executed expression is now refused exactly as `@` is.
  *Comment splicing:* `StripSQLComments` deleted comment bytes, and T-SQL treats
  a comment as a token separator — so `EXECUTE/**/AS` reached the lint as
  `EXECUTEAS` and the impersonation rule, the one the file calls the thing that
  would make every other rule negotiable, stopped matching. Deleting made the
  lint weaker than not stripping at all: `CREATE/**/TABLE` became `CREATETABLE`,
  which no rule matches either. Comments now blank to one space per byte, which
  restores the separator and also keeps every later offset true. Both closures
  are covered by tests proven by mutation. The blocklist of writing procedures
  remains enumerable by nature — `sp_rename` and `sp_msforeachdb` still pass —
  which is why the surrounding claims were corrected rather than strengthened:
  `README.md` now carries the "guard against the accident, not a sandbox"
  reserve that `MANIFEST.txt` and the DBA guide already had. Found by an
  external reviewer during an adversarial harm review; two of its three claimed
  bypasses were confirmed and the third, that comment splicing defeats every
  multi-token rule, was not — `DROP`, `BULK INSERT`, `CREATE TABLE`, `ALTER` and
  `DBCC` were all still refused.
- **A database name could plant executable T-SQL in the grant script.**
  `--grant-script` interpolated server-reported names raw into `-- ` comment
  lines, and a SQL Server identifier may contain a newline — so a database
  called `y⏎GRANT CONTROL SERVER TO [x];⏎-- ` left live T-SQL in a file whose
  own header tells the reader to run it as sysadmin. The principals differ:
  creating that name needs `dbcreator`, running the script needs sysadmin, and
  the payload rode on the tool's own least-privilege recommendation. The login
  was printed raw inside the `/* */` header the same way, where `*/` closes the
  block. Every string reaching a comment now goes through `commentSafe`, which
  is the comment-side counterpart of `quoteIdent`. The statements themselves
  were never affected: `quoteIdent` keeps a name with newlines inside one
  bracketed identifier. Found by an external reviewer during an adversarial harm
  review, verified end-to-end, and covered by a test proven by mutation.
- **`--all` turned on nine opt-ins while the documentation promised seven.**
  `README.md`, `--help` and the wizard all said seven after `--include-default-trace`
  and `--plan-cache-plans` were added. The two missing were the two heaviest:
  cached statement text can carry the literal parameter values a statement was
  written with, where Query Store text is parameterised. Screen 3 of the wizard
  now offers all nine — it could not turn those two on at all — the counts are
  corrected everywhere, and `docs/dba-guide.md` gains the two rows plus a
  paragraph on what the plan cache discloses. A new test compares `flagOrder`
  against `collect.KnownFlags`, so the wizard can no longer fall behind the
  command line in silence.

- **`90.availability/043.replication-subscriber.sql` reported nothing on a
  subscriber.** It gated on `sys.databases.is_subscribed`, which reads 0 on a
  push subscriber whose database carries the apply procedures the snapshot
  generated, so the collector returned `applies: 0` and every count at zero and
  the topology had to be rebuilt from the publisher's archive. Recognition now
  goes through `MSreplication_subscriptions`, then through those procedures, and
  `applies_source` names the test that answered.

- **The certificate advice wrapped badly**, and it is the most important message
  this tool prints. The phrase naming what `SQL_TRUST_SERVER_CERTIFICATE=true`
  gives up was substituted mid-sentence and pre-wrapped with a newline of its
  own; no single wrapping suits both the Windows and the SQL-login variant, so
  the paragraph ended in three ragged lines of 38, 26 and 33 columns against the
  74 the rest of it keeps. Someone meeting that decision for the first time was
  reading something that looked broken. The closing paragraph is now written out
  per case.

- **Links in the packaged `README.md`** are rewritten to absolute URLs at build
  time, pinned to the tag being released. `docs/` and `.env.example` are not in
  the archive, so from an unpacked copy those links led nowhere — and the reader
  they failed was the DBA sent to the guide before authorising a run. The
  release now stops if any relative link survives packaging.

## [0.21.0] - 2026-09-04

The first release with binaries. Everything below already worked from a
`go build`; what changes is that it can now be downloaded, checked against a
published SHA-256, and tied to the commit and workflow that produced it.

### Added

- **Published archives** for linux/amd64 and windows/amd64, each carrying the
  binary, `LICENSE` and `README.md`, alongside a checksum file and a build
  provenance attestation. Until now a binary somebody handed you could be
  checked against nothing but its own query corpus.

### What the collector is at this version

- **`check`** — connectivity, permissions and configuration, and the full list
  of what a collection would run and which databases it would touch, printed
  before anything is collected. `--grant-script FILE` writes the T-SQL granting
  exactly the permissions found missing, for the login the server reports, with
  a reason for each; the tool never runs it.
- **`collect`** — 62 read-only queries against catalog and dynamic management
  views, written to JSON and packed into a zip with a `MANIFEST.txt` that
  records what ran, what did not, and why.
- **`queries export`** — writes the embedded corpus to disk, so what the
  collector will ask can be read before a run is authorised. `--queries-dir`
  runs a corpus from disk in its place.
- **`env init`** — writes the annotated `.env` template, so the settings this
  tool accepts can be read on a machine that has only the executable.
- **The wizard** — an argument-less run on a terminal opens a four-step
  wizard covering the three things a first run gets wrong.
- **Disclosure is a flag, and the manifest says so.** Session text, object
  definitions, deadlock graphs, blocked process reports and Query Store plans
  are each off by default because each can carry application data or
  credentials; `MANIFEST.txt` records every one of them individually, whether
  it was on or off.
- **The collector takes no locks.** Every query runs under
  `READ UNCOMMITTED` with a `LOCK_TIMEOUT`, and no user or application table is
  read.

### Known limits

- The supported floor is SQL Server 2012. CI exercises 2017 and 2022, the
  oldest and newest images Microsoft publishes; 2012 is verified by hand, and
  what that covers is set out in [docs/verification-2012.md](docs/verification-2012.md).
- The build is not reproducible. The attestation is a statement by the build
  system about what it did, not something you can recompute by compiling.
- Only the linux/amd64 archive is smoke-tested on the runner. The Windows
  build is covered by compiling and by the test suite, not by execution.
