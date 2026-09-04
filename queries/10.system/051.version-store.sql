-- @scope:       instance
-- @resultsets:  root:object, version_store_space:array
-- @permissions: VIEW SERVER STATE
-- @timeout:     60
-- @min_version: 13.0.5026
--
-- Which databases are consuming the tempdb version store, and by how much.
-- Restored from 10.system/050.tempdb.sql, which holds the SQL Server 2012
-- floor and cannot name this view.
--
-- sys.dm_tran_version_store_space_usage is documented as "SQL Server 2016
-- (13.x) SP2 and later versions" — it was added by KB4052908 — so the gate is
-- the SP2 build 13.0.5026 and not the bare major 13.
--
-- 10.system/050.tempdb.sql covers the same ground on 2012 with the Version
-- Store Size (KB) performance counter, which is an instance total; this view
-- attributes that total to individual databases, which the counter cannot do.
-- It is cheap: it aggregates page counts rather than walking version records,
-- unlike sys.dm_tran_version_store.
--
-- SERVERPROPERTY returns NULL for a property it does not know, so
-- IsTempDbMetadataMemoryOptimized (SQL Server 2019) is safe to ask for on any
-- build and rides along here rather than earning a gate of its own. On
-- anything below 2019 it is NULL, which is also the answer: the feature does
-- not exist there.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT
    CONVERT(int, SERVERPROPERTY('IsTempDbMetadataMemoryOptimized'))  AS [tempdb.metadata_memory_optimized],
    (SELECT CAST(SUM(vs.reserved_space_kb) / 1024.0 AS DECIMAL(14,1))
       FROM sys.dm_tran_version_store_space_usage AS vs)             AS [tempdb.version_store_total_mb]
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── version_store_space: tempdb version store per database ─────────
   Databases with no versioned rows report zero rather than being absent, so
   the list is every database, ordered by consumption. */
SELECT
    vs.database_id,
    DB_NAME(vs.database_id)                                  AS [database],
    vs.reserved_page_count,
    CAST(vs.reserved_space_kb / 1024.0 AS DECIMAL(14,1))     AS reserved_mb
FROM sys.dm_tran_version_store_space_usage AS vs
ORDER BY vs.reserved_space_kb DESC
OPTION (RECOMPILE, MAXDOP 1);
