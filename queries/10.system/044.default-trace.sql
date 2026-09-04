-- @scope:       instance
-- @resultsets:  root:object, events:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     300
--
-- What happened to this instance, from the default trace — aggregated.
--
-- The default trace runs on every instance audited so far. It is on by
-- default, and nothing in this corpus read it. It is the only free record of
-- what was done to the server: configuration changes, database creation and
-- deletion, file autogrowth and autoshrink, object DDL, changes to server role
-- membership, login failures, and backup errors. It keeps five rolling 20 MB
-- files, which on a quiet instance is weeks and on a busy one is days.
--
-- The audit that raised this found a configuration option with a pending
-- reconfigure — someone had changed a setting and never applied it. The report
-- could say what the state was and had to write that it would be better to
-- look at what happened before settling it, which is an instruction to the
-- client to go and find out. The trace had the answer, with a timestamp.
--
-- THE PERMISSION IS THE WHOLE PROBLEM, AND NOTHING IS ASKED FOR. sys.traces
-- and sys.fn_trace_gettable need ALTER TRACE, not VIEW SERVER STATE. Measured
-- with a login holding exactly CONNECT and VIEW SERVER STATE: Msg 8189, "You
-- do not have permission to run SYS.TRACES", and the same for
-- FN_TRACE_GETTABLE even when the path is passed as a literal, bypassing
-- sys.traces entirely. Granting ALTER TRACE alone makes both work — and ALTER
-- TRACE allows creating, modifying and stopping traces. It is the Profiler
-- permission, and it is exactly the class this practice refuses to ask a
-- client for.
--
-- DECLARING IT WOULD HAVE BEEN WORSE THAN NOT COLLECTING AT ALL. The declared
-- permission drives the skip gate: declare something the login holds and the
-- file runs and fails, landing in Errors on every ordinary audit run instead
-- of in "queries not run". So this file declares what it really needs to be
-- offered a connection, attempts the read, and records the refusal. Where a
-- DBA runs the tool themselves, or a client grants it deliberately, the
-- archive gets the trace; otherwise it gets a row saying why not.
--
-- SYS.TRACES IS READ THROUGH DYNAMIC SQL INTO A TABLE VARIABLE, AND THAT IS
-- NOT DECORATION. Measured: under a login without ALTER TRACE, a plain SELECT
-- against sys.traces inside a TRY emits the column metadata as an EMPTY RESULT
-- SET before raising Msg 8189. The CATCH then fires, the handler's own set
-- follows, and the unit returns one result set more than it declared.
-- sys.traces is a table-valued function and the engine sends its shape before
-- it evaluates the permission. Staging it the way every other guard in this
-- corpus works suppresses the phantom set.
--
-- THE EVENT CLASS LIST IS MEASURED AGAINST sys.trace_events, because a list
-- written from memory was wrong. There is NO sp_configure event class: a
-- configuration change arrives as a class 22 ErrorLog row whose text reads
-- "Configuration option '...' changed from 0 to 1. Run the RECONFIGURE
-- statement to install." — read live on the test instance. Class 152, which an
-- earlier draft named for configuration, is Audit Change Database Owner, and a
-- collector built from that list would have labelled database-owner changes as
-- configuration changes in an archive a client acts on.
--
-- THE AUTOGROW DURATION IS PART OF THE AGGREGATE AND NOT AN EXTRA. A count of
-- growth events without their duration throws the finding away: log autogrowth
-- cannot use instant file initialisation, so it is unbuffered, and a single
-- 40-second growth stalls every writer on the database. "The log grew 180
-- times" is a curiosity; "the log grew 180 times and the slowest took 41
-- seconds" is the report.
--
-- NO TEXT IS COLLECTED HERE. This file is the aggregate: one row per class and
-- object type, with counts and a window. The retained rows themselves are a
-- separate, flag-gated collector, because a directive gates a whole file and
-- an aggregate that only runs when a flag is set would be an aggregate nobody
-- gets.
--
-- Three cautions the reader needs:
--
--   THE PATH MAY NOT EXIST. With the default trace disabled, sys.traces has no
--   row with is_default = 1, and fn_trace_gettable(NULL, DEFAULT) raises Msg
--   19050 rather than returning nothing. The path is tested before it is used.
--
--   DEFAULT READS THE ROLLOVER SET — verified, 264 rows across files — but
--   sys.traces.path names the CURRENT file, so the set read is the one from
--   that file forward. The span reported is the span actually read, not the
--   span the five files hold.
--
--   ABSENCE PROVES NOTHING. The files rolled. An empty autogrow count must not
--   be read as "the files never grew".

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @enabled int = 0, @path nvarchar(260) = NULL,
        @err int = 0, @msg nvarchar(2048) = N'';

