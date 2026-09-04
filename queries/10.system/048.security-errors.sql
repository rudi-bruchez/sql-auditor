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
-- NOT ONE OF THESE FIELDS IS A NUMBER, AND THE FIRST VERSION ONLY KNEW IT
-- ABOUT ONE OF THEM. ErrorCode is a hexadecimal string: a live record carries
-- 0x139F, and CAST('0x139F' AS int) fails with Msg 245 — measured. That much
-- was right from the start.
--
-- SQLErrorCode and SQLErrorState were typed as int beside it, by assuming from
-- their names, and on the first client instance this file ever ran against one
-- of them carried the symbolic value 'x_cse_Success'. value() with an explicit
-- int raised on it, which is a batch-level failure: the whole collector
-- returned nothing, on an instance where it had something to say.
--
-- So all three are projected as text, verbatim. A reader who wants a decimal
-- can convert through varbinary; the archive keeps the form the engine wrote,
-- which is also the form every search engine will match. The lesson is wider
-- than these two columns and is written into the paragraph below: the shape of
-- an undocumented buffer is not knowable from a field's name, so nothing here
-- asks the parser to assert a type the record never promised.
--
-- AGGREGATE, NOT A DUMP. The records carry a session id and no user name, so a
-- per-SPID listing would add a number nobody can resolve to a person while the
-- aggregate already answers the question.
--
-- WHAT IS DECODED HERE IS UNSUPPORTED, the same caveat 047.resource-pressure
-- carries: Microsoft publishes the record schema of no ring buffer. So the
-- shape is asserted from observation, and the contract is that a renamed OR
-- RETYPED element yields a NULL in one column rather than taking the batch
-- down. An explicit numeric type in value() breaks that contract, because it
-- converts a surprise into an error instead of into a NULL — which is what
-- 'x_cse_Success' proved. Text out of the buffer, and TRY_CONVERT afterwards
-- wherever a number is genuinely needed, is the shape that keeps the
-- promise.
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
           x.value('(//Error/SQLErrorCode)[1]', 'varchar(64)')                  AS sql_error_code,
           x.value('(//Error/SQLErrorState)[1]', 'varchar(64)')                 AS sql_error_state
    FROM sys.dm_os_ring_buffers AS rb
    CROSS APPLY (SELECT CAST(rb.record AS xml)) AS q(x)
    WHERE rb.ring_buffer_type = 'RING_BUFFER_SECURITY_ERROR'
) AS e
GROUP BY e.api_name, e.calling_api_name, e.error_code, e.sql_error_code, e.sql_error_state
ORDER BY COUNT(*) DESC
OPTION (RECOMPILE, MAXDOP 1);
