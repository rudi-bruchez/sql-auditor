-- @scope:       database
-- @resultsets:  root:object, columns:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     120
-- @profiles:    space
--
-- The columns of every user table: type, nullability, identity, computed
-- expression and default constraint.
--
-- Why this collector exists. 010.objects.sql counts the columns of a table and
-- stops there, so the archive could say a table has 47 columns and never say
-- what any of them is. An auditor reading an execution plan offline needs the
-- types to make sense of an implicit conversion, the nullability to make sense
-- of a NOT IN, and the defaults to make sense of an INSERT that names half the
-- columns. None of that was in the archive.
--
-- THE LARGEST TABLES, UP TO 100 000 COLUMNS, THE SAME TABLES AS 010, 090 AND
-- 091. Until October 2026 this file and 010.objects.sql listed the union of
-- the 200 tables with the most rows and the 50 with the most reserved pages.
-- Twelve real collections taken on eight client instances between August and
-- September 2026 measured what that cost (docs/caps-inventory.md): it bound in
-- 7 of 27 databases, of 207 to 846 tables, and left out 3 to 65 percent of
-- their columns, 8 572 of 24 823 in the worst; 27 pieces of evidence of the
-- analysis rest on this file. So the cap was lifted, and put back the same
-- day in another form, because none of those databases was an ERP. On the lab,
-- 4 000 tables of 60 columns made this file 115 MB, 477 bytes a column, and
-- the collector 1.17 GB resident; 20 000 tables of 20 columns made it 190 MB
-- and the collector 2.09 GB, with the five files of 70.schema that list
-- tables, columns, statistics and heaps writing 253 MB of the 256 MiB the
-- whole run may write. An ERP schema (some 90 000 tables and millions of
-- columns for SAP) would go past what an operator's machine holds, and the
-- collector holds every row of a result in memory before it writes or
-- refuses the file, so the whole collection would be lost, not this file.
--
-- The selection is now the user tables by rows, largest first, object_id
-- breaking ties, for as long as their columns add up to 100 000 at most and
-- their statistics to 70 000 at most. 100 000 columns is about 48 MB here,
-- and the collector measured 537 MB resident on the first lab database and
-- 505 MB on the second, each cut to its first 1 666 and 5 000 tables. Every
-- database of the twelve collections fits whole under it, the largest by a
-- factor of four. One statement fills #listed_tables in 010, 060, 090 and
-- 091, and a test keeps it identical in all four: two selections in one
-- directory would be a trap, a table found in one file and absent from
-- another reading as a defect in the collector. Ordering by rows puts the
-- cut at the tail of 010's own list, which is ordered the same way.
-- Each file runs the statement for itself, seconds apart, so on a database
-- whose row counts move while the cap binds, a table near the cut can be in
-- one file and not the next. That is the margin only, and each root counts
-- what its own file listed.
--
-- The root says whether the cap bound: tables_covered beside tables_total,
-- columns_listed beside columns_total, which the analysis reads, equal unless
-- the selection cut or the catalog moved between two reads, and
-- listing_cap.columns and listing_cap.statistics for the two numbers. The
-- selection reads sys.dm_db_partition_stats for the row counts, which is
-- what the declared VIEW SERVER STATE now pays for.
--
-- WHY max_length IS PROJECTED RAW BESIDE A RENDERED DECLARATION. max_length
-- counts bytes, so an nvarchar(50) reports 100, and -1 means (max). Printing
-- nvarchar(100) for a column declared nvarchar(50) would be a false fact about
-- the schema, so the rendering divides by two for the Unicode types and the
-- catalog's own value stays beside it as the source. Read [type.declaration]
-- for convenience and [type.max_length] when it matters.
--
-- NO JUDGEMENT IS APPLIED. A nullable column is not a defect, a table of
-- nvarchar(max) is not a verdict, and a missing default is not a finding. Which
-- of these matter depends on the queries that touch them, which lives in
-- 80.workload — deciding is the analysis layer's work.
--
-- SQL Server 2012 is the floor. Not collected for that reason:
--   sys.columns.is_hidden               (2016, temporal)
--   sys.columns.generated_always_type   (2016, temporal)
--   sys.columns.is_masked               (2016, dynamic data masking)
--   sys.columns.graph_type              (2017)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

/* THE SHARED SELECTION, the same statement in 010, 060, 090 and 091, which a
   test keeps identical: the user tables by rows, largest first, object_id
   breaking ties, for as long as their columns add up to no more than
   @listing_cap_columns and their statistics to no more than
   @listing_cap_statistics. See the header for the two numbers. */
DECLARE @listing_cap_columns int = 100000, @listing_cap_statistics int = 70000;

IF OBJECT_ID('tempdb..#listed_tables') IS NOT NULL DROP TABLE #listed_tables;
CREATE TABLE #listed_tables (object_id int NOT NULL PRIMARY KEY);

INSERT INTO #listed_tables (object_id)
SELECT r.object_id
FROM (SELECT t.object_id,
             SUM(ISNULL(c.n, 0)) OVER (ORDER BY p.row_count DESC, t.object_id
                                       ROWS UNBOUNDED PRECEDING) AS columns_running,
             SUM(ISNULL(s.n, 0)) OVER (ORDER BY p.row_count DESC, t.object_id
                                       ROWS UNBOUNDED PRECEDING) AS statistics_running
      FROM      sys.tables AS t
      LEFT JOIN (SELECT ps.object_id, SUM(ps.row_count) AS row_count
                 FROM sys.dm_db_partition_stats AS ps
                 WHERE ps.index_id IN (0, 1)
                 GROUP BY ps.object_id) AS p ON p.object_id = t.object_id
      LEFT JOIN (SELECT co.object_id, COUNT(*) AS n
                 FROM sys.columns AS co
                 GROUP BY co.object_id) AS c ON c.object_id = t.object_id
      LEFT JOIN (SELECT st.object_id, COUNT(*) AS n
                 FROM sys.stats AS st
                 GROUP BY st.object_id) AS s ON s.object_id = t.object_id
      WHERE t.is_ms_shipped = 0) AS r
