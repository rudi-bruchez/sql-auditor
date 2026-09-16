-- @scope:       instance
-- @resultsets:  root:object, top_spills:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     120
-- @min_version: 13.0.5026
--
-- Which statements spilled to tempdb, and whether the grant or the estimate
-- was to blame.
--
-- Why this collector exists: an audit that names a slow procedure is expected
-- to say where its time went. On one instance the answer was a hash aggregate
-- spilling to tempdb twice, about ten seconds each, more than the rest of the
-- plan put together, and nothing in the archive could have said so. The
-- diagnosis needed an actual post-execution plan, which only the client can
-- produce, so the report had to ask for one and wait for a second exchange.
--
-- THE FLOOR IS MICROSOFT'S, NOT OURS. total_spills and last_spills arrived in
-- SQL Server 2016 SP2 (13.0.5026) and 2017 CU3. Below that build there is no
-- path to a spill except an actual plan: Query Store under 2019 stores
-- estimated plans only, which is why 022.query-store-profiled is gated at 15.0,
-- and query_store_runtime_stats gained no spill column until 2017. An instance
-- below the floor is not a gap in this corpus, and a report that asks for a
-- plan there should say which of the two it is.
--
-- THE SAME SP2 CHANGED WHAT THE COLUMN COUNTS. From SP2 the spills columns of
-- sys.dm_exec_query_stats also include pages spilled by parallelism operators,
-- which they did not before. Two builds either side of that line do not measure
-- the same thing, so the build is projected on the root rather than left to be
-- looked up.
--
-- THE GRANT IS THE OTHER HALF, and it is what turns a spill into an action.
-- total_grant_kb, total_used_grant_kb and total_ideal_grant_kb sit in the same
-- view since SQL Server 2016, so they cost nothing extra here. A statement that
-- spilled while using far less than it was granted was not short of memory: its
-- estimate was wrong, and more memory would not fix it. One that used all of a
-- grant well under its ideal was starved, which is a different conversation and
-- a different fix.
--
-- NO STATEMENT TEXT IS READ, which is a stronger claim than not projecting it
-- and the one this corpus actually requires. The first draft reached the
-- database and the object through sys.dm_exec_sql_text and projected neither
-- its text column nor anything derived from it; the corpus guard refused it
-- anyway, and the guard is right. Reading the text is the disclosure, because
-- what the server hands back is the statement itself, and a collector that
-- takes it is inside --include-session-text however carefully it discards it.
--
-- sys.dm_exec_plan_attributes answers with no text at all: dbid and objectid
-- are plan attributes. The object name is resolved ONLY when the cached plan
-- says objtype = 'Proc'. That guard is not decoration: for an ad hoc plan the
-- objectid attribute holds a HASH of the statement, not an object id, and
-- resolving it would eventually name some unrelated table that happens to
-- carry the same number. An ad hoc statement therefore arrives named by its
-- hashes alone, which is enough to find it again in the Query Store or the
-- plan cache.
--
-- NO JUDGEMENT IS APPLIED. A spill is not a defect. A one-off report that
-- spills once a month is not worth a grant hint, and a statement that spills a
-- page is not spilling in any sense a reader cares about. What is here is the
-- count, the pages and the two grant ratios; whether any of it is worth acting
-- on needs the workload, which is not in this archive.
--
-- THE COLUMN PREFIX IS memory_grant AND NOT grant, because the corpus lint
-- forbids the word GRANT outside a comment and it is right to: a collector may
-- only read, and the manifest attests as much. The lint strips comments and
-- blanks string literals before it looks, but a bracketed alias is neither, so
-- [grant.used_mb] reads to it exactly like the statement it exists to catch.
--
-- A LOW RATIO IS NOT A SPILL. Measured on SQL Server 2025, four statements
-- capped by MAX_GRANT_PERCENT ran with between 1.6 and 4.9 percent of their
-- ideal grant and spilled nothing at all. The engine's adaptive behaviour
-- absorbs more than the ratio suggests, so the spill columns are the finding
-- and the ratios only say which kind of conversation it is.
--
-- THE CACHE IS A SNAPSHOT, NOT A HISTORY. These rows live with their plan, so
-- a restart, memory pressure, most sp_configure changes and an explicit flush
-- take them. The instance start time is projected beside the counts so a reader
-- knows how much time they cover.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    CONVERT(nvarchar(128), SERVERPROPERTY('ProductVersion'))    AS [product_version],
    CONVERT(varchar(23), (SELECT sqlserver_start_time FROM sys.dm_os_sys_info), 126)
                                                                AS [instance_start],

    (SELECT COUNT(*) FROM sys.dm_exec_query_stats)              AS [statements.cached],
    (SELECT COUNT(*) FROM sys.dm_exec_query_stats
      WHERE total_spills > 0)                                   AS [statements.spilling],
    -- Pages, converted once here rather than in every reader. 8 KB a page.
    (SELECT CAST(SUM(total_spills) * 8.0 / 1024 AS DECIMAL(18,1))
       FROM sys.dm_exec_query_stats)                            AS [spilled.total_mb],
    (SELECT CAST(MAX(max_spills) * 8.0 / 1024 AS DECIMAL(18,1))
       FROM sys.dm_exec_query_stats)                            AS [spilled.largest_single_execution_mb]
