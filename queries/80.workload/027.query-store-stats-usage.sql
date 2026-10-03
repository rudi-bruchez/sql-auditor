-- @scope:       database
-- @resultsets:  root:object, statistics_used:array, indexes_read:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     300
-- @min_version: 14
-- @profiles:    space
--
-- Which statistics the optimizer actually loaded, and which indexes the plans
-- read, both out of the plans the Query Store already holds and in one pass
-- over them.
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
-- indexes_read IS THE SAME VETO, FOR INDEXES, and it exists because the DMV
-- indexes do have is not enough. sys.dm_db_index_usage_stats is emptied when
-- the instance restarts and when the database is closed or detached, so an
-- index read by a monthly report looks unused on the 29th day of an uptime of
-- 28 days, and the topic that proposes drops from that DMV has nothing to
-- refuse them with. A plan in the store that reads the index survives the
-- restart. The same rule holds: an index absent from indexes_read proves
-- nothing, and one present must not be dropped on the strength of the usage
-- DMV alone. forced_plans is the hardest form of it: a forced plan that reads
-- an index stops being forceable when the index is dropped.
--
-- WHAT COUNTS AS READ is //RelOp/IndexScan/Object, never //Object. Every index
-- an INSERT, UPDATE, DELETE or MERGE maintains is named as an Object child of
-- the plan's Update element, and so is the index a CREATE INDEX builds; read
-- through //Object, an index whose only activity is being kept up to date
-- would veto its own drop, and it is exactly the index a cleanup is after.
-- Measured on SQL Server 2025 with an index that an UPDATE maintained and no
-- query read: //Object listed it, //RelOp/IndexScan/Object
-- did not. IndexScan is the element of every index seek, index scan, key
-- lookup and columnstore scan, verified on the same build: Index Seek,
-- Clustered Index Seek, Index Scan and a nonclustered columnstore scan
-- (PhysicalOp "Index Scan", Storage "ColumnStore") all carry it. A key lookup
-- is a Clustered Index Seek whose IndexScan has Lookup="1", so it names the
-- clustered index and never a nonclustered one; the nonclustered index that
-- drove it appears as its own seek. A heap is left out by Object[@Index]: a
-- Table Scan has a TableScan element, and a RID Lookup an Object with no Index
-- attribute.
--
-- THE INDEX NAME IS THE JOIN KEY, unbracketed and unescaped as the statistic
-- name is (below), so it matches sys.indexes.name, and the column is called
-- index_name because 70.schema/020.index-usage and 070.index-columns call it
-- that: the three join on (table, index_name) without renaming anything.
-- A plan compiled before an index was renamed names the old name, and one
-- compiled before a drop and re-create under the same name names the new
-- index; the store keeps names, not index ids.
--
-- WHAT THE INDEX LIST CANNOT SEE, each a reason absence proves nothing:
-- plans evicted by the store's own cleanup, by age past
-- state.stale_threshold_days or by size when state.cleanup_mode is AUTO and
-- the store reaches its maximum; queries never captured, because under
-- QUERY_CAPTURE_MODE AUTO a query that runs rarely and cheaply is not stored
-- at all, which is the profile of the monthly report this veto is for (CUSTOM
-- has the same effect by its thresholds, NONE captures nothing new); plans
-- beyond the cap, since only the @cap most recent are read and
-- window.oldest_execution says how far back that reached, which on a busy
-- store is hours rather than the retention; and every database whose store is
-- off. It sees too much in one direction, deliberately: a plan names every
-- index it may read, including one on a branch that never ran, such as the
-- unexecuted side of an adaptive join or a startup filter. For a veto that is
-- the safe error.
--
-- THE FILE BELONGS TO THE SPACE PROFILE because of indexes_read. A space run
-- proposes to disable or drop the indexes the usage DMV calls unread, and it
-- is the run that most needs the veto; without this file in the profile, the
-- deliverable that acts on unread indexes would be the one never to see it.
-- The price is this file's scan, measured below, added to a space run.
--
-- A FORCED PLAN IS NOT SEEN BECAUSE IT IS FORCED. It is read like any other
-- plan, so one whose query has not run since the cap was reached is outside
-- the scan; forced_plans counts only the forced plans inside it.
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
-- NO @discloses. The projection carries object names, statistic names and
-- index names, never query text and never the plan. This is the one Query Store collector
-- that can run on a client who refuses QUERY TEXT disclosure, and every column
-- added here has to keep it that way.
--
-- Read that sentence narrowly, because a reviewer read it broadly and was
-- right to. It says nothing about identifiers. A database, schema, table,
-- statistic or index name can itself carry a client's name, an account reference or an
-- email address, and this file copies all four verbatim, as every schema
-- collector in the corpus does. @discloses is about query text and plans; an
-- operator who cannot transfer object names is not served by any of them.
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
-- BRACKETS ARE STRIPPED AND THE ]] ESCAPE IS UNDONE, in that order, because
-- showplan quotes these attributes. The element carries Table="[Orders]", 090
-- carries dbo.Orders, and a join between them fails silently on the brackets.
-- An inner ] is escaped by doubling, so a statistic really named
-- ST]Bracket arrives as [ST]]Bracket] and stripping the outer pair alone
-- leaves ST]]Bracket, which is not what 090 emits and does not join to it.
-- An earlier version of this file did exactly that while its header claimed
-- the opposite; an external reviewer created the object and read both files.
--
-- SCHEMA AND TABLE ARE EMITTED SEPARATELY AS WELL AS CONCATENATED, and the
-- same reviewer is why. Grouping on schema + '.' + table alone is not
-- injective: a table [c] in schema [a.b] and a table [b.c] in schema [a] both
-- render a.b.c, and the collector MERGED them into one row, reporting one
-- distinct statistic where there were two. The concatenated [table] stays,
-- because it is what 70.schema/090.statistics emits and what the join needs;
-- the grouping and the distinct counts now use the parts. 090 cannot tell the
-- two apart either, so the join remains ambiguous for such a schema; what is
-- fixed here is this file silently losing one of them, which for a veto is the
-- dangerous direction.
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
-- The index list drops the same catalog rows, and also every index in tempdb
-- or on a table variable (whose Object has no Database attribute): temporary
-- tables, the collector's own table variables among them, are not drop
-- candidates. They are counted in root as temporary_indexes_excluded.
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
-- The per-plan figure follows plan size. On a lab store of 2 600 plans
-- averaging 37 KB of nvarchar each (SQL Server 2025, 4 October 2026), the scan
-- of 2 000 took 113 s, 56 ms a plan, of which the cast to xml alone was 46 ms.
-- At that size the cap stays within the 300 s timeout; plans about two and a
-- half times larger on average would not.
--
-- THE INDEX LIST SHARES THE PASS, AND THE MEASUREMENT CHOSE THAT OVER A FILE
-- OF ITS OWN. On the same lab store, the cast is most of the cost and a second
-- file would pay it again: reading 100 plans for the statistics took 5.6 s,
-- and reading them a second time for the indexes 11.5 s in all, double. Asking
-- one .query() for both paths took 6.2 to 6.4 s, and the whole collector
-- through this tool 126 s against 113 s for the statistics alone, so the
-- index list costs about an eighth more and not a second scan.
-- Two cheaper-looking refinements were measured and refused. Splitting reads
-- into seek, scan and lookup needs an element constructor per access, which
-- cost 8.7 to 18.9 s for the same 100 plans, two to three times the scan; and
-- keeping the whole IndexScan element, which would carry the Lookup flag and
-- the seek predicates for 6.2 s, made the fragment four times larger with no
-- bound, since IndexScan holds one column reference per column read. The
-- Object element alone is a few hundred bytes. The fragments at the cap went
-- from about 2.4 MB to 4.5 MB of tempdb on that store.
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
-- the failure mode worth more than the coverage. The index list would work on
-- a 2016 store, IndexScan being much older than that; it shares the floor
-- because it shares the pass, and a 2016 instance gets neither.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @cap   int = 2000;
DECLARE @chunk int = 100;

