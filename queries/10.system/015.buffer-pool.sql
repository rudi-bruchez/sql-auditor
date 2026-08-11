-- @scope:       instance
-- @resultsets:  root:object, clerks:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- What the buffer pool actually holds, against what the edition allows it to.
--
-- Why this collector exists, and why the obvious counter is not enough. Every
-- tool reports "SQL Server memory" as Total Server Memory, which is what the
-- memory manager has committed across all its clerks. On an edition with a
-- buffer pool ceiling that number is not the size of the data cache, and the
-- difference is not small: an audited Standard 2017 instance with 344 GB of RAM
-- reported 283.6 GB of Total Server Memory while caching data in 128.0 GB of
-- it, the ceiling, with 129.9 GB committed and permanently empty.
--
-- Anything computed from the wrong number inherits the error. That audit
-- divided a measured working set of 10.4 TB a day by "292 GB of cache" and
-- reported 36 times; against the 128 GB the cache actually was, the true figure
-- is 81. The recommendation did not change, its magnitude did, and a report
-- quoting the wrong one gets challenged on it.
--
-- THE CEILING IS AN EDITION PROPERTY AND IT MOVES BETWEEN VERSIONS. Standard
-- was 64 GB through 2012, 128 GB from 2014 to 2022, and 256 GB from 2025.
-- Enterprise and Developer have none. The cap is computed here rather than left
-- to the analysis layer because it depends on two facts only the instance
-- knows, its edition and its major version, and because getting it wrong in
-- either direction produces a confident false finding.
--
-- The cap applies to the buffer pool alone. Plan cache, lock manager, query
-- memory grants and CLR allocate outside it, bounded by max server memory only.
-- That is why total_server_memory_mb legitimately exceeds the cap on a healthy
-- capped instance, and why the clerk breakdown travels with the root object:
-- without it, "283 GB committed but 128 GB cached" invites the conclusion that
-- 155 GB is unaccounted for, when most of it is simply free.
--
-- free_memory_mb IS THE FINDING when it is large and stable. Free memory is
-- committed memory the server is not using. On an uncapped instance under load
-- it is small, because anything free gets filled with pages. Large and steady
-- is the signature of a ceiling — but one reading cannot tell that from an
-- instance that started an hour ago and has not warmed yet, so the collector
-- also reports the uptime that lets the analysis layer tell them apart.
--
-- WHAT IS DELIBERATELY NOT HERE. The per-database breakdown of the pool comes
-- from sys.dm_os_buffer_descriptors, which returns one row per cached 8 KB
-- page: 16.8 million rows on a 128 GB pool, measured at 26 seconds, and
-- proportionally worse on an Enterprise instance with a terabyte cached. It is
-- the natural follow-up question and it does not belong in a collection that
-- runs unattended. It is in the standalone script instead.
--
-- SQL Server 2012 is the floor. sys.dm_os_memory_clerks.pages_kb replaced the
-- split single_pages_kb/multi_pages_kb columns in 2012, and this file uses it.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

DECLARE @major int =
    CONVERT(int, PARSENAME(CONVERT(varchar(32), SERVERPROPERTY('ProductVersion')), 4));
DECLARE @edition nvarchar(128) = CONVERT(nvarchar(128), SERVERPROPERTY('Edition'));

/* NULL means the edition imposes no buffer pool ceiling, which is Enterprise
   and Developer. It is not the same as "unknown", and the analysis layer has to
   tell the two apart: an unrecognised edition string also lands here, so the
   edition itself is projected beside the cap for the reader to check. */
DECLARE @cap_mb bigint =
    CASE
        WHEN @edition LIKE 'Enterprise%' OR @edition LIKE 'Developer%' THEN NULL
        WHEN @edition LIKE 'Standard%'   THEN CASE WHEN @major <= 11 THEN 64 * 1024
                                                   WHEN @major <= 16 THEN 128 * 1024
                                                   ELSE 256 * 1024 END
        WHEN @edition LIKE 'Web%'        THEN 64 * 1024
        WHEN @edition LIKE 'Express%'    THEN 1410
        ELSE NULL
    END;

