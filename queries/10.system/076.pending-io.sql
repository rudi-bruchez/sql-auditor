-- @scope:       instance
-- @resultsets:  root:object, pending:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- I/O requests waiting to be completed at the moment of collection, by
-- database and file.
--
-- Why this collector exists. The SQL Server Assessment ruleset flags pending
-- disk I/O requests, and the number only means something attached to the file
-- it belongs to.
--
-- sys.dm_io_pending_io_requests HAS NEITHER file_id NOR database_id — asking
-- for either fails with Msg 207. The attribution goes through
-- sys.dm_io_virtual_file_stats(NULL, NULL), whose file_handle joins to the
-- pending request's io_handle. That join is the documented contract of this
-- file: it was verified under VIEW SERVER STATE on a 2022 container, and it
-- is the only route to per-file attribution without a permission this grant
-- script will never ask for. Should a future version break the handle match,
-- the documented fallback is an instance-level [pending_count] alone — the
-- Microsoft probe itself tests only EXISTS, so the aggregate answers the rule.
--
-- A FROZEN COLLECTION CAN MISS A PEAK, and the rule built on this data stays
-- prudent for that reason. Pending I/O is a moment: a burst that came and
-- went between two runs leaves no trace here, and a quiet array proves the
-- moment was quiet, nothing more. io_pending_ms_ticks carries the age of the
-- oldest request in the group, and max is reported because one aged request
-- is the finding, not the average.
--
-- AN EMPTY ARRAY IS THE EXPECTED ANSWER. A healthy instance has no pending
-- I/O at the instant of collection; zero rows is the reading, not a failure.
-- The root object carries the instance-wide total so a zero there is checked
-- against the array, not assumed.
--
-- NO JUDGEMENT IS APPLIED. A pending count is a fact at an instant; what count
-- indicts the storage is a ruleset question.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       (SELECT COUNT_BIG(*)
          FROM sys.dm_io_pending_io_requests)                      AS [pending_total]
OPTION (RECOMPILE, MAXDOP 1);

SELECT vfs.database_id                                            AS [database_id],
       vfs.file_id                                                AS [file_id],
       COUNT_BIG(*)                                               AS [pending_count],
       MAX(p.io_pending_ms_ticks)                                 AS [max_pending_ms]
FROM sys.dm_io_pending_io_requests AS p
JOIN sys.dm_io_virtual_file_stats(NULL, NULL) AS vfs
  ON p.io_handle = vfs.file_handle
GROUP BY vfs.database_id, vfs.file_id
ORDER BY [pending_count] DESC
OPTION (RECOMPILE, MAXDOP 1);
