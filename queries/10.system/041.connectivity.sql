-- @scope:       instance
-- @resultsets:  root:object, ring_buffers:array, connectivity:array, exceptions:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     120
--
-- Connection failures and losses, from the connectivity ring buffer.
--
-- Why this collector exists: a client reported intermittent "connection reset
-- by peer" errors with no known network fault. This buffer showed 841 resets
-- in six and a half hours, 791 of them from one monitoring host that opened a
-- TCP connection to port 1433 and closed it before login — a port check, not a
-- lost connection. The genuine incidents were the remaining fifty. Nothing
-- else on the instance records a connection that never became a session.
--
-- THE BUFFER IS CAPPED AT 1024 RECORDS AND IT WRAPS. On that instance the cap
-- meant six hours of coverage, so an incident reported the previous day had
-- already been overwritten — by the monitoring noise. That is why the time
-- span of every buffer is reported in ring_buffers rather than only the
-- connectivity rows: a narrow span IS the finding, and a reader who sees
-- counts without a window will read a saturated buffer as a quiet instance.
--
-- Timestamps are reconstructed from ms_ticks, and the arithmetic is done in
-- SECONDS. The obvious form, DATEADD(millisecond, -(@ticks - timestamp), ...),
-- overflows a 32-bit int after about 24 days and fails outright on any
-- well-kept server — which is exactly the population worth auditing. Dividing
-- to seconds first costs one second of precision and works to 68 years.
--
-- The XML is read with the value() method, which requires QUOTED_IDENTIFIER
-- ON. The TDS default is ON and the collector relies on it; a client tool that
-- turns it off will see error 1934 here rather than wrong data.
--
-- RING_BUFFER_EXCEPTION IS AGGREGATED HERE RATHER THAN GIVEN ITS OWN FILE.
-- It holds 459 records on an instance doing nothing, it survives a cycle of
-- the error log, and an aggregate by error number and severity is cheap: it
-- would say "this instance throws three thousand 8134s an hour", which the
-- error log's filtered read can miss entirely. A dump would be noise, so the
-- Stack element is never projected — and note that these records carry NO
-- SPID, whatever a reading that confuses them with RING_BUFFER_SECURITY_ERROR
-- may suggest.
--
-- SQL Server 2012 is the floor. sys.dm_os_ring_buffers predates it. The record
-- layout is undocumented and has been stable for many versions, but it is not
-- contractual — hence value() with explicit types rather than positional
-- parsing, so a layout change yields NULLs in one column instead of shifting
-- every field silently.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @ticks bigint = (SELECT ms_ticks FROM sys.dm_os_sys_info);

SELECT si.sqlserver_start_time                                    AS [instance_start],
       DATEDIFF(second, si.sqlserver_start_time, GETDATE())       AS [seconds_since_instance_start],
       SYSDATETIME()                                              AS [collected_at],
       (SELECT COUNT(*) FROM sys.dm_os_ring_buffers
        WHERE ring_buffer_type = 'RING_BUFFER_CONNECTIVITY')      AS [connectivity_records],
       1024                                                       AS [connectivity_capacity]
FROM sys.dm_os_sys_info AS si
OPTION (RECOMPILE, MAXDOP 1);

/* Every buffer, with the window it still covers. A buffer whose oldest record
   is minutes old is full and discarding history. */
SELECT rb.ring_buffer_type                                        AS [ring_buffer],
       COUNT(*)                                                   AS [records],
       MIN(DATEADD(second, -((@ticks - rb.timestamp) / 1000), GETDATE())) AS [oldest],
       MAX(DATEADD(second, -((@ticks - rb.timestamp) / 1000), GETDATE())) AS [newest],
       DATEDIFF(second,
                MIN(DATEADD(second, -((@ticks - rb.timestamp) / 1000), GETDATE())),
                MAX(DATEADD(second, -((@ticks - rb.timestamp) / 1000), GETDATE()))) AS [span_seconds]