SELECT @enabled = CONVERT(int, c.value_in_use)
FROM sys.configurations AS c
WHERE c.name = 'default trace enabled'
OPTION (RECOMPILE, MAXDOP 1);

DECLARE @traces TABLE ([id] int, [path] nvarchar(260) NULL,
                       [status] int NULL, [is_default] bit NULL);

DECLARE @events TABLE ([EventClass] int, [ObjectType] int NULL,
                       [Duration] bigint NULL, [IntegerData] int NULL,
                       [StartTime] datetime NULL);

BEGIN TRY
    INSERT INTO @traces ([id], [path], [status], [is_default])
    EXEC sys.sp_executesql
        N'SELECT t.id, t.path, t.status, t.is_default
          FROM sys.traces AS t OPTION (RECOMPILE, MAXDOP 1)';
END TRY
BEGIN CATCH
    SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT @path = MAX(t.[path]) FROM @traces AS t WHERE t.[is_default] = 1
OPTION (RECOMPILE, MAXDOP 1);

/* The statement is a literal and the path is a parameter, which is what keeps
   this readable to the corpus's own statement lint: dynamic SQL assembled from
   variables is a statement nothing can vouch for. */
IF @path IS NOT NULL AND @err = 0
BEGIN
    BEGIN TRY
        INSERT INTO @events ([EventClass], [ObjectType], [Duration],
                             [IntegerData], [StartTime])
        EXEC sys.sp_executesql
            N'SELECT g.EventClass, g.ObjectType, g.Duration, g.IntegerData,
                     g.StartTime
              FROM sys.fn_trace_gettable(@p, DEFAULT) AS g
              WHERE g.EventClass IN (20, 22, 46, 47, 92, 93, 94, 95,
                                     104, 105, 108, 115, 116, 164)
              OPTION (RECOMPILE, MAXDOP 1)',
            N'@p nvarchar(260)', @p = @path;
    END TRY
    BEGIN CATCH
        SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH
END

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                 AS [collected_at],
       @enabled                                                 AS [default_trace_enabled],
       CASE WHEN @err = 0 AND @path IS NOT NULL THEN 1 ELSE 0 END
                                                                AS [collected],
       @err                                                     AS [error_number],
       NULLIF(@msg, N'')                                        AS [error_message],
       @path                                                    AS [current_file],
       (SELECT COUNT(*) FROM @traces)                           AS [counts.traces],
       (SELECT COUNT(*) FROM @events)                           AS [counts.events],
       /* The window is the finding as often as the content is. A trace whose
          oldest record is four hours old, on a server up for eighty days, says
          the instance rolls 100 MB in an afternoon. */
       (SELECT MIN(e.[StartTime]) FROM @events AS e)            AS [window.oldest],
       (SELECT MAX(e.[StartTime]) FROM @events AS e)            AS [window.newest],
       DATEDIFF(second, (SELECT MIN(e.[StartTime]) FROM @events AS e),
                        (SELECT MAX(e.[StartTime]) FROM @events AS e))
                                                                AS [window.span_seconds]
OPTION (RECOMPILE, MAXDOP 1);

SELECT e.[EventClass]                                           AS [event_class],
       CASE e.[EventClass]
            WHEN 20  THEN 'Audit Login Failed'
            WHEN 22  THEN 'ErrorLog'
            WHEN 46  THEN 'Object:Created'
            WHEN 47  THEN 'Object:Deleted'
            WHEN 92  THEN 'Data File Auto Grow'
            WHEN 93  THEN 'Log File Auto Grow'
            WHEN 94  THEN 'Data File Auto Shrink'
            WHEN 95  THEN 'Log File Auto Shrink'
            WHEN 104 THEN 'Audit Addlogin'
            WHEN 105 THEN 'Audit Login GDR'
            WHEN 108 THEN 'Audit Add Login to Server Role'
            WHEN 115 THEN 'Backup/Restore'
            WHEN 116 THEN 'Audit DBCC'
            WHEN 164 THEN 'Object:Altered'
       END                                                      AS [event_class_desc],
       e.[ObjectType]                                           AS [object_type],
       COUNT(*)                                                 AS [occurrences],
       MIN(e.[StartTime])                                       AS [first_seen],
       MAX(e.[StartTime])                                       AS [last_seen],
       /* Carried for every class, and load-bearing for 92 and 93: Duration is
          in microseconds and IntegerData is the growth in 8 KB pages. */
       SUM(e.[Duration])                                        AS [total_duration_us],
       MAX(e.[Duration])                                        AS [max_duration_us],
       SUM(CAST(e.[IntegerData] AS BIGINT))                     AS [total_pages],
       MAX(e.[IntegerData])                                     AS [max_pages]
FROM @events AS e
GROUP BY e.[EventClass], e.[ObjectType]
ORDER BY COUNT(*) DESC
OPTION (RECOMPILE, MAXDOP 1);
