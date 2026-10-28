-- @scope:       database
-- @resultsets:  root:object, functions:array, schemes:array, ranges:array, indexes:array, filegroups:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     120
-- @profiles:    space
--
-- Runs once per user database, with the connection context switched to it.
--
-- How the database is partitioned: the functions and their boundaries, the
-- schemes and which filegroup each partition lands on, the indexes of every
-- partitioned table, and every filegroup with whether it is read only.
--
-- WHY THIS COLLECTOR EXISTS. Found on a client data warehouse in October 2026:
-- a database of 12.8 TB, 55 partitioned tables, 367 monthly filegroups and 586
-- files, whose oldest years, on a slow archive volume, were read 22 to 92 times
-- their own size in eleven days. The archive could say that much from the file
-- statistics, and nothing more. Whether the queries failed to eliminate
-- partitions, whether the statistics on those tables were incremental, whether
-- the archive filegroups were read only, which dates sat on which filegroup:
-- none of it was collected. 70.schema/020.index-usage counts partitions per
-- index and 70.schema/040.compression counts filegroups per index, and both
-- stop there. docs/partitioning-spec.md is the specification.
--
-- WHAT A READER GETS, IN THE ORDER THEY NEED IT.
--
-- functions: one row per partition function, with the type of its key, its
-- side (RANGE LEFT or RIGHT), its number of partitions, its first and last
-- boundary, and the step between consecutive boundaries: smallest, largest,
-- and the most frequent with its share. A step of 1 day at 100 % is a daily
-- function; 28 to 31 days with 31 as the commonest is monthly. The grain is
-- read from the steps and not named here: an analysis names it, this file
-- measures it.
--
-- schemes: one row per scheme, with its function, how many filegroups it
-- spreads over, how many of them are read only, and the NEXT USED filegroup
-- the next SPLIT will put its new partition on.
--
-- ranges: the scheme's partitions collapsed into runs of consecutive
-- partitions on the same filegroup, each with the boundaries that enclose it.
-- A daily function over monthly filegroups is one row per month here, not one
-- per day, and that is the row that says which dates live on which filegroup,
-- hence on which volume once joined to 20.databases/020.properties.
--
-- indexes: every index of every partitioned table, the non-partitioned ones
-- included, because an index left on a single filegroup under a partitioned
-- table is what makes ALTER TABLE ... SWITCH refuse and what defeats the
-- point of having partitions. Each row says whether it is aligned, how many
-- of its partitions are empty, and how many sit on a read-only filegroup.
--
-- filegroups: every filegroup, partitioned or not, with is_read_only, its
-- files and size, the data it holds, and how many partitions and scheme slots
-- point at it.
--
-- BOUNDARY VALUES ARE DATA, SO NOT ALL OF THEM LEAVE THE SERVER. A boundary is
-- a value of the partitioning column, written into the function. Dates and
-- numbers say how the data is cut and nothing about whom it describes; a
-- string boundary can be a customer name or a country code, and a binary or
-- uniqueidentifier one is a key nobody can read anyway. So the values, and
-- the steps computed from them, are projected for the date and time types and
-- the exact numeric types only. For the others the function row carries
-- boundary_values = 0 and NULLs, and the counts stay: how many partitions
-- there are is never a disclosure.
--
-- NO JUDGEMENT IS APPLIED. A daily function is not wrong in itself, a
-- read-write archive filegroup may be read write on purpose, and an unaligned
-- index may exist for a query that needs it. What makes each a finding is the
-- workload, which this file does not see.
--
-- THE COST IS CATALOGUE READS ONLY. sys.partitions and sys.allocation_units
-- are read once each, for the whole database, into table variables; nothing
-- reads a page of data and nothing needs VIEW SERVER STATE. Rows come from
-- sys.partitions.rows, which the documentation calls approximate: it is what
-- the engine keeps, and it is close enough for "empty or not" and for a share.
-- Measured in docs/partitioning-spec.md.
--
-- LOCKS. Catalogue views take short schema-stability locks on what they
-- describe, and READ UNCOMMITTED does not remove them. On a database whose
-- loads run DDL, a read can queue behind a schema-modification lock, which is
-- the situation that motivated this file. LOCK_TIMEOUT bounds each wait at 10
-- seconds and every area sits in its own TRY: a blocked area is reported in
-- errors.* and the others are still collected.
--
-- Each list is capped and the root counts what it would hold without the cap,
-- beside the cap itself, so a list cut short never reads as complete.
--
-- SQL Server 2012 is the floor, the corpus default. Every view and column
-- read here is older than that.
--
-- Not collected, deliberately:
--   one row per partition          (a daily function over ten years is
--     3 650 rows per index; ranges and the per-index aggregates answer what
--     that list would, at a fraction of its size)
--   sys.dm_db_incremental_stats_properties (per statistic and per partition:
--     the same multiplication; whether a statistic is incremental at all is
--     in 70.schema/090.statistics, which is the question that decides)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @cap_functions int = 1000, @cap_schemes int = 1000, @cap_ranges int = 5000,
        @cap_indexes int = 2000, @cap_filegroups int = 2000;