FROM sys.dm_os_ring_buffers AS rb
GROUP BY rb.ring_buffer_type
ORDER BY COUNT(*) DESC
OPTION (RECOMPILE, MAXDOP 1);

/* Grouped, not one row per event: 1024 raw records are mostly repetition, and
   the shape of the repetition is the answer. RemoteHost is what separates a
   monitoring probe from an application, and the socket error under
   TdsBufInfo/InputBufError is what separates a reset by the peer (10054) from
   a broken pipe (109) or a clean close. */
SELECT c.rec_type                                                 AS [record_type],
       c.rec_source                                               AS [record_source],
       c.remote_host                                              AS [remote_host],
       c.sni_error                                                AS [sni_consumer_error],
       c.socket_error                                             AS [socket_error],
       c.state                                                    AS [state],
       COUNT(*)                                                   AS [occurrences],
       MIN(c.when_local)                                          AS [first_seen],
       MAX(c.when_local)                                          AS [last_seen],
       MIN(c.total_login_ms)                                      AS [min_login_ms],
       MAX(c.total_login_ms)                                      AS [max_login_ms]
FROM (
    SELECT DATEADD(second, -((@ticks - rb.timestamp) / 1000), GETDATE())              AS when_local,
           x.value('(//ConnectivityTraceRecord/RecordType)[1]',   'varchar(60)')      AS rec_type,
           x.value('(//ConnectivityTraceRecord/RecordSource)[1]', 'varchar(60)')      AS rec_source,
           x.value('(//ConnectivityTraceRecord/RemoteHost)[1]',   'varchar(128)')     AS remote_host,
           x.value('(//ConnectivityTraceRecord/SniConsumerError)[1]', 'int')          AS sni_error,
           x.value('(//ConnectivityTraceRecord/TdsBufInfo/InputBufError)[1]', 'int')  AS socket_error,
           x.value('(//ConnectivityTraceRecord/State)[1]', 'int')                     AS state,
           x.value('(//ConnectivityTraceRecord/LoginTimersInMilliseconds/TotalTime)[1]', 'bigint') AS total_login_ms
    FROM sys.dm_os_ring_buffers AS rb
    CROSS APPLY (SELECT CAST(rb.record AS xml)) AS r(x)
    WHERE rb.ring_buffer_type = 'RING_BUFFER_CONNECTIVITY'
) AS c
GROUP BY c.rec_type, c.rec_source, c.remote_host, c.sni_error, c.socket_error, c.state
ORDER BY COUNT(*) DESC
OPTION (RECOMPILE, MAXDOP 1);

/* The exception buffer, aggregated. Same undocumented-shape caveat as every
   other decode of this view: explicit XPaths with explicit types, so a renamed
   element gives one NULL column rather than a silently shifted row. */
SELECT e.error_number                                             AS [error_number],
       e.severity                                                 AS [severity],
       e.state                                                    AS [state],
       COUNT(*)                                                   AS [occurrences],
       MIN(e.when_local)                                          AS [first_seen],
       MAX(e.when_local)                                          AS [last_seen]
FROM (
    SELECT DATEADD(second, -((@ticks - rb.timestamp) / 1000), GETDATE())      AS when_local,
           x.value('(//Exception/Error)[1]',    'int')                        AS error_number,
           x.value('(//Exception/Severity)[1]', 'int')                        AS severity,
           x.value('(//Exception/State)[1]',    'int')                        AS state
    FROM sys.dm_os_ring_buffers AS rb
    CROSS APPLY (SELECT CAST(rb.record AS xml)) AS r(x)
    WHERE rb.ring_buffer_type = 'RING_BUFFER_EXCEPTION'
) AS e
GROUP BY e.error_number, e.severity, e.state
ORDER BY COUNT(*) DESC
OPTION (RECOMPILE, MAXDOP 1);
