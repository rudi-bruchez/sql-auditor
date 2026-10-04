-- @scope:       database
-- @resultsets:  root:object, command_types:array, commands:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     120
-- @widened:     system_databases
--
-- The history of Ola Hallengren's maintenance solution, read from its
-- dbo.CommandLog table in the database where it was installed: what each kind
-- of command cost over the last 30 days, and the index and statistics commands
-- that never ended, failed, or ran longest.
--
-- Runs once per user database, and on master and msdb, with the connection
-- context switched to it.
--
-- WHY THIS COLLECTOR EXISTS. IndexOptimize, DatabaseIntegrityCheck and
-- DatabaseBackup log one row per command when they run with @LogToTable = 'Y',
-- which is the default: the start, the end, the error number, and for an
-- index the page count and fragmentation it was chosen on. That is the only
-- record of how long the nightly maintenance of one given index takes, and of
-- an ALTER INDEX that was cut off. An ALTER_INDEX row with no EndTime is a
-- command interrupted by a killed job, a failover or a restart, or one still
-- running; a REORGANIZE that runs for an hour on a large index is the window
-- in which an asynchronous statistics update waits for its Sch-M lock and
-- every compilation needing that statistic queues behind it. Until this file
-- the archive could name the job (50.agent/020) and not one duration of what
-- it ran.
--
-- THE TABLE IS LOOKED FOR IN EVERY DATABASE, NOT ASSUMED IN ONE. The solution
-- creates its objects wherever the installer chose: master by default, often a
-- utility database the DBA named. Every database collected asks
-- OBJECT_ID(N'dbo.CommandLog', N'U'), a catalog lookup that reads no data, and
-- a database without the table returns its root with present 0 and nothing
-- else. master is the default and msdb is possible, and the per-database
-- runner opens neither for an ordinary collector, hence @widened:
-- system_databases. DB_INCLUDE still narrows the user databases: a run cadenced
-- on an application database does not open a utility database it did not
-- name, and the log there is then not read.
--
-- WHY NOT ONE INSTANCE FILE LOOPING OVER sys.databases. It was written first,
-- and the corpus lint refused it: reading a table of another database needs
-- its name in the statement, so either EXEC of a procedure whose name is in a
-- variable (<db>.sys.sp_executesql) or dynamic SQL assembled by concatenation,
-- and the lint admits neither, since a statement it cannot read whole is one
-- it cannot vouch for. The runner's own context switch is the reviewed way to
-- reach every database.
--
-- A TABLE OF THAT NAME IS READ ONLY IF IT HAS THE SOLUTION'S COLUMNS. The 15
-- columns this file reads are counted in sys.columns first; missing_columns
-- says how many are absent, and a table missing any is reported and not read.
-- The reads are dynamic so that such a table never has to compile.
--
-- THE READ IS BOUNDED BY ID, BECAUSE NOTHING IS INDEXED ON TIME. Ola's table
-- has one index, the clustered primary key on ID, and a filter on StartTime
-- scans the whole table. Measured on SQL Server 2025 against the published
-- table definition filled with one million synthetic rows (470 MB), a COUNT
-- over the last 30 days by StartTime read 61 041 pages; the same 74 000 rows
-- taken by an ID range read 4 459. The file finds the first ID of the window
-- by a binary search over the clustered key, each step one seek (20 for a
-- million rows, log.seeks), then reads the window by ID range, newest first,
-- up to 100 000 rows (caps.rows_reached). The search relies on StartTime rising
-- with ID, which holds because CommandExecute inserts each row as its command
-- starts; rows inserted out of order by hand, or a clock set back, can move
-- the boundary by those rows. On that table the whole file read 36 029 pages,
-- the window and the work tables included, and ran in 1.2 s, of which 0.15 to
-- 0.2 s in the log itself (log.read_ms). The solution's CommandLog Cleanup job
-- deletes rows older than 30 days by default, so the window usually covers
-- what is there; log.oldest_start says how far back the table goes.
--
-- WHAT IS PROJECTED. command_types totals every command of the window by the
-- database it acted on and its CommandType: count, without end, failed, total,
-- average and longest duration. commands lists ALTER_INDEX and
-- UPDATE_STATISTICS rows only: the 100 newest with no EndTime, the 100 newest
-- with a non-zero ErrorNumber, and the 25 longest of each type, each row
-- flagged for every reason it is listed. For each, the names of the database,
-- schema, table, index or statistic and the partition; the operation
-- (REORGANIZE or REBUILD for an index, FULLSCAN, SAMPLE, RESAMPLE or default
-- for a statistic) and whether a rebuild was ONLINE, read out of the command
-- text; the page count, fragmentation, row count and modification counter that
-- IndexOptimize recorded in ExtendedInfo; later_commands_same_database, the
-- commands logged after it for the same database, which tells a command cut
-- off and followed by the rest of the run from the last one, perhaps still
-- running; and the other completed runs of the same command on the same
-- target in the window, so that a duration reads against its own history
-- rather than against a threshold. Times are the server's local clock, as
-- CommandExecute takes them with SYSDATETIME().
--
-- WHAT STAYS ON THE SERVER, AND WHAT LEAVES. The Command text, ErrorMessage
-- and the ExtendedInfo document are not projected: the first two can be long
-- and carry paths and literals, and only four numbers of the third are read.
-- What leaves is object names, the class 70.schema already collects, with one
-- difference to know before sending the archive on: the log names every
-- database the solution maintained, so database and object names come back
-- for databases that DB_INCLUDE did not select or DB_EXCLUDE removed, as
-- database names do in 60.backup/010.history.
--
-- PERMISSIONS. Finding the table needs metadata visibility only. Reading it
-- needs SELECT on dbo.CommandLog, which no grant of the corpus gives since the
-- database is not known in advance. A refusal is caught and recorded: measured
-- with a login holding VIEW ANY DEFINITION and a user in the database, the
-- root said present 1, readable 0 and error 229, and the file completed.
--
-- The XML methods require QUOTED_IDENTIFIER ON, the TDS default this corpus
-- relies on, as in 50.agent/040.maintenance-plans. Measured from a client that
-- turns it off: the log is read, the operation and the four numbers stay NULL
-- on every listed row, and error_number says 1934.
--
-- NO JUDGEMENT IS APPLIED. Whether a duration is abnormal depends on the index,
-- and same_target says what that index usually takes.
--
-- SQL Server 2012 is the floor: TRY_CONVERT and the ROWS frame of a windowed
-- COUNT are the newest constructs used.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @window_days      int = 30,
        @row_cap          int = 100000,
        @longest_per_type int = 25,
        @listing_cap      int = 100;