DECLARE @err_functions int = 0, @err_schemes int = 0, @err_partitions int = 0,
        @err_filegroups int = 0, @msg nvarchar(2048) = N'';

/* The boundaries, one row per value, with the value as text and as a number
   the steps can be computed from. kind says which family the key type is in,
   and decides whether a value may be projected at all. */
DECLARE @boundaries TABLE (
    [function_id]  int NOT NULL,
    [boundary_id]  int NOT NULL,
    [kind]         char(1) NOT NULL,          -- t temporal, n exact numeric, o other
    [value_text]   nvarchar(64) NULL,
    [value_num]    float NULL,                -- days for t, the value for n
    PRIMARY KEY ([function_id], [boundary_id]));

DECLARE @functions TABLE (
    [function_id]   int NOT NULL PRIMARY KEY,
    [name]          sysname NOT NULL,
    [key_type]      nvarchar(128) NULL,
    [kind]          char(1) NOT NULL,
    [on_right]      bit NOT NULL,
    [fanout]        int NOT NULL);

/* One row per partition of every index on a partitioned table, and of every
   index on a table whose base is partitioned. The allocation units are summed
   here, once, and the filegroup is the in-row unit's: that is where the
   partition's rows live. */
DECLARE @parts TABLE (
    [object_id]        int NOT NULL,
    [index_id]         int NOT NULL,
    [partition_number] int NOT NULL,
    [rows]             bigint NULL,
    [reserved_pages]   bigint NULL,
    [data_space_id]    int NULL,
    PRIMARY KEY ([object_id], [index_id], [partition_number]));

/* Runs of consecutive partitions of a scheme on the same filegroup. */
DECLARE @ranges TABLE (
    [scheme_id]       int NOT NULL,
    [function_id]     int NOT NULL,
    [data_space_id]   int NOT NULL,
    [first_partition] int NOT NULL,
    [last_partition]  int NOT NULL,
    PRIMARY KEY ([scheme_id], [first_partition]));

/* Pages per filegroup across the whole database, partitioned or not. */
DECLARE @fg_pages TABLE (
    [data_space_id]  int NOT NULL PRIMARY KEY,
    [reserved_pages] bigint NOT NULL,
    [objects]        int NOT NULL);

