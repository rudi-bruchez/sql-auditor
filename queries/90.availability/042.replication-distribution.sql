-- @scope:       database
-- @resultsets:  root:object, configuration:array, publications:array, articles:array, agents:array, agent_profiles:array, agent_profile_parameters:array, latency:array, repl_errors:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     120
-- @discloses:   replication_messages
-- @widened:     replication
--
-- The distribution database: what it distributes, for whom, how far behind,
-- and what it has been complaining about.
--
-- ONE TRY/CATCH PER OBJECT FAMILY, NOT ONE FOR THE FILE. The replication
-- tables in msdb are created by sp_adddistributor, not by setup, so on an
-- instance that was never a distributor they are absent — measured, zero rows
-- in msdb.sys.tables for the whole list. A single handler would let one absent
-- family cost every other section.
--
-- THE TWO delivery_latency COLUMNS ARE NOT THE SAME MEASUREMENT AND ARE NEVER
-- ADDED TOGETHER. In MSlogreader_history it is the milliseconds between a
-- command committing in the published database and arriving here. In
-- MSdistribution_history it is the milliseconds between here and the
-- subscriber. A topology that is behind is behind on one leg or the other, and
-- which one decides where to look.
--
-- THE MEDIAN IS THE ANALYTIC FORM. PERCENTILE_CONT as an aggregate — WITHIN
-- GROUP with no OVER — exists in Azure SQL and Fabric and on no version of SQL
-- Server: Msg 10753 on 2022. At the 2012 floor and everywhere else it is
-- OVER (PARTITION BY ...) in a CTE, collapsed by a grouping outside it.
--
-- MERGE IS IN THE AGENT INVENTORY AND NOT IN THE LATENCY ARRAY, deliberately.
-- MSmerge_agents has the same shape as the other three agent tables and joins
-- them under kind = 'merge'. Its history does not: MSmerge_history carries only
-- a session id, a comment and a time, and the numbers live in MSmerge_sessions
-- as duration, delivery_time, upload_time, download_time and row counts —
-- measured on a configured merge publication, not read off documentation.
-- Those are not delivery latency and putting them in a column called
-- median_latency_ms would be the same error as adding the two transactional
-- legs together. Merge session statistics are therefore NOT COLLECTED here;
-- what the archive holds for a merge topology is its agents, its errors and
-- its configuration.
--
-- THE ROW COUNT OF MSrepl_commands HAS ITS OWN HANDLER because
-- sys.dm_db_partition_stats needs VIEW DATABASE STATE, which this file does
-- not declare. A login without it still gets the topology.
--
-- NO PASSWORD COLUMN IS PROJECTED. MSlogreader_agents carries
-- publisher_password and job_password. Projections stay explicit.
--
-- comments IS TRUNCATED INSIDE THE CTE AND NOT OUTSIDE IT, WHICH IS THE WHOLE
-- POINT. It is nvarchar(4000), and the CTE feeds a window sort of a week of
-- history — millions of rows on a busy distributor, sorted single-threaded
-- under MAXDOP 1. Carrying the full width through the sort is what turns a
-- diagnostic into a memory grant that spills to tempdb and hits the timeout.
-- Only the newest row's comment is ever emitted, so 512 characters is the
-- widest anything downstream can see either way.
--
-- THE AGENT PROFILES ARE READ FROM msdb AND THE AGENT JOB STEPS, BOTH. A
-- profile is a named set of agent parameters in msdb.dbo.MSagent_profiles and
-- MSagent_parameters, and each agent row here carries the profile_id it was
-- assigned. But the job step that starts the agent may repeat a parameter on
-- its command line, and the command line wins. Two measurements on SQL Server
-- 2025 decide that the step has to be read too:
--
--   - sp_add_agent_parameter refuses -MaxCmdsInTran for a Log Reader profile
--     (Msg 21806, validated against msdb.dbo.MSagentparameterlist, which does
--     not list it), so on a current build that parameter can only ever be set
--     on the command line. A profile read alone would never see it.
--   - -SkipErrors is the parameter an audit must always report, because an
--     agent that skips errors leaves the subscriber silently different from
--     the publisher. It can be set in a profile (the system profile "Continue
--     on data consistency errors." ships with 2601:2627:20598) or on the step.
--
-- THE STEP COMMAND IS NEVER PROJECTED. A replication agent's command line is
-- where -PublisherPassword and -SubscriberPassword are written when an agent
-- uses SQL authentication. Only the token after -SkipErrors and after
-- -MaxCmdsInTran is extracted, capped at 100 characters: a list of error
-- numbers and an integer. Everything else on the line stays on the server.
--
-- A NEW PROFILE IS A COPY, SO "NON-DEFAULT" IS MEASURED AGAINST THE ENGINE'S
-- OWN LIST. sp_add_agent_profile copies every parameter of the default
-- profile of its agent type into the new one, measured. A custom profile
-- therefore lists a dozen parameters of which perhaps one was changed.
-- agent_profile_parameters keeps only those whose value differs from
-- default_value in msdb.dbo.MSagentparameterlist, or that the list does not
-- know. The shipped default profiles themselves differ from that list in
-- places (-HistoryVerboseLevel 1 in the Distribution default profile against
-- 2 in the list, measured), so a row here means "this profile sets something
-- other than the agent's built-in default", not "somebody changed this".
-- Where the list cannot be read, every parameter of every profile in use is
-- emitted with default_value NULL, which is wider and never silently thinner.
--
-- msdb grants nothing on these tables to public, measured: no row in
-- msdb.sys.database_permissions names them. A login outside sysadmin and
-- msdb's own roles is refused, each of the three msdb reads has its own
-- handler, and the refusal is in errors.* without touching collected, on the
-- same terms as the configuration read above.
--
-- SQL Server 2012 is the floor.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @window_days int = 7;

DECLARE @applies bit = 0,
        @err_cfg int = 0, @err_topo int = 0, @err_agents int = 0,
        @err_hist int = 0, @err_errs int = 0, @err_size int = 0,
        @err_prof int = 0, @err_pdef int = 0, @err_steps int = 0,
        @msg nvarchar(2048) = N'';

SELECT @applies = CONVERT(bit, d.is_distributor)
FROM sys.databases AS d
WHERE d.database_id = DB_ID()
OPTION (RECOMPILE, MAXDOP 1);
DECLARE @cfg TABLE ([name] sysname, [min_distretention] int,
                    [max_distretention] int, [history_retention] int);

DECLARE @pubs TABLE ([publisher_id] smallint, [publisher_db] sysname,
                     [publication] sysname, [publication_id] int,
                     [publication_type] int, [retention] int,
                     [immediate_sync] bit, [independent_agent] bit);

DECLARE @arts TABLE ([publisher_id] smallint, [publisher_db] sysname,
                     [publication_id] int, [article] sysname, [article_id] int,
                     [source_owner] sysname NULL, [source_object] sysname NULL,
                     [destination_owner] sysname NULL, [destination_object] sysname NULL);

DECLARE @agents TABLE ([kind] varchar(12), [id] int, [name] nvarchar(100),
                       [publisher_db] sysname NULL, [publication] sysname NULL,
                       [subscriber_db] sysname NULL, [job_id] binary(16) NULL,
                       [local_job] bit NULL, [profile_id] int NULL,
                       [subscriptionstreams] tinyint NULL);

DECLARE @profiles TABLE ([profile_id] int, [profile_name] sysname,
                         [agent_type] int, [type] int, [def_profile] bit);

DECLARE @params TABLE ([profile_id] int, [parameter_name] sysname,
                       [value] nvarchar(255) NULL);

DECLARE @param_defaults TABLE ([agent_type] int, [parameter_name] sysname,
                               [default_value] nvarchar(255) NULL);

DECLARE @steps TABLE ([job_id] uniqueidentifier, [step_id] int,
                      [skip_errors] nvarchar(100) NULL,
                      [max_cmds_in_tran] nvarchar(100) NULL);

DECLARE @hist TABLE ([leg] varchar(40), [agent_id] int, [runstatus] int,
                     [last_time] datetime NULL, [last_duration] int NULL,
                     [last_latency_ms] int NULL, [max_latency_ms] int NULL,
                     [median_latency_ms] float NULL, [sessions] int,
                     [delivered_commands] bigint NULL,
                     [last_comment] nvarchar(512) NULL);

DECLARE @errs TABLE ([id] int, [time] datetime, [error_code] sysname NULL,
                     [error_text] nvarchar(512) NULL, [source_type_id] int NULL,
                     [in_window] int NULL);

DECLARE @size TABLE ([table_name] sysname, [row_count] bigint);
IF @applies = 1
BEGIN
    BEGIN TRY
        INSERT INTO @cfg
        EXEC sys.sp_executesql N'
            SELECT d.name, d.min_distretention, d.max_distretention, d.history_retention
            FROM msdb.dbo.MSdistributiondbs AS d
            WHERE d.name = DB_NAME()
            OPTION (RECOMPILE, MAXDOP 1)';
    END TRY
    BEGIN CATCH
        SELECT @err_cfg = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH

    BEGIN TRY
        INSERT INTO @pubs
        EXEC sys.sp_executesql N'
            SELECT p.publisher_id, p.publisher_db, p.publication, p.publication_id,
                   p.publication_type, p.retention, p.immediate_sync, p.independent_agent
            FROM dbo.MSpublications AS p
            OPTION (RECOMPILE, MAXDOP 1)';

        INSERT INTO @arts
        EXEC sys.sp_executesql N'
            SELECT a.publisher_id, a.publisher_db, a.publication_id, a.article,
                   a.article_id, a.source_owner, a.source_object,
                   a.destination_owner, a.destination_object
            FROM dbo.MSarticles AS a
            OPTION (RECOMPILE, MAXDOP 1)';
    END TRY
    BEGIN CATCH
        SELECT @err_topo = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH
    BEGIN TRY
        INSERT INTO @agents ([kind], [id], [name], [publisher_db], [publication],
                             [subscriber_db], [job_id], [local_job],
                             [profile_id], [subscriptionstreams])
        EXEC sys.sp_executesql N'
            SELECT ''distribution'', a.id, a.name, a.publisher_db, a.publication,
                   a.subscriber_db, a.job_id, a.local_job,
                   a.profile_id, a.subscriptionstreams
            FROM dbo.MSdistribution_agents AS a
            OPTION (RECOMPILE, MAXDOP 1)';

        INSERT INTO @agents ([kind], [id], [name], [publisher_db], [publication],
                             [subscriber_db], [job_id], [local_job], [profile_id])
        EXEC sys.sp_executesql N'
            SELECT ''logreader'', a.id, a.name, a.publisher_db, a.publication,
                   NULL, a.job_id, a.local_job, a.profile_id
            FROM dbo.MSlogreader_agents AS a
            OPTION (RECOMPILE, MAXDOP 1)';

        INSERT INTO @agents ([kind], [id], [name], [publisher_db], [publication],
                             [subscriber_db], [job_id], [local_job], [profile_id])
        EXEC sys.sp_executesql N'
            SELECT ''snapshot'', a.id, a.name, a.publisher_db, a.publication,
                   NULL, a.job_id, a.local_job, a.profile_id
            FROM dbo.MSsnapshot_agents AS a
            OPTION (RECOMPILE, MAXDOP 1)';

        INSERT INTO @agents ([kind], [id], [name], [publisher_db], [publication],
                             [subscriber_db], [job_id], [local_job], [profile_id])
        EXEC sys.sp_executesql N'
            SELECT ''merge'', a.id, a.name, a.publisher_db, a.publication,
                   a.subscriber_db, a.job_id, a.local_job, a.profile_id
            FROM dbo.MSmerge_agents AS a
            OPTION (RECOMPILE, MAXDOP 1)';
    END TRY
    BEGIN CATCH
        SELECT @err_agents = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH

    /* Every profile and every parameter, not only those in use: the msdb read
       cannot see @agents, and the whole of both tables is a few dozen rows on
       any instance (16 profiles and their parameters on a fresh 2025
       distributor). The filter to the profiles in use is applied on output. */
    BEGIN TRY
        INSERT INTO @profiles
        EXEC sys.sp_executesql N'
            SELECT p.profile_id, p.profile_name, p.agent_type, p.[type], p.def_profile
            FROM msdb.dbo.MSagent_profiles AS p
            OPTION (RECOMPILE, MAXDOP 1)';

        INSERT INTO @params
        EXEC sys.sp_executesql N'
            SELECT x.profile_id, x.parameter_name, LEFT(x.value, 255)
            FROM msdb.dbo.MSagent_parameters AS x
            OPTION (RECOMPILE, MAXDOP 1)';
    END TRY
    BEGIN CATCH
        SELECT @err_prof = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH

    /* Its own handler, so that a build or a login without the list still
       gets the profiles: the parameters are then emitted unfiltered. */
    BEGIN TRY
        INSERT INTO @param_defaults
        EXEC sys.sp_executesql N'
            SELECT l.agent_type, l.parameter_name, LEFT(l.default_value, 255)
            FROM msdb.dbo.MSagentparameterlist AS l
            OPTION (RECOMPILE, MAXDOP 1)';
    END TRY
    BEGIN CATCH
        SELECT @err_pdef = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH

    /* The agent steps of every replication job on this server, reduced to the
       two tokens. The command itself never leaves this statement; see the
       header. The comparison is made case-insensitive explicitly, because the
       agents accept the parameter in any case and msdb may have been installed
       under a case-sensitive collation. A pull subscription's Distribution
       Agent runs at the subscriber and has no step here: its row in agents
       says job_step_found 0, which is not the same as "no -SkipErrors". */
    BEGIN TRY
        INSERT INTO @steps
        EXEC sys.sp_executesql N'
            SELECT s.job_id, s.step_id,
                   CASE WHEN se.p > 0 THEN LEFT(LEFT(se.rest, CHARINDEX(N'' '', se.rest + N'' '') - 1), 100) END,
                   CASE WHEN mc.p > 0 THEN LEFT(LEFT(mc.rest, CHARINDEX(N'' '', mc.rest + N'' '') - 1), 100) END
            FROM msdb.dbo.sysjobsteps AS s
            CROSS APPLY (SELECT CONVERT(nvarchar(4000), s.command) COLLATE Latin1_General_CI_AS AS cmd) AS c
            CROSS APPLY (SELECT CHARINDEX(N''-SkipErrors'', c.cmd) AS p) AS se0
            CROSS APPLY (SELECT se0.p, CASE WHEN se0.p > 0
                                THEN LTRIM(SUBSTRING(c.cmd, se0.p + 11, 4000)) END AS rest) AS se
            CROSS APPLY (SELECT CHARINDEX(N''-MaxCmdsInTran'', c.cmd) AS p) AS mc0
            CROSS APPLY (SELECT mc0.p, CASE WHEN mc0.p > 0
                                THEN LTRIM(SUBSTRING(c.cmd, mc0.p + 14, 4000)) END AS rest) AS mc
            WHERE s.subsystem IN (N''Distribution'', N''LogReader'', N''Snapshot'', N''Merge'')
            OPTION (RECOMPILE, MAXDOP 1)';
    END TRY
    BEGIN CATCH
        SELECT @err_steps = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH
    /* KNOWN COST, LEFT AS IT IS ON PURPOSE. Each branch of the UNION ALL below
       computes two window functions over the same partition with different
       orderings — PERCENTILE_CONT by delivery_latency, ROW_NUMBER by [time] —
       so the plan carries two sort operators per branch, four in all, over
       seven days of MSdistribution_history and MSlogreader_history. LEFT(x
       .comments, 512) is projected inside the CTE, so up to a kilobyte a row
       travels through both sorts although one row per agent is emitted.

       Every rewrite considered costs more than it saves or cannot be judged
       without measuring. Lifting comments out through an OUTER APPLY replaces
       the width with a per-agent lookup whose cost depends on an index this
       file must not assume. Splitting the CTE so each sort sees only its own
       columns makes the base tables read three times, because SQL Server does
       not materialise a CTE referenced more than once.

       What bounds it meanwhile: @timeout 120 on the client side, READ
       UNCOMMITTED and SET LOCK_TIMEOUT so it can neither block nor be blocked
       for long. The worst case is a slow collector, not a stalled distributor.

       Anyone changing this needs a distributor with real history and an actual
       plan, not this comment. */
    BEGIN TRY
        INSERT INTO @hist
        EXEC sys.sp_executesql N'
            WITH h AS (
                SELECT ''distribution_to_subscriber'' AS leg, x.agent_id, x.runstatus,
                       x.[time], x.duration, x.delivery_latency, x.delivered_commands,
                       LEFT(x.comments, 512) AS comments,
                       PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY x.delivery_latency)
                           OVER (PARTITION BY x.agent_id) AS median_latency,
                       ROW_NUMBER() OVER (PARTITION BY x.agent_id ORDER BY x.[time] DESC) AS rn
                FROM dbo.MSdistribution_history AS x
                WHERE x.[time] >= DATEADD(day, -@days, GETDATE())
                UNION ALL
                SELECT ''publisher_to_distribution'', x.agent_id, x.runstatus,
                       x.[time], x.duration, x.delivery_latency, x.delivered_commands,
                       LEFT(x.comments, 512),
                       PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY x.delivery_latency)
                           OVER (PARTITION BY x.agent_id),
                       ROW_NUMBER() OVER (PARTITION BY x.agent_id ORDER BY x.[time] DESC)
                FROM dbo.MSlogreader_history AS x
                WHERE x.[time] >= DATEADD(day, -@days, GETDATE())
            )
            SELECT h.leg, h.agent_id,
                   MAX(CASE WHEN h.rn = 1 THEN h.runstatus END),
                   MAX(CASE WHEN h.rn = 1 THEN h.[time] END),
                   MAX(CASE WHEN h.rn = 1 THEN h.duration END),
                   MAX(CASE WHEN h.rn = 1 THEN h.delivery_latency END),
                   MAX(h.delivery_latency),
                   MAX(h.median_latency),
                   COUNT(*),
                   SUM(CONVERT(bigint, h.delivered_commands)),
                   MAX(CASE WHEN h.rn = 1 THEN h.comments END)
            FROM h GROUP BY h.leg, h.agent_id
            OPTION (RECOMPILE, MAXDOP 1)',
            N'@days int', @days = @window_days;
    END TRY
    BEGIN CATCH
        SELECT @err_hist = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH
    /* The 50 newest errors of the window, and how many the window held.
       COUNT(*) OVER () is computed before the TOP, so the count rides on the
       read that lists the rows and costs no second pass over MSrepl_errors.
       Without it, 50 rows read as "50 errors in seven days" whether the
       window held 50 or 50 000, and an analysis counting the array writes
       exactly that sentence. The listing stays at 50, which is enough to
       name the errors; it is the count that says how often. */
    BEGIN TRY
        INSERT INTO @errs
        EXEC sys.sp_executesql N'
            SELECT TOP (50) e.id, e.[time], e.error_code, LEFT(CONVERT(nvarchar(4000), e.error_text), 512),
                   e.source_type_id, COUNT(*) OVER ()
            FROM dbo.MSrepl_errors AS e
            WHERE e.[time] >= DATEADD(day, -@days, GETDATE())
            ORDER BY e.[time] DESC
            OPTION (RECOMPILE, MAXDOP 1)',
            N'@days int', @days = @window_days;
    END TRY
    BEGIN CATCH
        SELECT @err_errs = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH

    /* Its own handler: sys.dm_db_partition_stats needs VIEW DATABASE STATE,
       which this file does not declare. No COUNT(*) — MSrepl_commands is the
       largest table on any busy distributor and this collector must not be the
       reason one stalls. */
    BEGIN TRY
        INSERT INTO @size
        EXEC sys.sp_executesql N'
            SELECT o.name, SUM(ps.row_count)
            FROM sys.dm_db_partition_stats AS ps
            JOIN sys.objects AS o ON o.object_id = ps.object_id
            WHERE ps.index_id IN (0, 1)
              AND o.name IN (N''MSrepl_commands'', N''MSrepl_transactions'')
            GROUP BY o.name
            OPTION (RECOMPILE, MAXDOP 1)';
    END TRY
    BEGIN CATCH
        SELECT @err_size = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH
