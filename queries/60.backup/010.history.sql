-- @scope:       instance
-- @resultsets:  root:object, per_database:array, devices:array, recent:array, growth:array
-- @permissions: CONNECT, MSDB READ
-- @timeout:     120
-- @profiles:    space
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
-- GROWTH IS MEASURED HERE BECAUSE NOTHING ELSE CAN MEASURE IT. A collection
-- is one point in time: file sizes say how big the database is, never how fast
-- it grows, and the growth rate is the term that decides how much free space a
-- data file has to keep and for how long. The successive full backups msdb
-- still holds are the one series already in reach, and backup_size is the
-- uncompressed size of what was written, which tracks used pages rather than
-- allocated file size.
--
-- THE GROWTH SERIES IGNORES THE THIRTY-DAY WINDOW, on purpose. Everything else
-- here answers "what is happening now"; a rate wants the longest honest span,
-- so it reads whatever the purge has left and reports the span beside the
-- number. Snapshot backups are excluded because their recorded size measures
-- something else entirely. A shrink or a purge inside the span shows up as a
-- negative rate, which is reported as measured rather than clamped to zero: a
-- database that got smaller is a fact about the period, and hiding it would
-- turn one honest measurement into an invented one.
--
-- SQL Server 2012 is the floor. compressed_backup_size is 2008+, is_snapshot
-- is 2005+, and LAG is 2012 itself, so all three are safe and none of them
-- raises the floor. Not collected for that reason:
--   backupset.encryptor_type / key_algorithm   (2014+)
--   backupset.is_memory_optimized_enabled      (2014+)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

-- One instant, read once. The window starts from it and the gap between the
-- last backup and now is measured against it, so the two cannot disagree.
--
-- GETDATE() AND NOT GETUTCDATE(), AND THIS IS NOT AN OVERSIGHT.
-- backupset.backup_finish_date is recorded on the instance's local clock.
-- Subtracting a UTC instant from it yields the offset as a wait nobody waited,
-- and on a server running in UTC, which is what every test container does, the
-- two agree and the defect is invisible. Local clock on both sides, always.
--
-- The residual, stated rather than fixed: a gap that spans a daylight-saving
-- transition is off by the offset, because local time is what msdb recorded and
-- local time is not monotonic. It costs an hour twice a year on a measure whose
-- findings are counted in days, and there is no better source. Converting would
-- trade a known hour for the unknown offset above.
DECLARE @now datetime = GETDATE();
DECLARE @since datetime = DATEADD(day, -30, @now);

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
   before it starts failing.

   A GAP IS NOT A DURATION. max_seconds is how long a backup took;
   max_gap_seconds is how long the database went without one, which is the
   term a recovery objective is written in. The two read alike and answer
   different questions, which is why both names say which one they are. The
   gap needs LAG, and a window function is computed after the grouping and
   cannot see the preceding row, so the per-row gap is resolved in a CTE and
   only then aggregated.

   NULL MEANS NO PAIR, NOT NO WAIT. A type with a single backup in the window
   has nothing to measure against and LAG returns NULL for it. Folding that to
   zero would read as "never went without", which is the opposite of what was
   observed.

   THE WINDOW IS APPLIED BEFORE THE GAP, inside the CTE. The first backup
   inside the window therefore has no predecessor, and an interval straddling
   the 30-day boundary is not reported rather than reported as if it began
   inside. Filtering after the gap would make this result set speak about
   history the collector says it does not read.

   THE LAST BACKUP IS NOT THE END OF THE SERIES, and no LAG can close that
   edge. A database whose log backups stopped three weeks ago still reports
   the gaps of what it did before stopping: ten nightly log backups then
   twenty days of nothing give max_gap_seconds = 86400, which reads as a
   healthy daily schedule. seconds_since_last is what contradicts it, and a
   schedule review reads the larger of the two. No judgement is applied to
   either, as above: which interval is too long is the client's recovery
   objective and not ours. */