BEGIN TRY
    INSERT INTO @functions
    SELECT pf.function_id, pf.name,
           ty.name + CASE
               WHEN ty.name IN (N'varchar', N'char', N'varbinary', N'binary')
                    THEN N'(' + CASE WHEN pp.max_length = -1 THEN N'max' ELSE CAST(pp.max_length AS nvarchar(10)) END + N')'
               WHEN ty.name IN (N'nvarchar', N'nchar')
                    THEN N'(' + CASE WHEN pp.max_length = -1 THEN N'max' ELSE CAST(pp.max_length / 2 AS nvarchar(10)) END + N')'
               WHEN ty.name IN (N'decimal', N'numeric')
                    THEN N'(' + CAST(pp.precision AS nvarchar(10)) + N',' + CAST(pp.scale AS nvarchar(10)) + N')'
               WHEN ty.name IN (N'datetime2', N'datetimeoffset', N'time')
                    THEN N'(' + CAST(pp.scale AS nvarchar(10)) + N')'
               ELSE N'' END,
           CASE WHEN ty.name IN (N'date', N'datetime', N'datetime2', N'smalldatetime', N'datetimeoffset')
                     THEN 't'
                WHEN ty.name IN (N'tinyint', N'smallint', N'int', N'bigint', N'decimal', N'numeric',
                                 N'money', N'smallmoney')
                     THEN 'n'
                ELSE 'o' END,
           pf.boundary_value_on_right, pf.fanout
    FROM sys.partition_functions AS pf
    LEFT JOIN sys.partition_parameters AS pp ON pp.function_id = pf.function_id AND pp.parameter_id = 1
    LEFT JOIN sys.types AS ty ON ty.user_type_id = pp.user_type_id
    OPTION (RECOMPILE, MAXDOP 1);

    /* The text of a value only where its kind may leave the server; the number
       only where a step means something. A temporal value is turned into days
       since 1900-01-01, so the step between two boundaries is a number of
       days whatever the type, and a six-hour step reads 0.25. */
    INSERT INTO @boundaries
    SELECT prv.function_id, prv.boundary_id, f.[kind],
           /* Each type in the form a reader writes it: a date without a time, an
              offset kept with its offset, an integer without decimals. */
           CASE WHEN f.[kind] = 't' THEN
                     CASE CAST(SQL_VARIANT_PROPERTY(prv.value, 'BaseType') AS sysname)
                          WHEN N'date'           THEN CONVERT(nvarchar(64), CAST(prv.value AS date), 23)
                          WHEN N'datetimeoffset' THEN CONVERT(nvarchar(64), CAST(prv.value AS datetimeoffset(7)), 126)
                          ELSE CONVERT(nvarchar(64), CAST(prv.value AS datetime2(7)), 126) END
                WHEN f.[kind] = 'n' THEN
                     CASE WHEN CAST(SQL_VARIANT_PROPERTY(prv.value, 'BaseType') AS sysname)
                               IN (N'tinyint', N'smallint', N'int', N'bigint')
                          THEN CONVERT(nvarchar(64), CAST(prv.value AS bigint))
                          ELSE CONVERT(nvarchar(64), prv.value) END
           END,
           /* Whole days, then the fraction of the day in seconds. DATEDIFF
              returns an int, so minutes since 1900 overflow after the year
              5983, and a 9999-12-31 boundary, the usual "end of time" of a
              warehouse, failed the whole area with error 535 on the lab. Days
              reach about 2.9 million over the range of datetime2, and the
              seconds stay within one day. */
           CASE f.[kind]
                WHEN 't' THEN DATEDIFF(day, CAST('19000101' AS date), CAST(prv.value AS date))
                              + DATEDIFF(second, CAST(CAST(prv.value AS date) AS datetime2(7)),
                                         CAST(prv.value AS datetime2(7))) / 86400.0
                WHEN 'n' THEN CAST(prv.value AS float)
           END
    FROM sys.partition_range_values AS prv
    JOIN @functions AS f ON f.function_id = prv.function_id
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_functions = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

BEGIN TRY
    /* Gaps and islands: within a scheme and a filegroup, consecutive
       destinations share destination_id - ROW_NUMBER(). Destinations past the
       fanout are the NEXT USED filegroup, which holds no partition yet. */
    INSERT INTO @ranges
    SELECT g.scheme_id, g.function_id, g.data_space_id,
           MIN(g.destination_id), MAX(g.destination_id)
    FROM (SELECT ps.data_space_id AS scheme_id, pf.function_id, dds.data_space_id,
                 dds.destination_id,
                 dds.destination_id - ROW_NUMBER() OVER (PARTITION BY ps.data_space_id, dds.data_space_id
                                                         ORDER BY dds.destination_id) AS grp
          FROM sys.partition_schemes AS ps
          JOIN sys.partition_functions AS pf ON pf.function_id = ps.function_id
          JOIN sys.destination_data_spaces AS dds ON dds.partition_scheme_id = ps.data_space_id
          WHERE dds.destination_id <= pf.fanout) AS g
    GROUP BY g.scheme_id, g.function_id, g.data_space_id, g.grp
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_schemes = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

