-- @scope:        database
-- @resultsets:   root:object, profiled:array
-- @permissions:  CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:      600
-- @min_version:  15.0
-- @requires_flag: query_store_plan_stats
-- @writer:       query-store-profiled
--
-- The last ACTUAL execution plan — the one carrying real row counts rather
-- than estimates — for the queries 021.query-store-detail.sql selected.
--
-- THIS COLLECTOR IS BEST EFFORT, and returning nothing is an ordinary outcome
-- rather than a fault. Four conditions must all hold for a plan to come back:
--   the instance is SQL Server 2019 or later, which the version gate handles;
--   LAST_QUERY_PLAN_STATS is switched on for the database, which is off by
--     default and is a decision someone has to have taken;
--   the plan is still resident in the cache, which a restart, memory pressure
--     or a recompile ends at any moment;
--   the plan matches a Query Store plan by hash.
-- Any one of them being false produces a matched_plans of zero, which is why
-- root also reports whether the feature was even on: a database with it off
-- and a plan cache holding nothing look identical from the output otherwise.
--
-- THE MATCH IS MADE ON query_plan_hash, WHICH IS AN MD5. A collision is
-- possible, so the mode of the match is recorded — the literal 'plan_hash' —
-- rather than the match being asserted as an identity. Several plan_handle
-- values routinely share one hash, since any recompile adds another, so the
-- candidates count travels with each row: a reader who sees candidates 4 knows
-- what they are looking at, and a reader told nothing sees a certainty.
--
-- ITS @timeout IS 600 SECONDS, five times the corpus's usual ceiling, and the
-- reason is the paragraph below. Reaching into the whole instance's plan cache
-- means the cost of this collector is set by the size of that cache and not by
-- the database it was pointed at: on an instance holding twenty-seven thousand
-- plans it ran past 120 seconds and was cut off, while every other collector
-- of the same run finished. A collector nobody gets unless they ask for it
-- should not then fail for want of time, and a run that already accepted a
-- deep read has accepted the minutes that go with it. The precedent is
-- 70.schema/041.compression-savings.sql, opt-in and allowed 1800.
--
-- IT CARRIES ITS OWN FLAG rather than sharing query_store_detail, and the
-- reason is scope, not volume. It reaches through sys.dm_exec_query_stats into
-- the plan cache of the WHOLE INSTANCE; every other per-database collector in
-- the corpus sees only the database it was pointed at. The dbid filter
-- restricts what is KEPT, not what is READ, and an operator who consented to a
-- deep read of one database has not thereby consented to that.
--
-- PREPARED STATEMENTS HAVE A NULL dbid in the DMF's output, so the
-- qps.dbid = DB_ID() filter drops them silently. That is a known, accepted gap
-- rather than an oversight: a reader comparing the count here with the count
-- in 021 will find fewer plans, and this is one of the reasons why.
--
-- The dbid comes from the DMF's own output and from nowhere else.
-- sys.dm_exec_query_stats has no database column, and the obvious way to get
-- one — sys.dm_exec_sql_text — also returns the verbatim text of every batch
-- it is asked about. That is the DMV the disclosure logic watches, and reading
-- it here would flip the archive's session-text disclosure for a reason that
-- has nothing to do with session text.
--
-- IT IS INERT WITHOUT 021. The query id list arrives empty when the detail
-- collector selected nothing, CHARINDEX then matches nothing, and this file
-- writes an index saying nothing matched. That is a clean degradation and it
-- needs no coordination code on either side.
--
-- THE PLAN CACHE IS READ TWICE, once for matched_plans in root and once for
-- the rows themselves, and the cache is a live structure: a plan can age out
-- between the two statements. THE ROWS ARE AUTHORITATIVE — they are what
-- produced the files on disk — and matched_plans is the count as it stood a
-- moment earlier. The second pass exists because a count agreeing with itself
-- by construction would say nothing: it is what makes a matched_plans of zero
-- explicable rather than merely observed. A small disagreement between the two
-- is the cache moving, not a defect.
--
-- The id list is read with CHARINDEX over a comma-wrapped string and NOT with
-- STRING_SPLIT. STRING_SPLIT needs database compatibility level 130, or the
-- ALLOW_BUILTIN_TVF_IN_ALL_COMPAT_LEVELS scoped configuration, and a database
-- left at compat 110 or 120 on a modern instance is ordinary in production:
-- the version gate above would pass and the batch would still fail with
-- "'STRING_SPLIT' is not a recognized built-in function name". CHARINDEX works
-- at every compatibility level.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

