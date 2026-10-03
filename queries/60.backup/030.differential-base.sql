-- @scope:       database
-- @resultsets:  root:object, files:array
-- @permissions: CONNECT, VIEW SERVER STATE, MSDB READ
-- @timeout:     30
-- @min_version: 13.0.5026
-- @profiles:    space
--
-- Runs once per user database, with the connection context switched to it.
--
-- How big a differential backup of this database would be if it were taken
-- now, and which full backup it would be taken against.
--
-- WHY THIS COLLECTOR EXISTS. 60.backup/010.history says what was backed up and
-- how big each backup was. It cannot say what a differential WOULD weigh on a
-- database that never takes one, which is the question asked when somebody
-- proposes replacing a nightly full with a weekly full and daily
-- differentials, and the answer usually offered is a guess. The engine keeps
-- the answer: the differential changed map (DCM) marks every extent written
-- since the last full backup, and a differential copies exactly those extents.
-- sys.dm_db_file_space_usage.modified_extent_page_count reads that bitmap.
-- Found on a client instance in October 2026: a database of 3 TB showed 5 GB
-- of modified extents eighteen hours after its full backup, 0.17 percent,
-- which put a differential at roughly 30 to 40 GB by the sixth day of a week.
-- One query settled a discussion that had been running on intuition.
--
-- THE FLOOR IS SQL SERVER 2016 SP2 (13.0.5026), not 2017. The reference gives
-- modified_extent_page_count as "SQL Server 2016 (13.x) SP2 and later", and
-- 20.databases/023.log-vlf uses the same build for the same service pack.
-- The column was verified on 17.0.4065.4 only; the 2016 SP2 floor is the
-- documentation's, not a measurement.
--
-- MEASURED ON 17.0.4065.4, against a scratch database of 22 784 allocated
-- pages (178 MB):
--
--   - modified_extent_page_count times 8 KB is the size of the differential
--     to within a fixed overhead of 1 to 2 MB: 2 808 pages predicted 22.0 MB
--     and backupset.backup_size recorded 23.1 MB; 25 368 pages predicted
--     198.2 MB and the backup recorded 200.1 MB. backup_size is the
--     uncompressed size, and compression shrinks the file on disk below it;
--   - the count is a number of pages in modified EXTENTS, always a multiple
--     of eight, so a scattered change costs a whole extent per row:
--     updating 200 rows, one in every thousand, marked 1 680 pages, 13 MB of
--     differential for 200 rows;
--   - an index rebuild marks the whole new index as modified. One rebuild of
--     the only table took the count from 2 808 to 25 368 pages, and the next
--     differential (200.1 MB) was larger than the full it was based on
--     (180.1 MB). Nightly index maintenance makes differentials nearly as big
--     as fulls, whatever the application writes;
--   - a full backup does not leave the count at zero: 88 pages read as
--     modified a second after the full, the backup's own bookkeeping;
--   - a differential does not reset the count. Differentials are cumulative
--     until the next full, so the count only grows between two fulls;
--   - a COPY_ONLY full changes neither the count nor the base. That is what
--     COPY_ONLY is for, and why a full that is NOT copy-only, taken by a VSS
--     or storage snapshot agent outside the DBA's schedule, silently moves
--     the base of every later differential onto a backup the native restore
--     chain may not hold;
--   - differential_base_guid equals msdb.dbo.backupset.backup_set_uuid of the
--     base full, and differential_base_lsn its first_lsn. That is the join
--     below, and the reason MSDB READ is declared;
--   - before the database has ever had a full backup, the base is NULL and
--     modified_extent_page_count is 0. Zero there does not mean "nothing
--     changed", it means there is nothing to be differential against, and
--     base.time IS NULL is what tells the two apart.
--
-- THE BASE MAY NOT BE IN THIS MSDB, and base.backup_found = 0 says so. msdb
-- is per instance, so a base taken on another instance (the database was
-- restored here from a full taken elsewhere, or the base was taken on another
-- replica of an availability group) has no row here, and neither has a base
-- whose history the msdb purge has already removed.
--
-- ON A SECONDARY REPLICA, documented and not measured: no availability group
-- could be built for this collector. Before SQL Server 2025 a secondary
-- replica takes only COPY_ONLY fulls and log backups, so the differential base
-- is always set by a full taken on the primary; SQL Server 2025 allows full
-- and differential backups on a secondary. What sys.dm_db_file_space_usage
-- returns on a readable secondary is not documented. Read the figure from the
-- primary's collection, and treat a secondary's as unverified.
--
-- WHAT THE COUNT DOES NOT COVER. The view has a row per ROWS data file only.
-- A FILESTREAM or memory-optimized filegroup is copied by a differential too,
-- from its own containers, and counts.filestream_files says when that part
-- exists and the projection is therefore a floor. On a read-only database the
-- reference says the bitmap overstates what changed and the base is kept in
-- master; is_read_only is projected for that reason.
--
-- THE CLOCK OF differential_base_time IS NOT ESTABLISHED. It matched
-- backupset.backup_finish_date to the second on the lab, but the lab runs in
-- UTC, where local and UTC times agree. base.hours_since is computed against
-- GETDATE(), like the msdb dates in 010.history; base.finish, which is on the
-- local clock, is projected beside it so a disagreement can be seen.
--
-- COST. The view reads the allocation bitmaps, never the data pages: on the
-- lab the read took under a millisecond. VIEW SERVER STATE (VIEW SERVER
-- PERFORMANCE STATE from SQL Server 2022) is required, and its absence is
-- loud: measured, Msg 300 and no rows, not an empty result.
--
-- NO JUDGEMENT IS APPLIED, as in 010.history. Whether a differential schedule
-- would pay depends on the modification rate over the period between fulls,
-- which this single reading and the full backups in 010.history together let
-- the analysis estimate.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @now datetime = GETDATE();

