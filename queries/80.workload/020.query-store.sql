-- @scope:       database
-- @resultsets:  root:object, top_queries:array, forced_plans:array, by_query_hash:array
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
-- The truncation bounds how much is taken, not what: a short statement fits
-- whole in 500 characters, literals included, so an ALTER LOGIN or an INSERT
-- carrying a password or a card number in clear lands in the archive as it
-- was sent. What it does keep out is the tail of a long statement and every
-- plan. That is the trade that leaves this collector outside any flag, taken
-- knowingly and stated in MANIFEST.txt and docs/dba-guide.md. If it is wrong
-- for a client, the file is the place to change it.
--
-- 021.query-store-detail.sql and 022.query-store-profiled.sql do the DEEP
-- read: the whole statement text, the execution plans, and the per-interval
-- statistics behind each plan. Both are flag-gated where this file is not, and
-- the 500-character truncation above is exactly what buys this one its place
-- in every collection. A summary that runs always and a deep read that runs on
-- request are not a duplication: the first is what tells an operator whether
-- the second is worth asking for.
--
-- THE ROOT CARRIES THE WINDOW'S TOTALS, and they are not the sum of the rows a
-- reader might add up. The top 200 is a sample of the heaviest by duration, CPU
-- and logical reads in turn, and says nothing about the share of the whole it
-- represents; a finding that five queries carry half
-- the CPU needs the denominator, which only this file can compute. The totals
-- leave out the queries of scalar and multi-statement functions and of
-- triggers, because each of those is recorded twice: once as its own query and
-- once inside the statement that called it. Measured on SQL Server 2025 CU7,
-- 27 September 2026: a scalar function whose body runs a query, called 300
-- times from one SELECT, gave the caller 4 179 ms of CPU and the function's
-- query 4 120 ms; an AFTER INSERT trigger gave the INSERT 741 ms and the
-- trigger's own statement 616 ms. Reproduced the same day on a second lab
-- database: caller 1 346 ms, function 1 336 ms over 300 calls, and totals.cpu_ms
-- of 2 139 where the naive sum was 3 475. That function had to be declared
-- INLINE = OFF: a scalar function the engine inlines records no query of its
-- own, and nothing is counted twice. What was left out is projected beside the
-- totals, so the reader can see how much it was.
--
-- Nested is decided by looking the recorded object up in sys.objects NOW, and
-- the store outlives DDL: the queries of a function or trigger since dropped no
-- longer resolve and cannot be classed. They stay in the totals, and are
-- counted under totals.unresolved so a reader knows how much of the
-- denominator is that uncertain. An object id reused by a different object
-- would be misclassed and cannot be detected here. top_queries carries the
-- same classification per row, nested 1, 0, or null when unresolved, so a
-- numerator can be built from the same population as the denominator. An
-- empty store has totals of 0, not null.
--
-- counts.query_hashes beside counts.queries says how scattered the workload
-- is. A statement sent with literals instead of parameters becomes one
-- query_id per distinct literal and one query_hash for all of them, so a ratio
-- far below one is the ad hoc workload the plan cache topics describe, visible
-- here without reading a single text. by_query_hash is the ranking that
-- follows from it: the same round robin with the hash as the key, so twenty
-- cheap literal variants of one statement read as the one heavy row they are.
--
-- NO JUDGEMENT IS APPLIED. Nothing here is labelled a regression: comparing
-- two intervals and deciding a plan got worse is analysis, and it needs the
-- deployment calendar to be worth anything.
--
-- SQL Server 2016 is the floor for this file, which is above the corpus floor
-- of 2012, since the Query Store does not exist before it. Not collected here for
-- that reason:
--   sys.query_store_wait_stats            (2017, read per query with the log,
--                                          tempdb and physical I/O columns by
--                                          028.query-store-resources.sql)
--   sys.query_store_query_hints           (2022)
--   sys.query_store_plan_feedback         (2022)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

/* THE LISTING CAP, one number for both rankings and the root. It was 50 until
   4 October 2026, and 50 was hit in 9 of the 11 non-empty stores among the
   real collections (238 to 42 630 queries). The whole store is already
   aggregated and ranked to choose the 50, so 200 costs the server nothing
   more; it costs the archive 30 to 43 KB per 50 rows, so 100 to 130 KB more
   per database. listing_cap against counts.queries and counts.query_hashes
   says whether a listing is the whole store or its head. */
