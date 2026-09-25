-- @scope:       instance
-- @resultsets:  root:object, latches:array, spinlocks:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- Where the engine contended with itself below the lock manager: latch classes
-- and spinlock types, cumulative since the instance started.
--
-- WHY THIS COLLECTOR EXISTS, AND WHY THE WAIT STATISTICS ARE NOT ENOUGH.
-- 80.workload/010.wait-stats.sql reports every wait type including the
-- LATCH_EX, LATCH_SH and PAGELATCH_* families, so an archive can already say
-- that sessions waited on latches and for how long. What it cannot say is on
-- WHICH latch, and that is the whole diagnosis. A latch protects one in-memory
-- structure, and the class names the structure: LOG_MANAGER is not the same
-- problem as ACCESS_METHODS_HOBT_VIRTUAL_ROOT, and neither is fixed the way
-- the other is. A wait type says the engine queued; a latch class says what
-- it queued for.
--
-- Two of this project's own topics send the reader to sys.dm_os_latch_stats
-- for exactly this reason, and until now the archive could not follow them:
-- operational-hygiene/unnecessary-scheduled-instance-restarts and
-- performance-indexing/missing-index-on-temp-table. A corpus that tells a
-- reader to look at a view it does not collect is a corpus with a hole in it.
--
-- SPINLOCKS RIDE ALONG BECAUSE THEY ARE THE SAME KIND OF COUNTER. Same scope,
-- same permission, same reset, same window caveat, and a spinlock is the
-- degenerate case of the same idea: a structure held so briefly that queuing
-- would cost more than spinning. Collecting them apart would duplicate the
-- window logic and the reset warning for no gain. What separates them in
-- practice is that a spinlock costs CPU rather than wall-clock time, so
-- collisions and backoffs matter where wait_time_ms would for a latch.
--
-- NOTHING IS FILTERED BY IMPORTANCE, for the reason 010.wait-stats.sql gives
-- at length. The point is worth repeating here because this is precisely where
-- the temptation is strongest. Microsoft's own Waits-and-Latches view ships an
-- exclusion of the BUFFER class, commented out; measured on both lab instances,
-- BUFFER is 8 641 ms of an 8 662 ms total on one and 8 548 of 8 551 on the
-- other, so excluding it would leave a list that looks clean and hides the
-- only class that moved. Whether a busy BUFFER latch is normal for a workload
-- belongs to the analysis, which can see the buffer pool and the I/O latency
-- beside it. Rows are dropped only when the class never occurred at all.
--
-- THAT FILTER IS WORTH THE LINE IT COSTS. Measured on 16.0.4265.3 and
-- 17.0.4065.4: 181 and 183 latch classes exist, 8 of them had ever waited; 481
-- and 530 spinlock types exist, 43 and 26 had ever collided. So the filter
-- removes about nineteen rows in twenty and loses no fact.
--
-- THE WINDOW IS AN UPPER BOUND, NOT A MEASUREMENT, and for a sharper reason
-- than in 010.wait-stats.sql. DBCC SQLPERF('sys.dm_os_latch_stats', CLEAR)
-- resets these counters without restarting anything, and unlike the wait
-- statistics there is no widely-run tool that leaves them alone: anyone who has
-- been troubleshooting latch contention has probably cleared them. So a small
-- number here is ambiguous in a way a small number of waits is not. The column
-- is named seconds_since_instance_start rather than window for that reason.
--
-- Durations are compared in SECONDS, never milliseconds, for the overflow
-- reason 010.wait-stats.sql documents.
--
-- NO JUDGEMENT IS APPLIED. A latch class with time on it is not a defect. Every
-- instance that does any work at all shows BUFFER, and an instance showing
-- nothing at all has either just restarted or just been cleared.
--
-- SQL Server 2012 is the floor. sys.dm_os_latch_stats and
-- sys.dm_os_spinlock_stats both predate it. Not collected for that reason:
--   sys.dm_os_wait_stats             (80.workload/010.wait-stats.sql has it)
--   sys.dm_os_waiting_tasks          (an instant of a queue, usually empty on a
--     single-pass collection, and a line of live diagnosis rather than a fact
--     about the period)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT si.sqlserver_start_time                                  AS [instance_start],
       DATEDIFF(second, si.sqlserver_start_time, GETDATE())     AS [seconds_since_instance_start],
       CONVERT(varchar(23), SYSDATETIME(), 126)                 AS [collected_at],
       /* Both denominators, so a reader can see how much of each view the
          arrays below represent without counting rows.

          They live under counts and not under latches and spinlocks, which
          would have read better. Those two prefixes are the names of the two
          result sets, and a root column that claims a top-level key belonging
          to an array makes the encoder refuse the whole document: the
          collector would have run, reported success and written nothing. The
          corpus lint catches it, which is the only reason this file is not
          shaped that way. */
       (SELECT COUNT(*) FROM sys.dm_os_latch_stats)             AS [counts.latch_classes_known],
       (SELECT COUNT(*) FROM sys.dm_os_latch_stats
         WHERE waiting_requests_count > 0)                      AS [counts.latch_classes_waited],
       (SELECT SUM(l.wait_time_ms) FROM sys.dm_os_latch_stats AS l)
                                                                AS [counts.latch_total_wait_ms],
       (SELECT COUNT(*) FROM sys.dm_os_spinlock_stats)          AS [counts.spinlock_types_known],
       (SELECT COUNT(*) FROM sys.dm_os_spinlock_stats
         WHERE collisions > 0)                                  AS [counts.spinlock_types_collided],
       (SELECT SUM(s.spins) FROM sys.dm_os_spinlock_stats AS s) AS [counts.spinlock_total_spins],
       (SELECT SUM(s.backoffs) FROM sys.dm_os_spinlock_stats AS s)
                                                                AS [counts.spinlock_total_backoffs]
