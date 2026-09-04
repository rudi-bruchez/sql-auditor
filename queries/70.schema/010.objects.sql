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
-- EVERY READ HERE NAMES USER OBJECTS, SO EVERY READ IS BLOCKABLE. This is the
-- most exposed collector in the corpus on that count: sys.tables, sys.columns,
-- sys.foreign_keys, sys.check_constraints and sys.indexes all need a schema
-- stability lock on what they describe, and READ UNCOMMITTED gives up locks on
-- DATA and never on METADATA. Measured on SQL Server 2022 behind one open
-- ALTER TABLE, this file came back 1222 after ten seconds and lost all five of
-- its result sets, because a statement that fails mid-batch takes the rest of
-- the batch's output with it.
--
-- So each area reads inside its own TRY/CATCH into a table variable, the CATCH
-- assigns variables and nothing else, and the five emitting SELECTs at the
-- bottom run unconditionally. An ALTER on one table now costs the areas that
-- touch it and not the document. The root object says which area failed and
-- with what error number, so an empty array is never mistaken for an empty
-- database. See 020.index-usage.sql for the measurement behind the pattern.
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
SET LOCK_TIMEOUT 10000;

DECLARE @err_counts int = 0, @err_object_counts int = 0, @err_tables int = 0,
        @err_constraints int = 0, @err_deprecated int = 0,
        @msg nvarchar(2048) = N'';

DECLARE @n_schemas int, @n_tables int, @n_views int, @n_procedures int,
        @n_functions int, @n_triggers int, @n_heaps int, @n_no_pk int,
        @n_untrusted_fk int, @n_untrusted_ck int, @n_deprecated_cols int;

DECLARE @object_counts TABLE (
    [type]    nvarchar(60),
    [objects] int);

DECLARE @tables TABLE (
    [table]                 nvarchar(300),
    [is_heap]               int,
    [has_primary_key]       int,
    [nonclustered_indexes]  int,
    [columns]               int,
    [rows]                  bigint,
    [data_reserved_mb]      decimal(18,2),
    [data_used_mb]          decimal(18,2),
    [index_reserved_mb]     decimal(18,2),
    [total_reserved_mb]     decimal(18,2),
    [create_date]           datetime,
    [modify_date]           datetime,
    [object_id]             int);

DECLARE @constraints TABLE (
    [kind]            varchar(16),
    [table]           nvarchar(300) NULL,
    [constraint_name] sysname,
    [is_disabled]     int,
    [is_not_trusted]  int);

DECLARE @deprecated TABLE (
    [schema_name] sysname,
    [table_name]  sysname,
    [column_id]   int,
    [table]       nvarchar(300),
    [column]      sysname,
    [type]        sysname,
    [is_nullable] int);

BEGIN TRY
    SELECT @n_schemas = COUNT(*) FROM sys.schemas AS s
    WHERE s.schema_id < 16384 AND s.name NOT IN ('sys','INFORMATION_SCHEMA')
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @n_tables = COUNT(*) FROM sys.tables AS t WHERE t.is_ms_shipped = 0
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @n_views = COUNT(*) FROM sys.views AS v WHERE v.is_ms_shipped = 0
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @n_procedures = COUNT(*) FROM sys.objects AS o
    WHERE o.is_ms_shipped = 0 AND o.type = 'P'
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @n_functions = COUNT(*) FROM sys.objects AS o
    WHERE o.is_ms_shipped = 0 AND o.type IN ('FN','IF','TF','FS','FT')
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @n_triggers = COUNT(*) FROM sys.triggers AS tr WHERE tr.is_ms_shipped = 0
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @n_heaps = COUNT(*) FROM sys.tables AS t
    JOIN sys.indexes AS i ON i.object_id = t.object_id AND i.index_id = 0
    WHERE t.is_ms_shipped = 0
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @n_no_pk = COUNT(*) FROM sys.tables AS t
    WHERE t.is_ms_shipped = 0
      AND NOT EXISTS (SELECT 1 FROM sys.indexes AS i
                      WHERE i.object_id = t.object_id AND i.is_primary_key = 1)
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @n_untrusted_fk = COUNT(*) FROM sys.foreign_keys AS f
    WHERE f.is_not_trusted = 1 OR f.is_disabled = 1
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @n_untrusted_ck = COUNT(*) FROM sys.check_constraints AS k
    WHERE k.is_not_trusted = 1 OR k.is_disabled = 1
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @n_deprecated_cols = COUNT(*) FROM sys.columns AS c
    JOIN sys.types AS ty ON ty.user_type_id = c.user_type_id
    JOIN sys.tables AS t ON t.object_id = c.object_id AND t.is_ms_shipped = 0
    WHERE ty.name IN ('text','ntext','image')
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_counts = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

