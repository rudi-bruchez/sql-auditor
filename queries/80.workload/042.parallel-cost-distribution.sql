-- @scope:       instance
-- @resultsets:  root:object, bands:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     120
-- @min_version: 13
--
-- How the estimated costs of the statements that actually run are distributed,
-- and which of them go parallel: the only measurement that can choose a value
-- for 'cost threshold for parallelism'.
--
-- WHY THIS COLLECTOR EXISTS. The threshold is usually set from a house
-- reference rather than from the workload. Found on a client estate in
-- 2026: a previous audit had recommended 50 on nine instances because two of
-- them already had it. Whether raising the threshold from 5 to
-- 50 changes anything depends on where the parallel work sits on the cost
-- axis, and nothing in this archive said: 040.plan-cache says what the cache
-- is made of, 041.plan-cache-plans extracts plans behind a flag, and neither
-- crosses cost with degree of parallelism. Here the 500 statements with the
-- most CPU are put in cost bands, and each band says how many of them ran in
-- parallel, how often, and with how much CPU.
--
-- NOTHING NOMINATIVE LEAVES THE SERVER. No statement text, no plan, no handle,
-- no database or object name: bands and counts only. The threshold and
-- max degree of parallelism in use are not repeated here either, because
-- 10.system/010.properties carries every row of sys.configurations and the
-- analysis joins the two.
--
-- THE TWO TRAPS THE SHAPE OF THIS FILE ANSWERS, both paid for on client
-- instances before this file existed.
--
-- One. sys.dm_exec_query_plan(plan_handle) returns the plan of the whole
-- batch or procedure, and the first StmtSimple in it is the batch's first
-- statement, not the one sys.dm_exec_query_stats measured. A first pass read
-- it that way and reported parallel statements "under 5" that were the cost
-- of a neighbour. Reproduced on the lab: the second statement of a
-- procedure, parallel at a cost of 20.75, read 0.0033 through the batch's
-- plan, the cost of the cheap lookup before it. The plan is fetched here per
-- statement, with sys.dm_exec_text_query_plan and the row's two offsets, so
-- the fragment holds the measured statement and nothing else.
--
-- Two. sys.dm_exec_text_query_plan returns text, and a plan nested deeper
-- than the 128 levels the xml type allows does not convert. TRY_CAST turns
-- that into a NULL, and the statement goes to the band "unknown", which is
-- always projected and never folded into another: up to 35 of 500 statements
-- on one client instance. Reproduced on the lab with a join of 80 tables
-- under FORCE ORDER: a 335 KB fragment that TRY_CAST returns as NULL. The
-- root says why each unknown is unknown.
--
-- THE SHOWPLAN NAMESPACE IS DECLARED. Without WITH XMLNAMESPACES, the path
-- //StmtSimple/@StatementSubTreeCost matches nothing in a showplan document
-- and every statement lands in "unknown". Measured on 17.0.4065.4: seven
-- statements, seven NULL costs without the declaration, seven costs with it.
-- The path read is rooted, Statements/*, so it reads the measured
-- statement's own cost whatever its kind (StmtSimple, StmtCond, StmtCursor)
-- and never descends into the operators.
--
-- THE TOP IS CHOSEN BEFORE ANY PLAN IS READ. The 500 statements are taken
-- from sys.dm_exec_query_stats into a table variable first, without touching a
-- plan, and only those 500 fragments are fetched and converted. A harm review
-- of 27 September 2026 found two default collectors that converted the whole
-- cache before applying their TOP; this one cannot, by construction.
--
-- COST, MEASURED ON 17.0.4065.4 (22 schedulers, Linux, lab caches of 768
-- and 1 024 statements): 500 fragments holding 8.8 MB of plan text were
-- fetched and converted in 1.1 s, and 21.3 MB in 2.5 s, about 115 ms per
-- megabyte. examined.plan_kb and examined.duration_ms project both figures
-- on every run, so the cost on a client instance is read from its own
-- archive. On client instances running SQL Server 2019 the same reading took
-- from 1.6 to 5.9 s. The cost grows with the size of the 500 fragments, not
-- with the size of the cache, which only the DMV sort and the cache.* totals
-- read in full.
--
-- WHAT A BAND IS. A statement is in the band of the StatementSubTreeCost of
-- its cached plan, in the optimizer's units: [0, 5), [5, 25), [25, 50),
-- [50, 100), [100, 500), [500, +inf), and unknown. Parallel means max_dop > 1.
-- The bands fix the boundaries an analysis can answer at: how much parallel
-- work lies between 5 and 25 is answerable, between 5 and 30 is not, and
-- nothing should be interpolated inside a band.
--
-- THE LIMITS A READER MUST KEEP IN VIEW.
--
-- The cache sees only the plans it still holds. Under memory pressure it is
-- emptied continuously: one client instance held 114 plans in all. cache.*
-- says how many statements the cache held, from when, and what share of the
-- cache's CPU the 500 represent; a small cache or a recent oldest_creation is
-- the finding, and the distribution is then too thin to choose a threshold.
-- The cache is also emptied by a restart, by most sp_configure changes, and
-- by an explicit flush.
--
-- max_dop is the highest degree one execution reached since the plan was
-- compiled, not the degree of every execution. A statement counted parallel
-- may have run serially most of the time, and its executions and CPU are all
-- counted on the parallel side. The parallel columns are therefore an upper
-- bound of the parallel work.
--
-- The cost of a parallel plan is lower than the serial cost the optimizer
-- compared with the threshold. Measured on the lab: a scan and aggregate
-- whose serial plan costs 24.08 got a parallel plan costing 20.75, and a
-- collection under a threshold of 5 found two statements in band [0, 5) that
-- had run at degree 2. So a parallel statement in band [5, 25) may have had a serial cost above 25, and
-- raising the threshold to 25 would not make it serial. The count of parallel
-- statements between the current threshold and a candidate is an upper bound
-- of what raising it would make serial. A parallel statement below the
-- current threshold is either this effect or a plan compiled under a lower
-- threshold and still cached; the cache does not record which.
--
-- THE QUERY STORE IS NOT READ HERE. It keeps what the cache evicts, and
-- the same reading on it (the cost from sys.query_store_plan.query_plan,
-- avg_dop and max_dop from sys.query_store_runtime_stats) would not depend on
-- eviction. It runs per database, so its conversions multiply by the number
-- of databases, and it needs a window and a cap of its own; it is not a
-- second result set of this file.
--
-- SQL Server 2016 is the floor: max_dop in sys.dm_exec_query_stats is
-- documented from 13.x. Worker time is in microseconds and is projected in
-- seconds; for a parallel plan it is the CPU of every thread.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @examined int = 500;
DECLARE @started datetime2(3) = SYSDATETIME();

DECLARE @window TABLE (
    [plan_handle]            varbinary(64),
    [statement_start_offset] int,
    [statement_end_offset]   int,
    [execution_count]        bigint,
    [total_worker_time]      bigint,
    [max_dop]                bigint,
    [creation_time]          datetime);

DECLARE @costed TABLE (
    [execution_count]   bigint,
    [total_worker_time] bigint,
    [max_dop]           bigint,
    [creation_time]     datetime,
    [plan_bytes]        bigint,
    [plan_state]        tinyint,      -- 0 costed, 1 no plan, 2 not xml, 3 no cost
    [cost]              float);

/* The window, without reading a plan. The sort runs over every statement of
   the cache, which is the unavoidable part and costs no plan conversion. */
