-- @scope:       instance
-- @resultsets:  root:object, statements:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     300
--
-- What the optimizer wrote down about the plans it built: the warnings it
-- raised, the times it gave up, and the cursors it converted.
--
-- WHY THIS COLLECTOR EXISTS. The engine annotates a plan with the problems it
-- met while building it, and nothing in this archive read those annotations.
-- They are not counters that can be derived from elsewhere: no dynamic
-- management view says that a join has no predicate, that a column the
-- optimizer needed has no statistics, or that a cursor was asked for one type
-- and given another. The plan is the only place these exist, and they are
-- already written down, which makes them the cheapest findings in the archive.
--
-- 80.workload/030.implicit-conversions.sql reads the same plans for one
-- specific pattern and keeps its own file because the question it answers is
-- narrower and its sample is ordered differently: it ranks by logical reads,
-- since a conversion that defeats an index shows up as reads. This one ranks
-- by CPU, because a cartesian product and a give-up on optimisation both show
-- up as processor time rather than as I/O.
--
-- THE SHAPE OF THIS FILE IS THE MEASURED ANSWER TO THREE TRAPS, and none of
-- the three is obvious from reading a plan.
--
-- One. A warning read with // from the root of a batch's plan belongs to the
-- batch, not to the statement. A procedure with twelve statements, one of them
-- carrying a warning, would have every one of the twelve reported. So the plan
-- here is fetched per statement, with sys.dm_exec_text_query_plan and the two
-- offsets from the same sys.dm_exec_query_stats row: the document then holds
-- one statement and // cannot reach a sibling. That is a structural answer
-- rather than a careful XPath, which is why it is the one used.
--
-- Two. Matching a pattern in the plan converted to text also matches the
-- statement's own text, because showplan carries StatementText. A query that
-- merely MENTIONS NoJoinPredicate is reported as having one. Measured on
-- 16.0.4265.3 with a statement written to carry the words and no warning: the
-- text match reports it, the node reads below do not. This is the same defect
-- that 030.implicit-conversions.sql carried until 25 September 2026, and the
-- reason the text match survives here only as a prefilter, where
-- over-selecting is harmless.
--
-- Three. Reading with exist() or value() and a path re-walks the document on
-- every call. Measured on 17.0.4065.4, on single-statement fragments averaging
-- 98 KB: three exist() calls cost 16 s and one combined call using self:: cost
-- 18.5 s, where binding the node with nodes() cost 2.1 s. And the candidates
-- are materialised into a table variable rather than left in a common table
-- expression, because a CTE is not a temporary table: every reference re-runs
-- it, which re-fetched and re-parsed each plan once per APPLY and cost 15 s
-- against 2.1 s for the same work done once.
--
-- WHAT EACH FLAG MEANS, AND WHAT IT IS WORTH.
--
-- no_join_predicate. A join with no predicate is a cartesian product. Measured:
-- it is raised only for a genuinely unrestricted join; the same query with a
-- WHERE on each side raises nothing, so this does not fire on the ordinary
-- large join that merely looks expensive.
--
-- columns_with_no_statistics. The optimizer needed a distribution it did not
-- have and guessed. On a database with AUTO_CREATE_STATISTICS off this is the
-- consequence of that setting rather than an accident, so read it beside
-- 20.databases/010.all-databases.sql before writing it up.
--
-- Verified on a real workload and NOT on a synthetic one, which is worth
-- separating. Five statements on 17.0.4065.4 carry it, one of them naming
-- master.sys.sysguidrefs.id, so the read works. A purpose-built reproduction
-- failed: a join on a column with no statistics at all, on a database with
-- AUTO_CREATE_STATISTICS off and sys.stats showing only the two primary keys,
-- raised nothing. Written down so the next person does not spend an hour
-- building the bed that does not work.
--
-- The warning names the column it lacked a distribution for, in a
-- ColumnReference, and that is deliberately not projected: the finding is the
-- statement, and 70.schema/090.statistics.sql is where the per-column question
-- belongs.
--
-- plan_affecting_convert. The engine says a conversion changed the plan, and
-- convert_issue says how: "Cardinality Estimate" means it distorted the row
-- estimate, "Seek Plan" means it cost a seek. Kept here as well as in 030
-- because 030 looks for CONVERT_IMPLICIT to nvarchar and nothing else, while
-- this warning also fires on an EXPLICIT CONVERT written by the application.
-- Measured on 17.0.4065.4: every PlanAffectingConvert on that instance carries
-- ConvertIssue="Cardinality Estimate", and one of them is
-- CONVERT(varchar(30),[Union1018],0), an explicit conversion that 030 cannot
-- see by construction.
--
-- unmatched_indexes. A filtered index the optimizer could not use because the
-- query is parameterised, which is the finding an estate that invested in
-- filtered indexes never hears about.
--
-- THIS ONE IS THE UNVERIFIED READ. A covering filtered index plus a
-- parameterised predicate on its filter column raised nothing on 16.0.4265.3,
-- and no plan on either lab instance carries the attribute. The read stays,
-- written from the showplan schema, because the case is real and this lab is
-- two standalone containers rather than an estate; what is written down is that
-- it has matched nothing verified.
--
-- IT APPEARED TO BE REPRODUCED FIRST, AND THAT SIGHTING WAS THE MEASURING
-- INSTRUMENT. The survey query carried the string UnmatchedIndexes in its own
-- text, its plan went into the cache like any other, and showplan stores
-- StatementText: a text search for a warning name finds the query that searches
-- for it. That is the same mechanism as the false positive this collector's
-- prefilter is allowed to have and its node reads are not, met from the other
-- side, and it is why nothing here concludes anything from a text match.
--
-- optimizer_early_abort. StatementOptmEarlyAbortReason says the optimizer
-- stopped before finishing. GoodEnoughPlanFound is the ordinary and healthy
-- one and is by far the most common; TimeOut and MemoryLimitExceeded are the
-- two that mean the plan in the cache is not the plan the optimizer would have
-- chosen. Projected as the reason rather than as a flag, because the three
-- reasons are not the same finding and a boolean would have flattened them.
--
-- Verified on a real workload: 15 statements of the 200 sampled on
-- 17.0.4065.4 carry TimeOut or MemoryLimitExceeded, one of them TimeOut with
-- CardinalityEstimationModelVersion 170. That count is the reason
-- counts.optimizer_gave_up excludes GoodEnoughPlanFound: counting all three
-- together would have read as 15 problems among many rather than as 15.
--
-- optimized_nested_loops. A nested loops join the engine chose to feed with a
-- hidden batch sort, which inflates the memory grant and is the thing trace
-- flag 2340 and USE HINT DISABLE_OPTIMIZED_NESTED_LOOP exist to switch off.
-- THE POLARITY IS THE WHOLE POINT AND IT IS EASY TO GET BACKWARDS: the
-- attribute is Optimized, and "0" is the ordinary case. Measured on
-- 16.0.4265.3, 7 statements of 20 carried Optimized="0" and none carried "1".
-- Collecting the wrong one would have flagged most of the instance.
--
-- cursor_requested_type / cursor_actual_type. The two are projected side by
-- side because the finding is the GAP, not the cursor. A cursor silently
-- converted is a cursor whose cost is nothing like what its author asked for.
-- The attributes the plan carries are CursorRequestedType and
-- CursorActualType; there is no CursorType, which is worth saying because it
-- is the name one expects and a read of it returns nothing without erroring.
--
-- Verified with a conversion built for the purpose on 16.0.4265.3, because a
-- comparison between two attributes that has never once been true is a
-- comparison nobody has tested. A KEYSET cursor over a heap with no unique
-- index comes back as SnapShot, and counts.cursors_converted reported 1 of 4
-- cursors with the pair Keyset -> SnapShot named in the array. A DYNAMIC
-- cursor over the same table is not converted, so the same run also shows the
-- predicate staying false where it should.
--
-- BOOLEAN ATTRIBUTES ARE TESTED FOR BOTH SPELLINGS. showplan types these as
-- xs:boolean, which serialises as either 1 or true. Measured on 16.0.4265.3
-- and 17.0.4065.4, both emit "1". The reads accept either, because an older
-- engine emitting "true" would otherwise report a clean instance and say so
-- with no error at all.
--
-- NOT COLLECTED, AND THE REASONS ARE MEASURED RATHER THAN ASSUMED:
--   SpillToTempDb              (only ever in an ACTUAL plan, confirmed: zero
--     occurrences across every cached plan on both lab instances)
--   the actual MemoryGrant     (same, and confirmed the same way; note that
--     MemoryGrantInfo, the ESTIMATE, IS in a cached plan and was found in 7 of
--     them, so the common claim that grants are absent from a cached plan is
--     half wrong)
--   UserDefinedFunction        (scalar functions are inlined from 2019 at
--     compatibility level 150, so the node is absent on a modern engine even
--     when a scalar function is called. Measured: a deliberately
--     multi-statement function reported sys.sql_modules.is_inlineable = 1 and
--     produced no node. 70.schema/080.modules.sql is where that question
--     belongs, through is_inlineable, not here through the plan)
--   the statement text         (it is application code; this file identifies a
--     statement by query_hash, database and object, as 030 does, so it needs
--     no disclosure annotation and the archive carries no client SQL from
--     here. Worth knowing for whoever edits this header: naming that
--     annotation in prose at the start of a line makes the corpus parser read
--     it as one, and it refused this file with "unknown value" until the
--     sentence was rewritten)
--
-- SQL Server 2012 is the floor. sys.dm_exec_text_query_plan and every
-- attribute read below predate it.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @examined int = 200;

