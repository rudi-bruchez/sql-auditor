-- @scope:       instance
-- @resultsets:  jobs:array, outcomes:array, outcomes_status:object
-- @permissions: CONNECT, AGENT JOBS
-- @timeout:     60
--
-- SQL Server Agent job inventory, and how each job's runs have gone.
--
-- Why this collector exists: on a real audit, six enabled jobs had been
-- failing every 15 minutes for four months with nobody notified. Nothing in
-- the rest of the corpus could have surfaced that.
--
-- It reads ONLY msdb.dbo.sysjobs_view, msdb.dbo.syscategories and
-- sp_help_jobhistory. That restraint is the design, not an oversight. A
-- read-only login in SQLAgentReaderRole — the least privilege that can answer
-- this question at all — was measured against a client instance and can read:
--
--     sysjobs_view      yes        sysjobsteps      no
--     syscategories     yes        sysjobhistory    no
--     backupset         yes        sysjobschedules  no
--     backupmediafamily yes        sysschedules     no
--                                  sysjobservers    no
--                                  sysjobactivity   no
--                                  sysalerts        no
--                                  sysoperators     no
--                                  sysmail_*        no
--
-- So steps, schedules, alerts, operators and Database Mail are NOT collected
-- here. A collector reading sysjobsteps would fail at every properly
-- locked-down client while passing against a sysadmin login in a lab. They
-- need their own collector and their own declared permission.
--
-- sp_help_job is NOT used, though it looks like the obvious route to a last
-- run outcome: it performs an INSERT ... EXEC internally, so capturing it into
-- a table raises error 8164, "an INSERT EXEC statement cannot be nested". It
-- works interactively and fails the moment a collector tries to keep the rows.
-- sp_help_jobhistory has no such problem and carries more: the run history,
-- not just the last verdict.
--
-- run_status is emitted raw AND decoded. Decoding a documented enumeration is
-- a lookup, not a judgement: 0 Failed, 1 Succeeded, 2 Retry, 3 Canceled,
-- 4 In progress. Whether a failure matters is the analysis layer's call.
--
-- The failure message of the last run is collected, truncated to 512
-- characters. It is the single most useful diagnostic here, and it is job
-- error text rather than table data — but it is written by whatever the job
-- runs, so treat it as it is disclosed in MANIFEST.txt.
--
-- The history window is bounded and REPORTED. An unbounded sp_help_jobhistory
-- is unpredictable in size, and a run count with no window is unreadable:
-- "no failures" means nothing until you know over how long.
--
-- SQL Server 2012 is the floor. sysjobs_view, syscategories and
-- sp_help_jobhistory all predate it, so no @min_version applies.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @window_days int = 30;
DECLARE @start_run_date int =
    CONVERT(int, CONVERT(char(8), DATEADD(day, -@window_days, GETDATE()), 112));

/* The inventory. This half always works once the permission probe passed: it
   is a plain SELECT on two views the Agent reader role grants. */
SELECT j.name                                        AS [name],
       CAST(j.enabled AS int)                        AS [enabled],
       c.name                                        AS [category],
       SUSER_SNAME(j.owner_sid)                      AS [owner],
       j.description                                 AS [description],
       j.date_created                                AS [date_created],
       j.date_modified                               AS [date_modified],
       j.version_number                              AS [version_number],
       CAST(j.delete_level AS int)                   AS [delete_level],
       j.notify_level_eventlog                       AS [notify.eventlog],
       j.notify_level_email                          AS [notify.email],
       j.notify_level_netsend                        AS [notify.netsend],
       j.notify_level_page                           AS [notify.page]
FROM       msdb.dbo.sysjobs_view AS j
LEFT JOIN  msdb.dbo.syscategories AS c ON c.category_id = j.category_id
ORDER BY j.name
OPTION (RECOMPILE, MAXDOP 1);

/* The history. INSERT ... EXEC binds to the procedure's column list by
   position, so a future version that adds a column breaks this — loudly, with
   error 213 — rather than shifting values into the wrong columns.

   That break must not read as "no jobs failed". The TRY/CATCH keeps the run
   alive and outcomes_status reports exactly what happened, so an empty
   outcomes array is never ambiguous: either it was collected and empty, or it
   was not collected and says why. */
