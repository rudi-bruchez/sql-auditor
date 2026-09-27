-- @scope:       instance
-- @resultsets:  root:object, transactions:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- The transactions open when the collector ran, the oldest first: when each
-- began, whether its session is doing anything, and how much log it holds.
--
-- Why this collector exists. 20.databases/024.log-stats says a log cannot be
-- truncated and why, and log_reuse_wait_desc = ACTIVE_TRANSACTION names the
-- cause without naming the transaction. Nothing else in the corpus dates an
-- open transaction or says which session holds it, so an audit could report a
-- log held for days and not whether it was one forgotten BEGIN TRAN or a
-- workload that is never quiet. Found missing by a comparison with a
-- monitoring tool's collection, September 2026.
--
-- IT READS SESSIONS, NOT REQUESTS. The dangerous case is a
-- session that opened a transaction, finished its batch and went to sleep with
-- it still open: an application that forgot to commit, or a query window left
-- open in a management tool. It has no row in sys.dm_exec_requests, so a query
-- built on requests does not see it at all. Measured on SQL Server 2025 CU7:
-- a session that inserted 2 000 rows and then slept held 677 988 bytes of log
-- and appeared here as sleeping. A monitoring tool's report of long open
-- transactions, built on requests, was measured the same month returning
-- nothing for a session in that state.
--
-- THE LOG FIGURES COUNT ONLY THE DATABASES THE TRANSACTION WROTE TO. Every
-- transaction also has a row in sys.dm_tran_database_transactions for the
-- database it merely has as context, with no begin time and no log. Those rows
-- are left out, so databases_written is 0 for a transaction that only read,
-- which is exactly the distinction a reader needs: a read transaction holds
-- locks, a write transaction holds locks and log.
--
-- No login, no host name, no program name and no statement text, the same
-- line 046.local-sessions draws. The session id is kept because it is how a
-- reader joins this list to the blocking reports of 063 and to the deadlock
-- graphs of 061, which name sessions by id. The collector's own session is
-- left out.
--
-- ONE ROW PER SESSION AND TRANSACTION, KEYED BY transaction_id. A bound
-- session or a distributed transaction enlists several sessions in one
-- transaction, and MARS can give one session several; the pair is the row, and
-- transaction_id says which rows are the same transaction. The counts are of
-- distinct transactions, and the log figures, which belong to the transaction,
-- repeat on each of its rows.
--
-- A TRANSACTION WITH NO SESSION IS KEPT ONLY IF IT IS DISTRIBUTED. An in-doubt
-- or orphaned DTC transaction can outlive every session and hold its locks and
-- log until someone resolves it by its unit of work, which is projected. Every
-- other sessionless transaction is the engine's own: measured on an idle
-- 17.0.4065.4, all twelve were worktables and workfiles. The DTC case was not
-- reproduced, the lab having no coordinator; it is read from the
-- documentation. User transactions are listed before system ones, so internal
-- work cannot crowd a forgotten user transaction out of the fifty.
--
-- The log a transaction holds is split as the view splits it: what it wrote
-- itself, and what system transactions wrote on its behalf.
--
-- It is a snapshot. A transaction that opened and closed between two runs is
-- not here, and the age is the age at the moment of collection.
--
-- SQL Server 2012 is the floor, and every view read here predates it.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @err_transactions int = 0, @msg nvarchar(2048) = N'';

DECLARE @transactions TABLE (
    [transaction_id]         bigint,
    [session_id]             int NULL,
    [is_user_transaction]    int NULL,
    [dtc_uow]                uniqueidentifier NULL,
    [session_status]         nvarchar(30) NULL,
    [is_user_process]        int NULL,
    [begin_time]             datetime,
    [age_seconds]            int,
    [type]                   nvarchar(20),
    [state]                  int,
    [open_transaction_count] int NULL,
    [idle_seconds]           int NULL,
    [databases_written]      int,
    [log_bytes_used]         bigint NULL,
    [log_bytes_reserved]     bigint NULL,
    [log_bytes_used_system]  bigint NULL,
    [log_bytes_reserved_system] bigint NULL,
    [first_write_time]       datetime NULL);

