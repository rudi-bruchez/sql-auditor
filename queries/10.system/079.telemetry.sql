-- @scope:       instance
-- @resultsets:  root:object, registry:array, services:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- Whether this instance sends usage data and crash dumps to Microsoft, as far
-- as the engine itself can say.
--
-- WHY THIS COLLECTOR EXISTS. Two separate streams leave a default SQL Server
-- installation. Usage and diagnostic data, historically CEIP, reports how
-- features are used. Error reporting sends crash dumps. Microsoft's own page
-- says the second "collects crash dumps that are sent to Microsoft and that may
-- contain sensitive information", and a dump is a copy of engine memory, which
-- is where query text and data pages live. That is the sentence that makes this
-- an audit question rather than a preference: the usage stream is documented as
-- carrying no values from user tables, and the dump stream carries whatever was
-- in memory when the instance fell over.
--
-- The two are configured independently and an instance can be opted out of one
-- and not the other, which is the state nobody discovers by accident.
--
-- WHAT THE ENGINE CAN ACTUALLY SEE, MEASURED. The opt-out on Windows lives in
-- the registry, in three places: the instance's own key under {InstanceID}\CPE,
-- a Reporting Services key under SSRS\CPE, and a shared-features key named for
-- the major version. sys.dm_server_registry is the only T-SQL window onto that
-- hive, and this collector asks it.
--
-- On Linux it answers nothing at all. Measured on 16.0.4265.3 and 17.0.4065.4,
-- both on Linux: sys.dm_server_registry returns ZERO rows, not zero matching
-- rows. There is no registry to read, and the Linux opt-out lives in
-- mssql.conf, which no T-SQL reaches. sys.dm_server_services on the same two
-- instances names no telemetry service, and does not even agree with itself
-- about the rest: 17.0.4065.4 lists the engine and the Agent, 16.0.4265.3
-- lists the Agent alone. That is why the array below is projected whole and
-- why counts.services_reported is in the root. An absent name in this view is
-- not an absent service.
--
-- WHETHER THE CPE SUBKEY IS EXPOSED ON WINDOWS IS NOT VERIFIED. The subkey is a
-- sibling of MSSQLServer under the instance's own key, which makes it plausible
-- that the DMV carries it, and plausible is not measured. This file is written
-- so that the difference is legible in the archive rather than guessed at
-- later: counts.registry_rows_total says whether the DMV answered at all, and
-- array below carries whatever it matched. An archive that shows a populated
-- DMV and no CPE row has established something; one that shows an empty DMV has
-- established nothing about telemetry.
--
-- NOTHING HERE IS REPORTED AS "ENABLED" OR "DISABLED" WHEN IT IS UNKNOWN, and
-- that is the whole design. The documented values are 0 for opt out and 1 for
-- opt in, and an absent key is neither: the default is opt in, so "no key" and
-- "key set to 0" are opposite states that a naive collector would both render
-- as a missing row. The root therefore carries three counts, found, opted out
-- and opted in, and never a single boolean.
--
-- WHAT THE ARCHIVE ALREADY HOLDS THAT BEARS ON THIS. 10.system/046.local-
-- sessions.sql groups local connections by program_name, so a telemetry client
-- connected at the moment of collection appears there under its own name. That
-- is worth reading beside this file and is not a substitute for it: the service
-- connects periodically, so its absence from a single snapshot means nothing.
-- 10.system/071.memory-dumps.sql lists the dumps this instance has written,
-- which is the other half of the error-reporting question: a dump that exists
-- and an error-reporting opt-in together mean a copy of it left the building.
--
-- THE EDITION DECIDES WHETHER OPTING OUT IS EVEN OFFERED. Microsoft's page is
-- explicit: "You can disable the sending of information to Microsoft only in
-- paid versions of SQL Server." So on Express and on Developer the finding is
-- not "telemetry is on", it is "telemetry cannot be turned off", which is a
-- different conversation and a different recommendation.
-- 10.system/010.properties.sql carries the edition.
--
-- NO JUDGEMENT IS APPLIED, and here the restraint matters more than usual.
-- Disabling the CEIP service is explicitly unsupported by Microsoft, and
-- removing its resources from a cluster group is unsupported too. An audit that
-- recommends it is recommending an unsupported configuration that a cumulative
-- update will silently undo. What is supported is the opt-out, and this file
-- collects the opt-out.
--
-- SQL Server 2012 is the floor. sys.dm_server_registry and
-- sys.dm_server_services both predate it. Not collected for that reason:
--   mssql.conf                    (a file on the host, outside T-SQL)
--   the CEIP service's own queries (they run under their own login and land in
--     the plan cache like any others; 80.workload/040.plan-cache.sql already
--     carries what the cache holds, and singling them out by text would mean
--     matching on a name Microsoft is free to change)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @err_telemetry int = 0, @msg nvarchar(2048) = N'';

DECLARE @registry TABLE (
    [registry_key]  nvarchar(512),
    [value_name]    nvarchar(256),
    [value_data]    nvarchar(512));

DECLARE @rows_total int = 0;

