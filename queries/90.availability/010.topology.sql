-- @scope:       instance
-- @resultsets:  root:object, mirroring:array, endpoints:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     60
--
-- Which high-availability and replication mechanisms exist on this instance, in
-- booleans and counts.
--
-- Why this file exists before the four that follow it. Always On, log shipping
-- and replication have three different metadata models, three ways of measuring
-- lag and three failure modes, so each gets its own collector. What none of them
-- can do is tell a reader that the OTHERS are absent — a missing file reads as a
-- collector that failed, not as a technology nobody uses. This one says, in a
-- single document, what is and is not on the instance, and that is what makes
-- the absence of the others legible.
--
-- THE STANDALONE SERVER IS THE CASE THIS AREA IS SHAPED AROUND. Most audited
-- instances replicate nothing, and for them every count here is zero and every
-- array empty — which is a finding, stated, rather than a gap. A zero that is
-- printed is a measurement; a file that is not there is a question.
--
-- EVERY COUNT IS PROJECTED EVEN AT ZERO, for that reason. Nothing here is
-- omitted because it happened to be empty.
--
-- DATABASE MIRRORING IS STILL HERE, deprecated since 2012 and still in
-- production in more places than anyone admits. It is two columns and it costs
-- nothing to look; an instance mirroring a database and not knowing it is
-- exactly the sort of thing an audit exists to surface.
--
-- AND A MIRRORING COUNT OF ZERO CAN MEAN TWO THINGS, which is why
-- db_mirroring.readable_as_sysadmin sits beside it. sys.database_mirroring returns
-- NULL in every mirroring_* column, on the instance holding the MIRROR copy, to
-- anyone who is not sysadmin — and the audit login this tool is built for never
-- is. So the guid filter drops the row, the count reads 0, and the archive would
-- say "this instance mirrors nothing" when nobody was allowed to look. The flag
-- and rows_examined make the two cases distinguishable, which rule 2 requires;
-- neither is a judgement, both are facts.
--
-- ENDPOINTS ARE COLLECTED BECAUSE THEY EXPLAIN FAILURES. An availability group
-- that will not connect, a mirroring session stuck in DISCONNECTED — the answer
-- is usually the endpoint: stopped, on an unexpected port, or with an
-- encryption or authentication setting the partner does not share. Nothing else
-- in this archive reports them.
--
-- NO JUDGEMENT IS APPLIED. Mirroring is not called obsolete here, a single
-- replica is not called a risk, and no replication topology is called wrong.
--
-- LOG SHIPPING IS COUNTED IN 030 AND NOT HERE, and that is a permission
-- boundary rather than a preference. The log_shipping_* tables in msdb are
-- reachable by neither MSDB READ — which is SELECT on backupset and nothing
-- else — nor SQLAgentReaderRole; they need a grant of their own, which 030
-- declares. Left here, one denied SELECT would take this whole file down with
-- it, and this is the file that has to work everywhere: it is what makes the
-- absence of the others a fact rather than a failure. Measured, not assumed —
-- the first version did read them and was refused on the very audit login it
-- was written for.
--
-- SQL Server 2012 is the floor. Not collected for that reason:
--   sys.availability_groups.cluster_type_desc   (2017; no file reads it yet)
--
-- Not collected for a DIFFERENT reason, and the distinction matters to whoever
-- maintains this: sys.dm_hadr_cluster and its quorum columns exist in 2012 and
-- are read by 020. They are absent here because they need VIEW SERVER STATE,
-- which this file deliberately does not ask for — see above.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       /* The instance-level switch. 0 means the feature is not enabled in
          Configuration Manager, whatever the catalog views below contain — a
          restored database can leave availability metadata behind on an
          instance that cannot use it. */
       CONVERT(int, SERVERPROPERTY('IsHadrEnabled'))              AS [always_on.is_enabled],
       CONVERT(sysname, SERVERPROPERTY('HadrManagerStatus'))      AS [always_on.manager_status],
       (SELECT COUNT(*) FROM sys.availability_groups)             AS [always_on.groups],
       (SELECT COUNT(*) FROM sys.availability_replicas)           AS [always_on.replicas],
       (SELECT COUNT(*) FROM sys.availability_group_listeners)    AS [always_on.listeners],
       /* Replication is read from the database flags rather than from the
          distribution database, whose name is not fixed and which a collector
          cannot reach without knowing it. 040 explains that choice at length. */
       (SELECT COUNT(*) FROM sys.databases AS d WHERE d.is_published = 1)       AS [replication.published],
       (SELECT COUNT(*) FROM sys.databases AS d WHERE d.is_merge_published = 1) AS [replication.merge_published],
       (SELECT COUNT(*) FROM sys.databases AS d WHERE d.is_subscribed = 1)      AS [replication.subscribed],
       (SELECT COUNT(*) FROM sys.databases AS d WHERE d.is_distributor = 1)     AS [replication.distributor_databases],
       CONVERT(int, SERVERPROPERTY('IsClustered'))                AS [windows_cluster.is_clustered],
       /* Mirroring, counted here and detailed below — with the one fact that
          decides whether the count means anything. */
       (SELECT COUNT(*) FROM sys.database_mirroring AS m
         WHERE m.mirroring_guid IS NOT NULL)                      AS [db_mirroring.databases],
       (SELECT COUNT(*) FROM sys.database_mirroring)              AS [db_mirroring.rows_examined],
       CONVERT(int, IS_SRVROLEMEMBER('sysadmin'))                 AS [db_mirroring.readable_as_sysadmin]
