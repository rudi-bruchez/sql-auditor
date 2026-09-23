-- @scope:       instance
-- @resultsets:  root:object, status:object, top_messages:array, by_source:array, notable:array, database_mounts:array, derived_patterns:array, recovery:array
-- @permissions: CONNECT, ERROR LOG
-- @timeout:     300
-- @discloses:   error_log
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

/* ===========================================================================
   DERIVING A LIKE PATTERN FROM A MESSAGE NUMBER.

   The header above says the log is localised and that a parser keyed on
   English words returns nothing at all on half the estate, silently. The
   notable set below is exactly such a parser: twelve hand-written English
   LIKE patterns. Most of them are nets — %CHECKDB% covers 38 messages,
   %deadlock% 13 — and a net is left alone. Two messages carry a DECISION
   instead, and those are derived here from their number:

     9017   the log has an excessive number of virtual log files
     3421   recovery completed for a database, with the time it took

   sys.messages holds the same message in every installed language, and the
   number is the only thing that is the same in all of them. So the pattern is
   built from the template: split it on its parameter markers, keep the
   literal pieces, and take the longest piece that identifies this message and
   no other in that language's catalog. Nothing below is specific to a number
   or to a language; @wanted is the input and the rest follows.

   Eight things this has to get right, each of them measured:

   1. BINARY COLLATION on both sides, for the split and for the uniqueness
      check. The French fragment of 17137 is unique under Latin1_General_BIN2
      and appears four times under a case- and accent-insensitive server
      collation (messages 915, 41633, 41636), so without it the derivation
      finds no unique fragment in French, Italian and Norwegian — the very
      case that motivates deriving at all.

   2. A GENERAL MARKER PATTERN, never an enumerated list. The English catalog
      alone uses 68 distinct markers: %1! through %27!, %ld, %x, %p, %I64u,
      %#016I64x, %.*ls, %0*I64x, and the symbolic %S_MSG (706 messages),
      %S_PGID (199), %S_LSN (43), %S_DATE, %S_XID, %S_RID, %S_TS, %S_PAGE,
      %S_BLKIOPTR. Message 3421 alone carries eleven parameters.

   3. A FRAGMENT THAT STILL CARRIES A % IS REFUSED. The failure it prevents is
      silence, not noise: a pattern built from a half-recognised marker asks
      the log for literal text that the log never contains, and returns zero
      rows while looking like it worked.

   4. THE LONGEST FRAGMENT THAT IS UNIQUE, not the longest. The longest fixed
      piece of 3421 is the closing courtesy, present in all 22 languages and
      shared by 119 English messages, 157 German ones and 22 French ones.

   5. NOT THE PREFIX. The template opens on its first parameter in Turkish for
      9017 and 3421, and in German for 17137, where it reads
      %1!-Datenbank wird gestartet and has no leading literal at all.

   6. %, _ AND [ ARE ESCAPED, with an ESCAPE clause. Measured:
      N'redo 12 ms [system undo 3 ms].' LIKE N'% ms [system undo %' is NO
      MATCH and becomes a match with \[ and ESCAPE N'\'. The backslash itself
      is escaped first, or a fragment containing one would break the clause
      that protects the other three.

   7. THE LANGUAGE IS FOUND BY VERIFICATION. It is not read from the login's
      default language, which sets the language of NEW logins and says nothing
      about the language the engine wrote this log in. Every language that has
      a catalog is tried, its longest usable fragment is matched against the
      lines actually in #log, and the one that matches most lines wins.

   8. THE FALLBACK IS 1033. Only 22 languages have a catalog in sys.messages
      while sys.syslanguages lists 33 distinct msglangid values, so a session
      language is not evidence that a catalog exists. Iterating the language
      ids present in sys.messages avoids the question entirely, and also
      avoids the two join traps around sys.syslanguages: langid and
      language_id are different things, and 1033 is carried by two rows.

   The whole block costs 250 to 330 milliseconds for three numbers against a
   @timeout of 300 seconds. The cost that has to be watched is the uniqueness
   check, which is one catalog scan per candidate fragment; copying the
   retained language into #cat first is what keeps it at 100 ms rather than
   scanning all twenty-two languages every time.
   =========================================================================== */

DECLARE @lang int = 1033;

DECLARE @wanted TABLE (message_id int PRIMARY KEY);
INSERT INTO @wanted (message_id) VALUES (9017), (3421), (17137);