END

/* One row per agent with what its profile and its job step say, resolved
   once here so that the agents array and the root count cannot disagree.
   The step wins where it sets a value, as it does for the agent. A token that
   starts with "-" is the next parameter, meaning the step names -SkipErrors
   with no value, and is read as no value. An empty -SkipErrors is the shipped
   default in the Distribution default profile, measured, and means none. */
DECLARE @agent_eff TABLE ([kind] varchar(12), [id] int, [job_step_found] int,
                          [job_skip_errors] nvarchar(100) NULL,
                          [job_max_cmds_in_tran] nvarchar(100) NULL,
                          [profile_skip_errors] nvarchar(255) NULL);

INSERT INTO @agent_eff
SELECT a.[kind], a.[id],
       CASE WHEN @err_steps <> 0 THEN NULL WHEN st.[n] > 0 THEN 1 ELSE 0 END,
       NULLIF(CASE WHEN LEFT(st.[skip_errors], 1) = N'-' THEN N'' ELSE st.[skip_errors] END, N''),
       NULLIF(CASE WHEN LEFT(st.[max_cmds_in_tran], 1) = N'-' THEN N'' ELSE st.[max_cmds_in_tran] END, N''),
       (SELECT NULLIF(x.[value], N'') FROM @params AS x
        WHERE x.[profile_id] = a.[profile_id] AND x.[parameter_name] = N'-SkipErrors')
