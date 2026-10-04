package collect

import "testing"

// The adversarial table.
//
// statementLint has now been walked past twice by two separate reviews — the
// concatenation hole on 4 September 2026, and the foreach procedures, the
// writing procedures and DBCC SQLPERF(CLEAR) on 12 September. Each time the
// fix was a better regex, and each time the regexes looked complete before the
// review. So the protection that outlives the next set of patterns is this
// table rather than the patterns themselves: every entry is a statement that
// must never reach an audited instance, and an entry that starts passing is a
// hole reopened.
//
// Add to it whenever a review finds another way through, and prefer a
// statement somebody would plausibly paste into a corpus directory over one
// invented to defeat a rule. The accident is what this lint stops; it says so
// itself, and an author it cannot stop is out of scope by construction.
func TestStatementLintRefusesTheAdversarialTable(t *testing.T) {
	mustRefuse := []struct{ name, sql string }{
		// Execution vehicles. The payload is identical in all four; only the
		// vehicle differs, and only two of them were recognised before.
		{"exec of a literal", `EXEC('DROP TABLE dbo.Orders')`},
		{"exec of a concatenation", `EXEC('DR' + 'OP DATABASE victim')`},
		{"sp_executesql", `EXEC sp_executesql N'DROP TABLE dbo.Orders'`},
		{"sp_msforeachdb", `EXEC sp_msforeachdb 'DROP TABLE dbo.Orders'`},
		{"sp_msforeachdb taking every database offline", `EXEC sp_msforeachdb 'ALTER DATABASE [?] SET OFFLINE WITH ROLLBACK IMMEDIATE'`},
		{"sp_msforeachtable", `EXEC sp_msforeachtable 'TRUNCATE TABLE ?'`},
		{"sp_MSforeach_worker", `EXEC sp_MSforeach_worker 'DROP TABLE dbo.Orders'`},

		// Writes with no keyword any other rule looks for.
		{"sp_updatestats", `EXEC sp_updatestats`},
		{"sp_recompile", `EXEC sp_recompile 'dbo.Orders'`},
		{"sp_cycle_errorlog", `EXEC sp_cycle_errorlog`},
		{"sp_trace_setstatus", `EXEC sp_trace_setstatus 2, 0`},
		{"checkpoint", `CHECKPOINT`},

		// The one allowlisted DBCC command whose argument decides.
		{"sqlperf clearing wait stats", `DBCC SQLPERF('sys.dm_os_wait_stats', CLEAR)`},
		{"sqlperf clearing latch stats", `DBCC SQLPERF('sys.dm_os_latch_stats', CLEAR)`},
		{"sqlperf clearing, unicode and no_infomsgs", `DBCC SQLPERF (N'sys.dm_os_wait_stats', CLEAR) WITH NO_INFOMSGS`},

		// The rules that were already here, kept so a rewrite cannot lose them.
		{"kill", `KILL 53`},
		{"drop database", `DROP DATABASE victim`},
		{"alter database", `ALTER DATABASE x SET OFFLINE`},
		{"dbcc freeproccache", `DBCC FREEPROCCACHE`},
		{"execute as", `EXECUTE AS LOGIN = 'sa'; SELECT 1`},
		{"xp_cmdshell", `EXEC xp_cmdshell 'del /q C:\data\*'`},
		{"select into a permanent table", `SELECT * INTO dbo.Copy FROM sys.databases`},

		// The harm review of 27 September 2026. Twenty writing statements that
		// carried no keyword the rules looked for, and five ways of calling a
		// procedure without a name the lint could read. Every one was accepted.
		{"disable server triggers", `DISABLE TRIGGER ALL ON ALL SERVER`},
		{"disable table triggers", `DISABLE TRIGGER ALL ON dbo.Orders`},
		{"enable a database trigger", `ENABLE TRIGGER trg ON DATABASE`},
		{"receive dequeues messages", `RECEIVE TOP (1000) * FROM dbo.TargetQueue`},
		{"end conversation with cleanup", `END CONVERSATION @h WITH CLEANUP`},
		{"add signature", `ADD SIGNATURE TO dbo.p BY CERTIFICATE c`},
		// The same verbs after an ordinary statement, where the first-word rule
		// does not see them and only the keyword list does.
		{"disable server triggers mid-batch", `SET NOCOUNT ON; DISABLE TRIGGER ALL ON ALL SERVER`},
		{"receive mid-batch", `SET NOCOUNT ON; RECEIVE TOP (1) * FROM dbo.TargetQueue`},
		{"add signature mid-batch", `SELECT 1; ADD SIGNATURE TO dbo.p BY CERTIFICATE c`},
		{"enable trigger mid-batch", `SELECT 1; ENABLE TRIGGER trg ON DATABASE`},
		{"purge backup history", `EXEC msdb.dbo.sp_delete_backuphistory @oldest_date = '2030-01-01'`},
		{"purge one database's backup history", `EXEC msdb.dbo.sp_delete_database_backuphistory 'SALESDB'`},
		{"purge job history", `EXEC msdb.dbo.sp_purge_jobhistory`},
		{"purge mail items", `EXEC msdb.dbo.sysmail_delete_mailitems_sp @sent_before = '2030-01-01'`},
		{"drop every plan guide", `EXEC sp_control_plan_guide N'DROP ALL'`},
		{"remove a query store query", `EXEC sp_query_store_remove_query 42`},
		{"unforce a plan", `EXEC sp_query_store_unforce_plan 42, 7`},
		{"reset query store stats", `EXEC sp_query_store_reset_exec_stats 7`},
		{"rename a table", `EXEC sp_rename 'dbo.Orders', 'Orders_old'`},
		{"startup procedure", `EXEC sp_procoption 'dbo.p', 'startup', 'on'`},
		{"create statistics", `EXEC sp_createstats`},
		{"turn auto stats off", `EXEC sp_autostats 'dbo.Orders', 'OFF'`},
		{"drop a user", `EXEC sp_dropuser 'x'`},
		{"firewall rule", `EXEC sp_set_database_firewall_rule N'x', '0.0.0.0', '255.255.255.255'`},
		{"procedure opening a batch run by sp_executesql", `EXEC sp_executesql N'msdb.dbo.sp_purge_jobhistory'`},
		// codex review, 27 September 2026.
		{"user procedure named like a sys one", `EXEC dbo.sp_readerrorlog`},
		{"user procedure named like the error log sizes", `EXEC dbo.sp_enumerrorlogs`},
		{"msdb procedure unqualified", `EXEC sp_help_jobhistory`},
		{"permanent SELECT INTO beside a temp INSERT INTO", `SELECT name INTO dbo.AuditCopy FROM sys.databases; INSERT INTO #scratch SELECT 1`},
		{"sequence advanced by a SELECT", `SELECT NEXT VALUE FOR dbo.audit_sequence AS value`},
		{"procedure opening a batch run by EXEC()", `EXEC('sp_delete_backuphistory ''2030-01-01''')`},
		{"procedure called with a return code", `EXEC @rc = msdb.dbo.sp_purge_jobhistory`},
		{"procedure named by a variable", `DECLARE @p sysname = N'sp_purge_jobhistory'; EXEC @p`},
		{"bracketed three-part name", `EXECUTE [msdb].[dbo].[sp_purge_jobhistory]`},
	}
	for _, c := range mustRefuse {
		if msg := statementLint(c.sql); msg == "" {
			t.Errorf("%s: accepted, and it must not be — %s", c.name, c.sql)
		}
	}

	// The other half of the test. A lint that refuses everything protects
	// nothing, and each of these is a shape the corpus actually needs: the
	// read-only DBCC commands it runs, scratch that dies with the connection,
	// and the word "checkpoint" inside the identifiers the corpus selects.
	mustAccept := []struct{ name, sql string }{
		{"plain select", `SELECT 1`},
		{"dbcc tracestatus", `DBCC TRACESTATUS(-1)`},
		{"dbcc sqlperf logspace", `DBCC SQLPERF(LOGSPACE)`},
		{"dbcc show_statistics", `DBCC SHOW_STATISTICS('dbo.T', 'IX_T')`},
		{"temp table scratch", `CREATE TABLE #t (a int); INSERT INTO #t SELECT 1; SELECT * FROM #t; DROP TABLE #t`},
		{"table variable scratch", `DECLARE @t TABLE (a int); INSERT INTO @t SELECT 1; SELECT * FROM @t`},
		{"select into a temp table", `SELECT * INTO #t FROM sys.databases`},
		{"checkpoint inside an identifier", `SELECT ls.log_since_last_checkpoint_mb, ls.log_checkpoint_lsn FROM sys.dm_db_log_stats(1) ls`},
		// The five procedures the shipped corpus calls, under the spellings it
		// uses. Turning the list round must not refuse them.
		{"error log", `INSERT INTO #log EXEC sys.sp_readerrorlog 0`},
		{"error log sizes", `INSERT INTO #logs (archive, log_date, size_bytes) EXEC sys.sp_enumerrorlogs`},
		{"job history", `INSERT INTO @h EXEC msdb.dbo.sp_help_jobhistory @mode = 'FULL'`},
		{"compression estimate", `INSERT INTO #s EXEC sys.sp_estimate_data_compression_savings @schema_name = N'dbo', @object_name = N'T', @index_id = NULL, @partition_number = NULL, @data_compression = N'PAGE'`},
		{"guarded dynamic select", `EXEC sp_executesql N'SELECT name FROM sys.databases WHERE database_id = @id', N'@id int', @id = 1`},
		{"columns named like the new keywords", `SELECT s.enable_broker, s.is_disabled, q.send_count FROM sys.databases s CROSS JOIN (SELECT 0 AS send_count) q`},
	}
	for _, c := range mustAccept {
		if msg := statementLint(c.sql); msg != "" {
			t.Errorf("%s: refused, and it must not be — %s: %s", c.name, c.sql, msg)
		}
	}
}

