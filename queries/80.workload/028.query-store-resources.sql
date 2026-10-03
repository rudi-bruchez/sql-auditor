-- @scope:       database
-- @resultsets:  root:object, by_query:array, waits:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     180
-- @min_version: 14
-- @discloses:   query_text
--
-- What each query waited on, and how much transaction log, tempdb and physical
-- I/O it cost. The instance wait statistics of 010.wait-stats.sql say that the
-- server waits on locks; this file says which query does.
--
-- WHY A SEPARATE FILE. sys.query_store_wait_stats and the runtime columns read
-- here, avg_log_bytes_used, avg_tempdb_space_used and avg_num_physical_io_reads,
-- arrived in SQL Server 2017. 020.query-store.sql is gated at 2016, where the
-- Query Store starts, and listed them as not collected for that reason. This is
-- the 2017 sibling, the same arrangement as 024.query-store-rowcount.sql.
--
-- UNITS, from the documentation of sys.query_store_runtime_stats, and they are
-- not the same unit three times:
--   avg_log_bytes_used          BYTES of the database's log;
--   avg_tempdb_space_used       8 KB PAGES of tempdb;
--   avg_num_physical_io_reads   read I/O OPERATIONS, not pages;
--   avg_physical_io_reads       8 KB PAGES read physically (2016 column, kept
--                               beside the previous one: pages per operation
--                               is what read-ahead did).
-- Wait times in sys.query_store_wait_stats are milliseconds. Every total here
-- is SUM(count_executions * avg_x), so a tempdb total is pages summed over
-- executions: a volume of work, not a size tempdb ever reached. The size is
-- tempdb.pages_max, the largest single execution.
--
-- Checked on SQL Server 2025 CU7, 3 October 2026, against counters read inside
-- the same statement's transaction or task. A 50,000-row INSERT recorded
-- 28,424,096 log bytes where sys.dm_tran_database_transactions gave 28,422,340
-- for the transaction just before its last statement: bytes. A serial SELECT
-- INTO #temp recorded 2,872 tempdb pages, exactly the task's
-- user_objects_alloc_page_count delta (the table itself held 2,640): pages.
-- A cold scan that STATISTICS IO reported as 1 physical and 1,682 read-ahead
-- pages recorded 1,690 physical pages in 46 read operations.
--
-- PARALLEL PLANS COUNT EVERY THREAD. The same SELECT INTO at DOP 22 recorded
-- 11,304 tempdb pages, four times the serial figure for the same rows, and
-- 38.8 s of waits, 36 s of them Latch, for a statement of 1.8 s. Wait time
-- above duration is therefore normal for a parallel query, and a tempdb
-- figure is the allocation the plan made, not the size of its result.
--
-- WHICH QUERIES. A capped round robin over four measures, the idiom of
-- 020.query-store.sql: every measure's first place, then every measure's
-- second, and so on, each query keeping its best (rank, measure) pair, so one
-- query leading all four takes one slot. The measures are wait time, log
-- bytes, tempdb pages and physical read operations, each a total over the
-- whole retained window. Ranking on execution count, as 023 and 024 do, would
-- miss exactly the queries this file exists for: a statement blocked for a
-- minute once is a lock finding, and it is nowhere near the most executed. A
-- query enters on a measure only if that measure is above zero, so a store
-- without wait capture does not fill its wait slots with idle queries in
-- query_id order. query_id is the tie-break, so two collections of an
-- unchanged store list the same queries.
--
-- IDLE AND USER WAIT ARE PROJECTED, NOT DROPPED, AND NOT RANKED ON. The
-- documentation files WAITFOR, WAIT_FOR_RESULTS and BROKER_RECEIVE_WAITFOR
-- under User Wait (category 18), and SLEEP_%, LOGMGR_QUEUE, CHECKPOINT_QUEUE
-- and other background waits under Idle (11). A Service Broker reader blocked
-- in WAITFOR (RECEIVE ...) would top any ranking that summed them, while it
-- costs nothing. So waits.ranked_ms, the measure the round robin uses, leaves
-- them out; waits.total_ms keeps every category; and the waits result set
-- lists each category raw with its class, idle, user_wait or resource, for the
-- analysis to decide. Nothing is summed away before it reaches the archive.
-- Measured on 2025: a WAITFOR (RECEIVE ...) with a 3-second timeout on an
-- empty queue recorded 3,000 ms of User Wait and no other wait. It entered
-- the lab listing only because it also logged 284 bytes; a reader that logs
-- nothing enters on no measure and shows only in the root's waits_recorded. A
-- WAITFOR DELAY inside a transaction was not recorded as a query at all, and
-- the 5 s another session spent blocked behind it came out as Lock on the
-- blocked SELECT, which is where this file looks for it.
--
-- WAIT CAPTURE IS A SEPARATE OPTION. WAIT_STATS_CAPTURE_MODE, ON by default
-- from 2017, can be switched off while the Query Store keeps recording runtime
-- statistics. The root projects it with the number of wait rows the store
-- holds, so an empty waits result set reads as "not captured" or "nothing
-- waited", never as a collector failure. Measured on 2025: enabling the
-- Query Store without naming the option gave ON; after switching it OFF, a
-- SELECT blocked for 3 s was recorded with its duration and no wait row,
-- while the rows captured before stayed. A store whose wait capture was
-- switched off some time ago therefore shows old waits and none for recent
-- queries.
--
-- COST. The retained set is decided once, into #retained, and the per category
-- listing joins sys.query_store_wait_stats through the plans of those queries
-- only. Ranking on wait time still needs one aggregate pass over the wait
-- view, grouped by plan before anything is joined; that pass is the price of
-- finding the most blocked query, and it reads one row per plan, interval,
-- category and execution type. One #temp table also keeps by_query and waits
-- on the same population: with capture mode ALL the collector's own statements
-- enter the store between two statements, and a ranking computed twice could
-- disagree with itself.
-- Measured on a 2025 lab store of 15,955 queries: 2.8 s, against 3.2 s for
-- 020 and 0.9 s for 024 on the same store. That store held 25 wait rows, so
-- what the wait pass costs on a store with millions of them is not measured;
-- the run's own duration_ms is in _run.json.
--
-- ABORTED AND FAILED EXECUTIONS ARE COUNTED, in both views, as in 024: a
-- statement cancelled by a lock timeout is often the very one this file is
-- looking for. Statements of functions and triggers are recorded as their own
-- queries and inside their caller as well (020 measured it for CPU), so the
-- log or tempdb of a trigger can appear twice in the listing. No share of a
-- store total is computed here for that reason, and the root's
-- waits_recorded is a plain sum, not a denominator.
--
-- It aggregates the whole retained window and takes no window parameter, like
-- 023 and 024; QUERY_STORE_DAYS and QUERY_STORE_DB_INCLUDE narrow the deep
-- extraction of 021 and 022, not the ungated summaries. Query text is the
-- first 500 characters, as in 020 and 024.
--
-- On a database whose Query Store is off, by_query and waits are empty and
-- root still reports the state.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

