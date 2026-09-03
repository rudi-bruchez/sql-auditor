-- @scope:       instance
-- @resultsets:  root:object, snapshots:array, worst_clerks:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
-- @min_version: 17
--
-- The memory health history the engine keeps about itself.
--
-- Why this collector exists. Every other memory reading in this archive is an
-- instant: 015.buffer-pool.sql and 010.properties.sql say what memory looked
-- like at the moment of collection, and a collection is one moment. An
-- instance that spends five minutes an hour unable to satisfy allocations, and
-- is comfortable the rest of the time, reads as comfortable — which is the
-- reading that sends an audit looking somewhere else entirely.
--
-- SQL Server 2025 keeps that history itself. A snapshot every fifteen seconds,
-- with the engine's own severity verdict, the memory it could still hand out,
-- and the memory it could reclaim by shrinking caches.
--
-- THE WINDOW IS 256 SNAPSHOTS, AND THAT IS AN HOUR AND FOUR MINUTES. It is
-- also reset by a restart. So this view describes the last hour of an
-- instance's life and nothing before it, which makes it excellent evidence of
-- a problem and no evidence at all of its absence. The span goes in the root
-- object beside the counts for exactly that reason — the same discipline
-- 041.connectivity.sql applies to its ring buffers, and for the same reason:
-- a reader given counts without a window reads a quiet hour as a quiet server.
--
-- severity_level is the engine's verdict and not ours: 1 LOW, no issue
-- identified; 2 MEDIUM, allocations might fail and the data cache might be
-- shrinking; 3 HIGH, memory is likely insufficient. It is projected as the code
-- and the label together, the same rule the replication collectors follow for
-- runstatus — a code alone sends every reader to the documentation, a label
-- alone cannot be matched on.
--
-- WHAT IS NOT PROJECTED, AND WHY. top_memory_clerks is a JSON document of up to
-- 4000 characters, present on every one of the 256 rows. Projecting all of them
-- would put a megabyte of near-identical JSON in the archive to answer a
-- question that has one interesting instant: the worst snapshot. So the
-- snapshots array carries the numbers without the JSON, and the clerks are
-- expanded once, for the snapshot with the highest severity — ties broken by
-- the least allocation potential, then by the most recent.
--
-- Three columns of the view are documented as "identified for informational
-- purposes only, not supported, future compatibility not guaranteed" —
-- out_of_memory_event_count, memgrant_timeout_count and memgrant_waiter_count.
-- They are not read. A column Microsoft declines to stand behind has no place
-- in a document a client acts on, and 80.workload/010.wait-stats.sql already
-- carries the supported reading of memory grant pressure.
--
-- Permissions: the documentation names VIEW SERVER PERFORMANCE STATE, one of
-- the narrow server permissions introduced in 2022. VIEW SERVER STATE covers
-- it and is what the directive above declares, because that is the vocabulary
-- the preflight probes and the grant script share.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT
    COUNT(*)                                                          AS [health.snapshots],
    MIN(h.snapshot_time)                                              AS [health.oldest],
    MAX(h.snapshot_time)                                              AS [health.newest],
    DATEDIFF(second, MIN(h.snapshot_time), MAX(h.snapshot_time))      AS [health.span_seconds],
    MAX(h.severity_level)                                             AS [health.worst_severity],
    SUM(CASE WHEN h.severity_level = 2 THEN 1 ELSE 0 END)             AS [health.medium_snapshots],
    SUM(CASE WHEN h.severity_level = 3 THEN 1 ELSE 0 END)             AS [health.high_snapshots],
    MIN(h.allocation_potential_memory_mb)                             AS [health.min_allocation_potential_mb],
    MAX(h.allocation_potential_memory_mb)                             AS [health.max_allocation_potential_mb],
    MIN(h.reclaimable_cache_memory_mb)                                AS [health.min_reclaimable_cache_mb],
    MAX(h.reclaimable_cache_memory_mb)                                AS [health.max_reclaimable_cache_mb]
FROM sys.dm_os_memory_health_history AS h
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── snapshots: the series, without the JSON ───────── */
SELECT
    h.snapshot_time,
    h.severity_level,
    h.severity_level_desc,
    h.allocation_potential_memory_mb,
    h.reclaimable_cache_memory_mb
FROM sys.dm_os_memory_health_history AS h
ORDER BY h.snapshot_time DESC
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── worst_clerks: where the memory was, at the worst moment ─────────
   One snapshot only. OPENJSON needs no compatibility level check here: the
   view itself is 2025, and OPENJSON has been available since 2016. */
SELECT
    worst.snapshot_time,
    worst.severity_level,
    worst.severity_level_desc,
    c.clerk_type,
    c.pages_allocated_kb
FROM (
    SELECT TOP (1)
           h.snapshot_time,
           h.severity_level,
           h.severity_level_desc,
           h.top_memory_clerks
    FROM sys.dm_os_memory_health_history AS h
    ORDER BY h.severity_level DESC,
             h.allocation_potential_memory_mb ASC,
             h.snapshot_time DESC
) AS worst
CROSS APPLY OPENJSON (worst.top_memory_clerks)
    WITH (
        clerk_type        sysname '$.clerk_type',
        pages_allocated_kb bigint '$.pages_allocated_kb'
    ) AS c
ORDER BY c.pages_allocated_kb DESC
OPTION (RECOMPILE, MAXDOP 1);
