-- @scope:       instance
-- @resultsets:  root:object, samples:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- How much of this machine's CPU is going to something that is not SQL Server.
--
-- An application server sharing a host with its database is a finding an audit
-- should make on its own, and today it is made by accident: in September 2026
-- it surfaced only because the client mentioned it in passing.
-- 010.properties.sql already collects the facts of the memory side — total,
-- available, in use and locked pages — and left the arithmetic that turns them
-- into a finding to a reader who has no reason to attempt it. Both residues
-- are derived here rather than there, because both are meaningless without the
-- platform and 010 does not resolve one: a derived column whose validity
-- depends on a fact its own file cannot state is the answer-that-looks-like-an
-- -answer this corpus refuses everywhere else. 014.cpu-topology.sql is the
-- precedent for deriving at all — it projects the hardware-versus-soft-NUMA
-- answer rather than leaving it to be inferred.
--
-- THE MEMORY RESIDUE GOES NEGATIVE IF IT IS WRITTEN THE OBVIOUS WAY. Measured
-- on an idle instance with nothing else running, total - available - in_use
-- came to -3041 MB. Two independent causes: the three counters are not sampled
-- atomically, so small negatives are ordinary, and physical_memory_in_use_kb
-- is the process working set, which EXCLUDES locked pages — so on an instance
-- using Lock Pages in Memory the residue is overstated by exactly the locked
-- allocation, which 010 already collects and the first design did not use.
--
-- The clamp does not rescue Linux and "underestimate" was too kind a word for
-- it. Measured on the container: total 12756, available 11708, SQL in use
-- 4178, residue -3130. The OS page cache counts as available, so the residue
-- is not merely low, it is negative on an idle machine, and a clamp would
-- turn it into a flat zero on every Linux host. A zero meaning "we cannot
-- compute this here" is worse than no column, so the value is NULL off
-- Windows and residue_computed says which it is.
--
-- The clamp is a CASE and not MAX(0, x): MAX in T-SQL is an aggregate taking
-- one argument — measured, Msg 174 — and the scalar GREATEST arrived only with
-- 2022, above this corpus's floor.
--
-- Memory says something else is RESIDENT. It does not say something else is
-- RUNNING, and the ring buffer below is the half that says the second one.
--
-- RING_BUFFER_SCHEDULER_MONITOR records carry a SystemHealth element with
-- ProcessUtilization, the CPU percentage used by the SQL Server process, and
-- SystemIdle. What is left over is everybody else.
--
-- THAT FORMULA IS WINDOWS-ONLY AND UNGUARDED IT PRINTS A FALSE FINDING.
-- Measured: on Linux every scheduler-monitor record carries
-- ProcessUtilization = 0 and SystemIdle = 0, on all 256 of them, so 100 - 0 - 0
-- reports that other processes are using the entire CPU, all the time, on every
-- Linux instance. The record shape exists, so the collector would not error —
-- it would lie. The residue is therefore projected only where the host
-- platform is Windows, and the platform is projected beside it.
--
-- THE GUARD IS ON THE PLATFORM, NEVER ON THE VALUES, and the first design of
-- this file got that wrong in a way worth keeping as a warning. It computed
-- the residue "only where both operands are non-zero". On a Windows host at
-- 100% CPU — SQL Server at 35%, something else at 65% — SystemIdle is exactly
-- zero, so the value-based guard would suppress the residue precisely on the
-- saturated machine where the neighbour IS the finding. A guard that fails on
-- the interesting case is worse than no guard, because it looks like a
-- measurement that came back empty.
--
-- The clamp at zero is a different thing and is kept: scheduling jitter can
-- push the two percentages past 100, and a negative residue is not a finding.
--
-- TWO CONSTRAINTS ARE INHERITED FROM 041.connectivity.sql, both learned the
-- hard way there.
--
--   The buffer wraps at 256 records. Confirmed on the test instance, which
--   reached exactly 256; measured span on a real one, 4 hours 15 minutes. The
--   window is reported beside the numbers, because a short window IS the
--   finding when someone reads a quiet afternoon as a quiet server.
--
--   The ms_ticks arithmetic is done in SECONDS. The columns are bigint; what
--   overflows is DATEADD's increment argument, which is an int and gives up
--   after 24 days of uptime — exactly the population worth auditing.
--
-- THE HONEST LIMIT BELONGS IN THE FILE. Nothing inside SQL Server can
-- enumerate the processes of its host. There is no read-only view for it, and
-- the paths that exist — a shell out, a CLR assembly, WMI through a linked
-- server — all need permissions this collector refuses to ask for and would
-- break the read-only promise. So the finding this makes possible is "SQL
-- Server is not alone on this machine, and here is how much CPU it is not
-- getting". It is not a process list. An audit that says "18.5 GB and 40% of
-- the CPU go to other processes" is reporting a measurement; one that names
-- the application is repeating what somebody said.
--
-- The record shape is undocumented — Microsoft publishes the schema of no ring
-- buffer — so it is asserted from observation, and every element is read by an
-- explicit XPath so that a renamed element yields a NULL in one column instead
-- of shifting every field silently.
--
-- THE TWO PERCENTAGES COME OUT AS TEXT AND ARE CONVERTED AFTERWARDS.
-- 048.security-errors.sql asked value() for an int on a field whose name
-- promised a number, and the first client instance it met answered with a
-- symbolic value, which raised and took the whole batch. Asking value() for a
-- numeric type turns a surprise into an ERROR; TRY_CONVERT turns it into a
-- NULL, which then flows through the residue arithmetic below and leaves one
-- sample empty instead of leaving the archive with nothing at all.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @ticks bigint = (SELECT ms_ticks FROM sys.dm_os_sys_info);
DECLARE @platform varchar(32) = 'Windows', @err int = 0;

