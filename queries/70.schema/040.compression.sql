-- @scope:       database
-- @resultsets:  root:object, by_compression:array, largest_uncompressed:array, mixed_tables:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     120
-- @profiles:    space
--
-- Which tables and indexes are compressed, and which are not.
--
-- Why this collector exists: the corpus could report a 1,27 To data file and
-- a 521 Go table without ever saying whether either was compressed. On an
-- estate of that size it is the first question a storage conversation asks,
-- and nothing could answer it.
--
-- COMPRESSION IS A PROPERTY OF A PARTITION, NOT OF A TABLE, and that is why
-- mixed_tables exists as its own result set. A table can be half compressed —
-- the usual cause is a partitioned table whose old partitions were compressed
-- and whose new ones were created without inheriting it, or an index rebuilt
-- with a DATA_COMPRESSION clause someone forgot. Reporting one value per table
-- would pick a winner and hide the split, which is exactly the case worth
-- finding.
--
-- IT IS ALSO A LICENSING FACT WORTH GETTING RIGHT. Data compression was
-- Enterprise-only until SQL Server 2016 SP1, when it came to Standard. An
-- instance below that build cannot use it and an uncompressed table there is
-- not a finding; above it, the same table is a decision nobody made. The
-- collector reports the build in root so the analysis layer can tell the two
-- apart rather than assuming.
--
-- NO SAVING IS ESTIMATED. sp_estimate_data_compression_savings samples and
-- builds a copy of the data in tempdb — on the tables where the answer would
-- matter most, that is the single most expensive thing this corpus could do,
-- and it would run on every database of every audit. The facts are collected
-- here; estimating a specific candidate is a deliberate follow-up, run once,
-- by someone who has decided to.
--
-- SQL Server 2012 is the floor. sys.partitions.data_compression predates it.
-- COLUMNSTORE and COLUMNSTORE_ARCHIVE appear as values on 2012 and later, so
-- they are reported as they come rather than mapped to a fixed list.
--
-- A NOTE ON THE WORD "PARTITION", BECAUSE IT COSTS A CLIENT REPORT.
--
-- sys.partitions returns one row per index per table, whether or not anything
-- is partitioned: a table with three indexes yields four rows. A field named
-- "partitions" therefore reports a number that has nothing to do with
-- partitioning, and it will be read as though it does. It was, in an audit of
-- an estate with 3 799 such rows and not one partitioned table, and the client
-- asked for the list.
--
-- The columns are named storage_units for that reason. The only field here
-- that speaks about partitioning is partitioned_tables, which counts objects
-- with partition_number > 1 and is the number to quote when someone asks
-- whether anything is partitioned.
--
-- LARGEST_UNCOMPRESSED IS CAPPED AT 2 000 ROWS, AND THE ROOT SAYS HOW MANY
-- THERE WERE. A row is one index, or the heap, of one table, with at least one
-- uncompressed partition; counts.uncompressed_indexes counts those rows before
-- the cap, so a list cut short reads as cut. The cap was 200, with no stated
-- reason and no total, until October 2026. Twelve real collections taken on
-- eight client instances between August and September 2026 had it bind in 9
-- of 27 databases, up to 1 617 uncompressed storage units against 200 rows
-- (docs/caps-inventory.md), so the candidates for compression past the 200th
-- largest were invisible and nothing said so. A row is about 165 bytes, so
-- 2 000 rows are some 330 KB raw, and the cap is now there for the archive
-- alone: the list is cut from two table variables read whole, so the server
-- does the same work at 2 000 rows as at 200 (see the comment on the staging
-- below). On a lab database of 2 851 storage units and 2 400 uncompressed
-- indexes the file took 3.2 s with the old cap and 3.0 s with the new one.
-- Size orders the list, with object_id and index_id breaking ties, so two runs
-- over the same catalog keep the same rows.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT DB_NAME()                                                  AS [database],
       SYSDATETIME()                                              AS [collected_at],
       CONVERT(nvarchar(128), SERVERPROPERTY('ProductVersion'))   AS [product_version],
       CONVERT(nvarchar(128), SERVERPROPERTY('Edition'))          AS [edition],
       (SELECT COUNT(DISTINCT p.object_id) FROM sys.partitions AS p
        JOIN sys.tables AS t ON t.object_id = p.object_id AND t.is_ms_shipped = 0) AS [counts.tables],
       (SELECT COUNT(*) FROM sys.partitions AS p
        JOIN sys.tables AS t ON t.object_id = p.object_id AND t.is_ms_shipped = 0) AS [counts.storage_units],
       (SELECT COUNT(*) FROM sys.partitions AS p
        JOIN sys.tables AS t ON t.object_id = p.object_id AND t.is_ms_shipped = 0
        WHERE p.data_compression <> 0)                            AS [counts.compressed_storage_units],
       (SELECT COUNT(DISTINCT p.object_id) FROM sys.partitions AS p
        JOIN sys.tables AS t ON t.object_id = p.object_id AND t.is_ms_shipped = 0
        WHERE p.partition_number > 1)                             AS [counts.partitioned_tables],
       /* The rows largest_uncompressed would hold without its cap: one per
          (table, index) with an uncompressed partition, grouped as below. */
       (SELECT COUNT(*) FROM (SELECT DISTINCT p.object_id, p.index_id
                              FROM sys.partitions AS p
                              JOIN sys.tables AS t ON t.object_id = p.object_id AND t.is_ms_shipped = 0
                              WHERE p.data_compression = 0) AS u) AS [counts.uncompressed_indexes],
       2000                                                       AS [listing_cap]
