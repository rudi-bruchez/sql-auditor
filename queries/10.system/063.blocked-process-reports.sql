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
-- EVERY REPORT IS READ, A SAMPLE IS KEPT WHOLE. The fields that say who blocked
-- whom, on what and for how long are extracted from EVERY report in the files
-- and projected on every row: the blocked session and its ownerId, the blocking
-- session, its status and trancount, the wait resource, the lock mode and the
-- wait. The writer groups them into episodes over the whole capture. Only the
-- full XML is capped, and the cap keeps ONE report per episode, its longest,
-- the longest episodes first.
--
-- The earlier cap kept the 500 most recent reports. On a client capture of
-- 1 635 that threw away 1 135 and shrank the window to nineteen hours, and it
-- biased every ranking a blocking analysis makes — who blocks, on what, in which
-- mode, the median wait — towards the last hour. A report is re-emitted every
-- monitor tick while the block lasts, so most of what the recency cap kept was
-- the same few episodes again. Found in September 2026.
--
-- THE EPISODE KEY is the blocked session's spid and ownerId, the blocking spid
-- and the wait resource, and it was measured rather than chosen: ownerId is
-- carried on the blocked side even when its trancount is 0, and absent from the
-- blocking side, so a key built on the blocker's ownerId would merge every
-- episode into one.
--
-- monitor_loop IS WHICH PASS OF THE MONITOR EMITTED THE REPORT. The
-- monitorLoop attribute of the report counts the passes of the deadlock
-- monitor, which also produces blocked process reports, from 0 at instance
-- start; reports that share it were detected in the same pass, so they are
-- one picture of the blocking at one moment, and a blocker that is itself
-- blocked in a report of the same pass is a link of a chain rather than its
-- head. That test needs the pass of EVERY report, and keeping one report whole
-- per episode had removed it from the archive, so it is projected on every row
-- and carried into the episodes as their first and last pass. It is not a
-- clock: the monitor runs about every five seconds and faster after it finds a
-- deadlock, so the gap between two passes is not a duration, and the counter
-- restarts with the instance. Microsoft does not document the attribute; its
-- meaning here is the one published by Michael J. Swart in February 2017. It
-- was not measured on real reports tonight, because producing them needs
-- 'blocked process threshold (s)' changed on the lab; the XPath was run
-- against a literal report of the documented shape instead, and an absent
-- attribute reads NULL.
--
-- TWO CAPS, NEITHER SILENT. At most 500 reports kept whole, and at most 1 MiB
-- each. Past either, the XML is NULLed by a CONDITIONAL PROJECTION — never a
-- WHERE — so the row survives with its timestamp, its size and its fields.
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
DECLARE @threshold int;
SELECT @threshold = CAST(c.value_in_use AS int) FROM sys.configurations AS c
 WHERE c.name = 'blocked process threshold (s)'
OPTION (RECOMPILE, MAXDOP 1);

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
ORDER BY CASE WHEN rs.name IS NULL THEN 1 ELSE 0 END, s.event_session_id
OPTION (RECOMPILE, MAXDOP 1);

/* The directory from the running target, the stem from the definition, and a
   wildcard between them so the rollover files come too. When the session is not
   running there is no running target, and the configured name is used as it
   stands, directory included: a bare name resolves to the LOG directory, and a
   full path to wherever it points. The earlier version stripped the directory
   first and so read a session configured into another folder from the LOG
   directory, where its files are not; found by an external review in
   September 2026 and reproduced on SQL Server 2025 CU7. */
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
/* The separator is either slash. SQL Server on Linux reports its files as
   /var/opt/mssql/log/..., and the earlier version looked for a backslash only:
   finding none, it kept the whole current file name as the "directory" and
   read '..._0_134349356263740000.xelclaude_sonde_bpr*.xel', which matches
   nothing. The capture then read as present and empty on an instance that held
   four reports. Measured on SQL Server 2025 CU7 on Linux, September 2026.
   PATINDEX on the reversed name finds the LAST separator of either kind; a
   backslash inside a LIKE bracket is a literal. */
DECLARE @configured_stem nvarchar(400) = @stem;
IF @stem IS NOT NULL
BEGIN
    SET @stem = REVERSE(LEFT(REVERSE(@stem), CASE WHEN PATINDEX('%[\/]%', REVERSE(@stem)) = 0
                                                  THEN LEN(@stem)
                                                  ELSE PATINDEX('%[\/]%', REVERSE(@stem)) - 1 END));
    SET @path = CASE
        WHEN @current IS NULL THEN @configured_stem + N'*.xel'
        ELSE LEFT(@current, LEN(@current) - PATINDEX('%[\/]%', REVERSE(@current)) + 1) + @stem + N'*.xel'
    END;
END;

