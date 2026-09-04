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
-- SQL Server 2012 is the floor. sys.dm_db_index_operational_stats predates it.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT DB_NAME()                                                  AS [database],
       SYSDATETIME()                                              AS [collected_at],
       si.sqlserver_start_time                                    AS [instance_start],
       DATEDIFF(second, si.sqlserver_start_time, GETDATE())       AS [seconds_since_instance_start],
       (SELECT COUNT(*) FROM sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL)) AS [rows_reporting],
       (SELECT SUM(os.forwarded_fetch_count)
        FROM sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
        WHERE os.index_id = 0)                                    AS [forwarded_fetches_total],
       (SELECT COUNT(DISTINCT os.object_id)
        FROM sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
        WHERE os.index_id = 0 AND os.forwarded_fetch_count > 0)   AS [heaps_with_forwarded_fetches],
       (SELECT SUM(os.row_lock_wait_in_ms + os.page_lock_wait_in_ms)
        FROM sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os) AS [lock_wait_ms_total],
       (SELECT SUM(os.index_lock_promotion_count)
        FROM sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os) AS [lock_escalations_total],
       200                                                        AS [listing_cap]
FROM sys.dm_os_sys_info AS si
OPTION (RECOMPILE, MAXDOP 1);

/* Heaps only. leaf_update_count travels with the forwarded count because a
   forwarded record is created by an update: the ratio is what separates a
   heap that is merely written from one that is being damaged. */
SELECT TOP (200)
       SCHEMA_NAME(o.schema_id) + '.' + o.name                    AS [table],
       SUM(os.forwarded_fetch_count)                              AS [forwarded_fetches],
       SUM(os.leaf_insert_count)                                  AS [leaf_inserts],
       SUM(os.leaf_update_count)                                  AS [leaf_updates],
       SUM(os.leaf_delete_count)                                  AS [leaf_deletes],
       SUM(os.leaf_ghost_count)                                   AS [leaf_ghosts],
       SUM(os.range_scan_count)                                   AS [range_scans],
       SUM(os.singleton_lookup_count)                             AS [singleton_lookups],
       MAX(ps.row_count)                                          AS [rows]
FROM       sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
JOIN       sys.objects AS o ON o.object_id = os.object_id AND o.type = 'U'
LEFT JOIN  sys.dm_db_partition_stats AS ps
        ON ps.object_id = os.object_id AND ps.index_id = os.index_id
WHERE os.index_id = 0
GROUP BY o.schema_id, o.name
ORDER BY SUM(os.forwarded_fetch_count) DESC, SUM(os.leaf_update_count) DESC
OPTION (RECOMPILE, MAXDOP 1);

/* Ordered by total lock wait, not by lock count: a million locks taken and
   released instantly are not a problem, and one lock held for four minutes
   is. */
SELECT TOP (200)
       SCHEMA_NAME(o.schema_id) + '.' + o.name                    AS [table],
       ISNULL(i.name, '(heap)')                                   AS [index_name],
       os.index_id                                                AS [index_id],
       SUM(os.row_lock_wait_count)                                AS [row_lock.waits],
       SUM(os.row_lock_wait_in_ms)                                AS [row_lock.wait_ms],
       SUM(os.page_lock_wait_count)                               AS [page_lock.waits],
       SUM(os.page_lock_wait_in_ms)                               AS [page_lock.wait_ms],
       SUM(os.index_lock_promotion_count)                         AS [lock_escalations],
       SUM(os.page_latch_wait_in_ms)                              AS [latch.wait_ms],
       SUM(os.page_io_latch_wait_in_ms)                           AS [io_latch.wait_ms]
FROM       sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
JOIN       sys.objects AS o ON o.object_id = os.object_id AND o.type = 'U'
LEFT JOIN  sys.indexes AS i ON i.object_id = os.object_id AND i.index_id = os.index_id
WHERE os.row_lock_wait_in_ms > 0 OR os.page_lock_wait_in_ms > 0
   OR os.index_lock_promotion_count > 0
GROUP BY o.schema_id, o.name, i.name, os.index_id
ORDER BY SUM(os.row_lock_wait_in_ms) + SUM(os.page_lock_wait_in_ms) DESC
OPTION (RECOMPILE, MAXDOP 1);
