-- @scope:       database
-- @resultsets:  root:object, aborted:array, exceptions:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     120
-- @min_version: 13
-- @discloses:   query_text
--
-- The executions that did not finish: stopped by the client (Aborted, which
-- is where query timeouts land) or by an error (Exception). A timeout is
-- decided by the client and leaves no error on the server, no attention event
-- in the default trace or system_health, and sys.dm_exec_query_stats does not
-- count the interrupted execution at all. The Query Store keeps one runtime
-- row per plan, interval and execution_type (0 Regular, 3 Aborted, 4
-- Exception), and nothing else in the corpus ranks on that column: 020, 023
-- and 024 count an aborted execution like a finished one.
-- docs/query-store-interrupted-spec.md is the design and records what it was
-- measured against.
--
-- WHAT THE DURATION SAYS. An aborted execution lasts at most the client's
-- command timeout, and exactly that only when the statement was the first
-- thing the request did: the client clock runs over the whole batch or RPC,
-- the Query Store times the statement. Minimum, average and maximum are
-- projected; the timeout is never inferred. CPU is projected beside the
-- duration because it is the reading that separates the two usual causes:
-- CPU near the duration, the statement was working when it was stopped; CPU
-- far below it, the statement was waiting.
--
-- WHAT THE LISTING CANNOT SEE. Under the default capture mode, AUTO, a query
-- is captured on execution count or CPU, never on duration: a rarely run
-- statement that times out while blocked uses no CPU and is never captured
-- (measured: five timeouts, nothing recorded), and a frequent one loses the
-- executions before its capture. So under AUTO the listing is a lower bound,
-- and state.capture_mode is in the root. A timeout during compilation and an
-- execution ended by KILL leave no runtime row either.
--
-- Exception carries no error number: a lock timeout, a deadlock victim and a
-- division by zero are the same row. NO JUDGEMENT IS APPLIED: nothing is
-- labelled a timeout, a block or an error.
--
-- It reads the WHOLE RETAINED HISTORY and takes no parameter, like 023: a
-- query that timed out every night for a month is the finding. The text is
-- cut at 500 characters, which is why it carries no flag.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

/* ───────── root ─────────
   A LEFT JOIN from sys.databases, as in 021: a database whose store was never
   enabled has no options row, and a root with no row is an encoder error.
   The totals are 0 on an empty store and NULL only when there is no store:
   a bare SUM gives NULL on an empty store, and a NULL beside a readable store
   would look like a count nobody took. */
SELECT DB_NAME()                                                  AS [database],
       SYSDATETIMEOFFSET()                                        AS [collected_at],
       o.actual_state_desc                                        AS [state.actual],
       o.desired_state_desc                                       AS [state.desired],
       o.readonly_reason                                          AS [state.readonly_reason],
       o.query_capture_mode_desc                                  AS [state.capture_mode],
       w.oldest_interval                                          AS [window.oldest_interval],
       w.newest_interval                                          AS [window.newest_interval],
       w.intervals                                                AS [window.intervals],
       CASE WHEN o.actual_state_desc IS NOT NULL THEN ISNULL(t.regular, 0) END   AS [executions.regular],
       CASE WHEN o.actual_state_desc IS NOT NULL THEN ISNULL(t.aborted, 0) END   AS [executions.aborted],
       CASE WHEN o.actual_state_desc IS NOT NULL THEN ISNULL(t.exception, 0) END AS [executions.exception],
       CASE WHEN o.actual_state_desc IS NOT NULL THEN ISNULL(t.q_aborted, 0) END   AS [queries.with_aborted],
       CASE WHEN o.actual_state_desc IS NOT NULL THEN ISNULL(t.q_exception, 0) END AS [queries.with_exception],
       50                                                         AS [listing_cap]
FROM sys.databases AS d
LEFT JOIN sys.database_query_store_options AS o ON 1 = 1
OUTER APPLY (SELECT MIN(i.start_time) AS oldest_interval, MAX(i.end_time) AS newest_interval,
                    COUNT(*) AS intervals
             FROM sys.query_store_runtime_stats_interval AS i) AS w
