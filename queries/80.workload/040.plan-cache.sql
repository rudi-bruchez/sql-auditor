-- @scope:       instance
-- @resultsets:  root:object, by_type:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     120
--
-- What the plan cache is made of, and how much of it is used once and kept.
--
-- Why this collector exists: 015.buffer-pool.sql reports stolen memory, and on
-- a real instance that was 14 GB with no way to say what had stolen it. The
-- plan cache is the usual answer and the archive could not confirm it. Worse,
-- the specific finding — a cache dominated by ad hoc plans compiled once and
-- never reused — had to be inferred from the ratio of distinct queries to
-- distinct plans in the Query Store, which is a different population measured a
-- different way. An inference from a neighbouring source is not a finding.
--
-- USECOUNTS = 1 IS THE WHOLE MEASURE. A cached plan that has been used once is
-- either a plan for a statement that runs once — legitimate — or a plan the
-- server will never match again because the statement arrives with a different
-- literal each time. Both consume memory that could hold data pages. Their
-- share of the cache is the argument for or against
-- 'optimize for ad hoc workloads', and nothing else in this archive can make
-- it.
--
-- NO STATEMENT TEXT IS COLLECTED. Aggregating by objtype and cacheobjtype
-- answers the question without projecting a single query, which keeps this
-- collector out of the disclosure that --include-session-text carries. The
-- text of the heaviest plans belongs to the Query Store extraction, behind its
-- own flag.
--
-- NO JUDGEMENT IS APPLIED. A large ad hoc share is normal on some workloads
-- and pathological on others, and 'optimize for ad hoc workloads' has costs of
-- its own — the first execution of every statement stores a stub instead of a
-- plan, so a workload that reuses on the second execution pays twice. Whether
-- the trade is worth making needs the application's shape, which is not here.
--
-- THE CACHE IS A SNAPSHOT, NOT A HISTORY. It is emptied by a restart, by
-- memory pressure, by most sp_configure changes and by an explicit flush. The
-- instance start time is projected beside it so a reader knows how much time
-- the numbers cover — a cache measured ten minutes after a restart says
-- nothing about the workload.
--
-- SQL Server 2012 is the floor; sys.dm_exec_cached_plans predates it. The
-- memory clerk totals come from sys.dm_os_memory_clerks, already read by
-- 015.buffer-pool.sql for a different purpose.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    -- Without this, every count below is unreadable.
    CONVERT(varchar(23), (SELECT sqlserver_start_time FROM sys.dm_os_sys_info), 126)
                                                                AS [instance_start],

    (SELECT COUNT(*) FROM sys.dm_exec_cached_plans)             AS [cache.plans],
    (SELECT CAST(SUM(CAST(size_in_bytes AS bigint)) / 1048576.0 AS DECIMAL(18,1))
       FROM sys.dm_exec_cached_plans)                           AS [cache.total_mb],

    (SELECT COUNT(*) FROM sys.dm_exec_cached_plans
      WHERE usecounts = 1)                                      AS [single_use.plans],
    (SELECT CAST(SUM(CAST(size_in_bytes AS bigint)) / 1048576.0 AS DECIMAL(18,1))
       FROM sys.dm_exec_cached_plans WHERE usecounts = 1)       AS [single_use.mb],
    -- The two numbers a decision is made on, stated rather than left to be
    -- divided by hand.
    (SELECT CAST(COUNT(*) * 100.0 / NULLIF((SELECT COUNT(*) FROM sys.dm_exec_cached_plans), 0)
                 AS DECIMAL(5,1))
       FROM sys.dm_exec_cached_plans WHERE usecounts = 1)       AS [single_use.percent_of_plans],
    (SELECT CAST(SUM(CAST(size_in_bytes AS bigint)) * 100.0
                 / NULLIF((SELECT SUM(CAST(size_in_bytes AS bigint))
                             FROM sys.dm_exec_cached_plans), 0) AS DECIMAL(5,1))
       FROM sys.dm_exec_cached_plans WHERE usecounts = 1)       AS [single_use.percent_of_memory],

    -- Ad hoc specifically: the population 'optimize for ad hoc workloads' acts
    -- on. Prepared and procedure plans are reused by design and are not what
    -- that setting addresses.
    (SELECT COUNT(*) FROM sys.dm_exec_cached_plans
      WHERE objtype = 'Adhoc' AND usecounts = 1)                AS [single_use.adhoc_plans],
    (SELECT CAST(SUM(CAST(size_in_bytes AS bigint)) / 1048576.0 AS DECIMAL(18,1))
       FROM sys.dm_exec_cached_plans
      WHERE objtype = 'Adhoc' AND usecounts = 1)                AS [single_use.adhoc_mb],

    (SELECT CAST(CAST(value_in_use AS int) AS bit) FROM sys.configurations
      WHERE name = 'optimize for ad hoc workloads')             AS [config.optimize_for_adhoc],
    (SELECT CAST(value_in_use AS int) FROM sys.configurations
      WHERE name = 'max server memory (MB)')                    AS [config.max_server_memory_mb],

    -- The clerks the cache actually lives in, so the total above can be placed
    -- against the stolen memory 015.buffer-pool.sql reports.
    (SELECT CAST(SUM(pages_kb) / 1024.0 AS DECIMAL(18,1)) FROM sys.dm_os_memory_clerks
      WHERE type = 'CACHESTORE_SQLCP')                          AS [clerks.sql_plans_mb],
    (SELECT CAST(SUM(pages_kb) / 1024.0 AS DECIMAL(18,1)) FROM sys.dm_os_memory_clerks
      WHERE type = 'CACHESTORE_OBJCP')                          AS [clerks.object_plans_mb],
    (SELECT CAST(SUM(pages_kb) / 1024.0 AS DECIMAL(18,1)) FROM sys.dm_os_memory_clerks
      WHERE type = 'CACHESTORE_PHDR')                           AS [clerks.bound_trees_mb],
    (SELECT CAST(SUM(pages_kb) / 1024.0 AS DECIMAL(18,1)) FROM sys.dm_os_memory_clerks
      WHERE type = 'MEMORYCLERK_SQLOPTIMIZER')                  AS [clerks.optimizer_mb]