FROM @agents AS a
OUTER APPLY (SELECT COUNT(*) AS [n], MAX(s.[skip_errors]) AS [skip_errors],
                    MAX(s.[max_cmds_in_tran]) AS [max_cmds_in_tran]
             FROM @steps AS s
             WHERE s.[job_id] = CONVERT(uniqueidentifier, a.[job_id])) AS st
OPTION (RECOMPILE, MAXDOP 1);
SELECT CONVERT(varchar(23), SYSDATETIME(), 126)     AS [collected_at],
       CONVERT(int, @applies)                       AS [applies],
       /* collected, on the same terms as 041, 043 and 044: every read this
          file's declared permissions entitle it to returned. That is the four
          reads INSIDE this database, and deliberately not the two that are
          not: the configuration read crosses into msdb, and the size read
          needs a DMV right this file does not declare and says so above. Both
          report themselves in errors.* and neither falsifies this flag.

          The conjunction of all six was measured wrong. A login holding
          exactly what the header declares collected the topology, the four
          agents, the history and the errors — everything this file exists for
          — and the archive still said collected 0, because msdb was closed to
          it and sys.dm_db_partition_stats answered Msg 262. An analysis layer
          reading that flag would have discarded a complete document. */
       CASE WHEN @err_topo = 0 AND @err_agents = 0
             AND @err_hist = 0 AND @err_errs = 0
            THEN 1 ELSE 0 END                       AS [collected],
       @window_days                                 AS [window_days],
       @err_cfg    AS [errors.configuration], @err_topo  AS [errors.topology],
       @err_agents AS [errors.agents],        @err_hist  AS [errors.history],
       @err_errs   AS [errors.repl_errors],   @err_size  AS [errors.size],
       @err_prof   AS [errors.profiles],      @err_pdef  AS [errors.parameter_defaults],
       @err_steps  AS [errors.job_steps],
       NULLIF(@msg, N'')                            AS [errors.last_message],
       (SELECT COUNT(*) FROM @pubs)                 AS [counts.publications],
       (SELECT COUNT(*) FROM @arts)                 AS [counts.articles],
       (SELECT COUNT(*) FROM @agents)               AS [counts.agents],
       /* Every error of the window, not the 50 listed in repl_errors. NULL
          when MSrepl_errors could not be read; 0 when it was and the window
          held none. */
       CASE WHEN @applies = 0 OR @err_errs <> 0 THEN NULL
            ELSE ISNULL((SELECT MAX([in_window]) FROM @errs), 0)
       END                                          AS [counts.repl_errors_in_window],
       50                                           AS [repl_errors_listing_cap],
       /* The number this section exists for: agents that skip errors, by
          their profile or their job step. A count above zero stands whatever
          else failed, as a floor. Zero is only emitted when the agents, the
          profiles and the steps were all read, because otherwise it claims
          something nobody looked at: measured, a login refused the agent
          tables and allowed sysjobsteps reported 0 here before this rule. */
       CASE WHEN (SELECT COUNT(*) FROM @agent_eff AS e
                  WHERE COALESCE(e.[job_skip_errors], e.[profile_skip_errors]) IS NOT NULL) > 0
            THEN (SELECT COUNT(*) FROM @agent_eff AS e
                  WHERE COALESCE(e.[job_skip_errors], e.[profile_skip_errors]) IS NOT NULL)
            WHEN @err_agents <> 0 OR @err_prof <> 0 OR @err_steps <> 0 THEN NULL
            ELSE 0
       END                                          AS [counts.agents_skipping_errors],
       (SELECT COUNT(DISTINCT [profile_id]) FROM @agents WHERE [profile_id] IS NOT NULL)
                                                    AS [counts.profiles_in_use],
       (SELECT [row_count] FROM @size WHERE [table_name] = N'MSrepl_commands')
                                                    AS [counts.repl_commands_rows],
       /* Staged by the same read as the line above, so projecting it costs
          nothing and dropping it would leave the query paying for a row it
          throws away. The pair is also the useful reading: commands per
          transaction says whether the backlog is many small units or few
          large ones. */
       (SELECT [row_count] FROM @size WHERE [table_name] = N'MSrepl_transactions')
                                                    AS [counts.repl_transactions_rows]
