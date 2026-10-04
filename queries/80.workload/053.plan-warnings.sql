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
-- no_join_predicate. A join with no predicate is a cartesian product. It is
-- not raised only for those. Measured again on 17.0.4065.4, 27 September 2026:
-- an equality on a unique key on each side raises nothing, but a range filter
-- on each side raises it, and so does a correct JOIN ... ON b.val = a.grp
-- WHERE a.grp = 3, where the optimizer turned the join predicate into a filter
-- on each input. The earlier sentence here, that a WHERE on each side raises
-- nothing, held for the unique-key case only. A statement flagged here is a
-- candidate to read, not a cartesian product established.
--
-- columns_with_no_statistics. The optimizer needed a distribution it did not
-- have and guessed. On a database with AUTO_CREATE_STATISTICS off this is the
-- consequence of that setting rather than an accident, so read it beside
-- 20.databases/010.all-databases.sql before writing it up.
--
-- Verified on a real workload first: five statements on 17.0.4065.4 carry it,
-- one of them naming master.sys.sysguidrefs.id. A first purpose-built
-- reproduction raised nothing; a second one, on 27 September 2026, did: with
-- AUTO_CREATE_STATISTICS off, a join to a heap column with no statistics and a
-- filter on such a column both raise it, and switching the option on removes
-- it and creates _WA_Sys_ statistics. The option change also flushed the
-- database's cached plans, so a reading right after it finds nothing.
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
-- VERIFIED ON 17.0.4065.4 ON 27 SEPTEMBER 2026, after being written from the
-- showplan schema alone: an earlier attempt on 16.0.4265.3 raised nothing. The
-- engine emits both the Warnings/@UnmatchedIndexes attribute read here and a
-- QueryPlan/UnmatchedIndexes element naming the index, together in every case
-- measured: a local variable, an sp_executesql parameter and a procedure
-- parameter, each reading the clustered index instead (57 reads against 2).
-- OPTION (RECOMPILE) raises neither. It has a FALSE POSITIVE this count
-- includes: an ad hoc literal that simple parameterisation attempted carries
-- the warning and still seeks the filtered index. object is NULL both for that
-- case and for sp_executesql, so the plan is what separates them.
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

DECLARE @examined int = 1000;
DECLARE @candidate_cap int = 500;

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

DECLARE @window TABLE (
    [plan_handle]            varbinary(64),
    [statement_start_offset] int,
    [statement_end_offset]   int,
    [query_hash]             binary(8),
    [execution_count]        bigint,
    [total_worker_time]      bigint,
    [total_logical_reads]    bigint,
    [creation_time]          datetime,
    [last_execution_time]    datetime);
DECLARE @matching TABLE ([plan_handle] varbinary(64) PRIMARY KEY);

