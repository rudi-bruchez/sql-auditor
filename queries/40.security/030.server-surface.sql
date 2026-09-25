-- @scope:       instance
-- @resultsets:  root:object, linked_servers:array, linked_logins:array, server_triggers:array, credentials:array, audits:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     60
--
-- Four ways into this instance that are not a login, and whether anything
-- writes down what happens once someone is in.
--
-- WHY THESE FOUR SIT IN ONE FILE. 010.principals.sql answers who can connect
-- and what they hold. It is the right question and it is not the whole
-- perimeter: a linked server, a credential, a logon trigger and an audit
-- specification each change what a given principal can reach or what is
-- recorded about them, and none of the four appears in a list of principals.
-- They are one file because they are one question, they are all catalogue
-- reads answering in milliseconds, and splitting them would give four
-- collectors that are almost always empty.
--
-- LINKED SERVERS, AND THE ONE COLUMN THAT MATTERS. A linked server is an
-- outbound door, and sys.linked_logins says who walks through it. The row that
-- deserves a finding is local_principal_id = 0 with uses_self_credential = 0:
-- that is the default mapping, applying to every local principal who has no
-- mapping of their own, sending them to the remote server under one fixed
-- remote_name. So an account with minimal rights here can hold whatever that
-- remote identity holds there, and nothing in this instance's permissions says
-- so. is_rpc_out_enabled and is_data_access_enabled say what can be done once
-- through, and the pair is worth reading together: data access alone is a
-- query, RPC out is an execution.
--
-- NOTHING HERE CONTACTS A REMOTE SERVER, AND THAT IS A CONSTRAINT AND NOT AN
-- OVERSIGHT. sp_testlinkedserver and sp_linkedservers both connect, and on a
-- linked server pointing at a machine that is down or firewalled they block
-- until the provider's own timeout, which is not @timeout and not ours. A
-- collection that hangs on somebody else's network is a collection that comes
-- back empty. Every row below is read from the local catalogue, so a dead
-- linked server is inventoried exactly like a live one, which is also the
-- honest behaviour: an audit wants to know the door exists.
--
-- SERVER TRIGGERS, INCLUDING THE ONE THAT CAN LOCK EVERYONE OUT. A LOGON
-- trigger runs inside the login transaction of every session, so one that
-- errors, or that queries something slow or unavailable, refuses connections
-- to the whole instance including the connection that would come to fix it.
-- The only way back in is the dedicated administrator connection, which has to
-- have been enabled beforehand. That makes the existence of a logon trigger a
-- fact worth carrying even when it is working, which is why is_disabled is
-- projected beside it rather than used as a filter.
--
-- CREDENTIALS ARE NAMED, NEVER OPENED. sys.credentials holds the identity a
-- credential presents, not its secret, and the secret is not readable through
-- any catalogue. credential_identity is projected because it is the point: a
-- credential mapping to a domain administrator, used by a proxy that a job
-- step runs under, is a privilege escalation path with no trace in the
-- principals list.
--
-- THE AUDIT PICTURE IS INSTANCE-LEVEL HERE ON PURPOSE. sys.server_audits and
-- sys.server_audit_specifications predate this corpus's 2012 floor, so the
-- question "does anything audit this instance at all" is answerable on every
-- version. The per-database detail lives in 031.database-protections.sql,
-- which is gated at SQL Server 2016 for its other contents; an instance below
-- that floor still gets the answer that matters most, which is whether an
-- audit exists and whether it is running.
--
-- WHAT AN EMPTY RESULT MEANS, AND WHY THE COUNTS ARE IN THE ROOT. All five
-- arrays are empty on a plain instance, and an empty array is also what a
-- failed read produces. The root carries a count for each, taken separately,
-- so a reader can tell "there are none" from "this did not run".
--
-- Because an all-empty run proves nothing about the queries, the five arrays
-- were exercised on 16.0.4265.3 against objects built for the purpose and then
-- removed: a linked server pointing at 192.0.2.1, which is the RFC 5737
-- documentation range and therefore unreachable by construction, with a
-- default login mapping; a credential; a server DDL trigger on
-- CREATE_DATABASE; and a server audit with a specification of two action
-- groups, created and never enabled so that it wrote no file. All five
-- rendered, the counts agreed with the rows, and the linked login's password
-- appeared nowhere in the output. The objects were dropped and sys.servers
-- returned to its single loopback row.
--
-- That loopback row is worth a sentence of its own. Every instance carries an
-- entry in sys.servers for itself, and it also carries a row in
-- sys.linked_logins. A count over either view without WHERE is_linked = 1
-- therefore reports one linked server on an instance that has none, which is
-- the shape of error that survives review because the number is small and
-- plausible.
--
-- NO JUDGEMENT IS APPLIED. A linked server is how distributed reporting is
-- built, a credential is how a job reaches a file share, and an instance with
-- no audit is the normal case outside regulated estates. What each of them
-- costs depends on what it reaches, which is the reader's work.
--
-- SQL Server 2012 is the floor. All six catalogues predate it. Not collected
-- for that reason:
--   sys.dm_server_audit_status       (2012 has it, but it adds a status to an
--     audit that is_state_enabled already answers, and it returned no rows on
--     both lab instances where sys.server_audits also returned none, so it
--     tells a reader nothing the audits array does not)
--   sys.remote_logins                (remote servers, replaced by linked
--     servers in SQL Server 2005 and deprecated since)
--   the credential's secret          (not readable through any catalogue)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT
    CONVERT(sysname, SERVERPROPERTY('ServerName'))              AS [instance],
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    /* One count per array, so an empty array is never ambiguous. is_linked
       separates a linked server from the loopback entry every instance carries
       for itself, which is the row that makes a naive count read as 1. */
    (SELECT COUNT(*) FROM sys.servers WHERE is_linked = 1)      AS [counts.linked_servers],
    (SELECT COUNT(*) FROM sys.linked_logins AS ll
       JOIN sys.servers AS s ON s.server_id = ll.server_id
      WHERE s.is_linked = 1)                                    AS [counts.linked_logins],
    -- The default mapping, counted on its own because it is the one that
    -- changes what every local principal can reach.
    (SELECT COUNT(*) FROM sys.linked_logins AS ll
       JOIN sys.servers AS s ON s.server_id = ll.server_id
      WHERE s.is_linked = 1 AND ll.local_principal_id = 0
        AND ll.uses_self_credential = 0)                        AS [counts.linked_logins_default_mapped],
    (SELECT COUNT(*) FROM sys.server_triggers WHERE is_ms_shipped = 0)
                                                                AS [counts.server_triggers],
    /* Logon triggers counted apart from DDL ones, because only the first can
       refuse connections to the whole instance. The distinction is not in
       sys.server_triggers, whose type_desc says SQL or CLR; it is in the event
       the trigger is bound to. */
    (SELECT COUNT(DISTINCT t.object_id)
       FROM sys.server_triggers AS t
       JOIN sys.server_trigger_events AS te ON te.object_id = t.object_id
      WHERE t.is_ms_shipped = 0 AND te.type_desc = 'LOGON')     AS [counts.logon_triggers],
    (SELECT COUNT(DISTINCT t.object_id)
       FROM sys.server_triggers AS t
       JOIN sys.server_trigger_events AS te ON te.object_id = t.object_id
      WHERE t.is_ms_shipped = 0 AND te.type_desc = 'LOGON'
        AND t.is_disabled = 0)                                  AS [counts.logon_triggers_enabled],
    (SELECT COUNT(*) FROM sys.credentials)                      AS [counts.credentials],
    (SELECT COUNT(*) FROM sys.server_audits)                    AS [counts.audits],
    (SELECT COUNT(*) FROM sys.server_audits WHERE is_state_enabled = 1)
                                                                AS [counts.audits_running],
    (SELECT COUNT(*) FROM sys.server_audit_specifications)      AS [counts.audit_specifications]