BEGIN TRY
    INSERT INTO @object_counts
    SELECT o.type_desc, COUNT(*)
    FROM sys.objects AS o
    WHERE o.is_ms_shipped = 0
    GROUP BY o.type_desc
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_object_counts = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* The 200 largest tables by row count, with the two structural facts that a
   row count alone cannot supply. A heap of nine rows and a heap of nine
   hundred million are the same fact and completely different problems, which
   is why this is ordered by size rather than by name.

   FOUR SIZES, NOT ONE, and the split is the whole point. This used to project a
   single [size.reserved_mb] summed over index_id IN (0, 1) — the heap or the
   clustered index and nothing else. That number reads as "the size of this
   table" and is not: on a table carrying eight nonclustered indexes the indexes
   can outweigh the data, and the archive stated half the footprint under a name
   that claimed all of it. A collector may report a partial figure; it may not
   name a partial figure as if it were the whole.

   So the data and the indexes are now separate columns, each saying which it
   is, and the total is projected rather than left to be added up — a reader
   summing two columns to get the number they wanted is a reader who can forget
   to. used is kept for the data only: free space inside a nonclustered index is
   a fragmentation question, and 030 and 050 already own that ground. */
BEGIN TRY
    INSERT INTO @tables
    SELECT TOP (200)
           SCHEMA_NAME(t.schema_id) + '.' + t.name,
           CASE WHEN EXISTS (SELECT 1 FROM sys.indexes AS i
                             WHERE i.object_id = t.object_id AND i.index_id = 0)
                THEN 1 ELSE 0 END,
           CASE WHEN EXISTS (SELECT 1 FROM sys.indexes AS i
                             WHERE i.object_id = t.object_id AND i.is_primary_key = 1)
                THEN 1 ELSE 0 END,
           (SELECT COUNT(*) FROM sys.indexes AS i
            WHERE i.object_id = t.object_id AND i.index_id > 1),
           (SELECT COUNT(*) FROM sys.columns AS c WHERE c.object_id = t.object_id),
           ps.row_count,
           CAST(ps.data_reserved  * 8.0 / 1024 AS DECIMAL(18,2)),
           CAST(ps.data_used      * 8.0 / 1024 AS DECIMAL(18,2)),
           CAST(ps.index_reserved * 8.0 / 1024 AS DECIMAL(18,2)),
           CAST(ps.total_reserved * 8.0 / 1024 AS DECIMAL(18,2)),
           t.create_date,
           t.modify_date,
           t.object_id
    FROM sys.tables AS t
    /* One pass over every partition of the table, split by index_id in the SELECT
       rather than filtered in the WHERE: the totals have to come from the same
       read, or a table written between two passes would report an index footprint
       that does not belong to the data footprint beside it.

       index_id 0 is the heap and 1 the clustered index — a table has one or the
       other, never both — and everything above 1 is a nonclustered index. ELSE 0,
       not NULL: a table with no nonclustered index has an index footprint of zero,
       which is a measurement, whereas NULL would read as "not collected". */
    CROSS APPLY (SELECT SUM(CASE WHEN p.index_id IN (0, 1) THEN p.row_count            ELSE 0 END) AS row_count,
                        SUM(CASE WHEN p.index_id IN (0, 1) THEN p.reserved_page_count  ELSE 0 END) AS data_reserved,
                        SUM(CASE WHEN p.index_id IN (0, 1) THEN p.used_page_count      ELSE 0 END) AS data_used,
                        SUM(CASE WHEN p.index_id  > 1      THEN p.reserved_page_count  ELSE 0 END) AS index_reserved,
                        SUM(p.reserved_page_count)                                                 AS total_reserved
                 FROM sys.dm_db_partition_stats AS p
                 WHERE p.object_id = t.object_id) AS ps
    WHERE t.is_ms_shipped = 0
    /* object_id is the tie-break, and 060.columns.sql repeats this ORDER BY
       verbatim. Two tables with equal row counts — empty ones, of which a real
       database has many — would otherwise be ordered by nothing in particular, and
       the 200th place could go to a different table in each of the two statements.
       The archive would then carry the columns of a table it does not list, and
       list a table whose columns are missing, with nothing anywhere saying why. */
    ORDER BY ps.row_count DESC, t.object_id
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_tables = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* Untrusted and disabled are reported separately because they are different
   states with different fixes: a disabled constraint enforces nothing at all,
   an untrusted one enforces new rows but is ignored by the optimizer. */
