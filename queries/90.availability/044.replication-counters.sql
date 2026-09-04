-- @scope:       instance
-- @resultsets:  root:object, counters:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- The replication performance counters, live, without touching a history
-- table in a distribution database.
--
-- THIS IS A SEPARATE FILE FROM 040 ON PURPOSE. sys.dm_os_performance_counters
-- needs VIEW SERVER STATE; 040 declares only CONNECT and VIEW ANY DEFINITION
-- and succeeds on a login holding exactly that. Since @permissions drives the
-- skip gate, folding these counters into 040 would make a login without
-- VIEW SERVER STATE lose the four replication flags it collects today. A
-- thinner archive is better than a file that runs and fails.
--
-- COUNTERS ARE CUMULATIVE OR INSTANTANEOUS DEPENDING ON cntr_type, AND THIS
-- FILE DOES NOT INTERPRET THEM. cntr_value is projected with cntr_type beside
-- it so the analysis can decide; a rate computed from one sample is not a
-- rate.
--
-- The names carry trailing spaces in this DMV, which is why they are trimmed
-- on the way out. Not in the WHERE clause: a trailing % already absorbs the
-- padding, and trimming there would run the function over every row of the
-- DMV to reach the same set.
--
-- SQL Server 2012 is the floor.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @c TABLE (
    [object_name] nvarchar(128), [counter_name] nvarchar(128),
    [instance_name] nvarchar(128) NULL, [cntr_value] bigint, [cntr_type] int);

DECLARE @collected bit = 1, @err int = 0, @msg nvarchar(2048) = N'';

BEGIN TRY
    INSERT INTO @c
    EXEC sys.sp_executesql N'
        SELECT RTRIM(p.object_name), RTRIM(p.counter_name), RTRIM(p.instance_name),
               p.cntr_value, p.cntr_type
        FROM sys.dm_os_performance_counters AS p
        WHERE p.object_name LIKE ''%Replication%''
        OPTION (RECOMPILE, MAXDOP 1)';
END TRY
BEGIN CATCH
    SELECT @collected = 0, @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)     AS [collected_at],
       CONVERT(int, @collected)                     AS [collected],
       @err                                         AS [error_number],
       NULLIF(@msg, N'')                            AS [error_message],
       (SELECT COUNT(*) FROM @c)                    AS [counts.counters]
OPTION (RECOMPILE, MAXDOP 1);

SELECT c.[object_name], c.[counter_name], c.[instance_name],
       c.[cntr_value], c.[cntr_type]
FROM @c AS c ORDER BY c.[object_name], c.[counter_name], c.[instance_name]
OPTION (RECOMPILE, MAXDOP 1);
