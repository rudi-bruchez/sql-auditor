-- @scope:         instance
-- @resultsets:    root:object, server_triggers:array
-- @permissions:   CONNECT, VIEW ANY DEFINITION
-- @timeout:       60
-- @requires_flag: object_definitions
-- @discloses:     server_trigger_source
--
-- The source of every server-scoped trigger, LOGON and DDL alike, when the
-- operator has asked for object definitions.
--
-- WHY THIS EXISTS. 030.server-surface.sql lists the server triggers with their
-- events and whether each is enabled, on every run, and stops short of the
-- body on purpose. That answers whether a LOGON trigger exists and not the two
-- questions an audit asks next: what it reads, and whether it can fail. A
-- logon trigger that refers to a table which has since been dropped, or that
-- returns a result set, refuses every connection to the instance, and only the
-- body says whether this one does. Until this file the archive could not
-- answer, and the audit had to ask for the definition afterwards.
--
-- WHY IT SITS BEHIND --include-object-definitions AND NOT IN 030. A server
-- trigger body is code written on the client's side, the same class of content
-- as the module source 70.schema/080.modules.sql exports: it names tables,
-- logins and hosts, and it can embed the literals its author used. So it
-- follows the same decision and the same flag. It cannot live in 080.modules,
-- which runs once per database and reads sys.sql_modules; server triggers live
-- in sys.server_sql_modules, at instance scope, and a per-database collector
-- would export them once per database. The gate is a property of the whole
-- file, so the body could not be added to 030 without hiding the rest of 030
-- behind the flag as well.
--
-- WHY @discloses AS WELL AS THE FLAG. MANIFEST.txt's paragraph on object
-- definitions is latched from the .sql files 080.modules.sql writes, and a run
-- against an instance whose databases hold no module would write none while
-- this file still put a trigger body in the archive. The declaration makes the
-- manifest name this content whenever this file runs.
--
-- THREE STATES OF A DEFINITION, KEPT APART. A Transact-SQL trigger has a row
-- in sys.server_sql_modules and its definition is the text. A trigger created
-- WITH ENCRYPTION has the row and a NULL definition. A CLR trigger has no row
-- there at all; its code is in an assembly. source_state says which, so a NULL
-- is never read as a collection failure. A definition above 1 MiB is NULLed by
-- a conditional projection and said so, the cap 080.modules.sql uses; the row
-- survives with its length.
--
-- execute_as is read from the module because a trigger's identity is half of
-- the question whether it can fail: by default it runs as the caller, so a
-- logon trigger that reads a table works for a sysadmin and refuses everybody
-- else. NULL in the catalog means CALLER, -2 means OWNER, anything else names
-- a server principal.
--
-- Measured on 17.0.4065.4 with a server DDL trigger on CREATE_DATABASE,
-- created for the purpose and dropped seconds later: with the flag the body
-- came back whole and source_state read sql; without it the file did not run.
-- No LOGON trigger was created on the shared lab instance to measure this,
-- because one that fails locks out every other session; the two kinds share
-- this catalog and this projection.
--
-- NO JUDGEMENT IS APPLIED. Whether a body can fail is the reader's work.
--
-- SQL Server 2012 is the floor. sys.server_sql_modules,
-- sys.server_triggers and sys.server_trigger_events predate every supported
-- version.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @err int = 0, @msg nvarchar(2048) = N'';

DECLARE @triggers TABLE (
    [name]              sysname,
    [implementation]    nvarchar(60),
    [is_disabled]       bit,
    [events]            nvarchar(max) NULL,
    [execute_as]        sysname NULL,
    [source_state]      varchar(16),
    [definition_length] int NULL,
    [definition]        nvarchar(max) NULL);

BEGIN TRY
    INSERT INTO @triggers
    SELECT
        t.name,
        t.type_desc,
        CAST(t.is_disabled AS bit),
        STUFF((SELECT N', ' + te.type_desc
                 FROM sys.server_trigger_events AS te
                WHERE te.object_id = t.object_id
                ORDER BY te.type_desc
                FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, N''),
        CASE WHEN m.object_id IS NULL             THEN NULL
             WHEN m.execute_as_principal_id IS NULL THEN N'CALLER'
             WHEN m.execute_as_principal_id = -2  THEN N'OWNER'
             ELSE ISNULL(SUSER_NAME(m.execute_as_principal_id),
                         CONVERT(sysname, m.execute_as_principal_id)) END,
        CASE WHEN m.object_id IS NULL                  THEN 'clr'
             WHEN m.definition IS NULL                 THEN 'encrypted'
             WHEN DATALENGTH(m.definition) > 1048576   THEN 'above_cap'
             ELSE 'sql' END,
        LEN(m.definition),
        CASE WHEN DATALENGTH(m.definition) > 1048576 THEN NULL
             ELSE m.definition END
    FROM sys.server_triggers AS t
    LEFT JOIN sys.server_sql_modules AS m ON m.object_id = t.object_id
    WHERE t.is_ms_shipped = 0
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH;

SELECT
    CONVERT(sysname, SERVERPROPERTY('ServerName'))              AS [instance],
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    (SELECT COUNT(*) FROM @triggers)                            AS [counts.server_triggers],
    (SELECT COUNT(*) FROM @triggers WHERE [source_state] = 'sql')
                                                                AS [counts.definitions],
    1048576                                                     AS [definition_cap_bytes],
    CASE WHEN @err = 0 THEN 1 ELSE 0 END                        AS [collected],
    @err                                                        AS [error_number],
    NULLIF(@msg, N'')                                           AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

SELECT t.[name], t.[implementation], t.[is_disabled], t.[events],
       t.[execute_as], t.[source_state], t.[definition_length], t.[definition]
FROM @triggers AS t
ORDER BY t.[name]
OPTION (RECOMPILE, MAXDOP 1);
