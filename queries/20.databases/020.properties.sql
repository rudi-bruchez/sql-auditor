-- @scope:       database
-- @resultsets:  root:object, backups:object, files:array, largest_objects:array, unused_indexes:array, missing_indexes:array, fragmentation:array
-- @permissions: CONNECT, VIEW SERVER STATE, VIEW ANY DEFINITION, MSDB READ
-- @timeout:     300
--
-- Runs once per user database, with the connection context switched to it.
--
-- SEVEN RESULT SETS, SEVEN INDEPENDENT READS, AND THAT IS WHY EACH HAS ITS OWN
-- TRY/CATCH. This file is the widest collector in the corpus and the one with
-- the most ways to be blocked: it names user tables and indexes through
-- sys.tables, sys.indexes and sys.dm_db_index_physical_stats, and READ
-- UNCOMMITTED gives up locks on DATA and never on METADATA. Measured on SQL
-- Server 2022 behind one open ALTER TABLE, it came back 1222 after ten seconds
-- and lost all seven result sets, because a statement that fails mid-batch
-- takes the rest of the batch's output with it — so an ALTER on one table cost
-- the whole database's properties, files and backup dates, none of which had
-- anything to do with that table.
--
-- Each area now reads into variables or a table variable inside its own
-- TRY/CATCH, the CATCH assigns variables and nothing else, and the seven
-- emitting SELECTs at the bottom run unconditionally. The root object carries
-- one collected flag and one error number per area, because an empty array and
-- a blocked read are different facts and the archive must not merge them.
--
-- The root is emitted from VARIABLES and not from a buffered row. A root
-- result set that returns no rows is skipped by the encoder — that is correct
-- for "nothing to merge" — so a buffered root that failed would produce a
-- document with neither the properties nor any word about why, which is the
-- silent failure this whole design exists to prevent.
--
-- The fragmentation block is the one most likely to time out rather than
-- block: LIMITED mode still walks index metadata, and on a very large database
-- it is the expensive part of this file. It is last for that reason — the six
-- cheap areas are already buffered by the time it starts.
--
-- SQL Server 2012 is the floor. Removed for that reason:
--   sys.database_scoped_configurations   (2016) — whole result set
--   sys.database_query_store_options     (2016) — whole result set
--   sys.dm_db_log_info                   (2016 SP2) — space.vlf_count
--   sys.databases.is_query_store_on      (2016)
--   sys.databases.delayed_durability_desc              (2014)
--   sys.databases.is_auto_create_stats_incremental_on  (2014)
-- containment_desc and target_recovery_time_in_seconds are both 2012, kept.
-- sys.dm_db_log_space_usage is 2012; log_space_in_bytes_since_last_backup is
-- 2014 and is not projected.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @err_database int = 0, @err_backups int = 0, @err_files int = 0,
        @err_largest int = 0, @err_unused int = 0, @err_missing int = 0,
        @err_fragmentation int = 0, @msg nvarchar(2048) = N'';

DECLARE @db_name sysname, @db_id int, @db_create_date datetime,
        @db_owner nvarchar(128), @db_compat tinyint, @db_collation sysname,
        @db_state nvarchar(60), @db_user_access nvarchar(60),
        @db_recovery nvarchar(60), @db_page_verify nvarchar(60),
        @db_log_reuse_wait nvarchar(60), @db_containment nvarchar(60),
        @db_target_recovery int, @db_snapshot_isolation nvarchar(60),
        @db_rcsi bit, @db_auto_create_stats bit, @db_auto_update_stats bit,
        @db_auto_update_stats_async bit, @db_auto_close bit, @db_auto_shrink bit,
        @db_read_only bit, @db_tde bit, @db_trustworthy bit, @db_broker bit,
        @db_chaining bit;

DECLARE @data_allocated_mb decimal(14,1), @data_used_mb decimal(14,1),
        @log_size_mb decimal(14,1), @log_used_mb decimal(14,1),
        @log_used_pct decimal(5,2);

DECLARE @last_full datetime, @last_differential datetime, @last_log datetime;

