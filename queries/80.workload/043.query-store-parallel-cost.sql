-- @scope:       database
-- @resultsets:  root:object, bands:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     120
-- @min_version: 13
--
-- The cost bands of 80.workload/042.parallel-cost-distribution.sql, read from
-- the Query Store of each database instead of from the plan cache: which
-- parallel plans ran in the last seven days, at what estimated cost, how often,
-- with how much CPU and at what degree. The design and every measurement it
-- rests on are in docs/query-store-parallel-cost-spec.md.
--
-- WHY THIS FILE EXISTS BESIDE 042. 042 sees only what the cache still holds,
-- and a cache under memory pressure holds little (114 plans in all on one
-- client instance); a restart, most sp_configure changes and a flush empty it.
-- The store keeps what the cache evicts. The two describe different sets (042
-- the whole instance, this file the databases whose store is read) and must
-- not be added together.
--
-- WHAT IS READ. The plans with is_parallel_plan = 1 that executed in the
-- window, never max_dop: serial plans report a max_dop above 1 (INSERT ...
-- EXEC of sp_estimate_data_compression_savings reported 41 on 22 schedulers),
-- and a plan that is not parallel cannot be made serial by a threshold. Those
-- serial plans are counted in window.serial_plans_dop_above_1, as 042 counts
-- its own in examined.serial_plans_dop_above_1, and never banded. Serial plans
-- are not read at all; the serial side is the runtime totals of the root.
--
-- THE RUNTIME ROWS ARE AGGREGATED ONCE, per plan, into #runtime, and the root,
-- the ranking and the bands all come from it, so no total can disagree with
-- the sum of the bands because a statement ran in between. Execution types are
-- summed, as in 029: an execution stopped by a timeout used its CPU.
--
-- OTHER REPLICAS. On an availability group secondary the store is the
-- primary's, and this file does not read it (state.not_read_because says
-- secondary, or replica state unreadable when the replica's state cannot be
-- read, the rule of 70.schema/050.heaps.sql). A database restored WITH
-- STANDBY (log shipping, sys.databases.is_in_standby = 1) is not read either,
-- for the same reason: its store describes the source server (state.not_read_because
-- says standby). On a primary whose store has
-- both the replica_group_id column (SQL Server 2022) and the view
-- sys.query_store_replicas (present on 2022 CU26 and on 2025, measured), the
-- runtime rows whose group the view maps to a role other than 1 are staged
-- first into #other_rows, through sp_executesql because the column does not
-- exist before SQL Server 2022, and left out of everything; excluded.* counts
-- them. The predicate is negative: a group the view does not list is kept.
-- Where the column or the view is missing nothing is left out and excluded.*
-- is NULL: 0 means looked and found none, NULL could not look. A row of
-- another role recorded between the staging and the aggregation, a few
-- milliseconds apart, is kept: the two statements do not read one snapshot.
--
-- NESTED QUERIES (sys.objects.type FN, TF, TR) stay in the bands, since a
-- parallel plan inside them is one the threshold acts on, and are counted in
-- nested.*. A scalar function's CPU is already in its caller, so window.cpu_s
-- leaves FN queries out and window.scalar_function_cpu_s says how much; a
-- trigger's or a multi-statement function's CPU is in no caller and stays in.
--
-- THE COST IS FOUND, NOT PARSED, as in 042: the first StatementSubTreeCost="
-- of the plan text, read up to the next double quote, TRY_CAST to float. No
-- plan is converted to xml. A stored plan is the plan of one query and its
-- first costed statement element is that query's own. Each chunk of plans is
-- first copied into a table variable column and searched there: every
-- reference to sys.query_store_plan.query_plan decompresses the plan again, so
-- the bytes are counted on the copy, never on the view.
--
-- THE CAP AND THE BUDGETS ARE CONSTANTS AND NOT OPTIONS, as in 027. The
-- parallel plans of the window are ranked by their CPU at a degree above 1,
-- then by plan id, and the first @cap + 1 are pinned before any plan text is
-- read; reaching @cap + 1 is the evidence that the cap bit. The pinned plans
-- are read @chunk at a time. Before each chunk the loop ends when every pinned
-- plan has been reached, then stops when @budget_ms have passed, then when the
-- bytes read reach @budget_bytes (examined.stopped_by: time, bytes, or cap when
-- the cap bit and no budget stopped the read). A chunk once started is read
-- whole, so either budget can be passed by one chunk; the selection before
-- the loop has no budget, and @timeout is the hard bound of the whole file.
-- selection.duration_ms, window.runtime_rows, examined.duration_ms,
-- examined.bytes_read, examined.largest_plan_bytes and examined.stopped_by
-- put the cost on the instance in the archive. Each statement that reads
-- sys.query_store_plan holds the store's shared lock, and a chunk releases it.
--
-- THE AUDIT'S OWN STATEMENTS. The audit's own statements are in the store, in
-- the serial totals (window.executions, window.cpu_s,
-- window.serial_plans_dop_above_1) and in the intervals (window.intervals,
-- window.newest_interval). Every one of them that reads rows carries OPTION
-- (RECOMPILE, MAXDOP 1), which the contract lint enforces, so they do not
-- reach a band, unless a Query Store hint set on one of them overrides it, or
-- the sample copy that sp_estimate_data_compression_savings runs under
-- --estimate-compression compiles parallel.
--
-- THE LIMITS A READER MUST KEEP IN VIEW. Only stores that are not off, and
-- only what they captured (under capture mode AUTO a cheap and rare query is
-- missing). The cost is the parallel plan's, lower than the serial cost the
-- optimizer compared with the threshold, so counts under a candidate are upper
-- bounds of what it would make serial. A plan compiled under an earlier
-- threshold is in the window with its old cost. avg_dop inherits the
-- documented anomaly of the DOP columns on many processors;
-- window.dop_above_schedulers counts the rows above the scheduler count. After
-- a failover inside the window, the primary role's earlier rows were run by
-- the other instance. window.intervals and window.newest_interval include the
-- audit's own activity and are not evidence that the client's workload ran.
--
-- A BAND. Seven rows always, with 042's boundaries and column names: [0, 5),
-- [5, 25), [25, 50), [50, 100), [100, 500), [500, +inf) and unknown (no plan
-- left in the store when its chunk came, or no statement cost in its text).
-- statements counts parallel plans; parallel_statements those with a runtime
-- row at max_dop > 1; parallel_executions and parallel_cpu_s are over the rows
-- where max_dop > 1, an upper bound; parallel_executions_min and
-- parallel_cpu_s_min over the rows where min_dop > 1, a lower bound; avg_dop
-- is weighted by parallel_executions. Pinned plans a budget left unread are in
-- no band, so examined.share_of_parallel_cpu_pct, the bands' parallel CPU over
-- window.parallel_cpu_s, is their share by construction.
--
-- NOTHING THAT NAMES A QUERY LEAVES THE SERVER: no text, no plan, no query or
-- plan id or hash, no object, no replica name. No @discloses.
--
-- Every statement that reads rows carries OPTION (RECOMPILE, MAXDOP 1), the
-- staging literal included; the replica state is read by assignments, a CASE in
-- the DECLARE of @not_read tests it, and no DECLARE or SET holds a subquery,
-- which the lint refuses.
--
-- SQL Server 2016 is the floor: every column read outside the sp_executesql
-- branch exists there. Not measured on 2016.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

