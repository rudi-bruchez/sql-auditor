-- @scope:       database
-- @resultsets:  root:object, procedures:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     120
--
-- Execution statistics for every stored procedure of this database still in the
-- plan cache.
--
-- Why this collector exists. The corpus reads the plan cache in aggregate
-- (80.workload/040) and the Query Store per query (80.workload/020 and 021),
-- and neither works at the grain a developer acts on, which is A PROCEDURE. A
-- query-level ranking hands back forty statements from the same procedure and
-- says nothing about the procedure; this says which one to open.
--
-- It replaces seven queries of the reference diagnostic script at once — by
-- executions, by elapsed time, by CPU, by logical and physical reads, by logical
-- writes — which differ from each other only in their ORDER BY. So there is no
-- TOP and no ordering here: choosing one would be choosing which metric matters,
-- and that decision belongs to whoever knows what the application does.
--
-- TOTALS AND PER-EXECUTION SIDE BY SIDE, always. A total confounds "slow" with
-- "called constantly", and those need opposite fixes: one is a rewrite, the
-- other is a caller doing too many round trips. Reporting only totals would let
-- a reader act on the wrong one, which is worse than reporting nothing.
--
-- cached_time IS NOT DECORATION. Every counter below accumulates only since the
-- plan entered the cache, so a procedure recompiled two minutes ago and one
-- cached for three weeks are not comparable — and nothing else in the archive
-- says which is which. A memory-pressure eviction resets it silently.
--
-- WHAT THIS IS NOT. The plan cache is not history: a procedure absent here ran
-- and was evicted, or has not run since the last restart. Absence proves
-- nothing, which is why the Query Store collectors exist beside this one.
--
-- NO STATEMENT TEXT IS COLLECTED, and that is what keeps this file ungated. The
-- DMV carries counters; joining sys.dm_exec_sql_text would turn a metadata
-- collector into a disclosure. The object name is enough to find the body, and
-- 70.schema/080.modules.sql exports that under its own flag.
--
-- NO JUDGEMENT IS APPLIED. Nothing here is called slow. A procedure averaging
-- four seconds may be a nightly batch doing exactly what it should.
--
-- SQL Server 2012 is the floor. sys.dm_exec_procedure_stats is 2008. Not
-- collected for that reason:
--   total_spills / last_spills / min_spills / max_spills   (2016 SP1)
--   total_page_server_reads and its siblings               (2022, Hyperscale)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT DB_NAME()                                                  AS [database],
       CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       (SELECT COUNT(*) FROM sys.dm_exec_procedure_stats AS ps
         WHERE ps.database_id = DB_ID())                          AS [cached_procedures],
       /* Every procedure that exists, so a reader can see how much of the
          database the cache is describing. A cache holding 12 of 400 is a
          different picture from one holding 380 of 400. */
       (SELECT COUNT(*) FROM sys.objects AS o
         WHERE o.type = 'P' AND o.is_ms_shipped = 0)              AS [procedures_in_database],
       /* The instance start, because every counter below is bounded by it as
          well as by cached_time. */
       CONVERT(varchar(23), (SELECT sqlserver_start_time FROM sys.dm_os_sys_info), 126)
                                                                  AS [instance_start]
OPTION (RECOMPILE, MAXDOP 1);

/* No TOP and no ORDER BY: see the header. One row per cached procedure. */
SELECT OBJECT_SCHEMA_NAME(ps.object_id, ps.database_id)
         + '.' + OBJECT_NAME(ps.object_id, ps.database_id)        AS [procedure],
       ps.type_desc                                               AS [type],
       CONVERT(varchar(23), ps.cached_time, 126)                  AS [cached_time],
       CONVERT(varchar(23), ps.last_execution_time, 126)          AS [last_execution],
       ps.execution_count                                         AS [executions],
       /* Elapsed */
       ps.total_elapsed_time                                      AS [elapsed.total_us],
       ps.total_elapsed_time / ps.execution_count                 AS [elapsed.avg_us],
       ps.min_elapsed_time                                        AS [elapsed.min_us],
       ps.max_elapsed_time                                        AS [elapsed.max_us],
       ps.last_elapsed_time                                       AS [elapsed.last_us],
       /* CPU */
       ps.total_worker_time                                       AS [cpu.total_us],
       ps.total_worker_time / ps.execution_count                  AS [cpu.avg_us],
       ps.max_worker_time                                         AS [cpu.max_us],
       /* Logical reads: memory pressure, and the number that usually moves when
          an index is added. */
       ps.total_logical_reads                                     AS [logical_reads.total],
       ps.total_logical_reads / ps.execution_count                AS [logical_reads.avg],
       ps.max_logical_reads                                       AS [logical_reads.max],
       /* Physical reads: what was not in the buffer pool, so disk. */
       ps.total_physical_reads                                    AS [physical_reads.total],
       ps.total_physical_reads / ps.execution_count               AS [physical_reads.avg],
       /* Writes, which say a procedure marked read-only by convention is not. */
       ps.total_logical_writes                                    AS [logical_writes.total],
       ps.total_logical_writes / ps.execution_count               AS [logical_writes.avg]
FROM sys.dm_exec_procedure_stats AS ps
/* The DMV covers the whole instance; this keeps the database it was pointed at.
   execution_count is never 0 for a row that exists, so the divisions above are
   safe — but the filter is what makes the object names resolve, since
   OBJECT_NAME with a foreign database_id returns NULL. */
WHERE ps.database_id = DB_ID()
OPTION (RECOMPILE, MAXDOP 1);
