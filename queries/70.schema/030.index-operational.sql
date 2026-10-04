-- @scope:       database
-- @resultsets:  root:object, heaps:array, contention:array, page_compression:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     120
-- @profiles:    space
--
-- Forwarded records on heaps, where lock and latch waits actually land, and
-- whether partitions set to PAGE compression actually get it.
--
-- Why this collector exists: an audit found 1 840 of 2 040 tables with no
-- clustered index, and had no way to say whether that cost anything.
-- forwarded_fetch_count is the measure that answers it, and it exists only
-- here.
--
-- A FORWARDED RECORD IS THE SPECIFIC DISEASE OF A HEAP. When a row in a heap
-- grows past the free space on its page, SQL Server moves it and leaves a
-- pointer behind. Every later read of that row costs a second page fetch, and
-- the pointers accumulate: a heap that is updated will degrade indefinitely
-- while its row count stays flat, and nothing in the size or usage figures
-- shows it. A table with a clustered index cannot have them at all, which is
-- why this result set is heaps only — index_id 0.
--
-- THESE COUNTERS ARE WEAKER EVIDENCE THAN sys.dm_db_index_usage_stats AND THE
-- DIFFERENCE MATTERS. They reset on restart like the others, but they also
-- reset whenever the index metadata is evicted from memory — which happens
-- under memory pressure, with no event and no record. A low count is therefore
-- not proof of a healthy heap; it may be proof of a recent eviction. A HIGH
-- count is trustworthy, because nothing inflates it. Read this result set in
-- one direction only.
--
-- Lock waits are attributed per index, which is the part sys.dm_os_wait_stats
-- cannot do: it says the instance waited 2 382 hours on LCK_M_IS, not which
-- object anyone was queuing for. index_lock_promotion_count is collected with
-- them because a lock escalating to table level is how one writer stops every
-- reader, and it is the mechanism behind the longest waits.
--
-- The DMV is cheap: it reads counters already in memory and does not touch the
-- data, unlike sys.dm_db_index_physical_stats, which scans. There is no reason
-- to gate this collector behind a flag.
--
-- THE HEAPS LISTING HAS NO CAP. It kept the 200 heaps with the most forwarded
-- fetches until October 2026, with "the same cap as the two others" for its
-- only reason, and no heap total beside it. Twelve real collections taken on
-- eight client instances between August and September 2026 had it bind in 4
-- databases, up to 810 heaps (docs/caps-inventory.md). The aggregate already
-- read the DMV for every heap before the TOP kept 200 of them, so the whole
-- list costs output only, about 200 bytes a heap, some 120 KB raw more on the
-- largest seen. listing_cap, still 200, now describes contention and
-- page_compression and not heaps.
--
-- IT IS STILL BLOCKABLE, AND THAT IS WHY THE READS ARE BUFFERED. Cheap is not
-- the same as lock-free: the DMV is joined to sys.objects and sys.indexes,
-- which need a schema stability lock, and READ UNCOMMITTED gives up locks on
-- DATA and never on METADATA. Measured behind one open ALTER TABLE, this file
-- came back 1222 after ten seconds and lost its whole document — a statement
-- that fails mid-batch takes the rest of the batch's output with it. Each area
-- now reads inside its own TRY/CATCH into a table variable, the CATCH assigns
-- variables and nothing else, and the emitting SELECTs at the bottom run
-- unconditionally (four, since page_compression joined them). A blocked area
-- comes back empty with its error number in the root object. See 020.index-usage.sql for the same pattern and the
-- measurement behind it.
--
-- A COLUMNSTORE INDEX HAS NO ROW OF ITS OWN IN THIS DMV, AND THIS FILE SPENT
-- ITS LIFE NOT SAYING SO. sys.dm_db_index_operational_stats answers one row per
-- allocation unit per partition, which for a rowstore index is one row and for
-- a clustered columnstore index is two or three: its delta store and its delete
-- bitmap, both carrying the index's own index_id and partition_number. Measured
-- on 16.0.4265.3 and 17.0.4065.4, on a 300 000-row table given a clustered
-- columnstore index, a third of its rows deleted and a seventh updated: the DMV
-- returned two rows for index_id 1, whose hobt_id values matched the
-- COLUMN_STORE_DELTA_STORE and COLUMN_STORE_DELETE_BITMAP rows of
-- sys.internal_partitions. Neither was the hobt_id sys.partitions reports for
-- that index. The columnstore's own segments are not represented at all.
--
-- So the contention aggregate sums two internal objects under the name of the
-- index. The scope of that is narrower than it first looks, and worth stating
-- precisely rather than dramatically: this result set only keeps rows that
-- waited, so a columnstore index appears here only when its delta store or its
-- delete bitmap was contended, and the error is then that the contention of two
-- distinct internal structures is reported as one index's. Those two are
-- contended for opposite reasons. A hot delta store means rows are arriving one
-- at a time instead of in bulk-sized batches; a hot delete bitmap means rows
-- are being deleted or updated in place. Summed under one name, the row says
-- neither, and the remedy for one is not the remedy for the other.
--
-- The rows are not filtered out, because a delta store under lock contention is
-- a real finding and dropping them would lose it. What changes is that the row
-- now says what it is: index_type names the columnstore, and units_summed says
-- how many rows of the DMV went into it. One is a plain index, two or three
-- with a columnstore type is this case, and a larger number on a rowstore index
-- is a partitioned index reporting its partitions. Whoever reads the archive
-- can tell the three apart, which before this they could not.
--
-- sys.internal_partitions would name the two objects outright, and it is not
-- used here: it arrived in SQL Server 2016 and this collector's floor is 2012.
--
-- THE WAIT COUNTS NOW CARRY THEIR DENOMINATORS. row_lock_count and
-- page_lock_count are how many lock requests were made; row_lock_wait_count and
-- page_lock_wait_count are how many of them had to wait. Only the second pair
-- was projected, so this collector could say an index waited and never what
-- share of its requests did. Ten thousand waits out of ten thousand requests
-- and ten thousand out of forty million are the same number here and different
-- findings. Both columns sit in the rows already being read.
--
-- PAGE COMPRESSION IS A SETTING, NOT A RESULT, AND THE THIRD LISTING SAYS WHICH.
-- page_compression lists the rowstore partitions whose data_compression is PAGE
-- (sys.partitions.data_compression = 2) with page_compression_attempt_count and
-- page_compression_success_count. Neither result set above could carry them:
-- heaps keeps index_id 0 and contention keeps rows that waited, and a PAGE
-- index is usually neither. So it is a listing of its own, in its own
-- TRY/CATCH like the others.
--
-- What the two counters count was measured on 17.0.4065.4 rather than taken
-- from the reference, because the reference's one sentence ("pages that were
-- evaluated") does not say when a page is evaluated, and the obvious reading
-- of the ratio is wrong in one common case.
--
-- An attempt is a leaf page that is full when a row arrives. The engine tries
-- prefix and dictionary compression on it; success means the page gained
-- enough room to take the row. On an ever-increasing key that has a ceiling of
-- one half, not one: a page that compresses fills a second time, the second
-- attempt finds nothing more to gain, and the page splits. 20 000 rows of
-- repetitive char(200) gave 54 attempts and 27 successes on 28 leaf pages; the
-- same rows of random binary(200) gave 571 attempts and 0 successes on 572.
-- Half of each row compressible gave 562 and 281, for 282 pages against 409
-- under ROW. A ratio near 0.5 is PAGE working; it never reaches 1.
--
-- A LOW RATIO DOES NOT PROVE THE DATA RESISTS COMPRESSION. A page split from a
-- page that is already compressed comes out compressed without counting a
-- success, and its next fill counts a failed attempt. The same repetitive rows
-- inserted under a random uniqueidentifier key, in 200 batches, gave 111
-- attempts and 1 success, and sys.dm_db_index_physical_stats (SAMPLED) showed
-- 112 of 112 leaf pages compressed. The random binary rows under the same key
-- gave 839 and 0, with 0 of 840 compressed. The two partitions look alike
-- here, and leaf_allocations, projected with them, is close to the attempts
-- in both. What settles it is compressed_page_count from a SAMPLED scan, which
-- this collector does not do and 70.schema/055.page-density projects, behind
-- --measure-page-density, for the largest index partitions: its row for the
-- same table, index_id and partition_number says how many leaf pages are
-- compressed. For heaps 70.schema/050.heaps projects it by default, for the
-- 50 largest, on table and partition. Without it, an estimate from 041.compression-savings. Zero
-- successes on many attempts is a question to ask, not a finding.
--
-- A REBUILD STARTS EVERY COUNTER AGAIN, AND WHAT IT THEN COUNTS OF ITSELF
-- DEPENDS ON WHAT IS REBUILT AND HOW. The rebuilt partition gets a new hobt_id
-- and its row starts from zero: forwarded fetches, updates, deletes, ghosts,
-- lookups, lock counts, all of them. What the rebuild adds was measured on
-- 17.0.4065.4, 4 October 2026, on a PAGE heap and a PAGE clustered index of
-- the same 20,000 repetitive rows, each with one nonclustered index, after
-- 2,000 updates and 1,000 deletes (the online pair after 100 more inserts):
--
--   offline, clustered index  leaf_inserts      0, attempts 66, successes 44
--   offline, heap             leaf_inserts 19,000, attempts 57, successes 32
--   online, clustered index   leaf_inserts 19,100, attempts 81, successes 27
--   online, heap              leaf_inserts 19,100, attempts 59, successes 30
--
-- Where leaf_inserts is not 0 it is the number of rows the rebuild wrote, not
-- a count carried over: 19,000 is the 20,000 inserted less the 1,000 deleted,
-- and a second offline rebuild of the heap read 19,100 again, not twice that.
-- So attempts with no leaf_inserts describe an offline index rebuild (the
-- repetitive table of the measurements above read 54 and 27 again after each
-- of two), while a heap rebuilt, or any partition rebuilt ONLINE, reads as
-- though its rows had just been inserted. Nothing in the row tells that apart
-- from a load, and the heaps listing above shows the same leaf_inserts.
-- ALTER TABLE ... REBUILD on a heap rebuilds its nonclustered indexes too,
-- whose rows start again; on a table with a clustered index it rebuilds that
-- index alone, and the nonclustered rows kept their counts. ALTER INDEX ALL
-- on a heap left the heap's row as it was. TRUNCATE
-- TABLE does not reset them, measured over three reloads of one table,
-- although the reference says a truncated partition leaves the function; that
-- was not measured with TRUNCATE ... WITH (PARTITIONS). Closing the database (OFFLINE then ONLINE
-- was measured) removes every row, and a partition reappears, at zero, only
-- once something touches it. So the root counts PAGE partitions with and
-- without a row: a missing row is "not touched since its metadata was loaded",
-- not "never attempted". Restart was not measured.
--
-- A HEAP SET TO PAGE ATTEMPTS NOTHING UNDER ORDINARY INSERTS, and that is the
-- other thing this listing shows. 20 000 repetitive rows inserted without
-- TABLOCK into a PAGE heap counted 0 attempts and took 57 pages; the same rows
-- after ALTER TABLE ... REBUILD took 28, with 54 attempts and 27 successes.
-- An INSERT ... WITH (TABLOCK) compressed them as it went. A heap row here
-- with leaf_inserts and no attempts is a heap whose new pages are row-only.
-- A heap rebuilt and then filled by ordinary inserts reads like a heap only
-- rebuilt, attempts and successes and all, though its new pages are row-only
-- too; 70.schema/050.heaps says how many of its pages are compressed.
--
-- A failed attempt is cheap. 200 000 random binary(200) rows into a PAGE
-- clustered index and into a ROW one, three times each: PAGE cost 13 to 31 ms
-- more CPU over about 850 ms, for 5 263 failed attempts. What PAGE fails to
-- deliver on such a partition is the space, not a CPU budget it burns.
--
-- This listing is why the file belongs to the space profile. A partition set
-- to PAGE that never compresses is space the database was expected to give
-- back and did not, and a space run that left this file out could not say so.
-- The rest of the file comes along: it is cheap, and splitting it would cost a
-- second pass over the same view.
--
-- The listing keeps the 200 partitions with the most attempts, the cap
-- contention has, so listing_cap describes both. Attempts order it
-- because they are the volume of compression work the partition asked for.
-- Partitions with none come last, by leaf inserts, which is where a PAGE heap
-- filled by ordinary inserts sits: past 200 partitions with attempts, such a
-- heap falls off the listing. The totals at the root are not capped, and two
-- of them are for that heap: page_partitions.heaps counts the PAGE heap
-- partitions from sys.partitions, and page_partitions.heaps_without_attempts
-- those whose counter row shows leaf inserts and no attempt, the row-only
-- shape described above. They are counted here although 050.heaps now
-- projects compressed_page_count per heap, because that file reads only the
-- 50 largest heaps above 128 pages, and here they cost a CASE in two counts
-- already made. A heap with no counter row is in neither
-- the second count nor the listing, only in the gap between total and
-- reporting. Its cost is the same kind as the other
-- areas: counters already in memory, joined to sys.partitions and
-- sys.dm_db_partition_stats. On a database holding 10 017 PAGE partitions the
-- whole file took about 0.3 s longer than without this area, 1.0 s against
-- 0.7 s, three runs each.
--
-- SQL Server 2012 is the floor. sys.dm_db_index_operational_stats predates it,
-- and both compression counters and sys.partitions.data_compression arrived
-- with SQL Server 2008.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @err_counts int = 0, @err_heaps int = 0, @err_contention int = 0,
        @err_page int = 0, @msg nvarchar(2048) = N'';

