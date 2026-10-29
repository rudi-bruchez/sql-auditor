-- @scope:       database
-- @resultsets:  root:object, filegroups:array, indexes:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     120
-- @profiles:    space
--
-- Runs once per user database, with the connection context switched to it.
--
-- Where the reads and writes land since the counters started: per filegroup,
-- and per index of every partitioned table, from
-- sys.dm_db_index_operational_stats read once per partition.
--
-- WHY THIS COLLECTOR EXISTS. On the data warehouse that motivated
-- 70.schema/046.partitioning (October 2026), the file statistics said a slow
-- archive volume holding monthly filegroups from 2012 to 2019 was read 46 TB
-- in eleven days, every month of it, the oldest included. 046 says how the
-- tables are cut and which filegroup holds which months. Neither says which
-- tables and indexes do the reading, and that is the list a remedy starts
-- from: the queries to rewrite, the indexes to align, and whether an archive
-- filegroup is still written, which decides whether it can be made read only.
-- 70.schema/030.index-operational reads the same counters, summed per index, so the
-- partition and with it the filegroup are lost there. docs/partition-activity-spec.md
-- is the specification.
--
-- WHAT A READER GETS. A partition here is a row of sys.partitions: one per
-- index of a table that is not partitioned, one per index and partition of one
-- that is.
--
-- filegroups: every filegroup, with how many partitions it holds, how many of
-- them the counters report, how many were scanned and how many written, and
-- the sums: range scans, singleton lookups, page I/O latch waits and their
-- milliseconds, leaf writes. Joined to 20.databases/020.properties it names
-- the volume; set beside 10.system/030.file-io it says which share of a
-- volume's reads the counters can attribute.
--
-- indexes: every index of every partitioned table, the non-partitioned ones
-- included, with the same sums, and four numbers that say whether queries
-- eliminate partitions: partitions_scanned against partitions, and the least
-- and the most range scans any non-empty partition received. A query that
-- eliminates touches the partitions it needs; a predicate that defeats
-- elimination starts a scan on every partition, and min and max then come
-- close to each other and to the number of executions. first and last
-- partition written say where the writes go: on a date key they are the
-- months still modified.
--
-- WHAT THE COUNTERS COUNT. range_scan_count is the number of scans started on
-- the partition, a seek with a range included, not the pages read.
-- page_io_latch_wait_count and _in_ms are the waits for a page to come from
-- disk: what the slow volume costs, attributed to an index and a partition,
-- which sys.dm_os_wait_stats cannot do. Leaf writes are inserts, updates and
-- deletes at the leaf level. Columnstore segments are not represented in this
-- function at all (70.schema/030.index-operational has the measurement), so a columnstore
-- read from the archive shows its delta store and delete bitmap and nothing
-- else.
--
-- READ IN ONE DIRECTION. The counters start at zero when a partition's
-- metadata enters the cache and vanish when it leaves, under memory pressure,
-- on restart, or when the database closes, with no record. A partition with no
-- row is counted in partitions and not in partitions_reporting; a low count
-- may be a recent eviction. A high count is trustworthy: nothing inflates it.
-- seconds_since_instance_start is the longest window any count can cover, not
-- the window of each.
--
-- NO JUDGEMENT IS APPLIED. A scan of every partition is right for a query that
-- needs the whole history, and an archive month may be written on purpose.
-- What makes either a finding is the query, in the Query Store or the plan
-- cache, which this file does not read.
--
-- THE COST. The function reads counters held in memory and touches no data
-- page. It is read once into a table variable, and sys.partitions with
-- sys.allocation_units once into another; everything after joins the two.
-- LOCKS: the catalogue views take short schema-stability locks, so each area
-- sits in its own TRY with LOCK_TIMEOUT at 10 seconds, as in 046, and a
-- blocked area is reported in errors.* while the other is still collected.
--
-- Each list is capped and the root counts what it would hold without the cap.
--
-- SQL Server 2012 is the floor, the corpus default.
--
-- Not collected, deliberately:
--   one row per partition          (3 650 rows per index on a daily function
--     over ten years; the per-filegroup and per-index sums answer the question
--     and 046 maps filegroups to boundaries)
--   lock and latch contention       (70.schema/030.index-operational)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @cap_filegroups int = 2000, @cap_indexes int = 2000;

DECLARE @err_counters int = 0, @err_partitions int = 0, @msg nvarchar(2048) = N'';