OPTION (RECOMPILE, MAXDOP 1);

SELECT c.[name], c.[min_distretention], c.[max_distretention], c.[history_retention]
FROM @cfg AS c OPTION (RECOMPILE, MAXDOP 1);

SELECT p.[publisher_id], p.[publisher_db], p.[publication], p.[publication_id],
       p.[publication_type],
       CASE p.[publication_type] WHEN 0 THEN 'transactional' WHEN 1 THEN 'snapshot'
                                 WHEN 2 THEN 'merge' END       AS [publication_type_desc],
       p.[retention], CONVERT(int, p.[immediate_sync])          AS [immediate_sync],
       CONVERT(int, p.[independent_agent])                      AS [independent_agent]
FROM @pubs AS p ORDER BY p.[publisher_db], p.[publication]
OPTION (RECOMPILE, MAXDOP 1);

SELECT a.[publisher_db], a.[publication_id], a.[article], a.[article_id],
       a.[source_owner], a.[source_object], a.[destination_owner], a.[destination_object]
FROM @arts AS a ORDER BY a.[publisher_db], a.[article]
OPTION (RECOMPILE, MAXDOP 1);

/* job_id is staged and now projected, because it is the join to the Agent job
   inventory 50.agent/010.jobs.sql already collects: an agent that is failing
   here and a job that is failing there are the same fact reported twice, and
   without this column nothing connects them. It is binary(16) in these tables
   and a uniqueidentifier in msdb.dbo.sysjobs; the conversion was measured
   against both and yields the matching GUID. local_job stays beside it — it
   answers a different question, whether the job runs on this server at all. */
