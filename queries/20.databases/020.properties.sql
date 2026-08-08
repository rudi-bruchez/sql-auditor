-- @scope:       database
-- @resultsets:  root:object, backups:object, files:array, largest_objects:array, unused_indexes:array, missing_indexes:array, fragmentation:array
-- @permissions: CONNECT, VIEW SERVER STATE, VIEW ANY DEFINITION, MSDB READ
-- @timeout:     300
--
-- Runs once per user database, with the connection context switched to it.
--
-- SQL Server 2012 is the floor. Removed for that reason:
--   sys.database_scoped_configurations   (2016) — whole result set
--   sys.database_query_store_options     (2016) — whole result set
--   sys.dm_db_log_info                   (2016 SP2) — space.vlf_count
--   sys.databases.is_query_store_on      (2016)
--   sys.databases.delayed_durability_desc              (2014)
--   sys.databases.is_auto_create_stats_incremental_on  (2014)
-- containment_desc and target_recovery_time_in_seconds are both 2012, kept.
-- sys.dm_db_log_space_usage is 2012; log_space_in_bytes_since_last_backup is
-- 2014 and is not projected.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT
    /* ───────── database identity & options ───────── */
    d.name                                                          AS [database.name],
    d.database_id                                                   AS [database.id],
    d.create_date                                                   AS [database.create_date],
    SUSER_SNAME(d.owner_sid)                                        AS [database.owner],
    d.compatibility_level                                           AS [database.compatibility_level],
    d.collation_name                                                AS [database.collation],
    d.state_desc                                                    AS [database.state],
    d.user_access_desc                                              AS [database.user_access],
    d.recovery_model_desc                                           AS [database.recovery_model],
    d.page_verify_option_desc                                       AS [database.page_verify],
    d.log_reuse_wait_desc                                           AS [database.log_reuse_wait],
    d.containment_desc                                              AS [database.containment],
    d.target_recovery_time_in_seconds                               AS [database.target_recovery_time_sec],
    d.snapshot_isolation_state_desc                                 AS [database.snapshot_isolation],
    CAST(d.is_read_committed_snapshot_on   AS BIT)                  AS [database.rcsi_enabled],
    CAST(d.is_auto_create_stats_on         AS BIT)                  AS [database.auto_create_stats],
    CAST(d.is_auto_update_stats_on         AS BIT)                  AS [database.auto_update_stats],
    CAST(d.is_auto_update_stats_async_on   AS BIT)                  AS [database.auto_update_stats_async],
    CAST(d.is_auto_close_on                AS BIT)                  AS [database.auto_close],
    CAST(d.is_auto_shrink_on               AS BIT)                  AS [database.auto_shrink],
    CAST(d.is_read_only                    AS BIT)                  AS [database.read_only],
    CAST(d.is_encrypted                    AS BIT)                  AS [database.tde_encrypted],
    CAST(d.is_trustworthy_on               AS BIT)                  AS [database.trustworthy],
    CAST(d.is_broker_enabled               AS BIT)                  AS [database.broker_enabled],
    CAST(d.is_db_chaining_on               AS BIT)                  AS [database.cross_db_chaining],

    /* ───────── space summary ───────── */
    (SELECT CAST(SUM(size) * 8 / 1024.0 AS DECIMAL(14,1))
       FROM sys.database_files WHERE type = 0)                      AS [space.data_allocated_mb],
    (SELECT CAST(SUM(CAST(FILEPROPERTY(name,'SpaceUsed') AS BIGINT)) * 8 / 1024.0 AS DECIMAL(14,1))
       FROM sys.database_files WHERE type = 0)                      AS [space.data_used_mb],
    CAST(ls.total_log_size_in_bytes / 1048576.0 AS DECIMAL(14,1))   AS [space.log_size_mb],
    CAST(ls.used_log_space_in_bytes / 1048576.0 AS DECIMAL(14,1))   AS [space.log_used_mb],
    CAST(ls.used_log_space_in_percent AS DECIMAL(5,2))              AS [space.log_used_pct]
FROM       sys.databases             AS d
CROSS JOIN sys.dm_db_log_space_usage AS ls
WHERE d.database_id = DB_ID()
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── backups: last backup of each type, from msdb ───────── */
SELECT
    (SELECT MAX(backup_finish_date) FROM msdb.dbo.backupset
      WHERE database_name = DB_NAME() AND type = 'D')      AS last_full,
    (SELECT MAX(backup_finish_date) FROM msdb.dbo.backupset
      WHERE database_name = DB_NAME() AND type = 'I')      AS last_differential,
    (SELECT MAX(backup_finish_date) FROM msdb.dbo.backupset
      WHERE database_name = DB_NAME() AND type = 'L')      AS last_log
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── files ───────── */
SELECT df.name,
       df.type_desc                              AS type,
       df.physical_name,
       df.state_desc                             AS state,
       CAST(df.size * 8 / 1024.0 AS DECIMAL(14,1))                           AS size_mb,
       CAST(FILEPROPERTY(df.name,'SpaceUsed') * 8 / 1024.0 AS DECIMAL(14,1)) AS used_mb,
       CASE WHEN df.max_size = -1 THEN 'unlimited'
            WHEN df.max_size = 268435456 THEN 'log_2tb'
            ELSE CAST(CAST(df.max_size * 8 / 1024.0 AS DECIMAL(14,1)) AS varchar(20)) END AS max_mb,
       CAST(df.is_percent_growth AS BIT)         AS percent_growth,
       CASE WHEN df.is_percent_growth = 1 THEN CONCAT(df.growth, ' %')
            ELSE CONCAT(CAST(df.growth * 8 / 1024.0 AS DECIMAL(14,1)), ' MB') END AS growth