OPTION (RECOMPILE, MAXDOP 1);

/* The estate-level answer in one table: how much data sits under each
   compression setting. Reserved size, not row count — the question is about
   storage. */
SELECT p.data_compression_desc                                    AS [compression],
       COUNT(*)                                                   AS [storage_units],
       COUNT(DISTINCT p.object_id)                                AS [objects],
       SUM(p.rows)                                                AS [rows],
       CAST(SUM(ps.reserved_page_count) * 8.0 / 1024 AS DECIMAL(18,1)) AS [reserved_mb]
FROM       sys.partitions AS p
JOIN       sys.tables     AS t  ON t.object_id = p.object_id AND t.is_ms_shipped = 0
LEFT JOIN  sys.dm_db_partition_stats AS ps
        ON ps.object_id = p.object_id AND ps.index_id = p.index_id
       AND ps.partition_number = p.partition_number
GROUP BY p.data_compression_desc
ORDER BY SUM(ps.reserved_page_count) DESC
OPTION (RECOMPILE, MAXDOP 1);

/* Ordered by size, because that is the only order in which this list is
   actionable: the candidates are the big ones. */
/* THE FILEGROUP IS READ THROUGH THE ALLOCATION UNITS, not through
   sys.indexes. FILEGROUP_NAME(i.data_space_id) is the obvious route and it is
   wrong twice over: on a partitioned index data_space_id is a partition scheme
   id, so the name comes back NULL, and the id itself overflows the smallint
   FILEGROUP_NAME takes. Measured on SQL Server 2025 against a table on a two
   filegroup scheme: "Arithmetic overflow error for data type smallint, value =
   65601", which in this file would have cost every result set of the batch and
   not just this one.

   A row here aggregates every partition of an index, so the answer can be
   several filegroups. filegroup names the one when there is exactly one, and
   is NULL otherwise; filegroup_count says which case it is. A consumer sizing
   the room an operation has must refuse to conclude on a count above one
   rather than pick a filegroup, because a partitioned object rebuilds
   partition by partition and each partition answers to its own. */
DECLARE @uncompressed TABLE (
    [object_id]      int           NOT NULL,
    [index_id]       int           NOT NULL,
    [table]          nvarchar(300) NULL,
    [index_name]     sysname       NULL,
    [index_type]     nvarchar(60)  NULL,
    [storage_units]  int           NULL,
    [rows]           bigint        NULL,
    [reserved_mb]    decimal(18,1) NULL,
    [reserved_pages] bigint        NULL,
    PRIMARY KEY ([object_id], [index_id]));

DECLARE @filegroups TABLE (
    [object_id] int     NOT NULL,
    [index_id]  int     NOT NULL,
    [count]     int     NOT NULL,
    [one]       sysname NULL,
    PRIMARY KEY ([object_id], [index_id]));

