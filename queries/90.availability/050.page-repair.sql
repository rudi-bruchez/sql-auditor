-- @scope:       instance
-- @resultsets:  root:object, page_repairs:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- Pages that a replica had to supply because this instance could not read its
-- own copy, or read it and found it wrong.
--
-- WHY THIS COLLECTOR EXISTS, AND WHY 070.suspect-pages.sql IS NOT THE SAME
-- RECORD. That file reads msdb.dbo.suspect_pages, which is the engine's list of
-- pages it failed on. Automatic page repair is a different mechanism with a
-- different record: when a database is in an availability group or a mirroring
-- session and a page read fails an integrity check, the engine asks the partner
-- for its copy, writes it over the bad one, and carries on. The query that
-- triggered it may succeed. Nobody is told.
--
-- So the two answer different questions. suspect_pages says the engine gave up
-- on a page. sys.dm_hadr_auto_page_repair says the engine did not have to,
-- because there was a second copy. An instance can therefore show a clean
-- suspect_pages table and a history of repairs, and the second is the more
-- alarming of the two: a page that had to be fetched from a replica is a page
-- the local storage got wrong, and storage that gets one page wrong is storage
-- that will get another.
--
-- THE RECORD IS SMALL AND IT IS NOT PERSISTED. Both views are in-memory and
-- bounded: the engine keeps roughly the last hundred repairs per database and
-- discards the rest, and everything is lost on restart or on a failover. A
-- collection is therefore a sample of whatever window the instance has been up
-- for, and an empty result on a server that restarted last night says nothing
-- at all. instance_start is projected beside the rows for exactly that reason,
-- as 010.wait-stats.sql does for its own counters.
--
-- TWO VIEWS, ONE ARRAY, BECAUSE THE FACT IS THE SAME. sys.dm_hadr_auto_page_
-- repair covers availability groups and sys.dm_db_mirroring_auto_page_repair
-- covers database mirroring, which is deprecated and still deployed. Their
-- columns differ in name and agree in meaning, so they are unioned with a
-- mechanism column saying which is which. An estate still running mirroring is
-- exactly the kind of estate where this finding turns up, so collecting only
-- the modern one would miss it where it matters most.
--
-- page_status IS THE COLUMN TO READ FIRST, and its values are not ordered by
-- severity. 2 means the page was queued for repair, 3 means the request went to
-- the partner, 4 means the repair succeeded and 5 means it is irreparable
-- because the partner's copy is bad too. The one that ends an audit
-- conversation is 5: it means both copies are wrong and the only route back is
-- a restore. 4 is the common case and is not reassuring, it is the record of
-- local corruption that happened to be recoverable.
--
-- MEASURED READABILITY, NOT MEASURED CONTENT. Both views returned zero rows on
-- 16.0.4265.3 and 17.0.4065.4, which are standalone instances with neither
-- mechanism configured, in 0 ms and 47 ms respectively. Producing a real repair
-- would mean corrupting a page on purpose in a mirrored pair, which is not
-- something to do on a shared lab, so the shape of the array below is written
-- from the documented columns and the query is proved only to run. That is said
-- here rather than left for a reader to assume otherwise.
--
-- NO JUDGEMENT IS APPLIED, and this is one of the few files where the absence
-- of judgement needs stating twice. A single repair is not proof of a failing
-- disk; a page can be damaged by a driver, a firmware path or a one-off. What
-- makes it a finding is its company: read this beside 070.suspect-pages.sql,
-- beside the 823, 824 and 825 counts in 040.error-log.sql, and beside the
-- ioLatchTimeouts of 064.server-diagnostics.sql. Any two of those agreeing is
-- a different conversation from any one of them alone.
--
-- SQL Server 2012 is the floor. sys.dm_hadr_auto_page_repair arrived with
-- availability groups in 2012 and the mirroring view predates it. Not collected
-- for that reason:
--   the page contents                 (never readable, and never wanted)
--   DBCC PAGE                         (undocumented, and it needs sysadmin)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @err_repairs int = 0, @msg nvarchar(2048) = N'';