DECLARE @scan TABLE (
    rn             int    PRIMARY KEY,
    plan_id        bigint NOT NULL UNIQUE,
    query_id       bigint         NOT NULL,
    last_compile   datetimeoffset NULL,
    last_execution datetimeoffset NULL,
    is_forced      bit            NOT NULL
);

/* One row per plan scanned, carrying only its OptimizerStatsUsage element
   and the Object element of each index access, a few hundred bytes each. frag is empty rather than
   absent for a plan that had neither, so the row count here is the number of
   plans read and not the number that had something to say. */
DECLARE @frag TABLE (
    plan_id        bigint PRIMARY KEY,
    query_id       bigint         NOT NULL,
    last_compile   datetimeoffset NULL,
    last_execution datetimeoffset NULL,
    is_forced      bit            NOT NULL,
    frag           xml            NULL
);

DECLARE @usage TABLE (
    [database]    nvarchar(300)  NULL,
    [schema]      nvarchar(300)  NULL,
    [table_name]  nvarchar(300)  NULL,
    [table]       nvarchar(600)  NULL,
    [statistic]   nvarchar(300)  NULL,
    plan_id       bigint         NOT NULL,
    query_id      bigint         NOT NULL,
    last_compile  datetimeoffset NULL,
    sampling      float          NULL,
    modifications bigint         NULL
);

