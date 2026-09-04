-- @scope:       instance
-- @resultsets:  databases_2014_options:array
-- @permissions: VIEW ANY DEFINITION
-- @timeout:     60
-- @min_version: 12
--
-- The two sys.databases options that arrived in SQL Server 2014, restored for
-- the whole instance from 20.databases/010.all-databases.sql, which holds the
-- 2012 floor and cannot name these columns.
--
-- sys.databases.delayed_durability / delayed_durability_desc and
-- is_auto_create_stats_incremental_on are all documented as "SQL Server 2014
-- (12.x) and later versions", with no service-pack qualifier, so the gate is
-- the bare major 12: any 12.x satisfies it.
--
-- There is no root result set: this is a list keyed by db_id, meant to be
-- joined to the databases array of 20.databases/010.all-databases.sql, and
-- root must be a single-row object. The same WHERE d.database_id > 4 excludes
-- the system databases, so the two lists line up row for row.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT
    d.database_id                                                AS [db_id],
    d.name                                                       AS [database],
    d.delayed_durability                                         AS [delayed_durability],
    d.delayed_durability_desc                                    AS [delayed_durability_desc],
    d.is_auto_create_stats_incremental_on                        AS [auto_create_stats_incremental]
FROM sys.databases AS d
WHERE d.database_id > 4                   -- exclude system DBs; remove to include them
ORDER BY d.name
OPTION (RECOMPILE, MAXDOP 1);
