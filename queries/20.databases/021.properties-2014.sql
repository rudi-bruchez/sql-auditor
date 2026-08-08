-- @scope:       database
-- @resultsets:  root:object
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     60
-- @min_version: 12
--
-- Runs once per user database, with the connection context switched to it.
--
-- The two sys.databases options that arrived in SQL Server 2014, restored for
-- the current database from 20.databases/020.properties.sql, which holds the
-- 2012 floor and cannot name these columns. The instance-wide list of the
-- same two options is 20.databases/011.all-databases-2014.sql; both are kept
-- because the per-database document is meant to stand on its own.
--
-- sys.databases.delayed_durability / delayed_durability_desc and
-- is_auto_create_stats_incremental_on are all documented as "SQL Server 2014
-- (12.x) and later versions", with no service-pack qualifier, so the gate is
-- the bare major 12.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT
    d.name                                                          AS [database.name],
    d.database_id                                                   AS [database.id],
    d.delayed_durability                                            AS [database.delayed_durability],
    d.delayed_durability_desc                                       AS [database.delayed_durability_desc],
    CAST(d.is_auto_create_stats_incremental_on AS BIT)              AS [database.auto_create_stats_incremental]
FROM sys.databases AS d
WHERE d.database_id = DB_ID()
OPTION (RECOMPILE, MAXDOP 1);
