package collect

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// text, text_tail and text_length against a real store, through runUnit.
// Skipped unless SQL_AUDITOR_LIVE_SERVER is set (liveConfig):
//
//	SQL_AUDITOR_LIVE_SERVER=localhost,1433 SQL_AUDITOR_LIVE_USER=sa \
//	SQL_AUDITOR_LIVE_PASSWORD=... go test ./collect/ -run '^TestLiveQueryStoreTextTail' -v
//
// 028 is the collector the tail was added for, 020 the one that defines the
// fields, and 023 and 024 are listings that group, where the tail goes
// through MAX. 025 and 026 are held by the corpus test only: one needs its
// flag's two windows, the other an aborted execution. The database is created under
// the ZzFixA prefix and dropped by t.Cleanup.

func corpusFileScript(t *testing.T, rel string) Script {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "queries", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	s := parseScript(rel, string(b))
	if s.LintError != "" {
		t.Fatalf("%s does not lint: %s", rel, s.LintError)
	}
	return s
}

// wideInsert is an INSERT ... SELECT over the first n columns, the shape of
// the extraction queries the tail was added for: the FROM clause comes after
// the column lists. alias marks the statement. No semicolon: the store keeps
// the statement without it, and the lengths below are compared with the store.
func wideInsert(n int, alias string) string {
	cols := make([]string, n)
	for i := range cols {
		cols[i] = fmt.Sprintf("column_%03d", i+1)
	}
	list := strings.Join(cols, ", ")
	sel := make([]string, n)
	for i, c := range cols {
		sel[i] = alias + "." + c
	}
	return "INSERT INTO dbo.ZzFixAWideCopy (" + list + ") SELECT " + strings.Join(sel, ", ") +
		" FROM dbo.ZzFixAWide AS " + alias
}

func TestLiveQueryStoreTextTail(t *testing.T) {
	cfg, admin := pcAdmin(t)
	name := "ZzFixATextTail" + pcSuffix()
	pcCreateDatabase(t, admin, name, "", false)
	w := pcOpen(t, cfg, name)
	defs := make([]string, 60)
	for i := range defs {
		defs[i] = fmt.Sprintf("column_%03d int NOT NULL DEFAULT 1", i+1)
	}
	pcExec(t, w, "CREATE TABLE dbo.ZzFixAWide ("+strings.Join(defs, ", ")+");")
	pcExec(t, w, "CREATE TABLE dbo.ZzFixAWideCopy ("+strings.Join(defs, ", ")+");")
	pcExec(t, w, "INSERT INTO dbo.ZzFixAWide DEFAULT VALUES;")

	stmts := map[string]string{
		"zz_long":   wideInsert(60, "zz_long"), // past 1 000: the middle is cut
		"zz_medium": wideInsert(16, "zz_mid"),  // between 500 and 1 000: whole across text and text_tail
		"zz_short":  wideInsert(2, "zz_short"), // fits in text
	}
	for k, s := range stmts {
		t.Logf("%s: %d characters", k, len(s))
	}
	if n := len(stmts["zz_long"]); n <= 1000 {
		t.Fatalf("the long statement is %d characters, want more than 1 000", n)
	}
	if n := len(stmts["zz_medium"]); n <= 500 || n > 1000 {
		t.Fatalf("the medium statement is %d characters, want 501 to 1 000", n)
	}
	if n := len(stmts["zz_short"]); n > 500 {
		t.Fatalf("the short statement is %d characters, want at most 500", n)
	}
	// A store just switched on does not capture at once: on the shared lab
	// instance it held nothing of this database for up to 2 min 20 s after the
	// ALTER, then everything run after that. So the statements are run again
	// until the store holds all three.
	began := time.Now()
	for {
		for _, s := range stmts {
			pcExec(t, w, s)
		}
		pcExec(t, w, "EXEC sys.sp_query_store_flush_db;")
		var n int
		if err := w.QueryRow("SELECT COUNT(*) FROM sys.query_store_query_text " +
			"WHERE query_sql_text LIKE N'INSERT INTO dbo.ZzFixAWideCopy%'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n >= len(stmts) {
			t.Logf("captured after %v", time.Since(began).Round(time.Second))
			break
		}
		if time.Since(began) > 5*time.Minute {
			var state string
			_ = w.QueryRow("SELECT actual_state_desc + N', readonly_reason ' + " +
				"CONVERT(nvarchar(12), readonly_reason) FROM sys.database_query_store_options").Scan(&state)
			t.Fatalf("the store holds %d of the %d statements after 5 minutes (%s)", n, len(stmts), state)
		}
		time.Sleep(2 * time.Second)
	}

	for _, c := range []struct{ rel, array string }{
		{"80.workload/028.query-store-resources.sql", "by_query"},
		{"80.workload/023.query-store-most-executed.sql", "by_query"},
		{"80.workload/020.query-store.sql", "top_queries"},
		{"80.workload/024.query-store-rowcount.sql", "by_query"},
	} {
		t.Run(c.rel, func(t *testing.T) {
			_, doc := pcRun(t, cfg, corpusFileScript(t, c.rel), name)
			rows, _ := doc[c.array].([]any)
			found := map[string]map[string]any{}
			for _, r := range rows {
				row, _ := r.(map[string]any)
				head, _ := row["text"].(string)
				if strings.HasPrefix(head, "INSERT INTO dbo.ZzFixAWideCopy") {
					for k, s := range stmts {
						if head == s[:min(500, len(s))] {
							found[k] = row
						}
					}
				}
			}
			for k, s := range stmts {
				row, ok := found[k]
				if !ok {
					t.Errorf("%s: no row whose text is the first 500 characters of %s", c.rel, k)
					continue
				}
				if l, _ := row["text_length"].(float64); int(l) != len(s) {
					t.Errorf("%s %s: text_length = %v, want %d", c.rel, k, row["text_length"], len(s))
				}
				tail, present := row["text_tail"]
				var want any
				switch {
				case len(s) > 1000:
					want = s[len(s)-500:]
				case len(s) > 500:
					want = s[500:]
				}
				if want == nil {
					if tail != nil {
						t.Errorf("%s %s: text_tail = %q, want null or absent", c.rel, k, tail)
					}
					continue
				}
				if !present || tail != want {
					t.Errorf("%s %s: text_tail = %q,\nwant %q", c.rel, k, tail, want)
				}
				if s, _ := tail.(string); !strings.Contains(s, "FROM dbo.ZzFixAWide AS ") {
					t.Errorf("%s %s: the FROM clause is not in text_tail", c.rel, k)
				}
			}
		})
	}
}