DECLARE @reads TABLE (
    [database]     nvarchar(300)  NULL,
    [schema]       nvarchar(300)  NULL,
    [table_name]   nvarchar(300)  NULL,
    [table]        nvarchar(600)  NULL,
    [index]        nvarchar(300)  NULL,
    index_kind     nvarchar(60)   NULL,
    storage        nvarchar(60)   NULL,
    plan_id        bigint         NOT NULL,
    query_id       bigint         NOT NULL,
    last_compile   datetimeoffset NULL,
    last_execution datetimeoffset NULL,
    is_forced      bit            NOT NULL
);

/* ───────── the scan set, pinned ─────────
   The plans this run is answerable for, chosen once so that every count below
   is about the same set of plans and not about whatever the store held a
   second later. */
INSERT INTO @scan (rn, plan_id, query_id, last_compile, last_execution, is_forced)
SELECT ROW_NUMBER() OVER (ORDER BY t.last_execution_time DESC),
       t.plan_id,
       t.query_id,
       t.last_compile_start_time,
       t.last_execution_time,
       t.is_forced_plan
FROM (SELECT TOP (@cap + 1)
             p.plan_id,
             p.query_id,
             p.last_compile_start_time,
             p.last_execution_time,
             p.is_forced_plan
      FROM sys.query_store_plan AS p
      WHERE p.query_plan IS NOT NULL
      ORDER BY p.last_execution_time DESC) AS t
OPTION (RECOMPILE, MAXDOP 1);

/* One more than the cap was asked for, so that reaching @cap + 1 is EVIDENCE
   that a further eligible plan exists rather than an inference from equality.
   The extra row is then dropped and never read. A store holding exactly @cap
   plans reports truncated 0, which is the truth; the previous test, cap
   reached therefore truncated, called a complete scan partial. */
DECLARE @truncated bit = CASE WHEN (SELECT COUNT_BIG(*) FROM @scan) > @cap THEN 1 ELSE 0 END;
DELETE FROM @scan WHERE rn > @cap;

DECLARE @plans_selected bigint = (SELECT COUNT_BIG(*) FROM @scan);
DECLARE @plans_total    bigint = (SELECT COUNT_BIG(*) FROM sys.query_store_plan);

/* ───────── the fragments, a hundred plans at a time ─────────
   One XML parse per plan, keeping only the OptimizerStatsUsage element and
   the Object of each index read, both from the same .query() call so the plan
   is parsed once for the two lists. The whole plan is never stored: 279 plans
   on a lab instance carried 14 MB of showplan and 738 KB of fragment, a
   twentieth; a reviewer measured a sixteenth on 2 000 plans of a different
   shape. The ratio follows the workload, and what matters is the magnitude it
   settles at: about 1.5 MB of tempdb at the cap before the index reads were
   added, 4.5 MB with them on a store of larger plans, which no client
   instance notices.

   TRY_CAST and not CAST. query_plan is nvarchar(max), and a plan the engine
   truncated on the way into the store is not well-formed XML; CAST would fail
   the whole statement on one such row and lose every other plan in the
   database, which is the opposite of what an audit collector should do when
   one input is bad.

   THE LOOP IS ABOUT THE LOCK, NOT ABOUT MEMORY. Reading sys.query_store_plan
   takes a shared QDS database lock, and the first version of this file held it
   for the length of the whole scan: on the lab instance the run was cancelled
   by this tool's own blocking watch after another session had waited 5.2 s for
   LCK_M_X on that lock. A statement per hundred plans measured 1.2 s on the
   author's store and 1.7 to 1.9 s on a reviewer's, so the lock is released
   every couple of seconds and a waiter gets through; the reviewer confirmed
   that a competing QUERY_STORE option change does get in. Total elapsed time
   is unchanged; what changes is how long anyone else is stopped.

   @chunk is deliberately small for that reason alone. Raising it makes the
   collector no faster and makes it a worse neighbour. */