/* ───────── root ─────────
   Scalar subqueries rather than a FROM over sys.database_query_store_options:
   that view has no row on a database whose store was never enabled, and a
   root with no row is an encoder error. The aggregate over the wait view
   always returns one row, so it can stand as the FROM.

   waits_recorded is the plain sum of the view by class, every query
   included. It is there for the queries no listing below can show: one whose
   only wait is Idle or User Wait enters on no measure, and a Service Broker
   reader waiting in WAITFOR (RECEIVE ...) would otherwise be invisible. It is
   not a denominator for shares: statements of functions and triggers are
   counted inside their caller too. */
SELECT DB_NAME()                                                   AS [database],
       (SELECT COUNT(*) FROM sys.query_store_runtime_stats_interval) AS [window.intervals_total],
       (SELECT MIN(start_time) FROM sys.query_store_runtime_stats_interval) AS [window.oldest],
       (SELECT MAX(end_time) FROM sys.query_store_runtime_stats_interval)   AS [window.newest],
       CAST((SELECT actual_state_desc FROM sys.database_query_store_options) AS NVARCHAR(60)) AS [state.actual],
       CAST((SELECT query_capture_mode_desc FROM sys.database_query_store_options) AS NVARCHAR(60)) AS [state.capture_mode],
       CAST((SELECT wait_stats_capture_mode_desc FROM sys.database_query_store_options) AS NVARCHAR(60)) AS [state.wait_stats_capture_mode],
       w.wait_rows                                                 AS [counts.wait_stat_rows],
       ISNULL(w.resource_ms, 0)                                    AS [waits_recorded.resource_ms],
       ISNULL(w.idle_ms, 0)                                        AS [waits_recorded.idle_ms],
       ISNULL(w.user_wait_ms, 0)                                   AS [waits_recorded.user_wait_ms],
       50                                                          AS [listing_cap]
