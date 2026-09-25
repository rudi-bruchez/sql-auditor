-- @scope:       database
-- @resultsets:  root:object, heaps:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     300
-- @profiles:    space
--
-- Tables with no clustered index, and how many of their rows have been
-- forwarded out of place.
--
-- Why this collector exists, and why it is not a column added to an existing
-- one. 030.index-operational.sql already reports forwarded_fetches: the number
-- of times a forwarded pointer was followed, accumulated since the instance
-- started. That is the cost already paid. It is not the same quantity as
-- forwarded_record_count, which is how many redirected rows exist right now —
-- and it is the second that says whether rebuilding a heap today would fix
-- anything. An audit that has only the first has to write "redirections
-- followed" everywhere and cannot answer "is it worth acting".
--
-- WHY A SEPARATE SCAN. forwarded_record_count is NULL in the LIMITED mode that
-- 20.databases/025.fragmentation uses: the count requires SAMPLED or
-- DETAILED, which reads pages rather than metadata. Adding the column to that
-- query would have returned NULL on every row without saying why — the exact
-- shape of failure this corpus tries hardest to avoid.
--
-- THE COST IS BOUNDED BY THE CAP, NOT BY THE MODE. SAMPLED samples 1% of pages
-- only above 10000 leaf pages; at or below that threshold the engine reads
-- every page and answers as DETAILED would. Measured on SQL Server 2025 against
-- a 2403-page heap: SAMPLED and DETAILED both returned record_count 60000,
-- exactly the row count. So most heaps in a database are read in full, and what
-- keeps this collector cheap is the cap on how many of them are scanned, not
-- the mode. The cap is projected in the root so a reader knows the list is not
-- exhaustive. On a database of small heaps this collector is nearly free; on
-- one with a 200 GB heap it is not, which is why the timeout is generous and
-- the cap is low.
--
-- NO JUDGEMENT IS APPLIED. A heap is not a defect. Staging tables that are
-- truncated and bulk-loaded are legitimately heaps, and rebuilding one that is
-- emptied nightly would be work for nothing. What makes a forwarded record
-- expensive is being read often, which is what forwarded_fetches in
-- 030.index-operational.sql measures — the two files answer one question
-- between them, and neither answers it alone.
--
-- A HEAP HAS NO REBUILD THAT IS FREE. ALTER TABLE ... REBUILD removes the
-- forwarding, in Standard Edition it is offline, and it rebuilds every
-- non-clustered index on the table as well, because each of them holds the
-- physical row locator that the rebuild changes. The count of those indexes is
-- therefore part of the price, which is why it is projected per heap below.
-- That belongs in the analysis, not here; this file reports the count and the
-- size.
--
-- SQL Server 2012 is the floor. forwarded_record_count predates it. Not
-- collected for that reason: nothing.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT
    DB_NAME()                                                   AS [database],
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    -- Every heap, whether or not it was sampled below.
    (SELECT COUNT(*) FROM sys.indexes AS i
       JOIN sys.objects AS o ON o.object_id = i.object_id
      WHERE i.index_id = 0 AND o.type = 'U' AND o.is_ms_shipped = 0)
                                                                AS [counts.heaps],
    (SELECT COUNT(*) FROM sys.indexes AS i
       JOIN sys.objects AS o ON o.object_id = i.object_id
      WHERE i.index_id = 0 AND o.type = 'U' AND o.is_ms_shipped = 0
        AND EXISTS (SELECT 1 FROM sys.indexes AS ni
                     WHERE ni.object_id = i.object_id AND ni.index_id > 0))
                                                                AS [counts.heaps_with_nonclustered],
    (SELECT CAST(SUM(ps.used_page_count) * 8 / 1024.0 AS DECIMAL(18,1))
       FROM sys.dm_db_partition_stats AS ps
       JOIN sys.indexes AS i ON i.object_id = ps.object_id AND i.index_id = ps.index_id
       JOIN sys.objects AS o ON o.object_id = i.object_id
      WHERE i.index_id = 0 AND o.type = 'U' AND o.is_ms_shipped = 0)
                                                                AS [counts.total_mb],
    -- The cap, so a reader never mistakes the list below for the whole story.
    50                                                          AS [sample.largest_heaps_scanned],
    'SAMPLED'                                                   AS [sample.mode]
OPTION (RECOMPILE, MAXDOP 1);

/* The fifty largest heaps, scanned in SAMPLED mode. Chosen by page count from
   metadata first, so the expensive scan only touches the objects that could
   matter. */
