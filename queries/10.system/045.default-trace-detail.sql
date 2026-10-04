-- @scope:       instance
-- @resultsets:  root:object, events:array, classes:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @requires_flag: default_trace
-- @discloses:   error_log
-- @timeout:     300
--
-- The retained rows of the default trace, for the event classes that carry a
-- decision.
--
-- 044.default-trace.sql is the aggregate and runs on every collection: a count
-- per class with a window costs nothing and discloses nothing. This is the
-- other half — the rows themselves, with the login, host, application and
-- database each event names, and the text of the class 22 messages where a
-- configuration change is recorded.
--
-- A DIRECTIVE GATES A WHOLE FILE, WHICH IS WHY THERE ARE TWO OF THEM. An
-- earlier design had the aggregate and the detail in one collector with only
-- the second half gated, and that is not buildable: skipReason drops the file,
-- not a result set. An aggregate that only ran when a disclosure flag was
-- passed would be an aggregate nobody ever gets.
--
-- THE DISCLOSURE IS THE ERROR LOG'S, REACHED THROUGH A DIFFERENT DOOR, and it
-- is declared with the same value 040.error-log.sql declares rather than a new
-- one. The wording a security officer reads should not depend on which
-- collector happened to find the message: what lands here names logins,
-- databases, file paths and client addresses, which is exactly what that
-- disclosure already says.
--
-- TEXTDATA IS PROJECTED FOR CLASS 22 AND FOR NOTHING ELSE, and the restraint is
-- deliberate. Class 22 is where a configuration change arrives — the row whose
-- text reads "Configuration option '...' changed from 0 to 1. Run the
-- RECONFIGURE statement to install." — and that message is the whole reason
-- section 6 of the collection gaps specification exists. The other classes are
-- described perfectly well by the object they name, and taking their text as
-- well would widen the disclosure to no analytical gain.
--
-- THE PERMISSION POSTURE IS 044'S AND FOR 044'S REASON. sys.traces and
-- sys.fn_trace_gettable need ALTER TRACE, which is the Profiler permission and
-- is not asked for. Declaring it would make the skip gate pass and land the
-- file in Errors on every ordinary run; declaring nothing and trying is what
-- puts the refusal in a row a reader can see. sys.traces is read through
-- dynamic SQL into a table variable because under a login without ALTER TRACE
-- a plain SELECT emits its column metadata as an empty result set BEFORE
-- raising Msg 8189 — the engine sends a table-valued function's shape before it
-- evaluates the permission — and the unit would return one result set more than
-- it declared.
--
-- THE ROWS ARE CAPPED AT 5000, SHARED OUT BY EVENT CLASS. Five rolling 20 MB
-- files can hold far more than an archive should carry, and the aggregate in
-- 044 is what reports the true totals. Until October 2026 the 5000 were simply
-- the most recent, on the assumption that the newest rows were a fair sample of
-- what the events were. They are not: a flood of one class pushes every other
-- class out. Measured on three client instances in September 2026, the cap was
-- reached every time (5000 kept of 30 611, 14 262 and 15 050), the kept window
-- was 10 minutes against the 68 the files held, and on one of them 14 of the 21
-- configuration changes (class 22) and 26 of the 40 DBCC events (class 116) were
-- dropped in favour of class 164, Object:Altered. On the lab the same day, 4999
-- of the 5000 were class 46, and all six configuration changes were lost.
--
-- So the rows are ranked inside their class, newest first, and taken round
-- robin: every class's newest row, then every class's second newest, and so on
-- until the 5000 are spent. A class with fewer rows than its share is kept
-- whole, and the classes that flood share what is left. On the lab that kept
-- every row of every class but 46.
--
-- THE CAP STAYS AT 5000 ON PURPOSE, and it is a disclosure decision as much as
-- a size one. These rows name logins, hosts and applications, and the operator
-- who passes --include-default-trace accepts an archive of that kind and that
-- size. Raising the cap to the 30 000 the largest trace held would multiply the
-- number of named rows by six without anybody deciding it; sharing the same
-- 5000 out differently changes which rows are named, not how many, nor which
-- columns. What it does change, and it is the point: the rare classes it keeps
-- are 20 (failed logins), 104, 105 and 108 (login and role administration) as
-- much as configuration changes, so an archive that used to hold only the
-- flooding Object:* rows now holds those too, within the same 5000.
--
-- No server cost: the ORDER BY StartTime DESC this replaces already read and
-- sorted every row of the files, and the ranking is one more sort of the same
-- rows. The classes result set says, per class, how many rows the files held
-- and how many were kept, and the window of what was kept, so a class cut by
-- the cap is named rather than inferred. capped says whether any class was.
-- ABSENCE STILL PROVES NOTHING, the same caution 044 carries: the files rolled,
-- and an empty result must not be read as "it never happened".

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

