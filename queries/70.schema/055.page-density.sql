-- @scope:       database
-- @resultsets:  root:object, indexes:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     1800
-- @requires_flag: measure_page_density
-- @profiles:    space
--
-- How full the leaf pages of the largest rowstore index partitions are.
--
-- Why this collector exists: whether rebuilding an index gives space back
-- depends on how full its pages are, and nothing else in the corpus says.
-- 20.databases/025.fragmentation reads sys.dm_db_index_physical_stats in LIMITED
-- mode, where avg_page_space_used_in_percent is NULL, and keeps only indexes
-- above 10 % logical fragmentation. Logical fragmentation is page ORDER, not
-- page FULLNESS: a table can be perfectly ordered and half empty, and it is
-- then the one a rebuild shrinks.
--
-- IT IS BEHIND A FLAG FOR COST, LIKE 041.compression-savings. SAMPLED works
-- allocation unit by allocation unit: one below 10,000 pages is read in full,
-- and a larger one is sampled. How much of a large one is read has been
-- measured twice on SQL Server 2025 from a cold buffer pool, with different
-- results. On 14 September 2026, 8 to 12 % of its pages, attributed to each
-- sample reading a whole extent. On 27 September 2026, on freshly loaded
-- tables, about 1 %: a 30,113-page clustered index read 424 pages from its
-- file and a 30,001-page heap 319. What separates the two runs is not known,
-- so the higher figure is the one to plan with. The LOB and row-overflow
-- pages of the index count as units of their own and are read the same way.
-- On a large database that is tens of gigabytes pulled into the buffer pool,
-- evicting what the workload had there, and SET LOCK_TIMEOUT does not bound it.
--
-- THE TIMEOUT IS 1800 SECONDS, LIKE 041. A batch cancelled on timeout loses the whole
-- document: TRY/CATCH does not catch a client cancel, so the summary row goes
-- with the detail rows, after the buffer pool was already evicted. Measured in
-- the field in September 2026, on a collection taken under the space profile,
-- that is what happened: the 1800 seconds ran out on the two largest databases
-- of one instance and both lost the whole file, summary included, so nothing
-- said which partitions had been measured or that anything had been.
--
-- So it measures ONE PARTITION PER CALL, largest first, and starts no new call
-- once @budget_sec have passed since the batch began, the way
-- 20.databases/025.fragmentation bounds its own read. The root says
-- in sample.measured_partitions how many were measured against
-- counts.eligible_partitions, so a list cut short is not read as complete. The
-- budget bounds the NUMBER of calls, not the length of one: a single enormous
-- partition can still outlast the remainder, and nothing here can prevent that.
--
-- THE UNIT IS THE PARTITION, NOT THE INDEX. sys.dm_db_partition_stats has one
-- row per partition, and a rebuild can be run per partition, so the cap of 50
-- is 50 partitions. One heavily partitioned index can take every slot; root
-- says how many distinct indexes the list covers so a reader sees it.
--
-- CHOSEN AND ORDERED BY IN-ROW PAGES, NOT BY RESERVED PAGES. reserved_page_count
-- counts LOB and row-overflow pages too. Measured on SQL Server 2025: a table
-- holding one 30 MB varbinary(max) row reserved 29.3 MB and had ONE in-row leaf
-- page, so ranking by reserved size spent a scan on an index with nothing a
-- rebuild could repack. The ranking decides what is measured, not what it
-- costs: the LOB pages of a chosen partition are read too, so lob_reserved_mb
-- is projected beside the other two sizes and the cost of each row is visible.
--
-- Indexed views are included. The clustered index of a view occupies space
-- and is rebuilt like a table's.
--
-- NO JUDGEMENT IS APPLIED, and no reclaimable size is computed. What a rebuild
-- would free depends on the fill factor it is run with, which is the
-- analysis layer's choice; this file reports the fullness, the fill factor
-- the index carries and the page count, which are the three inputs.
--
-- Leaf level of in-row data only. Upper levels are a rounding error on a large
-- index, and LOB and row-overflow pages are not repacked by a rebuild the same
-- way, so mixing them in would describe no real operation.
--
-- COMPRESSED_PAGE_COUNT SAYS WHETHER A PAGE PARTITION GOT WHAT IT ASKED FOR.
-- The SAMPLED call already returns it (LIMITED returns NULL), so projecting it
-- reads nothing more: what this file scans is unchanged. It is here because
-- 70.schema/030.index-operational cannot settle its own zero. Its
-- page_compression listing counts attempts and successes per PAGE partition,
-- and a page split from a compressed page comes out compressed without
-- counting a success, so "the data resists PAGE" and "the key is random and
-- every page is compressed" read alike there. Measured through both files on
-- 17.0.4065.4, 3 October 2026, on one database:
--
--   increasing key, repetitive rows  559 attempts, 280 successes, 280 of 280 compressed
--   increasing key, random rows      526 attempts,   0 successes,   0 of 527 compressed
--   random GUID key, repetitive rows 201 attempts,   1 success,   201 of 201 compressed
--   random GUID key, random rows     821 attempts,   0 successes,   0 of 822 compressed
--
-- The third and fourth have leaf allocations close to their attempts (201
-- and 822), which is all 030 can offer, and only the count here tells them
-- apart. The two files' rows meet on the database, table, index_id and
-- partition_number, which both project in the same form.
--
-- data_compression is projected beside it, from sys.partitions, because a
-- zero says something only on a PAGE partition: a ROW partition of the same
-- repetitive rows read 0 of 527, as every ROW or NONE partition will.
--
-- ON A PARTITION ABOVE 10,000 PAGES THE COUNT IS THE SAMPLE'S, SCALED UP. A
-- 16,667-page clustered index, every leaf page compressed after a TABLOCK
-- load, read 16,600 in SAMPLED and 16,667 in DETAILED, like record_count
-- beside it (298,800 against 300,000). Read it as a share of page_count, not
-- as an exact number of pages.
--
-- It covers what this file measures and nothing else: indexes, not heaps,
-- and only partitions above 128 used in-row pages among the 50 largest. A
-- PAGE heap, or a small PAGE partition, has no row here to join to; 030 counts
-- PAGE heaps at its root for that reason.
--
-- SQL Server 2012 is the floor. Every column read here is documented before
-- it; the file has been executed on SQL Server 2025 only.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

