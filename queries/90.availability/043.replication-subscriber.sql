-- @scope:       database
-- @resultsets:  root:object, subscriptions:array, agents:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     120
-- @widened:     replication
--
-- What this database subscribes to, and when it last heard from the
-- Distribution Agent.
--
-- ON A PULL SUBSCRIPTION THIS IS THE ONLY PLACE THE TOPOLOGY IS VISIBLE AT
-- ALL. The agent runs on the subscriber and its history lives on the
-- distributor, which may be a server this audit never touches. [Time] going
-- stale is then the whole signal.
--
-- IS_SUBSCRIBED IS NOT THE TEST, AND THAT IS A CORRECTION. This file used to
-- gate every read on sys.databases.is_subscribed. On a push subscriber audited
-- in September 2026 that column was 0 while the subscription database carried
-- 272 generated apply procedures and the distributor named it in its own agent
-- rows. The collector reported applies: 0 and every count at zero, so the
-- subscriber's own archive could not establish that it was a subscriber and
-- the topology had to be reconstructed from the publisher's — which is exactly
-- the detour this collector exists to remove.
--
-- There are now three recognisers, in decreasing order of authority, and the
-- one that answered is named in applies_source:
--
--   MSreplication_subscriptions  the table the snapshot creates in every
--                                subscription database, and the one that
--                                carries the topology itself
--   apply_procedures             the generated sp_MSins_, sp_MSupd_ and
--                                sp_MSdel_ procedures, which corroborate when
--                                the table is unreadable
--   is_subscribed                kept last and still projected, because a
--                                column that lied once is evidence about the
--                                instance rather than about replication
--
-- SO THE READS ARE NO LONGER GATED. They run in every database the collector
-- opens, which costs two deferred reads that fail at once where the objects
-- are absent. Msg 208 from them is not a failure, it is the answer "this is
-- not a subscription database", and it is recorded as absence — otherwise
-- every ordinary database in the archive would carry collected: 0 and the one
-- real error would be invisible among them.
--
-- ONLY THE COUNT OF APPLY PROCEDURES IS PROJECTED, NEVER THEIR NAMES. A
-- generated name embeds the published table's name, so a list of them would
-- put an application schema into a result set whose declared purpose is
-- topology.
--
-- The guard is the one described in 041 and measured in
-- docs/verification-replication-guard.md: the read is deferred through
-- sp_executesql so that a missing object is a catchable runtime error, the
-- rows land in table variables, and the result sets are emitted whatever
-- happened.
--
-- WHERE distribution_agent LIVES IS NOT SETTLED. Microsoft documents it on
-- MSreplication_subscriptions and a reviewer placed it on
-- MSsubscription_properties. This file reads the documented one; if the column
-- is not there the guard records the error rather than failing the unit, and
-- the verification run against a real subscriber settles it. That unsettled
-- column is also why the two reads below have a handler each rather than one
-- between them: an unproven column must not be able to take the agent
-- inventory down with it.
--
-- SQL Server 2012 is the floor.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @applies bit = 0, @is_subscribed bit = 0, @apply_procs int = 0,
        @absent_subs bit = 0, @absent_agents bit = 0,
        @err_subs int = 0, @err_agents int = 0,
        @source varchar(32) = 'none', @msg nvarchar(2048) = N'';

SELECT @is_subscribed = CONVERT(bit, d.is_subscribed)
FROM sys.databases AS d
WHERE d.database_id = DB_ID()
OPTION (RECOMPILE, MAXDOP 1);

/* The corroborating recogniser, and a plain catalog read rather than a guarded
   one: sys.procedures exists in every database. Counted, never named. */
SELECT @apply_procs = COUNT(*)
FROM sys.procedures AS p
WHERE p.name LIKE 'sp_MSins%' OR p.name LIKE 'sp_MSupd%' OR p.name LIKE 'sp_MSdel%'
OPTION (RECOMPILE, MAXDOP 1);

DECLARE @subs TABLE (
    [publisher] sysname, [publisher_db] sysname, [publication] sysname NULL,
    [independent_agent] bit, [subscription_type] int,
    [distribution_agent] sysname NULL, [last_updated] smalldatetime NULL,
    [immediate_sync] bit);

