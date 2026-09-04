-- @scope:       database
-- @resultsets:  root:object, functions:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @min_version: 13.0
-- @timeout:     120
--
-- Execution statistics for every scalar user-defined function of this database
-- still in the plan cache.
--
-- Why this is a separate file from 050.procedure-stats.sql, which it otherwise
-- mirrors exactly. sys.dm_exec_function_stats arrived in SQL Server 2016;
-- sys.dm_exec_procedure_stats has been there since 2008. Putting both in one
-- file would gate the procedure statistics behind a 2016 floor and lose them on
-- every older instance — the same reason 011.all-databases-2014.sql is not part
-- of 010.
--
-- WHY IT IS WORTH A FILE OF ITS OWN. A scalar function called once per row is
-- the classic cause of a query that is slow for no visible reason: before SQL
-- Server 2019 it does not appear in the execution plan as a separate operator,
-- it inhibits parallelism for the whole statement, and its cost is folded
-- invisibly into the calling operator. execution_count is what exposes it — a
-- function showing forty million executions against a workload of a few thousand
-- statements is being called per row, and nothing in a plan would have said so.
--
-- ONLY SCALAR AND ASSEMBLY FUNCTIONS APPEAR HERE, by construction of the DMV.
-- Inline and multi-statement table-valued functions are not tracked, and their
-- absence is not evidence they are not used. The root projects the counts of
-- each kind so the gap is visible rather than assumed.
--
-- NO STATEMENT TEXT, no @requires_flag, same as 050.
--
-- NO JUDGEMENT IS APPLIED. A scalar function is not a defect; it is a design
-- whose cost depends entirely on how it is called.
--
-- SQL Server 2016 is the floor and it is the DMV's own. Not collected for that
-- reason:
--   sys.sql_modules.is_inlineable / inline_type   (2019, scalar UDF inlining)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT DB_NAME()                                                  AS [database],
       CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       (SELECT COUNT(*) FROM sys.dm_exec_function_stats AS fs
         WHERE fs.database_id = DB_ID())                          AS [cached_functions],
       /* Split by kind, because the DMV only ever reports the first of these
          three and a reader has to know what it cannot see. */
       (SELECT COUNT(*) FROM sys.objects AS o
         WHERE o.is_ms_shipped = 0 AND o.type = 'FN')             AS [counts.scalar],
       (SELECT COUNT(*) FROM sys.objects AS o
         WHERE o.is_ms_shipped = 0 AND o.type = 'IF')             AS [counts.inline_table_valued],
       (SELECT COUNT(*) FROM sys.objects AS o
         WHERE o.is_ms_shipped = 0 AND o.type = 'TF')             AS [counts.multistatement_table_valued],
       CONVERT(varchar(23), (SELECT sqlserver_start_time FROM sys.dm_os_sys_info), 126)
                                                                  AS [instance_start]
OPTION (RECOMPILE, MAXDOP 1);

SELECT OBJECT_SCHEMA_NAME(fs.object_id, fs.database_id)
         + '.' + OBJECT_NAME(fs.object_id, fs.database_id)        AS [function],
       fs.type_desc                                               AS [type],
       CONVERT(varchar(23), fs.cached_time, 126)                  AS [cached_time],
       CONVERT(varchar(23), fs.last_execution_time, 126)          AS [last_execution],
       /* The number this file exists for. */
       fs.execution_count                                         AS [executions],
       fs.total_elapsed_time                                      AS [elapsed.total_us],
       fs.total_elapsed_time / fs.execution_count                 AS [elapsed.avg_us],
       fs.min_elapsed_time                                        AS [elapsed.min_us],
       fs.max_elapsed_time                                        AS [elapsed.max_us],
       fs.total_worker_time                                       AS [cpu.total_us],
       fs.total_worker_time / fs.execution_count                  AS [cpu.avg_us],
       fs.total_logical_reads                                     AS [logical_reads.total],
       fs.total_logical_reads / fs.execution_count                AS [logical_reads.avg],
       fs.total_physical_reads                                    AS [physical_reads.total],
       fs.total_logical_writes                                    AS [logical_writes.total]
FROM sys.dm_exec_function_stats AS fs
WHERE fs.database_id = DB_ID()
OPTION (RECOMPILE, MAXDOP 1);
