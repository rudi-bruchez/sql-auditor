-- @scope:       database
-- @resultsets:  root:object, intervals:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     120
-- @min_version: 13
--
-- The database's load over time: one row per Query Store interval, over the
-- whole retained history, with the executions, duration, CPU and reads of
-- everything the store recorded in that interval. No query text, no query id,
-- no plan: the totals of an interval are all this file takes.
--
-- It exists for the question of WHEN. Which hours of the day and which days
-- of the week carry the load, where the quiet hours are that a backup, an
-- index rebuild or a statistics update could go, and whether a slow night
-- reported by the client coincides with a peak or with nothing. The other
-- histories in the corpus cannot place a peak over a month: the scheduler
-- ring of 10.system/043.cpu-neighbours.sql holds 256 minutes, and the ring
-- read by 10.system/064.server-diagnostics.sql about a day. The Query Store
-- keeps thirty days by default (stale_query_threshold_days), and
-- 80.workload/021.query-store-detail.sql reads its intervals only for the
-- queries it selected, behind a flag.
--
-- THE GRAIN IS THE STORE'S OWN. An interval lasts INTERVAL_LENGTH_MINUTES
-- (1, 5, 10, 15, 30, 60 or 1440; 60 by default), projected in the root as
-- config.interval_minutes. A change of that option applies to new intervals
-- only, so a retained history can mix lengths, and each row carries its own
-- minutes. Nothing is re-bucketed here: folding fifteen-minute intervals into
-- hours, or hours into days of the week, is analysis.
--
-- THE LISTING IS CAPPED at the 1 488 most recent intervals, and the root says
-- how many the store holds (window.intervals) and from when
-- (window.oldest_interval). 1 488 is 62 days of hourly intervals: a store on
-- the default settings, thirty days of sixty-minute intervals or 720 rows, is
-- never cut, and neither is one kept twice as long. At fifteen minutes the cap
-- covers fifteen and a half days, at one minute about a day; a listing of
-- exactly listing_cap rows below a larger window.intervals is a cut one, and
-- its first row says where it starts. A row is about 320 bytes of JSON
-- (measured), so a database costs at most some 500 KB of archive. The cap keeps the newest
-- intervals because a quiet hour has to hold now to be worth scheduling in.
-- Under capture mode ALL the collector's own statements are recorded, and can
-- open a new interval between the root and the listing: the listing may then
-- hold one interval more than window.intervals counted.
--
-- THE TOTALS LEAVE OUT NESTED QUERIES, with the rule of
-- 80.workload/020.query-store.sql and for its reason: a query that belongs to
-- a scalar or multi-statement table-valued function, or to a trigger, is
-- recorded once as its own query and once inside the statement that called
-- it. Measured for this file on SQL Server 2025 CU7, 3 October 2026, with a
-- scalar function declared INLINE = OFF whose body runs a COUNT(*), called
-- 4 200 times over five one-minute intervals: the callers carried 19 003 ms of
-- CPU and the function's own query 18 872 ms, the same time twice. The naive
-- sum over every query was 39 567 ms, this file's intervals added up to
-- 20 679 ms and 020's totals.cpu_ms to 20 677 ms, the difference being the
-- collector's own statements recorded in between. excluded.nested_cpu_ms
-- carries, per interval, what was left out, so the naive sum can be rebuilt.
-- The object is looked up in sys.objects now; a query of a dropped object
-- cannot be classed and stays in the totals, as in 020.
--
-- EXECUTION TYPES ARE KEPT APART IN THE COUNTS AND SUMMED IN THE RESOURCES.
-- The store keeps one runtime row per plan, interval and execution_type (0
-- Regular, 3 Aborted, 4 Exception). An execution stopped by a client timeout
-- or by an error still used its CPU and its reads in that interval, so the
-- duration, CPU and reads below are over every type: leaving them out would
-- make an interval where everything timed out look quiet. The executions are
-- split, so a peak made of timeouts is told from a peak of work.
-- 80.workload/026.query-store-interrupted.sql names the queries.
--
-- TIMES ARE PROJECTED AS STORED. start_time and end_time are datetimeoffset,
-- carrying the offset they were recorded with, and nothing converts them to
-- any zone: an hour of the day is read in that offset. collected_at is
-- SYSDATETIMEOFFSET(), so the server's current offset is in the same file.
-- The newest interval is usually still open, its totals partial, and is
-- flagged so a reader does not take it for a dip.
--
-- WHAT IT CANNOT SEE. Under the capture mode AUTO, the default from 2019, a
-- query is recorded only once it passes a threshold of executions or CPU, so
-- a light ad hoc workload is under-counted, and a run of short timeouts can be
-- missed; state.capture_mode is in the root. Nothing outside this database's
-- store is counted: other databases, and work that is not a query at all
-- (backups, DBCC, the ghost cleanup). A store in READ_ONLY records nothing
-- while it stays so, whether it filled or hit its memory limit (both seen),
-- and the profile goes silent from then on; state.actual says so.
-- Memory grants are not summed: a sum of per-execution maxima is not a volume.
--
-- Cost: one pass over sys.query_store_runtime_stats grouped by interval, and
-- one over sys.query_store_query to class each query, with no read of the
-- query text. Measured on the lab store of 41 052 runtime rows and 23 029
-- queries: 270 ms of CPU, where 020's root totals, the same pass with the
-- same classification, took 550 ms on the same store. A lab store is small;
-- the 120-second timeout is the bound on a large one.
--
-- No @discloses: no statement text and no identifier of a query leave the
-- server. Like 023 and 026 it reads the whole retained history and takes no
-- window parameter.
--
-- SQL Server 2016 is the floor, where the Query Store appears. Every column
-- read here exists there.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

