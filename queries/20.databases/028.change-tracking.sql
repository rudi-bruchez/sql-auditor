-- @scope:       database
-- @resultsets:  root:object, tracked_tables:array, internal_tables:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     60
-- @profiles:    space
--
-- Runs once per user database, with the connection context switched to it.
--
-- Whether this database tracks its own changes, how far behind the cleanup is,
-- and how much space the tracking is holding that nothing else can see.
--
-- WHY THIS COLLECTOR EXISTS. Change tracking keeps a side table for every
-- tracked table plus one transaction table for the database, and those tables
-- are internal: they have no row in sys.tables, they do not appear in
-- 70.schema/010.objects.sql, and 20.databases/010.all-databases.sql counts
-- their pages in the database size without attributing them to anything. So a
-- database can grow by tens of gigabytes with every visible object accounted
-- for and the growth unexplained. That is the shape of finding this corpus
-- exists to prevent, and until now nothing here could see it.
--
-- WHAT GOES WRONG, AND WHY IT GOES WRONG QUIETLY. The cleanup is a background
-- task. It removes rows older than the retention period, and it only removes
-- them once every consumer has caught up: a consumer that stopped calling
-- CHANGETABLE months ago holds the whole history open. The engine does not
-- complain. Retention is a promise about what is kept, never about what is
-- discarded, and a database whose retention says two days can be holding two
-- years.
--
-- THE LAG IS THE MEASUREMENT, AND IT IS A DIFFERENCE OF VERSIONS. Change
-- tracking numbers its state with a monotonic version, and three of them
-- matter per table. CHANGE_TRACKING_CURRENT_VERSION() is where the database is
-- now. min_valid_version is the oldest version a consumer may still ask for,
-- so current minus min_valid is how much history is retained right now.
-- cleanup_version is how far the cleanup has actually got. When
-- cleanup_version sits far below min_valid_version the cleanup is behind;
-- when min_valid_version sits far below current on a database whose retention
-- is short, the cleanup is not running at all. Versions are not time, so the
-- figure is a count of committed transactions rather than a duration, and it
-- has to be read against how busy the database is.
--
-- cleanup_version IS NULL UNTIL THE CLEANUP HAS RUN ONCE, which is measured
-- rather than assumed: on a database where change tracking was switched on
-- minutes earlier the column was NULL on every tracked table, so
-- cleanup_behind_by is NULL too. NULL there means "never yet run" and is not
-- the same statement as zero, which would mean "ran and had nothing to do".
-- The difference matters on a freshly restored database, where a NULL is
-- expected and a large number is not.
--
-- THE SIZE IS READ FROM THE INTERNAL TABLES, WHICH TAKES A JOIN NOBODY
-- REMEMBERS. sys.internal_tables carries a row per side table, with parent_id
-- pointing at the user table it belongs to, and its pages are reached through
-- sys.dm_db_partition_stats on the internal table's own object_id. The
-- database-wide transaction table, whose internal_type_desc is
-- TRACKED_COMMITTED_TRANSACTIONS, has no parent and is the one that grows
-- when the cleanup stalls across every table at once.
--
-- That last one exists whether or not change tracking is on, which is why
-- counts.tracking_internal_tables reads 1 on a database that tracks nothing.
-- Measured on tempdb on both lab instances: tracking.enabled 0, every version
-- NULL, and one tracking internal table holding no pages. A reader who takes
-- that count as evidence of tracking will be wrong, and tracking.enabled is
-- the column that settles it.
--
-- THAT VIEW IS NOISY AND IS FILTERED ON PURPOSE. Measured on 16.0.4265.3 and
-- 17.0.4065.4, sys.internal_tables returns 41 and 48 rows in master alone on
-- instances where change tracking has never been switched on: Query Store's
-- own storage, service broker queues, contained-feature tables. On the test
-- database below, 38 internal tables for two tracked ones. A collector
-- that read the view without filtering internal_type would report those as
-- change tracking overhead. The second array carries every internal table with
-- its type rather than only the tracking ones, because the same blind spot
-- applies to all of them: Query Store's internal tables are just as invisible
-- to an object inventory and just as capable of holding gigabytes.
--
-- SQL Server 2012 is the floor. Change tracking and sys.internal_tables both
-- arrived in 2008. Not collected for that reason:
--   CHANGETABLE / CHANGE_TRACKING_MIN_VALID_VERSION per table (the catalogue
--     column min_valid_version answers the same thing without a function call
--     per table)
--   the side tables' rows                (they hold primary key values of the
--     tracked table, which is client data; the page count answers the size
--     question without reading any of them)
--   sp_flush_CT_internal_table_on_demand (undocumented, and it writes)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @current bigint = NULL;

