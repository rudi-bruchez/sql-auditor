-- @scope:       instance
-- @resultsets:  root:object, objects:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     60
--
-- User objects created in master, the instance-scoped view: database-scoped
-- collectors never run against master, so no other file in this archive can
-- see them.
--
-- Why this collector exists. The SQL Server Assessment ruleset flags user
-- objects in master, and the database-scoped collectors (@scope: database)
-- are not candidates for master on this runner, by a deliberate decision
-- recorded in collect/runner.go — so a user table or procedure parked in
-- master was invisible to every collection. The rule still has to be
-- answerable, and this file answers it from the instance scope instead.
--
-- THE INSTANCE SCOPE IS NOT A WORKAROUND, and this file is not in 70.schema/
-- because the corpus aligns directory with scope: sys.objects is read through
-- the three-part name master.sys.objects from wherever the collector runs,
-- and one pass covers master regardless of which databases the runner picks.
--
-- NAMES ARE PROJECTED, NOT JUST COUNTED. The archive is what leaves the
-- client, and an audit that reports "3 objects in master" without saying
-- which ones sends the reader back to the instance for the only information
-- that matters. 010.objects.sql already names tables in user databases on the
-- same principle; counting without naming would answer the letter of the rule
-- and none of its question.
--
-- TOP (200) ORDERED BY modify_date DESC, newest first. Three user objects in
-- master is a finding worth listing; three thousand is a migration gone
-- wrong, and the newest 200 still name the practice while the root object
-- carries the true total so the cap is never mistaken for the population.
--
-- BARE MASTER IS THE EXPECTED FACT, not an absence of data: a master with no
-- user object returns zero rows, and zero rows is the healthy answer. Any row
-- here is a finding by existing. No judgement beyond that is computed — the
-- corpus collects, the ruleset's threshold stays in the analysis layer.
--
-- SQL Server 2012 is the floor. Nothing in sys.objects used here is newer;
-- no column was excluded for that reason.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       DB_NAME()                                                  AS [collected_from],
       (SELECT COUNT(*)
          FROM master.sys.objects
         WHERE is_ms_shipped = 0)                                 AS [objects_total],
       (SELECT COUNT(*)
          FROM (SELECT TOP (200) o.modify_date
                  FROM master.sys.objects AS o
                 WHERE o.is_ms_shipped = 0
                 ORDER BY o.modify_date DESC) AS listed)           AS [objects_listed],
       200                                                        AS [listing_cap]
OPTION (RECOMPILE, MAXDOP 1);

SELECT TOP (200)
       o.name                                                      AS [name],
       o.type_desc                                                 AS [type],
       o.create_date                                               AS [created],
       o.modify_date                                               AS [modified]
FROM master.sys.objects AS o
WHERE o.is_ms_shipped = 0
ORDER BY o.modify_date DESC, o.object_id
OPTION (RECOMPILE, MAXDOP 1);
