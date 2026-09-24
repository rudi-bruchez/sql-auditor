-- @scope:       database
-- @resultsets:  root:object, indexes:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     120
-- @profiles:    space
--
-- The definition of every index: its key columns in order, its included
-- columns, its filter, and the filegroup or partition scheme it sits on.
--
-- Why this collector exists. 020.index-usage.sql reports, for every index, how
-- often it was sought, scanned and updated — and never which columns it is on.
-- The same file reports the columns of the indexes SQL Server *suggests*
-- creating, because sys.dm_db_missing_index_details supplies them. So the
-- archive could say "create an index on (customer_id, order_date) INCLUDE
-- (total)" and could not say that such an index already exists. Answering that
-- is the first thing anyone does with a missing-index list, and it required
-- going back to the instance.
--
-- ONE ROW PER INDEX, NOT ONE PER COLUMN, and the choice is about being able to
-- compare. sys.index_columns naturally yields one row per column; the two key
-- and included lists are concatenated here into the same shape
-- sys.dm_db_missing_index_details already uses in 020, so the existing index
-- and the suggested one can be read side by side without reshaping either. A
-- normalised form would be tidier and would make the one comparison this file
-- exists for a manual chore.
--
-- DESC IS PART OF THE KEY AND IS KEPT. An index on (a, b DESC) does not serve
-- ORDER BY a, b — it serves ORDER BY a, b DESC, or the whole thing reversed.
-- Dropping the suffix would make the string wrong for the single question it is
-- asked. Included columns carry no direction and have none appended.
--
-- IDENTIFIERS ARE QUOTENAME'd, and the reason is not the comma. It is that
-- sys.dm_db_missing_index_details brackets everything it returns, 020 projects
-- those strings verbatim, and this file emitted bare names: the two lists a
-- reader is asked to compare used different conventions on every table, not
-- only on the strange ones. QUOTENAME doubles an inner right bracket, so a
-- column named a]b arrives as [a]]b] from both sides now, which is what makes
-- the comparison a string comparison again.
--
-- The worst collision is not the comma either. Measured on SQL Server 2025
-- (17.0.4065.4): an index on ([x] DESC) and an index on ([x DESC]) both came
-- out of here as keys "x DESC" with key_count 1, which are the only two
-- signals the archive carries, so nothing downstream could tell them apart or
-- even know to abstain. Quoted, they are [x] DESC and [x DESC], and the
-- direction marker is the part that sits OUTSIDE the closing bracket.
--
-- The table field stays unquoted, as schema + '.' + name, because 020 builds
-- it the same way. It is consistent between the two and it cannot be split
-- back apart; that asymmetry is known and is not closed here.
--
-- FOR XML PATH rather than STRING_AGG, which is SQL Server 2017 and this floor
-- is 2012. TYPE and .value() are not decoration: without them a column named
-- with an ampersand comes back as &amp; and the archive states a column name
-- that does not exist.
--
-- HEAPS ARE ABSENT, and that is correct rather than an omission: index_id = 0
-- has no index_columns rows to report. 050.heaps.sql covers them, and
-- 010.objects.sql counts them.
--
-- NO JUDGEMENT IS APPLIED. Two indexes sharing a leading column are not
-- declared redundant here. Whether the narrower one can go depends on what
-- seeks it, what it makes unique, and what a plan would do without it — the
-- usage counters in 020 and the plans in 80.workload are the other half, and
-- putting the verdict in the collector would fix an answer that needs both.
--
-- THE KEY WIDTH IS COLLECTED BECAUSE A REBUILD CANNOT BE SIZED WITHOUT IT.
-- An ONLINE operation on a clustered index builds a temporary mapping index
-- from old bookmarks to new, one row per table row, and with SORT_IN_TEMPDB
-- OFF it is built in the data file rather than in tempdb. Its size is the row
-- count times the width of the bookmarks it maps, so on a large table it runs
-- to tens of gigabytes and it is the term routinely left out of a sizing.
-- Nothing else in the archive carries a key width: 060.columns has the widths
-- but not which columns are keys, and the key list here is a string of names.
--
-- key_bytes is the DECLARED width, which is the safe high assumption for
-- sizing and not the average row. The uniquifier of a non-unique clustered
-- index is not added: is_unique sits beside it and the analysis adds the four
-- bytes where they apply, rather than the collector deciding it. A key column
-- of MAX type is impossible, so a negative max_length would mean the view was
-- read wrongly; it produces NULL for the whole index rather than a plausible
-- small number.
--
-- SQL Server 2012 is the floor. Not collected for that reason:
--   sys.indexes.optimize_for_sequential_key   (2019)
--   sys.index_columns on columnstore ordering (2019, ordered clustered CCI)
--   sys.indexes.compression_delay             (2016)
--   sys.indexes.auto_created                  (2017)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

/* No cap on this one, deliberately, and the two counts below prove it rather
   than asking to be believed: indexes_total and indexes_listed are read from
   the same population and are equal on every run. 020.index-usage.sql lists
   every index without a TOP for the same reason — a key list that covered only
   part of the indexes could not be joined to a usage list that covers all of
   them. */
