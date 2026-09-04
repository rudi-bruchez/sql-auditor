-- @scope:       database
-- @resultsets:  by_query:array, by_object:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     120
-- @min_version: 13
-- @discloses:   query_text
--
-- The most frequently executed queries, plain call counts, nothing about what
-- they mean. A row-by-row loop is recognised by its call count and its
-- derisory unit cost, not by its plan — the plan of such a query is usually
-- perfect, and the caller is the problem. Nothing here is labelled RBAR, no
-- threshold is applied, no is_rbar column exists: deciding that four million
-- calls are a defect requires knowing what the application does, and that is
-- not in the Query Store.
--
-- This file carries no flag because the text is truncated to 500 characters,
-- the same trade 80.workload/020.query-store.sql makes and for the same
-- reason: enough to identify a statement, not enough to reconstruct a
-- payload. A row-by-row query is a few dozen characters anyway.
--
-- It aggregates the WHOLE RETAINED WINDOW, unlike 021.query-store-detail.sql:
-- a loop that ran a million times last month is still the finding, and a
-- longer window makes the pattern clearer rather than diluting it. This is
-- also why it takes no window parameter.
--
-- The two numbers it exists to carry are the call count and the unit cost.
-- Their product is the total time, which is arithmetic, not judgement, and it
-- is what makes a loop worth discussing or not.
--
-- executions_per_hour IS ARITHMETIC OVER THE OBSERVED SPAN, and that span can
-- be short: a query captured only in its most recent, possibly just-opened,
-- interval can show a few seconds between its first interval and its last
-- execution. Dividing a large count by a few seconds and multiplying up to an
-- hour produces a number in the hundreds of thousands without a single thing
-- being wrong with the arithmetic — it is the observation that is thin, not
-- the query. No threshold nulls the rate out, the way none exists anywhere
-- else in this file; instead window.span_seconds and window.intervals are
-- projected beside it in both result sets, so a rate can be checked against
-- what backs it before it is quoted.
--
-- No root: this is a listing keyed by query and by object, not a single-row
-- state. On a database whose Query Store is off, both joins below return no
-- rows and the file is two empty arrays — that is the decision, not an
-- oversight, and it is how the rest of the corpus already says "nothing
-- here". Suppressing the file instead would make "the Query Store is off"
-- indistinguishable from "this collector never ran".
--
-- Durations are converted from microseconds to milliseconds, as everywhere
-- else in the corpus.
--
-- SQL Server 2016 is the floor for this file, which is above the corpus floor
-- of 2012 — the Query Store does not exist before it.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

/* ───────── by_query ─────────
   TOP (50) by call count, not by cost: this is the RBAR-hunting ranking, and
   a loop's total cost can be unremarkable even while its call count is not.
   window.since and window.last_execution bound the observation for THIS
   query specifically; window.span_seconds and window.intervals say how much
   of that bound is actually backed by data, which is what keeps
   executions_per_hour honest for a query seen in one freshly-opened
   interval. The object label distinguishes three different facts that a
   single ISNULL would collapse into one: no object_id at all (ad hoc), an
   object_id that no longer resolves (dropped since capture), and a resolved
   name. */
SELECT TOP (50)
       q.query_id                                                 AS [query_id],
       CASE
           WHEN q.object_id IS NULL OR q.object_id = 0
               THEN '(ad hoc batch)'
           WHEN OBJECT_SCHEMA_NAME(q.object_id) IS NULL
               THEN '(dropped object, object_id ' + CAST(q.object_id AS VARCHAR(20)) + ')'
           ELSE OBJECT_SCHEMA_NAME(q.object_id) + '.' + OBJECT_NAME(q.object_id)
       END                                                         AS [object],
       SUM(rs.count_executions)                                   AS [executions],
       CAST(SUM(rs.avg_duration * rs.count_executions)
            / NULLIF(SUM(rs.count_executions), 0)
            / 1000.0 AS DECIMAL(18,1))                            AS [per_execution.duration_ms],
       CAST(SUM(rs.avg_cpu_time * rs.count_executions)
            / NULLIF(SUM(rs.count_executions), 0)
            / 1000.0 AS DECIMAL(18,1))                            AS [per_execution.cpu_ms],
       CAST(SUM(rs.avg_logical_io_reads * rs.count_executions)
            / NULLIF(SUM(rs.count_executions), 0) AS DECIMAL(18,1)) AS [per_execution.logical_reads],
       CAST(SUM(rs.avg_duration * rs.count_executions) / 1000.0 AS DECIMAL(18,1)) AS [total.duration_ms],
       CAST(SUM(rs.avg_cpu_time * rs.count_executions) / 1000.0 AS DECIMAL(18,1)) AS [total.cpu_ms],
       MIN(i.start_time)                                          AS [window.since],
       MAX(rs.last_execution_time)                                AS [window.last_execution],
       DATEDIFF(SECOND, MIN(i.start_time), MAX(rs.last_execution_time)) AS [window.span_seconds],
       COUNT(DISTINCT i.runtime_stats_interval_id)                AS [window.intervals],
       CAST(SUM(rs.count_executions)
            / NULLIF(DATEDIFF(SECOND, MIN(i.start_time), MAX(rs.last_execution_time)) / 3600.0, 0)
            AS DECIMAL(18,1))                                     AS [executions_per_hour],
       LEFT(qt.query_sql_text, 500)                                AS [text]