BEGIN TRY
    INSERT INTO @transactions
    SELECT at.transaction_id,
           st.session_id,
           CAST(st.is_user_transaction AS int),
           at.transaction_uow,
           s.status,
           CAST(s.is_user_process AS int),
           at.transaction_begin_time,
           DATEDIFF(second, at.transaction_begin_time, GETDATE()),
           CASE at.transaction_type
                WHEN 1 THEN N'read/write'
                WHEN 2 THEN N'read-only'
                WHEN 3 THEN N'system'
                WHEN 4 THEN N'distributed'
           END,
           -- Raw, as documented for sys.dm_tran_active_transactions: 2 is
           -- active, 7 is being rolled back, 5 is a distributed transaction
           -- prepared and waiting for its coordinator.
           at.transaction_state,
           s.open_transaction_count,
           -- How long a sleeping session has been doing nothing while holding
           -- the transaction. NULL for a session that is working.
           CASE WHEN s.status = N'sleeping'
                THEN DATEDIFF(second, s.last_request_end_time, GETDATE()) END,
           ISNULL(w.databases, 0),
           w.log_bytes_used,
           w.log_bytes_reserved,
           w.log_bytes_used_system,
           w.log_bytes_reserved_system,
           w.first_write
    FROM      sys.dm_tran_active_transactions  AS at
    LEFT JOIN sys.dm_tran_session_transactions AS st ON st.transaction_id = at.transaction_id
    LEFT JOIN sys.dm_exec_sessions             AS s  ON s.session_id = st.session_id
    OUTER APPLY (
        SELECT COUNT(*)                                        AS databases,
               SUM(dt.database_transaction_log_bytes_used)     AS log_bytes_used,
               SUM(dt.database_transaction_log_bytes_reserved) AS log_bytes_reserved,
               -- The two system columns are int; summed as int they could
               -- overflow and cost the whole listing in the CATCH.
               SUM(CAST(dt.database_transaction_log_bytes_used_system AS bigint))     AS log_bytes_used_system,
               SUM(CAST(dt.database_transaction_log_bytes_reserved_system AS bigint)) AS log_bytes_reserved_system,
               MIN(dt.database_transaction_begin_time)         AS first_write
        FROM sys.dm_tran_database_transactions AS dt
        -- Correlated on the transaction and not on the session row: a
        -- sessionless distributed transaction has no st row, and joining
        -- through it reported every such transaction with no log at all.
        WHERE dt.transaction_id = at.transaction_id
          AND dt.database_transaction_begin_time IS NOT NULL
    ) AS w
    WHERE (st.session_id IS NOT NULL AND st.session_id <> @@SPID)
       OR (st.session_id IS NULL
           AND (at.transaction_type = 4 OR at.transaction_uow IS NOT NULL))
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_transactions = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT SYSDATETIME()                                             AS [collected_at],
       (SELECT COUNT(DISTINCT [transaction_id]) FROM @transactions) AS [counts.transactions],
       (SELECT COUNT(DISTINCT [transaction_id]) FROM @transactions
         WHERE [is_user_transaction] = 1)                        AS [counts.user],
       (SELECT COUNT(DISTINCT [transaction_id]) FROM @transactions
         WHERE [session_id] IS NULL)                             AS [counts.without_session],
       (SELECT COUNT(DISTINCT [transaction_id]) FROM @transactions
         WHERE [session_status] = N'sleeping')                   AS [counts.sleeping],
       (SELECT COUNT(DISTINCT [transaction_id]) FROM @transactions
         WHERE [databases_written] > 0)                          AS [counts.writing],
       -- System transactions left out: the engine's own work says nothing
       -- about what somebody forgot to commit.
       (SELECT MAX([age_seconds]) FROM @transactions
         WHERE ISNULL([is_user_transaction], 1) = 1)             AS [oldest.age_seconds],
       (SELECT MAX([age_seconds]) FROM @transactions
         WHERE [session_status] = N'sleeping')                   AS [oldest.sleeping_age_seconds],
       50                                                        AS [listing_cap],
       CASE WHEN @err_transactions = 0 THEN 1 ELSE 0 END         AS [collected.transactions],
       @err_transactions                                         AS [errors.transactions],
       NULLIF(@msg, N'')                                         AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

SELECT TOP (50)
       t.[transaction_id], t.[session_id], t.[is_user_transaction], t.[dtc_uow],
       t.[session_status], t.[is_user_process], t.[begin_time],
       t.[age_seconds], t.[type], t.[state], t.[open_transaction_count],
       t.[idle_seconds], t.[databases_written], t.[log_bytes_used],
       t.[log_bytes_reserved], t.[log_bytes_used_system],
       t.[log_bytes_reserved_system], t.[first_write_time]
FROM @transactions AS t
ORDER BY CASE WHEN t.[is_user_transaction] = 0 THEN 1 ELSE 0 END,
         t.[begin_time], t.[transaction_id], t.[session_id]
OPTION (RECOMPILE, MAXDOP 1);
