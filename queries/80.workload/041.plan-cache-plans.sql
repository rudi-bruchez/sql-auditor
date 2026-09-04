-- @scope:       instance
-- @resultsets:  root:object, plans:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @requires_flag: plan_cache_plans
-- @writer:      plan-cache-plans
-- @timeout:     300
--
-- Execution plans when the Query Store is off.
--
-- 021.query-store-detail.sql extracts plans from the Query Store, capped and
-- chosen by a round robin over four metrics. When the Query Store is off, the
-- archive contains NO PLAN AT ALL: the analysis has aggregate counters and no
-- way to see a plan shape, which is the difference between "this procedure is
-- expensive" and "this procedure is expensive because a CTE is expanded five
-- times".
--
-- INSTANCE SCOPE, NOT DATABASE, AND THAT IS A CORRECTION. The plan cache is an
-- instance-wide structure: sys.dm_exec_query_stats has no database_id, and
-- filtering by database means a CROSS APPLY over sys.dm_exec_plan_attributes
-- across the whole cache. A database-scoped file would scan and sort the entire
-- cache once per database — fifty times on a fifty-database instance — and
-- write largely the same plans into each database's directory.
--
-- DEDUPLICATION IS ON plan_handle, NOT ON query_plan_hash, and the first design
-- had this backwards. sys.dm_exec_query_stats holds one row per STATEMENT, and
-- every statement of a procedure shares one plan_handle while carrying its own
-- query_plan_hash. Deduplicating on the hash would select all ten statements of
-- a ten-statement procedure, and sys.dm_exec_query_plan(plan_handle) returns
-- the plan for the whole procedure — so the same XML would be written ten
-- times. The statistics are therefore SUMMED per plan_handle here, and the
-- statement count is projected so a reader knows how many rows went into them.
--
-- FOUR METRICS, TWENTY-FIVE EACH, ONE HUNDRED PLANS. The same argument 021
-- makes: the archive should hold the plans that matter by any of four
-- definitions of mattering — total CPU, total duration, total reads and
-- execution count — rather than by one. A plan ranking well on several is
-- selected once, so the hundred is a ceiling and not a quota.
--
-- THREE THINGS THIS FILE MUST SAY ABOUT ITSELF, and the writer repeats them in
-- the index:
--
--   THE CACHE IS NOT HISTORY. A plan absent from it was evicted, or has not run
--   since the last restart, or was never compiled. 021 can say a query was not
--   among the top N; this file cannot tell that apart from "never ran".
--
--   CACHED PLANS CARRY NO RUNTIME STATISTICS. They are compiled plans — the
--   same limitation as the Query Store plans — and an operator cost in one is
--   an estimate the optimiser made, never a measurement of what happened.
--
--   sys.dm_exec_query_plan RETURNS NULL RATHER THAN RAISING for a plan that is
--   too large or contains an XML-invalid construct. A NULL is written as a NULL
--   and counted in the index, never silently dropped.
--
-- THE TEXT IS RESOLVED FROM THE CACHE, AND THAT IS A DISCLOSURE OF ITS OWN.
-- Cached text can carry the literal parameter values a statement was written
-- with, where Query Store text is parameterised — so this is not the same
-- disclosure as either the Query Store's or a live session's, and it has its
-- own flag, its own entry in the manifest's collected kinds and its own
-- paragraph there. The statement is cut out of the batch with the offsets, and
-- the heaviest statement of the plan is the one kept: without it the summed
-- statistics could not be attributed to anything a reader can read.
--
-- WHAT IT DELIBERATELY DOES NOT DO. It does not compile a plan for a procedure
-- that has none: obtaining an estimated plan means executing the batch under
-- SHOWPLAN, which needs parameter values the collector cannot invent, compiles
-- on the production instance, and would break the read-only promise. "Every
-- plan we can get" means "every plan already materialised in the cache". And it
-- does not collect all procedures: eight hundred procedures whose plans run
-- from 0.5 to 2 MB would produce a multi-gigabyte archive, and the cap is what
-- makes the collector usable at all.
--
-- The two caps below are also Go constants, and
-- TestPlanCacheCapsAreTheSameNumbersInTheCorpus fails on any drift. A Go
-- constant raised above a stale SQL literal would make plan 101 arrive NULL and
-- be reported as a plan the cache does not hold — a false fact about the server.
--
-- SQL Server 2012 is the floor. Nothing read here is newer.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                     AS [collected_at],
       (SELECT COUNT(*) FROM sys.dm_exec_query_stats)               AS [cache.statements],
       (SELECT COUNT(DISTINCT qs.plan_handle)
          FROM sys.dm_exec_query_stats AS qs)                       AS [cache.plans],
       /* The cache resets on restart, on recompilation and under memory
          pressure, so the oldest creation time is the honest floor of what this
          collection can see. A cache minutes old is the finding. */
       (SELECT MIN(qs.creation_time) FROM sys.dm_exec_query_stats AS qs)
                                                                    AS [cache.oldest_plan],
       (SELECT MAX(qs.last_execution_time) FROM sys.dm_exec_query_stats AS qs)
                                                                    AS [cache.newest_execution],
       100                                                          AS [cap.plans],
       25                                                           AS [cap.per_metric],
       4194304                                                      AS [cap.plan_bytes]