/* The split. A marker is recognised by a grammar, not by a list:
     - % followed by digits and then "!" is a positional parameter, %27!
       included, and it ends at the "!";
     - otherwise % takes the C form: any run of flags, width, precision and
       length modifiers (# . * digits h l I), then one conversion character,
       which is what makes %d, %ls, %.*ls, %I64u and %#016I64x all end in the
       right place;
     - a conversion character of S followed by "_" is the symbolic form and
       swallows the name after it, %S_MSG and %S_PGID with it. The check is
       case sensitive, which is why the binary collation is on the template
       before anything else touches it: %s and %S are different markers.
   A marker that overlaps the one before it is dropped, which is what a
   doubled %% is, and a fragment of length zero or less is clamped to empty.
   Fragment n sits before marker n, so fragment n and fragment n+1 are the two
   literals that delimit parameter n. */
CREATE TABLE #frag (message_id int, language_id int, seq int, ordinal int, fstart int,
                    frag nvarchar(2048) COLLATE Latin1_General_BIN2,
                    esc  nvarchar(4000) COLLATE Latin1_General_BIN2);

WITH tally AS (
    SELECT TOP (2048) n = CONVERT(int, ROW_NUMBER() OVER (ORDER BY (SELECT NULL)))
    FROM sys.all_objects AS a CROSS JOIN sys.all_objects AS b),
tpl AS (
    SELECT m.message_id, m.language_id,
           tmpl = CONVERT(nvarchar(2048), m.text) COLLATE Latin1_General_BIN2,
           tlen = CONVERT(int, DATALENGTH(m.text) / 2)
    FROM sys.messages AS m
    JOIN @wanted     AS w ON w.message_id = m.message_id),
mk0 AS (
    SELECT t.message_id, t.language_id, t.tlen, pos = p.n,
           mlen = CASE WHEN o.isord = 1 THEN 1 + d.dig + 1 ELSE 1 + c.k + 1 + y.sym END,
           /* The VALUE of the digits, not how many there are. %10! is
              parameter ten and %9! is parameter nine; taking the run length
              here made every single-digit marker parameter 1 and every
              two-digit one parameter 2, which showed up as nine duplicate
              rows for the Dutch 3421 and a recovery time of NULL. */
           ord  = CASE WHEN o.isord = 1 THEN CONVERT(int, SUBSTRING(x.s, 1, d.dig)) ELSE NULL END
    FROM       tpl   AS t
    JOIN       tally AS p ON p.n <= t.tlen
    CROSS APPLY (SELECT s = SUBSTRING(t.tmpl, p.n + 1, 48) + N'zz') AS x
    CROSS APPLY (SELECT dig = PATINDEX(N'%[^0-9]%', x.s) - 1) AS d
    CROSS APPLY (SELECT isord = CASE WHEN d.dig > 0 AND SUBSTRING(x.s, d.dig + 1, 1) = N'!'
                                     THEN 1 ELSE 0 END) AS o
    CROSS APPLY (SELECT k = PATINDEX(N'%[^#.*0-9hlI]%', x.s) - 1) AS c
    CROSS APPLY (SELECT conv = SUBSTRING(x.s, c.k + 1, 1)) AS v
    CROSS APPLY (SELECT sym = CASE WHEN v.conv = N'S' AND SUBSTRING(x.s, c.k + 2, 1) = N'_'
                                   THEN PATINDEX(N'%[^A-Z0-9_]%', SUBSTRING(x.s, c.k + 2, 48) + N'zz') - 1
                                   ELSE 0 END) AS y
    WHERE SUBSTRING(t.tmpl, p.n, 1) = N'%'),
mk AS (
    SELECT message_id, language_id, tlen, pos, mlen, ord,
           seq = ROW_NUMBER() OVER (PARTITION BY message_id, language_id ORDER BY pos)
    FROM (SELECT z0.*, prevend = LAG(z0.pos + z0.mlen) OVER (PARTITION BY z0.message_id,
                                       z0.language_id ORDER BY z0.pos)
          FROM mk0 AS z0) AS z
    WHERE z.prevend IS NULL OR z.pos >= z.prevend),
edge AS (
    SELECT message_id, language_id, seq, ord,
           fstart = CASE WHEN seq = 1 THEN 1
                         ELSE LAG(pos + mlen) OVER (PARTITION BY message_id, language_id ORDER BY seq) END,
           fend = pos
    FROM mk
    UNION ALL
    SELECT message_id, language_id, MAX(seq) + 1, NULL, MAX(pos + mlen), MAX(tlen) + 1
    FROM mk GROUP BY message_id, language_id)
