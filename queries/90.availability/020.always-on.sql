-- @scope:       instance
-- @resultsets:  root:object, groups:array, replicas:array, databases:array, listeners:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     120
--
-- Availability groups, their replicas, their databases and their listeners.
--
-- WHY THERE IS NO GUARD AROUND THIS, and there was nearly one. The obvious shape
-- is IF SERVERPROPERTY('IsHadrEnabled') = 1 around the body — but the catalog
-- views and the dm_hadr_* DMVs return an EMPTY ROWSET on an instance that is not
-- enabled rather than raising, so the guard would buy nothing and would cost the
-- one thing that matters: four empty result sets are a stated finding, and four
-- absent ones are a collector that appears to have failed. The root projects
-- is_enabled so a reader can tell an empty group list from a disabled feature.
--
-- THE databases RESULT SET IS THE ONE THAT MATTERS. The other three describe the
-- configuration, which changes rarely and could be read from a screenshot. This
-- one carries the four numbers that say whether the secondaries are keeping up,
-- and they are the reason this file exists.
--
-- WHAT IS DELIBERATELY NOT COMPUTED HERE: an RPO. The obvious move is
-- redo_queue_size / redo_rate and call it "seconds behind". It is wrong twice
-- over — the division explodes when the rate is momentarily zero, which is the
-- normal state of an idle secondary, and a rate measured over an instant does
-- not predict how long a queue will take to drain. The four raw numbers are
-- projected and the arithmetic belongs to whoever can see more than one sample.
-- SQL Server's own answer to this, secondary_lag_seconds, is 2016 and lives in
-- 021.
--
-- AND A QUEUE READ ONCE IS NOT A TREND. redo_queue_size at a single moment does
-- not distinguish a queue draining from a queue growing. The only honest way to
-- tell is to sample twice, which a collector that runs once cannot do — so the
-- collection timestamp is projected and the question is left open rather than
-- answered wrongly.
--
-- NO JUDGEMENT IS APPLIED. No replica is called behind, no group unhealthy.
-- SYNCHRONIZING on an asynchronous replica is the normal steady state, not a
-- problem, and the collector has no way to know which the reader is looking at.
--
-- db_failover WAS PROJECTED HERE AND IS NOT ANY MORE. It is SQL Server 2016,
-- and one column newer than the declared floor fails the WHOLE batch with
-- "Invalid column name" — so keeping it would have cost every 2012 and 2014
-- instance the databases result set this file exists for, to gain one flag. It
-- is in 021 with the rest of what 2016 added.
--
-- SQL Server 2012 is the floor and every column below is 2012. What 2016 added
-- is in 021.always-on-2016.sql, in its own file, so that this one keeps working
-- on 2012 and 2014.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       CONVERT(int, SERVERPROPERTY('IsHadrEnabled'))              AS [is_enabled],
       CONVERT(sysname, SERVERPROPERTY('HadrManagerStatus'))      AS [manager_status],
       /* The Windows cluster underneath. Empty on an instance that is not a
          cluster member, which is itself the answer when a group will not come
          online. */
       (SELECT TOP (1) c.cluster_name FROM sys.dm_hadr_cluster AS c)      AS [cluster.name],
       (SELECT TOP (1) c.quorum_type_desc FROM sys.dm_hadr_cluster AS c)  AS [cluster.quorum_type],
       (SELECT TOP (1) c.quorum_state_desc FROM sys.dm_hadr_cluster AS c) AS [cluster.quorum_state],
       (SELECT COUNT(*) FROM sys.availability_groups)             AS [counts.groups],
       (SELECT COUNT(*) FROM sys.availability_replicas)           AS [counts.replicas],
       (SELECT COUNT(*) FROM sys.dm_hadr_database_replica_states) AS [counts.database_replicas],
       (SELECT COUNT(*) FROM sys.availability_group_listeners)    AS [counts.listeners]
OPTION (RECOMPILE, MAXDOP 1);

SELECT g.name                                                     AS [group],
       ags.primary_replica                                        AS [primary_replica],
       ags.primary_recovery_health_desc                           AS [primary_recovery_health],
       ags.secondary_recovery_health_desc                         AS [secondary_recovery_health],
       ags.synchronization_health_desc                            AS [synchronization_health],
       g.failure_condition_level                                  AS [failure_condition_level],
       g.health_check_timeout                                     AS [health_check_timeout_ms],
       g.automated_backup_preference_desc                         AS [backup_preference]
FROM sys.availability_groups                        AS g
LEFT JOIN sys.dm_hadr_availability_group_states     AS ags ON ags.group_id = g.group_id
ORDER BY g.name
OPTION (RECOMPILE, MAXDOP 1);

/* One row per replica. availability_mode and failover_mode are the pair that
   decides what a failover costs: synchronous with automatic failover is the only
   combination that fails over without a decision, and asynchronous can lose
   committed transactions by design. */
