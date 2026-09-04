-- @scope:       database
-- @resultsets:  root:object, heaps:array, contention:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     120
--
-- Forwarded records on heaps, and where lock and latch waits actually land.
--
-- Why this collector exists: an audit found 1 840 of 2 040 tables with no
-- clustered index, and had no way to say whether that cost anything.
-- forwarded_fetch_count is the measure that answers it, and it exists only
-- here.
--
-- A FORWARDED RECORD IS THE SPECIFIC DISEASE OF A HEAP. When a row in a heap
-- grows past the free space on its page, SQL Server moves it and leaves a
-- pointer behind. Every later read of that row costs a second page fetch, and
-- the pointers accumulate: a heap that is updated will degrade indefinitely
-- while its row count stays flat, and nothing in the size or usage figures
-- shows it. A table with a clustered index cannot have them at all, which is
-- why this result set is heaps only — index_id 0.
--
-- THESE COUNTERS ARE WEAKER EVIDENCE THAN sys.dm_db_index_usage_stats AND THE
-- DIFFERENCE MATTERS. They reset on restart like the others, but they also
-- reset whenever the index metadata is evicted from memory — which happens
-- under memory pressure, with no event and no record. A low count is therefore
-- not proof of a healthy heap; it may be proof of a recent eviction. A HIGH
-- count is trustworthy, because nothing inflates it. Read this result set in
-- one direction only.
--
-- Lock waits are attributed per index, which is the part sys.dm_os_wait_stats
-- cannot do: it says the instance waited 2 382 hours on LCK_M_IS, not which
-- object anyone was queuing for. index_lock_promotion_count is collected with
-- them because a lock escalating to table level is how one writer stops every
-- reader, and it is the mechanism behind the longest waits.
--
-- The DMV is cheap: it reads counters already in memory and does not touch the
-- data, unlike sys.dm_db_index_physical_stats, which scans. There is no reason
-- to gate this collector behind a flag.
--
-- IT IS STILL BLOCKABLE, AND THAT IS WHY THE READS ARE BUFFERED. Cheap is not
-- the same as lock-free: the DMV is joined to sys.objects and sys.indexes,
-- which need a schema stability lock, and READ UNCOMMITTED gives up locks on
-- DATA and never on METADATA. Measured behind one open ALTER TABLE, this file
-- came back 1222 after ten seconds and lost its whole document — a statement
-- that fails mid-batch takes the rest of the batch's output with it. Each area
-- now reads inside its own TRY/CATCH into a table variable, the CATCH assigns
-- variables and nothing else, and the three emitting SELECTs at the bottom run
-- unconditionally. A blocked area comes back empty with its error number in
-- the root object. See 020.index-usage.sql for the same pattern and the
-- measurement behind it.
--
-- SQL Server 2012 is the floor. sys.dm_db_index_operational_stats predates it.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @err_counts int = 0, @err_heaps int = 0, @err_contention int = 0,
        @msg nvarchar(2048) = N'';

DECLARE @instance_start datetime, @seconds_since int,
        @rows_reporting int, @forwarded_total bigint, @heaps_forwarded int,
        @lock_wait_ms bigint, @lock_escalations bigint;

DECLARE @heaps TABLE (
    [table]             nvarchar(300),
    [forwarded_fetches] bigint,
    [leaf_inserts]      bigint,
    [leaf_updates]      bigint,
    [leaf_deletes]      bigint,
    [leaf_ghosts]       bigint,
    [range_scans]       bigint,
    [singleton_lookups] bigint,
    [rows]              bigint NULL);

DECLARE @contention TABLE (
    [table]             nvarchar(300),
    [index_name]        nvarchar(128),
    [index_id]          int,
    [row_lock_waits]    bigint,
    [row_lock_wait_ms]  bigint,
    [page_lock_waits]   bigint,
    [page_lock_wait_ms] bigint,
    [lock_escalations]  bigint,
    [latch_wait_ms]     bigint,
    [io_latch_wait_ms]  bigint);

BEGIN TRY
    SELECT @instance_start = si.sqlserver_start_time,
           @seconds_since  = DATEDIFF(second, si.sqlserver_start_time, GETDATE())
    FROM sys.dm_os_sys_info AS si
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @rows_reporting = COUNT(*)
    FROM sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL)
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @forwarded_total = SUM(os.forwarded_fetch_count)
    FROM sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
    WHERE os.index_id = 0
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @heaps_forwarded = COUNT(DISTINCT os.object_id)
    FROM sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
    WHERE os.index_id = 0 AND os.forwarded_fetch_count > 0
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @lock_wait_ms = SUM(os.row_lock_wait_in_ms + os.page_lock_wait_in_ms)
    FROM sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @lock_escalations = SUM(os.index_lock_promotion_count)
    FROM sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_counts = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* Heaps only. leaf_update_count travels with the forwarded count because a
   forwarded record is created by an update: the ratio is what separates a
   heap that is merely written from one that is being damaged. */