DECLARE @lo int = 1;
WHILE @lo <= @plans_selected
BEGIN
    WITH XMLNAMESPACES (DEFAULT 'http://schemas.microsoft.com/sqlserver/2004/07/showplan')
    INSERT INTO @frag (plan_id, query_id, last_compile, last_execution, is_forced, frag)
    SELECT s.plan_id,
           s.query_id,
           s.last_compile,
           s.last_execution,
           s.is_forced,
           px.x.query('(//OptimizerStatsUsage, //RelOp/IndexScan/Object[@Index])')
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
   From a stored xml column, and the reason is that the alternative is
   UNBOUNDED. Applied to an xml EXPRESSION, a TRY_CAST in a derived table,
   every .value() call re-parses the whole plan, so six attributes per element
   cost six parses of the entire document. The cost of this form therefore
   grows with plan size times element count; the cost of the column form grows
   with plan count alone, one parse each.

   Measured on one instance, 300 plans each way, same store:

       plans            avg plan   expression form   column form
       300 largest         62 KB           155.1 s         5.2 s
       300 smallest         6 KB             2.2 s         2.0 s

   So on small plans the two are indistinguishable and the expression form is
   occasionally ahead. That is not an argument for it. A client's workload is
   stored procedures and joins, not single-table lookups, and at 62 KB the
   expression form is thirty times slower and would pass this collector's
   declared 300 s timeout long before the cap. The column form is chosen for
   having a ceiling, not for being faster on a given day.

   An external reviewer measured the expression form ahead on a store of small
   plans and read that as contradicting the figure above. It does not; the
   figure was stated without its condition, which was the real defect.

   Note for anyone re-measuring this: a benchmark that wraps the projection in
   SELECT COUNT(*) measures nothing, because the optimizer eliminates .value()
   calls whose results are never read. It reported 1.5 s for a form that took
   116 s when the rows were actually written. Insert the rows. */
WITH XMLNAMESPACES (DEFAULT 'http://schemas.microsoft.com/sqlserver/2004/07/showplan')
INSERT INTO @usage ([database], [schema], [table_name], [table], [statistic], plan_id, query_id,
                    last_compile, sampling, modifications)
SELECT unq.db,
       unq.sch,
       unq.tbl,
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
        CASE WHEN LEFT(raw.db, 1)  = '[' AND RIGHT(raw.db, 1)  = ']' THEN REPLACE(SUBSTRING(raw.db,  2, LEN(raw.db)  - 2), ']]', ']') ELSE raw.db  END,
        CASE WHEN LEFT(raw.sch, 1) = '[' AND RIGHT(raw.sch, 1) = ']' THEN REPLACE(SUBSTRING(raw.sch, 2, LEN(raw.sch) - 2), ']]', ']') ELSE raw.sch END,
        CASE WHEN LEFT(raw.tbl, 1) = '[' AND RIGHT(raw.tbl, 1) = ']' THEN REPLACE(SUBSTRING(raw.tbl, 2, LEN(raw.tbl) - 2), ']]', ']') ELSE raw.tbl END,
        CASE WHEN LEFT(raw.st, 1)  = '[' AND RIGHT(raw.st, 1)  = ']' THEN REPLACE(SUBSTRING(raw.st,  2, LEN(raw.st)  - 2), ']]', ']') ELSE raw.st  END
     )) AS unq(db, sch, tbl, st)
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── the index reads ─────────
   Same stored column, same reason, and no second parse of any plan: the
   Object elements were kept by the one .query() call above that also kept
   OptimizerStatsUsage, so the plan was parsed once for both.

   //RelOp/IndexScan/Object and never //Object. Every index an INSERT, UPDATE,
   DELETE or MERGE maintains is named as an Object child of the Update element
   of its plan, and so is the index a CREATE INDEX builds; //Object would read
   that maintenance as use and veto the drop of an index whose only activity is
   being kept up to date, which is the very index a cleanup is looking for.
   IndexScan is the element of every index seek, index scan, key lookup and
   columnstore scan: verified on SQL Server 2025 plans, where an Index Seek, a
   Clustered Index Seek, an Index Scan, a Clustered Index Scan and a
   nonclustered columnstore scan (PhysicalOp "Index Scan", Storage
   "ColumnStore") all carry it. A key lookup is a Clustered Index Seek whose
   IndexScan has Lookup="1", so it names the CLUSTERED index, never a
   nonclustered one; the nonclustered index that drove it shows as its own
   seek. Object[@Index] leaves out the heap: a Table Scan has a TableScan
   element and a RID Lookup an Object with no Index attribute, and a heap is
   not an index anyone drops. */
