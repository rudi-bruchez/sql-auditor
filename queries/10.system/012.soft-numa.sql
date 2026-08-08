-- @scope:       instance
-- @resultsets:  root:object
-- @permissions: VIEW SERVER STATE
-- @timeout:     60
-- @min_version: 13
--
-- Soft-NUMA configuration, restored from 10.system/010.properties.sql, which
-- holds the SQL Server 2012 floor and cannot name these columns.
--
-- sys.dm_os_sys_info.softnuma_configuration and softnuma_configuration_desc
-- are documented as "SQL Server 2016 (13.x) and later versions", so the gate
-- is the bare major 13: any 13.x satisfies it. An unrecognised column name
-- aborts the whole batch rather than the single column, which is why this
-- lives in its own file instead of being guarded inside the 2012 collector.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT
    si.softnuma_configuration                                       AS [system.softnuma_configuration],
    si.softnuma_configuration_desc                                  AS [system.softnuma_configuration_desc]
FROM sys.dm_os_sys_info AS si
OPTION (RECOMPILE, MAXDOP 1);