SELECT
    OBJECT_SCHEMA_NAME(h.object_id) + '.' + OBJECT_NAME(h.object_id) AS [table],
    h.partition_number                                          AS [partition],
    h.rows                                                      AS [rows],
    CAST(h.used_mb AS DECIMAL(18,1))                            AS [used_mb],
    ips.page_count                                              AS [page_count],
    -- The count that says whether acting today is justified.
    ips.forwarded_record_count                                  AS [forwarded_records],
    -- Its share of the rows, because ten thousand forwarded rows in a billion
    -- is noise and ten thousand in twenty thousand is the table.
    CAST(CASE WHEN h.rows > 0
              THEN ips.forwarded_record_count * 100.0 / h.rows
         END AS DECIMAL(9,3))                                   AS [forwarded_percent_of_rows],
    -- FROM THE LIMITED CALL, NOT THE SAMPLED ONE, AND THAT IS THE WHOLE POINT.
    -- SAMPLED does not measure extent fragmentation on a heap. The reference
    -- says the column is NULL for heaps in SAMPLED mode; measured, it is worse
    -- than that, because the engine returns 0.0. A NULL would have read as "not
    -- measured" and a 0.00 reads as "not fragmented", so this collector spent
    -- its life reporting every heap in the estate as perfectly ordered.
    -- Measured twice, on 16.0.4265.3 and on 17.0.4065.4, on a heap built for
    -- the question and then emptied of one row in three: SAMPLED returned 0.0
    -- where LIMITED and DETAILED both returned the real figure, 3.33 and 97.56
    -- respectively on the two shapes tried.
    --
    -- The two modes cannot be merged into one call. forwarded_record_count and
    -- record_count are NULL in LIMITED, and they are what this file exists for;
    -- avg_fragmentation_in_percent is only populated in LIMITED or DETAILED.
    -- So the cheap mode is called a second time for this one column. LIMITED
    -- reads allocation metadata rather than the data pages, which is why it can
    -- be afforded per heap and why DETAILED cannot.
    CAST(lim.avg_fragmentation_in_percent AS DECIMAL(5,2))      AS [fragmentation_pct],
    CAST(ips.avg_page_space_used_in_percent AS DECIMAL(5,2))    AS [page_fullness_pct],
    ips.record_count                                            AS [records_scanned],
    -- How many non-clustered indexes ride on this heap. Every one of them
    -- stores the heap's physical row locator, so ALTER TABLE ... REBUILD has to
    -- rebuild all of them too: moving every row gives every locator a new
    -- value. That is the cost of the rebuild, and it is proportional to this
    -- number. Measured on SQL Server 2025, one heap and one non-clustered
    -- index: before the rebuild the index sat at 73.1% fragmentation over 212
    -- pages, after it at 28.9% over 149. It was rebuilt, not left alone.
    (SELECT COUNT(*) FROM sys.indexes AS ni
      WHERE ni.object_id = h.object_id AND ni.index_id > 0)     AS [nonclustered_indexes],
    -- How many partitions the heap has, which decides whether a rebuild may be
    -- written without a PARTITION clause. The two mistakes are not
    -- symmetrical: PARTITION = n on a heap that has one fails loudly and
    -- changes nothing, while omitting it on a heap that has forty rebuilds all
    -- forty in silence.
    --
    -- It cannot be deduced from the rows of this result set. The list is the
    -- fifty largest heaps, and within those only the partitions that passed
    -- the page-count filter, so a single row means either one partition or
    -- siblings that did not make the cut. This reads metadata, so neither the
    -- cap nor the SAMPLED scan reaches it.
    (SELECT COUNT(*) FROM sys.partitions AS pc
      WHERE pc.object_id = h.object_id AND pc.index_id = 0)     AS [partition_count],
    -- The filegroup this partition of the heap sits on, which is the one a
    -- rebuild has to find room in. Read through the allocation units: on a
    -- partitioned heap sys.indexes carries a partition scheme id, which names
    -- no filegroup and overflows the smallint FILEGROUP_NAME takes.
    (SELECT TOP (1) ds.name
       FROM sys.allocation_units AS au
       JOIN sys.data_spaces      AS ds ON ds.data_space_id = au.data_space_id
      WHERE au.container_id = h.partition_id AND au.type = 1) AS [filegroup]
FROM (
    SELECT TOP (50)
           ps.object_id,
           ps.partition_id,
           ps.partition_number,
           ps.row_count                                  AS rows,
           ps.used_page_count * 8 / 1024.0               AS used_mb
    FROM sys.dm_db_partition_stats AS ps
    JOIN sys.indexes AS i ON i.object_id = ps.object_id AND i.index_id = ps.index_id
    JOIN sys.objects AS o ON o.object_id = i.object_id
    WHERE i.index_id = 0 AND o.type = 'U' AND o.is_ms_shipped = 0
      AND ps.used_page_count > 128
    ORDER BY ps.used_page_count DESC
) AS h
CROSS APPLY sys.dm_db_index_physical_stats(DB_ID(), h.object_id, 0, h.partition_number, 'SAMPLED') AS ips
-- One row per allocation unit, not one per partition. A heap holding a LOB or a
-- row-overflow column produces two or three rows for the same partition, and
-- without this filter each of them became a row of this result set: the same
-- table listed twice, the second time with a NULL forwarded_records and a
-- page_count that is the size of the LOB chain rather than of the heap.
-- Measured on SQL Server 2025, one table with a varchar(max) column returned
-- IN_ROW_DATA at 5488 pages and LOB_DATA at 13720. Forwarding is a property of
-- in-row data alone, so that is the only unit this collector has ever meant.
-- The filegroup lookup above already reads au.type = 1 for the same reason.
/* OUTER, not CROSS: a heap whose fragmentation cannot be read must still be
   listed with its forwarded records, which are the reason this file exists. The
   IN_ROW_DATA filter is repeated here because this call returns one row per
   allocation unit too. */
OUTER APPLY (
    SELECT TOP (1) l.avg_fragmentation_in_percent
    FROM sys.dm_db_index_physical_stats(DB_ID(), h.object_id, 0,
                                        h.partition_number, 'LIMITED') AS l
    WHERE l.alloc_unit_type_desc = N'IN_ROW_DATA'
) AS lim
WHERE ips.alloc_unit_type_desc = N'IN_ROW_DATA'
ORDER BY ips.forwarded_record_count DESC, h.used_mb DESC
OPTION (RECOMPILE, MAXDOP 1);
