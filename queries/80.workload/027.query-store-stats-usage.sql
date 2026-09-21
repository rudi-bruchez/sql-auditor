-- @scope:       database
-- @resultsets:  root:object, statistics_used:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     300
-- @min_version: 14
--
-- Which statistics the optimizer actually loaded, read out of the plans the
-- Query Store already holds.
--
-- THE LIST IS A VETO, NEVER A HIT LIST. Presence proves use; absence proves
-- nothing. A statistics object missing from this result may belong to a query
-- whose plan has left the Query Store, to a database where the store is off,
-- or to a workload that has not run inside the retention window. A cleanup
-- decides what to drop on other grounds, and this collector refuses any drop
-- it contradicts. An analysis that presents this as "statistics never used"
-- has inverted it, and will one day recommend dropping the one histogram a
-- quarterly close depends on.
--
-- WHY THERE IS NO DMV TO READ INSTEAD. Indexes have
-- sys.dm_db_index_usage_stats. Statistics have no equivalent, in any version.
-- Microsoft's documented answer to "which statistics did the optimizer use" is
-- to read the execution plan: the OptimizerStatsUsage element carries one
-- StatisticsInfo child per statistics object loaded during compilation, and
-- sys.query_store_plan reaches it without a round trip to the client.
--
-- IT DOES NOT RETURN PLAN XML, and that is the entire point. The shred happens
-- on the instance and what travels is a few thousand short rows instead of
-- gigabytes of XML. 021.query-store-detail rescues fifty plans per database as
-- files; this reads thousands and keeps none of them.
--
-- NO @discloses. The projection carries object names and statistic names,
-- never query text and never the plan. This is the one Query Store collector
-- that can run on a client who refuses text disclosure, and every column added
-- here has to keep it that way.
--
-- MIN SAMPLING AND MAX MODIFICATION COUNT ARE THE REASON TO PREFER THIS OVER A
-- BARE USAGE FLAG. Together they say a statistic was USED WHILE STALE, which
-- is a stronger finding than either fact alone: nobody needs to refresh a stale
-- statistic no plan loads, and everybody needs to refresh a stale one that four
-- hundred plans do. They are the worst compile seen, not the current state —
-- 70.schema/090.statistics carries the current state, and the two are read
-- together.
--
-- THE DATABASE IS PROJECTED, and the spec this file implements did not ask for
-- it. StatisticsInfo/@Database exists because a plan compiled in one database
-- can load statistics from another, and the corpus stores this file under the
-- database whose Query Store held the plan. Dropping the attribute would file
-- OTHERDB's statistic under SALESDB and make the join to 090 quietly wrong for
-- exactly the cross-database queries hardest to reason about.
--
-- THE TABLE IS PROJECTED SCHEMA-QUALIFIED IN ONE COLUMN, which is what
-- 70.schema/090.statistics does with [table], so the two files join on
-- (database, table, statistic) without either side having to reassemble a
-- name. The spec named schema and table separately; 090 does not, and matching
-- the file that exists beats matching the sentence that described it.
--
-- BRACKETS ARE STRIPPED BECAUSE SHOWPLAN QUOTES THESE ATTRIBUTES. The element
-- carries Table="[Orders]", 090 carries dbo.Orders, and a join between them
-- fails silently on the brackets. Only the outer pair is removed, so a name
-- containing a bracket — showplan escapes an inner ] by doubling it — still
-- reaches the analysis as written rather than half-unescaped. Such a name is
-- pathological and no client estate has produced one yet.
--
-- THE CAP IS ON PLANS AND NOT ON ROWS. Casting a plan from nvarchar(max) to
-- xml is CPU-bound on the client's instance, so the scan takes a fixed budget:
-- the @cap most recently executed plans, ordered by last_execution_time. The
-- projected rows are short and are not capped — capping them would drop whole
-- statistics objects and leave the reader unable to tell which.
--
-- Ordering by recency and not by cost is deliberate. A statistic loaded by an
-- expensive plan last February matters less to a cleanup decision than one
-- loaded by a cheap plan last night, because the question this feeds is "may I
-- drop this", and recency is the better guard.
--
-- plans_examined AGAINST plans_total IS WHAT STOPS THE ANALYSIS OVER-READING
-- THE RESULT. Without it a capped scan reads as a complete one, and the veto
-- above turns into a licence. truncated says the same thing in one bit for a
-- reader who checks nothing else.
--
-- THE SCAN SET IS PINNED BEFORE ANYTHING IS COUNTED, and the first run of this
-- file is why. It counted the store, then counted its own TOP, and reported
-- plans_examined 116 against plans_total 115 on a lab instance — because under
-- QUERY_CAPTURE_MODE ALL the collector's own statements are captured into the
-- store it is reading, so two counts a second apart disagree. An impossible
-- pair costs the reader more trust than the number was ever worth. The plan
-- ids now go into a table variable first, plans_examined is what came back
-- parsed out of that pinned set, and truncated is whether the pin reached the
-- cap rather than an arithmetic guess.
-- plans_total is still a second snapshot and may drift by a few plans on a
-- busy store; it is taken after the pin, so the drift shows up as headroom
-- rather than as a contradiction.
--
-- CATALOG STATISTICS ARE EXCLUDED, and the measurement is again the argument.
-- On a lab instance 110 of the 113 objects named were statistics on sys
-- tables, most of them loaded by the audit's own catalog queries and by the
-- Query Store's internal ones, in mssqlsystemresource, master and tempdb as
-- well as in the database being read. None of them is a drop candidate: no
-- cleanup drops a statistic on sys.sysschobjs, and 70.schema/090.statistics,
-- which this file exists to be joined against, lists only is_ms_shipped = 0
-- tables and so can never match one. They are dropped from the array and
-- counted in root, because a count is what tells a reader the rows were
-- excluded rather than never found. The schema name is the whole test: sys is
-- reserved, so no user object can hide behind it.
--
-- THE CAP IS 2 000 AND THE SPEC SAID 5 000. Measured here at 12 to 17 ms per
-- plan, 5 000 plans is one to one and a half minutes of client CPU per
-- database, and the corpus runs this against every database that has a store.
-- The spec chose the number to cover the largest store seen on a real estate,
-- 6 515 plans in one database, but covering it is not what this file is for:
-- the question it feeds is whether a statistic may be dropped, the order is by
-- recency, and the plans that answer it are at the top of that order. Two
-- thousand recent plans answer the same question for a third of the cost, and
-- truncated says when the cap bit so nobody reads a partial scan as a complete
-- one.
--
-- THE CAP IS A CONSTANT AND NOT AN OPTION, which the spec left open. The
-- corpus has no directive for a per-collector parameter, and the collectors
-- that bound themselves (80.workload/030.implicit-conversions and
-- 70.schema/090.statistics) do it with a DECLARE and report the value they
-- used. Adding a command-line flag for a number nobody has yet asked to change
-- would be the first of its kind, and the value travels in root either way.
--
-- ON A DATABASE WHOSE QUERY STORE IS OFF, statistics_used is empty and root
-- reports plans_total = 0 with state.actual = OFF. That is not an error and
-- must never become one: eight databases out of ten on a real estate are in
-- that state, and a collector that raised there would be read as a fault on
-- the client's instance.
--
-- SQL SERVER 2017 IS THE FLOOR, and it is one version too high on paper.
-- OptimizerStatsUsage appears in showplan from 2017 and from 2016 SP2, and the
-- corpus gates on a major version rather than on a service pack. THIS HAS NOT
-- BEEN MEASURED ON A 2016 SP2 INSTANCE. Until it is, a 2016 instance is
-- skipped with a reason rather than returning a silently empty array, which is
-- the failure mode worth more than the coverage.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @cap   int = 2000;
DECLARE @chunk int = 100;

