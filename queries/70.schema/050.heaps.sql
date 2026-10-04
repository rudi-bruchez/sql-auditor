-- @scope:       database
-- @resultsets:  root:object, heaps:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @optional_permissions: VIEW SERVER STATE
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
-- VIEW SERVER STATE IS OPTIONAL. The physical reads need it
-- (sys.dm_db_index_physical_stats, sys.dm_db_partition_stats, and the replica
-- state that decides whether they may run), and all of them sit inside TRY.
-- A login refused it still gets the counts of the root, read from the catalog
-- and the allocation units: how many heaps, how many carry nonclustered
-- indexes, and their total size. The heaps array is empty, collected.heaps is
-- false with the error, and the manifest lists the file under
-- reduced_scripts.
--
-- WHY A SEPARATE SCAN. forwarded_record_count is NULL in the LIMITED mode that
-- 20.databases/025.fragmentation uses: the count requires SAMPLED or
-- DETAILED, which reads pages rather than metadata. Adding the column to that
-- query would have returned NULL on every row without saying why — the exact
-- shape of failure this corpus tries hardest to avoid.
--
-- THE COST IS BOUNDED BY A PAGE BUDGET, NOT BY THE MODE. The reference says
-- SAMPLED reads a 1 percent sample, and that an index or heap of fewer than
-- 10,000 pages is read in DETAILED mode instead. Measured on SQL Server 2025
-- against a 2403-page heap: SAMPLED and DETAILED both returned record_count
-- 60000, exactly the row count. So most heaps in a database are read in full.
-- Above the threshold the 1 percent holds for a heap: from a buffer pool emptied
-- by taking the database offline, SAMPLED brought 265 pages of a 25,000-page
-- heap into the buffer pool, and 159 of a 15,110-page heap whose extents were
-- interleaved with another table's, 27 September 2026.
--
-- The cap of 100 heaps is a count, and a count bounds nothing: a hundred heaps
-- of 9,000 pages are 900,000 pages read in full. So each candidate is priced
-- before it is read, allocation unit by allocation unit from metadata, at its
-- used pages below 10,000 and at 1 percent of them from there on, and the
-- heaps are read largest first until @page_budget would be exceeded. A heap
-- that does not fit is skipped and counted in sample.skipped_budget, and a
-- smaller one after it may still fit. The budget and the pages actually spent
-- are projected in the root beside the cap, so a reader knows the list is not
-- exhaustive and why. The LIMITED call made for fragmentation reads allocation
-- pages only and is not priced. A heap is still read whole or not at all: the
-- budget decides which heaps are read, not how much of one.
--
-- THE CAP WAS 50 UNTIL OCTOBER 2026, AND NOTHING SAID HOW MANY HEAPS IT LEFT.
-- counts.eligible_partitions now counts the heap partitions above 128 used
-- pages, the population the candidates are taken from, so the cap reads
-- against it: measured_heaps, skipped_budget and skipped_locked say what
-- happened to the first 100, and eligible_partitions how many there were. It
-- is read from the catalog and the allocation units, like the candidates, so
-- a locked heap does not cost it. Twelve real collections taken on eight
-- client instances between August and September 2026 (docs/caps-inventory.md)
-- had the 50 bind in 4 databases under 0.18.0, holding 108 to 810 heaps, and
-- in none of those taken by 0.21.0 or 0.23.0. The page and time budgets
-- are unchanged, so raising the count to 100 reads more heaps only where the
-- budgets have room for them: the worst case is still 200,000 pages and
-- 240 s.
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
-- COMPRESSED_PAGE_COUNT SAYS HOW MUCH OF A PAGE HEAP IS COMPRESSED NOW. The
-- SAMPLED call above already returns it, so projecting it reads nothing more:
-- the candidates, the page budget and the pages read are unchanged. A heap set
-- to PAGE compresses its pages when it is rebuilt or loaded WITH (TABLOCK);
-- pages that ordinary inserts add stay row-only. 70.schema/030.index-operational
-- sees that only through its counters, which a rebuild, a restart or an
-- eviction of the metadata starts again, and none of which says how many
-- pages are compressed at present. Measured through both files on
-- 17.0.4065.4, 4 October 2026, on one database:
--
--   ordinary inserts only         0 attempts,   0 successes,   0 of 3062 compressed
--   rebuilt                     420 attempts, 228 successes, 217 of 218 compressed
--   random rows, rebuilt        526 attempts,   0 successes,   0 of 527 compressed
--   rebuilt, then inserts       422 attempts, 228 successes, 217 of 539 compressed
--
-- The fourth is the case this column exists for: in 030 it reads like the
-- second, and the 322 pages its inserts added since the rebuild are row-only.
-- The rows of the two files meet on the database, the table (schema.name in
-- both), this file's partition against 030's partition_number, and index_id
-- 0, which this file does not project because every row here is a heap.
--
-- data_compression is projected beside it, taken from sys.partitions with the
-- candidate, which reads past a locked heap. A zero says something only on a
-- PAGE heap: the same rows rebuilt under ROW read 0 of 3063, as every ROW or
-- NONE heap will.
--
-- ON A HEAP ABOVE 10,000 PAGES THE COUNT IS THE SAMPLE'S, SCALED UP, and so is
-- page_count beside it. A heap loaded WITH (TABLOCK) read 14,700 of 14,700 in
-- SAMPLED and 14,603 of 14,603 in DETAILED. Read it as a share of page_count,
-- not as an exact number of pages.
--
-- It covers the heaps this file reads and no others: the 100 largest
-- partitions above 128 used pages that fit the page budget. A smaller PAGE
-- heap has no row here; 030 counts the row-only shape of all of them at its
-- root, in page_partitions.heaps_without_attempts.
--
-- SQL Server 2012 is the floor. forwarded_record_count predates it, and so do
-- compressed_page_count and sys.partitions.data_compression_desc, both from
-- SQL Server 2008. Not collected for that reason: nothing.
--
-- IT IS BLOCKABLE, AND THE READS ARE THEREFORE BUFFERED. This file had no
-- TRY/CATCH at all, so one lock timeout anywhere lost the whole document: the
-- run reported an error and the archive held no 050.heaps.json, with no word
-- about why. Measured on 17.0.4065.4 behind a Sch-M held on one heap by an open
-- ALTER TABLE, 27 September 2026. Each area now reads inside its own
-- TRY/CATCH, the root is emitted from variables, and a blocked area comes back
-- empty with its error number, as 030.index-operational does.
--
-- A LOCK ON ONE HEAP COSTS THAT HEAP, NOT THE LIST. Behind a Sch-M held by an
-- open ALTER TABLE, sys.dm_db_partition_stats times out as a whole, and so does
-- the rows column of sys.partitions, while sys.allocation_units and
-- sys.partitions without rows read past the locked object; measured on
-- 17.0.4065.4, 27 September 2026. So the candidates and the total size are
-- taken from those two, and everything that names or reads one heap
-- (OBJECT_NAME, the row count, both calls to the DMV) runs per heap inside its
-- own TRY/CATCH. A lock timeout there, 1222, skips that heap and counts it in
-- sample.skipped_locked; any other error ends the list and is reported as the
-- area's error, as before. Each skipped heap costs a LOCK_TIMEOUT of waiting,
-- so no new heap is started once @budget_sec have passed since the batch
-- began, well inside @timeout, and sample.measured_heaps says how many were
-- read.

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
    /* Assignments rather than IF EXISTS, which cannot carry the query hint:
       a SELECT that finds no row leaves the variables as they were. */
    SELECT @readable_secondary = 1
    FROM sys.databases AS d
    JOIN sys.dm_hadr_availability_replica_states AS ars
      ON ars.replica_id = d.replica_id AND ars.is_local = 1
    WHERE d.database_id = DB_ID() AND ars.role = 2
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @readable_secondary = 1, @replica_err = ERROR_NUMBER(),
           @replica_msg = N'availability replica state unreadable, physical reads skipped: '
                          + ERROR_MESSAGE()
    FROM sys.databases
    WHERE database_id = DB_ID() AND replica_id IS NOT NULL
    OPTION (RECOMPILE, MAXDOP 1);
