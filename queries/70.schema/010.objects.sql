-- @scope:       database
-- @resultsets:  root:object, object_counts:array, tables:array, untrusted_constraints:array, deprecated_types:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     120
--
-- The shape of one database: what objects exist, which tables have no
-- clustered index or no primary key, which constraints the optimizer does not
-- trust, and which columns use types retired in 2005.
--
-- Why this collector exists: it is the largest single gap between what the
-- corpus reads and what the two harvested bodies of work read — 22 catalog
-- objects carrying 276 references across the inventory, more than any other
-- theme. And on the audit that prompted it, the biggest object on the whole
-- instance was a 914-million-row table with no clustered index, which nothing
-- in the corpus could see.
--
-- WHAT IS COUNTED AND WHAT IS LISTED ARE DIFFERENT DECISIONS. Object counts
-- are exhaustive because they are cheap and bounded by the number of types.
-- Tables are capped at the 200 largest by row count, and the cap is reported:
-- a database with 5 000 tables would otherwise produce an archive nobody
-- opens, and the tail of that list is empty tables. Constraints and deprecated
-- columns are capped the same way and for the same reason.
--
-- is_not_trusted is the point of the constraints result set, and it is not a
-- style question. A foreign key or check constraint left untrusted after a
-- bulk load or a NOCHECK re-enable is ignored by the optimizer when it
-- simplifies a plan — the constraint still enforces new rows, but it stops
-- earning its keep in query plans, silently and permanently.
--
-- text, ntext and image were replaced by varchar(max), nvarchar(max) and
-- varbinary(max) in SQL Server 2005 and have been announced for removal ever
-- since. They are listed rather than counted because migrating one is a
-- schema change someone has to plan.
--
-- NO JUDGEMENT IS APPLIED. A heap is not a defect — a staging table written
-- once and read once is legitimately a heap, and a small lookup table without
-- a primary key may be deliberate. The collector reports structure; deciding
-- which of these matter needs the row counts, the workload and the calendar,
-- and that is the analysis layer's work.
--
-- SQL Server 2012 is the floor. Not collected for that reason:
--   sys.tables.is_memory_optimized   (2014)
--   sys.tables.temporal_type         (2016)
--   sys.tables.is_external           (2016)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT DB_NAME()                                                  AS [database],
       SYSDATETIME()                                              AS [collected_at],
       (SELECT COUNT(*) FROM sys.schemas AS s
        WHERE s.schema_id < 16384 AND s.name NOT IN ('sys','INFORMATION_SCHEMA')) AS [counts.schemas],
       (SELECT COUNT(*) FROM sys.tables AS t WHERE t.is_ms_shipped = 0)           AS [counts.tables],
       (SELECT COUNT(*) FROM sys.views AS v WHERE v.is_ms_shipped = 0)            AS [counts.views],
       (SELECT COUNT(*) FROM sys.objects AS o
        WHERE o.is_ms_shipped = 0 AND o.type = 'P')               AS [counts.procedures],
       (SELECT COUNT(*) FROM sys.objects AS o
        WHERE o.is_ms_shipped = 0 AND o.type IN ('FN','IF','TF','FS','FT')) AS [counts.functions],
       (SELECT COUNT(*) FROM sys.triggers AS tr WHERE tr.is_ms_shipped = 0)  AS [counts.triggers],
       (SELECT COUNT(*) FROM sys.tables AS t
        JOIN sys.indexes AS i ON i.object_id = t.object_id AND i.index_id = 0
        WHERE t.is_ms_shipped = 0)                                AS [counts.heaps],
       (SELECT COUNT(*) FROM sys.tables AS t
        WHERE t.is_ms_shipped = 0
          AND NOT EXISTS (SELECT 1 FROM sys.indexes AS i
                          WHERE i.object_id = t.object_id AND i.is_primary_key = 1)) AS [counts.tables_without_primary_key],
       (SELECT COUNT(*) FROM sys.foreign_keys AS f WHERE f.is_not_trusted = 1 OR f.is_disabled = 1)
                                                                  AS [counts.untrusted_foreign_keys],
       (SELECT COUNT(*) FROM sys.check_constraints AS k WHERE k.is_not_trusted = 1 OR k.is_disabled = 1)
                                                                  AS [counts.untrusted_check_constraints],
       (SELECT COUNT(*) FROM sys.columns AS c
        JOIN sys.types AS ty ON ty.user_type_id = c.user_type_id
        JOIN sys.tables AS t ON t.object_id = c.object_id AND t.is_ms_shipped = 0
        WHERE ty.name IN ('text','ntext','image'))                AS [counts.deprecated_type_columns],
       200                                                        AS [listing_cap]
