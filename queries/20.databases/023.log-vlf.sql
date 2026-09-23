-- @scope:       database
-- @resultsets:  root:object, vlf_per_file:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
-- @profiles:    space
--
-- Runs once per user database, with the connection context switched to it.
--
-- Virtual log file detail for the transaction log — the space.vlf_count that
-- 20.databases/020.properties.sql had to drop at the SQL Server 2012 floor,
-- plus the size distribution that explains it.
--
-- THIS FILE USED TO CARRY A MIN_VERSION AND NO LONGER DOES. The gate was
-- 13.0.5026, the build where sys.dm_db_log_info arrived, and below it the
-- archive had no VLF count at all — on precisely the instances whose logs have
-- been growing by percentage increments for years. Two 2016 SP1 instances
-- audited in September 2026 ran 13.0.4451 and 13.0.4457, so the report had to
-- write that the count could not be collected and that a 61 GB log grown in
-- 10% increments was probably fragmented. Run by hand afterwards the real
-- answer was 46, which is fine. The guess was wrong and it was printed.
--
-- THE CONDITION IS NOT "WHICH BUILD" BUT "DOES THE FUNCTION EXIST", so the
-- file asks that directly rather than through arithmetic on a version string.
-- One file, two mechanisms, no overlap and no hole by construction. It also
-- avoids a trap a version gate would have walked into: Azure SQL Database and
-- Managed Instance report ProductVersion 12.0.x indefinitely, so a collector
-- gated at "below 13.0.5026" would have selected the DBCC path there.
--
-- NOTHING IS PROBED AND NOTHING IS ASKED FOR. DBCC LOGINFO is documented as
-- requiring sysadmin, and measured on 2022 a db_owner of the database gets the
-- same Msg 2571 as a bare login — so does VIEW SERVER PERFORMANCE STATE. That
-- is a permission this practice will not ask a client for. The collector
-- attempts the read and records the refusal, which is the posture
-- docs/replication-spec.md already takes for the publication catalog: on a
-- read-only audit login below 2016 SP2 the count stays uncollected and the
-- archive says why in the row rather than by omission.
--
-- THE COLUMN COUNT OF DBCC LOGINFO IS NOT KNOWABLE FROM HERE. Microsoft does
-- not document the command at all — there is no page, which is why
-- sys.dm_db_log_info exists — and the reviewers of this design disagreed about
-- whether RecoveryUnitId arrived in 2012 or in 2016 SP2. A wrong declaration
-- mismatches on every build where the branch runs, raises Msg 213, is caught,
-- and records nothing: a collector dead on arrival on exactly the versions it
-- was written for. So the file does not depend on the answer. It attempts the
-- eight-column shape, and on Msg 213 — which is precisely the shape-mismatch
-- error — it attempts the seven-column one. The source column records which
-- succeeded, so the first real run settles the question for good.
--
-- FILESIZE IS IN BYTES, AND THAT IS MEASURED. DBCC LOGINFO returns 253952 and
-- 262144 for VLFs that sys.dm_db_log_info reports as 0.242 and 0.25 MB.
-- Without the division the archive would be wrong by six orders of magnitude,
-- in the direction that looks like a catastrophic log.
--
-- THE TWO MECHANISMS DO NOT ROUND ALIKE and the archive does not pretend they
-- do. DBCC LOGINFO gives bytes, converted here to three decimal places;
-- sys.dm_db_log_info.vlf_size_mb is a float and reports the same VLF as 0.24
-- where the conversion gives 0.242. Per VLF that is noise; across a log with
-- tens of thousands of them the totals diverge visibly, and an analysis
-- comparing an archive from a 2014 instance with one from a 2016 SP2 instance
-- would see a difference that is arithmetic rather than fragmentation. The
-- source column is what lets the reader tell. It is a fact to record, not a
-- defect to fix.
--
-- WHERE THE ACTIVE TAIL SITS IS A SEPARATE QUESTION FROM HOW MANY VLFS THERE
-- ARE, and it is the one that says whether the log can be reused from the
-- start or is pinned at the end of the file. A log with two active VLFs out of
-- two thousand is healthy if they are the first two and stuck if they are the
-- last two, and the count alone cannot tell those apart. The position is
-- therefore published relative to the total: space.vlf_last_active_pct over
-- the whole log, and vlf_last_active_position per file beside the vlf_count it
-- is relative to. A bare rank would need the reader to fetch the total before
-- it meant anything.
--
-- THE ORDER COMES FROM THE OFFSET, NOT FROM THE INSERT. A table variable has
-- no guaranteed insertion order, so @vlf stages the byte offset of each VLF —
-- vlf_begin_offset from the view, StartOffset from DBCC LOGINFO — and the rank
-- is computed from it. Both mechanisms report that offset in bytes and neither
-- rounds it, so the position is exact on either path and the divergence above
-- does not reach it.
--
-- SOURCE, ERROR_NUMBER AND ERROR_MESSAGE ARE IN THE ROOT OBJECT for the same
-- reason: a reader must never have to infer which mechanism produced a number,
-- and an analysis comparing two archives must be able to tell "no VLF count
-- because the login was refused" from "no VLF count because nothing ran".
--
-- No threshold is applied. The counts, the size spread and the active/inactive
-- split are what a log-growth problem looks like; naming one is the analysis
-- step's job.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @source varchar(20) = 'none', @err int = 0, @msg nvarchar(2048) = N'';

