-- @scope:       database
-- @resultsets:  root:object, columnstore:array, trim_reasons:array
-- @permissions: CONNECT, VIEW SERVER STATE, VIEW ANY DEFINITION
-- @timeout:     120
-- @min_version: 13
-- @profiles:    space
--
-- Runs once per user database, with the connection context switched to it.
--
-- The health of every columnstore index: how its rows are grouped, how many of
-- them are dead, and why the groups closed at the size they did.
--
-- WHY THIS COLLECTOR EXISTS, AND WHY NOTHING ELSE IN THIS ARCHIVE ANSWERS IT.
-- A columnstore index is not a B-tree and none of the rowstore measurements
-- apply to it. 20.databases/025.fragmentation.sql reports logical
-- fragmentation, which has no meaning for a columnstore. 70.schema/055.page-
-- density.sql reports how full leaf pages are, and a columnstore has no leaf
-- pages. 70.schema/030.index-operational.sql was measured, in September 2026,
-- to have no row at all for a columnstore index: the two rows it returns under
-- that index's index_id belong to the delta store and the delete bitmap. So an
-- archive taken on a warehouse could report the size of its largest object and
-- nothing whatever about whether that object was in good order.
--
-- WHAT GOOD ORDER MEANS HERE, IN THREE NUMBERS.
--
-- Deleted rows. A columnstore never removes a row in place: a DELETE or the
-- delete half of an UPDATE sets a bit in the delete bitmap and the row stays in
-- its compressed segment, still read, still decompressed, still discarded. A
-- warehouse where a third of the rows are deleted reads a third more than it
-- needs to, forever, until the index is rebuilt or reorganised. This is the
-- single most valuable number in the file and it has no rowstore equivalent:
-- a B-tree page with deleted rows gets reused, a columnstore segment does not.
--
-- Rowgroup size. A compressed rowgroup holds up to 1 048 576 rows, and the
-- compression and the segment elimination both work better the closer it gets.
-- Rowgroups of fifty thousand rows mean the index is paying the columnstore's
-- costs without earning its benefits, and the usual cause is not a mystery, it
-- is written down: see trim_reason below.
--
-- The delta store. Rows arriving in small batches land in a rowstore delta
-- store and stay there until a rowgroup fills or the tuple mover runs. An
-- index whose delta store holds millions of rows is being fed by a trickle of
-- single-row inserts, which is the wrong shape of write for this index type
-- and is a design finding rather than a maintenance one.
--
-- trim_reason IS THE COLUMN THAT MAKES THIS FILE WORTH WRITING. When a
-- rowgroup closes below the maximum, the engine records why, and the reasons
-- call for opposite actions. BULKLOAD means the batch size of the load is the
-- limit, so the fix is in the ETL and costs nothing at the database.
-- DICTIONARY_SIZE means a high-cardinality string column is defeating the
-- compression, so the fix is the column. MEMORY_LIMITATION means the index
-- build could not get a large enough grant, so the fix is memory or a
-- serialised rebuild. RESIDUAL_ROW_GROUP is the last group of a build and is
-- expected. Told only that groups are small, a reader guesses; told which of
-- those four, they act.
--
-- THE FLOOR IS SQL SERVER 2016, AND THAT LOSES SOMETHING REAL. trim_reason and
-- transition_to_compressed_state arrived with sys.dm_db_column_store_row_group_
-- physical_stats in 2016. The catalogue view sys.column_store_row_groups goes
-- back to 2012 and carries the state, the row counts and the deleted counts,
-- which is most of what the root below reports. So an instance on 2014 running
-- a clustered columnstore index gets nothing from this file, and that is a
-- deliberate trade rather than an oversight: the 2016 view is a strict superset
-- of the 2012 one, so writing the poorer version as the base would give two
-- files where one is contained in the other. If a dossier on a 2014 warehouse
-- ever needs it, the same query against sys.column_store_row_groups with the
-- two columns dropped is the whole companion file, and it should be written
-- then rather than guessed at now.
--
-- ONE ROW PER INDEX PARTITION, NOT PER ROWGROUP. A large warehouse has tens of
-- thousands of rowgroups and an archive is not the place to carry them
-- individually. What a reader needs is the shape of the distribution, so the
-- rowgroups are counted by state and summarised by size, and the reasons they
-- were trimmed come in a second array keyed the same way. The two arrays join
-- on table, index and partition.
--
-- NO JUDGEMENT IS APPLIED. A columnstore index with open rowgroups is being
-- written to, which is what it is for. Deleted rows accumulate between
-- maintenance windows and that is normal; what makes a proportion a finding is
-- how it compares to the maintenance that is supposed to clear it, which
-- 50.agent/040.maintenance-plans.sql answers and this file does not.
--
-- Not collected, deliberately:
--   one row per rowgroup                (see above)
--   sys.column_store_segments           (per column per rowgroup: on a wide
--     fact table that is the rowgroup count multiplied by the column count,
--     and the question it answers, which column compresses badly, is already
--     answered by DICTIONARY_SIZE appearing in trim_reasons)
--   sys.column_store_dictionaries       (same shape, same reason)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @err_columnstore int = 0, @msg nvarchar(2048) = N'';

