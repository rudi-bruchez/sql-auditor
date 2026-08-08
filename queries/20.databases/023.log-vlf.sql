-- @scope:       database
-- @resultsets:  root:object, vlf_per_file:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
-- @min_version: 13.0.5026
--
-- Runs once per user database, with the connection context switched to it.
--
-- Virtual log file detail for the transaction log — the space.vlf_count that
-- 20.databases/020.properties.sql had to drop at the SQL Server 2012 floor,
-- plus the size distribution that explains it.
--
-- sys.dm_db_log_info is documented as "SQL Server 2016 (13.x) SP2 and later
-- versions" — it was added by KB4052908 as the replacement for
-- DBCC LOGINFO — so the gate is the SP2 build 13.0.5026 and not the bare
-- major 13. On 2016 RTM the function does not exist and the batch fails.
--
-- The individual VLF rows are not projected: a badly grown log can hold tens
-- of thousands of them and the per-VLF detail says nothing the aggregate does
-- not. The counts, the size spread and the active/inactive split are what a
-- log-growth problem looks like; no threshold is applied here.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT
    COUNT(*)                                                        AS [log.vlf_count],
    SUM(CASE WHEN li.vlf_active = 1 THEN 1 ELSE 0 END)              AS [log.vlf_active_count],
    SUM(CASE WHEN li.vlf_active = 0 THEN 1 ELSE 0 END)              AS [log.vlf_inactive_count],
    CAST(MIN(li.vlf_size_mb) AS DECIMAL(14,2))                      AS [log.vlf_min_size_mb],
    CAST(AVG(li.vlf_size_mb) AS DECIMAL(14,2))                      AS [log.vlf_avg_size_mb],
    CAST(MAX(li.vlf_size_mb) AS DECIMAL(14,2))                      AS [log.vlf_max_size_mb],
    SUM(CASE WHEN li.vlf_size_mb < 1 THEN 1 ELSE 0 END)             AS [log.vlf_under_1mb_count],
    COUNT(DISTINCT li.file_id)                                      AS [log.file_count]
FROM sys.dm_db_log_info(NULL) AS li
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── vlf_per_file: one row per transaction log file ───────── */
SELECT
    li.file_id,
    df.name                                                 AS logical_name,
    COUNT(*)                                                AS vlf_count,
    SUM(CASE WHEN li.vlf_active = 1 THEN 1 ELSE 0 END)      AS vlf_active_count,
    CAST(SUM(li.vlf_size_mb) AS DECIMAL(14,2))              AS vlf_total_mb,
    CAST(MIN(li.vlf_size_mb) AS DECIMAL(14,2))              AS vlf_min_size_mb,
    CAST(MAX(li.vlf_size_mb) AS DECIMAL(14,2))              AS vlf_max_size_mb
FROM sys.dm_db_log_info(NULL) AS li
LEFT JOIN sys.database_files AS df ON df.file_id = li.file_id
GROUP BY li.file_id, df.name
ORDER BY li.file_id
OPTION (RECOMPILE, MAXDOP 1);