FROM       sys.query_store_query                AS q
JOIN       sys.query_store_query_text            AS qt ON qt.query_text_id = q.query_text_id
JOIN       sys.query_store_plan                   AS p  ON p.query_id = q.query_id
JOIN       sys.query_store_runtime_stats           AS rs ON rs.plan_id = p.plan_id
JOIN       sys.query_store_runtime_stats_interval   AS i  ON i.runtime_stats_interval_id = rs.runtime_stats_interval_id
GROUP BY q.query_id, q.object_id, LEFT(qt.query_sql_text, 500)
ORDER BY SUM(rs.count_executions) DESC
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── by_object ─────────
   The same executions grouped by object: a procedure issuing twelve
   statements four million times each reads as one line here and is scattered
   across twelve in the ranking above. Rows with no object_id — ad hoc
   batches — group into one row rather than being dropped: GROUP BY treats
   NULL as a single bucket, and the CASE below gives that bucket a name
   instead of a blank one; a non-NULL object_id that no longer resolves (the
   object was dropped since the Query Store captured it) gets its own label
   carrying the id, kept out of the ad hoc bucket because the two are
   different facts about the instance. window.span_seconds and
   window.intervals are the union across every query grouped into the row, so
   a busy object is less exposed to the short-span distortion than a single
   query is in by_query above — but a freshly-created object with one query
   in one interval is exactly as exposed. No cap: the number of distinct
   objects on an instance is already bounded by what exists, unlike the
   number of distinct queries. */
SELECT CASE
           WHEN q.object_id IS NULL OR q.object_id = 0
               THEN '(ad hoc batch)'
           WHEN OBJECT_SCHEMA_NAME(q.object_id) IS NULL
               THEN '(dropped object, object_id ' + CAST(q.object_id AS VARCHAR(20)) + ')'
           ELSE OBJECT_SCHEMA_NAME(q.object_id) + '.' + OBJECT_NAME(q.object_id)
       END                                                         AS [object],
       COUNT(DISTINCT q.query_id)                                 AS [statements],
       SUM(rs.count_executions)                                   AS [executions],
       CAST(SUM(rs.avg_duration * rs.count_executions) / 1000.0 AS DECIMAL(18,1)) AS [total.duration_ms],
       DATEDIFF(SECOND, MIN(i.start_time), MAX(rs.last_execution_time)) AS [window.span_seconds],
       COUNT(DISTINCT i.runtime_stats_interval_id)                AS [window.intervals],
       CAST(SUM(rs.count_executions)
            / NULLIF(DATEDIFF(SECOND, MIN(i.start_time), MAX(rs.last_execution_time)) / 3600.0, 0)
            AS DECIMAL(18,1))                                     AS [executions_per_hour]
FROM       sys.query_store_query                AS q
JOIN       sys.query_store_plan                   AS p  ON p.query_id = q.query_id
JOIN       sys.query_store_runtime_stats           AS rs ON rs.plan_id = p.plan_id
JOIN       sys.query_store_runtime_stats_interval   AS i  ON i.runtime_stats_interval_id = rs.runtime_stats_interval_id
GROUP BY q.object_id
ORDER BY SUM(rs.count_executions) DESC
OPTION (RECOMPILE, MAXDOP 1);
