-- @scope:        database
-- @resultsets:   root:object, selected:array, intervals:array
-- @permissions:  CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:      300
-- @min_version:  13
-- @requires_flag: query_store_detail
-- @writer:       query-store-detail
--
-- The DEEP read of the Query Store: the full text of the queries retained, the
-- execution plans behind them, and the per-interval statistics each plan
-- accumulated.
--
-- 80.workload/020.query-store.sql is the SUMMARY, and the two are deliberately
-- separate files. The summary answers "what is heavy here" in one JSON that
-- costs nothing to read and discloses nothing beyond 500 characters of SQL.
-- This file answers "what is that query actually doing", which needs the whole
-- statement and its plan — and that is a different disclosure decision, not a
-- larger version of the same one.
--
-- Which is why this file carries a flag and 020 does not. The text is
-- collected UNTRUNCATED and the plans come with it: application SQL with the
-- literal values of the workload in it, and plan XML naming every index,
-- column and parameter the statement touched. 052.session-text.sql puts the
-- same class of data behind an explicit flag, and so does this. The default is
-- "not collected".
--
-- EVERY STATE BUT OFF IS READ — READ_WRITE, READ_ONLY, ERROR and
-- READ_CAPTURE_SECONDARY alike. A store that stopped recording still holds the
-- history from before it stopped, and that history exists nowhere else on the
-- instance: it is not in the plan cache, which is emptied by a restart, and it
-- is not in any DMV, which knows only about now. readonly_reason is projected
-- raw so the reader learns why it stopped.
--
-- The state is REPORTED, never filtered on, in the SQL. This file always
-- returns its root row — a LEFT JOIN from sys.databases, for the reason
-- 20.databases/022.query-store.sql documents — so a database with the store
-- switched off still produces an index saying exactly that. Deciding what to
-- do with an OFF is the writer's business, and "the Query Store is off here"
-- and "the collector never ran here" have to stay distinguishable.
--
-- THE EFFECTIVE WINDOW IS REPORTED BESIDE THE REQUESTED ONE. A store holding
-- two days cannot answer a seven-day question, and it does not say so: it
-- returns what it has, which looks exactly like a quiet week. Both bounds are
-- clamped to what the store still holds, and the interval count says how many
-- buckets actually intersect the request. The effective window is aligned on
-- interval boundaries, so its precision is interval_minutes and that value is
-- projected too.
--
-- Durations are converted from microseconds to milliseconds, as
-- 020.query-store.sql already does. A column that silently changes unit
-- between two files of the same corpus is a defect waiting to be quoted in a
-- report.
--
-- AT MOST THREE PLANS PER QUERY CARRY THEIR XML. Every plan is still returned,
-- with its metadata, its size and its per-interval statistics; only the XML of
-- the fourth and beyond is nulled out, and the writer records each one as a
-- named omission. Nothing is filtered: a plan the reader cannot open is still a
-- plan the archive says exists.
--
-- NO JUDGEMENT IS APPLIED. Nothing here is labelled a regression, and no
-- ranking is a verdict: comparing two intervals and deciding a plan got worse
-- is analysis, and it needs the deployment calendar to be worth anything.
--
-- SQL Server 2016 is the floor for this file, which is above the corpus floor
-- of 2012 — the Query Store does not exist before it. Not collected for that
-- reason:
--   sys.query_store_wait_stats            (2017)
--   sys.query_store_query_hints           (2022)
--   sys.query_store_plan_feedback         (2022)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

/* ───────── root ─────────
   A LEFT JOIN from sys.databases, not a bare SELECT from
   sys.database_query_store_options: on a database where the Query Store has
   never been enabled that view returns NO rows, and a root result set with no
   row is an encoder error — so an ordinary database with the feature off would
   produce a recorded error, an exit 2 and manifest noise on every run.
   20.databases/022.query-store.sql solved this first; this is the same shape.
   state.actual then arrives NULL, which the writer reads exactly like OFF. */