OPTION (RECOMPILE, MAXDOP 1);

/* The fifty heaviest spillers, by pages spilled. Ordered that way because the
   list is read to decide where to look first, and a statement that spilled a
   gigabyte once is a better place to start than one that spilled a page a
   thousand times — the second is in the list too, and its execution count says
   which it is. */
SELECT TOP (50)
    DB_NAME(pa.dbid)                                            AS [database],
    -- NULL for anything but a compiled module, on purpose: see the header.
    CASE WHEN cp.objtype = 'Proc'
         THEN OBJECT_NAME(pa.objectid, pa.dbid) END             AS [object],
    cp.objtype                                                  AS [plan_type],
    CONVERT(varchar(34), q.query_hash, 1)                       AS [query_hash],
    CONVERT(varchar(34), q.query_plan_hash, 1)                  AS [query_plan_hash],
    q.execution_count                                           AS [executions],
    CAST(q.total_spills * 8.0 / 1024 AS DECIMAL(18,1))          AS [spilled_mb],
    CAST(q.max_spills   * 8.0 / 1024 AS DECIMAL(18,1))          AS [spilled_mb_worst_execution],
    CAST(q.last_spills  * 8.0 / 1024 AS DECIMAL(18,1))          AS [spilled_mb_last_execution],
    CAST(q.total_grant_kb       / 1024.0 AS DECIMAL(18,1))      AS [memory_grant.total_mb],
    CAST(q.total_used_grant_kb  / 1024.0 AS DECIMAL(18,1))      AS [memory_grant.used_mb],
    CAST(q.total_ideal_grant_kb / 1024.0 AS DECIMAL(18,1))      AS [memory_grant.ideal_mb],
    -- The two ratios that say which conversation this is. A statement that
    -- used a small share of what it was granted and spilled anyway has an
    -- estimate problem, not a memory problem. One granted far less than its
    -- ideal was starved by the server, usually by a resource governor pool or
    -- by concurrency.
    CAST(q.total_used_grant_kb * 100.0
         / NULLIF(q.total_grant_kb, 0) AS DECIMAL(5,1))         AS [memory_grant.used_pct_of_granted],
    CAST(q.total_grant_kb * 100.0
         / NULLIF(q.total_ideal_grant_kb, 0) AS DECIMAL(5,1))   AS [memory_grant.granted_pct_of_ideal],
    CAST(q.total_elapsed_time / 1000000.0 AS DECIMAL(18,1))     AS [total_elapsed_sec],
    CONVERT(varchar(23), q.creation_time, 126)                  AS [plan_cached_at],
    CONVERT(varchar(23), q.last_execution_time, 126)            AS [last_executed_at]
FROM sys.dm_exec_query_stats AS q
LEFT JOIN sys.dm_exec_cached_plans AS cp ON cp.plan_handle = q.plan_handle
CROSS APPLY (
    SELECT MAX(CASE WHEN a.attribute = 'dbid'     THEN CONVERT(int, a.value) END) AS dbid,
           MAX(CASE WHEN a.attribute = 'objectid' THEN CONVERT(int, a.value) END) AS objectid
    FROM sys.dm_exec_plan_attributes(q.plan_handle) AS a) AS pa
WHERE q.total_spills > 0
ORDER BY q.total_spills DESC
OPTION (RECOMPILE, MAXDOP 1);
