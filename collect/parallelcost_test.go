package collect

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// parallelCostRel is 80.workload/043's path in the corpus. The live tests
// read the file from the tree, not from the embedded corpus, because the
// collect package cannot import the root package that embeds it.
const parallelCostRel = "80.workload/043.query-store-parallel-cost.sql"

// parallelCostScript reads 043 and rewrites the constants named in set, as a
// --queries-dir corpus would, then parses and lints the result. A constant
// must be declared exactly once, on a line of its own, as
// DECLARE @name int = N; or DECLARE @name bigint = N;. A rewrite that matched
// nothing would leave the test running the shipped value and calling it the
// rewritten one, so that is a fatal error here.
func parallelCostScript(t *testing.T, set map[string]int64) Script {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "queries", filepath.FromSlash(parallelCostRel)))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	for name, v := range set {
		re := regexp.MustCompile(`(?m)^DECLARE ` + regexp.QuoteMeta(name) + ` (int|bigint) = \d+;`)
		if n := len(re.FindAllString(sql, -1)); n != 1 {
			t.Fatalf("043 declares %s %d times as DECLARE %s int|bigint = N; on a line of its own, want 1", name, n, name)
		}
		sql = re.ReplaceAllString(sql, "DECLARE "+name+" ${1} = "+strconv.FormatInt(v, 10)+";")
	}
	s := parseScript(parallelCostRel, sql)
	if s.LintError != "" {
		t.Fatalf("043 does not lint after rewriting %v: %s", set, s.LintError)
	}
	return s
}

func TestParallelCostConstantsAreRewrittenOnce(t *testing.T) {
	s := parallelCostScript(t, map[string]int64{
		"@window_days": 3, "@cap": 2, "@chunk": 2, "@budget_bytes": 1, "@budget_ms": 0,
	})
	for _, want := range []string{
		"DECLARE @window_days int = 3;", "DECLARE @cap int = 2;", "DECLARE @chunk int = 2;",
		"DECLARE @budget_bytes bigint = 1;", "DECLARE @budget_ms int = 0;",
	} {
		if n := strings.Count(s.SQL, want); n != 1 {
			t.Errorf("the rewritten 043 holds %q %d times, want 1", want, n)
		}
	}
}
