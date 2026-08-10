-- @scope:       instance
-- @resultsets:  root:object, status:object, top_messages:array, by_source:array
-- @permissions: CONNECT, ERROR LOG
-- @timeout:     300
--
-- The current SQL Server error log, summarised.
--
-- Why this collector exists: on a real audit this file was the single richest
-- source on the instance. It dated a restart to the second, showed recovery
-- completing in fifteen seconds, counted 38 897 login failures against one
-- offline database, revealed nightly log-backup failures nobody had seen, and
-- carried the last CHECKDB date for every database. None of it is reachable
-- from any catalog view.
--
-- THE LOG IS NOT DUMPED. A 284-day log held tens of thousands of lines, 94 %
-- of them one repeated message. Shipping it whole would bury the signal and
-- bloat the archive; shipping the tail would miss exactly the recurring
-- failure that matters. So it is aggregated by message prefix, which is what
-- makes a repetition visible as a count instead of as noise.
--
-- Grouping is on LEFT(text, 80) and NOT on a parsed error number, because the
-- log is LOCALISED: the same event reads "Error: 18456, Severity: 14" on an
-- English instance and "Erreur : 18456, Gravité : 14" on a French one. Any
-- parser keyed on English words returns nothing at all on half the estate,
-- silently. A prefix works in every language, and the sample text lets the
-- analysis layer parse afterwards if it wants to.
--
-- Only log file 0 — the current one — is read. Archived logs need one call
-- each and their number is a server setting; the date range is reported so a
-- reader knows what window the counts cover rather than assuming "everything".
--
-- sp_readerrorlog, not xp_readerrorlog. The extended procedure is denied to
-- anyone below sysadmin, while the wrapper is reachable through ownership
-- chaining: on the audited instance a read-only login could execute the first
-- and not the second.
--
-- SQL Server 2012 is the floor. sp_readerrorlog predates it.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

DECLARE @collected bit = 1, @err int = 0, @msg nvarchar(2048) = N'';

CREATE TABLE #log (LogDate datetime, ProcessInfo nvarchar(100), Txt nvarchar(4000));

/* The permission probe answers "may I", not "does it work". A denied EXEC, a
   log being rolled over mid-read, or a text column longer than the table
   accepts all fail here — and an empty summary must never be readable as
   "nothing was logged". status carries the difference. */
BEGIN TRY
    INSERT INTO #log EXEC sys.sp_readerrorlog 0;
END TRY
BEGIN CATCH
    SELECT @collected = 0, @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH;

SELECT COUNT(*)                                                   AS [lines],
       MIN(l.LogDate)                                             AS [oldest],
       MAX(l.LogDate)                                             AS [newest],
       DATEDIFF(second, MIN(l.LogDate), MAX(l.LogDate))           AS [span_seconds],
       COUNT(DISTINCT LEFT(l.Txt, 80))                            AS [distinct_message_prefixes],
       SYSDATETIME()                                              AS [collected_at]
FROM #log AS l
OPTION (RECOMPILE, MAXDOP 1);

SELECT @collected                                                 AS [collected],
       @err                                                       AS [error_number],
       NULLIF(@msg, N'')                                          AS [error_message],
       0                                                          AS [log_file],
       80                                                         AS [grouping_prefix_length],
       40                                                         AS [top_messages_kept]
OPTION (RECOMPILE, MAXDOP 1);

/* TOP 40 by count, and the cut is REPORTED above rather than left implicit: a
   truncated list that does not say it is truncated reads as a complete one. */
SELECT TOP (40)
       LEFT(l.Txt, 80)                                            AS [message_prefix],
       COUNT(*)                                                   AS [occurrences],
       MIN(l.LogDate)                                             AS [first_seen],
       MAX(l.LogDate)                                             AS [last_seen],
       MIN(LEFT(l.Txt, 400))                                      AS [sample]
FROM #log AS l
GROUP BY LEFT(l.Txt, 80)
ORDER BY COUNT(*) DESC
OPTION (RECOMPILE, MAXDOP 1);

/* ProcessInfo is locale-independent and tells a reader which subsystem is
   talking: Logon, Backup, Server, or a session id. Session ids are collapsed
   because their individual values carry nothing once the log is aggregated. */
SELECT CASE WHEN l.ProcessInfo LIKE 'spid%' THEN 'spid' ELSE l.ProcessInfo END AS [source],
       COUNT(*)                                                   AS [occurrences],
       MIN(l.LogDate)                                             AS [first_seen],
       MAX(l.LogDate)                                             AS [last_seen]
FROM #log AS l
GROUP BY CASE WHEN l.ProcessInfo LIKE 'spid%' THEN 'spid' ELSE l.ProcessInfo END
ORDER BY COUNT(*) DESC
OPTION (RECOMPILE, MAXDOP 1);
