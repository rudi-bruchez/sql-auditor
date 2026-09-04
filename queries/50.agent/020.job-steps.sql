-- @scope:       instance
-- @resultsets:  root:object, steps:array
-- @permissions: CONNECT, AGENT JOB STEPS
-- @timeout:     60
-- @discloses:   job_step_text
--
-- What the Agent jobs actually run, step by step.
--
-- Why this collector exists, and why it is separate from 010.jobs.sql. That
-- file documents, from a measurement made against a locked-down client login,
-- that SQLAgentReaderRole reads sysjobs_view and sysjobhistory but NOT
-- sysjobsteps. Folding the steps into it would have made the whole collector
-- fail wherever the corpus is run with the least privilege that can answer the
-- question at all — passing only against a sysadmin login in a lab. So the
-- steps get their own file and their own declared permission, and an instance
-- that refuses them loses this collector and nothing else.
--
-- What it buys: an archive can already say that a maintenance job exists and
-- ran successfully. It cannot say what it did. On a real audit, heap
-- fragmentation of 90-99% alongside 22-25% on the non-clustered indexes
-- established that the heaps were escaping the maintenance job — but the
-- argument named @Indexes, which would have proved it, lives in a step
-- command, and the finding had to be written as a deduction.
--
-- THE COMMAND BODY IS ONLY PROJECTED FOR T-SQL STEPS, AND ONLY ITS BEGINNING.
-- Job step commands are the one place in msdb where a connection string or a
-- password is routinely written in clear by whoever wrote the job. A CmdExec,
-- PowerShell or SSIS step is precisely where an external credential appears,
-- so for those subsystems this file projects the subsystem and the length and
-- stops there. A T-SQL step runs inside the instance and its text is the same
-- class of thing as this corpus, so its first 200 characters are projected —
-- enough for a call to dbo.IndexOptimize naming its @Databases and its
-- fragmentation-level arguments to be readable, which is the question this
-- file was written for.
--
-- The residual risk is stated rather than hidden: a T-SQL step whose first 200
-- characters contain a literal password would carry it into the archive. That
-- is a narrower exposure than projecting every command in full, and it is the
-- line this file draws. A collector that projects command bodies in full
-- belongs behind an opt-in flag, like session text, and does not exist yet.
--
-- NO JUDGEMENT IS APPLIED. A job that rebuilds every index nightly is not
-- called a defect here; the archive records the command and the schedule it
-- runs under, and whether that suits the instance is decided elsewhere.
--
-- SQL Server 2012 is the floor; sysjobsteps predates it. Not collected for
-- that reason: nothing — every column read here has been present since 2005.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT
    (SELECT COUNT(*) FROM msdb.dbo.sysjobsteps)                 AS [counts.steps],
    (SELECT COUNT(DISTINCT job_id) FROM msdb.dbo.sysjobsteps)   AS [counts.jobs_with_steps],
    (SELECT COUNT(*) FROM msdb.dbo.sysjobsteps
      WHERE subsystem = 'TSQL')                                 AS [counts.tsql_steps],
    (SELECT COUNT(*) FROM msdb.dbo.sysjobsteps
      WHERE subsystem <> 'TSQL')                                AS [counts.other_subsystem_steps],
    -- A proxy means the step runs as an identity other than the Agent service
    -- account. Counting them says whether that mechanism is in use at all.
    (SELECT COUNT(*) FROM msdb.dbo.sysjobsteps
      WHERE proxy_id IS NOT NULL)                               AS [counts.steps_with_proxy],
    -- A step that writes its output to a file is writing somewhere on the host,
    -- and that path is a fact about the instance's footprint.
    (SELECT COUNT(*) FROM msdb.dbo.sysjobsteps
      WHERE output_file_name IS NOT NULL)                       AS [counts.steps_with_output_file],
    (SELECT COUNT(DISTINCT subsystem) FROM msdb.dbo.sysjobsteps) AS [counts.distinct_subsystems]
OPTION (RECOMPILE, MAXDOP 1);

SELECT
    j.name                                                      AS [job],
    CAST(j.enabled AS bit)                                      AS [job_enabled],
    s.step_id                                                   AS [step_id],
    s.step_name                                                 AS [step_name],
    s.subsystem                                                 AS [subsystem],
    s.database_name                                             AS [database],
    LEN(s.command)                                              AS [command_length],
    -- The rule this file is built on. Anything that is not a T-SQL step keeps
    -- its command on the instance.
    CASE WHEN s.subsystem = 'TSQL'
         THEN LEFT(REPLACE(REPLACE(s.command, CHAR(13), ' '), CHAR(10), ' '), 200)
    END                                                         AS [command_start],
    CAST(CASE WHEN s.subsystem = 'TSQL' THEN 1 ELSE 0 END AS bit)
                                                                AS [command_projected],
    s.on_success_action                                         AS [on_success_action],
    s.on_fail_action                                            AS [on_fail_action],
    s.retry_attempts                                            AS [retry_attempts],
    s.retry_interval                                            AS [retry_interval],
    -- The path, not the contents. A step logging to a share says something
    -- about where diagnostics land when the job fails at 3 a.m.
    s.output_file_name                                          AS [output_file],
    p.name                                                      AS [proxy],
    -- Agent stores these as integers, 20260810 and 143005, not as datetime.
    -- Projected raw and named so, because assembling them into a timestamp
    -- is the analysis layer's job and a half-conversion here would look
    -- like a date that failed to parse.
    s.last_run_date                                             AS [last_run_date_raw],
    s.last_run_duration                                         AS [last_run_duration_raw],
    s.last_run_outcome                                          AS [last_run_outcome_raw]
FROM msdb.dbo.sysjobsteps AS s
JOIN msdb.dbo.sysjobs_view AS j ON j.job_id = s.job_id
LEFT JOIN msdb.dbo.sysproxies AS p ON p.proxy_id = s.proxy_id
ORDER BY j.name, s.step_id
OPTION (RECOMPILE, MAXDOP 1);
