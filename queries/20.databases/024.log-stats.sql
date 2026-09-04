-- @scope:       database
-- @resultsets:  root:object
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     30
-- @min_version: 13.0.5026
--
-- How much of this database's transaction log is live, and what is holding it.
--
-- Why this collector exists: 010.all-databases.sql already projects
-- log_reuse_wait_desc, so an archive can say a log is held by OLDEST_PAGE or
-- LOG_BACKUP. It cannot say by how much. "The indirect checkpoint is behind"
-- and "the indirect checkpoint is 9 GB behind" lead to different decisions,
-- and only the second is actionable.
--
-- log_recovery_size_mb is the column that carries its weight here: it is how
-- much log a crash recovery would have to replay, which is the real measure of
-- how far behind the checkpoint has fallen. On an instance whose MessageBox
-- log had grown to seven times its data size with log_reuse_wait = OLDEST_PAGE,
-- nothing in the archive could put a number on it.
--
-- log_since_last_log_backup_mb answers the neighbouring question — whether the
-- log backup interval matches how fast the log is written — which today has to
-- be inferred from file sizes and backup timestamps in two different files.
--
-- NO JUDGEMENT IS APPLIED. A large active log is not a defect: a long-running
-- transaction that is supposed to be long-running produces one. What the
-- numbers mean depends on the recovery objective and on what the application
-- is doing, neither of which is in this archive.
--
-- WHY THE 2016 SP2 FLOOR. sys.dm_db_log_stats arrived in SQL Server 2016 SP2
-- (13.0.5026). Below it the same questions need DBCC LOGINFO and
-- DBCC SQLPERF(LOGSPACE), which are undocumented, take different shapes across
-- versions, and cannot be joined; 023.log-vlf.sql already carries what is
-- reachable there. This collector is skipped with its version reason recorded
-- rather than degraded into something that answers a narrower question under
-- the same name.
--
-- ON AN AVAILABILITY GROUP SECONDARY THIS RETURNS ALMOST NOTHING. Microsoft
-- documents that against a secondary replica the function returns only
-- database_id, recovery_model and log_backup_time; every size and every LSN
-- comes back NULL. That is the documented behaviour and not a failure, but a
-- reader who meets a row of NULLs without knowing it will read them as a
-- broken collector. The replica role is not asked here — it belongs to a
-- high-availability collector that does not exist yet — so the note has to
-- carry it.
--
-- The VLF geometry is NOT repeated here — 023.log-vlf.sql owns it. Two files
-- reporting the same count is how they come to disagree.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT
    DB_NAME()                                                   AS [database],
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],

    CAST(ls.total_log_size_mb AS DECIMAL(18,1))                 AS [size.total_mb],
    CAST(ls.active_log_size_mb AS DECIMAL(18,1))                AS [size.active_mb],
    -- The share of the file that cannot be reused right now. A file that is
    -- large because it once needed to be reads very differently from one that
    -- is large because it still needs to be.
    CAST(CASE WHEN ls.total_log_size_mb > 0
              THEN ls.active_log_size_mb * 100.0 / ls.total_log_size_mb
         END AS DECIMAL(5,1))                                   AS [size.active_percent],

    -- What a crash recovery would replay. This is the indirect-checkpoint lag
    -- in megabytes, and the reason this collector exists.
    CAST(ls.log_recovery_size_mb AS DECIMAL(18,1))              AS [holdup.recovery_mb],
    CAST(ls.log_since_last_log_backup_mb AS DECIMAL(18,1))      AS [holdup.since_last_log_backup_mb],
    CAST(ls.log_since_last_checkpoint_mb AS DECIMAL(18,1))      AS [holdup.since_last_checkpoint_mb],
    CONVERT(varchar(23), ls.log_backup_time, 126)               AS [holdup.last_log_backup],
    -- The LSN the recovery would start from. Projected beside the megabytes
    -- because the two together say whether the holdup is one long transaction
    -- or a checkpoint that never catches up.
    ls.log_recovery_lsn                                         AS [holdup.recovery_lsn],
    ls.log_checkpoint_lsn                                       AS [holdup.checkpoint_lsn],
    -- Repeated from 010.all-databases.sql on purpose: the reason and the size
    -- of the holdup are one fact, and splitting them across two files is what
    -- made this hard to read in the first place.
    d.log_reuse_wait_desc                                       AS [holdup.reason],

    ls.recovery_model                                           AS [config.recovery_model],
    CAST(d.target_recovery_time_in_seconds AS int)              AS [config.target_recovery_seconds],
    ls.log_min_lsn                                              AS [config.log_min_lsn],
    ls.current_vlf_sequence_number                              AS [config.current_vlf_sequence],
    CAST(ls.log_truncation_holdup_reason AS nvarchar(60))       AS [config.truncation_holdup_reason],

    -- How many VLFs a crash recovery would have to walk. This is the startup
    -- and failover cost of the holdup above, and the reason a log held open is
    -- not only a disk-space question: recovery reads them one after another.
    ls.recovery_vlf_count                                       AS [churn.recovery_vlf_count],
    ls.total_vlf_count                                          AS [churn.vlf_count],
    ls.active_vlf_count                                         AS [churn.active_vlf_count],
    CAST(ls.current_vlf_size_mb AS DECIMAL(18,1))               AS [churn.current_vlf_mb],
    ls.log_end_lsn                                              AS [churn.log_end_lsn]
FROM sys.dm_db_log_stats(DB_ID()) AS ls
CROSS JOIN (SELECT log_reuse_wait_desc, target_recovery_time_in_seconds
              FROM sys.databases WHERE database_id = DB_ID()) AS d
OPTION (RECOMPILE, MAXDOP 1);
