-- @scope:       database
-- @resultsets:  root:object, statistics:array, statistics_all:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @min_version: 11.0.3000
-- @timeout:     180
--
-- Every statistic in the database, in two widths: a short row for each one,
-- and the full detail for those on the largest tables. When it was last
-- updated, on how many rows, sampled how far, and how many rows have changed
-- since.
--
-- Why this collector exists. Nothing in this archive could say when the
-- optimiser's numbers were last refreshed. An estimate of one row against an
-- actual of four million is the single most common reason a plan is bad, and
-- the cause is almost always here — a statistic last updated before the table
-- tripled, or one sampled at 0.4% on a billion-row table. The plans in
-- 80.workload show the symptom; this file is where the cause is legible.
--
-- ROWS AND ROWS_SAMPLED SIDE BY SIDE, and their ratio is the point. Automatic
-- updates sample, and the sample rate falls as the table grows — a
-- 900-million-row table is routinely sampled below 1%, which is enough for an
-- even distribution and useless for a skewed one. A "recently updated"
-- statistic sampled at 0.3% is not a fresh statistic in any sense the optimiser
-- benefits from, and reporting last_updated alone would say it was.
--
-- MODIFICATION_COUNTER IS THE OTHER HALF. It counts rows changed since the last
-- update, and it is what says whether a statistic dated last March is stale or
-- simply describes a table nobody has touched since. Neither number means
-- anything without the other.
--
-- THE FULL DETAIL COVERS THE SAME 200 TABLES AS 010.objects.sql, by the same
-- ordering and the same tie-break, for the reason 060.columns.sql uses it: two
-- different caps in one directory make a table found in one file and missing
-- from another read as a collector defect. Auto-created statistics are
-- included; there are usually more of them than of the deliberate ones, and
-- they are the ones nobody knows about.
--
-- STATISTICS_ALL IS THE SAME LIST WITHOUT THE CAP, one short row per statistic
-- on every user table. The cap alone made every count taken from this file a
-- floor: on a client database in September 2026 the detail listed 1,354 of
-- 2,235 statistics over 599 tables, so "how many statistics are redundant" or
-- "how many have not moved in a year" could only be answered for the largest
-- 200 tables, and a rule that counts has no use for a floor. The short row
-- carries what those counts need and nothing that is expensive to emit:
-- identity, columns, the auto/user/index origin, has_filter, last_updated,
-- rows, modifications_since and persisted_sample_percent. It leaves out the
-- filter text, the sampling detail and the histogram figures, which are what
-- the detail is for. statistics_total counts this array; statistics_listed
-- still counts the detail. Measured on a lab database of 600 tables and 2,400
-- statistics: the detail held 800, the short list 2,400, the file went from
-- 286 KB to 935 KB and the run from one second to two. The detail keeps its
-- key and its columns, with persisted_sample_percent added at the end, so a
-- reader of "statistics" sees nothing removed or renamed.
--
-- 091.statistics-density keeps its cap, and has no short form, for reasons
-- given in its own header.
--
-- WHAT IT COSTS. sys.dm_db_stats_properties reads the header page of each
-- statistics blob, so the work is one small read per statistic rather than a
-- scan. It is read ONCE, for every statistic, into a table variable that both
-- lists then join, rather than applied twice; lifting the cap made the pass
-- cover every statistic, and a second pass over the capped subset would only
-- repeat reads already done. Both lists LEFT JOIN the staged rows, so a
-- statistic the DMF says nothing about is listed with NULLs rather than
-- dropped. There are two such cases and they read the same. A statistic that
-- has never been populated (an index created on an empty table, a filtered
-- statistic whose predicate matches nothing) comes back from the DMF as a row
-- of NULLs, measured on 2025; this header said "no row" until September 2026.
-- And a caller without SELECT on the table gets an empty rowset, as the DMF
-- documents: measured with a database user holding VIEW DEFINITION and VIEW
-- DATABASE STATE only, every one of 2,400 statistics came through with NULL
-- dates, and an inner join in place of the LEFT JOIN emitted none of them.
-- That NULL is a fact and is left visible.
--
-- NO JUDGEMENT IS APPLIED. No statistic is called stale here. Whether an
-- 8-month-old statistic matters depends on whether the table changed, on
-- whether anything queries the column, and on what the plans actually chose —
-- which needs 80.workload and the deployment calendar. The collector reports
-- the dates and the counts.
--
-- SQL Server 2012 SP1 is the floor, and the floor is the DMF rather than the
-- corpus default: sys.dm_db_stats_properties arrived in 2012 SP1 (11.0.3000)
-- and in 2008 R2 SP2. On 2012 RTM the batch would fail on an invalid object
-- name, so the gate is dotted rather than major-only.
--
-- PERSISTED_SAMPLE_PERCENT IS ABOVE THAT FLOOR. The DMF column arrived in
-- 2016 SP1 CU4 (13.0.4446) and in 2017 CU1, so 2017 RTM does not have it
-- either. It is the sample rate an UPDATE STATISTICS ... WITH
-- PERSIST_SAMPLE_PERCENT = ON pinned, which every later automatic update then
-- reuses; 0 means nothing is persisted. It matters because a pinned 1% on a
-- table that has since grown tenfold is a sampling decision nobody sees being
-- made again. Measured on 2025: after WITH SAMPLE 50 PERCENT,
-- PERSIST_SAMPLE_PERCENT = ON on two statistics, one inside the 200 tables and
-- one outside, the detail showed 50 on the first, statistics_all 50 on both,
-- and 0 everywhere else.
--
-- It is asked for by existence rather than by build number, the shape
-- 10.system/050.tempdb.sql and 20.databases/020.properties.sql use for
-- is_autogrow_all_files. COL_LENGTH answers on the DMF as on a view (8, the
-- float, measured on 2025), and the read that names the column goes through
-- sp_executesql, because a column the server does not know is a compile-time
-- error that no TRY at this level catches and that would take both lists with
-- it. Where COL_LENGTH is NULL (2012, 2014, 2016 before SP1 CU4, 2017 RTM) the
-- static branch stages the same rows with a NULL in that column, which reads as
-- "not knowable on this build" and never as "not persisted". Forcing that
-- branch on 2025 left every other value of both lists unchanged.
--
-- The CI matrix runs 2017 and 2022, which take the dynamic branch. The 2012
-- floor runs the static one and is checked by hand, so here is the reasoning:
-- the static branch names only DMF columns that 2012 SP1 documents; the batch
-- compiles there because the one statement naming the new column is a string
-- literal the server does not compile unless COL_LENGTH lets it run; and
-- COL_LENGTH, table variables with a composite primary key and datetime2 are
-- all 2008 or older.
--
-- Not collected, because the floor is below them:
--   sys.stats.is_incremental              (2014)
--   sys.dm_db_incremental_stats_properties (2014)
--   sys.stats.has_persisted_sample        (2019; this line said 2016 SP1 until
--                                          September 2026, and sys.stats
--                                          documents 2019. It says only
--                                          whether a rate is persisted, which
--                                          persisted_sample_percent above
--                                          already says with the rate)
--   sys.stats.auto_drop                   (2022)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