SELECT SYSDATETIME()                                              AS [collected_at],
       @edition                                                   AS [edition.name],
       @major                                                     AS [edition.major_version],
       @cap_mb                                                    AS [edition.buffer_pool_cap_mb],
       CONVERT(bigint, si.physical_memory_kb / 1024)              AS [machine.physical_memory_mb],
       DATEDIFF(SECOND, si.sqlserver_start_time, SYSDATETIME())   AS [machine.uptime_seconds],
       (SELECT CONVERT(bigint, value_in_use) FROM sys.configurations
         WHERE name = 'max server memory (MB)')                   AS [config.max_server_memory_mb],
       (SELECT CONVERT(bigint, value_in_use) FROM sys.configurations
         WHERE name = 'min server memory (MB)')                   AS [config.min_server_memory_mb],
       CONVERT(bigint, si.committed_kb / 1024)                    AS [committed.total_server_memory_mb],
       CONVERT(bigint, si.committed_target_kb / 1024)             AS [committed.target_server_memory_mb],
       CONVERT(bigint, pm.physical_memory_in_use_kb / 1024)       AS [committed.process_memory_mb],
       /* The cache, as opposed to everything the process committed. Two
          independent sources for the same quantity: the clerk and the
          performance counter. They should agree, and a disagreement is itself
          worth seeing rather than hiding behind a single reading. */
       (SELECT CONVERT(bigint, SUM(pages_kb) / 1024) FROM sys.dm_os_memory_clerks
         WHERE type = 'MEMORYCLERK_SQLBUFFERPOOL')                AS [pool.buffer_pool_mb],
       (SELECT CONVERT(bigint, cntr_value / 1024) FROM sys.dm_os_performance_counters
         WHERE counter_name = 'Database Cache Memory (KB)')       AS [pool.database_cache_mb],
       (SELECT CONVERT(bigint, cntr_value / 1024) FROM sys.dm_os_performance_counters
         WHERE counter_name = 'Stolen Server Memory (KB)')        AS [pool.stolen_memory_mb],
       (SELECT CONVERT(bigint, cntr_value / 1024) FROM sys.dm_os_performance_counters
         WHERE counter_name = 'Free Memory (KB)')                 AS [pool.free_memory_mb],
       /* One row per NUMA node plus a total, so the minimum is the node under
          the most pressure. Reporting the total would flatter a machine whose
          nodes are unevenly loaded. */
       (SELECT MIN(cntr_value) FROM sys.dm_os_performance_counters
         WHERE counter_name = 'Page life expectancy')             AS [pool.page_life_expectancy_s],
       /* The usual target of 300 seconds per 4 GB, computed on the cache that
          exists rather than on installed memory. Computing it on the RAM is how
          a capped instance gets an impossible target. */
       CONVERT(bigint, 300.0 *
         ((SELECT SUM(pages_kb) / 1024.0 FROM sys.dm_os_memory_clerks
            WHERE type = 'MEMORYCLERK_SQLBUFFERPOOL') / 4096.0))  AS [pool.page_life_expectancy_target_s],
       pm.memory_utilization_percentage                           AS [pressure.working_set_pct],
       CONVERT(bit, pm.process_physical_memory_low)               AS [pressure.physical_low],
       CONVERT(bit, pm.process_virtual_memory_low)                AS [pressure.virtual_low],
       CONVERT(bigint, pm.locked_page_allocations_kb / 1024)      AS [pressure.locked_pages_mb]
FROM       sys.dm_os_sys_info        AS si
CROSS JOIN sys.dm_os_process_memory  AS pm
OPTION (RECOMPILE, MAXDOP 1);

/* Where the committed memory actually is. MEMORYCLERK_SQLBUFFERPOOL is the
   pool and is subject to the cap; everything else is outside it. Clerks holding
   nothing are dropped because SQL Server declares hundreds of them and the
   empty ones say nothing. */
SELECT TOP (25)
       type                                                       AS [clerk],
       CONVERT(bigint, SUM(pages_kb) / 1024)                      AS [pages_mb],
       CONVERT(bigint, SUM(virtual_memory_reserved_kb) / 1024)    AS [virtual_reserved_mb],
       CONVERT(bigint, SUM(virtual_memory_committed_kb) / 1024)   AS [virtual_committed_mb],
       CONVERT(bigint, SUM(awe_allocated_kb) / 1024)              AS [awe_allocated_mb]
FROM sys.dm_os_memory_clerks
GROUP BY type
HAVING SUM(pages_kb) > 0
ORDER BY SUM(pages_kb) DESC
OPTION (RECOMPILE, MAXDOP 1);
