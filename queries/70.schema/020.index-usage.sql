-- @scope:       database
-- @resultsets:  root:object, usage:array, missing:array
-- @permissions: CONNECT, VIEW SERVER STATE, VIEW ANY DEFINITION
-- @timeout:     120
--
-- Index usage counters and optimizer index suggestions, complete and
-- unfiltered, for one database.
--
-- Why this collector exists, and why it overlaps 20.databases/020.properties:
-- that file carries unused_indexes and missing_indexes as TOP (25) triage
-- views, which is the right shape for a routine audit. This one is a BASELINE.
-- Both DMVs behind it reset on restart, and sys.dm_db_index_usage_stats also
-- loses a database's rows when that database is closed or detached. A top-25
-- extract cannot be re-expanded afterwards, so before a restart the whole
-- picture has to be taken or it is gone.
--
-- Consequently there is no TOP and no threshold here. Every index of every
-- user table is emitted, used or not.
--
-- has_usage_row is the point of the usage result set, and the reason the
-- counters are NOT collapsed with ISNULL(…, 0). An index with a DMV row
-- showing zero seeks has been considered and not used; an index with no DMV
-- row at all has not been touched since the counters last reset. Those are
-- different facts, and writing 0 for both destroys the distinction exactly
-- when it matters — deciding whether an index is dead.
--
-- Durations are compared in SECONDS, never milliseconds: DATEDIFF(millisecond,
-- …) overflows a 32-bit int after about 24 days, so it fails on precisely the
-- long-uptime servers whose counters are most worth collecting.
--
-- The instance start time is emitted because a usage counter is meaningless
-- without the period it accumulated over. It is an upper bound on that period,
-- not a measurement — the counters can also be cleared without a restart.
--
-- SQL Server 2012 is the floor. Not collected for that reason:
--   sys.dm_db_index_operational_stats leaf_page_merge (behaviour differs)
--   sys.dm_db_missing_index_group_stats_query          (2019)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT DB_NAME()                                                AS [database],
       si.sqlserver_start_time                                  AS [instance_start],
       DATEDIFF(second, si.sqlserver_start_time, GETDATE())     AS [seconds_since_instance_start],
       SYSDATETIME()                                            AS [collected_at],
       (SELECT COUNT(*) FROM sys.indexes AS i
        JOIN sys.objects AS o ON o.object_id = i.object_id AND o.type = 'U') AS [indexes_total],
       (SELECT COUNT(*) FROM sys.dm_db_index_usage_stats AS us
        WHERE us.database_id = DB_ID())                         AS [indexes_with_usage_row],
       (SELECT COUNT(*) FROM sys.dm_db_missing_index_details AS mid
        WHERE mid.database_id = DB_ID())                        AS [missing_suggestions]
FROM sys.dm_os_sys_info AS si
OPTION (RECOMPILE, MAXDOP 1);

/* Heaps are included: index_id 0 is a heap and its usage counters are as real
   as any other. Excluding them would hide the read pattern of exactly the
   tables most likely to need a clustered index. */
SELECT SCHEMA_NAME(o.schema_id) + '.' + o.name                  AS [table],
       i.name                                                   AS [index_name],
       i.index_id                                               AS [index_id],
       i.type_desc                                              AS [index_type],
       CAST(i.is_unique AS int)                                 AS [is_unique],
       CAST(i.is_primary_key AS int)                            AS [is_primary_key],
       CAST(i.is_unique_constraint AS int)                      AS [is_unique_constraint],
       CAST(i.is_disabled AS int)                               AS [is_disabled],
       i.filter_definition                                      AS [filter_definition],
       ps.row_count                                             AS [size.rows],
       CAST(ps.reserved_page_count * 8.0 / 1024 AS DECIMAL(18,2)) AS [size.reserved_mb],
       CASE WHEN us.index_id IS NULL THEN 0 ELSE 1 END          AS [usage.has_usage_row],
       us.user_seeks                                            AS [usage.user_seeks],
       us.user_scans                                            AS [usage.user_scans],
       us.user_lookups                                          AS [usage.user_lookups],
       us.user_updates                                          AS [usage.user_updates],
       us.last_user_seek                                        AS [usage.last_user_seek],
       us.last_user_scan                                        AS [usage.last_user_scan],
       us.last_user_lookup                                      AS [usage.last_user_lookup],
       us.last_user_update                                      AS [usage.last_user_update],
       us.system_seeks                                          AS [usage.system_seeks],
       us.system_scans                                          AS [usage.system_scans]
FROM       sys.indexes AS i
JOIN       sys.objects AS o
        ON o.object_id = i.object_id AND o.type = 'U'
LEFT JOIN  sys.dm_db_index_usage_stats AS us
        ON us.object_id = i.object_id AND us.index_id = i.index_id
       AND us.database_id = DB_ID()
LEFT JOIN (SELECT p.object_id, p.index_id,
                  SUM(p.row_count)            AS row_count,
                  SUM(p.reserved_page_count)  AS reserved_page_count
           FROM sys.dm_db_partition_stats AS p
           GROUP BY p.object_id, p.index_id) AS ps
        ON ps.object_id = i.object_id AND ps.index_id = i.index_id
ORDER BY o.schema_id, o.name, i.index_id
OPTION (RECOMPILE, MAXDOP 1);

/* No impact score and no ordering by it. avg_user_impact is the optimizer's
   own estimate and multiplying it into a single number is the analysis layer's
   business; the raw components are what resets on restart. */
SELECT OBJECT_SCHEMA_NAME(mid.object_id, mid.database_id) + '.'
     + OBJECT_NAME(mid.object_id, mid.database_id)              AS [table],
       mid.equality_columns                                     AS [equality_columns],
       mid.inequality_columns                                   AS [inequality_columns],
       mid.included_columns                                     AS [included_columns],
       migs.user_seeks                                          AS [user_seeks],
       migs.user_scans                                          AS [user_scans],
       CAST(migs.avg_total_user_cost AS DECIMAL(18,4))          AS [avg_total_user_cost],
       CAST(migs.avg_user_impact AS DECIMAL(5,1))               AS [avg_user_impact_pct],
       migs.last_user_seek                                      AS [last_user_seek],
       migs.last_user_scan                                      AS [last_user_scan]
FROM       sys.dm_db_missing_index_group_stats AS migs
JOIN       sys.dm_db_missing_index_groups AS mig
        ON mig.index_group_handle = migs.group_handle
JOIN       sys.dm_db_missing_index_details AS mid
        ON mid.index_handle = mig.index_handle
WHERE mid.database_id = DB_ID()
ORDER BY mid.object_id
OPTION (RECOMPILE, MAXDOP 1);