/* The DMF, read once for every statistic on a user table. Both lists below
   LEFT JOIN this, so a statistic with no row here (the DMF refused the
   caller) is still listed, with NULLs. The two branches select the same rows
   and differ only in the last column, which the static branch cannot name:
   see the header. */
DECLARE @props TABLE (
    [object_id]                int    NOT NULL,
    [stats_id]                 int    NOT NULL,
    [last_updated]             datetime2(7) NULL,
    [rows]                     bigint NULL,
    [rows_sampled]             bigint NULL,
    [steps]                    int    NULL,
    [unfiltered_rows]          bigint NULL,
    [modification_counter]     bigint NULL,
    [persisted_sample_percent] float  NULL,
    PRIMARY KEY ([object_id], [stats_id]));

IF COL_LENGTH('sys.dm_db_stats_properties', 'persisted_sample_percent') IS NOT NULL
    INSERT INTO @props ([object_id], [stats_id], [last_updated], [rows],
                        [rows_sampled], [steps], [unfiltered_rows],
                        [modification_counter], [persisted_sample_percent])
    EXEC sys.sp_executesql
        N'SELECT st.object_id, st.stats_id, sp.last_updated, sp.rows,
                 sp.rows_sampled, sp.steps, sp.unfiltered_rows,
                 sp.modification_counter, sp.persisted_sample_percent
          FROM sys.stats  AS st
          JOIN sys.tables AS t ON t.object_id = st.object_id AND t.is_ms_shipped = 0
          CROSS APPLY sys.dm_db_stats_properties(st.object_id, st.stats_id) AS sp
          OPTION (RECOMPILE, MAXDOP 1)';
