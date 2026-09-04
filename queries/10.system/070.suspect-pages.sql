-- @scope:       instance
-- @resultsets:  root:object, pages:array
-- @permissions: CONNECT, MSDB READ
-- @timeout:     60
--
-- Pages the engine could not read, or read and found wrong.
--
-- Why this collector exists, for three lines of SQL. Every other corruption
-- signal in this archive is indirect: an error in the log, a failed job, a
-- backup that did not run. This table is direct. A row in it means SQL Server
-- met an 823, an 824 or a bad checksum on a specific page of a specific file,
-- and recorded it. Nothing else in the corpus reported it.
--
-- AN EMPTY TABLE IS THE NORMAL CASE AND MUST READ AS ONE. The count is
-- projected in the root so that zero is a measurement rather than a file that
-- looks truncated — the same reason every other count in this corpus sits
-- beside its list.
--
-- event_type IS TRANSLATED, because the numbers are meaningless without the
-- table and two of them are good news. 4 means DBCC repaired the page and 7
-- means it was restored; reading either as an outstanding problem would be
-- wrong, and reading 1 or 2 as historical would be worse.
--
-- THE TABLE IS NEVER PURGED AUTOMATICALLY. A row can describe an incident dealt
-- with two years ago and still be there, so last_update_date is what decides
-- whether it is current — and clearing the table is a write, which this tool
-- does not do. A reader finding old rows should check the date before alarming
-- anyone.
--
-- IT ALSO HAS A CEILING. msdb keeps at most 1000 rows here; past that the
-- engine stops recording. A table sitting exactly at 1000 is therefore not a
-- count, it is a saturation, and the root says so by projecting the limit
-- beside the count.
--
-- NO JUDGEMENT IS APPLIED. The severity of a corrupt page depends on which
-- object it belongs to and whether a good backup predates it, neither of which
-- is decided here.
--
-- SQL Server 2012 is the floor. msdb.dbo.suspect_pages is 2005.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT (SELECT COUNT(*) FROM msdb.dbo.suspect_pages)              AS [count],
       /* Not a threshold: the engine's own hard limit. At 1000 the table has
          stopped recording and the count no longer means what it says. */
       1000                                                       AS [msdb_row_limit],
       CONVERT(varchar(23), (SELECT MIN(last_update_date) FROM msdb.dbo.suspect_pages), 126)
                                                                  AS [oldest],
       CONVERT(varchar(23), (SELECT MAX(last_update_date) FROM msdb.dbo.suspect_pages), 126)
                                                                  AS [newest],
       CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at]
OPTION (RECOMPILE, MAXDOP 1);

SELECT DB_NAME(sp.database_id)                                    AS [database],
       sp.database_id                                             AS [database_id],
       sp.file_id                                                 AS [file_id],
       sp.page_id                                                 AS [page_id],
       sp.event_type                                              AS [event_type],
       CASE sp.event_type
            WHEN 1 THEN 'error 823: the operating system could not read the page'
            WHEN 2 THEN 'error 824: bad checksum or torn page'
            WHEN 3 THEN 'restore attempted on a page marked bad'
            WHEN 4 THEN 'repaired: DBCC fixed the page'
            WHEN 5 THEN 'the page was deallocated by DBCC'
            WHEN 7 THEN 'restored: the page was replaced from a backup'
            ELSE 'unknown event type' END                         AS [event],
       /* 4, 5 and 7 are resolutions rather than problems. Projected as a flag so
          a reader can separate the two populations without parsing prose. */
       CAST(CASE WHEN sp.event_type IN (4, 5, 7) THEN 1 ELSE 0 END AS int)
                                                                  AS [is_resolved],
       sp.error_count                                             AS [error_count],
       CONVERT(varchar(23), sp.last_update_date, 126)             AS [last_update]
FROM msdb.dbo.suspect_pages AS sp
ORDER BY sp.last_update_date DESC
OPTION (RECOMPILE, MAXDOP 1);
