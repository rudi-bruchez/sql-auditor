-- @scope:       instance
-- @resultsets:  root:object, status:object, top_messages:array, by_source:array, notable:array, database_mounts:array, derived_patterns:array, recovery:array, vlf_warnings:array
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
-- THE LOG IS MEASURED BEFORE IT IS READ. The whole current log goes into #log
-- in tempdb, and an instance that never cycles its log can carry hundreds of
-- megabytes of it. sys.sp_enumerrorlogs gives the size of every log file
-- without reading any, under the same permission check as sp_readerrorlog,
-- and above 50 MB the read is skipped: status then says collected = 0 with
-- skipped_for_size = 1 and the size, and every other set is empty because the
-- log was not read, not because nothing was logged. Measured on SQL Server
-- 2025, a 107 KB log held 798 lines and took 155 KB of text in #log, so 50 MB
-- is in the order of 400 000 lines and 70 MB of tempdb: a log that size has
-- gone unrecycled for a long time, and the copy costs the instance more than
-- the summary is worth. The remedy is sp_cycle_errorlog, which is the
-- operator's to run, and the next collection reads the new, short log. If the
-- size cannot be read, the log is read as before and status carries why the
-- size is missing.
--
-- SQL Server 2012 is the floor. sp_readerrorlog and sp_enumerrorlogs predate it.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @collected bit = 1, @err int = 0, @msg nvarchar(2048) = N'';

-- The size guard. See the header: 50 MB, in bytes.
DECLARE @size_limit bigint = 52428800, @log_bytes bigint = NULL,
        @skipped_for_size bit = 0, @size_msg nvarchar(2048) = N'';

CREATE TABLE #logs (archive int, log_date nvarchar(64), size_bytes bigint);
BEGIN TRY
    INSERT INTO #logs (archive, log_date, size_bytes) EXEC sys.sp_enumerrorlogs;
    SELECT @log_bytes = size_bytes FROM #logs WHERE archive = 0;
END TRY
BEGIN CATCH
    SELECT @size_msg = ERROR_MESSAGE();
END CATCH;

CREATE TABLE #log (LogDate datetime, ProcessInfo nvarchar(100), Txt nvarchar(4000));

/* The permission probe answers "may I", not "does it work". A denied EXEC, a
   log being rolled over mid-read, or a text column longer than the table
   accepts all fail here — and an empty summary must never be readable as
   "nothing was logged". status carries the difference. */
IF @log_bytes > @size_limit
    SELECT @collected = 0, @skipped_for_size = 1;
ELSE
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

   A second list of numbers is derived for one question only, HOW MANY LINES:
   the messages an audit needs when a client reports connection failures or
   stalls that the server supposedly never saw. derived_patterns gives a count
   for each, and a count of 0 is written out rather than left absent, because
   "no line matched" is the answer to the question.

     18456  login failed
     17806  SSPI handshake failed (integrated security)
     17809  maximum number of user connections reached
     17830  network error while establishing a connection, login timers included
     17187  not ready to accept new client connections
     17189  failed to spawn a thread for a new login or connection
       833  I/O requests taking longer than a threshold to complete
     17883  a worker appears non-yielding on its scheduler
     17884  new queries not picked up by a worker thread
     17888  all schedulers on a node appear deadlocked
     17890  a significant part of the process memory has been paged out
      9002  the transaction log of a database is full

   Each meaning was read from sys.messages, language 1033, before it went in.
   9002 is the outage a log that cannot grow ends in, and the only record of it
   once the moment has passed is this log: sys.databases says why a log waits
   now, never that it filled last week. It is severity 17 and logged in all
   twenty-two languages, so it is counted by its header line. Measured on SQL
   Server 2025 by filling a 4 MB log that could not grow under an open
   transaction: one header line and one message line naming the database and
   ACTIVE_TRANSACTION, and occurrences of 1. Its text there ends on the holdup
   LSN, which older versions do not write; the derivation reads the template
   of the instance it runs on, so either text is matched.
   17810 was considered and left out: it refuses a second DEDICATED ADMIN
   connection, which says nothing about an application's connections.

   The whole block cost 250 to 330 milliseconds for three numbers; with the
   fourteen above and the header template of 18052 it costs about two seconds
   on SQL Server 2025 (still two with 9002 added as a fifteenth), and the
   whole collector 2.7 against 1.5, against a
   @timeout of 300 seconds. The cost that has to be watched is the uniqueness
   check, which is one catalog scan per candidate fragment and is now a
   second of that; copying the retained language into #cat first is what
   keeps it there rather than scanning all twenty-two languages every time.
   =========================================================================== */