BEGIN TRY
    /* Every index of every table whose heap or clustered index is on a
       partition scheme. The base is found first, so a non-partitioned index
       under a partitioned table is listed, which is the point. */
    /* Two equality joins rather than one on a CASE. The documented rule is
       container_id = hobt_id for in-row and row-overflow units and
       partition_id for LOB units, and a CASE in the join condition leaves the
       engine nothing but nested loops: measured on the lab, 47 402 partitions
       took 323 seconds that way, against the time in the spec this way. */
    INSERT INTO @parts
    SELECT u.object_id, u.index_id, u.partition_number, u.[rows],
           SUM(u.total_pages),
           MAX(CASE WHEN u.type = 1 THEN u.data_space_id END)
    FROM (SELECT p.object_id, p.index_id, p.partition_number, p.rows AS [rows],
                 au.total_pages, au.type, au.data_space_id
          FROM sys.partitions AS p
          JOIN sys.allocation_units AS au ON au.container_id = p.hobt_id AND au.type IN (1, 3)
          UNION ALL
          SELECT p.object_id, p.index_id, p.partition_number, p.rows,
                 au.total_pages, au.type, au.data_space_id
          FROM sys.partitions AS p
          JOIN sys.allocation_units AS au ON au.container_id = p.partition_id AND au.type = 2) AS u
    WHERE u.object_id IN (SELECT b.object_id
                          FROM sys.indexes AS b
                          JOIN sys.partition_schemes AS ps ON ps.data_space_id = b.data_space_id
                          JOIN sys.objects AS o ON o.object_id = b.object_id
                          WHERE b.index_id IN (0, 1) AND o.is_ms_shipped = 0)
    GROUP BY u.object_id, u.index_id, u.partition_number, u.[rows]
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_partitions = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

BEGIN TRY
    INSERT INTO @fg_pages
    SELECT u.data_space_id, SUM(u.total_pages), COUNT(DISTINCT u.object_id)
    FROM (SELECT au.data_space_id, au.total_pages, p.object_id
          FROM sys.allocation_units AS au
          JOIN sys.partitions AS p ON p.hobt_id = au.container_id AND au.type IN (1, 3)
          UNION ALL
          SELECT au.data_space_id, au.total_pages, p.object_id
          FROM sys.allocation_units AS au
          JOIN sys.partitions AS p ON p.partition_id = au.container_id AND au.type = 2) AS u
    GROUP BY u.data_space_id
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_filegroups = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* The steps between consecutive boundaries, per function. LAG is 2012. */
DECLARE @steps TABLE ([function_id] int NOT NULL, [step] float NOT NULL);
INSERT INTO @steps
SELECT s.function_id, s.step
FROM (SELECT b.function_id,
             ROUND(b.value_num - LAG(b.value_num) OVER (PARTITION BY b.function_id
                                                      ORDER BY b.boundary_id), 6) AS step
      FROM @boundaries AS b
      WHERE b.[kind] IN ('t', 'n')) AS s
WHERE s.step IS NOT NULL
OPTION (RECOMPILE, MAXDOP 1);