OPTION (RECOMPILE, MAXDOP 1);

/* Linked servers, the loopback row excluded. provider and data_source say
   what is on the other end; a provider that is not SQLNCLI or MSOLEDBSQL is
   reaching something that is not SQL Server, which changes both the risk and
   who maintains it. */
SELECT
    s.name                                                      AS [name],
    s.product                                                   AS [product],
    s.provider                                                  AS [provider],
    s.data_source                                               AS [data_source],
    s.catalog                                                   AS [catalog],
    CAST(s.is_rpc_out_enabled AS bit)                           AS [rpc_out],
    CAST(s.is_data_access_enabled AS bit)                       AS [data_access],
    CAST(s.is_remote_login_enabled AS bit)                      AS [remote_login],
    -- Off means every query against this server runs with the remote server's
    -- collation assumed to match, and a mismatch there is a silent source of
    -- wrong results rather than an error.
    CAST(s.is_collation_compatible AS bit)                      AS [collation_compatible],
    s.collation_name                                            AS [collation],
    -- On means the provider is not asked to validate the remote schema before
    -- each execution, which is faster and means a changed remote schema is
    -- discovered as a failure at run time.
    CAST(s.lazy_schema_validation AS bit)                       AS [lazy_schema_validation],
    s.connect_timeout                                           AS [connect_timeout],
    s.query_timeout                                             AS [query_timeout],
    CONVERT(varchar(23), s.modify_date, 126)                    AS [modified_at],
    -- The replication roles, because a linked server that is also a publisher
    -- or a distributor is maintained by the replication topology rather than by
    -- whoever created it, and removing it is not the same conversation.
    CAST(s.is_publisher AS bit)                                 AS [is_publisher],
    CAST(s.is_subscriber AS bit)                                AS [is_subscriber],
    CAST(s.is_distributor AS bit)                               AS [is_distributor]