/* STAGED, BECAUSE THE TOP MADE THE PLAN PER ROW. Written as one statement, the
   TOP (2000) set a row goal and the optimiser answered it with nested loops
   that searched the catalog once per row: on the lab, 3.3 s for 2 000 rows
   where the aggregate and the filegroups take 50 ms and 25 ms read apart, and
   the per-row lookup grows with the number of partitions it searches. Each
   half is read whole into a table variable, with no TOP to aim at, and the
   list is cut from those. */
INSERT INTO @uncompressed ([object_id], [index_id], [table], [index_name], [index_type],
                           [storage_units], [rows], [reserved_mb], [reserved_pages])
SELECT t.object_id,
       p.index_id,
       SCHEMA_NAME(t.schema_id) + '.' + t.name,
       ISNULL(i.name, '(heap)'),
       i.type_desc,
       COUNT(*),
       SUM(p.rows),
       CAST(SUM(ps.reserved_page_count) * 8.0 / 1024 AS DECIMAL(18,1)),
       SUM(ps.reserved_page_count)
FROM       sys.partitions AS p
JOIN       sys.tables     AS t  ON t.object_id = p.object_id AND t.is_ms_shipped = 0
LEFT JOIN  sys.indexes    AS i  ON i.object_id = p.object_id AND i.index_id = p.index_id
LEFT JOIN  sys.dm_db_partition_stats AS ps
        ON ps.object_id = p.object_id AND ps.index_id = p.index_id
       AND ps.partition_number = p.partition_number
WHERE p.data_compression = 0
GROUP BY t.schema_id, t.name, t.object_id, i.name, p.index_id, i.type_desc
OPTION (RECOMPILE, MAXDOP 1);

/* The filegroups of every index in one pass. The two kinds of allocation unit
   are joined apart, in-row and LOB data on the partition id and row-overflow
   data on the hobt id, as the reference says; the OR of the two that the
   per-row lookup used cannot be hashed. Every partition of the index counts,
   compressed or not, as it did. */
INSERT INTO @filegroups ([object_id], [index_id], [count], [one])
SELECT d.object_id, d.index_id, COUNT(*), MIN(d.nom)
FROM (SELECT pp.object_id, pp.index_id, ds.name AS nom
      FROM sys.partitions AS pp
      JOIN sys.allocation_units AS au
        ON au.type IN (1,3) AND au.container_id = pp.partition_id
      JOIN sys.data_spaces AS ds ON ds.data_space_id = au.data_space_id
      UNION
      SELECT pp.object_id, pp.index_id, ds.name
      FROM sys.partitions AS pp
      JOIN sys.allocation_units AS au
        ON au.type = 2 AND au.container_id = pp.hobt_id
      JOIN sys.data_spaces AS ds ON ds.data_space_id = au.data_space_id) AS d
GROUP BY d.object_id, d.index_id
OPTION (RECOMPILE, MAXDOP 1);

/* ISNULL keeps the 0 an index with no allocation unit has always had here. */
SELECT TOP (2000)
       u.[table], u.[index_name], u.[index_id], u.[index_type],
       u.[storage_units], u.[rows], u.[reserved_mb],
       CASE WHEN fg.[count] = 1 THEN fg.[one] END                 AS [filegroup],
       ISNULL(fg.[count], 0)                                      AS [filegroup_count]
FROM @uncompressed AS u
LEFT JOIN @filegroups AS fg
       ON fg.[object_id] = u.[object_id] AND fg.[index_id] = u.[index_id]
ORDER BY u.[reserved_pages] DESC, u.[object_id], u.[index_id]
OPTION (RECOMPILE, MAXDOP 1);

/* A table whose partitions disagree. Empty is the expected result; a row here
   is almost always an accident rather than a design. */
SELECT SCHEMA_NAME(t.schema_id) + '.' + t.name                    AS [table],
       COUNT(DISTINCT p.data_compression_desc)                    AS [distinct_settings],
       COUNT(*)                                                   AS [storage_units],
       MIN(p.data_compression_desc)                               AS [setting_min],
       MAX(p.data_compression_desc)                               AS [setting_max],
       SUM(p.rows)                                                AS [rows]
FROM sys.partitions AS p
JOIN sys.tables     AS t ON t.object_id = p.object_id AND t.is_ms_shipped = 0
GROUP BY t.schema_id, t.name
HAVING COUNT(DISTINCT p.data_compression_desc) > 1
ORDER BY SUM(p.rows) DESC
OPTION (RECOMPILE, MAXDOP 1);
