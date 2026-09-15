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
-- 20.databases/020.properties reads sys.dm_db_index_physical_stats in LIMITED
-- mode, where avg_page_space_used_in_percent is NULL, and keeps only indexes
-- above 10 % logical fragmentation. Logical fragmentation is page ORDER, not
-- page FULLNESS: a table can be perfectly ordered and half empty, and it is
-- then the one a rebuild shrinks.
--
-- IT IS BEHIND A FLAG FOR COST, LIKE 041.compression-savings. SAMPLED is not
-- the 1 % read its name suggests. Measured on SQL Server 2025, from a cold
-- buffer pool, allocation unit by allocation unit: one with 10,000 pages or
-- more brings in 8 to 12 % of its pages, because each sample reads a whole
-- extent; one below 10,000 pages is read in full. The LOB and row-overflow
-- pages of the index count as units of their own and are read the same way.
-- On a large database that is tens of gigabytes pulled into the buffer pool,
-- evicting what the workload had there, and SET LOCK_TIMEOUT does not bound it.
--
-- THE TIMEOUT IS 1800 SECONDS, LIKE 041. A batch cancelled on timeout loses the whole
-- document: TRY/CATCH does not catch a client cancel, so the summary row goes
-- with the detail rows, after the buffer pool was already evicted.
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
-- SQL Server 2012 is the floor. Every column read here is documented before
-- it; the file has been executed on SQL Server 2025 only.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @top int = 50;
DECLARE @eligible int = NULL, @indexes_covered int = NULL;
DECLARE @err int = 0, @msg nvarchar(2048) = N'';
DECLARE @density TABLE (
    [table]           nvarchar(300) NOT NULL,
    index_name        sysname       NULL,
    index_id          int           NOT NULL,
    index_type        nvarchar(60)  NOT NULL,
    partition_number  int           NOT NULL,
    fill_factor       tinyint       NOT NULL,
    reserved_mb       decimal(18,1) NOT NULL,
    in_row_reserved_mb decimal(18,1) NOT NULL,
    lob_reserved_mb   decimal(18,1) NOT NULL,
    page_count        bigint        NULL,
    page_fullness_pct decimal(5,2)  NULL,
    fragmentation_pct decimal(5,2)  NULL,
    record_count      bigint        NULL
);

/* Read inside TRY/CATCH into a table variable, and emitted unconditionally
   below, for the reason 70.schema/020.index-usage gives: this names user
   objects, READ UNCOMMITTED does not release metadata locks, and a blocked read
   must cost this list rather than the whole document. */
BEGIN TRY
    SELECT @eligible = COUNT(*)
    FROM sys.dm_db_partition_stats AS ps
    JOIN sys.indexes AS i ON i.object_id = ps.object_id AND i.index_id = ps.index_id
    JOIN sys.objects AS o ON o.object_id = ps.object_id
    WHERE o.type IN ('U', 'V') AND o.is_ms_shipped = 0
      AND i.type IN (1, 2) AND i.is_disabled = 0 AND i.is_hypothetical = 0
      AND ps.in_row_used_page_count > 128
    OPTION (RECOMPILE, MAXDOP 1);

    INSERT INTO @density
    SELECT OBJECT_SCHEMA_NAME(c.object_id) + N'.' + OBJECT_NAME(c.object_id),
           c.index_name,
           c.index_id,
           c.index_type,
           c.partition_number,
           c.fill_factor,
           CAST(c.reserved_pages * 8 / 1024.0 AS decimal(18,1)),
           CAST(c.in_row_reserved_pages * 8 / 1024.0 AS decimal(18,1)),
           CAST(c.lob_reserved_pages * 8 / 1024.0 AS decimal(18,1)),
           ips.page_count,
           CAST(ips.avg_page_space_used_in_percent AS decimal(5,2)),
           CAST(ips.avg_fragmentation_in_percent AS decimal(5,2)),
           ips.record_count
    FROM (
        SELECT TOP (@top)
               ps.object_id, ps.index_id, ps.partition_number,
               i.name                 AS index_name,
               i.type_desc            AS index_type,
               i.fill_factor,
               ps.reserved_page_count        AS reserved_pages,
               ps.in_row_reserved_page_count AS in_row_reserved_pages,
               ps.lob_reserved_page_count    AS lob_reserved_pages
        FROM sys.dm_db_partition_stats AS ps
        JOIN sys.indexes AS i ON i.object_id = ps.object_id AND i.index_id = ps.index_id
        JOIN sys.objects AS o ON o.object_id = ps.object_id
        WHERE o.type IN ('U', 'V') AND o.is_ms_shipped = 0
          AND i.type IN (1, 2) AND i.is_disabled = 0 AND i.is_hypothetical = 0
          AND ps.in_row_used_page_count > 128
        ORDER BY ps.in_row_reserved_page_count DESC, ps.object_id, ps.index_id, ps.partition_number
    ) AS c
    CROSS APPLY sys.dm_db_index_physical_stats(DB_ID(), c.object_id, c.index_id, c.partition_number, 'SAMPLED') AS ips
    WHERE ips.index_level = 0
      AND ips.alloc_unit_type_desc = N'IN_ROW_DATA'
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT @indexes_covered = COUNT(*)
FROM (SELECT DISTINCT [table], index_id FROM @density) AS d;

SELECT DB_NAME()                                   AS [database],
       CONVERT(varchar(23), SYSDATETIME(), 126)    AS [collected_at],
       @top                                        AS [sample.largest_partitions_scanned],
       'SAMPLED'                                   AS [sample.mode],
       @eligible                                   AS [counts.eligible_partitions],
       @indexes_covered                            AS [counts.indexes_covered],
       CASE WHEN @err = 0 THEN 1 ELSE 0 END        AS [collected.indexes],
       @err                                        AS [errors.indexes],
       NULLIF(@msg, N'')                           AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

SELECT [table], index_name, index_id, index_type, partition_number, fill_factor,
       reserved_mb, in_row_reserved_mb, lob_reserved_mb, page_count, page_fullness_pct, fragmentation_pct, record_count
FROM @density
ORDER BY in_row_reserved_mb DESC, [table], index_id, partition_number
OPTION (RECOMPILE, MAXDOP 1);