DECLARE @p TABLE ([platform] varchar(32));

/* The same guard 021.host-info.sql uses, and for the same reason: the view
   arrived with 2017, and below it there was no platform but Windows. A file
   that cannot name its platform must not compute the residue, so the fallback
   is the deduction rather than an empty string. */
IF OBJECT_ID(N'sys.dm_os_host_info') IS NOT NULL
BEGIN
    BEGIN TRY
        INSERT INTO @p ([platform])
        EXEC sys.sp_executesql
            N'SELECT h.host_platform FROM sys.dm_os_host_info AS h
              OPTION (RECOMPILE, MAXDOP 1)';
    END TRY
    BEGIN CATCH
        SET @err = ERROR_NUMBER();
    END CATCH
END

SELECT @platform = ISNULL((SELECT TOP 1 x.[platform] FROM @p AS x), 'Windows')
OPTION (RECOMPILE, MAXDOP 1);

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                     AS [collected_at],
       @platform                                                    AS [platform],
       @err                                                         AS [error_number],
       /* Named rather than implied: a reader must not have to work out why the
          residue is NULL on half the archives. */
       CASE WHEN @platform = 'Windows' THEN 1 ELSE 0 END            AS [residue_computed],
       s.[records],
       256                                                          AS [capacity],
       s.[oldest],
       s.[newest],
       DATEDIFF(second, s.[oldest], s.[newest])                     AS [span_seconds],
       s.[avg_process_utilization],
       s.[max_process_utilization],
       s.[avg_other_processes_pct],
       s.[max_other_processes_pct],

       /* The memory half. Read here rather than in 010.properties.sql for the
          reason the header gives: the residue is only meaningful beside the
          platform, and this is the file that resolves one. The raw counters
          stay in 010; what is new is the subtraction. */
       CAST(m.total_kb     / 1024.0 AS DECIMAL(12,1))               AS [memory.total_physical_ram_mb],
       CAST(m.available_kb / 1024.0 AS DECIMAL(12,1))               AS [memory.available_physical_ram_mb],
       CAST(m.in_use_kb    / 1024.0 AS DECIMAL(12,1))               AS [memory.sql_ram_in_use_mb],
       CAST(m.locked_kb    / 1024.0 AS DECIMAL(12,1))               AS [memory.sql_locked_pages_mb],
       CASE WHEN @platform = 'Windows'
            THEN CASE WHEN m.total_kb - m.available_kb - (m.in_use_kb + m.locked_kb) < 0
                      THEN 0
                      ELSE CAST((m.total_kb - m.available_kb
                                 - (m.in_use_kb + m.locked_kb)) / 1024.0 AS DECIMAL(12,1)) END
       END                                                          AS [memory.other_processes_mb]
