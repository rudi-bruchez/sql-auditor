-- @scope:       instance
-- @resultsets:  root:object, commands:array
-- @permissions: CONNECT, AGENT JOBS, AGENT JOB STEPS
-- @timeout:     60
-- @discloses:   job_step_command
-- @requires_flag: job_step_commands
--
-- The complete text of every Transact-SQL job step, when the operator has
-- asked for it.
--
-- WHY THIS EXISTS BESIDE 020.job-steps.sql RATHER THAN INSTEAD OF IT.
-- 020 projects the first 200 characters of each T-SQL step and runs in every
-- collection. That bound is deliberate and it is about disclosure: a job step
-- is application code, and a job step is the one place in an instance where a
-- password is typed in rather than kept in a credential. Two hundred
-- characters keeps that risk small, because a connection string rarely fits in
-- them.
--
-- The trouble is that the truncation cuts exactly where the answer lives.
-- Measured on a client's maintenance job in September 2026: the projection
-- stopped at 200 characters of a 457-character command, and every parameter
-- that says whether the job is aware of its availability group sat beyond the
-- cut. The question the audit was there to answer was decided in the half that
-- was thrown away, and it had to be inferred from the job's run duration
-- instead.
--
-- So the bound stays where it is and the full text becomes a decision the
-- operator makes, once, knowing what it costs. That is the same shape as
-- --include-session-text and --query-store-detail, and for the same reason.
--
-- WHY THE WHOLE COMMAND AND NOT A LONGER TRUNCATION. A longer cap would be a
-- number somebody would have to keep raising, and the next job would exceed it
-- the same way. Worse, it would keep the disclosure while losing the guarantee:
-- 200 characters is small enough to say plainly what can be in it, and 2 000 is
-- not, so a cap in between discloses nearly everything while still arriving
-- truncated. Either the bound holds or it is lifted deliberately.
--
-- NON-TRANSACT-SQL STEPS ARE STILL NOT READ, and that rule does not move with
-- the flag. A CmdExec step is a command line and a PowerShell step is a script;
-- both are further from the database and closer to the host, and the reason 020
-- gives for leaving them on the instance is not weakened by an operator asking
-- for T-SQL commands. counts.other_subsystem_steps below says how many were
-- left, so the omission is visible rather than silent.
--
-- WHAT A READER SHOULD LOOK FOR, since this file exists for a specific class of
-- question. The parameters at the END of a long maintenance command are the
-- ones that decide behaviour, and they are the ones nobody sees: whether an
-- IndexOptimize call names @AvailabilityGroups, whether it writes to a log
-- table, whether a time limit bounds it, and which shipped objects it includes.
-- (Those parameter names are deliberately not all spelled here: a header line
-- that BEGINS with an @ word is read as a directive by the corpus lint, even in
-- prose, and this file was refused once for exactly that.)
--
-- Read this beside 50.agent/010.jobs.sql, which carries the run durations: a
-- maintenance command whose parameters promise hours of work and whose last run
-- took one second is a job that did nothing, and neither half says that alone.
--
-- LINE ENDINGS ARE FLATTENED TO SPACES, like 020 does, and the reason is
-- worth stating because it loses something. A multi-line command becomes one
-- line, so the shape of the T-SQL is gone and only its content survives. That
-- keeps the projection a single value per step rather than a document, which
-- is what lets it sit in an array beside the other columns. Whoever needs the
-- formatting has the instance.
--
-- SQL Server 2012 is the floor. msdb.dbo.sysjobsteps predates every supported
-- version. Not collected for that reason:
--   the command of a non-T-SQL step   (see above)
--   sysjobsteps.output_file_name      (020 already carries the path)
--   the job schedule                  (010.jobs.sql)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @err int = 0, @msg nvarchar(2048) = N'';

DECLARE @commands TABLE (
    [job]            sysname,
    [step_id]        int,
    [step_name]      sysname,
    [database_name]  sysname NULL,
    [command_length] int,
    [command]        nvarchar(max));

BEGIN TRY
    INSERT INTO @commands
    SELECT j.name, s.step_id, s.step_name, s.database_name,
           LEN(s.command),
           REPLACE(REPLACE(s.command, CHAR(13), N' '), CHAR(10), N' ')
    FROM msdb.dbo.sysjobsteps AS s
    JOIN msdb.dbo.sysjobs AS j ON j.job_id = s.job_id
    WHERE s.subsystem = 'TSQL'
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH;

SELECT
    CONVERT(sysname, SERVERPROPERTY('ServerName'))              AS [instance],
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    (SELECT COUNT(*) FROM @commands)                            AS [counts.commands],
    (SELECT COUNT(DISTINCT [job]) FROM @commands)               AS [counts.jobs],
    /* What this file did NOT read, so the omission is a number rather than an
       absence. A reader who finds zero commands and a non-zero count here is
       looking at an instance whose jobs are all CmdExec or PowerShell, not at
       a collector that failed. */
    (SELECT COUNT(*) FROM msdb.dbo.sysjobsteps
      WHERE subsystem <> 'TSQL')                                AS [counts.other_subsystem_steps],
    /* The reason the flag exists, in one number: how much of the text 020
       could not show. It is the sum of what lies beyond its 200-character cut,
       and on an estate of long maintenance commands it is most of it. */
    (SELECT ISNULL(SUM(CASE WHEN [command_length] > 200
                            THEN [command_length] - 200 ELSE 0 END), 0)
       FROM @commands)                                          AS [counts.characters_beyond_the_cut],
    (SELECT COUNT(*) FROM @commands WHERE [command_length] > 200)
                                                                AS [counts.commands_truncated_in_020],
    (SELECT ISNULL(MAX([command_length]), 0) FROM @commands)    AS [counts.longest_command],
    CASE WHEN @err = 0 THEN 1 ELSE 0 END                        AS [collected.job_step_commands],
    @err                                                        AS [errors.job_step_commands],
    NULLIF(@msg, N'')                                           AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

/* One row per Transact-SQL step, in job then step order, which is the order the
   Agent runs them in and therefore the order a reader follows a job. */
SELECT
    c.[job]                                                     AS [job],
    c.[step_id]                                                 AS [step_id],
    c.[step_name]                                               AS [step_name],
    c.[database_name]                                           AS [database],
    c.[command_length]                                          AS [command_length],
    c.[command]                                                 AS [command]
FROM @commands AS c
ORDER BY c.[job], c.[step_id]
OPTION (RECOMPILE, MAXDOP 1);