INSERT INTO @window
SELECT TOP (@examined)
       qs.plan_handle, qs.statement_start_offset, qs.statement_end_offset,
       qs.execution_count, qs.total_worker_time, qs.max_dop, qs.creation_time
FROM sys.dm_exec_query_stats AS qs
ORDER BY qs.total_worker_time DESC
OPTION (RECOMPILE, MAXDOP 1);

/* One fragment per statement of the window, converted once. OUTER APPLY so a
   statement whose plan the engine no longer returns is counted, not lost. */
WITH XMLNAMESPACES (DEFAULT 'http://schemas.microsoft.com/sqlserver/2004/07/showplan')
INSERT INTO @costed
SELECT w.execution_count, w.total_worker_time, w.max_dop, w.creation_time,
       DATALENGTH(tp.query_plan),
       CASE WHEN tp.query_plan IS NULL THEN 1
            WHEN x.px IS NULL THEN 2
            WHEN c.cost IS NULL THEN 3
            ELSE 0 END,
       c.cost
FROM @window AS w
OUTER APPLY sys.dm_exec_text_query_plan(w.plan_handle, w.statement_start_offset,
                                        w.statement_end_offset) AS tp
CROSS APPLY (SELECT TRY_CAST(tp.query_plan AS xml) AS px) AS x
CROSS APPLY (SELECT x.px.value('(/ShowPlanXML/BatchSequence/Batch/Statements/*/@StatementSubTreeCost)[1]',
                               'float') AS cost) AS c
OPTION (RECOMPILE, MAXDOP 1);

DECLARE @finished datetime2(3) = SYSDATETIME();

