-- @scope:       instance
-- @resultsets:  root:object, alerts:array, operators:array, notifications:array
-- @permissions: CONNECT, AGENT ALERTS
-- @timeout:     60
--
-- Whether anyone is told when this instance breaks.
--
-- 010.jobs.sql exists because six enabled jobs had been failing every fifteen
-- minutes for four months with nobody notified. This file is the other half of
-- that: an instance can corrupt a page, run out of log space or hit a severity
-- 24 hardware error and raise no alert at all, and nothing in the archive said
-- so. The topic that needs it — alerts missing for severities 19 to 25 and for
-- the I/O errors 823, 824 and 825 — stayed out of collection on every audit
-- until now.
--
-- IT HAS ITS OWN DECLARED PERMISSION AND THAT IS THE POINT. 010.jobs.sql
-- measured, against a client instance, that a login in SQLAgentReaderRole reads
-- sysjobs_view and is refused sysalerts, sysoperators and the mail tables. MSDB
-- READ is SELECT on backupset and nothing else. So a collector reading these
-- three tables under either of those declarations would run and fail on every
-- properly locked-down client while passing against a sysadmin login in a lab —
-- and a declared permission that does not cover the read is worse than no
-- collector, because the skip gate never fires and the file lands in Errors
-- instead of in "queries not run".
--
-- THE THREE TABLES ARE ONE PERMISSION BECAUSE THEY ARE ONE FINDING. An alert
-- nobody is notified of is the same as no alert: it fires, it is counted, and
-- the DBA learns about the corruption from the application. So the operators
-- and the notifications that bind them to the alerts are granted and read
-- together with the alerts themselves.
--
-- NO OPERATOR ADDRESS IS COLLECTED. sysoperators carries email, pager and
-- net send addresses, and the grant script says in so many words that the
-- collector does not read them. What is projected is whether an address is
-- configured at all, which is what the finding rests on: an operator with no
-- address is a notification that goes nowhere, and the address itself adds
-- nothing an audit can act on while adding personal contact details to an
-- archive that gets mailed around.
--
-- AN EMPTY RESULT IS THE FINDING AND NOT A FAILURE, which is the whole reason
-- the preflight probe for this capability does not require rows. An instance
-- with no alerts returns nothing here, and that silence is exactly what the
-- report is looking for. The counts in the root object are what let a reader
-- tell it from a collector that did not run.
--
-- SEVERITY COVERAGE IS COUNTED HERE RATHER THAN LEFT TO THE READER, for the
-- reason 014.cpu-topology.sql derives its NUMA answer: a fact that is
-- meaningless without an operation over its neighbours does not get to stay
-- implicit. Which of the seven severities is uncovered is in the array; how
-- many are covered is in the root.
--
-- NO JUDGEMENT IS APPLIED. An instance may be monitored by something that
-- never touches SQL Server Agent, and an estate with a central monitoring
-- platform is not defective for having no alerts. Whether the gap matters is
-- the analysis layer's call, made against what else the client runs.
--
-- SQL Server 2012 is the floor. Nothing read here is newer.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       (SELECT COUNT(*) FROM msdb.dbo.sysalerts)                  AS [counts.alerts],
       (SELECT COUNT(*) FROM msdb.dbo.sysalerts AS a
         WHERE a.enabled = 1)                                     AS [counts.enabled_alerts],
       (SELECT COUNT(*) FROM msdb.dbo.sysoperators)               AS [counts.operators],
       (SELECT COUNT(*) FROM msdb.dbo.sysoperators AS o
         WHERE o.enabled = 1)                                     AS [counts.enabled_operators],
       (SELECT COUNT(*) FROM msdb.dbo.sysnotifications)           AS [counts.notifications],
       /* An enabled alert with nothing bound to it. The count that turns "we
          have alerts" into "we have alerts and nobody hears them". */
       (SELECT COUNT(*)
          FROM msdb.dbo.sysalerts AS a
         WHERE a.enabled = 1
           AND NOT EXISTS (SELECT 1 FROM msdb.dbo.sysnotifications AS n
                            WHERE n.alert_id = a.id))             AS [counts.alerts_without_notification],
       /* How many of the seven severities an enabled alert covers. Seven is
          full coverage; anything less is the finding, and the array below says
          which ones are missing. */
       (SELECT COUNT(DISTINCT a.severity)
          FROM msdb.dbo.sysalerts AS a
         WHERE a.enabled = 1 AND a.severity BETWEEN 19 AND 25)     AS [coverage.severities_19_to_25],
       7                                                          AS [coverage.severities_expected],
       /* The three I/O errors that mean a page could not be read, could not be
          read consistently, or was read with a checksum failure. They are
          message ids rather than severities, so a severity alert does not
          cover them and an instance can be fully covered on 19 to 25 and still
          silent on corruption. */
       (SELECT COUNT(DISTINCT a.message_id)
          FROM msdb.dbo.sysalerts AS a
         WHERE a.enabled = 1 AND a.message_id IN (823, 824, 825))  AS [coverage.io_error_alerts],
       3                                                          AS [coverage.io_errors_expected],
       (SELECT CONVERT(int, COUNT(*)) FROM msdb.dbo.sysoperators AS o
         WHERE o.enabled = 1
           AND (NULLIF(LTRIM(RTRIM(ISNULL(o.email_address, ''))), '') IS NOT NULL
             OR NULLIF(LTRIM(RTRIM(ISNULL(o.pager_address, ''))), '') IS NOT NULL))
                                                                  AS [counts.reachable_operators]