DECLARE @agents TABLE (
    [id] int, [publisher] sysname, [publisher_db] sysname,
    [publication] sysname NULL, [subscription_type] int, [queue_id] sysname NULL);

/* One handler per family, as in 042, and here the reason is written into the
   header above: this file reads distribution_agent from the table Microsoft
   documents it on, and a reviewer put it somewhere else. If that column is not
   there the read raises Msg 207 — and under a single shared handler that
   unsettled column would take the agent inventory down with it, which is the
   whole failure the split exists to prevent.

   Msg 208 is told apart from the rest in both handlers. It means the object is
   not here, which on the overwhelming majority of databases is the truth and
   not an incident. */
BEGIN TRY
    INSERT INTO @subs
    EXEC sys.sp_executesql N'
        SELECT s.publisher, s.publisher_db, s.publication,
               s.independent_agent, s.subscription_type,
               s.distribution_agent, s.[Time], s.immediate_sync
        FROM dbo.MSreplication_subscriptions AS s
        OPTION (RECOMPILE, MAXDOP 1)';
END TRY
BEGIN CATCH
    IF ERROR_NUMBER() = 208
        SET @absent_subs = 1;
    ELSE
        SELECT @err_subs = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

BEGIN TRY
    INSERT INTO @agents
    EXEC sys.sp_executesql N'
        SELECT a.id, a.publisher, a.publisher_db, a.publication,
               a.subscription_type, a.queue_id
        FROM dbo.MSsubscription_agents AS a
        OPTION (RECOMPILE, MAXDOP 1)';
END TRY
BEGIN CATCH
    IF ERROR_NUMBER() = 208
        SET @absent_agents = 1;
    ELSE
        SELECT @err_agents = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* The order is the order of authority, and applies_source is what lets a
   reader tell a topology this file read from one it merely inferred. */
SELECT @source = CASE
        WHEN @absent_subs = 0 AND @err_subs = 0
             AND (SELECT COUNT(*) FROM @subs) > 0 THEN 'MSreplication_subscriptions'
        WHEN @apply_procs > 0                     THEN 'apply_procedures'
        WHEN @is_subscribed = 1                   THEN 'is_subscribed'
        ELSE 'none' END;

SET @applies = CASE WHEN @source = 'none' THEN 0 ELSE 1 END;

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)     AS [collected_at],
       CONVERT(int, @applies)                       AS [applies],
       @source                                      AS [applies_source],
       CONVERT(int, @is_subscribed)                 AS [is_subscribed],
       /* collected on the same terms as 041 and 042: every guarded read
          returned. Two families here, so it is their conjunction — and an
          object that is simply not present is not a failed read. */
       CASE WHEN @err_subs = 0 AND @err_agents = 0 THEN 1 ELSE 0 END
                                                    AS [collected],
       CONVERT(int, @absent_subs)                   AS [absent.subscriptions],
       CONVERT(int, @absent_agents)                 AS [absent.agents],
       @err_subs                                    AS [errors.subscriptions],
       @err_agents                                  AS [errors.agents],
       NULLIF(@msg, N'')                            AS [errors.last_message],
       (SELECT COUNT(*) FROM @subs)                 AS [counts.subscriptions],
       @apply_procs                                 AS [counts.apply_procedures],
       (SELECT MIN(s.[last_updated]) FROM @subs AS s) AS [observed.oldest_update],
       (SELECT MAX(s.[last_updated]) FROM @subs AS s) AS [observed.newest_update]
OPTION (RECOMPILE, MAXDOP 1);

SELECT s.[publisher], s.[publisher_db], s.[publication],
       s.[subscription_type],
       CASE s.[subscription_type] WHEN 0 THEN 'push' WHEN 1 THEN 'pull'
                                  WHEN 2 THEN 'anonymous' END AS [subscription_type_desc],
       s.[distribution_agent], s.[last_updated],
       CONVERT(int, s.[independent_agent])          AS [independent_agent],
       CONVERT(int, s.[immediate_sync])             AS [immediate_sync]
FROM @subs AS s ORDER BY s.[publisher], s.[publisher_db]
OPTION (RECOMPILE, MAXDOP 1);

SELECT a.[id], a.[publisher], a.[publisher_db], a.[publication],
       a.[subscription_type], a.[queue_id]
FROM @agents AS a ORDER BY a.[id]
OPTION (RECOMPILE, MAXDOP 1);