FROM (SELECT COUNT(*)                                                         AS wait_rows,
             SUM(CASE WHEN wait_category NOT IN (11, 18) THEN total_query_wait_time_ms END) AS resource_ms,
             SUM(CASE WHEN wait_category = 11 THEN total_query_wait_time_ms END)            AS idle_ms,
             SUM(CASE WHEN wait_category = 18 THEN total_query_wait_time_ms END)            AS user_wait_ms
      FROM sys.query_store_wait_stats) AS w
OPTION (RECOMPILE, MAXDOP 1);

CREATE TABLE #retained (
    query_id      bigint       NOT NULL PRIMARY KEY,
    object_id     int          NOT NULL,
    plans         int          NOT NULL,
    executions    bigint       NOT NULL,
    duration_us   float        NULL,
    cpu_us        float        NULL,
    log_bytes     float        NULL,
    log_bytes_max bigint       NULL,
    tempdb_pages  float        NULL,
    tempdb_max    bigint       NULL,
    read_ops      float        NULL,
    read_pages    float        NULL,
    waits_ms      bigint       NULL,
    waits_ranked  bigint       NULL,
    rn_waits      bigint       NOT NULL,
    rn_log        bigint       NOT NULL,
    rn_tempdb     bigint       NOT NULL,
    rn_reads      bigint       NOT NULL,
    slot_rn       bigint       NOT NULL,
    slot_metric   int          NOT NULL
);

/* Waits are aggregated by plan first, with no join, then folded into the
   query. Categories 11 (Idle) and 18 (User Wait) stay in total_ms and out of
   ranked_ms; see the header. */
WITH waits_by_plan AS (
    SELECT ws.plan_id,
           SUM(ws.total_query_wait_time_ms)                         AS total_ms,
           SUM(CASE WHEN ws.wait_category IN (11, 18) THEN 0
                    ELSE ws.total_query_wait_time_ms END)           AS ranked_ms
    FROM sys.query_store_wait_stats AS ws
    GROUP BY ws.plan_id
),
runtime AS (
    SELECT q.query_id, q.object_id,
           COUNT(DISTINCT p.plan_id)                                AS plans,
           SUM(rs.count_executions)                                 AS executions,
           SUM(rs.count_executions * rs.avg_duration)               AS duration_us,
           SUM(rs.count_executions * rs.avg_cpu_time)               AS cpu_us,
           SUM(rs.count_executions * rs.avg_log_bytes_used)         AS log_bytes,
           MAX(rs.max_log_bytes_used)                               AS log_bytes_max,
           SUM(rs.count_executions * rs.avg_tempdb_space_used)      AS tempdb_pages,
           MAX(rs.max_tempdb_space_used)                            AS tempdb_max,
           SUM(rs.count_executions * rs.avg_num_physical_io_reads)  AS read_ops,
           SUM(rs.count_executions * rs.avg_physical_io_reads)      AS read_pages
    FROM       sys.query_store_query         AS q
    JOIN       sys.query_store_plan          AS p  ON p.query_id = q.query_id
    JOIN       sys.query_store_runtime_stats AS rs ON rs.plan_id = p.plan_id
    GROUP BY q.query_id, q.object_id
),
waits AS (
    SELECT p.query_id,
           SUM(w.total_ms)                                          AS total_ms,
           SUM(w.ranked_ms)                                         AS ranked_ms
    FROM waits_by_plan            AS w
    JOIN sys.query_store_plan     AS p ON p.plan_id = w.plan_id
    GROUP BY p.query_id
),
ranked AS (
    SELECT r.*, w.total_ms AS waits_ms, w.ranked_ms AS waits_ranked,
           ROW_NUMBER() OVER (ORDER BY ISNULL(w.ranked_ms, 0) DESC, r.query_id) AS rn_waits,
           ROW_NUMBER() OVER (ORDER BY ISNULL(r.log_bytes, 0)   DESC, r.query_id) AS rn_log,
           ROW_NUMBER() OVER (ORDER BY ISNULL(r.tempdb_pages, 0) DESC, r.query_id) AS rn_tempdb,
           ROW_NUMBER() OVER (ORDER BY ISNULL(r.read_ops, 0)    DESC, r.query_id) AS rn_reads
    FROM runtime    AS r
    LEFT JOIN waits AS w ON w.query_id = r.query_id
),
best AS (
    SELECT r.query_id, m.metric_order, m.rn,
           ROW_NUMBER() OVER (PARTITION BY r.query_id ORDER BY m.rn, m.metric_order) AS dedupe
    FROM ranked AS r
    CROSS APPLY (VALUES (1, r.rn_waits,  r.waits_ranked),
                        (2, r.rn_log,    r.log_bytes),
                        (3, r.rn_tempdb, r.tempdb_pages),
                        (4, r.rn_reads,  r.read_ops)) AS m(metric_order, rn, value)
    WHERE m.value > 0
),
capped AS (            /* ORDER BY rn, metric_order IS the round robin */
    SELECT TOP (50) query_id, rn, metric_order
    FROM best
    WHERE dedupe = 1
    ORDER BY rn, metric_order, query_id
)
INSERT INTO #retained
SELECT r.query_id, r.object_id, r.plans, r.executions, r.duration_us, r.cpu_us,
       r.log_bytes, r.log_bytes_max, r.tempdb_pages, r.tempdb_max,
       r.read_ops, r.read_pages, r.waits_ms, r.waits_ranked,
       r.rn_waits, r.rn_log, r.rn_tempdb, r.rn_reads, c.rn, c.metric_order
