-- @scope:         database
-- @resultsets:    root:object, queries:array, plans:array
-- @permissions:   CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:       300
-- @min_version:   13
-- @requires_flag: query_store_compare
-- @discloses:     query_text
--
-- The Query Store read ACROSS A CHANGE: a migration, a release, a
-- compatibility level, an index dropped. The operator names when the change
-- happened (--query-store-compare-at) and this file returns, for the queries
-- whose cost moved most across it, the same measures on both sides.
-- docs/query-store-compare-spec.md is the design and records what it was
-- measured against.
--
-- 020 ranks the whole retained history and 021 reads one window: two runs of
-- 021 give two top-N lists selected independently, and a query that regressed
-- without having been heavy before is missing from the first. This one
-- selects on the difference.
--
-- THE CHANGE IS A SPAN, NOT AN INSTANT. The operator never knows the second:
-- a minute typed means "during that minute", a date "some time that day". Go
-- sends the span's bounds as @qs_change_from and @qs_change_to. Every interval
-- overlapping the span is excluded from both sides, because its averages mix
-- the two behaviours: measured on a one-minute store with an index dropped at
-- 15:50:13, the 15:50 interval held 585 executions of the old plan and 2943 of
-- the new.
--
-- THE SIDES ARE COMPUTED HERE, per database, because they depend on this
-- store's intervals, which Go cannot see. Both have the same length, a whole
-- number of intervals, capped at seven days, and hold only closed intervals:
-- the one still open at collection time is on neither. So a total on one side
-- can be set against the total on the other without a correction. The length
-- can be zero (a daily store and a change six hours ago); that is reported in
-- the root, not refused, because another database of the same run may have a
-- one-minute store and a usable comparison.
--
-- AN INTERVAL COUNT PROVES NOTHING ABOUT THE WORKLOAD. The store writes an
-- interval only when it captured something, and the instance's own background
-- queries fill intervals in an idle database. The counts are reported beside
-- the state; neither claims to separate "nothing ran" from "nothing recorded".
--
-- SELECTION IS BY STATEMENT, ROWS ARE BY query_id. A change of SET options
-- (a new driver brings one) splits a statement into a new query_id, and
-- ANSI_NULLS even into a new query_hash; query_text_id survives, but it is
-- shared by the same statement in two procedures, so (query_text_id,
-- object_id) is the statement. It is ranked as one, or a statement split
-- across the change would sit on one side per query_id and never rank as a
-- regression. Every query_id of a selected statement then gets its own row,
-- never merged: two settings can give two plans, and that difference may be
-- the finding.
--
-- NO JUDGEMENT IS APPLIED. The rankings decide what is returned, not what it
-- means; a query slower after the change may be slower for reasons that have
-- nothing to do with it, and telling which needs the deployment calendar.
--
-- The text is cut at 500 characters, as in 020, and no plan XML is returned:
-- the plans are --query-store-detail's disclosure, not this file's.
--
-- SQL Server 2016 is the floor, the Query Store's. Not collected for that
-- reason: sys.query_store_wait_stats (2017).

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @now datetimeoffset = SYSDATETIMEOFFSET(),
        @m int, @excl_from datetimeoffset, @excl_to datetimeoffset,
        @oldest datetimeoffset, @side_minutes int = 0;

/* NULL when the store was never enabled: the options view has no row then,
   and nothing below finds an interval either. */
SELECT @m = o.interval_length_minutes FROM sys.database_query_store_options AS o
OPTION (RECOMPILE, MAXDOP 1);

/* The excluded span: every interval overlapping the change. When none does,
   nothing was captured during it, and the bounds move out to the edges of
   the real intervals on either side rather than stopping in the middle of an
   absent one. Interval edges are read, never computed from @m: a change of
   interval length stretches the interval open at that moment (measured,
   16:15 to 16:30 became 16:15 to 00:00 on a switch to a day). */
SELECT @excl_from = MIN(i.start_time), @excl_to = MAX(i.end_time)
FROM sys.query_store_runtime_stats_interval AS i
WHERE i.start_time < @qs_change_to AND i.end_time > @qs_change_from
OPTION (RECOMPILE, MAXDOP 1);