WHERE r.columns_running    <= @listing_cap_columns
  AND r.statistics_running <= @listing_cap_statistics
OPTION (RECOMPILE, MAXDOP 1);

/* tables_total and columns_total count every user table and every column of
   one; tables_covered and columns_listed count what the rows below carry.
   They are equal unless the selection above bound, and the gap is then the
   structure this file leaves out. listing_cap says at what. */
SELECT DB_NAME()                                                  AS [database],
       CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       (SELECT COUNT(*) FROM #listed_tables)                      AS [tables_covered],
       (SELECT COUNT(*) FROM sys.tables AS t WHERE t.is_ms_shipped = 0) AS [tables_total],
       (SELECT COUNT(*)
        FROM sys.columns AS c
        JOIN sys.tables  AS t ON t.object_id = c.object_id AND t.is_ms_shipped = 0)
                                                                  AS [columns_total],
       (SELECT COUNT(*)
        FROM sys.columns     AS c
        JOIN #listed_tables  AS lt ON lt.object_id = c.object_id)
                                                                  AS [columns_listed],
       @listing_cap_columns                                       AS [listing_cap.columns],
       @listing_cap_statistics                                    AS [listing_cap.statistics]
OPTION (RECOMPILE, MAXDOP 1);

/* One row per column, ordered by table then column_id. column_id is the
   declaration order, and that is information rather than presentation: a
   varbinary(max) declared first does not read as the same design decision as
   the same column declared last, and a reader comparing the archive to a CREATE
   TABLE script needs the order to line up. */
SELECT SCHEMA_NAME(t.schema_id) + '.' + t.name                    AS [table],
       c.name                                                     AS [column],
       c.column_id                                                AS [ordinal],
       ty.name                                                    AS [type.name],
       /* The declaration as a DBA would write it. Every branch below exists
          because max_length is in bytes and the catalog has no rendered form:
          nchar and nvarchar halve, the fixed-width types have no length to
          show at all, and -1 is (max) for all of them. */
       CASE WHEN c.max_length = -1 THEN ty.name + '(max)'
            WHEN ty.name IN ('nchar', 'nvarchar')
                 THEN ty.name + '(' + CAST(c.max_length / 2 AS varchar(11)) + ')'
            WHEN ty.name IN ('char', 'varchar', 'binary', 'varbinary')
                 THEN ty.name + '(' + CAST(c.max_length AS varchar(11)) + ')'
            WHEN ty.name IN ('decimal', 'numeric')
                 THEN ty.name + '(' + CAST(c.precision AS varchar(11))
                              + ',' + CAST(c.scale     AS varchar(11)) + ')'
            WHEN ty.name IN ('datetime2', 'datetimeoffset', 'time')
                 THEN ty.name + '(' + CAST(c.scale AS varchar(11)) + ')'
            ELSE ty.name END                                      AS [type.declaration],
       /* Bytes, from the catalog, untouched. The rendering above is a
          convenience; this is the fact. */
       c.max_length                                               AS [type.max_length],
       c.precision                                                AS [type.precision],
       c.scale                                                    AS [type.scale],
       CAST(ty.is_user_defined AS int)                            AS [type.is_user_defined],
       c.collation_name                                           AS [collation],
       CAST(c.is_nullable AS int)                                 AS [is_nullable],
       CAST(c.is_identity AS int)                                 AS [is_identity],
       /* last_value is the seed until the first insert, and NULL on a column
          that has never been used. It is here because an identity approaching
          the ceiling of its type is invisible without it, and the type is one
          column to the left. */
       CONVERT(bigint, ic.seed_value)                             AS [identity.seed],
       CONVERT(bigint, ic.increment_value)                        AS [identity.increment],
       CONVERT(bigint, ic.last_value)                             AS [identity.last_value],
       CAST(c.is_computed AS int)                                 AS [is_computed],
       cc.definition                                              AS [computed.definition],
       CAST(cc.is_persisted AS int)                               AS [computed.is_persisted],
       dc.name                                                    AS [default.name],
       dc.definition                                              AS [default.definition],
       CAST(c.is_sparse AS int)                                   AS [is_sparse],
       CAST(c.is_filestream AS int)                               AS [is_filestream],
       CAST(c.is_rowguidcol AS int)                               AS [is_rowguidcol]
FROM       sys.columns           AS c
JOIN       sys.tables            AS t  ON t.object_id = c.object_id AND t.is_ms_shipped = 0
JOIN       #listed_tables        AS lt ON lt.object_id = t.object_id
JOIN       sys.types             AS ty ON ty.user_type_id = c.user_type_id
LEFT JOIN  sys.identity_columns  AS ic ON ic.object_id = c.object_id AND ic.column_id = c.column_id
LEFT JOIN  sys.computed_columns  AS cc ON cc.object_id = c.object_id AND cc.column_id = c.column_id
/* default_object_id is 0, not NULL, when a column has no default. */
LEFT JOIN  sys.default_constraints AS dc ON dc.object_id = c.default_object_id
ORDER BY t.schema_id, t.name, c.column_id
OPTION (RECOMPILE, MAXDOP 1);

DROP TABLE #listed_tables;
