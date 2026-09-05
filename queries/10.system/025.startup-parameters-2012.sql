-- @scope:       instance
-- @resultsets:  startup_parameters:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
-- @max_version: 13.0.4001
--
-- The parameters the engine was started with, for the 2012 to 2016 RTM
-- window.
--
-- Why this file exists: 10.system/020.host-services.sql holds the same
-- projection and normally answers this question, but its version floor of
-- 13.0.4001 comes from ONE column of the services view —
-- instant_file_initialization_enabled, which arrived in SQL Server 2016 SP1.
-- The startup-parameters projection itself reads sys.dm_server_registry,
-- which exists since before 2012, so on 2012 to 2016 RTM the floor of 020
-- would drop the persisted startup parameters — the trace flags that
-- survive a restart — for a reason that has nothing to do with them. This
-- collector covers that window and mutes itself from 2016 SP1 up, where 020
-- runs instead.
--
-- The corpus knows floors but no ceilings, so this is also the first
-- collector with a ceiling. It is needed because 020 and this file project
-- the same shape under the same name: without a ceiling the two would
-- double-collect the same rows on every 2016 SP1+ instance.
--
-- The window this file serves is Windows by construction: SQL Server on
-- Linux starts with 2017, so sys.dm_server_registry is reliable on every
-- version the ceiling lets this script reach.
--
-- The -T entries here are the trace flags that survive a restart. Anything in
-- DBCC TRACESTATUS but absent from this list disappears at the next one.
--
-- The projection below mirrors the startup_parameters result set of
-- 10.system/020.host-services.sql exactly — same aliases, same WHERE, same
-- sql_variant conversion — so the analysis reads one shape whatever the
-- version.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

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