/* ON A READABLE SECONDARY, NO PHYSICAL READ. sys.dm_db_index_physical_stats
   takes an intent-shared lock on the table it reads, and on an availability
   group secondary that lock can block the REDO thread asking for an exclusive
   one, as the Microsoft reference for the function says: the replica falls
   behind while this file reads, for as long as it reads. LOCK_TIMEOUT bounds
   this file's waits, not REDO's. Added 27 September 2026 after a harm review;
   not reproduced, the lab having no availability group. The skip is reported
   as a reason, not as an error, so the run is not marked partial for it. A database that
   belongs to an availability group and whose replica state cannot be read is
   skipped too, and that one is reported as an error: reading on would be
   guessing that it is not a secondary. */
DECLARE @readable_secondary bit = 0, @replica_err int = 0, @replica_msg nvarchar(2048) = N'';
BEGIN TRY
    IF EXISTS (SELECT 1
               FROM sys.databases AS d
               JOIN sys.dm_hadr_availability_replica_states AS ars
                 ON ars.replica_id = d.replica_id AND ars.is_local = 1
               WHERE d.database_id = DB_ID() AND ars.role = 2)
        SET @readable_secondary = 1;
END TRY
BEGIN CATCH
    IF EXISTS (SELECT 1 FROM sys.databases
               WHERE database_id = DB_ID() AND replica_id IS NOT NULL)
        SELECT @readable_secondary = 1, @replica_err = ERROR_NUMBER(),
               @replica_msg = N'availability replica state unreadable, physical reads skipped: '
                              + ERROR_MESSAGE();
END CATCH

DECLARE @top int = 50;
DECLARE @eligible int = NULL, @indexes_covered int = NULL;
DECLARE @err int = @replica_err, @msg nvarchar(2048) = @replica_msg;
DECLARE @batch_started datetime2 = SYSDATETIME(), @budget_sec int = 1500,
        @measured int = 0, @i int = 1, @skipped_locked int = 0;