FROM sys.dm_os_sys_info AS si
OPTION (RECOMPILE, MAXDOP 1);

/* Every latch class that has ever waited, complete and unranked. The list is
   short by construction, so there is no cap: capping it would be the same
   judgement the header refuses. */
SELECT
    l.latch_class                                               AS [latch_class],
    l.waiting_requests_count                                    AS [waits],
    l.wait_time_ms                                              AS [wait_ms],
    l.max_wait_time_ms                                          AS [max_wait_ms],
    -- The average is projected rather than left to the reader because the two
    -- shapes it separates call for opposite readings: a class with a million
    -- waits averaging a tenth of a millisecond is the workload breathing, and
    -- one with forty waits averaging two seconds is an incident.
    CAST(CASE WHEN l.waiting_requests_count > 0
              THEN l.wait_time_ms * 1.0 / l.waiting_requests_count
         END AS DECIMAL(18,3))                                  AS [avg_wait_ms]
FROM sys.dm_os_latch_stats AS l
WHERE l.waiting_requests_count > 0 OR l.wait_time_ms > 0
ORDER BY l.wait_time_ms DESC, l.waiting_requests_count DESC
OPTION (RECOMPILE, MAXDOP 1);

/* Every spinlock type that has ever collided. spins_per_collision is the
   engine's own ratio and is kept as it gives it: a high value means each
   collision spun a long time before giving up, which is the shape that burns
   CPU without appearing anywhere in the wait statistics. */
SELECT
    s.name                                                      AS [spinlock],
    s.collisions                                                AS [collisions],
    s.spins                                                     AS [spins],
    CAST(s.spins_per_collision AS DECIMAL(18,3))                AS [spins_per_collision],
    s.sleep_time                                                AS [sleep_time],
    -- A backoff is the engine giving up on spinning and yielding. Collisions
    -- without backoffs are cheap; backoffs are where the CPU went.
    s.backoffs                                                  AS [backoffs]
FROM sys.dm_os_spinlock_stats AS s
WHERE s.collisions > 0 OR s.backoffs > 0
ORDER BY s.backoffs DESC, s.spins DESC
OPTION (RECOMPILE, MAXDOP 1);
