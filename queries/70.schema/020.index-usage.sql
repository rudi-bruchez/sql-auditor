-- @scope:       database
-- @resultsets:  root:object, usage:array, missing:array
-- @permissions: CONNECT, VIEW SERVER STATE, VIEW ANY DEFINITION
-- @timeout:     120
--
-- Index usage counters and optimizer index suggestions, complete and
-- unfiltered, for one database.
--
-- Why this collector exists, and why it overlaps 20.databases/020.properties:
-- that file carries unused_indexes and missing_indexes as TOP (25) triage
-- views, which is the right shape for a routine audit. This one is a BASELINE.
-- Both DMVs behind it reset on restart, and sys.dm_db_index_usage_stats also
-- loses a database's rows when that database is closed or detached. A top-25
-- extract cannot be re-expanded afterwards, so before a restart the whole
-- picture has to be taken or it is gone.
--
-- Consequently there is no TOP and no threshold here. Every index of every
-- user table is emitted, used or not.
--
-- THE READS ARE BUFFERED THROUGH TABLE VARIABLES, AND THE THREE RESULT SETS
-- ARE EMITTED WHATEVER HAPPENED. This file names user objects — sys.indexes,
-- sys.objects — so it needs a schema stability lock on each, and READ
-- UNCOMMITTED does not give that up: it releases locks on DATA, never on
-- METADATA. Behind an ALTER holding Sch-M, this collector waits, and since
-- SET LOCK_TIMEOUT 10000 entered the contract it gives up after ten seconds
-- with error 1222 instead. Measured on SQL Server 2022, against a database
-- with one ALTER TABLE open in another session: this file, 010.objects,
-- 030.index-operational and 20.databases/020.properties all came back 1222
-- and every one of them lost its WHOLE document, because a statement that
-- fails mid-batch takes the rest of the batch's output with it.
--
-- A collector that loses everything over one blocked read is worse than one
-- that reports what it got. So each area is read inside its own TRY/CATCH
-- into a table variable, the CATCH assigns variables and nothing else, and
-- the emitting SELECTs at the bottom run unconditionally. A blocked area
-- comes back empty with its error number in the root object; the others are
-- unaffected.
--
-- The table variables are declared, rather than the reads deferred through
-- sp_executesql as in the replication collectors, because there the object
-- may not exist and Msg 208 is raised at COMPILE time where TRY/CATCH cannot
-- reach it. Everything here always exists. 1222 is a run-time error and an
-- ordinary TRY/CATCH catches it — verified rather than assumed.
--
-- has_usage_row is the point of the usage result set, and the reason the
-- counters are NOT collapsed with ISNULL(…, 0). An index with a DMV row
-- showing zero seeks has been considered and not used; an index with no DMV
-- row at all has not been touched since the counters last reset. Those are
-- different facts, and writing 0 for both destroys the distinction exactly
-- when it matters — deciding whether an index is dead.
--
-- Durations are compared in SECONDS, never milliseconds: DATEDIFF(millisecond,
-- …) overflows a 32-bit int after about 24 days, so it fails on precisely the
-- long-uptime servers whose counters are most worth collecting.
--
-- The instance start time is emitted because a usage counter is meaningless
-- without the period it accumulated over. It is an upper bound on that period,
-- not a measurement — the counters can also be cleared without a restart.
--
-- SQL Server 2012 is the floor. Not collected for that reason:
--   sys.dm_db_index_operational_stats leaf_page_merge (behaviour differs)
--   sys.dm_db_missing_index_group_stats_query          (2019)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @err_counts int = 0, @err_usage int = 0, @err_missing int = 0,
        @msg nvarchar(2048) = N'';

DECLARE @instance_start datetime, @seconds_since int,
        @indexes_total int, @indexes_with_usage_row int, @missing_suggestions int;

/* schema_id is buffered although it is never emitted: the ORDER BY below is
   by schema_id and not by schema NAME, and the two differ. Sorting the
   buffered rows by the name would reorder the archive for no reason anyone
   could see. */
DECLARE @usage TABLE (
    [schema_id]            int,
    [table]                nvarchar(300),
    [index_name]           sysname NULL,
    [index_id]             int,
    [index_type]           nvarchar(60),
    [is_unique]            int,
    [is_primary_key]       int,
    [is_unique_constraint] int,
    [is_disabled]          int,
    [filter_definition]    nvarchar(max) NULL,
    [rows]                 bigint NULL,
    [reserved_mb]          decimal(18,2) NULL,
    [has_usage_row]        int,
    [user_seeks]           bigint NULL,
    [user_scans]           bigint NULL,
    [user_lookups]         bigint NULL,
    [user_updates]         bigint NULL,
    [last_user_seek]       datetime NULL,
    [last_user_scan]       datetime NULL,
    [last_user_lookup]     datetime NULL,
    [last_user_update]     datetime NULL,
    [system_seeks]         bigint NULL,
    [system_scans]         bigint NULL);

DECLARE @missing TABLE (
    [object_id]            int,
    [table]                nvarchar(300) NULL,
    [equality_columns]     nvarchar(4000) NULL,
    [inequality_columns]   nvarchar(4000) NULL,
    [included_columns]     nvarchar(4000) NULL,
    [user_seeks]           bigint,
    [user_scans]           bigint,
    [avg_total_user_cost]  decimal(18,4),
    [avg_user_impact_pct]  decimal(5,1),
    [last_user_seek]       datetime NULL,
    [last_user_scan]       datetime NULL);