SELECT g.name                                                     AS [group],
       r.replica_server_name                                      AS [replica],
       rs.role_desc                                               AS [role],
       r.availability_mode_desc                                   AS [availability_mode],
       r.failover_mode_desc                                       AS [failover_mode],
       rs.operational_state_desc                                  AS [operational_state],
       rs.connected_state_desc                                    AS [connected_state],
       rs.recovery_health_desc                                    AS [recovery_health],
       rs.synchronization_health_desc                             AS [synchronization_health],
       CAST(rs.is_local AS int)                                   AS [is_local],
       r.endpoint_url                                             AS [endpoint_url],
       r.session_timeout                                          AS [session_timeout_s],
       r.primary_role_allow_connections_desc                      AS [connections.as_primary],
       r.secondary_role_allow_connections_desc                    AS [connections.as_secondary],
       r.backup_priority                                          AS [backup_priority],
       r.read_only_routing_url                                    AS [read_only_routing_url],
       /* Why a partner is not connected, when it is not. */
       rs.last_connect_error_number                               AS [last_connect_error.number],
       rs.last_connect_error_description                          AS [last_connect_error.description],
       CONVERT(varchar(23), rs.last_connect_error_timestamp, 126) AS [last_connect_error.at]
FROM sys.availability_replicas                      AS r
JOIN sys.availability_groups                        AS g  ON g.group_id = r.group_id
LEFT JOIN sys.dm_hadr_availability_replica_states   AS rs ON rs.replica_id = r.replica_id
ORDER BY g.name, r.replica_server_name
OPTION (RECOMPILE, MAXDOP 1);

/* The four queue and rate numbers, raw, with the instants beside them. See the
   header on why no lag is computed from them here. */
SELECT g.name                                                     AS [group],
       r.replica_server_name                                      AS [replica],
       DB_NAME(drs.database_id)                                   AS [database],
       drs.synchronization_state_desc                             AS [synchronization_state],
       drs.synchronization_health_desc                            AS [synchronization_health],
       drs.database_state_desc                                    AS [database_state],
       CAST(drs.is_local AS int)                                  AS [is_local],
       CAST(drs.is_commit_participant AS int)                     AS [is_commit_participant],
       CAST(drs.is_suspended AS int)                              AS [is_suspended],
       drs.suspend_reason_desc                                    AS [suspend_reason],
       /* Sending: what the primary has not yet shipped. */
       drs.log_send_queue_size                                    AS [send.queue_kb],
       drs.log_send_rate                                          AS [send.rate_kb_s],
       /* Redoing: what the secondary has received and not yet applied. This is
          the one that decides how long a failover takes. */
       drs.redo_queue_size                                        AS [redo.queue_kb],
       drs.redo_rate                                              AS [redo.rate_kb_s],
       drs.filestream_send_rate                                   AS [send.filestream_rate_kb_s],
       /* The instants. A queue size means little without knowing when the last
          block was hardened. */
       CONVERT(varchar(23), drs.last_sent_time, 126)              AS [times.last_sent],
       CONVERT(varchar(23), drs.last_received_time, 126)          AS [times.last_received],
       CONVERT(varchar(23), drs.last_hardened_time, 126)          AS [times.last_hardened],
       CONVERT(varchar(23), drs.last_redone_time, 126)            AS [times.last_redone],
       CONVERT(varchar(23), drs.last_commit_time, 126)            AS [times.last_commit],
       drs.last_commit_lsn                                        AS [lsn.last_commit],
       drs.last_hardened_lsn                                      AS [lsn.last_hardened],
       drs.end_of_log_lsn                                         AS [lsn.end_of_log],
       drs.low_water_mark_for_ghosts                              AS [low_water_mark_for_ghosts]
FROM sys.dm_hadr_database_replica_states            AS drs
JOIN sys.availability_replicas                      AS r ON r.replica_id = drs.replica_id
JOIN sys.availability_groups                        AS g ON g.group_id = drs.group_id
ORDER BY g.name, DB_NAME(drs.database_id), r.replica_server_name
OPTION (RECOMPILE, MAXDOP 1);

SELECT g.name                                                     AS [group],
       l.dns_name                                                 AS [dns_name],
       l.port                                                     AS [port],
       l.is_conformant                                            AS [is_conformant],
       l.ip_configuration_string_from_cluster                     AS [ip_configuration],
       ls.state_desc                                              AS [tcp_state],
       ls.ip_address                                              AS [ip_address],
       ls.listener_id                                             AS [listener_id]
FROM sys.availability_group_listeners               AS l
JOIN sys.availability_groups                        AS g  ON g.group_id = l.group_id
LEFT JOIN sys.dm_tcp_listener_states                AS ls ON ls.port = l.port AND ls.type = 1
ORDER BY g.name, l.dns_name
OPTION (RECOMPILE, MAXDOP 1);