/* profile_id joins to agent_profiles. subscriptionstreams is the column the
   subscription itself carries (sp_addsubscription @subscriptionstreams), only
   on Distribution Agents, and NULL when the subscription leaves it to the
   profile, measured on a push subscription created without it.

   skip_errors is the value the agent runs with: the job step's when the step
   sets one, else the profile's. skip_errors_source says which, so that a
   reader who changes the profile knows whether that will change anything.
   job_step_found 0 means no step of this agent's job is on this server (a
   pull subscription, or a job deleted by hand); NULL means the steps could not
   be read, and errors.job_steps says why. */
SELECT a.[kind], a.[id], a.[name], a.[publisher_db], a.[publication],
       a.[subscriber_db],
       CONVERT(char(36), CONVERT(uniqueidentifier, a.[job_id])) AS [job_id],
       CONVERT(int, a.[local_job]) AS [local_job],
       a.[profile_id],
       CONVERT(int, a.[subscriptionstreams])                   AS [subscriptionstreams],
       e.[job_step_found], e.[job_skip_errors], e.[job_max_cmds_in_tran],
       COALESCE(e.[job_skip_errors], e.[profile_skip_errors])  AS [skip_errors],
       CASE WHEN e.[job_skip_errors] IS NOT NULL THEN 'job_step'
            WHEN e.[profile_skip_errors] IS NOT NULL THEN 'profile' END AS [skip_errors_source]