DECLARE @repairs TABLE (
    [mechanism]         varchar(20),
    [database_name]     sysname NULL,
    [file_id]           int,
    [page_id]           bigint,
    [error_type]        int,
    [page_status]       int,
    [modification_time] datetime2(3) NULL);

BEGIN TRY
    INSERT INTO @repairs
    SELECT 'availability_group',
           DB_NAME(r.database_id), r.file_id, r.page_id,
           r.error_type, r.page_status, r.modification_time
    FROM sys.dm_hadr_auto_page_repair AS r
    OPTION (RECOMPILE, MAXDOP 1);

    /* The mirroring view names its columns differently and means the same
       things. modification_time does not exist there, so the column is NULL
       for these rows rather than invented. */
    INSERT INTO @repairs
    SELECT 'mirroring',
           DB_NAME(m.database_id), m.file_id, m.page_id,
           m.error_type, m.page_status, NULL
    FROM sys.dm_db_mirroring_auto_page_repair AS m
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_repairs = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT
    CONVERT(sysname, SERVERPROPERTY('ServerName'))              AS [instance],
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    -- The window. These views are in-memory, so a count of zero on an instance
    -- that restarted this morning is not evidence of anything.
    CONVERT(varchar(23), (SELECT sqlserver_start_time FROM sys.dm_os_sys_info), 126)
                                                                AS [instance_start],
    (SELECT DATEDIFF(second, sqlserver_start_time, GETDATE())
       FROM sys.dm_os_sys_info)                                 AS [seconds_since_instance_start],
    (SELECT COUNT(*) FROM @repairs)                             AS [counts.repairs],
    (SELECT COUNT(DISTINCT r.[database_name]) FROM @repairs AS r)
                                                                AS [counts.databases_affected],
    -- Split by outcome, because the two ends of the scale are different
    -- findings. 4 is corruption that was recovered; 5 is corruption on both
    -- copies, which no replica can fix.
    (SELECT COUNT(*) FROM @repairs WHERE [page_status] = 4)     AS [counts.repaired],
    (SELECT COUNT(*) FROM @repairs WHERE [page_status] = 5)     AS [counts.irreparable],
    (SELECT COUNT(*) FROM @repairs WHERE [page_status] IN (2, 3))
                                                                AS [counts.in_progress],
    (SELECT COUNT(*) FROM @repairs WHERE [mechanism] = 'mirroring')
                                                                AS [counts.from_mirroring],
    CASE WHEN @err_repairs = 0 THEN 1 ELSE 0 END                AS [collected.page_repairs],
    @err_repairs                                                AS [errors.page_repairs],
    NULLIF(@msg, N'')                                           AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

/* One row per repaired page, newest first. The page is identified and never
   read: file_id and page_id locate it for whoever goes on to run DBCC PAGE
   with the rights to do so, and that is as far as an audit collector goes. */
SELECT
    r.[mechanism]                                               AS [mechanism],
    r.[database_name]                                           AS [database],
    r.[file_id]                                                 AS [file_id],
    r.[page_id]                                                 AS [page_id],
    -- 1 is a checksum mismatch, 2 a torn page, 3 any other hardware-reported
    -- error, 4 a page that failed its own header checks and 5 a page the
    -- engine restored over. The first two are storage; the others can be too.
    r.[error_type]                                              AS [error_type],
    r.[page_status]                                             AS [page_status],
    CASE r.[page_status]
         WHEN 2 THEN 'queued'
         WHEN 3 THEN 'requested_from_partner'
         WHEN 4 THEN 'repaired'
         WHEN 5 THEN 'irreparable'
         ELSE CAST(r.[page_status] AS varchar(10)) END          AS [page_status_desc],
    CONVERT(varchar(23), r.[modification_time], 126)            AS [modified_at]
FROM @repairs AS r
ORDER BY r.[modification_time] DESC, r.[database_name], r.[page_id]
OPTION (RECOMPILE, MAXDOP 1);