DECLARE @instance_start datetime, @seconds_since int,
        @rows_reporting int, @forwarded_total bigint, @heaps_forwarded int,
        @lock_wait_ms bigint, @lock_escalations bigint,
        @page_partitions int, @page_partitions_reporting int,
        @page_attempts bigint, @page_successes bigint,
        @page_heaps int, @page_heaps_without_attempts int;

DECLARE @heaps TABLE (
    [table]             nvarchar(300),
    [forwarded_fetches] bigint,
    [leaf_inserts]      bigint,
    [leaf_updates]      bigint,
    [leaf_deletes]      bigint,
    [leaf_ghosts]       bigint,
    [range_scans]       bigint,
    [singleton_lookups] bigint,
    [rows]              bigint NULL);

DECLARE @contention TABLE (
    [table]             nvarchar(300),
    [index_name]        nvarchar(128),
    [index_id]          int,
    [index_type]        nvarchar(60),
    [units_summed]      int,
    [row_locks]         bigint,
    [page_locks]        bigint,
    [row_lock_waits]    bigint,
    [row_lock_wait_ms]  bigint,
    [page_lock_waits]   bigint,
    [page_lock_wait_ms] bigint,
    [lock_escalations]  bigint,
    [latch_wait_ms]     bigint,
    [io_latch_wait_ms]  bigint);