FROM @agents AS a
JOIN @agent_eff AS e ON e.[kind] = a.[kind] AND e.[id] = a.[id]
ORDER BY a.[kind], a.[name]
OPTION (RECOMPILE, MAXDOP 1);

/* The profiles assigned to at least one agent of this distribution database,
   with the five parameters an audit reads first spelled out. NULL means the
   profile does not set the parameter, so the agent runs with its built-in
   default; an empty -SkipErrors is reported as NULL for the same reason.
   max_cmds_in_tran will be NULL on any profile created through
   sp_add_agent_parameter on a build that validates against
   MSagentparameterlist; it is here for profiles carried over from older
   builds, and the job step column in agents is where it is normally found.
   is_system is MSagent_profiles.type = 0, a profile shipped by SQL Server;
   is_default is def_profile, the one new agents of that type receive. */
SELECT p.[profile_id], p.[profile_name], p.[agent_type],
       CASE p.[agent_type] WHEN 1 THEN 'snapshot' WHEN 2 THEN 'logreader'
                           WHEN 3 THEN 'distribution' WHEN 4 THEN 'merge'
                           WHEN 9 THEN 'queuereader' END             AS [agent_type_desc],
       CASE p.[type] WHEN 0 THEN 1 ELSE 0 END                        AS [is_system],
       CONVERT(int, p.[def_profile])                                 AS [is_default],
       (SELECT COUNT(*) FROM @agents AS a WHERE a.[profile_id] = p.[profile_id]) AS [agents],
       NULLIF(MAX(CASE x.[parameter_name] WHEN N'-SkipErrors' THEN x.[value] END), N'') AS [skip_errors],
       MAX(CASE x.[parameter_name] WHEN N'-MaxCmdsInTran'      THEN x.[value] END) AS [max_cmds_in_tran],
       MAX(CASE x.[parameter_name] WHEN N'-SubscriptionStreams' THEN x.[value] END) AS [subscription_streams],
       MAX(CASE x.[parameter_name] WHEN N'-ReadBatchSize'      THEN x.[value] END) AS [read_batch_size],
       MAX(CASE x.[parameter_name] WHEN N'-CommitBatchSize'    THEN x.[value] END) AS [commit_batch_size]
