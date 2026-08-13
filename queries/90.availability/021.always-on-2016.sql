-- @scope:       instance
-- @resultsets:  root:object, groups:array, replicas:array, lag:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @min_version: 13.0
-- @timeout:     120
--
-- What SQL Server 2016 and later add to the Always On picture that 020 cannot
-- read.
--
-- Why this is a second file rather than columns added to 020. Four of the
-- columns below are 2016, one is 2017, and all of them sit in the SELECT list of
-- statements 020 would otherwise carry. A single missing column name fails the
-- whole batch, so merging them would lose the entire Always On collection on
-- 2012 and 2014 to gain four columns on 2016 — the split that
-- 011.all-databases-2014.sql already makes for the same reason.
--
-- secondary_lag_seconds IS THE REASON THIS FILE IS WORTH ITS EXISTENCE. 020
-- projects four raw numbers and deliberately computes no lag from them, because
-- a queue divided by an instantaneous rate is not a duration. This is the
-- engine's own answer, computed inside SQL Server from the commit timestamps
-- rather than from a division, and it is the only trustworthy "how far behind is
-- this secondary" available anywhere.
--
-- IT IS NULL ON THE PRIMARY AND ON A SYNCHRONOUS SECONDARY THAT IS CAUGHT UP,
-- and NULL there means "not applicable" rather than "not measured". A reader who
-- takes it for a missing measurement will look for a fault that is not there —
-- so the role sits beside it in the same row.
--
-- BASIC AVAILABILITY GROUPS are 2016 Standard Edition: one database, two
-- replicas, no readable secondary. basic_features is what tells them apart from
-- a full group that happens to have two replicas, and the two have completely
-- different capabilities — which matters the day somebody asks why the secondary
-- cannot be read.
--
-- CLUSTER TYPE is 2017: NONE for a read-scale group with no cluster underneath,
-- EXTERNAL for Pacemaker on Linux, WSFC for the classic case. A group with
-- cluster type NONE has no automatic failover at all, by design, and that is not
-- visible anywhere else.
--
-- NO JUDGEMENT IS APPLIED. A lag of forty seconds is not called a problem; on an
-- asynchronous replica behind a nightly load it may be exactly as designed.
--
-- SQL Server 2016 is the floor. Not collected for that reason:
--   sys.availability_groups.is_contained             (2022)
--   sys.dm_hadr_database_replica_states.write_lease_remaining_ticks (2019)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       CONVERT(int, SERVERPROPERTY('IsHadrEnabled'))              AS [is_enabled],
       (SELECT COUNT(*) FROM sys.availability_groups AS g WHERE g.basic_features = 1)
                                                                  AS [counts.basic_groups],
       (SELECT COUNT(*) FROM sys.availability_groups AS g WHERE g.is_distributed = 1)
                                                                  AS [counts.distributed_groups],
       /* The worst lag on the instance right now, so a reader has the headline
          before the detail. NULL when nothing is lagging or nothing applies. */
       (SELECT MAX(drs.secondary_lag_seconds) FROM sys.dm_hadr_database_replica_states AS drs)
                                                                  AS [max_secondary_lag_seconds]
OPTION (RECOMPILE, MAXDOP 1);

SELECT g.name                                                     AS [group],
       CAST(g.basic_features AS int)                              AS [is_basic],
       CAST(g.is_distributed AS int)                              AS [is_distributed],
       g.required_synchronized_secondaries_to_commit              AS [required_synchronized_secondaries],
       /* Whether a secondary that goes away can block commits on the primary.
          With required_synchronized_secondaries at 1 and one synchronous
          replica offline, the primary stops accepting writes — which is the
          behaviour, not a fault, and is the single most surprising thing about
          this setting. */
       CAST(g.dtc_support AS int)                                 AS [dtc_support],
       g.version                                                  AS [metadata_version]
FROM sys.availability_groups AS g
ORDER BY g.name
OPTION (RECOMPILE, MAXDOP 1);

SELECT g.name                                                     AS [group],
       r.replica_server_name                                      AS [replica],
       /* AUTOMATIC seeding streams the database over the availability endpoint
          instead of asking anyone to restore a backup. Which mode a replica
          uses changes what adding a database to the group actually requires. */
       r.seeding_mode_desc                                        AS [seeding_mode]
FROM sys.availability_replicas AS r
JOIN sys.availability_groups   AS g ON g.group_id = r.group_id
ORDER BY g.name, r.replica_server_name
OPTION (RECOMPILE, MAXDOP 1);

/* The engine's own lag figure, per database and replica, with the role beside it
   so a NULL can be read correctly. */
SELECT g.name                                                     AS [group],
       r.replica_server_name                                      AS [replica],
       DB_NAME(drs.database_id)                                   AS [database],
       rs.role_desc                                               AS [role],
       r.availability_mode_desc                                   AS [availability_mode],
       drs.secondary_lag_seconds                                  AS [secondary_lag_seconds],
       drs.synchronization_state_desc                             AS [synchronization_state],
       CONVERT(varchar(23), drs.last_commit_time, 126)            AS [last_commit]
FROM sys.dm_hadr_database_replica_states          AS drs
JOIN sys.availability_replicas                    AS r  ON r.replica_id = drs.replica_id
JOIN sys.availability_groups                      AS g  ON g.group_id = drs.group_id
LEFT JOIN sys.dm_hadr_availability_replica_states AS rs ON rs.replica_id = drs.replica_id
ORDER BY g.name, DB_NAME(drs.database_id), r.replica_server_name
OPTION (RECOMPILE, MAXDOP 1);