DECLARE @groups TABLE (
    [object_id]         int,
    [index_id]          int,
    [partition_number]  int,
    [state_desc]        nvarchar(60),
    [trim_reason_desc]  nvarchar(60),
    [row_groups]        int,
    [total_rows]        bigint,
    [deleted_rows]      bigint,
    [size_bytes]        bigint,
    [min_rows]          bigint,
    [max_rows]          bigint);

BEGIN TRY
    /* Shredded once at the finest grain this file keeps, then aggregated twice
       below. The DMV reads metadata rather than segments, so the cost is in the
       number of rowgroups and not in the size of the index. */
    INSERT INTO @groups
    SELECT rg.object_id, rg.index_id, rg.partition_number,
           rg.state_desc, rg.trim_reason_desc,
           COUNT(*), SUM(rg.total_rows), SUM(ISNULL(rg.deleted_rows, 0)),
           SUM(ISNULL(rg.size_in_bytes, 0)),
           MIN(rg.total_rows), MAX(rg.total_rows)
    FROM sys.dm_db_column_store_row_group_physical_stats AS rg
    GROUP BY rg.object_id, rg.index_id, rg.partition_number,
             rg.state_desc, rg.trim_reason_desc
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_columnstore = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT
    DB_NAME()                                                   AS [database],
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    -- Read from sys.indexes rather than from the rowgroups, so an index that
    -- exists and holds nothing is still counted. type 5 is clustered
    -- columnstore, 6 is non-clustered.
    (SELECT COUNT(*) FROM sys.indexes AS i
       JOIN sys.objects AS o ON o.object_id = i.object_id
      WHERE i.type IN (5, 6) AND o.is_ms_shipped = 0)           AS [counts.columnstore_indexes],
    (SELECT COUNT(*) FROM sys.indexes AS i
       JOIN sys.objects AS o ON o.object_id = i.object_id
      WHERE i.type = 5 AND o.is_ms_shipped = 0)                 AS [counts.clustered_columnstore],
    (SELECT SUM(g.[row_groups]) FROM @groups AS g)              AS [counts.row_groups],
    (SELECT SUM(g.[total_rows]) FROM @groups AS g)              AS [counts.total_rows],
    (SELECT SUM(g.[deleted_rows]) FROM @groups AS g)            AS [counts.deleted_rows],
    -- The proportion, because the absolute figure means nothing on its own.
    (SELECT CAST(CASE WHEN SUM(g.[total_rows]) > 0
                      THEN SUM(g.[deleted_rows]) * 100.0 / SUM(g.[total_rows])
                 END AS DECIMAL(9,3)) FROM @groups AS g)        AS [counts.deleted_percent],
    -- Rows still in the delta store, which is rowstore and is not compressed.
    (SELECT ISNULL(SUM(g.[total_rows]), 0) FROM @groups AS g
      WHERE g.[state_desc] IN (N'OPEN', N'CLOSED'))             AS [counts.delta_store_rows],
    (SELECT ISNULL(SUM(g.[row_groups]), 0) FROM @groups AS g
      WHERE g.[state_desc] = N'COMPRESSED'
        AND g.[trim_reason_desc] NOT IN (N'NO_TRIM', N'RESIDUAL_ROW_GROUP'))
                                                                AS [counts.row_groups_trimmed],
    1048576                                                     AS [counts.max_rows_per_row_group],
    CASE WHEN @err_columnstore = 0 THEN 1 ELSE 0 END            AS [collected.columnstore],
    @err_columnstore                                            AS [errors.columnstore],
    NULLIF(@msg, N'')                                           AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

/* One row per index partition. avg_rows_per_compressed_group is the number to
   read first: against the 1 048 576 maximum it says whether this index is
   getting what it was created for. */
