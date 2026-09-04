-- @scope:       instance
-- @resultsets:  root:object, status:object, top_messages:array, by_source:array, notable:array, database_mounts:array
-- @permissions: CONNECT, ERROR LOG
-- @timeout:     300
--
-- The current SQL Server error log, summarised.
--
-- Why this collector exists: on a real audit this file was the single richest
-- source on the instance. It dated a restart to the second, showed recovery
-- completing in fifteen seconds, counted 38 897 login failures against one
-- offline database, revealed nightly log-backup failures nobody had seen, and
-- carried the last CHECKDB date for every database. None of it is reachable
-- from any catalog view.
--
-- THE LOG IS NOT DUMPED. A 284-day log held tens of thousands of lines, 94 %
-- of them one repeated message. Shipping it whole would bury the signal and
-- bloat the archive; shipping the tail would miss exactly the recurring
-- failure that matters. So it is aggregated by message prefix, which is what
-- makes a repetition visible as a count instead of as noise.
--
-- Grouping is on LEFT(text, 80) and NOT on a parsed error number, because the
-- log is LOCALISED: the same event reads "Error: 18456, Severity: 14" on an
-- English instance and "Erreur : 18456, Gravité : 14" on a French one. Any
-- parser keyed on English words returns nothing at all on half the estate,
-- silently. A prefix works in every language, and the sample text lets the
-- analysis layer parse afterwards if it wants to.
--
-- Only log file 0 — the current one — is read. Archived logs need one call
-- each and their number is a server setting; the date range is reported so a
-- reader knows what window the counts cover rather than assuming "everything".
--
-- sp_readerrorlog, not xp_readerrorlog. The extended procedure is denied to
-- anyone below sysadmin, while the wrapper is reachable through ownership
-- chaining: on the audited instance a read-only login could execute the first
-- and not the second.
--
-- SQL Server 2012 is the floor. sp_readerrorlog predates it.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @collected bit = 1, @err int = 0, @msg nvarchar(2048) = N'';

CREATE TABLE #log (LogDate datetime, ProcessInfo nvarchar(100), Txt nvarchar(4000));

/* The permission probe answers "may I", not "does it work". A denied EXEC, a
   log being rolled over mid-read, or a text column longer than the table
   accepts all fail here — and an empty summary must never be readable as
   "nothing was logged". status carries the difference. */
BEGIN TRY
    INSERT INTO #log EXEC sys.sp_readerrorlog 0;
END TRY
BEGIN CATCH
    SELECT @collected = 0, @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH;

SELECT COUNT(*)                                                   AS [lines],
       MIN(l.LogDate)                                             AS [oldest],
       MAX(l.LogDate)                                             AS [newest],
       DATEDIFF(second, MIN(l.LogDate), MAX(l.LogDate))           AS [span_seconds],
       COUNT(DISTINCT LEFT(l.Txt, 80))                            AS [distinct_message_prefixes],
       SYSDATETIME()                                              AS [collected_at]
FROM #log AS l
OPTION (RECOMPILE, MAXDOP 1);

SELECT @collected                                                 AS [collected],
       @err                                                       AS [error_number],
       NULLIF(@msg, N'')                                          AS [error_message],
       0                                                          AS [log_file],
       80                                                         AS [grouping_prefix_length],
       40                                                         AS [top_messages_kept]
OPTION (RECOMPILE, MAXDOP 1);

/* TOP 40 by count, and the cut is REPORTED above rather than left implicit: a
   truncated list that does not say it is truncated reads as a complete one. */
--
-- POURQUOI UN CLASSEMENT PAR FRÉQUENCE NE SUFFIT PAS.
--
-- top_messages rend les quarante préfixes les plus fréquents, ce qui est la
-- bonne question pour « qu'est-ce qui pollue le journal ». C'est la mauvaise
-- pour « que s'est-il passé ». Sur l'instance qui a motivé ce jeu de
-- résultats, le journal comptait 5 252 préfixes distincts et deux événements
-- décisifs étaient uniques, donc invisibles :
--
--   Autogrow of file 'X_log' ... was cancelled by user or timed out
--   Configuration option 'max server memory (MB)' changed from 220000 to 300000
--
-- Le second datait du lendemain d'un redémarrage difficile : quelqu'un avait
-- augmenté la mémoire pour régler un problème de performance. Cela n'a servi à
-- rien, l'édition plafonnant le buffer pool, mais l'audit devait le savoir et
-- ne l'a pas su.
--
-- D'où ce jeu de résultats : une liste fermée de motifs qui comptent quelle que
-- soit leur fréquence, rendus par ordre chronologique. Un changement de
-- configuration, une extension de fichier annulée, une entrée/sortie longue,
-- une erreur de cohérence ou un CHECKDB se lisent une fois et pèsent lourd.

SELECT TOP (40)
       LEFT(l.Txt, 80)                                            AS [message_prefix],
       COUNT(*)                                                   AS [occurrences],
       MIN(l.LogDate)                                             AS [first_seen],
       MAX(l.LogDate)                                             AS [last_seen],
       MIN(LEFT(l.Txt, 400))                                      AS [sample]
FROM #log AS l
GROUP BY LEFT(l.Txt, 80)
ORDER BY COUNT(*) DESC
OPTION (RECOMPILE, MAXDOP 1);

/* ProcessInfo is locale-independent and tells a reader which subsystem is
   talking: Logon, Backup, Server, or a session id. Session ids are collapsed
   because their individual values carry nothing once the log is aggregated. */
