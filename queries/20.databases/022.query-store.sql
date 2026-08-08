-- @scope:       database
-- @resultsets:  root:object, scoped_configurations:array
-- @permissions: CONNECT, VIEW SERVER STATE, VIEW ANY DEFINITION
-- @timeout:     60
-- @min_version: 13
--
-- Runs once per user database, with the connection context switched to it.
--
-- The two SQL Server 2016 database-scope settings catalogues that
-- 20.databases/020.properties.sql had to drop at the 2012 floor: the Query
-- Store's configuration and actual state, and the database scoped
-- configurations. Both are documented as "SQL Server 2016 (13.x) and later
-- versions" with no service-pack qualifier, so both are satisfied by the bare
-- major 13 and can share one file.
--
-- Columns deliberately not projected, because they would abort the batch on a
-- 13.x instance:
--   sys.database_query_store_options.wait_stats_capture_mode(_desc)  (2017)
--   sys.database_query_store_options.capture_policy_*                (2019)
--   sys.database_scoped_configurations.is_value_default              (2017)
--
-- desired_state is what was asked for; actual_state is what the engine is
-- doing, and the two diverge when the Query Store hits its quota or errors —
-- readonly_reason is the bit map that says why. All three are projected raw.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

/* A LEFT JOIN from sys.databases, not a bare SELECT from
   sys.database_query_store_options, so root is a single row even on a
   database where that view returns nothing. */
SELECT
    d.name                                                          AS [database.name],
    CAST(d.is_query_store_on AS BIT)                                AS [database.query_store_on],
    qso.desired_state_desc                                          AS [query_store.desired_state],
    qso.actual_state_desc                                           AS [query_store.actual_state],
    qso.readonly_reason                                             AS [query_store.readonly_reason],
    CAST(qso.current_storage_size_mb AS BIGINT)                     AS [query_store.current_storage_mb],
    CAST(qso.max_storage_size_mb     AS BIGINT)                     AS [query_store.max_storage_mb],
    CAST(qso.flush_interval_seconds  AS BIGINT)                     AS [query_store.flush_interval_sec],
    CAST(qso.interval_length_minutes AS BIGINT)                     AS [query_store.interval_length_min],
    CAST(qso.stale_query_threshold_days AS BIGINT)                  AS [query_store.stale_query_threshold_days],
    CAST(qso.max_plans_per_query     AS BIGINT)                     AS [query_store.max_plans_per_query],
    qso.query_capture_mode_desc                                     AS [query_store.query_capture_mode],
    qso.size_based_cleanup_mode_desc                                AS [query_store.size_based_cleanup_mode]
FROM sys.databases AS d
LEFT JOIN sys.database_query_store_options AS qso ON 1 = 1
WHERE d.database_id = DB_ID()
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── scoped_configurations ─────────
   value is sql_variant; it is converted to text so the client-side encoder
   sees one type per column rather than one per row. */
SELECT
    dsc.configuration_id,
    dsc.name,
    CONVERT(nvarchar(256), dsc.value)               AS value,
    CONVERT(nvarchar(256), dsc.value_for_secondary) AS value_for_secondary
FROM sys.database_scoped_configurations AS dsc
ORDER BY dsc.configuration_id
OPTION (RECOMPILE, MAXDOP 1);
