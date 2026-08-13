-- @scope:       instance
-- @resultsets:  root:object, per_database:array, devices:array, recent:array
-- @permissions: CONNECT, MSDB READ
-- @timeout:     120
--
-- The backup history, as msdb recorded it, over the last 30 days.
--
-- Why this collector exists: the corpus already reads msdb.dbo.backupset in
-- three places, but only ever as MAX(backup_finish_date) by type. That answers
-- "when was the last one" and nothing else. Every other question an audit asks
-- about backups — where do they go, how long do they take, are they actually
-- compressed, is there more than one chain, who runs them — needs the rows
-- rather than the maximum, and needed cross-referencing job schedules against
-- error-log timestamps by hand to guess at.
--
-- WHERE THEY GO IS A FINDING. physical_device_name is the whole reason for the
-- devices result set: backups written to the same storage as the data files
-- are not backups in any sense that survives losing that storage, and nothing
-- else in this archive can show it.
--
-- COMPRESSION CANNOT BE READ FROM CONFIGURATION. sp_configure 'backup
-- compression default' is a default, not a rule: a job that says
-- WITH COMPRESSION compresses whatever the server default is, and a job that
-- does not, does not. Only the ratio of compressed_backup_size to backup_size,
-- measured on the rows, says what actually happened.
--
-- TWO CHAINS ARE COMMON AND INVISIBLE OTHERWISE. is_snapshot separates a VSS
-- or array-level snapshot chain from a native SQL Server chain. Without it,
-- their coexistence has to be inferred from "I/O is frozen" messages in the
-- error log, which is a guess dressed as a finding.
--
-- NO JUDGEMENT IS APPLIED. A 30-day window with no full backup of a database
-- is reported as such and not called a failure: a database created yesterday,
-- or one deliberately excluded because it is rebuilt from source, are both
-- legitimate. Whether an interval is too long depends on the recovery
-- objective, which lives in the client's own policy and not in this archive.
--
-- THIRTY DAYS IS A WINDOW, NOT A RETENTION. msdb keeps whatever the purge job
-- leaves it; the count of rows outside the window is projected so a reader can
-- see whether history is being trimmed aggressively, which itself changes what
-- can be audited later.
--
-- SQL Server 2012 is the floor. compressed_backup_size is 2008+, is_snapshot
-- is 2005+, so both are safe. Not collected for that reason:
--   backupset.encryptor_type / key_algorithm   (2014+)
--   backupset.is_memory_optimized_enabled      (2014+)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

DECLARE @since datetime = DATEADD(day, -30, GETDATE());

SELECT
    30                                                          AS [window.days],
    CONVERT(varchar(19), @since, 126)                           AS [window.since],
    (SELECT COUNT(*) FROM msdb.dbo.backupset
      WHERE backup_finish_date >= @since)                       AS [window.backups_in_window],
    -- History older than the window. A small number here on an instance that
    -- has run for years means the purge is aggressive, and that a later audit
    -- will have less to read than this one did.
    (SELECT COUNT(*) FROM msdb.dbo.backupset
      WHERE backup_finish_date < @since)                        AS [window.backups_older],
    (SELECT CONVERT(varchar(19), MIN(backup_finish_date), 126)
       FROM msdb.dbo.backupset)                                 AS [window.oldest_record],
    (SELECT COUNT(DISTINCT database_name) FROM msdb.dbo.backupset
      WHERE backup_finish_date >= @since)                       AS [window.databases_seen],
    (SELECT COUNT(*) FROM msdb.dbo.backupset
      WHERE backup_finish_date >= @since AND is_snapshot = 1)   AS [window.snapshot_backups],
    (SELECT COUNT(DISTINCT user_name) FROM msdb.dbo.backupset
      WHERE backup_finish_date >= @since)                       AS [window.distinct_users]
OPTION (RECOMPILE, MAXDOP 1);

/* One row per database and backup type. The averages are what a schedule
   review needs: a full backup whose duration has doubled is a finding long
   before it starts failing. */