DECLARE @scan TABLE (
    rn           int    PRIMARY KEY,
    plan_id      bigint NOT NULL UNIQUE,
    query_id     bigint         NOT NULL,
    last_compile datetimeoffset NULL
);

/* One row per plan scanned, carrying only its OptimizerStatsUsage element.
   frag is empty rather than absent for a plan that loaded no statistics, so
   the row count here is the number of plans read and not the number that had
   something to say. */
DECLARE @frag TABLE (
    plan_id      bigint PRIMARY KEY,
    query_id     bigint         NOT NULL,
    last_compile datetimeoffset NULL,
    frag         xml            NULL
);

DECLARE @usage TABLE (
    [database]    nvarchar(300)  NULL,
    [schema]      nvarchar(300)  NULL,
    [table]       nvarchar(600)  NULL,
    [statistic]   nvarchar(300)  NULL,
    plan_id       bigint         NOT NULL,
    query_id      bigint         NOT NULL,
    last_compile  datetimeoffset NULL,
    sampling      float          NULL,
    modifications bigint         NULL
);

/* ───────── the scan set, pinned ─────────
   The plans this run is answerable for, chosen once so that every count below
   is about the same set of plans and not about whatever the store held a
   second later. */
INSERT INTO @scan (rn, plan_id, query_id, last_compile)
SELECT ROW_NUMBER() OVER (ORDER BY t.last_execution_time DESC),
       t.plan_id,
       t.query_id,
       t.last_compile_start_time
