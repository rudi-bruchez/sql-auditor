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

- `90.availability/042.replication-distribution.sql` collects the replication agent profiles. Each agent row carries its `profile_id`, the `subscriptionstreams` of its subscription, and `skip_errors` with its source, read from the agent's job step when the step sets it and from the profile otherwise, since the command line wins. `agent_profiles` lists the profiles in use with `-SkipErrors`, `-MaxCmdsInTran`, `-SubscriptionStreams`, `-ReadBatchSize` and `-CommitBatchSize` in clear, `agent_profile_parameters` every parameter of those profiles that differs from the agent's built-in default in `msdb.dbo.MSagentparameterlist`, and the root counts the agents that skip errors. The job step command is never projected, only the tokens after `-SkipErrors` and `-MaxCmdsInTran`, because it is where `-PublisherPassword` is written. Measured on a SQL Server 2025 distributor: `sp_add_agent_parameter` refuses `-MaxCmdsInTran` in a profile, so that parameter is only found on the step. The three msdb reads are guarded and ask for no new grant; refused, they leave their number in `errors.profiles`, `errors.parameter_defaults` and `errors.job_steps`.
- `check`, and the third screen of the wizard, print a duration ceiling under the database list: the number of databases, the costly collectors that will run (the two options off for cost, `--estimate-compression` and `--measure-page-density`), the sum of their `@timeout` over the databases they run on, and the same sum over every unit of the plan. It is labelled as a ceiling and not an estimate, because it is one. Nothing bounds the collection as a whole yet; this only says before the run how long it can go.
- `20.databases/010.all-databases.sql` projects `cdc` (`sys.databases.is_cdc_enabled`) on every database. Change data capture holds the log with `log_reuse_wait = REPLICATION` while no replication flag is set, so until now that wait on a database publishing nothing had no explanation in the archive.
- `50.agent/010.jobs.sql` projects each job's `job_id`, in the same text form `90.availability/042.replication-distribution.sql` uses, so a replication agent joins to its Agent job by id rather than by a name anyone can rename. Measured on SQL Server 2025: both forms give the same 36 characters for one job.
- `10.system/040.error-log` keeps long-checkpoint lines in `notable`: the `FlushCache: cleaned up` header of an automatic checkpoint and its `I/O saturation` and `avgWriteLatency` lines, and the single `DirtyPageMgr::ForceCatchupOrFlushCache` line of an indirect one. The engine writes them when a checkpoint outlasts the recovery interval, which makes them a direct sign of a write path that cannot keep up. Both shapes were produced and matched on SQL Server 2025 under trace flag 3504.
- `80.workload/030.implicit-conversions.sql` projects which column each conversion converts, under `converted`: `database`, `schema`, `table` and `column` as 70.schema names them, the type it is converted to and its length in bytes, and `columns_in_expression` when one predicate converts several. They are read on the node the detection already binds, and only a table column counts, so a row whose `converted.column` is null converted a parameter or a table variable's column. Measured on SQL Server 2025, which also corrected the header: under a Windows collation the column conversion stays in the seek's residual predicate and the range comes from `GetRangeThroughConvert` on the parameter, while the seek-key clause matched only a varchar parameter widened against an nvarchar column. CI plants the Windows-collation case and asserts the column.

### Changed

- The collector lint now compares the result sets a file declares in `@resultsets` with the hinted statements that return rows, and refuses a file where the two differ, saying which way the count is off. It used to require only at least as many `OPTION (RECOMPILE, MAXDOP 1)` hints as declarations, so a file that lost an entry from `@resultsets` kept its hints, passed every test, and was refused by the runner at execution ("returned more result sets than declared"). A hinted `SELECT @var =` assignment and a hinted `INSERT INTO` a table variable or `#temp` table are not counted, since they emit nothing. Measured on the embedded corpus: the raw hint count matched the declarations in 80 of 114 files, and the count of emitting statements matches in all 114.
- `60.backup/010.history.sql` lists the last 2 000 backups of the window in `recent`, not 200, and the root says so: `window.recent_cap` is the cap and `window.recent_capped` is 1 when `window.backups_in_window` is larger. On twelve client collections the 200 were hit in six and reached back 9 hours of the 30 days on the busiest instance; 2 000 rows are about half a megabyte and reach about four days there. The whole window was not taken because nothing else bounds it.
- `10.system/045.default-trace-detail.sql` (`--include-default-trace`) shares its 5 000 rows out by event class, round robin from each class's newest row, instead of keeping the 5 000 most recent. A flood of one class used to push every other one out: on a client instance 14 of 21 configuration changes were lost to `Object:Altered`, and on the lab 4 999 of the 5 000 rows were `Object:Created` and all six configuration changes were gone. The new `classes` set gives, per class, the rows the trace files `held`, the rows `kept` and the window they cover, the root adds `counts.events_in_files`, and `capped` now means that some class was cut. The total stays at 5 000 on purpose: these rows name logins and hosts, and sharing them differently changes which are named, not how many. Lab cost: about 0.45 s against 0.35 s for 13 800 rows.
- `10.system/040.error-log.sql` keeps 200 message prefixes in `top_messages`, not 40: the 40 were hit on all fifteen client collections read and covered 54 % to 90 % of the lines. `notable` now reports its cap, as `status.notable_cap` and `status.notable_matched` (the lines that matched before it), which its header claimed and nothing did, and it is listed newest first, so a cut drops the oldest lines rather than the latest. The `sample` of `top_messages` and the text of `notable` are cut at 1 000 characters instead of 400, so long engine messages arrive whole; lines written by an application (`RAISERROR ... WITH LOG`) or by trace flags 1204 and 1222 carry more of their statement text as a result, by decision. Lab: 48 KB to 89 KB for the file.
- `50.agent/010.jobs.sql` reads 90 days of job history instead of 30, so `window.runs`, `window.failures` and `window.days_requested` cover three months where msdb keeps them. The output is one row per job whatever the window, and msdb's own retention, 1 000 rows by default, was the shorter limit on nine of twelve client collections. The header no longer justifies the window by archive size, and `50.agent/020.job-steps.sql` no longer says that a collector of full step commands does not exist: it is `021.job-step-commands.sql`.
- Three listings lose a cap that had no stated reason and no count beside it: `20.databases/025.fragmentation.sql` lists every fragmented partition it measured rather than the 25 most fragmented (at most 100, the measurement cap), `10.system/060.system-health.sql` every deadlock timestamp the ring holds rather than 200, and `20.databases/028.change-tracking.sql` every internal table holding a page rather than 200. The header of 060 also gives the measured reach of the ring, 15 to 46 minutes on nine client instances, instead of "hours on a busy instance, weeks on a quiet one".
- `90.availability/042.replication-distribution.sql` counts every error of its seven-day window in `counts.repl_errors_in_window` and states its listing cap as `repl_errors_listing_cap`. `repl_errors` still lists the 50 newest, but 50 rows no longer read as 50 errors: the count is taken by `COUNT(*) OVER ()` on the read that lists them, so `MSrepl_errors` is not read twice. NULL when the table could not be read, 0 when the window held none.
- `70.schema/010.objects`, `060.columns`, `090.statistics` and `091.statistics-density` list every user table. 010 and 060 took the union of the 200 tables with the most rows and the 50 with the most reserved pages, 090's detail and 091 the 200 by rows alone, so a large LOB table holding few rows was in the first two and in neither statistics file, although 090's header said the four covered the same tables. Twelve real collections taken on eight client instances in August and September 2026 had the table cap bind in 7 of 27 databases, leaving out up to 65 % of a database's columns, and the statistics cap in 6, at worst 5 617 of 12 996 statistics listed; 010's tail of tables was not empty in 3 of the 7, against what its header said. 010's untrusted constraint and retired-type lists lose their 200-row caps too. `listing_cap` and `listing_cap_by_size` leave the roots of 010 and 060, `listing_cap` those of 090 and 091; the totals beside them stay, and now equal the listed counts. Measured on a lab database of 1 201 tables and 3 001 statistics: 060 went from 436 KB to 2.6 MB at 0.8 s either way, 090 from 0.9 to 1.9 MB and from 1.2 to 1.4 s, 091 from 127 to 881 KB and from 0.29 to 0.47 s, 010 from 99 to 461 KB and from 0.8 to 1.0 s. A test now keeps all four files free of a TOP and filtered on `is_ms_shipped = 0`, where the old one compared 010 with 060 only, and CI asserts each list against its count.
- `70.schema/030.index-operational` lists every heap the DMV reports in `heaps`, instead of the 200 with the most forwarded fetches. The aggregate already read them all before the `TOP` kept 200, and the cap bound in 4 databases of the real collections, up to 810 heaps, with no heap total beside it. `listing_cap` stays at 200 and now describes `contention` and `page_compression` only. On a lab database of 600 heaps the file went from 33 to 97 KB, at 0.34 s either way.
- The caps that stay in 70.schema are raised and say when they bind. `040.compression` lists 2 000 rows of `largest_uncompressed` instead of 200 and counts them all in `counts.uncompressed_indexes`; the cap bound silently in 9 of 27 databases of the real collections. Its list is now cut from two table variables read whole, because as one statement the `TOP` set a row goal and the plan searched the catalog once per row: on a lab database of 2 400 uncompressed indexes the file took 6.6 s at 2 000 rows in that form and 3.0 s staged, against 3.2 s at 200 before, with the same rows. `045.columnstore` lists 2 000 index partitions and 2 000 trim-reason rows instead of 200 and 400, and adds `counts.index_partitions`, `counts.trim_reason_rows`, `listing_cap` and `listing_cap_trim_reasons`; a lab index of 451 partitions took the file from 131 to 235 KB. `050.heaps` samples the 100 largest heaps instead of 50 under the same page and time budgets, and counts the heap partitions above 128 pages in `counts.eligible_partitions`; with 130 eligible on the lab it read 100 instead of 50, in 4.0 s instead of 2.0. `021.missing-index-queries` takes 12 000 rows instead of 1 000, 600 groups times 20 queries, so its outer cap can no longer drop whole suggestions in handle order. A test keeps each projected cap equal to the `TOP` it describes and requires its count, and CI asserts the counts against the lists.
- `80.workload/030.implicit-conversions.sql` examines the 2 500 statements with the most logical reads instead of 1 000, and reads up to 1 000 candidates out of them instead of 200. The candidate cut used to be silent; the root now carries `bounds.matched` (statements whose plan passed the text prefilter), `bounds.candidates` and `bounds.candidate_cap`, and `bounds.statements_examined` is the window as filled, with the cap beside it in `bounds.examined_cap`, where it used to report the cap. On a lab cache of 4 000 to 5 000 statements on SQL Server 2025, 547 to 724 statements matched and all were read, 26 to 33 conversions were found instead of 21 to 29, and the file took 9 to 10 s instead of 6. CI asserts the bounds.
- `80.workload/053.plan-warnings.sql` reads up to 500 candidates out of its window instead of 200, and says how many matched its prefilter before the cut, in `bounds.matched` beside `bounds.candidate_cap`. `bounds.statements_examined` is now the window as filled, with the cap in `bounds.examined_cap`. On a lab cache where 930 statements of the 1 000 matched, 200 candidates reported 33 to 41 missing join predicates and 500 reported 150; the file went from 9 s to 18.5 s and from 83 KB to 209 KB. CI asserts the bounds.
- `80.workload/020.query-store.sql`, `023.query-store-most-executed.sql` and `024.query-store-rowcount.sql` list 200 queries instead of 50 (both rankings of 020, `by_query` and `by_query_hash` of 023, `by_query` of 024). The 50 was hit in 9 of 11 non-empty stores among real collections, and the whole store is already ranked to choose it, so the raise costs the archive and not the server: on the lab database that reached the cap, 020 went from 74 KB to 285 KB, 023 from 65 KB to 272 KB and 024 from 29 KB to 120 KB, with 023 taking 6.7 s instead of 6.2 s for the count its new root reads. 023 gains a root, with `listing_cap`, `counts.queries` and `counts.query_hashes`, and 024 gains `listing_cap` and `counts.queries`: until now neither said that its listing was cut. Each cap is one variable read by the `TOP` and by the field that reports it, which a new test holds, along with 023 and 024 ranking the same number; CI checks each listing against its population.
- `80.workload/026.query-store-interrupted.sql`, `028.query-store-resources.sql` and `060.spills.sql` list 200 rows instead of 50. All three already read their whole population to rank it, so the raise costs the archive only: on the lab database that reached the cap, 028 went from 50 to 200 queries of 205 eligible, and its files from 236 KB to 379 KB across ten databases, in the same time. 028 gains `counts.queries_eligible`, the queries above zero on at least one of its four measures, counted before the cap, and its root moves after the retained set to carry it; 026 already counted its populations, and 060 counts its spilling statements. All three project `listing_cap` from the variable their `TOP` reads, and CI checks each listing against its population.
- `80.workload/042.parallel-cost-distribution.sql` puts the 1 000 statements with the most CPU in its cost bands instead of 500. On a lab cache of 5 700 statements on SQL Server 2025, the 500 already held 97.5 % of the cache's CPU but only 13 statements in the [5, 25) band a threshold decision turns on, and the 1 000 put 40 there, for 4.2 s instead of 1.9 s and 145 MB of plan fragments instead of 62 MB, which `examined.plan_kb` and `examined.duration_ms` report on every run. The file's size does not change. The test that pairs each 80.workload cap with its reported value now covers this window and the windows and candidate caps of 030 and 053.
- `80.workload/041.plan-cache-plans.sql`, behind `--plan-cache-plans`, reaches 50 deep in each of its four rankings instead of 25, and its ceiling moves from 100 plans to 200 to stay four times the depth; a new test holds the two together. Three real collections selected 41 to 56 plans at 25, 2.6 to 4.6 MB; on the lab the selection went from 51 plans and 9.7 MB to 96 plans and 15.5 MB, and the file from 1.4 s to 2.2 s. More statements reach the archive, with their cached text, behind the same flag and of the same kind. The header's claim that collecting every procedure would make "a multi-gigabyte archive" is corrected: the 256 MiB run budget stops it first.
- The header of `80.workload/027.query-store-stats-usage.sql` no longer prices its 2 000-plan cap at 12 to 17 ms a plan, a figure its own later measurement of 56 ms a plan contradicted, and no longer names `70.schema/090.statistics` among the collectors that bound themselves with a `DECLARE`: 090 used a literal `TOP`. No behaviour changes.