DECLARE @collected bit = 1, @err int = 0, @msg nvarchar(2048) = N'';

DECLARE @h TABLE (
    instance_id       int,
    job_id            uniqueidentifier,
    job_name          nvarchar(256),
    step_id           int,
    step_name         nvarchar(256),
    sql_message_id    int,
    sql_severity      int,
    message           nvarchar(4000),
    run_status        int,
    run_date          int,
    run_time          int,
    run_duration      int,
    operator_emailed  nvarchar(256),
    operator_netsent  nvarchar(256),
    operator_paged    nvarchar(256),
    retries_attempted int,
    server            nvarchar(256));

BEGIN TRY
    INSERT INTO @h EXEC msdb.dbo.sp_help_jobhistory
        @mode = N'FULL', @start_run_date = @start_run_date;
END TRY
BEGIN CATCH
    SELECT @collected = 0, @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH;

/* step_id = 0 is the job's own outcome row; anything above it is a step.
   run_date and run_time are the Agent's integer encodings — YYYYMMDD, and
   HHMMSS with leading zeros dropped — and run_duration is HHMMSS as a
   duration, which is why it is decomposed rather than read as a number. */
WITH job_runs AS (
    SELECT h.job_name, h.run_status, h.run_date, h.run_time, h.run_duration,
           h.message, h.retries_attempted,
           ROW_NUMBER() OVER (PARTITION BY h.job_name
                              ORDER BY h.run_date DESC, h.run_time DESC) AS rn
    FROM @h AS h
    WHERE h.step_id = 0)
SELECT r.job_name                                     AS [name],
       r.run_status                                   AS [last_run.status_code],
       CASE r.run_status
            WHEN 0 THEN 'failed'
            WHEN 1 THEN 'succeeded'
            WHEN 2 THEN 'retry'
            WHEN 3 THEN 'canceled'
            WHEN 4 THEN 'in progress'
       END                                            AS [last_run.status],
       CONVERT(datetime,
           CONVERT(char(8), r.run_date) + ' ' +
           STUFF(STUFF(RIGHT('000000' + CONVERT(varchar(6), r.run_time), 6), 5, 0, ':'), 3, 0, ':'),
           120)                                       AS [last_run.at],
       (r.run_duration / 10000) * 3600
         + ((r.run_duration / 100) % 100) * 60
         + (r.run_duration % 100)                     AS [last_run.duration_sec],
       r.retries_attempted                            AS [last_run.retries],
       CASE WHEN r.run_status <> 1
            THEN LEFT(r.message, 512) END             AS [last_run.message],
       w.runs                                         AS [window.runs],
       w.failures                                     AS [window.failures]
FROM job_runs AS r
CROSS APPLY (SELECT COUNT(*) AS runs,
                    SUM(CASE WHEN a.run_status = 0 THEN 1 ELSE 0 END) AS failures
             FROM @h AS a
             WHERE a.step_id = 0 AND a.job_name = r.job_name) AS w
WHERE r.rn = 1
ORDER BY r.job_name
OPTION (RECOMPILE, MAXDOP 1);

/* Both the window ASKED FOR and the span actually returned. They differ
   whenever msdb's own history retention is shorter than the request — on the
   instance this collector was built against, a 30-day ask returned two days,
   because the history had been purged. Reporting only the ask would let a
   reader turn "34 failures out of 34 runs" into "34 failures in 30 days",
   understating a job that is in fact failing every single time. */
SELECT @collected                                     AS [collected],
       @err                                           AS [error_number],
       NULLIF(@msg, N'')                              AS [error_message],
       @window_days                                   AS [window.days_requested],
       @start_run_date                                AS [window.start_run_date],
       (SELECT MIN(a.run_date) FROM @h AS a)          AS [observed.oldest_run_date],
       (SELECT MAX(a.run_date) FROM @h AS a)          AS [observed.newest_run_date],
       (SELECT COUNT(*) FROM @h AS a WHERE a.step_id = 0) AS [observed.job_runs],
       (SELECT COUNT(DISTINCT a.job_name) FROM @h AS a)   AS [observed.jobs_with_history]
OPTION (RECOMPILE, MAXDOP 1);
