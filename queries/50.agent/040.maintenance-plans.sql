-- @scope:       instance
-- @resultsets:  root:object, tasks:array
-- @permissions: CONNECT, MAINTENANCE PLANS
-- @timeout:     60
--
-- What the maintenance plans actually do, task by task.
--
-- Why this collector exists, and why it is separate from 010.jobs.sql and
-- 020.job-steps.sql. Those files document Agent jobs: their steps, schedules
-- and commands. A maintenance plan is not a job: it is a design surface whose
-- task definitions are stored as SSIS packages in msdb.dbo.sysssispackages
-- (packagetype 6), one row per subplan per task. A job step that runs a
-- maintenance plan says only "Subplan_1"; the package says the plan contains
-- a shrink task, a rebuild task, a check-database task. No fixed database
-- role reads this table — the db_ssis* roles exist but are deliberately not
-- the route this corpus proposes, for the reasons the grant script's caveat
-- states — so the collector declares its own permission and an instance that
-- refuses it loses this file and nothing else.
--
-- THE PACKAGE BODY IS NEVER PROJECTED. A package can carry connection
-- strings — the plan's own local server connection, and on a plan written
-- against a remote target, that target's — so only the two task attributes
-- this audit reads leave the instance: ObjectName, the label the designer
-- shows, and CreationName, the immutable task type
-- (Microsoft.SqlServer.Management.DatabaseMaintenance.DbMaintenanceShrinkTask
-- and so on). The label is renamed and localised freely; the type is the
-- fact. Downstream detection keys on task_type and keeps the label for the
-- human reader.
--
-- WHY TRY_CAST AND NOT CAST: an encrypted package (isencrypted = 1) holds
-- binary that is not XML, and the optimiser does not guarantee the filter
-- runs before the conversion — a plain CAST fails the whole collection with
-- Msg 9420 on the first encrypted row. TRY_CAST exists since SQL Server
-- 2012, the floor of this corpus, and turns an unreadable package into an
-- XML NULL instead of an error.
--
-- WHY OUTER APPLY AND NOT CROSS APPLY: the same encrypted package must
-- survive as a row with task NULL. That row is what carries the verdict
-- "this plan exists and its contents may not be looked at" into the archive;
-- an inner apply would make the plan vanish and read as "no tasks", which is
-- the opposite finding.
--
-- The XML methods below require QUOTED_IDENTIFIER ON. The TDS default is ON
-- and the collector relies on it, exactly like 041.connectivity.sql; a client
-- tool that turns it off will see error 1934 rather than wrong data.
--
-- WHY THE CONTAINER FILTER: //*:Executable also selects the package root,
-- the subplans and the sequence containers, all of which carry a CreationName
-- beginning SSIS.Package or STOCK:. Filtering on CreationName, not on
-- ObjectName: the label is arbitrary, the type is not.
--
-- NO JUDGEMENT IS APPLIED. A plan that shrinks every database every night is
-- not called a defect here; the archive records the tasks and their types,
-- and whether that suits the instance is decided elsewhere.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT
    (SELECT COUNT(*) FROM msdb.dbo.sysssispackages WHERE packagetype = 6)
                                                                AS [counts.plans],
    (SELECT COUNT(*) FROM msdb.dbo.sysssispackages WHERE packagetype = 6
      AND isencrypted = 1)                                     AS [counts.encrypted_plans],
    -- Counted through the same XML projection as the tasks result set, so the
    -- two numbers cannot disagree about what a task is.
    (SELECT COUNT(*) FROM msdb.dbo.sysssispackages AS p
      OUTER APPLY (SELECT TRY_CAST(CAST(p.packagedata AS varbinary(max)) AS xml)) AS pkg(x2)
      OUTER APPLY pkg.x2.nodes('//*:Executable') AS t(x)
      WHERE p.packagetype = 6
        AND x.value('(self::node()/@*:CreationName)[1]', 'nvarchar(256)') NOT LIKE 'SSIS.Package%'
        AND x.value('(self::node()/@*:CreationName)[1]', 'nvarchar(256)') NOT LIKE 'STOCK:%')
                                                                AS [counts.tasks]
OPTION (RECOMPILE, MAXDOP 1);

SELECT
    p.name                                                      AS [plan],
    CAST(p.isencrypted AS bit)                                  AS [is_encrypted],
    -- ObjectName: the designer label, renamed and localised freely. Kept
    -- for the reader; detection keys on task_type. The attribute is read
    -- through self::node() because value() rejects a bare attribute axis
    -- as its top-level step (Msg 2390).
    x.value('(self::node()/@*:ObjectName)[1]',  'nvarchar(256)') AS [task],
    -- CreationName: the immutable task type
    -- (Microsoft.SqlServer.Management.DatabaseMaintenance.*). This is the
    -- column downstream rules test. Projected from the apply that computes
    -- it once, so the projection and the container filter cannot disagree.
    cn.task_type                                                AS [task_type]
FROM msdb.dbo.sysssispackages AS p
-- The cast goes through a one-row subquery because the engine does not
-- accept .nodes() invoked directly on a (TRY_)CAST expression in an APPLY.
OUTER APPLY (SELECT TRY_CAST(CAST(p.packagedata AS varbinary(max)) AS xml)) AS pkg(x2)
-- OUTER APPLY, not CROSS: an encrypted or unreadable package must survive
-- as a row with task NULL; nodes() over a NULL xml simply yields no task.
OUTER APPLY pkg.x2.nodes('//*:Executable') AS t(x)
-- The task type is computed once, in its own apply: when nodes() yields no
-- row, x is NULL, and a NULL NOT LIKE in the WHERE is UNKNOWN — it would
-- filter out the very row the OUTER APPLY exists to keep. Measured on a
-- 2022 instance: without the IS NULL arm below, the encrypted plan vanishes.
OUTER APPLY (SELECT x.value('(self::node()/@*:CreationName)[1]', 'nvarchar(256)')) AS cn(task_type)
WHERE p.packagetype = 6
  -- The package root, subplans and sequence containers are not tasks.
  AND (cn.task_type IS NULL
       OR (cn.task_type NOT LIKE 'SSIS.Package%' AND cn.task_type NOT LIKE 'STOCK:%'))
ORDER BY p.name
OPTION (RECOMPILE, MAXDOP 1);
