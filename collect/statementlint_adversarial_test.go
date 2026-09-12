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
	}
	for _, c := range mustAccept {
		if msg := statementLint(c.sql); msg != "" {
			t.Errorf("%s: refused, and it must not be — %s: %s", c.name, c.sql, msg)
		}
	}
}