FROM capped AS c
JOIN ranked AS r ON r.query_id = c.query_id
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── by_query ───────── */
SELECT r.query_id                                                   AS [query_id],
       CASE
           WHEN r.object_id = 0
               THEN '(ad hoc batch)'
           WHEN OBJECT_SCHEMA_NAME(r.object_id) IS NULL
               THEN '(dropped object, object_id ' + CAST(r.object_id AS VARCHAR(20)) + ')'
           ELSE OBJECT_SCHEMA_NAME(r.object_id) + '.' + OBJECT_NAME(r.object_id)
       END                                                          AS [object],
       r.plans                                                      AS [plans],
       r.executions                                                 AS [executions],
       CAST(r.duration_us / 1000.0 AS DECIMAL(18,1))                AS [total.duration_ms],
       CAST(r.cpu_us / 1000.0 AS DECIMAL(18,1))                     AS [total.cpu_ms],
       ISNULL(r.waits_ms, 0)                                        AS [waits.total_ms],
       ISNULL(r.waits_ranked, 0)                                    AS [waits.ranked_ms],
       CAST(r.log_bytes AS BIGINT)                                  AS [log.bytes_total],
       CAST(r.log_bytes / NULLIF(r.executions, 0) AS DECIMAL(18,1)) AS [log.bytes_per_execution],
       r.log_bytes_max                                              AS [log.bytes_max],
       CAST(r.tempdb_pages AS BIGINT)                               AS [tempdb.pages_total],
       CAST(r.tempdb_pages / NULLIF(r.executions, 0) AS DECIMAL(18,1)) AS [tempdb.pages_per_execution],
       r.tempdb_max                                                 AS [tempdb.pages_max],
       CAST(r.read_ops AS BIGINT)                                   AS [physical.read_ops_total],
       CAST(r.read_ops / NULLIF(r.executions, 0) AS DECIMAL(18,1))  AS [physical.read_ops_per_execution],
       CAST(r.read_pages AS BIGINT)                                 AS [physical.pages_total],
       -- The raw ranks: whichever of the four let the query in is the
       -- smallest of them.
       r.rn_waits                                                   AS [rank.waits],
       r.rn_log                                                     AS [rank.log],
       r.rn_tempdb                                                  AS [rank.tempdb],
       r.rn_reads                                                   AS [rank.physical_reads],
       LEFT(qt.query_sql_text, 500)                                 AS [text]
FROM       #retained                     AS r
JOIN       sys.query_store_query          AS q  ON q.query_id = r.query_id
JOIN       sys.query_store_query_text     AS qt ON qt.query_text_id = q.query_text_id
ORDER BY r.slot_rn, r.slot_metric, r.query_id
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── waits ─────────
   One row per retained query and wait category, every category the store
   recorded for it, Idle and User Wait included and labelled. The join to
   sys.query_store_wait_stats goes through the plans of the retained queries
   only. */
SELECT r.query_id                                                   AS [query_id],
       ws.wait_category_desc                                        AS [category],
       CASE ws.wait_category WHEN 11 THEN 'idle'
                             WHEN 18 THEN 'user_wait'
                             ELSE 'resource' END                    AS [class],
       SUM(ws.total_query_wait_time_ms)                             AS [wait_ms],
       MAX(ws.max_query_wait_time_ms)                               AS [max_wait_ms]
FROM       #retained                AS r
JOIN       sys.query_store_plan     AS p  ON p.query_id = r.query_id
JOIN       sys.query_store_wait_stats AS ws ON ws.plan_id = p.plan_id
GROUP BY r.query_id, r.slot_rn, r.slot_metric, ws.wait_category, ws.wait_category_desc
ORDER BY r.slot_rn, r.slot_metric, r.query_id, SUM(ws.total_query_wait_time_ms) DESC, ws.wait_category
OPTION (RECOMPILE, MAXDOP 1);