OUTER APPLY (SELECT SUM(CASE WHEN rs.execution_type = 0 THEN rs.count_executions END) AS regular,
                    SUM(CASE WHEN rs.execution_type = 3 THEN rs.count_executions END) AS aborted,
                    SUM(CASE WHEN rs.execution_type = 4 THEN rs.count_executions END) AS exception,
                    COUNT(DISTINCT CASE WHEN rs.execution_type = 3 THEN p.query_id END) AS q_aborted,
                    COUNT(DISTINCT CASE WHEN rs.execution_type = 4 THEN p.query_id END) AS q_exception
             FROM sys.query_store_runtime_stats AS rs
             JOIN sys.query_store_plan AS p ON p.plan_id = rs.plan_id) AS t
WHERE d.database_id = DB_ID()
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── aborted ─────────
   One row per query, over every plan and interval: counts summed, averages
   weighted by executions, the minimum of min_duration and the maximum of
   max_duration. first and last execution are end times (as Microsoft
   documents them), per type, so they date the interruptions. The plans
   counts say when finished and interrupted executions ran under different
   plans, which the comparison of their durations would otherwise hide.

   The object label is 023's: no object_id is ad hoc, an object_id that no
   longer resolves is a dropped object, and the two must not both be NULL in
   a file that reads the whole history. */
WITH perType AS (
    SELECT p.query_id, rs.execution_type,
           SUM(rs.count_executions)                        AS n,
           COUNT(DISTINCT rs.plan_id)                      AS plans,
           SUM(rs.avg_duration * rs.count_executions)      AS duration,
           SUM(rs.avg_cpu_time * rs.count_executions)      AS cpu,
           MIN(rs.min_duration)                            AS min_duration,
           MAX(rs.max_duration)                            AS max_duration,
           MIN(rs.first_execution_time)                    AS first_execution,
           MAX(rs.last_execution_time)                     AS last_execution
    FROM sys.query_store_runtime_stats AS rs
    JOIN sys.query_store_plan AS p ON p.plan_id = rs.plan_id
    WHERE rs.execution_type IN (0, 3, 4)
    GROUP BY p.query_id, rs.execution_type
)
SELECT TOP (50)
       x.query_id                                                 AS [query_id],
       CASE
           WHEN q.object_id IS NULL OR q.object_id = 0
               THEN '(ad hoc)'
           WHEN OBJECT_SCHEMA_NAME(q.object_id) IS NULL
               THEN '(dropped object, object_id ' + CAST(q.object_id AS VARCHAR(20)) + ')'
           ELSE OBJECT_SCHEMA_NAME(q.object_id) + '.' + OBJECT_NAME(q.object_id)
       END                                                        AS [object],
       x.n                                                        AS [aborted.executions],
       x.plans                                                    AS [aborted.plans],
       CAST(x.duration / x.n / 1000.0 AS decimal(18,1))           AS [aborted.avg_duration_ms],
       CAST(x.min_duration / 1000.0 AS decimal(18,1))             AS [aborted.min_duration_ms],
       CAST(x.max_duration / 1000.0 AS decimal(18,1))             AS [aborted.max_duration_ms],
       CAST(x.cpu / x.n / 1000.0 AS decimal(18,1))                AS [aborted.avg_cpu_ms],
       x.first_execution                                          AS [aborted.first_execution],
       x.last_execution                                           AS [aborted.last_execution],
       r.n                                                        AS [regular.executions],
       r.plans                                                    AS [regular.plans],
       CAST(r.duration / r.n / 1000.0 AS decimal(18,1))           AS [regular.avg_duration_ms],
       CAST(r.max_duration / 1000.0 AS decimal(18,1))             AS [regular.max_duration_ms],
       CAST(r.cpu / r.n / 1000.0 AS decimal(18,1))                AS [regular.avg_cpu_ms],
       e.n                                                        AS [exception.executions],
       LEFT(qt.query_sql_text, 500)                               AS [text]