/* NULL when change tracking is off for this database, which is the normal
   case and is not an error. Guarded because the function raises rather than
   returning NULL on some builds when the feature is not enabled. */
BEGIN TRY
    SET @current = CHANGE_TRACKING_CURRENT_VERSION();
END TRY
BEGIN CATCH
    SET @current = NULL;
END CATCH

SELECT
    DB_NAME()                                                   AS [database],
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    CAST(CASE WHEN EXISTS (SELECT 1 FROM sys.change_tracking_databases
                            WHERE database_id = DB_ID())
              THEN 1 ELSE 0 END AS bit)                         AS [tracking.enabled],
    (SELECT CAST(ctd.is_auto_cleanup_on AS bit)
       FROM sys.change_tracking_databases AS ctd
      WHERE ctd.database_id = DB_ID())                          AS [tracking.auto_cleanup],
    (SELECT ctd.retention_period FROM sys.change_tracking_databases AS ctd
      WHERE ctd.database_id = DB_ID())                          AS [tracking.retention_period],
    (SELECT ctd.retention_period_units_desc FROM sys.change_tracking_databases AS ctd
      WHERE ctd.database_id = DB_ID())                          AS [tracking.retention_units],
    @current                                                    AS [tracking.current_version],
    -- How far the cleanup has got across the whole database. The minimum over
    -- tables, because the database's history is only as short as its most
    -- neglected table.
    (SELECT MIN(ctt.cleanup_version) FROM sys.change_tracking_tables AS ctt)
                                                                AS [tracking.min_cleanup_version],
    (SELECT MIN(ctt.min_valid_version) FROM sys.change_tracking_tables AS ctt)
                                                                AS [tracking.min_valid_version],
    -- The headline: how many versions of history the database is still
    -- holding. A count of committed transactions, never a duration.
    (SELECT @current - MIN(ctt.min_valid_version)
       FROM sys.change_tracking_tables AS ctt)                  AS [tracking.versions_retained],
    (SELECT COUNT(*) FROM sys.change_tracking_tables)            AS [counts.tracked_tables],
    (SELECT COUNT(*) FROM sys.internal_tables
      WHERE internal_type_desc IN (N'CHANGE_TRACKING',
                                   N'TRACKED_COMMITTED_TRANSACTIONS'))
                                                                AS [counts.tracking_internal_tables],
    -- What the tracking is holding, in megabytes, attributed to nothing in any
    -- other collector.
    (SELECT CAST(ISNULL(SUM(ps.used_page_count), 0) * 8 / 1024.0 AS DECIMAL(18,1))
       FROM sys.internal_tables AS it
       JOIN sys.dm_db_partition_stats AS ps ON ps.object_id = it.object_id
      WHERE it.internal_type_desc IN (N'CHANGE_TRACKING',
                                      N'TRACKED_COMMITTED_TRANSACTIONS'))
                                                                AS [counts.tracking_mb],
    (SELECT COUNT(*) FROM sys.internal_tables)                  AS [counts.internal_tables_all],
    (SELECT CAST(ISNULL(SUM(ps.used_page_count), 0) * 8 / 1024.0 AS DECIMAL(18,1))
       FROM sys.internal_tables AS it
       JOIN sys.dm_db_partition_stats AS ps ON ps.object_id = it.object_id)
                                                                AS [counts.internal_tables_all_mb]