IF @excl_from IS NULL
    SELECT @excl_from = ISNULL(MAX(i.end_time), @qs_change_from)
    FROM sys.query_store_runtime_stats_interval AS i
    WHERE i.end_time <= @qs_change_from
    OPTION (RECOMPILE, MAXDOP 1);
IF @excl_to IS NULL
    SELECT @excl_to = ISNULL(MIN(i.start_time), @qs_change_to)
    FROM sys.query_store_runtime_stats_interval AS i
    WHERE i.start_time >= @qs_change_to
    OPTION (RECOMPILE, MAXDOP 1);

/* L is the shortest of three lengths, in whole minutes (seconds divided,
   because DATEDIFF(minute) counts boundaries crossed, not time elapsed):
   from the end of the excluded span to now; from the oldest interval the
   store still holds to the start of the excluded span, so the before side is
   never nominally longer than the history behind it (a store enabled or
   cleared shortly before a migration would otherwise compare a few hours of
   before with a week of after, and the totals would measure retention); and
   a week. Then rounded down to a whole number of intervals. Never negative:
   the excluded span ends after now when the interval still open at
   collection time overlaps the change, and L is then zero. */
SELECT @oldest = MIN(i.start_time) FROM sys.query_store_runtime_stats_interval AS i
OPTION (RECOMPILE, MAXDOP 1);
IF @m > 0 AND @excl_to < @now AND @oldest < @excl_from
    SELECT @side_minutes = MIN(x.minutes) / @m * @m
    FROM (VALUES (DATEDIFF_BIG(second, @excl_to, @now) / 60),
                 (DATEDIFF_BIG(second, @oldest, @excl_from) / 60),
                 (CAST(10080 AS bigint))) AS x(minutes);

/* Each interval on one side or none, decided once. Whole intervals only: a
   side never takes a part of an interval, at either end. */
DECLARE @side TABLE (interval_id bigint PRIMARY KEY, side char(1) NOT NULL);
IF @side_minutes > 0
    INSERT INTO @side (interval_id, side)
    SELECT i.runtime_stats_interval_id,
           CASE WHEN i.end_time <= @excl_from THEN 'B' ELSE 'A' END
    FROM sys.query_store_runtime_stats_interval AS i
    WHERE (i.start_time >= DATEADD(minute, -@side_minutes, @excl_from) AND i.end_time <= @excl_from)
       OR (i.start_time >= @excl_to AND i.end_time <= DATEADD(minute, @side_minutes, @excl_to))
    OPTION (RECOMPILE, MAXDOP 1);

/* One row per plan and side. Sums of avg * count, so every average below is
   weighted by executions, as in 020 and 021. Microseconds here, converted at
   projection. */
DECLARE @agg TABLE (
    plan_id bigint NOT NULL, side char(1) NOT NULL,
    executions bigint, duration float, cpu float, logical_reads float,
    physical_reads float, logical_writes float, memory_pages float, row_count float,
    first_execution datetimeoffset, last_execution datetimeoffset,
    PRIMARY KEY (plan_id, side));
INSERT INTO @agg
SELECT rs.plan_id, s.side,
       SUM(rs.count_executions),
       SUM(rs.avg_duration            * rs.count_executions),
       SUM(rs.avg_cpu_time            * rs.count_executions),
       SUM(rs.avg_logical_io_reads    * rs.count_executions),
       SUM(rs.avg_physical_io_reads   * rs.count_executions),
       SUM(rs.avg_logical_io_writes   * rs.count_executions),
       SUM(rs.avg_query_max_used_memory * rs.count_executions),
       SUM(rs.avg_rowcount            * rs.count_executions),
       MIN(rs.first_execution_time), MAX(rs.last_execution_time)
FROM sys.query_store_runtime_stats AS rs
JOIN @side AS s ON s.interval_id = rs.runtime_stats_interval_id
GROUP BY rs.plan_id, s.side
OPTION (RECOMPILE, MAXDOP 1);