DECLARE @started datetime2(7) = SYSDATETIME();
DECLARE @now     datetime2(7) = @started;
DECLARE @since   datetime2(7) = DATEADD(DAY, -@window_days, @now);

DECLARE @present  bit = CASE WHEN OBJECT_ID(N'dbo.CommandLog', N'U') IS NULL THEN 0 ELSE 1 END,
        @missing  int,
        @readable bit,
        @err      int,
        @msg      nvarchar(2048),
        @min_id   bigint,
        @max_id   bigint,
        @oldest   datetime2(7),
        @newest   datetime2(7),
        @first    bigint,
        @seeks    int = 0,
        @rows     int = 0,
        @read_ms  int,
        @selected int,
        @t0       datetime2(7);

IF @present = 1
    SELECT @missing = 15 - COUNT(*)
    FROM sys.columns
    WHERE object_id = OBJECT_ID(N'dbo.CommandLog')
      AND name IN (N'ID', N'DatabaseName', N'SchemaName', N'ObjectName', N'ObjectType',
                   N'IndexName', N'IndexType', N'StatisticsName', N'PartitionNumber',
                   N'ExtendedInfo', N'Command', N'CommandType', N'StartTime', N'EndTime',
                   N'ErrorNumber')
    OPTION (RECOMPILE, MAXDOP 1);

/* The commands of the window, without their text. */
CREATE TABLE #w (
    id               bigint        NOT NULL,
    database_name    sysname       NULL,
    schema_name      sysname       NULL,
    object_name      sysname       NULL,
    index_name       sysname       NULL,
    statistics_name  sysname       NULL,
    partition_number int           NULL,
    command_type     nvarchar(60)  NOT NULL,
    start_time       datetime2(7)  NOT NULL,
    end_time         datetime2(7)  NULL,
    error_number     int           NULL,
    -- Descending, the order the window is read in: ascending, every row went
    -- in at the front of the index and the insert cost more than the read.
    PRIMARY KEY (id DESC));