BEGIN TRY
    SELECT @instance_start = si.sqlserver_start_time,
           @seconds_since  = DATEDIFF(second, si.sqlserver_start_time, GETDATE())
    FROM sys.dm_os_sys_info AS si
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @indexes_total = COUNT(*)
    FROM sys.indexes AS i
    JOIN sys.objects AS o ON o.object_id = i.object_id AND o.type = 'U'
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @indexes_with_usage_row = COUNT(*)
    FROM sys.dm_db_index_usage_stats AS us
    WHERE us.database_id = DB_ID()
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @missing_suggestions = COUNT(*)
    FROM sys.dm_db_missing_index_details AS mid
    WHERE mid.database_id = DB_ID()
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_counts = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* Heaps are included: index_id 0 is a heap and its usage counters are as real
   as any other. Excluding them would hide the read pattern of exactly the
   tables most likely to need a clustered index. */
BEGIN TRY
    INSERT INTO @usage
    SELECT o.schema_id,
           SCHEMA_NAME(o.schema_id) + '.' + o.name,
           i.name,
           i.index_id,
           i.type_desc,
           CAST(i.is_unique AS int),
           CAST(i.is_primary_key AS int),
           CAST(i.is_unique_constraint AS int),
           CAST(i.is_disabled AS int),
           i.filter_definition,
           ps.row_count,
           CAST(ps.reserved_page_count * 8.0 / 1024 AS DECIMAL(18,2)),
           CASE WHEN us.index_id IS NULL THEN 0 ELSE 1 END,
           us.user_seeks, us.user_scans, us.user_lookups, us.user_updates,
           us.last_user_seek, us.last_user_scan, us.last_user_lookup,
           us.last_user_update, us.system_seeks, us.system_scans
    FROM       sys.indexes AS i
    JOIN       sys.objects AS o
            ON o.object_id = i.object_id AND o.type = 'U'
    LEFT JOIN  sys.dm_db_index_usage_stats AS us
            ON us.object_id = i.object_id AND us.index_id = i.index_id
           AND us.database_id = DB_ID()
    LEFT JOIN (SELECT p.object_id, p.index_id,
                      SUM(p.row_count)            AS row_count,
                      SUM(p.reserved_page_count)  AS reserved_page_count
               FROM sys.dm_db_partition_stats AS p
               GROUP BY p.object_id, p.index_id) AS ps
            ON ps.object_id = i.object_id AND ps.index_id = i.index_id
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_usage = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* No impact score and no ordering by it. avg_user_impact is the optimizer's
   own estimate and multiplying it into a single number is the analysis layer's
   business; the raw components are what resets on restart. */
BEGIN TRY
    INSERT INTO @missing
    SELECT mid.object_id,
           OBJECT_SCHEMA_NAME(mid.object_id, mid.database_id) + '.'
         + OBJECT_NAME(mid.object_id, mid.database_id),
           mid.equality_columns,
           mid.inequality_columns,
           mid.included_columns,
           migs.user_seeks,
           migs.user_scans,
           CAST(migs.avg_total_user_cost AS DECIMAL(18,4)),
           CAST(migs.avg_user_impact AS DECIMAL(5,1)),
           migs.last_user_seek,
           migs.last_user_scan
    FROM       sys.dm_db_missing_index_group_stats AS migs
    JOIN       sys.dm_db_missing_index_groups AS mig
            ON mig.index_group_handle = migs.group_handle
    JOIN       sys.dm_db_missing_index_details AS mid
            ON mid.index_handle = mig.index_handle
    WHERE mid.database_id = DB_ID()
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_missing = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT DB_NAME()                                            AS [database],
       @instance_start                                      AS [instance_start],
       @seconds_since                                       AS [seconds_since_instance_start],
       SYSDATETIME()                                        AS [collected_at],
       @indexes_total                                       AS [indexes_total],
       @indexes_with_usage_row                              AS [indexes_with_usage_row],
       @missing_suggestions                                 AS [missing_suggestions],
       CASE WHEN @err_counts  = 0 THEN 1 ELSE 0 END         AS [collected.counts],
       CASE WHEN @err_usage   = 0 THEN 1 ELSE 0 END         AS [collected.usage],
       CASE WHEN @err_missing = 0 THEN 1 ELSE 0 END         AS [collected.missing],
       @err_counts                                          AS [errors.counts],
       @err_usage                                           AS [errors.usage],
       @err_missing                                         AS [errors.missing],
       NULLIF(@msg, N'')                                    AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

SELECT u.[table], u.[index_name], u.[index_id], u.[index_type],
       u.[is_unique], u.[is_primary_key], u.[is_unique_constraint],
       u.[is_disabled], u.[filter_definition],
       u.[rows]                                             AS [size.rows],
       u.[reserved_mb]                                      AS [size.reserved_mb],
       u.[has_usage_row]                                    AS [usage.has_usage_row],
       u.[user_seeks]                                       AS [usage.user_seeks],
       u.[user_scans]                                       AS [usage.user_scans],
       u.[user_lookups]                                     AS [usage.user_lookups],
       u.[user_updates]                                     AS [usage.user_updates],
       u.[last_user_seek]                                   AS [usage.last_user_seek],
       u.[last_user_scan]                                   AS [usage.last_user_scan],
       u.[last_user_lookup]                                 AS [usage.last_user_lookup],
       u.[last_user_update]                                 AS [usage.last_user_update],
       u.[system_seeks]                                     AS [usage.system_seeks],
       u.[system_scans]                                     AS [usage.system_scans]
FROM @usage AS u
ORDER BY u.[schema_id], u.[table], u.[index_id]
OPTION (RECOMPILE, MAXDOP 1);

SELECT m.[table], m.[equality_columns], m.[inequality_columns],
       m.[included_columns], m.[user_seeks], m.[user_scans],
       m.[avg_total_user_cost], m.[avg_user_impact_pct],
       m.[last_user_seek], m.[last_user_scan]
FROM @missing AS m
ORDER BY m.[object_id]
OPTION (RECOMPILE, MAXDOP 1);