BEGIN TRY
    INSERT INTO @constraints
    SELECT TOP (200)
           'FOREIGN_KEY',
           SCHEMA_NAME(f.schema_id) + '.' + OBJECT_NAME(f.parent_object_id),
           f.name,
           CAST(f.is_disabled AS int),
           CAST(f.is_not_trusted AS int)
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
END TRY
BEGIN CATCH
    SELECT @err_constraints = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

BEGIN TRY
    INSERT INTO @deprecated
    SELECT TOP (200)
           SCHEMA_NAME(t.schema_id),
           t.name,
           c.column_id,
           SCHEMA_NAME(t.schema_id) + '.' + t.name,
           c.name,
           ty.name,
           CAST(c.is_nullable AS int)
    FROM sys.columns AS c
    JOIN sys.types  AS ty ON ty.user_type_id = c.user_type_id
    JOIN sys.tables AS t  ON t.object_id = c.object_id AND t.is_ms_shipped = 0
    WHERE ty.name IN ('text', 'ntext', 'image')
    ORDER BY SCHEMA_NAME(t.schema_id), t.name, c.column_id
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_deprecated = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT DB_NAME()                                                  AS [database],
       SYSDATETIME()                                              AS [collected_at],
       @n_schemas                                                 AS [counts.schemas],
       @n_tables                                                  AS [counts.tables],
       @n_views                                                   AS [counts.views],
       @n_procedures                                              AS [counts.procedures],
       @n_functions                                               AS [counts.functions],
       @n_triggers                                                AS [counts.triggers],
       @n_heaps                                                   AS [counts.heaps],
       @n_no_pk                                                   AS [counts.tables_without_primary_key],
       @n_untrusted_fk                                            AS [counts.untrusted_foreign_keys],
       @n_untrusted_ck                                            AS [counts.untrusted_check_constraints],
       @n_deprecated_cols                                         AS [counts.deprecated_type_columns],
       200                                                        AS [listing_cap],
       CASE WHEN @err_counts        = 0 THEN 1 ELSE 0 END         AS [collected.counts],
       CASE WHEN @err_object_counts = 0 THEN 1 ELSE 0 END         AS [collected.object_counts],
       CASE WHEN @err_tables        = 0 THEN 1 ELSE 0 END         AS [collected.tables],
       CASE WHEN @err_constraints   = 0 THEN 1 ELSE 0 END         AS [collected.untrusted_constraints],
       CASE WHEN @err_deprecated    = 0 THEN 1 ELSE 0 END         AS [collected.deprecated_types],
       @err_counts                                                AS [errors.counts],
       @err_object_counts                                         AS [errors.object_counts],
       @err_tables                                                AS [errors.tables],
       @err_constraints                                           AS [errors.untrusted_constraints],
       @err_deprecated                                            AS [errors.deprecated_types],
       NULLIF(@msg, N'')                                          AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

SELECT oc.[type], oc.[objects]
FROM @object_counts AS oc
ORDER BY oc.[objects] DESC
OPTION (RECOMPILE, MAXDOP 1);

SELECT t.[table],
       t.[is_heap]              AS [structure.is_heap],
       t.[has_primary_key]      AS [structure.has_primary_key],
       t.[nonclustered_indexes] AS [structure.nonclustered_indexes],
       t.[columns]              AS [structure.columns],
       t.[rows]                 AS [size.rows],
       t.[data_reserved_mb]     AS [size.data_reserved_mb],
       t.[data_used_mb]         AS [size.data_used_mb],
       t.[index_reserved_mb]    AS [size.index_reserved_mb],
       t.[total_reserved_mb]    AS [size.total_reserved_mb],
       t.[create_date]          AS [create_date],
       t.[modify_date]          AS [modify_date]
FROM @tables AS t
ORDER BY t.[rows] DESC, t.[object_id]
OPTION (RECOMPILE, MAXDOP 1);

SELECT c.[kind], c.[table], c.[constraint_name], c.[is_disabled], c.[is_not_trusted]
FROM @constraints AS c
OPTION (RECOMPILE, MAXDOP 1);

SELECT d.[table], d.[column], d.[type], d.[is_nullable]
FROM @deprecated AS d
ORDER BY d.[schema_name], d.[table_name], d.[column_id]
OPTION (RECOMPILE, MAXDOP 1);