SELECT
    DB_NAME()                                                   AS [database],
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    (SELECT COUNT(*) FROM @functions)                           AS [counts.functions],
    (SELECT COUNT(*) FROM sys.partition_schemes)                AS [counts.schemes],
    (SELECT COUNT(DISTINCT pt.object_id) FROM @parts AS pt)     AS [counts.partitioned_tables],
    (SELECT COUNT(*) FROM (SELECT DISTINCT pt.object_id, pt.index_id FROM @parts AS pt) AS x)
                                                                AS [counts.indexes],
    (SELECT COUNT(*) FROM @parts AS pt WHERE pt.index_id IN (0, 1))
                                                                AS [counts.partitions],
    (SELECT COUNT(*) FROM sys.filegroups)                       AS [counts.filegroups],
    (SELECT COUNT(*) FROM sys.filegroups AS fg WHERE fg.is_read_only = 1)
                                                                AS [counts.read_only_filegroups],
    (SELECT COUNT(*) FROM sys.destination_data_spaces AS dds
       JOIN sys.partition_schemes AS ps ON ps.data_space_id = dds.partition_scheme_id
       JOIN sys.partition_functions AS pf ON pf.function_id = ps.function_id
      WHERE dds.destination_id <= pf.fanout)                    AS [counts.scheme_slots],
    (SELECT COUNT(*) FROM @ranges)                              AS [counts.ranges],
    @cap_functions                                              AS [listing_cap.functions],
    @cap_schemes                                                AS [listing_cap.schemes],
    @cap_ranges                                                 AS [listing_cap.ranges],
    @cap_indexes                                                AS [listing_cap.indexes],
    @cap_filegroups                                             AS [listing_cap.filegroups],
    CASE WHEN @err_functions  = 0 THEN 1 ELSE 0 END             AS [collected.functions],
    CASE WHEN @err_schemes    = 0 THEN 1 ELSE 0 END             AS [collected.ranges],
    CASE WHEN @err_partitions = 0 THEN 1 ELSE 0 END             AS [collected.partitions],
    CASE WHEN @err_filegroups = 0 THEN 1 ELSE 0 END             AS [collected.filegroups],
    @err_functions                                              AS [errors.functions],
    @err_schemes                                                AS [errors.ranges],
    @err_partitions                                             AS [errors.partitions],
    @err_filegroups                                             AS [errors.filegroups],
    NULLIF(@msg, N'')                                           AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

/* functions */
SELECT TOP (@cap_functions)
    f.[name]                                                    AS [function],
    f.[key_type]                                                AS [key_type],
    CASE WHEN f.[on_right] = 1 THEN 'RIGHT' ELSE 'LEFT' END     AS [range],
    f.[fanout]                                                  AS [partitions],
    f.[fanout] - 1                                              AS [boundaries],
    CASE WHEN f.[kind] IN ('t', 'n') THEN 1 ELSE 0 END          AS [boundary_values],
    (SELECT b.value_text FROM @boundaries AS b
      WHERE b.function_id = f.function_id AND b.boundary_id = 1) AS [first_boundary],
    (SELECT b.value_text FROM @boundaries AS b
      WHERE b.function_id = f.function_id AND b.boundary_id = f.[fanout] - 1)
                                                                AS [last_boundary],
    CASE f.[kind] WHEN 't' THEN 'days' WHEN 'n' THEN 'value' END AS [step_unit],
    (SELECT MIN(s.step) FROM @steps AS s WHERE s.function_id = f.function_id) AS [step_min],
    (SELECT MAX(s.step) FROM @steps AS s WHERE s.function_id = f.function_id) AS [step_max],
    m.step                                                      AS [step_mode],
    CAST(m.n * 100.0 / NULLIF((SELECT COUNT(*) FROM @steps AS s
                               WHERE s.function_id = f.function_id), 0) AS decimal(5,1))
                                                                AS [step_mode_pct],
    (SELECT COUNT(*) FROM sys.partition_schemes AS ps WHERE ps.function_id = f.function_id)
                                                                AS [schemes],
    (SELECT COUNT(DISTINCT i.object_id) FROM sys.indexes AS i
       JOIN sys.partition_schemes AS ps ON ps.data_space_id = i.data_space_id
      WHERE ps.function_id = f.function_id AND i.index_id IN (0, 1)) AS [tables]
FROM @functions AS f
OUTER APPLY (SELECT TOP (1) s.step, COUNT(*) AS n
             FROM @steps AS s WHERE s.function_id = f.function_id
             GROUP BY s.step ORDER BY COUNT(*) DESC, s.step) AS m
ORDER BY f.[fanout] DESC, f.[name]
OPTION (RECOMPILE, MAXDOP 1);