/* The constants, one declaration per line in this exact form: a test rewrites
   them in a copy of this text, as a --queries-dir corpus would. */
DECLARE @window_days int = 7;
DECLARE @cap int = 1000;
DECLARE @chunk int = 100;
DECLARE @budget_bytes bigint = 104857600;
DECLARE @budget_ms int = 10000;

DECLARE @collected_at datetimeoffset = SYSDATETIMEOFFSET();
DECLARE @from datetimeoffset = DATEADD(day, -@window_days, @collected_at);
DECLARE @schedulers int;
SELECT @schedulers = i.scheduler_count FROM sys.dm_os_sys_info AS i OPTION (RECOMPILE, MAXDOP 1);

/* ───────── whether the store is read ─────────
   A store that is OFF still returns its data, so the state is a predicate
   here. master and tempdb have no options row: no store. */
DECLARE @has_options bit = 0, @state nvarchar(60) = NULL;
SELECT @has_options = 1, @state = o.actual_state_desc
FROM sys.database_query_store_options AS o
OPTION (RECOMPILE, MAXDOP 1);

/* The availability group test of 70.schema/050.heaps.sql: an assignment, not
   IF EXISTS, which cannot carry the hint. A database whose replica_id is set
   and whose replica state cannot be read is treated as a secondary. */