SELECT DB_NAME()                                                  AS [database],
       SYSDATETIME()                                              AS [collected_at],
       o.actual_state_desc                                        AS [state.actual],
       o.desired_state_desc                                       AS [state.desired],
       o.readonly_reason                                          AS [state.readonly_reason],
       /* Under state, not config: the index keeps the state and window blocks
          and drops the rest, and capture mode is what says whether a query
          missing from the selection was cheap or merely never captured. */
       o.query_capture_mode_desc                                  AS [state.capture_mode],
       @qs_from                                                   AS [window.requested_from],
       @qs_to                                                     AS [window.requested_to],
       /* The later of what was asked for and what the store still holds; the
          symmetric expression with MIN bounds the other end. Both are clamped,
          because a window can fall off either side of the retention — and asking
          about a one-hour incident eighteen days ago is exactly how it falls off
          the old end. */
       (SELECT MAX(x.f) FROM (VALUES
            (@qs_from),
            ((SELECT MIN(i.start_time) FROM sys.query_store_runtime_stats_interval AS i))
        ) AS x(f))                                                AS [window.effective_from],
       (SELECT MIN(x.t) FROM (VALUES
            (@qs_to),
            ((SELECT MAX(i.end_time) FROM sys.query_store_runtime_stats_interval AS i))
        ) AS x(t))                                                AS [window.effective_to],
       /* The buckets that actually intersect the request, not the store's
          total: zero here and a healthy store is a window that missed. */
       (SELECT COUNT(*) FROM sys.query_store_runtime_stats_interval AS i
         WHERE i.start_time < @qs_to AND i.end_time > @qs_from)    AS [window.intervals],
       /* The effective bounds are interval-aligned, so this is their precision
          and the reader has to be told it. */
       o.interval_length_minutes                                  AS [window.interval_minutes],
       @qs_top                                                    AS [selection.cap]
FROM sys.databases AS d
LEFT JOIN sys.database_query_store_options AS o ON 1 = 1
WHERE d.database_id = DB_ID()
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── selected ─────────
   A capped round robin over FOUR metrics, then the forced plans outside the
   cap. This is the one statement in the corpus whose shape is not obvious from
   reading it, so it is written and commented in the order it is built. */
WITH agg AS (          /* one row per query, aggregated over the window only */
    SELECT q.query_id,
           SUM(rs.avg_duration          * rs.count_executions) AS total_duration,
           SUM(rs.avg_cpu_time          * rs.count_executions) AS total_cpu,
           SUM(rs.avg_logical_io_reads  * rs.count_executions) AS total_reads,
           SUM(rs.avg_logical_io_writes * rs.count_executions) AS total_writes,
           SUM(rs.avg_physical_io_reads * rs.count_executions) AS total_physical_reads,
           SUM(rs.count_executions)                            AS executions
    FROM       sys.query_store_query         AS q
    JOIN       sys.query_store_plan          AS p  ON p.query_id = q.query_id
    JOIN       sys.query_store_runtime_stats AS rs ON rs.plan_id = p.plan_id
    JOIN       sys.query_store_runtime_stats_interval AS i
                 ON i.runtime_stats_interval_id = rs.runtime_stats_interval_id
    WHERE i.start_time < @qs_to AND i.end_time > @qs_from   /* overlap, not containment:
       a 60-minute bucket straddling the requested start belongs to the window, and
       demanding containment would silently drop the first bucket of every query. */
    GROUP BY q.query_id
),
ranked AS (            /* the four rankings, side by side */
    SELECT query_id,
           ROW_NUMBER() OVER (ORDER BY total_duration DESC, query_id) AS rn_duration,
           ROW_NUMBER() OVER (ORDER BY total_cpu      DESC, query_id) AS rn_cpu,
           ROW_NUMBER() OVER (ORDER BY total_reads    DESC, query_id) AS rn_reads,
           ROW_NUMBER() OVER (ORDER BY executions     DESC, query_id) AS rn_exec
    FROM agg
),
/* Unpivoted to one row per (query, metric): the round robin below orders on
   the rank first and the metric second, which is what makes it a round robin
   rather than a concatenation of four lists. The fourth metric is execution
   count, and it is there for the query that is invisible on the other three:
   four million calls at 0.3 ms rank nowhere on duration, CPU or reads, and a
   row-by-row loop is exactly that shape. */
