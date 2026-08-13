-- @scope:       instance
-- @resultsets:  root:object, events:array, deadlocks:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     120
--
-- What the always-on system_health session has caught since it last wrapped.
--
-- Why this collector exists: wait statistics say that sessions waited on locks
-- and never on what. An instance can show millions of LCK_M_* waits averaging
-- hundreds of milliseconds, and nothing else in this archive narrows that to a
-- resource, a pair of statements, or even a count of deadlocks. system_health
-- has been running by default since SQL Server 2008, costs nothing to read,
-- and is the only source in reach that records the events themselves.
--
-- NO STATEMENT TEXT IS COLLECTED, AND THAT IS THE POINT OF THIS FILE'S SHAPE.
-- A deadlock graph carries the verbatim SQL of both victims, which can hold
-- literals copied out of application tables — the same disclosure that puts
-- --include-session-text behind a flag. Projecting graphs here would put that
-- text into every archive by default, so this collector projects counts,
-- timestamps and event names only. The graphs stay on the instance.
--
-- WHAT THE RING BUFFER IS. system_health writes to a memory ring of bounded
-- size: old events are overwritten, not archived. The window it covers is
-- therefore whatever the event rate leaves — hours on a busy instance, weeks
-- on a quiet one. earliest_event is projected for exactly this reason: a count
-- of "3 deadlocks" means nothing until a reader knows whether it covers two
-- days or twenty minutes. A restart empties it entirely.
--
-- NO JUDGEMENT IS APPLIED. Deadlocks are not defects in themselves — a retry
-- loop around a deadlock-prone pattern is a legitimate design, and some
-- workloads are expected to produce them. The count, the rate and the window
-- are reported; whether the rate is acceptable belongs to whoever knows what
-- the application does when one occurs.
--
-- Errors reported by the session are counted by error number rather than
-- listed, because their message text can embed data values.
--
-- SQL Server 2012 is the floor. sys.dm_xe_session_targets and the
-- system_health session both predate it. Not collected for that reason:
--   the event_file target of system_health   (2012 has it, but reading it
--     requires sys.fn_xe_file_target_read_file over the file system, which is
--     a different permission and a different failure mode; the ring buffer
--     answers the same question without leaving memory)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

DECLARE @ring xml =
    (SELECT CAST(t.target_data AS xml)
       FROM sys.dm_xe_session_targets AS t
       JOIN sys.dm_xe_sessions AS s ON s.address = t.event_session_address
      WHERE s.name = 'system_health' AND t.target_name = 'ring_buffer');

DECLARE @events TABLE (
    event_name  sysname,
    event_time  datetime2(3),
    error_number int NULL
);

INSERT INTO @events (event_name, event_time, error_number)
SELECT
    x.value('@name', 'sysname'),
    x.value('@timestamp', 'datetime2(3)'),
    x.value('(data[@name="error_number"]/value)[1]', 'int')
FROM @ring.nodes('/RingBufferTarget/event') AS e(x);

SELECT
    CAST(CASE WHEN @ring IS NULL THEN 0 ELSE 1 END AS bit)      AS [session.running],
    (SELECT COUNT(*) FROM @events)                              AS [session.events_in_ring],
    -- How far back the ring reaches. Every count below is meaningless without
    -- it: the ring is overwritten, so a low count can mean a quiet instance or
    -- a very busy one that wrapped an hour ago.
    CONVERT(varchar(23), (SELECT MIN(event_time) FROM @events), 126)
                                                                AS [session.earliest_event],
    CONVERT(varchar(23), (SELECT MAX(event_time) FROM @events), 126)
                                                                AS [session.latest_event],
    DATEDIFF(minute, (SELECT MIN(event_time) FROM @events),
                     (SELECT MAX(event_time) FROM @events))     AS [session.window_minutes],
    (SELECT COUNT(*) FROM @events WHERE event_name = 'xml_deadlock_report')
                                                                AS [session.deadlocks],
    (SELECT COUNT(*) FROM @events WHERE event_name = 'error_reported')
                                                                AS [session.errors_reported],
    -- The instance start, so a reader can tell an empty ring from a ring
    -- emptied by a restart an hour ago.
    CONVERT(varchar(23), (SELECT sqlserver_start_time FROM sys.dm_os_sys_info), 126)
                                                                AS [session.instance_start]
OPTION (RECOMPILE, MAXDOP 1);

/* Every event kind the ring holds, with its span. This is the inventory: what
   system_health actually caught, before asking anything about any one of them. */
SELECT
    event_name                                                  AS [event],
    COUNT(*)                                                    AS [count],
    CONVERT(varchar(23), MIN(event_time), 126)                  AS [first],
    CONVERT(varchar(23), MAX(event_time), 126)                  AS [last],
    -- Error number only, never the message: the text of an error can embed the
    -- value that caused it.
    COUNT(DISTINCT error_number)                                AS [distinct_error_numbers],
    MIN(error_number)                                           AS [min_error_number],
    MAX(error_number)                                           AS [max_error_number]
FROM @events
GROUP BY event_name
ORDER BY COUNT(*) DESC
OPTION (RECOMPILE, MAXDOP 1);

/* One row per deadlock, timestamp only. Enough to say how many, how often and
   whether they cluster; not enough to say what was running, which is the
   line this collector does not cross. */
SELECT TOP (200)
    CONVERT(varchar(23), event_time, 126)                       AS [occurred_at]
FROM @events
WHERE event_name = 'xml_deadlock_report'
ORDER BY event_time DESC
OPTION (RECOMPILE, MAXDOP 1);