DECLARE @candidates TABLE (
    [query_hash]          binary(8),
    [execution_count]     bigint,
    [total_worker_time]   bigint,
    [total_logical_reads] bigint,
    [creation_time]       datetime,
    [last_execution_time] datetime,
    [dbid]                smallint,
    [objectid]            int,
    [px]                  xml);

DECLARE @err int = 0, @msg nvarchar(2048) = N'';

BEGIN TRY
    /* The prefilter is a text match, and it is safe here for the one reason
       that makes a wrong test usable: it only has to be a SUPERSET. A
       statement carrying any of these annotations necessarily carries the
       string in its plan text, so nothing true is lost; a statement that
       merely mentions one is let through and rejected by the node reads
       below. Without it the shred runs on the whole sample, which is what
       cost 27 s in the file this one is modelled on. */
    INSERT INTO @candidates
    SELECT c.query_hash, c.execution_count, c.total_worker_time,
           c.total_logical_reads, c.creation_time, c.last_execution_time,
           tp.dbid, tp.objectid, TRY_CAST(tp.query_plan AS xml)
    FROM (
        SELECT TOP (200) qs.plan_handle, qs.statement_start_offset,
               qs.statement_end_offset, qs.query_hash, qs.execution_count,
               qs.total_worker_time, qs.total_logical_reads,
               qs.creation_time, qs.last_execution_time
        FROM sys.dm_exec_query_stats AS qs
        CROSS APPLY sys.dm_exec_query_plan(qs.plan_handle) AS p
        WHERE p.query_plan IS NOT NULL
          AND (CAST(p.query_plan AS nvarchar(max)) LIKE '%<Warnings%'
            OR CAST(p.query_plan AS nvarchar(max)) LIKE '%CursorPlan%'
            OR CAST(p.query_plan AS nvarchar(max)) LIKE '%StatementOptmEarlyAbortReason%'
            OR CAST(p.query_plan AS nvarchar(max)) LIKE '%Optimized="1"%'
            OR CAST(p.query_plan AS nvarchar(max)) LIKE '%Optimized="true"%')
        ORDER BY qs.total_worker_time DESC) AS c
    CROSS APPLY sys.dm_exec_text_query_plan(c.plan_handle, c.statement_start_offset,
                                            c.statement_end_offset) AS tp
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH;