DECLARE @files TABLE (
    [file_type]      tinyint,
    [file_id]        int,
    [name]           sysname,
    [type]           nvarchar(60),
    [physical_name]  nvarchar(260),
    [state]          nvarchar(60),
    [size_mb]        decimal(14,1) NULL,
    [used_mb]        decimal(14,1) NULL,
    [max_mb]         varchar(20),
    [percent_growth] bit,
    [growth]         nvarchar(4000));

/* reserved_pages is buffered although it is never emitted: the ranking is by
   the page count and not by the megabytes derived from it. Two objects that
   round to the same DECIMAL(14,1) are not the same size, and ordering on the
   rounded value would let the archive's order drift from the measurement. The
   same reasoning gives the three sort keys below. */
DECLARE @largest TABLE (
    [reserved_pages]   bigint,
    [table]            nvarchar(300),
    [storage]          nvarchar(60),
    [rows]             bigint,
    [data_reserved_mb] decimal(14,1),
    [data_used_mb]     decimal(14,1));

DECLARE @unused TABLE (
    [sort_writes]    bigint,
    [table]          nvarchar(300),
    [index_name]     sysname,
    [user_seeks]     bigint NULL,
    [user_scans]     bigint NULL,
    [user_lookups]   bigint NULL,
    [writes]         bigint NULL,
    [last_user_seek] datetime NULL,
    [last_user_scan] datetime NULL);

DECLARE @missing TABLE (
    [sort_impact]         float,
    [table]               nvarchar(300) NULL,
    [impact_score]        decimal(18,2),
    [uses]                bigint,
    [avg_impact_pct]      decimal(5,1),
    [equality_columns]    nvarchar(4000) NULL,
    [inequality_columns]  nvarchar(4000) NULL,
    [included_columns]    nvarchar(4000) NULL);

DECLARE @fragmentation TABLE (
    [sort_frag]         float,
    [table]             nvarchar(300) NULL,
    [index_name]        sysname NULL,
    [index_type]        nvarchar(60),
    [partition_number]  int,
    [page_count]        bigint,
    [fragmentation_pct] decimal(5,2));

