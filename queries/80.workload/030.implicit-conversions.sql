-- @scope:       instance
-- @resultsets:  root:object, conversions:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     300
--
-- Cached plans that convert a column to nvarchar, and what they cost.
--
-- Why this collector exists: on a real audit an UPDATE running 2 825 times in
-- a few minutes showed an Index Seek whose seek key was one column and whose
-- residual predicate carried four CONVERT_IMPLICIT(nvarchar(n), column, 0)
-- expressions. The columns were varchar; the driver sent nvarchar parameters;
-- nvarchar wins datatype precedence, so SQL Server converted the COLUMN and
-- the predicate stopped being seekable. The database in question held 11 145
-- varchar and char columns, so the exposure was the schema, not the query.
--
-- THE DIRECTION OF THE CONVERSION IS THE WHOLE FINDING. Converting a
-- parameter is free and normal; converting a column is what defeats the
-- index. The pattern matched here is CONVERT_IMPLICIT(nvarchar — with the
-- column inside — so a plan that merely widens a parameter does not appear.
--
-- IT DOES NOT USE THE PlanAffectingConvert WARNING, and that is deliberate.
-- The obvious detection is to look for SQL Server's own warning about the
-- conversion. The plan that prompted this collector carried NO warning at
-- all: the engine emits it only when it judges the conversion to affect a
-- seek plan or a cardinality estimate, and it frequently does not. A
-- warning-based check would have reported a clean instance. Matching the plan
-- text finds the case the warning misses.
--
-- BOUNDED, AND THE BOUND IS REPORTED. Casting plan XML to text is CPU-heavy
-- per plan, so only the heaviest cached plans are examined — heaviest by
-- logical reads, since that is the quantity this defect inflates. A count of
-- how many plans were examined out of how many exist travels with the result,
-- because "no conversions found" means nothing without knowing how much of
-- the cache was looked at.
--
-- IT SEES ONLY WHAT IS STILL CACHED, which on a busy instance can be hours
-- rather than days, and nothing at all for statements that never cache. The
-- plan cache age is reported for the same reason.
--
-- NO STATEMENT TEXT IS COLLECTED, and the first version of this file got that
-- wrong. It projected 300 characters of sys.dm_exec_sql_text, which is
-- application SQL and can carry literals from the workload — the exact class
-- of data 052.session-text.sql puts behind --include-session-text. The corpus
-- test that gates session text caught it before it ever ran unattended.
--
-- Identification is done from sys.dm_exec_plan_attributes instead: it yields
-- the database and the object id of the statement without touching its text.
-- A statement inside a stored procedure is therefore named; an ad-hoc
-- parameterised statement from an application is not, and is identified by
-- query_hash, which the analysis layer can resolve with an elevated login or
-- by re-running the collection with --include-session-text.
--
-- SQL Server 2012 is the floor. All the DMVs used predate it.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

DECLARE @examined int = 200;

SELECT SYSDATETIME()                                              AS [collected_at],
       @examined                                                  AS [bounds.plans_examined],
       (SELECT COUNT(*) FROM sys.dm_exec_query_stats)             AS [bounds.plans_in_cache],
       (SELECT MIN(qs.creation_time) FROM sys.dm_exec_query_stats AS qs) AS [cache.oldest_plan],
       (SELECT MAX(qs.last_execution_time) FROM sys.dm_exec_query_stats AS qs) AS [cache.newest_execution],
       (SELECT CAST(value_in_use AS int) FROM sys.configurations
        WHERE name = 'optimize for ad hoc workloads')             AS [cache.optimize_for_ad_hoc]
OPTION (RECOMPILE, MAXDOP 1);

/* reads_per_execution is the column to read first. A statement converting a
   column and reading four rows per execution is a curiosity; one reading two
   hundred thousand to return one is the finding, and the ratio says which is
   which without needing the plan. */
WITH heaviest AS (
    SELECT TOP (200) qs.plan_handle, qs.query_hash, qs.query_plan_hash,
           qs.execution_count, qs.total_logical_reads, qs.total_worker_time,
           qs.last_execution_time, qs.creation_time
    FROM sys.dm_exec_query_stats AS qs
    ORDER BY qs.total_logical_reads DESC),
attributed AS (
    SELECT h.*,
           MAX(CASE WHEN a.attribute = 'dbid'     THEN CONVERT(int, a.value) END) AS dbid,
           MAX(CASE WHEN a.attribute = 'objectid' THEN CONVERT(int, a.value) END) AS objectid
    FROM heaviest AS h
    CROSS APPLY sys.dm_exec_plan_attributes(h.plan_handle) AS a
    WHERE a.attribute IN ('dbid', 'objectid')
    GROUP BY h.plan_handle, h.query_hash, h.query_plan_hash, h.execution_count,
             h.total_logical_reads, h.total_worker_time, h.last_execution_time, h.creation_time)
SELECT DB_NAME(a.dbid)                                            AS [database],
       OBJECT_SCHEMA_NAME(a.objectid, a.dbid)
         + '.' + OBJECT_NAME(a.objectid, a.dbid)                  AS [object],
       CONVERT(varchar(18), a.query_hash, 1)                      AS [query_hash],
       a.execution_count                                          AS [execution_count],
       a.total_logical_reads                                      AS [total_logical_reads],
       a.total_logical_reads / NULLIF(a.execution_count, 0)       AS [reads_per_execution],
       a.total_worker_time / NULLIF(a.execution_count, 0)         AS [cpu_us_per_execution],
       a.creation_time                                            AS [plan_created],
       a.last_execution_time                                      AS [last_execution],
       CASE WHEN CAST(p.query_plan AS nvarchar(max)) LIKE '%PlanAffectingConvert%'
            THEN 1 ELSE 0 END                                     AS [server_raised_warning]
FROM       attributed AS a
CROSS APPLY sys.dm_exec_query_plan(a.plan_handle) AS p
WHERE p.query_plan IS NOT NULL
  AND CAST(p.query_plan AS nvarchar(max)) LIKE '%CONVERT_IMPLICIT(nvarchar%'
ORDER BY a.total_logical_reads DESC
OPTION (RECOMPILE, MAXDOP 1);