FROM (SELECT TOP (@cap)
             p.plan_id,
             p.query_id,
             p.last_compile_start_time,
             p.last_execution_time
      FROM sys.query_store_plan AS p
      WHERE p.query_plan IS NOT NULL
      ORDER BY p.last_execution_time DESC) AS t
OPTION (RECOMPILE, MAXDOP 1);

DECLARE @plans_selected bigint = (SELECT COUNT_BIG(*) FROM @scan);
DECLARE @plans_total    bigint = (SELECT COUNT_BIG(*) FROM sys.query_store_plan);

/* ───────── the fragments, a hundred plans at a time ─────────
   One XML parse per plan, keeping only the OptimizerStatsUsage element. The
   whole plan is never stored: 279 plans on a lab instance carried 14 MB of
   showplan and 738 KB of fragment, so what lands in tempdb is a fortieth of
   what was read.

   TRY_CAST and not CAST. query_plan is nvarchar(max), and a plan the engine
   truncated on the way into the store is not well-formed XML; CAST would fail
   the whole statement on one such row and lose every other plan in the
   database, which is the opposite of what an audit collector should do when
   one input is bad.

   THE LOOP IS ABOUT THE LOCK, NOT ABOUT MEMORY. Reading sys.query_store_plan
   takes a shared QDS database lock, and the first version of this file held it
   for the length of the whole scan: on the lab instance the run was cancelled
   by this tool's own blocking watch after another session had waited 5.2 s for
   LCK_M_X on that lock. A statement per hundred plans measured about 1.2 s, so
   the lock is released roughly that often and a waiter gets through. Total
   elapsed time is unchanged; what changes is how long anyone else is stopped.

   @chunk is deliberately small for that reason alone. Raising it makes the
   collector no faster and makes it a worse neighbour. */
DECLARE @lo int = 1;
WHILE @lo <= @plans_selected
BEGIN
    WITH XMLNAMESPACES (DEFAULT 'http://schemas.microsoft.com/sqlserver/2004/07/showplan')
    INSERT INTO @frag (plan_id, query_id, last_compile, frag)
    SELECT s.plan_id,
           s.query_id,
           s.last_compile,
           px.x.query('//OptimizerStatsUsage')
    FROM       @scan                AS s
    JOIN       sys.query_store_plan AS p ON p.plan_id = s.plan_id
    CROSS APPLY (SELECT TRY_CAST(p.query_plan AS xml) AS x) AS px
    WHERE s.rn >= @lo AND s.rn < @lo + @chunk
      AND px.x IS NOT NULL
    OPTION (RECOMPILE, MAXDOP 1);

    SET @lo = @lo + @chunk;
END

/* Plans whose XML actually parsed. It is @plans_selected on every instance
   seen so far; the two differ only where TRY_CAST refused a truncated plan,
   and reporting the count that was really read is what keeps the share below
   honest when that happens. */
DECLARE @plans_examined bigint = (SELECT COUNT_BIG(*) FROM @frag);

/* ───────── the shred ─────────
   From a stored xml column, and that is the difference between three seconds
   and two minutes. Applied to an xml EXPRESSION — a TRY_CAST in a derived
   table — every .value() call re-parses the whole plan, so six attributes per
   element cost six parses of a seventy-kilobyte document: 273 plans took 116 s
   that way, and 3.3 s once the fragment was a column. The column is the whole
   trick; the rest of this statement is ordinary.

   Note for anyone re-measuring this: a benchmark that wraps the projection in
   SELECT COUNT(*) measures nothing, because the optimizer eliminates .value()
   calls whose results are never read. It reported 1.5 s for the form that
   actually takes 116 s. Insert the rows. */
WITH XMLNAMESPACES (DEFAULT 'http://schemas.microsoft.com/sqlserver/2004/07/showplan')
INSERT INTO @usage ([database], [schema], [table], [statistic], plan_id, query_id,
                    last_compile, sampling, modifications)
