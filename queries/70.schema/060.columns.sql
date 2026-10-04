-- @scope:       database
-- @resultsets:  root:object, columns:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     120
-- @discloses:   schema_expressions
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
-- EVERY USER TABLE, AS IN 010.objects.sql. Until October 2026 both files
-- listed the union of the 200 tables with the most rows and the 50 with the
-- most reserved pages, kept identical by a test, because two selections in
-- one directory would be a trap: a table found in one file and absent from
-- the other reads as a defect in the collector. Twelve real collections taken
-- on eight client instances between August and September 2026 measured what
-- the selection cost (docs/caps-inventory.md): it bound in 7 of 27 databases,
-- of 207 to 846 tables, and left out 3 to 65 percent of their columns, 8 572
-- of 24 823 in the worst; 27 pieces of evidence of the analysis rest on this
-- file. A column row is about 465 bytes, so that database goes from 4.0 MB to
-- about 11.5 MB raw, under 1 MB zipped, and the 6.9 s the file took there
-- were already mostly the catalog work that counts every column. With no
-- selection there is nothing left to keep identical, and the test that kept
-- the copies equal now keeps 010, 060, 090 and 091 free of one.
--
-- The root keeps tables_covered beside tables_total and columns_listed beside
-- columns_total, which the analysis reads; they are equal unless a table is
-- created or dropped between two reads. listing_cap and listing_cap_by_size
-- left with the selection. Without it this file reads no DMV; VIEW SERVER
-- STATE stays declared until dropping it is decided apart, since it changes
-- which logins run the file and not what the file says.
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

/* tables_total and columns_total count every user table and every column of
   one; tables_covered and columns_listed count what the rows below carry.
   While the listing was capped the pair was what told a reader how much
   structure this file did not carry. Both are kept so that an archive from
   before October 2026 and one from after read the same way, and on a
   database whose catalog moves during the read they can differ by a table. */
SELECT DB_NAME()                                                  AS [database],
       CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       (SELECT COUNT(*) FROM sys.tables AS t WHERE t.is_ms_shipped = 0) AS [tables_covered],
       (SELECT COUNT(*) FROM sys.tables AS t WHERE t.is_ms_shipped = 0) AS [tables_total],
       (SELECT COUNT(*)
        FROM sys.columns AS c
        JOIN sys.tables  AS t ON t.object_id = c.object_id AND t.is_ms_shipped = 0)
                                                                  AS [columns_total],
       (SELECT COUNT(*)
        FROM sys.columns AS c
        JOIN sys.tables  AS t ON t.object_id = c.object_id AND t.is_ms_shipped = 0)
                                                                  AS [columns_listed]
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
JOIN       sys.types             AS ty ON ty.user_type_id = c.user_type_id
LEFT JOIN  sys.identity_columns  AS ic ON ic.object_id = c.object_id AND ic.column_id = c.column_id
LEFT JOIN  sys.computed_columns  AS cc ON cc.object_id = c.object_id AND cc.column_id = c.column_id
/* default_object_id is 0, not NULL, when a column has no default. */
LEFT JOIN  sys.default_constraints AS dc ON dc.object_id = c.default_object_id
ORDER BY t.schema_id, t.name, c.column_id
OPTION (RECOMPILE, MAXDOP 1);
