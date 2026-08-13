-- @scope:         database
-- @resultsets:    root:object, modules:array
-- @permissions:   CONNECT, VIEW ANY DEFINITION
-- @requires_flag: object_definitions
-- @writer:        object-definitions
-- @timeout:       300
--
-- The source of every view, stored procedure, function and trigger, one file
-- each.
--
-- Why this collector exists. The corpus counts views and procedures in
-- 010.objects.sql and has never exported a line of their code. An audit that
-- finds a plan scanning a table learns nothing about why unless it can read the
-- view underneath it, and an undocumented trigger is exactly the thing nobody
-- mentions in the interview and everybody discovers afterwards.
--
-- WHY THIS ONE IS BEHIND A FLAG when the rest of 70.schema is not. A module
-- body is code the client wrote. It carries literals, table names, the
-- addresses of linked servers, and now and then a password in clear inside an
-- OPENQUERY or an EXECUTE AS. That is the same class of content as session text
-- and plan XML, so it follows the same rule the whole project follows: what can
-- carry the client's own data is opt-in, and the archive says in MANIFEST.txt
-- that it holds it. The default run collects none of this.
--
-- WHY A @writer. A procedure of 200 KB inside a JSON array is a file no editor
-- opens comfortably and from which the reader has to extract each module by
-- hand — for the one artifact they will certainly want to open in an editor.
-- The writer produces one .sql per module plus an _index.json.
--
-- TWO CAPS, AND NEITHER IS SILENT. At most 2000 modules per database, keeping
-- the most recently modified, and at most 1 MiB per module. Past either, the
-- definition is NULLed by a CONDITIONAL PROJECTION — never a WHERE — so the row
-- survives with its name, its size and its dates, and the writer records a
-- named omission. A module dropped from the result set would be a module the
-- archive never admits exists.
--
-- ENCRYPTED MODULES ARE A THIRD, DIFFERENT CASE. WITH ENCRYPTION makes
-- sys.sql_modules.definition NULL, and that NULL means "the server will not
-- tell you", not "this was too big" and not "there is nothing here". The three
-- are reported apart, because a reader who cannot tell them apart will read the
-- wrong one as a collector failure.
--
-- NO JUDGEMENT IS APPLIED. Nothing here is called dead code, badly written or
-- unused. A procedure last modified in 2011 may be the one that closes the
-- financial year. Which modules matter needs the workload and the calendar.
--
-- SQL Server 2012 is the floor. Not collected for that reason:
--   sys.sql_modules.is_inlineable         (2019, scalar UDF inlining)
--   sys.sql_modules.uses_native_compilation (2014, In-Memory OLTP)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

/* modules_total counts every module of the database, before either cap. Beside
   modules_listed it is what says how much code this archive does not carry. */
SELECT DB_NAME()                                                  AS [database],
       CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       (SELECT COUNT(*) FROM sys.sql_modules AS m
          JOIN sys.objects AS o ON o.object_id = m.object_id
         WHERE o.is_ms_shipped = 0)                               AS [modules_total],
       (SELECT COUNT(*) FROM sys.sql_modules AS m
          JOIN sys.objects AS o ON o.object_id = m.object_id
         WHERE o.is_ms_shipped = 0 AND m.definition IS NULL)      AS [modules_without_definition],
       (SELECT COUNT(*) FROM sys.views AS v WHERE v.is_ms_shipped = 0)  AS [counts.views],
       (SELECT COUNT(*) FROM sys.objects AS o
         WHERE o.is_ms_shipped = 0 AND o.type = 'P')              AS [counts.procedures],
       (SELECT COUNT(*) FROM sys.objects AS o
         WHERE o.is_ms_shipped = 0 AND o.type IN ('FN','IF','TF')) AS [counts.functions],
       (SELECT COUNT(*) FROM sys.triggers AS tr
         WHERE tr.is_ms_shipped = 0 AND tr.parent_class = 1)      AS [counts.triggers],
       /* Both caps, written out, so the exported corpus states them where a DBA
          reading the SQL will see them. maxModules and maxModuleBytes on the Go
          side are held to these two numbers by a test. */
       2000                                                       AS [caps.modules],
       1048576                                                    AS [caps.module_bytes]
OPTION (RECOMPILE, MAXDOP 1);

/* One row per module, whether or not its definition came back. The rank and the
   count travel with every row: rank 2417 of 3900 is what tells a reader the
   file is absent by decision, and not because the catalog holds nothing.

   modify_date DESC keeps the code that has been touched most recently, which is
   the code an audit is most likely to be about. object_id DESC is the stable
   tie-break, for the reason plan_id is one in 80.workload/021: a whole
   deployment can share one modify_date to the second, and without a tie-break
   two collections of an unchanged database can keep different modules with
   nothing anywhere saying so. */
WITH ranked AS (
    SELECT m.object_id,
           ROW_NUMBER() OVER (ORDER BY o.modify_date DESC, o.object_id DESC) AS module_rank,
           COUNT(*) OVER ()                                                  AS module_count
    FROM sys.sql_modules AS m
    JOIN sys.objects     AS o ON o.object_id = m.object_id
    WHERE o.is_ms_shipped = 0
)
SELECT SCHEMA_NAME(o.schema_id)                                   AS [schema],
       o.name                                                     AS [name],
       o.type_desc                                                AS [type],
       o.object_id                                                AS [object_id],
       /* NULL for an encrypted module, for one past either cap, and for one the
          catalog simply has nothing for. The writer tells the three apart from
          the columns beside it — is_encrypted, module.rank and
          definition_bytes — and names the reason in _index.json. Guessing from
          the NULL alone would state a false fact about the server. */
       CASE WHEN DATALENGTH(m.definition) <= 1048576
             AND r.module_rank <= 2000
            THEN m.definition END                                 AS [definition],
       /* Projected even when the definition is not: the reader learns how large
          the code they cannot read was. */
       DATALENGTH(m.definition)                                   AS [definition_bytes],
       CAST(ISNULL(OBJECTPROPERTY(o.object_id, 'IsEncrypted'), 0) AS int)
                                                                  AS [is_encrypted],
       r.module_rank                                              AS [module.rank],
       r.module_count                                             AS [module.count],
       o.create_date                                              AS [create_date],
       o.modify_date                                              AS [modify_date],
       CAST(m.uses_ansi_nulls AS int)                             AS [uses_ansi_nulls],
       CAST(m.uses_quoted_identifier AS int)                      AS [uses_quoted_identifier],
       CAST(m.is_schema_bound AS int)                             AS [is_schema_bound],
       CAST(m.is_recompiled AS int)                               AS [is_recompiled],
       /* WITH EXECUTE AS changes who the module's statements run as, which is a
          privilege question an audit asks and which no other collector reports. */
       USER_NAME(m.execute_as_principal_id)                       AS [execute_as]
FROM       sys.sql_modules AS m
JOIN       sys.objects     AS o ON o.object_id = m.object_id
JOIN       ranked          AS r ON r.object_id = m.object_id
WHERE o.is_ms_shipped = 0
ORDER BY r.module_rank
OPTION (RECOMPILE, MAXDOP 1);
