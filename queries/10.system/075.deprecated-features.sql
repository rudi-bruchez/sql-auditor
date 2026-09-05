-- @scope:       instance
-- @resultsets:  root:object, deprecated:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- Deprecated features the instance still answers to, and how often since the
-- last restart.
--
-- Why this collector exists. The SQL Server Assessment ruleset flags deprecated
-- features in use, and nothing else in this archive says so. The counters live
-- in sys.dm_os_performance_counters under 'SQLServer:Deprecated Features'
-- ('MSSQL$<instance>:Deprecated Features' on a named instance, which the LIKE
-- covers), one instance_name per feature.
--
-- ONLY COUNTERS ABOVE ZERO ARE PROJECTED, and that is the filter the Microsoft
-- probe itself applies: an idle-but-old instance carries more than two hundred
-- deprecated-features counters at exactly zero, and shipping them all would
-- bury the thirty-something that carry a signal in every archive. A feature
-- absent from this array was not seen above zero since the restart — absence
-- here is not proof of absence.
--
-- instance_name IS NCHAR AND PADDED, which is why it is RTRIMmed: the same
-- feature name arrives with trailing spaces from this counter and without
-- them from anywhere else, and a topic joining on the raw value would miss
-- every match.
--
-- THE WINDOW IS SINCE THE LAST RESTART, and nothing more. These counters do
-- not persist; an instance up for a week reports a week of usage, an instance
-- restarted last night reports almost nothing. The root object carries the
-- instance start so the window is never wider than the reader thinks it is.
--
-- NO JUDGEMENT IS APPLIED. A feature listed here is a fact — usage counts, not
-- verdicts. Whether a given count tolerates the usage is a ruleset question.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       (SELECT COUNT(*)
          FROM sys.dm_os_performance_counters AS pc
         WHERE pc.object_name LIKE '%Deprecated Features%'
           AND pc.cntr_value > 0)                                 AS [features.count],
       CONVERT(varchar(23), (SELECT sqlserver_start_time
                               FROM sys.dm_os_sys_info), 126)    AS [instance_start]
OPTION (RECOMPILE, MAXDOP 1);

SELECT RTRIM(pc.instance_name)                                    AS [feature],
       pc.cntr_value                                              AS [value]
FROM sys.dm_os_performance_counters AS pc
WHERE pc.object_name LIKE '%Deprecated Features%'
  AND pc.cntr_value > 0
ORDER BY pc.cntr_value DESC
OPTION (RECOMPILE, MAXDOP 1);