FROM (
    SELECT COUNT(*)                                                 AS [records],
           MIN(r.when_local)                                        AS [oldest],
           MAX(r.when_local)                                        AS [newest],
           AVG(r.process_utilization)                               AS [avg_process_utilization],
           MAX(r.process_utilization)                               AS [max_process_utilization],
           AVG(CASE WHEN @platform = 'Windows'
                    THEN CASE WHEN 100 - r.process_utilization - r.system_idle < 0
                              THEN 0
                              ELSE 100 - r.process_utilization - r.system_idle END
               END)                                                 AS [avg_other_processes_pct],
           MAX(CASE WHEN @platform = 'Windows'
                    THEN CASE WHEN 100 - r.process_utilization - r.system_idle < 0
                              THEN 0
                              ELSE 100 - r.process_utilization - r.system_idle END
               END)                                                 AS [max_other_processes_pct]
    FROM (
        SELECT DATEADD(second, -((@ticks - rb.timestamp) / 1000), GETDATE())            AS when_local,
               TRY_CONVERT(int, x.value('(//SystemHealth/ProcessUtilization)[1]', 'varchar(64)'))
                                                                                        AS process_utilization,
               TRY_CONVERT(int, x.value('(//SystemHealth/SystemIdle)[1]', 'varchar(64)'))
                                                                                        AS system_idle
        FROM sys.dm_os_ring_buffers AS rb
        CROSS APPLY (SELECT CAST(rb.record AS xml)) AS q(x)
        WHERE rb.ring_buffer_type = 'RING_BUFFER_SCHEDULER_MONITOR'
    ) AS r
) AS s
CROSS JOIN (
    SELECT sm.total_physical_memory_kb                              AS total_kb,
           sm.available_physical_memory_kb                          AS available_kb,
           pm.physical_memory_in_use_kb                             AS in_use_kb,
           pm.locked_page_allocations_kb                            AS locked_kb
    FROM sys.dm_os_sys_memory AS sm
    CROSS JOIN sys.dm_os_process_memory AS pm
) AS m
OPTION (RECOMPILE, MAXDOP 1);

/* One row per record. 256 of them at most, so the history is cheap, and it is
   the history that separates a machine busy all afternoon from one busy for
   four minutes while somebody looked. */
SELECT r.when_local                                                 AS [sampled_at],
       r.process_utilization                                        AS [sql_process_pct],
       r.system_idle                                                AS [system_idle_pct],
       CASE WHEN @platform = 'Windows'
            THEN CASE WHEN 100 - r.process_utilization - r.system_idle < 0
                      THEN 0
                      ELSE 100 - r.process_utilization - r.system_idle END
       END                                                          AS [other_processes_pct]
FROM (
    SELECT DATEADD(second, -((@ticks - rb.timestamp) / 1000), GETDATE())                AS when_local,
           TRY_CONVERT(int, x.value('(//SystemHealth/ProcessUtilization)[1]', 'varchar(64)'))
                                                                                        AS process_utilization,
           TRY_CONVERT(int, x.value('(//SystemHealth/SystemIdle)[1]', 'varchar(64)'))
                                                                                        AS system_idle
    FROM sys.dm_os_ring_buffers AS rb
    CROSS APPLY (SELECT CAST(rb.record AS xml)) AS q(x)
    WHERE rb.ring_buffer_type = 'RING_BUFFER_SCHEDULER_MONITOR'
) AS r
ORDER BY r.when_local
OPTION (RECOMPILE, MAXDOP 1);
