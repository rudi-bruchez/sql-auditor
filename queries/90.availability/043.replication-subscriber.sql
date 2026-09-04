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

DECLARE @applies bit = 0, @err_subs int = 0, @err_agents int = 0,
        @msg nvarchar(2048) = N'';

SELECT @applies = CONVERT(bit, d.is_subscribed)
FROM sys.databases AS d
WHERE d.database_id = DB_ID()
OPTION (RECOMPILE, MAXDOP 1);

DECLARE @subs TABLE (
    [publisher] sysname, [publisher_db] sysname, [publication] sysname NULL,
    [independent_agent] bit, [subscription_type] int,
    [distribution_agent] sysname NULL, [last_updated] smalldatetime NULL,
    [immediate_sync] bit);

DECLARE @agents TABLE (
    [id] int, [publisher] sysname, [publisher_db] sysname,
    [publication] sysname NULL, [subscription_type] int, [queue_id] sysname NULL);

IF @applies = 1
BEGIN
    /* One handler per family, as in 042, and here the reason is written into
       the header above: this file reads distribution_agent from the table
       Microsoft documents it on, and a reviewer put it somewhere else. If that
       column is not there the read raises Msg 207 — and under a single shared
       handler that unsettled column would take the agent inventory down with
       it, which is the whole failure the split exists to prevent. */
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
        SELECT @err_agents = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH
END

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)     AS [collected_at],
       CONVERT(int, @applies)                       AS [applies],
       /* collected on the same terms as 041 and 042: every guarded read
          returned. Two families here, so it is their conjunction. */
       CASE WHEN @err_subs = 0 AND @err_agents = 0 THEN 1 ELSE 0 END
                                                    AS [collected],
       @err_subs                                    AS [errors.subscriptions],
       @err_agents                                  AS [errors.agents],
       NULLIF(@msg, N'')                            AS [errors.last_message],
       (SELECT COUNT(*) FROM @subs)                 AS [counts.subscriptions],
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
