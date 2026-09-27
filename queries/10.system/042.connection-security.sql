-- @scope:       instance
-- @resultsets:  root:object, connections:array, pools:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
-- @discloses:   connection_pools
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
-- addresses and host names into the archive for no analytical gain. The pools
-- set below does carry host names, still aggregated, because the question it
-- answers cannot be asked without them.
--
-- THE COUNT IS OF ADDRESSES, NOT HOSTS. sys.dm_exec_connections has
-- client_net_address; host names live in sys.dm_exec_sessions and reaching
-- them means a join, for a number that is less reliable — a host name is
-- client-supplied. The connections set refuses that join. It is made twice
-- elsewhere, each time for a question of its own: in 046.local-sessions.sql,
-- and in the pools set below.
--
-- THE POOLS SET ANSWERS "IS AN APPLICATION'S POOL FULL?" A client that reports
-- repeated connection failures while the server logged nothing is very often
-- an application waiting for a free connection in its OWN pool: the timeout is
-- raised in the client ("the timeout period elapsed prior to obtaining a
-- connection from the pool") and never reaches the server, so no view and no
-- log line here records it. What the server can show is how many connections
-- each application holds right now, and that is what this set counts: live
-- connections grouped by host_name, program_name and login_name, with the
-- oldest and newest connect_time and the number of distinct client addresses.
--
-- The figure to compare each group with is 100, the default Max Pool Size of
-- ADO.NET and Microsoft.Data.SqlClient. It is a CLIENT-SIDE default and not a
-- server limit: nothing in SQL Server refuses the 101st connection, the
-- connection string can raise or lower it, and a pool exists per process and
-- per distinct connection string. A group here is coarser than a pool. Several
-- worker processes on one host, or two connection strings for two databases,
-- land in the same group, so a group well above 100 is several pools and a
-- group sitting at exactly 100 is the signature of one pool that is full.
-- counts.pool_groups_at_default_max at the root counts the groups at or above
-- it, and nothing more is concluded here.
--
-- The count is of PHYSICAL connections. Under MARS a session carries one row
-- per logical session beside its physical connection, with net_transport set
-- to Session, and a pool holds physical connections, so those rows are left
-- out. Only user sessions are kept.
--
-- WHAT IT DISCLOSES is declared with @discloses and printed in MANIFEST.txt: the
-- host names of the application servers, the program names they report and
-- the logins they connect as, a Windows login being a person's account as
-- often as a service's. All three are client-supplied or client-chosen, which
-- is why they are grouped and counted rather than listed per session, and no
-- session id, statement or address is projected beside them.
--
-- THE LIST IS CAPPED AT 200 GROUPS, largest first, and the root carries the
-- total number of groups: a truncated list that does not say so reads as a
-- complete one.
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

/* The groups are built once, so that the root's totals and the capped list
   below are the same snapshot rather than two reads of a view that changes
   between them. */
CREATE TABLE #pools (host_name nvarchar(128), program_name nvarchar(128),
                     login_name nvarchar(128), connections int,
                     oldest_connect_time datetime, newest_connect_time datetime,
                     client_addresses int, contains_collector_session int);
INSERT INTO #pools
SELECT s.host_name, s.program_name, s.login_name,
       COUNT(*),
       MIN(c.connect_time),
       MAX(c.connect_time),
       COUNT(DISTINCT c.client_net_address),
       MAX(CASE WHEN c.session_id = @@SPID THEN 1 ELSE 0 END)
FROM sys.dm_exec_connections AS c
JOIN sys.dm_exec_sessions AS s ON s.session_id = c.session_id
WHERE s.is_user_process = 1
  AND c.net_transport <> N'Session'
GROUP BY s.host_name, s.program_name, s.login_name;

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                 AS [collected_at],
       CASE WHEN @registry_rows > 0 THEN 1 ELSE 0 END           AS [registry_readable],
       @force_encryption                                        AS [force_encryption],
       @certificate                                             AS [certificate_thumbprint],
       (SELECT COUNT(*) FROM sys.dm_exec_connections)           AS [counts.connections],
       (SELECT COUNT(DISTINCT c.client_net_address)
        FROM sys.dm_exec_connections AS c)                      AS [counts.client_addresses],
       (SELECT COUNT(*) FROM sys.dm_exec_connections AS c
        WHERE c.encrypt_option = 'TRUE')                        AS [counts.encrypted_connections],
       (SELECT COUNT(*) FROM #pools)                            AS [counts.pool_groups],
       (SELECT COUNT(*) FROM #pools WHERE connections >= 100)   AS [counts.pool_groups_at_default_max],
       200                                                      AS [pool_groups_kept],
       100                                                      AS [client_default_max_pool_size]
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

SELECT TOP (200)
       p.host_name                                              AS [host_name],
       p.program_name                                           AS [program_name],
       p.login_name                                             AS [login_name],
       p.connections                                            AS [connections],
       p.oldest_connect_time                                    AS [oldest_connect_time],
       p.newest_connect_time                                    AS [newest_connect_time],
       p.client_addresses                                       AS [client_addresses],
       p.contains_collector_session                             AS [contains_collector_session]
FROM #pools AS p
ORDER BY p.connections DESC, p.host_name, p.program_name, p.login_name
OPTION (RECOMPILE, MAXDOP 1);
