-- @scope:       instance
-- @resultsets:  root:object, objects:array, startup_procedures:array
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
-- STARTUP PROCEDURES ARE LISTED APART, AND ALL OF THEM. A procedure of master
-- flagged with sp_procoption 'startup' runs at every start of the instance,
-- with sysadmin rights, with nobody connected to see it: a legitimate
-- way to warm a cache or start a Service Broker queue, and a classic way to
-- persist on a compromised server (sp_Blitz check 7). The flag is
-- sys.procedures.is_auto_executed, which sys.objects does not carry, so the
-- objects listing above cannot show it, and its cap could push an old one out
-- of view anyway. startup_procedures therefore has no cap and no is_ms_shipped
-- filter: replication is reported to flag sp_MSrepl_startup on the instances
-- it configures (not measured here), and a procedure marked as system with
-- sp_MS_marksystemobject is exactly what a filter on is_ms_shipped would hide.
-- The procedures run only when the configuration option 'scan for startup
-- procs' is 1, which sp_procoption sets when it flags the first procedure and
-- clears when it unflags the last, and which 010.properties collects; both
-- halves are needed to say that something runs. The documentation adds that
-- dropping a flagged procedure without unflagging it first leaves the option
-- at 1, so an option at 1 with no procedure here is that trace (not measured:
-- it would leave a configuration change behind). Measured on SQL Server 2025:
-- a procedure flagged with sp_procoption appears here with is_auto_executed =
-- 1 and the option reads 1 with a value_in_use of 0, since it applies at the
-- next start; unflagging it removed the row and put the option back to 0.
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
       200                                                        AS [listing_cap],
       (SELECT COUNT(*)
          FROM master.sys.procedures
         WHERE is_auto_executed = 1)                              AS [startup_procedures_total]
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

SELECT p.name                                                      AS [name],
       SCHEMA_NAME(p.schema_id)                                    AS [schema],
       CAST(p.is_ms_shipped AS bit)                                AS [is_ms_shipped],
       p.create_date                                               AS [created],
       p.modify_date                                               AS [modified]
FROM master.sys.procedures AS p
WHERE p.is_auto_executed = 1
ORDER BY p.name
OPTION (RECOMPILE, MAXDOP 1);