WITH XMLNAMESPACES (DEFAULT 'http://schemas.microsoft.com/sqlserver/2004/07/showplan')
INSERT INTO @reads ([database], [schema], [table_name], [table], [index], index_kind, storage,
                    plan_id, query_id, last_compile, last_execution, is_forced)
SELECT unq.db,
       unq.sch,
       unq.tbl,
       CASE WHEN unq.sch IS NULL OR unq.sch = '' THEN unq.tbl
            ELSE unq.sch + '.' + unq.tbl END,
       unq.ix,
       ir.n.value('@IndexKind', 'nvarchar(60)'),
       ir.n.value('@Storage',   'nvarchar(60)'),
       f.plan_id,
       f.query_id,
       f.last_compile,
       f.last_execution,
       f.is_forced
FROM       @frag AS f
CROSS APPLY f.frag.nodes('/Object') AS ir(n)
CROSS APPLY (VALUES (ir.n.value('@Database', 'nvarchar(300)'),
                     ir.n.value('@Schema',   'nvarchar(300)'),
                     ir.n.value('@Table',    'nvarchar(300)'),
                     ir.n.value('@Index',    'nvarchar(300)'))) AS raw(db, sch, tbl, ix)
CROSS APPLY (VALUES (
        CASE WHEN LEFT(raw.db, 1)  = '[' AND RIGHT(raw.db, 1)  = ']' THEN REPLACE(SUBSTRING(raw.db,  2, LEN(raw.db)  - 2), ']]', ']') ELSE raw.db  END,
        CASE WHEN LEFT(raw.sch, 1) = '[' AND RIGHT(raw.sch, 1) = ']' THEN REPLACE(SUBSTRING(raw.sch, 2, LEN(raw.sch) - 2), ']]', ']') ELSE raw.sch END,
        CASE WHEN LEFT(raw.tbl, 1) = '[' AND RIGHT(raw.tbl, 1) = ']' THEN REPLACE(SUBSTRING(raw.tbl, 2, LEN(raw.tbl) - 2), ']]', ']') ELSE raw.tbl END,
        CASE WHEN LEFT(raw.ix, 1)  = '[' AND RIGHT(raw.ix, 1)  = ']' THEN REPLACE(SUBSTRING(raw.ix,  2, LEN(raw.ix)  - 2), ']]', ']') ELSE raw.ix  END
     )) AS unq(db, sch, tbl, ix)
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
       @plans_selected                                                AS [plans_selected],
       @plans_examined                                                AS [plans_examined],
       @plans_selected - @plans_examined                              AS [plans_unparsed],
       (SELECT COUNT(DISTINCT u.plan_id) FROM @usage AS u)            AS [plans_with_usage],
       (SELECT COUNT_BIG(*)
        FROM (SELECT DISTINCT u.[database], u.[schema], u.[table_name], u.[statistic]
              FROM @usage AS u WHERE ISNULL(u.[schema], N'') <> 'sys') AS d)        AS [statistics_named],
       (SELECT COUNT_BIG(*)
        FROM (SELECT DISTINCT u.[database], u.[schema], u.[table_name], u.[statistic]
              FROM @usage AS u WHERE u.[schema] = 'sys') AS d)         AS [catalog_statistics_excluded],
       (SELECT COUNT(DISTINCT r.plan_id) FROM @reads AS r)            AS [plans_with_index_reads],
       (SELECT COUNT_BIG(*)
        FROM (SELECT DISTINCT r.[database], r.[schema], r.[table_name], r.[index]
              FROM @reads AS r
              WHERE ISNULL(r.[schema], N'') <> 'sys'
                AND ISNULL(r.[database], N'tempdb') <> 'tempdb') AS d)  AS [indexes_named],
       (SELECT COUNT_BIG(*)
        FROM (SELECT DISTINCT r.[database], r.[schema], r.[table_name], r.[index]
              FROM @reads AS r WHERE r.[schema] = 'sys') AS d)         AS [catalog_indexes_excluded],
       (SELECT COUNT_BIG(*)
        FROM (SELECT DISTINCT r.[database], r.[schema], r.[table_name], r.[index]
              FROM @reads AS r
              WHERE ISNULL(r.[schema], N'') <> 'sys'
                AND ISNULL(r.[database], N'tempdb') = 'tempdb') AS d)  AS [temporary_indexes_excluded],
       CAST(@truncated AS int)                                        AS [truncated],
       (SELECT MIN(s.last_execution) FROM @scan AS s)                 AS [window.oldest_execution],
       (SELECT MAX(s.last_execution) FROM @scan AS s)                 AS [window.newest_execution],
       CAST((SELECT actual_state_desc
             FROM sys.database_query_store_options) AS nvarchar(60))   AS [state.actual],
       CAST((SELECT query_capture_mode_desc
             FROM sys.database_query_store_options) AS nvarchar(60))   AS [state.capture_mode],
       (SELECT stale_query_threshold_days
        FROM sys.database_query_store_options)                        AS [state.stale_threshold_days],
       CAST((SELECT size_based_cleanup_mode_desc
             FROM sys.database_query_store_options) AS nvarchar(60))   AS [state.cleanup_mode]
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── statistics_used ─────────
   queries before plans in the ordering, because one query recompiled fifty
   times is not fifty users, and the count that decides whether a statistic may
   be dropped is how many distinct queries wanted it. */
