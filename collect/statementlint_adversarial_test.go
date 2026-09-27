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
		// The four procedures the shipped corpus calls, under the spellings it
		// uses. Turning the list round must not refuse them.
		{"error log", `INSERT INTO #log EXEC sys.sp_readerrorlog 0`},
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
