-- @scope:       instance
-- @resultsets:  root:object, security_errors:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- What the security API has been complaining about, from
-- RING_BUFFER_SECURITY_ERROR.
--
-- The records carry SPID, APIName, CallingAPIName, ErrorCode, SQLErrorCode and
-- SQLErrorState — SSPI negotiation failures, Kerberos problems, impersonation
-- refused.
--
-- IT IS NOT THE SAME THING AS A FAILED LOGIN, AND THAT IS WHY IT IS WORTH
-- HAVING. 040.error-log.sql counts 18456s, which says an authentication
-- attempt failed. This buffer says whether the cause was a wrong password or a
-- broken SPN. Read beside the auth_scheme distribution from
-- 042.connection-security.sql it closes the loop: NTLM where Kerberos was
-- expected is the symptom, these error codes are the cause.
--
-- ERRORCODE IS A HEXADECIMAL STRING AND NOT A NUMBER. A live record carries
-- 0x139F, and CAST('0x139F' AS int) fails with Msg 245 — measured. It is
-- projected as text, verbatim: a reader who wants the decimal can convert
-- through varbinary, and the archive keeps the form the engine wrote, which is
-- also the form every search engine will match.
--
-- AGGREGATE, NOT A DUMP. The records carry a session id and no user name, so a
-- per-SPID listing would add a number nobody can resolve to a person while the
-- aggregate already answers the question.
--
-- WHAT IS DECODED HERE IS UNSUPPORTED, the same caveat 047.resource-pressure
-- carries: Microsoft publishes the record schema of no ring buffer, so the
-- shape is asserted from observation and every element is read with value()
-- and an explicit type, so that a renamed element yields a NULL in one column
-- rather than shifting every field silently.
--
-- Two constraints are inherited from 041.connectivity.sql: the ms_ticks
-- arithmetic is done in SECONDS, because DATEADD's increment argument is an
-- int and gives up after 24 days of uptime; and the buffer's window is
-- reported beside its numbers, because a short window IS the finding when
-- someone reads a quiet afternoon as a quiet server.

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
WHERE rb.ring_buffer_type = 'RING_BUFFER_SECURITY_ERROR'
OPTION (RECOMPILE, MAXDOP 1);

SELECT e.api_name                                                   AS [api_name],
       e.calling_api_name                                           AS [calling_api_name],
       e.error_code                                                 AS [error_code],
       e.sql_error_code                                             AS [sql_error_code],
       e.sql_error_state                                            AS [sql_error_state],
       COUNT(*)                                                     AS [occurrences],
       MIN(e.when_local)                                            AS [first_seen],
       MAX(e.when_local)                                            AS [last_seen]
FROM (
    SELECT DATEADD(second, -((@ticks - rb.timestamp) / 1000), GETDATE())        AS when_local,
           x.value('(//Error/APIName)[1]', 'varchar(60)')                       AS api_name,
           x.value('(//Error/CallingAPIName)[1]', 'varchar(60)')                AS calling_api_name,
           x.value('(//Error/ErrorCode)[1]', 'varchar(32)')                     AS error_code,
           x.value('(//Error/SQLErrorCode)[1]', 'int')                          AS sql_error_code,
           x.value('(//Error/SQLErrorState)[1]', 'int')                         AS sql_error_state
    FROM sys.dm_os_ring_buffers AS rb
    CROSS APPLY (SELECT CAST(rb.record AS xml)) AS q(x)
    WHERE rb.ring_buffer_type = 'RING_BUFFER_SECURITY_ERROR'
) AS e
GROUP BY e.api_name, e.calling_api_name, e.error_code, e.sql_error_code, e.sql_error_state
ORDER BY COUNT(*) DESC
OPTION (RECOMPILE, MAXDOP 1);