END CATCH

DECLARE @err_counts int = 0, @err_heaps int = @replica_err, @msg nvarchar(2048) = @replica_msg,
        @heap_count int, @heaps_with_nc int, @heap_total_mb decimal(18,1),
        @eligible int;

DECLARE @top int = 100, @page_budget bigint = 200000, @pages_spent bigint = 0,
        @batch_started datetime2 = SYSDATETIME(), @budget_sec int = 240,
        @measured int = 0, @skipped_locked int = 0, @skipped_budget int = 0,
        @i int = 1, @candidate_count int = 0, @obj int, @pid bigint, @part int, @est_pages bigint,
        @compression nvarchar(60);

/* The heap partitions to read, largest first, with what each is expected to
   cost. The reference joins sys.allocation_units on the hobt id for in-row and
   row-overflow units and on the partition id for LOB units; the two ids hold
   the same value (207 partitions of 207 on the lab), so the joins below use
   partition_id alone, as the filegroup lookup further down always has, and
   keep an equality the optimiser can seek on. */
DECLARE @candidates TABLE (
    [n]                int IDENTITY(1,1) PRIMARY KEY,
    [object_id]        int    NOT NULL,
    [partition_id]     bigint NOT NULL,
    [partition_number] int    NOT NULL,
    [used_pages]       bigint NOT NULL,
    [est_pages]        bigint NOT NULL,
    [data_compression] nvarchar(60) NOT NULL);