// Comments inside dynamic SQL, found by the harm review of 4 October 2026.
//
// The top level of a file reaches statementLint with its comments already
// stripped by lint, but the text of an executed literal did not: it was
// blanked and scanned with its comments in place. Every rule that reads a
// word followed by a separator, or the first word of a batch, then read the
// comment instead. Each statement below was measured on SQL Server 2025 with a
// harmless procedure in its place, and each runs: the server treats a comment
// as whitespace wherever whitespace may stand, nested block comments included,
// and between the parts of a multipart name.
func TestStatementLintSeesThroughCommentsInDynamicSQL(t *testing.T) {
	mustRefuse := []struct{ name, sql string }{
		{"block comment opening an EXEC() batch", `EXEC ('/* bypass */ msdb.dbo.sp_delete_backuphistory ''2020-01-01''')`},
		{"line comment opening an EXEC() batch", "EXEC ('-- bypass\nmsdb.dbo.sp_delete_backuphistory ''2020-01-01''')"},
		{"block comment opening an sp_executesql batch", `EXEC sp_executesql N'/* bypass */ msdb.dbo.sp_purge_jobhistory'`},
		{"line comment opening an sp_executesql batch", "EXEC sp_executesql N'-- bypass\nmsdb.dbo.sp_purge_jobhistory'"},
		{"block comment two levels down", `EXEC ('EXEC sp_executesql N''/* bypass */ msdb.dbo.sp_purge_jobhistory''')`},
		{"line comment two levels down", "EXEC ('EXEC sp_executesql N''-- bypass\nmsdb.dbo.sp_purge_jobhistory''')"},
		{"nested block comment", `EXEC ('/* a /* b */ c */ msdb.dbo.sp_purge_jobhistory')`},
		{"apostrophe in the comment", `EXEC ('/* it''s */ msdb.dbo.sp_purge_jobhistory')`},
		{"comment after the database", `EXEC ('msdb./**/dbo.sp_purge_jobhistory')`},
		{"comment after the schema", `EXEC ('msdb.dbo./**/sp_purge_jobhistory')`},
		{"comment between EXEC and the name", `EXEC ('EXEC/**/msdb.dbo.sp_purge_jobhistory')`},
		{"comment after a return code", `EXEC ('DECLARE @r int; EXEC @r = /**/ msdb.dbo.sp_purge_jobhistory')`},
		{"comment between EXEC and its parenthesis", `EXEC ('EXEC/**/(''DROP TABLE dbo.Orders'')')`},
		{"comment between sp_executesql and its literal", `EXEC ('EXEC sp_executesql/**/N''DROP TABLE dbo.Orders''')`},
		{"comment splitting EXECUTE AS", `EXEC ('EXECUTE/**/AS LOGIN = ''sa''; SELECT 1')`},
		{"comment splitting END CONVERSATION", `EXEC ('END/**/CONVERSATION @h WITH CLEANUP')`},
		{"comment splitting NEXT VALUE FOR", `EXEC ('SELECT NEXT/**/VALUE/**/FOR dbo.audit_sequence')`},
		// The same forms at the top level, which lint strips before calling;
		// statementLint must not depend on its caller having done so.
		{"comment after the database, top level", `EXEC msdb./**/dbo.sp_purge_jobhistory`},
		{"comment after the schema, top level", `EXEC msdb.dbo./**/sp_purge_jobhistory`},
		{"comment after a return code, top level", `DECLARE @r int; EXEC @r = /**/ msdb.dbo.sp_purge_jobhistory`},
		{"comment opening the batch, top level", `/* c */ msdb.dbo.sp_purge_jobhistory`},
		// A carriage return on its own ends a line comment on the server, and
		// classifySQL ended it only at \n, so the code after it was stripped as
		// comment, at the top level as much as in dynamic SQL.
		{"line comment ended by a bare CR, top level", "SELECT 1 AS a -- note\rEXEC msdb.dbo.sp_purge_jobhistory"},
		{"line comment ended by a bare CR, dynamic", "EXEC ('-- note\rmsdb.dbo.sp_purge_jobhistory')"},
	}
	for _, c := range mustRefuse {
		if msg := statementLint(c.sql); msg == "" {
			t.Errorf("%s: accepted, and it must not be — %s", c.name, c.sql)
		}
	}

	// Prose in dynamic SQL is prose, as it is at the top level: a guard that
	// explains what it does not do must not be refused for saying so.
	mustAccept := []struct{ name, sql string }{
		{"comment naming a forbidden word", `EXEC sp_executesql N'/* no ALTER, no sp_purge_jobhistory */ SELECT 1'`},
		{"line comment before a select", "EXEC ('-- reads only\nSELECT name FROM sys.databases')"},
	}
	for _, c := range mustAccept {
		if msg := statementLint(c.sql); msg != "" {
			t.Errorf("%s: refused, and it must not be — %s: %s", c.name, c.sql, msg)
		}
	}
}