/* Staged, then emitted. Both result sets come out of these table variables
   whatever happened above, which is the whole point: a batch that stops inside
   a CATCH would declare two result sets and produce none. */
DECLARE @vlf TABLE (file_id int, vlf_begin_offset bigint,
                    vlf_size_mb decimal(18,3), vlf_active bit);

/* RecoveryUnitId leads the eight-column shape. The other columns are staged
   only because INSERT ... EXEC has to match the whole rowset; none of them is
   projected, so their types are wide rather than exact — CreateLSN in
   particular is numeric on the builds seen and is taken as text so that a
   different scale cannot fail the conversion. */
DECLARE @dbcc8 TABLE (RecoveryUnitId int, FileId int, FileSize bigint,
                      StartOffset bigint, FSeqNo bigint, Status int,
                      Parity int, CreateLSN nvarchar(64));

DECLARE @dbcc7 TABLE (FileId int, FileSize bigint,
                      StartOffset bigint, FSeqNo bigint, Status int,
                      Parity int, CreateLSN nvarchar(64));

IF OBJECT_ID(N'sys.dm_db_log_info') IS NOT NULL
BEGIN
    /* Deferred through sp_executesql for the reason 90.availability/041 states
       and docs/verification-replication-guard.md measures: a missing object is
       a compile-time error where a TRY at the same level cannot catch it, and
       a runtime one inside the deferred batch where it can. OBJECT_ID decides,
       but it is not trusted to be the last word. */
    BEGIN TRY
        INSERT INTO @vlf (file_id, vlf_begin_offset, vlf_size_mb, vlf_active)
        EXEC sys.sp_executesql
            N'SELECT li.file_id, li.vlf_begin_offset, li.vlf_size_mb, li.vlf_active
              FROM sys.dm_db_log_info(DB_ID()) AS li OPTION (RECOMPILE, MAXDOP 1)';
        /* A deferred read that returns nothing raises nothing either, and
           this view always has rows for a database it can read: a log has at
           least two VLFs. Zero rows therefore means the read did not happen,
           and saying dm_db_log_info there would put log_file_count 0 and
           vlf_count 0 in the root, which read as measurements. Measured on an
           instance whose collation was changed in the same server lifetime,
           see docs/verification-binary-collation.md. */
        IF @@ROWCOUNT > 0
            SET @source = 'dm_db_log_info';
    END TRY
    BEGIN CATCH
        SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH
END
ELSE
BEGIN
    BEGIN TRY
        INSERT INTO @dbcc8 EXEC ('DBCC LOGINFO WITH NO_INFOMSGS');
        SET @source = 'dbcc_loginfo_8';
    END TRY
    BEGIN CATCH
        SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH

    /* Msg 213 is the shape mismatch and nothing else. Msg 2571 is the refusal,
       and it is kept: retrying a permission error under a different column
       count would only replace one true answer with the same one. */
    IF @err = 213
    BEGIN
        SELECT @err = 0, @msg = N'';
        BEGIN TRY
            INSERT INTO @dbcc7 EXEC ('DBCC LOGINFO WITH NO_INFOMSGS');
            SET @source = 'dbcc_loginfo_7';
        END TRY
        BEGIN CATCH
            SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
        END CATCH
    END

    /* Status 2 is an active VLF. FileSize is bytes, hence the division; the
       divisor is written as a decimal so the arithmetic is never integer. */
    INSERT INTO @vlf (file_id, vlf_begin_offset, vlf_size_mb, vlf_active)
    SELECT d.FileId, d.StartOffset, d.FileSize / 1048576.0,
           CASE WHEN d.Status = 2 THEN 1 ELSE 0 END
    FROM @dbcc8 AS d
    UNION ALL
    SELECT d.FileId, d.StartOffset, d.FileSize / 1048576.0,
           CASE WHEN d.Status = 2 THEN 1 ELSE 0 END
    FROM @dbcc7 AS d
    OPTION (RECOMPILE, MAXDOP 1);