DECLARE @heaps TABLE (
    [table]                     nvarchar(300),
    [partition]                 int,
    [rows]                      bigint,
    [used_mb]                   decimal(18,1),
    [page_count]                bigint,
    [forwarded_records]         bigint,
    [forwarded_percent_of_rows] decimal(9,3),
    [fragmentation_pct]         decimal(5,2),
    [page_fullness_pct]         decimal(5,2),
    [records_scanned]           bigint,
    [nonclustered_indexes]      int,
    [partition_count]           int,
    [filegroup]                 sysname NULL,
    [data_compression]          nvarchar(60),
    [compressed_page_count]     bigint);

BEGIN TRY
    -- Every heap, whether or not it was sampled below.
    SELECT @heap_count = COUNT(*) FROM sys.indexes AS i
       JOIN sys.objects AS o ON o.object_id = i.object_id
      WHERE i.index_id = 0 AND o.type = 'U' AND o.is_ms_shipped = 0
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @heaps_with_nc = COUNT(*) FROM sys.indexes AS i
       JOIN sys.objects AS o ON o.object_id = i.object_id
      WHERE i.index_id = 0 AND o.type = 'U' AND o.is_ms_shipped = 0
        AND EXISTS (SELECT 1 FROM sys.indexes AS ni
                     WHERE ni.object_id = i.object_id AND ni.index_id > 0)
    OPTION (RECOMPILE, MAXDOP 1);

    -- From the allocation units rather than sys.dm_db_partition_stats, which
    -- times out whole on one locked heap (see the header). used_pages summed
    -- over a partition's units is the used_page_count of the DMV.
    SELECT @heap_total_mb = CAST(SUM(au.used_pages) * 8 / 1024.0 AS DECIMAL(18,1))
       FROM sys.partitions AS p
       JOIN sys.objects AS o ON o.object_id = p.object_id
       JOIN sys.allocation_units AS au ON au.container_id = p.partition_id
      WHERE p.index_id = 0 AND o.type = 'U' AND o.is_ms_shipped = 0
    OPTION (RECOMPILE, MAXDOP 1);

    -- The population the candidates below are taken from, before their TOP:
    -- the same filter on the same two catalogs, so the two cannot disagree.
    SELECT @eligible = COUNT(*)
       FROM sys.partitions AS p
       JOIN sys.objects AS o ON o.object_id = p.object_id
       CROSS APPLY (SELECT SUM(au.used_pages) AS used_pages
                    FROM sys.allocation_units AS au
                    WHERE au.container_id = p.partition_id) AS a
      WHERE p.index_id = 0 AND o.type = 'U' AND o.is_ms_shipped = 0
        AND a.used_pages > 128
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_counts = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* The hundred largest heaps, scanned in SAMPLED mode. Chosen by page count from
   metadata first, so the expensive scan only touches the objects that could
   matter. The estimate follows the reference: a unit of fewer than 10,000 pages
   is read whole, a larger one at 1 percent. */
IF @readable_secondary = 0
BEGIN
BEGIN TRY
    INSERT INTO @candidates ([object_id], [partition_id], [partition_number],
                             [used_pages], [est_pages], [data_compression])
    SELECT TOP (@top) p.object_id, p.partition_id, p.partition_number,
           a.used_pages, a.est_pages, p.data_compression_desc
    FROM sys.partitions AS p
    JOIN sys.objects AS o ON o.object_id = p.object_id
    CROSS APPLY (SELECT SUM(au.used_pages) AS used_pages,
                        SUM(CASE WHEN au.used_pages < 10000 THEN au.used_pages
                                 ELSE CEILING(au.used_pages / 100.0) END) AS est_pages
                 FROM sys.allocation_units AS au
                 WHERE au.container_id = p.partition_id) AS a
    WHERE p.index_id = 0 AND o.type = 'U' AND o.is_ms_shipped = 0
      AND a.used_pages > 128
    ORDER BY a.used_pages DESC, p.object_id, p.partition_number
    OPTION (RECOMPILE, MAXDOP 1);
    SET @candidate_count = @@ROWCOUNT;
END TRY
BEGIN CATCH
    SELECT @err_heaps = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* The loop runs while @i <= @candidate_count, the number of rows the INSERT above put
   in the candidates. [n] is an IDENTITY filled by that one INSERT, so it runs
   1 to @candidate_count without a gap, and this is the test EXISTS (SELECT 1 FROM the
   candidates WHERE [n] = @i) made; but a subquery in a WHILE cannot carry
   OPTION (RECOMPILE, MAXDOP 1), and the contract wants it on every read. */