SELECT
    bs.database_name                                            AS [database],
    CASE bs.type WHEN 'D' THEN 'full'
                 WHEN 'I' THEN 'differential'
                 WHEN 'L' THEN 'log'
                 WHEN 'F' THEN 'file'
                 WHEN 'G' THEN 'file_differential'
                 WHEN 'P' THEN 'partial'
                 WHEN 'Q' THEN 'partial_differential'
                 ELSE bs.type END                               AS [type],
    COUNT(*)                                                    AS [count],
    CONVERT(varchar(19), MIN(bs.backup_finish_date), 126)       AS [first],
    CONVERT(varchar(19), MAX(bs.backup_finish_date), 126)       AS [last],
    CAST(AVG(CAST(DATEDIFF(second, bs.backup_start_date, bs.backup_finish_date) AS bigint))
         AS bigint)                                             AS [avg_seconds],
    MAX(DATEDIFF(second, bs.backup_start_date, bs.backup_finish_date))
                                                                AS [max_seconds],
    CAST(SUM(bs.backup_size) / 1048576.0 AS DECIMAL(18,1))      AS [total_mb],
    CAST(AVG(bs.backup_size) / 1048576.0 AS DECIMAL(18,1))      AS [avg_mb],
    -- The ratio, not the flag. 1.0 means nothing was compressed; a job that
    -- asked for compression on already-compressed data also lands near 1.0,
    -- which is why the raw sizes are projected beside it.
    CAST(SUM(bs.compressed_backup_size) / 1048576.0 AS DECIMAL(18,1))
                                                                AS [compressed_mb],
    CAST(CASE WHEN SUM(bs.backup_size) > 0
              THEN SUM(bs.compressed_backup_size) * 1.0 / SUM(bs.backup_size)
         END AS DECIMAL(5,3))                                   AS [compression_ratio],
    SUM(CASE WHEN bs.is_snapshot = 1 THEN 1 ELSE 0 END)         AS [snapshot_count],
    SUM(CASE WHEN bs.is_copy_only = 1 THEN 1 ELSE 0 END)        AS [copy_only_count],
    COUNT(DISTINCT bs.user_name)                                AS [distinct_users],
    MAX(bs.user_name)                                           AS [a_user],
    -- Arbitrary like a_user above, and named to say so: the recovery model
    -- can legitimately change inside the window, and MAX picks the
    -- alphabetically last rather than the current one. sys.databases holds
    -- the model in force now; this says what one of the backups was taken
    -- under.
    MAX(bs.recovery_model)                                      AS [a_recovery_model]
FROM msdb.dbo.backupset AS bs
WHERE bs.backup_finish_date >= @since
GROUP BY bs.database_name, bs.type
ORDER BY bs.database_name, bs.type
OPTION (RECOMPILE, MAXDOP 1);

/* Where the backups are written. Grouped by device rather than listed per
   backup: the question is how many destinations exist and what goes to each,
   not the name of every file. */
SELECT
    CASE bmf.device_type
         WHEN 2   THEN 'disk'
         WHEN 5   THEN 'tape'
         WHEN 7   THEN 'virtual_device'
         WHEN 9   THEN 'azure_storage'
         WHEN 105 THEN 'permanent_disk'
         WHEN 106 THEN 'permanent_tape'
         WHEN 107 THEN 'permanent_virtual_device'
         ELSE CAST(bmf.device_type AS varchar(10)) END          AS [device_type],
    -- The directory, not the file. A backup file name carries a timestamp and
    -- would produce one row per backup; the path is what says which storage
    -- the copies live on.
    CASE WHEN bmf.physical_device_name LIKE '%\%'
         THEN LEFT(bmf.physical_device_name,
                   LEN(bmf.physical_device_name)
                   - CHARINDEX('\', REVERSE(bmf.physical_device_name)))
         ELSE bmf.physical_device_name END                      AS [path],
    COUNT(*)                                                    AS [backups],
    COUNT(DISTINCT bs.database_name)                            AS [databases],
    CAST(SUM(bs.backup_size) / 1073741824.0 AS DECIMAL(18,1))   AS [total_gb],
    CONVERT(varchar(19), MAX(bs.backup_finish_date), 126)       AS [last]
FROM msdb.dbo.backupmediafamily AS bmf
JOIN msdb.dbo.backupset AS bs ON bs.media_set_id = bmf.media_set_id
WHERE bs.backup_finish_date >= @since
GROUP BY bmf.device_type,
    CASE WHEN bmf.physical_device_name LIKE '%\%'
         THEN LEFT(bmf.physical_device_name,
                   LEN(bmf.physical_device_name)
                   - CHARINDEX('\', REVERSE(bmf.physical_device_name)))
         ELSE bmf.physical_device_name END
ORDER BY COUNT(*) DESC
OPTION (RECOMPILE, MAXDOP 1);

/* The last 200 backups, newest first. The aggregate above says what usually
   happens; this says what happened last night, which is the question asked
   when something went wrong. */
SELECT TOP (200)
    bs.database_name                                            AS [database],
    CASE bs.type WHEN 'D' THEN 'full'
                 WHEN 'I' THEN 'differential'
                 WHEN 'L' THEN 'log'
                 ELSE bs.type END                               AS [type],
    CONVERT(varchar(19), bs.backup_start_date, 126)             AS [start],
    CONVERT(varchar(19), bs.backup_finish_date, 126)            AS [finish],
    DATEDIFF(second, bs.backup_start_date, bs.backup_finish_date) AS [seconds],
    CAST(bs.backup_size / 1048576.0 AS DECIMAL(18,1))           AS [mb],
    CAST(bs.compressed_backup_size / 1048576.0 AS DECIMAL(18,1)) AS [compressed_mb],
    CAST(bs.is_snapshot AS bit)                                 AS [is_snapshot],
    CAST(bs.is_copy_only AS bit)                                AS [is_copy_only],
    bs.user_name                                                AS [user],
    bs.server_name                                              AS [server]
FROM msdb.dbo.backupset AS bs
WHERE bs.backup_finish_date >= @since
ORDER BY bs.backup_finish_date DESC
OPTION (RECOMPILE, MAXDOP 1);