SELECT DB_NAME()                                                  AS [database],
       CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       (SELECT COUNT(*)
        FROM sys.indexes AS i
        JOIN sys.objects AS o ON o.object_id = i.object_id
        WHERE i.index_id > 0 AND o.type = 'U' AND o.is_ms_shipped = 0)
                                                                  AS [indexes_total],
       (SELECT COUNT(*)
        FROM sys.index_columns AS ic
        JOIN sys.indexes AS i ON i.object_id = ic.object_id AND i.index_id = ic.index_id
        JOIN sys.objects AS o ON o.object_id = i.object_id
        WHERE i.index_id > 0 AND o.type = 'U' AND o.is_ms_shipped = 0)
                                                                  AS [columns_in_indexes],
       (SELECT COUNT(*)
        FROM sys.indexes AS i
        JOIN sys.objects AS o ON o.object_id = i.object_id
        WHERE i.index_id > 0 AND o.type = 'U' AND o.is_ms_shipped = 0
          AND i.filter_definition IS NOT NULL)                    AS [filtered_indexes]
OPTION (RECOMPILE, MAXDOP 1);

/* Ordered by table then index_id, which is the order 020.index-usage.sql uses,
   so the two files line up line by line without either being sorted first. */
SELECT SCHEMA_NAME(o.schema_id) + '.' + o.name                    AS [table],
       i.name                                                     AS [index_name],
       i.index_id                                                 AS [index_id],
       i.type_desc                                                AS [index_type],
       /* key_ordinal > 0 is what makes a column a key. A clustered index's
          non-key columns are the table's other columns and are not listed
          here at all — sys.index_columns does not carry them, and 060.columns
          does. */
       STUFF((SELECT ', ' + QUOTENAME(c.name)
                   + CASE WHEN ic.is_descending_key = 1 THEN ' DESC' ELSE '' END
              FROM sys.index_columns AS ic
              JOIN sys.columns       AS c ON c.object_id = ic.object_id
                                         AND c.column_id = ic.column_id
              WHERE ic.object_id = i.object_id
                AND ic.index_id  = i.index_id
                AND ic.key_ordinal > 0
              ORDER BY ic.key_ordinal
              FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, '')
                                                                  AS [keys],
       (SELECT COUNT(*) FROM sys.index_columns AS ic
        WHERE ic.object_id = i.object_id AND ic.index_id = i.index_id
          AND ic.key_ordinal > 0)                                 AS [key_count],
       (SELECT CASE WHEN MIN(c.max_length) < 0 THEN NULL
                    ELSE SUM(CAST(c.max_length AS int)) END
        FROM sys.index_columns AS ic
        JOIN sys.columns       AS c ON c.object_id = ic.object_id
                                   AND c.column_id = ic.column_id
        WHERE ic.object_id = i.object_id AND ic.index_id = i.index_id
          AND ic.key_ordinal > 0)                                 AS [key_bytes],
       STUFF((SELECT ', ' + QUOTENAME(c.name)
              FROM sys.index_columns AS ic
              JOIN sys.columns       AS c ON c.object_id = ic.object_id
                                         AND c.column_id = ic.column_id
              WHERE ic.object_id = i.object_id
                AND ic.index_id  = i.index_id
                AND ic.is_included_column = 1
              ORDER BY ic.index_column_id
              FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, '')
                                                                  AS [included],
       (SELECT COUNT(*) FROM sys.index_columns AS ic
        WHERE ic.object_id = i.object_id AND ic.index_id = i.index_id
          AND ic.is_included_column = 1)                          AS [included_count],
       i.filter_definition                                        AS [filter_definition],
       CAST(i.is_unique AS int)                                   AS [is_unique],
       CAST(i.is_primary_key AS int)                              AS [is_primary_key],
       CAST(i.is_unique_constraint AS int)                        AS [is_unique_constraint],
       CAST(i.is_disabled AS int)                                 AS [is_disabled],
       CAST(i.is_padded AS int)                                   AS [is_padded],
       /* 0 means unspecified. The engine fills the leaf pages the same way for
          0 and for 100: neither reserves free space. Measured on SQL Server
          2025, the same 50000 rows under a clustered index built without a
          FILLFACTOR and one built WITH (FILLFACTOR = 100), both settled at
          about 99.5% page fullness. So a reader must not treat a 0 as a
          setting that needs correcting to 100; there is nothing to correct.
          The value is still left raw rather than folded to 100, because the
          two say different things about intent: 0 is nobody ever chose, 100 is
          somebody chose. Only the second tells you a decision was taken. */
       i.fill_factor                                              AS [fill_factor],
       CAST(i.allow_page_locks AS int)                            AS [allow_page_locks],
       CAST(i.allow_row_locks AS int)                             AS [allow_row_locks],
       /* Where the index physically lives — a filegroup name, or the name of a
          partition scheme. Nothing else in the corpus reports it, and an index
          on the same filegroup as the data it duplicates is a different
          operational fact from one placed apart. */
       ds.name                                                    AS [data_space],
       ds.type_desc                                               AS [data_space_type],
       CASE WHEN ds.type = 'PS' THEN 1 ELSE 0 END                 AS [is_partitioned]
FROM       sys.indexes     AS i
JOIN       sys.objects     AS o  ON o.object_id = i.object_id
LEFT JOIN  sys.data_spaces AS ds ON ds.data_space_id = i.data_space_id
WHERE i.index_id > 0
  AND o.type = 'U'
  AND o.is_ms_shipped = 0
ORDER BY o.schema_id, o.name, i.index_id
OPTION (RECOMPILE, MAXDOP 1);