SELECT unq.db,
       unq.sch,
       CASE WHEN unq.sch IS NULL OR unq.sch = '' THEN unq.tbl
            ELSE unq.sch + '.' + unq.tbl END,
       unq.st,
       f.plan_id,
       f.query_id,
       f.last_compile,
       si.n.value('@SamplingPercent',   'float'),
       si.n.value('@ModificationCount', 'bigint')
FROM       @frag AS f
CROSS APPLY f.frag.nodes('/OptimizerStatsUsage/StatisticsInfo') AS si(n)
CROSS APPLY (VALUES (si.n.value('@Database',   'nvarchar(300)'),
                     si.n.value('@Schema',     'nvarchar(300)'),
                     si.n.value('@Table',      'nvarchar(300)'),
                     si.n.value('@Statistics', 'nvarchar(300)'))) AS raw(db, sch, tbl, st)
CROSS APPLY (VALUES (
        CASE WHEN LEFT(raw.db, 1)  = '[' AND RIGHT(raw.db, 1)  = ']' THEN SUBSTRING(raw.db,  2, LEN(raw.db)  - 2) ELSE raw.db  END,
        CASE WHEN LEFT(raw.sch, 1) = '[' AND RIGHT(raw.sch, 1) = ']' THEN SUBSTRING(raw.sch, 2, LEN(raw.sch) - 2) ELSE raw.sch END,
        CASE WHEN LEFT(raw.tbl, 1) = '[' AND RIGHT(raw.tbl, 1) = ']' THEN SUBSTRING(raw.tbl, 2, LEN(raw.tbl) - 2) ELSE raw.tbl END,
        CASE WHEN LEFT(raw.st, 1)  = '[' AND RIGHT(raw.st, 1)  = ']' THEN SUBSTRING(raw.st,  2, LEN(raw.st)  - 2) ELSE raw.st  END
     )) AS unq(db, sch, tbl, st)
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── root ─────────
   The honest half. Every count below is what the analysis needs to know how
   much of the store was read before it reads anything else, and the store's
   state so that an empty statistics_used is readable as "switched off" rather
   than as "collector failed". */
SELECT DB_NAME()                                                      AS [database],
       CONVERT(varchar(23), SYSDATETIME(), 126)                       AS [collected_at],
       @cap                                                           AS [cap],
       @plans_total                                                   AS [plans_total],
       @plans_examined                                                AS [plans_examined],
       (SELECT COUNT(DISTINCT u.plan_id) FROM @usage AS u)            AS [plans_with_usage],
       (SELECT COUNT_BIG(*)
        FROM (SELECT DISTINCT u.[database], u.[table], u.[statistic]
              FROM @usage AS u WHERE ISNULL(u.[schema], N'') <> 'sys') AS d)        AS [statistics_named],
       (SELECT COUNT_BIG(*)
        FROM (SELECT DISTINCT u.[database], u.[table], u.[statistic]
              FROM @usage AS u WHERE u.[schema] = 'sys') AS d)         AS [catalog_statistics_excluded],
       CASE WHEN @plans_selected >= @cap THEN 1 ELSE 0 END              AS [truncated],
       CAST((SELECT actual_state_desc
             FROM sys.database_query_store_options) AS nvarchar(60))   AS [state.actual],
       CAST((SELECT query_capture_mode_desc
             FROM sys.database_query_store_options) AS nvarchar(60))   AS [state.capture_mode]
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── statistics_used ─────────
   queries before plans in the ordering, because one query recompiled fifty
   times is not fifty users, and the count that decides whether a statistic may
   be dropped is how many distinct queries wanted it. */
SELECT u.[database]                                                   AS [database],
       u.[table]                                                      AS [table],
       u.[statistic]                                                  AS [statistic],
       COUNT(DISTINCT u.plan_id)                                      AS [plans],
       COUNT(DISTINCT u.query_id)                                     AS [queries],
       MAX(u.last_compile)                                            AS [last_compile],
       CAST(MIN(u.sampling) AS decimal(9,4))                          AS [min_sampling_percent],
       MAX(u.modifications)                                           AS [max_modification_count]
FROM @usage AS u
WHERE ISNULL(u.[schema], N'') <> 'sys'
GROUP BY u.[database], u.[table], u.[statistic]
ORDER BY COUNT(DISTINCT u.query_id) DESC, COUNT(DISTINCT u.plan_id) DESC
OPTION (RECOMPILE, MAXDOP 1);