BEGIN TRY
    INSERT INTO @heaps
    SELECT TOP (200)
           SCHEMA_NAME(o.schema_id) + '.' + o.name,
           SUM(os.forwarded_fetch_count),
           SUM(os.leaf_insert_count),
           SUM(os.leaf_update_count),
           SUM(os.leaf_delete_count),
           SUM(os.leaf_ghost_count),
           SUM(os.range_scan_count),
           SUM(os.singleton_lookup_count),
           MAX(ps.row_count)
    FROM       sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
    JOIN       sys.objects AS o ON o.object_id = os.object_id AND o.type = 'U'
    LEFT JOIN  sys.dm_db_partition_stats AS ps
            ON ps.object_id = os.object_id AND ps.index_id = os.index_id
    WHERE os.index_id = 0
    GROUP BY o.schema_id, o.name
    ORDER BY SUM(os.forwarded_fetch_count) DESC, SUM(os.leaf_update_count) DESC
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_heaps = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* Ordered by total lock wait, not by lock count: a million locks taken and
   released instantly are not a problem, and one lock held for four minutes
   is. */
BEGIN TRY
    INSERT INTO @contention
    SELECT TOP (200)
           SCHEMA_NAME(o.schema_id) + '.' + o.name,
           ISNULL(i.name, '(heap)'),
           os.index_id,
           SUM(os.row_lock_wait_count),
           SUM(os.row_lock_wait_in_ms),
           SUM(os.page_lock_wait_count),
           SUM(os.page_lock_wait_in_ms),
           SUM(os.index_lock_promotion_count),
           SUM(os.page_latch_wait_in_ms),
           SUM(os.page_io_latch_wait_in_ms)
    FROM       sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
    JOIN       sys.objects AS o ON o.object_id = os.object_id AND o.type = 'U'
    LEFT JOIN  sys.indexes AS i ON i.object_id = os.object_id AND i.index_id = os.index_id
    WHERE os.row_lock_wait_in_ms > 0 OR os.page_lock_wait_in_ms > 0
       OR os.index_lock_promotion_count > 0
    GROUP BY o.schema_id, o.name, i.name, os.index_id
    ORDER BY SUM(os.row_lock_wait_in_ms) + SUM(os.page_lock_wait_in_ms) DESC
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_contention = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT DB_NAME()                                            AS [database],
       SYSDATETIME()                                        AS [collected_at],
       @instance_start                                      AS [instance_start],
       @seconds_since                                       AS [seconds_since_instance_start],
       @rows_reporting                                      AS [rows_reporting],
       @forwarded_total                                     AS [forwarded_fetches_total],
       @heaps_forwarded                                     AS [heaps_with_forwarded_fetches],
       @lock_wait_ms                                        AS [lock_wait_ms_total],
       @lock_escalations                                    AS [lock_escalations_total],
       200                                                  AS [listing_cap],
       CASE WHEN @err_counts     = 0 THEN 1 ELSE 0 END      AS [collected.counts],
       CASE WHEN @err_heaps      = 0 THEN 1 ELSE 0 END      AS [collected.heaps],
       CASE WHEN @err_contention = 0 THEN 1 ELSE 0 END      AS [collected.contention],
       @err_counts                                          AS [errors.counts],
       @err_heaps                                           AS [errors.heaps],
       @err_contention                                      AS [errors.contention],
       NULLIF(@msg, N'')                                    AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

SELECT h.[table], h.[forwarded_fetches], h.[leaf_inserts], h.[leaf_updates],
       h.[leaf_deletes], h.[leaf_ghosts], h.[range_scans],
       h.[singleton_lookups], h.[rows]
FROM @heaps AS h
ORDER BY h.[forwarded_fetches] DESC, h.[leaf_updates] DESC
OPTION (RECOMPILE, MAXDOP 1);

SELECT c.[table], c.[index_name], c.[index_id],
       c.[row_lock_waits]    AS [row_lock.waits],
       c.[row_lock_wait_ms]  AS [row_lock.wait_ms],
       c.[page_lock_waits]   AS [page_lock.waits],
       c.[page_lock_wait_ms] AS [page_lock.wait_ms],
       c.[lock_escalations]  AS [lock_escalations],
       c.[latch_wait_ms]     AS [latch.wait_ms],
       c.[io_latch_wait_ms]  AS [io_latch.wait_ms]
FROM @contention AS c
ORDER BY c.[row_lock_wait_ms] + c.[page_lock_wait_ms] DESC
OPTION (RECOMPILE, MAXDOP 1);