FROM perType AS x
JOIN      sys.query_store_query      AS q  ON q.query_id = x.query_id
JOIN      sys.query_store_query_text AS qt ON qt.query_text_id = q.query_text_id
LEFT JOIN perType AS r ON r.query_id = x.query_id AND r.execution_type = 0
LEFT JOIN perType AS e ON e.query_id = x.query_id AND e.execution_type = 4
WHERE x.execution_type = 3 AND x.n > 0
ORDER BY x.n DESC, x.query_id
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── exceptions ─────────
   The same shape on execution_type 4. No error number: the Query Store does
   not keep one. A lock timeout lasts about LOCK_TIMEOUT, and deadlock victims
   can be matched against 10.system/061.deadlock-graphs.sql. */
WITH perType AS (
    SELECT p.query_id, rs.execution_type,
           SUM(rs.count_executions)                        AS n,
           COUNT(DISTINCT rs.plan_id)                      AS plans,
           SUM(rs.avg_duration * rs.count_executions)      AS duration,
           SUM(rs.avg_cpu_time * rs.count_executions)      AS cpu,
           MIN(rs.min_duration)                            AS min_duration,
           MAX(rs.max_duration)                            AS max_duration,
           MIN(rs.first_execution_time)                    AS first_execution,
           MAX(rs.last_execution_time)                     AS last_execution
    FROM sys.query_store_runtime_stats AS rs
    JOIN sys.query_store_plan AS p ON p.plan_id = rs.plan_id
    WHERE rs.execution_type IN (0, 3, 4)
    GROUP BY p.query_id, rs.execution_type
)
SELECT TOP (50)
       x.query_id                                                 AS [query_id],
       CASE
           WHEN q.object_id IS NULL OR q.object_id = 0
               THEN '(ad hoc)'
           WHEN OBJECT_SCHEMA_NAME(q.object_id) IS NULL
               THEN '(dropped object, object_id ' + CAST(q.object_id AS VARCHAR(20)) + ')'
           ELSE OBJECT_SCHEMA_NAME(q.object_id) + '.' + OBJECT_NAME(q.object_id)
       END                                                        AS [object],
       x.n                                                        AS [exception.executions],
       x.plans                                                    AS [exception.plans],
       CAST(x.duration / x.n / 1000.0 AS decimal(18,1))           AS [exception.avg_duration_ms],
       CAST(x.min_duration / 1000.0 AS decimal(18,1))             AS [exception.min_duration_ms],
       CAST(x.max_duration / 1000.0 AS decimal(18,1))             AS [exception.max_duration_ms],
       CAST(x.cpu / x.n / 1000.0 AS decimal(18,1))                AS [exception.avg_cpu_ms],
       x.first_execution                                          AS [exception.first_execution],
       x.last_execution                                           AS [exception.last_execution],
       r.n                                                        AS [regular.executions],
       r.plans                                                    AS [regular.plans],
       CAST(r.duration / r.n / 1000.0 AS decimal(18,1))           AS [regular.avg_duration_ms],
       CAST(r.max_duration / 1000.0 AS decimal(18,1))             AS [regular.max_duration_ms],
       CAST(r.cpu / r.n / 1000.0 AS decimal(18,1))                AS [regular.avg_cpu_ms],
       a.n                                                        AS [aborted.executions],
       LEFT(qt.query_sql_text, 500)                               AS [text]
FROM perType AS x
JOIN      sys.query_store_query      AS q  ON q.query_id = x.query_id
JOIN      sys.query_store_query_text AS qt ON qt.query_text_id = q.query_text_id
LEFT JOIN perType AS r ON r.query_id = x.query_id AND r.execution_type = 0
LEFT JOIN perType AS a ON a.query_id = x.query_id AND a.execution_type = 3
WHERE x.execution_type = 4 AND x.n > 0
ORDER BY x.n DESC, x.query_id
OPTION (RECOMPILE, MAXDOP 1);