OPTION (RECOMPILE, MAXDOP 1);

WITH agg AS (
    /* One row per plan, not per statement. See the header: the statements of a
       procedure share a plan_handle, and the plan is the procedure's. */
    SELECT qs.plan_handle,
           SUM(qs.execution_count)                                  AS execution_count,
           SUM(qs.total_worker_time)                                AS total_worker_time,
           SUM(qs.total_elapsed_time)                               AS total_elapsed_time,
           SUM(qs.total_logical_reads)                              AS total_logical_reads,
           SUM(qs.total_physical_reads)                             AS total_physical_reads,
           SUM(qs.total_logical_writes)                             AS total_logical_writes,
           MIN(qs.creation_time)                                    AS creation_time,
           MAX(qs.last_execution_time)                              AS last_execution_time,
           COUNT(*)                                                 AS statements
    FROM sys.dm_exec_query_stats AS qs
    GROUP BY qs.plan_handle
),
ranked AS (
    SELECT a.*,
           ROW_NUMBER() OVER (ORDER BY a.total_worker_time DESC)    AS r_cpu,
           ROW_NUMBER() OVER (ORDER BY a.total_elapsed_time DESC)   AS r_duration,
           ROW_NUMBER() OVER (ORDER BY a.total_logical_reads DESC)  AS r_reads,
           ROW_NUMBER() OVER (ORDER BY a.execution_count DESC)      AS r_executions
    FROM agg AS a
),
selected AS (
    SELECT r.*
    FROM ranked AS r
    WHERE r.r_cpu <= 25 OR r.r_duration <= 25
       OR r.r_reads <= 25 OR r.r_executions <= 25
),
numbered AS (
    /* The rank is computed in its own step because a window function may not
       appear in a WHERE clause: ranking and filtering on the rank are two
       passes in T-SQL, however much they read as one thought. */
    SELECT s.*,
           ROW_NUMBER() OVER (ORDER BY s.total_worker_time DESC)    AS plan_rank
    FROM selected AS s
)
SELECT s.plan_rank                                                  AS [plan.rank],
       CONVERT(varchar(130), s.plan_handle, 1)                      AS [plan_handle],
       s.statements                                                 AS [statements],
       s.execution_count                                            AS [execution_count],
       s.total_worker_time                                          AS [total_worker_time_us],
       s.total_elapsed_time                                         AS [total_elapsed_time_us],
       s.total_logical_reads                                        AS [total_logical_reads],
       s.total_physical_reads                                       AS [total_physical_reads],
       s.total_logical_writes                                       AS [total_logical_writes],
       s.creation_time                                              AS [creation_time],
       s.last_execution_time                                        AS [last_execution_time],
       s.r_cpu                                                      AS [rank_cpu],
       s.r_duration                                                 AS [rank_duration],
       s.r_reads                                                    AS [rank_reads],
       s.r_executions                                               AS [rank_executions],
       DB_NAME(st.dbid)                                             AS [database_name],
       OBJECT_NAME(st.objectid, st.dbid)                            AS [object_name],
       st.statement_text                                            AS [statement_text],
       /* NULL above the cap rather than absent: plan_bytes is projected either
          way, so the index can say "too large" instead of "not held". */
       DATALENGTH(qp.query_plan)                                    AS [plan_bytes],
       CASE WHEN DATALENGTH(qp.query_plan) <= 4194304
            THEN CONVERT(nvarchar(max), qp.query_plan) END          AS [query_plan]
FROM numbered AS s
/* OUTER, not CROSS: dm_exec_query_plan returns NULL for a plan too large or
   XML-invalid, and dropping the row would turn "we could not render it" into
   "the cache does not hold it". */
OUTER APPLY sys.dm_exec_query_plan(s.plan_handle) AS qp
/* The heaviest statement of the plan, cut out of its batch with the offsets.
   The end offset is -1 for the last statement, which is why the CASE is here
   and not a plain subtraction. */
OUTER APPLY (
    SELECT TOP (1)
           t.dbid,
           t.objectid,
           SUBSTRING(t.text, (qs2.statement_start_offset / 2) + 1,
                     ((CASE qs2.statement_end_offset
                            WHEN -1 THEN DATALENGTH(t.text)
                            ELSE qs2.statement_end_offset END
                       - qs2.statement_start_offset) / 2) + 1)      AS statement_text
    FROM sys.dm_exec_query_stats AS qs2
    CROSS APPLY sys.dm_exec_sql_text(qs2.sql_handle) AS t
    WHERE qs2.plan_handle = s.plan_handle
    ORDER BY qs2.total_worker_time DESC
) AS st
WHERE s.plan_rank <= 100
ORDER BY s.plan_rank
OPTION (RECOMPILE, MAXDOP 1);
