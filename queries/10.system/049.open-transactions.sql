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
-- IT STARTS FROM THE SESSION, NOT FROM THE REQUEST. The dangerous case is a
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
-- It is a snapshot. A transaction that opened and closed between two runs is
-- not here, and the age is the age at the moment of collection.
--
-- SQL Server 2012 is the floor, and every view read here predates it.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @err_transactions int = 0, @msg nvarchar(2048) = N'';

DECLARE @transactions TABLE (
    [session_id]             int,
    [session_status]         nvarchar(30),
    [is_user_process]        int,
    [begin_time]             datetime,
    [age_seconds]            int,
    [type]                   nvarchar(20),
    [state]                  int,
    [open_transaction_count] int,
    [idle_seconds]           int NULL,
    [databases_written]      int,
    [log_bytes_used]         bigint NULL,
    [log_bytes_reserved]     bigint NULL,
    [first_write_time]       datetime NULL);

BEGIN TRY
    INSERT INTO @transactions
    SELECT st.session_id,
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
           w.first_write
    FROM sys.dm_tran_session_transactions AS st
    JOIN sys.dm_tran_active_transactions  AS at ON at.transaction_id = st.transaction_id
    JOIN sys.dm_exec_sessions             AS s  ON s.session_id = st.session_id
    OUTER APPLY (
        SELECT COUNT(*)                                        AS databases,
               SUM(dt.database_transaction_log_bytes_used)     AS log_bytes_used,
               SUM(dt.database_transaction_log_bytes_reserved) AS log_bytes_reserved,
               MIN(dt.database_transaction_begin_time)         AS first_write
        FROM sys.dm_tran_database_transactions AS dt
        WHERE dt.transaction_id = st.transaction_id
          AND dt.database_transaction_begin_time IS NOT NULL
    ) AS w
    WHERE st.session_id <> @@SPID
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_transactions = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT SYSDATETIME()                                             AS [collected_at],
       (SELECT COUNT(*) FROM @transactions)                      AS [counts.transactions],
       (SELECT COUNT(*) FROM @transactions
         WHERE [session_status] = N'sleeping')                   AS [counts.sleeping],
       (SELECT COUNT(*) FROM @transactions
         WHERE [databases_written] > 0)                          AS [counts.writing],
       (SELECT MAX([age_seconds]) FROM @transactions)            AS [oldest.age_seconds],
       (SELECT MAX([age_seconds]) FROM @transactions
         WHERE [session_status] = N'sleeping')                   AS [oldest.sleeping_age_seconds],
       50                                                        AS [listing_cap],
       CASE WHEN @err_transactions = 0 THEN 1 ELSE 0 END         AS [collected.transactions],
       @err_transactions                                         AS [errors.transactions],
       NULLIF(@msg, N'')                                         AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

SELECT TOP (50)
       t.[session_id], t.[session_status], t.[is_user_process], t.[begin_time],
       t.[age_seconds], t.[type], t.[state], t.[open_transaction_count],
       t.[idle_seconds], t.[databases_written], t.[log_bytes_used],
       t.[log_bytes_reserved], t.[first_write_time]
FROM @transactions AS t
ORDER BY t.[begin_time], t.[session_id]
OPTION (RECOMPILE, MAXDOP 1);
