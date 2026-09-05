-- @scope:       database
-- @resultsets:  root:object, fks:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     120
--
-- The columns of every foreign key, in key order, with the columns they
-- reference.
--
-- Why this collector exists. 070.index-columns.sql lists the key and included
-- columns of every index, and 010.objects.sql counts foreign keys, but nothing
-- in the archive said which columns a foreign key spans. The SQL Server
-- Assessment rule that flags a foreign key with no supporting index reads
-- exactly that pairing, and answering it required going back to the instance.
--
-- ONE ROW PER (FOREIGN KEY, COLUMN), NOT ONE PER FOREIGN KEY. The rule above
-- correlates foreign-key columns with index key columns, and that comparison
-- is a row-by-row join, not a string match: composite keys whose column order
-- differs between the foreign key and the index are the interesting case, and
-- an order-preserving list flattens into a string hides them. column_order is
-- constraint_column_id, the position of the column inside the key, and it is
-- information rather than presentation.
--
-- NO CORRELATION IS COMPUTED SERVER-SIDE. Which index, if any, supports a
-- foreign key is a judgement, and the corpus collects facts while topics
-- judge. This file projects the foreign-key side; 070.index-columns.sql
-- projects the index side; the archive joins them offline.
--
-- NO JUDGEMENT IS APPLIED. A foreign key is not declared unindexed here, and
-- a wide one is not declared wrong. Both readings need the index list this
-- file deliberately does not join to.
--
-- SQL Server 2012 is the floor. Nothing in sys.foreign_key_columns is newer;
-- no column was excluded for that reason.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

/* No cap on either result set, deliberately: a truncated foreign-key list
   could not be joined to a complete index list, and the join is the reason
   this file exists. The two counts below are read from the same population
   the array is built from, so on every run listed equals total — the pair is
   here so a reader of a truncated archive (a failed read mid-batch, for
   instance) can tell the list is short rather than assume master is clean. */
SELECT DB_NAME()                                                  AS [database],
       CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       (SELECT COUNT(*)
          FROM sys.foreign_keys AS fk
          JOIN sys.tables AS t
            ON t.object_id = fk.parent_object_id AND t.is_ms_shipped = 0)
                                                                  AS [fks_total],
       (SELECT COUNT(*)
          FROM sys.foreign_key_columns AS fkc
          JOIN sys.foreign_keys AS fk
            ON fk.object_id = fkc.constraint_object_id
          JOIN sys.tables AS t
            ON t.object_id = fk.parent_object_id AND t.is_ms_shipped = 0)
                                                                  AS [fk_columns_total]
OPTION (RECOMPILE, MAXDOP 1);

SELECT fk.name                                                     AS [fk],
       SCHEMA_NAME(t.schema_id) + '.' + t.name                     AS [table],
       c.name                                                      AS [column],
       fkc.constraint_column_id                                    AS [column_order],
       SCHEMA_NAME(rt.schema_id) + '.' + rt.name                   AS [referenced_table],
       rc.name                                                     AS [referenced_column]
FROM       sys.foreign_keys        AS fk
JOIN       sys.foreign_key_columns AS fkc
        ON fkc.constraint_object_id = fk.object_id
JOIN       sys.tables              AS t
        ON t.object_id = fk.parent_object_id AND t.is_ms_shipped = 0
JOIN       sys.columns             AS c
        ON c.object_id = fkc.parent_object_id AND c.column_id = fkc.parent_column_id
JOIN       sys.tables              AS rt
        ON rt.object_id = fkc.referenced_object_id
JOIN       sys.columns             AS rc
        ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
ORDER BY t.schema_id, t.name, fk.name, fkc.constraint_column_id
OPTION (RECOMPILE, MAXDOP 1);
