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
-- index. The pattern matched here is CONVERT_IMPLICIT(nvarchar, and the text
-- alone does not say what is inside it: measured on 17.0.4065.4, a varchar
-- parameter sought against an nvarchar column matches too, in the seek key.
-- Which column is converted is projected (converted.*, below), and a row whose
-- converted.column is NULL converted a parameter, not a column.
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
-- while the converted columns remain in the residual predicate of the seek,
-- evaluated per row. Measured on 17.0.4065.4 under Latin1_General_CI_AS: the
-- residual keeps the conversion of the column that got the range as well as of
-- the others, and the range itself comes from GetRangeThroughConvert applied
-- to the parameter, so the conversion of the column is found in the Predicate.
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
-- BOUNDED, AND THE BOUND IS REPORTED. Rendering a plan as text costs CPU in
-- proportion to its size, so only the heaviest cached plans are examined,
-- heaviest by logical reads, since that is the quantity this defect inflates.
-- What the search costs per megabyte is set out beside the prefilter below. A
-- count of how many plans were examined out of how many exist travels with
-- the result, because "no conversions found" means nothing without knowing
-- how much of the cache was looked at.
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
-- What IS collected from the plan is the identity of the converted column:
-- database, schema, table and column names as the plan's ColumnReference
-- spells them, and the type it was converted to. These are object names, the
-- same class of data 70.schema exports for every table; no literal and no
-- parameter value is read, and the declared type is left to
-- 70.schema/060.columns.
--
-- SQL Server 2012 is the floor. All the DMVs used predate it.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @examined int = 1000;

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

/* THE WINDOW IS BOUNDED BEFORE ANY PLAN IS READ. Until 27 September 2026 the
   TOP (200) below sat on the query that cast each plan to text, so the server
   cast and searched the plan of EVERY statement in the cache, a batch plan
   once per statement of the batch, before keeping two hundred; the cap bounded
   the rows kept, not the work. Measured on 17.0.4065.4: about 95 ms per
   megabyte of plan text, on a cache where 44 statements in 50 passed the text
   match, so on a production cache of several gigabytes of plans the read ran
   into the 300-second timeout and returned nothing. Found by a harm review.

   Now the thousand statements with the most logical reads are taken from the
   DMV first, which reads no plan, and only their batch plans are searched,
   each plan once. This changes what is found as well as what it costs: a
   converting statement outside the thousand heaviest is no longer seen. The
   finding is about statements that read a lot, so the heaviest are where it
   is, and bounds.statements_examined says how wide the window was. */
DECLARE @window TABLE (
    [plan_handle]            varbinary(64),
    [statement_start_offset] int,
    [statement_end_offset]   int,
    [query_hash]             binary(8),
    [execution_count]        bigint,
    [total_logical_reads]    bigint,
    [total_worker_time]      bigint,
    [creation_time]          datetime,
    [last_execution_time]    datetime);

INSERT INTO @window
SELECT TOP (@examined) qs.plan_handle, qs.statement_start_offset, qs.statement_end_offset,
       qs.query_hash, qs.execution_count, qs.total_logical_reads,
       qs.total_worker_time, qs.creation_time, qs.last_execution_time
FROM sys.dm_exec_query_stats AS qs
ORDER BY qs.total_logical_reads DESC
OPTION (RECOMPILE, MAXDOP 1);

/* A CHEAP SUPERSET NEXT, once per batch plan. The exact test parses XML out
   of text and walks it; run over a whole sample it cost 27 s. The text match
   is a correct superset, since a statement that converts necessarily carries
   the pattern in its batch's plan text, so nothing true is lost and the
   expensive test then runs on a handful of candidates.

   THE PLAN IS READ AS TEXT AND COMPARED IN BINARY, and each half of that is
   most of the cost. Until 27 September 2026 this read sys.dm_exec_query_plan,
   which builds an xml value, cast it back to nvarchar(max) and ran LIKE under
   the instance collation. Measured on 17.0.4065.4 over a window of 1 000
   statements holding 66 MB of batch plan text: building the xml cost about
   50 ms per megabyte, of which rendering the text is 15 and the conversion to
   xml the rest, the cast 14 more, and a LIKE under the case-insensitive
   SQL_Latin1_General_CP1_CI_AS about 24, against 3 under Latin1_General_BIN2.
   The old form took 5.3 s of CPU, this one 1.2 s, and both kept the same 128
   plans. That is one build, on Linux, under a SQL_ collation, on a lab schema
   of five tables whose plans average 131 KB: the ratio between xml and text
   can move with the shape of the plans, and a Windows collation was not
   measured. Repeated on a second lab cache the same day, 62 MB in the
   window: this step went from 4.6 s to 1.1 s and the whole file from 6.0 s
   to 2.5 s, output unchanged. The xml is still built, but only by the
   TRY_CAST below, on the statement fragments of the two hundred candidates,
   and with the node reads that is most of the 1.4 s left.

   BIN2 is case sensitive, so the pattern is spelt as showplan spells it, and
   that loses nothing: the exact test below uses XPath contains(), which is
   case sensitive too, so a plan the binary match rejects is one the exact
   test would have rejected. What it drops is a statement that mentions the
   pattern in other case in its own text, a decoy the node reads below reject
   anyway.

   sys.dm_exec_text_query_plan also renders a plan that sys.dm_exec_query_plan
   returns as NULL because it nests deeper than the 128 levels the xml type
   allows. Such a batch can now pass here. If its statement's fragment is
   shallow, the statement is examined where it used to be skipped; if the
   fragment is deep too, TRY_CAST gives NULL and the final WHERE drops it, but
   only after it has taken one of the two hundred places, which matters only
   when more than two hundred statements match. */
DECLARE @matching TABLE ([plan_handle] varbinary(64) PRIMARY KEY);

INSERT INTO @matching
SELECT h.plan_handle
FROM (SELECT DISTINCT w.plan_handle FROM @window AS w) AS h
CROSS APPLY sys.dm_exec_text_query_plan(h.plan_handle, 0, -1) AS p
WHERE p.query_plan IS NOT NULL
  AND p.query_plan COLLATE Latin1_General_BIN2 LIKE N'%CONVERT_IMPLICIT(nvarchar%'
OPTION (RECOMPILE, MAXDOP 1);

INSERT INTO @candidates
SELECT c.query_hash, c.execution_count, c.total_logical_reads, c.total_worker_time,
       c.creation_time, c.last_execution_time, tp.dbid, tp.objectid,
       TRY_CAST(tp.query_plan AS xml)
FROM (
    SELECT TOP (200) w.*
    FROM @window AS w
    JOIN @matching AS m ON m.plan_handle = w.plan_handle
    ORDER BY w.total_logical_reads DESC) AS c
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
       CASE WHEN warn.hit IS NULL THEN 0 ELSE 1 END               AS [server_raised_warning],
       conv.[db]                                                  AS [converted.database],
       conv.[sch]                                                 AS [converted.schema],
       conv.[tbl]                                                 AS [converted.table],
       conv.[col]                                                 AS [converted.column],
       conv.[to_type]                                             AS [converted.to_type],
       conv.[to_length]                                           AS [converted.to_length_bytes],
       conv.[n_cols]                                              AS [converted.columns_in_expression]
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
   question is whether any exists, not how many.

   WHICH COLUMN IS CONVERTED is read on the node already bound, never by a
   second walk of the plan: under the matched ScalarOperator, the first Convert
   that is implicit, targets nvarchar and has a table column as its operand, and
   the ColumnReference inside it. The [@Table] test is what separates the column
   from a parameter: showplan writes [@p] as a ColumnReference too, with no
   Database, Schema or Table. Measured on 17.0.4065.4 with a varchar(20) column
   compared to an nvarchar(20) parameter, the residual predicate reads

     <ScalarOperator ScalarString="CONVERT_IMPLICIT(nvarchar(20),[db].[s].[t].[Code],0)=[@p]">
       <Compare><ScalarOperator><Convert DataType="nvarchar" Length="40" Implicit="1">
         <ScalarOperator><Identifier>
           <ColumnReference Database="[db]" Schema="[s]" Table="[t]" Column="Code"/>

   Database, Schema and Table come bracketed and Column does not; PARSENAME
   strips the brackets so the names compare with what 70.schema exports.
   Length is in bytes, as the plan carries it: 40 for nvarchar(20). The
   column's declared type is not in the plan; 70.schema/060.columns has it.

   When one predicate converts several columns, the first in document order is
   projected and columns_in_expression says how many there were. Document
   order is the order of the predicate, not of the index keys.

   The reads cost something. On the lab, 17.0.4065.4, two hundred candidates
   and about a dozen rows out, the whole file went from about 4.0 s to
   between 4.7 and 5.3 s over three runs each, wall clock through sqlcmd. */
OUTER APPLY (SELECT TOP (1) 1 AS hit,
                    PARSENAME(cr.n.value('@Database', 'nvarchar(258)'), 1) AS [db],
                    PARSENAME(cr.n.value('@Schema', 'nvarchar(258)'), 1)   AS [sch],
                    PARSENAME(cr.n.value('@Table', 'nvarchar(258)'), 1)    AS [tbl],
                    cr.n.value('@Column', 'nvarchar(128)')                 AS [col],
                    cv.n.value('@DataType', 'nvarchar(128)')               AS [to_type],
                    cv.n.value('@Length', 'int')                           AS [to_length],
                    so.n.value('count(.//Convert[@Implicit="1" or @Implicit="true"][substring(@DataType,1,8)="nvarchar"][ScalarOperator/Identifier/ColumnReference/@Table])', 'int') AS [n_cols]
             FROM a.[px].nodes('//Predicate//ScalarOperator[contains(@ScalarString,"CONVERT_IMPLICIT(nvarchar")]') AS so(n)
             OUTER APPLY so.n.nodes('(.//Convert[@Implicit="1" or @Implicit="true"][substring(@DataType,1,8)="nvarchar"][ScalarOperator/Identifier/ColumnReference/@Table])[1]') AS cv(n)
             OUTER APPLY cv.n.nodes('ScalarOperator/Identifier/ColumnReference') AS cr(n)) AS pred
/* The second place a conversion can sit. Until 4 October 2026 this comment
   said that under a Windows collation the engine, having achieved a range seek
   across the conversion, puts the conversion under RangeExpressions instead
   of a residual Predicate, and that this was reasoned, not measured. Measured
   since, on 17.0.4065.4, with a varchar column under Latin1_General_CI_AS
   indexed and compared to an nvarchar parameter: the reasoning was wrong about
   where the conversion goes. The plan is an Index Seek whose StartRange and
   EndRange RangeExpressions read [Expr1007] and [Expr1008], computed upstream
   by a Compute Scalar as GetRangeThroughConvert(CONVERT_IMPLICIT(nvarchar(20),
   [@p],0), ...), which converts the parameter, and the seek keeps the column
   conversion as its residual Predicate. That form is therefore found by the
   Predicate clause above, column included, and not by this one.

   What this clause did match on the lab is the harmless direction: an
   nvarchar column sought with a varchar parameter puts
   CONVERT_IMPLICIT(nvarchar(20),[@p],0) in the seek key. Such a row has a
   NULL converted.column, because the operand is a parameter, and that NULL is
   how the analysis can tell it apart. The clause is kept, not removed, so that
   what is detected does not change in the same commit as what is projected;
   whether it ever finds a column conversion is not measured. */
OUTER APPLY (SELECT TOP (1) 1 AS hit,
                    PARSENAME(cr.n.value('@Database', 'nvarchar(258)'), 1) AS [db],
                    PARSENAME(cr.n.value('@Schema', 'nvarchar(258)'), 1)   AS [sch],
                    PARSENAME(cr.n.value('@Table', 'nvarchar(258)'), 1)    AS [tbl],
                    cr.n.value('@Column', 'nvarchar(128)')                 AS [col],
                    cv.n.value('@DataType', 'nvarchar(128)')               AS [to_type],
                    cv.n.value('@Length', 'int')                           AS [to_length],
                    so.n.value('count(.//Convert[@Implicit="1" or @Implicit="true"][substring(@DataType,1,8)="nvarchar"][ScalarOperator/Identifier/ColumnReference/@Table])', 'int') AS [n_cols]
             FROM a.[px].nodes('//SeekPredicates//ScalarOperator[contains(@ScalarString,"CONVERT_IMPLICIT(nvarchar")]') AS so(n)
             OUTER APPLY so.n.nodes('(.//Convert[@Implicit="1" or @Implicit="true"][substring(@DataType,1,8)="nvarchar"][ScalarOperator/Identifier/ColumnReference/@Table])[1]') AS cv(n)
             OUTER APPLY cv.n.nodes('ScalarOperator/Identifier/ColumnReference') AS cr(n)) AS seek
/* The predicate's column if it named one, else the seek key's. */
OUTER APPLY (SELECT TOP (1) x.[db], x.[sch], x.[tbl], x.[col], x.[to_type], x.[to_length], x.[n_cols]
             FROM (SELECT 1 AS o, pred.[db], pred.[sch], pred.[tbl], pred.[col],
                          pred.[to_type], pred.[to_length], pred.[n_cols]
                   UNION ALL
                   SELECT 2, seek.[db], seek.[sch], seek.[tbl], seek.[col],
                          seek.[to_type], seek.[to_length], seek.[n_cols]) AS x
             WHERE x.[col] IS NOT NULL
             ORDER BY x.o) AS conv
OUTER APPLY (SELECT TOP (1) 1 AS hit
             FROM a.[px].nodes('//Warnings/PlanAffectingConvert') AS w(n)) AS warn
WHERE a.[px] IS NOT NULL
  AND (pred.hit IS NOT NULL OR seek.hit IS NOT NULL)
ORDER BY a.[total_logical_reads] DESC
OPTION (RECOMPILE, MAXDOP 1);