perMetric AS (
    SELECT r.query_id, m.metric_order, m.rn
    FROM ranked AS r
    CROSS APPLY (VALUES (1, r.rn_duration), (2, r.rn_cpu),
                        (3, r.rn_reads),    (4, r.rn_exec)) AS m(metric_order, rn)
),
/* Each query keeps its single best (rank, metric) pair, so a query that leads
   three metrics occupies one slot, not three — which is exactly why fifty
   slots reach deeper than fifty divided by four. */
best AS (
    SELECT query_id, metric_order, rn,
           ROW_NUMBER() OVER (PARTITION BY query_id ORDER BY rn, metric_order) AS dedupe
    FROM perMetric
),
/* The cap. ORDER BY rn, metric_order, query_id IS the round robin: every
   metric's first place, then every metric's second place, and so on. The query
   that leads logical reads and sits eightieth everywhere else enters on the
   first pass — which was the whole point, and a cap that sorted on a synthetic
   score would undo it. query_id is the tie-break, and it is not decoration:
   without it two consecutive collections of an unchanged store can differ at
   the fiftieth place with nothing anywhere saying so. */
capped AS (
    SELECT TOP (@qs_top) query_id, rn, metric_order
    FROM best
    WHERE dedupe = 1
    ORDER BY rn, metric_order, query_id
),
/* Forced plans enter OUTSIDE the cap. They are few, and they are not here on
   account of their cost: someone took a decision, and force_failure_count is
   the part that goes unnoticed — a plan that can no longer be forced stops
   being applied without anything raising an error. */
forced AS (
    SELECT DISTINCT p.query_id
    FROM sys.query_store_plan AS p
    WHERE p.is_forced_plan = 1 OR p.force_failure_count > 0
),
/* The two populations merged, keeping the round robin's position for the
   ranked ones and a sentinel for a query that arrived only because it is
   forced, so those sort last instead of interleaving. is_forced_selection is
   carried so _index.json can count the two populations separately. */
selection AS (
    SELECT u.query_id,
           MIN(u.sort_rn)             AS sort_rn,
           MIN(u.sort_metric)         AS sort_metric,
           MAX(u.is_forced_selection) AS is_forced_selection
    FROM (
        SELECT c.query_id, c.rn, c.metric_order, 0 AS is_forced_selection
        FROM capped AS c
        UNION ALL
        SELECT f.query_id, 2147483647, 0, 1
        FROM forced AS f
    ) AS u(query_id, sort_rn, sort_metric, is_forced_selection)
    GROUP BY u.query_id
),
/* Every plan of every selected query, numbered. Nothing here removes a row:
   this rank is read by the projection below, which NULLs the plan XML past the
   third plan and leaves the plan's metadata, its size and its per-interval
   statistics exactly where they were. A query in a thirty-day store accumulates
   a plan per recompile, and each of them carries up to 8 MiB of XML that is
   materialised in memory before a byte is written — that is what is bounded,
   and only that.

   is_forced_plan FIRST, and it is not decoration. A query can be in the
   selection BECAUSE one of its plans is forced, and evicting that plan's XML
   would erase the evidence for its own selection. The Query Store forces at
   most one plan per query, so one slot always suffices for it and the remaining
   two still go to the most recently executed.

   plan_id DESC is the tie-break, for the reason query_id is one in the round
   robin above: without it two consecutive collections of an unchanged store can
   keep different plans with nothing anywhere saying so. last_execution_time is
   NULL for a plan compiled but never executed, and those sort last under DESC,
   which is where they belong. */