WITH data_files AS (
    SELECT f.file_id,
           f.differential_base_lsn,
           f.differential_base_guid,
           f.differential_base_time,
           u.allocated_extent_page_count,
           u.modified_extent_page_count
    FROM sys.database_files AS f
    LEFT JOIN sys.dm_db_file_space_usage AS u ON u.file_id = f.file_id
    WHERE f.type = 0
),
base AS (
    -- The base of the primary data file, which every differential needs.
    -- Other files can carry another base after a file backup, and
    -- counts.distinct_bases says when they do.
    SELECT d.differential_base_lsn, d.differential_base_guid, d.differential_base_time
    FROM data_files AS d
    WHERE d.file_id = 1
)
SELECT
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    CASE WHEN DATABASEPROPERTYEX(DB_NAME(), 'Updateability') = 'READ_ONLY'
         THEN 1 ELSE 0 END                                      AS [is_read_only],
    CONVERT(varchar(23), b.differential_base_time, 126)         AS [base.time],
    CAST(DATEDIFF(minute, b.differential_base_time, @now) / 60.0 AS decimal(12,1))
                                                                AS [base.hours_since],
    CAST(b.differential_base_lsn AS varchar(30))                AS [base.lsn],
    CAST(b.differential_base_guid AS varchar(36))               AS [base.guid],
    CASE WHEN bs.backup_set_uuid IS NULL THEN 0 ELSE 1 END      AS [base.backup_found],
    CASE bs.type WHEN 'D' THEN 'full'
                 WHEN 'F' THEN 'file'
                 WHEN 'P' THEN 'partial'
                 ELSE bs.type END                               AS [base.type],
    CONVERT(varchar(19), bs.backup_finish_date, 126)            AS [base.finish],
    CAST(bs.is_copy_only AS bit)                                AS [base.is_copy_only],
    CAST(bs.is_snapshot AS bit)                                 AS [base.is_snapshot],
    CAST(bs.backup_size / 1048576.0 AS decimal(18,1))           AS [base.backup_mb],
    -- A virtual device is a third-party agent or a VSS requestor writing
    -- through the backup API, not a file the DBA's job wrote.
    (SELECT TOP (1)
            CASE bmf.device_type
                 WHEN 2   THEN 'disk'
                 WHEN 5   THEN 'tape'
                 WHEN 7   THEN 'virtual_device'
                 WHEN 9   THEN 'azure_storage'
                 WHEN 105 THEN 'permanent_disk'
                 WHEN 106 THEN 'permanent_tape'
                 WHEN 107 THEN 'permanent_virtual_device'
                 ELSE CAST(bmf.device_type AS varchar(10)) END
     FROM msdb.dbo.backupmediafamily AS bmf
     WHERE bmf.media_set_id = bs.media_set_id
     ORDER BY bmf.family_sequence_number)                       AS [base.device_type],
    (SELECT ISNULL(SUM(d.allocated_extent_page_count), 0) FROM data_files AS d)
                                                                AS [extents.allocated_pages],
    (SELECT ISNULL(SUM(d.modified_extent_page_count), 0) FROM data_files AS d)
                                                                AS [extents.modified_pages],
    (SELECT CAST(ISNULL(SUM(d.allocated_extent_page_count), 0) / 128.0 AS decimal(18,1))
       FROM data_files AS d)                                    AS [extents.allocated_mb],
    -- What a differential taken now would copy, without its fixed overhead
    -- and before compression.
    (SELECT CAST(ISNULL(SUM(d.modified_extent_page_count), 0) / 128.0 AS decimal(18,1))
       FROM data_files AS d)                                    AS [extents.modified_mb],
    (SELECT CAST(100.0 * SUM(d.modified_extent_page_count)
                 / NULLIF(SUM(d.allocated_extent_page_count), 0) AS decimal(6,2))
       FROM data_files AS d)                                    AS [extents.modified_pct],
    (SELECT COUNT(*) FROM data_files)                           AS [counts.data_files],
    (SELECT COUNT(*) FROM data_files AS d
      WHERE d.differential_base_guid IS NULL)                   AS [counts.files_without_base],
    (SELECT COUNT(DISTINCT d.differential_base_guid) FROM data_files AS d)
                                                                AS [counts.distinct_bases],
    (SELECT COUNT(*) FROM sys.database_files AS f WHERE f.type = 2)
                                                                AS [counts.filestream_files]
