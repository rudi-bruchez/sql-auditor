-- @scope:       database
-- @resultsets:  root:object, density:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @min_version: 13.0.4422
-- @profiles:    space
-- @timeout:     300
--
-- How selective each statistic's leading column is, so that an index key order
-- can be argued from the archive instead of guessed.
--
-- 090.statistics.sql projects freshness — last_updated, rows_sampled,
-- modifications_since — and nothing about distribution. Consolidating the
-- optimizer's missing-index suggestions produces candidates with several
-- equality columns, and the order of those columns is the one decision that
-- matters most in an index: most selective first. The archive could not settle
-- it. On the busiest table of an audited subscriber, all three consolidated
-- candidates carried an undecided key order and the report had to recommend
-- the index without being able to fix its leading column.
--
-- WHY THIS IS NOT all_density, WHICH IS WHAT THE QUESTION REALLY WANTS.
-- sys.dm_db_stats_properties does not return the density vector: it carries
-- rows, rows_sampled, steps, unfiltered_rows and the modification counter, and
-- none of those describes distribution. The vector itself comes from
-- DBCC SHOW_STATISTICS ... WITH DENSITY_VECTOR, which takes a table and a
-- statistic by name — one call per statistic, assembled from variables. This
-- corpus cannot issue that: collect/statementlint.go refuses dynamic SQL built
-- out of variables, because a statement the lint cannot read is one it cannot
-- vouch for, and that refusal is what lets the manifest promise the collector
-- only reads. Weakening it to reach a density vector would be a poor trade.
--
-- So the histogram is used instead, and the estimate it yields is stated as an
-- estimate. For the LEADING column of each statistic:
--
--   rows      = SUM(range_rows) + SUM(equal_rows)
--   distinct  = SUM(distinct_range_rows) + COUNT(*)
--   density   = 1 / distinct
--
-- The COUNT(*) term is not padding: each step contributes its own
-- range_high_key as one more distinct value, over and above the distinct values
-- counted inside its range.
--
-- FOUR LIMITS, AND THEY BELONG IN THE OUTPUT RATHER THAN IN A FOOTNOTE.
--
--   The histogram describes the FIRST column of the statistic and no other. So
--   this file answers "how selective is column X" only where some statistic
--   leads on X, and leading_column is what says which one that is. For a
--   multi-column candidate, the columns are compared through the single-column
--   statistics the optimiser auto-created — which is usually exactly what
--   exists.
--
--   leading_column is QUOTENAME'd, for the reason 070.index-columns.sql now
--   quotes its key list: the ordering rule joins this name to the bracketed
--   list sys.dm_db_missing_index_details returns, and quoting one side and not
--   the other fixes half a join. QUOTENAME of a sysname runs to 258
--   characters, which is why the holding column is nvarchar(258) and not
--   sysname; sysname would silently truncate a long name into a string with no
--   closing bracket.
--
--   The numbers are an estimate built on a sample. Where rows_sampled is below
--   rows in 090, the distinct count here is an estimate of an estimate, and
--   histogram_rows is projected beside it so the reader can see how far the
--   histogram's own total is from the table.
--
--   The histogram caps at 200 steps, so a column with more distinct values than
--   that has them estimated from distinct_range_rows rather than counted. The
--   estimate degrades gracefully and it does not become a different kind of
--   number, but a density from 200 steps on a billion rows is a direction and
--   not a measurement.
--
--   Filtered statistics describe a subset. has_filter is projected so a density
--   computed over one is not read as a density over the table.
--
-- EVERY STATISTIC ON EVERY LISTED TABLE IS READ, the tables of the shared
-- selection of 70.schema: the user tables by rows, largest first, object_id
-- breaking ties, for as long as their columns add up to 100 000 at most and
-- their statistics to 70 000 at most. The same statement fills #listed_tables
-- in 010.objects.sql, 060.columns.sql and 090.statistics.sql, and a test keeps
-- it identical in all four, so a table found in one of them is here too.
-- Until October 2026 this file read the 200 tables with the most rows, and
-- its header named the condition for lifting that cap: audits meeting
-- missing-index candidates on tables outside the largest 200. The condition
-- was met: on one real database 51 of 206 missing-index suggestions sat on
-- such tables, and of three real collections that carried this file one had
-- the cap bind, at 1 109 of 1 674 statistics (docs/caps-inventory.md). Each
-- row is already the short form, one statistic, its leading column and two
-- numbers, 298 bytes measured on the lab, so there is no lighter list to fall
-- back to. At 70 000 statistics that is 21 MB, and the histogram read, about
-- 0.27 ms a statistic on the lab, some 19 s, inside the @timeout of 300 s.
-- The cap came back with 060.columns.sql, which carries the measurement: a
-- lab schema of 20 000 tables made this file 17 MB, its neighbours of
-- 70.schema 236 MB more, and the collector 2.09 GB resident.
-- counts.statistics is the number of statistics read and
-- counts.statistics_total the statistics on user tables; counts.tables_covered
-- and counts.tables_total do the same for tables. Each pair is equal unless
-- the selection cut. listing_cap.columns and listing_cap.statistics give the
-- two numbers.
--
-- NO KEY ORDER IS RECOMMENDED HERE. This file reports distribution; deciding an
-- index is the analysis step's job, with the write cost and the query shapes in
-- hand that a collector does not have.
--
-- THE FLOOR IS THE FUNCTION AND NOT THE CORPUS DEFAULT.
-- sys.dm_db_stats_histogram arrived in SQL Server 2016 SP1 CU2, build
-- 13.0.4422, so the gate is dotted rather than the bare major. That is above
-- 090.statistics.sql's own 11.0.3000 floor, which is why this is a second file
-- and not four more columns in the first: raising 090's gate to reach the
-- histogram would take statistic freshness away from every 2012 and 2014
-- instance to add distribution to none of them.
--
-- The read is deferred and staged for the reason the guarded collectors of
-- 90.availability are. The permission wording of this function is SELECT on the
-- statistics columns, or ownership, or db_ddladmin — the same wording
-- sys.dm_db_stats_properties carries, and 090 has run under an audit login on
-- client instances with it. But the two functions differ in one documented
-- respect: dm_db_stats_properties is documented to return an EMPTY ROWSET when
-- the caller lacks permission, and dm_db_stats_histogram is documented no such
-- way. An unproven refusal must not be able to cost the unit its result sets,
-- so the rows land in a table variable and both sets are emitted whatever
-- happened.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @collected bit = 1, @err int = 0, @msg nvarchar(2048) = N'';

