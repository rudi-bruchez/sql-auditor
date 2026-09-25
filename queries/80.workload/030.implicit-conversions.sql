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
-- WHERE THE PATTERN IS LOOKED FOR IS AS IMPORTANT AS THE PATTERN, and until
-- 25 September 2026 this file got it wrong. It cast the whole plan to text and
-- matched the pattern anywhere in it. Showplan carries StatementText, so a
-- statement that merely MENTIONS CONVERT_IMPLICIT(nvarchar was reported as
-- performing one. Measured on 16.0.4265.3 against an emptied cache: the
-- previous version reported two conversions, one real and one a statement that
-- converts nothing and carries the string as a literal in its select list. The
-- current version reports the real one alone, on the same cache. The likeliest
-- statement to trip this on a client instance is a DBA's own tuning script, a
-- monitoring product's query, or this tool's.
--
-- Reading @ScalarString on any ScalarOperator does not fix it, and that is
-- worth saying because it is the obvious next idea: a string literal in a
-- select list IS a ScalarOperator, and the same decoy was flagged again. What
-- separates them is where the operator sits, so the test is a conversion under
-- Predicate or under SeekPredicates. Four candidate tests were driven against
-- the pair; two discriminated and two did not.
--
-- ONE PLAN PER STATEMENT, NOT PER BATCH, for the same reason. The rows come
-- from sys.dm_exec_query_stats, which is per statement, and the plan used to
-- come from sys.dm_exec_query_plan, which is per batch: a warning belonging to
-- one statement of a procedure was attributed to every other statement of it.
-- sys.dm_exec_text_query_plan with the two offsets returns the fragment for the
-- statement the row came from, so the document holds one StmtSimple and a //
-- read cannot reach a sibling. It also returns dbid and objectid directly,
-- which removed the sys.dm_exec_plan_attributes join this file used to need.
--
-- IT DOES NOT USE THE PlanAffectingConvert WARNING, and the reason is the
-- collation, not luck.
--
-- Under a WINDOWS collation — French_CI_AS, Latin1_General_CI_AS, anything
-- not prefixed SQL_ — varchar sorts in the same order as the equivalent
-- nvarchar. Order being preserved, the optimizer can still seek across the
-- conversion by computing a range at runtime, which shows in the plan as
-- StartRange/EndRange against an Expr rather than a plain equality. Having
-- achieved a seek, the engine has nothing to warn about and stays silent —
-- while every column beyond the one that got the range remains in the
-- residual predicate, evaluated per row.
--
-- Under a legacy SQL_ collation the two sort orders differ, no range is
-- possible, the seek is lost outright, and only then does the warning appear.
--
-- So the warning is absent precisely where the schema is modern. Measured on
-- the instance this was built against, a French_CI_AS estate: 10 of 22
-- detected conversions carried no warning, including the three most-executed
-- statements. A warning-based check reports a clean bill of health on exactly
-- the estates most likely to be affected. server_raised_warning is projected
-- so the analysis layer can see that split rather than infer it.
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
SET LOCK_TIMEOUT 10000;

DECLARE @examined int = 200;

SELECT SYSDATETIME()                                              AS [collected_at],
       @examined                                                  AS [bounds.statements_examined],
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
/* THE CANDIDATES ARE MATERIALISED, and that is not tidiness, it is the whole
   cost. A common table expression is not a temporary table: every reference to
   it re-runs it. The shredded plan is read by three APPLYs below, so leaving it
   in a CTE re-fetched and re-parsed each plan three times, which measured 15 s
   on 17.0.4065.4 against 2.1 s for the same test run once. Twelve rows of
   roughly 100 KB is a megabyte of table variable and it buys back thirteen
   seconds. */
DECLARE @candidates TABLE (
    [query_hash]          binary(8),
    [execution_count]     bigint,
    [total_logical_reads] bigint,
    [total_worker_time]   bigint,
    [creation_time]       datetime,
    [last_execution_time] datetime,
    [dbid]                smallint,
    [objectid]            int,
    [px]                  xml);

INSERT INTO @candidates
SELECT c.query_hash, c.execution_count, c.total_logical_reads, c.total_worker_time,
       c.creation_time, c.last_execution_time, tp.dbid, tp.objectid,
       TRY_CAST(tp.query_plan AS xml)
FROM (
    /* A CHEAP SUPERSET FIRST, and it is not an optimisation either, it is what
       makes the exact test affordable at all. The exact test parses XML out of
       text and walks it; run over the whole sample it cost 27 s. The text match
       it replaces is a correct superset, since a statement that converts
       necessarily carries the pattern in its batch's plan text, so nothing true
       is lost and the expensive test then runs on a handful of candidates. On
       the same instance the sample of 200 came down to 12. */
    SELECT TOP (200) qs.plan_handle, qs.statement_start_offset, qs.statement_end_offset,
           qs.query_hash, qs.execution_count, qs.total_logical_reads,
           qs.total_worker_time, qs.creation_time, qs.last_execution_time
    FROM sys.dm_exec_query_stats AS qs
    CROSS APPLY sys.dm_exec_query_plan(qs.plan_handle) AS p
    WHERE p.query_plan IS NOT NULL
      AND CAST(p.query_plan AS nvarchar(max)) LIKE '%CONVERT_IMPLICIT(nvarchar%'
    ORDER BY qs.total_logical_reads DESC) AS c
/* One plan per STATEMENT, not per batch. sys.dm_exec_text_query_plan with the
   two offsets returns the fragment for the statement the row came from, so the
   document holds exactly one StmtSimple and a // read cannot reach a sibling
   statement's warning. The previous version paired a per-statement row from
   dm_exec_query_stats with the whole batch's plan, which is that mistake. dbid
   and objectid come back on this view directly, so the
   sys.dm_exec_plan_attributes join this file used to need is gone. */
CROSS APPLY sys.dm_exec_text_query_plan(c.plan_handle, c.statement_start_offset,
                                        c.statement_end_offset) AS tp
OPTION (RECOMPILE, MAXDOP 1);

WITH XMLNAMESPACES (DEFAULT 'http://schemas.microsoft.com/sqlserver/2004/07/showplan')
SELECT DB_NAME(a.[dbid])                                          AS [database],
       OBJECT_SCHEMA_NAME(a.[objectid], a.[dbid])
         + '.' + OBJECT_NAME(a.[objectid], a.[dbid])              AS [object],
       CONVERT(varchar(18), a.[query_hash], 1)                    AS [query_hash],
       a.[execution_count]                                        AS [execution_count],
       a.[total_logical_reads]                                    AS [total_logical_reads],
       a.[total_logical_reads] / NULLIF(a.[execution_count], 0)   AS [reads_per_execution],
       a.[total_worker_time] / NULLIF(a.[execution_count], 0)     AS [cpu_us_per_execution],
       a.[creation_time]                                          AS [plan_created],
       a.[last_execution_time]                                    AS [last_execution],
       CASE WHEN warn.hit IS NULL THEN 0 ELSE 1 END               AS [server_raised_warning]
FROM @candidates AS a
/* THE CONVERSION HAS TO BE IN A PREDICATE, and the spelling matters twice.

   Wrong: matching the pattern anywhere in the plan text, which is what this
   file did until 25 September 2026. Showplan carries StatementText, so a
   statement that merely MENTIONS CONVERT_IMPLICIT(nvarchar was reported as
   performing one. Measured against an emptied cache on 16.0.4265.3: the old
   version reported two conversions, one real and one a statement converting
   nothing that carried the string as a literal in its select list; the current
   version reports the real one alone on the same cache. Also wrong: reading
   @ScalarString on ANY ScalarOperator, the obvious next idea, because a string
   literal in a select list IS a ScalarOperator and the decoy passes again.
   What separates them is where the operator sits.

   Slow: exist() with a path, 16 s for three calls and 18.5 s for one combined
   call using self::. nodes() binds the node once, and a bound node is already
   located where a path read re-walks the document. TOP (1) because the
   question is whether any exists, not how many. */
OUTER APPLY (SELECT TOP (1) 1 AS hit
             FROM a.[px].nodes('//Predicate//ScalarOperator[contains(@ScalarString,"CONVERT_IMPLICIT(nvarchar")]') AS so(n)) AS pred
/* The second place a conversion can sit, and the one the Windows collation
   paragraph above is about: having achieved a range seek across the
   conversion, the engine puts it under RangeExpressions rather than in a
   residual Predicate. The lab runs a legacy SQL_ collation, under which that
   shape cannot occur, so this clause is reasoned from the showplan schema and
   is NOT measured. It is here rather than absent because the case it covers is
   the one the whole file exists for. */
OUTER APPLY (SELECT TOP (1) 1 AS hit
             FROM a.[px].nodes('//SeekPredicates//ScalarOperator[contains(@ScalarString,"CONVERT_IMPLICIT(nvarchar")]') AS so(n)) AS seek
OUTER APPLY (SELECT TOP (1) 1 AS hit
             FROM a.[px].nodes('//Warnings/PlanAffectingConvert') AS w(n)) AS warn
WHERE a.[px] IS NOT NULL
  AND (pred.hit IS NOT NULL OR seek.hit IS NOT NULL)
ORDER BY a.[total_logical_reads] DESC
OPTION (RECOMPILE, MAXDOP 1);
