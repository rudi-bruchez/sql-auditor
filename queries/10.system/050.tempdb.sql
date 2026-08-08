-- @scope:       instance
-- @resultsets:  root:object, trace_flags:array, space:object, version_store:object, oldest_snapshot_transactions:array, tempdb_files:array, files:array, file_io:array, perf_counters:array, live_page_contention:array, instance_latch_waits:array
-- @permissions: VIEW SERVER STATE, VIEW ANY DEFINITION
-- @timeout:     60
--
-- tempdb configuration, space, version store and allocation contention.
--
-- The batch switches to tempdb because sys.dm_db_file_space_usage,
-- sys.database_files and FILEPROPERTY all report on the current database and
-- have no cross-database form on SQL Server 2012.
--
-- Raw facts only: the per-file rows replace the four collapsed booleans the
-- earlier version derived (equal size, equal growth, any percent growth, same
-- volume), and recommended_start_files is gone — that threshold is a judgement
-- and belongs to the analysis layer, not the collector.
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

/* ───────── oldest_snapshot_transactions: blocking version-store cleanup ───────── */
SELECT TOP (5)
       ast.session_id,
       ast.transaction_sequence_num,
       ast.elapsed_time_seconds,
       es.login_name, es.host_name, es.program_name,
       est.text AS current_batch
FROM sys.dm_tran_active_snapshot_database_transactions AS ast
LEFT JOIN sys.dm_exec_sessions AS es ON es.session_id = ast.session_id
OUTER APPLY (
    SELECT r.sql_handle FROM sys.dm_exec_requests r WHERE r.session_id = ast.session_id
) AS req
OUTER APPLY sys.dm_exec_sql_text(req.sql_handle) AS est
ORDER BY ast.elapsed_time_seconds DESC
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── tempdb_files: size, growth and volume per file ─────────
   One row per file, so the analysis layer can say which file differs and by
   how much instead of only that they differ. */
SELECT
    mf.file_id                                        AS [file_id],
    mf.name                                           AS [logical_name],
    mf.physical_name                                  AS [physical_name],
    mf.type_desc                                      AS [type],
    CAST(mf.size * 8 / 1024.0 AS DECIMAL(14,1))       AS [size_mb],
    mf.growth                                         AS [growth],
    mf.is_percent_growth                              AS [is_percent_growth],
    LEFT(mf.physical_name, 3)                         AS [volume]
FROM sys.master_files AS mf
WHERE mf.database_id = 2
ORDER BY mf.type, mf.file_id
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── files: config + used + volume free ───────── */
SELECT
    mf.file_id,
    mf.name,
    mf.type_desc                                            AS type,
    mf.physical_name,
    df.state_desc                                           AS state,
    CAST(mf.size * 8 / 1024.0 AS DECIMAL(14,1))             AS size_mb,
    CAST(FILEPROPERTY(mf.name,'SpaceUsed') * 8 / 1024.0 AS DECIMAL(14,1)) AS used_mb,
    CASE WHEN mf.max_size = -1 THEN 'unlimited'
         WHEN mf.max_size = 268435456 THEN 'log_2tb'
         ELSE CAST(CAST(mf.max_size * 8 / 1024.0 AS DECIMAL(14,1)) AS varchar(20)) END AS max_mb,
    CAST(mf.is_percent_growth AS BIT)                       AS percent_growth,
    CASE WHEN mf.is_percent_growth = 1 THEN CONCAT(mf.growth, ' %')
         ELSE CONCAT(CAST(mf.growth * 8 / 1024.0 AS DECIMAL(14,1)), ' MB') END AS growth,
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