DECLARE @density TABLE (
    [schema_name]        sysname,
    [table_name]         sysname,
    [statistic]          sysname,
    [stats_id]           int,
    [leading_column]     nvarchar(258) NULL,
    [is_auto_created]    int,
    [is_index_statistic] int,
    [has_filter]         int,
    [steps]              int    NULL,
    [histogram_rows]     float  NULL,
    [histogram_distinct] bigint NULL);

/* THE SHARED SELECTION, the same statement in 010, 060, 090 and 091, which a
   test keeps identical. It is read here, outside the deferred batch, because
   it touches nothing the histogram function adds; the deferred batch below
   joins the temporary table it fills, which a nested batch can see. */
DECLARE @listing_cap_columns int = 100000, @listing_cap_statistics int = 70000;

IF OBJECT_ID('tempdb..#listed_tables') IS NOT NULL DROP TABLE #listed_tables;
CREATE TABLE #listed_tables (object_id int NOT NULL PRIMARY KEY);

INSERT INTO #listed_tables (object_id)
SELECT r.object_id
FROM (SELECT t.object_id,
             SUM(ISNULL(c.n, 0)) OVER (ORDER BY p.row_count DESC, t.object_id
                                       ROWS UNBOUNDED PRECEDING) AS columns_running,
             SUM(ISNULL(s.n, 0)) OVER (ORDER BY p.row_count DESC, t.object_id
                                       ROWS UNBOUNDED PRECEDING) AS statistics_running
      FROM      sys.tables AS t
      LEFT JOIN (SELECT ps.object_id, SUM(ps.row_count) AS row_count
                 FROM sys.dm_db_partition_stats AS ps
                 WHERE ps.index_id IN (0, 1)
                 GROUP BY ps.object_id) AS p ON p.object_id = t.object_id
      LEFT JOIN (SELECT co.object_id, COUNT(*) AS n
                 FROM sys.columns AS co
                 GROUP BY co.object_id) AS c ON c.object_id = t.object_id
      LEFT JOIN (SELECT st.object_id, COUNT(*) AS n
                 FROM sys.stats AS st
                 GROUP BY st.object_id) AS s ON s.object_id = t.object_id
      WHERE t.is_ms_shipped = 0) AS r
WHERE r.columns_running    <= @listing_cap_columns
  AND r.statistics_running <= @listing_cap_statistics
OPTION (RECOMPILE, MAXDOP 1);

/* The deferred batch carries no string literal of its own, which is why the
   schema and table names are projected as two columns rather than concatenated
   the way 090 concatenates them: a literal separator would have to be doubled
   inside this one, and an escaping mistake in a 25-line statement is the kind
   of defect that only shows up on the instance. */