/* The selection is made on STATEMENTS, not on query_ids. A statement is
   (query_text_id, object_id): the same text in the same module. A change of
   SET options, which a new driver brings with it, gives every statement a new
   query_id (and, for ANSI_NULLS, a new query_hash), one before the change and
   one after; ranked by query_id, each half would be on one side only, the
   per-execution rankings could not see it, and a split statement that
   regressed would lose its place to unchanged heavy ones. query_text_id alone
   is not the statement: two procedures holding the same text share it.

   Three rankings over statements:
     cpu_per_execution, duration_per_execution: statements on BOTH sides only,
       by |avg after - avg before| * executions after, the change in
       per-execution cost at the after side's volume;
     cpu_total: every statement, by |total after - total before|, a missing
       side counting as zero. The sides have the same length and the before
       side is never longer than the history behind it, so totals compare
       directly, and this is the ranking that sees a statement appear, vanish
       or change volume.
   Then 021's round robin: each statement keeps its best (rank, metric) pair,
   and ORDER BY rank, metric, then the statement's lowest query_id fills the
   cap one place of each ranking at a time. The tie-break is what makes two
   collections of an unchanged store select the same statements. */
DECLARE @stmt TABLE (
    query_text_id bigint NOT NULL, object_id bigint NOT NULL,
    selected_by varchar(40) NOT NULL, sort_rn int NOT NULL, sort_metric int NOT NULL,
    first_query_id bigint NOT NULL,
    rn_cpu_exec int, rn_duration_exec int, rn_cpu_total int,
    PRIMARY KEY (query_text_id, object_id));
WITH perStatement AS (
    SELECT q.query_text_id, q.object_id, MIN(q.query_id) AS first_query_id,
           SUM(CASE WHEN a.side = 'B' THEN a.executions END) AS eb,
           SUM(CASE WHEN a.side = 'A' THEN a.executions END) AS ea,
           SUM(CASE WHEN a.side = 'B' THEN a.cpu END)        AS cb,
           SUM(CASE WHEN a.side = 'A' THEN a.cpu END)        AS ca,
           SUM(CASE WHEN a.side = 'B' THEN a.duration END)   AS db,
           SUM(CASE WHEN a.side = 'A' THEN a.duration END)   AS da
    FROM @agg AS a
    JOIN sys.query_store_plan  AS p ON p.plan_id = a.plan_id
    JOIN sys.query_store_query AS q ON q.query_id = p.query_id
    GROUP BY q.query_text_id, q.object_id
),
ranked AS (
    SELECT query_text_id, object_id, first_query_id,
           CASE WHEN eb > 0 AND ea > 0 THEN ROW_NUMBER() OVER (
                ORDER BY CASE WHEN eb > 0 AND ea > 0 THEN ABS(ca / ea - cb / eb) * ea END DESC,
                         first_query_id) END AS rn_cpu_exec,
           CASE WHEN eb > 0 AND ea > 0 THEN ROW_NUMBER() OVER (
                ORDER BY CASE WHEN eb > 0 AND ea > 0 THEN ABS(da / ea - db / eb) * ea END DESC,
                         first_query_id) END AS rn_duration_exec,
           ROW_NUMBER() OVER (ORDER BY ABS(ISNULL(ca, 0) - ISNULL(cb, 0)) DESC,
                              first_query_id)            AS rn_cpu_total
    FROM perStatement
),
perMetric AS (
    SELECT r.query_text_id, r.object_id, r.first_query_id, m.metric_order, m.rn
    FROM ranked AS r
    CROSS APPLY (VALUES (1, r.rn_cpu_exec), (2, r.rn_duration_exec),
                        (3, r.rn_cpu_total)) AS m(metric_order, rn)
    WHERE m.rn IS NOT NULL
),
best AS (
    SELECT query_text_id, object_id, first_query_id, metric_order, rn,
           ROW_NUMBER() OVER (PARTITION BY query_text_id, object_id
                              ORDER BY rn, metric_order) AS dedupe
    FROM perMetric
),
capped AS (
    SELECT TOP (@qs_top) query_text_id, object_id, first_query_id, rn, metric_order
    FROM best
    WHERE dedupe = 1
    ORDER BY rn, metric_order, first_query_id
)
INSERT INTO @stmt (query_text_id, object_id, selected_by, sort_rn, sort_metric, first_query_id,
                   rn_cpu_exec, rn_duration_exec, rn_cpu_total)