DECLARE @obj int, @idx int, @part int;
DECLARE @candidates TABLE (
    [n]                int IDENTITY(1,1) PRIMARY KEY,
    [object_id]        int    NOT NULL,
    [index_id]         int    NOT NULL,
    [partition_number] int    NOT NULL,
    [index_name]       sysname NULL,
    [index_type]       nvarchar(60) NOT NULL,
    [fill_factor]      tinyint NOT NULL,
    [data_compression] nvarchar(60) NOT NULL,
    [reserved_pages]   bigint NOT NULL,
    [in_row_reserved_pages] bigint NOT NULL,
    [lob_reserved_pages]    bigint NOT NULL
);
DECLARE @density TABLE (
    [table]           nvarchar(300) NOT NULL,
    index_name        sysname       NULL,
    index_id          int           NOT NULL,
    index_type        nvarchar(60)  NOT NULL,
    partition_number  int           NOT NULL,
    fill_factor       tinyint       NOT NULL,
    data_compression  nvarchar(60)  NOT NULL,
    reserved_mb       decimal(18,1) NOT NULL,
    in_row_reserved_mb decimal(18,1) NOT NULL,
    lob_reserved_mb   decimal(18,1) NOT NULL,
    page_count        bigint        NULL,
    page_fullness_pct decimal(5,2)  NULL,
    fragmentation_pct decimal(5,2)  NULL,
    record_count      bigint        NULL,
    compressed_page_count bigint    NULL
);

/* Read inside TRY/CATCH into a table variable, and emitted unconditionally
   below, for the reason 70.schema/020.index-usage gives: this names user
   objects, READ UNCOMMITTED does not release metadata locks, and a blocked read
   must cost this list rather than the whole document.

   A LOCK ON ONE INDEX COSTS THAT PARTITION, NOT THE LIST. The candidates used
   to come from sys.dm_db_partition_stats, which times out whole when one table
   is under a schema-modification lock, and the first timeout inside the loop
   ended the loop and marked the area failed. Measured on 17.0.4065.4 behind an
   open ALTER TABLE, 27 September 2026: sys.partitions (without its rows column)
   and sys.allocation_units read past the locked table. So the sizes come from
   the allocation units, IN_ROW_DATA being type 1 and LOB_DATA type 2, with the
   same meaning as the in_row and lob page counts of the DMV, and each partition
   is read in its own TRY/CATCH: a lock timeout, 1222, skips it and is counted
   in sample.skipped_locked; any other error ends the list and is the area's
   error, as before. A skip costs one LOCK_TIMEOUT of waiting, which the time
   budget counts like any other call. The units are joined on partition_id,
   which holds the same value as hobt_id; see 70.schema/050.heaps. */
IF @readable_secondary = 0
BEGIN
BEGIN TRY
    DECLARE @sizes TABLE (
        [object_id] int, [index_id] int, [partition_number] int,
        [index_name] sysname NULL, [index_type] nvarchar(60), [fill_factor] tinyint,
        [data_compression] nvarchar(60), [reserved_pages] bigint, [in_row_reserved_pages] bigint,
        [lob_reserved_pages] bigint);

    INSERT INTO @sizes
    SELECT p.object_id, p.index_id, p.partition_number,
           i.name, i.type_desc, i.fill_factor, p.data_compression_desc,
           a.reserved_pages, a.in_row_reserved_pages, a.lob_reserved_pages
    FROM sys.partitions AS p
    JOIN sys.indexes AS i ON i.object_id = p.object_id AND i.index_id = p.index_id
    JOIN sys.objects AS o ON o.object_id = p.object_id
    CROSS APPLY (SELECT SUM(au.total_pages) AS reserved_pages,
                        SUM(CASE WHEN au.type = 1 THEN au.total_pages ELSE 0 END) AS in_row_reserved_pages,
                        SUM(CASE WHEN au.type = 2 THEN au.total_pages ELSE 0 END) AS lob_reserved_pages,
                        SUM(CASE WHEN au.type = 1 THEN au.used_pages  ELSE 0 END) AS in_row_used_pages
                 FROM sys.allocation_units AS au
                 WHERE au.container_id = p.partition_id) AS a
    WHERE o.type IN ('U', 'V') AND o.is_ms_shipped = 0
      AND i.type IN (1, 2) AND i.is_disabled = 0 AND i.is_hypothetical = 0
      AND a.in_row_used_pages > 128
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @eligible = COUNT(*) FROM @sizes;

    INSERT INTO @candidates ([object_id], [index_id], [partition_number],
                             [index_name], [index_type], [fill_factor], [data_compression],
                             [reserved_pages], [in_row_reserved_pages], [lob_reserved_pages])
    SELECT TOP (@top)
           [object_id], [index_id], [partition_number],
           [index_name], [index_type], [fill_factor], [data_compression],
           [reserved_pages], [in_row_reserved_pages], [lob_reserved_pages]
    FROM @sizes
    ORDER BY [in_row_reserved_pages] DESC, [object_id], [index_id], [partition_number]
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