FROM @profiles AS p
LEFT JOIN @params AS x ON x.[profile_id] = p.[profile_id]
WHERE EXISTS (SELECT 1 FROM @agents AS a WHERE a.[profile_id] = p.[profile_id])
GROUP BY p.[profile_id], p.[profile_name], p.[agent_type], p.[type], p.[def_profile]
ORDER BY p.[agent_type], p.[profile_id]
OPTION (RECOMPILE, MAXDOP 1);

/* Every parameter of a profile in use whose value is not the agent's built-in
   default, with that default beside it. The list stores names without the
   leading dash, the profiles with it, measured, hence the STUFF. A parameter
   the list does not know is kept, and so is every parameter when the list
   could not be read: default_value is then NULL. */
SELECT x.[profile_id], p.[profile_name], x.[parameter_name], x.[value],
       d.[default_value]
FROM @params AS x
JOIN @profiles AS p ON p.[profile_id] = x.[profile_id]
LEFT JOIN @param_defaults AS d
       ON d.[agent_type] = p.[agent_type]
      AND d.[parameter_name] = STUFF(x.[parameter_name], 1, 1, N'')
WHERE EXISTS (SELECT 1 FROM @agents AS a WHERE a.[profile_id] = p.[profile_id])
  AND (d.[parameter_name] IS NULL
       OR ISNULL(x.[value], N'') <> ISNULL(d.[default_value], N''))