SELECT c.query_text_id, c.object_id,
       CASE c.metric_order WHEN 1 THEN 'cpu_per_execution'
                           WHEN 2 THEN 'duration_per_execution'
                           ELSE 'cpu_total' END,
       c.rn, c.metric_order, c.first_query_id,
       r.rn_cpu_exec, r.rn_duration_exec, r.rn_cpu_total
FROM capped AS c
JOIN ranked AS r ON r.query_text_id = c.query_text_id AND r.object_id = c.object_id
OPTION (RECOMPILE, MAXDOP 1);

/* A forced plan is a decision someone took, and a forcing that fails is the
   part that goes unnoticed: its statement enters outside the cap, when a
   query of it ran on either side. */
INSERT INTO @stmt (query_text_id, object_id, selected_by, sort_rn, sort_metric, first_query_id)
SELECT q.query_text_id, q.object_id, 'forced_plan', 2147483647, 0, MIN(q.query_id)
FROM sys.query_store_plan  AS p
JOIN sys.query_store_query AS q ON q.query_id = p.query_id
WHERE (p.is_forced_plan = 1 OR p.force_failure_count > 0)
  AND NOT EXISTS (SELECT 1 FROM @stmt AS x
                  WHERE x.query_text_id = q.query_text_id AND x.object_id = q.object_id)
  AND EXISTS (SELECT 1 FROM @agg AS a JOIN sys.query_store_plan AS p2 ON p2.plan_id = a.plan_id
              WHERE p2.query_id = q.query_id)
GROUP BY q.query_text_id, q.object_id
OPTION (RECOMPILE, MAXDOP 1);

/* Every query_id of a selected statement that ran on either side, one row
   each: the statement is the unit of selection, the query_id stays the unit
   of the rows, because two SET combinations can give two plans and that
   difference may be the finding. */
DECLARE @sel TABLE (
    query_id bigint PRIMARY KEY, selected_by varchar(40) NOT NULL,
    sort_rn int NOT NULL, sort_metric int NOT NULL, sort_query bigint NOT NULL,
    rn_cpu_exec int, rn_duration_exec int, rn_cpu_total int);
INSERT INTO @sel
SELECT DISTINCT q.query_id, st.selected_by, st.sort_rn, st.sort_metric, st.first_query_id,
       st.rn_cpu_exec, st.rn_duration_exec, st.rn_cpu_total
FROM @stmt AS st
JOIN sys.query_store_query AS q ON q.query_text_id = st.query_text_id AND q.object_id = st.object_id
JOIN sys.query_store_plan  AS p ON p.query_id = q.query_id
JOIN @agg                  AS a ON a.plan_id = p.plan_id
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── root ─────────
   A LEFT JOIN from sys.databases, as in 021: a database whose store was never
   enabled has no options row, and a root with no row is an encoder error. */
SELECT DB_NAME()                                                  AS [database],
       @now                                                       AS [collected_at],
       o.actual_state_desc                                        AS [state.actual],
       o.desired_state_desc                                       AS [state.desired],
       o.readonly_reason                                          AS [state.readonly_reason],
       o.query_capture_mode_desc                                  AS [state.capture_mode],
       o.interval_length_minutes                                  AS [interval_minutes],
       @qs_change_from                                            AS [change.from],
       @qs_change_to                                              AS [change.to],
       @excl_from                                                 AS [excluded.from],
       @excl_to                                                   AS [excluded.to],
       /* The interval overlapping the change can still be open: its
          executions are then partial, and there is no after side yet. */
       CAST(CASE WHEN @excl_to > @now THEN 1 ELSE 0 END AS bit)   AS [excluded.still_open],
       (SELECT COUNT(*) FROM sys.query_store_runtime_stats_interval AS i
         WHERE i.start_time >= @excl_from AND i.end_time <= @excl_to)          AS [excluded.intervals],
       (SELECT SUM(rs.count_executions)
          FROM sys.query_store_runtime_stats AS rs
          JOIN sys.query_store_runtime_stats_interval AS i
            ON i.runtime_stats_interval_id = rs.runtime_stats_interval_id
         WHERE i.start_time < @excl_to AND i.end_time > @excl_from)           AS [excluded.executions],
       @oldest                                                    AS [oldest_interval],
       @side_minutes                                              AS [side_minutes],
       DATEADD(minute, -@side_minutes, @excl_from)                AS [before.from],
       @excl_from                                                 AS [before.to],
       (SELECT COUNT(*) FROM @side WHERE side = 'B')              AS [before.intervals],
       @excl_to                                                   AS [after.from],
       DATEADD(minute, @side_minutes, @excl_to)                   AS [after.to],
       (SELECT COUNT(*) FROM @side WHERE side = 'A')              AS [after.intervals],
       @qs_top                                                    AS [selection.cap],
       (SELECT COUNT(*) FROM @stmt)                               AS [selection.statements],
       (SELECT COUNT(*) FROM @sel)                                AS [selection.queries]