ELSE
    INSERT INTO @props ([object_id], [stats_id], [last_updated], [rows],
                        [rows_sampled], [steps], [unfiltered_rows],
                        [modification_counter], [persisted_sample_percent])
    SELECT st.object_id, st.stats_id, sp.last_updated, sp.rows,
           sp.rows_sampled, sp.steps, sp.unfiltered_rows,
           sp.modification_counter, NULL
    FROM sys.stats  AS st
    JOIN sys.tables AS t ON t.object_id = st.object_id AND t.is_ms_shipped = 0
    CROSS APPLY sys.dm_db_stats_properties(st.object_id, st.stats_id) AS sp
    OPTION (RECOMPILE, MAXDOP 1);

WITH sized AS (
    SELECT TOP (200) t.object_id
    FROM sys.tables AS t
    CROSS APPLY (SELECT SUM(p.row_count) AS row_count
                 FROM sys.dm_db_partition_stats AS p
                 WHERE p.object_id = t.object_id AND p.index_id IN (0, 1)) AS ps
    WHERE t.is_ms_shipped = 0
    ORDER BY ps.row_count DESC, t.object_id
)
SELECT DB_NAME()                                                  AS [database],
       CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       200                                                        AS [listing_cap],
       (SELECT COUNT(*) FROM sized)                               AS [tables_covered],
       (SELECT COUNT(*)
        FROM sys.stats  AS st
        JOIN sys.tables AS t ON t.object_id = st.object_id AND t.is_ms_shipped = 0)
                                                                  AS [statistics_total],
       (SELECT COUNT(*)
        FROM sys.stats AS st
        JOIN sized     AS z ON z.object_id = st.object_id)        AS [statistics_listed],
       /* The database-level switches that decide whether any of the dates below
          could have moved on their own. A database with AUTO_UPDATE_STATISTICS
          off and an old date is a different story from one with it on. */
       (SELECT CAST(d.is_auto_create_stats_on AS int)
          FROM sys.databases AS d WHERE d.database_id = DB_ID())  AS [options.auto_create],
       (SELECT CAST(d.is_auto_update_stats_on AS int)
          FROM sys.databases AS d WHERE d.database_id = DB_ID())  AS [options.auto_update],
       (SELECT CAST(d.is_auto_update_stats_async_on AS int)
          FROM sys.databases AS d WHERE d.database_id = DB_ID())  AS [options.auto_update_async]
OPTION (RECOMPILE, MAXDOP 1);