### Fixed

- The statement lint of a `--queries-dir` corpus no longer reads a comment, an unusual space or a bare carriage return as something other than the server does. Comments were stripped at the top level of a file but not inside the dynamic SQL it executes, so `EXEC ('/* x */ msdb.dbo.sp_delete_backuphistory ...')` was accepted where the same call without the comment was refused, and `EXECUTE/**/AS`, `END/**/CONVERSATION` and `EXEC/**/(...)` went through the same way; a no-break space, a control character or a zero-width space, all token separators on SQL Server 2025, defeated `EXECUTE AS` and the procedure rule at any level; and a line comment ended only at `\n`, while the server ends it at a bare `\r` too, so every statement after `-- note<CR>` was hidden from the lint. Every level of dynamic SQL is now stripped and its separators made plain spaces before any rule reads it.
- The header of `10.system/064.server-diagnostics.sql` said the system_health window is about a day, and compared it with the four hours of `043.cpu-neighbours.sql` on that ground. That was the idle SQL Server 2022 lab; on nine client instances the ring reached back 15 to 46 minutes, 3 to 9 rounds, and the SQL Server 2025 lab held 4 rounds. The header now says so and points at `session.window_minutes`, and the comment on the cap of 400 intervals says it is a guard that has never bound.
- `60.backup/010.history.sql` groups `devices` by destination again. The directory was cut at the last backslash, so a virtual device (a VSS requestor or a backup agent, named with a GUID per backup), a Linux path and an Azure URL came back once per backup file: on seven of twelve client collections the set had a row per backup, up to 15 183 rows and 97 % of the file. A virtual device now has a NULL `path` and is grouped by its type, a path is cut at its last backslash or slash, and each row carries `device_names`, the number of names it stands for, and `a_device_name`, one of them. A count of destinations read from this set falls accordingly.
- `80.workload/030.implicit-conversions.sql` no longer names a table variable as the converted table. Showplan writes the column of an aliased table variable with `Table="@t"` and no `Database`, which passed the test meant to tell a column from a parameter, so this tool's own `10.system/040` came out converting column `pattern` of table `@derived`. A converted column now has to carry a `Database`, as every column of a real or temporary table does; the statement is still reported, with a null `converted.column`. Measured on SQL Server 2017 and 2025.
- The header of `90.availability/041.replication-publisher.sql` said `immediate_sync` and `allow_anonymous` explain a publisher log held by `log_reuse_wait = REPLICATION`. They make the distribution database keep commands; the publisher's log waits only for delivery to the distribution database. The header now says so.
- `10.system/021.host-info.sql` reads `host_architecture` only where the column exists. On SQL Server 2017 the view has no such column, the whole statement failed with error 207, and the archive lost the Windows distribution, release, service pack level and SKU to one absent column, which left an end-of-life OS question unanswerable. The architecture is now NULL there and the rest of the host row is kept. Both branches were run on SQL Server 2025.
- The `VIEW SERVER STATE` capability is labelled "Read the server state views (VIEW SERVER STATE)" instead of "Read performance counters (VIEW SERVER STATE)", in `check`, MANIFEST.txt and the skip reason of every collector gated on it, and the grant script's section for it is titled the same way. The old label was printed in the skip reason of collectors that read no counter, such as `10.system/042.connection-security.sql`, and told the reader the wrong thing about what was lost. The capability's name in `_run.json`, `view_server_state`, is unchanged.
- A connection that gives up on a server which accepts the socket and never answers the login now closes that socket, so the attempt ends with it. Since 0.37.0 the caller returned at its deadline, but go-mssqldb reads the pre-login answer with no deadline and without watching the context, so the driver's goroutine, the one waiting to close a late connection, and the socket lived until the server hung up. In the command line the process exit ended them; in the assistant, a long process, each attempt against a mute server left two goroutines and a socket behind. The pool is now built from the driver's connector with a dialer of its own, which records the sockets each attempt dials so they can be closed when it is abandoned. Measured against a listener that accepts and stays silent, with a 300 ms deadline: the server side sees the end of the stream at once and the goroutine count is back to where it was, where without the close it was two higher after two seconds and the socket still open.
- A same-day rerun no longer deletes the run of another target that was filed under the same folder. When a server gives no name, the run is filed under the address it was reached at, so a nameless target at address `SQL01` shared the folder of a server calling itself `SQL01`, and a rerun of either set the other's run aside and deleted it once it completed; the comparison of what each run collected did not catch it when both read databases of the same names. The run set aside is now kept when its `_run.json` names another server than this run's (case ignored), or records no server name at all, and the reason goes to the screen with the path and into the manifest's warnings.
- A same-day rerun no longer deletes the run of another target that shares its server name, or the lack of one. `_run.json` now records where the run was pointed, `SQL_SERVER` as given under `server.address` and `SQL_DATABASE` under `server.database`, and the run set aside is kept when either differs from this run's, with both named in the reason. Two nameless targets filed under one folder, such as address `A` with `SQL_DATABASE=B` and address `A_B`, and two clones giving one name from two addresses, are now told apart. The address is compared as `SQL_SERVER` is read (case, spaces, the `tcp:` prefix and the comma or colon before the port do not count) and `master` is the same as no database; a short name and its FQDN still count as two, which keeps the earlier run. A `_run.json` without the address, written before this change, is compared by name only. The archive therefore carries the address as well, and `MANIFEST.txt` lists it among what names things; an address holding an `@` or the password is not written.
- A database dropped while the collection is reading it no longer makes the run partial. Each collector left on it failed with error 911, the run exited 2, and a same-day rerun kept the archive it would have replaced, for a staging database another job had created and dropped meanwhile. When a collector meets error 911 and `DB_ID` confirms the database is gone, that collector and the ones after it on the database are skipped with the reason "the database was dropped during the collection", one warning names the collector that found it gone, and MANIFEST.txt lists the skips in one entry; a 911 on a database that still exists stays an error. A rerun after a run that had read the database keeps that run, and names the database once with the reason rather than once per collector.