DECLARE @page TABLE (
    [table]             nvarchar(300),
    [index_name]        nvarchar(128),
    [index_id]          int,
    [index_type]        nvarchar(60),
    [partition_number]  int,
    [attempts]          bigint,
    [successes]         bigint,
    [leaf_inserts]      bigint,
    [leaf_updates]      bigint,
    [leaf_allocations]  bigint,
    [rows]              bigint NULL,
    [in_row_pages]      bigint NULL);

BEGIN TRY
    SELECT @instance_start = si.sqlserver_start_time,
           @seconds_since  = DATEDIFF(second, si.sqlserver_start_time, GETDATE())
    FROM sys.dm_os_sys_info AS si
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @rows_reporting = COUNT(*)
    FROM sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL)
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @forwarded_total = SUM(os.forwarded_fetch_count)
    FROM sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
    WHERE os.index_id = 0
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @heaps_forwarded = COUNT(DISTINCT os.object_id)
    FROM sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
    WHERE os.index_id = 0 AND os.forwarded_fetch_count > 0
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @lock_wait_ms = SUM(os.row_lock_wait_in_ms + os.page_lock_wait_in_ms)
    FROM sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @lock_escalations = SUM(os.index_lock_promotion_count)
    FROM sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_counts = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* Heaps only. leaf_update_count travels with the forwarded count because a
   forwarded record is created by an update: the ratio is what separates a
   heap that is merely written from one that is being damaged. */
