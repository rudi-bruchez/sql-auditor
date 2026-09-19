-- @scope:       instance
-- @resultsets:  root:object, counters:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- The raw content of sys.dm_os_performance_counters: what Performance
-- Monitor would show, one snapshot. Six collectors read a few counters each
-- for a named purpose (010, 015, 050, 075, 011, 044); everything else in the
-- view reached no archive: Access Methods, Locks per lock type, Latches, SQL
-- Errors, Plan Cache, Query Store, Columnstore, resource pools and the
-- per-database Databases counters. docs/performance-counters-spec.md is the
-- design and records what it was measured against.
--
-- DEPRECATED FEATURES ARE LEFT OUT: 075 owns them and keeps those above zero.
-- Everything else is kept, zeros included.
--
-- ONE READ. The view is copied once into a table variable and both result
-- sets come from it: a CREATE DATABASE, or any access that reopens an
-- AUTO_CLOSE database, adds about 87 rows, and two reads would give a root
-- that disagrees with its own array.
--
-- THE TYPE IS WHAT THE ENGINE DECLARES, NOT HOW TO READ THE VALUE. type_name
-- is the Windows name of cntr_type. The engine does not keep to it: rows
-- declared PERF_COUNTER_LARGE_RAWCOUNT include counts since start (Batch
-- Resp Statistics, Log Growths) and rates it computed itself (Requests
-- completed/sec). Fractions and averages cannot be paired with their base
-- by name or by order. So NO RATE, RATIO OR AVERAGE IS COMPUTED here.
--
-- THE WINDOW. uptime_s is from ticks, which no clock change skews, and it is
-- the window of the instance-wide counters that accumulate. A per-database
-- row starts with its database and resets on OFFLINE/ONLINE, restore,
-- attach, or an AUTO_CLOSE reopening; nothing here dates that.
--
-- object keeps its prefix (SQLServer:, MSSQL$<name>:, SQLPAL:, or a product
-- name with a year and no colon for XTP): match with LIKE, as 010 does.
-- (object, counter, instance) is not unique: SQLPAL:Host Memory repeats.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @counters TABLE (
    [object]    nvarchar(128) NOT NULL,
    [counter]   nvarchar(128) NOT NULL,
    [instance]  nvarchar(128) NULL,
    [value]     bigint        NOT NULL,
    [type]      int           NOT NULL);

INSERT INTO @counters
SELECT RTRIM(pc.object_name), RTRIM(pc.counter_name),
       NULLIF(RTRIM(pc.instance_name), N''), pc.cntr_value, pc.cntr_type
FROM sys.dm_os_performance_counters AS pc
WHERE pc.object_name NOT LIKE N'%Deprecated Features%'
OPTION (RECOMPILE, MAXDOP 1);

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       CONVERT(varchar(23), i.sqlserver_start_time, 126)          AS [instance_start],
       (i.ms_ticks - i.sqlserver_start_time_ms_ticks) / 1000      AS [uptime_s],
       (SELECT COUNT(*) FROM @counters)                           AS [counts.rows],
       (SELECT COUNT(DISTINCT c.[object]) FROM @counters AS c)    AS [counts.objects]
FROM sys.dm_os_sys_info AS i
OPTION (RECOMPILE, MAXDOP 1);

SELECT c.[object]                                                 AS [object],
       c.[counter]                                                AS [counter],
       c.[instance]                                               AS [instance],
       c.[value]                                                  AS [value],
       c.[type]                                                   AS [type],
       CASE c.[type]
           WHEN 65792      THEN 'PERF_COUNTER_LARGE_RAWCOUNT'
           WHEN 272696576  THEN 'PERF_COUNTER_BULK_COUNT'
           WHEN 272696320  THEN 'PERF_COUNTER_COUNTER'
           WHEN 537003264  THEN 'PERF_LARGE_RAW_FRACTION'
           WHEN 1073874176 THEN 'PERF_AVERAGE_BULK'
           WHEN 1073939712 THEN 'PERF_LARGE_RAW_BASE'
       END                                                        AS [type_name]
FROM @counters AS c
ORDER BY c.[object], c.[counter], c.[instance]
OPTION (RECOMPILE, MAXDOP 1);
