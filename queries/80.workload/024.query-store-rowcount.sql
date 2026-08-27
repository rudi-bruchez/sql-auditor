-- @scope:       database
-- @resultsets:  root:object, by_query:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     180
-- @min_version: 14
--
-- How many rows each execution returned, and how the executions were spread
-- over time. Those are the two facts that turn a call count into a diagnosis,
-- and 023.query-store-most-executed.sql cannot carry either.
--
-- WHY A SEPARATE FILE AND NOT TWO MORE COLUMNS ON 023. The rowcount columns
-- of sys.query_store_runtime_stats — count_rowcount, avg_rowcount, max_rowcount
-- and their siblings — arrived in SQL Server 2017. 023 is gated at 2016,
-- because that is where the Query Store starts, and adding a 2017 column to it
-- would break it on every 2016 instance in the field. The corpus already pairs
-- a portable collector with a version sibling that adds the newer columns:
-- 20.databases/010.all-databases.sql and 011.all-databases-2014.sql are the
-- same arrangement.
--
-- WHAT THE ROWCOUNT IS FOR. A call count on its own cannot tell a row-by-row
-- loop from an ordinary chatty client. Both look like a query executed a
-- million times for a few logical reads. They are different problems with
-- different owners: a loop is one business operation issuing one call per row,
-- and the fix is to send the set; session chatter is one call per connection,
-- and the fix is the connection pool. A million executions returning one row
-- each is the first. A million executions returning one row each, spread
-- evenly over every hour of the retention window, is the second.
--
-- HENCE THE SPREAD, WHICH IS THE OTHER HALF. intervals_seen against the
-- instance's total tells whether a query is always there or comes in bursts,
-- and peak_interval_executions says how hard the burst hits. A loop shows up
-- as a query absent from a third of the intervals and then running tens of
-- thousands of times inside one of them. Per-connection chatter is flat: it
-- appears in every interval and its busiest one is unremarkable.
--
-- NOTHING HERE IS LABELLED RBAR. There is no is_rbar column and no threshold,
-- for the reason 023 states and this file inherits: deciding that four million
-- calls are a defect requires knowing what the application does, and that is
-- not in the Query Store. What is here is the arithmetic that makes the
-- decision possible without a second round trip to the client.
--
-- THE TRAP IN avg_rowcount, and it is worth naming before someone quotes the
-- column. It is rows per EXECUTION of the statement, not rows per business
-- operation. An INSERT of one row reports 1, and ten thousand of them report 1
-- ten thousand times, which is the finding. But a SELECT feeding an API cursor
-- reports the rows of that fetch, not of the result set, so a cursor walked one
-- row at a time reads as one row per execution too — correctly, since that is
-- what happened, but the culprit there is the cursor and not the caller's
-- loop. Read this file next to 021's plans, which say whether a cursor is
-- involved.
--
-- It aggregates the WHOLE RETAINED WINDOW and takes no window parameter, for
-- the same reason as 023: a loop that ran a million times last month is still
-- the finding.
--
-- On a database whose Query Store is off, by_query is empty and root still
-- reports the state. That is how the rest of the corpus says "nothing here".

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

/* ───────── root ─────────
   The denominator every share below is computed against, stated once rather
   than repeated on each row, and the Query Store's state so that an empty
   by_query is readable as "switched off" rather than as "collector failed". */