WITH history AS (
    SELECT
        bs.database_name,
        bs.type,
        bs.backup_start_date,
        bs.backup_finish_date,
        bs.backup_size,
        bs.compressed_backup_size,
        bs.is_snapshot,
        bs.is_copy_only,
        bs.has_backup_checksums,
        bs.has_incomplete_metadata,
        bs.is_password_protected,
        bs.expiration_date,
        bs.user_name,
        bs.recovery_model,
        DATEDIFF(second,
                 LAG(bs.backup_finish_date) OVER (PARTITION BY bs.database_name, bs.type
                                                  ORDER BY bs.backup_finish_date),
                 bs.backup_finish_date)                         AS gap_seconds
    FROM msdb.dbo.backupset AS bs
    WHERE bs.backup_finish_date >= @since
)
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
    MAX(bs.gap_seconds)                                         AS [max_gap_seconds],
    DATEDIFF(second, MAX(bs.backup_finish_date), @now)          AS [seconds_since_last],
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
    -- HOW MANY OF THESE BACKUPS WOULD NOTICE A CORRUPT PAGE GOING INTO THEM.
    -- WITH CHECKSUM is not the default: BACKUP verifies page checksums on the
    -- way out only when asked, or when the instance runs with trace flag 3023,
    -- which 025.startup-parameters-2012.sql would show. Without it a page that
    -- was already damaged is copied into the backup set, the backup reports
    -- success, and the damage is only found on the restore that had to work.
    -- Nothing else in this archive answers it, and it costs nothing here
    -- because the column sits in the row already being read.
    SUM(CASE WHEN bs.has_backup_checksums = 1 THEN 1 ELSE 0 END) AS [with_checksum_count],
    -- Tail-log backups, that is BACKUP LOG ... WITH NO_TRUNCATE or CONTINUE_
    -- AFTER_ERROR. One is the normal first step of a restore; a run of them in
    -- a 30-day window says the database has been left in the restoring state,
    -- or that somebody has been taking log backups off a damaged database.
    SUM(CASE WHEN bs.has_incomplete_metadata = 1 THEN 1 ELSE 0 END)
                                                                AS [incomplete_metadata_count],
    -- Password-protected backup sets. The feature is deprecated and the
    -- password is not encryption; what makes it a finding is that a restore
    -- needs a password nobody has written down.
    SUM(CASE WHEN bs.is_password_protected = 1 THEN 1 ELSE 0 END)
                                                                AS [password_protected_count],
    -- Backups that carry an expiry, which changes what an overwrite does to
    -- the media set.
    SUM(CASE WHEN bs.expiration_date IS NOT NULL THEN 1 ELSE 0 END)
                                                                AS [with_expiry_count],
    COUNT(DISTINCT bs.user_name)                                AS [distinct_users],
    MAX(bs.user_name)                                           AS [a_user],
    -- Arbitrary like a_user above, and named to say so: the recovery model
    -- can legitimately change inside the window, and MAX picks the
    -- alphabetically last rather than the current one. sys.databases holds
    -- the model in force now; this says what one of the backups was taken
    -- under.
    MAX(bs.recovery_model)                                      AS [a_recovery_model],
    -- Which is why the count sits beside it. The three models sort
    -- BULK_LOGGED, FULL, SIMPLE, so a database switched to BULK_LOGGED for a
    -- nightly load and switched back leaves a_recovery_model reading FULL and
    -- the excursion invisible. Anything above 1 here says the model moved
    -- inside the window, which is the fact worth having: under BULK_LOGGED a
    -- log backup cannot serve a point-in-time restore into the interval it
    -- covers, so a window that contains a switch contains a hole in the
    -- recovery story that the dates alone do not show.
    COUNT(DISTINCT bs.recovery_model)                           AS [distinct_recovery_models]
FROM history AS bs
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
    CAST(bs.has_backup_checksums AS bit)                        AS [has_checksums],
    CAST(bs.has_incomplete_metadata AS bit)                     AS [incomplete_metadata],
    -- THE TWO NUMBERS THAT SAY WHETHER A RESTORE CHAIN ACTUALLY HOLDS, and
    -- they belong here rather than in the aggregate above because they are
    -- identities and not quantities. database_backup_lsn is the log sequence
    -- number of the full backup this one belongs to; differential_base_lsn is
    -- the full a differential is computed against. A differential restores only
    -- onto the full whose first_lsn equals its differential_base_lsn, so a
    -- differential whose base is a full that was taken by a third-party agent,
    -- or by an ad-hoc COPY_ONLY that nobody kept, restores onto nothing. That
    -- is invisible from dates alone, which is how it survives a review: the
    -- schedule looks complete because a full and a differential both ran.
    bs.database_backup_lsn                                      AS [database_backup_lsn],
    bs.differential_base_lsn                                    AS [differential_base_lsn],
    bs.first_lsn                                                AS [first_lsn],
    bs.user_name                                                AS [user],
    bs.server_name                                              AS [server]
FROM msdb.dbo.backupset AS bs
WHERE bs.backup_finish_date >= @since
ORDER BY bs.backup_finish_date DESC
OPTION (RECOMPILE, MAXDOP 1);

/* Data growth per database, from the full backups msdb still holds. One row
   per database that has at least one full backup; a database with exactly one
   reports fulls = 1, a span of zero days and a NULL rate, which is the
   difference between "not growing" and "not measurable" that a zero would
   destroy. */
WITH fulls AS (
    SELECT bs.database_name,
           bs.backup_finish_date,
           bs.backup_size,
           ROW_NUMBER() OVER (PARTITION BY bs.database_name
                              ORDER BY bs.backup_finish_date ASC)  AS oldest_first,
           ROW_NUMBER() OVER (PARTITION BY bs.database_name
                              ORDER BY bs.backup_finish_date DESC) AS newest_first,
           COUNT(*)     OVER (PARTITION BY bs.database_name)       AS fulls
    FROM msdb.dbo.backupset AS bs
    WHERE bs.type = 'D' AND bs.is_snapshot = 0
)
SELECT
    o.database_name                                             AS [database],
    o.fulls                                                     AS [fulls],
    CONVERT(varchar(19), o.backup_finish_date, 126)             AS [first],
    CONVERT(varchar(19), n.backup_finish_date, 126)             AS [last],
    DATEDIFF(day, o.backup_finish_date, n.backup_finish_date)   AS [span_days],
    CAST(o.backup_size / 1048576.0 AS DECIMAL(18,1))            AS [first_mb],
    CAST(n.backup_size / 1048576.0 AS DECIMAL(18,1))            AS [last_mb],
    CAST((n.backup_size - o.backup_size) / 1048576.0 AS DECIMAL(18,1))
                                                                AS [growth_mb],
    CAST((n.backup_size - o.backup_size) / 1048576.0
         / NULLIF(DATEDIFF(day, o.backup_finish_date, n.backup_finish_date), 0)
         AS DECIMAL(18,2))                                      AS [mb_per_day]
FROM fulls AS o
JOIN fulls AS n ON n.database_name = o.database_name
               AND n.newest_first = 1
WHERE o.oldest_first = 1
ORDER BY o.database_name
OPTION (RECOMPILE, MAXDOP 1);
