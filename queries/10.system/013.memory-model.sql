-- @scope:       instance
-- @resultsets:  root:object
-- @permissions: VIEW SERVER STATE
-- @timeout:     60
-- @min_version: 13.0.4001
--
-- Which memory model the engine is using — conventional, locked pages or
-- large pages. Restored from 10.system/010.properties.sql, which holds the
-- SQL Server 2012 floor and cannot name these columns.
--
-- sys.dm_os_sys_info.sql_memory_model and sql_memory_model_desc are
-- documented as "SQL Server 2012 (11.x) SP4, SQL Server 2016 (13.x) SP1, and
-- later versions". That set is not a version range: the columns exist on
-- 11.0.7001+ but not on any 12.x. A @min_version gate is a numeric floor and
-- cannot express a hole, so the floor is the later of the two, 2016 SP1
-- (13.0.4001). A gate of 11.0.7001 would let the query run on SQL Server 2014
-- and abort the batch there. The columns are therefore not collected on
-- 2012 SP4 — deliberately, because getting a gate too low is the dangerous
-- direction.
--
-- 10.system/010.properties.sql already reports the observable consequence of
-- the locked-pages model (memory.sql_locked_pages_mb and
-- memory.sql_large_pages_mb) on every version, so nothing is lost on 2012
-- beyond the configured model's own name.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT
    si.sql_memory_model                                             AS [memory.sql_memory_model],
    si.sql_memory_model_desc                                        AS [memory.sql_memory_model_desc]
FROM sys.dm_os_sys_info AS si
OPTION (RECOMPILE, MAXDOP 1);
