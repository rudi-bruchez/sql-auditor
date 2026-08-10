-- @scope:         database
-- @resultsets:    root:object, estimates:array, not_estimated:array
-- @permissions:   CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:       1800
-- @requires_flag: estimate_compression
--
-- Estimated page-compression savings on the largest uncompressed objects.
--
-- Derived from Rudi Bruchez's tsql-scripts, which in turn adapt Glenn Berry's
-- estimation loop. What is added here is what automation needs and an
-- interactive script does not: bounds, and a statement of what those bounds
-- left out.
--
-- WHY THIS IS THE ONE COLLECTOR BEHIND A COST FLAG, alongside session text.
-- sp_estimate_data_compression_savings does not read statistics — it COPIES a
-- sample of the real data into tempdb, compresses it, and measures. On a
-- 500 GB heap that is minutes of I/O and a large tempdb allocation, on the
-- very instance being audited. The originating scripts loop over every index
-- and every partition of a database, which is right for a DBA who has chosen
-- one database and is watching; it would be indefensible as part of an
-- unattended sweep of eleven.
--
-- SO IT IS BOUNDED THREE WAYS, and all three are reported in root:
--   - uncompressed objects only, since estimating a compressed one answers a
--     question nobody asked;
--   - at least 100 MB reserved, below which the saving cannot matter;
--   - the 20 largest, because the estimate cost is roughly proportional to
--     size and the tail is where the cost/benefit inverts.
-- not_estimated lists what those bounds excluded, with its size. A bounded
-- measurement that does not publish its bound reads as a complete one, and
-- "we estimated the savings" would then quietly mean "we estimated some".
--
-- PAGE ONLY, deliberately. ROW would double an already expensive pass, and
-- PAGE is the right first question on warehouse data — it subsumes ROW's
-- prefix compression and adds dictionary compression across the page. Where
-- PAGE looks unattractive on a specific candidate, ROW is a follow-up on that
-- candidate, not a second sweep of everything.
--
-- @index_id and @partition_number are passed as NULL so the procedure does
-- every index of a table in one call. The originating scripts cursor per
-- index and per partition, which multiplies the number of sampling passes
-- for the same answer.
--
-- IT READS THE DATA, AND THAT BREAKS THE CORPUS'S CENTRAL PROMISE.
-- MANIFEST.txt states that the collector reads no user or application table.
-- Every other file here keeps that promise; this one cannot, because the
-- procedure works by SELECTing rows and compressing a copy of them. A
-- read-only audit login therefore has no SELECT anywhere and this collector
-- fails with error 229 — measured, not assumed, on the instance it was built
-- against. That failure is the correct default.
--
-- So the flag is not only about spending I/O. Turning it on means granting
-- SELECT on the audited data and accepting that the run touches it. Whoever
-- approves the run has to be told, in the same way session text is disclosed.
--
-- Each object is wrapped in TRY/CATCH, and the first error is KEPT — number
-- and message — not merely counted. Thirteen of thirteen objects failed on
-- the first run of this file with a bare count to show for it, which is the
-- silent failure this corpus exists to avoid. A PRINT would not have helped:
-- print output cannot be collected, and concatenating an int into its message
-- raises error 245 from inside the handler, losing the very error it was
-- written to report.
--
-- SQL Server 2012 is the floor for the procedure. Data compression itself
-- reached Standard Edition only in 2016 SP1, so root carries the edition and
-- build: on an older Standard instance these numbers describe a saving that
-- cannot be taken.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

DECLARE @top int = 20, @min_mb int = 100, @kind nvarchar(60) = N'PAGE';
DECLARE @failures int = 0, @attempted int = 0;
DECLARE @first_err int = 0, @first_msg nvarchar(2048) = N'', @first_obj sysname = NULL;

CREATE TABLE #candidates (
    schema_name sysname, object_name sysname, object_id int,
    reserved_mb decimal(18,2), rows_ bigint);

INSERT INTO #candidates (schema_name, object_name, object_id, reserved_mb, rows_)
SELECT TOP (@top)
       SCHEMA_NAME(t.schema_id), t.name, t.object_id,
       CAST(SUM(ps.reserved_page_count) * 8.0 / 1024 AS decimal(18,2)),
       SUM(CASE WHEN p.index_id IN (0,1) THEN p.rows ELSE 0 END)
FROM       sys.tables AS t
JOIN       sys.partitions AS p  ON p.object_id = t.object_id
JOIN       sys.dm_db_partition_stats AS ps
        ON ps.object_id = p.object_id AND ps.index_id = p.index_id
       AND ps.partition_number = p.partition_number
WHERE t.is_ms_shipped = 0 AND p.data_compression = 0
GROUP BY t.schema_id, t.name, t.object_id
HAVING SUM(ps.reserved_page_count) * 8.0 / 1024 >= @min_mb
ORDER BY SUM(ps.reserved_page_count) DESC;

CREATE TABLE #savings (
    object_name sysname, schema_name sysname, index_id int, partition_number int,
    size_current_kb bigint, size_requested_kb bigint,
    sample_current_kb bigint, sample_requested_kb bigint);