## [0.37.0] - 2026-10-04

This release lets a collector lose one guarded part instead of the whole
document. A permission can now be declared optional: a login refused it keeps
the rest, and the manifest says what was left out. Four collectors use it
already. A same-day rerun that loses a collector to a missing right keeps the
earlier run, and the first connection gives up on a server that accepts the
socket and never answers. New collectors read Ola Hallengren's CommandLog, the
size and base of the next differential backup, the Query Store load profile
and resources per query, the spread of cached plan costs against the
parallelism threshold, server trigger sources, encryption certificates and
loaded modules. The backup history now carries each database's recent full
backups, so a differential base can be placed in its chain.

### Added

- A collector can declare a permission it uses without requiring it, in a new `@optional_permissions` directive with the same closed vocabulary and the same lint as `@permissions`. `check` probes it and notes it on the query's line (`runs without ... if refused`), the grant script asks for it and marks the collector under it as optional, and under a profile it counts as needed. A refused optional permission never skips the collector: it runs, the part that needs the permission is read inside `TRY` and reports the refusal in the document's root, and the manifest lists the collector under `reduced_scripts` in `_run.json` (script, capability, reason) and under "Queries run without an optional permission" in MANIFEST.txt. Lint refuses `CONNECT` as optional and a permission declared both ways. Until now a permission was all or nothing, so a login refused the one right a single guarded read needed lost the whole document. Measured on SQL Server 2025 with a login holding `VIEW ANY DEFINITION` and `VIEW SERVER PERFORMANCE STATE` only: `40.security/040.encryption-certificates.sql` was skipped whole before, and now runs with `encryption_keys.readable` false and error 300, while the grant script still asks for `VIEW SERVER SECURITY STATE`.
- `50.agent/050.commandlog.sql` reads the `dbo.CommandLog` table of Ola Hallengren's maintenance solution in the database where it is installed, which the per-database run finds by `OBJECT_ID` and a check of its fifteen columns: master by default, which the collector reaches through `@widened: system_databases`, or any user database the run collects. Over the last 30 days it totals every command by database and type, and lists the `ALTER_INDEX` and `UPDATE_STATISTICS` commands that have no end time, that failed, or that ran longest (25 per type), each with its operation (REORGANIZE, REBUILD, FULLSCAN...), the page count and fragmentation IndexOptimize recorded, how many commands were logged after it for the same database, and the other runs of the same command on the same index. The table is indexed on `ID` only, so the window is found by a binary search over `ID` and read by range, at most 100 000 rows: on a synthetic log of one million rows (470 MB) on SQL Server 2025, a filter on `StartTime` read 61 041 pages for the window where the range read 4 459, and the whole file took 1.2 s. The command text, the error message and the `ExtendedInfo` document stay on the server; object names leave, as in `70.schema`, and they cover every database the solution maintained, including ones the run did not select. Reading the table needs `SELECT` on it, which the grant script does not give; a refusal is recorded in the root as error 229 and the file completes. The manifest's notice for master and msdb now names this collector beside the database principals.
- `10.system/040.error-log.sql` counts error 9002, a transaction log that filled, in `derived_patterns`, derived from `sys.messages` like the other numbers so it is recognised in a localised log. It is severity 17 and logged in all 22 catalog languages, so `occurrences` comes from its header line. Measured on SQL Server 2025 by filling a 4 MB log that could not grow under an open transaction: one occurrence, counted by both routes. The count covers the current error log only, and it is the one trace of the outage left once the log has been freed.
- `20.databases/013.all-databases-2019.sql` lists `accelerated_database_recovery` for every database of the instance, system ones included, from `sys.databases.is_accelerated_database_recovery_on`, gated at SQL Server 2019 where the column appears. The row that motivated it is tempdb: SQL Server 2025 lets ADR be enabled there, which turns a rollback of temp-table work into a logical revert and lets the tempdb log truncate under a long transaction, and the archive had no way to say whether it was on. Measured on SQL Server 2025 CU7: a boolean on every row, tempdb included, 0 for tempdb on a default installation. The per-database document `020.properties` does not carry it, since it never runs on tempdb.
- `80.workload/029.query-store-load-profile.sql` returns, per database, the load of every Query Store interval over the whole retained history: executions with finished, aborted and failed apart, duration, CPU, logical and physical reads and logical writes. No query text and no query id leave the server. It is the only source in the corpus that places a peak hour or a quiet one over a month, where the other histories cover hours, and it is what a maintenance window can be chosen from. The grain is the database's `INTERVAL_LENGTH_MINUTES`, projected in the root and on every row; the listing keeps the 1 488 newest intervals, 62 days of hourly ones, and times are `datetimeoffset` as stored. The totals leave out the queries of scalar and multi-statement functions and of triggers with the predicate the totals of `020.query-store.sql` use, and report what they left out per interval: on SQL Server 2025, a non-inlined scalar function called 4 200 times was counted in its caller (19.0 s of CPU) and again in its own query (18.9 s), the naive sum was 39.6 s and the profile 20.7 s, matching the total of 020. An hour in which nothing ran has no interval, measured, so a missing hour is a zero. From SQL Server 2016.
- `60.backup/010.history.sql` ends with `fulls`: the ten most recent full backups of each database in the 30-day window, copy-only and snapshot ones included, with `backup_set_uuid`, `first_lsn`, `checkpoint_lsn` and `database_backup_lsn` as text, `is_copy_only`, `is_snapshot`, the device type, the finish date and both sizes. `recent` is the last 200 backups of the whole instance, which on a busy instance covers a few hours, so the full a differential depends on and the copy-only fulls taken since were not in the archive; joined to `030.differential-base` by `backup_set_uuid`, this set says whether the differential base is the latest full or an older one and what was taken in between. The cap of ten per database bounds an instance whose snapshot agent takes a full every hour, and the count of fulls in `per_database` says when it cut a database's list. Measured on SQL Server 2025: a T-SQL snapshot backup given `COPY_ONLY` on the `BACKUP` statement was recorded with `is_copy_only = 0` and became the differential base, since the bitmap is cleared at the suspend; only `SUSPEND_FOR_SNAPSHOT_BACKUP = ON (MODE = COPY_ONLY)` left the base in place.
- `60.backup/030.differential-base.sql` says, per database, how big a differential backup taken now would be and which full backup it would be taken against, from SQL Server 2016 SP2 on. It reads `modified_extent_page_count` from `sys.dm_db_file_space_usage`, the differential bitmap rather than the data, and `differential_base_time`, `_lsn` and `_guid` from `sys.database_files`, and looks the base up in msdb to say whether it was a snapshot, a copy-only or a virtual-device backup, or is missing from this instance's history. Measured on SQL Server 2025, the modified pages times 8 KB matched the differential's recorded size to within 2 MB, and one index rebuild produced a differential larger than its full. A database that never had a full backup reports a NULL base and zero modified pages, which is no base rather than no change.
- `80.workload/042.parallel-cost-distribution.sql` puts the 500 cached statements with the most CPU into bands of estimated cost (under 5, 5 to 25, 25 to 50, 50 to 100, 100 to 500, 500 and above, unknown) and says for each band how many statements, executions and CPU seconds ran in parallel, which is the measurement a value of `cost threshold for parallelism` can be chosen from. The cost is read from each statement's own plan fragment, fetched with `sys.dm_exec_text_query_plan` and its offsets: the batch's plan gives the cost of the batch's first statement. The cost is found by a text search in that fragment rather than by converting it to `xml`, so a plan too deeply nested for the `xml` type is costed like any other and the time does not grow with plan parsing: 1.7 s for 75 MB of plan text on a 2025 lab instance, against 8.2 s for the conversion. The 500 are chosen before any plan is read, and the file measures its own duration. No statement text leaves the server. From SQL Server 2016, where `max_dop` appears in `sys.dm_exec_query_stats`.
- `80.workload/028.query-store-resources.sql` says, per database and from SQL Server 2017, what each Query Store query waited on and how much log, tempdb and physical I/O it cost: `sys.query_store_wait_stats` by `wait_category_desc`, and `avg_log_bytes_used` (bytes), `avg_tempdb_space_used` (8 KB pages) and `avg_num_physical_io_reads` (read operations, beside `avg_physical_io_reads` in pages), totalled over the retained window. Up to 50 queries are kept by a round robin over wait time, log bytes, tempdb pages and physical reads, so a statement blocked once for a minute is listed where a ranking by executions would miss it. Idle and User Wait are listed per query with their class and left out of the wait ranking, so a Service Broker reader in `WAITFOR (RECEIVE ...)` does not top it; the root gives the store's waits by class and `WAIT_STATS_CAPTURE_MODE`, so an empty listing is explained. Measured on SQL Server 2025: the log bytes matched the transaction's own count, the tempdb pages the task's allocation counter, and a parallel SELECT INTO at DOP 22 recorded four times the serial tempdb pages and 39 s of waits for a 1.8 s statement, because every thread counts. The first 500 characters of each query's text are kept, as in 020 and 024. `020.query-store.sql` and `021.query-store-detail.sql`, which listed the wait statistics as not collected, now point to it.
- `70.schema/030.index-operational.sql` ends with `page_compression`: every rowstore partition set to PAGE compression, up to 200 by attempts, with `page_compression_attempt_count` and `page_compression_success_count` beside its leaf inserts, updates and allocations, rows and pages. The root counts PAGE partitions with and without a counter row, since closing a database empties the view, and the area has its own `errors.page_compression`. Measured on SQL Server 2025: compressible rows inserted on an increasing key give one success per two attempts, never more, and random rows give none; but rows inserted under a random key gave 1 success in 111 attempts while every leaf page was compressed, because a page split from a compressed page stays compressed without counting a success. A low ratio is therefore a question, not a finding. A PAGE heap filled by ordinary inserts attempts nothing and stays row-compressed until rebuilt. The file joins the `space` profile for this listing.
- `70.schema/055.page-density.sql` projects `compressed_page_count` and the partition's `data_compression` beside the fullness of each partition it measures. The `SAMPLED` call already returned the count, so nothing more is scanned. It settles the zero of `030.index-operational`'s `page_compression`, joined on table, `index_id` and `partition_number`: measured on SQL Server 2025, compressible rows under a random GUID key gave 1 success in 201 attempts with 201 of 201 leaf pages compressed, and random rows under the same key 0 in 821 with 0 of 822, two rows the counters alone cannot tell apart. Above 10,000 pages the count is the sample's scaled up (16,600 against 16,667 in `DETAILED`), so it reads as a share of `page_count`. `030.index-operational` adds `page_partitions.heaps` and `page_partitions.heaps_without_attempts` at its root, so a PAGE heap filled by ordinary inserts is counted even when the 200-partition cap drops it from the listing; `055` measures no heap.
- `40.security/032.server-trigger-definitions.sql` collects the source of every server-scoped trigger, LOGON and DDL alike, from `sys.server_sql_modules`, under `--include-object-definitions`. `030.server-surface.sql` already listed them without their body, which left an audit unable to say what a LOGON trigger reads or whether it can fail, and a failing LOGON trigger refuses every connection. Each row carries its events, the identity it runs as and a `source_state` (`sql`, `encrypted`, `clr`, `above_cap`), so a missing body is never read as a collection failure. MANIFEST.txt names the content through a new `server_trigger_source` disclosure, because its object-definitions paragraph is latched from the per-database module files and a run over databases holding no module would otherwise say nothing. Verified on SQL Server 2025 with a DDL trigger on `CREATE_DATABASE`: the body came back whole with the option and the file was reported not run without it.
- `40.security/040.encryption-certificates.sql` lists the certificates of master with the date their private key was last backed up, and resolves each database encryption key (TDE) and each encryptor found in the msdb backup history to the certificate or asymmetric key that protects it, with that certificate's backup date on the same row. A certificate whose private key was never exported makes every database and backup it protects unrestorable on another instance. A backup whose encryptor is no longer in master stays visible with a NULL name. From SQL Server 2022 the key view asks for `VIEW SERVER SECURITY STATE`, which the grant script's `VIEW SERVER PERFORMANCE STATE` does not include: the read is then refused (error 300, measured on 2025), and the root says so while the certificates and the backup encryptors are still collected. tempdb is left out. Measured on SQL Server 2025, where `encryptor_type` reads `CERTIFICATE_OAEP_256`.
- `10.system/080.loaded-modules.sql` lists the modules loaded in the SQL Server process whose company is not `Microsoft Corporation`, NULL included, with the counts that account for every module, so the analysis can look for the antivirus and filter DLLs Microsoft documents as a cause of crashes and slowdowns. On SQL Server 2025 on Linux the view describes the Windows image of the platform layer, with bare names and no path, and lists fourteen Microsoft modules with a NULL or translated company; the Linux process's own libraries are not in it. Windows was not measured.