IF @missing = 0
BEGIN
    SET @t0 = SYSDATETIME();
    /* Dynamic so that a table of this name without these columns, which
       @missing has already turned away, never has to compile here. */
    BEGIN TRY
        EXEC sys.sp_executesql
            N'DECLARE @lo bigint, @hi bigint, @mid bigint, @t datetime2(7);
              SELECT @min_id = MIN(ID), @max_id = MAX(ID) FROM dbo.CommandLog OPTION (RECOMPILE, MAXDOP 1);
              SELECT @oldest = StartTime FROM dbo.CommandLog WHERE ID = @min_id OPTION (RECOMPILE, MAXDOP 1);
              SELECT @newest = StartTime FROM dbo.CommandLog WHERE ID = @max_id OPTION (RECOMPILE, MAXDOP 1);
              IF @newest >= @since
              BEGIN
                  SELECT @lo = @min_id, @hi = @max_id;
                  WHILE @lo < @hi
                  BEGIN
                      SET @mid = @lo + (@hi - @lo) / 2;
                      SELECT TOP (1) @t = StartTime
                      FROM dbo.CommandLog WHERE ID >= @mid ORDER BY ID OPTION (RECOMPILE, MAXDOP 1);
                      SET @seeks = @seeks + 1;
                      IF @t >= @since SET @hi = @mid; ELSE SET @lo = @mid + 1;
                  END;
                  SET @first = @lo;
                  INSERT INTO #w (id, database_name, schema_name, object_name, index_name,
                                  statistics_name, partition_number, command_type,
                                  start_time, end_time, error_number)
                  SELECT TOP (@row_cap) c.ID, c.DatabaseName, c.SchemaName, c.ObjectName,
                         c.IndexName, c.StatisticsName, c.PartitionNumber, c.CommandType,
                         c.StartTime, c.EndTime, c.ErrorNumber
                  FROM dbo.CommandLog AS c
                  WHERE c.ID >= @first AND c.StartTime >= @since
                  ORDER BY c.ID DESC
                  OPTION (RECOMPILE, MAXDOP 1);
                  SET @rows = @@ROWCOUNT;
              END;',
            N'@since datetime2(7), @row_cap int, @min_id bigint OUTPUT, @max_id bigint OUTPUT,
              @oldest datetime2(7) OUTPUT, @newest datetime2(7) OUTPUT, @first bigint OUTPUT,
              @seeks int OUTPUT, @rows int OUTPUT',
            @since = @since, @row_cap = @row_cap, @min_id = @min_id OUTPUT,
            @max_id = @max_id OUTPUT, @oldest = @oldest OUTPUT, @newest = @newest OUTPUT,
            @first = @first OUTPUT, @seeks = @seeks OUTPUT, @rows = @rows OUTPUT;
        SET @readable = 1;
    END TRY
    BEGIN CATCH
        DELETE FROM #w OPTION (RECOMPILE, MAXDOP 1);
        SELECT @readable = 0, @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE(),
               @rows = 0, @first = NULL;
    END CATCH;
    SET @read_ms = DATEDIFF(MILLISECOND, @t0, SYSDATETIME());
END;

/* The commands listed: index and statistics maintenance that has no end time,
   that failed, or that is among the longest of its type in the window. */
CREATE TABLE #sel (
    id                    bigint         NOT NULL PRIMARY KEY,
    is_open               bit            NOT NULL,
    is_failed             bit            NOT NULL,
    is_longest            bit            NOT NULL,
    operation             varchar(20)    NULL,
    is_online             bit            NULL,
    page_count            bigint         NULL,
    fragmentation_pct     decimal(9,2)   NULL,
    row_count             bigint         NULL,
    modification_counter  bigint         NULL);

INSERT INTO #sel (id, is_open, is_failed, is_longest)
SELECT x.id,
       MAX(CASE WHEN x.reason = 'open'    THEN 1 ELSE 0 END),
       MAX(CASE WHEN x.reason = 'failed'  THEN 1 ELSE 0 END),
       MAX(CASE WHEN x.reason = 'longest' THEN 1 ELSE 0 END)