SELECT u.[database]                                                   AS [database],
       u.[schema]                                                     AS [schema],
       u.[table]                                                      AS [table],
       u.[statistic]                                                  AS [statistic],
       COUNT(DISTINCT u.plan_id)                                      AS [plans],
       COUNT(DISTINCT u.query_id)                                     AS [queries],
       MAX(u.last_compile)                                            AS [last_compile],
       CAST(MIN(u.sampling) AS decimal(9,4))                          AS [min_sampling_percent],
       MAX(u.modifications)                                           AS [max_modification_count]
FROM @usage AS u
WHERE ISNULL(u.[schema], N'') <> 'sys'
GROUP BY u.[database], u.[schema], u.[table_name], u.[table], u.[statistic]
ORDER BY COUNT(DISTINCT u.query_id) DESC, COUNT(DISTINCT u.plan_id) DESC
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── indexes_read ─────────
   The same veto as statistics_used, for indexes, and the same ordering for
   the same reason. forced_plans is the strongest form of the veto: a forced
   plan names the index it requires, and dropping that index makes the forcing
   fail. */
SELECT r.[database]                                                   AS [database],
       r.[schema]                                                     AS [schema],
       r.[table]                                                      AS [table],
       r.[index]                                                      AS [index_name],
       MAX(r.index_kind)                                              AS [index_kind],
       MAX(r.storage)                                                 AS [storage],
       COUNT(DISTINCT r.plan_id)                                      AS [plans],
       COUNT(DISTINCT r.query_id)                                     AS [queries],
       COUNT(DISTINCT CASE WHEN r.is_forced = 1     THEN r.plan_id END) AS [forced_plans],
       MAX(r.last_execution)                                          AS [last_execution],
       MAX(r.last_compile)                                            AS [last_compile]
FROM @reads AS r
WHERE ISNULL(r.[schema], N'') <> 'sys'
  AND ISNULL(r.[database], N'tempdb') <> 'tempdb'
GROUP BY r.[database], r.[schema], r.[table_name], r.[table], r.[index]
ORDER BY COUNT(DISTINCT r.query_id) DESC, COUNT(DISTINCT r.plan_id) DESC
OPTION (RECOMPILE, MAXDOP 1);