BEGIN TRY
    INSERT INTO @density ([schema_name], [table_name], [statistic], [stats_id],
                          [leading_column], [is_auto_created],
                          [is_index_statistic], [has_filter], [steps],
                          [histogram_rows], [histogram_distinct])
    EXEC sys.sp_executesql N'
        SELECT SCHEMA_NAME(t.schema_id), t.name, st.name, st.stats_id,
               QUOTENAME(c.name),
               CONVERT(int, st.auto_created),
               CONVERT(int, CASE WHEN i.index_id IS NULL THEN 0 ELSE 1 END),
               CONVERT(int, st.has_filter),
               h.steps, h.histogram_rows, h.histogram_distinct
        FROM      sys.stats  AS st
        JOIN      sys.tables AS t ON t.object_id = st.object_id AND t.is_ms_shipped = 0
        JOIN      #listed_tables AS lt ON lt.object_id = t.object_id
        LEFT JOIN sys.indexes AS i ON i.object_id = st.object_id
                                  AND i.index_id  = st.stats_id
        LEFT JOIN sys.stats_columns AS sc ON sc.object_id       = st.object_id
                                         AND sc.stats_id        = st.stats_id
                                         AND sc.stats_column_id = 1
        LEFT JOIN sys.columns AS c ON c.object_id = sc.object_id
                                  AND c.column_id = sc.column_id
        OUTER APPLY (
            SELECT COUNT(*)                                   AS steps,
                   SUM(hh.range_rows) + SUM(hh.equal_rows)    AS histogram_rows,
                   SUM(hh.distinct_range_rows) + COUNT(*)     AS histogram_distinct
            FROM sys.dm_db_stats_histogram(st.object_id, st.stats_id) AS hh
        ) AS h
        OPTION (RECOMPILE, MAXDOP 1)';
END TRY
BEGIN CATCH
    SELECT @collected = 0, @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT DB_NAME()                                                AS [database],
       CONVERT(varchar(23), SYSDATETIME(), 126)                 AS [collected_at],
       CONVERT(int, @collected)                                 AS [collected],
       @err                                                     AS [error_number],
       NULLIF(@msg, N'')                                        AS [error_message],
       (SELECT COUNT(*) FROM @density)                          AS [counts.statistics],
       (SELECT COUNT(*)
        FROM sys.stats  AS st
        JOIN sys.tables AS t ON t.object_id = st.object_id AND t.is_ms_shipped = 0)
                                                                AS [counts.statistics_total],
       (SELECT COUNT(*) FROM #listed_tables)                    AS [counts.tables_covered],
       (SELECT COUNT(*) FROM sys.tables AS t WHERE t.is_ms_shipped = 0)
                                                                AS [counts.tables_total],
       @listing_cap_columns                                     AS [listing_cap.columns],
       @listing_cap_statistics                                  AS [listing_cap.statistics],
       /* A statistic with no histogram has never been populated — a filtered
          statistic whose predicate matches nothing, or one on a table that has
          never held a row. Counted rather than dropped, because a file whose
          array is short says nothing about why.

          THE TEST IS ZERO AND NOT NULL, and the first version had it wrong:
          the histogram is read through an OUTER APPLY over a scalar aggregate,
          which always returns a row, so steps is 0 and never NULL. The count
          could not fire, and a statistic with no histogram still produced a
          row with a name, a leading column and a null density. An ordering
          rule that tests for the row rather than for the density orders on
          nothing. */
       (SELECT COUNT(*) FROM @density AS d WHERE d.[steps] IS NULL OR d.[steps] = 0)
                                                                AS [counts.without_histogram]
OPTION (RECOMPILE, MAXDOP 1);

SELECT d.[schema_name]                                          AS [schema],
       d.[table_name]                                           AS [table],
       d.[statistic]                                            AS [statistic],
       d.[stats_id]                                             AS [stats_id],
       d.[leading_column]                                       AS [leading_column],
       d.[is_auto_created]                                      AS [is_auto_created],
       d.[is_index_statistic]                                   AS [is_index_statistic],
       d.[has_filter]                                           AS [has_filter],
       d.[steps]                                                AS [histogram_steps],
       d.[histogram_rows]                                       AS [histogram_rows],
       d.[histogram_distinct]                                   AS [histogram_distinct_values],
       /* The two derived numbers, and they are named as estimates because that
          is what they are. Density falls as selectivity rises, which is the
          direction a key order argument runs in: lowest density leads. */
       CAST(1.0 / NULLIF(d.[histogram_distinct], 0) AS DECIMAL(19,12))
                                                                AS [all_density_estimate],
       CAST(100.0 * d.[histogram_distinct] / NULLIF(d.[histogram_rows], 0) AS DECIMAL(9,4))
                                                                AS [distinct_pct_estimate]
FROM @density AS d
ORDER BY d.[schema_name], d.[table_name], d.[statistic]
OPTION (RECOMPILE, MAXDOP 1);

DROP TABLE #listed_tables;
