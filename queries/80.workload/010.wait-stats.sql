-- @scope:       instance
-- @resultsets:  root:object, waits:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- Cumulative wait statistics, and the window they accumulated over.
--
-- Why this collector exists: these counters reset on restart and cannot be
-- reconstructed afterwards. On an instance that had been up 284 days, the wait
-- profile was the single most valuable artifact on the server and there was no
-- way to collect it. A baseline taken the day before a restart is worth more
-- than any amount of analysis done the day after.
--
-- NOTHING IS FILTERED BY IMPORTANCE. The well-known lists of "benign" or
-- "idle" wait types are a judgement, and judgements belong to the analysis
-- layer. Rows are excluded only when the wait type has never occurred at all
-- (no tasks and no time), which removes several hundred rows that say nothing
-- and loses no fact.
--
-- THE WINDOW IS AN UPPER BOUND, NOT A MEASUREMENT. sys.dm_os_wait_stats has no
-- "last cleared" timestamp, and DBCC SQLPERF('sys.dm_os_wait_stats', CLEAR)
-- resets every counter without restarting the instance. So the instance start
-- time bounds the accumulation period from above and may overstate it. The
-- column is named for what it is — seconds since instance start — rather than
-- "window", so no reader can mistake it for a measured duration.
--
-- Durations are compared in SECONDS, never milliseconds. DATEDIFF(millisecond,
-- …) overflows a 32-bit int after about 24 days, which means it fails on
-- exactly the well-kept servers most worth auditing; the same trap sits in
-- sys.dm_os_sys_info.ms_ticks. In seconds the same int reaches 68 years.
--
-- SQL Server 2012 is the floor. sys.dm_os_wait_stats and
-- sys.dm_os_sys_info.sqlserver_start_time both predate it. Not collected for
-- that reason:
--   sys.dm_exec_session_wait_stats   (2016)
--   sys.dm_os_wait_stats.wait_type filtering by help_wait_type (never existed)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT si.sqlserver_start_time                                  AS [instance_start],
       DATEDIFF(second, si.sqlserver_start_time, GETDATE())     AS [seconds_since_instance_start],
       SYSDATETIME()                                            AS [collected_at],
       (SELECT COUNT(*) FROM sys.dm_os_wait_stats AS w
        WHERE w.waiting_tasks_count > 0 OR w.wait_time_ms > 0)  AS [wait_types_observed],
       (SELECT COUNT(*) FROM sys.dm_os_wait_stats)              AS [wait_types_known],
       (SELECT SUM(w.wait_time_ms) FROM sys.dm_os_wait_stats AS w) AS [total_wait_ms]
FROM sys.dm_os_sys_info AS si
OPTION (RECOMPILE, MAXDOP 1);

/* resource_wait_ms is wait_time_ms minus signal_wait_time_ms: the time spent
   waiting for the resource itself rather than for a scheduler after the
   resource arrived. That is subtraction, not interpretation — the split is
   what the two columns mean. */
SELECT w.wait_type                                              AS [wait_type],
       w.waiting_tasks_count                                    AS [waiting_tasks],
       w.wait_time_ms                                           AS [wait_ms],
       w.signal_wait_time_ms                                    AS [signal_wait_ms],
       w.wait_time_ms - w.signal_wait_time_ms                   AS [resource_wait_ms],
       w.max_wait_time_ms                                       AS [max_wait_ms]
FROM sys.dm_os_wait_stats AS w
WHERE w.waiting_tasks_count > 0 OR w.wait_time_ms > 0
ORDER BY w.wait_time_ms DESC
OPTION (RECOMPILE, MAXDOP 1);