DECLARE @listing_cap int = 200;

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
       (SELECT COUNT(DISTINCT query_hash) FROM sys.query_store_query) AS [counts.query_hashes],
       (SELECT COUNT(*) FROM sys.query_store_plan)                AS [counts.plans],
       (SELECT COUNT(*) FROM sys.query_store_plan WHERE is_forced_plan = 1) AS [counts.forced_plans],
       (SELECT COUNT(*) FROM sys.query_store_runtime_stats)       AS [counts.runtime_stat_rows],
       (SELECT MIN(i.start_time) FROM sys.query_store_runtime_stats_interval AS i) AS [window.oldest_interval],
       (SELECT MAX(i.end_time)   FROM sys.query_store_runtime_stats_interval AS i) AS [window.newest_interval],
       (SELECT COUNT(*) FROM sys.query_store_runtime_stats_interval)               AS [window.intervals],
       ISNULL(tot.executions, 0)                                  AS [totals.executions],
       CAST(ISNULL(tot.duration_us, 0) / 1000.0 AS DECIMAL(18,1)) AS [totals.duration_ms],
       CAST(ISNULL(tot.cpu_us, 0) / 1000.0 AS DECIMAL(18,1))      AS [totals.cpu_ms],
       ISNULL(tot.logical_reads, 0)                               AS [totals.logical_reads],
       ISNULL(tot.nested_queries, 0)                              AS [totals.excluded.nested_queries],
       CAST(ISNULL(tot.nested_cpu_us, 0) / 1000.0 AS DECIMAL(18,1)) AS [totals.excluded.nested_cpu_ms],
       ISNULL(tot.unresolved_queries, 0)                          AS [totals.unresolved.queries],
       CAST(ISNULL(tot.unresolved_cpu_us, 0) / 1000.0 AS DECIMAL(18,1)) AS [totals.unresolved.cpu_ms],
       @listing_cap                                               AS [listing_cap]
FROM sys.database_query_store_options AS o
OUTER APPLY (
    -- Nested means run from inside another statement that is recorded too:
    -- scalar and table-valued multi-statement functions, and triggers. See the
    -- header for the measurement.
    SELECT SUM(CASE WHEN ISNULL(n.nested, 0) = 0 THEN rs.count_executions END)                   AS executions,
           SUM(CASE WHEN ISNULL(n.nested, 0) = 0 THEN rs.avg_duration * rs.count_executions END) AS duration_us,
           SUM(CASE WHEN ISNULL(n.nested, 0) = 0 THEN rs.avg_cpu_time * rs.count_executions END) AS cpu_us,
           SUM(CASE WHEN ISNULL(n.nested, 0) = 0
                    THEN CAST(rs.avg_logical_io_reads * rs.count_executions AS bigint) END)     AS logical_reads,
           COUNT(DISTINCT CASE WHEN n.nested = 1 THEN q.query_id END)                  AS nested_queries,
           SUM(CASE WHEN n.nested = 1 THEN rs.avg_cpu_time * rs.count_executions END)  AS nested_cpu_us,
           COUNT(DISTINCT CASE WHEN n.nested IS NULL THEN q.query_id END)              AS unresolved_queries,
           SUM(CASE WHEN n.nested IS NULL THEN rs.avg_cpu_time * rs.count_executions END) AS unresolved_cpu_us
    FROM       sys.query_store_query         AS q
    JOIN       sys.query_store_plan          AS p  ON p.query_id = q.query_id
    JOIN       sys.query_store_runtime_stats AS rs ON rs.plan_id = p.plan_id
    CROSS APPLY (SELECT CASE WHEN q.object_id = 0 THEN 0
                             WHEN ob.type IN ('FN', 'TF', 'TR') THEN 1
                             WHEN ob.object_id IS NOT NULL THEN 0
                        END AS nested
                 FROM (SELECT 1 AS one) AS x
                 LEFT JOIN sys.objects AS ob ON ob.object_id = q.object_id) AS n
) AS tot
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── top_queries ─────────
   Aggregated across every interval the store still holds, so the ranking is
   over the whole retained window rather than over whichever interval happened
   to be open when the collector ran.

   A capped round robin over three metrics, the idiom of
   021.query-store-detail.sql without its fourth: every metric's first place,
   then every metric's second place, and so on, each query keeping its single
   best (rank, metric) pair so that one leading all three takes one slot.
   Ranked on duration alone, the list missed the query that leads logical
   reads while waiting on nothing. Execution count is left to
   023.query-store-most-executed.sql, which exists for it. query_id is the
   tie-break, so two collections of an unchanged store list the same rows. */
