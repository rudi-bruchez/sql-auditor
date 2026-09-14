-- @scope:       instance
-- @resultsets:  databases:array
-- @permissions: VIEW ANY DEFINITION, MSDB READ
-- @timeout:     60
-- @profiles:    space
--
-- One row per database, system ones included: options, file counts and
-- sizes, and the raw backup timestamps.
--
-- System databases are in scope on purpose. auto_shrink on model is copied to
-- every database created afterwards, and a collector that cannot see model
-- blinds the audit rules that read it. tempdb has no backups at all and
-- master never has a log backup, so their backup columns stay NULL — the
-- analysis layer must exclude them by name before judging staleness.
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
--
-- last_good_checkdb comes from DATABASEPROPERTYEX(db, 'LastGoodCheckDbTime'),
-- one line per database in this instance-scoped list rather than a
-- database-scoped collector: the per-database collectors never run against
-- master, model or msdb, which are precisely the databases whose integrity
-- history matters most and are covered here. The property answers under
-- VIEW SERVER STATE alone (verified on a minimally-granted login). It
-- renders 1900-01-01 for a database that never had a successful CHECKDB; the
-- analysis layer reads that as "never", not as a date. On SQL Server builds
-- older than 2016 SP2 the property is unknown and returns NULL — TRY_CAST
-- keeps the column silent rather than failing — so the first collection on a
-- 2012/2014 instance confirms the gap by itself.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

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
    CAST(d.is_parameterization_forced AS bit)                    AS [parameterization_forced],
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
    TRY_CAST(DATABASEPROPERTYEX(d.name, 'LastGoodCheckDbTime') AS datetime)
                                                                 AS [last_good_checkdb],
    ds.data_files, ds.data_mb,
    ls.log_files, ls.log_mb,
    bk.last_full, bk.last_diff, bk.last_log
FROM sys.databases AS d
OUTER APPLY (
    -- The cast to BIGINT is before the SUM and not after it. sys.master_files.size
    -- is an int page count, SUM over int returns int, and int * 8 overflows at
    -- 2.1 TB — 281 million pages times eight is 2.25 billion, past the 2.147
    -- billion ceiling. The whole statement then fails with "Arithmetic overflow
    -- converting expression to data type int", and since this collector is the
    -- one projecting compatibility level, page verify, auto-shrink, auto-close,
    -- RCSI, collation and owner, a single oversized database took all of them
    -- out of the archive for every database on the instance.
    SELECT COUNT(*) AS data_files,
           CAST(SUM(CAST(mf.size AS BIGINT)) * 8 / 1024.0 AS DECIMAL(14,1)) AS data_mb
    FROM sys.master_files AS mf
    WHERE mf.database_id = d.database_id AND mf.type = 0
) AS ds
OUTER APPLY (
    SELECT COUNT(*) AS log_files,
           CAST(SUM(CAST(mf.size AS BIGINT)) * 8 / 1024.0 AS DECIMAL(14,1)) AS log_mb
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
ORDER BY d.name
OPTION (RECOMPILE, MAXDOP 1);