OPTION (RECOMPILE, MAXDOP 1);

/* One row per tracked table. The side table's size sits beside the versions
   because the two answer different halves of the same question: the versions
   say how much history is retained, the megabytes say what that costs. */
SELECT
    OBJECT_SCHEMA_NAME(ctt.object_id) + '.' + OBJECT_NAME(ctt.object_id)
                                                                AS [table],
    CAST(ctt.is_track_columns_updated_on AS bit)                AS [tracks_columns_updated],
    ctt.min_valid_version                                       AS [min_valid_version],
    ctt.begin_version                                           AS [begin_version],
    ctt.cleanup_version                                         AS [cleanup_version],
    -- Versions of history still held for this table.
    @current - ctt.min_valid_version                            AS [versions_retained],
    -- How far the cleanup trails what is still valid. A large positive number
    -- means rows that could be removed have not been.
    ctt.min_valid_version - ctt.cleanup_version                 AS [cleanup_behind_by],
    -- The side table, by size. It carries one row per tracked change that has
    -- not been cleaned up, so this is the space the retention is costing.
    (SELECT CAST(ISNULL(SUM(ps.used_page_count), 0) * 8 / 1024.0 AS DECIMAL(18,1))
       FROM sys.internal_tables AS it
       JOIN sys.dm_db_partition_stats AS ps ON ps.object_id = it.object_id
      WHERE it.parent_id = ctt.object_id
        AND it.internal_type_desc = N'CHANGE_TRACKING')         AS [side_table_mb],
    (SELECT ISNULL(SUM(ps.row_count), 0)
       FROM sys.internal_tables AS it
       JOIN sys.dm_db_partition_stats AS ps
         ON ps.object_id = it.object_id AND ps.index_id IN (0, 1)
      WHERE it.parent_id = ctt.object_id
        AND it.internal_type_desc = N'CHANGE_TRACKING')         AS [side_table_rows],
    -- The tracked table itself, for the ratio that makes the side table
    -- readable: a side table larger than the table it tracks is the finding.
    (SELECT CAST(ISNULL(SUM(ps.used_page_count), 0) * 8 / 1024.0 AS DECIMAL(18,1))
       FROM sys.dm_db_partition_stats AS ps
      WHERE ps.object_id = ctt.object_id AND ps.index_id IN (0, 1))
                                                                AS [table_mb]
FROM sys.change_tracking_tables AS ctt
ORDER BY ctt.min_valid_version - ctt.cleanup_version DESC, ctt.object_id
OPTION (RECOMPILE, MAXDOP 1);

/* Every internal table, not only the tracking ones. They share one property
   that makes them worth listing together: none of them appears in an object
   inventory, so all of them can hold space that nothing accounts for. The
   type says which feature is responsible. */
SELECT TOP (200)
    it.internal_type_desc                                       AS [type],
    it.name                                                     AS [name],
    CASE WHEN it.parent_id > 0
         THEN OBJECT_SCHEMA_NAME(it.parent_id) + '.' + OBJECT_NAME(it.parent_id)
    END                                                         AS [parent],
    ISNULL(ps.rows, 0)                                          AS [rows],
    CAST(ISNULL(ps.used_mb, 0) AS DECIMAL(18,1))                AS [used_mb]
FROM sys.internal_tables AS it
OUTER APPLY (
    SELECT SUM(CASE WHEN s.index_id IN (0, 1) THEN s.row_count END) AS rows,
           SUM(s.used_page_count) * 8 / 1024.0                      AS used_mb
    FROM sys.dm_db_partition_stats AS s
    WHERE s.object_id = it.object_id
) AS ps
WHERE ps.used_mb > 0
ORDER BY ps.used_mb DESC, it.internal_type_desc, it.name
OPTION (RECOMPILE, MAXDOP 1);