planRank AS (
    SELECT p.plan_id,
           ROW_NUMBER() OVER (PARTITION BY p.query_id
                              ORDER BY p.is_forced_plan       DESC,
                                       p.last_execution_time  DESC,
                                       p.plan_id              DESC) AS plan_rank,
           COUNT(*) OVER (PARTITION BY p.query_id)                  AS plan_count
    FROM sys.query_store_plan AS p
    JOIN selection            AS s ON s.query_id = p.query_id
)
SELECT q.query_id                                                 AS [query_id],
       p.plan_id                                                  AS [plan_id],
       /* The text WHOLE — no LEFT(). That truncation is what keeps
          020.query-store.sql ungated; this file is gated instead. */
       qt.query_sql_text                                          AS [text],
       /* A conditional PROJECTION, never a WHERE filter: an oversized plan
          arrives as a NULL the writer can tell apart from a genuinely absent
          one, because the size beside it says which. The literal is written
          out rather than parameterised so a DBA reading the exported file sees
          the cap; it is the same constant on the Go side, maxPlanBytes, and
          TestPlanCapIsTheSameNumberInTheCorpus reads this line to hold the two
          together. Raising one alone would make an oversized plan arrive NULL
          with a size the writer no longer calls oversized, and the archive
          would then state that the Query Store holds no plan for it — a false
          fact about the server. DATALENGTH counts
          the nvarchar bytes, two per character, so the guard is conservative
          rather than exact — deliberately, and in the safe direction.

          The per-query plan cap is the SECOND condition of the same CASE, and
          it is here rather than in a WHERE for the reason above: the row stays,
          only the XML goes. Both conditions in one CASE so a plan that is both
          oversized and beyond the cap arrives NULL exactly once, with
          [plan.rank] and [plan.count] beside it saying which of the two it was
          and how many plans the query has. 3 is written out for the same reason
          8388608 is — a DBA reading the exported corpus must see the number —
          and maxPlansPerQuery on the Go side is held to it by
          TestPlanCountCapIsTheSameNumberInTheCorpus. */
       CASE WHEN DATALENGTH(p.query_plan) <= 8388608
             AND pr.plan_rank <= 3
            THEN p.query_plan END                                 AS [query_plan],
       DATALENGTH(p.query_plan)                                   AS [query_plan_bytes],
       p.is_forced_plan                                           AS [is_forced],
       CAST(sel.is_forced_selection AS BIT)                       AS [is_forced_selection],
       /* RAW ranks, always projected, never capped and never nulled. The round
          robin can retain a query whose best rank exceeds the cap when the
          early passes dedupe heavily, and a NULL there would read as "not
          ranked" in the very metric that let it in. rank.duration 3,
          rank.cpu 1, rank.logical_reads 812, rank.executions 4001 says more
          about a query than four bits ever could. A query selected only
          because a plan is forced has no rank at all, and gets NULL. */
       r.rn_duration                                              AS [rank.duration],
       r.rn_cpu                                                   AS [rank.cpu],
       r.rn_reads                                                 AS [rank.logical_reads],
       r.rn_exec                                                  AS [rank.executions],
       /* A loop lives inside a procedure, and the object name is what points
          at it. */
       q.object_id                                                AS [object_id],
       OBJECT_SCHEMA_NAME(q.object_id)
         + '.' + OBJECT_NAME(q.object_id)                         AS [object],
       q.query_parameterization_type_desc                         AS [parameterization],
       /* Always projected, for every plan including the ones whose XML was
          nulled: rank 4 of 9 is what tells a reader that the file is absent by
          decision and not because the store has nothing. */
       pr.plan_rank                                               AS [plan.rank],
       pr.plan_count                                              AS [plan.count],
       p.plan_group_id                                            AS [plan.group_id],
       p.engine_version                                           AS [plan.engine_version],
       p.compatibility_level                                      AS [plan.compatibility_level],
       CONVERT(varchar(18), p.query_plan_hash, 1)                 AS [plan.query_plan_hash],
       p.is_trivial_plan                                          AS [plan.is_trivial],
       p.is_parallel_plan                                         AS [plan.is_parallel],
       p.is_natively_compiled                                     AS [plan.is_natively_compiled],
       p.force_failure_count                                      AS [plan.force_failure_count],
       p.last_force_failure_reason_desc                           AS [plan.last_force_failure_reason],
       p.count_compiles                                           AS [plan.count_compiles],
       p.initial_compile_start_time                               AS [plan.initial_compile_start],
       p.last_compile_start_time                                  AS [plan.last_compile_start],
       p.last_execution_time                                      AS [plan.last_execution],
       /* Totals AND per execution. Four million calls at 0.3 ms and forty
          calls at thirty seconds have similar totals and nothing else in
          common, and the ratio is the only thing that separates them. */
       a.executions                                               AS [executions],
       CAST(a.total_duration / 1000.0 AS DECIMAL(18,1))           AS [total.duration_ms],
       CAST(a.total_cpu      / 1000.0 AS DECIMAL(18,1))           AS [total.cpu_ms],
       CAST(a.total_reads           AS BIGINT)                    AS [total.logical_reads],
       CAST(a.total_writes          AS BIGINT)                    AS [total.logical_writes],
       CAST(a.total_physical_reads  AS BIGINT)                    AS [total.physical_reads],
       CAST(a.total_duration / NULLIF(a.executions, 0)
            / 1000.0 AS DECIMAL(18,1))                            AS [per_execution.duration_ms],
       CAST(a.total_cpu / NULLIF(a.executions, 0)
            / 1000.0 AS DECIMAL(18,1))                            AS [per_execution.cpu_ms],
       CAST(a.total_reads / NULLIF(a.executions, 0)
            AS DECIMAL(18,1))                                     AS [per_execution.logical_reads]