/* schemes */
SELECT TOP (@cap_schemes)
    ps.name                                                     AS [scheme],
    pf.name                                                     AS [function],
    (SELECT COUNT(DISTINCT dds.data_space_id) FROM sys.destination_data_spaces AS dds
      WHERE dds.partition_scheme_id = ps.data_space_id AND dds.destination_id <= pf.fanout)
                                                                AS [filegroups],
    (SELECT COUNT(DISTINCT dds.data_space_id) FROM sys.destination_data_spaces AS dds
       JOIN sys.filegroups AS fg ON fg.data_space_id = dds.data_space_id
      WHERE dds.partition_scheme_id = ps.data_space_id AND dds.destination_id <= pf.fanout
        AND fg.is_read_only = 1)                                AS [read_only_filegroups],
    (SELECT fg.name FROM sys.destination_data_spaces AS dds
       JOIN sys.filegroups AS fg ON fg.data_space_id = dds.data_space_id
      WHERE dds.partition_scheme_id = ps.data_space_id AND dds.destination_id = pf.fanout + 1)
                                                                AS [next_used],
    (SELECT COUNT(DISTINCT i.object_id) FROM sys.indexes AS i
      WHERE i.data_space_id = ps.data_space_id AND i.index_id IN (0, 1)) AS [tables],
    (SELECT COUNT(*) FROM sys.indexes AS i
      WHERE i.data_space_id = ps.data_space_id)                 AS [indexes]
FROM sys.partition_schemes AS ps
JOIN sys.partition_functions AS pf ON pf.function_id = ps.function_id
ORDER BY pf.fanout DESC, ps.name
OPTION (RECOMPILE, MAXDOP 1);

/* ranges: runs of consecutive partitions on the same filegroup. Partition n
   lies between boundary n-1 and boundary n; which side is inclusive is the
   function's range, in functions. NULL is unbounded. */
SELECT TOP (@cap_ranges)
    ps.name                                                     AS [scheme],
    fg.name                                                     AS [filegroup],
    r.first_partition                                           AS [first_partition],
    r.last_partition                                            AS [last_partition],
    r.last_partition - r.first_partition + 1                    AS [partitions],
    lo.value_text                                               AS [from_boundary],
    hi.value_text                                               AS [to_boundary],
    CAST(fg.is_read_only AS int)                                AS [is_read_only]
FROM @ranges AS r
JOIN sys.partition_schemes AS ps ON ps.data_space_id = r.scheme_id
JOIN sys.filegroups AS fg ON fg.data_space_id = r.data_space_id
LEFT JOIN @boundaries AS lo ON lo.function_id = r.function_id AND lo.boundary_id = r.first_partition - 1
LEFT JOIN @boundaries AS hi ON hi.function_id = r.function_id AND hi.boundary_id = r.last_partition
ORDER BY ps.name, r.first_partition
OPTION (RECOMPILE, MAXDOP 1);

/* indexes: every index of every partitioned table, largest first. */
SELECT TOP (@cap_indexes)
    s.name + N'.' + o.name                                      AS [table],
    i.name                                                      AS [index_name],
    i.index_id                                                  AS [index_id],
    i.type_desc                                                 AS [index_type],
    ps.name                                                     AS [scheme],
    pf.name                                                     AS [function],
    pc.name                                                     AS [partition_column],
    /* Aligned: on a scheme of the base's function, by the same column. The
       base is aligned with itself; an index on a single filegroup is not. */
    CASE WHEN ps.function_id IS NOT NULL AND ps.function_id = bps.function_id
              AND pic.column_id = bic.column_id THEN 1 ELSE 0 END AS [aligned],
    a.partitions                                                AS [partitions],
    a.empty_partitions                                          AS [empty_partitions],
    a.[rows]                                                    AS [rows],
    CAST(a.reserved_pages * 8 / 1024.0 AS decimal(18,1))        AS [reserved_mb],
    a.largest_rows                                              AS [largest_partition_rows],
    CAST(a.largest_pages * 8 / 1024.0 AS decimal(18,1))         AS [largest_partition_mb],
    a.filegroups                                                AS [filegroups],
    a.read_only_partitions                                      AS [read_only_partitions]
