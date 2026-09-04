-- @scope:       database
-- @resultsets:  root:object, configuration:array, publications:array, articles:array, agents:array, latency:array, repl_errors:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     120
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
-- SQL Server 2012 is the floor.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @window_days int = 7;

DECLARE @applies bit = 0,
        @err_cfg int = 0, @err_topo int = 0, @err_agents int = 0,
        @err_hist int = 0, @err_errs int = 0, @err_size int = 0,
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
                       [local_job] bit NULL);

DECLARE @hist TABLE ([leg] varchar(40), [agent_id] int, [runstatus] int,
                     [last_time] datetime NULL, [last_duration] int NULL,
                     [last_latency_ms] int NULL, [max_latency_ms] int NULL,
                     [median_latency_ms] float NULL, [sessions] int,
                     [delivered_commands] bigint NULL,
                     [last_comment] nvarchar(512) NULL);

DECLARE @errs TABLE ([id] int, [time] datetime, [error_code] sysname NULL,
                     [error_text] nvarchar(512) NULL, [source_type_id] int NULL);

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
                             [subscriber_db], [job_id], [local_job])
        EXEC sys.sp_executesql N'
            SELECT ''distribution'', a.id, a.name, a.publisher_db, a.publication,
                   a.subscriber_db, a.job_id, a.local_job
            FROM dbo.MSdistribution_agents AS a
            OPTION (RECOMPILE, MAXDOP 1)';

        INSERT INTO @agents ([kind], [id], [name], [publisher_db], [publication],
                             [subscriber_db], [job_id], [local_job])
        EXEC sys.sp_executesql N'
            SELECT ''logreader'', a.id, a.name, a.publisher_db, a.publication,
                   NULL, a.job_id, a.local_job
            FROM dbo.MSlogreader_agents AS a
            OPTION (RECOMPILE, MAXDOP 1)';

        INSERT INTO @agents ([kind], [id], [name], [publisher_db], [publication],
                             [subscriber_db], [job_id], [local_job])
        EXEC sys.sp_executesql N'
            SELECT ''snapshot'', a.id, a.name, a.publisher_db, a.publication,
                   NULL, a.job_id, a.local_job
            FROM dbo.MSsnapshot_agents AS a
            OPTION (RECOMPILE, MAXDOP 1)';

        INSERT INTO @agents ([kind], [id], [name], [publisher_db], [publication],
                             [subscriber_db], [job_id], [local_job])
        EXEC sys.sp_executesql N'
            SELECT ''merge'', a.id, a.name, a.publisher_db, a.publication,
                   a.subscriber_db, a.job_id, a.local_job
            FROM dbo.MSmerge_agents AS a
            OPTION (RECOMPILE, MAXDOP 1)';
    END TRY
    BEGIN CATCH
        SELECT @err_agents = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
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
    BEGIN TRY
        INSERT INTO @errs
        EXEC sys.sp_executesql N'
            SELECT TOP (50) e.id, e.[time], e.error_code, LEFT(CONVERT(nvarchar(4000), e.error_text), 512),
                   e.source_type_id
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
       NULLIF(@msg, N'')                            AS [errors.last_message],
       (SELECT COUNT(*) FROM @pubs)                 AS [counts.publications],
       (SELECT COUNT(*) FROM @arts)                 AS [counts.articles],
       (SELECT COUNT(*) FROM @agents)               AS [counts.agents],
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
SELECT a.[kind], a.[id], a.[name], a.[publisher_db], a.[publication],
       a.[subscriber_db],
       CONVERT(char(36), CONVERT(uniqueidentifier, a.[job_id])) AS [job_id],
       CONVERT(int, a.[local_job]) AS [local_job]
FROM @agents AS a ORDER BY a.[kind], a.[name]
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