DECLARE @instance_start datetime, @seconds_since int;

/* The counters, one row per partition. A columnstore index answers two or
   three rows for one partition; they are summed here. */
DECLARE @ops TABLE (
    [object_id]         int NOT NULL,
    [index_id]          int NOT NULL,
    [partition_number]  int NOT NULL,
    [range_scans]       bigint NOT NULL,
    [singleton_lookups] bigint NOT NULL,
    [io_latch_waits]    bigint NOT NULL,
    [io_latch_wait_ms]  bigint NOT NULL,
    [leaf_writes]       bigint NOT NULL,
    PRIMARY KEY ([object_id], [index_id], [partition_number]));

/* Every partition of every user object, with the filegroup of its in-row
   unit, where its rows live, and whether its table is partitioned. */
DECLARE @parts TABLE (
    [object_id]         int NOT NULL,
    [index_id]          int NOT NULL,
    [partition_number]  int NOT NULL,
    [rows]              bigint NULL,
    [data_space_id]     int NULL,
    [partitioned_table] bit NOT NULL,
    PRIMARY KEY ([object_id], [index_id], [partition_number]));

BEGIN TRY
    SELECT @instance_start = si.sqlserver_start_time,
           @seconds_since  = DATEDIFF(second, si.sqlserver_start_time, GETDATE())
    FROM sys.dm_os_sys_info AS si
    OPTION (RECOMPILE, MAXDOP 1);

    INSERT INTO @ops
    SELECT os.object_id, os.index_id, os.partition_number,
           SUM(os.range_scan_count), SUM(os.singleton_lookup_count),
           SUM(os.page_io_latch_wait_count), SUM(os.page_io_latch_wait_in_ms),
           SUM(os.leaf_insert_count + os.leaf_update_count + os.leaf_delete_count)
    FROM sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
    GROUP BY os.object_id, os.index_id, os.partition_number
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_counters = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

BEGIN TRY
    INSERT INTO @parts
    SELECT p.object_id, p.index_id, p.partition_number, p.rows, au.data_space_id,
           CASE WHEN p.object_id IN (SELECT b.object_id
                                     FROM sys.indexes AS b
                                     JOIN sys.partition_schemes AS ps ON ps.data_space_id = b.data_space_id
                                     WHERE b.index_id IN (0, 1))
                THEN 1 ELSE 0 END
    FROM sys.partitions AS p
    JOIN sys.objects AS o ON o.object_id = p.object_id AND o.is_ms_shipped = 0
    LEFT JOIN sys.allocation_units AS au ON au.container_id = p.hobt_id AND au.type = 1
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_partitions = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT
    DB_NAME()                                                   AS [database],
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    CONVERT(varchar(23), @instance_start, 126)                  AS [instance_start],
    @seconds_since                                              AS [seconds_since_instance_start],
    (SELECT COUNT(*) FROM @parts)                               AS [counts.partition_rows],
    (SELECT COUNT(*) FROM @parts AS pt
       JOIN @ops AS op ON op.object_id = pt.object_id AND op.index_id = pt.index_id
                      AND op.partition_number = pt.partition_number)
                                                                AS [counts.partition_rows_reporting],
    (SELECT COUNT(*) FROM (SELECT DISTINCT pt.object_id, pt.index_id FROM @parts AS pt
                            WHERE pt.partitioned_table = 1) AS x)
                                                                AS [counts.indexes],
    (SELECT COUNT(*) FROM sys.filegroups)                       AS [counts.filegroups],
    @cap_filegroups                                             AS [listing_cap.filegroups],
    @cap_indexes                                                AS [listing_cap.indexes],
    CASE WHEN @err_counters   = 0 THEN 1 ELSE 0 END             AS [collected.counters],
    CASE WHEN @err_partitions = 0 THEN 1 ELSE 0 END             AS [collected.partitions],
    @err_counters                                               AS [errors.counters],
    @err_partitions                                             AS [errors.partitions],
    NULLIF(@msg, N'')                                           AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