WHILE @err = 0
      AND EXISTS (SELECT 1 FROM @candidates WHERE [n] = @i)
      AND DATEDIFF(second, @batch_started, SYSDATETIME()) < @budget_sec
BEGIN
    SELECT @obj = [object_id], @idx = [index_id], @part = [partition_number]
    FROM @candidates
    WHERE [n] = @i;

    BEGIN TRY
        /* The DMV is called with scalars, not through a CROSS APPLY filtered on
           [n]: an APPLY over the candidate table would leave the optimiser free
           to invoke the function for every row and filter afterwards, which is
           50 scans per iteration instead of one. 20.databases/025.fragmentation
           reads its own partitions the same way, for the same reason. */
        INSERT INTO @density
        SELECT OBJECT_SCHEMA_NAME(@obj) + N'.' + OBJECT_NAME(@obj),
               c.index_name,
               c.index_id,
               c.index_type,
               c.partition_number,
               c.fill_factor,
               c.data_compression,
               CAST(c.reserved_pages * 8 / 1024.0 AS decimal(18,1)),
               CAST(c.in_row_reserved_pages * 8 / 1024.0 AS decimal(18,1)),
               CAST(c.lob_reserved_pages * 8 / 1024.0 AS decimal(18,1)),
               ips.page_count,
               CAST(ips.avg_page_space_used_in_percent AS decimal(5,2)),
               CAST(ips.avg_fragmentation_in_percent AS decimal(5,2)),
               ips.record_count,
               ips.compressed_page_count
        FROM sys.dm_db_index_physical_stats(DB_ID(), @obj, @idx, @part, 'SAMPLED') AS ips
        JOIN @candidates AS c ON c.[n] = @i
        WHERE ips.index_level = 0
          AND ips.alloc_unit_type_desc = N'IN_ROW_DATA'
        OPTION (RECOMPILE, MAXDOP 1);

        SET @measured += 1;
    END TRY
    BEGIN CATCH
        IF ERROR_NUMBER() = 1222
            SET @skipped_locked += 1;
        ELSE
            SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH
    SET @i += 1;
END
END

SELECT @indexes_covered = COUNT(*)
FROM (SELECT DISTINCT [table], index_id FROM @density) AS d;

SELECT DB_NAME()                                   AS [database],
       CONVERT(varchar(23), SYSDATETIME(), 126)    AS [collected_at],
       @readable_secondary                                         AS [skipped.readable_secondary],
       @top                                        AS [sample.largest_partitions_scanned],
       @measured                                   AS [sample.measured_partitions],
       @budget_sec                                 AS [sample.budget_sec],
       @skipped_locked                             AS [sample.skipped_locked],
       'SAMPLED'                                   AS [sample.mode],
       @eligible                                   AS [counts.eligible_partitions],
       @indexes_covered                            AS [counts.indexes_covered],
       CASE WHEN @err = 0 AND @readable_secondary = 0 THEN 1 ELSE 0 END AS [collected.indexes],
       @err                                        AS [errors.indexes],
       NULLIF(@msg, N'')                           AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

SELECT [table], index_name, index_id, index_type, partition_number, fill_factor,
       data_compression,
       reserved_mb, in_row_reserved_mb, lob_reserved_mb, page_count, page_fullness_pct, fragmentation_pct, record_count,
       compressed_page_count
FROM @density
ORDER BY in_row_reserved_mb DESC, [table], index_id, partition_number
OPTION (RECOMPILE, MAXDOP 1);