OPTION (RECOMPILE, MAXDOP 1);

/* One row per kind of cached object. objtype says what compiled it — Adhoc,
   Prepared, Proc, Trigger, View — and cacheobjtype says what is stored, since
   a "Compiled Plan Stub" is what optimize for ad hoc workloads leaves behind
   and counting it as a plan would overstate the cache. */
SELECT
    cp.cacheobjtype                                             AS [cache_object_type],
    cp.objtype                                                  AS [object_type],
    COUNT(*)                                                    AS [plans],
    CAST(SUM(CAST(cp.size_in_bytes AS bigint)) / 1048576.0 AS DECIMAL(18,1))
                                                                AS [total_mb],
    SUM(CASE WHEN cp.usecounts = 1 THEN 1 ELSE 0 END)           AS [single_use_plans],
    CAST(SUM(CASE WHEN cp.usecounts = 1
                  THEN CAST(cp.size_in_bytes AS bigint) ELSE 0 END) / 1048576.0
         AS DECIMAL(18,1))                                      AS [single_use_mb],
    MAX(cp.usecounts)                                           AS [max_usecounts],
    -- The average is projected beside the maximum because one heavily reused
    -- plan hides a thousand that never were.
    CAST(AVG(CAST(cp.usecounts AS bigint)) AS bigint)           AS [avg_usecounts]
FROM sys.dm_exec_cached_plans AS cp
GROUP BY cp.cacheobjtype, cp.objtype
ORDER BY SUM(CAST(cp.size_in_bytes AS bigint)) DESC
OPTION (RECOMPILE, MAXDOP 1);
