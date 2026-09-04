-- @scope:       database
-- @resultsets:  root:object, publications:array, articles:array, subscriptions:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     120
-- @widened:     replication
--
-- What a publisher publishes, and on what terms.
--
-- THE READ IS DEFERRED THROUGH sp_executesql, AND THAT IS THE WHOLE
-- MECHANISM. dbo.syspublications exists only in a published database. A direct
-- SELECT against it raises Msg 208 when it is absent, at compile time, where a
-- TRY/CATCH at the same level cannot catch it and the batch dies with its
-- result sets half emitted. Inside sp_executesql the same error is a runtime
-- one and is caught. Measured, both ways, in docs/verification-replication-guard.md.
--
-- THERE IS NO OBJECT_ID TEST, AND THERE USED TO BE. OBJECT_ID answers "may I
-- see this" rather than "does this exist": it returns NULL for an object the
-- login has no rights on. Guarding with it turned error 229 into an empty
-- result set, so a publisher with publications was recorded as one with none.
--
-- IMMEDIATE_SYNC AND ALLOW_ANONYMOUS ARE WHY THIS FILE EXISTS. Together they
-- make the distribution database keep every command for the full retention
-- period whether or not a subscriber has taken it, which is the ordinary
-- explanation for a publisher log that will not truncate. Read beside
-- 20.databases/024.log-stats.sql, they turn log_reuse_wait = REPLICATION from
-- a symptom into a cause.
--
-- THE HOMONYM TRAP. syspublications, sysarticles and syssubscriptions exist as
-- tables here and as views with different columns in a distribution database.
-- This file's projections are written for the publication database; a column
-- list that happens to compile in the other one is a silent wrong answer.
--
-- SCHEMA_OPTION IS WHAT SAYS WHERE THE SUBSCRIBER'S INDEXES CAME FROM. It is
-- the bitmask the Snapshot Agent obeys when it recreates an article, and the
-- archive had no column for it until an audit in September 2026 had to ask the
-- client by email. The answer inverted the recommendation: with the
-- nonclustered bit off, every index on the subscriber is hand-made, nothing
-- keeps it aligned with the publisher, and a reinitialisation drops all of
-- them. That is a paragraph of the report, and it should come from the
-- archive rather than from correspondence.
--
-- The raw value is projected beside the decoded bits, in the 0x form the
-- documentation of sp_addarticle uses, because the bits decoded here are six
-- of more than thirty and the next question is always about a seventh. The
-- conversion to bigint is not decoration either: the mask reaches 0x800000000,
-- which is past what an int holds.
--
-- NO PASSWORD COLUMN IS PROJECTED. syspublications carries ftp_password.
-- Projections stay explicit for that reason and must never drift to SELECT *.
--
-- SQL Server 2012 is the floor. Nothing here is newer.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @applies bit = 0, @collected bit = 1, @err int = 0, @msg nvarchar(2048) = N'';

SELECT @applies = CONVERT(bit, d.is_published)
FROM sys.databases AS d
WHERE d.database_id = DB_ID()
OPTION (RECOMPILE, MAXDOP 1);

DECLARE @pub TABLE (
    [name] sysname, [status] tinyint, [repl_freq] tinyint, [sync_method] tinyint,
    [retention] int, [allow_push] bit, [allow_pull] bit, [allow_anonymous] bit,
    [immediate_sync] bit, [independent_agent] bit, [allow_sync_tran] bit,
    [allow_queued_tran] bit, [pubid] int);

DECLARE @art TABLE (
    [artid] int, [name] sysname, [dest_table] sysname, [objid] int, [pubid] int,
    [schema_option] varbinary(8) NULL);

DECLARE @sub TABLE (
    [artid] int, [srvid] smallint, [srvname] sysname NULL, [dest_db] sysname,
    [status] tinyint, [sync_type] tinyint, [subscription_type] int);

