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
-- These are the licensing-relevant numbers, and only they are here: the file
-- carries what is genuinely gated at 2016 SP2 and nothing else.
-- 10.system/010.properties.sql is ungated and always runs, so
-- system.logical_cpus and system.hyperthread_ratio come from there on every
-- version and are not repeated here — two root objects in the same area
-- emitting the same key path would force a precedence decision downstream for
-- no benefit.
--
-- 010.properties.sql also derives system.numa_nodes from sys.dm_os_nodes,
-- which counts the engine's memory nodes rather than the hardware's;
-- numa_node_count here includes both physical and soft-NUMA nodes and is the
-- authoritative figure where it is available. The two keys differ in name as
-- well as in meaning, so both can stand.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT
    si.socket_count                                                 AS [system.socket_count],
    si.cores_per_socket                                             AS [system.cores_per_socket],
    si.numa_node_count                                              AS [system.numa_node_count]
FROM sys.dm_os_sys_info AS si
OPTION (RECOMPILE, MAXDOP 1);
