package collect

import (
	"os"
	"strings"
	"testing"
)

// The corpus this program ships is the only one whose read-only nature the
// manifest is allowed to attest, so it has to pass the rule that decides that.
// A collector added with a destructive statement in it fails here rather than
// on a client's instance.
func TestStatementLintPassesTheEmbeddedCorpus(t *testing.T) {
	scripts, err := Discover(os.DirFS(".."), "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(scripts) == 0 {
		t.Fatal("no scripts found; the test is not testing anything")
	}
	for _, s := range scripts {
		if msg := statementLint(StripSQLComments(s.SQL)); msg != "" {
			t.Errorf("%s: statementLint refused a shipped collector: %s", s.Path, msg)
		}
	}
}

func TestStatementLintRefusesWhatChangesTheServer(t *testing.T) {
	for _, tc := range []struct {
		name, sql, want string
	}{
		{"drop database", "DROP DATABASE SALESDB;", "DROP"},
		{"alter", "ALTER DATABASE SALESDB SET SINGLE_USER;", "ALTER"},
		{"permanent table", "CREATE TABLE dbo.audit (a int);", "CREATE"},
		{"delete from a real table", "DELETE FROM dbo.orders;", "DELETE"},
		{"update a real table", "UPDATE dbo.orders SET total = 0;", "UPDATE"},
		{"insert into a real table", "INSERT INTO dbo.orders (a) VALUES (1);", "INSERT"},
		{"truncate", "TRUNCATE TABLE dbo.orders;", "TRUNCATE"},
		{"select into a permanent table", "SELECT * INTO dbo.copy FROM sys.objects;", "INTO"},
		{"grant", "GRANT CONTROL SERVER TO AUDIT_RO;", "GRANT"},
		{"kill", "KILL 53;", "KILL"},
		{"backup", "BACKUP DATABASE SALESDB TO DISK = 'x';", "BACKUP"},
		{"restore", "RESTORE DATABASE SALESDB FROM DISK = 'x';", "RESTORE"},
		{"reconfigure", "RECONFIGURE;", "RECONFIGURE"},
		{"shutdown", "SHUTDOWN;", "SHUTDOWN"},
		{"a DBCC that is not a read", "DBCC FREEPROCCACHE;", "FREEPROCCACHE"},
		{"impersonation", "EXECUTE AS LOGIN = 'sa';", "EXECUTE AS"},
		{"an extended procedure", "EXEC sys.xp_cmdshell 'del *.*';", "xp_cmdshell"},
		{"reconfiguring the instance", "EXEC sys.sp_configure 'xp_cmdshell', 1;", "sp_configure"},
		{"reading a foreign source", "SELECT * FROM OPENROWSET('x', 'y', 'z');", "OPENROWSET"},
		{"destruction inside dynamic SQL", "EXEC sys.sp_executesql N'DROP DATABASE SALESDB';", "DROP"},
		{"destruction two literals deep",
			"EXEC sys.sp_executesql N'EXEC sys.sp_executesql N''DROP DATABASE SALESDB''';", "DROP"},
		{"dynamic SQL built in a variable", "DECLARE @s nvarchar(max); EXEC (@s);", "assembled"},
		{"sp_executesql given a variable", "DECLARE @s nvarchar(max); EXEC sys.sp_executesql @s;", "assembled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := statementLint(tc.sql)
			if msg == "" {
				t.Fatalf("statementLint(%q) allowed it", tc.sql)
			}
			if !strings.Contains(msg, tc.want) {
				t.Errorf("statementLint(%q) = %q, want a message naming %q", tc.sql, msg, tc.want)
			}
		})
	}
}

func TestStatementLintAllowsWhatACollectorLegitimatelyDoes(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"a plain read", "SELECT name FROM sys.databases;"},
		{"a session temp table", "CREATE TABLE #log (a int); INSERT INTO #log (a) VALUES (1); DROP TABLE #log;"},
		{"a table variable", "DECLARE @t TABLE (a int); INSERT INTO @t (a) SELECT 1;"},
		{"select into a temp table", "SELECT name INTO #t FROM sys.objects;"},
		{"fetch into variables", "FETCH NEXT FROM c INTO @s, @o;"},
		{"a read-only DBCC", "INSERT INTO @tf EXEC ('DBCC TRACESTATUS(-1) WITH NO_INFOMSGS');"},
		{"the guard pattern", "INSERT INTO @c EXEC sys.sp_executesql N'SELECT 1 AS x';"},
		{"a parameterised guard", "EXEC sys.sp_executesql N'SELECT @d', N'@d int', @d = 7;"},
		// The four words below appear in this corpus inside literals that are
		// never executed — a CASE arm spelling out what a suspect-page event
		// means, a publication type. Text a collector selects is data, and
		// linting it as code would refuse files that do nothing at all.
		{"a keyword in prose that is selected, not run",
			"SELECT CASE WHEN 1 = 1 THEN 'DBCC fixed the page' ELSE 'restore attempted' END AS x;"},
		{"a keyword in a comparison", "SELECT 1 WHERE p.type = 'merge' AND q.name LIKE '%Update Snapshot%';"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if msg := statementLint(tc.sql); msg != "" {
				t.Errorf("statementLint(%q) refused it: %s", tc.sql, msg)
			}
		})
	}
}

// The point of the whole rule: a foreign corpus supplied with --queries-dir is
// linted before it is run, so a maintenance script dropped into the directory
// is refused rather than executed with the login's privileges.
func TestLintRefusesADestructiveFileThatMeetsTheContract(t *testing.T) {
	sql := contractPreamble + "DROP DATABASE SALESDB;\nSELECT 1 AS x OPTION (RECOMPILE, MAXDOP 1);"
	msg := lint(sql, []ResultSpec{{Name: "root", Shape: ShapeObject}})
	if !strings.Contains(msg, "DROP") {
		t.Errorf("lint = %q, want a refusal naming DROP", msg)
	}
}