ORDER BY x.[profile_id], x.[parameter_name]
OPTION (RECOMPILE, MAXDOP 1);

/* runstatus is projected raw beside its description, and the raw one is the
   authority. The documented set is 1 to 6, and a freshly configured
   distributor was measured returning 0 on the rows sp_addsubscription seeds
   when it registers an agent. An undocumented code therefore leaves
   runstatus_desc NULL rather than inventing a word for it, and the number is
   still in the archive for whoever meets it next. */
SELECT h.[leg], h.[agent_id], h.[runstatus],
       CASE h.[runstatus] WHEN 1 THEN 'start' WHEN 2 THEN 'succeed'
                          WHEN 3 THEN 'in progress' WHEN 4 THEN 'idle'
                          WHEN 5 THEN 'retry' WHEN 6 THEN 'fail' END AS [runstatus_desc],
       h.[last_time], h.[last_duration], h.[last_latency_ms],
       h.[max_latency_ms], h.[median_latency_ms], h.[sessions],
       h.[delivered_commands], h.[last_comment]
FROM @hist AS h ORDER BY h.[leg], h.[agent_id]
OPTION (RECOMPILE, MAXDOP 1);

SELECT e.[id], e.[time], e.[error_code], e.[error_text], e.[source_type_id]
FROM @errs AS e ORDER BY e.[time] DESC
OPTION (RECOMPILE, MAXDOP 1);