FROM (
    SELECT o.id, 'open' AS reason
    FROM (SELECT TOP (@listing_cap) w.id
          FROM #w AS w
          WHERE w.command_type IN (N'ALTER_INDEX', N'UPDATE_STATISTICS')
            AND w.end_time IS NULL
          ORDER BY w.start_time DESC) AS o
    UNION ALL
    SELECT f.id, 'failed'
    FROM (SELECT TOP (@listing_cap) w.id
          FROM #w AS w
          WHERE w.command_type IN (N'ALTER_INDEX', N'UPDATE_STATISTICS')
            AND w.error_number <> 0
          ORDER BY w.start_time DESC) AS f
    UNION ALL
    SELECT r.id, 'longest'
    FROM (SELECT w.id,
                 ROW_NUMBER() OVER (PARTITION BY w.command_type
                                    ORDER BY DATEDIFF(SECOND, w.start_time, w.end_time) DESC,
                                             w.start_time DESC) AS rn
          FROM #w AS w
          WHERE w.command_type IN (N'ALTER_INDEX', N'UPDATE_STATISTICS')
            AND w.end_time IS NOT NULL) AS r
    WHERE r.rn <= @longest_per_type
) AS x
GROUP BY x.id
OPTION (RECOMPILE, MAXDOP 1);
SET @selected = @@ROWCOUNT;

/* Back to the log for the listed rows only, by clustered seek: what each
   command did and the numbers IndexOptimize recorded, read out of Command and
   ExtendedInfo, neither of which leaves the server. Index and statistic names
   are bracketed in the command, so the keyword is looked for after a closing
   bracket and an index named REBUILD_x is not taken for a rebuild. The test is
   on the count of the INSERT above, because IF EXISTS cannot carry the query
   hint. */
IF @selected > 0
BEGIN TRY
    EXEC sys.sp_executesql
        N'UPDATE #sel
          SET operation = CASE
                  WHEN c.CommandType = N''ALTER_INDEX'' THEN
                      CASE WHEN c.Command LIKE N''%] REORGANIZE%'' THEN ''REORGANIZE''
                           WHEN c.Command LIKE N''%] REBUILD%''    THEN ''REBUILD''
                           ELSE ''other'' END
                  ELSE
                      CASE WHEN c.Command LIKE N''%FULLSCAN%''  THEN ''FULLSCAN''
                           WHEN c.Command LIKE N''%RESAMPLE%''  THEN ''RESAMPLE''
                           WHEN c.Command LIKE N''%SAMPLE %''   THEN ''SAMPLE''
                           ELSE ''default'' END
              END,
              is_online = CASE WHEN c.CommandType = N''ALTER_INDEX''
                               THEN CASE WHEN c.Command LIKE N''%ONLINE = ON%'' THEN 1 ELSE 0 END
                          END,
              page_count = TRY_CONVERT(bigint,
                  c.ExtendedInfo.value(''(/ExtendedInfo/PageCount)[1]'', ''nvarchar(40)'')),
              fragmentation_pct = TRY_CONVERT(decimal(9,2), TRY_CONVERT(float,
                  c.ExtendedInfo.value(''(/ExtendedInfo/Fragmentation)[1]'', ''nvarchar(40)''))),
              row_count = TRY_CONVERT(bigint,
                  c.ExtendedInfo.value(''(/ExtendedInfo/RowCount)[1]'', ''nvarchar(40)'')),
              modification_counter = TRY_CONVERT(bigint,
                  c.ExtendedInfo.value(''(/ExtendedInfo/ModificationCounter)[1]'', ''nvarchar(40)''))
          FROM #sel
          JOIN dbo.CommandLog AS c ON c.ID = #sel.id
          OPTION (RECOMPILE, MAXDOP 1);';
END TRY
BEGIN CATCH
    SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH;

/* The other completed runs of each listed command's target, in one pass over
   the window: how many, their total, and the two longest, so that the longest
   run of a target can be compared with the longest of the others. A lookup
   per listed row was measured at ten times the reads. */
CREATE TABLE #tg (
    command_type     nvarchar(60)  NOT NULL,
    database_name    sysname       NULL,
    schema_name      sysname       NULL,
    object_name      sysname       NULL,
    index_name       sysname       NULL,
    statistics_name  sysname       NULL,
    partition_number int           NULL,
    runs             int           NOT NULL,
    total_s          bigint        NOT NULL,
    max1_s           int           NULL,
    max1_id          bigint        NULL,
    max2_s           int           NULL);

