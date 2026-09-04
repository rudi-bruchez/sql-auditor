-- @scope:         instance
-- @resultsets:    root:object, reports:array
-- @permissions:   CONNECT, VIEW SERVER STATE
-- @requires_flag: blocked_process_reports
-- @writer:        blocked-process-reports
-- @timeout:       300
--
-- The blocked process reports captured by whatever Extended Events session on
-- this instance subscribes to them, read out of that session's .xel files, one
-- .xml file each.
--
-- WHY THIS IS THE ONE THING WAIT STATISTICS CANNOT GIVE YOU. LCK_M_* waits say
-- sessions waited on locks, for how long in total and on average. They never say
-- who blocked whom. A blocked process report does: it names the blocked session
-- and the blocking session, their SQL, their transaction state and the resource
-- in dispute. On the audit that prompted this file the instance had 2 382 hours
-- of LCK_M_IS at 3.6 minutes average, and nothing anywhere said what was holding
-- the locks.
--
-- IT ONLY EXISTS IF SOMEBODY TURNED IT ON, and two things have to be true.
-- 'blocked process threshold (s)' must be non-zero — at its default of 0 the
-- event never fires at all — and a session must subscribe to
-- blocked_process_report. 062.xe-sessions.sql reports both, without a flag, so
-- an archive always says which of the two is missing. This collector then reads
-- what was captured, if anything was.
--
-- READING A .xel IS NOT LIKE READING A DMV, and the difference is the whole
-- reason for the shape below. sys.fn_xe_file_target_read_file reaches the file
-- system, as the SQL Server service account rather than as the login connected
-- here, and it raises rather than returning empty when the path is wrong, the
-- file is locked or the account cannot read the directory. A raise in the middle
-- of a result set is a half-sent result set. So the read happens into a table
-- variable inside TRY/CATCH, the error number and message are projected into the
-- root, and the archive states why it read nothing instead of implying there was
-- nothing to read.
--
-- THE PATH IS DERIVED, NOT GUESSED. The running target reports the file it is
-- writing right now, with its full path; the session definition holds the file
-- name as configured. The directory of the first and the stem of the second give
-- the wildcard that covers the rollover files, which is where the history is —
-- the current file alone would be the same mistake as reading the ring buffer.
--
-- TWO CAPS, NEITHER SILENT. At most 500 reports, the most recent first, and at
-- most 1 MiB each. Past either, the XML is NULLed by a CONDITIONAL PROJECTION —
-- never a WHERE — so the row survives with its timestamp and its size, and the
-- writer records a named omission.
--
-- NO JUDGEMENT IS APPLIED. No report is called serious. A twenty-second block on
-- a nightly load is not the same finding as a twenty-second block at 10am, and
-- the collector has no way to know which it is looking at.
--
-- SQL Server 2012 is the floor. sys.fn_xe_file_target_read_file and the
-- blocked_process_report event both predate it.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @session   sysname       = NULL;
DECLARE @path      nvarchar(600) = NULL;
DECLARE @configured nvarchar(400) = NULL;
DECLARE @running   nvarchar(max) = NULL;
DECLARE @err_number int          = NULL;
DECLARE @err_message nvarchar(400) = NULL;
DECLARE @threshold int =
    (SELECT CAST(c.value_in_use AS int) FROM sys.configurations AS c
      WHERE c.name = 'blocked process threshold (s)');

/* The session that both subscribes to the event and writes to a file. A session
   capturing to a ring buffer only is not readable this way and is left out
   deliberately: reporting it as a source that yielded nothing would be worse
   than not naming it, and 062 lists it either way.

   When more than one qualifies, the running one wins, then the lowest id —
   stable, so two collections of an unchanged instance read the same session. */
SELECT TOP (1)
       @session    = s.name,
       @configured = CAST(f.value AS nvarchar(400)),
       @running    = CAST(rt.target_data AS nvarchar(max))
FROM sys.server_event_sessions             AS s
JOIN sys.server_event_session_events       AS e  ON e.event_session_id = s.event_session_id
                                                AND e.name = 'blocked_process_report'
JOIN sys.server_event_session_targets      AS t  ON t.event_session_id = s.event_session_id
                                                AND t.name = 'event_file'
LEFT JOIN sys.server_event_session_fields  AS f  ON f.event_session_id = s.event_session_id
                                                AND f.object_id = t.target_id
                                                AND f.name = 'filename'
LEFT JOIN sys.dm_xe_sessions               AS rs ON rs.name = s.name
LEFT JOIN sys.dm_xe_session_targets        AS rt ON rt.event_session_address = rs.address
                                                AND rt.target_name = 'event_file'
ORDER BY CASE WHEN rs.name IS NULL THEN 1 ELSE 0 END, s.event_session_id;

/* The directory from the running target, the stem from the definition, and a
   wildcard between them so the rollover files come too. When the session is not
   running there is no running target, and the configured name is used as it
   stands — relative to the LOG directory, which is where the function resolves
   a bare name anyway. */
