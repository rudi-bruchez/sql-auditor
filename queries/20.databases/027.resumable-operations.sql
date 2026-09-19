-- @scope:       database
-- @resultsets:  root:object, operations:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     30
-- @min_version: 14
--
-- Runs once per user database, with the connection context switched to it.
--
-- The resumable index operations this database holds, running or paused:
-- ALTER INDEX ... REBUILD, CREATE INDEX and ALTER TABLE ... ADD CONSTRAINT
-- issued WITH (RESUMABLE = ON). sys.index_resumable_operations arrived with
-- SQL Server 2017, which is the floor.
--
-- WHY A PAUSED OPERATION IS A FINDING. It is work somebody started and nobody
-- finished, and it does not sit quietly. Measured on SQL Server 2025:
--
--   - while a rebuild of one index of a table is paused, a resumable CREATE
--     INDEX on the same table fails with Msg 10637, "one or more indexes are
--     currently in resumable index rebuild state", so the next maintenance
--     job on that table fails for a reason its own log does not explain;
--   - a paused CREATE INDEX has an index_id here and NO ROW in sys.indexes.
--     Every other schema collector reads sys.indexes, so this file is the only
--     place in the archive where the half-built index appears at all.
--
-- The partial structure also keeps the data pages it has written, page_count
-- here, until the operation is resumed or aborted. Since SQL Server 2019 the
-- database scoped configuration PAUSED_RESUMABLE_INDEX_ABORT_DURATION_MINUTES
-- aborts a paused operation after a delay, 1440 minutes by default and 0 for
-- never; 20.databases/020.properties already carries the whole of
-- sys.database_scoped_configurations, so the setting is not repeated here.
--
-- index_exists SAYS WHICH KIND OF OPERATION IT IS without reading sql_text.
-- A rebuild works on an index that exists; a creation, of an index or of a
-- constraint, works on one that does not yet. sql_text is not projected: it
-- is the statement as typed, and what it would add beyond the columns below
-- is the options, which are not what an audit acts on.
--
-- The table name comes from OBJECT_SCHEMA_NAME and OBJECT_NAME and the index
-- name from the view itself, not from a join to sys.indexes, for the reason
-- above: the join would drop every paused creation.
--
-- VIEW ANY DEFINITION IS NOT A FORMALITY. Measured: a login holding CONNECT
-- alone runs this file without an error and gets counts.operations = 0 on a
-- database with two paused operations. Metadata visibility filters the view
-- silently, so a missing grant reads exactly like a clean database.
--
-- AN EMPTY ARRAY IS THE ANSWER AND NOT A FAILURE, as in
-- 20.databases/026.persisted-sku-features: nearly every database holds no
-- resumable operation at all, and counts.operations in the root is what tells
-- "none" from "not collected".

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                     AS [collected_at],
       COUNT(*)                                                     AS [counts.operations],
       COUNT(CASE WHEN r.state = 1 THEN 1 END)                      AS [counts.paused],
       COUNT(CASE WHEN r.state = 0 THEN 1 END)                      AS [counts.running],
       ISNULL(SUM(CASE WHEN r.state = 1 THEN r.page_count END), 0)  AS [counts.paused_pages]
FROM sys.index_resumable_operations AS r
OPTION (RECOMPILE, MAXDOP 1);

SELECT OBJECT_SCHEMA_NAME(r.object_id) + '.' + OBJECT_NAME(r.object_id)  AS [table],
       r.name                                                       AS [index_name],
       r.index_id                                                   AS [index_id],
       CASE WHEN EXISTS (SELECT 1 FROM sys.indexes AS i
                         WHERE i.object_id = r.object_id
                           AND i.index_id = r.index_id)
            THEN 1 ELSE 0 END                                       AS [index_exists],
       r.partition_number                                           AS [partition_number],
       r.state_desc                                                 AS [state],
       CAST(r.percent_complete AS decimal(5,2))                     AS [percent_complete],
       r.page_count                                                 AS [page_count],
       r.last_max_dop_used                                          AS [last_max_dop_used],
       r.total_execution_time                                       AS [total_execution_minutes],
       CONVERT(varchar(23), r.start_time, 126)                      AS [start_time],
       CONVERT(varchar(23), r.last_pause_time, 126)                 AS [last_pause_time],
       CASE WHEN r.state = 1
            THEN DATEDIFF(minute, r.last_pause_time, SYSDATETIME()) END AS [paused_minutes]
FROM sys.index_resumable_operations AS r
ORDER BY r.page_count DESC, r.object_id, r.index_id
OPTION (RECOMPILE, MAXDOP 1);
