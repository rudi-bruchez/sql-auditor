-- @scope:       instance
-- @resultsets:  root:object
-- @permissions: VIEW SERVER STATE
-- @timeout:     60
-- @min_version: 13.0.5026
--
-- Physical CPU topology: sockets, cores per socket and the NUMA node count as
-- the engine sees it. Restored from 10.system/010.properties.sql, which holds
-- the SQL Server 2012 floor and cannot name these columns.
--
-- sys.dm_os_sys_info.socket_count, cores_per_socket and numa_node_count are
-- documented as "SQL Server 2016 (13.x) SP2 and later versions", so the gate
-- is the SP2 build 13.0.5026 and not the bare major 13: on 2016 RTM the
-- column names are unknown and the whole batch fails, not just the column.
--
-- These are the licensing-relevant numbers. 10.system/010.properties.sql
-- reports system.logical_cpus and system.hyperthread_ratio on every version,
-- and derives system.numa_nodes from sys.dm_os_nodes, which counts the
-- engine's memory nodes rather than the hardware's; numa_node_count here
-- includes both physical and soft-NUMA nodes and is the authoritative figure
-- where it is available.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT
    si.socket_count                                                 AS [system.socket_count],
    si.cores_per_socket                                             AS [system.cores_per_socket],
    si.numa_node_count                                              AS [system.numa_node_count],
    si.cpu_count                                                    AS [system.logical_cpus],
    si.hyperthread_ratio                                            AS [system.hyperthread_ratio]
FROM sys.dm_os_sys_info AS si
OPTION (RECOMPILE, MAXDOP 1);