/* One row per statistic, ordered by table then name. */
WITH sized AS (
    SELECT TOP (200) t.object_id
    FROM sys.tables AS t
    CROSS APPLY (SELECT SUM(p.row_count) AS row_count
                 FROM sys.dm_db_partition_stats AS p
                 WHERE p.object_id = t.object_id AND p.index_id IN (0, 1)) AS ps
    WHERE t.is_ms_shipped = 0
    ORDER BY ps.row_count DESC, t.object_id
)
SELECT SCHEMA_NAME(t.schema_id) + '.' + t.name                    AS [table],
       st.name                                                    AS [statistic],
       st.stats_id                                                AS [stats_id],
       /* Which columns it describes, leading column first: a statistic's
          histogram is on its FIRST column only, and the rest carry density
          alone. A reader who cannot see the order cannot tell which of the two
          a given estimate came from. Same concatenation idiom as
          070.index-columns.sql, QUOTENAME included, and 2012 has no
          STRING_AGG.

          THE QUOTING IS WHAT MAKES THE LIST READABLE AT ALL. Bare, a statistic
          on one column named "a, b" is indistinguishable from a statistic on
          two columns a and b, so nobody can say from this field how many
          columns the statistic has. That is not a rounding error on a strange
          name: the count decides whether a given estimate came from the
          histogram or from density, which is the question this field exists to
          answer. QUOTENAME doubles an inner right bracket, so a column named
          a]b comes out as [a]]b] and stays decodable.

          No width to widen here, unlike 091.statistics-density: the list goes
          straight into nvarchar(max) rather than through a sysname column, so
          the 258 characters QUOTENAME can return have nowhere to be cut. */
       STUFF((SELECT ', ' + QUOTENAME(c.name)
              FROM sys.stats_columns AS sc
              JOIN sys.columns       AS c ON c.object_id = sc.object_id
                                         AND c.column_id = sc.column_id
              WHERE sc.object_id = st.object_id
                AND sc.stats_id  = st.stats_id
              ORDER BY sc.stats_column_id
              FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, '')
                                                                  AS [columns],
       /* An auto-created statistic is one the optimiser asked for, and its name
          starts _WA_Sys_. That it exists at all says a query filtered on a
          column no index covers. */
       CAST(st.auto_created AS int)                               AS [is_auto_created],
       CAST(st.user_created AS int)                               AS [is_user_created],
       /* A statistic attached to an index is maintained by the index rebuild;
          a standalone one is not. Different maintenance, so it is projected. */
       CAST(CASE WHEN i.index_id IS NULL THEN 0 ELSE 1 END AS int) AS [is_index_statistic],
       CAST(st.no_recompute AS int)                               AS [no_recompute],
       CAST(st.has_filter AS int)                                 AS [has_filter],
       st.filter_definition                                       AS [filter_definition],
       CONVERT(varchar(23), sp.last_updated, 126)                 AS [last_updated],
       sp.rows                                                    AS [rows],
       sp.rows_sampled                                            AS [rows_sampled],
       /* Computed here rather than left to the reader, because this is the
          number the whole file is about and a ratio nobody works out is a
          ratio nobody reads. NULLIF guards the never-populated statistic,
          whose rows is 0 rather than NULL. */
       CAST(sp.rows_sampled * 100.0 / NULLIF(sp.rows, 0) AS DECIMAL(9,4))
                                                                  AS [sampled_pct],
       sp.steps                                                   AS [histogram_steps],
       sp.unfiltered_rows                                         AS [unfiltered_rows],
       /* Rows changed since last_updated. With last_updated, this is the pair
          that separates a stale statistic from an untouched table. */
       sp.modification_counter                                    AS [modifications_since],
       /* The rate later automatic updates will reuse, 0 when none is pinned,
          NULL where the build cannot say. Beside sampled_pct because the two
          answer different questions: what the last update sampled, and what
          the next one will. */
       sp.persisted_sample_percent                                AS [persisted_sample_percent]
FROM       sys.stats    AS st
JOIN       sized        AS z ON z.object_id = st.object_id
JOIN       sys.tables   AS t ON t.object_id = st.object_id
LEFT JOIN  sys.indexes  AS i ON i.object_id = st.object_id AND i.index_id = st.stats_id
/* LEFT, not inner: a caller the DMF refuses gets no row for any statistic,
   and an inner join would turn "not readable" into "no statistics". */
LEFT JOIN  @props       AS sp ON sp.object_id = st.object_id AND sp.stats_id = st.stats_id
ORDER BY t.schema_id, t.name, st.name
OPTION (RECOMPILE, MAXDOP 1);

/* statistics_all: one short row per statistic on every user table, uncapped.
   The columns are the detail's, under the same names, so a reader can join
   the two on table and statistic; see the header for what is left out. */
SELECT SCHEMA_NAME(t.schema_id) + '.' + t.name                    AS [table],
       st.name                                                    AS [statistic],
       st.stats_id                                                AS [stats_id],
       /* Same idiom and same quoting as the detail's columns, for the same
          reasons: the leading column and the column count are what a
          redundancy rule compares. */
       STUFF((SELECT ', ' + QUOTENAME(c.name)
              FROM sys.stats_columns AS sc
              JOIN sys.columns       AS c ON c.object_id = sc.object_id
                                         AND c.column_id = sc.column_id
              WHERE sc.object_id = st.object_id
                AND sc.stats_id  = st.stats_id
              ORDER BY sc.stats_column_id
              FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, '')
                                                                  AS [columns],
       CAST(st.auto_created AS int)                               AS [is_auto_created],
       CAST(st.user_created AS int)                               AS [is_user_created],
       /* Kept in the short row: a statistic duplicating an index's leading
          column is the commonest redundancy, and it cannot be told without
          knowing which of the two is the index. */
       CAST(CASE WHEN i.index_id IS NULL THEN 0 ELSE 1 END AS int) AS [is_index_statistic],
       CAST(st.has_filter AS int)                                 AS [has_filter],
       CONVERT(varchar(23), sp.last_updated, 126)                 AS [last_updated],
       /* rows beside modifications_since: a change count only reads as stale
          against the size it is a share of. */
       sp.rows                                                    AS [rows],
       sp.modification_counter                                    AS [modifications_since],
       sp.persisted_sample_percent                                AS [persisted_sample_percent]
FROM       sys.stats    AS st
JOIN       sys.tables   AS t ON t.object_id = st.object_id AND t.is_ms_shipped = 0
LEFT JOIN  sys.indexes  AS i ON i.object_id = st.object_id AND i.index_id = st.stats_id
LEFT JOIN  @props       AS sp ON sp.object_id = st.object_id AND sp.stats_id = st.stats_id
ORDER BY t.schema_id, t.name, st.name
OPTION (RECOMPILE, MAXDOP 1);