FROM sys.databases AS d
LEFT JOIN sys.database_query_store_options AS o ON 1 = 1
WHERE d.database_id = DB_ID()
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── queries ─────────
   One row per selected query_id, both sides side by side. A side where the
   query did not run has NULL executions and NULL averages, never zeros: zero
   would read as a query that ran and cost nothing. */
WITH perQuerySide AS (
    SELECT p.query_id, a.side,
           SUM(a.executions) AS n, COUNT(DISTINCT a.plan_id) AS plans,
           SUM(a.duration) AS duration, SUM(a.cpu) AS cpu,
           SUM(a.logical_reads) AS logical_reads, SUM(a.physical_reads) AS physical_reads,
           SUM(a.logical_writes) AS logical_writes, SUM(a.memory_pages) AS memory_pages,
           SUM(a.row_count) AS row_count
    FROM @agg AS a
    JOIN sys.query_store_plan AS p ON p.plan_id = a.plan_id
    JOIN @sel AS s ON s.query_id = p.query_id
    GROUP BY p.query_id, a.side
)
SELECT q.query_id                                                 AS [query_id],
       q.query_text_id                                            AS [query_text_id],
       q.object_id                                                AS [object_id],
       /* 023's label: ad hoc and a dropped object must not both be NULL
          when the before side can hold objects dropped since. */
       CASE
           WHEN q.object_id IS NULL OR q.object_id = 0
               THEN '(ad hoc)'
           WHEN OBJECT_SCHEMA_NAME(q.object_id) IS NULL
               THEN '(dropped object, object_id ' + CAST(q.object_id AS VARCHAR(20)) + ')'
           ELSE OBJECT_SCHEMA_NAME(q.object_id) + '.' + OBJECT_NAME(q.object_id)
       END                                                        AS [object],
       CONVERT(varchar(18), q.query_hash, 1)                      AS [query_hash],
       q.context_settings_id                                      AS [context_settings_id],
       CONVERT(varchar(18), cs.set_options, 1)                    AS [set_options],
       s.selected_by                                              AS [selected_by],
       s.rn_cpu_exec                                              AS [rank.cpu_per_execution],
       s.rn_duration_exec                                         AS [rank.duration_per_execution],
       s.rn_cpu_total                                             AS [rank.cpu_total],
       b.n                                                        AS [before.executions],
       b.plans                                                    AS [before.plans],
       CAST(b.duration / b.n / 1000.0 AS decimal(18,3))           AS [before.duration_ms],
       CAST(b.cpu / b.n / 1000.0 AS decimal(18,3))                AS [before.cpu_ms],
       CAST(b.logical_reads / b.n AS decimal(18,1))               AS [before.logical_reads],
       CAST(b.physical_reads / b.n AS decimal(18,1))              AS [before.physical_reads],
       CAST(b.logical_writes / b.n AS decimal(18,1))              AS [before.logical_writes],
       CAST(b.memory_pages / b.n AS decimal(18,1))                AS [before.max_used_memory_pages],
       CAST(b.row_count / b.n AS decimal(18,1))                   AS [before.rowcount],
       a.n                                                        AS [after.executions],
       a.plans                                                    AS [after.plans],
       CAST(a.duration / a.n / 1000.0 AS decimal(18,3))           AS [after.duration_ms],
       CAST(a.cpu / a.n / 1000.0 AS decimal(18,3))                AS [after.cpu_ms],
       CAST(a.logical_reads / a.n AS decimal(18,1))               AS [after.logical_reads],
       CAST(a.physical_reads / a.n AS decimal(18,1))              AS [after.physical_reads],
       CAST(a.logical_writes / a.n AS decimal(18,1))              AS [after.logical_writes],
       CAST(a.memory_pages / a.n AS decimal(18,1))                AS [after.max_used_memory_pages],
       CAST(a.row_count / a.n AS decimal(18,1))                   AS [after.rowcount],
       LEFT(qt.query_sql_text, 500)                               AS [text]
