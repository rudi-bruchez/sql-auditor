-- @scope:       instance
-- @resultsets:  root:object, local_sessions:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- Which sessions originate on the server itself, and what they call themselves.
--
-- This is the third signal of "is SQL Server alone on this machine", beside
-- the memory residue in 010.properties.sql and the CPU residue in
-- 043.cpu-neighbours.sql. A session that arrives without crossing the network
-- is a process running on the host, and a host running the application beside
-- its database is a finding an audit should make from the archive rather than
-- from a remark in a meeting.
--
-- THE TEST IS NOT SHARED MEMORY ALONE. On Windows a local session arrives over
-- Shared Memory and net_transport says so. Shared Memory DOES NOT EXIST ON
-- LINUX: there every local session arrives over TCP loopback, so the test has
-- to include the loopback addresses, and a file testing only the transport
-- would report that no Linux instance has ever had a local session.
--
-- PROGRAM_NAME IS CLIENT-SUPPLIED AND THEREFORE NOT EVIDENCE. It is empty for
-- a default SqlClient connection and it can say anything at all. It
-- corroborates, it does not prove: the finding is "sessions originate on this
-- machine", and the program name is what tells the reader where to look next.
-- That is also why it is aggregated and why no login name, host name or
-- session id is projected beside it.
--
-- IT GOES IN ITS OWN FILE AND NOT INTO 042.connection-security.sql, which is
-- defined as an aggregate over sys.dm_exec_connections and explicitly refuses
-- the join to sys.dm_exec_sessions. Adding program_name there would change the
-- grouping key and contradict that file's own header two screens later — which
-- is what happens when one collector is specified in two places.
--
-- The disclosure question is real and smaller than it looks. The corpus's
-- invariant about session-derived text governs STATEMENT text, and the
-- manifest's disclosure is driven by the read of sys.dm_exec_sql_text, which
-- this file does not make. A client-supplied program name is not that, and
-- saying so here is the whole reconciliation.
--
-- The collector's own session is local whenever the tool runs on the server,
-- so it is marked rather than filtered, the way 042 marks its own group.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                     AS [collected_at],
       (SELECT COUNT(*) FROM sys.dm_exec_connections)               AS [counts.connections],
       (SELECT COUNT(*)
        FROM sys.dm_exec_connections AS c
        WHERE c.net_transport = 'Shared memory'
           OR c.client_net_address IN ('127.0.0.1', '::1', '<local>'))
                                                                    AS [counts.local_connections],
       (SELECT COUNT(DISTINCT s.program_name)
        FROM sys.dm_exec_connections AS c
        JOIN sys.dm_exec_sessions AS s ON s.session_id = c.session_id
        WHERE c.net_transport = 'Shared memory'
           OR c.client_net_address IN ('127.0.0.1', '::1', '<local>'))
                                                                    AS [counts.local_programs]
OPTION (RECOMPILE, MAXDOP 1);

SELECT s.program_name                                               AS [program_name],
       c.net_transport                                              AS [net_transport],
       c.client_net_address                                         AS [client_net_address],
       COUNT(*)                                                     AS [sessions],
       MIN(s.login_time)                                            AS [oldest_login],
       MAX(s.login_time)                                            AS [newest_login],
       MAX(CASE WHEN c.session_id = @@SPID THEN 1 ELSE 0 END)       AS [contains_collector_session]
FROM sys.dm_exec_connections AS c
JOIN sys.dm_exec_sessions AS s ON s.session_id = c.session_id
WHERE c.net_transport = 'Shared memory'
   OR c.client_net_address IN ('127.0.0.1', '::1', '<local>')
GROUP BY s.program_name, c.net_transport, c.client_net_address
ORDER BY COUNT(*) DESC
OPTION (RECOMPILE, MAXDOP 1);