DECLARE @s sysname, @o sysname;
DECLARE cur CURSOR LOCAL FAST_FORWARD FOR SELECT schema_name, object_name FROM #candidates;
OPEN cur;
FETCH NEXT FROM cur INTO @s, @o;
WHILE @@FETCH_STATUS = 0
BEGIN
    SET @attempted = @attempted + 1;
    BEGIN TRY
        INSERT INTO #savings
        EXEC sys.sp_estimate_data_compression_savings
             @schema_name = @s, @object_name = @o,
             @index_id = NULL, @partition_number = NULL, @data_compression = @kind;
    END TRY
    BEGIN CATCH
        SET @failures = @failures + 1;
        IF @first_err = 0
            SELECT @first_err = ERROR_NUMBER(), @first_msg = ERROR_MESSAGE(),
                   @first_obj = @s + N'.' + @o;
    END CATCH;
    FETCH NEXT FROM cur INTO @s, @o;
END
CLOSE cur; DEALLOCATE cur;

SELECT DB_NAME()                                                  AS [database],
       SYSDATETIME()                                              AS [collected_at],
       CONVERT(nvarchar(128), SERVERPROPERTY('ProductVersion'))   AS [product_version],
       CONVERT(nvarchar(128), SERVERPROPERTY('Edition'))          AS [edition],
       @kind                                                      AS [bounds.compression_type],
       @top                                                       AS [bounds.max_objects],
       @min_mb                                                    AS [bounds.min_reserved_mb],
       @attempted                                                 AS [attempted_objects],
       @failures                                                  AS [failed_objects],
       NULLIF(@first_err, 0)                                      AS [first_failure.error_number],
       NULLIF(@first_msg, N'')                                    AS [first_failure.message],
       @first_obj                                                 AS [first_failure.object],
       (SELECT COUNT(*) FROM #savings)                            AS [estimate_rows],
       CAST((SELECT SUM(size_current_kb)   FROM #savings) / 1024.0 AS decimal(18,1)) AS [totals.current_mb],
       CAST((SELECT SUM(size_requested_kb) FROM #savings) / 1024.0 AS decimal(18,1)) AS [totals.estimated_mb],
       CAST((SELECT SUM(size_current_kb - size_requested_kb) FROM #savings) / 1024.0 AS decimal(18,1)) AS [totals.saved_mb]
OPTION (RECOMPILE, MAXDOP 1);

/* sample_* travels with the estimate on purpose: it is the fraction of the
   object the procedure actually compressed to reach its number. A large
   object measured from a small sample is a projection, and the reader is
   entitled to see the basis rather than take the total on faith. */
SELECT sv.schema_name + '.' + sv.object_name                      AS [table],
       ISNULL(i.name, '(heap)')                                   AS [index_name],
       sv.index_id                                                AS [index_id],
       i.type_desc                                                AS [index_type],
       sv.partition_number                                        AS [partition_number],
       CAST(sv.size_current_kb   / 1024.0 AS decimal(18,1))       AS [current_mb],
       CAST(sv.size_requested_kb / 1024.0 AS decimal(18,1))       AS [estimated_mb],
       CAST((sv.size_current_kb - sv.size_requested_kb) / 1024.0 AS decimal(18,1)) AS [saved_mb],
       CAST(100.0 - (100.0 * sv.size_requested_kb)
            / NULLIF(sv.size_current_kb, 0) AS decimal(5,2))      AS [saved_pct],
       CAST(sv.sample_current_kb   / 1024.0 AS decimal(18,1))     AS [sample.current_mb],
       CAST(sv.sample_requested_kb / 1024.0 AS decimal(18,1))     AS [sample.estimated_mb]
FROM       #savings AS sv
LEFT JOIN  sys.indexes AS i
        ON i.object_id = OBJECT_ID(QUOTENAME(sv.schema_name) + '.' + QUOTENAME(sv.object_name))
       AND i.index_id = sv.index_id
ORDER BY sv.size_current_kb - sv.size_requested_kb DESC
OPTION (RECOMPILE, MAXDOP 1);

/* What the bounds excluded. Ordered by size, so the first row answers "how
   much did we leave unmeasured, and was it worth measuring". */
SELECT TOP (100)
       SCHEMA_NAME(t.schema_id) + '.' + t.name                    AS [table],
       CAST(SUM(ps.reserved_page_count) * 8.0 / 1024 AS decimal(18,2)) AS [reserved_mb],
       SUM(CASE WHEN p.index_id IN (0,1) THEN p.rows ELSE 0 END)  AS [rows],
       CASE WHEN SUM(ps.reserved_page_count) * 8.0 / 1024 < @min_mb
            THEN 'below the size threshold'
            ELSE 'outside the largest ' + CAST(@top AS varchar(10)) END AS [reason]
FROM       sys.tables AS t
JOIN       sys.partitions AS p  ON p.object_id = t.object_id
JOIN       sys.dm_db_partition_stats AS ps
        ON ps.object_id = p.object_id AND ps.index_id = p.index_id
       AND ps.partition_number = p.partition_number
WHERE t.is_ms_shipped = 0 AND p.data_compression = 0
  AND t.object_id NOT IN (SELECT object_id FROM #candidates)
GROUP BY t.schema_id, t.name
ORDER BY SUM(ps.reserved_page_count) DESC
OPTION (RECOMPILE, MAXDOP 1);
