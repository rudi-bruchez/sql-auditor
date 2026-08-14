-- @scope:       instance
-- @resultsets:  root:object, trace_flags:array, space:object, version_store:object, oldest_snapshot_transactions:array, files:array, file_io:array, perf_counters:array, live_page_contention:array, instance_latch_waits:array
-- @permissions: VIEW SERVER STATE, VIEW ANY DEFINITION
-- @timeout:     60
--
-- tempdb configuration, space, version store and allocation contention.
--
-- The batch switches to tempdb because sys.dm_db_file_space_usage,
-- sys.database_files and FILEPROPERTY all report on the current database and
-- have no cross-database form on SQL Server 2012.
--
-- Raw facts only: the per-file rows of the files result set replace the four
-- collapsed booleans the earlier version derived (equal size, equal growth,
-- any percent growth, same volume), and recommended_start_files is gone — that
-- threshold is a judgement and belongs to the analysis layer, not the
-- collector.
--
-- There is one per-file result set, not two. An earlier version shipped
-- tempdb_files beside files: the two asked the same question of the same
-- catalog view and answered it differently (raw growth pages against a
-- formatted string, a drive-letter prefix against a real mount point), so a
-- reader had no way to tell which one to believe. files is the survivor
-- because it derives the volume from sys.dm_os_volume_stats. LEFT(physical_name, 3)
-- is a Windows drive-letter heuristic and yields "/va" for every default path
-- on Linux, which makes every file look co-located whatever the layout.
--
-- Session statement text is NOT collected here. It lives in
-- 052.session-text.sql behind --include-session-text.
--
-- SQL Server 2012 is the floor. Removed for that reason:
--   SERVERPROPERTY('IsTempdbMetadataMemoryOptimized')      (2019)
--   sys.dm_tran_version_store_space_usage                  (2016 SP2)

USE tempdb;

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

/* Trace flags cannot be produced by a SELECT — capture them into a table
   variable first. INSERT ... EXEC swallows the DBCC rowset, so this emits
   no result set of its own. */
DECLARE @tf TABLE (TraceFlag int, Status int, [Global] int, [Session] int);
INSERT INTO @tf EXEC ('DBCC TRACESTATUS(-1) WITH NO_INFOMSGS');

DECLARE @cpus int;
SELECT @cpus = COUNT(*)
FROM sys.dm_os_schedulers
WHERE status = 'VISIBLE ONLINE' AND scheduler_id < 1048576
OPTION (RECOMPILE, MAXDOP 1);

SELECT
    /* ───────── instance context ───────── */
    CONVERT(sysname,       SERVERPROPERTY('ServerName'))            AS [instance.instance_name],
    CONVERT(nvarchar(128), SERVERPROPERTY('ProductVersion'))        AS [instance.version],
    CONVERT(nvarchar(128), SERVERPROPERTY('Edition'))               AS [instance.edition],
    @cpus                                                           AS [instance.online_cpus],
    (SELECT recovery_model_desc FROM sys.databases WHERE database_id = 2) AS [instance.tempdb_recovery_model],
    (SELECT collation_name      FROM sys.databases WHERE database_id = 2) AS [instance.tempdb_collation],

    /* ───────── tempdb file counts (raw) ───────── */
    (SELECT COUNT(*) FROM sys.master_files WHERE database_id = 2 AND type = 0) AS [tempdb.data_file_count],
    (SELECT COUNT(*) FROM sys.master_files WHERE database_id = 2 AND type = 1) AS [tempdb.log_file_count]
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── trace_flags ───────── */
SELECT TraceFlag AS flag, [Global] AS global_on, [Session] AS session_on
FROM @tf
ORDER BY TraceFlag
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── space: usage / allocation breakdown ─────────
   Version-store / user / internal columns are populated only in tempdb
   context, which is why the batch runs USE tempdb. */
SELECT
    CAST(SUM(total_page_count)              * 8 / 1024.0 AS DECIMAL(14,1)) AS total_mb,
    CAST(SUM(allocated_extent_page_count)   * 8 / 1024.0 AS DECIMAL(14,1)) AS allocated_mb,
    CAST(SUM(unallocated_extent_page_count) * 8 / 1024.0 AS DECIMAL(14,1)) AS free_mb,
    CAST(SUM(version_store_reserved_page_count)   * 8 / 1024.0 AS DECIMAL(14,1)) AS version_store_mb,
    CAST(SUM(user_object_reserved_page_count)     * 8 / 1024.0 AS DECIMAL(14,1)) AS user_objects_mb,
    CAST(SUM(internal_object_reserved_page_count) * 8 / 1024.0 AS DECIMAL(14,1)) AS internal_objects_mb,
    CAST(SUM(mixed_extent_page_count)       * 8 / 1024.0 AS DECIMAL(14,1)) AS mixed_extent_mb
