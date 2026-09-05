-- @scope:       instance
-- @resultsets:  root:object, hints:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- Optimizer hints the query optimizer has been asked to honour, and how many
-- optimizations that represents.
--
-- Why this collector exists. The SQL Server Assessment ruleset flags optimizer
-- hints (hints statistics), and nothing else in this archive says the
-- optimizer was ever told to stop optimizing. sys.dm_exec_query_optimizer_info
-- accumulates one row per optimizer event type; the hint family — 'hints',
-- 'join hint', 'order hint' — is what this file keeps. The LIKE is deliberate:
-- Microsoft has added hint-granular counters across releases, and matching the
-- family rather than one counter name keeps this file working when the next
-- one lands.
--
-- occurrence IS A SHARE, NOT A COUNT OF BAD QUERIES, and the root object says
-- what it is a share of. On a busy instance a handful of hinted queries is a
-- rounding error; on a quiet one it is the workload. Without the total
-- optimization count beside it, the hint counts are not interpretable, so the
-- root carries [optimizations.total] — the denominator a reader needs before
-- deciding whether 42 occurrences mean anything.
--
-- THE WINDOW IS SINCE THE LAST RESTART. These counters are in-memory only; the
-- root object carries the instance start so the window is stated next to the
-- numbers.
--
-- NO QUERY TEXT IS COLLECTED. The DMV carries counts, not plans, and joining
-- dm_exec_query_stats for the offenders would turn a metadata collector into
-- a disclosure. This file says whether hints are in use; finding which query
-- carries them is a follow-up on the instance itself.
--
-- NO JUDGEMENT IS APPLIED. A hint count above zero is a fact; whether it is
-- tolerable depends on the workload, which is a ruleset question.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       (SELECT SUM(CONVERT(bigint, oi.occurrence))
          FROM sys.dm_exec_query_optimizer_info AS oi
         WHERE oi.counter = 'optimizations')                      AS [optimizations.total],
       CONVERT(varchar(23), (SELECT sqlserver_start_time
                               FROM sys.dm_os_sys_info), 126)    AS [instance_start]
OPTION (RECOMPILE, MAXDOP 1);

SELECT oi.counter                                                 AS [counter],
       oi.occurrence                                              AS [occurrence],
       oi.value                                                   AS [value]
FROM sys.dm_exec_query_optimizer_info AS oi
WHERE oi.counter LIKE '%hint%'
ORDER BY oi.counter
OPTION (RECOMPILE, MAXDOP 1);
