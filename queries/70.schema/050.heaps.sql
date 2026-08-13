-- @scope:       database
-- @resultsets:  root:object, heaps:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     300
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
-- 020.properties.sql uses for fragmentation: the count requires SAMPLED or
-- DETAILED, which reads pages rather than metadata. Adding the column to that
-- query would have returned NULL on every row without saying why — the exact
-- shape of failure this corpus tries hardest to avoid.
--
-- THE COST IS BOUNDED ON PURPOSE. SAMPLED reads about 1% of pages, and the
-- scan is applied only to the largest heaps rather than to every object, so
-- the work is proportional to what is worth measuring. The cap is projected in
-- the root so a reader knows the list is not exhaustive. On a database of
-- small heaps this collector is nearly free; on one with a 200 GB heap it is
-- not, which is why the timeout is generous and the cap is low.
--
-- NO JUDGEMENT IS APPLIED. A heap is not a defect. Staging tables that are
-- truncated and bulk-loaded are legitimately heaps, and rebuilding one that is
-- emptied nightly would be work for nothing. What makes a forwarded record
-- expensive is being read often, which is what forwarded_fetches in
-- 030.index-operational.sql measures — the two files answer one question
-- between them, and neither answers it alone.
--
-- A HEAP HAS NO REBUILD THAT IS FREE. ALTER TABLE ... REBUILD removes the
-- forwarding, and in Standard Edition it is offline. That belongs in the
-- analysis, not here; this file reports the count and the size.
--
-- SQL Server 2012 is the floor. forwarded_record_count predates it. Not
-- collected for that reason: nothing.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

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
    CAST(ips.avg_fragmentation_in_percent AS DECIMAL(5,2))      AS [fragmentation_pct],
    CAST(ips.avg_page_space_used_in_percent AS DECIMAL(5,2))    AS [page_fullness_pct],
    ips.record_count                                            AS [records_scanned],
    -- How many non-clustered indexes ride on this heap. Rebuilding a heap
    -- rebuilds none of them, but every one of them stores the heap's physical
    -- row locator, so their size is part of the decision.
    (SELECT COUNT(*) FROM sys.indexes AS ni
      WHERE ni.object_id = h.object_id AND ni.index_id > 0)     AS [nonclustered_indexes]
FROM (
    SELECT TOP (50)
           ps.object_id,
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
ORDER BY ips.forwarded_record_count DESC, h.used_mb DESC
OPTION (RECOMPILE, MAXDOP 1);