BEGIN TRY
    SELECT @rows_total = COUNT(*) FROM sys.dm_server_registry;

    /* The two documented value names, wherever they appear, plus any key whose
       path names the opt-out subkey. Matching on the value name rather than on
       the key path is what makes this work across the three documented
       locations without hardcoding an instance id or a major version. */
    INSERT INTO @registry
    SELECT r.registry_key, r.value_name,
           CONVERT(nvarchar(512), r.value_data)
    FROM sys.dm_server_registry AS r
    WHERE r.value_name IN (N'CustomerFeedback', N'EnableErrorReporting')
       OR r.registry_key LIKE N'%\CPE'
       OR r.registry_key LIKE N'%\CPE\%'
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_telemetry = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT
    CONVERT(sysname, SERVERPROPERTY('ServerName'))              AS [instance],
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    /* Did the registry view answer at all. Zero here means the platform has no
       registry to read, and every count below is therefore uninformative rather
       than reassuring.

       These four live under counts and not under registry and services, which
       would have read better. Those two are the names of the result sets
       below, and a root column that claims an array's top-level key makes the
       encoder refuse the whole document: the collector runs, reports success
       and writes nothing. Second time in one day that this file's author made
       the mistake, and both times the corpus lint was what caught it. */
    @rows_total                                                 AS [counts.registry_rows_total],
    (SELECT COUNT(*) FROM @registry)                            AS [counts.registry_matches],
    /* The three counts that replace a boolean. An absent key is opt in by
       default, so "not found" is not "disabled", and rendering either as a
       single flag would lose the only distinction that matters. */
    (SELECT COUNT(*) FROM @registry
      WHERE [value_name] = N'CustomerFeedback' AND [value_data] = N'0')
                                                                AS [usage_data.opted_out_keys],
    (SELECT COUNT(*) FROM @registry
      WHERE [value_name] = N'CustomerFeedback' AND [value_data] <> N'0')
                                                                AS [usage_data.opted_in_keys],
    (SELECT COUNT(*) FROM @registry
      WHERE [value_name] = N'EnableErrorReporting' AND [value_data] = N'0')
                                                                AS [error_reporting.opted_out_keys],
    (SELECT COUNT(*) FROM @registry
      WHERE [value_name] = N'EnableErrorReporting' AND [value_data] <> N'0')
                                                                AS [error_reporting.opted_in_keys],
    -- Services whose name suggests a telemetry client. Named rather than
    -- counted silently, because this view reports a short and unreliable list:
    -- a hit here would be news, and a miss establishes nothing.
    (SELECT COUNT(*) FROM sys.dm_server_services
      WHERE servicename LIKE N'%TELEMETRY%' OR servicename LIKE N'%CEIP%')
                                                                AS [counts.services_telemetry_like],
    (SELECT COUNT(*) FROM sys.dm_server_services)               AS [counts.services_reported],
    -- The edition, because it decides whether opting out is offered at all.
    -- Repeated from 010.properties.sql on purpose: a reader of this file needs
    -- it in the same breath as the counts above, and it is one string.
    --
    -- Named [edition] and not [instance.edition], which is how 010.properties
    -- names it, because [instance] above is a scalar here and an object there.
    -- The encoder builds an object out of every dotted prefix, so the two
    -- spellings in one result set make [instance] both a string and a
    -- container and the whole document is refused. 010.properties is the one
    -- file in the corpus with no scalar [instance] column, which is what lets
    -- it own the prefix.
    CONVERT(nvarchar(128), SERVERPROPERTY('Edition'))           AS [edition],
    CASE WHEN @err_telemetry = 0 THEN 1 ELSE 0 END              AS [collected.telemetry],
    @err_telemetry                                              AS [errors.telemetry],
    NULLIF(@msg, N'')                                           AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

/* Every matching registry value, with its key. The key path is what says which
   of the three documented scopes a value belongs to: an instance key, the
   Reporting Services key, or the shared-features key named for the major
   version. They are not interchangeable, and an instance opted out while the
   shared features are opted in is a real and common state. */
SELECT
    r.[registry_key]                                            AS [registry_key],
    r.[value_name]                                              AS [value_name],
    r.[value_data]                                              AS [value_data],
    -- 0 is opt out and 1 is opt in, per the documented entry type. Rendered
    -- here rather than left to the reader because the polarity is easy to get
    -- backwards and a report that inverts it is worse than one that omits it.
    CASE WHEN r.[value_data] = N'0' THEN 'opted_out'
         WHEN r.[value_data] IS NULL THEN 'unknown'
         ELSE 'opted_in' END                                    AS [state]
FROM @registry AS r
ORDER BY r.[registry_key], r.[value_name]
OPTION (RECOMPILE, MAXDOP 1);

/* The services the engine will admit to. Projected whole rather than filtered,
   so that a reader can see that the telemetry service is absent from it because
   the view does not report it, and not because it is not installed. Measured on
   two Linux instances, the view does not even list the engine on both of them,
   which is the strongest argument there is against reading anything into what
   it leaves out. */
SELECT
    s.servicename                                               AS [service],
    s.startup_type_desc                                         AS [startup_type],
    s.status_desc                                               AS [status],
    CAST(CASE WHEN s.servicename LIKE N'%TELEMETRY%'
                OR s.servicename LIKE N'%CEIP%'
              THEN 1 ELSE 0 END AS bit)                         AS [looks_like_telemetry]
FROM sys.dm_server_services AS s
ORDER BY s.servicename
OPTION (RECOMPILE, MAXDOP 1);