/* ───────── root ─────────
   requested_queries counts QUERIES, not plans: the list holds query ids, one
   query can have several plans, and naming it requested_plans beside
   matched_plans would invite comparing two different populations. The list is
   wrapped in commas — ,11,22, — so the count is one less than the number of
   separators, and an empty list is zero rather than one.

   last_query_plan_stats is what turns a silent zero into an explicable one,
   and it costs a scalar subquery. */
SELECT DB_NAME()                                                  AS [database],
       CASE WHEN LEN(ISNULL(@qs_query_ids, '')) = 0 THEN 0
            ELSE LEN(@qs_query_ids)
                 - LEN(REPLACE(@qs_query_ids, ',', '')) - 1 END   AS [requested_queries],
       (SELECT COUNT(*)
        FROM       sys.query_store_plan  AS p
        JOIN       sys.query_store_query AS q ON q.query_id = p.query_id
        WHERE CHARINDEX(',' + CAST(q.query_id AS nvarchar(20)) + ',', @qs_query_ids) > 0
          AND EXISTS (SELECT 1
                      FROM       sys.dm_exec_query_stats AS qs
                      CROSS APPLY sys.dm_exec_query_plan_stats(qs.plan_handle) AS qps
                      WHERE qs.query_plan_hash = p.query_plan_hash
                        AND qps.dbid = DB_ID()))                  AS [matched_plans],
       (SELECT CONVERT(nvarchar(64), dsc.value)
        FROM sys.database_scoped_configurations AS dsc
        WHERE dsc.name = 'LAST_QUERY_PLAN_STATS')                 AS [last_query_plan_stats]
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── profiled ─────────
   One row per Query Store plan that matched something in the cache. The
   ranking keeps the most recently executed of the cached plans sharing the
   hash, and the count of them is projected beside it rather than discarded. */
WITH matched AS (
    SELECT p.query_id,
           p.plan_id,
           qs.last_execution_time,
           /* Converted once, here, so the guard below and the size beside it
              measure the very same string. */
           CONVERT(nvarchar(max), qps.query_plan)                 AS query_plan,
           ROW_NUMBER() OVER (PARTITION BY p.plan_id
                              ORDER BY qs.last_execution_time DESC) AS rn,
           COUNT(*) OVER (PARTITION BY p.plan_id)                 AS candidates
    FROM       sys.query_store_query    AS q
    JOIN       sys.query_store_plan     AS p  ON p.query_id = q.query_id
    JOIN       sys.dm_exec_query_stats  AS qs ON qs.query_plan_hash = p.query_plan_hash
    CROSS APPLY sys.dm_exec_query_plan_stats(qs.plan_handle) AS qps
    WHERE CHARINDEX(',' + CAST(q.query_id AS nvarchar(20)) + ',', @qs_query_ids) > 0
      /* The DMF's own dbid, and the reason the whole instance's cache is read
         to keep one database's plans. NULL for a prepared statement, which
         this predicate therefore drops. */
      AND qps.dbid = DB_ID()
)
SELECT m.query_id                                                 AS [query_id],
       m.plan_id                                                  AS [plan_id],
       /* Declared, never asserted: this names how the row was found. The N
          prefix is not decoration — without it the column is varchar, alone
          among this file's strings. */
       N'plan_hash'                                               AS [match],
       m.candidates                                               AS [candidates],
       m.last_execution_time                                      AS [last_execution_time],
       /* The same conditional projection and size column as 021: an oversized
          plan arrives NULL with its size stated, so an omission can be
          recorded instead of a plan silently going missing. The literal is the
          Go constant maxPlanBytes, and TestPlanCapIsTheSameNumberInTheCorpus
          reads this line as well as 021's to keep the three in step. */
       CASE WHEN DATALENGTH(m.query_plan) <= 8388608
            THEN m.query_plan END                                 AS [query_plan],
       DATALENGTH(m.query_plan)                                   AS [query_plan_bytes]
FROM matched AS m
WHERE m.rn = 1
ORDER BY m.query_id, m.plan_id
OPTION (RECOMPILE, MAXDOP 1);