FROM (SELECT 1 AS one) AS anchor
LEFT JOIN base AS b ON 1 = 1
LEFT JOIN msdb.dbo.backupset AS bs ON bs.backup_set_uuid = b.differential_base_guid
OPTION (RECOMPILE, MAXDOP 1);

/* One row per ROWS data file. Most databases have one, and the root carries
   the totals; the rows matter when the files disagree, after a file or
   filegroup backup moved the base of some of them. */
SELECT
    f.file_id                                                   AS [file_id],
    f.name                                                      AS [logical_name],
    f.data_space_id                                             AS [filegroup_id],
    u.total_page_count                                          AS [total_pages],
    u.allocated_extent_page_count                               AS [allocated_pages],
    u.modified_extent_page_count                                AS [modified_pages],
    CAST(100.0 * u.modified_extent_page_count
         / NULLIF(u.allocated_extent_page_count, 0) AS decimal(6,2))
                                                                AS [modified_pct],
    CAST(f.differential_base_lsn AS varchar(30))                AS [base_lsn],
    CAST(f.differential_base_guid AS varchar(36))               AS [base_guid],
    CONVERT(varchar(23), f.differential_base_time, 126)         AS [base_time]
FROM sys.database_files AS f
LEFT JOIN sys.dm_db_file_space_usage AS u ON u.file_id = f.file_id
WHERE f.type = 0
ORDER BY f.file_id
OPTION (RECOMPILE, MAXDOP 1);
