-- @scope:       instance
-- @resultsets:  root:object, nodes:array, shared_drives:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- Whether this instance is a failover cluster instance, and if so which nodes
-- it can move to and which disks it shares with them.
--
-- WHY THIS COLLECTOR EXISTS. The archive already says that an instance is
-- clustered: 90.availability/010.topology.sql carries
-- SERVERPROPERTY('IsClustered'). It has never said WHICH nodes, and that is the
-- question an audit actually needs. A two-node cluster with one node down has
-- no failover left and looks exactly like a healthy two-node cluster from the
-- surviving node: the instance is up, the databases are online, and nothing in
-- the archive was different. The node list is what separates them.
--
-- The shared drives matter for the same reason and are the other half of the
-- FCI's identity: an FCI can only fail over what sits on storage the other
-- nodes can mount, so a data file on a local disk is a database that will not
-- come back after a failover. 10.system/030.file-io.sql carries the file paths;
-- this file carries which drive letters the cluster owns, and the comparison is
-- the finding.
--
-- THE VIEWS THAT LOOK LIKE AN ANSWER AND ARE NOT. This is the reason the file
-- is shaped the way it is. Measured on 16.0.4265.3 and 17.0.4065.4, both plain
-- standalone containers with no cluster of any kind:
--
--   SERVERPROPERTY('IsClustered')        0          correct
--   sys.dm_os_cluster_nodes              0 rows     correct
--   sys.dm_io_cluster_shared_drives      0 rows     correct
--   sys.dm_hadr_cluster                  1 ROW      misleading
--   sys.dm_hadr_cluster_members          1 ROW      misleading
--
-- The dm_hadr_cluster row carries an EMPTY cluster_name, and then
-- quorum_type_desc NODE_MAJORITY and quorum_state_desc NORMAL_QUORUM, which
-- read as a healthy cluster. The dm_hadr_cluster_members row names the host and
-- describes it as a CLUSTER_NODE whose state is UP, with a NULL vote count.
-- Anyone reading either one concludes there is a one-node cluster. There is no
-- cluster at all.
--
-- So the two hadr views are counted here and never used as evidence, and the
-- two dm_os / dm_io views are the ones the arrays come from. The counts are
-- projected precisely so that a reader who finds a quorum state elsewhere in
-- the archive can see what it was worth: counts.hadr_cluster_members of 1 on an
-- instance with counts.nodes of 0 is the signature of the trap, not of a
-- cluster.
--
-- 90.availability/020.always-on.sql projects cluster.name, cluster.quorum_type
-- and cluster.quorum_state from sys.dm_hadr_cluster. Those three are subject to
-- exactly this, and 020 now says so where it projects them.
--
-- WHAT WAS MEASURED HERE IS READABILITY, NOT CONTENT. Both instances are
-- standalone, so every array below came back empty, in under 21 ms. Producing a
-- real node list would mean building a Windows failover cluster, which the lab
-- does not have, so the columns are written from the documented shape and only
-- the query is proved to run. Saying so is better than letting a reader assume
-- the shape was seen full.
--
-- SQL Server 2012 is the floor, and on 2012 this file is the ONLY thing that
-- answers for an FCI. sys.dm_hadr_cluster does not cover a failover cluster
-- instance on 2012; support for reading it from an FCI arrived in 2014. An
-- archive taken on a 2012 FCI therefore says nothing about its cluster unless
-- these two views are read, and they go back further than the floor.
--
-- NO JUDGEMENT IS APPLIED. A node that is down may be down for a patch window,
-- and a cluster with no shared drive may be an Always On FCI-less deployment
-- that never had one. What makes a finding is the pair: is_clustered of 1 with
-- fewer nodes up than nodes known, or a data file on a drive the cluster does
-- not own. Both comparisons need another file, and neither is decided here.
--
-- Not collected, deliberately:
--   sys.dm_hadr_cluster_networks    (network names and subnets; it is cluster
--     topology rather than instance topology, and it names addresses)
--   the node list's own IP addresses (sys.dm_os_cluster_nodes does not carry
--     them, and the views that do are not readable from T-SQL)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @err_cluster int = 0, @msg nvarchar(2048) = N'';

