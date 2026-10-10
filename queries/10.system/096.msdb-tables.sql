-- @scope:       instance
-- @resultsets:  root:object, tables:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     60
-- @profiles:    space
--
-- What fills msdb: its largest tables by reserved space, with used space and
-- rows.
--
-- Why this collector exists. On a client instance in October 2026 msdb
-- weighed 9.8 GB, and the archive could say how big the files were and not
-- which table held the space: backup history (backupset, backupfile,
-- backupmediafamily), job history (sysjobhistory), Database Mail (sysmail_*),
-- log shipping monitoring, maintenance plan logs and Tuning Advisor sessions
-- are all candidates, and each has a different purge. The report had to name
-- a "first suspect" without proof.
--
-- WHY AN INSTANCE-SCOPED FILE AND NOT THE DATABASE-SCOPED SIZE READS. The
-- database-scoped collectors never run against msdb, by the decision recorded
-- on widenSystem in collect/runner.go: an index or statistics inventory of
-- msdb is noise in a report about the application, so msdb is widened in only
-- for the collectors that declare @widened: system_databases. Widening it for
-- 20.databases/020.properties, whose largest_objects would answer this, would
-- offer msdb to that file's every other area as well. This file reads msdb's
-- catalogs through three-part names instead, like 095.master-user-objects does
-- for master, and one pass covers msdb whatever databases the run selected.
-- None of the existing msdb readers fits: 60.backup and 50.agent read the
-- rows of the history tables, not their size, and the question here is which
-- of them, or something else, is the large one.
--
-- CATALOG VIEWS, NOT sys.dm_db_partition_stats. Two reasons. The DMV times
-- out whole when any one table is under a schema-modification lock, while
-- sys.allocation_units reads past it (measured for 20.databases/025, see its
-- comment). And the DMV would add VIEW SERVER STATE to what this file needs,
-- where the catalog views ask only for the metadata visibility that VIEW ANY
-- DEFINITION gives, as they do for 095.master-user-objects. sys.partitions
-- is still read for its rows column, which is the one part that waits on such
-- a lock; LOCK_TIMEOUT bounds the wait and the TRY below reports it.
--
-- THE READS SIT IN A TRY, because no preflight probe vouches for them: a
-- login with no user in msdb, guest disabled there, is refused msdb's catalog
-- views like its tables (collect/msdb_reads_test.go). The error lands in
-- errors.tables, which the pipeline reads back as a partial collection
-- (collect/partial.go), and the root is emitted whatever happened.
--
-- PAGES ARE ATTRIBUTED TO THEIR OWNER. Database Mail queues its messages in
-- Service Broker queues, whose rows live in internal tables (queue_messages_*)
-- that a list of user tables never shows. An internal table's pages are added
-- to its parent, the queue, through sys.internal_tables, so a mail queue that
-- grew is named as the queue it is.
--
-- EVERY OBJECT IS NAMED, MICROSOFT'S OR NOT, AND is_ms_shipped SAYS WHICH. A
-- table someone created in msdb can carry an application name, and naming
-- only the shipped objects while summing the rest was considered. It was
-- rejected: the archive already names every table of every application
-- database (70.schema/010.objects), and 095.master-user-objects names user
-- objects in master on the same principle, so naming one here discloses
-- nothing of a new kind; and the user table is precisely the case where a
-- name is the finding. A maintenance log installed in msdb (CommandLog), or a
-- Tuning Advisor session table (DTA_*), is not flagged is_ms_shipped, and an
-- aggregate "user objects: 6 GB" would send the reader back to the instance
-- for the one fact that matters. The flag is projected so the analysis can
-- tell the two apart without a list of Microsoft's names.
--
-- TOP (200) BY RESERVED PAGES, object_id breaking ties, objects holding no
-- page left out. msdb holds a few hundred tables on a default install, most of
-- them empty, so the cap is a bound on a pathological msdb rather than a cut
-- of an ordinary one (not measured: the lab could not be reached when this
-- file was written). A row costs about a hundred bytes. The root carries the
-- totals over every object, so the cap is never taken for the population:
-- objects_total against objects_listed says whether it cut, and
-- reserved_kb_total against the sum of the listed rows how much the tail holds.
--
-- NO JUDGEMENT IS APPLIED. A large sysjobhistory is a retention setting, a
-- large backupset a missing sp_delete_backuphistory, and both are legitimate
-- choices; which one applies is the analysis layer's call.
--
-- SQL Server 2012 is the floor. Nothing read here is newer.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @err int = 0, @msg nvarchar(2048) = N'';
DECLARE @listing_cap int = 200;

DECLARE @sizes TABLE (
    [object_id]      int PRIMARY KEY,
    [reserved_pages] bigint,
    [used_pages]     bigint,
    [rows]           bigint);

DECLARE @tables TABLE (
    [object_id]      int,
    [schema]         sysname NULL,
    [name]           sysname NULL,
    [type]           nvarchar(60) NULL,
    [is_ms_shipped]  bit NULL,
    [reserved_pages] bigint,
    [used_pages]     bigint,
    [rows]           bigint);

BEGIN TRY
    /* Rows are counted once per partition, on its in-row allocation unit, and
       only for the heap or clustered index; the pages of every index and
       allocation unit are summed. */
    INSERT INTO @sizes ([object_id], [reserved_pages], [used_pages], [rows])
    SELECT COALESCE(it.parent_id, p.object_id),
           SUM(au.total_pages),
           SUM(au.used_pages),
           SUM(CASE WHEN au.type = 1 AND p.index_id IN (0, 1) THEN p.rows ELSE 0 END)
    FROM msdb.sys.partitions AS p
    JOIN msdb.sys.allocation_units AS au ON au.container_id = p.partition_id
    LEFT JOIN msdb.sys.internal_tables AS it ON it.object_id = p.object_id
    GROUP BY COALESCE(it.parent_id, p.object_id)
    OPTION (RECOMPILE, MAXDOP 1);

    INSERT INTO @tables
    SELECT TOP (@listing_cap)
           s.object_id,
           sc.name,
           o.name,
           o.type_desc,
           o.is_ms_shipped,
           s.reserved_pages,
           s.used_pages,
           s.rows
    FROM @sizes AS s
    LEFT JOIN msdb.sys.objects AS o ON o.object_id = s.object_id
    LEFT JOIN msdb.sys.schemas AS sc ON sc.schema_id = o.schema_id
    WHERE s.reserved_pages > 0
    ORDER BY s.reserved_pages DESC, s.object_id
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       DB_NAME()                                                  AS [collected_from],
       (SELECT CAST(SUM([reserved_pages]) AS bigint) * 8 FROM @sizes) AS [reserved_kb_total],
       (SELECT CAST(SUM([used_pages]) AS bigint) * 8 FROM @sizes)     AS [used_kb_total],
       (SELECT COUNT(*) FROM @sizes WHERE [reserved_pages] > 0)   AS [objects_total],
       (SELECT COUNT(*) FROM @tables)                             AS [objects_listed],
       @listing_cap                                               AS [listing_cap],
       CASE WHEN @err = 0 THEN 1 ELSE 0 END                       AS [collected.tables],
       @err                                                       AS [errors.tables],
       NULLIF(@msg, N'')                                          AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

SELECT [schema], [name], [type], [is_ms_shipped],
       [reserved_pages] * 8                                       AS [reserved_kb],
       [used_pages] * 8                                           AS [used_kb],
       [rows]
FROM @tables
ORDER BY [reserved_pages] DESC, [object_id]
OPTION (RECOMPILE, MAXDOP 1);