/* ───────── root ─────────
   A LEFT JOIN from sys.databases, as in 026: a database whose store was never
   enabled has no options row, and a root with no row is an encoder error.
   The counts are 0 on an empty store and NULL only when there is no store. */
SELECT DB_NAME()                                                  AS [database],
       SYSDATETIMEOFFSET()                                        AS [collected_at],
       o.actual_state_desc                                        AS [state.actual],
       o.desired_state_desc                                       AS [state.desired],
       o.query_capture_mode_desc                                  AS [state.capture_mode],
       o.interval_length_minutes                                  AS [config.interval_minutes],
       o.stale_query_threshold_days                               AS [config.stale_threshold_days],
       w.oldest_interval                                          AS [window.oldest_interval],
       w.newest_interval                                          AS [window.newest_interval],
       CASE WHEN o.actual_state_desc IS NOT NULL THEN ISNULL(w.intervals, 0) END AS [window.intervals],
       1488                                                       AS [listing_cap]
FROM sys.databases AS d
LEFT JOIN sys.database_query_store_options AS o ON 1 = 1
OUTER APPLY (SELECT MIN(i.start_time) AS oldest_interval, MAX(i.end_time) AS newest_interval,
                    COUNT(*) AS intervals
             FROM sys.query_store_runtime_stats_interval AS i) AS w
WHERE d.database_id = DB_ID()
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── intervals ─────────
   The nested class is decided once per query, with 020's predicate verbatim,
   and only the runtime rows of the capped intervals are folded, so a store
   retained beyond the cap is not aggregated for intervals never returned. The cap picks the newest intervals; the rows come out oldest first,
   in the order a profile is read. An interval the store holds with no
   runtime row is listed with zeros: seen on the lab for the interval that
   had just opened when the collector ran. An hour in which nothing ran at
   all has no interval and no row, so a missing hour is a zero, not a gap
   in the collection. */