SELECT DB_NAME()                                                   AS [database],
       (SELECT COUNT(*) FROM sys.query_store_runtime_stats_interval) AS [window.intervals_total],
       (SELECT MIN(start_time) FROM sys.query_store_runtime_stats_interval) AS [window.oldest],
       (SELECT MAX(end_time) FROM sys.query_store_runtime_stats_interval)   AS [window.newest],
       CAST((SELECT actual_state_desc FROM sys.database_query_store_options) AS NVARCHAR(60)) AS [state.actual],
       CAST((SELECT query_capture_mode_desc FROM sys.database_query_store_options) AS NVARCHAR(60)) AS [state.capture_mode]
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── by_query ─────────
   TOP (50) by call count, like 023, so the two files rank the same population
   and can be read side by side.

   per_interval collapses the plans of one query inside one interval before
   anything is measured. Without it, peak_interval_executions would be the
   busiest PLAN of the busiest interval, and a query whose load is split
   across two plans — which every parameterised query here has, since the
   Query Store keeps one plan per compilation — would report roughly half its
   real burst. That is the kind of arithmetic error nothing downstream can
   catch.

   rows_total is the product summed, not the average multiplied: a query whose
   rowcount varies by interval is common, and SUM(count_executions *
   avg_rowcount) is the only form that stays true when it does. */
WITH per_interval AS (
    SELECT q.query_id                                  AS query_id,
           q.object_id                                 AS object_id,
           i.runtime_stats_interval_id                 AS interval_id,
           SUM(rs.count_executions)                    AS executions,
           SUM(rs.count_executions * rs.avg_rowcount)  AS rows_returned,
           SUM(rs.count_executions * rs.avg_cpu_time)  AS cpu_us,
           SUM(rs.count_executions * rs.avg_logical_io_reads) AS logical_reads,
           MAX(rs.max_rowcount)                        AS rows_max
    FROM       sys.query_store_query                      AS q
    JOIN       sys.query_store_plan                       AS p  ON p.query_id = q.query_id
    JOIN       sys.query_store_runtime_stats              AS rs ON rs.plan_id = p.plan_id
    JOIN       sys.query_store_runtime_stats_interval     AS i  ON i.runtime_stats_interval_id = rs.runtime_stats_interval_id
    GROUP BY q.query_id, q.object_id, i.runtime_stats_interval_id
)
SELECT TOP (50)
       pi.query_id                                                AS [query_id],
       CASE
           WHEN pi.object_id IS NULL OR pi.object_id = 0
               THEN '(ad hoc batch)'
           WHEN OBJECT_SCHEMA_NAME(pi.object_id) IS NULL
               THEN '(dropped object, object_id ' + CAST(pi.object_id AS VARCHAR(20)) + ')'
           ELSE OBJECT_SCHEMA_NAME(pi.object_id) + '.' + OBJECT_NAME(pi.object_id)
       END                                                        AS [object],
       SUM(pi.executions)                                         AS [executions],
       CAST(SUM(pi.rows_returned) / NULLIF(SUM(pi.executions), 0)
            AS DECIMAL(18,2))                                     AS [rows.per_execution],
       MAX(pi.rows_max)                                           AS [rows.max_seen],
       CAST(SUM(pi.rows_returned) AS BIGINT)                      AS [rows.total],
       CAST(SUM(pi.logical_reads) / NULLIF(SUM(pi.executions), 0)
            AS DECIMAL(18,1))                                     AS [cost.reads_per_execution],
       CAST(SUM(pi.cpu_us) / 1000000.0 AS DECIMAL(18,1))          AS [cost.cpu_sec],
       COUNT(*)                                                   AS [spread.intervals_seen],
       MAX(pi.executions)                                         AS [spread.peak_interval_executions],
       CAST(MAX(pi.executions) * 1.0 / NULLIF(SUM(pi.executions), 0)
            AS DECIMAL(9,4))                                      AS [spread.busiest_interval_share],
       LEFT(qt.query_sql_text, 500)                               AS [text]
FROM       per_interval                       AS pi
JOIN       sys.query_store_query               AS q  ON q.query_id = pi.query_id
JOIN       sys.query_store_query_text          AS qt ON qt.query_text_id = q.query_text_id
GROUP BY pi.query_id, pi.object_id, LEFT(qt.query_sql_text, 500)
ORDER BY SUM(pi.executions) DESC
OPTION (RECOMPILE, MAXDOP 1);
