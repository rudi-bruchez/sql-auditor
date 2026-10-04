-- @scope:       instance
-- @resultsets:  databases_2019_options:array
-- @permissions: VIEW ANY DEFINITION
-- @timeout:     60
-- @min_version: 15
--
-- Accelerated database recovery, per database, restored for the whole
-- instance from 20.databases/010.all-databases.sql, which holds the 2012 floor
-- and cannot name this column.
--
-- sys.databases.is_accelerated_database_recovery_on is documented as "SQL
-- Server 2019 (15.x) and later versions", so the gate is the bare major 15.
--
-- WHY tempdb IS THE ROW THAT MATTERS MOST. Before SQL Server 2025 the option
-- cannot be turned on for tempdb, and from 2025 it can, after a restart: a
-- rollback of temp-table work then becomes a logical revert instead of an
-- undo pass over the log, and the tempdb log can be truncated under a long
-- transaction. Whether that is worth proposing depends on this bit for
-- database_id 2, which nothing else in the archive carries. On a user
-- database the same bit says the persistent version store lives in that
-- database's own data files, which is what a reader of its size and of its
-- version-store figures needs to know. model is in the list because its value
-- is copied to every database created afterwards.
--
-- Measured on SQL Server 2025 CU7: the column is a bit, not NULL on any row,
-- tempdb included, and reads 0 for tempdb on a default installation.
--
-- There is no root result set: this is a list keyed by db_id, meant to be
-- joined to the databases array of 20.databases/010.all-databases.sql, and
-- root must be a single-row object. It covers the same databases as 010,
-- system ones included, so the two lists line up row for row. The
-- per-database document 020.properties.sql does not carry the option: it runs
-- on user databases only, so it would never see tempdb, and the instance list
-- already answers for every user database.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT
    d.database_id                                                AS [db_id],
    d.name                                                       AS [database],
    d.is_accelerated_database_recovery_on                        AS [accelerated_database_recovery]
FROM sys.databases AS d
ORDER BY d.name
OPTION (RECOMPILE, MAXDOP 1);