DECLARE @lang int = 1033;

DECLARE @wanted TABLE (message_id int PRIMARY KEY);
INSERT INTO @wanted (message_id) VALUES (9017), (3421), (17137),
    (18456), (17806), (17809), (17830), (17187), (17189),
    (833), (17883), (17884), (17888), (17890), (9002);

/* The template of the line the engine writes BEFORE an error it logs:
   "Error: 18456, Severity: 14, State: 8." in English, "Erreur : 18456,
   Gravité : 14, État : 8." in French. It is split with the others so that
   the two literals around its first parameter are known in the retained
   language, and that is all it is used for: it takes no part in the language
   vote and gets no row of its own in derived_patterns. See the note on 18456
   above derived_patterns for why it is needed. */
DECLARE @header int = 18052;

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

/* The numbers table is materialised. Written as a CTE it was rebuilt from
   the cross join of sys.all_objects for every template, and with fifteen
   templates in twenty-two languages the split alone took 2.5 seconds on
   SQL Server 2025; from a table it takes a quarter of one. */
CREATE TABLE #tally (n int PRIMARY KEY);
INSERT INTO #tally (n)
SELECT TOP (2048) CONVERT(int, ROW_NUMBER() OVER (ORDER BY (SELECT NULL)))
FROM sys.all_objects AS a CROSS JOIN sys.all_objects AS b;

WITH tpl AS (
    SELECT m.message_id, m.language_id,
           tmpl = CONVERT(nvarchar(2048), m.text) COLLATE Latin1_General_BIN2,
           tlen = CONVERT(int, DATALENGTH(m.text) / 2)
    FROM sys.messages AS m
    JOIN (SELECT message_id FROM @wanted UNION SELECT @header) AS w
      ON w.message_id = m.message_id),
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
    JOIN       #tally AS p ON p.n <= t.tlen
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
          WHERE DATALENGTH(g.frag) > 0 AND CHARINDEX(N'%', g.frag) = 0
            AND g.message_id <> @header) AS f
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
  AND f.message_id <> @header
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
                        refused nvarchar(200), severity int,
                        header_pattern nvarchar(4000));