FROM       selection                      AS sel
JOIN       sys.query_store_query          AS q  ON q.query_id = sel.query_id
JOIN       sys.query_store_query_text     AS qt ON qt.query_text_id = q.query_text_id
JOIN       sys.query_store_plan           AS p  ON p.query_id = q.query_id
JOIN       planRank                       AS pr ON pr.plan_id = p.plan_id
/* LEFT, both of them: a query selected only because one of its plans is forced
   need not have run inside the window at all, and dropping it here would keep
   it in the selection and out of the output. */
LEFT JOIN  agg                            AS a  ON a.query_id = sel.query_id
LEFT JOIN  ranked                         AS r  ON r.query_id = sel.query_id
ORDER BY sel.sort_rn, sel.sort_metric, q.query_id, p.plan_id
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── intervals ─────────
   One row per (query, plan, interval) for the selected queries only, so the
   shape of the workload over the window is readable rather than averaged into
   a single number. Restricted to the selection, not to the whole store: the
   store holds every interval of every query, and that is a table nobody reads.

   The selection is rebuilt here rather than carried in a temp table. The
   collector runs this script once per database on one shared session, and a
   #table that outlives the batch would collide on the second database; the
   duplication is the price of leaving the server exactly as it was found. A
   selection that drifts between the two statements is harmless — the writer
   matches intervals to queries by query_id and ignores the rest.

   THE INVARIANT BETWEEN THE TWO COPIES: the four ranking metrics, and the
   window predicate they are aggregated over, must stay identical to the
   selected copy above. Nothing else need match — this copy drops the two
   aggregates that no ranking reads — but a metric changed on one side only
   would silently select one set of queries and collect the intervals of
   another.

   NO PLAN CAP HERE, deliberately, and the asymmetry is the point. What the cap
   above bounds is plan XML — up to 8 MiB a plan, held in memory before a byte
   is written. This statement carries no XML at all, only numbers, and its rows
   are what say how a plan behaved over the window. Restricting it to the three
   plans whose XML was written would delete the per-interval shape of exactly
   the plans the reader can no longer open, leaving them named in the index and
   described nowhere. */