FROM sys.database_files AS df
ORDER BY df.type, df.file_id
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── largest_objects: top 20 by reserved size ───────── */
SELECT TOP (20)
       SCHEMA_NAME(t.schema_id) + '.' + t.name              AS [table],
       CASE WHEN i.index_id IN (0,1) THEN i.type_desc ELSE 'HEAP/CLUSTERED' END AS storage,
       SUM(ps.row_count)                                    AS [rows],
       CAST(SUM(ps.reserved_page_count) * 8 / 1024.0 AS DECIMAL(14,1)) AS reserved_mb,
       CAST(SUM(ps.used_page_count)     * 8 / 1024.0 AS DECIMAL(14,1)) AS used_mb
FROM sys.dm_db_partition_stats AS ps
JOIN sys.tables  AS t ON t.object_id = ps.object_id
JOIN sys.indexes AS i ON i.object_id = ps.object_id AND i.index_id = ps.index_id
WHERE i.index_id IN (0,1)
GROUP BY t.schema_id, t.name, i.type_desc, i.index_id
ORDER BY SUM(ps.reserved_page_count) DESC
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── unused_indexes: write-only nonclustered indexes ───────── */
SELECT TOP (25)
       SCHEMA_NAME(o.schema_id) + '.' + o.name              AS [table],
       i.name                                               AS index_name,
       us.user_seeks, us.user_scans, us.user_lookups,
       us.user_updates                                      AS writes,
       us.last_user_seek, us.last_user_scan
FROM sys.indexes AS i
JOIN sys.objects AS o ON o.object_id = i.object_id AND o.type = 'U'
LEFT JOIN sys.dm_db_index_usage_stats AS us
       ON us.object_id = i.object_id AND us.index_id = i.index_id
      AND us.database_id = DB_ID()
WHERE i.type_desc = 'NONCLUSTERED' AND i.is_primary_key = 0 AND i.is_unique_constraint = 0
  AND ISNULL(us.user_seeks,0) + ISNULL(us.user_scans,0) + ISNULL(us.user_lookups,0) = 0
ORDER BY ISNULL(us.user_updates,0) DESC
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── missing_indexes: optimizer suggestions ───────── */
SELECT TOP (25)
       OBJECT_SCHEMA_NAME(mid.object_id, mid.database_id) + '.'
     + OBJECT_NAME(mid.object_id, mid.database_id)          AS [table],
       CAST(migs.avg_total_user_cost * migs.avg_user_impact * (migs.user_seeks + migs.user_scans) AS DECIMAL(18,2)) AS impact_score,
       migs.user_seeks + migs.user_scans                    AS uses,
       CAST(migs.avg_user_impact AS DECIMAL(5,1))           AS avg_impact_pct,
       mid.equality_columns, mid.inequality_columns, mid.included_columns
FROM sys.dm_db_missing_index_group_stats AS migs
JOIN sys.dm_db_missing_index_groups AS mig ON mig.index_group_handle = migs.group_handle
JOIN sys.dm_db_missing_index_details AS mid ON mid.index_handle = mig.index_handle
WHERE mid.database_id = DB_ID()
ORDER BY migs.avg_total_user_cost * migs.avg_user_impact * (migs.user_seeks + migs.user_scans) DESC
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── fragmentation: LIMITED mode, page_count > 1000 ─────────
   COSTLY on very large databases — remove this block if needed. */
SELECT TOP (25)
       OBJECT_SCHEMA_NAME(ips.object_id) + '.' + OBJECT_NAME(ips.object_id) AS [table],
       i.name                                               AS index_name,
       ips.index_type_desc                                  AS index_type,
       ips.partition_number,
       ips.page_count,
       CAST(ips.avg_fragmentation_in_percent AS DECIMAL(5,2)) AS fragmentation_pct
FROM sys.dm_db_index_physical_stats(DB_ID(), NULL, NULL, NULL, 'LIMITED') AS ips
JOIN sys.indexes AS i ON i.object_id = ips.object_id AND i.index_id = ips.index_id
WHERE ips.page_count > 1000 AND ips.avg_fragmentation_in_percent > 10
ORDER BY ips.avg_fragmentation_in_percent DESC
OPTION (RECOMPILE, MAXDOP 1);