DECLARE @secondary bit = 0, @replica_unreadable bit = 0;
BEGIN TRY
    SELECT @secondary = 1
    FROM sys.databases AS d
    JOIN sys.dm_hadr_availability_replica_states AS ars
      ON ars.replica_id = d.replica_id AND ars.is_local = 1
    WHERE d.database_id = DB_ID() AND ars.role = 2
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @secondary = 1, @replica_unreadable = 1
    FROM sys.databases AS d
    WHERE d.database_id = DB_ID() AND d.replica_id IS NOT NULL
    OPTION (RECOMPILE, MAXDOP 1);
END CATCH

/* A database restored WITH STANDBY (log shipping) holds the source server's
   store, for the same reason as a secondary: an assignment, no subquery. */
DECLARE @standby bit = 0;
SELECT @standby = d.is_in_standby
FROM sys.databases AS d
WHERE d.database_id = DB_ID()
OPTION (RECOMPILE, MAXDOP 1);

DECLARE @not_read nvarchar(30) =
    CASE WHEN @has_options = 0        THEN N'no store'
         WHEN @state = N'OFF'         THEN N'off'
         WHEN @replica_unreadable = 1 THEN N'replica state unreadable'
         WHEN @secondary = 1          THEN N'secondary'
         WHEN @standby = 1            THEN N'standby'
    END;
DECLARE @read bit = CASE WHEN @not_read IS NULL THEN 1 ELSE 0 END;

/* The replica groups can be told apart only where both the column (SQL
   Server 2022) and the view (measured on 2022 CU26 and 2025) exist. */
DECLARE @replicas_known bit =
    CASE WHEN COL_LENGTH('sys.query_store_runtime_stats', 'replica_group_id') IS NOT NULL
          AND OBJECT_ID('sys.query_store_replicas') IS NOT NULL THEN 1 ELSE 0 END;

CREATE TABLE #other_rows (
    runtime_stats_id bigint NOT NULL PRIMARY KEY,
    executions       bigint NOT NULL,
    cpu_us           float  NOT NULL);

/* One row per plan executed in the window. par_* are the rows where
   max_dop > 1, min_* the rows where min_dop > 1. */
CREATE TABLE #runtime (
    plan_id               bigint NOT NULL PRIMARY KEY,
    is_parallel_plan      bit    NOT NULL,
    nested                bit    NOT NULL,
    scalar_function       bit    NOT NULL,
    runtime_rows          bigint NOT NULL,
    executions            bigint NOT NULL,
    cpu_us                float  NOT NULL,
    par_rows              bigint NOT NULL,
    par_executions        bigint NOT NULL,
    par_cpu_us            float  NOT NULL,
    min_executions        bigint NOT NULL,
    min_cpu_us            float  NOT NULL,
    dop_weighted          float  NOT NULL,
    max_dop               bigint NULL,
    rows_above_schedulers bigint NOT NULL);

