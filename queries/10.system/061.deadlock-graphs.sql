-- @scope:         instance
-- @resultsets:    root:object, deadlocks:array
-- @permissions:   CONNECT, VIEW SERVER STATE
-- @requires_flag: deadlock_graphs
-- @writer:        deadlock-graphs
-- @timeout:       120
--
-- The deadlock graphs system_health still holds, one .xdl file each.
--
-- Why this file exists and 060.system-health.sql does not do it.
-- 060 deliberately projects the count and the timestamps of every deadlock and
-- nothing else, and says so in its own header: a deadlock graph carries the
-- verbatim SQL of both victims, which can hold literals copied out of
-- application tables. Putting graphs in 060 would put that text into every
-- archive by default. This file is the same read behind a flag, and it is the
-- only difference between the two.
--
-- WHAT A GRAPH IS WORTH. Wait statistics say sessions waited on locks; 060 says
-- how many deadlocks and when. Neither says which two statements, on which
-- resource, in which order, or which one was chosen as victim. That is in the
-- graph and nowhere else in reach, and it is the difference between an audit
-- that reports "lock contention" and one that names the pattern.
--
-- .xdl RATHER THAN .xml, because SSMS opens an .xdl as the deadlock diagram
-- rather than as a wall of angle brackets. The bytes are identical.
--
-- THE RING BUFFER IS A WINDOW, NOT AN ARCHIVE. system_health overwrites its
-- oldest events, and a restart empties it. What is here is whatever the ring
-- still held at the moment of collection; 060 projects the span so a reader can
-- tell "no deadlocks" from "no memory of any". Nothing on the instance is
-- modified, read or cleared by this file — collecting a graph does not consume
-- it.
--
-- TWO CAPS, NEITHER SILENT. At most 100 graphs, the most recent first, and at
-- most 1 MiB each. Past either, the XML is NULLed by a CONDITIONAL PROJECTION —
-- never a WHERE — so the row survives with its timestamp, its size and its
-- rank, and the writer records a named omission. A deadlock dropped from the
-- result set would be a deadlock the archive never admits happened, and its
-- timestamp is exactly what makes a cluster visible.
--
-- TIMESTAMPS ARE UTC. Extended Events stamps its ring in UTC while everything
-- else in this archive is the server's local time. 060 explains the offset at
-- length; the same convention holds here, and the file names are sequential
-- rather than timestamped so that no colon has to be sanitised out of one.
--
-- NO JUDGEMENT IS APPLIED. A deadlock is not a defect. A retry loop around a
-- deadlock-prone pattern is a legitimate design and some workloads are expected
-- to produce them. The graphs are collected; reading them is analysis.
--
-- SQL Server 2012 is the floor. The ring buffer target and the system_health
-- session both predate it.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

DECLARE @ring xml =
    (SELECT CAST(t.target_data AS xml)
       FROM sys.dm_xe_session_targets AS t
       JOIN sys.dm_xe_sessions AS s ON s.address = t.event_session_address
      WHERE s.name = 'system_health' AND t.target_name = 'ring_buffer');

/* Shredded once into a table variable rather than twice out of the XML: the
   ring can hold thousands of events and each .nodes() over it is a full parse.
   The graph is taken as nvarchar(max) here — the writer needs the bytes, not a
   queryable document, and casting once is cheaper than casting per projection. */
DECLARE @deadlocks TABLE (
    event_time datetime2(3),
    graph      nvarchar(max)
);

INSERT INTO @deadlocks (event_time, graph)
SELECT x.value('@timestamp', 'datetime2(3)'),
       CAST(x.query('(data[@name="xml_report"]/value/*)[1]') AS nvarchar(max))
FROM @ring.nodes('/RingBufferTarget/event[@name="xml_deadlock_report"]') AS e(x);

SELECT
    CAST(CASE WHEN @ring IS NULL THEN 0 ELSE 1 END AS bit)      AS [session.running],
    (SELECT COUNT(*) FROM @deadlocks)                           AS [session.deadlocks],
    CONVERT(varchar(23), (SELECT MIN(event_time) FROM @deadlocks), 126)
                                                                AS [session.earliest_deadlock],
    CONVERT(varchar(23), (SELECT MAX(event_time) FROM @deadlocks), 126)
                                                                AS [session.latest_deadlock],
    /* Both caps, written out, so a DBA reading the exported corpus sees the
       numbers. maxDeadlockGraphs and maxDeadlockBytes on the Go side are held
       to these two by a test. */
    100                                                         AS [caps.graphs],
    1048576                                                     AS [caps.graph_bytes]
OPTION (RECOMPILE, MAXDOP 1);

/* One row per deadlock, whether or not its graph came back. The rank travels
   with every row: rank 137 of 400 is what tells a reader the file is absent by
   decision rather than because the ring held nothing.

   Most recent first. A deadlock from four minutes ago is the one somebody is
   asking about; one from the far end of the ring is history that the ring is
   about to lose anyway. */
SELECT
    ROW_NUMBER() OVER (ORDER BY d.event_time DESC)              AS [graph.rank],
    COUNT(*)     OVER ()                                        AS [graph.count],
    CONVERT(varchar(23), d.event_time, 126)                     AS [occurred_at],
    /* The same CASE for both caps, so a graph that is both oversized and past
       the count cap arrives NULL exactly once, with the rank and the size
       beside it saying which it was. */
    CASE WHEN DATALENGTH(d.graph) <= 1048576
          AND ROW_NUMBER() OVER (ORDER BY d.event_time DESC) <= 100
         THEN d.graph END                                       AS [graph],
    /* Projected even when the graph is not: the reader learns how large the
       diagram they cannot open was. */
    DATALENGTH(d.graph)                                         AS [graph_bytes]
FROM @deadlocks AS d
ORDER BY d.event_time DESC
OPTION (RECOMPILE, MAXDOP 1);