OPTION (RECOMPILE, MAXDOP 1);

SELECT o.type_desc                                                AS [type],
       COUNT(*)                                                   AS [objects]
FROM sys.objects AS o
WHERE o.is_ms_shipped = 0
GROUP BY o.type_desc
ORDER BY COUNT(*) DESC
OPTION (RECOMPILE, MAXDOP 1);

/* The 200 largest tables by row count, with the two structural facts that a
   row count alone cannot supply. A heap of nine rows and a heap of nine
   hundred million are the same fact and completely different problems, which
   is why this is ordered by size rather than by name. */
SELECT TOP (200)
       SCHEMA_NAME(t.schema_id) + '.' + t.name                    AS [table],
       CASE WHEN EXISTS (SELECT 1 FROM sys.indexes AS i
                         WHERE i.object_id = t.object_id AND i.index_id = 0)
            THEN 1 ELSE 0 END                                     AS [structure.is_heap],
       CASE WHEN EXISTS (SELECT 1 FROM sys.indexes AS i
                         WHERE i.object_id = t.object_id AND i.is_primary_key = 1)
            THEN 1 ELSE 0 END                                     AS [structure.has_primary_key],
       (SELECT COUNT(*) FROM sys.indexes AS i
        WHERE i.object_id = t.object_id AND i.index_id > 1)       AS [structure.nonclustered_indexes],
       (SELECT COUNT(*) FROM sys.columns AS c WHERE c.object_id = t.object_id) AS [structure.columns],
       ps.row_count                                               AS [size.rows],
       CAST(ps.reserved_page_count * 8.0 / 1024 AS DECIMAL(18,2)) AS [size.reserved_mb],
       t.create_date                                              AS [create_date],
       t.modify_date                                              AS [modify_date]
FROM sys.tables AS t
CROSS APPLY (SELECT SUM(p.row_count)           AS row_count,
                    SUM(p.reserved_page_count) AS reserved_page_count
             FROM sys.dm_db_partition_stats AS p
             WHERE p.object_id = t.object_id AND p.index_id IN (0, 1)) AS ps
WHERE t.is_ms_shipped = 0
ORDER BY ps.row_count DESC
OPTION (RECOMPILE, MAXDOP 1);

/* Untrusted and disabled are reported separately because they are different
   states with different fixes: a disabled constraint enforces nothing at all,
   an untrusted one enforces new rows but is ignored by the optimizer. */
SELECT TOP (200)
       'FOREIGN_KEY'                                              AS [kind],
       SCHEMA_NAME(f.schema_id) + '.' + OBJECT_NAME(f.parent_object_id) AS [table],
       f.name                                                     AS [constraint_name],
       CAST(f.is_disabled AS int)                                 AS [is_disabled],
       CAST(f.is_not_trusted AS int)                              AS [is_not_trusted]
FROM sys.foreign_keys AS f
WHERE f.is_not_trusted = 1 OR f.is_disabled = 1
UNION ALL
SELECT TOP (200)
       'CHECK_CONSTRAINT',
       SCHEMA_NAME(k.schema_id) + '.' + OBJECT_NAME(k.parent_object_id),
       k.name,
       CAST(k.is_disabled AS int),
       CAST(k.is_not_trusted AS int)
FROM sys.check_constraints AS k
WHERE k.is_not_trusted = 1 OR k.is_disabled = 1
OPTION (RECOMPILE, MAXDOP 1);

SELECT TOP (200)
       SCHEMA_NAME(t.schema_id) + '.' + t.name                    AS [table],
       c.name                                                     AS [column],
       ty.name                                                    AS [type],
       CAST(c.is_nullable AS int)                                 AS [is_nullable]
FROM sys.columns AS c
JOIN sys.types  AS ty ON ty.user_type_id = c.user_type_id
JOIN sys.tables AS t  ON t.object_id = c.object_id AND t.is_ms_shipped = 0
WHERE ty.name IN ('text', 'ntext', 'image')
ORDER BY SCHEMA_NAME(t.schema_id), t.name, c.column_id
OPTION (RECOMPILE, MAXDOP 1);
