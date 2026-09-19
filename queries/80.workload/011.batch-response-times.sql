-- @scope:       instance
-- @resultsets:  root:object, buckets:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- How long the batches this instance ran since its last restart took, as a
-- histogram of elapsed time and one of CPU time.
--
-- WHY THIS COLLECTOR EXISTS. Every other throughput figure in the archive is a
-- count or an average: batch requests in 10.system/010.properties, per-query
-- totals in the plan cache and Query Store. An average of 40 ms says nothing
-- about whether a few hundred batches ran for over ten seconds, and a user who
-- complains about "the application freezing" is complaining about the tail,
-- not the mean. The 'Batch Resp Statistics' counters are the only place the
-- engine keeps that shape for the whole instance, whatever the client, with
-- nothing to set up beforehand.
--
-- ONE ROW PER BUCKET, pivoted from the four instance_name values the counter
-- object carries per bucket: Elapsed Time:Requests, Elapsed Time:Total(ms),
-- CPU Time:Requests, CPU Time:Total(ms). The bucket bounds are parsed from
-- counter_name ('Batches >=000005ms & <000010ms'), upper_ms is NULL on the
-- last, open bucket.
--
-- THE TWO HISTOGRAMS ARE INDEPENDENT. A batch is filed once by its elapsed
-- time and once, separately, by its CPU time, so the elapsed and CPU columns
-- of one row do not describe the same batches. Measured on SQL Server 2025:
-- the lowest bucket held 17,331 batches by elapsed time and 17,440 by CPU
-- time, and the two histograms did not even add up to the same total, 44,297
-- against 44,477, so they are not two views of one population. A reader who
-- divides cpu_total_ms by elapsed_total_ms on a row to get "the share of time
-- spent waiting" gets a number that means nothing. The
-- comparison that holds is between the two distributions as a whole.
--
-- THE WINDOW IS SINCE THE LAST RESTART, an upper bound as for
-- 80.workload/010.wait-stats: the root carries the instance start and the
-- seconds since, so a histogram of three hours is never read as one of three
-- months.
--
-- object_name is matched with LIKE, which covers the 'MSSQL$<instance>:'
-- prefix of a named instance. counts.buckets at 0 means the counter object
-- was not there at all, not that no batch ran: an instance that ran nothing
-- still reports every bucket, at zero.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @buckets TABLE (
    [lower_ms]          int PRIMARY KEY,
    [upper_ms]          int NULL,
    [elapsed_requests]  bigint,
    [elapsed_total_ms]  bigint,
    [cpu_requests]      bigint,
    [cpu_total_ms]      bigint);

INSERT INTO @buckets
SELECT CAST(SUBSTRING(c.name, 11, 6) AS int),
       CASE WHEN CHARINDEX('<', c.name) > 0
            THEN CAST(SUBSTRING(c.name, CHARINDEX('<', c.name) + 1, 6) AS int) END,
       SUM(CASE WHEN c.measure = 'Elapsed Time:Requests'  THEN c.value ELSE 0 END),
       SUM(CASE WHEN c.measure = 'Elapsed Time:Total(ms)' THEN c.value ELSE 0 END),
       SUM(CASE WHEN c.measure = 'CPU Time:Requests'      THEN c.value ELSE 0 END),
       SUM(CASE WHEN c.measure = 'CPU Time:Total(ms)'     THEN c.value ELSE 0 END)
FROM (SELECT RTRIM(pc.counter_name)  AS name,
             RTRIM(pc.instance_name) AS measure,
             pc.cntr_value           AS value
      FROM sys.dm_os_performance_counters AS pc
      WHERE pc.object_name LIKE '%Batch Resp Statistics%'
        AND pc.counter_name LIKE 'Batches >=%') AS c
GROUP BY c.name
OPTION (RECOMPILE, MAXDOP 1);

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
       CONVERT(varchar(23), si.sqlserver_start_time, 126)           AS [instance_start],
       DATEDIFF(second, si.sqlserver_start_time, GETDATE())         AS [seconds_since_instance_start],
       (SELECT COUNT(*) FROM @buckets)                              AS [counts.buckets],
       (SELECT ISNULL(SUM(elapsed_requests), 0) FROM @buckets)      AS [counts.batches],
       (SELECT ISNULL(SUM(elapsed_requests), 0) FROM @buckets
         WHERE lower_ms >= 1000)                                    AS [counts.batches_1s_or_more],
       (SELECT ISNULL(SUM(elapsed_requests), 0) FROM @buckets
         WHERE lower_ms >= 10000)                                   AS [counts.batches_10s_or_more]
FROM sys.dm_os_sys_info AS si
OPTION (RECOMPILE, MAXDOP 1);

SELECT b.lower_ms, b.upper_ms,
       b.elapsed_requests, b.elapsed_total_ms,
       b.cpu_requests, b.cpu_total_ms
FROM @buckets AS b
ORDER BY b.lower_ms
OPTION (RECOMPILE, MAXDOP 1);
