-- @scope:       database
-- @resultsets:  root:object, fragmentation:array
-- @permissions: CONNECT, VIEW SERVER STATE, VIEW ANY DEFINITION
-- @timeout:     300
-- @profiles:    space
--
-- Runs once per user database, with the connection context switched to it.
--
-- Logical fragmentation of the largest index partitions, in LIMITED mode, the
-- ones above 1000 pages and 10 %.
--
-- WHY THIS IS A FILE OF ITS OWN. It used to be the seventh result set of
-- 20.databases/020.properties, and the only expensive one: the six others are
-- catalog reads that answer in milliseconds, this one calls
-- sys.dm_db_index_physical_stats, which reads the level above the leaf of every
-- index it visits. @timeout is enforced by the client, which cancels the
-- batch, and a cancelled batch returns none of its result sets however well
-- the areas inside it were guarded. Measured in the field in September 2026 on
-- a database of 2.4 TB: 020.properties ran out of its 300 seconds, and the
-- files, their autogrowth settings and the largest objects went with the
-- fragmentation, for a reason that had nothing to do with any of them. The
-- per-partition budget below makes that rarer and cannot rule it out, since it
-- bounds the number of calls and not the length of one. Apart, a timeout here
-- costs this file only.
--
-- The field names are those it had inside 020.properties, fragmentation and
-- fragmentation_sample, so a reader that knew them there only has to look in a
-- different file. An archive older than this split has no such file and
-- carries the same fields in 020.properties.json.
--
-- THE BUDGET STAYS. Alone in its file, the fragmentation read no longer puts
-- anything else at risk, but a cancelled batch still returns nothing of what
-- it had measured. So it measures one partition at a time, the largest first,
-- stops starting new ones once @frag_budget_sec have passed since the batch
-- began, and says in fragmentation_sample how many it measured out of how many
-- were eligible. One call on a single enormous partition can still outlast the
-- remainder; the budget bounds the number of calls, not the length of one.
--
-- The root is emitted from VARIABLES and not from a buffered row, as in
-- 020.properties: a root that returns no rows is skipped by the encoder, and a
-- blocked read would then leave a document with no word about why.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @err_fragmentation int = 0, @msg nvarchar(2048) = N'';

DECLARE @batch_started datetime2 = SYSDATETIME(), @frag_budget_sec int = 150,
        @frag_eligible int = NULL, @frag_measured int = 0, @frag_i int = 1,
        @frag_object int, @frag_index int, @frag_partition int;

/* The partitions the fragmentation read will visit, largest first. */
DECLARE @frag_candidates TABLE (
    [n]                 int IDENTITY(1,1) PRIMARY KEY,
    [object_id]         int,
    [index_id]          int,
    [partition_number]  int);

DECLARE @fragmentation TABLE (
    [sort_frag]         float,
    [table]             nvarchar(300) NULL,
    [index_name]        sysname NULL,
    [index_type]        nvarchar(60),
    [partition_number]  int,
    [page_count]        bigint,
    [fragmentation_pct] decimal(5,2));

/* One partition per call, the 100 largest by used pages, until the budget runs
   out. The candidates come from sys.dm_db_partition_stats, which is metadata;
   used_page_count covers every allocation unit, so no partition the page_count
   filter below would keep is left out for being too small. */
BEGIN TRY
    SELECT @frag_eligible = COUNT(*)
    FROM sys.dm_db_partition_stats AS ps
    JOIN sys.indexes AS i ON i.object_id = ps.object_id AND i.index_id = ps.index_id
    WHERE ps.used_page_count > 1000
    OPTION (RECOMPILE, MAXDOP 1);

    INSERT INTO @frag_candidates ([object_id], [index_id], [partition_number])
    SELECT TOP (100) ps.object_id, ps.index_id, ps.partition_number
    FROM sys.dm_db_partition_stats AS ps
    JOIN sys.indexes AS i ON i.object_id = ps.object_id AND i.index_id = ps.index_id
    WHERE ps.used_page_count > 1000
    ORDER BY ps.used_page_count DESC, ps.object_id, ps.index_id, ps.partition_number
    OPTION (RECOMPILE, MAXDOP 1);

    WHILE EXISTS (SELECT 1 FROM @frag_candidates WHERE [n] = @frag_i)
          AND DATEDIFF(second, @batch_started, SYSDATETIME()) < @frag_budget_sec
    BEGIN
        SELECT @frag_object = [object_id], @frag_index = [index_id],
               @frag_partition = [partition_number]
        FROM @frag_candidates
        WHERE [n] = @frag_i;

        INSERT INTO @fragmentation
        SELECT ips.avg_fragmentation_in_percent,
               OBJECT_SCHEMA_NAME(ips.object_id) + '.' + OBJECT_NAME(ips.object_id),
               i.name,
               ips.index_type_desc,
               ips.partition_number,
               ips.page_count,
               CAST(ips.avg_fragmentation_in_percent AS DECIMAL(5,2))
        FROM sys.dm_db_index_physical_stats(DB_ID(), @frag_object, @frag_index,
                                            @frag_partition, 'LIMITED') AS ips
        JOIN sys.indexes AS i ON i.object_id = ips.object_id AND i.index_id = ips.index_id
        -- sys.dm_db_index_physical_stats answers one row per allocation unit,
        -- not one per partition, and the two extra units are not what logical
        -- fragmentation means. IN_ROW_DATA is an ordered B-tree whose pages can
        -- be out of order, which is what avg_fragmentation_in_percent measures
        -- and what a rebuild repairs. LOB_DATA and ROW_OVERFLOW_DATA are
        -- allocation chains; reorganising them is a different operation.
        --
        -- THIS PREDICATE CHANGES NO ROW TODAY, AND IS KEPT ON PURPOSE. LIMITED
        -- reads the level above the leaf, and those two units have no such
        -- level, so LIMITED reports their fragmentation as zero and the > 10
        -- below already hides them. Measured on SQL Server 2025 over a database
        -- built for the question: two LOB_DATA units of 608 and 7509 pages and
        -- one ROW_OVERFLOW_DATA unit of 400, all three at 0.0, none of them
        -- reaching the threshold. So the filter is not fixing a defect that is
        -- live; it is saying which unit this query is about, so that lowering
        -- the threshold or moving to SAMPLED, both of which have been
        -- considered for this file, does not quietly start listing LOB chains
        -- under the name of the index they hang from. 70.schema/050.heaps reads
        -- the same DMV in SAMPLED with no threshold, and there the same filter
        -- removes a real duplicate row.
        WHERE ips.alloc_unit_type_desc = N'IN_ROW_DATA'
          AND ips.page_count > 1000 AND ips.avg_fragmentation_in_percent > 10
        OPTION (RECOMPILE, MAXDOP 1);

        SET @frag_measured += 1;
        SET @frag_i += 1;
    END
END TRY
BEGIN CATCH
    SELECT @err_fragmentation = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT @frag_eligible               AS [fragmentation_sample.eligible_partitions],
       @frag_measured               AS [fragmentation_sample.measured_partitions],
       @frag_budget_sec             AS [fragmentation_sample.budget_sec],
       CASE WHEN @err_fragmentation = 0 THEN 1 ELSE 0 END AS [collected.fragmentation],
       @err_fragmentation           AS [errors.fragmentation],
       NULLIF(@msg, N'')            AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

SELECT TOP (25) g.[table], g.[index_name], g.[index_type], g.[partition_number],
       g.[page_count], g.[fragmentation_pct]
FROM @fragmentation AS g
ORDER BY g.[sort_frag] DESC
OPTION (RECOMPILE, MAXDOP 1);
