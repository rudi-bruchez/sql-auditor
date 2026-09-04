-- @scope:       instance
-- @resultsets:  databases_query_store:array
-- @permissions: VIEW ANY DEFINITION
-- @timeout:     60
-- @min_version: 13
--
-- Which databases have the Query Store switched on, restored for the whole
-- instance from 20.databases/010.all-databases.sql, which holds the SQL
-- Server 2012 floor and cannot name this column.
--
-- sys.databases.is_query_store_on is documented as "SQL Server 2016 (13.x)
-- and later versions", so the gate is the bare major 13.
--
-- This is the requested state only. The state the Query Store is actually in
-- — it can fall back to READ_ONLY or ERROR — comes from
-- sys.database_query_store_options, which is per database and is collected in
-- 20.databases/022.query-store.sql.
--
-- There is no root result set: this is a list keyed by db_id, meant to be
-- joined to the databases array of 20.databases/010.all-databases.sql, and
-- root must be a single-row object.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT
    d.database_id                                                AS [db_id],
    d.name                                                       AS [database],
    d.is_query_store_on                                          AS [query_store_on]
FROM sys.databases AS d
WHERE d.database_id > 4                   -- exclude system DBs; remove to include them
ORDER BY d.name
OPTION (RECOMPILE, MAXDOP 1);