BEGIN TRY
    SELECT
        /* ───────── database identity & options ───────── */
        @db_name                    = d.name,
        @db_id                      = d.database_id,
        @db_create_date             = d.create_date,
        @db_owner                   = SUSER_SNAME(d.owner_sid),
        @db_compat                  = d.compatibility_level,
        @db_collation               = d.collation_name,
        @db_state                   = d.state_desc,
        @db_user_access             = d.user_access_desc,
        @db_recovery                = d.recovery_model_desc,
        @db_page_verify             = d.page_verify_option_desc,
        @db_log_reuse_wait          = d.log_reuse_wait_desc,
        @db_containment             = d.containment_desc,
        @db_target_recovery         = d.target_recovery_time_in_seconds,
        @db_snapshot_isolation      = d.snapshot_isolation_state_desc,
        @db_rcsi                    = CAST(d.is_read_committed_snapshot_on   AS BIT),
        @db_auto_create_stats       = CAST(d.is_auto_create_stats_on         AS BIT),
        @db_auto_update_stats       = CAST(d.is_auto_update_stats_on         AS BIT),
        @db_auto_update_stats_async = CAST(d.is_auto_update_stats_async_on   AS BIT),
        @db_auto_close              = CAST(d.is_auto_close_on                AS BIT),
        @db_auto_shrink             = CAST(d.is_auto_shrink_on               AS BIT),
        @db_read_only               = CAST(d.is_read_only                    AS BIT),
        @db_tde                     = CAST(d.is_encrypted                    AS BIT),
        @db_trustworthy             = CAST(d.is_trustworthy_on               AS BIT),
        @db_broker                  = CAST(d.is_broker_enabled               AS BIT),
        @db_chaining                = CAST(d.is_db_chaining_on               AS BIT),

        /* ───────── space summary ───────── */
        -- The cast to BIGINT is before the SUM and not after it. sys.database_files.size
        -- is an int page count, SUM over int returns int, and int * 8 overflows at
        -- 2.1 TB — 281 million pages times eight is 2.25 billion, past the 2.147
        -- billion ceiling. The whole statement then fails with "Arithmetic overflow
        -- converting expression to data type int", so a 2.1 TB database reports no
        -- compatibility level, no page verify, no collation and no owner either.
        -- The line below already casts for the same reason.
        @data_allocated_mb =
            (SELECT CAST(SUM(CAST(size AS BIGINT)) * 8 / 1024.0 AS DECIMAL(14,1))
               FROM sys.database_files WHERE type = 0),
        @data_used_mb =
            (SELECT CAST(SUM(CAST(FILEPROPERTY(name,'SpaceUsed') AS BIGINT)) * 8 / 1024.0 AS DECIMAL(14,1))
               FROM sys.database_files WHERE type = 0),
        @log_size_mb  = CAST(ls.total_log_size_in_bytes / 1048576.0 AS DECIMAL(14,1)),
        @log_used_mb  = CAST(ls.used_log_space_in_bytes / 1048576.0 AS DECIMAL(14,1)),
        @log_used_pct = CAST(ls.used_log_space_in_percent AS DECIMAL(5,2))
    FROM       sys.databases             AS d
    CROSS JOIN sys.dm_db_log_space_usage AS ls
    WHERE d.database_id = DB_ID()
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_database = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* ───────── backups: last backup of each type, from msdb ───────── */
BEGIN TRY
    SELECT @last_full =
             (SELECT MAX(backup_finish_date) FROM msdb.dbo.backupset
               WHERE database_name = DB_NAME() AND type = 'D'),
           @last_differential =
             (SELECT MAX(backup_finish_date) FROM msdb.dbo.backupset
               WHERE database_name = DB_NAME() AND type = 'I'),
           @last_log =
             (SELECT MAX(backup_finish_date) FROM msdb.dbo.backupset
               WHERE database_name = DB_NAME() AND type = 'L')
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_backups = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* ───────── files ───────── */
BEGIN TRY
    INSERT INTO @files
    SELECT df.type,
           df.file_id,
           df.name,
           df.type_desc,
           df.physical_name,
           df.state_desc,
           CAST(CAST(df.size AS BIGINT) * 8 / 1024.0 AS DECIMAL(14,1)),
           CAST(CAST(FILEPROPERTY(df.name,'SpaceUsed') AS BIGINT) * 8 / 1024.0 AS DECIMAL(14,1)),
           CASE WHEN df.max_size = -1 THEN 'unlimited'
                WHEN df.max_size = 268435456 THEN 'log_2tb'
                ELSE CAST(CAST(CAST(df.max_size AS BIGINT) * 8 / 1024.0 AS DECIMAL(14,1)) AS varchar(20)) END,
           CAST(df.is_percent_growth AS BIT),
           CASE WHEN df.is_percent_growth = 1 THEN CONCAT(df.growth, ' %')
                ELSE CONCAT(CAST(CAST(df.growth AS BIGINT) * 8 / 1024.0 AS DECIMAL(14,1)), ' MB') END
    FROM sys.database_files AS df
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_files = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* ───────── largest_objects: top 20 by reserved size ─────────

   DATA ONLY, and the column names say so. The WHERE keeps index_id 0 and 1 —
   the heap or the clustered index — so a table's nonclustered indexes are not
   in these megabytes. They can outweigh the data, so this is a ranking of where
   the rows are, not a ranking of what occupies the disk; 70.schema/010 carries
   the four-way split and the total, and 70.schema/020 the size of every index
   one by one.

   The storage column used to carry an ELSE branch labelling anything outside
   index_id 0 and 1 as 'HEAP/CLUSTERED'. The WHERE below already guarantees
   there is nothing outside it, so the branch was unreachable and its label was
   the opposite of what it described. */