DECLARE @events TABLE (
    [StartTime]       datetime      NULL,
    [EventClass]      int           NULL,
    [DatabaseName]    nvarchar(256) NULL,
    [ObjectName]      nvarchar(256) NULL,
    [ObjectType]      int           NULL,
    [LoginName]       nvarchar(256) NULL,
    [ApplicationName] nvarchar(256) NULL,
    [HostName]        nvarchar(256) NULL,
    [Duration]        bigint        NULL,
    [IntegerData]     int           NULL,
    [Severity]        int           NULL,
    [Success]         int           NULL,
    [TextData]        nvarchar(4000) NULL,
    [ClassTotal]      int           NULL);

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
   this readable to the corpus's own statement lint. TextData is narrowed to
   class 22 inside the deferred batch rather than after it, so the other
   classes' text never enters the archive at all — filtering on the way out
   would have staged it first. */
IF @path IS NOT NULL AND @err = 0
BEGIN
    BEGIN TRY
        INSERT INTO @events ([StartTime], [EventClass], [DatabaseName],
                             [ObjectName], [ObjectType], [LoginName],
                             [ApplicationName], [HostName], [Duration],
                             [IntegerData], [Severity], [Success], [TextData],
                             [ClassTotal])
        EXEC sys.sp_executesql
            N'SELECT TOP (5000)
                     r.StartTime, r.EventClass, r.DatabaseName,
                     r.ObjectName, r.ObjectType, r.LoginName,
                     r.ApplicationName, r.HostName, r.Duration,
                     r.IntegerData, r.Severity, r.Success,
                     r.TextData, r.class_total
              FROM (SELECT g.StartTime, g.EventClass, g.DatabaseName,
                           g.ObjectName, g.ObjectType, g.LoginName,
                           g.ApplicationName, g.HostName, g.Duration,
                           g.IntegerData, g.Severity, g.Success,
                           CASE WHEN g.EventClass = 22
                                THEN CONVERT(nvarchar(4000), g.TextData) END AS TextData,
                           ROW_NUMBER() OVER (PARTITION BY g.EventClass
                                              ORDER BY g.StartTime DESC) AS class_rank,
                           COUNT(*) OVER (PARTITION BY g.EventClass) AS class_total
                    FROM sys.fn_trace_gettable(@p, DEFAULT) AS g
                    WHERE g.EventClass IN (20, 22, 46, 47, 92, 93, 94, 95,
                                           104, 105, 108, 115, 116, 164)) AS r
              ORDER BY r.class_rank, r.StartTime DESC, r.EventClass
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
       5000                                                     AS [row_cap],
       (SELECT COUNT(*) FROM @events)                           AS [counts.events],
       /* What the files held for the classes read, one total per class summed
          once. Equal to counts.events when nothing was cut. */
       (SELECT SUM(c.[held]) FROM (SELECT MAX(e.[ClassTotal]) AS [held]
                                   FROM @events AS e
                                   GROUP BY e.[EventClass]) AS c)
                                                                AS [counts.events_in_files],
       /* At the cap, the window below is the window of what was KEPT, and it is
          no longer one window: each class reaches back as far as its share
          allowed. classes carries the window per class. */
       CASE WHEN EXISTS (SELECT 1 FROM @events AS e
                         GROUP BY e.[EventClass]
                         HAVING COUNT(*) < MAX(e.[ClassTotal]))
            THEN 1 ELSE 0 END                                   AS [capped],
       (SELECT MIN(e.[StartTime]) FROM @events AS e)            AS [window.oldest],
       (SELECT MAX(e.[StartTime]) FROM @events AS e)            AS [window.newest]
OPTION (RECOMPILE, MAXDOP 1);

SELECT e.[StartTime]                                            AS [start_time],
       e.[EventClass]                                           AS [event_class],
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
       e.[DatabaseName]                                         AS [database_name],
       e.[ObjectName]                                           AS [object_name],
       e.[ObjectType]                                           AS [object_type],
       e.[LoginName]                                            AS [login_name],
       e.[ApplicationName]                                      AS [application_name],
       e.[HostName]                                             AS [host_name],
       e.[Duration]                                             AS [duration_us],
       e.[IntegerData]                                          AS [integer_data],
       e.[Severity]                                             AS [severity],
       e.[Success]                                              AS [success],
       e.[TextData]                                             AS [text_data]
FROM @events AS e
ORDER BY e.[StartTime] DESC
OPTION (RECOMPILE, MAXDOP 1);

/* One row per class read: what the files held, what was kept, and the window
   the kept rows cover. A class whose kept is below its held is one the cap
   cut, and its oldest_kept says how far back it still reaches. A class with no
   row in the files has no row here, which is the same statement 044 makes. */
SELECT e.[EventClass]                                           AS [event_class],
       MAX(e.[ClassTotal])                                      AS [held],
       COUNT(*)                                                 AS [kept],
       MIN(e.[StartTime])                                       AS [oldest_kept],
       MAX(e.[StartTime])                                       AS [newest_kept]
FROM @events AS e
GROUP BY e.[EventClass]
ORDER BY e.[EventClass]
OPTION (RECOMPILE, MAXDOP 1);
