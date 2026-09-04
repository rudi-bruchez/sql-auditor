-- @scope:       instance
-- @resultsets:  root:object, sessions:array, session_events:array, session_targets:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- What Extended Events sessions exist on this instance, which are running, what
-- each captures and where it writes it.
--
-- Why this collector exists. 060.system-health.sql reads one session by name and
-- assumes it is there, which it always is. Everything else is invisible: a site
-- that built its own capture, a diagnostic session somebody started last week and
-- forgot, a session defined and never started. On the audit that prompted this
-- file the instance carried seven sessions, one of them a deadlock capture
-- running for ten months that listened to the wrong events, and one recording
-- every batch to a file on a data volume. Neither was in any archive.
--
-- IT IS ALSO THE MAP FOR THE OTHER TWO. 061.deadlock-graphs.sql and
-- 063.blocked-process-reports.sql both read .xel files, and this is the file
-- that says which sessions write them and where. A reader who finds no blocked
-- process report in the archive needs to be able to tell "nothing captures them"
-- from "the capture exists and was empty", and that answer is here.
--
-- blocked_process_threshold IS PROJECTED IN THE ROOT, and it belongs here rather
-- than with the other sp_configure settings in 010.properties.sql. At 0 — the
-- default — the blocked_process_report event never fires, so a session
-- subscribing to it collects nothing and looks like a session that saw no
-- blocking. The setting and the subscription are one fact between them, and
-- separating them is how a reader concludes the wrong thing.
--
-- NO STATEMENT TEXT IS COLLECTED. A session's definition names events, actions
-- and targets — never data. The predicate text is deliberately left out: a
-- filter can carry a literal, and it is the one part of a session definition
-- that can.
--
-- NO JUDGEMENT IS APPLIED. A session capturing every batch is not called
-- expensive here, and a stopped session is not called a mistake — a session that
-- exists and does not run is a normal way to keep a diagnostic ready. What it
-- costs and whether it should run is analysis.
--
-- SQL Server 2012 is the floor. Extended Events and all three catalog views
-- predate it. Not collected for that reason: nothing.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT
    (SELECT COUNT(*) FROM sys.server_event_sessions)              AS [counts.defined],
    (SELECT COUNT(*) FROM sys.dm_xe_sessions)                     AS [counts.running],
    /* Sessions subscribing to the two events an audit looks for. Counted here
       so a reader has the answer before scrolling the inventory. */
    (SELECT COUNT(DISTINCT e.event_session_id)
       FROM sys.server_event_session_events AS e
      WHERE e.name = 'blocked_process_report')                    AS [counts.capturing_blocked_processes],
    (SELECT COUNT(DISTINCT e.event_session_id)
       FROM sys.server_event_session_events AS e
      WHERE e.name = 'xml_deadlock_report')                       AS [counts.capturing_deadlock_graphs],
    /* 0 means the event cannot fire, whatever any session subscribes to. */
    (SELECT CAST(c.value_in_use AS int) FROM sys.configurations AS c
      WHERE c.name = 'blocked process threshold (s)')             AS [blocked_process.threshold_seconds],
    CONVERT(varchar(23), SYSDATETIME(), 126)                      AS [collected_at]
OPTION (RECOMPILE, MAXDOP 1);

/* Every session, defined or running. startup_state is the one that says whether
   what is running now will still be running after a restart. */
SELECT
    s.name                                                        AS [name],
    CAST(CASE WHEN r.name IS NULL THEN 0 ELSE 1 END AS int)       AS [is_running],
    CAST(s.startup_state AS int)                                  AS [starts_with_instance],
    CONVERT(varchar(23), r.create_time, 126)                      AS [started_at],
    /* A session running since long before the instance's own start would be a
       contradiction; a session started this morning is somebody's diagnostic. */
    s.event_retention_mode_desc                                   AS [retention_mode],
    s.max_dispatch_latency                                        AS [max_dispatch_latency_ms],
    s.max_memory                                                  AS [max_memory_kb],
    CAST(s.track_causality AS int)                                AS [track_causality]
FROM sys.server_event_sessions AS s
LEFT JOIN sys.dm_xe_sessions   AS r ON r.name = s.name
ORDER BY s.name
OPTION (RECOMPILE, MAXDOP 1);

/* One row per event a session subscribes to. This is the result set that
   answers "does anything capture blocked process reports", and the one that
   showed a deadlock capture listening to lock_deadlock rather than to
   xml_deadlock_report — the events that say an interblocking happened, without
   the report that says what it was. */
SELECT
    s.name                                                        AS [session],
    e.name                                                        AS [event],
    e.package                                                     AS [package]
FROM sys.server_event_sessions       AS s
JOIN sys.server_event_session_events AS e ON e.event_session_id = s.event_session_id
ORDER BY s.name, e.name
OPTION (RECOMPILE, MAXDOP 1);

/* One row per target, with the settings that decide how much history it keeps.
   For an event_file that is the path, the file size and the rollover count; for
   a ring_buffer it is max_events_limit and max_memory — and it is the second of
   those that usually bites. An anneau configured for 5000 events with 4 MB of
   memory holds whatever fits in 4 MB, which on a busy instance is minutes.

   THE RUNNING TARGET'S DATA IS PROJECTED FOR event_file AND FOR NOTHING ELSE,
   and the restriction is a disclosure decision rather than a size one. A
   ring_buffer's target_data is the ring itself — the events, with their
   statement text and, for system_health, whole deadlock reports. Projecting it
   here would put into an ungated collector exactly the content
   --include-deadlock-graphs exists to gate. An event_file's target_data is a
   list of file names and byte offsets and carries no event at all.

   It is projected because it is the only place the real path appears:
   sys.server_event_session_fields holds the filename as configured, which is
   commonly relative to the LOG directory, while the running target reports
   where the bytes are actually going. 061 and 063 both need that path. A
   session that is defined and not running has only the configured name, and
   that is a fact about the session rather than a gap in this file. */
SELECT
    s.name                                                        AS [session],
    t.name                                                        AS [target],
    f.name                                                        AS [setting],
    CAST(f.value AS nvarchar(400))                                AS [value],
    CASE WHEN t.name = 'event_file'
         THEN CAST(rt.target_data AS nvarchar(max)) END           AS [running_target_data]
FROM sys.server_event_sessions             AS s
JOIN sys.server_event_session_targets      AS t  ON t.event_session_id = s.event_session_id
LEFT JOIN sys.server_event_session_fields  AS f  ON f.event_session_id = s.event_session_id
                                                AND f.object_id = t.target_id
LEFT JOIN sys.dm_xe_sessions               AS rs ON rs.name = s.name
LEFT JOIN sys.dm_xe_session_targets        AS rt ON rt.event_session_address = rs.address
                                                AND rt.target_name = t.name
ORDER BY s.name, t.name, f.name
OPTION (RECOMPILE, MAXDOP 1);
