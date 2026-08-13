-- @scope:       instance
-- @resultsets:  root:object, services:array, startup_parameters:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
-- @min_version: 13.0.4001
--
-- The SQL Server services on the host, and the parameters the engine was
-- started with.
--
-- Why this collector exists: two findings on a real audit came from here and
-- from nowhere else. Instant file initialization was disabled, which turns a
-- 124 GB autogrowth into 124 GB of zero-writing while writes to that file
-- wait. And the startup parameters held nothing but -d, -e and -l, which
-- refuted the theory that a predecessor had left trace flags behind that a
-- restart would silently drop.
--
-- Both are questions a restart makes urgent, and neither is answerable from
-- any other view.
--
-- INSTANT FILE INITIALIZATION IS A PROPERTY OF THE SERVICE ACCOUNT, NOT OF
-- SQL SERVER. It is granted by the "Perform volume maintenance tasks" Windows
-- right, it takes effect at service start, and it never applies to log files —
-- a transaction log is always zero-filled, whatever the setting. A report must
-- not present it as a database or instance option.
--
-- startup_parameters is the persisted truth. Trace flags set with
-- DBCC TRACEON are NOT here: they live only in the running instance and are
-- lost on restart. Comparing this list against DBCC TRACESTATUS(-1), which
-- 050.tempdb.sql collects, is what tells a reader whether a flag survives a
-- restart — which is why the two are collected separately and joined by the
-- analysis layer rather than merged here.
--
-- The version floor above is set by one column:
-- sys.dm_server_services.instant_file_initialization_enabled arrived in
-- SQL Server 2016 SP1. The view itself predates the 2012 floor, but a
-- collector that silently dropped its most valuable column on older instances
-- would report "IFI unknown" as though it had looked. On 2012 to 2016 RTM the
-- setting has to be read from the Windows security policy instead, and this
-- collector is skipped with that reason recorded.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

-- SERVERPROPERTY returns sql_variant, always. Projected raw, the driver hands
-- the encoder a type it cannot render and the value is dropped with a warning,
-- so the column reaches the archive empty. Converting is not cosmetic: it is
-- what makes the value survive the trip. InstanceName carried the same defect
-- without ever warning, because it is NULL on a default instance and a NULL
-- sql_variant has no base type to complain about — it would have surfaced on
-- the first named instance instead.
SELECT CONVERT(sysname,  SERVERPROPERTY('MachineName'))           AS [machine_name],
       CONVERT(sysname,  SERVERPROPERTY('ComputerNamePhysicalNetBIOS'))
                                                                  AS [physical_name],
       CONVERT(sysname,  SERVERPROPERTY('InstanceName'))          AS [instance_name],
       CONVERT(bit,      SERVERPROPERTY('IsClustered'))           AS [is_clustered],
       si.sqlserver_start_time                                    AS [instance_start],
       DATEDIFF(second, si.sqlserver_start_time, GETDATE())       AS [seconds_since_instance_start],
       (SELECT COUNT(*) FROM sys.dm_server_services)              AS [services_reported],
       (SELECT COUNT(*) FROM sys.dm_server_registry
        WHERE registry_key LIKE '%Parameters')                    AS [startup_parameters_count]
FROM sys.dm_os_sys_info AS si
OPTION (RECOMPILE, MAXDOP 1);

/* service_account is a login name and belongs to the "names things" disclosure
   MANIFEST.txt already makes. It is collected because a service running under
   a personal account, or under LocalSystem, is itself a finding. */
SELECT s.servicename                                              AS [service],
       s.startup_type_desc                                        AS [startup_type],
       s.status_desc                                              AS [status],
       s.service_account                                          AS [service_account],
       s.process_id                                               AS [process_id],
       s.last_startup_time                                        AS [last_startup_time],
       s.is_clustered                                             AS [is_clustered],
       s.cluster_nodename                                         AS [cluster_node],
       s.instant_file_initialization_enabled                      AS [instant_file_initialization],
       s.filename                                                 AS [binary_path]
FROM sys.dm_server_services AS s
ORDER BY s.servicename
OPTION (RECOMPILE, MAXDOP 1);

/* The -T entries here are the trace flags that survive a restart. Anything in
   DBCC TRACESTATUS but absent from this list disappears at the next one. */
/* value_data is sql_variant, which LIKE rejects outright with error 8116 —
   it has to be converted before it can be matched or projected. */
SELECT r.value_name                                               AS [name],
       CONVERT(nvarchar(1024), r.value_data)                      AS [value],
       CASE WHEN CONVERT(nvarchar(1024), r.value_data) LIKE '-T%'
            THEN 1 ELSE 0 END                                     AS [is_trace_flag]
FROM sys.dm_server_registry AS r
WHERE r.registry_key LIKE '%Parameters'
ORDER BY r.value_name
OPTION (RECOMPILE, MAXDOP 1);
