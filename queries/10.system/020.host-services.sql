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
-- right and it takes effect at service start. A report must not present it as
-- a database or instance option. Two limits belong with it, because a
-- recommendation that ignores either one promises a gain that will not arrive:
-- transparent data encryption blocks it on data files whatever the version,
-- and the log is zero-filled except on SQL Server 2022 and later, where
-- autogrowth events up to 64 MB can use it, TDE or not.
--
-- THE ENGINE ROW IS PROJECTED ONTO THE ROOT, as engine.instant_file_
-- initialization, and the services array still carries every row. The setting
-- belongs to the Database Engine service, and reaching it in the array means
-- selecting a row whose name carries the instance name — which differs on
-- every host, so nothing downstream can address it by a fixed path. The
-- row is picked by process id and not by name: the service is called
-- "SQL Server (MSSQLSERVER)" on Windows and "MSSQLSERVER" on Linux, and a
-- pattern written for one returns NULL on the other. Measured: a first version
-- matching 'SQL Server (%' reported NULL on SQL Server 2025 on Linux while the
-- services array beside it showed Y. SERVERPROPERTY('ProcessID') is the
-- process answering this very query, so it names the engine on both.
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
SET LOCK_TIMEOUT 10000;

-- SERVERPROPERTY returns sql_variant, always. Projected raw, the encoder has
-- no type to render it by: it falls back to fmt.Sprint and warns. The value
-- survives, which is the trap — is_clustered reached a real archive as the
-- string "0" rather than a boolean, true enough to read past and wrong enough
-- to break anything that tests it. Converting routes each value to the encoder
-- branch that knows how to write it.
--
-- InstanceName carried the same defect without ever warning, because it is
-- NULL on a default instance and a NULL sql_variant has no base type to
-- complain about. It would have surfaced on the first named instance instead.
SELECT CONVERT(sysname,  SERVERPROPERTY('MachineName'))           AS [machine_name],
       CONVERT(sysname,  SERVERPROPERTY('ComputerNamePhysicalNetBIOS'))
                                                                  AS [physical_name],
       CONVERT(sysname,  SERVERPROPERTY('InstanceName'))          AS [instance_name],
       CONVERT(bit,      SERVERPROPERTY('IsClustered'))           AS [is_clustered],
       si.sqlserver_start_time                                    AS [instance_start],
       DATEDIFF(second, si.sqlserver_start_time, GETDATE())       AS [seconds_since_instance_start],
       (SELECT COUNT(*) FROM sys.dm_server_services)              AS [services_reported],
       (SELECT MAX(s.instant_file_initialization_enabled)
          FROM sys.dm_server_services AS s
         WHERE s.process_id = CONVERT(int, SERVERPROPERTY('ProcessID')))
                                                                  AS [engine.instant_file_initialization],
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