OPTION (RECOMPILE, MAXDOP 1);

/* Only the databases actually mirroring. A row per database with a NULL guid
   would be one row per database on the instance, saying nothing. */
SELECT DB_NAME(m.database_id)                                     AS [database],
       m.mirroring_role_desc                                      AS [role],
       m.mirroring_state_desc                                     AS [state],
       m.mirroring_safety_level_desc                              AS [safety_level],
       m.mirroring_partner_name                                   AS [partner],
       m.mirroring_partner_instance                               AS [partner_instance],
       m.mirroring_witness_name                                   AS [witness],
       m.mirroring_witness_state_desc                             AS [witness_state],
       m.mirroring_failover_lsn                                   AS [failover_lsn]
FROM sys.database_mirroring AS m
WHERE m.mirroring_guid IS NOT NULL
ORDER BY DB_NAME(m.database_id)
OPTION (RECOMPILE, MAXDOP 1);

/* Every endpoint that carries availability traffic, plus its TCP state.

   The LEFT JOINs are for shape, not for state: sys.tcp_endpoints carries a row
   per TCP endpoint whatever its state, so an endpoint that is stopped still
   appears. What they protect against is an endpoint that is not TCP, and one
   that is not a mirroring endpoint. An earlier comment here justified them by
   claiming rows go missing "on some versions", which the documentation does not
   say — a reason invented in a corpus whose headers ARE its documentation. */
SELECT e.name                                                     AS [name],
       e.type_desc                                                AS [type],
       e.state_desc                                               AS [state],
       e.protocol_desc                                            AS [protocol],
       t.port                                                     AS [port],
       t.is_dynamic_port                                          AS [is_dynamic_port],
       t.ip_address                                               AS [ip_address],
       dme.role_desc                                              AS [mirroring_role],
       dme.encryption_algorithm_desc                              AS [encryption],
       CAST(dme.is_encryption_enabled AS int)                     AS [is_encryption_enabled],
       dme.connection_auth_desc                                   AS [authentication]
FROM sys.endpoints                          AS e
LEFT JOIN sys.tcp_endpoints                 AS t   ON t.endpoint_id = e.endpoint_id
LEFT JOIN sys.database_mirroring_endpoints  AS dme ON dme.endpoint_id = e.endpoint_id
/* type 4 is DATABASE_MIRRORING, which is also what an availability group uses.
   The TSQL endpoints every instance has are not what this collector is about. */
WHERE e.type = 4
ORDER BY e.name
OPTION (RECOMPILE, MAXDOP 1);