INSERT INTO #frag (message_id, language_id, seq, ordinal, fstart, frag, esc)
SELECT e.message_id, e.language_id, e.seq,
       COALESCE(e.ord, e.seq),
       e.fstart,
       f.frag,
       REPLACE(REPLACE(REPLACE(REPLACE(
                 f.frag, N'\', N'\\'), N'%', N'\%'), N'_', N'\_'), N'[', N'\[')
FROM       edge AS e
JOIN       tpl  AS t ON t.message_id = e.message_id AND t.language_id = e.language_id
CROSS APPLY (SELECT frag = CONVERT(nvarchar(2048), SUBSTRING(t.tmpl, e.fstart,
                 CASE WHEN e.fend - e.fstart > 0 THEN e.fend - e.fstart ELSE 0 END))) AS f;

/* The language, by verification against the lines that are actually there.
   Only the longest usable fragment of each message is tried: an earlier
   version of this scored every fragment and Czech, Finnish, Dutch and Swedish
   each beat English 184 to 91 on an English log, because two-character
   fragments such as N'. ' match anything. Ties go to 1033. */
SELECT @lang = COALESCE((
    SELECT TOP (1) f.language_id
    FROM (SELECT g.language_id, g.esc,
                 rn = ROW_NUMBER() OVER (PARTITION BY g.message_id, g.language_id
                                         ORDER BY DATALENGTH(g.frag) DESC)
          FROM #frag AS g
          WHERE DATALENGTH(g.frag) > 0 AND CHARINDEX(N'%', g.frag) = 0) AS f
    JOIN #log AS l ON l.Txt COLLATE Latin1_General_BIN2 LIKE N'%' + f.esc + N'%' ESCAPE N'\'
    WHERE f.rn = 1
    GROUP BY f.language_id
    ORDER BY COUNT(*) DESC, CASE WHEN f.language_id = 1033 THEN 0 ELSE 1 END, f.language_id), 1033);

/* One copy of the retained language's catalog, in the binary collation, so
   the uniqueness check below reads 16 750 rows instead of 368 500. */
CREATE TABLE #cat (message_id int, txt nvarchar(2048) COLLATE Latin1_General_BIN2);
INSERT INTO #cat (message_id, txt)
SELECT k.message_id, CONVERT(nvarchar(2048), k.text)
FROM sys.messages AS k
WHERE k.language_id = @lang;

/* The candidates, and how many messages of this catalog each one matches.
   The WHERE line carrying CHARINDEX(N'%', ...) is requirement 3: a fragment
   that still holds a marker never becomes a pattern. */
CREATE TABLE #uniq (message_id int, seq int,
                    frag nvarchar(2048) COLLATE Latin1_General_BIN2,
                    esc  nvarchar(4000) COLLATE Latin1_General_BIN2,
                    flen int, hits int);
INSERT INTO #uniq (message_id, seq, frag, esc, flen, hits)
SELECT f.message_id, f.seq, f.frag, f.esc,
       CONVERT(int, DATALENGTH(f.frag) / 2),
       (SELECT COUNT(*) FROM #cat AS k WHERE k.txt LIKE N'%' + f.esc + N'%' ESCAPE N'\')
FROM #frag AS f
WHERE f.language_id = @lang
  AND DATALENGTH(f.frag) > 0
  AND CHARINDEX(N'%', f.frag) = 0;

/* The delimiters of every parameter, which is what an extraction needs and a
   unique fragment does not give. The left literal is empty for parameter 1 of
   the German 17137, and that is a supported answer, not a missing one. */
DECLARE @bounds TABLE (message_id int, ordinal int,
                       lit_left nvarchar(2048), pos_left int,
                       lit_right nvarchar(2048), pos_right int);
INSERT INTO @bounds (message_id, ordinal, lit_left, pos_left, lit_right, pos_right)
SELECT z.message_id, z.ordinal, z.lit_left, z.pos_left, z.lit_right, z.pos_right
FROM (SELECT a.message_id, a.ordinal, lit_left = a.frag, pos_left = a.fstart,
             lit_right = b.frag, pos_right = b.fstart,
             /* A template may name the same parameter twice. One row per
                ordinal, the first occurrence, so nothing downstream has to
                choose between two answers. */
             rn = ROW_NUMBER() OVER (PARTITION BY a.message_id, a.ordinal ORDER BY a.seq)
      FROM #frag AS a
      JOIN #frag AS b ON b.message_id = a.message_id
                     AND b.language_id = a.language_id
                     AND b.seq = a.seq + 1
      WHERE a.language_id = @lang) AS z
WHERE z.rn = 1;

DECLARE @derived TABLE (message_id int, language_id int, template nvarchar(2048),
                        fragment nvarchar(2048), pattern nvarchar(4000),
                        catalog_matches int, param_ordinal int,
                        lit_left nvarchar(2048), pos_left int,
                        lit_right nvarchar(2048), pos_right int,
                        refused nvarchar(200));
INSERT INTO @derived (message_id, language_id, template, fragment, pattern,
                      catalog_matches, param_ordinal, lit_left, pos_left,
                      lit_right, pos_right, refused)
SELECT w.message_id, @lang, m.text, best.frag,
       CASE WHEN best.frag IS NULL THEN NULL
            ELSE CONVERT(nvarchar(4000), N'%' + best.esc + N'%') END,
       best.hits, b.ordinal, b.lit_left, b.pos_left, b.lit_right, b.pos_right,
       CASE WHEN m.message_id IS NULL
                 THEN N'this message has no entry in the retained language of sys.messages'
            WHEN best.frag IS NOT NULL THEN NULL
            WHEN cand.n = 0
                 THEN N'every fragment of the template still carries a parameter marker after the split'
            ELSE N'no fragment of this message is unique in the catalog of the retained language'
       END
FROM @wanted AS w
LEFT JOIN sys.messages AS m ON m.message_id = w.message_id AND m.language_id = @lang
OUTER APPLY (SELECT n = COUNT(*) FROM #uniq AS u WHERE u.message_id = w.message_id) AS cand
OUTER APPLY (SELECT TOP (1) u.frag, u.esc, u.hits
             FROM #uniq AS u
             WHERE u.message_id = w.message_id AND u.hits = 1
             ORDER BY u.flen DESC, u.seq) AS best
LEFT JOIN @bounds AS b ON b.message_id = w.message_id AND b.ordinal = 1;

/* notable reads these two, and the recovery set below reads the delimiters of
   3421. Parameter 3 of 3421 is the elapsed seconds in every language: the
   localised templates number their parameters explicitly, and the English one
   has them in that order. */
DECLARE @p9017 nvarchar(4000), @p3421 nvarchar(4000);
SELECT @p9017 = MAX(CASE WHEN message_id = 9017 THEN pattern END),
       @p3421 = MAX(CASE WHEN message_id = 3421 THEN pattern END)
FROM @derived;

DECLARE @db_left nvarchar(2048), @db_right nvarchar(2048),
        @sec_left nvarchar(2048), @sec_right nvarchar(2048);
SELECT @db_left = lit_left, @db_right = lit_right
FROM @bounds WHERE message_id = 3421 AND ordinal = 1;
SELECT @sec_left = lit_left, @sec_right = lit_right
FROM @bounds WHERE message_id = 3421 AND ordinal = 3;

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
   OR l.Txt LIKE '%could not be started%'
   /* The two derived ones. They are added to the list rather than replacing
      anything: the twelve above are nets, these two carry a decision. Neither
      9017 nor 3421 was reachable through the twelve, in any language.

      The rarity filter above still applies to them, and what it drops is not
      what one would guess. Measured on thirty synthetic recoveries: thirty
      DIFFERENT databases all survive, because the database name and id fall
      inside the eighty characters the filter groups on, so each one is its
      own prefix with a count of one. Thirty recoveries of the SAME database
      are all dropped. So the case that loses 3421 here is an instance that
      restarts or cycles one database repeatedly, not an instance with many
      databases. That is what the recovery set below is for. */
   OR (@p9017 IS NOT NULL AND l.Txt COLLATE Latin1_General_BIN2 LIKE @p9017 ESCAPE N'\')
   OR (@p3421 IS NOT NULL AND l.Txt COLLATE Latin1_General_BIN2 LIKE @p3421 ESCAPE N'\'))
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

/* What the derivation decided, per number, whether or not it succeeded.
   A reader has to be able to tell a message that did not occur from a message
   whose pattern could not be built, and refused says which. catalog_matches
   is the uniqueness itself: 1 when the fragment names this message and no
   other, which is the condition the fragment was chosen under.

   The two literals and their positions are here because a unique fragment is
   not enough to EXTRACT anything. Delimiting parameter 1 needs what sits on
   either side of it, and the left side is legitimately empty — the German
   17137 is %1!-Datenbank wird gestartet and starts on its parameter.

   Message 18456 is where this mechanism stops, and it is worth knowing why:
   a unique fragment exists for it in only two languages out of twenty-two,
   because "Login failed for user '" appears in fifteen English messages. This
   set is how that would be visible rather than silent. */
SELECT d.message_id                                                AS [message_id],
       d.language_id                                               AS [language_id],
       d.template                                                  AS [template],
       d.fragment                                                  AS [fragment],
       d.pattern                                                   AS [pattern],
       CONVERT(nchar(1), N'\')                                     AS [escape_char],
       d.catalog_matches                                           AS [catalog_matches],
       CASE WHEN d.pattern IS NULL THEN NULL ELSE
            (SELECT COUNT(*) FROM #log AS l
             WHERE l.Txt COLLATE Latin1_General_BIN2 LIKE d.pattern ESCAPE N'\')
       END                                                         AS [log_matches],
       d.param_ordinal                                             AS [param_ordinal],
       d.lit_left                                                  AS [left_literal],
       d.pos_left                                                  AS [left_literal_pos],
       d.lit_right                                                 AS [right_literal],
       d.pos_right                                                 AS [right_literal_pos],
       d.refused                                                   AS [refused]
FROM @derived AS d
ORDER BY d.message_id
OPTION (RECOMPILE, MAXDOP 1);

/* One row per recovery, with the time it took as a NUMBER.

   This exists because the analysis layer cannot get the number out of the
   string. The duration arrives inside a localised sentence, and parsing it
   downstream would mean twenty-two regular expressions that go stale with the
   next translation. Here the two literals that delimit parameter 3 are
   already known, so the same CHARINDEX pair that isolates the database name
   isolates the seconds, in any language, with no language named.

   It also exists because notable can lose 3421 to its own rarity filter on an
   instance with more than twenty databases, and this is the message that says
   whether a log with a large virtual-log-file count actually costs anything
   at startup. seconds_text is kept beside the number so a locale that groups
   its digits in a way CONVERT refuses is visible rather than silently NULL. */
SELECT l.LogDate                                                   AS [when],
       CASE WHEN p.db_from > 0 AND p.db_to > p.db_from
            THEN SUBSTRING(t.line, p.db_from, p.db_to - p.db_from) END
                                                                   AS [database],
       TRY_CONVERT(bigint, REPLACE(REPLACE(
            CASE WHEN p.sec_from > 0 AND p.sec_to > p.sec_from
                 THEN SUBSTRING(t.line, p.sec_from, p.sec_to - p.sec_from) END,
            N' ', N''), NCHAR(160), N''))                          AS [recovery_seconds],
       CASE WHEN p.sec_from > 0 AND p.sec_to > p.sec_from
            THEN SUBSTRING(t.line, p.sec_from, p.sec_to - p.sec_from) END
                                                                   AS [seconds_text]
FROM        #log AS l
CROSS APPLY (SELECT line = CONVERT(nvarchar(4000), RTRIM(l.Txt)) COLLATE Latin1_General_BIN2) AS t
CROSS APPLY (SELECT
        db_from = CASE WHEN DATALENGTH(@db_left) = 0 THEN 1
                       ELSE NULLIF(CHARINDEX(@db_left, t.line), 0) + DATALENGTH(@db_left) / 2 END,
        sec_from = CASE WHEN DATALENGTH(@sec_left) = 0 THEN 1
                        ELSE NULLIF(CHARINDEX(@sec_left, t.line), 0) + DATALENGTH(@sec_left) / 2 END) AS s
CROSS APPLY (SELECT s.db_from, s.sec_from,
        db_to = CASE WHEN DATALENGTH(@db_right) = 0 THEN DATALENGTH(t.line) / 2 + 1
                     ELSE NULLIF(CHARINDEX(@db_right, t.line, s.db_from), 0) END,
        sec_to = CASE WHEN DATALENGTH(@sec_right) = 0 THEN DATALENGTH(t.line) / 2 + 1
                      ELSE NULLIF(CHARINDEX(@sec_right, t.line, s.sec_from), 0) END) AS p
WHERE @p3421 IS NOT NULL
  AND t.line LIKE @p3421 ESCAPE N'\'
ORDER BY l.LogDate
OPTION (RECOMPILE, MAXDOP 1);