WITH targets AS (
    SELECT DISTINCT w.command_type, w.database_name, w.schema_name, w.object_name,
           w.index_name, w.statistics_name, w.partition_number
    FROM #sel AS s
    JOIN #w AS w ON w.id = s.id
),
done AS (
    SELECT o.id, o.command_type, o.database_name, o.schema_name, o.object_name,
           o.index_name, o.statistics_name, o.partition_number,
           DATEDIFF(SECOND, o.start_time, o.end_time) AS dur,
           ROW_NUMBER() OVER (PARTITION BY o.command_type, o.database_name, o.schema_name,
                                           o.object_name, o.index_name, o.statistics_name,
                                           o.partition_number
                              ORDER BY DATEDIFF(SECOND, o.start_time, o.end_time) DESC, o.id) AS rn
    FROM #w AS o
    JOIN targets AS t
      ON  t.command_type = o.command_type
      AND ISNULL(t.database_name, N'') = ISNULL(o.database_name, N'')
      AND ISNULL(t.schema_name, N'') = ISNULL(o.schema_name, N'')
      AND ISNULL(t.object_name, N'') = ISNULL(o.object_name, N'')
      AND ISNULL(t.index_name, N'') = ISNULL(o.index_name, N'')
      AND ISNULL(t.statistics_name, N'') = ISNULL(o.statistics_name, N'')
      AND ISNULL(t.partition_number, 0) = ISNULL(o.partition_number, 0)
    WHERE o.end_time IS NOT NULL
)
INSERT INTO #tg (command_type, database_name, schema_name, object_name, index_name,
                 statistics_name, partition_number, runs, total_s, max1_s, max1_id, max2_s)
SELECT d.command_type, d.database_name, d.schema_name, d.object_name, d.index_name,
       d.statistics_name, d.partition_number, COUNT(*), SUM(CAST(d.dur AS bigint)),
       MAX(CASE WHEN d.rn = 1 THEN d.dur END), MAX(CASE WHEN d.rn = 1 THEN d.id END),
       MAX(CASE WHEN d.rn = 2 THEN d.dur END)
FROM done AS d
GROUP BY d.command_type, d.database_name, d.schema_name, d.object_name, d.index_name,
         d.statistics_name, d.partition_number
OPTION (RECOMPILE, MAXDOP 1);

SELECT
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    @present                                                    AS [present],
    @missing                                                    AS [missing_columns],
    @readable                                                   AS [readable],
    @err                                                        AS [error_number],
    @msg                                                        AS [error_message],
    CONVERT(varchar(19), @since, 126)                           AS [window.start],
    CONVERT(varchar(19), @now, 126)                             AS [window.end],
    @window_days                                                AS [window.days],
    @min_id                                                     AS [log.min_id],
    @max_id                                                     AS [log.max_id],
    CONVERT(varchar(19), @oldest, 126)                          AS [log.oldest_start],
    CONVERT(varchar(19), @newest, 126)                          AS [log.newest_start],
    @first                                                      AS [log.window_first_id],
    @max_id - @first + 1                                        AS [log.window_id_span],
    @seeks                                                      AS [log.seeks],
    @rows                                                       AS [log.rows_read],
    @read_ms                                                    AS [log.read_ms],
    @row_cap                                                    AS [caps.rows],
    CAST(CASE WHEN @rows >= @row_cap THEN 1 ELSE 0 END AS bit)  AS [caps.rows_reached],
    @longest_per_type                                           AS [caps.longest_per_type],
    @listing_cap                                                AS [caps.open_or_failed],
    (SELECT COUNT(*) FROM #w
      WHERE command_type = N'ALTER_INDEX' AND end_time IS NULL) AS [counts.alter_index_without_end],
    (SELECT COUNT(*) FROM #w
      WHERE command_type = N'UPDATE_STATISTICS' AND end_time IS NULL)
                                                                AS [counts.update_statistics_without_end],
    (SELECT COUNT(*) FROM #w
      WHERE command_type IN (N'ALTER_INDEX', N'UPDATE_STATISTICS')
        AND error_number <> 0)                                  AS [counts.index_and_statistics_failed],
    DATEDIFF(MILLISECOND, @started, SYSDATETIME())              AS [duration_ms]