WITH XMLNAMESPACES (DEFAULT 'http://schemas.microsoft.com/sqlserver/2004/07/showplan')
SELECT
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    @examined                                                   AS [bounds.statements_examined],
    (SELECT COUNT(*) FROM sys.dm_exec_query_stats)              AS [bounds.statements_in_cache],
    (SELECT COUNT(*) FROM @candidates)                          AS [bounds.candidates],
    /* A plan the engine returned as text and that did not parse back into XML.
       It is invisible to every read below, so it is counted: a zero here is
       what makes the counts that follow mean anything. */
    (SELECT COUNT(*) FROM @candidates WHERE [px] IS NULL)       AS [bounds.plans_unparsed],
    (SELECT MIN([creation_time]) FROM @candidates)              AS [cache.oldest_candidate_plan],
    (SELECT COUNT(*) FROM @candidates AS c
      WHERE c.[px].exist('//Warnings[@NoJoinPredicate="1" or @NoJoinPredicate="true"]') = 1)
                                                                AS [counts.no_join_predicate],
    (SELECT COUNT(*) FROM @candidates AS c
      WHERE c.[px].exist('//Warnings/ColumnsWithNoStatistics') = 1)
                                                                AS [counts.columns_with_no_statistics],
    (SELECT COUNT(*) FROM @candidates AS c
      WHERE c.[px].exist('//Warnings/PlanAffectingConvert') = 1)
                                                                AS [counts.plan_affecting_convert],
    (SELECT COUNT(*) FROM @candidates AS c
      WHERE c.[px].exist('//Warnings[@UnmatchedIndexes="1" or @UnmatchedIndexes="true"]') = 1)
                                                                AS [counts.unmatched_indexes],
    (SELECT COUNT(*) FROM @candidates AS c
      WHERE c.[px].exist('//NestedLoops[@Optimized="1" or @Optimized="true"]') = 1)
                                                                AS [counts.optimized_nested_loops],
    (SELECT COUNT(*) FROM @candidates AS c
      WHERE c.[px].exist('//CursorPlan') = 1)                   AS [counts.cursors],
    /* Split from the count above, because a cursor is not a finding and a
       converted one is: the engine gave back something other than what the
       author asked for, and the cost of the two is not comparable. */
    (SELECT COUNT(*) FROM @candidates AS c
      WHERE c.[px].exist('//CursorPlan[@CursorRequestedType != @CursorActualType]') = 1)
                                                                AS [counts.cursors_converted],
    /* GoodEnoughPlanFound is deliberately not counted here. It is the ordinary
       outcome on a simple statement and counting it would drown the two that
       matter, which the array below names one by one. */
    (SELECT COUNT(*) FROM @candidates AS c
      WHERE c.[px].exist('//StmtSimple[@StatementOptmEarlyAbortReason="TimeOut"
                                    or @StatementOptmEarlyAbortReason="MemoryLimitExceeded"]') = 1)
                                                                AS [counts.optimizer_gave_up],
    CASE WHEN @err = 0 THEN 1 ELSE 0 END                        AS [collected.plan_warnings],
    @err                                                        AS [errors.plan_warnings],
    NULLIF(@msg, N'')                                           AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

/* One row per statement that carries at least one annotation. The flags are
   read from nodes bound once rather than from paths walked per call, for the
   reason in the header, and each OUTER APPLY takes TOP (1) because the
   question is whether any exists. */
WITH XMLNAMESPACES (DEFAULT 'http://schemas.microsoft.com/sqlserver/2004/07/showplan')
SELECT
    DB_NAME(a.[dbid])                                           AS [database],
    OBJECT_SCHEMA_NAME(a.[objectid], a.[dbid])
      + '.' + OBJECT_NAME(a.[objectid], a.[dbid])               AS [object],
    CONVERT(varchar(18), a.[query_hash], 1)                     AS [query_hash],
    a.[execution_count]                                         AS [execution_count],
    a.[total_worker_time] / NULLIF(a.[execution_count], 0)      AS [cpu_us_per_execution],
    a.[total_logical_reads] / NULLIF(a.[execution_count], 0)    AS [reads_per_execution],
    CONVERT(varchar(23), a.[creation_time], 126)                AS [plan_created],
    CONVERT(varchar(23), a.[last_execution_time], 126)          AS [last_execution],
    CAST(CASE WHEN nojoin.hit IS NULL THEN 0 ELSE 1 END AS bit) AS [no_join_predicate],
    CAST(CASE WHEN nostats.hit IS NULL THEN 0 ELSE 1 END AS bit) AS [columns_with_no_statistics],
    CAST(CASE WHEN unmatched.hit IS NULL THEN 0 ELSE 1 END AS bit) AS [unmatched_indexes],
    CAST(CASE WHEN optnl.hit IS NULL THEN 0 ELSE 1 END AS bit)  AS [optimized_nested_loops],
    -- The conversion warning, with the engine's own word for what it broke.
    -- Null means no such warning rather than an unknown issue.
    conv.issue                                                  AS [convert_issue],
    -- Named rather than flagged: TimeOut and MemoryLimitExceeded mean the
    -- cached plan is not the one the optimizer would have chosen, and
    -- GoodEnoughPlanFound means nothing is wrong.
    stmt.early_abort                                            AS [optimizer_early_abort],
    stmt.optm_level                                             AS [optimizer_level],
    cur.requested                                               AS [cursor_requested_type],
    cur.actual                                                  AS [cursor_actual_type]
FROM @candidates AS a
OUTER APPLY (SELECT TOP (1) 1 AS hit
             FROM a.[px].nodes('//Warnings[@NoJoinPredicate="1" or @NoJoinPredicate="true"]') AS w(n)) AS nojoin
OUTER APPLY (SELECT TOP (1) 1 AS hit
             FROM a.[px].nodes('//Warnings/ColumnsWithNoStatistics') AS w(n)) AS nostats
OUTER APPLY (SELECT TOP (1) 1 AS hit
             FROM a.[px].nodes('//Warnings[@UnmatchedIndexes="1" or @UnmatchedIndexes="true"]') AS w(n)) AS unmatched
OUTER APPLY (SELECT TOP (1) 1 AS hit
             FROM a.[px].nodes('//NestedLoops[@Optimized="1" or @Optimized="true"]') AS w(n)) AS optnl
OUTER APPLY (SELECT TOP (1) w.n.value('@ConvertIssue', 'varchar(60)') AS issue
             FROM a.[px].nodes('//Warnings/PlanAffectingConvert') AS w(n)) AS conv
OUTER APPLY (SELECT TOP (1) s.n.value('@StatementOptmEarlyAbortReason', 'varchar(60)') AS early_abort,
                            s.n.value('@StatementOptmLevel', 'varchar(20)') AS optm_level
             FROM a.[px].nodes('//StmtSimple') AS s(n)) AS stmt
OUTER APPLY (SELECT TOP (1) c.n.value('@CursorRequestedType', 'varchar(40)') AS requested,
                            c.n.value('@CursorActualType', 'varchar(40)') AS actual
             FROM a.[px].nodes('//CursorPlan') AS c(n)) AS cur
WHERE a.[px] IS NOT NULL
  /* Only statements that actually carry something. The prefilter let through
     whatever mentioned the words; this is where a statement that merely
     mentions them is dropped. */
  AND (nojoin.hit IS NOT NULL
    OR nostats.hit IS NOT NULL
    OR unmatched.hit IS NOT NULL
    OR optnl.hit IS NOT NULL
    OR conv.issue IS NOT NULL
    OR cur.requested IS NOT NULL
    OR stmt.early_abort IN ('TimeOut', 'MemoryLimitExceeded'))
ORDER BY a.[total_worker_time] DESC
OPTION (RECOMPILE, MAXDOP 1);