WITH cls AS (
    SELECT q.query_id,
           CASE WHEN q.object_id = 0 THEN 0
                WHEN ob.type IN ('FN', 'TF', 'TR') THEN 1
                WHEN ob.object_id IS NOT NULL THEN 0
           END                                                    AS nested
    FROM sys.query_store_query AS q
    LEFT JOIN sys.objects AS ob ON ob.object_id = q.object_id
),
capped AS (
    SELECT TOP (1488) i.runtime_stats_interval_id, i.start_time, i.end_time
    FROM sys.query_store_runtime_stats_interval AS i
    ORDER BY i.start_time DESC
),
perInterval AS (
    SELECT rs.runtime_stats_interval_id                           AS interval_id,
           SUM(CASE WHEN ISNULL(c.nested, 0) = 0 AND rs.execution_type = 0 THEN rs.count_executions END) AS regular,
           SUM(CASE WHEN ISNULL(c.nested, 0) = 0 AND rs.execution_type = 3 THEN rs.count_executions END) AS aborted,
           SUM(CASE WHEN ISNULL(c.nested, 0) = 0 AND rs.execution_type = 4 THEN rs.count_executions END) AS exception,
           SUM(CASE WHEN ISNULL(c.nested, 0) = 0 THEN rs.avg_duration * rs.count_executions END)         AS duration_us,
           SUM(CASE WHEN ISNULL(c.nested, 0) = 0 THEN rs.avg_cpu_time * rs.count_executions END)         AS cpu_us,
           SUM(CASE WHEN ISNULL(c.nested, 0) = 0
                    THEN CAST(rs.avg_logical_io_reads * rs.count_executions AS bigint) END)              AS logical_reads,
           SUM(CASE WHEN ISNULL(c.nested, 0) = 0
                    THEN CAST(rs.avg_physical_io_reads * rs.count_executions AS bigint) END)             AS physical_reads,
           SUM(CASE WHEN ISNULL(c.nested, 0) = 0
                    THEN CAST(rs.avg_logical_io_writes * rs.count_executions AS bigint) END)             AS logical_writes,
           SUM(CASE WHEN c.nested = 1 THEN rs.count_executions END)                                      AS nested_executions,
           SUM(CASE WHEN c.nested = 1 THEN rs.avg_cpu_time * rs.count_executions END)                    AS nested_cpu_us
    FROM sys.query_store_runtime_stats AS rs
    JOIN sys.query_store_plan          AS p ON p.plan_id = rs.plan_id
    LEFT JOIN cls                      AS c ON c.query_id = p.query_id
    WHERE rs.runtime_stats_interval_id IN (SELECT runtime_stats_interval_id FROM capped)
    GROUP BY rs.runtime_stats_interval_id
)
SELECT c.start_time                                               AS [start_time],
       c.end_time                                                 AS [end_time],
       DATEDIFF(minute, c.start_time, c.end_time)                 AS [minutes],
       CASE WHEN c.end_time > SYSDATETIMEOFFSET() THEN 1 ELSE 0 END AS [open],
       ISNULL(x.regular, 0)                                       AS [executions.regular],
       ISNULL(x.aborted, 0)                                       AS [executions.aborted],
       ISNULL(x.exception, 0)                                     AS [executions.exception],
       CAST(ISNULL(x.duration_us, 0) / 1000.0 AS decimal(18,1))   AS [duration_ms],
       CAST(ISNULL(x.cpu_us, 0) / 1000.0 AS decimal(18,1))        AS [cpu_ms],
       ISNULL(x.logical_reads, 0)                                 AS [logical_reads],
       ISNULL(x.physical_reads, 0)                                AS [physical_reads],
       ISNULL(x.logical_writes, 0)                                AS [logical_writes],
       ISNULL(x.nested_executions, 0)                             AS [excluded.nested_executions],
       CAST(ISNULL(x.nested_cpu_us, 0) / 1000.0 AS decimal(18,1)) AS [excluded.nested_cpu_ms]
FROM capped AS c
LEFT JOIN perInterval AS x ON x.interval_id = c.runtime_stats_interval_id
ORDER BY c.start_time
OPTION (RECOMPILE, MAXDOP 1);
