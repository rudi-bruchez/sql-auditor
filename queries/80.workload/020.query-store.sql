-- @scope:       database
-- @resultsets:  root:object, top_queries:array, forced_plans:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     120
-- @min_version: 13.0
-- @discloses:   query_text
--
-- What the Query Store recorded: the heaviest queries, and any plan someone
-- has forced.
--
-- This is the DATA. 20.databases/022.query-store.sql is the CONFIGURATION, and
-- the two are deliberately separate files. The corpus carried the second
-- without the first for a while, which meant it could report that the Query
-- Store was switched on and never once open it — the light was checked, the
-- room never entered.
--
-- Both are needed and neither substitutes for the other: a Query Store in
-- READ_ONLY because it filled its quota still answers "is it on?" with yes,
-- while recording nothing new since the day it filled.
--
-- THE OBSERVATION WINDOW IS REPORTED, and it is the first thing to read. The
-- Query Store keeps a bounded history — by size, by staleness, and by capture
-- mode — so "the ten heaviest queries" means nothing until you know whether
-- the window is nine months or ninety minutes. A store enabled yesterday and
-- one running for a year produce the same shaped table and completely
-- different evidence.
--
-- Durations are converted from microseconds to milliseconds here because every
-- other timing in the corpus is in milliseconds, and a column that silently
-- changes unit between files is a defect waiting to be quoted in a report.
--
-- Query text is truncated to 500 characters. It is application SQL and can
-- contain literal values from the workload, which is a disclosure decision:
-- 052.session-text.sql puts the same class of data behind an explicit flag.
-- The truncation here is a deliberate middle ground — enough to identify a
-- statement, not enough to reconstruct a payload — and it is the reason this
-- collector is not itself flag-gated. If that trade is wrong for a client,
-- the file is the place to change it.
--
-- 021.query-store-detail.sql and 022.query-store-profiled.sql do the DEEP
-- read: the whole statement text, the execution plans, and the per-interval
-- statistics behind each plan. Both are flag-gated where this file is not, and
-- the 500-character truncation above is exactly what buys this one its place
-- in every collection. A summary that runs always and a deep read that runs on
-- request are not a duplication: the first is what tells an operator whether
-- the second is worth asking for.
--
-- NO JUDGEMENT IS APPLIED. Nothing here is labelled a regression: comparing
-- two intervals and deciding a plan got worse is analysis, and it needs the
-- deployment calendar to be worth anything.
--
-- SQL Server 2016 is the floor for this file, which is above the corpus floor
-- of 2012 — the Query Store does not exist before it. Not collected for that
-- reason:
--   sys.query_store_wait_stats            (2017)
--   sys.query_store_query_hints           (2022)
--   sys.query_store_plan_feedback         (2022)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT DB_NAME()                                                  AS [database],
       SYSDATETIME()                                              AS [collected_at],
       o.actual_state_desc                                        AS [state.actual],
       o.desired_state_desc                                       AS [state.desired],
       o.readonly_reason                                          AS [state.readonly_reason],
       o.query_capture_mode_desc                                  AS [config.capture_mode],
       o.size_based_cleanup_mode_desc                             AS [config.cleanup_mode],
       o.interval_length_minutes                                  AS [config.interval_minutes],
       o.stale_query_threshold_days                               AS [config.stale_threshold_days],
       o.current_storage_size_mb                                  AS [config.storage_used_mb],
       o.max_storage_size_mb                                      AS [config.storage_max_mb],
       (SELECT COUNT(*) FROM sys.query_store_query)               AS [counts.queries],
       (SELECT COUNT(*) FROM sys.query_store_plan)                AS [counts.plans],
       (SELECT COUNT(*) FROM sys.query_store_plan WHERE is_forced_plan = 1) AS [counts.forced_plans],
       (SELECT COUNT(*) FROM sys.query_store_runtime_stats)       AS [counts.runtime_stat_rows],
       (SELECT MIN(i.start_time) FROM sys.query_store_runtime_stats_interval AS i) AS [window.oldest_interval],
       (SELECT MAX(i.end_time)   FROM sys.query_store_runtime_stats_interval AS i) AS [window.newest_interval],
       (SELECT COUNT(*) FROM sys.query_store_runtime_stats_interval)               AS [window.intervals],
       50                                                         AS [listing_cap]
FROM sys.database_query_store_options AS o
OPTION (RECOMPILE, MAXDOP 1);

/* Aggregated across every interval the store still holds, so the ranking is
   over the whole retained window rather than over whichever interval happened
   to be open when the collector ran. */
SELECT TOP (50)
       q.query_id                                                 AS [query_id],
       OBJECT_SCHEMA_NAME(q.object_id)
         + '.' + OBJECT_NAME(q.object_id)                         AS [object],
       COUNT(DISTINCT p.plan_id)                                  AS [plans],
       SUM(rs.count_executions)                                   AS [executions],
       CAST(SUM(rs.avg_duration * rs.count_executions) / 1000.0 AS DECIMAL(18,1))    AS [total.duration_ms],
       CAST(SUM(rs.avg_cpu_time * rs.count_executions) / 1000.0 AS DECIMAL(18,1))    AS [total.cpu_ms],
       SUM(CAST(rs.avg_logical_io_reads * rs.count_executions AS bigint))            AS [total.logical_reads],
       CAST(SUM(rs.avg_duration * rs.count_executions)
            / NULLIF(SUM(rs.count_executions), 0) / 1000.0 AS DECIMAL(18,1))         AS [per_execution.duration_ms],
       CAST(SUM(rs.avg_logical_io_reads * rs.count_executions)
            / NULLIF(SUM(rs.count_executions), 0) AS DECIMAL(18,1))                  AS [per_execution.logical_reads],
       MAX(rs.last_execution_time)                                AS [last_execution],
       LEFT(qt.query_sql_text, 500)                               AS [text]
FROM       sys.query_store_query          AS q
JOIN       sys.query_store_query_text     AS qt ON qt.query_text_id = q.query_text_id
JOIN       sys.query_store_plan           AS p  ON p.query_id = q.query_id
JOIN       sys.query_store_runtime_stats  AS rs ON rs.plan_id = p.plan_id
GROUP BY q.query_id, q.object_id, LEFT(qt.query_sql_text, 500)
ORDER BY SUM(rs.avg_duration * rs.count_executions) DESC
OPTION (RECOMPILE, MAXDOP 1);

/* A forced plan is a decision someone took, and force_failure_count is the
   part that goes unnoticed: a plan that can no longer be forced stops being
   applied without anything raising an error. */
SELECT p.plan_id                                                  AS [plan_id],
       p.query_id                                                 AS [query_id],
       p.is_forced_plan                                           AS [is_forced],
       p.force_failure_count                                      AS [force_failure_count],
       p.last_force_failure_reason_desc                           AS [last_force_failure_reason],
       p.last_compile_start_time                                  AS [last_compile_start],
       p.count_compiles                                           AS [count_compiles],
       LEFT(qt.query_sql_text, 500)                               AS [text]
FROM       sys.query_store_plan       AS p
JOIN       sys.query_store_query      AS q  ON q.query_id = p.query_id
JOIN       sys.query_store_query_text AS qt ON qt.query_text_id = q.query_text_id
WHERE p.is_forced_plan = 1 OR p.force_failure_count > 0
ORDER BY p.plan_id
OPTION (RECOMPILE, MAXDOP 1);