FROM @sel AS s
JOIN      sys.query_store_query            AS q  ON q.query_id = s.query_id
JOIN      sys.query_store_query_text       AS qt ON qt.query_text_id = q.query_text_id
LEFT JOIN sys.query_context_settings       AS cs ON cs.context_settings_id = q.context_settings_id
LEFT JOIN perQuerySide AS b ON b.query_id = s.query_id AND b.side = 'B' AND b.n > 0
LEFT JOIN perQuerySide AS a ON a.query_id = s.query_id AND a.side = 'A' AND a.n > 0
ORDER BY s.sort_rn, s.sort_metric, s.sort_query, s.query_id
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── plans ─────────
   Every plan of a selected query that ran on either side. query_plan_hash
   groups plans of one shape; it is a hash, and 022 already treats a match on
   it as a match on a hash rather than an identity. compatibility_level and
   engine_version are those of the plan's LAST compilation (measured: a plan
   compiled at 170 reported 130 after the level changed and it recompiled to
   the same shape), so they are named for it and do not tell the sides apart. */
SELECT p.query_id                                                 AS [query_id],
       p.plan_id                                                  AS [plan_id],
       CONVERT(varchar(18), p.query_plan_hash, 1)                 AS [query_plan_hash],
       p.is_forced_plan                                           AS [is_forced_plan],
       p.force_failure_count                                      AS [force_failure_count],
       p.compatibility_level                                      AS [last_compile_compatibility_level],
       p.engine_version                                           AS [last_compile_engine_version],
       b.executions                                               AS [before.executions],
       b.first_execution                                          AS [before.first_execution],
       b.last_execution                                           AS [before.last_execution],
       CAST(b.duration / b.executions / 1000.0 AS decimal(18,3))  AS [before.duration_ms],
       CAST(b.cpu / b.executions / 1000.0 AS decimal(18,3))       AS [before.cpu_ms],
       CAST(b.logical_reads / b.executions AS decimal(18,1))      AS [before.logical_reads],
       CAST(b.physical_reads / b.executions AS decimal(18,1))     AS [before.physical_reads],
       CAST(b.logical_writes / b.executions AS decimal(18,1))     AS [before.logical_writes],
       CAST(b.memory_pages / b.executions AS decimal(18,1))       AS [before.max_used_memory_pages],
       CAST(b.row_count / b.executions AS decimal(18,1))          AS [before.rowcount],
       a.executions                                               AS [after.executions],
       a.first_execution                                          AS [after.first_execution],
       a.last_execution                                           AS [after.last_execution],
       CAST(a.duration / a.executions / 1000.0 AS decimal(18,3))  AS [after.duration_ms],
       CAST(a.cpu / a.executions / 1000.0 AS decimal(18,3))       AS [after.cpu_ms],
       CAST(a.logical_reads / a.executions AS decimal(18,1))      AS [after.logical_reads],
       CAST(a.physical_reads / a.executions AS decimal(18,1))     AS [after.physical_reads],
       CAST(a.logical_writes / a.executions AS decimal(18,1))     AS [after.logical_writes],
       CAST(a.memory_pages / a.executions AS decimal(18,1))       AS [after.max_used_memory_pages],
       CAST(a.row_count / a.executions AS decimal(18,1))          AS [after.rowcount]
FROM sys.query_store_plan AS p
JOIN @sel AS s ON s.query_id = p.query_id
LEFT JOIN @agg AS b ON b.plan_id = p.plan_id AND b.side = 'B' AND b.executions > 0
LEFT JOIN @agg AS a ON a.plan_id = p.plan_id AND a.side = 'A' AND a.executions > 0
WHERE b.plan_id IS NOT NULL OR a.plan_id IS NOT NULL
ORDER BY s.sort_rn, s.sort_metric, s.sort_query, p.query_id, p.plan_id
OPTION (RECOMPILE, MAXDOP 1);
