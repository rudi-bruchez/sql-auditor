-- @scope:       instance
-- @resultsets:  root:object, ple_per_node:array, schedulers_per_node:array, configuration:array, memory_clerks:array, waits:array
-- @permissions: VIEW SERVER STATE
-- @timeout:     60
--
-- Instance identity, topology, memory and the cumulative waits that explain
-- memory pressure. JSON is assembled client-side, so nothing here uses FOR
-- JSON; the dotted aliases in the first result set carry the nesting.
--
-- SQL Server 2012 is the floor. Removed for that reason:
--   sys.dm_os_sys_info.socket_count / cores_per_socket / numa_node_count  (2016 SP2)
--   sys.dm_os_sys_info.softnuma_configuration_desc                        (2016)
--   sys.dm_os_sys_info.sql_memory_model_desc                              (2012 SP4 / 2016 SP1)
-- NUMA node count is recovered from sys.dm_os_nodes, which exists since 2008.
-- SERVERPROPERTY('ProductUpdateLevel') is 2012 SP3+, but SERVERPROPERTY
-- returns NULL for a property it does not know, so it is safe on any build.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT
    /* ───────── instance ───────── */
    CONVERT(sysname,        SERVERPROPERTY('MachineName'))          AS [instance.server],
    CONVERT(sysname,        SERVERPROPERTY('ServerName'))           AS [instance.instance_name],
    CONVERT(nvarchar(128),  SERVERPROPERTY('Edition'))              AS [instance.edition],
    CONVERT(nvarchar(128),  SERVERPROPERTY('ProductVersion'))       AS [instance.version],
    CONVERT(nvarchar(128),  SERVERPROPERTY('ProductLevel'))         AS [instance.level],
    CONVERT(nvarchar(128),  SERVERPROPERTY('ProductUpdateLevel'))   AS [instance.cu],
    si.sqlserver_start_time                                         AS [instance.sqlserver_start_time],
    DATEDIFF(HOUR, si.sqlserver_start_time, SYSDATETIME()) / 24     AS [instance.uptime_days],
    DATEDIFF(HOUR, si.sqlserver_start_time, SYSDATETIME()) % 24     AS [instance.uptime_hours],

    /* ───────── system / topology ───────── */
    si.cpu_count                                                    AS [system.logical_cpus],
    si.scheduler_count                                              AS [system.active_schedulers],
    -- node_state_desc is a composite string, not an enum: one state
    -- (ONLINE / OFFLINE / IDLE / IDLE_READY) followed by zero or more
    -- combinable flags (DAC, THREAD_RESOURCES_LOW, HOT ADDED). Matching
    -- 'ONLINE DAC' exactly would miss 'IDLE DAC' and
    -- 'ONLINE DAC THREAD_RESOURCES_LOW', silently counting one node too many.
    (SELECT COUNT(*) FROM sys.dm_os_nodes
      WHERE node_state_desc NOT LIKE '%DAC%')                       AS [system.numa_nodes],
    si.hyperthread_ratio                                            AS [system.hyperthread_ratio],
    si.affinity_type_desc                                           AS [system.affinity_type],
    si.virtual_machine_type_desc                                    AS [system.machine_type],
    si.max_workers_count                                            AS [system.max_workers],
    CAST(si.physical_memory_kb / 1048576.0 AS DECIMAL(10,1))        AS [system.ram_gb],

    /* ───────── memory ───────── */
    CAST(pm.physical_memory_in_use_kb   / 1024.0 AS DECIMAL(12,1))  AS [memory.sql_ram_in_use_mb],
    CAST(si.committed_kb                / 1024.0 AS DECIMAL(12,1))  AS [memory.sql_committed_mb],
    CAST(si.committed_target_kb         / 1024.0 AS DECIMAL(12,1))  AS [memory.sql_committed_target_mb],
    CAST(pm.locked_page_allocations_kb  / 1024.0 AS DECIMAL(12,1))  AS [memory.sql_locked_pages_mb],
    CAST(pm.large_page_allocations_kb   / 1024.0 AS DECIMAL(12,1))  AS [memory.sql_large_pages_mb],
    pm.memory_utilization_percentage                                AS [memory.pct_workingset_in_ram],
    CAST(pm.process_physical_memory_low AS BIT)                     AS [memory.physical_pressure_signal],
    CAST(pm.process_virtual_memory_low  AS BIT)                     AS [memory.virtual_pressure_signal],
    CAST(sm.total_physical_memory_kb     / 1024.0 AS DECIMAL(12,1)) AS [memory.total_physical_ram_mb],
    CAST(sm.available_physical_memory_kb / 1024.0 AS DECIMAL(12,1)) AS [memory.available_physical_ram_mb],
    CAST(100.0 * sm.available_physical_memory_kb
              / NULLIF(sm.total_physical_memory_kb,0) AS DECIMAL(5,2)) AS [memory.pct_ram_free],
    sm.system_memory_state_desc                                     AS [memory.system_memory_state],
    (SELECT CAST(value_in_use AS INT) FROM sys.configurations
      WHERE name = 'min server memory (MB)')                        AS [memory.min_server_memory_mb],
    (SELECT CAST(value_in_use AS INT) FROM sys.configurations
      WHERE name = 'max server memory (MB)')                        AS [memory.max_server_memory_mb],
    (SELECT cntr_value / 1024 FROM sys.dm_os_performance_counters
      WHERE counter_name = 'Total Server Memory (KB)'
        AND object_name LIKE '%Memory Manager%')                    AS [memory.total_server_memory_mb],
    (SELECT cntr_value / 1024 FROM sys.dm_os_performance_counters
      WHERE counter_name = 'Target Server Memory (KB)'
        AND object_name LIKE '%Memory Manager%')                    AS [memory.target_server_memory_mb],
    (SELECT cntr_value FROM sys.dm_os_performance_counters
      WHERE counter_name = 'Memory Grants Pending'
        AND object_name LIKE '%Memory Manager%')                    AS [memory.memory_grants_pending],
    (SELECT cntr_value FROM sys.dm_os_performance_counters
      WHERE counter_name = 'Memory Grants Outstanding'
        AND object_name LIKE '%Memory Manager%')                    AS [memory.memory_grants_outstanding],
    (SELECT CAST(100.0 * bchr.cntr_value / NULLIF(base.cntr_value,0) AS DECIMAL(5,2))
       FROM sys.dm_os_performance_counters AS bchr
       JOIN sys.dm_os_performance_counters AS base
            ON base.object_name  = bchr.object_name
           AND base.counter_name = 'Buffer cache hit ratio base'
      WHERE bchr.counter_name = 'Buffer cache hit ratio'
        AND bchr.object_name LIKE '%Buffer Manager%')               AS [memory.buffer_cache_hit_ratio_pct],
    (SELECT cntr_value FROM sys.dm_os_performance_counters
      WHERE counter_name = 'Page life expectancy'
        AND object_name LIKE '%Buffer Manager%')                    AS [memory.ple_global_measured_sec],
    (SELECT CAST( (CAST(value_in_use AS BIGINT) / 1024.0) / 4.0 * 300 AS INT)
       FROM sys.configurations WHERE name = 'max server memory (MB)') AS [memory.ple_empirical_target_sec]