### Changed

- Four permissions that one guarded part of a collector needed are now optional, so a login refused them keeps the rest of the document instead of losing it whole: `MSDB READ` in `20.databases/020.properties.sql` (the backup dates; the properties, files and index listings stay) and in `40.security/040.encryption-certificates.sql` (the backup encryptors), `VIEW SERVER STATE` in `70.schema/010.objects.sql` (the tables listing; the object counts, untrusted constraints and deprecated types stay) and in `70.schema/050.heaps.sql` (the per-heap measurement; the root's counts stay). Measured on SQL Server 2025: with `VIEW ANY DEFINITION` alone, 010 and 050 were skipped before and now run with `collected.tables` and `collected.heaps` false and error 262; with msdb refused, by a `DENY SELECT` on `backupset` or a `DENY CONNECT` on msdb, 020 runs with `collected.backups` false and error 229 or 916. 040's backup history read also moved inside `TRY` whole: `COL_LENGTH` is NULL for a table the login cannot see as well as for a missing column, so a login without msdb would have been told the instance predates backup encryption; it now gets `backup_history.source` `none` and the error (916 measured).

- `10.system/063.blocked-process-reports.sql` projects `monitor_loop`, the `monitorLoop` attribute of each blocked process report: the pass of the deadlock monitor that emitted it, counted from 0 at instance start. Reports of the same pass were detected at the same moment, which is how a blocker that is itself blocked is told to be a link of a chain rather than its head. Since only one report per episode is kept whole, the pass of the others had left the archive; `_index.json` now carries it on every entry of `reports[]` (null when the attribute is absent) and gives each episode its `first_monitor_loop` and `last_monitor_loop`. A pass number is not a clock: the monitor runs faster after a deadlock. The attribute is undocumented by Microsoft, its meaning is the one Michael J. Swart published in 2017, and it was not measured on real reports, which needs `blocked process threshold (s)` changed; the XPath was run on SQL Server 2025 against a literal report of the documented shape.
- `80.workload/027.query-store-stats-usage.sql` stops reading plans at a budget of 200 MB of plan text per database, newest first, so a store of large plans returns what it read instead of reaching the 300 s timeout and returning nothing: on a 2025 lab store whose 2,000 newest plans averaged 339 KB (663 MB), the file timed out before this change and now returns in 27 s, having read the 913 newest plans. The cap of 2,000 plans is unchanged and binds first on a store whose plans average under about 100 KB. The read stops at the first plan that would start past the budget and never skips one to continue, so the plans read are the most recent without a gap, and `window.oldest_execution` is now taken over the plans read rather than over the pinned set. The root gains `budget.bytes`, `budget.bytes_read`, `budget.plans_read`, `budget.plans_skipped` and `budget.duration_ms`; `truncated` is now 1 when either the cap or the budget left plans unread, and `plans_unparsed` counts the plans read that did not parse. Each chunk of plans is also cast into a table variable before the XQuery runs on it, rather than queried as the cast expression: the same fragments came out at 80 to 130 ms per MB instead of about 440, measured on plans of 44, 80 and 337 KB, and without the budget the 2,000 newest plans of the large store (562 MB) were read in 63 s.
- `80.workload/027.query-store-stats-usage.sql` gains `indexes_read`: every index the stored plans read, with how many plans and distinct queries read it, how many of those plans are forced, and when it was last executed and compiled. It is the same veto as `statistics_used`, for the drop of an index that `sys.dm_db_index_usage_stats` calls unused, a DMV that empties on every restart: presence proves a read, absence proves nothing. Only `//RelOp/IndexScan/Object` counts, which covers seeks, scans, key lookups and columnstore scans; an index named only as an `Object` of an `Update` element is maintained, not read, and an index that an UPDATE maintained and no query read was measured to be listed by `//Object` and left out by this path. Index names are unbracketed to match `sys.indexes`, and catalog, tempdb and table-variable indexes are counted in root and left out. The root also says how far back the scan reached (`window.oldest_execution`) and the store's retention and cleanup mode, since evicted plans and queries that `QUERY_CAPTURE_MODE AUTO` never captured are what the list cannot see. The index list shares the existing pass over the plans: on a 2025 lab store of 2,000 plans averaging 37 KB, the collector took 126 s against 113 s before, where a second file would have cast every plan to `xml` again and doubled it. The file joins the `space` profile, whose run is the one that proposes disabling unread indexes.
- `10.system/095.master-user-objects.sql` gains a `startup_procedures` array and a `startup_procedures_total` count: every procedure of master flagged to run at startup with `sp_procoption`, Microsoft-shipped ones included and with no cap, since the flag is in `sys.procedures.is_auto_executed` and not in the `sys.objects` listing. Measured on SQL Server 2025: flagging set `scan for startup procs` to 1 (in use at the next start), and unflagging removed the row and set it back to 0.
- `20.databases/010.all-databases.sql` projects `local_cursor_default` and `20.databases/020.properties.sql` projects `database.local_cursor_default`, both from `sys.databases.is_local_cursor_default`. It is the `CURSOR_DEFAULT` option, which decides whether a `DECLARE CURSOR` naming neither `LOCAL` nor `GLOBAL` is global, and without it a cursor declared that way in a module body could not be qualified. Measured on SQL Server 2025: under the default `GLOBAL`, a procedure that leaves its cursor open by an early `RETURN` fails on its second call in the same session with error 16915; under `LOCAL` it does not, and the setting that counts is that of the procedure's database, not the caller's. A go-mssqldb pooled connection reset between checkouts cleared the leftover cursor on the same session id.
- `70.schema/010.objects.sql` projects `is_not_for_replication` on each row of `untrusted_constraints`, and counts the not-for-replication constraints apart in `counts.untrusted_foreign_keys_not_for_replication` and `counts.untrusted_check_constraints_not_for_replication`. Measured on SQL Server 2025, a foreign key or check constraint created `NOT FOR REPLICATION` is untrusted from birth and stays so after `WITH CHECK CHECK CONSTRAINT`, which succeeds without changing anything, so the archive used to present it as a constraint to revalidate. The list is still capped at 200 rows per kind, but each kind is now ordered before it is capped, disabled constraints first and not-for-replication ones last: the TOP of each `UNION ALL` branch had no `ORDER BY`, so a capped list was a different sample on every run. The header now says that `counts.untrusted_foreign_keys` and `counts.untrusted_check_constraints` include the disabled and not-for-replication constraints; the names are kept because collected archives carry them.
- `10.system/064.server-diagnostics.sql` reads the worker pool out of every `queryProcessing` round of `system_health`: `max_workers`, `workers_created`, `workers_idle`, `pending_tasks`, `oldest_pending_task_wait`, `unresolvable_deadlock` and `deadlocked_schedulers` per interval, with `intervals_with_pending_tasks`, `workers_created_peak`, `max_workers` and `intervals_with_scheduler_deadlock` in the root. That is about a day of worker exhaustion with a date on each round, where the archive had only the instantaneous scheduler queue and the cumulative `THREADPOOL` wait. Each interval also gives the longest pending I/O of its round and the file it waits on (`longest_pending_io_ms`, `longest_pending_io_file`), taken by duration rather than by position in the list. The worker attributes were measured on SQL Server 2025; the pending I/O element was empty on every round measured, so the file has never been seen filled. The new reads are bare attributes off the payload root already bound, plus two paths on the I/O component only.
- `70.schema/050.heaps.sql` projects `compressed_page_count` and the partition's `data_compression` on each heap it reads. The `SAMPLED` call already returned the count, so the candidates, the page budget and the pages read are unchanged. It says how much of a PAGE heap is compressed now, which `030.index-operational` cannot: measured through both files on SQL Server 2025, a heap rebuilt and then filled by ordinary inserts read 422 attempts and 228 successes, like a heap only rebuilt (420 and 228), while 217 of its 539 pages were compressed against 217 of 218. Rows meet `030`'s `page_compression` on table, `partition` against `partition_number`, and `index_id` 0. Above 10,000 pages the count is the sample's scaled up, as `page_count` is. `030.index-operational`'s header no longer says a rebuild leaves `leaf_inserts` at 0: that holds for an offline rebuild of an index, while a heap rebuilt, or any partition rebuilt online, counts every row it writes as a leaf insert (19,000 after 1,000 of 20,000 rows were deleted), so such a row reads like a load.
- `10.system/010.properties.sql` projects `schedulers.failed_to_create_worker`, the number of visible online schedulers that could not create a worker, a state readable in one snapshot.

### Fixed

- A same-day rerun that skipped a collector the earlier run had brought back results for no longer deletes the earlier run. Since 0.36.0 a skip counted as planning, so a login that lost a permission between the two runs, or a newer binary that raised a collector's version floor, moved it out of the profile or made it opt-in, replaced an archive holding that collector's data with one that had none. Such a skip is now a loss, named on screen and in the warnings with the collector, its databases and the skip's own reason. Skips that only restate a narrowing already named, an opt-in the earlier run had on, a changed `--profile` or `QUERY_STORE_DB_INCLUDE`, are not named twice, and a collector the earlier run only failed on loses nothing when skipped.
- `50.agent/020.job-steps.sql` no longer fails whole for a login built from the grant script. Its proxy names came from `msdb.dbo.sysproxies`, which neither SQLAgentReaderRole nor SELECT on `sysjobsteps` covers; the read is now guarded like the one of `40.security/030.server-surface.sql`, and the root says in `proxies.readable` whether the names could be read.
- MANIFEST.txt no longer says the Query Store detail was collected "because --query-store-detail was passed" when it was not. The disclosure also latches, on purpose, when a default collector's copied query text carries the Showplan XML namespace, and the sentence now says so and points to the warnings that name the files.
- `10.system/064.server-diagnostics.sql` truncates event timestamps to the second instead of rounding them. `CONVERT` to `datetime2(0)` rounds, so two components of one round posted at .499 and .500 fell a second apart and the round read as two partial rows, which the file presents as ring-buffer overwrite.
- On SQL Server 2022 and later, a login built from `check --grant-script` could not read `sys.dm_database_encryption_keys`, and nothing said so before the run. The script grants `VIEW SERVER PERFORMANCE STATE` there rather than `VIEW SERVER STATE`, and that view asks for `VIEW SERVER SECURITY STATE`, which the narrower permission does not include: `40.security/040.encryption-certificates.sql` came back with `encryption_keys.readable` false and error 300. `@permissions` now accepts `VIEW SERVER SECURITY STATE`, 040 declares it in place of `VIEW SERVER STATE`, `check` probes it with a read of the view (`view_server_security_state`), and the grant script grants it by that name from 2022, and before 2022 folds it into `VIEW SERVER STATE`, the only permission that covers the view there. Measured on SQL Server 2025 with exactly the server-level grants the script wrote: the keys read. 040 declares it optional (see `@optional_permissions` under Added), so a login refused it still collects the certificates.
- A grant script written by `check` against a corpus from disk restricted to a few files told the DBA, under every section no file of that corpus declared, that the preflight and the corpus disagreed and that this was worth reporting as a bug. The probes are the same whatever the corpus, so with a restricted one this is the expected outcome; the line now says so, and that the grant can be left out, while still calling it a bug with the embedded corpus. The sections themselves are unchanged: the capabilities were refused, and the script still says so.
- `check --grant-script` asked for a user in every database the login could not enter even when no collector of the corpus runs inside a database. The question was put under a profile only, so a corpus from `--queries-dir` holding instance-scoped files alone wrote a `CREATE USER` for each database on the instance: measured on SQL Server 2025 with two instance-scoped files, seven of them. The section is now written only when a planned collector is database-scoped, with or without a profile. With the embedded corpus the script is unchanged, byte for byte on the same instance and login.
- `40.security/030.server-surface.sql` failed whole for a login that cannot read `msdb.dbo.sysproxies`, losing the linked servers, server triggers, credentials and audits to "The SELECT permission was denied on the object 'sysproxies'". The proxy count of each credential is now read first inside `TRY`, as `042.replication-distribution.sql` reads msdb: refused, `agent_proxies` is NULL on every credential rather than 0, and the root says why in `agent_proxies.readable`, `error_number` and `error_message`. Declaring `MSDB READ` instead was rejected: its probe reads `backupset`, on a default msdb only `TargetServersRole` may read `sysproxies`, so the declaration would have skipped the whole file for a login without backup history and still let it fail for a login built from the grant script. Measured on SQL Server 2025 with a login holding `VIEW ANY DEFINITION` and `VIEW SERVER STATE` only: every array collected, error 229 in the root; as sysadmin, the count matched the previous expression with two proxies on one credential. A corpus test now requires any msdb read in a file declaring no msdb capability to sit inside `TRY`.
- The first connection of a collection, of `check` and of the assistant, the blocking watch's two connections, and the collection's reconnects now give up on a server that accepts the socket and never completes the login. The driver bounds only the dial, so such a server held the run until the socket died: measured, still waiting after two minutes. The budget is `SQL_CONNECT_TIMEOUT_SEC` for the dial and the same again for the login.

## [0.36.0] - 2026-09-27

This release finishes the harm review of 27 September 2026 and makes the
collector cheaper and harder to mislead on the instance it audits. The heap
scan has a page budget, the physical reads skip a locked table instead of
losing the list, the error log is measured before it is copied into tempdb,
and every collector's temporary tables go when it does. A same-day rerun no
longer deletes a wider run, and the blocking watch retries before it gives up.
The plan search reads plans as text under a binary collation, about four times
cheaper for the same matches. On the reading side: every statistic without a
cap, Query Store queries folded by hash, connections counted per application
against the client pool size, connection failures counted in the error log on
a localised instance too, and the database principals of master and msdb.

### Added

- `70.schema/090.statistics` emits `statistics_all`, one short row per statistic on every user table with no cap, so redundancy and staleness counts are no longer floors on wide schemas. The capped `statistics` detail is unchanged.
- `70.schema/090.statistics` collects `persisted_sample_percent` (2016 SP1 CU4 and 2017 CU1 onward, NULL on older builds) in both lists.
- `80.workload/020.query-store.sql` and `023.query-store-most-executed.sql` end with `by_query_hash`, which folds the query_ids that share a `query_hash` and counts them under `query_ids`. On SQL Server 2025, twenty literal variants of one join ranked 7th to 27th as query_ids and 3rd as one hash row.
- `40.security/020.database-principals.sql` also runs on master and msdb, so msdb role memberships and master's certificate users are collected. They are brought in by a new `@widened: system_databases` purpose, so no other database-scoped collector reads them; DB_INCLUDE does not narrow them, DB_EXCLUDE does.
- `10.system/042.connection-security.sql` has a `pools` result set: live connections grouped by host, program and login, capped at 200 groups with the total at the root, to compare each application with the client-side default Max Pool Size of 100. It is declared as the new `connection_pools` disclosure, which MANIFEST.txt prints on every run.
- `10.system/040.error-log.sql` counts failed logins (18456), SSPI handshake failures (17806), the user connection limit (17809), network errors at login (17830), connections refused at start or for want of a thread (17187, 17189), long I/O (833), non-yielding workers and schedulers (17883, 17884, 17888) and paged-out memory (17890), each derived from sys.messages so it works on a localised log.

### Changed

- A failed blocking-watch poll is retried three times on a new connection (0.5, 1 and 2 seconds, never past the run) before the watch stops; each recovery and the stop are warnings in `_run.json`.
- `10.system/040.error-log.sql` measures the current log with `sp_enumerrorlogs` and skips the read above 50 MB, reporting `skipped_for_size` and the size in `status`.
- `70.schema/050.heaps` reads heaps within a budget of 200,000 estimated pages per database (full read under 10,000 pages, 1 percent above), reports `sample.skipped_budget`, `sample.page_budget`, `sample.estimated_pages_read` and `sample.measured_heaps`, and starts no new heap after 240 seconds (`sample.budget_sec`).
- `80.workload/020.query-store.sql` ranks `top_queries` by a round robin over duration, CPU and logical reads, as 021 does, and projects `rank.duration`, `rank.cpu` and `rank.logical_reads`. Ranked on duration alone, the list missed the query that leads reads.
- `40.security/010.principals.sql` lists server grants to certificate- and asymmetric-key-mapped logins as well, including the instance's own signing certificate logins.
- `80.workload/030.implicit-conversions.sql` and `80.workload/053.plan-warnings.sql` read each batch plan with `sys.dm_exec_text_query_plan` and search it under `Latin1_General_BIN2`, instead of building it as xml, casting it to text and searching case-insensitively. Same matches on the lab, about four times cheaper in CPU for the search (030 5.3 s to 1.2 s, 053 7.7 s to 1.4 s over 1,000 statements). A batch plan too deep for the xml type can now reach the candidates; 053 counts it in `bounds.plans_unparsed`.
- `derived_patterns` in `10.system/040.error-log.sql` reports NULL instead of 0 when the log was not read, including when the 50 MB guard skipped it.
- The DBA guide states that the rerun rule compares what each run set out to collect, not what it brought back, and points to `--keep` to hold on to both runs.

### Fixed

- The blocking watch opened its identity connection from a pool of one, so every collection started the watch several seconds late and never identified a waiter.
- A same-day rerun that exits 0 no longer deletes the earlier run when that run had an opt-in, a database or a wider profile the rerun lacks; what was missing is named on screen and in the warnings.
- The collection session is reset after every collector, so `#temp` tables (the error log's whole `#log` among them) no longer stay in tempdb until the run ends.
- `20.databases/025.fragmentation`, `70.schema/055.page-density` and `70.schema/050.heaps` skip a table held by a lock (open ALTER TABLE, offline rebuild) instead of losing the whole list; the skip is counted in `skipped_locked`, and only errors other than a lock timeout mark the area failed. `050.heaps` computes `counts.total_mb` from allocation units, so a locked heap no longer fails its counts either.
- A malformed `.env` line is reported by line number without echoing its value.
- Module definition file names skip names already taken, so a module named `p~2` is no longer overwritten.
- README, the guide and `--help` said `--all` turns on ten options; it turns on eleven. The guide no longer says the 256 MB budget protects the disk.
- The headers of 020 and 023 no longer claim that 500 characters are too few to rebuild a payload. `docs/dba-guide.md` lists which default collectors keep statement text and says a password written in one can reach the archive.
- The `70.schema/090.statistics` header claimed the stats properties DMF returns no row for a never-populated statistic; on SQL Server 2025 it returns a row of NULLs.
- A same-day rerun that narrowed QUERY_STORE_DB_INCLUDE, lowered QUERY_STORE_TOP, shortened the Query Store window or changed the comparison point now keeps the earlier run instead of replacing it.
- A same-day rerun also keeps the earlier run when it did not plan a collector the earlier one ran, on any database both read. The comparison reads the collectors each run recorded rather than trusting `QUERIES_DIR`, which let a narrower corpus under the same setting, or a new binary with another embedded corpus, delete a wider run.
- The blocking watch's first poll is retried on a new connection like later polls, so a transient failure at start no longer leaves the whole collection unwatched; a server's refusal still turns the watch off at once.
- The blocking watch's first poll retries a transient server error (deadlock victim, resource, throttling) instead of turning the watch off for the whole collection; only permission refusals (229, 297, 300) stop it at once.
- Each blocking watch reconnect is bounded to 3 seconds whatever SQL_CONNECT_TIMEOUT_SEC says, even against a server that accepts the connection and never answers; a collector is unwatched for 20.5 seconds at worst during a retry, down from about a minute.
- The archive is no longer written through a symbolic link at its name in OUTPUT_DIR; a name already taken at archive time stops the archive and the run folder is kept.
- A server whose SERVERPROPERTY('ServerName') is NULL is filed under the SQL_SERVER address, plus SQL_DATABASE when it is not master, instead of `_`, with a short hash of the address when it holds characters a folder name cannot, so two such targets collected the same day (`SQL01\PROD` and `SQL01_PROD` among them) no longer replace each other.
- The page density header, the README and the guide state both lab measurements of what SAMPLED reads from a large allocation unit (about 1 percent, and 8 to 12 percent), and plan with the higher one.

## [0.35.0] - 2026-09-27

Two things moved in this release. The collector reads more of what an instance
already records about itself: the transactions open when the run happened, the
blocking reports grouped into episodes over the whole capture, why a kept plan
is serial and what it cost to compile, the Query Store totals a top 50 needs
for a denominator, and the grants to server roles that never reached the
archive. And a harm review on 27 September 2026 asked what the tool can do to
the instance it audits: the plan cache is no longer cast to text whole before
the cap applies, the physical reads stand down on an availability group
secondary, the documentation no longer promises that nothing waits behind the
collector, and the lint that guards `--queries-dir` accepts only the procedures
the shipped corpus calls.

### Added

- `80.workload/053.plan-warnings.sql` says, for each statement it keeps, why
  the plan is serial (`non_parallel_reason`), what compiling it cost
  (`compile.time_ms`, `cpu_ms`, `memory_kb`) and which trace flags were in
  force at compile time (`compile_trace_flags`). They are read on the
  statements the warnings already selected and not added to the prefilter:
  every plan of an instance at MAXDOP 1 carries a non-parallel reason, and
  filtering on it would crowd out the annotated statements. Measured on SQL
  Server 2025 CU7.

- `10.system/049.open-transactions.sql`: the transactions open when the run
  happened, oldest first, with their begin time, whether the session is
  sleeping and for how long, and the log each holds in the databases it wrote
  to. No login, host, program or statement text; the session id is kept to
  join the blocking reports and deadlock graphs. `024.log-stats` could say a
  log was held by an active transaction and not which one or since when. It
  starts from the session rather than the request, because the case that
  matters is a session that went to sleep with its transaction open, which has
  no request: measured on SQL Server 2025 CU7, such a session holding 677 988
  bytes of log is listed here as sleeping. Rows are keyed by `transaction_id`
  and the counts are of distinct transactions, so a bound session or MARS does
  not count one transaction twice; a transaction with no session is listed
  only when it is distributed, the in-doubt DTC case, since every other one is
  the engine's worktables; user transactions come before system ones in the
  listing; the log written by system transactions on a transaction's behalf is
  projected apart.

### Changed

- `80.workload/020.query-store.sql` gives its root the totals of the retained
  window (`totals.executions`, `duration_ms`, `cpu_ms`, `logical_reads`) and
  `counts.query_hashes` beside `counts.queries`. The top 50 is ranked by
  duration and had no denominator, so no share of the workload could be drawn
  from it. The totals leave out the queries of scalar and multi-statement
  functions and of triggers, and say how much that was under
  `totals.excluded`: each of them is recorded both as its own query and inside
  the statement that ran it. Measured on SQL Server 2025 CU7, a function's
  query at 4 120 ms of CPU inside a caller at 4 179, a trigger's at 616 inside
  an INSERT at 741. On the lab database the naive sum of the top queries came
  to 4 231 ms against 2 188 for the corrected total. Thirty statements sent
  with literals gave 40 query ids for 11 query hashes.
  A statement whose object no longer resolves, a function since dropped, stays
  in the totals and is counted under `totals.unresolved`; `top_queries` carries
  the same classification per row as `nested`, so a share can be computed over
  one population. An empty store reports totals of 0.
- `10.system/063.blocked-process-reports.sql` reads the fields that place a
  report in a blocking episode out of EVERY report in the capture, and the index
  gains `episodes[]`: blocked session and ownerId, blocking session, wait
  resource and lock mode, report count, first and last seen, longest wait, and
  the blocker's status and trancount. The 500 reports written whole are now one
  per episode, its longest, the longest episodes first, where they used to be
  the 500 most recent. On a client capture of 1 635 reports the old cap dropped
  1 135 and shrank the window to nineteen hours, and since a report is re-emitted
  every monitor tick while a block lasts, most of what it kept was the same few
  episodes again. A report that is not its episode's longest is no longer an
  omission, and the ones past the cap are announced to the manifest once rather
  than one line each.

### Fixed

- On an availability group readable secondary, `20.databases/025.fragmentation`,
  `70.schema/050.heaps` and `70.schema/055.page-density` skip their physical
  reads and say so under `skipped.readable_secondary`, without marking the run
  partial. `sys.dm_db_index_physical_stats` takes an intent-shared lock that
  Microsoft documents as able to block REDO there. A database that belongs to
  an availability group and whose replica state cannot be read is skipped too,
  and that is reported as an error, so the run is partial. Not reproduced: the
  lab has no availability group; tested with the flag and the failed read
  forced.
- The README and the guide no longer say the collector takes no lock the
  workload waits behind, nor that reading the Query Store takes no lock. Both
  were contradicted by the guide's own section on the other direction and by
  the header of `80.workload/027`. The guide also says that a blocking watch
  whose read fails during the run stops for the rest of it.
- `80.workload/030.implicit-conversions.sql` and `053.plan-warnings.sql` bound
  their window before reading any plan: the 1 000 statements with the most
  reads (030) or CPU (053) are taken from `sys.dm_exec_query_stats`, and only
  their batch plans are cast to text and searched, once each. The `TOP (200)`
  sat on the query that did the casting, so every plan in the cache was cast
  and searched before the cap applied, at about 95 ms per megabyte measured on
  SQL Server 2025 CU7, which on a large production cache runs into the
  300-second timeout. A converting statement outside the window is no longer
  seen; `bounds.statements_examined` now reports 1 000. On the lab instance the
  two versions returned the same statements, with one more for the new 030.
- The statement lint that guards `--queries-dir` refuses every procedure
  except the four the shipped corpus calls (`sp_executesql`, `sp_readerrorlog`,
  `sp_estimate_data_compression_savings`, unqualified or in `sys`, and
  `msdb.dbo.sp_help_jobhistory`; `dbo.sp_readerrorlog` is a user procedure
  and is refused), a procedure
  named by a variable, and a batch that opens with a procedure name, which
  T-SQL calls without `EXEC`. It refused writing procedures by name, and a
  harm review on 27 September 2026 passed twenty statements through it,
  among them `msdb.dbo.sp_delete_backuphistory`, `sp_purge_jobhistory`,
  `sp_control_plan_guide N'DROP ALL'` and `sp_rename`. `ENABLE`, `DISABLE`,
  the Service Broker verbs, `ADD SIGNATURE` and `NEXT VALUE FOR`, which
  advances a sequence from a SELECT, are refused too. `SELECT ... INTO
  dbo.Copy` is no longer let through by an `INSERT INTO #scratch` in the same
  batch, which the count of permitted forms credited twice. All of them are in
  the adversarial test table.
- `40.security/010.principals.sql` names the target of a grant on a login:
  `IMPERSONATE ON LOGIN::sa` came out with `on_object` null, because class 101
  was resolved with `OBJECT_NAME`, which looks up database objects. It now uses
  `SUSER_NAME`, and names the endpoint for class 105. `server_permissions` also
  lists grants to server roles, `public` included, with a new `grantee_type`:
  `CONTROL SERVER` granted to a user-defined role and `VIEW SERVER STATE`
  granted to `public` never reached the archive. Measured on SQL Server 2025
  CU7.
- `10.system/010.properties.sql` no longer flags `pending_reconfigure` for
  `max server memory (MB)` at 0 shown in use as 2147483647, nor `min server
  memory (MB)` at 0 shown as 16. Both are the engine's documented display and
  the second is how an instance comes out of installation, so every archive
  carried a pending change nobody had made. Measured on SQL Server 2025 CU7.
- `061.deadlock-graphs.sql` and `063.blocked-process-reports.sql` cut the path
  of an `.xel` on either slash. They looked for a backslash only, so on SQL
  Server for Linux the pattern they read matched nothing: 063 reported a present
  and empty capture on an instance holding four reports, and 061 kept only what
  the ring buffer still held. Measured on SQL Server 2025 CU7 on Linux.
- A database in `SINGLE_USER` mode is skipped with the reason
  `user_access=SINGLE_USER`. With another session inside it, `HAS_DBACCESS`
  answers 0, so it used to be skipped as `no access for this login`, and the
  grant script offered a database user the login already had. With the slot
  free it used to be collected; it is now left alone, since the mode says
  somebody wants to be, and the mode stays visible in
  `20.databases/010.all-databases`. Measured on SQL Server 2025 CU7.
- A database in `RESTRICTED_USER` mode that the login may not enter is skipped
  with the reason `user_access=RESTRICTED_USER`. The mode admits only
  `db_owner`, `dbcreator` and `sysadmin`, so `HAS_DBACCESS` answers 0 for an
  audit login that has a user there, and the grant script offered to create
  it. A login the mode admits is collected as before.
- The `_index.json` of `70.schema/<database>/080.modules/` carries what each
  module is besides its text: `create_date`, `modify_date`, `uses_ansi_nulls`,
  `uses_quoted_identifier`, `is_schema_bound`, `is_recompiled` and
  `execute_as`. The SQL projected all of them and the writer kept only the
  schema, name, type, rank and size. `execute_as` also reads `OWNER` for a
  module created `WITH EXECUTE AS OWNER`: it was `USER_NAME` of the stored id,
  and that id is -2, whose name is NULL, the same as a module with no
  `EXECUTE AS`. The stored id travels beside it as `execute_as_principal_id`,
  since a database may also hold a user called `OWNER`: measured, `EXECUTE AS
  OWNER` and `EXECUTE AS 'OWNER'` both read `OWNER`, with ids -2 and 5.
  Measured on SQL Server 2025 CU7.
- `70.schema/050.heaps.sql` reads its two areas inside TRY/CATCH into buffers
  and always emits its document, with `collected.*`, `errors.*` and
  `error_message` like `030.index-operational`. It had no TRY at all, so one
  lock timeout lost the whole file with no word in the archive about why:
  measured behind a Sch-M held on one heap by an open `ALTER TABLE`, the old
  version wrote nothing and the new one wrote the list with the blocked counts
  marked. A lock can still cost the whole list, because the query that picks
  the heaps blocks too.

## [0.34.0] - 2026-09-24

The error log is localised, and this repository has said so in a comment since
the collector was written. Underneath that comment sat twelve hand-written
English `LIKE` patterns, which is exactly the parser the comment warns against.
Two of the messages they were meant to catch carry a decision rather than a
hint, so those two are now derived from their message number instead of typed
out, and the three collectors that decide whether a transaction log is healthy
gained the fields that separate a log that works from a log that is stuck.

### Added

- `10.system/040.error-log.sql` derives the pattern for a message from its
  NUMBER, in the language the instance actually logs in. The template comes out
  of `sys.messages`, it is split on a grammar of parameter markers rather than
  on a list of them, and the pattern is the longest literal piece that matches
  this message and no other in that language's catalog. Eight things had to be
  right and each is recorded in the file with the measurement behind it. Three
  of them fail silently rather than loudly, so each was verified by breaking it
  in a language where it can fall: without the binary collation the French
  fragment of 17137 goes from unique to four matches, without the `ESCAPE`
  clause the Dutch fragment of 3421 stops matching a line that literally
  contains it, and with an enumerated marker list instead of the grammar the
  retained fragment of 15457 keeps a percent sign and then matches zero lines
  of a real log.

  Two result sets come with it. `derived_patterns` publishes what the
  derivation decided for each number, including the reason when it decided
  nothing, so a message that could not be resolved stays distinguishable from a
  message that did not occur. It also carries the two literals that delimit the
  first parameter and their positions, because a unique fragment is enough to
  find a message and not enough to pull a name out of it; the left literal is
  legitimately empty in German, where 17137 opens on its parameter. `recovery`
  publishes one row per recovery with the elapsed seconds as a number, because
  that number arrives inside a localised sentence and parsing it downstream
  would mean one regular expression per language.

  Measured on the full English catalog while building it: `FORMATMESSAGE`
  returns NULL, without raising, for every `message_id` below 13001, which is
  the user-definable boundary. It renders all 10 384 messages at or above it
  and none of the 6 366 below. That is worth knowing before spending an
  afternoon on argument arity.

- `20.databases/023.log-vlf.sql` reports where the active tail of the log sits.
  The count alone cannot tell a healthy log from a blocked one: two active VLFs
  out of two thousand are nothing if they are the first two and are a log that
  can no longer wrap if they are the last two. The position was not computable
  as the file stood, because the staging table held the size and the active
  flag but no ordering key and a table variable has no guaranteed insertion
  order. Both mechanisms already report a byte offset, `vlf_begin_offset` from
  the view and `StartOffset` from `DBCC LOGINFO`, so the rank now comes from
  that. The root carries `space.vlf_last_active_pct` over the whole log and
  `vlf_per_file` carries the rank beside the count it is relative to.

  Both `DBCC LOGINFO` shapes were exercised on a 2025 instance by forcing the
  branch, which the design had assumed impossible there, and the eight-column
  shape is what 2025 returns. What that does not prove, and the file says so,
  is the behaviour on the versions the branch exists for.

- `60.backup/010.history.sql` reports the maximum interval between two
  consecutive backups per database and per type, and the interval between the
  last backup and the collection. The existing `max_seconds` is the duration of
  one backup, which is a different question, and the `recent` set cannot answer
  this one because its cap is global to the instance. `LAG` returns NULL on the
  first row of a partition, which gives the contract for "no pair" for free and
  is published as NULL rather than covered by a zero. The right edge is
  projected rather than left to inference: without it the set would report a
  24 hour maximum gap for a database whose log backups stopped three weeks ago,
  which is the failure this collector exists to reveal.

- `vlf_warnings` in `10.system/040.error-log.sql` names the database each 9017
  line is about, and the count it reports. `derived_patterns` says how many
  lines matched a number and never which database any of them names, so the
  analysis layer had nothing to attribute the engine's own warning to and could
  only escalate at the level of the whole instance, reporting as a finding every
  database whose virtual log files had been measured. The delimiters were
  already published for every parameter of every derived number, so this is the
  same gesture the recovery set makes for the elapsed seconds of 3421.

### Changed

- `database_mounts` in `10.system/040.error-log.sql` now extracts the database
  name through the derived delimiters rather than from the first two
  apostrophes of the line. Fixing only the filter would have been worse than
  fixing neither: applying the old extraction to the twenty-two templates of
  17137 returns an empty name in nine languages, five of which quote nothing at
  all, so on a German instance every database would have been found and every
  one of them folded into a single row named with the empty string. All
  twenty-two catalog languages now return the right names and counts, and the
  set is unchanged row for row against a real English log.

### Fixed

- `tools/verify-corpus-grammar.ps1` read `INTO` off the wrong node of the parse
  tree, so the exclusion that keeps `SELECT ... INTO` out of the result-set
  count had never fired. Nothing showed because no collector in the corpus
  writes that form, which means the comment above it described behaviour the
  script did not have. The fix is a no-op on the corpus as it stands.

## [0.33.0] - 2026-09-22

An audit could say which statistics were stale and not which ones were used.
The engine has no usage DMV for statistics and never has; it does write the
answer into every execution plan, and the Query Store has been keeping those
plans all along.

### Added

- `80.workload/027.query-store-stats-usage.sql` reads the `OptimizerStatsUsage`
  element out of the plans already in the Query Store and reports, per
  statistics object, how many plans and how many distinct queries loaded it,
  when it was last wanted, and the worst sampling and modification count any
  compile saw. Those last two are the point: they say a statistic was used
  while stale, which neither a usage flag nor `090.statistics` can say alone.
  The shredding happens on the instance and no plan XML travels, so the file is
  a few thousand short rows instead of gigabytes. It is the only Query Store
  collector with no `@discloses`, which is what lets it run on a client who
  refuses text disclosure, and the header states the rule the result has to be
  read by: presence proves use, absence proves nothing, so the list is a veto
  on a drop and never a list of things to drop. Design in
  `docs/statistics-usage-spec.md`.

  Three things came from running it rather than from reading the spec. The
  fragment is materialised into an `xml` column before any attribute is read,
  because applying `.value()` to an XML expression re-parses the whole plan
  once per attribute: 273 plans took 116 seconds that way and 3.3 seconds as a
  column. The scan is chunked a hundred plans at a time, because the first
  version held the shared QDS lock for the whole scan and was cancelled by this
  tool's own blocking watch after another session had waited 5.2 seconds for
  it. And catalog statistics are excluded, because they were 110 of the 113
  objects the first run named and none of them is something anyone can drop.

  Two external reviewers then ran against the finished branch with disjoint
  scopes, and three of their findings were reproduced and fixed here. The CI
  assertion tested that no emitted row carried a column named `text`; a
  reviewer added query text under a different alias and the archive shipped the
  workload's SQL with every check green, so the assertion now compares the
  whole key set of the root object and of each row against the declared one.
  The identity the file emits was not injective: showplan escapes an inner `]`
  by doubling it and only the outer pair was stripped, so a quoted statistic
  name did not join to `090.statistics`, and two tables whose schema and name
  differ only in where a dot falls were merged into one row, under-counting
  distinct statistics. The escape is undone and `schema` is emitted beside the
  concatenated `table`, which is what the grouping now uses. And `truncated`
  was set on the scan reaching the cap, which called a complete scan of exactly
  2 000 plans partial; the scan now asks for one plan more than it keeps and
  sets the flag on that plan existing. `plans_selected` and `plans_unparsed`
  join the root object, because a plan whose XML failed to parse used to lower
  `plans_examined` and leave no trace.

  The three steps section 6 requires are done, the third of them in the private
  repository: this collector now feeds the statistics facet of the report's
  object axis, beside `090` and `091`, with a fixture and a rendering test.

  The cap is 2 000 plans and the spec said 5 000. At the measured 12 to 17 ms
  per plan the larger number is over a minute of the client's CPU per database,
  and covering the biggest store on record is not what the file is for: the
  scan is ordered by recency because recent use is the better guard on a drop,
  so the plans that answer the question are the ones at the top of the order.
  `truncated` says when the cap bit.

- `10.system/014.cpu-topology.sql` reports the edition's compute ceiling, in
  six new fields under `processors`: `schedulers_offline`, `affinity_type`,
  `edition_socket_limit`, `edition_core_limit`, `edition_cap_signature` and
  `cpus_lost_at_least`. An edition licensed for fewer processors than the
  machine carries makes every other count in that file read as something it is
  not, and an analysis layer computing a MAXDOP from `cpu_count` then
  recommends a degree of parallelism over schedulers that do not exist.

  The signature is a string and it is a veto rather than a verdict. A positive
  value proves a ceiling; `none_observed` proves nothing, because once a
  ceiling is applied the instance stops reporting the size of the host it was
  cut down from, and on one capped instance measured for this change
  `socket_count` came back as 0. It is a string and not a flag because the
  actionable part is which of the three readings fired, which a boolean would
  erase.

  The two ceilings do not look alike, and that is what made the first version
  of this work wrong. A socket ceiling leaves surplus schedulers in
  `VISIBLE OFFLINE`, so `scheduler_count` falls below `cpu_count`. A logical
  processor ceiling applies before SQLOS starts, so `cpu_count` is already the
  granted number and no scheduler is offline at all; a test on
  `scheduler_count < cpu_count` never sees it, and `hyperthread_ratio`
  exceeding `cpu_count` is what gives it away. Both were measured on disposable
  instances before the table was written.

  The per-edition limits come from two Microsoft pages, and the header names
  which rows come from which, because they are not one source: the compute
  capacity limits page carries four editions only, and the Web, Business
  Intelligence and Express with Advanced Services rows come from older
  "Editions and supported features" pages. A null limit means the operating
  system maximum and never "unknown", which is what `unknown_edition` is for.
  Continuous integration cannot exercise the table, since one instance knows
  one edition, and the CI file says so rather than implying otherwise.

### Changed

- `10.system/014.cpu-topology.sql` computes `numa.maxdop_guidance` from the
  nodes rather than from `cpu_count`, and applies the table in force since SQL
  Server 2016 rather than the one that ended with 2014. This is a change of
  contract, and the field had no consumer in either repository when it was
  made.

  Two defects, found in that order. Computing on `cpu_count` recommended a
  parallelism over processors a capped instance is not allowed to use, which is
  the whole reason the fields above exist. It is now the smallest
  `online_scheduler_count` among the non-DAC nodes that carry a scheduler, the
  minimum and not the maximum because asymmetric nodes of four and eight would
  otherwise be handed eight, twice what the smaller node can hold.

  Then the cap itself was wrong. Eight is the SQL Server 2008 to 2014 rule. The
  current table has four rows and turns on the node count as well as the node
  size, and the sentence that decides how to read it sits under the table:
  NUMA node there means the soft-NUMA node, not the hardware node. That
  distinction is this file's own subject, and it is not cosmetic. On the
  instance this was measured on, reading hardware nodes gives one node of 22
  and a guidance of 8, while reading SQLOS nodes gives two of 11 and a guidance
  of 11. `numa.maxdop_guidance_basis` names which of the four rows answered, so
  a guidance of 8 is never ambiguous between the single-node cap and a node
  that simply holds eight schedulers.

## [0.32.0] - 2026-09-20

The archive could say the optimizer asked for an index four thousand times and
not whether that was one nightly report or four hundred statements. It can now.

### Added

- `70.schema/021.missing-index-queries.sql` says which queries wanted each
  missing-index suggestion, from the view SQL Server 2019 added. The archive
  carried the suggestions and nothing about what asked for them, so a report
  could not tell one nightly job from four hundred statements. It publishes
  the query hash and the plan hash, as two collectors already do in every
  default archive, and never a statement handle: the view carries one that
  returns the statement with its literals, and a test refuses it by name and
  refuses a star expansion with it. The cap is twenty queries per suggestion
  rather than a single global list, because a global list drops whole
  suggestions and keeps the loudest statements, which answers the file's own
  question in the wrong direction; each row says how many queries its
  suggestion has in all. Design in `docs/missing-index-queries-spec.md`.

## [0.31.2] - 2026-09-20

Two collectors stopped writing down a number nobody measured. Both came out
of a verification pass against instances with unusual collations, and neither
needs an unusual instance to matter: any silent failure of a deferred read
produced them.

### Fixed

- `20.databases/023.log-vlf.sql` counted an empty staging table when its
  deferred read came back with nothing, and wrote `space.vlf_count` 0 and
  `space.log_file_count` 0 into its root. A transaction log always has virtual
  log files, so zero was not a possible measurement. Those fields are null
  now, beside a `source` of `none`, which the file already had a word for.
- `10.system/043.cpu-neighbours.sql` fell back to `platform` `Windows`
  whenever its deferred read returned nothing. The deduction holds only where
  `sys.dm_os_host_info` does not exist, which is below SQL Server 2017; where
  the view is there and the read merely failed, the file published a memory
  residue computed from a false premise with `residue_computed` set to 1. The
  fallback now applies only where the view is absent, the platform is null
  otherwise, and the residue goes with it.

The measurements, including the state that produced the silent failure and the
design withdrawn once it was understood, are in
`docs/verification-binary-collation.md` and
`docs/dynamic-sql-probe-spec.md`.

## [0.31.1] - 2026-09-20

A guard that could never fire, found by a reader attacking yesterday's
measurements rather than the code. Nothing collected was wrong; a count that
was always zero said a database had no statistic without a histogram when it
had six.

### Fixed

- `70.schema/091.statistics-density.sql` could never report a statistic
  without a histogram. `counts.without_histogram` tested `steps IS NULL`,
  while the histogram is read through an `OUTER APPLY` over a scalar
  aggregate, which always returns a row: the count was always zero, and such a
  statistic still produced a row with a name, a leading column and a null
  density. An ordering rule testing for the row rather than for the density
  would have ordered on nothing.

### Changed

- The 0.31.0 entry below claimed `091.statistics-density` was the cheapest
  collector of the `space` profile in time. That was measured on one shape
  only: on a database with 4,600 statistics it runs behind nine of them. What
  holds is that its cost is of the same order as the profile's other
  collectors, and that it grows with the number of statistics rather than with
  the data. `docs/missing-index-suggestions-spec.md` carries both measurements.

## [0.31.0] - 2026-09-20

A profile change, and the measurements behind it. The `space` profile could
name the columns an index should be built on and not the order to put them in,
because the density that settles the order was collected only by a full run.
The collector that reads it is now part of the profile, and
`docs/missing-index-suggestions-spec.md` answers the four questions it had
left open, each against a live instance rather than by argument.

### Changed

- `70.schema/091.statistics-density.sql` joins the `space` profile. The index
  ordering rule needs a density for each candidate column, and the statistic
  it reads is the one the optimizer auto-creates for the column it filtered
  on: on a workload of 793 distinct query shapes, all twelve columns any
  suggestion named had a statistic leading on them. Measured on a database of
  200 tables and 800 statistics, it is the cheapest collector of the profile
  in time, at 158 to 176 ms.

## [0.30.0] - 2026-09-20

An audit that holds up production leaves a record of it. The blocking watch
already cancelled a collector someone had waited five seconds on; what it saw
ended as a sentence in a warning, so a run that held up three deployments
counted them and said nothing else. Every wait is now a record in the run
file, and the manifest also says where the run's own time went.

### Added

- The run file records what the collection held up. The blocking watch already
  saw the sessions waiting on a collector; `blocking_watch.waits` now carries
  one record each, with the collector, the database, the duration, the wait
  type, the resource, whether the collector was cancelled, how many sessions
  waited and what the longest one calls itself. `MANIFEST.txt` lists them, and
  says so in its disclosure paragraph when a program name is there. No login
  name, host name or statement text: those stay behind
  `--include-session-text`. Design in `docs/blocking-record-spec.md`.
- `MANIFEST.txt` names the five slowest collectors, failures included. A unit
  that spends its timeout and then fails writes no result, so the error entry
  in `_run.json` carries its duration now.

## [0.29.0] - 2026-09-20

Two collectors, both for questions an audit is asked after the fact. "The
application gets timeouts" left nothing on the server to read: the Query Store
does keep the interrupted executions, and one collector now ranks them. And the
performance counters a DBA would open Performance Monitor for reached no
archive beyond the handful six collectors already picked out; another now
carries the rest of that view.

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