SELECT
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    CONVERT(varchar(23), (SELECT sqlserver_start_time FROM sys.dm_os_sys_info), 126)
                                                                AS [instance_start],
    /* The whole cache, read from the DMV alone: what the 500 are a sample
       of. The parallel totals need no plan, so they cover every statement. */
    agg.statements                                              AS [cache.statements],
    agg.executions                                              AS [cache.executions],
    CAST(agg.worker_us / 1000000.0 AS decimal(18,1))            AS [cache.cpu_s],
    agg.parallel_statements                                     AS [cache.parallel_statements],
    CAST(agg.parallel_worker_us / 1000000.0 AS decimal(18,1))   AS [cache.parallel_cpu_s],
    CONVERT(varchar(23), agg.oldest_creation, 126)              AS [cache.oldest_creation],
    @examined                                                   AS [examined.cap],
    (SELECT COUNT(*) FROM @costed)                              AS [examined.statements],
    (SELECT CAST(100.0 * SUM(c.total_worker_time) / NULLIF(agg.worker_us, 0)
                 AS decimal(5,1)) FROM @costed AS c)            AS [examined.share_of_cache_cpu_pct],
    (SELECT CONVERT(varchar(23), MIN(c.creation_time), 126) FROM @costed AS c)
                                                                AS [examined.oldest_creation],
    (SELECT CAST(ISNULL(SUM(c.plan_bytes), 0) / 1024.0 AS decimal(18,1)) FROM @costed AS c)
                                                                AS [examined.plan_kb],
    DATEDIFF(millisecond, @started, @finished)                  AS [examined.duration_ms],
    /* Why the unknown band is unknown. not_xml is the nesting limit; no_plan
       is a plan the engine no longer returns; no_cost a fragment that holds
       no statement cost. */
    (SELECT COUNT(*) FROM @costed WHERE plan_state = 1)         AS [unknown.no_plan],
    (SELECT COUNT(*) FROM @costed WHERE plan_state = 2)         AS [unknown.not_xml],
    (SELECT COUNT(*) FROM @costed WHERE plan_state = 3)         AS [unknown.no_cost]
FROM (SELECT COUNT(*)                                           AS statements,
             SUM(qs.execution_count)                            AS executions,
             SUM(qs.total_worker_time)                          AS worker_us,
             SUM(CASE WHEN qs.max_dop > 1 THEN 1 ELSE 0 END)    AS parallel_statements,
             SUM(CASE WHEN qs.max_dop > 1 THEN qs.total_worker_time ELSE 0 END)
                                                                AS parallel_worker_us,
             MIN(qs.creation_time)                              AS oldest_creation
      FROM sys.dm_exec_query_stats AS qs) AS agg
OPTION (RECOMPILE, MAXDOP 1);

/* Every band, always, empty ones included: an absent row and a band holding
   nothing must not look alike, and "unknown" above all must stay visible.
   cost_from is inclusive and cost_to exclusive; both are NULL for unknown. */
SELECT
    b.band                                                      AS [band],
    b.cost_from                                                 AS [cost_from],
    b.cost_to                                                   AS [cost_to],
    COUNT(c.execution_count)                                    AS [statements],
    SUM(CASE WHEN c.max_dop > 1 THEN 1 ELSE 0 END)              AS [parallel_statements],
    ISNULL(SUM(c.execution_count), 0)                           AS [executions],
    ISNULL(SUM(CASE WHEN c.max_dop > 1 THEN c.execution_count END), 0)
                                                                AS [parallel_executions],
    CAST(ISNULL(SUM(c.total_worker_time), 0) / 1000000.0 AS decimal(18,1))
                                                                AS [cpu_s],
    CAST(ISNULL(SUM(CASE WHEN c.max_dop > 1 THEN c.total_worker_time END), 0) / 1000000.0
         AS decimal(18,1))                                      AS [parallel_cpu_s],
    MAX(c.max_dop)                                              AS [max_dop]
FROM (VALUES (1, 'lt_5',        0.0,   5.0),
             (2, '5_25',        5.0,  25.0),
             (3, '25_50',      25.0,  50.0),
             (4, '50_100',     50.0, 100.0),
             (5, '100_500',   100.0, 500.0),
             (6, 'ge_500',    500.0,  NULL),
             (7, 'unknown',    NULL,  NULL)) AS b (ord, band, cost_from, cost_to)
LEFT JOIN @costed AS c
       ON (b.band = 'unknown' AND c.cost IS NULL)
       OR (b.band <> 'unknown' AND c.cost >= b.cost_from
           AND (b.cost_to IS NULL OR c.cost < b.cost_to))
GROUP BY b.ord, b.band, b.cost_from, b.cost_to
ORDER BY b.ord
OPTION (RECOMPILE, MAXDOP 1);
