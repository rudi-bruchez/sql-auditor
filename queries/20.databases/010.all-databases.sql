-- @scope:       instance
-- @resultsets:  databases:array
-- @permissions: VIEW ANY DEFINITION, MSDB READ
-- @timeout:     60
--
-- One row per user database: options, file counts and sizes, and the raw
-- backup timestamps.
--
-- There is no root result set here: this collector projects a list, not a
-- property bag, and root must be a single-row object. The whole list is the
-- document's "databases" array.
--
-- No judgement is computed. The backup_flag CASE that used to sit at the end
-- of the SELECT list — with its hard-coded seven-day threshold — is gone;
-- last_full, last_diff and last_log are projected raw and the analysis layer
-- decides what "stale" means.
--
-- log_used_pct is gone too. It came from an OUTER APPLY over
-- sys.dm_db_log_space_usage guarded by WHERE 1 = 0, because that view reports
-- only on the current database; the column was permanently NULL. Log space is
-- collected per database in 20.databases/020.properties.sql.
--
-- SQL Server 2012 is the floor. Removed for that reason:
--   sys.databases.is_auto_create_stats_incremental_on  (2014)
--   sys.databases.delayed_durability_desc              (2014)
--   sys.databases.is_query_store_on                    (2016)
-- containment_desc and target_recovery_time_in_seconds are both 2012, kept.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT
    d.database_id                                                AS [db_id],
    d.name                                                       AS [database],
    d.state_desc                                                 AS [state],
    d.recovery_model_desc                                        AS [recovery_model],
    d.compatibility_level                                        AS [compat_level],
    d.user_access_desc                                           AS [user_access],
    d.is_read_only                                               AS [read_only],
    d.is_auto_close_on                                           AS [auto_close],
    d.is_auto_shrink_on                                          AS [auto_shrink],
    d.is_auto_create_stats_on                                    AS [auto_create_stats],
    d.is_auto_update_stats_on                                    AS [auto_update_stats],
    d.is_auto_update_stats_async_on                              AS [auto_update_stats_async],
    d.page_verify_option_desc                                    AS [page_verify],
    d.is_read_committed_snapshot_on                              AS [rcsi],
    d.snapshot_isolation_state_desc                              AS [snapshot_isolation],
    d.is_encrypted                                               AS [tde],
    d.is_trustworthy_on                                          AS [trustworthy],
    d.is_broker_enabled                                          AS [broker],
    d.is_db_chaining_on                                          AS [cross_db_chaining],
    d.target_recovery_time_in_seconds                            AS [target_recovery_sec],
    d.containment_desc                                           AS [containment],
    d.log_reuse_wait_desc                                        AS [log_reuse_wait],
    d.collation_name                                             AS [collation],
    SUSER_SNAME(d.owner_sid)                                     AS [owner],
    d.create_date                                                AS [create_date],
    ds.data_files, ds.data_mb,
    ls.log_files, ls.log_mb,
    bk.last_full, bk.last_diff, bk.last_log
FROM sys.databases AS d
OUTER APPLY (
    SELECT COUNT(*) AS data_files,
           CAST(SUM(mf.size) * 8 / 1024.0 AS DECIMAL(14,1)) AS data_mb
    FROM sys.master_files AS mf
    WHERE mf.database_id = d.database_id AND mf.type = 0
) AS ds
OUTER APPLY (
    SELECT COUNT(*) AS log_files,
           CAST(SUM(mf.size) * 8 / 1024.0 AS DECIMAL(14,1)) AS log_mb
    FROM sys.master_files AS mf
    WHERE mf.database_id = d.database_id AND mf.type = 1
) AS ls
OUTER APPLY (
    SELECT
        MAX(CASE WHEN bs.type = 'D' THEN bs.backup_finish_date END) AS last_full,
        MAX(CASE WHEN bs.type = 'I' THEN bs.backup_finish_date END) AS last_diff,
        MAX(CASE WHEN bs.type = 'L' THEN bs.backup_finish_date END) AS last_log
    FROM msdb.dbo.backupset AS bs
    WHERE bs.database_name = d.name
) AS bk
WHERE d.database_id > 4                   -- exclude system DBs; remove to include them
ORDER BY d.name
OPTION (RECOMPILE, MAXDOP 1);
