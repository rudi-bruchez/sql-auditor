-- @scope:       database
-- @resultsets:  root:object, statistics:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @min_version: 11.0.3000
-- @timeout:     180
--
-- Every statistic on the largest tables: when it was last updated, on how many
-- rows, sampled how far, and how many rows have changed since.
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
-- THE SAME 200 TABLES AS 010.objects.sql, by the same ordering and the same
-- tie-break, for the reason 060.columns.sql uses it: two different caps in one
-- directory make a table found in one file and missing from another read as a
-- collector defect. Auto-created statistics are included — there are usually
-- more of them than of the deliberate ones, and they are the ones nobody knows
-- about.
--
-- WHAT IT COSTS. sys.dm_db_stats_properties reads the header page of each
-- statistics blob, so the work is one small read per statistic rather than a
-- scan. It is applied through OUTER APPLY, which returns a row of NULLs rather
-- than failing when a statistic has never been populated — a filtered statistic
-- on an empty partition, or one on a table that has never held a row. That NULL
-- is a fact and is left visible.
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
-- name, so the gate is dotted rather than major-only. Not collected for that
-- reason:
--   sys.stats.is_incremental              (2014)
--   sys.dm_db_incremental_stats_properties (2014)
--   sys.stats.has_persisted_sample        (2016 SP1)
--   sys.stats.auto_drop                   (2022)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

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
          070.index-columns.sql, and 2012 has no STRING_AGG. */
       STUFF((SELECT ', ' + c.name
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
       sp.modification_counter                                    AS [modifications_since]
FROM       sys.stats    AS st
JOIN       sized        AS z ON z.object_id = st.object_id
JOIN       sys.tables   AS t ON t.object_id = st.object_id
LEFT JOIN  sys.indexes  AS i ON i.object_id = st.object_id AND i.index_id = st.stats_id
/* OUTER, not CROSS: a statistic that has never been populated returns no row
   from the DMF, and dropping it here would hide exactly the statistic that has
   never described anything. */
OUTER APPLY sys.dm_db_stats_properties(st.object_id, st.stats_id) AS sp
ORDER BY t.schema_id, t.name, st.name
OPTION (RECOMPILE, MAXDOP 1);
