-- @scope:       database
-- @resultsets:  root:object, queries:array
-- @permissions: CONNECT, VIEW SERVER STATE, VIEW ANY DEFINITION
-- @timeout:     120
-- @min_version: 15
--
-- Which queries wanted each missing-index suggestion, from SQL Server 2019's
-- sys.dm_db_missing_index_group_stats_query. 020.index-usage.sql carries the
-- suggestions and says how often they were met; it cannot say whether four
-- thousand seeks are one nightly report or four hundred statements, which is
-- what anyone asks before building the index.
-- docs/missing-index-queries-spec.md is the design.
--
-- IDENTITY, NEVER TEXT. This file projects query_hash and query_plan_hash, as
-- 80.workload/030.implicit-conversions.sql and 060.spills.sql already do in
-- every default archive. It MUST NEVER project last_sql_handle,
-- last_statement_sql_handle, last_statement_start_offset or
-- last_statement_end_offset. The first two turn a hash into a statement and
-- the offsets cut the batch down to the line: measured, the handle and the
-- two offsets return the statement whole, literals included, while the plan
-- is in cache. The sibling last_statement_sql_handle is refused by
-- sys.dm_exec_sql_text (Msg 12413), which is exactly what makes the other one
-- look safe to someone reading the column list.
--
-- THE CAP IS PER SUGGESTION, and that is not decoration. A single list capped
-- by seeks drops whole suggestions: measured on a fixture, 500 rows all
-- belonging to one suggestion while another with three queries appeared
-- nowhere. It would also answer the file's own question in the wrong
-- direction, since the tail of small statements is what tells a report from a
-- crowd. Twenty queries per suggestion, a thousand rows in the file, and
-- every row says how many queries its suggestion has in all.
--
-- THE ROWS ARE NOT BOUNDED BY THE 600 GROUPS. That bound is on suggestions;
-- one suggestion carried 625 query rows in a measurement. Saturation is read
-- from 020, which projects the instance-wide suggestion count.
--
-- THE VIEW IS THE EXECUTION SIDE of a group: zero until the statements have
-- run. Its rows survive DBCC FREEPROCCACHE and do not survive taking the
-- database offline, which is what the suggestions themselves do.
--
-- VIEW ANY DEFINITION is for the object name, not for the view: a login with
-- VIEW SERVER STATE alone reads every column of it.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @err int = 0, @msg nvarchar(2048) = N'', @collected bit = 0;

DECLARE @q TABLE (
    [group_handle]           int           NOT NULL,
    [table]                  nvarchar(520) NULL,
    [equality_columns]       nvarchar(4000) NULL,
    [inequality_columns]     nvarchar(4000) NULL,
    [included_columns]       nvarchar(4000) NULL,
    [query_hash]             varchar(18)   NULL,
    [query_plan_hash]        varchar(18)   NULL,
    [user_seeks]             bigint        NULL,
    [user_scans]             bigint        NULL,
    [avg_total_user_cost]    decimal(18,4) NULL,
    [avg_user_impact_pct]    decimal(5,1)  NULL,
    [last_user_seek]         datetime      NULL,
    [last_user_scan]         datetime      NULL,
    [queries_for_suggestion] int           NULL,
    [rank_in_suggestion]     int           NOT NULL);

/* One read, ranked per suggestion, so the cap keeps every suggestion that has
   queries. queries_for_suggestion is counted over the same window, which is
   what makes the pre-cap figure available per row rather than only in the
   root. The columns the header forbids are absent here and in the projection
   below, and no star expansion of the view exists in this file. */