WITH agg AS (
    SELECT q.query_id,
           SUM(rs.avg_duration         * rs.count_executions) AS total_duration,
           SUM(rs.avg_cpu_time         * rs.count_executions) AS total_cpu,
           SUM(rs.avg_logical_io_reads * rs.count_executions) AS total_reads,
           SUM(rs.count_executions)                           AS executions
    FROM       sys.query_store_query         AS q
    JOIN       sys.query_store_plan          AS p  ON p.query_id = q.query_id
    JOIN       sys.query_store_runtime_stats AS rs ON rs.plan_id = p.plan_id
    JOIN       sys.query_store_runtime_stats_interval AS i
                 ON i.runtime_stats_interval_id = rs.runtime_stats_interval_id
    WHERE i.start_time < @qs_to AND i.end_time > @qs_from
    GROUP BY q.query_id
),
ranked AS (
    SELECT query_id,
           ROW_NUMBER() OVER (ORDER BY total_duration DESC, query_id) AS rn_duration,
           ROW_NUMBER() OVER (ORDER BY total_cpu      DESC, query_id) AS rn_cpu,
           ROW_NUMBER() OVER (ORDER BY total_reads    DESC, query_id) AS rn_reads,
           ROW_NUMBER() OVER (ORDER BY executions     DESC, query_id) AS rn_exec
    FROM agg
),
perMetric AS (
    SELECT r.query_id, m.metric_order, m.rn
    FROM ranked AS r
    CROSS APPLY (VALUES (1, r.rn_duration), (2, r.rn_cpu),
                        (3, r.rn_reads),    (4, r.rn_exec)) AS m(metric_order, rn)
),
best AS (
    SELECT query_id, metric_order, rn,
           ROW_NUMBER() OVER (PARTITION BY query_id ORDER BY rn, metric_order) AS dedupe
    FROM perMetric
),
capped AS (
    SELECT TOP (@qs_top) query_id
    FROM best
    WHERE dedupe = 1
    ORDER BY rn, metric_order, query_id
),
selection AS (
    SELECT query_id FROM capped
    UNION
    SELECT p.query_id
    FROM sys.query_store_plan AS p
    WHERE p.is_forced_plan = 1 OR p.force_failure_count > 0
)
SELECT p.query_id                                                 AS [query_id],
       p.plan_id                                                  AS [plan_id],
       i.runtime_stats_interval_id                                AS [interval_id],
       i.start_time                                               AS [start_time],
       i.end_time                                                 AS [end_time],
       rs.execution_type_desc                                     AS [execution_type],
       rs.count_executions                                        AS [count_executions],
       CAST(rs.avg_duration / 1000.0 AS DECIMAL(18,1))            AS [duration_ms.avg],
       CAST(rs.min_duration / 1000.0 AS DECIMAL(18,1))            AS [duration_ms.min],
       CAST(rs.max_duration / 1000.0 AS DECIMAL(18,1))            AS [duration_ms.max],
       CAST(rs.avg_cpu_time / 1000.0 AS DECIMAL(18,1))            AS [cpu_ms.avg],
       CAST(rs.min_cpu_time / 1000.0 AS DECIMAL(18,1))            AS [cpu_ms.min],
       CAST(rs.max_cpu_time / 1000.0 AS DECIMAL(18,1))            AS [cpu_ms.max],
       CAST(rs.avg_logical_io_reads AS BIGINT)                    AS [logical_reads.avg],
       CAST(rs.min_logical_io_reads AS BIGINT)                    AS [logical_reads.min],
       CAST(rs.max_logical_io_reads AS BIGINT)                    AS [logical_reads.max],
       CAST(rs.avg_dop AS DECIMAL(18,1))                          AS [dop.avg],
       rs.min_dop                                                 AS [dop.min],
       rs.max_dop                                                 AS [dop.max],
       /* The memory columns are counted in 8 KB pages, not in kilobytes, and
          the name says so rather than leaving the reader to guess a unit. */
       CAST(rs.avg_query_max_used_memory AS BIGINT)               AS [used_memory_pages.avg],
       rs.min_query_max_used_memory                               AS [used_memory_pages.min],
       rs.max_query_max_used_memory                               AS [used_memory_pages.max],
       rs.last_execution_time                                     AS [last_execution]
FROM       selection                                  AS sel
JOIN       sys.query_store_plan                       AS p  ON p.query_id = sel.query_id
JOIN       sys.query_store_runtime_stats              AS rs ON rs.plan_id = p.plan_id
JOIN       sys.query_store_runtime_stats_interval     AS i
             ON i.runtime_stats_interval_id = rs.runtime_stats_interval_id
WHERE i.start_time < @qs_to AND i.end_time > @qs_from
ORDER BY p.query_id, p.plan_id, i.start_time
OPTION (RECOMPILE, MAXDOP 1);