BEGIN TRY
    /* THE WINDOW IS BOUNDED BEFORE ANY PLAN IS READ. Until 27 September 2026
       the TOP (200) sat on the query that cast each plan to text, so every
       statement's batch plan in the cache was cast and searched before two
       hundred were kept: the cap bounded rows, not work. Measured on
       17.0.4065.4 at about 95 ms per megabyte of plan text, which on a large
       production cache runs into the 300-second timeout and returns nothing.
       Found by a harm review. Now the thousand statements with the most CPU
       are taken from the DMV first, without reading a plan, and each of their
       batch plans is searched once. A statement outside that window is no
       longer seen; bounds.statements_examined says how wide it was. */
    INSERT INTO @window
    SELECT TOP (@examined) qs.plan_handle, qs.statement_start_offset,
           qs.statement_end_offset, qs.query_hash, qs.execution_count,
           qs.total_worker_time, qs.total_logical_reads,
           qs.creation_time, qs.last_execution_time
    FROM sys.dm_exec_query_stats AS qs
    ORDER BY qs.total_worker_time DESC
    OPTION (RECOMPILE, MAXDOP 1);

    /* The prefilter is a text match, and it is safe here for the one reason
       that makes a wrong test usable: it only has to be a SUPERSET. A
       statement carrying any of these annotations necessarily carries the
       string in its plan text, so nothing true is lost; a statement that
       merely mentions one is let through and rejected by the node reads
       below. Read once per batch plan, not once per predicate and statement.

       THE PLAN IS READ AS TEXT AND COMPARED IN BINARY, and each half of that
       is most of the cost. Until 27 September 2026 this read
       sys.dm_exec_query_plan, which builds an xml value, cast it back to
       nvarchar(max) and ran the five LIKEs under the instance collation.
       Measured on 17.0.4065.4 over a window of 1 000 statements holding 66 MB
       of batch plan text: building the xml cost about 50 ms per megabyte, of
       which rendering the text is 15, the cast 14 more, and the five LIKEs
       under the case-insensitive SQL_Latin1_General_CP1_CI_AS about 56,
       against 6.5 under Latin1_General_BIN2. The old form took 7.7 s of CPU,
       this one 1.4 s, and both kept the same 200 plans. That is one build, on
       Linux, under a SQL_ collation, on a lab schema of five tables whose
       plans average 131 KB; a Windows collation was not measured. Repeated
       on a second lab cache the same day, 67 MB in the window: this step went
       from 5.0 s to 1.1 s and the whole file from 8.5 s to 4.6 s, output
       unchanged. The xml is still built, but only by the TRY_CAST below, on
       the statement fragments of the candidates, and with the node reads
       that is most of the 3.5 s left. (These figures were taken with 200
       candidates; the header of the candidate cut below says what 500 cost.)

       BIN2 is case sensitive, so each pattern is spelt as showplan spells it:
       the element names Warnings and CursorPlan, the attribute names, and the
       attribute values between double quotes, which is how the text form
       writes them. That loses nothing, because the node reads below are
       XPath, whose names and value comparisons are case sensitive too. What
       it drops is a statement mentioning a pattern in other case in its own
       text, a decoy those reads reject anyway.

       sys.dm_exec_text_query_plan also renders a batch that
       sys.dm_exec_query_plan returns as NULL for nesting deeper than the 128
       levels the xml type allows. Such a batch can now pass here: a shallow
       statement of it is examined where it used to be skipped, and a deep one
       fails the TRY_CAST and is counted in bounds.plans_unparsed rather than
       being invisible, at the price of one of the @candidate_cap places. */
    INSERT INTO @matching
    SELECT h.plan_handle
    FROM (SELECT DISTINCT w.plan_handle FROM @window AS w) AS h
    CROSS APPLY sys.dm_exec_text_query_plan(h.plan_handle, 0, -1) AS p
    CROSS APPLY (SELECT p.query_plan COLLATE Latin1_General_BIN2 AS t) AS x
    WHERE p.query_plan IS NOT NULL
      AND (x.t LIKE N'%<Warnings%'
        OR x.t LIKE N'%CursorPlan%'
        OR x.t LIKE N'%StatementOptmEarlyAbortReason%'
        OR x.t LIKE N'%Optimized="1"%'
        OR x.t LIKE N'%Optimized="true"%')
    OPTION (RECOMPILE, MAXDOP 1);

    /* THE CANDIDATE CUT, and it is the one that binds. Measured on the lab
       (17.0.4065.4, a cache of about 2 500 statements), 4 October 2026: 894
       statements of the window passed the prefilter and 200 were read. Read
       up to 1 000, the counts went from 16 to 38 missing join predicates,
       86 to 209 plan-affecting converts and 50 to 157 optimizer timeouts,
       for 9 s against 27 s: the two hundred were reporting a third of what
       the window held. 500 is the middle of that. Measured the same day
       through the collector on a busier cache (5 000 statements, 930 of the
       window matched): 9.2 to 9.4 s and 83 KB at 200, 18.4 to 18.8 s and
       209 KB at 500, 32.7 to 33.8 s and 365 to 387 KB at 1 000; missing
       join predicates 33 to 41 at 200 and 150 at 500, optimizer give-ups 26
       against 82 to 89. The cost is the xml parse and the node reads, once per
       candidate, so it follows the candidates and not the window. And
       bounds.matched against bounds.candidate_cap says whether the cut took
       anything and how much, which until then bounds.candidates = 200 said
       only by its value. */
    INSERT INTO @candidates
    SELECT c.query_hash, c.execution_count, c.total_worker_time,
           c.total_logical_reads, c.creation_time, c.last_execution_time,
           tp.dbid, tp.objectid, TRY_CAST(tp.query_plan AS xml)
    FROM (
        SELECT TOP (@candidate_cap) w.*
        FROM @window AS w
        JOIN @matching AS m ON m.plan_handle = w.plan_handle
        ORDER BY w.total_worker_time DESC) AS c
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
    /* The window as filled, not its cap: on a cache holding fewer statements
       than @examined the cap overstated it, until 4 October 2026. */
    (SELECT COUNT(*) FROM @window)                              AS [bounds.statements_examined],
    @examined                                                   AS [bounds.examined_cap],
    (SELECT COUNT(*) FROM sys.dm_exec_query_stats)              AS [bounds.statements_in_cache],
    /* Statements of the window whose batch plan passed the prefilter, before
       the candidate cut. Above candidate_cap, the counts below are a floor. */
    (SELECT COUNT(*) FROM @window AS w
       JOIN @matching AS m ON m.plan_handle = w.plan_handle)    AS [bounds.matched],
    (SELECT COUNT(*) FROM @candidates)                          AS [bounds.candidates],
    @candidate_cap                                              AS [bounds.candidate_cap],
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
    cur.actual                                                  AS [cursor_actual_type],
    -- What the plan says about itself, read on the statements already kept
    -- and NOT added to the prefilter: every plan of an instance at MAXDOP 1
    -- carries NonParallelPlanReason="MaxDOPSetToOne", so filtering on it
    -- would crowd the annotated statements out of the window. The reason
    -- is named so a reader can tell a code cause (a scalar function, a table
    -- variable modified) from a setting. Measured on 17.0.4065.4 in cached
    -- plans, 27 September 2026: MaxDOPSetToOne under OPTION (MAXDOP 1), the
    -- compile figures on every plan, and TraceFlags with IsCompileTime under
    -- QUERYTRACEON 9481.
    qp.non_parallel_reason                                      AS [non_parallel_reason],
    qp.compile_time_ms                                          AS [compile.time_ms],
    qp.compile_cpu_ms                                           AS [compile.cpu_ms],
    qp.compile_memory_kb                                        AS [compile.memory_kb],
    tf.flags                                                    AS [compile_trace_flags]
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
OUTER APPLY (SELECT TOP (1) q.n.value('@NonParallelPlanReason', 'varchar(100)') AS non_parallel_reason,
                            q.n.value('@CompileTime', 'int')              AS compile_time_ms,
                            q.n.value('@CompileCPU', 'int')               AS compile_cpu_ms,
                            q.n.value('@CompileMemory', 'int')            AS compile_memory_kb
             FROM a.[px].nodes('//QueryPlan') AS q(n)) AS qp
/* The trace flags in force when the plan was compiled, as a list. Only the
   compile-time set: the execution-time one belongs to an actual plan. */
OUTER APPLY (SELECT STUFF((SELECT ',' + t.n.value('@Value', 'varchar(10)')
                           FROM a.[px].nodes('//QueryPlan/TraceFlags[@IsCompileTime="1" or @IsCompileTime="true"]/TraceFlag') AS t(n)
                           FOR XML PATH('')), 1, 1, '') AS flags) AS tf
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