BEGIN TRY
    INSERT INTO @heaps
    SELECT
           SCHEMA_NAME(o.schema_id) + '.' + o.name,
           SUM(os.forwarded_fetch_count),
           SUM(os.leaf_insert_count),
           SUM(os.leaf_update_count),
           SUM(os.leaf_delete_count),
           SUM(os.leaf_ghost_count),
           SUM(os.range_scan_count),
           SUM(os.singleton_lookup_count),
           MAX(ps.row_count)
    FROM       sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
    JOIN       sys.objects AS o ON o.object_id = os.object_id AND o.type = 'U'
    LEFT JOIN  sys.dm_db_partition_stats AS ps
            ON ps.object_id = os.object_id AND ps.index_id = os.index_id
    WHERE os.index_id = 0
    GROUP BY o.schema_id, o.name
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_heaps = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* Ordered by total lock wait, not by lock count: a million locks taken and
   released instantly are not a problem, and one lock held for four minutes
   is. */
BEGIN TRY
    INSERT INTO @contention
    SELECT TOP (200)
           SCHEMA_NAME(o.schema_id) + '.' + o.name,
           ISNULL(i.name, '(heap)'),
           os.index_id,
           ISNULL(MAX(i.type_desc), N'HEAP'),
           -- HOW MANY ROWS OF THE DMV WENT INTO THIS ONE. Normally one per
           -- partition, so a partitioned index reports its partition count and
           -- everything else reports 1. A columnstore index reports 2 or 3 and
           -- means something different: see the header.
           COUNT(*),
           -- The denominators. Without them a wait count is unreadable: ten
           -- thousand waits out of ten thousand requests is a table that is
           -- always contended, and ten thousand out of forty million is noise
           -- that will be chased for a week. These are the same rows already
           -- being read, so the two columns are free.
           SUM(os.row_lock_count),
           SUM(os.page_lock_count),
           SUM(os.row_lock_wait_count),
           SUM(os.row_lock_wait_in_ms),
           SUM(os.page_lock_wait_count),
           SUM(os.page_lock_wait_in_ms),
           SUM(os.index_lock_promotion_count),
           SUM(os.page_latch_wait_in_ms),
           SUM(os.page_io_latch_wait_in_ms)
    FROM       sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
    JOIN       sys.objects AS o ON o.object_id = os.object_id AND o.type = 'U'
    LEFT JOIN  sys.indexes AS i ON i.object_id = os.object_id AND i.index_id = os.index_id
    WHERE os.row_lock_wait_in_ms > 0 OR os.page_lock_wait_in_ms > 0
       OR os.index_lock_promotion_count > 0
    GROUP BY o.schema_id, o.name, i.name, os.index_id
    ORDER BY SUM(os.row_lock_wait_in_ms) + SUM(os.page_lock_wait_in_ms) DESC
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_contention = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* Partitions set to PAGE, with what the engine made of it. The counts come
   from sys.partitions so that a PAGE partition the DMV has no row for is still
   counted: closing the database empties the function, and a partition comes
   back only once touched. See the header for what the ratio can and cannot
   say. */
