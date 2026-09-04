-- @scope:       instance
-- @resultsets:  root:object, dumps:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- The memory dumps this instance has written, by date, path and size.
--
-- Why this collector exists. A dump means SQL Server hit something it could not
-- handle — an access violation, a non-yielding scheduler, an assertion — and
-- stopped to write its state to disk. Nothing else in this archive says that
-- happened. The error log records the event too, but the log rotates and this
-- view does not, so an instance restarted a few times keeps the dumps and loses
-- the log entries.
--
-- A CLUSTER OF DUMPS IS THE FINDING, not any single one. One dump three years
-- ago is history. Four in one week is an instance repeatedly meeting the same
-- condition, and the dates are what make that visible — which is why they are
-- listed rather than counted.
--
-- ONLY THE METADATA. The path is collected and the file never is, and that is
-- deliberate rather than incidental: a dump contains the process address space,
-- which means page images, statement text and parameter values from every
-- database on the instance. It is the single most revealing artifact a SQL
-- Server produces, and this tool does not transport it. Anyone who needs one
-- sends it to Microsoft support, knowingly.
--
-- NO JUDGEMENT IS APPLIED. A dump is not a verdict on the instance's health.
-- Some are triggered deliberately, and a stack dump from a non-yielding
-- scheduler under a one-off load is not the same event as a repeated access
-- violation.
--
-- SQL Server 2012 is the floor. sys.dm_server_memory_dumps is 2008.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT (SELECT COUNT(*) FROM sys.dm_server_memory_dumps)          AS [count],
       CONVERT(varchar(23), (SELECT MIN(creation_time) FROM sys.dm_server_memory_dumps), 126)
                                                                  AS [oldest],
       CONVERT(varchar(23), (SELECT MAX(creation_time) FROM sys.dm_server_memory_dumps), 126)
                                                                  AS [newest],
       /* Beside the instance start, so a reader can tell dumps written by the
          current run of the service from ones inherited from before it. */
       CONVERT(varchar(23), (SELECT sqlserver_start_time FROM sys.dm_os_sys_info), 126)
                                                                  AS [instance_start],
       CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at]
OPTION (RECOMPILE, MAXDOP 1);

SELECT CONVERT(varchar(23), d.creation_time, 126)                 AS [created_at],
       d.filename                                                 AS [path],
       d.size_in_bytes                                            AS [bytes],
       CAST(d.size_in_bytes / 1048576.0 AS DECIMAL(14,1))         AS [size_mb]
FROM sys.dm_server_memory_dumps AS d
ORDER BY d.creation_time DESC
OPTION (RECOMPILE, MAXDOP 1);