DECLARE @nodes TABLE (
    [node_name]          nvarchar(128),
    [status]             int,
    [status_description] nvarchar(128),
    [is_current_owner]   int);

DECLARE @drives TABLE ([drive_name] nvarchar(128));

BEGIN TRY
    INSERT INTO @nodes
    SELECT n.NodeName, n.status, n.status_description,
           CONVERT(int, n.is_current_owner)
    FROM sys.dm_os_cluster_nodes AS n
    OPTION (RECOMPILE, MAXDOP 1);

    INSERT INTO @drives
    SELECT d.DriveName
    FROM sys.dm_io_cluster_shared_drives AS d
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_cluster = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT
    CONVERT(sysname, SERVERPROPERTY('ServerName'))              AS [instance],
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    -- The authoritative answer, and the only one in this file that is.
    -- Everything below is either a list or a count of something that lies.
    CONVERT(int, SERVERPROPERTY('IsClustered'))                 AS [is_clustered],
    CONVERT(int, SERVERPROPERTY('IsHadrEnabled'))               AS [is_hadr_enabled],
    (SELECT COUNT(*) FROM @nodes)                               AS [counts.nodes],
    -- A node this instance can actually fail over to right now. status 0 is up
    -- in sys.dm_os_cluster_nodes; every other value is a node that is not
    -- available, and status_description below names which.
    (SELECT COUNT(*) FROM @nodes WHERE [status] = 0)            AS [counts.nodes_up],
    (SELECT COUNT(*) FROM @drives)                              AS [counts.shared_drives],
    /* The two views that report a cluster where there is none, counted and
       never read. Measured on two standalone instances: both return exactly one
       row. A reader who finds counts.nodes at 0 beside counts.hadr_cluster_rows
       at 1 is looking at that artefact and not at a one-node cluster. */
    (SELECT COUNT(*) FROM sys.dm_hadr_cluster)                  AS [counts.hadr_cluster_rows],
    (SELECT COUNT(*) FROM sys.dm_hadr_cluster_members)          AS [counts.hadr_cluster_members],
    -- The name, which is the one field of sys.dm_hadr_cluster that tells the
    -- truth on a standalone instance: it comes back as an empty string. NULLIF
    -- turns that into a null rather than letting an empty string read as a
    -- cluster whose name nobody bothered to project.
    (SELECT NULLIF(TOP_1.cluster_name, N'')
       FROM (SELECT TOP (1) c.cluster_name FROM sys.dm_hadr_cluster AS c) AS TOP_1)
                                                                AS [hadr_cluster.name],
    CASE WHEN @err_cluster = 0 THEN 1 ELSE 0 END                AS [collected.cluster],
    @err_cluster                                                AS [errors.cluster],
    NULLIF(@msg, N'')                                           AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

/* One row per node of the Windows failover cluster this instance belongs to.
   Empty on a standalone instance, and empty is the correct answer there rather
   than a failure: this is the view that does not invent a cluster. */
SELECT
    n.[node_name]                                               AS [node],
    n.[status]                                                  AS [status],
    n.[status_description]                                      AS [status_description],
    -- Which node is running the instance at the moment of collection. It moves,
    -- so it is a fact about the collection and not about the cluster.
    n.[is_current_owner]                                        AS [is_current_owner]
FROM @nodes AS n
ORDER BY n.[node_name]
OPTION (RECOMPILE, MAXDOP 1);

/* The drives the cluster owns. Read this beside the file paths in
   10.system/030.file-io.sql: a data file on a drive that is not in this list is
   a database that does not come back after a failover, and that comparison is
   the reason this array exists. */
SELECT
    d.[drive_name]                                              AS [drive]
FROM @drives AS d
ORDER BY d.[drive_name]
OPTION (RECOMPILE, MAXDOP 1);