OPTION (RECOMPILE, MAXDOP 1);

SELECT
    w.database_name                                             AS [database],
    w.command_type                                              AS [command_type],
    COUNT(*)                                                    AS [commands],
    SUM(CASE WHEN w.end_time IS NULL THEN 1 ELSE 0 END)         AS [without_end],
    SUM(CASE WHEN w.error_number <> 0 THEN 1 ELSE 0 END)        AS [failed],
    SUM(CAST(DATEDIFF(SECOND, w.start_time, w.end_time) AS bigint))
                                                                AS [total_s],
    CAST(AVG(CAST(DATEDIFF(SECOND, w.start_time, w.end_time) AS decimal(18,1)))
         AS decimal(18,1))                                      AS [avg_s],
    MAX(DATEDIFF(SECOND, w.start_time, w.end_time))             AS [max_s],
    CONVERT(varchar(19), MIN(w.start_time), 126)                AS [first_start],
    CONVERT(varchar(19), MAX(w.start_time), 126)                AS [last_start]
FROM #w AS w
GROUP BY w.database_name, w.command_type
ORDER BY w.database_name, w.command_type
OPTION (RECOMPILE, MAXDOP 1);

SELECT
    w.id                                                        AS [id],
    w.database_name                                             AS [database],
    w.schema_name                                               AS [schema],
    w.object_name                                               AS [object],
    w.index_name                                                AS [index],
    w.statistics_name                                           AS [statistics],
    w.partition_number                                          AS [partition_number],
    w.command_type                                              AS [command_type],
    s.operation                                                 AS [operation],
    s.is_online                                                 AS [is_online],
    s.is_open                                                   AS [is_open],
    s.is_failed                                                 AS [is_failed],
    s.is_longest                                                AS [is_longest],
    CONVERT(varchar(23), w.start_time, 126)                     AS [start_time],
    CONVERT(varchar(23), w.end_time, 126)                       AS [end_time],
    DATEDIFF(SECOND, w.start_time, w.end_time)                  AS [duration_s],
    CASE WHEN w.end_time IS NULL
         THEN DATEDIFF(MINUTE, w.start_time, @now) END          AS [minutes_since_start],
    w.error_number                                              AS [error_number],
    s.page_count                                                AS [page_count],
    s.fragmentation_pct                                         AS [fragmentation_pct],
    s.row_count                                                 AS [row_count],
    s.modification_counter                                      AS [modification_counter],
    lat.later                                                   AS [later_commands_same_database],
    -- The same command on the same target, this row left out.
    tg.runs - CASE WHEN w.end_time IS NULL THEN 0 ELSE 1 END    AS [same_target.other_runs],
    CAST((tg.total_s - ISNULL(DATEDIFF(SECOND, w.start_time, w.end_time), 0)) * 1.0
         / NULLIF(tg.runs - CASE WHEN w.end_time IS NULL THEN 0 ELSE 1 END, 0)
         AS decimal(18,1))                                      AS [same_target.other_avg_s],
    CASE WHEN tg.max1_id = w.id THEN tg.max2_s ELSE tg.max1_s END
                                                                AS [same_target.other_max_s]
FROM #sel AS s
JOIN #w AS w ON w.id = s.id
JOIN (SELECT x.id,
             COUNT(*) OVER (PARTITION BY x.database_name ORDER BY x.id DESC
                            ROWS UNBOUNDED PRECEDING) - 1 AS later
      FROM #w AS x) AS lat ON lat.id = w.id
LEFT JOIN #tg AS tg
  ON  tg.command_type = w.command_type
  AND ISNULL(tg.database_name, N'') = ISNULL(w.database_name, N'')
  AND ISNULL(tg.schema_name, N'') = ISNULL(w.schema_name, N'')
  AND ISNULL(tg.object_name, N'') = ISNULL(w.object_name, N'')
  AND ISNULL(tg.index_name, N'') = ISNULL(w.index_name, N'')
  AND ISNULL(tg.statistics_name, N'') = ISNULL(w.statistics_name, N'')
  AND ISNULL(tg.partition_number, 0) = ISNULL(w.partition_number, 0)
ORDER BY w.command_type, s.is_open DESC,
         DATEDIFF(SECOND, w.start_time, w.end_time) DESC, w.start_time DESC
OPTION (RECOMPILE, MAXDOP 1);
