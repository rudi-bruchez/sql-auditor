-- @scope:       instance
-- @resultsets:  root:object, restores:array
-- @permissions: CONNECT, MSDB READ
-- @timeout:     60
--
-- What has been restored onto this instance, as msdb recorded it.
--
-- Why this collector exists: it answers a question that has no other source in
-- the archive, and answering it wrong changes a finding into a mistake.
--
-- When a database shows no rows at all in sys.dm_db_index_usage_stats, two
-- explanations fit and they are opposites. Either nothing has queried it since
-- the instance started — a real finding — or its counters were reset, which
-- happens when the database is restored or attached. On one audited instance
-- every one of 1 082 indexes had no usage row; concluding "all these indexes
-- are unused" from that would have been wrong, and nothing in the collection
-- could tell the two apart.
--
-- A restore also explains a create_date that disagrees with the age of the
-- data, and a recovery model or an option set that differs from its siblings
-- for no visible reason: it came from somewhere else.
--
-- NO JUDGEMENT IS APPLIED. A restore is not an incident. Refresh from
-- production into a test instance is routine, and this collector cannot tell
-- that apart from a recovery after a failure — it reports the fact and leaves
-- the reading to someone who knows why it was done.
--
-- restorehistory is not purged by the same job as backupset on every instance,
-- so no window is applied here: the whole table is read and the count is small
-- by nature. The rows are capped at 200 for the same reason as everywhere else
-- in this corpus — an archive nobody can read is not evidence.
--
-- SQL Server 2012 is the floor; restorehistory predates it entirely.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT
    (SELECT COUNT(*) FROM msdb.dbo.restorehistory)              AS [counts.total],
    (SELECT COUNT(DISTINCT destination_database_name)
       FROM msdb.dbo.restorehistory)                            AS [counts.databases],
    (SELECT COUNT(*) FROM msdb.dbo.restorehistory
      WHERE restore_date >= DATEADD(day, -30, GETDATE()))       AS [counts.last_30_days],
    (SELECT CONVERT(varchar(19), MAX(restore_date), 126)
       FROM msdb.dbo.restorehistory)                            AS [counts.most_recent],
    (SELECT CONVERT(varchar(19), MIN(restore_date), 126)
       FROM msdb.dbo.restorehistory)                            AS [counts.oldest_record]
OPTION (RECOMPILE, MAXDOP 1);

SELECT TOP (200)
    rh.destination_database_name                                AS [database],
    CONVERT(varchar(19), rh.restore_date, 126)                  AS [restored_at],
    CASE rh.restore_type WHEN 'D' THEN 'database'
                         WHEN 'F' THEN 'file'
                         WHEN 'G' THEN 'filegroup'
                         WHEN 'I' THEN 'differential'
                         WHEN 'L' THEN 'log'
                         WHEN 'V' THEN 'verifyonly'
                         ELSE rh.restore_type END               AS [restore_type],
    rh.user_name                                                AS [user],
    CAST(rh.replace AS bit)                                     AS [with_replace],
    -- The database this copy came from. When it differs from the destination,
    -- the database was cloned rather than recovered, which is the distinction
    -- that matters when reading a reset usage counter.
    bs.database_name                                            AS [source_database],
    bs.server_name                                              AS [source_server],
    CONVERT(varchar(19), bs.backup_finish_date, 126)            AS [source_backup_finish]
FROM msdb.dbo.restorehistory AS rh
LEFT JOIN msdb.dbo.backupset AS bs ON bs.backup_set_id = rh.backup_set_id
ORDER BY rh.restore_date DESC
OPTION (RECOMPILE, MAXDOP 1);