BEGIN TRY
    SELECT @page_partitions = COUNT(*),
           @page_heaps      = COUNT(CASE WHEN p.index_id = 0 THEN 1 END)
    FROM sys.partitions AS p
    JOIN sys.objects AS o ON o.object_id = p.object_id AND o.type = 'U'
    WHERE p.data_compression = 2
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @page_partitions_reporting = COUNT(*),
           @page_attempts             = SUM(os.page_compression_attempt_count),
           @page_successes            = SUM(os.page_compression_success_count),
           @page_heaps_without_attempts =
               COUNT(CASE WHEN os.index_id = 0
                           AND os.page_compression_attempt_count = 0
                           AND os.leaf_insert_count > 0 THEN 1 END)
    FROM       sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
    JOIN       sys.partitions AS p
            ON p.object_id = os.object_id AND p.index_id = os.index_id
           AND p.partition_number = os.partition_number
    JOIN       sys.objects AS o ON o.object_id = os.object_id AND o.type = 'U'
    WHERE p.data_compression = 2
    OPTION (RECOMPILE, MAXDOP 1);

    INSERT INTO @page
    SELECT TOP (200)
           SCHEMA_NAME(o.schema_id) + '.' + o.name,
           ISNULL(i.name, '(heap)'),
           os.index_id,
           ISNULL(i.type_desc, N'HEAP'),
           os.partition_number,
           os.page_compression_attempt_count,
           os.page_compression_success_count,
           os.leaf_insert_count,
           os.leaf_update_count,
           os.leaf_allocation_count,
           ps.row_count,
           ps.in_row_data_page_count
    FROM       sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
    JOIN       sys.partitions AS p
            ON p.object_id = os.object_id AND p.index_id = os.index_id
           AND p.partition_number = os.partition_number
    JOIN       sys.objects AS o ON o.object_id = os.object_id AND o.type = 'U'
    LEFT JOIN  sys.indexes AS i ON i.object_id = os.object_id AND i.index_id = os.index_id
    LEFT JOIN  sys.dm_db_partition_stats AS ps ON ps.partition_id = p.partition_id
    WHERE p.data_compression = 2
    ORDER BY os.page_compression_attempt_count DESC, os.leaf_insert_count DESC
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    -- The totals go back to NULL with the listing. Behind an open ALTER TABLE
    -- the first count was measured to complete and the next statement to time
    -- out, and a total left beside an area reported as refused would still be
    -- read as an answer.
    SELECT @err_page = ERROR_NUMBER(), @msg = ERROR_MESSAGE(),
           @page_partitions = NULL, @page_partitions_reporting = NULL,
           @page_attempts = NULL, @page_successes = NULL,
           @page_heaps = NULL, @page_heaps_without_attempts = NULL;
