-- @scope:       instance
-- @resultsets:  root:object, primary:array, secondary:array, errors:array
-- @permissions: CONNECT, LOG SHIPPING
-- @timeout:     60
--
-- Log shipping: what this instance ships, what it receives, and how far behind
-- each side is.
--
-- Why it is the simplest of the three availability collectors. Log shipping is
-- entirely in msdb — no DMV, no cluster, no endpoint — so the tables exist and
-- are empty on a server that does none, and no version gate is needed.
--
-- IT NEEDS A PERMISSION OF ITS OWN, and that was learned by being refused. MSDB
-- READ in this corpus is SELECT on msdb.dbo.backupset and nothing more, and
-- SQLAgentReaderRole does not reach these tables either — so a properly
-- locked-down audit login reads backups and jobs happily and is denied here.
-- Hence LOG SHIPPING, probed on its own, so a refusal is recorded as a refusal
-- and never mistaken for an instance that ships no logs. Those are opposite
-- findings, and only the probe can tell them apart.
--
-- THE ELAPSED SECONDS ARE COMPUTED HERE, and that is a departure from what 020
-- refuses to do for Always On. The difference is that this is subtraction of two
-- timestamps rather than division by an instantaneous rate: "the last backup
-- finished 4 200 seconds ago" is a fact, where "the redo queue will take 40
-- seconds to drain" is a prediction. One is arithmetic on measurements, the
-- other is a model.
--
-- THE THRESHOLDS ARE COLLECTED AND NEVER APPLIED. backup_threshold and
-- restore_threshold are the alert values the operator configured, and they are
-- projected beside the elapsed seconds precisely so a reader can compare them —
-- but the comparison is the analysis layer's, and nothing here says an alert
-- should have fired. The operator's own threshold is a fact about the
-- configuration, not a judgement this collector adopts.
--
-- THE MONITOR MAY BE ELSEWHERE. log_shipping_monitor_primary and
-- log_shipping_monitor_secondary are populated on whichever instance is
-- configured as the monitor, which is frequently neither the primary nor the
-- secondary. So a server can ship logs correctly and report NULL for every
-- timestamp below — that is a fact about where the monitor lives, not about the
-- shipping. The LEFT JOINs keep those rows rather than dropping them, and the
-- root counts both populations so the difference is visible.
--
-- ERROR MESSAGES ARE COLLECTED IN FULL, unlike the SQL Server error log, which
-- this corpus only samples. The text of a log shipping error names file paths,
-- share names and instance names — never application data — so it carries no
-- disclosure the archive does not already make.
--
-- NO JUDGEMENT IS APPLIED. A secondary hours behind may be a reporting copy
-- restored once a night on purpose.
--
-- SQL Server 2012 is the floor. All six msdb tables are 2005.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       (SELECT COUNT(*) FROM msdb.dbo.log_shipping_primary_databases)   AS [counts.primary_databases],
       (SELECT COUNT(*) FROM msdb.dbo.log_shipping_secondary_databases) AS [counts.secondary_databases],
       /* Whether this instance is the monitor for anything. When it is not, the
          timestamps below are NULL and that is why. */
       (SELECT COUNT(*) FROM msdb.dbo.log_shipping_monitor_primary)     AS [counts.monitored_primaries],
       (SELECT COUNT(*) FROM msdb.dbo.log_shipping_monitor_secondary)   AS [counts.monitored_secondaries],
       (SELECT COUNT(*) FROM msdb.dbo.log_shipping_monitor_error_detail) AS [counts.recorded_errors],
       200                                                        AS [errors_listing_cap]
OPTION (RECOMPILE, MAXDOP 1);

SELECT p.primary_database                                         AS [database],
       p.backup_directory                                         AS [backup_directory],
       p.backup_share                                             AS [backup_share],
       p.backup_retention_period                                  AS [retention_minutes],
       p.backup_compression                                       AS [compression],
       mp.last_backup_file                                        AS [last_backup_file],
       CONVERT(varchar(23), mp.last_backup_date, 126)             AS [last_backup_at],
       /* Subtraction of two measured instants: see the header on why this is
          computed and the Always On lag is not. NULL when the monitor for this
          database lives on another instance. */
       DATEDIFF(second, mp.last_backup_date, GETDATE())           AS [seconds_since_last_backup],
       mp.backup_threshold                                        AS [backup_threshold_minutes],
       CAST(mp.is_backup_alert_enabled AS int)                    AS [backup_alert_enabled],
       mp.monitor_server                                          AS [monitor_server]
FROM msdb.dbo.log_shipping_primary_databases  AS p
LEFT JOIN msdb.dbo.log_shipping_monitor_primary AS mp
       ON mp.primary_id = p.primary_id
ORDER BY p.primary_database
OPTION (RECOMPILE, MAXDOP 1);

/* Copy and restore are two separate lags and two separate failures: a secondary
   can be copying files it never restores, which looks healthy from the primary's
   side and is not. Both are projected. */
SELECT s.secondary_database                                       AS [database],
       s.primary_server                                           AS [primary_server],
       s.primary_database                                         AS [primary_database],
       s.restore_delay                                            AS [restore_delay_minutes],
       s.restore_mode                                             AS [restore_mode],
       CAST(s.disconnect_users AS int)                            AS [disconnect_users],
       ms.last_copied_file                                        AS [last_copied_file],
       CONVERT(varchar(23), ms.last_copied_date, 126)             AS [last_copied_at],
       DATEDIFF(second, ms.last_copied_date, GETDATE())           AS [seconds_since_last_copy],
       ms.last_restored_file                                      AS [last_restored_file],
       CONVERT(varchar(23), ms.last_restored_date, 126)           AS [last_restored_at],
       DATEDIFF(second, ms.last_restored_date, GETDATE())         AS [seconds_since_last_restore],
       ms.restore_threshold                                       AS [restore_threshold_minutes],
       CAST(ms.is_restore_alert_enabled AS int)                   AS [restore_alert_enabled],
       ms.monitor_server                                          AS [monitor_server]
FROM msdb.dbo.log_shipping_secondary_databases AS s
LEFT JOIN msdb.dbo.log_shipping_secondary      AS sec
       ON sec.primary_server = s.primary_server AND sec.primary_database = s.primary_database
LEFT JOIN msdb.dbo.log_shipping_monitor_secondary AS ms
       ON ms.secondary_database = s.secondary_database
      AND ms.primary_server = s.primary_server
      AND ms.primary_database = s.primary_database
ORDER BY s.secondary_database
OPTION (RECOMPILE, MAXDOP 1);

/* The 200 most recent errors. Capped, and the cap is projected in the root: a
   log shipping chain that has been failing for months would otherwise fill the
   archive with the same message. */
SELECT TOP (200)
       e.database_name                                            AS [database],
       e.agent_type                                               AS [agent_type],
       e.session_id                                               AS [session_id],
       e.message_id                                               AS [message_id],
       e.message                                                  AS [message],
       CONVERT(varchar(23), e.log_time, 126)                      AS [logged_at],
       e.source                                                   AS [source]
FROM msdb.dbo.log_shipping_monitor_error_detail AS e
ORDER BY e.log_time DESC
OPTION (RECOMPILE, MAXDOP 1);
