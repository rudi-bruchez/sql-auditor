-- @scope:       instance
-- @resultsets:  root:object, pools:array, groups:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- Whether Resource Governor is in use, and what its pools and workload groups
-- have actually done.
--
-- Why this collector exists. Resource Governor caps CPU, memory and I/O per
-- group of sessions. When it is on and nobody remembers it, a workload that is
-- inexplicably slow is being throttled by a rule written years ago — a
-- diagnosis nothing else in this archive could reach. When it is off, the same
-- views still answer a different question, below.
--
-- WHAT THESE VIEWS RETURN WHEN IT IS OFF is not what the documentation leads
-- you to expect, and this comment was wrong before it was checked. An earlier
-- version of it stated that the default pool and group always appear. On the
-- 2017 instance this file was first run against, with Resource Governor
-- disabled, sys.dm_resource_governor_resource_pools returned exactly ONE row —
-- internal — and the workload groups view likewise. So the collector must not
-- assume a default pool is there, and a reader must not read its absence as a
-- misconfiguration.
--
-- What that one row is still worth: max_memory_kb on the internal pool is what
-- the engine reserved for itself, and statistics_start_time dates it.
--
-- is_enabled IS THEREFORE PROJECTED FIRST, and the pool count beside it. Two
-- opposite mistakes are possible here — reading the presence of pools as
-- evidence the feature is configured, and reading a single pool as evidence
-- something is broken — and the root exists to make both impossible.
--
-- IT IS ENTERPRISE-ONLY, and the views exist in every edition anyway. An
-- instance in Standard Edition reports the default pool and cannot have
-- classified anything into another one — so more than one non-internal pool on
-- a Standard instance is a leftover from a restore or an edition downgrade,
-- which is a finding for the analysis layer rather than a claim made here.
--
-- COUNTERS ARE SINCE STARTUP OR SINCE THE LAST RECONFIGURE. Altering the
-- configuration resets them, so a low count on a pool does not mean it is
-- unused. statistics_start_time says which, and it is projected for that reason.
--
-- NO JUDGEMENT IS APPLIED. A cap is not a defect; it is usually the point.
--
-- SQL Server 2012 is the floor. Both DMVs are 2008. Not collected for that
-- reason:
--   max_outstanding_io_per_volume and the I/O governance columns   (2014)
--   sys.dm_resource_governor_external_resource_pools               (2016)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT CAST(c.is_enabled AS int)                                  AS [is_enabled],
       c.classifier_function_id                                   AS [classifier_function_id],
       /* Named rather than left as an id: a classifier is a function somebody
          wrote, and its name is what lets a reader go and read it. It lives in
          master, which is why the lookup names that database explicitly — the
          collector may be connected anywhere. 0 means every session lands in
          the default group. */
       CASE WHEN c.classifier_function_id = 0 THEN NULL
            ELSE OBJECT_SCHEMA_NAME(c.classifier_function_id, DB_ID('master'))
                 + '.' + OBJECT_NAME(c.classifier_function_id, DB_ID('master'))
            END                                                   AS [classifier_function],
       (SELECT COUNT(*) FROM sys.dm_resource_governor_resource_pools)  AS [counts.pools],
       (SELECT COUNT(*) FROM sys.dm_resource_governor_workload_groups) AS [counts.groups],
       CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at]
FROM sys.resource_governor_configuration AS c
OPTION (RECOMPILE, MAXDOP 1);

SELECT p.name                                                     AS [pool],
       p.pool_id                                                  AS [pool_id],
       CONVERT(varchar(23), p.statistics_start_time, 126)         AS [statistics_since],
       p.min_cpu_percent                                          AS [cpu.min_pct],
       p.max_cpu_percent                                          AS [cpu.max_pct],
       p.cap_cpu_percent                                          AS [cpu.cap_pct],
       p.min_memory_percent                                       AS [memory.min_pct],
       p.max_memory_percent                                       AS [memory.max_pct],
       p.max_memory_kb                                            AS [memory.max_kb],
       p.used_memory_kb                                           AS [memory.used_kb],
       p.target_memory_kb                                         AS [memory.target_kb],
       p.total_cpu_usage_ms                                       AS [usage.cpu_ms],
       p.read_io_completed_total                                  AS [usage.reads],
       p.write_io_completed_total                                 AS [usage.writes],
       p.read_io_stall_total_ms                                   AS [usage.read_stall_ms],
       p.write_io_stall_total_ms                                  AS [usage.write_stall_ms]
FROM sys.dm_resource_governor_resource_pools AS p
ORDER BY p.pool_id
OPTION (RECOMPILE, MAXDOP 1);

SELECT g.name                                                     AS [group],
       p.name                                                     AS [pool],
       CONVERT(varchar(23), g.statistics_start_time, 126)         AS [statistics_since],
       g.importance                                               AS [importance],
       g.request_max_memory_grant_percent                         AS [limits.max_memory_grant_pct],
       g.request_max_cpu_time_sec                                 AS [limits.max_cpu_sec],
       g.max_dop                                                  AS [limits.max_dop],
       g.group_max_requests                                       AS [limits.max_requests],
       g.total_request_count                                      AS [usage.requests],
       g.total_cpu_usage_ms                                       AS [usage.cpu_ms],
       g.total_queued_request_count                               AS [usage.queued_requests],
       /* Two counters that say the limits above are biting rather than sitting
          there: a request whose memory grant was cut, and one killed for going
          past the CPU limit. */
       g.total_reduced_memgrant_count                             AS [usage.reduced_memory_grants],
       g.total_cpu_limit_violation_count                          AS [usage.cpu_limit_violations],
       g.total_lock_wait_count                                    AS [usage.lock_waits]
FROM sys.dm_resource_governor_workload_groups AS g
JOIN sys.dm_resource_governor_resource_pools  AS p ON p.pool_id = g.pool_id
ORDER BY p.pool_id, g.group_id
OPTION (RECOMPILE, MAXDOP 1);