FROM sys.dm_db_file_space_usage
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── version_store ─────────
   The per-database reserved size that used to sit here came from
   sys.dm_tran_version_store_space_usage, which is SQL Server 2016 SP2 and
   later. The performance counters below cover the same ground on 2012. */
SELECT
    (SELECT cntr_value FROM sys.dm_os_performance_counters
      WHERE counter_name = 'Version Store Size (KB)' AND object_name LIKE '%Transactions%') AS size_kb_counter,
    (SELECT cntr_value FROM sys.dm_os_performance_counters
      WHERE counter_name = 'Longest Transaction Running Time' AND object_name LIKE '%Transactions%') AS longest_txn_running_sec,
    (SELECT COUNT(*) FROM sys.dm_tran_active_snapshot_database_transactions) AS active_snapshot_txn_count
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── oldest_snapshot_transactions: blocking version-store cleanup ─────────
   Identifiers and durations only. The statement text of these sessions, and
   the login, host and program names behind them, moved to
   052.session-text.sql, which runs only under --include-session-text: that
   text is the verbatim SQL of live batches and can carry literals copied out
   of application tables. Which of the two ran is what MANIFEST.txt's
   disclosure paragraph is written from, so the columns must not drift back
   here. session_id still joins the rows in both files. */
SELECT TOP (5)
       ast.session_id,
       ast.transaction_sequence_num,
       ast.elapsed_time_seconds
FROM sys.dm_tran_active_snapshot_database_transactions AS ast
ORDER BY ast.elapsed_time_seconds DESC
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── files: config, used space, volume and volume free ─────────
   One row per file, so the analysis layer can say which file differs and by
   how much instead of only that they differ. volume comes from
   sys.dm_os_volume_stats (2008 R2 and later, inside the 2012 floor), so
   co-location can be derived from it rather than guessed at.

   Measured on SQL Server 2022 for Linux, volume_mount_point comes back NULL
   while total_bytes and available_bytes are populated. NULL is the correct
   answer there: the analysis layer reads it as "the mount point is not known
   on this platform" and declines to judge, where the drive-letter prefix this
   column replaced returned "/va" for every default path and made every file
   look co-located whatever the layout. */
SELECT
    mf.file_id,
    mf.name,
    mf.type_desc                                            AS type,
    mf.physical_name,
    df.state_desc                                           AS state,
    -- Current size from sys.database_files, configured size from
    -- sys.master_files, and both are projected because for tempdb they answer
    -- different questions. tempdb is recreated at every startup at the size
    -- master_files holds, then grows under load; database_files is what it has
    -- grown to. Reading master_files alone reported eight 8 MB files on an
    -- instance whose used_mb was 11.1 — a file cannot hold more than its size,
    -- and that impossibility is what gave the defect away.
    CAST(CAST(COALESCE(df.size, mf.size) AS BIGINT) * 8 / 1024.0 AS DECIMAL(14,1)) AS size_mb,
    CAST(CAST(mf.size AS BIGINT) * 8 / 1024.0 AS DECIMAL(14,1)) AS configured_size_mb,
    CAST(CAST(FILEPROPERTY(mf.name,'SpaceUsed') AS BIGINT) * 8 / 1024.0 AS DECIMAL(14,1)) AS used_mb,
    CASE WHEN mf.max_size = -1 THEN 'unlimited'
         WHEN mf.max_size = 268435456 THEN 'log_2tb'
         ELSE CAST(CAST(CAST(mf.max_size AS BIGINT) * 8 / 1024.0 AS DECIMAL(14,1)) AS varchar(20)) END AS max_mb,
    CAST(mf.is_percent_growth AS BIT)                       AS percent_growth,
    CASE WHEN mf.is_percent_growth = 1 THEN CONCAT(mf.growth, ' %')
         ELSE CONCAT(CAST(CAST(mf.growth AS BIGINT) * 8 / 1024.0 AS DECIMAL(14,1)), ' MB') END AS growth,
    vs.volume_mount_point                                   AS volume,
    CAST(vs.total_bytes     / 1073741824.0 AS DECIMAL(14,1)) AS volume_total_gb,
    CAST(vs.available_bytes / 1073741824.0 AS DECIMAL(14,1)) AS volume_free_gb