BEGIN TRY
    INSERT INTO @largest
    SELECT TOP (20)
           SUM(ps.reserved_page_count),
           SCHEMA_NAME(t.schema_id) + '.' + t.name,
           i.type_desc,
           SUM(ps.row_count),
           CAST(SUM(ps.reserved_page_count) * 8 / 1024.0 AS DECIMAL(14,1)),
           CAST(SUM(ps.used_page_count)     * 8 / 1024.0 AS DECIMAL(14,1))
    FROM sys.dm_db_partition_stats AS ps
    JOIN sys.tables  AS t ON t.object_id = ps.object_id
    JOIN sys.indexes AS i ON i.object_id = ps.object_id AND i.index_id = ps.index_id
    WHERE i.index_id IN (0,1)
    GROUP BY t.schema_id, t.name, i.type_desc, i.index_id
    ORDER BY SUM(ps.reserved_page_count) DESC
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_largest = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* ───────── unused_indexes: write-only nonclustered indexes ───────── */
BEGIN TRY
    INSERT INTO @unused
    SELECT TOP (25)
           ISNULL(us.user_updates,0),
           SCHEMA_NAME(o.schema_id) + '.' + o.name,
           i.name,
           us.user_seeks, us.user_scans, us.user_lookups,
           us.user_updates,
           us.last_user_seek, us.last_user_scan
    FROM sys.indexes AS i
    JOIN sys.objects AS o ON o.object_id = i.object_id AND o.type = 'U'
    LEFT JOIN sys.dm_db_index_usage_stats AS us
           ON us.object_id = i.object_id AND us.index_id = i.index_id
          AND us.database_id = DB_ID()
    WHERE i.type_desc = 'NONCLUSTERED' AND i.is_primary_key = 0 AND i.is_unique_constraint = 0
      AND ISNULL(us.user_seeks,0) + ISNULL(us.user_scans,0) + ISNULL(us.user_lookups,0) = 0
    ORDER BY ISNULL(us.user_updates,0) DESC
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_unused = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* ───────── missing_indexes: optimizer suggestions ───────── */
BEGIN TRY
    INSERT INTO @missing
    SELECT TOP (25)
           migs.avg_total_user_cost * migs.avg_user_impact * (migs.user_seeks + migs.user_scans),
           OBJECT_SCHEMA_NAME(mid.object_id, mid.database_id) + '.'
         + OBJECT_NAME(mid.object_id, mid.database_id),
           CAST(migs.avg_total_user_cost * migs.avg_user_impact * (migs.user_seeks + migs.user_scans) AS DECIMAL(18,2)),
           migs.user_seeks + migs.user_scans,
           CAST(migs.avg_user_impact AS DECIMAL(5,1)),
           mid.equality_columns, mid.inequality_columns, mid.included_columns
    FROM sys.dm_db_missing_index_group_stats AS migs
    JOIN sys.dm_db_missing_index_groups AS mig ON mig.index_group_handle = migs.group_handle
    JOIN sys.dm_db_missing_index_details AS mid ON mid.index_handle = mig.index_handle
    WHERE mid.database_id = DB_ID()
    ORDER BY migs.avg_total_user_cost * migs.avg_user_impact * (migs.user_seeks + migs.user_scans) DESC
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_missing = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* ───────── fragmentation: LIMITED mode, page_count > 1000 ─────────
   COSTLY on very large databases — remove this block if needed. */