WITH agg AS (
    SELECT q.query_id, q.object_id,
           COUNT(DISTINCT p.plan_id)                                       AS plans,
           SUM(rs.count_executions)                                        AS executions,
           SUM(rs.avg_duration * rs.count_executions)                      AS total_duration,
           SUM(rs.avg_cpu_time * rs.count_executions)                      AS total_cpu,
           SUM(rs.avg_logical_io_reads * rs.count_executions)              AS total_reads,
           SUM(CAST(rs.avg_logical_io_reads * rs.count_executions AS bigint)) AS total_reads_int,
           MAX(rs.last_execution_time)                                     AS last_execution
    FROM       sys.query_store_query         AS q
    JOIN       sys.query_store_plan          AS p  ON p.query_id = q.query_id
    JOIN       sys.query_store_runtime_stats AS rs ON rs.plan_id = p.plan_id
    GROUP BY q.query_id, q.object_id
),
ranked AS (
    SELECT a.*,
           ROW_NUMBER() OVER (ORDER BY a.total_duration DESC, a.query_id) AS rn_duration,
           ROW_NUMBER() OVER (ORDER BY a.total_cpu      DESC, a.query_id) AS rn_cpu,
           ROW_NUMBER() OVER (ORDER BY a.total_reads    DESC, a.query_id) AS rn_reads
    FROM agg AS a
),
best AS (
    SELECT r.query_id, m.metric_order, m.rn,
           ROW_NUMBER() OVER (PARTITION BY r.query_id ORDER BY m.rn, m.metric_order) AS dedupe
    FROM ranked AS r
    CROSS APPLY (VALUES (1, r.rn_duration), (2, r.rn_cpu), (3, r.rn_reads)) AS m(metric_order, rn)
),
capped AS (            /* ORDER BY rn, metric_order IS the round robin */
    SELECT TOP (@listing_cap) query_id, rn, metric_order
    FROM best
    WHERE dedupe = 1
    ORDER BY rn, metric_order, query_id
)
SELECT r.query_id                                                 AS [query_id],
       OBJECT_SCHEMA_NAME(r.object_id)
         + '.' + OBJECT_NAME(r.object_id)                         AS [object],
       r.plans                                                    AS [plans],
       r.executions                                               AS [executions],
       CAST(r.total_duration / 1000.0 AS DECIMAL(18,1))           AS [total.duration_ms],
       CAST(r.total_cpu / 1000.0 AS DECIMAL(18,1))                AS [total.cpu_ms],
       r.total_reads_int                                          AS [total.logical_reads],
       CAST(r.total_duration
            / NULLIF(r.executions, 0) / 1000.0 AS DECIMAL(18,1))  AS [per_execution.duration_ms],
       CAST(r.total_reads
            / NULLIF(r.executions, 0) AS DECIMAL(18,1))           AS [per_execution.logical_reads],
       r.last_execution                                           AS [last_execution],
       -- The raw ranks, never capped: whichever of the three let the query in
       -- is the smallest of them.
       r.rn_duration                                              AS [rank.duration],
       r.rn_cpu                                                   AS [rank.cpu],
       r.rn_reads                                                 AS [rank.logical_reads],
       -- Same rule as the root totals: 1 for a function or trigger statement,
       -- already inside its caller's row; null when the object no longer
       -- resolves.
       CASE WHEN r.object_id = 0 THEN 0
            WHEN ob.type IN ('FN', 'TF', 'TR') THEN 1
            WHEN ob.object_id IS NOT NULL THEN 0
       END                                                        AS [nested],
       LEFT(qt.query_sql_text, 500)                               AS [text]
FROM       capped                         AS c
JOIN       ranked                         AS r  ON r.query_id = c.query_id
JOIN       sys.query_store_query          AS q  ON q.query_id = r.query_id
JOIN       sys.query_store_query_text     AS qt ON qt.query_text_id = q.query_text_id
LEFT JOIN  sys.objects                    AS ob ON ob.object_id = r.object_id
ORDER BY c.rn, c.metric_order, c.query_id
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

/* ───────── by_query_hash ─────────
   The same ranking with query_hash as the key. A statement sent with literals
   instead of parameters is one query_id per literal, and so are the variants
   the 2022 parameter sensitive plan optimisation compiles for one statement:
   each id alone can sit far down top_queries, or below its cap, while the
   statement they share is the heaviest thing the database runs. Measured on
   SQL Server 2025 CU7, 27 September 2026: one join sent twenty times with a
   different literal gave twenty query_ids of about 13 ms each, ranked from
   seventh to twenty-seventh on duration in top_queries, and one row here at
   259 ms, third.

   query_ids is how many ids the row folds together. The text is that of the
   heaviest of them by duration, and sample_query_id says which. nested
   follows the root's rule and is 1 only when every query of the hash is
   nested, 0 only when none is and all resolve, null otherwise: two
   procedures can run the same statement, so one hash can mix both kinds.
   Placed last so the positions of the result sets above do not move. */