OPTION (RECOMPILE, MAXDOP 1);

/* One row per alert. notification_message is projected because it is text an
   administrator wrote for other administrators, unlike a job step command,
   which is why 020.job-steps.sql is gated and this is not. */
SELECT a.id                                                       AS [alert_id],
       a.name                                                     AS [name],
       CONVERT(int, a.enabled)                                    AS [enabled],
       a.severity                                                 AS [severity],
       a.message_id                                               AS [message_id],
       a.database_name                                            AS [database_name],
       a.event_source                                             AS [event_source],
       a.occurrence_count                                         AS [occurrence_count],
       a.last_occurrence_date                                     AS [last_occurrence_date],
       a.last_response_date                                       AS [last_response_date],
       CONVERT(int, a.has_notification)                           AS [has_notification],
       a.performance_condition                                    AS [performance_condition],
       a.notification_message                                     AS [notification_message]
FROM msdb.dbo.sysalerts AS a
ORDER BY a.severity DESC, a.message_id, a.name
OPTION (RECOMPILE, MAXDOP 1);

/* Addresses are tested, never projected. See the header. */
SELECT o.id                                                       AS [operator_id],
       o.name                                                     AS [name],
       CONVERT(int, o.enabled)                                    AS [enabled],
       CASE WHEN NULLIF(LTRIM(RTRIM(ISNULL(o.email_address, ''))), '') IS NULL
            THEN 0 ELSE 1 END                                     AS [has_email_address],
       CASE WHEN NULLIF(LTRIM(RTRIM(ISNULL(o.pager_address, ''))), '') IS NULL
            THEN 0 ELSE 1 END                                     AS [has_pager_address],
       CASE WHEN NULLIF(LTRIM(RTRIM(ISNULL(o.netsend_address, ''))), '') IS NULL
            THEN 0 ELSE 1 END                                     AS [has_netsend_address],
       o.last_email_date                                          AS [last_email_date],
       o.weekday_pager_start_time                                 AS [weekday_pager_start_time],
       o.weekday_pager_end_time                                   AS [weekday_pager_end_time]
FROM msdb.dbo.sysoperators AS o
ORDER BY o.name
OPTION (RECOMPILE, MAXDOP 1);

/* What is actually bound to what. An alert and an operator both existing is
   not the same as the first reaching the second. */
SELECT n.alert_id                                                 AS [alert_id],
       a.name                                                     AS [alert],
       n.operator_id                                              AS [operator_id],
       o.name                                                     AS [operator],
       n.notification_method                                      AS [notification_method],
       /* The method is a bitmask: 1 email, 2 pager, 4 net send. Decoding a
          documented enumeration is a lookup and not a judgement, the same
          argument 010.jobs.sql makes for run_status. */
       CASE WHEN n.notification_method & 1 > 0 THEN 1 ELSE 0 END  AS [by_email],
       CASE WHEN n.notification_method & 2 > 0 THEN 1 ELSE 0 END  AS [by_pager],
       CASE WHEN n.notification_method & 4 > 0 THEN 1 ELSE 0 END  AS [by_netsend]
FROM msdb.dbo.sysnotifications AS n
JOIN msdb.dbo.sysalerts        AS a ON a.id = n.alert_id
JOIN msdb.dbo.sysoperators     AS o ON o.id = n.operator_id
ORDER BY a.name, o.name
OPTION (RECOMPILE, MAXDOP 1);
