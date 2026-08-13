-- @scope:       instance
-- @resultsets:  root:object, databases:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     60
--
-- Whether this instance publishes, subscribes or distributes anything.
--
-- WHAT THIS FILE DELIBERATELY DOES NOT DO, and it is the whole design. The
-- interesting replication metadata — publications, articles, distribution
-- history, tracer tokens, latency — lives in the DISTRIBUTION DATABASE, whose
-- name is chosen when replication is configured and is not fixed. An
-- instance-scoped collector cannot USE a database whose name it only learns at
-- run time, and the pipeline has no way to target one either: the database list
-- is settled before any query runs. Reaching it would be a change to the
-- collector, not a new file.
--
-- So this reads what is answerable from master and msdb with no dynamic name,
-- and says so. That is less than the reference diagnostic script offers and it
-- is honest about the gap; the alternative was a file that works on the author's
-- machine and fails on a distributor named anything else.
--
-- THE AGENTS ARE ALREADY COLLECTED, BY 50.agent/010.jobs.sql, and this file
-- deliberately does not repeat them. Replication runs as SQL Agent jobs in the
-- REPL-LogReader, REPL-Distribution, REPL-Merge and REPL-Snapshot categories,
-- and that collector reports every job with its category and its last outcome —
-- which is what an audit actually finds wrong, far more often than latency.
--
-- An earlier version of this file read msdb.dbo.sysjobs and sysjobhistory to
-- list them here. It was refused on the audit login it was written for, and
-- correctly: SQLAgentReaderRole reaches sysjobs_view and neither of those two.
-- 010.jobs.sql documents that boundary in its own header, measured on a client
-- instance. Duplicating the query would have cost a permission and returned
-- what the archive already holds.
--
-- is_published AND ITS SIBLINGS ARE FLAGS, NOT PROOF OF ACTIVITY. A database
-- restored from a publisher keeps them set, so a flag on a database nobody
-- replicates any more is a leftover rather than a topology. Read them with the
-- agent list beside them: flags without agents is the signature of exactly that.
--
-- NO JUDGEMENT IS APPLIED. A failing agent may be a decommissioned publication
-- nobody removed.
--
-- SQL Server 2012 is the floor. Not collected for that reason: nothing — the
-- limit here is the distribution database's name, not any version.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       (SELECT COUNT(*) FROM sys.databases AS d WHERE d.is_published = 1)       AS [counts.published],
       (SELECT COUNT(*) FROM sys.databases AS d WHERE d.is_merge_published = 1) AS [counts.merge_published],
       (SELECT COUNT(*) FROM sys.databases AS d WHERE d.is_subscribed = 1)      AS [counts.subscribed],
       (SELECT COUNT(*) FROM sys.databases AS d WHERE d.is_distributor = 1)     AS [counts.distributor],
       /* No SERVERPROPERTY here, and that is a correction rather than an
          omission. This projected IsPublisher and IsSubscriber until a live run
          returned NULL for both: neither name exists. SERVERPROPERTY does not
          raise on a name it does not know — it returns NULL forever — so an
          invented property is indistinguishable from a real one that does not
          apply, which is the failure docs/verification-2012.md warns about in
          those words. The database flags below are the real answer and they
          come from a catalog view that would have errored on a wrong column. */
       /* Named so a reader knows this file stopped short on purpose rather than
          because something failed. Two different reasons, said in one line. */
       'publications, articles and latency live in the distribution database, whose name is not fixed and which this collector does not reach; the replication agents are in 50.agent/010.jobs.json under their REPL- categories'
                                                                  AS [not_collected]
OPTION (RECOMPILE, MAXDOP 1);

/* Only the databases carrying a replication flag. Every database on the instance
   would be one row of zeros each. */
SELECT d.name                                                     AS [database],
       CAST(d.is_published AS int)                                AS [is_published],
       CAST(d.is_merge_published AS int)                          AS [is_merge_published],
       CAST(d.is_subscribed AS int)                               AS [is_subscribed],
       CAST(d.is_distributor AS int)                              AS [is_distributor],
       CAST(d.is_sync_with_backup AS int)                         AS [is_sync_with_backup],
       d.state_desc                                               AS [state],
       d.recovery_model_desc                                      AS [recovery_model]
FROM sys.databases AS d
WHERE d.is_published = 1 OR d.is_merge_published = 1
   OR d.is_subscribed = 1 OR d.is_distributor = 1
ORDER BY d.name
OPTION (RECOMPILE, MAXDOP 1);