DECLARE @pinned TABLE (rn int PRIMARY KEY, plan_id bigint NOT NULL UNIQUE);
DECLARE @intervals bigint, @oldest datetimeoffset, @newest datetimeoffset, @store_oldest datetimeoffset;
DECLARE @eligible bigint = 0, @cap_reached bit = 0, @plans_pinned bigint = 0;
DECLARE @selection_started datetime2 = SYSDATETIME(), @selection_ms int;

/* ───────── the selection: runtime rows only, no plan text ───────── */
IF @read = 1
BEGIN
    /* Semi-joins, not a JOIN: the view can hold several rows for one group, and a
       JOIN would repeat a runtime row and break the primary key. */
    IF @replicas_known = 1
        EXEC sys.sp_executesql
            N'INSERT INTO #other_rows (runtime_stats_id, executions, cpu_us)
              SELECT rs.runtime_stats_id, rs.count_executions, rs.avg_cpu_time * rs.count_executions
              FROM sys.query_store_runtime_stats AS rs
              JOIN sys.query_store_runtime_stats_interval AS i
                ON i.runtime_stats_interval_id = rs.runtime_stats_interval_id
              WHERE i.end_time > @from
                AND EXISTS (SELECT 1 FROM sys.query_store_replicas AS r
                            WHERE r.replica_group_id = rs.replica_group_id AND r.role_type <> 1)
                AND NOT EXISTS (SELECT 1 FROM sys.query_store_replicas AS p
                                WHERE p.replica_group_id = rs.replica_group_id AND p.role_type = 1) OPTION (RECOMPILE, MAXDOP 1)',
            N'@from datetimeoffset',
            @from = @from;

    INSERT INTO #runtime (plan_id, is_parallel_plan, nested, scalar_function, runtime_rows,
                          executions, cpu_us, par_rows, par_executions, par_cpu_us,
                          min_executions, min_cpu_us, dop_weighted, max_dop, rows_above_schedulers)
    SELECT rs.plan_id,
           p.is_parallel_plan,
           CASE WHEN ob.type IN ('FN', 'TF', 'TR') THEN 1 ELSE 0 END,
           CASE WHEN ob.type = 'FN' THEN 1 ELSE 0 END,
           COUNT_BIG(*),
           SUM(rs.count_executions),
           SUM(rs.avg_cpu_time * rs.count_executions),
           COUNT_BIG(CASE WHEN rs.max_dop > 1 THEN 1 END),
           SUM(CASE WHEN rs.max_dop > 1 THEN rs.count_executions ELSE 0 END),
           SUM(CASE WHEN rs.max_dop > 1 THEN rs.avg_cpu_time * rs.count_executions ELSE 0 END),
           SUM(CASE WHEN rs.min_dop > 1 THEN rs.count_executions ELSE 0 END),
           SUM(CASE WHEN rs.min_dop > 1 THEN rs.avg_cpu_time * rs.count_executions ELSE 0 END),
           SUM(CASE WHEN rs.max_dop > 1 THEN rs.avg_dop * rs.count_executions ELSE 0 END),
           MAX(rs.max_dop),
           COUNT_BIG(CASE WHEN rs.max_dop > @schedulers THEN 1 END)
    FROM sys.query_store_runtime_stats AS rs
    JOIN sys.query_store_runtime_stats_interval AS i
      ON i.runtime_stats_interval_id = rs.runtime_stats_interval_id
    JOIN sys.query_store_plan  AS p ON p.plan_id = rs.plan_id
    JOIN sys.query_store_query AS q ON q.query_id = p.query_id
    LEFT JOIN sys.objects AS ob ON ob.object_id = q.object_id AND q.object_id <> 0
    WHERE i.end_time > @from
      AND NOT EXISTS (SELECT 1 FROM #other_rows AS x WHERE x.runtime_stats_id = rs.runtime_stats_id)
    GROUP BY rs.plan_id, p.is_parallel_plan, ob.type OPTION (RECOMPILE, MAXDOP 1);

    SELECT @intervals = COUNT_BIG(*), @oldest = MIN(i.start_time), @newest = MAX(i.end_time)
    FROM sys.query_store_runtime_stats_interval AS i
    WHERE i.end_time > @from
      AND EXISTS (SELECT 1
                  FROM sys.query_store_runtime_stats AS rs
                  WHERE rs.runtime_stats_interval_id = i.runtime_stats_interval_id
                    AND NOT EXISTS (SELECT 1 FROM #other_rows AS x
                                    WHERE x.runtime_stats_id = rs.runtime_stats_id))
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @store_oldest = MIN(i.start_time)
    FROM sys.query_store_runtime_stats_interval AS i
    OPTION (RECOMPILE, MAXDOP 1);

    INSERT INTO @pinned (rn, plan_id)
    SELECT ROW_NUMBER() OVER (ORDER BY t.par_cpu_us DESC, t.plan_id), t.plan_id
    FROM (SELECT TOP (@cap + 1) r.plan_id, r.par_cpu_us
          FROM #runtime AS r
          WHERE r.is_parallel_plan = 1
          ORDER BY r.par_cpu_us DESC, r.plan_id) AS t OPTION (RECOMPILE, MAXDOP 1);

    SELECT @eligible = COUNT_BIG(*) FROM @pinned OPTION (RECOMPILE, MAXDOP 1);
    SET @cap_reached = CASE WHEN @eligible > @cap THEN 1 ELSE 0 END;
    DELETE FROM @pinned WHERE rn > @cap OPTION (RECOMPILE, MAXDOP 1);
    SET @plans_pinned = CASE WHEN @eligible > @cap THEN @cap ELSE @eligible END;
END
SET @selection_ms = DATEDIFF(millisecond, @selection_started, SYSDATETIME());

/* ───────── the read loop: @chunk plans at a time, within the budgets ───────── */
DECLARE @staged TABLE (plan_id bigint PRIMARY KEY, query_plan nvarchar(max) NULL);
DECLARE @costs TABLE (plan_id bigint PRIMARY KEY, plan_state tinyint NOT NULL, cost float NULL); -- 0 costed, 1 no plan, 3 no cost
DECLARE @lo int = 1, @bytes_read bigint = 0, @plans_read bigint = 0, @largest bigint = NULL;
DECLARE @stopped_by varchar(5) = NULL;
/* Both timers are datetime2 at full precision. At datetime2(3) the start is
   rounded and can land after the next SYSDATETIME(), the elapsed time then
   reads -1 ms, and a time budget of 0 never stops the loop (measured). */
DECLARE @loop_started datetime2 = SYSDATETIME(), @loop_ms int;

WHILE 1 = 1
BEGIN
    IF @read = 0 OR @lo > @plans_pinned BREAK;
    IF DATEDIFF(millisecond, @loop_started, SYSDATETIME()) >= @budget_ms
    BEGIN
        SET @stopped_by = 'time';
        BREAK;
    END
    IF @bytes_read >= @budget_bytes
    BEGIN
        SET @stopped_by = 'bytes';
        BREAK;
    END

    INSERT INTO @staged (plan_id, query_plan)
    SELECT k.plan_id, p.query_plan
    FROM @pinned AS k
    LEFT JOIN sys.query_store_plan AS p ON p.plan_id = k.plan_id
    WHERE k.rn >= @lo AND k.rn < @lo + @chunk
    OPTION (RECOMPILE, MAXDOP 1);

    INSERT INTO @costs (plan_id, plan_state, cost)
    SELECT s.plan_id,
           CASE WHEN s.query_plan IS NULL THEN 1 WHEN c.cost IS NULL THEN 3 ELSE 0 END,
           c.cost
    FROM @staged AS s
    CROSS APPLY (SELECT CHARINDEX(N'StatementSubTreeCost="', s.query_plan) + 22 AS v) AS k
    CROSS APPLY (SELECT CASE WHEN k.v > 22 THEN CHARINDEX(N'"', s.query_plan, k.v) END AS e) AS q
    CROSS APPLY (SELECT CASE WHEN q.e > k.v
                             THEN TRY_CAST(SUBSTRING(s.query_plan, k.v, q.e - k.v) AS float)
                        END AS cost) AS c
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @bytes_read = @bytes_read + ISNULL(SUM(DATALENGTH(s.query_plan)), 0),
           @plans_read = @plans_read + COUNT_BIG(CASE WHEN s.query_plan IS NOT NULL THEN 1 END),
           @largest    = CASE WHEN MAX(DATALENGTH(s.query_plan)) > ISNULL(@largest, -1)
                              THEN MAX(DATALENGTH(s.query_plan)) ELSE @largest END
    FROM @staged AS s
    OPTION (RECOMPILE, MAXDOP 1);

    DELETE FROM @staged OPTION (RECOMPILE, MAXDOP 1);
    SET @lo = @lo + @chunk;
END
SET @loop_ms = DATEDIFF(millisecond, @loop_started, SYSDATETIME());
IF @stopped_by IS NULL AND @cap_reached = 1 SET @stopped_by = 'cap';

/* ───────── root ─────────
   A LEFT JOIN from sys.databases, as in 026 and 029, so a database with no
   options row still returns its row. Counts are NULL when the store is not
   read and 0 when it is read and empty. */
SELECT DB_NAME()                                                     AS [database],
       @collected_at                                                 AS [collected_at],
       o.actual_state_desc                                           AS [state.actual],
       o.query_capture_mode_desc                                     AS [state.capture_mode],
       o.interval_length_minutes                                     AS [state.interval_minutes],
       @not_read                                                     AS [state.not_read_because],
       @schedulers                                                   AS [schedulers],
       @window_days                                                  AS [window.days],
       @from                                                         AS [window.from],
       @oldest                                                       AS [window.oldest_interval],
       @newest                                                       AS [window.newest_interval],
       @store_oldest                                                 AS [window.store_oldest_interval],
       @intervals                                                    AS [window.intervals],
       CASE WHEN @read = 1 THEN ISNULL(a.runtime_rows, 0) + x.other_rows END
                                                                     AS [window.runtime_rows],
       CASE WHEN @read = 1 THEN ISNULL(a.executions, 0) END          AS [window.executions],
       CASE WHEN @read = 1 THEN CAST(ISNULL(a.cpu_us, 0) / 1000000.0 AS decimal(18,1)) END
                                                                     AS [window.cpu_s],
       CASE WHEN @read = 1 THEN CAST(ISNULL(a.fn_cpu_us, 0) / 1000000.0 AS decimal(18,1)) END
                                                                     AS [window.scalar_function_cpu_s],
       CASE WHEN @read = 1 THEN a.par_plans END                      AS [window.parallel_plans],
       CASE WHEN @read = 1 THEN CAST(ISNULL(a.par_plan_cpu_us, 0) / 1000000.0 AS decimal(18,1)) END
                                                                     AS [window.parallel_plan_cpu_s],
       CASE WHEN @read = 1 THEN ISNULL(a.par_executions, 0) END      AS [window.parallel_executions],
       CASE WHEN @read = 1 THEN CAST(ISNULL(a.par_cpu_us, 0) / 1000000.0 AS decimal(18,1)) END
                                                                     AS [window.parallel_cpu_s],
       CASE WHEN @read = 1 THEN ISNULL(a.min_executions, 0) END      AS [window.parallel_executions_min],
       CASE WHEN @read = 1 THEN CAST(ISNULL(a.min_cpu_us, 0) / 1000000.0 AS decimal(18,1)) END
                                                                     AS [window.parallel_cpu_s_min],
       CASE WHEN @read = 1 THEN a.serial_dop_above_1 END             AS [window.serial_plans_dop_above_1],
       CASE WHEN @read = 1 THEN ISNULL(a.rows_above_schedulers, 0) END
                                                                     AS [window.dop_above_schedulers],
       CASE WHEN @read = 1 THEN a.nested_plans END                   AS [nested.parallel_plans],
       CASE WHEN @read = 1 THEN CAST(ISNULL(a.nested_par_cpu_us, 0) / 1000000.0 AS decimal(18,1)) END
                                                                     AS [nested.parallel_cpu_s],
       CASE WHEN @read = 1 AND @replicas_known = 1 THEN ISNULL(x.other_executions, 0) END
                                                                     AS [excluded.other_replicas_executions],
       CASE WHEN @read = 1 AND @replicas_known = 1
            THEN CAST(ISNULL(x.other_cpu_us, 0) / 1000000.0 AS decimal(18,1)) END
                                                                     AS [excluded.other_replicas_cpu_s],
       @cap                                                          AS [cap],
       @chunk                                                        AS [chunk],
       @budget_bytes                                                 AS [budget.bytes],
       @budget_ms                                                    AS [budget.ms],
       CASE WHEN @read = 1 THEN @selection_ms END                    AS [selection.duration_ms],
       CASE WHEN @read = 1 THEN @plans_pinned END                    AS [examined.plans],
       CASE WHEN @read = 1 THEN @plans_read END                      AS [examined.plans_read],
       CASE WHEN @read = 1 THEN @bytes_read END                      AS [examined.bytes_read],
       CASE WHEN @read = 1 THEN @loop_ms END                         AS [examined.duration_ms],
       @largest                                                      AS [examined.largest_plan_bytes],
       CASE WHEN @read = 1
            THEN CAST(100.0 * ISNULL(b.banded_par_cpu_us, 0) / NULLIF(ISNULL(a.par_cpu_us, 0), 0) AS decimal(5,1))
       END                                                           AS [examined.share_of_parallel_cpu_pct],
       @stopped_by                                                   AS [examined.stopped_by],
       CASE WHEN @read = 1 THEN CASE WHEN @stopped_by IS NULL THEN 0 ELSE 1 END END
                                                                     AS [truncated],
       CASE WHEN @read = 1 THEN b.no_plan END                        AS [unknown.no_plan],
       CASE WHEN @read = 1 THEN b.no_cost END                        AS [unknown.no_cost]
FROM sys.databases AS d
LEFT JOIN sys.database_query_store_options AS o ON 1 = 1
CROSS JOIN (SELECT SUM(r.runtime_rows)                                                    AS runtime_rows,
                   SUM(CASE WHEN r.scalar_function = 0 THEN r.executions END)             AS executions,
                   SUM(CASE WHEN r.scalar_function = 0 THEN r.cpu_us END)                 AS cpu_us,
                   SUM(CASE WHEN r.scalar_function = 1 THEN r.cpu_us END)                 AS fn_cpu_us,
                   COUNT_BIG(CASE WHEN r.is_parallel_plan = 1 THEN 1 END)                 AS par_plans,
                   SUM(CASE WHEN r.is_parallel_plan = 1 THEN r.cpu_us END)                AS par_plan_cpu_us,
                   SUM(CASE WHEN r.is_parallel_plan = 1 THEN r.par_executions END)        AS par_executions,
                   SUM(CASE WHEN r.is_parallel_plan = 1 THEN r.par_cpu_us END)            AS par_cpu_us,
                   SUM(CASE WHEN r.is_parallel_plan = 1 THEN r.min_executions END)        AS min_executions,
                   SUM(CASE WHEN r.is_parallel_plan = 1 THEN r.min_cpu_us END)            AS min_cpu_us,
                   COUNT_BIG(CASE WHEN r.is_parallel_plan = 0 AND r.par_rows > 0 THEN 1 END)
                                                                                          AS serial_dop_above_1,
                   SUM(CASE WHEN r.is_parallel_plan = 1 THEN r.rows_above_schedulers END) AS rows_above_schedulers,
                   COUNT_BIG(CASE WHEN r.is_parallel_plan = 1 AND r.nested = 1 THEN 1 END) AS nested_plans,
                   SUM(CASE WHEN r.is_parallel_plan = 1 AND r.nested = 1 THEN r.par_cpu_us END)
                                                                                          AS nested_par_cpu_us
            FROM #runtime AS r) AS a
CROSS JOIN (SELECT COUNT_BIG(*) AS other_rows, SUM(w.executions) AS other_executions,
                   SUM(w.cpu_us) AS other_cpu_us
            FROM #other_rows AS w) AS x
CROSS JOIN (SELECT SUM(r.par_cpu_us)                               AS banded_par_cpu_us,
                   COUNT_BIG(CASE WHEN c.plan_state = 1 THEN 1 END) AS no_plan,
                   COUNT_BIG(CASE WHEN c.plan_state = 3 THEN 1 END) AS no_cost
            FROM @costs AS c
            JOIN #runtime AS r ON r.plan_id = c.plan_id) AS b
WHERE d.database_id = DB_ID()
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── bands ─────────
   Every band, always, empty ones included. cost_from is inclusive and
   cost_to exclusive; both are NULL for unknown. A plan's runtime figures come
   from #runtime, so a plan that left the store before its chunk still counts,
   in unknown. */
SELECT b.band                                                             AS [band],
       b.cost_from                                                        AS [cost_from],
       b.cost_to                                                          AS [cost_to],
       COUNT(z.plan_id)                                                   AS [statements],
       COUNT(CASE WHEN z.par_rows > 0 THEN 1 END)                         AS [parallel_statements],
       ISNULL(SUM(z.executions), 0)                                       AS [executions],
       ISNULL(SUM(z.par_executions), 0)                                   AS [parallel_executions],
       CAST(ISNULL(SUM(z.cpu_us), 0) / 1000000.0 AS decimal(18,1))        AS [cpu_s],
       CAST(ISNULL(SUM(z.par_cpu_us), 0) / 1000000.0 AS decimal(18,1))    AS [parallel_cpu_s],
       MAX(z.max_dop)                                                     AS [max_dop],
       ISNULL(SUM(z.min_executions), 0)                                   AS [parallel_executions_min],
       CAST(ISNULL(SUM(z.min_cpu_us), 0) / 1000000.0 AS decimal(18,1))    AS [parallel_cpu_s_min],
       ROUND(SUM(z.dop_weighted) / NULLIF(SUM(z.par_executions), 0), 2)   AS [avg_dop]
FROM (VALUES (1, 'lt_5',        0.0,   5.0),
             (2, '5_25',        5.0,  25.0),
             (3, '25_50',      25.0,  50.0),
             (4, '50_100',     50.0, 100.0),
             (5, '100_500',   100.0, 500.0),
             (6, 'ge_500',    500.0,  NULL),
             (7, 'unknown',    NULL,  NULL)) AS b (ord, band, cost_from, cost_to)
LEFT JOIN (SELECT c.cost, r.plan_id, r.par_rows, r.executions, r.par_executions, r.cpu_us,
                  r.par_cpu_us, r.max_dop, r.min_executions, r.min_cpu_us, r.dop_weighted
           FROM @costs AS c
           JOIN #runtime AS r ON r.plan_id = c.plan_id) AS z
       ON (b.band = 'unknown' AND z.cost IS NULL)
       OR (b.band <> 'unknown' AND z.cost >= b.cost_from
           AND (b.cost_to IS NULL OR z.cost < b.cost_to))
GROUP BY b.ord, b.band, b.cost_from, b.cost_to
ORDER BY b.ord
OPTION (RECOMPILE, MAXDOP 1);