BEGIN TRY
    INSERT INTO @q
    SELECT x.[group_handle], x.[table], x.[equality_columns],
           x.[inequality_columns], x.[included_columns],
           x.[query_hash], x.[query_plan_hash],
           x.[user_seeks], x.[user_scans],
           x.[avg_total_user_cost], x.[avg_user_impact_pct],
           x.[last_user_seek], x.[last_user_scan],
           x.[queries_for_suggestion], x.[rank_in_suggestion]
    FROM (
        SELECT mig.index_group_handle                                  AS [group_handle],
               OBJECT_SCHEMA_NAME(mid.object_id, mid.database_id) + '.'
             + OBJECT_NAME(mid.object_id, mid.database_id)             AS [table],
               mid.equality_columns                                    AS [equality_columns],
               mid.inequality_columns                                  AS [inequality_columns],
               mid.included_columns                                    AS [included_columns],
               CONVERT(varchar(18), gsq.query_hash, 1)                 AS [query_hash],
               CONVERT(varchar(18), gsq.query_plan_hash, 1)            AS [query_plan_hash],
               gsq.user_seeks                                          AS [user_seeks],
               gsq.user_scans                                          AS [user_scans],
               CAST(gsq.avg_total_user_cost AS decimal(18,4))          AS [avg_total_user_cost],
               CAST(gsq.avg_user_impact AS decimal(5,1))               AS [avg_user_impact_pct],
               gsq.last_user_seek                                      AS [last_user_seek],
               gsq.last_user_scan                                      AS [last_user_scan],
               COUNT(*) OVER (PARTITION BY mig.index_group_handle)     AS [queries_for_suggestion],
               ROW_NUMBER() OVER (PARTITION BY mig.index_group_handle
                                  ORDER BY gsq.user_seeks DESC, gsq.user_scans DESC,
                                           gsq.query_hash)             AS [rank_in_suggestion]
        FROM sys.dm_db_missing_index_group_stats_query AS gsq
        JOIN sys.dm_db_missing_index_groups AS mig
          ON mig.index_group_handle = gsq.group_handle
        JOIN sys.dm_db_missing_index_details AS mid
          ON mid.index_handle = mig.index_handle
        WHERE mid.database_id = DB_ID()) AS x
    WHERE x.[rank_in_suggestion] <= 20
    OPTION (RECOMPILE, MAXDOP 1);
    SET @collected = 1;
END TRY
BEGIN CATCH
    SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

/* The counts are BEFORE the cap and say so in their names, which is the one
   thing that lets a reader see what the cap took. They cost a second read of
   the same three views: the capped read cannot produce them. */
SELECT DB_NAME()                                                  AS [database],
       CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       CAST(@collected AS int)                                    AS [collected],
       @err                                                       AS [error_number],
       NULLIF(@msg, N'')                                          AS [error_message],
       c.[rows]                                                   AS [counts.rows_before_cap],
       c.[suggestions]                                            AS [counts.suggestions_with_queries_before_cap],
       c.[hashes]                                                 AS [counts.distinct_query_hashes_before_cap],
       20                                                         AS [listing_cap_per_suggestion],
       1000                                                       AS [listing_cap]
FROM (SELECT COUNT(*)                                  AS [rows],
             COUNT(DISTINCT mig.index_group_handle)    AS [suggestions],
             COUNT(DISTINCT gsq.query_hash)            AS [hashes]
      FROM sys.dm_db_missing_index_group_stats_query AS gsq
      JOIN sys.dm_db_missing_index_groups AS mig
        ON mig.index_group_handle = gsq.group_handle
      JOIN sys.dm_db_missing_index_details AS mid
        ON mid.index_handle = mig.index_handle
      WHERE mid.database_id = DB_ID()) AS c
OPTION (RECOMPILE, MAXDOP 1);

/* Ordered by suggestion and then by the rank that chose the rows, so two
   collections of the same instance differ only where the instance did. */
SELECT TOP (1000)
       q.[group_handle]                                           AS [group_handle],
       q.[table]                                                  AS [table],
       q.[equality_columns]                                       AS [equality_columns],
       q.[inequality_columns]                                     AS [inequality_columns],
       q.[included_columns]                                       AS [included_columns],
       q.[queries_for_suggestion]                                 AS [queries_for_suggestion],
       q.[query_hash]                                             AS [query_hash],
       q.[query_plan_hash]                                        AS [query_plan_hash],
       q.[user_seeks]                                             AS [user_seeks],
       q.[user_scans]                                             AS [user_scans],
       q.[avg_total_user_cost]                                    AS [avg_total_user_cost],
       q.[avg_user_impact_pct]                                    AS [avg_user_impact_pct],
       CONVERT(varchar(23), q.[last_user_seek], 126)              AS [last_user_seek],
       CONVERT(varchar(23), q.[last_user_scan], 126)              AS [last_user_scan]
FROM @q AS q
ORDER BY q.[group_handle], q.[rank_in_suggestion]
OPTION (RECOMPILE, MAXDOP 1);
