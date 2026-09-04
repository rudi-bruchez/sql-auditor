-- @scope:       instance
-- @resultsets:  root:object, notifications:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- The instance's own history of memory pressure, from
-- RING_BUFFER_RESOURCE_MONITOR.
--
-- EVERY OTHER MEMORY READING IN THIS ARCHIVE IS AN INSTANT. 010.properties.sql
-- and 015.buffer-pool.sql say what memory looked like at the moment of
-- collection, and a collection is one moment. An instance that spends five
-- minutes an hour unable to satisfy allocations and is comfortable the rest of
-- the time reads as comfortable — which is the reading that sends an audit
-- looking somewhere else entirely. 074.memory-health.sql makes exactly this
-- argument, but that view is 2025 and later; this buffer covers the whole
-- supported range.
--
-- The records carry a Notification — RESOURCE_MEMPHYSICAL_LOW,
-- RESOURCE_MEMPHYSICAL_HIGH, RESOURCE_MEMVIRTUAL_LOW — with IndicatorsProcess,
-- IndicatorsSystem and IndicatorsPool, and the node.
--
-- THE NODE ELEMENT IS NodeId AND NOT Node, and that is not a detail. An XPath
-- on the wrong name returns NULL rather than failing, so the mistake ships and
-- produces a column of nulls nobody questions.
--
-- THE VALUES COME OUT AS TEXT AND ARE CONVERTED AFTERWARDS, and that is a
-- correction paid for elsewhere. 048.security-errors.sql asked value() for an
-- int on a field whose name promised a number, and the first client instance it
-- ran against answered 'x_cse_Success' — a symbolic value, in an undocumented
-- record, which raised and took the whole collector's batch with it. An
-- explicit numeric type converts a surprise into an ERROR; TRY_CONVERT converts
-- it into a NULL, which is the contract this file claims two paragraphs below.
-- These four are numbers on every record seen so far, and that is exactly the
-- evidence the other file had too.
--
-- WHAT IS DECODED HERE IS UNSUPPORTED. Microsoft documents
-- sys.dm_os_ring_buffers as "identified for informational purposes only, not
-- supported, future compatibility is not guaranteed", and publishes the record
-- schema of no buffer at all. The rule this corpus follows is therefore not
-- "decode what Microsoft documents" — that rule forbids everything it goes on
-- to propose — but: decode a buffer when its record shape has been stable
-- across the supported range and it answers a question no other collector
-- answers. The obligation that comes with it is that every projection must
-- survive an element that is missing or renamed, which is what the explicit
-- XPaths buy.
--
-- One record on a healthy idle instance, which is itself the reading: an
-- instance stacking LOW notifications is under pressure, and nothing else in
-- this archive says so.
--
-- Two constraints are inherited from 041.connectivity.sql and reused rather
-- than rediscovered: the ms_ticks arithmetic is done in SECONDS, because
-- DATEADD's increment argument is an int and gives up after 24 days of uptime;
-- and the buffer's window is reported beside its numbers, because a short
-- window IS the finding when someone reads a quiet afternoon as a quiet
-- server.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @ticks bigint = (SELECT ms_ticks FROM sys.dm_os_sys_info);

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                     AS [collected_at],
       COUNT(*)                                                     AS [counts.records],
       MIN(DATEADD(second, -((@ticks - rb.timestamp) / 1000), GETDATE()))       AS [window.oldest],
       MAX(DATEADD(second, -((@ticks - rb.timestamp) / 1000), GETDATE()))       AS [window.newest],
       DATEDIFF(second,
                MIN(DATEADD(second, -((@ticks - rb.timestamp) / 1000), GETDATE())),
                MAX(DATEADD(second, -((@ticks - rb.timestamp) / 1000), GETDATE())))
                                                                    AS [window.span_seconds]
FROM sys.dm_os_ring_buffers AS rb
WHERE rb.ring_buffer_type = 'RING_BUFFER_RESOURCE_MONITOR'
OPTION (RECOMPILE, MAXDOP 1);

SELECT n.notification                                               AS [notification],
       n.node_id                                                    AS [node_id],
       COUNT(*)                                                     AS [occurrences],
       MIN(n.when_local)                                            AS [first_seen],
       MAX(n.when_local)                                            AS [last_seen],
       MAX(n.indicators_process)                                    AS [max_indicators_process],
       MAX(n.indicators_system)                                     AS [max_indicators_system],
       MAX(n.indicators_pool)                                       AS [max_indicators_pool]
FROM (
    SELECT DATEADD(second, -((@ticks - rb.timestamp) / 1000), GETDATE())        AS when_local,
           x.value('(//ResourceMonitor/Notification)[1]', 'varchar(60)')        AS notification,
           TRY_CONVERT(int, x.value('(//ResourceMonitor/NodeId)[1]', 'varchar(64)'))
                                                                                AS node_id,
           TRY_CONVERT(int, x.value('(//ResourceMonitor/IndicatorsProcess)[1]', 'varchar(64)'))
                                                                                AS indicators_process,
           TRY_CONVERT(int, x.value('(//ResourceMonitor/IndicatorsSystem)[1]', 'varchar(64)'))
                                                                                AS indicators_system,
           TRY_CONVERT(int, x.value('(//ResourceMonitor/IndicatorsPool)[1]', 'varchar(64)'))
                                                                                AS indicators_pool
    FROM sys.dm_os_ring_buffers AS rb
    CROSS APPLY (SELECT CAST(rb.record AS xml)) AS q(x)
    WHERE rb.ring_buffer_type = 'RING_BUFFER_RESOURCE_MONITOR'
) AS n
GROUP BY n.notification, n.node_id
ORDER BY COUNT(*) DESC
OPTION (RECOMPILE, MAXDOP 1);