/* filegroups: every one, the most waited on first. */
SELECT TOP (@cap_filegroups)
    fg.name                                                     AS [filegroup],
    CAST(fg.is_read_only AS int)                                AS [is_read_only],
    COUNT(pt.object_id)                                         AS [partitions],
    COUNT(op.object_id)                                         AS [partitions_reporting],
    COUNT(CASE WHEN op.range_scans > 0 THEN 1 END)              AS [partitions_scanned],
    COUNT(CASE WHEN op.leaf_writes > 0 THEN 1 END)              AS [partitions_written],
    ISNULL(SUM(op.range_scans), 0)                              AS [range_scans],
    ISNULL(SUM(op.singleton_lookups), 0)                        AS [singleton_lookups],
    ISNULL(SUM(op.io_latch_waits), 0)                           AS [io_latch.waits],
    ISNULL(SUM(op.io_latch_wait_ms), 0)                         AS [io_latch.wait_ms],
    ISNULL(SUM(op.leaf_writes), 0)                              AS [leaf_writes]
FROM sys.filegroups AS fg
LEFT JOIN @parts AS pt ON pt.data_space_id = fg.data_space_id
LEFT JOIN @ops AS op ON op.object_id = pt.object_id AND op.index_id = pt.index_id
                    AND op.partition_number = pt.partition_number
/* With an area refused every sum would read zero, which is an answer; the
   listing comes back empty instead, and errors.* says why. */
WHERE @err_counters = 0 AND @err_partitions = 0
GROUP BY fg.data_space_id, fg.name, fg.is_read_only
ORDER BY ISNULL(SUM(op.io_latch_wait_ms), 0) DESC, fg.name
OPTION (RECOMPILE, MAXDOP 1);

/* indexes: every index of every partitioned table, the most waited on first.
   The least range scans is taken over non-empty partitions that report: an
   empty partition is skipped by a scan at no cost and says nothing. */
SELECT TOP (@cap_indexes)
    s.name + N'.' + o.name                                      AS [table],
    ISNULL(i.name, N'(heap)')                                   AS [index_name],
    a.index_id                                                  AS [index_id],
    a.partitions                                                AS [partitions],
    a.partitions_reporting                                      AS [partitions_reporting],
    a.partitions_scanned                                        AS [partitions_scanned],
    a.range_scans                                               AS [range_scans],
    a.range_scans_min                                           AS [range_scans_min],
    a.range_scans_max                                           AS [range_scans_max],
    a.singleton_lookups                                         AS [singleton_lookups],
    a.io_latch_waits                                            AS [io_latch.waits],
    a.io_latch_wait_ms                                          AS [io_latch.wait_ms],
    a.leaf_writes                                               AS [leaf_writes],
    a.partitions_written                                        AS [partitions_written],
    a.first_written                                             AS [first_partition_written],
    a.last_written                                              AS [last_partition_written]
FROM (SELECT pt.object_id, pt.index_id,
             COUNT(*)                                            AS partitions,
             COUNT(op.object_id)                                 AS partitions_reporting,
             COUNT(CASE WHEN op.range_scans > 0 THEN 1 END)      AS partitions_scanned,
             ISNULL(SUM(op.range_scans), 0)                      AS range_scans,
             MIN(CASE WHEN pt.[rows] > 0 THEN op.range_scans END) AS range_scans_min,
             MAX(op.range_scans)                                 AS range_scans_max,
             ISNULL(SUM(op.singleton_lookups), 0)                AS singleton_lookups,
             ISNULL(SUM(op.io_latch_waits), 0)                   AS io_latch_waits,
             ISNULL(SUM(op.io_latch_wait_ms), 0)                 AS io_latch_wait_ms,
             ISNULL(SUM(op.leaf_writes), 0)                      AS leaf_writes,
             COUNT(CASE WHEN op.leaf_writes > 0 THEN 1 END)      AS partitions_written,
             MIN(CASE WHEN op.leaf_writes > 0 THEN pt.partition_number END) AS first_written,
             MAX(CASE WHEN op.leaf_writes > 0 THEN pt.partition_number END) AS last_written
      FROM @parts AS pt
      LEFT JOIN @ops AS op ON op.object_id = pt.object_id AND op.index_id = pt.index_id
                          AND op.partition_number = pt.partition_number
      WHERE pt.partitioned_table = 1
      GROUP BY pt.object_id, pt.index_id) AS a
JOIN sys.objects AS o ON o.object_id = a.object_id
JOIN sys.schemas AS s ON s.schema_id = o.schema_id
LEFT JOIN sys.indexes AS i ON i.object_id = a.object_id AND i.index_id = a.index_id
ORDER BY a.io_latch_wait_ms DESC, a.range_scans DESC, a.object_id, a.index_id
OPTION (RECOMPILE, MAXDOP 1);
