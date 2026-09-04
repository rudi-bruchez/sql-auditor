-- @scope:       database
-- @resultsets:  root:object, features:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     30
--
-- Which Enterprise-era features are physically present in this database.
--
-- sys.dm_db_persisted_sku_features reports what has actually been used and
-- persisted rather than what is licensed: partitioning, data compression,
-- online index rebuild artefacts, change data capture, transparent encryption,
-- memory-optimized tables. One row per feature, per database, and it costs a
-- catalog read.
--
-- THE OBVIOUS MOTIVATION FOR THIS COLLECTOR IS OUT OF DATE, AND SAYING SO IS
-- HALF THE POINT OF THE FILE. "A backup taken on Enterprise restores onto
-- Standard only if this view is empty" has not been true since SQL Server 2016
-- SP1, which moved compression, partitioning, change data capture and
-- In-Memory OLTP into Standard. The view still lists them — measured, a table
-- created with DATA_COMPRESSION = PAGE puts a Compression row here on a
-- Developer instance — and the database restores onto Standard and the feature
-- works. An audit written against the old rule would report a defect against
-- healthy Standard instances, and the instances that motivated this file were
-- 2016 SP1.
--
-- So the finding is "this database carries these features", which is a
-- migration and licensing conversation, plus the genuinely edition-bound ones
-- — transparent data encryption before 2019, and what remains Enterprise-only
-- — where the restore question is still real. WHICH OF THEM CONSTRAIN THE
-- TARGET EDITION IS NOT DECIDED HERE. That needs the target version and
-- edition in hand, which the analysis step has and the collector does not.
--
-- THE PERMISSION IS VIEW SERVER STATE AND NOT VIEW ANY DEFINITION. Measured: a
-- login holding exactly CONNECT and VIEW ANY DEFINITION gets Msg 262, VIEW
-- DATABASE PERFORMANCE STATE permission denied, then Msg 297. This is a
-- dynamic management view and not a catalog view; VIEW ANY DEFINITION governs
-- metadata visibility and does not imply it. VIEW SERVER STATE carries VIEW
-- DATABASE STATE into every database, which grants.go already notes, and the
-- read then succeeds.
--
-- TWO RESULT SETS, NOT ONE. A root:object set returns at most one row and an
-- array set cannot merge a value into the root, so the count that distinguishes
-- "nothing persisted" from "the collector did not run" lives in the root and
-- the features are the array.
--
-- AN EMPTY ARRAY IS THE ANSWER AND NOT A FAILURE. The view is empty on a
-- database that never carried such a feature, which is the ordinary case. That
-- has to be said here because every other array in this corpus is empty only
-- when something went wrong, and a reader carrying that habit across would
-- read the commonest result as a broken collector.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                 AS [collected_at],
       (SELECT COUNT(*) FROM sys.dm_db_persisted_sku_features)  AS [counts.features]
OPTION (RECOMPILE, MAXDOP 1);

SELECT f.feature_name                                           AS [feature_name],
       f.feature_id                                             AS [feature_id]
FROM sys.dm_db_persisted_sku_features AS f
ORDER BY f.feature_name
OPTION (RECOMPILE, MAXDOP 1);
