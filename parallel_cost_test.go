package sqlauditor_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	sqlauditor "github.com/rudi-bruchez/sql-auditor"
	"github.com/rudi-bruchez/sql-auditor/collect"
)

// 80.workload/043.query-store-parallel-cost.sql, the cost bands of 042 read
// from the Query Store. docs/query-store-parallel-cost-spec.md is its design.

const parallelCostPath = "queries/80.workload/043.query-store-parallel-cost.sql"

// The five directive lines the spec gives 043. The collector runs by default
// and names nothing, so a @requires_flag, @discloses or @profiles line is a
// change of contract and fails here rather than passing as a new line.
var parallelCostDirectives = []string{
	"-- @scope:       database",
	"-- @resultsets:  root:object, bands:array",
	"-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE",
	"-- @timeout:     120",
	"-- @min_version: 13",
}

// corpusScript returns the embedded collector at path, as Discover reads it.
func corpusScript(t *testing.T, path string) collect.Script {
	t.Helper()
	scripts, err := collect.Discover(sqlauditor.Queries, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, s := range scripts {
		if "queries/"+s.Path == path {
			return s
		}
	}
	t.Fatalf("%s is not in the corpus", path)
	return collect.Script{}
}

func TestQueryStoreParallelCostDeclaresItsContract(t *testing.T) {
	s := corpusScript(t, parallelCostPath)
	var got []string
	for _, line := range strings.Split(s.SQL, "\n") {
		if strings.HasPrefix(line, "-- @") {
			got = append(got, strings.TrimRight(line, "\r"))
		}
	}
	if !slices.Equal(got, parallelCostDirectives) {
		t.Errorf("043's directive lines are\n%s\nwant\n%s",
			strings.Join(got, "\n"), strings.Join(parallelCostDirectives, "\n"))
	}
	if s.LintError != "" {
		t.Errorf("043 does not lint: %s", s.LintError)
	}
	if s.Scope != collect.ScopeDatabase || s.TimeoutSec != 120 || !slices.Equal(s.MinVersion, []int{13}) {
		t.Errorf("043 parsed as scope %v, @timeout %d, @min_version %v", s.Scope, s.TimeoutSec, s.MinVersion)
	}
	if s.RequiresFlag != "" || len(s.Discloses) != 0 || len(s.Profiles) != 0 || s.Widened != "" || s.Writer != "" {
		t.Errorf("043 runs by default, discloses nothing and widens nothing; got flag %q, discloses %v, profiles %v, widened %q, writer %q",
			s.RequiresFlag, s.Discloses, s.Profiles, s.Widened, s.Writer)
	}
	// The permissions are 029's, which reads the same views and sys.objects
	// for the same classes of nested query: two files, one line.
	if ref := corpusScript(t, "queries/80.workload/029.query-store-load-profile.sql"); !slices.Equal(s.Permissions, ref.Permissions) {
		t.Errorf("043 asks for %v, 029 for %v; the spec gives 043 029's permissions", s.Permissions, ref.Permissions)
	}
}

// Every form the spec names under "The collector" as one the contract lint
// must refuse in 043. Each mutation finds its anchor exactly once: a rewrite
// of the file that moves an anchor fails here by name, instead of passing on a
// text the mutation never touched.
func TestQueryStoreParallelCostLintRefusesItsMutations(t *testing.T) {
	raw := corpusScript(t, parallelCostPath).SQL
	lintOf := func(t *testing.T, text string) string {
		t.Helper()
		scripts, err := collect.Discover(fstest.MapFS{parallelCostPath: {Data: []byte(text)}}, "queries")
		if err != nil || len(scripts) != 1 {
			t.Fatalf("Discover: %v, %d scripts", err, len(scripts))
		}
		return scripts[0].LintError
	}
	if msg := lintOf(t, raw); msg != "" {
		t.Fatalf("043 as shipped does not lint: %s", msg)
	}
	for _, m := range []struct{ name, old, new, want string }{
		{"the DELETE of the extra pinned row without its hint",
			"DELETE FROM @pinned WHERE rn > @cap OPTION (RECOMPILE, MAXDOP 1);",
			"DELETE FROM @pinned WHERE rn > @cap;",
			"needs OPTION (RECOMPILE, MAXDOP 1)"},
		{"the pin without its hint",
			"r.plan_id) AS t OPTION (RECOMPILE, MAXDOP 1);",
			"r.plan_id) AS t;",
			"needs OPTION (RECOMPILE, MAXDOP 1)"},
		{"the aggregation without its hint",
			"GROUP BY rs.plan_id, p.is_parallel_plan, ob.type OPTION (RECOMPILE, MAXDOP 1);",
			"GROUP BY rs.plan_id, p.is_parallel_plan, ob.type;",
			"needs OPTION (RECOMPILE, MAXDOP 1)"},
		{"the replica literal without its hint",
			"AND r.role_type <> 1 OPTION (RECOMPILE, MAXDOP 1)'",
			"AND r.role_type <> 1'",
			"inside the dynamic SQL"},
		{"a subquery in a DECLARE",
			"SET @plans_pinned = CASE",
			"DECLARE @n int = (SELECT COUNT(*) FROM @pinned);\n    SET @plans_pinned = CASE",
			"cannot carry OPTION"},
		{"IF EXISTS for the replica test",
			"DECLARE @secondary bit = 0, @replica_unreadable bit = 0;",
			"DECLARE @secondary bit = 0, @replica_unreadable bit = 0;\n" +
				"IF EXISTS (SELECT 1 FROM sys.dm_hadr_availability_replica_states AS ars WHERE ars.is_local = 1 AND ars.role = 2) SET @secondary = 1;",
			"cannot carry OPTION"},
		{"a third statement that returns rows",
			"/* ───────── bands",
			"SELECT DB_NAME() AS [x] FROM sys.databases AS d OPTION (RECOMPILE, MAXDOP 1);\n/* ───────── bands",
			"3 hinted statements return rows"},
	} {
		t.Run(m.name, func(t *testing.T) {
			if n := strings.Count(raw, m.old); n != 1 {
				t.Fatalf("anchor %q is in 043 %d times, want 1: move the anchor with the file", m.old, n)
			}
			if msg := lintOf(t, strings.Replace(raw, m.old, m.new, 1)); !strings.Contains(msg, m.want) {
				t.Errorf("the lint said %q; want a refusal containing %q", msg, m.want)
			}
		})
	}
}

// bandValuesRE finds the table of bands that 042 and 043 both write.
var bandValuesRE = regexp.MustCompile(`(?s)FROM \(VALUES (.*?)\) AS b \(ord, band, cost_from, cost_to\)`)

// bandValues returns a file's table of bands, its whitespace removed, and
// fails unless the file holds exactly one.
func bandValues(t *testing.T, path string) string {
	t.Helper()
	m := bandValuesRE.FindAllStringSubmatch(corpusScript(t, path).SQL, -1)
	if len(m) != 1 {
		t.Fatalf("%s holds %d band tables of the form FROM (VALUES ...) AS b (ord, band, cost_from, cost_to), want 1", path, len(m))
	}
	return strings.Join(strings.Fields(m[0][1]), "")
}

// 043 reads 042's bands from another source, and the analysis reads the two
// with the same functions and prints them side by side, so the boundaries
// are 042's to the digit. The live fixture fills four bands on the lab; this
// holds every boundary, on every instance.
func TestQueryStoreParallelCostBandsAreThoseOf042(t *testing.T) {
	got := bandValues(t, parallelCostPath)
	want := bandValues(t, "queries/80.workload/042.parallel-cost-distribution.sql")
	if got != want {
		t.Errorf("043's bands are\n%s\n042's are\n%s", got, want)
	}
}