FROM sys.servers AS s
WHERE s.is_linked = 1
ORDER BY s.name
OPTION (RECOMPILE, MAXDOP 1);

/* Who goes through each door. local_principal is NULL for the default mapping,
   which is the row that applies to everyone without one of their own. */
SELECT
    s.name                                                      AS [linked_server],
    -- NULL here is not missing data: local_principal_id = 0 is the catalogue's
    -- way of saying "everybody else", and naming it as such is the difference
    -- between a row a reader skips and a row a reader stops on.
    CASE WHEN ll.local_principal_id = 0 THEN N'(all other logins)'
         ELSE SUSER_NAME(ll.local_principal_id) END             AS [local_principal],
    CAST(ll.uses_self_credential AS bit)                         AS [uses_self_credential],
    -- The identity presented at the far end when uses_self_credential is 0.
    -- The password stored beside it is not readable and is not collected.
    ll.remote_name                                              AS [remote_name],
    CONVERT(varchar(23), ll.modify_date, 126)                   AS [modified_at]
FROM sys.linked_logins AS ll
JOIN sys.servers AS s ON s.server_id = ll.server_id
WHERE s.is_linked = 1
ORDER BY s.name, ll.local_principal_id
OPTION (RECOMPILE, MAXDOP 1);

/* Server-scoped triggers, LOGON and DDL alike. The definition is not read
   here: 70.schema/080.modules.sql is where module source belongs, and a
   server trigger's body can embed the literals its author used. */
SELECT
    t.name                                                      AS [name],
    t.parent_class_desc                                         AS [scope],
    t.type_desc                                                 AS [implementation],
    CAST(t.is_disabled AS bit)                                  AS [is_disabled],
    CONVERT(varchar(23), t.create_date, 126)                    AS [created_at],
    CONVERT(varchar(23), t.modify_date, 126)                    AS [modified_at],
    -- Which event set it fires on, as the catalogue records it. A LOGON
    -- trigger and a DDL trigger have the same shape here and entirely
    -- different consequences when they misbehave.
    STUFF((SELECT N', ' + te.type_desc
             FROM sys.server_trigger_events AS te
            WHERE te.object_id = t.object_id
            ORDER BY te.type_desc
            FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, N'')
                                                                AS [events]
FROM sys.server_triggers AS t
WHERE t.is_ms_shipped = 0
ORDER BY t.name
OPTION (RECOMPILE, MAXDOP 1);

/* Credentials, by the identity they present. target_type distinguishes a
   plain credential from one bound to a cryptographic provider. */
SELECT
    c.name                                                      AS [name],
    c.credential_identity                                       AS [identity],
    c.target_type                                               AS [target_type],
    CONVERT(varchar(23), c.create_date, 126)                    AS [created_at],
    CONVERT(varchar(23), c.modify_date, 126)                    AS [modified_at],
    -- Which Agent proxies run under it, because a credential nobody uses is
    -- inert and one behind a proxy is an execution path. 50.agent/020.job-steps
    -- says which steps use which proxy.
    (SELECT COUNT(*) FROM msdb.dbo.sysproxies AS p
      WHERE p.credential_id = c.credential_id)                  AS [agent_proxies]
FROM sys.credentials AS c
ORDER BY c.name
OPTION (RECOMPILE, MAXDOP 1);

/* Audits and their server-level specifications, joined so one row says both
   that an audit exists and whether anything feeds it. An audit with no
   specification writes nothing, which looks identical to no audit at all in
   any report that only counts audits. */
SELECT
    a.name                                                      AS [audit],
    a.type_desc                                                 AS [destination],
    CAST(a.is_state_enabled AS bit)                             AS [is_running],
    -- What the engine does when it cannot write the audit. SHUTDOWN means a
    -- full audit destination stops the instance, which is the correct setting
    -- in a regulated estate and a self-inflicted outage everywhere else.
    a.on_failure_desc                                           AS [on_failure],
    a.queue_delay                                               AS [queue_delay_ms],
    CONVERT(varchar(23), a.create_date, 126)                    AS [created_at],
    s.name                                                      AS [specification],
    CAST(s.is_state_enabled AS bit)                             AS [specification_running],
    (SELECT COUNT(*) FROM sys.server_audit_specification_details AS d
      WHERE d.server_specification_id = s.server_specification_id)
                                                                AS [action_groups]
FROM sys.server_audits AS a
LEFT JOIN sys.server_audit_specifications AS s ON s.audit_guid = a.audit_guid
ORDER BY a.name, s.name
OPTION (RECOMPILE, MAXDOP 1);