FROM sys.master_files AS mf
LEFT JOIN sys.database_files AS df ON df.file_id = mf.file_id
OUTER APPLY sys.dm_os_volume_stats(2, mf.file_id) AS vs
WHERE mf.database_id = 2
ORDER BY mf.type, mf.file_id
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── file_io: per-file I/O and latency, cumulative since restart ───────── */
SELECT
    mf.name,
    mf.type_desc                                            AS type,
    fs.num_of_reads, fs.num_of_writes,
    CAST(fs.num_of_bytes_read  / 1048576.0 AS DECIMAL(14,1)) AS mb_read,
    CAST(fs.num_of_bytes_written / 1048576.0 AS DECIMAL(14,1)) AS mb_written,
    CAST(fs.io_stall_read_ms  * 1.0 / NULLIF(fs.num_of_reads,0)  AS DECIMAL(10,1)) AS avg_read_latency_ms,
    CAST(fs.io_stall_write_ms * 1.0 / NULLIF(fs.num_of_writes,0) AS DECIMAL(10,1)) AS avg_write_latency_ms
FROM sys.dm_io_virtual_file_stats(2, NULL) AS fs
JOIN sys.master_files AS mf ON mf.database_id = 2 AND mf.file_id = fs.file_id
ORDER BY mf.type, mf.file_id
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── perf_counters: tempdb-related ───────── */
SELECT RTRIM(counter_name) AS counter, cntr_value AS value
FROM sys.dm_os_performance_counters
WHERE (object_name LIKE '%Transactions%' AND counter_name IN
          ('Free Space in tempdb (KB)','Version Store Size (KB)','Version Store unit count',
           'Version Generation rate (KB/s)','Version Cleanup rate (KB/s)',
           'Longest Transaction Running Time','Snapshot Transactions','Update Snapshot Transactions',
           'NonSnapshot Version Transactions'))
   OR (object_name LIKE '%General Statistics%' AND counter_name IN
          ('Active Temp Tables','Temp Tables Creation Rate','Temp Tables For Destruction'))
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── live_page_contention: PFS/GAM/SGAM ─────────
   Snapshot at query time — usually empty unless contention is occurring now. */
SELECT
    wt.session_id,
    wt.wait_type,
    wt.wait_duration_ms,
    wt.resource_description,
    p.pg AS page_id,
    CASE
        WHEN p.pg = 1 OR (p.pg - 1) % 8088   = 0 THEN 'PFS'
        WHEN p.pg = 2 OR (p.pg - 2) % 511232 = 0 THEN 'GAM'
        WHEN p.pg = 3 OR (p.pg - 3) % 511232 = 0 THEN 'SGAM'
        ELSE 'other' END AS page_type
FROM sys.dm_os_waiting_tasks AS wt
CROSS APPLY (VALUES (TRY_CONVERT(BIGINT,
             PARSENAME(REPLACE(wt.resource_description, ':', '.'), 1)))) AS p(pg)
WHERE wt.wait_type LIKE 'PAGELATCH%'
  AND wt.resource_description LIKE '2:%'
  AND p.pg IS NOT NULL
ORDER BY wt.wait_duration_ms DESC
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── instance_latch_waits: context, NOT tempdb-attributable ───────── */
SELECT wait_type,
       waiting_tasks_count                                                       AS tasks,
       wait_time_ms / 1000                                                       AS wait_sec,
       CAST(wait_time_ms * 1.0 / NULLIF(waiting_tasks_count,0) AS DECIMAL(10,1)) AS avg_ms
FROM sys.dm_os_wait_stats
WHERE wait_type IN ('PAGELATCH_UP','PAGELATCH_EX','PAGELATCH_SH','PAGELATCH_KP',
                    'PAGEIOLATCH_UP','PAGEIOLATCH_EX','PAGEIOLATCH_SH')
  AND waiting_tasks_count > 0
ORDER BY wait_time_ms DESC
OPTION (RECOMPILE, MAXDOP 1);