DECLARE @reports TABLE (
    event_time datetime2(3),
    report     nvarchar(max),
    file_name  nvarchar(400),
    file_offset bigint,
    monitor_loop       bigint,
    blocked_spid       int,
    blocked_owner_id   bigint,
    blocked_wait_ms    bigint,
    blocked_trancount  int,
    lock_mode          nvarchar(20),
    wait_resource      nvarchar(256),
    blocking_spid      int,
    blocking_status    nvarchar(30),
    blocking_trancount int
);

/* The read itself, guarded. Everything that can go wrong here goes wrong at the
   file system — a path the service account cannot reach, a share withdrawn, a
   file being rolled at this instant — and none of it is a reason for the whole
   collection to fail. The error is data. */
IF @path IS NOT NULL
BEGIN
    BEGIN TRY
        INSERT INTO @reports (event_time, report, file_name, file_offset,
                              monitor_loop, blocked_spid, blocked_owner_id, blocked_wait_ms,
                              blocked_trancount, lock_mode, wait_resource,
                              blocking_spid, blocking_status, blocking_trancount)
        SELECT x.value('(/event/@timestamp)[1]', 'datetime2(3)'),
               CAST(x.query('(/event/data[@name="blocked_process"]/value/*)[1]') AS nvarchar(max)),
               t.file_name,
               t.file_offset,
               d.value('(@monitorLoop)[1]',                        'bigint'),
               d.value('(blocked-process/process/@spid)[1]',       'int'),
               d.value('(blocked-process/process/@ownerId)[1]',    'bigint'),
               d.value('(blocked-process/process/@waittime)[1]',   'bigint'),
               d.value('(blocked-process/process/@trancount)[1]',  'int'),
               d.value('(blocked-process/process/@lockMode)[1]',   'nvarchar(20)'),
               d.value('(blocked-process/process/@waitresource)[1]', 'nvarchar(256)'),
               d.value('(blocking-process/process/@spid)[1]',      'int'),
               d.value('(blocking-process/process/@status)[1]',    'nvarchar(30)'),
               d.value('(blocking-process/process/@trancount)[1]', 'int')
        FROM sys.fn_xe_file_target_read_file(@path, NULL, NULL, NULL) AS t
        CROSS APPLY (SELECT CAST(t.event_data AS xml)) AS e(x)
        OUTER APPLY x.nodes('/event/data[@name="blocked_process"]/value/blocked-process-report') AS b(d)
        WHERE t.object_name = 'blocked_process_report'
        OPTION (RECOMPILE, MAXDOP 1);
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

/* One row per report, whether or not its XML came back. Most recent first.

   in_episode ranks the reports of one episode by wait, longest first; the
   episode's representative is its 1. sample_rank then ranks the
   representatives among themselves, longest wait first, and is NULL on every
   other report, so the cap below can only ever keep representatives. */
WITH ranked AS (
    SELECT r.*,
           ROW_NUMBER() OVER (ORDER BY r.event_time DESC)       AS rank_recent,
           ROW_NUMBER() OVER (PARTITION BY r.blocked_spid, r.blocked_owner_id,
                                           r.blocking_spid, r.wait_resource
                              ORDER BY r.blocked_wait_ms DESC, r.event_time DESC)
                                                                AS in_episode
    FROM @reports AS r
), sampled AS (
    SELECT k.*,
           CASE WHEN k.in_episode = 1
                THEN ROW_NUMBER() OVER (PARTITION BY CASE WHEN k.in_episode = 1 THEN 0 ELSE 1 END
                                        ORDER BY k.blocked_wait_ms DESC, k.event_time DESC)
           END                                                  AS sample_rank
    FROM ranked AS k
)
SELECT
    s.rank_recent                                                 AS [report.rank],
    COUNT(*)     OVER ()                                          AS [report.count],
    s.sample_rank                                                 AS [report.sample_rank],
    CONVERT(varchar(23), s.event_time, 126)                       AS [occurred_at],
    s.file_name                                                   AS [file_name],
    s.monitor_loop                                                AS [monitor_loop],
    s.blocked_spid                                                AS [blocked.spid],
    s.blocked_owner_id                                            AS [blocked.owner_id],
    s.blocked_wait_ms                                             AS [blocked.wait_ms],
    s.blocked_trancount                                           AS [blocked.trancount],
    s.lock_mode                                                   AS [blocked.lock_mode],
    s.wait_resource                                               AS [blocked.wait_resource],
    s.blocking_spid                                               AS [blocking.spid],
    s.blocking_status                                             AS [blocking.status],
    s.blocking_trancount                                          AS [blocking.trancount],
    CASE WHEN DATALENGTH(s.report) <= 1048576
          AND s.sample_rank <= 500
         THEN s.report END                                        AS [report],
    DATALENGTH(s.report)                                          AS [report_bytes]
FROM sampled AS s
ORDER BY s.event_time DESC
OPTION (RECOMPILE, MAXDOP 1);