WITH agg AS (
    SELECT q.query_hash, q.query_id, q.object_id,
           COUNT(DISTINCT p.plan_id)                                       AS plans,
           SUM(rs.count_executions)                                        AS executions,
           SUM(rs.avg_duration * rs.count_executions)                      AS total_duration,
           SUM(rs.avg_cpu_time * rs.count_executions)                      AS total_cpu,
           SUM(rs.avg_logical_io_reads * rs.count_executions)              AS total_reads,
           SUM(CAST(rs.avg_logical_io_reads * rs.count_executions AS bigint)) AS total_reads_int,
           MAX(rs.last_execution_time)                                     AS last_execution
    FROM       sys.query_store_query         AS q
    JOIN       sys.query_store_plan          AS p  ON p.query_id = q.query_id
    JOIN       sys.query_store_runtime_stats AS rs ON rs.plan_id = p.plan_id
    GROUP BY q.query_hash, q.query_id, q.object_id
),
perQuery AS (
    SELECT a.*,
           CASE WHEN a.object_id = 0 THEN 0
                WHEN ob.type IN ('FN', 'TF', 'TR') THEN 1
                WHEN ob.object_id IS NOT NULL THEN 0
           END                                                            AS nested,
           ROW_NUMBER() OVER (PARTITION BY a.query_hash
                              ORDER BY a.total_duration DESC, a.query_id) AS heaviest
    FROM agg AS a
    LEFT JOIN sys.objects AS ob ON ob.object_id = a.object_id
),
hashAgg AS (
    SELECT query_hash,
           COUNT(*)                                                  AS query_ids,
           SUM(plans)                                                AS plans,
           SUM(executions)                                           AS executions,
           SUM(total_duration)                                       AS total_duration,
           SUM(total_cpu)                                            AS total_cpu,
           SUM(total_reads)                                          AS total_reads,
           SUM(total_reads_int)                                      AS total_reads_int,
           MAX(last_execution)                                       AS last_execution,
           MAX(CASE WHEN heaviest = 1 THEN query_id END)             AS sample_query_id,
           CASE WHEN COUNT(nested) = COUNT(*) AND MIN(nested) = MAX(nested)
                THEN MIN(nested) END                                 AS nested
    FROM perQuery
    GROUP BY query_hash
),
ranked AS (
    SELECT h.*,
           ROW_NUMBER() OVER (ORDER BY h.total_duration DESC, h.query_hash) AS rn_duration,
           ROW_NUMBER() OVER (ORDER BY h.total_cpu      DESC, h.query_hash) AS rn_cpu,
           ROW_NUMBER() OVER (ORDER BY h.total_reads    DESC, h.query_hash) AS rn_reads
    FROM hashAgg AS h
),
best AS (
    SELECT r.query_hash, m.metric_order, m.rn,
           ROW_NUMBER() OVER (PARTITION BY r.query_hash ORDER BY m.rn, m.metric_order) AS dedupe
    FROM ranked AS r
    CROSS APPLY (VALUES (1, r.rn_duration), (2, r.rn_cpu), (3, r.rn_reads)) AS m(metric_order, rn)
),
capped AS (
    SELECT TOP (@listing_cap) query_hash, rn, metric_order
    FROM best
    WHERE dedupe = 1
    ORDER BY rn, metric_order, query_hash
)
SELECT CONVERT(varchar(18), r.query_hash, 1)                      AS [query_hash],
       r.query_ids                                                AS [query_ids],
       r.plans                                                    AS [plans],
       r.executions                                               AS [executions],
       CAST(r.total_duration / 1000.0 AS DECIMAL(18,1))           AS [total.duration_ms],
       CAST(r.total_cpu / 1000.0 AS DECIMAL(18,1))                AS [total.cpu_ms],
       r.total_reads_int                                          AS [total.logical_reads],
       CAST(r.total_duration
            / NULLIF(r.executions, 0) / 1000.0 AS DECIMAL(18,1))  AS [per_execution.duration_ms],
       CAST(r.total_reads
            / NULLIF(r.executions, 0) AS DECIMAL(18,1))           AS [per_execution.logical_reads],
       r.last_execution                                           AS [last_execution],
       r.rn_duration                                              AS [rank.duration],
       r.rn_cpu                                                   AS [rank.cpu],
       r.rn_reads                                                 AS [rank.logical_reads],
       r.nested                                                   AS [nested],
       r.sample_query_id                                          AS [sample_query_id],
       LEFT(qt.query_sql_text, 500)                               AS [text]
FROM       capped                         AS c
JOIN       ranked                         AS r  ON r.query_hash = c.query_hash
JOIN       sys.query_store_query          AS q  ON q.query_id = r.sample_query_id
JOIN       sys.query_store_query_text     AS qt ON qt.query_text_id = q.query_text_id
ORDER BY c.rn, c.metric_order, c.query_hash
OPTION (RECOMPILE, MAXDOP 1);