SELECT TOP (200)
    OBJECT_SCHEMA_NAME(g.[object_id]) + '.' + OBJECT_NAME(g.[object_id])
                                                                AS [table],
    i.name                                                      AS [index_name],
    i.type_desc                                                 AS [index_type],
    g.[partition_number]                                        AS [partition],
    SUM(g.[row_groups])                                         AS [row_groups],
    SUM(CASE WHEN g.[state_desc] = N'COMPRESSED' THEN g.[row_groups] ELSE 0 END)
                                                                AS [compressed_groups],
    SUM(CASE WHEN g.[state_desc] = N'OPEN' THEN g.[row_groups] ELSE 0 END)
                                                                AS [open_groups],
    SUM(CASE WHEN g.[state_desc] = N'CLOSED' THEN g.[row_groups] ELSE 0 END)
                                                                AS [closed_groups],
    -- TOMBSTONE groups are emptied delta stores awaiting cleanup. A pile of
    -- them means the tuple mover has not run, not that data is missing.
    SUM(CASE WHEN g.[state_desc] = N'TOMBSTONE' THEN g.[row_groups] ELSE 0 END)
                                                                AS [tombstone_groups],
    SUM(g.[total_rows])                                         AS [rows],
    SUM(g.[deleted_rows])                                       AS [deleted_rows],
    CAST(CASE WHEN SUM(g.[total_rows]) > 0
              THEN SUM(g.[deleted_rows]) * 100.0 / SUM(g.[total_rows])
         END AS DECIMAL(9,3))                                   AS [deleted_percent],
    SUM(CASE WHEN g.[state_desc] IN (N'OPEN', N'CLOSED') THEN g.[total_rows] ELSE 0 END)
                                                                AS [delta_store_rows],
    CAST(SUM(g.[size_bytes]) / 1048576.0 AS DECIMAL(18,1))      AS [compressed_mb],
    -- The average over compressed groups only: including an open delta store,
    -- which is partly full by definition, would drag it down and hide the
    -- question this column asks.
    CASE WHEN SUM(CASE WHEN g.[state_desc] = N'COMPRESSED' THEN g.[row_groups] ELSE 0 END) > 0
         THEN SUM(CASE WHEN g.[state_desc] = N'COMPRESSED' THEN g.[total_rows] ELSE 0 END)
              / SUM(CASE WHEN g.[state_desc] = N'COMPRESSED' THEN g.[row_groups] ELSE 0 END)
    END                                                         AS [avg_rows_per_compressed_group],
    /* Both bounds over COMPRESSED groups only, for the same reason as the
       average above and it is not a theoretical one. Taken over every group,
       the minimum is the open delta store on any index currently being
       written to, so the column reported 2 on the test case and would report
       a handful of rows on every live warehouse: a number that is always the
       same thing and never the smallest compressed group, which is what the
       column is named for. */
    MIN(CASE WHEN g.[state_desc] = N'COMPRESSED' THEN g.[min_rows] END)
                                                                AS [smallest_compressed_group_rows],
    MAX(CASE WHEN g.[state_desc] = N'COMPRESSED' THEN g.[max_rows] END)
                                                                AS [largest_compressed_group_rows]
FROM @groups AS g
LEFT JOIN sys.indexes AS i
       ON i.object_id = g.[object_id] AND i.index_id = g.[index_id]
GROUP BY g.[object_id], i.name, i.type_desc, g.[partition_number]
ORDER BY SUM(g.[deleted_rows]) DESC, SUM(g.[total_rows]) DESC
OPTION (RECOMPILE, MAXDOP 1);

/* Why the compressed rowgroups closed where they did, per index partition.
   NO_TRIM and RESIDUAL_ROW_GROUP are the expected reasons and are kept rather
   than filtered: a reader needs the denominator to judge the others. */
SELECT TOP (400)
    OBJECT_SCHEMA_NAME(g.[object_id]) + '.' + OBJECT_NAME(g.[object_id])
                                                                AS [table],
    i.name                                                      AS [index_name],
    g.[partition_number]                                        AS [partition],
    g.[trim_reason_desc]                                        AS [trim_reason],
    SUM(g.[row_groups])                                         AS [row_groups],
    SUM(g.[total_rows])                                         AS [rows],
    CASE WHEN SUM(g.[row_groups]) > 0
         THEN SUM(g.[total_rows]) / SUM(g.[row_groups]) END     AS [avg_rows_per_group]
FROM @groups AS g
LEFT JOIN sys.indexes AS i
       ON i.object_id = g.[object_id] AND i.index_id = g.[index_id]
WHERE g.[state_desc] = N'COMPRESSED'
GROUP BY g.[object_id], i.name, g.[partition_number], g.[trim_reason_desc]
ORDER BY SUM(g.[row_groups]) DESC
OPTION (RECOMPILE, MAXDOP 1);
