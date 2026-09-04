-- @scope:       instance
-- @resultsets:  root:object, connections:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- How sessions actually reach this instance: over what transport,
-- authenticated how, encrypted or not — and whether the server demands it.
--
-- 041.connectivity.sql reads the connectivity ring buffer, which is
-- connections that failed or were reset. Nothing read sys.dm_exec_connections,
-- so the archive could not say how the sessions that succeeded got here, and
-- three findings depended on it:
--
--   encrypt_option is the only place the instance says whether the TDS session
--   is encrypted. An audit recommending encryption without knowing the current
--   state is guessing.
--
--   auth_scheme separates KERBEROS from NTLM and SQL. NTLM where Kerberos was
--   assumed means a missing SPN, which is a real finding with a real fix and
--   is invisible everywhere else in this archive.
--
--   net_transport separates TCP from Shared Memory and Named Pipes. A session
--   arriving over Shared Memory runs ON THE SERVER ITSELF, which changes what
--   an application-latency finding is allowed to mean.
--
-- AGGREGATE, NEVER PER SESSION. One row per transport, scheme, encryption and
-- protocol version, with a count. A per-session dump would carry client
-- addresses and host names into the archive for no analytical gain.
--
-- THE COUNT IS OF ADDRESSES, NOT HOSTS. sys.dm_exec_connections has
-- client_net_address; host names live in sys.dm_exec_sessions and reaching
-- them means a join, for a number that is less reliable — a host name is
-- client-supplied. That join is refused here and made, deliberately and for a
-- different question, in 046.local-sessions.sql.
--
-- THE COLLECTOR'S OWN SESSION IS IN THE RESULT and cannot honestly be excluded
-- from it, so it is marked rather than filtered. The marking is a property of
-- the group and not of a session: written as CASE WHEN session_id = @@SPID
-- beside a GROUP BY it does not compile at all — Msg 8120, reproduced — and
-- written as MAX(CASE ...) it says "this tuple contains the collector", which
-- is what the column is named.
--
-- WHAT THE SESSIONS DO IS NOT WHAT THE SERVER DEMANDS. A run where every
-- session shows TRUE may be a server forcing encryption, or a set of clients
-- that all happened to ask for it while the next one will not. ForceEncryption
-- lives in the instance's own registry hive and is readable through
-- sys.dm_server_registry, which 020.host-services.sql already reads for the
-- startup parameters. Forced and encrypted is a configuration; unforced and
-- encrypted is a coincidence, and the pair is the finding.
--
-- THAT HALF IS WINDOWS-ONLY AND THE OUTPUT SAYS SO. Measured:
-- sys.dm_server_registry exists on Linux and returns ZERO ROWS — no error,
-- nothing. There the setting lives in mssql-conf, which no view exposes. An
-- empty registry read is indistinguishable from "encryption is not forced" to
-- a reader who does not know the platform, so the projection carries a
-- registry_readable flag beside the value, and the platform from
-- 021.host-info.sql is what makes the pair legible.
--
-- The certificate is in the same hive and worth the same trip, with the same
-- caveat: what is stored is a SHA-1 thumbprint and not the certificate, so
-- there is no expiry and no issuer here. A self-signed certificate is the
-- default and is not a finding by itself, but it is what the reader asks about
-- next.
--
-- WHAT IS NOT REACHABLE is the SCHANNEL configuration. Whether TLS 1.0 and 1.1
-- are disabled lives outside SQL Server's hive and sys.dm_server_registry does
-- not expose it. That stays a question for the client.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @registry_rows int = 0, @force_encryption int = NULL,
        @certificate nvarchar(128) = NULL;

/* One pass over the hive. The row count is over the whole view rather than
   over the key below, because "the view returned nothing at all" is the Linux
   answer and "the view returned rows but not this value" is the Windows
   default — and a reader must be able to tell them apart. */
SELECT @registry_rows = COUNT(*),
       /* TRY_CONVERT and not CONVERT, deliberately. value_data is a
          sql_variant and the hive is not this collector's to guarantee: a value
          stored with an unexpected base type would fail the conversion, and
          that failure would take the whole batch — both declared result sets
          with it — for a column that is a nicety beside the connection
          aggregate. A NULL says "not readable as a number" and costs nothing.
          TRY_CONVERT is 2012, which is this corpus's floor. */
       @force_encryption = MAX(CASE WHEN r.value_name = 'ForceEncryption'
                                    THEN TRY_CONVERT(int, r.value_data) END),
       @certificate = MAX(CASE WHEN r.value_name = 'Certificate'
                               THEN TRY_CONVERT(nvarchar(128), r.value_data) END)
FROM sys.dm_server_registry AS r
OPTION (RECOMPILE, MAXDOP 1);

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                 AS [collected_at],
       CASE WHEN @registry_rows > 0 THEN 1 ELSE 0 END           AS [registry_readable],
       @force_encryption                                        AS [force_encryption],
       @certificate                                             AS [certificate_thumbprint],
       (SELECT COUNT(*) FROM sys.dm_exec_connections)           AS [counts.connections],
       (SELECT COUNT(DISTINCT c.client_net_address)
        FROM sys.dm_exec_connections AS c)                      AS [counts.client_addresses],
       (SELECT COUNT(*) FROM sys.dm_exec_connections AS c
        WHERE c.encrypt_option = 'TRUE')                        AS [counts.encrypted_connections]
OPTION (RECOMPILE, MAXDOP 1);

SELECT c.net_transport                                          AS [net_transport],
       c.auth_scheme                                            AS [auth_scheme],
       c.encrypt_option                                         AS [encrypt_option],
       c.protocol_version                                       AS [protocol_version],
       COUNT(*)                                                 AS [connections],
       COUNT(DISTINCT c.client_net_address)                     AS [client_addresses],
       MAX(CASE WHEN c.session_id = @@SPID THEN 1 ELSE 0 END)   AS [contains_collector_session]
FROM sys.dm_exec_connections AS c
GROUP BY c.net_transport, c.auth_scheme, c.encrypt_option, c.protocol_version
ORDER BY COUNT(*) DESC
OPTION (RECOMPILE, MAXDOP 1);