END CATCH

SELECT DB_NAME()                                            AS [database],
       SYSDATETIME()                                        AS [collected_at],
       @instance_start                                      AS [instance_start],
       @seconds_since                                       AS [seconds_since_instance_start],
       @rows_reporting                                      AS [rows_reporting],
       @forwarded_total                                     AS [forwarded_fetches_total],
       @heaps_forwarded                                     AS [heaps_with_forwarded_fetches],
       @lock_wait_ms                                        AS [lock_wait_ms_total],
       @lock_escalations                                    AS [lock_escalations_total],
       @page_partitions                                     AS [page_partitions.total],
       @page_partitions_reporting                           AS [page_partitions.reporting],
       @page_attempts                                       AS [page_partitions.attempts],
       @page_successes                                      AS [page_partitions.successes],
       @page_heaps                                          AS [page_partitions.heaps],
       @page_heaps_without_attempts                         AS [page_partitions.heaps_without_attempts],
       200                                                  AS [listing_cap],
       CASE WHEN @err_counts     = 0 THEN 1 ELSE 0 END      AS [collected.counts],
       CASE WHEN @err_heaps      = 0 THEN 1 ELSE 0 END      AS [collected.heaps],
       CASE WHEN @err_contention = 0 THEN 1 ELSE 0 END      AS [collected.contention],
       CASE WHEN @err_page       = 0 THEN 1 ELSE 0 END      AS [collected.page_compression],
       @err_counts                                          AS [errors.counts],
       @err_heaps                                           AS [errors.heaps],
       @err_contention                                      AS [errors.contention],
       @err_page                                            AS [errors.page_compression],
       NULLIF(@msg, N'')                                    AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

SELECT h.[table], h.[forwarded_fetches], h.[leaf_inserts], h.[leaf_updates],
       h.[leaf_deletes], h.[leaf_ghosts], h.[range_scans],
       h.[singleton_lookups], h.[rows]
FROM @heaps AS h
ORDER BY h.[forwarded_fetches] DESC, h.[leaf_updates] DESC
OPTION (RECOMPILE, MAXDOP 1);

SELECT c.[table], c.[index_name], c.[index_id],
       c.[index_type]        AS [index_type],
       c.[units_summed]      AS [units_summed],
       c.[row_locks]         AS [row_lock.requests],
       c.[page_locks]        AS [page_lock.requests],
       c.[row_lock_waits]    AS [row_lock.waits],
       c.[row_lock_wait_ms]  AS [row_lock.wait_ms],
       c.[page_lock_waits]   AS [page_lock.waits],
       c.[page_lock_wait_ms] AS [page_lock.wait_ms],
       c.[lock_escalations]  AS [lock_escalations],
       c.[latch_wait_ms]     AS [latch.wait_ms],
       c.[io_latch_wait_ms]  AS [io_latch.wait_ms]
FROM @contention AS c
ORDER BY c.[row_lock_wait_ms] + c.[page_lock_wait_ms] DESC
OPTION (RECOMPILE, MAXDOP 1);

SELECT pc.[table], pc.[index_name], pc.[index_id], pc.[index_type],
       pc.[partition_number],
       pc.[attempts]          AS [attempts],
       pc.[successes]         AS [successes],
       pc.[leaf_inserts]      AS [leaf_inserts],
       pc.[leaf_updates]      AS [leaf_updates],
       pc.[leaf_allocations]  AS [leaf_allocations],
       pc.[rows]              AS [rows],
       pc.[in_row_pages]      AS [in_row_pages]
FROM @page AS pc
ORDER BY pc.[attempts] DESC, pc.[leaf_inserts] DESC
OPTION (RECOMPILE, MAXDOP 1);