SELECT CASE WHEN l.ProcessInfo LIKE 'spid%' THEN 'spid' ELSE l.ProcessInfo END AS [source],
       COUNT(*)                                                   AS [occurrences],
       MIN(l.LogDate)                                             AS [first_seen],
       MAX(l.LogDate)                                             AS [last_seen]
FROM #log AS l
GROUP BY CASE WHEN l.ProcessInfo LIKE 'spid%' THEN 'spid' ELSE l.ProcessInfo END
ORDER BY COUNT(*) DESC
OPTION (RECOMPILE, MAXDOP 1);


/* Les événements qui comptent une fois. Le cap est de 200 lignes et il est
   reporté, parce qu'une liste tronquée sans le dire se lit comme une liste
   complète. L'ordre est chronologique : ce jeu se lit comme un récit, pas
   comme un classement. */
WITH frequence AS (
    /* Un motif notable peut aussi être bavard. « Configuration option 'user
       options' changed from 0 to 0 » correspond au filtre et apparaît 537 fois
       sur l'instance auditée : à lui seul il remplissait le cap et chassait les
       événements uniques, qui sont la raison d'être de ce jeu. Ce qui est
       fréquent est déjà dans top_messages ; ici on ne garde que le rare. */
    SELECT LEFT(RTRIM(Txt), 80) AS prefixe, COUNT(*) AS n
    FROM #log GROUP BY LEFT(RTRIM(Txt), 80))
SELECT TOP (200)
       l.LogDate                                                  AS [when],
       RTRIM(l.ProcessInfo)                                       AS [source],
       LEFT(RTRIM(l.Txt), 400)                                    AS [message],
       f.n                                                        AS [occurrences]
FROM       #log AS l
JOIN       frequence AS f ON f.prefixe = LEFT(RTRIM(l.Txt), 80)
WHERE f.n <= 20
  AND (l.Txt LIKE '%Configuration option%changed from%'
   OR l.Txt LIKE '%Autogrow of file%'
   OR l.Txt LIKE '%taking longer than%'
   OR l.Txt LIKE '%CHECKDB%'
   OR l.Txt LIKE '%consistency error%'
   OR l.Txt LIKE '%severe error%'
   OR l.Txt LIKE '%Recovery is complete%'
   OR l.Txt LIKE '%Setting database option%'
   OR l.Txt LIKE '%deadlock%'
   OR l.Txt LIKE '%stack dump%'
   OR l.Txt LIKE '%out of memory%'
   OR l.Txt LIKE '%could not be started%')
ORDER BY l.LogDate
OPTION (RECOMPILE, MAXDOP 1);

/* Quand chaque base a été montée, et combien de fois.

   Ce jeu existe pour une question précise qu'aucune autre source ne tranche :
   une base sans aucune ligne dans sys.dm_db_index_usage_stats est-elle jamais
   sollicitée, ou ses compteurs ont-ils été remis à zéro ? Ils le sont à chaque
   montage — donc à chaque démarrage d'instance, mais aussi à chaque restauration,
   attachement, passage hors ligne puis en ligne, ou changement d'état. Conclure
   « tous ces index sont inutilisés » sur une base montée il y a deux heures est
   une erreur, pas un constat.

   Il lui faut son propre jeu plutôt qu'une ligne de plus dans notable : ces
   messages sont fréquents par nature — un par base et par montage — donc le
   filtre de rareté de notable les écarte, et le cap de top_messages les noie.
   Ils sont regroupés par base, ce qui les rend à la fois complets et courts.

   La fenêtre est celle du journal d'erreurs lui-même, qui est recyclé : une base
   absente d'ici n'a pas forcément échappé à un montage, elle peut simplement
   avoir été montée avant le plus ancien fichier conservé. first_seen le dit. */
SELECT
       LTRIM(RTRIM(REPLACE(REPLACE(
           SUBSTRING(RTRIM(l.Txt),
                     CHARINDEX('''', RTRIM(l.Txt)) + 1,
                     CASE WHEN CHARINDEX('''', RTRIM(l.Txt),
                                         CHARINDEX('''', RTRIM(l.Txt)) + 1) > 0
                          THEN CHARINDEX('''', RTRIM(l.Txt),
                                         CHARINDEX('''', RTRIM(l.Txt)) + 1)
                               - CHARINDEX('''', RTRIM(l.Txt)) - 1
                          ELSE 0 END),
           CHAR(13), ''), CHAR(10), '')))                          AS [database],
       COUNT(*)                                                    AS [mounts],
       MIN(l.LogDate)                                              AS [first_seen],
       MAX(l.LogDate)                                              AS [last_seen]
FROM #log AS l
WHERE l.Txt LIKE 'Starting up database %'
GROUP BY LTRIM(RTRIM(REPLACE(REPLACE(
           SUBSTRING(RTRIM(l.Txt),
                     CHARINDEX('''', RTRIM(l.Txt)) + 1,
                     CASE WHEN CHARINDEX('''', RTRIM(l.Txt),
                                         CHARINDEX('''', RTRIM(l.Txt)) + 1) > 0
                          THEN CHARINDEX('''', RTRIM(l.Txt),
                                         CHARINDEX('''', RTRIM(l.Txt)) + 1)
                               - CHARINDEX('''', RTRIM(l.Txt)) - 1
                          ELSE 0 END),
           CHAR(13), ''), CHAR(10), '')))
ORDER BY MAX(l.LogDate) DESC
OPTION (RECOMPILE, MAXDOP 1);
