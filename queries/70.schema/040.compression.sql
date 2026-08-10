-- @scope:       database
-- @resultsets:  root:object, by_compression:array, largest_uncompressed:array, mixed_tables:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     120
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

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT DB_NAME()                                                  AS [database],
       SYSDATETIME()                                              AS [collected_at],
       CONVERT(nvarchar(128), SERVERPROPERTY('ProductVersion'))   AS [product_version],
       CONVERT(nvarchar(128), SERVERPROPERTY('Edition'))          AS [edition],
       (SELECT COUNT(DISTINCT p.object_id) FROM sys.partitions AS p
        JOIN sys.tables AS t ON t.object_id = p.object_id AND t.is_ms_shipped = 0) AS [counts.tables],
       (SELECT COUNT(*) FROM sys.partitions AS p
        JOIN sys.tables AS t ON t.object_id = p.object_id AND t.is_ms_shipped = 0) AS [counts.partitions],
       (SELECT COUNT(*) FROM sys.partitions AS p
        JOIN sys.tables AS t ON t.object_id = p.object_id AND t.is_ms_shipped = 0
        WHERE p.data_compression <> 0)                            AS [counts.compressed_partitions],
       (SELECT COUNT(DISTINCT p.object_id) FROM sys.partitions AS p
        JOIN sys.tables AS t ON t.object_id = p.object_id AND t.is_ms_shipped = 0
        WHERE p.partition_number > 1)                             AS [counts.partitioned_tables],
       200                                                        AS [listing_cap]
OPTION (RECOMPILE, MAXDOP 1);

/* The estate-level answer in one table: how much data sits under each
   compression setting. Reserved size, not row count — the question is about
   storage. */
SELECT p.data_compression_desc                                    AS [compression],
       COUNT(*)                                                   AS [partitions],
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
SELECT TOP (200)
       SCHEMA_NAME(t.schema_id) + '.' + t.name                    AS [table],
       ISNULL(i.name, '(heap)')                                   AS [index_name],
       p.index_id                                                 AS [index_id],
       i.type_desc                                                AS [index_type],
       COUNT(*)                                                   AS [partitions],
       SUM(p.rows)                                                AS [rows],
       CAST(SUM(ps.reserved_page_count) * 8.0 / 1024 AS DECIMAL(18,1)) AS [reserved_mb]
FROM       sys.partitions AS p
JOIN       sys.tables     AS t  ON t.object_id = p.object_id AND t.is_ms_shipped = 0
LEFT JOIN  sys.indexes    AS i  ON i.object_id = p.object_id AND i.index_id = p.index_id
LEFT JOIN  sys.dm_db_partition_stats AS ps
        ON ps.object_id = p.object_id AND ps.index_id = p.index_id
       AND ps.partition_number = p.partition_number
WHERE p.data_compression = 0
GROUP BY t.schema_id, t.name, i.name, p.index_id, i.type_desc
ORDER BY SUM(ps.reserved_page_count) DESC
OPTION (RECOMPILE, MAXDOP 1);

/* A table whose partitions disagree. Empty is the expected result; a row here
   is almost always an accident rather than a design. */
SELECT SCHEMA_NAME(t.schema_id) + '.' + t.name                    AS [table],
       COUNT(DISTINCT p.data_compression_desc)                    AS [distinct_settings],
       COUNT(*)                                                   AS [partitions],
       MIN(p.data_compression_desc)                               AS [setting_min],
       MAX(p.data_compression_desc)                               AS [setting_max],
       SUM(p.rows)                                                AS [rows]
FROM sys.partitions AS p
JOIN sys.tables     AS t ON t.object_id = p.object_id AND t.is_ms_shipped = 0
GROUP BY t.schema_id, t.name
HAVING COUNT(DISTINCT p.data_compression_desc) > 1
ORDER BY SUM(p.rows) DESC
OPTION (RECOMPILE, MAXDOP 1);