FROM        sys.dm_os_sys_info       AS si
CROSS JOIN  sys.dm_os_process_memory AS pm
CROSS JOIN  sys.dm_os_sys_memory     AS sm
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── ple_per_node: PLE per NUMA node ───────── */
SELECT RTRIM(instance_name) AS node, cntr_value AS ple_sec
FROM sys.dm_os_performance_counters
WHERE counter_name = 'Page life expectancy'
  AND object_name LIKE '%Buffer Node%'
ORDER BY TRY_CONVERT(INT, instance_name)
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── schedulers_per_node ───────── */
SELECT parent_node_id                                    AS node,
       COUNT(*)                                          AS schedulers,
       SUM(CASE WHEN is_online = 1 THEN 1 ELSE 0 END)    AS online,
       SUM(CASE WHEN is_online = 0 THEN 1 ELSE 0 END)    AS offline
FROM sys.dm_os_schedulers
WHERE status LIKE 'VISIBLE%'
GROUP BY parent_node_id
ORDER BY parent_node_id
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── configuration (instance) ───────── */
SELECT name AS setting, CAST(value_in_use AS BIGINT) AS value
FROM sys.configurations
WHERE name IN ('max degree of parallelism','cost threshold for parallelism',
               'min server memory (MB)','max server memory (MB)',
               'affinity mask','affinity I/O mask','affinity64 mask',
               'lightweight pooling','priority boost','max worker threads',
               'optimize for ad hoc workloads')
ORDER BY name
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── memory_clerks (> 100 MB) ───────── */
SELECT TOP (15) type,
       CAST(SUM(pages_kb) / 1024.0 AS DECIMAL(12,1)) AS mb
FROM sys.dm_os_memory_clerks
GROUP BY type
HAVING SUM(pages_kb) > 102400
ORDER BY SUM(pages_kb) DESC
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── waits: relevant cumulative waits ─────────
   The list names wait types that do not exist before SQL Server 2017
   (CXCONSUMER, CXSYNC_*). They are string literals in an IN list, so an
   older instance simply matches nothing. */
SELECT wait_type,
       waiting_tasks_count                                                    AS tasks,
       wait_time_ms / 1000                                                    AS wait_sec,
       CAST(wait_time_ms * 1.0 / NULLIF(waiting_tasks_count,0) AS DECIMAL(10,1)) AS avg_ms,
       signal_wait_time_ms / 1000                                             AS signal_sec
FROM sys.dm_os_wait_stats
WHERE wait_type IN ('RESOURCE_SEMAPHORE','RESOURCE_SEMAPHORE_QUERY_COMPILE',
                    'CMEMTHREAD','PAGEIOLATCH_SH','PAGEIOLATCH_EX',
                    'CXPACKET','CXCONSUMER','CXSYNC_PORT','CXSYNC_CONSUMER',
                    'SOS_SCHEDULER_YIELD','THREADPOOL','LCK_M_S','LCK_M_X')
  AND waiting_tasks_count > 0
ORDER BY wait_time_ms DESC
OPTION (RECOMPILE, MAXDOP 1);