INSERT INTO @derived (message_id, language_id, template, fragment, pattern,
                      catalog_matches, param_ordinal, lit_left, pos_left,
                      lit_right, pos_right, refused, severity, header_pattern)
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
       END,
       m.severity,
       /* Anchored at the start of the line, which is where the header template
          starts in all twenty-two languages, and closed by the literal that
          follows the number, so 18456 cannot match 184560. Only for a message
          above severity 10: an informational message is written as its
          sentence alone, with no header line before it. */
       CASE WHEN m.severity > 10 AND h.lit_right IS NOT NULL
            THEN CONVERT(nvarchar(4000),
                 REPLACE(REPLACE(REPLACE(REPLACE(h.lit_left,  N'\', N'\\'), N'%', N'\%'), N'_', N'\_'), N'[', N'\[')
                 + CONVERT(nvarchar(12), w.message_id)
                 + REPLACE(REPLACE(REPLACE(REPLACE(h.lit_right, N'\', N'\\'), N'%', N'\%'), N'_', N'\_'), N'[', N'\[')
                 + N'%') END
FROM @wanted AS w
LEFT JOIN sys.messages AS m ON m.message_id = w.message_id AND m.language_id = @lang
OUTER APPLY (SELECT n = COUNT(*) FROM #uniq AS u WHERE u.message_id = w.message_id) AS cand
OUTER APPLY (SELECT TOP (1) u.frag, u.esc, u.hits
             FROM #uniq AS u
             WHERE u.message_id = w.message_id AND u.hits = 1
             ORDER BY u.flen DESC, u.seq) AS best
LEFT JOIN @bounds AS b ON b.message_id = w.message_id AND b.ordinal = 1
LEFT JOIN @bounds AS h ON h.message_id = @header AND h.ordinal = 1;

/* notable reads these two, and the recovery set below reads the delimiters of
   3421. Parameter 3 of 3421 is the elapsed seconds in every language: the
   localised templates number their parameters explicitly, and the English one
   has them in that order. */
DECLARE @p9017 nvarchar(4000), @p3421 nvarchar(4000), @p17137 nvarchar(4000);
SELECT @p9017  = MAX(CASE WHEN message_id = 9017  THEN pattern END),
       @p3421  = MAX(CASE WHEN message_id = 3421  THEN pattern END),
       @p17137 = MAX(CASE WHEN message_id = 17137 THEN pattern END)
FROM @derived;

DECLARE @db_left nvarchar(2048), @db_right nvarchar(2048),
        @sec_left nvarchar(2048), @sec_right nvarchar(2048),
        @mnt_left nvarchar(2048), @mnt_right nvarchar(2048),
        @vlf_left nvarchar(2048), @vlf_right nvarchar(2048),
        @cnt_left nvarchar(2048), @cnt_right nvarchar(2048);
SELECT @db_left = lit_left, @db_right = lit_right
FROM @bounds WHERE message_id = 3421 AND ordinal = 1;
SELECT @sec_left = lit_left, @sec_right = lit_right
FROM @bounds WHERE message_id = 3421 AND ordinal = 3;
SELECT @mnt_left = lit_left, @mnt_right = lit_right
FROM @bounds WHERE message_id = 17137 AND ordinal = 1;
SELECT @vlf_left = lit_left, @vlf_right = lit_right
FROM @bounds WHERE message_id = 9017 AND ordinal = 1;
SELECT @cnt_left = lit_left, @cnt_right = lit_right
FROM @bounds WHERE message_id = 9017 AND ordinal = 2;

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
       @log_bytes                                                 AS [log_size_bytes],
       @size_limit                                                AS [log_size_limit_bytes],
       @skipped_for_size                                          AS [skipped_for_size],
       NULLIF(@size_msg, N'')                                     AS [size_error_message],
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
   avoir été montée avant le plus ancien fichier conservé. first_seen le dit.

   Both halves of this set go through the derivation of 17137, and they have to
   go together. The filter was LIKE 'Starting up database %' and the extraction
   took whatever sat between the first two apostrophes, which is a second
   English parser hidden inside a set whose filter was already one. Applying
   that extraction verbatim to the twenty-two templates of 17137 returns an
   EMPTY name in nine languages: cs, de, fi, hu, nl, no, pl, ru and sv. Five
   quote nothing at all, Hungarian and Polish use typographic quotes, Russian
   uses double quotes, and the Dutch template carries a single orphan
   apostrophe, De database %1! opstarten'. Repairing the filter alone would
   therefore fold every database of a German instance into one row with an
   empty name, which is a wrong answer where there used to be no answer at all.

   So the delimiters published by derived_patterns replace the apostrophes, the
   same way the recovery set below uses them for 3421. The left literal is empty
   in German, where the template is %1!-Datenbank wird gestartet and opens on
   its parameter, and an empty left literal means "from the first character"
   rather than "not found". Grouping is on the extracted NAME, so a row of this
   set means the same thing in every language. */
SELECT n.dbname                                                    AS [database],
       COUNT(*)                                                    AS [mounts],
       MIN(l.LogDate)                                              AS [first_seen],
       MAX(l.LogDate)                                              AS [last_seen]
FROM        #log AS l
CROSS APPLY (SELECT line = CONVERT(nvarchar(4000), RTRIM(l.Txt)) COLLATE Latin1_General_BIN2) AS t
CROSS APPLY (SELECT
        db_from = CASE WHEN DATALENGTH(@mnt_left) = 0 THEN 1
                       ELSE NULLIF(CHARINDEX(@mnt_left, t.line), 0) + DATALENGTH(@mnt_left) / 2 END) AS s
CROSS APPLY (SELECT s.db_from,
        db_to = CASE WHEN DATALENGTH(@mnt_right) = 0 THEN DATALENGTH(t.line) / 2 + 1
                     ELSE NULLIF(CHARINDEX(@mnt_right, t.line, s.db_from), 0) END) AS p
CROSS APPLY (SELECT dbname = LTRIM(RTRIM(REPLACE(REPLACE(
        CASE WHEN p.db_from > 0 AND p.db_to > p.db_from
             THEN SUBSTRING(t.line, p.db_from, p.db_to - p.db_from) END,
        NCHAR(13), N''), NCHAR(10), N'')))) AS n
WHERE @p17137 IS NOT NULL
  AND t.line LIKE @p17137 ESCAPE N'\'
GROUP BY n.dbname
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
   set is how that would be visible rather than silent, and it still is: 18456
   is in @wanted, gets no pattern, and its row says refused.

   It is COUNTED another way, which does not need a unique fragment. Every
   error above severity 10 that the engine logs is preceded by a line of its
   own carrying the number, message 18052, "Error: 18456, Severity: 14,
   State: 8." in English. That line is derived like everything else here: the
   two literals around its first parameter come from @bounds, in the retained
   language, and the number goes between them. The number is what makes the
   pattern unique, so no fragment of 18456 has to be. header_pattern and
   header_matches carry that route, for every wanted message above severity
   10. Measured on SQL Server 2025: five failed logins wrote five such lines
   and five "Login failed" lines, and 17137, at severity 10, wrote 93 lines
   and no header line at all.

   occurrences is the one number to read, and counted_by says which route
   gave it: the header line when the message has one, the fragment otherwise.
   It is 0 when the log was read and nothing matched, which is an answer, and
   NULL only when there is no answer: the log was not read (status says why,
   the size guard included), or no pattern could be built by either route. */
SELECT d.message_id                                                AS [message_id],
       d.language_id                                               AS [language_id],
       d.template                                                  AS [template],
       d.fragment                                                  AS [fragment],
       d.pattern                                                   AS [pattern],
       CONVERT(nchar(1), N'\')                                     AS [escape_char],
       d.catalog_matches                                           AS [catalog_matches],
       c.log_matches                                               AS [log_matches],
       d.severity                                                  AS [severity],
       d.header_pattern                                            AS [header_pattern],
       c.header_matches                                            AS [header_matches],
       COALESCE(c.header_matches, c.log_matches)                   AS [occurrences],
       CASE WHEN c.header_matches IS NOT NULL THEN 'error_header'
            WHEN c.log_matches    IS NOT NULL THEN 'fragment' END  AS [counted_by],
       d.param_ordinal                                             AS [param_ordinal],
       d.lit_left                                                  AS [left_literal],
       d.pos_left                                                  AS [left_literal_pos],
       d.lit_right                                                 AS [right_literal],
       d.pos_right                                                 AS [right_literal_pos],
       d.refused                                                   AS [refused]
FROM @derived AS d
/* A count of a log that was not read would be a 0 that means nothing, which
   is the one reading of 0 this set must not allow. */
CROSS APPLY (SELECT
       log_matches = CASE WHEN @collected = 0 OR d.pattern IS NULL THEN NULL ELSE
            (SELECT COUNT(*) FROM #log AS l
             WHERE l.Txt COLLATE Latin1_General_BIN2 LIKE d.pattern ESCAPE N'\') END,
       header_matches = CASE WHEN @collected = 0 OR d.header_pattern IS NULL THEN NULL ELSE
            (SELECT COUNT(*) FROM #log AS l
             WHERE l.Txt COLLATE Latin1_General_BIN2 LIKE d.header_pattern ESCAPE N'\') END) AS c
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

/* One row per engine warning about virtual log files, with the database it
   names and the count it reports.

   It exists because derived_patterns says only HOW MANY lines matched 9017,
   never which database each one is about, and the analysis layer needs the
   name: 9017 is the engine's own assessment and it is the first of the three
   criteria that decide whether a transaction log is worth rebuilding. Without
   this set the only honest answer was to escalate at the level of the whole
   instance and name every database whose virtual log files were measured,
   which reports as a finding databases the engine never complained about.
   That is the same shape of defect as an empty database name on a German
   mount, a wrong answer standing where there used to be no answer.

   The count is the second parameter, and it is taken as text as well as a
   number for the reason seconds_text exists on the recovery set: a locale
   that groups its digits must be visible rather than silently NULL.

   notable cannot serve instead. It carries the raw line, but behind a rarity
   filter and a two hundred row cap, so an instance that warns about thirty
   databases at every restart loses exactly the lines that matter most. */
SELECT l.LogDate                                                   AS [when],
       CASE WHEN p.db_from > 0 AND p.db_to > p.db_from
            THEN SUBSTRING(t.line, p.db_from, p.db_to - p.db_from) END
                                                                   AS [database],
       TRY_CONVERT(bigint, REPLACE(REPLACE(
            CASE WHEN p.cnt_from > 0 AND p.cnt_to > p.cnt_from
                 THEN SUBSTRING(t.line, p.cnt_from, p.cnt_to - p.cnt_from) END,
            N' ', N''), NCHAR(160), N''))                          AS [vlf_count],
       CASE WHEN p.cnt_from > 0 AND p.cnt_to > p.cnt_from
            THEN SUBSTRING(t.line, p.cnt_from, p.cnt_to - p.cnt_from) END
                                                                   AS [vlf_count_text]
FROM        #log AS l
CROSS APPLY (SELECT line = CONVERT(nvarchar(4000), RTRIM(l.Txt)) COLLATE Latin1_General_BIN2) AS t
CROSS APPLY (SELECT
        db_from = CASE WHEN DATALENGTH(@vlf_left) = 0 THEN 1
                       ELSE NULLIF(CHARINDEX(@vlf_left, t.line), 0) + DATALENGTH(@vlf_left) / 2 END,
        cnt_from = CASE WHEN DATALENGTH(@cnt_left) = 0 THEN 1
                        ELSE NULLIF(CHARINDEX(@cnt_left, t.line), 0) + DATALENGTH(@cnt_left) / 2 END) AS s
CROSS APPLY (SELECT s.db_from, s.cnt_from,
        db_to = CASE WHEN DATALENGTH(@vlf_right) = 0 THEN DATALENGTH(t.line) / 2 + 1
                     ELSE NULLIF(CHARINDEX(@vlf_right, t.line, s.db_from), 0) END,
        cnt_to = CASE WHEN DATALENGTH(@cnt_right) = 0 THEN DATALENGTH(t.line) / 2 + 1
                      ELSE NULLIF(CHARINDEX(@cnt_right, t.line, s.cnt_from), 0) END) AS p
WHERE @p9017 IS NOT NULL
  AND t.line LIKE @p9017 ESCAPE N'\'
ORDER BY l.LogDate
OPTION (RECOMPILE, MAXDOP 1);