FROM (SELECT pt.object_id, pt.index_id,
             COUNT(*)                                            AS partitions,
             SUM(CASE WHEN ISNULL(pt.[rows], 0) = 0 THEN 1 ELSE 0 END) AS empty_partitions,
             SUM(pt.[rows])                                      AS [rows],
             SUM(pt.reserved_pages)                              AS reserved_pages,
             MAX(pt.[rows])                                      AS largest_rows,
             MAX(pt.reserved_pages)                              AS largest_pages,
             COUNT(DISTINCT pt.data_space_id)                    AS filegroups,
             SUM(CASE WHEN fg.is_read_only = 1 THEN 1 ELSE 0 END) AS read_only_partitions
      FROM @parts AS pt
      LEFT JOIN sys.filegroups AS fg ON fg.data_space_id = pt.data_space_id
      GROUP BY pt.object_id, pt.index_id) AS a
JOIN sys.indexes AS i ON i.object_id = a.object_id AND i.index_id = a.index_id
JOIN sys.objects AS o ON o.object_id = i.object_id
JOIN sys.schemas AS s ON s.schema_id = o.schema_id
LEFT JOIN sys.partition_schemes AS ps ON ps.data_space_id = i.data_space_id
LEFT JOIN sys.partition_functions AS pf ON pf.function_id = ps.function_id
LEFT JOIN sys.index_columns AS pic ON pic.object_id = i.object_id AND pic.index_id = i.index_id
                                  AND pic.partition_ordinal = 1
LEFT JOIN sys.columns AS pc ON pc.object_id = pic.object_id AND pc.column_id = pic.column_id
JOIN sys.indexes AS b ON b.object_id = i.object_id AND b.index_id IN (0, 1)
LEFT JOIN sys.partition_schemes AS bps ON bps.data_space_id = b.data_space_id
LEFT JOIN sys.index_columns AS bic ON bic.object_id = b.object_id AND bic.index_id = b.index_id
                                  AND bic.partition_ordinal = 1
ORDER BY a.reserved_pages DESC, i.object_id, i.index_id
OPTION (RECOMPILE, MAXDOP 1);

/* filegroups: every one, partitioned or not. */
SELECT TOP (@cap_filegroups)
    fg.name                                                     AS [filegroup],
    fg.type_desc                                                AS [type],
    CAST(fg.is_read_only AS int)                                AS [is_read_only],
    CAST(fg.is_default AS int)                                  AS [is_default],
    (SELECT COUNT(*) FROM sys.database_files AS df WHERE df.data_space_id = fg.data_space_id)
                                                                AS [files],
    (SELECT CAST(ISNULL(SUM(CAST(df.size AS bigint)), 0) * 8 / 1024.0 AS decimal(18,1))
       FROM sys.database_files AS df WHERE df.data_space_id = fg.data_space_id)
                                                                AS [size_mb],
    CAST(ISNULL(fp.reserved_pages, 0) * 8 / 1024.0 AS decimal(18,1)) AS [reserved_mb],
    ISNULL(fp.objects, 0)                                       AS [objects],
    (SELECT COUNT(*) FROM @parts AS pt WHERE pt.data_space_id = fg.data_space_id
        AND pt.index_id IN (0, 1))                              AS [partitions],
    (SELECT COUNT(*) FROM sys.destination_data_spaces AS dds
       JOIN sys.partition_schemes AS ps ON ps.data_space_id = dds.partition_scheme_id
       JOIN sys.partition_functions AS pf ON pf.function_id = ps.function_id
      WHERE dds.data_space_id = fg.data_space_id AND dds.destination_id <= pf.fanout)
                                                                AS [scheme_slots]
FROM sys.filegroups AS fg
LEFT JOIN @fg_pages AS fp ON fp.data_space_id = fg.data_space_id
ORDER BY fg.name
OPTION (RECOMPILE, MAXDOP 1);
