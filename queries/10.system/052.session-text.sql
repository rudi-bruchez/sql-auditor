-- @scope:         instance
-- @resultsets:    oldest_snapshot_transactions:array
-- @permissions:   VIEW SERVER STATE
-- @requires_flag: include_session_text
-- @timeout:       60
--
-- Session identity and statement text for the snapshot transactions holding
-- the tempdb version store open.
--
-- This is the one collector in the corpus whose output is not purely
-- metadata. sys.dm_exec_sql_text returns the verbatim SQL of a live batch, so
-- a parameterless statement built by string concatenation arrives here with
-- its literals intact — an account number, an email address, whatever the
-- application put in the WHERE clause. login_name, host_name and program_name
-- name the person and machine that ran it.
--
-- It is therefore gated on @requires_flag: include_session_text, off by
-- default, and the same decision that runs or skips this file sets
-- collected.session_text in the manifest. MANIFEST.txt's disclosure paragraph
-- is written from that field, so the two cannot disagree.
--
-- 050.tempdb.sql collects the same transactions without any of these columns:
-- session_id, transaction_sequence_num and elapsed_time_seconds. Join on
-- session_id to put the two back together. Keep the split — moving a column
-- back would make the default archive's disclosure paragraph untrue.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

/* Same TOP (5) and same ORDER BY as 050.tempdb.sql, so the rows correspond
   one for one when both files are present. */
SELECT TOP (5)
       ast.session_id,
       ast.transaction_sequence_num,
       ast.elapsed_time_seconds,
       es.login_name,
       es.host_name,
       es.program_name,
       est.text AS current_batch
FROM sys.dm_tran_active_snapshot_database_transactions AS ast
LEFT JOIN sys.dm_exec_sessions AS es ON es.session_id = ast.session_id
OUTER APPLY (
    SELECT r.sql_handle FROM sys.dm_exec_requests r WHERE r.session_id = ast.session_id
) AS req
OUTER APPLY sys.dm_exec_sql_text(req.sql_handle) AS est
ORDER BY ast.elapsed_time_seconds DESC
OPTION (RECOMPILE, MAXDOP 1);