// Separators the server accepts and a regular expression's \s does not, found
// beside the comments above. Measured on SQL Server 2025: a control character,
// a no-break space, any Unicode space and the zero-width space all separate
// tokens, so EXECUTE<U+00A0>AS LOGIN runs as EXECUTE AS LOGIN. A no-break space
// is what a statement copied out of a web page or a word processor carries,
// which makes this the accident the lint exists for rather than a contrivance.
func TestStatementLintSeesThroughUnusualWhitespace(t *testing.T) {
	mustRefuse := []struct{ name, sql string }{
		{"no-break space in EXECUTE AS", "EXECUTE AS LOGIN = 'sa'; SELECT 1"},
		{"no-break space after EXEC", "EXEC msdb.dbo.sp_purge_jobhistory"},
		{"vertical tab after EXEC", "EXEC\vmsdb.dbo.sp_purge_jobhistory"},
		{"ideographic space after EXEC", "EXEC　msdb.dbo.sp_purge_jobhistory"},
		{"zero-width space opening a dynamic batch", "EXEC ('​msdb.dbo.sp_purge_jobhistory')"},
		{"no-break space opening a dynamic batch", "EXEC (' msdb.dbo.sp_purge_jobhistory')"},
		{"control character opening a dynamic batch", "EXEC ('\x01msdb.dbo.sp_purge_jobhistory')"},
		{"no-break space in END CONVERSATION", "END CONVERSATION @h WITH CLEANUP"},
	}
	for _, c := range mustRefuse {
		if msg := statementLint(c.sql); msg == "" {
			t.Errorf("%s: accepted, and it must not be — %q", c.name, c.sql)
		}
	}
	mustAccept := []struct{ name, sql string }{
		{"no-break space in a select", "SELECT name FROM sys.databases"},
		{"non-ASCII text in a selected literal", "SELECT N'quantité : 3' AS label"},
	}
	for _, c := range mustAccept {
		if msg := statementLint(c.sql); msg != "" {
			t.Errorf("%s: refused, and it must not be — %q: %s", c.name, c.sql, msg)
		}
	}
}