BEGIN TRY
    INSERT INTO @fragmentation
    SELECT TOP (25)
           ips.avg_fragmentation_in_percent,
           OBJECT_SCHEMA_NAME(ips.object_id) + '.' + OBJECT_NAME(ips.object_id),
           i.name,
           ips.index_type_desc,
           ips.partition_number,
           ips.page_count,
           CAST(ips.avg_fragmentation_in_percent AS DECIMAL(5,2))
    FROM sys.dm_db_index_physical_stats(DB_ID(), NULL, NULL, NULL, 'LIMITED') AS ips
    JOIN sys.indexes AS i ON i.object_id = ips.object_id AND i.index_id = ips.index_id
    WHERE ips.page_count > 1000 AND ips.avg_fragmentation_in_percent > 10
    ORDER BY ips.avg_fragmentation_in_percent DESC
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_fragmentation = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT @db_name                     AS [database.name],
       @db_id                       AS [database.id],
       @db_create_date              AS [database.create_date],
       @db_owner                    AS [database.owner],
       @db_compat                   AS [database.compatibility_level],
       @db_collation                AS [database.collation],
       @db_state                    AS [database.state],
       @db_user_access              AS [database.user_access],
       @db_recovery                 AS [database.recovery_model],
       @db_page_verify              AS [database.page_verify],
       @db_log_reuse_wait           AS [database.log_reuse_wait],
       @db_containment              AS [database.containment],
       @db_target_recovery          AS [database.target_recovery_time_sec],
       @db_snapshot_isolation       AS [database.snapshot_isolation],
       @db_rcsi                     AS [database.rcsi_enabled],
       @db_auto_create_stats        AS [database.auto_create_stats],
       @db_auto_update_stats        AS [database.auto_update_stats],
       @db_auto_update_stats_async  AS [database.auto_update_stats_async],
       @db_auto_close               AS [database.auto_close],
       @db_auto_shrink              AS [database.auto_shrink],
       @db_read_only                AS [database.read_only],
       @db_tde                      AS [database.tde_encrypted],
       @db_trustworthy              AS [database.trustworthy],
       @db_broker                   AS [database.broker_enabled],
       @db_chaining                 AS [database.cross_db_chaining],
       @data_allocated_mb           AS [space.data_allocated_mb],
       @data_used_mb                AS [space.data_used_mb],
       @log_size_mb                 AS [space.log_size_mb],
       @log_used_mb                 AS [space.log_used_mb],
       @log_used_pct                AS [space.log_used_pct],
       CASE WHEN @err_database      = 0 THEN 1 ELSE 0 END AS [collected.database],
       CASE WHEN @err_backups       = 0 THEN 1 ELSE 0 END AS [collected.backups],
       CASE WHEN @err_files         = 0 THEN 1 ELSE 0 END AS [collected.files],
       CASE WHEN @err_largest       = 0 THEN 1 ELSE 0 END AS [collected.largest_objects],
       CASE WHEN @err_unused        = 0 THEN 1 ELSE 0 END AS [collected.unused_indexes],
       CASE WHEN @err_missing       = 0 THEN 1 ELSE 0 END AS [collected.missing_indexes],
       CASE WHEN @err_fragmentation = 0 THEN 1 ELSE 0 END AS [collected.fragmentation],
       @err_database                AS [errors.database],
       @err_backups                 AS [errors.backups],
       @err_files                   AS [errors.files],
       @err_largest                 AS [errors.largest_objects],
       @err_unused                  AS [errors.unused_indexes],
       @err_missing                 AS [errors.missing_indexes],
       @err_fragmentation           AS [errors.fragmentation],
       NULLIF(@msg, N'')            AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

SELECT @last_full         AS last_full,
       @last_differential AS last_differential,
       @last_log          AS last_log
OPTION (RECOMPILE, MAXDOP 1);

SELECT f.[name], f.[type], f.[physical_name], f.[state], f.[size_mb],
       f.[used_mb], f.[max_mb], f.[percent_growth], f.[growth]
FROM @files AS f
ORDER BY f.[file_type], f.[file_id]
OPTION (RECOMPILE, MAXDOP 1);

SELECT l.[table], l.[storage], l.[rows], l.[data_reserved_mb], l.[data_used_mb]
FROM @largest AS l
ORDER BY l.[reserved_pages] DESC
OPTION (RECOMPILE, MAXDOP 1);

SELECT u.[table], u.[index_name], u.[user_seeks], u.[user_scans],
       u.[user_lookups], u.[writes], u.[last_user_seek], u.[last_user_scan]
FROM @unused AS u
ORDER BY u.[sort_writes] DESC
OPTION (RECOMPILE, MAXDOP 1);

SELECT m.[table], m.[impact_score], m.[uses], m.[avg_impact_pct],
       m.[equality_columns], m.[inequality_columns], m.[included_columns]
FROM @missing AS m
ORDER BY m.[sort_impact] DESC
OPTION (RECOMPILE, MAXDOP 1);

SELECT g.[table], g.[index_name], g.[index_type], g.[partition_number],
       g.[page_count], g.[fragmentation_pct]
FROM @fragmentation AS g
ORDER BY g.[sort_frag] DESC
OPTION (RECOMPILE, MAXDOP 1);