DECLARE @current nvarchar(600) =
    CAST(CAST(@running AS xml).value('(/EventFileTarget/File/@name)[1]', 'nvarchar(600)') AS nvarchar(600));
/* The extension is tested on the end of the name, not on a reversed copy of it.
   The earlier version searched the reversed name for the extension spelled
   forwards, which cannot match: reversing puts the dot last. The stem therefore
   kept its extension and the path came out as 'Blocked process.xel*.xel', where
   SQL Server writes 'Blocked process_0_133000000000000000.xel'. The collection
   reported an empty capture on an instance whose ring buffer held two reports,
   which is worse than an error: the audit concludes there is no blocking on a
   server that recorded some. Found on a client instance in August 2026.

   UPPER() rather than a bare comparison, and that is not decoration. The
   comparison takes the collation of the context database, so under a case
   sensitive one a session configured as '.XEL' would fail the test: the stem
   would keep its extension and the pattern would be wrong again, silently, in
   exactly the way the paragraph above describes. Folding the case states the
   intention instead of inheriting it from whichever database the collector
   happens to be pointed at. */
DECLARE @stem nvarchar(400) =
    CASE WHEN @configured IS NULL THEN NULL
         WHEN UPPER(RIGHT(@configured, 4)) = N'.XEL'
              THEN LEFT(@configured, LEN(@configured) - 4)
         ELSE @configured END;
IF @stem IS NOT NULL
BEGIN
    SET @stem = REVERSE(LEFT(REVERSE(@stem), CASE WHEN CHARINDEX('\', REVERSE(@stem)) = 0
                                                  THEN LEN(@stem)
                                                  ELSE CHARINDEX('\', REVERSE(@stem)) - 1 END));
    SET @path = CASE
        WHEN @current IS NULL THEN @stem + N'*.xel'
        ELSE LEFT(@current, LEN(@current) - CHARINDEX('\', REVERSE(@current)) + 1) + @stem + N'*.xel'
    END;
END;

DECLARE @reports TABLE (
    event_time datetime2(3),
    report     nvarchar(max),
    file_name  nvarchar(400),
    file_offset bigint
);

/* The read itself, guarded. Everything that can go wrong here goes wrong at the
   file system — a path the service account cannot reach, a share withdrawn, a
   file being rolled at this instant — and none of it is a reason for the whole
   collection to fail. The error is data. */
IF @path IS NOT NULL
BEGIN
    BEGIN TRY
        INSERT INTO @reports (event_time, report, file_name, file_offset)
        SELECT x.value('(/event/@timestamp)[1]', 'datetime2(3)'),
               CAST(x.query('(/event/data[@name="blocked_process"]/value/*)[1]') AS nvarchar(max)),
               t.file_name,
               t.file_offset
        FROM sys.fn_xe_file_target_read_file(@path, NULL, NULL, NULL) AS t
        CROSS APPLY (SELECT CAST(t.event_data AS xml)) AS e(x)
        WHERE t.object_name = 'blocked_process_report';
    END TRY
    BEGIN CATCH
        SET @err_number  = ERROR_NUMBER();
        SET @err_message = LEFT(ERROR_MESSAGE(), 400);
    END CATCH
END;

SELECT
    @session                                                      AS [source.session],
    @path                                                         AS [source.path],
    CAST(CASE WHEN @path IS NULL THEN 0 ELSE 1 END AS bit)        AS [source.readable],
    @err_number                                                   AS [source.error_number],
    @err_message                                                  AS [source.error_message],
    @threshold                                                    AS [blocked_process.threshold_seconds],
    /* Both are needed to read the count below. A threshold of 0 means the event
       cannot fire, so an empty capture says nothing about whether blocking
       occurred — it says the instance was never asked to look. */
    (SELECT COUNT(*) FROM @reports)                               AS [capture.reports_in_files],
    CONVERT(varchar(23), (SELECT MIN(event_time) FROM @reports), 126) AS [capture.earliest],
    CONVERT(varchar(23), (SELECT MAX(event_time) FROM @reports), 126) AS [capture.latest],
    500                                                           AS [caps.reports],
    1048576                                                       AS [caps.report_bytes]
OPTION (RECOMPILE, MAXDOP 1);

/* One row per report, whether or not its XML came back. Most recent first. */
SELECT
    ROW_NUMBER() OVER (ORDER BY r.event_time DESC)                AS [report.rank],
    COUNT(*)     OVER ()                                          AS [report.count],
    CONVERT(varchar(23), r.event_time, 126)                       AS [occurred_at],
    r.file_name                                                   AS [file_name],
    CASE WHEN DATALENGTH(r.report) <= 1048576
          AND ROW_NUMBER() OVER (ORDER BY r.event_time DESC) <= 500
         THEN r.report END                                        AS [report],
    DATALENGTH(r.report)                                          AS [report_bytes]
FROM @reports AS r
ORDER BY r.event_time DESC
OPTION (RECOMPILE, MAXDOP 1);