WHILE @err_heaps = 0
      AND @i <= @candidate_count
      AND DATEDIFF(second, @batch_started, SYSDATETIME()) < @budget_sec
BEGIN
    SELECT @obj = [object_id], @pid = [partition_id], @part = [partition_number],
           @est_pages = [est_pages], @compression = [data_compression]
    FROM @candidates
    WHERE [n] = @i
    OPTION (RECOMPILE, MAXDOP 1);
    SET @i += 1;

    IF @pages_spent + @est_pages > @page_budget
    BEGIN
        SET @skipped_budget += 1;
        CONTINUE;
    END

BEGIN TRY
INSERT INTO @heaps
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
    -- hundred largest heaps, and within those only the partitions that passed
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
      WHERE au.container_id = h.partition_id AND au.type = 1) AS [filegroup],
    -- Read with the candidate from sys.partitions, which reads past a locked
    -- heap; see the header for why it travels with the next column.
    @compression                                                AS [data_compression],
    -- Returned by the SAMPLED call already made for forwarded_record_count, so
    -- it costs no read. What it settles is in the header.
    ips.compressed_page_count                                   AS [compressed_page_count]
-- The row count comes from sys.dm_db_partition_stats for this one partition,
-- inside the per-heap TRY: read for the whole list it blocks on any locked heap.
-- The object_id predicate is what confines it. Filtered on partition_id alone,
-- the DMV still visited the locked heap and every heap timed out; measured.
FROM (
    SELECT ps.object_id,
           ps.partition_id,
           ps.partition_number,
           ps.row_count                                  AS rows,
           ps.used_page_count * 8 / 1024.0               AS used_mb
    FROM sys.dm_db_partition_stats AS ps
    WHERE ps.object_id = @obj AND ps.partition_id = @pid
) AS h
CROSS APPLY sys.dm_db_index_physical_stats(DB_ID(), @obj, 0, @part, 'SAMPLED') AS ips
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
    FROM sys.dm_db_index_physical_stats(DB_ID(), @obj, 0, @part, 'LIMITED') AS l
    WHERE l.alloc_unit_type_desc = N'IN_ROW_DATA'
) AS lim
WHERE ips.alloc_unit_type_desc = N'IN_ROW_DATA'
OPTION (RECOMPILE, MAXDOP 1);

    SELECT @pages_spent += @est_pages, @measured += 1;
END TRY
BEGIN CATCH
    IF ERROR_NUMBER() = 1222
        SET @skipped_locked += 1;
    ELSE
        SELECT @err_heaps = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH
END
END

SELECT
    DB_NAME()                                                   AS [database],
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    @heap_count                                                 AS [counts.heaps],
    @heaps_with_nc                                              AS [counts.heaps_with_nonclustered],
    @heap_total_mb                                              AS [counts.total_mb],
    -- Heap partitions above 128 used pages, the population the cap below is
    -- taken from.
    @eligible                                                   AS [counts.eligible_partitions],
    -- The cap, so a reader never mistakes the list below for the whole story.
    @top                                                        AS [sample.largest_heaps_scanned],
    'SAMPLED'                                                   AS [sample.mode],
    -- How many of those were read, and why the others were not.
    @measured                                                   AS [sample.measured_heaps],
    @skipped_locked                                             AS [sample.skipped_locked],
    @skipped_budget                                             AS [sample.skipped_budget],
    @page_budget                                                AS [sample.page_budget],
    @pages_spent                                                AS [sample.estimated_pages_read],
    @budget_sec                                                 AS [sample.budget_sec],
       @readable_secondary                                         AS [skipped.readable_secondary],
    CASE WHEN @err_counts = 0 THEN 1 ELSE 0 END                 AS [collected.counts],
    CASE WHEN @err_heaps  = 0 AND @readable_secondary = 0 THEN 1 ELSE 0 END AS [collected.heaps],
    @err_counts                                                 AS [errors.counts],
    @err_heaps                                                  AS [errors.heaps],
    NULLIF(@msg, N'')                                           AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

SELECT h.[table], h.[partition], h.[rows], h.[used_mb], h.[page_count],
       h.[forwarded_records], h.[forwarded_percent_of_rows], h.[fragmentation_pct],
       h.[page_fullness_pct], h.[records_scanned], h.[nonclustered_indexes],
       h.[partition_count], h.[filegroup], h.[data_compression],
       h.[compressed_page_count]
FROM @heaps AS h
ORDER BY h.[forwarded_records] DESC, h.[used_mb] DESC
OPTION (RECOMPILE, MAXDOP 1);