IF @applies = 1
BEGIN
    BEGIN TRY
        INSERT INTO @pub
        EXEC sys.sp_executesql N'
            SELECT p.name, p.status, p.repl_freq, p.sync_method, p.retention,
                   p.allow_push, p.allow_pull, p.allow_anonymous,
                   p.immediate_sync, p.independent_agent, p.allow_sync_tran,
                   p.allow_queued_tran, p.pubid
            FROM dbo.syspublications AS p
            OPTION (RECOMPILE, MAXDOP 1)';

        INSERT INTO @art
        EXEC sys.sp_executesql N'
            SELECT a.artid, a.name, a.dest_table, a.objid, a.pubid,
                   a.schema_option
            FROM dbo.sysarticles AS a
            OPTION (RECOMPILE, MAXDOP 1)';

        INSERT INTO @sub
        EXEC sys.sp_executesql N'
            SELECT s.artid, s.srvid, s.srvname, s.dest_db, s.status,
                   s.sync_type, s.subscription_type
            FROM dbo.syssubscriptions AS s
            OPTION (RECOMPILE, MAXDOP 1)';
    END TRY
    BEGIN CATCH
        SELECT @collected = 0, @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH
END

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)     AS [collected_at],
       CONVERT(int, @applies)                       AS [applies],
       CONVERT(int, @collected)                     AS [collected],
       @err                                         AS [error_number],
       NULLIF(@msg, N'')                            AS [error_message],
       (SELECT COUNT(*) FROM @pub)                  AS [counts.publications],
       (SELECT COUNT(*) FROM @art)                  AS [counts.articles],
       (SELECT COUNT(*) FROM @sub)                  AS [counts.subscriptions]
OPTION (RECOMPILE, MAXDOP 1);

SELECT p.[name], p.[pubid], p.[status], p.[repl_freq], p.[sync_method],
       p.[retention], CONVERT(int, p.[allow_push])         AS [allow_push],
       CONVERT(int, p.[allow_pull])                        AS [allow_pull],
       CONVERT(int, p.[allow_anonymous])                   AS [allow_anonymous],
       CONVERT(int, p.[immediate_sync])                    AS [immediate_sync],
       CONVERT(int, p.[independent_agent])                 AS [independent_agent],
       CONVERT(int, p.[allow_sync_tran])                   AS [allow_sync_tran],
       CONVERT(int, p.[allow_queued_tran])                 AS [allow_queued_tran]
FROM @pub AS p ORDER BY p.[name]
OPTION (RECOMPILE, MAXDOP 1);

SELECT a.[artid], a.[name], a.[dest_table], a.[objid], a.[pubid],
       CONVERT(varchar(18), a.[schema_option], 1)                  AS [schema_option],
       /* Decoded because a bitmask nobody decodes is a number nobody reads.
          The six bits are the ones that decide what the subscriber's schema
          owes to replication and what it owes to somebody's hand. */
       CASE WHEN a.[schema_option] IS NULL THEN NULL
            WHEN (CONVERT(bigint, a.[schema_option]) & 0x10) > 0 THEN 1
            ELSE 0 END                                      AS [copies_clustered_index],
       CASE WHEN a.[schema_option] IS NULL THEN NULL
            WHEN (CONVERT(bigint, a.[schema_option]) & 0x40) > 0 THEN 1
            ELSE 0 END                                      AS [copies_nonclustered_indexes],
       CASE WHEN a.[schema_option] IS NULL THEN NULL
            WHEN (CONVERT(bigint, a.[schema_option]) & 0x80) > 0 THEN 1
            ELSE 0 END                                      AS [copies_primary_key],
       CASE WHEN a.[schema_option] IS NULL THEN NULL
            WHEN (CONVERT(bigint, a.[schema_option]) & 0x200) > 0 THEN 1
            ELSE 0 END                                      AS [copies_foreign_keys],
       CASE WHEN a.[schema_option] IS NULL THEN NULL
            WHEN (CONVERT(bigint, a.[schema_option]) & 0x1000) > 0 THEN 1
            ELSE 0 END                                      AS [copies_collation],
       CASE WHEN a.[schema_option] IS NULL THEN NULL
            WHEN (CONVERT(bigint, a.[schema_option]) & 0x2000) > 0 THEN 1
            ELSE 0 END                                      AS [copies_extended_properties]
FROM @art AS a ORDER BY a.[pubid], a.[name]
OPTION (RECOMPILE, MAXDOP 1);

SELECT s.[artid], s.[srvid], s.[srvname], s.[dest_db], s.[status],
       s.[sync_type], s.[subscription_type]
FROM @sub AS s ORDER BY s.[artid]
OPTION (RECOMPILE, MAXDOP 1);