END

/* Where the active tail sits, as a percentage of the log rather than as a rank.
   A rank of 1800 says nothing on its own; 1800 out of 1810 says the log cannot
   wrap and the next write extends the file. The rank itself is published per
   file below, where the total it is relative to is in the same row. NULL when
   no VLF is active, which is a real state and not a missing measurement. */
DECLARE @last_active_pct decimal(5,1);

/* The CASE falls back to 0 rather than to NULL, and NULLIF puts the NULL back
   afterwards: a rank starts at 1, so 0 cannot collide with a real position, and
   MAX over a column holding NULLs raises the "null value is eliminated"
   warning once per database on an instance with ANSI_WARNINGS on. */
SELECT @last_active_pct = CAST(100.0
        * NULLIF(MAX(CASE WHEN o.vlf_active = 1 THEN o.pos ELSE 0 END), 0)
        / NULLIF(COUNT(*), 0) AS decimal(5,1))
FROM (SELECT v.vlf_active,
             ROW_NUMBER() OVER (ORDER BY v.file_id, v.vlf_begin_offset) AS pos
      FROM @vlf AS v) AS o
OPTION (RECOMPILE, MAXDOP 1);

SELECT
    @source                                                         AS [source],
    @err                                                            AS [error_number],
    NULLIF(@msg, N'')                                               AS [error_message],
    /* NULL rather than 0 where nothing was read: a log always has VLFs, so a
       count of zero beside a source of none would be a measurement nobody
       took. The averages below are NULL on an empty table already. */
    CASE WHEN @source = 'none' THEN NULL ELSE COUNT(*) END          AS [space.vlf_count],
    CASE WHEN @source = 'none' THEN NULL
         ELSE SUM(CASE WHEN v.vlf_active = 1 THEN 1 ELSE 0 END) END AS [space.vlf_active_count],
    CASE WHEN @source = 'none' THEN NULL
         ELSE SUM(CASE WHEN v.vlf_active = 0 THEN 1 ELSE 0 END) END AS [space.vlf_inactive_count],
    CAST(MIN(v.vlf_size_mb) AS DECIMAL(14,2))                       AS [space.vlf_min_size_mb],
    CAST(AVG(v.vlf_size_mb) AS DECIMAL(14,2))                       AS [space.vlf_avg_size_mb],
    CAST(MAX(v.vlf_size_mb) AS DECIMAL(14,2))                       AS [space.vlf_max_size_mb],
    CASE WHEN @source = 'none' THEN NULL
         ELSE SUM(CASE WHEN v.vlf_size_mb < 1 THEN 1 ELSE 0 END) END AS [space.vlf_under_1mb_count],
    CASE WHEN @source = 'none' THEN NULL
         ELSE COUNT(DISTINCT v.file_id) END                         AS [space.log_file_count],
    @last_active_pct                                                AS [space.vlf_last_active_pct]
FROM @vlf AS v
OPTION (RECOMPILE, MAXDOP 1);

/* vlf_per_file: one row per transaction log file. The name comes from
   sys.database_files, which both mechanisms can be joined to on file_id, so it
   is fetched once here rather than twice above. */
WITH ranked AS (
    SELECT v.file_id, v.vlf_size_mb, v.vlf_active,
           ROW_NUMBER() OVER (PARTITION BY v.file_id
                              ORDER BY v.vlf_begin_offset) AS vlf_position
    FROM @vlf AS v
)
SELECT
    v.file_id,
    df.name                                                 AS logical_name,
    COUNT(*)                                                AS vlf_count,
    SUM(CASE WHEN v.vlf_active = 1 THEN 1 ELSE 0 END)       AS vlf_active_count,
    NULLIF(MAX(CASE WHEN v.vlf_active = 1
                    THEN v.vlf_position ELSE 0 END), 0)     AS vlf_last_active_position,
    CAST(SUM(v.vlf_size_mb) AS DECIMAL(14,2))               AS vlf_total_mb,
    CAST(MIN(v.vlf_size_mb) AS DECIMAL(14,2))               AS vlf_min_size_mb,
    CAST(MAX(v.vlf_size_mb) AS DECIMAL(14,2))               AS vlf_max_size_mb
FROM ranked AS v
LEFT JOIN sys.database_files AS df ON df.file_id = v.file_id
GROUP BY v.file_id, df.name
ORDER BY v.file_id
OPTION (RECOMPILE, MAXDOP 1);
