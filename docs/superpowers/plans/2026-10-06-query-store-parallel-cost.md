# Query Store Parallel Cost (043) Implementation Plan

> For agentic workers: REQUIRED SUB-SKILL: use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task by task, and `~/.claude/skills/subagent-implementation/SKILL.md` for what each implementer prompt must carry. Steps use checkbox (`- [ ]`) syntax for tracking.

Goal: add `queries/80.workload/043.query-store-parallel-cost.sql`, a default per-database collector that reads 042's cost bands from the Query Store, with its offline and live tests, its CI steps, its documents, and the reading of it by the private consumer `seuil_parallelisme.py`.

Architecture: one SQL file, no change to non-test Go code. The file aggregates the window's runtime rows once into `#runtime`, pins the parallel plans by parallel CPU, reads their text a chunk at a time within two budgets, and emits a root object and seven bands. Offline tests hold its contract, its caps and its lint; live tests on the lab run it through `runUnit` against a database built to the spec's fixture and compare its bands with an independent reading of the store.

Tech stack: T-SQL (SQL Server 2016 and later), Go 1.27 tests (`collect` and the root package), GitHub Actions, Python 3.14 and pytest in the private repository.

Spec: `docs/query-store-parallel-cost-spec.md`, version 5. Executors read both. Where this plan departs from the spec, it says so in the task and in "Ambiguities and contradictions" at the end.

## Verification of this plan's code

Every piece of code below was run, not only read, on 6 October 2026, in a scratch copy of `main` at `f83d882` under `/var/tmp`, against the lab's SQL Server 2025 (22 schedulers, threshold 5, MAXDOP 0):

- the collector lints clean through `collect.Discover`, and each of the seven lint mutations of Task 2 is refused for the reason its test expects;
- the spec's fixture reproduced: eight parallel plans and one serial plan at `max_dop` 51 (the `#savings` anomaly); 043's bands matched the xml oracle; `is_parallel_plan` agreed with a `Parallel="1"` operator on every costed plan;
- the full live suite passed (15 `--- PASS` lines), and every break step listed in Tasks 3 to 6 failed as stated, with the messages quoted there;
- `go test ./...` was green, `tools/verify-corpus-grammar.ps1` parsed 043 under TSql130 with 2 of 2 result sets, and the CI `jq` expression of Task 7 accepted a real 043 document and refused two altered ones;
- the private consumer's code and tests ran (28 passed) and each of its break steps failed one test.

Two defects were found that way and are fixed in the code below: a header line that began with an `@` word (the parser refuses it as an unknown directive), and a loop timer declared `datetime2(3)`, whose rounding made the elapsed time read -1 ms so that a time budget of 0 never stopped the loop. All scratch databases were dropped.

## Global Constraints

- Base: execute on a branch from `main` at `f83d882` or later, which holds the 042 fix (`98e6d14`). This plan's own branch, `avg-dop-plan`, is based on `941cb38`, before it. On `f83d882` the root package lists 21 tests and `collect` 518 (`/usr/local/go/bin/go test -list '.*' <pkg> | /usr/bin/grep -c '^Test'`); measure them again at the start, and use the measured numbers.
- The repository is public: no client identifier anywhere, commit messages included. Test databases are named `ZzAvgDopLive...` only, and dropped.
- Code comments, documents and commit messages are in English. Commit messages are prose that says why, with no attribution trailer of any kind. Commit locally; do not push and do not merge.
- `go test ./...` passes before every commit, and `gofmt -l .` prints nothing.
- `testdata/corpus.txt` is regenerated with `go test . -run TestEmbeddedCorpusIsValid -update`, never edited by hand, never in CI.
- Run Go as `/usr/local/go/bin/go` and `grep` as `/usr/bin/grep` in the commands that count. A hook rewrites a bare `go test` or `grep` through `rtk`, which filters the `-v` output these steps count.
- Live tests: lab `localhost,11533`, user `sa`, the password only as `$(podman exec sql2025 printenv MSSQL_SA_PASSWORD)`, and every scratch file under `/var/tmp/sqa-avgdop` (`TMPDIR` and `GOTMPDIR`). The machine's memory is tight: never run two live suites at once. The lab's `max server memory` is 4096 MB by the owner's decision; do not change it.
- One shell invocation holds the environment and the test command together. Shell state does not survive between tool calls, so a live test run in a second call skips, and `go test` prints `ok` for a skip.
- Constants of 043, from the spec: `@window_days` 7, `@cap` 1000, `@chunk` 100, `@budget_bytes` 104857600, `@budget_ms` 10000; `@timeout` 120; `@min_version` 13; permissions `CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE` (029's).
- No `@requires_flag`, `@discloses` or `@profiles`. `KnownFlags`, `CostFlags`, `main.go`'s help, the README's option tables, the wizard and `.env.example` do not change. No non-test file under `collect/` changes.
- Every statement of 043 that reads rows carries `OPTION (RECOMPILE, MAXDOP 1)`, the dynamic literal included; no `DECLARE` or `SET` holds a subquery; no `IF EXISTS (SELECT ...)`.
- Markdown written by this plan uses no bold and no em or en dash.
- If your own measurement contradicts this plan, stop and say so in your report rather than making the code match the plan. Never `git checkout` a file that holds uncommitted work.

## Review Focus

The inputs the spec implies and its tests do not exercise, most likely to bite first. The first three now have tests in the tasks named; the last two cannot be built on the lab and are listed so a reviewer looks at the code for them.

1. A database with a case-sensitive collation and a name that needs quoting (a space, a closing bracket). Expected: read like any other. Test: Task 6, "serial only" runs in `Latin1_General_CS_AS` under a name holding `" Serial]"`; measured, an identifier in the wrong case there fails with "Invalid object name".
2. A store in `READ_ONLY` (full, or set so by the DBA). Expected: read, bands as for `READ_WRITE`. Test: Task 6, "read only store".
3. A store holding more parallel plans than the cap. Expected: `stopped_by` is `cap`, `truncated` 1, `examined.plans` equals the cap. Test: Task 5, "cap below the plans".
4. A store whose plans weigh megabytes each. Expected: one chunk may pass the byte budget by up to a hundred plans, and `examined.largest_plan_bytes` says how large they were. Only the reporting is tested (Task 5 compares it with `DATALENGTH`); the overshoot itself is not.
5. A plan that leaves the store between the pin and its chunk, and a parallel `StatMan` plan with no statement cost. Expected: both in the unknown band, counted in `unknown.no_plan` and `unknown.no_cost`, their runtime figures still counted. No test builds either; the oracle of Task 3 handles a NULL cost the same way 043 does if the fixture happens to hold one.

## Files

| File | Change | Task |
| --- | --- | --- |
| `workload_caps_test.go` | the cap table learns exact applied forms; two rows for 043 | 1, 2 |
| `queries/80.workload/043.query-store-parallel-cost.sql` | new collector | 2 |
| `parallel_cost_test.go` (root package) | directives, contract and lint mutations of 043 | 2 |
| `testdata/corpus.txt` | regenerated, one line added | 2 |
| `docs/dba-guide.md` | `120 s` tier 29 to 30 (Task 2); cost row, `check` listing, a section, one sentence (Task 8) | 2, 8 |
| `collect/parallelcost_test.go` | offline: the constant rewriter the live tests use | 3 |
| `collect/parallelcost_live_test.go` | live tests 2, 3, 5 and 6 of the spec | 3 to 6 |
| `.github/workflows/ci.yml` | a `jq` check of ci_probe's 043 document, and a live step | 7 |
| `docs/caps-inventory.md`, `README.md`, `CHANGELOG.md`, the spec | documents | 8 |
| private: `.claude/skills/analyser-collecte/scripts/seuil_parallelisme.py`, its tests, `SKILL.md`, `HISTORY.md` | the reader of 043 | 9 |

## Task order

1. Applied forms in the workload cap test (refactor, no new test).
2. The collector in the corpus, with its contract and lint tests, the inventory and the guide's timeout tier.
3. The live fixture and the bands, against an independent reading of the store (spec test 3).
4. The document's keys and the planted literal (spec test 2).
5. The stop rules (spec test 5).
6. Stores read only, off, missing, and serial only (spec test 6, Review Focus 1 and 2).
7. CI for ci_probe's document and the live tests in the integration job.
8. The documents.
9. PRIVATE REPOSITORY: the reader of 043 in `seuil_parallelisme.py`.

Spec test 1 is Tasks 1 and 2; spec test 4 is the existing `TestEmbeddedCorpusIsValid`, re-verified in Task 2; spec test 7 is Task 2's guide edit.

---

### Task 1: Applied forms in the workload cap test

Files:
- Modify: `workload_caps_test.go`

Interfaces:
- Produces: `type capForm struct { text string; count int }`, `func top(variable string, n int) []capForm`, and the field `workloadCap.applied []capForm` replacing `tops int`. Task 2 adds rows that use `[]capForm{...}` directly.

Why: today every row counts `TOP (@variable)`. 043 pins with `TOP (@cap + 1)` and reads its chunk as a range of ranks; with the existing check the only count that passes for `@chunk` is 0, which asserts nothing (spec, "Running by default", second review finding 1). This task changes the shape and no behaviour: every existing row keeps its count.

- [ ] Step 1: Measure the starting point, in one invocation.

```bash
cd <worktree> && mkdir -p /var/tmp/sqa-avgdop && export TMPDIR=/var/tmp/sqa-avgdop GOTMPDIR=/var/tmp/sqa-avgdop && git log --oneline -1 && /usr/local/go/bin/go test -list '.*' . | /usr/bin/grep -c '^Test' && /usr/local/go/bin/go test -list '.*' ./collect/ | /usr/bin/grep -c '^Test'
```

Expected: a commit at or after `f83d882`, then 21 and 518. Record both numbers; later tasks count from them.

- [ ] Step 2: Apply this change to `workload_caps_test.go`.

```diff
@@ -28,22 +28,38 @@ type workloadCap struct {
 	file      string
 	variable  string
 	value     int
-	tops      int      // how many TOP (@variable) the file must apply
-	projected []string // root fields that must carry @variable as is
+	applied   []capForm // the exact forms that apply the cap, each counted
+	projected []string  // root fields that must carry @variable as is
+}
+
+// capForm is one exact form in which a file applies a cap, and how many times
+// the file holds it. Until 043 every cap was a TOP (@variable) and the count
+// of that one string was the whole check; 043 pins with TOP (@cap + 1), as
+// 027 does, and reads its chunk as a range of ranks, which a count of
+// TOP (@chunk) cannot see: the only count it passes is 0, which asserts
+// nothing.
+type capForm struct {
+	text  string
+	count int
+}
+
+// top is the form every cap of this table used before 043.
+func top(variable string, n int) []capForm {
+	return []capForm{{"TOP (" + variable + ")", n}}
 }
 
 var workloadCaps = []workloadCap{
-	{"020.query-store.sql", "@listing_cap", 200, 2, []string{"listing_cap"}},
-	{"023.query-store-most-executed.sql", "@listing_cap", 200, 2, []string{"listing_cap"}},
-	{"024.query-store-rowcount.sql", "@listing_cap", 200, 1, []string{"listing_cap"}},
-	{"026.query-store-interrupted.sql", "@listing_cap", 200, 2, []string{"listing_cap"}},
-	{"028.query-store-resources.sql", "@listing_cap", 200, 1, []string{"listing_cap"}},
-	{"060.spills.sql", "@listing_cap", 200, 1, []string{"listing_cap"}},
-	{"042.parallel-cost-distribution.sql", "@examined", 1000, 1, []string{"examined.cap"}},
-	{"030.implicit-conversions.sql", "@examined", 2500, 1, []string{"bounds.examined_cap"}},
-	{"030.implicit-conversions.sql", "@candidate_cap", 1000, 1, []string{"bounds.candidate_cap"}},
-	{"053.plan-warnings.sql", "@examined", 1000, 1, []string{"bounds.examined_cap"}},
-	{"053.plan-warnings.sql", "@candidate_cap", 500, 1, []string{"bounds.candidate_cap"}},
+	{"020.query-store.sql", "@listing_cap", 200, top("@listing_cap", 2), []string{"listing_cap"}},
+	{"023.query-store-most-executed.sql", "@listing_cap", 200, top("@listing_cap", 2), []string{"listing_cap"}},
+	{"024.query-store-rowcount.sql", "@listing_cap", 200, top("@listing_cap", 1), []string{"listing_cap"}},
+	{"026.query-store-interrupted.sql", "@listing_cap", 200, top("@listing_cap", 2), []string{"listing_cap"}},
+	{"028.query-store-resources.sql", "@listing_cap", 200, top("@listing_cap", 1), []string{"listing_cap"}},
+	{"060.spills.sql", "@listing_cap", 200, top("@listing_cap", 1), []string{"listing_cap"}},
+	{"042.parallel-cost-distribution.sql", "@examined", 1000, top("@examined", 1), []string{"examined.cap"}},
+	{"030.implicit-conversions.sql", "@examined", 2500, top("@examined", 1), []string{"bounds.examined_cap"}},
+	{"030.implicit-conversions.sql", "@candidate_cap", 1000, top("@candidate_cap", 1), []string{"bounds.candidate_cap"}},
+	{"053.plan-warnings.sql", "@examined", 1000, top("@examined", 1), []string{"bounds.examined_cap"}},
+	{"053.plan-warnings.sql", "@candidate_cap", 500, top("@candidate_cap", 1), []string{"bounds.candidate_cap"}},
 }
 
 var (
@@ -75,8 +91,10 @@ func TestWorkloadCapsAreDeclaredAppliedAndReported(t *testing.T) {
 		if v, _ := strconv.Atoi(m[0][1]); v != c.value {
 			t.Errorf("%s: %s is %d, want %d", c.file, c.variable, v, c.value)
 		}
-		if n := strings.Count(code, "TOP ("+c.variable+")"); n != c.tops {
-			t.Errorf("%s: TOP (%s) applied %d times, want %d", c.file, c.variable, n, c.tops)
+		for _, f := range c.applied {
+			if n := strings.Count(code, f.text); n != f.count {
+				t.Errorf("%s: %s applied %d times, want %d", c.file, f.text, n, f.count)
+			}
 		}
 		for _, lit := range literalTop.FindAllStringSubmatch(code, -1) {
 			if n, _ := strconv.Atoi(lit[1]); n > 1 {
```

- [ ] Step 3: Run the test, in one invocation.

```bash
cd <worktree> && export GOTMPDIR=/var/tmp/sqa-avgdop && gofmt -l . ; /usr/local/go/bin/go test . -run '^TestWorkloadCapsAreDeclaredAppliedAndReported$' -count=1 -v 2>&1 | /usr/bin/grep -c -- '--- PASS'
```

Expected: no gofmt output, then 1. Any other count means the filter or the work is wrong.

- [ ] Step 4: Break it. Change 060's row to `top("@listing_cap", 2)` and rerun Step 3. Expected: a count of 0; run without the `grep`, the output says `060.spills.sql: TOP (@listing_cap) applied 1 times, want 2`. Then restore the row. This is the step that proves the loop over `applied` checks something; reporting that it failed is the success of this step.

- [ ] Step 5: `/usr/local/go/bin/go test ./... -count=1` passes; the root package still lists the count of Step 1 (21 on `f83d882`).

- [ ] Step 6: Commit.

```bash
git add workload_caps_test.go
git commit -m "Let the workload cap test check a cap applied in any exact form

Every capped collector of 80.workload applied its cap as TOP (@variable),
and the test counted that one string. The Query Store parallel cost
collector pins with TOP (@cap + 1) and reads its chunk as a range of ranks,
which that count cannot see: the only count it would pass is 0. Each row now
lists the exact forms it must find and how many times, and every existing
row keeps the count it had."
```

### Task 2: The collector in the corpus

Files:
- Create: `queries/80.workload/043.query-store-parallel-cost.sql`
- Create: `parallel_cost_test.go`
- Modify: `workload_caps_test.go` (two rows), `testdata/corpus.txt` (regenerated), `docs/dba-guide.md` (one table row)

Interfaces:
- Consumes: `capForm` from Task 1; `collect.Discover`, `collect.Script` (`Path`, `SQL`, `Scope`, `TimeoutSec`, `MinVersion`, `Permissions`, `RequiresFlag`, `Discloses`, `Profiles`, `Widened`, `Writer`, `LintError`), `collect.ScopeDatabase`, `sqlauditor.Queries`.
- Produces: the file, whose constants are declared one per line in exactly the form `DECLARE @cap int = 1000;` (Task 3's rewriter depends on it), and whose anchors listed in `TestQueryStoreParallelCostLintRefusesItsMutations` each occur once.

The literal handed to `sp_executesql` only stages the other-role rows into `#other_rows` (where the column and `sys.query_store_replicas` exist), and one static aggregation, the same on every version from 2016, leaves them out with `NOT EXISTS`. This is the spec as amended on 6 October 2026 by the owner's ruling (ambiguity 1, ruled): "The aggregation of the runtime rows", second paragraph, and "The collector". An unreadable replica state is reported by `state.not_read_because` alone, with no `errors.*` key, as the spec's "Other replicas" now says (ambiguity 2, ruled). The measured behaviour is the spec's: the role mutation empties the bands, the column mutation turns `excluded.*` NULL.

- [ ] Step 1: Write the failing tests. Create `parallel_cost_test.go`:

```go
package sqlauditor_test

import (
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
```

and add the two 043 rows at the end of `workloadCaps`:

```diff
@@ -60,6 +60,12 @@ var workloadCaps = []workloadCap{
 	{"030.implicit-conversions.sql", "@candidate_cap", 1000, top("@candidate_cap", 1), []string{"bounds.candidate_cap"}},
 	{"053.plan-warnings.sql", "@examined", 1000, top("@examined", 1), []string{"bounds.examined_cap"}},
 	{"053.plan-warnings.sql", "@candidate_cap", 500, top("@candidate_cap", 1), []string{"bounds.candidate_cap"}},
+	// 043 pins @cap + 1 plans, as 027 does, so that reaching the extra one is
+	// evidence the cap bit, and reads the pinned plans by ranges of @chunk.
+	{"043.query-store-parallel-cost.sql", "@cap", 1000,
+		[]capForm{{"TOP (@cap + 1)", 1}}, []string{"cap"}},
+	{"043.query-store-parallel-cost.sql", "@chunk", 100,
+		[]capForm{{"rn < @lo + @chunk", 1}, {"SET @lo = @lo + @chunk", 1}}, []string{"chunk"}},
 }
 
 var (
```

- [ ] Step 2: Run them, in one invocation.

```bash
cd <worktree> && export GOTMPDIR=/var/tmp/sqa-avgdop && /usr/local/go/bin/go test . -run '^(TestQueryStoreParallelCost|TestWorkloadCapsAreDeclaredAppliedAndReported$)' -count=1 2>&1 | tail -5
```

Expected: FAIL, with `queries/80.workload/043.query-store-parallel-cost.sql is not in the corpus` and `open queries/80.workload/043...: file does not exist` from the caps test.

- [ ] Step 3: Create `queries/80.workload/043.query-store-parallel-cost.sql` with exactly this content:

```sql
-- @scope:       database
-- @resultsets:  root:object, bands:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     120
-- @min_version: 13
--
-- The cost bands of 80.workload/042.parallel-cost-distribution.sql, read from
-- the Query Store of each database instead of from the plan cache: which
-- parallel plans ran in the last seven days, at what estimated cost, how often,
-- with how much CPU and at what degree. The design and every measurement it
-- rests on are in docs/query-store-parallel-cost-spec.md.
--
-- WHY THIS FILE EXISTS BESIDE 042. 042 sees only what the cache still holds,
-- and a cache under memory pressure holds little (114 plans in all on one
-- client instance); a restart, most sp_configure changes and a flush empty it.
-- The store keeps what the cache evicts. The two describe different sets (042
-- the whole instance, this file the databases whose store is read) and must
-- not be added together.
--
-- WHAT IS READ. The plans with is_parallel_plan = 1 that executed in the
-- window, never max_dop: serial plans report a max_dop above 1 (INSERT ...
-- EXEC of sp_estimate_data_compression_savings reported 41 on 22 schedulers),
-- and a plan that is not parallel cannot be made serial by a threshold. Those
-- serial plans are counted in window.serial_plans_dop_above_1, as 042 counts
-- its own in examined.serial_plans_dop_above_1, and never banded. Serial plans
-- are not read at all; the serial side is the runtime totals of the root.
--
-- THE RUNTIME ROWS ARE AGGREGATED ONCE, per plan, into #runtime, and the root,
-- the ranking and the bands all come from it, so no total can disagree with
-- the sum of the bands because a statement ran in between. Execution types are
-- summed, as in 029: an execution stopped by a timeout used its CPU.
--
-- OTHER REPLICAS. On an availability group secondary the store is the
-- primary's, and this file does not read it (state.not_read_because says
-- secondary, or replica state unreadable when the replica's state cannot be
-- read, the rule of 70.schema/050.heaps.sql). On a primary from SQL Server
-- 2025, the runtime rows whose replica_group_id sys.query_store_replicas maps
-- to a role other than 1 are staged first into #other_rows, through
-- sp_executesql because the column does not exist before SQL Server 2022, and
-- left out of everything; excluded.* counts them. The predicate is negative: a
-- group the view does not list is kept. Where the column or the view is
-- missing nothing is left out and excluded.* is NULL: 0 means looked and found
-- none, NULL could not look.
--
-- NESTED QUERIES (sys.objects.type FN, TF, TR) stay in the bands, since a
-- parallel plan inside them is one the threshold acts on, and are counted in
-- nested.*. A scalar function's CPU is already in its caller, so window.cpu_s
-- leaves FN queries out and window.scalar_function_cpu_s says how much; a
-- trigger's or a multi-statement function's CPU is in no caller and stays in.
--
-- THE COST IS FOUND, NOT PARSED, as in 042: the first StatementSubTreeCost="
-- of the plan text, read up to the next double quote, TRY_CAST to float. No
-- plan is converted to xml. A stored plan is the plan of one query and its
-- first costed statement element is that query's own. Each chunk of plans is
-- first copied into a table variable column and searched there: every
-- reference to sys.query_store_plan.query_plan decompresses the plan again, so
-- the bytes are counted on the copy, never on the view.
--
-- THE CAP AND THE BUDGETS ARE CONSTANTS AND NOT OPTIONS, as in 027. The
-- parallel plans of the window are ranked by their CPU at a degree above 1,
-- then by plan id, and the first @cap + 1 are pinned before any plan text is
-- read; reaching @cap + 1 is the evidence that the cap bit. The pinned plans
-- are read @chunk at a time. Before each chunk the loop ends when every pinned
-- plan has been reached, then stops when @budget_ms have passed, then when the
-- bytes read reach @budget_bytes (examined.stopped_by: time, bytes, or cap when
-- the cap bit and no budget stopped the read). A chunk once started is read
-- whole, so either budget can be passed by one chunk; the selection before
-- the loop has no budget, and @timeout is the hard bound of the whole file.
-- selection.duration_ms, window.runtime_rows, examined.duration_ms,
-- examined.bytes_read, examined.largest_plan_bytes and examined.stopped_by
-- put the cost on the instance in the archive. Each statement that reads
-- sys.query_store_plan holds the store's shared lock, and a chunk releases it.
--
-- THE AUDIT'S OWN STATEMENTS. The audit's own statements are in the store, in
-- the serial totals (window.executions, window.cpu_s,
-- window.serial_plans_dop_above_1) and in the intervals (window.intervals,
-- window.newest_interval). Every one of them that reads rows carries OPTION
-- (RECOMPILE, MAXDOP 1), which the contract lint enforces, so they do not
-- reach a band, unless a Query Store hint set on one of them overrides it, or
-- the sample copy that sp_estimate_data_compression_savings runs under
-- --estimate-compression compiles parallel.
--
-- THE LIMITS A READER MUST KEEP IN VIEW. Only stores that are not off, and
-- only what they captured (under capture mode AUTO a cheap and rare query is
-- missing). The cost is the parallel plan's, lower than the serial cost the
-- optimizer compared with the threshold, so counts under a candidate are upper
-- bounds of what it would make serial. A plan compiled under an earlier
-- threshold is in the window with its old cost. avg_dop inherits the
-- documented anomaly of the DOP columns on many processors;
-- window.dop_above_schedulers counts the rows above the scheduler count. After
-- a failover inside the window, the primary role's earlier rows were run by
-- the other instance. window.intervals and window.newest_interval include the
-- audit's own activity and are not evidence that the client's workload ran.
--
-- A BAND. Seven rows always, with 042's boundaries and column names: [0, 5),
-- [5, 25), [25, 50), [50, 100), [100, 500), [500, +inf) and unknown (no plan
-- left in the store when its chunk came, or no statement cost in its text).
-- statements counts parallel plans; parallel_statements those with a runtime
-- row at max_dop > 1; parallel_executions and parallel_cpu_s are over the rows
-- where max_dop > 1, an upper bound; parallel_executions_min and
-- parallel_cpu_s_min over the rows where min_dop > 1, a lower bound; avg_dop
-- is weighted by parallel_executions. Pinned plans a budget left unread are in
-- no band, so examined.share_of_parallel_cpu_pct, the bands' parallel CPU over
-- window.parallel_cpu_s, is their share by construction.
--
-- NOTHING THAT NAMES A QUERY LEAVES THE SERVER: no text, no plan, no query or
-- plan id or hash, no object, no replica name. No @discloses.
--
-- Every statement that reads rows carries OPTION (RECOMPILE, MAXDOP 1), the
-- aggregation literal included; the replica state is read by an assignment an
-- IF tests, and no DECLARE or SET holds a subquery, which the lint refuses.
--
-- SQL Server 2016 is the floor: every column read outside the sp_executesql
-- branch exists there. Not measured on 2016.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

/* The constants, one declaration per line in this exact form: a test rewrites
   them in a copy of this text, as a --queries-dir corpus would. */
DECLARE @window_days int = 7;
DECLARE @cap int = 1000;
DECLARE @chunk int = 100;
DECLARE @budget_bytes bigint = 104857600;
DECLARE @budget_ms int = 10000;

DECLARE @collected_at datetimeoffset = SYSDATETIMEOFFSET();
DECLARE @from datetimeoffset = DATEADD(day, -@window_days, @collected_at);
DECLARE @schedulers int;
SELECT @schedulers = i.scheduler_count FROM sys.dm_os_sys_info AS i OPTION (RECOMPILE, MAXDOP 1);

/* ───────── whether the store is read ─────────
   A store that is OFF still returns its data, so the state is a predicate
   here. master and tempdb have no options row: no store. */
DECLARE @has_options bit = 0, @state nvarchar(60) = NULL;
SELECT @has_options = 1, @state = o.actual_state_desc
FROM sys.database_query_store_options AS o
OPTION (RECOMPILE, MAXDOP 1);

/* The availability group test of 70.schema/050.heaps.sql: an assignment, not
   IF EXISTS, which cannot carry the hint. A database whose replica_id is set
   and whose replica state cannot be read is treated as a secondary. */
DECLARE @secondary bit = 0, @replica_unreadable bit = 0;
BEGIN TRY
    SELECT @secondary = 1
    FROM sys.databases AS d
    JOIN sys.dm_hadr_availability_replica_states AS ars
      ON ars.replica_id = d.replica_id AND ars.is_local = 1
    WHERE d.database_id = DB_ID() AND ars.role = 2
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @secondary = 1, @replica_unreadable = 1
    FROM sys.databases AS d
    WHERE d.database_id = DB_ID() AND d.replica_id IS NOT NULL
    OPTION (RECOMPILE, MAXDOP 1);
END CATCH

DECLARE @not_read nvarchar(30) =
    CASE WHEN @has_options = 0        THEN N'no store'
         WHEN @state = N'OFF'         THEN N'off'
         WHEN @replica_unreadable = 1 THEN N'replica state unreadable'
         WHEN @secondary = 1          THEN N'secondary'
    END;
DECLARE @read bit = CASE WHEN @not_read IS NULL THEN 1 ELSE 0 END;

/* The replica groups can be told apart only where both the column (SQL
   Server 2022) and the view (SQL Server 2025) exist. */
DECLARE @replicas_known bit =
    CASE WHEN COL_LENGTH('sys.query_store_runtime_stats', 'replica_group_id') IS NOT NULL
          AND OBJECT_ID('sys.query_store_replicas') IS NOT NULL THEN 1 ELSE 0 END;

CREATE TABLE #other_rows (
    runtime_stats_id bigint NOT NULL PRIMARY KEY,
    executions       bigint NOT NULL,
    cpu_us           float  NOT NULL);

/* One row per plan executed in the window. par_* are the rows where
   max_dop > 1, min_* the rows where min_dop > 1. */
CREATE TABLE #runtime (
    plan_id               bigint NOT NULL PRIMARY KEY,
    is_parallel_plan      bit    NOT NULL,
    nested                bit    NOT NULL,
    scalar_function       bit    NOT NULL,
    runtime_rows          bigint NOT NULL,
    executions            bigint NOT NULL,
    cpu_us                float  NOT NULL,
    par_rows              bigint NOT NULL,
    par_executions        bigint NOT NULL,
    par_cpu_us            float  NOT NULL,
    min_executions        bigint NOT NULL,
    min_cpu_us            float  NOT NULL,
    dop_weighted          float  NOT NULL,
    max_dop               bigint NULL,
    rows_above_schedulers bigint NOT NULL);

DECLARE @pinned TABLE (rn int PRIMARY KEY, plan_id bigint NOT NULL UNIQUE);
DECLARE @intervals bigint, @oldest datetimeoffset, @newest datetimeoffset, @store_oldest datetimeoffset;
DECLARE @eligible bigint = 0, @cap_reached bit = 0, @plans_pinned bigint = 0;
DECLARE @selection_started datetime2 = SYSDATETIME(), @selection_ms int;

/* ───────── the selection: runtime rows only, no plan text ───────── */
IF @read = 1
BEGIN
    IF @replicas_known = 1
        EXEC sys.sp_executesql
            N'INSERT INTO #other_rows (runtime_stats_id, executions, cpu_us)
              SELECT rs.runtime_stats_id, rs.count_executions, rs.avg_cpu_time * rs.count_executions
              FROM sys.query_store_runtime_stats AS rs
              JOIN sys.query_store_runtime_stats_interval AS i
                ON i.runtime_stats_interval_id = rs.runtime_stats_interval_id
              JOIN sys.query_store_replicas AS r
                ON r.replica_group_id = rs.replica_group_id
              WHERE i.end_time > @from
                AND r.role_type <> 1 OPTION (RECOMPILE, MAXDOP 1)',
            N'@from datetimeoffset',
            @from = @from;

    INSERT INTO #runtime (plan_id, is_parallel_plan, nested, scalar_function, runtime_rows,
                          executions, cpu_us, par_rows, par_executions, par_cpu_us,
                          min_executions, min_cpu_us, dop_weighted, max_dop, rows_above_schedulers)
    SELECT rs.plan_id,
           p.is_parallel_plan,
           CASE WHEN ob.type IN ('FN', 'TF', 'TR') THEN 1 ELSE 0 END,
           CASE WHEN ob.type = 'FN' THEN 1 ELSE 0 END,
           COUNT_BIG(*),
           SUM(rs.count_executions),
           SUM(rs.avg_cpu_time * rs.count_executions),
           COUNT_BIG(CASE WHEN rs.max_dop > 1 THEN 1 END),
           SUM(CASE WHEN rs.max_dop > 1 THEN rs.count_executions ELSE 0 END),
           SUM(CASE WHEN rs.max_dop > 1 THEN rs.avg_cpu_time * rs.count_executions ELSE 0 END),
           SUM(CASE WHEN rs.min_dop > 1 THEN rs.count_executions ELSE 0 END),
           SUM(CASE WHEN rs.min_dop > 1 THEN rs.avg_cpu_time * rs.count_executions ELSE 0 END),
           SUM(CASE WHEN rs.max_dop > 1 THEN rs.avg_dop * rs.count_executions ELSE 0 END),
           MAX(rs.max_dop),
           COUNT_BIG(CASE WHEN rs.max_dop > @schedulers THEN 1 END)
    FROM sys.query_store_runtime_stats AS rs
    JOIN sys.query_store_runtime_stats_interval AS i
      ON i.runtime_stats_interval_id = rs.runtime_stats_interval_id
    JOIN sys.query_store_plan  AS p ON p.plan_id = rs.plan_id
    JOIN sys.query_store_query AS q ON q.query_id = p.query_id
    LEFT JOIN sys.objects AS ob ON ob.object_id = q.object_id AND q.object_id <> 0
    WHERE i.end_time > @from
      AND NOT EXISTS (SELECT 1 FROM #other_rows AS x WHERE x.runtime_stats_id = rs.runtime_stats_id)
    GROUP BY rs.plan_id, p.is_parallel_plan, ob.type OPTION (RECOMPILE, MAXDOP 1);

    SELECT @intervals = COUNT_BIG(*), @oldest = MIN(i.start_time), @newest = MAX(i.end_time)
    FROM sys.query_store_runtime_stats_interval AS i
    WHERE i.end_time > @from
      AND EXISTS (SELECT 1
                  FROM sys.query_store_runtime_stats AS rs
                  WHERE rs.runtime_stats_interval_id = i.runtime_stats_interval_id
                    AND NOT EXISTS (SELECT 1 FROM #other_rows AS x
                                    WHERE x.runtime_stats_id = rs.runtime_stats_id))
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @store_oldest = MIN(i.start_time)
    FROM sys.query_store_runtime_stats_interval AS i
    OPTION (RECOMPILE, MAXDOP 1);

    INSERT INTO @pinned (rn, plan_id)
    SELECT ROW_NUMBER() OVER (ORDER BY t.par_cpu_us DESC, t.plan_id), t.plan_id
    FROM (SELECT TOP (@cap + 1) r.plan_id, r.par_cpu_us
          FROM #runtime AS r
          WHERE r.is_parallel_plan = 1
          ORDER BY r.par_cpu_us DESC, r.plan_id) AS t OPTION (RECOMPILE, MAXDOP 1);

    SELECT @eligible = COUNT_BIG(*) FROM @pinned OPTION (RECOMPILE, MAXDOP 1);
    SET @cap_reached = CASE WHEN @eligible > @cap THEN 1 ELSE 0 END;
    DELETE FROM @pinned WHERE rn > @cap OPTION (RECOMPILE, MAXDOP 1);
    SET @plans_pinned = CASE WHEN @eligible > @cap THEN @cap ELSE @eligible END;
END
SET @selection_ms = DATEDIFF(millisecond, @selection_started, SYSDATETIME());

/* ───────── the read loop: @chunk plans at a time, within the budgets ───────── */
DECLARE @staged TABLE (plan_id bigint PRIMARY KEY, query_plan nvarchar(max) NULL);
DECLARE @costs TABLE (plan_id bigint PRIMARY KEY, plan_state tinyint NOT NULL, cost float NULL); -- 0 costed, 1 no plan, 3 no cost
DECLARE @lo int = 1, @bytes_read bigint = 0, @plans_read bigint = 0, @largest bigint = NULL;
DECLARE @stopped_by varchar(5) = NULL;
/* Both timers are datetime2 at full precision. At datetime2(3) the start is
   rounded and can land after the next SYSDATETIME(), the elapsed time then
   reads -1 ms, and a time budget of 0 never stops the loop (measured). */
DECLARE @loop_started datetime2 = SYSDATETIME(), @loop_ms int;

WHILE 1 = 1
BEGIN
    IF @read = 0 OR @lo > @plans_pinned BREAK;
    IF DATEDIFF(millisecond, @loop_started, SYSDATETIME()) >= @budget_ms
    BEGIN
        SET @stopped_by = 'time';
        BREAK;
    END
    IF @bytes_read >= @budget_bytes
    BEGIN
        SET @stopped_by = 'bytes';
        BREAK;
    END

    INSERT INTO @staged (plan_id, query_plan)
    SELECT k.plan_id, p.query_plan
    FROM @pinned AS k
    LEFT JOIN sys.query_store_plan AS p ON p.plan_id = k.plan_id
    WHERE k.rn >= @lo AND k.rn < @lo + @chunk
    OPTION (RECOMPILE, MAXDOP 1);

    INSERT INTO @costs (plan_id, plan_state, cost)
    SELECT s.plan_id,
           CASE WHEN s.query_plan IS NULL THEN 1 WHEN c.cost IS NULL THEN 3 ELSE 0 END,
           c.cost
    FROM @staged AS s
    CROSS APPLY (SELECT CHARINDEX(N'StatementSubTreeCost="', s.query_plan) + 22 AS v) AS k
    CROSS APPLY (SELECT CASE WHEN k.v > 22 THEN CHARINDEX(N'"', s.query_plan, k.v) END AS e) AS q
    CROSS APPLY (SELECT CASE WHEN q.e > k.v
                             THEN TRY_CAST(SUBSTRING(s.query_plan, k.v, q.e - k.v) AS float)
                        END AS cost) AS c
    OPTION (RECOMPILE, MAXDOP 1);

    SELECT @bytes_read = @bytes_read + ISNULL(SUM(DATALENGTH(s.query_plan)), 0),
           @plans_read = @plans_read + COUNT_BIG(CASE WHEN s.query_plan IS NOT NULL THEN 1 END),
           @largest    = CASE WHEN MAX(DATALENGTH(s.query_plan)) > ISNULL(@largest, -1)
                              THEN MAX(DATALENGTH(s.query_plan)) ELSE @largest END
    FROM @staged AS s
    OPTION (RECOMPILE, MAXDOP 1);

    DELETE FROM @staged OPTION (RECOMPILE, MAXDOP 1);
    SET @lo = @lo + @chunk;
END
SET @loop_ms = DATEDIFF(millisecond, @loop_started, SYSDATETIME());
IF @stopped_by IS NULL AND @cap_reached = 1 SET @stopped_by = 'cap';

/* ───────── root ─────────
   A LEFT JOIN from sys.databases, as in 026 and 029, so a database with no
   options row still returns its row. Counts are NULL when the store is not
   read and 0 when it is read and empty. */
SELECT DB_NAME()                                                     AS [database],
       @collected_at                                                 AS [collected_at],
       o.actual_state_desc                                           AS [state.actual],
       o.query_capture_mode_desc                                     AS [state.capture_mode],
       o.interval_length_minutes                                     AS [state.interval_minutes],
       @not_read                                                     AS [state.not_read_because],
       @schedulers                                                   AS [schedulers],
       @window_days                                                  AS [window.days],
       @from                                                         AS [window.from],
       @oldest                                                       AS [window.oldest_interval],
       @newest                                                       AS [window.newest_interval],
       @store_oldest                                                 AS [window.store_oldest_interval],
       @intervals                                                    AS [window.intervals],
       CASE WHEN @read = 1 THEN ISNULL(a.runtime_rows, 0) + x.other_rows END
                                                                     AS [window.runtime_rows],
       CASE WHEN @read = 1 THEN ISNULL(a.executions, 0) END          AS [window.executions],
       CASE WHEN @read = 1 THEN CAST(ISNULL(a.cpu_us, 0) / 1000000.0 AS decimal(18,1)) END
                                                                     AS [window.cpu_s],
       CASE WHEN @read = 1 THEN CAST(ISNULL(a.fn_cpu_us, 0) / 1000000.0 AS decimal(18,1)) END
                                                                     AS [window.scalar_function_cpu_s],
       CASE WHEN @read = 1 THEN a.par_plans END                      AS [window.parallel_plans],
       CASE WHEN @read = 1 THEN CAST(ISNULL(a.par_plan_cpu_us, 0) / 1000000.0 AS decimal(18,1)) END
                                                                     AS [window.parallel_plan_cpu_s],
       CASE WHEN @read = 1 THEN ISNULL(a.par_executions, 0) END      AS [window.parallel_executions],
       CASE WHEN @read = 1 THEN CAST(ISNULL(a.par_cpu_us, 0) / 1000000.0 AS decimal(18,1)) END
                                                                     AS [window.parallel_cpu_s],
       CASE WHEN @read = 1 THEN ISNULL(a.min_executions, 0) END      AS [window.parallel_executions_min],
       CASE WHEN @read = 1 THEN CAST(ISNULL(a.min_cpu_us, 0) / 1000000.0 AS decimal(18,1)) END
                                                                     AS [window.parallel_cpu_s_min],
       CASE WHEN @read = 1 THEN a.serial_dop_above_1 END             AS [window.serial_plans_dop_above_1],
       CASE WHEN @read = 1 THEN ISNULL(a.rows_above_schedulers, 0) END
                                                                     AS [window.dop_above_schedulers],
       CASE WHEN @read = 1 THEN a.nested_plans END                   AS [nested.parallel_plans],
       CASE WHEN @read = 1 THEN CAST(ISNULL(a.nested_par_cpu_us, 0) / 1000000.0 AS decimal(18,1)) END
                                                                     AS [nested.parallel_cpu_s],
       CASE WHEN @read = 1 AND @replicas_known = 1 THEN ISNULL(x.other_executions, 0) END
                                                                     AS [excluded.other_replicas_executions],
       CASE WHEN @read = 1 AND @replicas_known = 1
            THEN CAST(ISNULL(x.other_cpu_us, 0) / 1000000.0 AS decimal(18,1)) END
                                                                     AS [excluded.other_replicas_cpu_s],
       @cap                                                          AS [cap],
       @chunk                                                        AS [chunk],
       @budget_bytes                                                 AS [budget.bytes],
       @budget_ms                                                    AS [budget.ms],
       CASE WHEN @read = 1 THEN @selection_ms END                    AS [selection.duration_ms],
       CASE WHEN @read = 1 THEN @plans_pinned END                    AS [examined.plans],
       CASE WHEN @read = 1 THEN @plans_read END                      AS [examined.plans_read],
       CASE WHEN @read = 1 THEN @bytes_read END                      AS [examined.bytes_read],
       CASE WHEN @read = 1 THEN @loop_ms END                         AS [examined.duration_ms],
       @largest                                                      AS [examined.largest_plan_bytes],
       CASE WHEN @read = 1
            THEN CAST(100.0 * ISNULL(b.banded_par_cpu_us, 0) / NULLIF(ISNULL(a.par_cpu_us, 0), 0) AS decimal(5,1))
       END                                                           AS [examined.share_of_parallel_cpu_pct],
       @stopped_by                                                   AS [examined.stopped_by],
       CASE WHEN @read = 1 THEN CASE WHEN @stopped_by IS NULL THEN 0 ELSE 1 END END
                                                                     AS [truncated],
       CASE WHEN @read = 1 THEN b.no_plan END                        AS [unknown.no_plan],
       CASE WHEN @read = 1 THEN b.no_cost END                        AS [unknown.no_cost]
FROM sys.databases AS d
LEFT JOIN sys.database_query_store_options AS o ON 1 = 1
CROSS JOIN (SELECT SUM(r.runtime_rows)                                                    AS runtime_rows,
                   SUM(CASE WHEN r.scalar_function = 0 THEN r.executions END)             AS executions,
                   SUM(CASE WHEN r.scalar_function = 0 THEN r.cpu_us END)                 AS cpu_us,
                   SUM(CASE WHEN r.scalar_function = 1 THEN r.cpu_us END)                 AS fn_cpu_us,
                   COUNT_BIG(CASE WHEN r.is_parallel_plan = 1 THEN 1 END)                 AS par_plans,
                   SUM(CASE WHEN r.is_parallel_plan = 1 THEN r.cpu_us END)                AS par_plan_cpu_us,
                   SUM(CASE WHEN r.is_parallel_plan = 1 THEN r.par_executions END)        AS par_executions,
                   SUM(CASE WHEN r.is_parallel_plan = 1 THEN r.par_cpu_us END)            AS par_cpu_us,
                   SUM(CASE WHEN r.is_parallel_plan = 1 THEN r.min_executions END)        AS min_executions,
                   SUM(CASE WHEN r.is_parallel_plan = 1 THEN r.min_cpu_us END)            AS min_cpu_us,
                   COUNT_BIG(CASE WHEN r.is_parallel_plan = 0 AND r.par_rows > 0 THEN 1 END)
                                                                                          AS serial_dop_above_1,
                   SUM(CASE WHEN r.is_parallel_plan = 1 THEN r.rows_above_schedulers END) AS rows_above_schedulers,
                   COUNT_BIG(CASE WHEN r.is_parallel_plan = 1 AND r.nested = 1 THEN 1 END) AS nested_plans,
                   SUM(CASE WHEN r.is_parallel_plan = 1 AND r.nested = 1 THEN r.par_cpu_us END)
                                                                                          AS nested_par_cpu_us
            FROM #runtime AS r) AS a
CROSS JOIN (SELECT COUNT_BIG(*) AS other_rows, SUM(w.executions) AS other_executions,
                   SUM(w.cpu_us) AS other_cpu_us
            FROM #other_rows AS w) AS x
CROSS JOIN (SELECT SUM(r.par_cpu_us)                               AS banded_par_cpu_us,
                   COUNT_BIG(CASE WHEN c.plan_state = 1 THEN 1 END) AS no_plan,
                   COUNT_BIG(CASE WHEN c.plan_state = 3 THEN 1 END) AS no_cost
            FROM @costs AS c
            JOIN #runtime AS r ON r.plan_id = c.plan_id) AS b
WHERE d.database_id = DB_ID()
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── bands ─────────
   Every band, always, empty ones included. cost_from is inclusive and
   cost_to exclusive; both are NULL for unknown. A plan's runtime figures come
   from #runtime, so a plan that left the store before its chunk still counts,
   in unknown. */
SELECT b.band                                                             AS [band],
       b.cost_from                                                        AS [cost_from],
       b.cost_to                                                          AS [cost_to],
       COUNT(z.plan_id)                                                   AS [statements],
       COUNT(CASE WHEN z.par_rows > 0 THEN 1 END)                         AS [parallel_statements],
       ISNULL(SUM(z.executions), 0)                                       AS [executions],
       ISNULL(SUM(z.par_executions), 0)                                   AS [parallel_executions],
       CAST(ISNULL(SUM(z.cpu_us), 0) / 1000000.0 AS decimal(18,1))        AS [cpu_s],
       CAST(ISNULL(SUM(z.par_cpu_us), 0) / 1000000.0 AS decimal(18,1))    AS [parallel_cpu_s],
       MAX(z.max_dop)                                                     AS [max_dop],
       ISNULL(SUM(z.min_executions), 0)                                   AS [parallel_executions_min],
       CAST(ISNULL(SUM(z.min_cpu_us), 0) / 1000000.0 AS decimal(18,1))    AS [parallel_cpu_s_min],
       ROUND(SUM(z.dop_weighted) / NULLIF(SUM(z.par_executions), 0), 2)   AS [avg_dop]
FROM (VALUES (1, 'lt_5',        0.0,   5.0),
             (2, '5_25',        5.0,  25.0),
             (3, '25_50',      25.0,  50.0),
             (4, '50_100',     50.0, 100.0),
             (5, '100_500',   100.0, 500.0),
             (6, 'ge_500',    500.0,  NULL),
             (7, 'unknown',    NULL,  NULL)) AS b (ord, band, cost_from, cost_to)
LEFT JOIN (SELECT c.cost, r.plan_id, r.par_rows, r.executions, r.par_executions, r.cpu_us,
                  r.par_cpu_us, r.max_dop, r.min_executions, r.min_cpu_us, r.dop_weighted
           FROM @costs AS c
           JOIN #runtime AS r ON r.plan_id = c.plan_id) AS z
       ON (b.band = 'unknown' AND z.cost IS NULL)
       OR (b.band <> 'unknown' AND z.cost >= b.cost_from
           AND (b.cost_to IS NULL OR z.cost < b.cost_to))
GROUP BY b.ord, b.band, b.cost_from, b.cost_to
ORDER BY b.ord
OPTION (RECOMPILE, MAXDOP 1);
```

- [ ] Step 4: In `docs/dba-guide.md`, under "#### The one thing it does not bound", change the row `| 120 s | 29 |` to `| 120 s | 30 |`. `TestTheGuideCountsTheTimeoutTiersOfTheCorpus` fails until it reads 30 (spec test 7). If the corpus on your base counts another number, the test prints the table it wants: use that and say so.

- [ ] Step 5: Regenerate the inventory, alone, then look at its diff.

```bash
cd <worktree> && export GOTMPDIR=/var/tmp/sqa-avgdop && /usr/local/go/bin/go test . -run TestEmbeddedCorpusIsValid -update -count=1 && git diff testdata/corpus.txt
```

Expected: one added line, `80.workload/043.query-store-parallel-cost.sql`, with no `@profiles` after it, between 042 and 050. Any other change to the file is a finding to report.

- [ ] Step 6: Run the tests, in one invocation, and count.

```bash
cd <worktree> && export GOTMPDIR=/var/tmp/sqa-avgdop && /usr/local/go/bin/go test . -run '^TestQueryStoreParallelCost' -count=1 -v 2>&1 | /usr/bin/grep -c -- '--- PASS' && /usr/local/go/bin/go test -list '.*' . | /usr/bin/grep -c '^Test' && /usr/local/go/bin/go test ./... -count=1 2>&1 | tail -6
```

Expected: 9 (two tests, seven mutation subtests), then the count measured in Task 1 plus 2 (23 on `f83d882`), then every package `ok`. The root package takes about 45 s.

- [ ] Step 7: Break steps. Each one edits the named file, runs the named test, must fail with the message given, and is then undone (copy the file aside first: `cp <file> /var/tmp/sqa-avgdop/keep` and copy it back; do not use `git checkout`). Reporting that one did not fail is a finding, not a failure of the task.

| Mutation (in 043) | Test that must fail | Message measured |
| --- | --- | --- |
| `TOP (@cap + 1)` written `TOP (@cap)` | `TestWorkloadCapsAreDeclaredAppliedAndReported` | `TOP (@cap + 1) applied 0 times, want 1` |
| `TOP (@cap + 1)` written `TOP (1001)` | the same | `literal TOP (1001) outside a comment` (twice, one per 043 row) |
| `SET @lo = @lo + @chunk;` written `SET @lo = @lo + 100;` | the same | `SET @lo = @lo + @chunk applied 0 times, want 1` |
| a line `-- @profiles:    space` added after `-- @min_version: 13` | `TestQueryStoreParallelCostDeclaresItsContract` | `043's directive lines are` and `profiles [space]` |
| the hint removed from `DELETE FROM @pinned WHERE rn > @cap` | `TestEmbeddedCorpusIsValid` | `every statement that reads rows needs OPTION (RECOMPILE, MAXDOP 1)` |

The last row is the spec's test 1 mutation on the `DELETE`; the mutation test already covers it on a copy, and this run shows the corpus test refuses the shipped file too.

- [ ] Step 8: Verify spec test 4 on this tree, without adding a test (the spec says nothing is added to it). For each mutation, apply it to `queries/50.agent/050.commandlog.sql` (keeping a copy aside), run `/usr/local/go/bin/go test . -run '^TestEmbeddedCorpusIsValid$' -count=1`, expect FAIL naming 050 with `every statement that reads rows needs OPTION (RECOMPILE, MAXDOP 1)`, and restore the file:
  - the `OPTION (RECOMPILE, MAXDOP 1)` after `) AS x` / `GROUP BY x.id` of `INSERT INTO #sel` removed;
  - `DECLARE @n bigint; SELECT @n = COUNT_BIG(*) FROM dbo.Ladder6 WHERE id % 7 = 5;` inserted after `SET LOCK_TIMEOUT 10000;`;
  - the file replaced by `git show 7984eef:queries/50.agent/050.commandlog.sql`.
  All three were measured refused on 6 October 2026. Restore 050 and confirm `git status` shows it unchanged.

- [ ] Step 9: Run the grammar check, which CI does not run.

```bash
cd <worktree> && pwsh -NoProfile -File tools/verify-corpus-grammar.ps1 2>&1 | /usr/bin/grep -E '80.workload/043|FAILED|^All '
```

Expected: `queries/80.workload/043.query-store-parallel-cost.sql TSql130 (SQL Server 2016) resultsets 2/2 ok`, and `All N files parse`.

- [ ] Step 10: Check for client names and commit.

```bash
git grep -niE "<the client names you have been working with>" -- queries/80.workload/043.query-store-parallel-cost.sql parallel_cost_test.go
git add queries/80.workload/043.query-store-parallel-cost.sql parallel_cost_test.go workload_caps_test.go testdata/corpus.txt docs/dba-guide.md
git commit -m "Read the cost bands of 042 from the Query Store of each database

The plan cache that 042 reads holds little under memory pressure and is
emptied by a restart or most configuration changes, so the distribution a
cost threshold for parallelism is chosen from can be too thin to use. The
Query Store keeps what the cache evicts. This collector runs by default, per
database, and gives the same seven bands over the parallel plans the store
holds for the last seven days, with a lower bound beside 042's upper one.

It reads the store only when it is not off and the database is not an
availability group secondary, leaves out the rows of other replica roles
where the store can tell them apart, finds each plan's cost by a text search
as 042 does, and reads at most a thousand plans, a hundred at a time, within
100 MB and 10 seconds per database. No text and no identifier of a query
leaves the server. The rows of other roles are staged through sp_executesql
rather than aggregated there, because a statement naming replica_group_id
does not compile before SQL Server 2022 and one aggregation should serve
every version."
```

### Task 3: The live fixture and the bands

Files:
- Create: `collect/parallelcost_test.go`, `collect/parallelcost_live_test.go`

Interfaces:
- Consumes (real names in `collect`, read from the tree): `liveConfig(t) *Config` (skips without `SQL_AUDITOR_LIVE_SERVER`), `Open(cfg *Config) (*sql.DB, error)`, `runUnit(ctx, bound context.Context, conn *sql.Conn, o Options, m *Manifest, rw *runWriter, s Script, u DatabaseFolder, watch *blockingWatch, spid int) (bool, error)`, `newRunWriter(root string, budget int)`, `ResultRelativePath(dir, base, dbFolder string) string`, `parseScript(rel, sql string) Script`, `quoteName(string) string`, `Options{Config: cfg}`, `DatabaseFolder{Name, Folder}`, `Manifest{}`.
- Produces, for Tasks 4 to 6: `parallelCostScript(t, map[string]int64) Script`; `pcRootKeys`, `pcBandKeys`; `pcAdmin`, `pcCreateDatabase(t, admin, name, collate string, noAutoStats bool)`, `pcOpen`, `pcExec`, `pcLadder`, `pcRun(t, cfg, s, name) ([]byte, map[string]any)`, `pcFlat`, `pcBands`, `pcNum`, `pcPlan`, `pcOracle`, `pcSerialAboveOne`, `pcParallel`, `pcCompareBands(t, doc, []pcPlan)`, `pcCheckShare`, `pcSettings`, `pcReplicasKnown`, and `TestLiveQueryStoreParallelCost`, to which Tasks 4 to 6 add subtests before its closing brace.

The collector exists since Task 2, so these tests may pass at once. What proves them is Step 5: each mutation of the spec's test 3 must make them fail.

Two choices made here, both flagged at the end. The spec asks that `window.serial_plans_dop_above_1` be at least 1 "and stops there if not"; read from the document alone, a 043 that excluded every row failed with a message blaming the fixture (measured with the role mutation). So the premise is read from the store first, and the document is then held to it. And the test adds one assertion the spec does not list: on every costed plan, `is_parallel_plan` agrees with an operator marked `Parallel="1"`, which is how 042 has called a statement parallel since `98e6d14`.

- [ ] Step 1: Create `collect/parallelcost_test.go`:

```go
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
```

- [ ] Step 2: Create `collect/parallelcost_live_test.go`:

```go
package collect

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 80.workload/043 against a real instance, through runUnit, the path a
// collection takes. Skipped unless SQL_AUDITOR_LIVE_SERVER is set
// (liveConfig):
//
//	SQL_AUDITOR_LIVE_SERVER=localhost,11533 SQL_AUDITOR_LIVE_USER=sa \
//	SQL_AUDITOR_LIVE_PASSWORD=... go test ./collect/ -run '^TestLiveQueryStoreParallelCost' -v
//
// Every database here is created under the ZzAvgDopLive prefix, with a
// suffix of its own so two runs cannot collide, and dropped by t.Cleanup. The
// oracles read the test database from master with three-part names, so that
// their own reads are not captured into the store they check.

// pcRootKeys and pcBandKeys are the lists of "The collector" in
// docs/query-store-parallel-cost-spec.md: the whole of what 043 may emit.
var pcRootKeys = []string{
	"database", "collected_at",
	"state.actual", "state.capture_mode", "state.interval_minutes", "state.not_read_because",
	"schedulers",
	"window.days", "window.from", "window.oldest_interval", "window.newest_interval",
	"window.store_oldest_interval", "window.intervals", "window.runtime_rows",
	"window.executions", "window.cpu_s", "window.scalar_function_cpu_s",
	"window.parallel_plans", "window.parallel_plan_cpu_s",
	"window.parallel_executions", "window.parallel_cpu_s",
	"window.parallel_executions_min", "window.parallel_cpu_s_min",
	"window.serial_plans_dop_above_1", "window.dop_above_schedulers",
	"nested.parallel_plans", "nested.parallel_cpu_s",
	"excluded.other_replicas_executions", "excluded.other_replicas_cpu_s",
	"cap", "chunk", "budget.bytes", "budget.ms",
	"selection.duration_ms",
	"examined.plans", "examined.plans_read", "examined.bytes_read", "examined.duration_ms",
	"examined.largest_plan_bytes", "examined.share_of_parallel_cpu_pct", "examined.stopped_by",
	"truncated",
	"unknown.no_plan", "unknown.no_cost",
}

var pcBandKeys = []string{
	"band", "cost_from", "cost_to", "statements", "parallel_statements",
	"executions", "parallel_executions", "cpu_s", "parallel_cpu_s", "max_dop",
	"parallel_executions_min", "parallel_cpu_s_min", "avg_dop",
}

// pcBandBounds are 042's boundaries; unknown is the band of a plan without a
// cost.
var pcBandBounds = []struct {
	name     string
	from, to float64
}{
	{"lt_5", 0, 5}, {"5_25", 5, 25}, {"25_50", 25, 50}, {"50_100", 50, 100},
	{"100_500", 100, 500}, {"ge_500", 500, math.Inf(1)},
}

// pcSuffix keeps two runs on one instance from creating the same database.
func pcSuffix() string { return strconv.FormatInt(time.Now().UnixNano()%1e9, 36) }

func pcExec(t *testing.T, db *sql.DB, stmt string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		t.Fatalf("%v\non: %s", err, stmt)
	}
}

// pcAdmin opens master for the DDL and the oracles.
func pcAdmin(t *testing.T) (*Config, *sql.DB) {
	t.Helper()
	cfg := liveConfig(t)
	cfg.Database = "master"
	cfg.QueryTimeout = 5 * time.Minute
	admin, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	return cfg, admin
}

// pcCreateDatabase creates a database whose store captures everything in
// one-minute intervals, and drops it when the test ends. collate and
// noAutoStats serve the serial store of TestLiveQueryStoreParallelCostStoresNotRead.
func pcCreateDatabase(t *testing.T, admin *sql.DB, name, collate string, noAutoStats bool) {
	t.Helper()
	q := quoteName(name)
	create := "CREATE DATABASE " + q
	if collate != "" {
		create += " COLLATE " + collate
	}
	pcExec(t, admin, create+";")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "IF DB_ID(@p1) IS NOT NULL BEGIN ALTER DATABASE "+q+
			" SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE "+q+"; END", name); err != nil {
			t.Errorf("dropping %s: %v; drop it by hand", name, err)
		}
	})
	pcExec(t, admin, "ALTER DATABASE "+q+" SET RECOVERY SIMPLE;")
	if noAutoStats {
		pcExec(t, admin, "ALTER DATABASE "+q+" SET AUTO_CREATE_STATISTICS OFF;")
	}
	pcExec(t, admin, "ALTER DATABASE "+q+" SET QUERY_STORE = ON "+
		"(OPERATION_MODE = READ_WRITE, QUERY_CAPTURE_MODE = ALL, INTERVAL_LENGTH_MINUTES = 1);")
}

// pcOpen opens a connection pool on the test database, closed at the end of
// the test (before the drop, cleanups running in reverse order).
func pcOpen(t *testing.T, cfg *Config, name string) *sql.DB {
	t.Helper()
	c := *cfg
	c.Database = name
	db, err := Open(&c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// pcLadder builds and runs test 3's workload, as the spec writes it, and
// flushes the store so the views hold it.
func pcLadder(t *testing.T, cfg *Config, name string) {
	t.Helper()
	w := pcOpen(t, cfg, name)
	sizes := []int{20000, 50000, 100000, 200000, 500000, 1000000, 2000000}
	for i, n := range sizes {
		tb := fmt.Sprintf("dbo.Ladder%d", i+1)
		pcExec(t, w, "CREATE TABLE "+tb+" (id bigint NOT NULL, pad char(100) NOT NULL);")
		pcExec(t, w, fmt.Sprintf("INSERT INTO %s WITH (TABLOCK) (id, pad) "+
			"SELECT TOP (%d) ROW_NUMBER() OVER (ORDER BY (SELECT NULL)), 'x' "+
			"FROM sys.all_columns AS a CROSS JOIN sys.all_columns AS b CROSS JOIN sys.all_columns AS c;", tb, n))
	}
	for i := range sizes {
		tb := fmt.Sprintf("dbo.Ladder%d", i+1)
		pcExec(t, w, "SELECT COUNT_BIG(*) FROM "+tb+" WHERE id % 7 = 3;")
		pcExec(t, w, "SELECT COUNT_BIG(*) FROM "+tb+" WHERE id % 7 = 3 OPTION (MAXDOP 2);")
	}
	pcExec(t, w, "SELECT COUNT_BIG(*) FROM dbo.Ladder6 WHERE pad <> 'ZZ043_PLANTED_TEXT' OPTION (MAXDOP 2);")
	pcExec(t, w, "CREATE PROCEDURE dbo.TwoStatements AS BEGIN SET NOCOUNT ON; "+
		"SELECT pad FROM dbo.Ladder1 WHERE id = 1; "+
		"SELECT COUNT_BIG(*) FROM dbo.Ladder7 WHERE id % 7 = 4; END;")
	pcExec(t, w, "EXEC dbo.TwoStatements;")
	pcExec(t, w, "CREATE TABLE #savings (object_name sysname, schema_name sysname, index_id int, "+
		"partition_number int, size_current_kb bigint, size_requested_kb bigint, "+
		"sample_current_kb bigint, sample_requested_kb bigint); "+
		"INSERT INTO #savings EXEC sys.sp_estimate_data_compression_savings @schema_name = N'dbo', "+
		"@object_name = N'Ladder7', @index_id = NULL, @partition_number = NULL, @data_compression = N'PAGE';")
	pcExec(t, w, "EXEC sys.sp_query_store_flush_db;")
}

// pcRun runs s on database name through runUnit and returns the document it
// wrote, raw and decoded.
func pcRun(t *testing.T, cfg *Config, s Script, name string) ([]byte, map[string]any) {
	t.Helper()
	ctx := context.Background()
	runner, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	conn, err := runner.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	dir := t.TempDir()
	m, rw := &Manifest{}, newRunWriter(dir, 1<<20)
	u := DatabaseFolder{Name: name, Folder: "db"}
	if _, err := runUnit(ctx, ctx, conn, Options{Config: cfg}, m, rw, s, u, nil, 0); err != nil {
		t.Fatalf("043 on %s: %v", name, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ResultRelativePath(s.Dir, s.Base, u.Folder))))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return raw, doc
}

// pcFlat is the document's root as dotted keys, the bands left out.
func pcFlat(doc map[string]any) map[string]any {
	out := map[string]any{}
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			if prefix == "" && k == "bands" {
				continue
			}
			if sub, ok := v.(map[string]any); ok {
				walk(prefix+k+".", sub)
				continue
			}
			out[prefix+k] = v
		}
	}
	walk("", doc)
	return out
}

func pcBands(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	arr, ok := doc["bands"].([]any)
	if !ok {
		t.Fatalf("the document has no bands array: %v", doc["bands"])
	}
	var out []map[string]any
	for _, b := range arr {
		out = append(out, b.(map[string]any))
	}
	return out
}

func pcNum(t *testing.T, v any, what string) float64 {
	t.Helper()
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("%s is %v (%T), want a number", what, v, v)
	}
	return f
}

// pcPlan is one plan of the test store, read by the oracle.
type pcPlan struct {
	id                                 int64
	parallel, marker                   bool
	cost                               sql.NullFloat64
	costed                             int64
	bytes                              int64
	execs, parRows, parExecs, minExecs int64
	cpu, parCPU, minCPU, dopWeighted   float64
	maxDOP                             int64
}

// pcOracle reads every plan of the test store executed in the last seven
// days, with its cost through the xml path on the first costed statement
// element, the independent reading the spec asks for, and its runtime figures
// summed by the test's own query.
func pcOracle(t *testing.T, admin *sql.DB, name string) []pcPlan {
	t.Helper()
	q := quoteName(name)
	rows, err := admin.Query(`
SELECT p.plan_id, CAST(p.is_parallel_plan AS int),
       CASE WHEN CHARINDEX(N' Parallel="1"', p.query_plan) > 0 THEN 1 ELSE 0 END,
       x.cost, ISNULL(x.costed, 0), ISNULL(DATALENGTH(p.query_plan), 0),
       r.execs, r.par_rows, r.par_execs, r.min_execs, r.cpu, r.par_cpu, r.min_cpu, r.dop_weighted, r.max_dop
FROM ` + q + `.sys.query_store_plan AS p
JOIN (SELECT rs.plan_id,
             SUM(rs.count_executions) AS execs,
             COUNT_BIG(CASE WHEN rs.max_dop > 1 THEN 1 END) AS par_rows,
             SUM(CASE WHEN rs.max_dop > 1 THEN rs.count_executions ELSE 0 END) AS par_execs,
             SUM(CASE WHEN rs.min_dop > 1 THEN rs.count_executions ELSE 0 END) AS min_execs,
             SUM(rs.avg_cpu_time * rs.count_executions) AS cpu,
             SUM(CASE WHEN rs.max_dop > 1 THEN rs.avg_cpu_time * rs.count_executions ELSE 0 END) AS par_cpu,
             SUM(CASE WHEN rs.min_dop > 1 THEN rs.avg_cpu_time * rs.count_executions ELSE 0 END) AS min_cpu,
             SUM(CASE WHEN rs.max_dop > 1 THEN rs.avg_dop * rs.count_executions ELSE 0 END) AS dop_weighted,
             MAX(rs.max_dop) AS max_dop
      FROM ` + q + `.sys.query_store_runtime_stats AS rs
      JOIN ` + q + `.sys.query_store_runtime_stats_interval AS i
        ON i.runtime_stats_interval_id = rs.runtime_stats_interval_id
      WHERE i.end_time > DATEADD(day, -7, SYSDATETIMEOFFSET())
      GROUP BY rs.plan_id) AS r ON r.plan_id = p.plan_id
CROSS APPLY (SELECT TRY_CAST(p.query_plan AS xml) AS doc) AS c
CROSS APPLY (SELECT c.doc.value('(//*[@StatementSubTreeCost])[1]/@StatementSubTreeCost', 'float') AS cost,
                    c.doc.value('count(//*[@StatementSubTreeCost])', 'int') AS costed) AS x
OPTION (MAXDOP 1);`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []pcPlan
	for rows.Next() {
		var p pcPlan
		var par, marker int
		if err := rows.Scan(&p.id, &par, &marker, &p.cost, &p.costed, &p.bytes, &p.execs, &p.parRows,
			&p.parExecs, &p.minExecs, &p.cpu, &p.parCPU, &p.minCPU, &p.dopWeighted, &p.maxDOP); err != nil {
			t.Fatal(err)
		}
		p.parallel, p.marker = par == 1, marker == 1
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// pcSerialAboveOne counts the serial plans with a runtime row at max_dop
// above 1, the anomaly of point 4 of the spec.
func pcSerialAboveOne(plans []pcPlan) int {
	n := 0
	for _, p := range plans {
		if !p.parallel && p.parRows > 0 {
			n++
		}
	}
	return n
}

// pcParallel returns the parallel plans in 043's rank order: CPU at a degree
// above 1, highest first, then plan id.
func pcParallel(plans []pcPlan) []pcPlan {
	var out []pcPlan
	for _, p := range plans {
		if p.parallel {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].parCPU != out[j].parCPU {
			return out[i].parCPU > out[j].parCPU
		}
		return out[i].id < out[j].id
	})
	return out
}

func pcBandOf(p pcPlan) string {
	if !p.cost.Valid {
		return "unknown"
	}
	for _, b := range pcBandBounds {
		if p.cost.Float64 >= b.from && p.cost.Float64 < b.to {
			return b.name
		}
	}
	return "unknown"
}

// pcSeconds is 043's rounding of microseconds to a decimal(18,1) of seconds.
func pcSeconds(us float64) float64 { return math.Round(us/1e5) / 10 }

// pcCompareBands checks the document's seven bands against the plans that
// should fill them. Counts are exact. CPU figures are rounded per band on
// both sides, so they may differ by the last digit when the sums are taken in
// another order; 0.1 s is that digit.
func pcCompareBands(t *testing.T, doc map[string]any, want []pcPlan) {
	t.Helper()
	type agg struct {
		statements, parStatements, execs, parExecs, minExecs int64
		cpu, parCPU, minCPU, dopWeighted                     float64
		maxDOP                                               int64
	}
	exp := map[string]*agg{}
	for _, p := range want {
		name := pcBandOf(p)
		if exp[name] == nil {
			exp[name] = &agg{maxDOP: -1}
		}
		a := exp[name]
		a.statements++
		if p.parRows > 0 {
			a.parStatements++
		}
		a.execs += p.execs
		a.parExecs += p.parExecs
		a.minExecs += p.minExecs
		a.cpu += p.cpu
		a.parCPU += p.parCPU
		a.minCPU += p.minCPU
		a.dopWeighted += p.dopWeighted
		a.maxDOP = max(a.maxDOP, p.maxDOP)
	}
	bands := pcBands(t, doc)
	var names []string
	for _, b := range bands {
		names = append(names, b["band"].(string))
	}
	if !slices.Equal(names, []string{"lt_5", "5_25", "25_50", "50_100", "100_500", "ge_500", "unknown"}) {
		t.Fatalf("bands %v, want 042's seven in order", names)
	}
	for _, b := range bands {
		name := b["band"].(string)
		a := exp[name]
		if a == nil {
			a = &agg{maxDOP: -1}
		}
		for _, c := range []struct {
			key  string
			want int64
		}{
			{"statements", a.statements}, {"parallel_statements", a.parStatements},
			{"executions", a.execs}, {"parallel_executions", a.parExecs},
			{"parallel_executions_min", a.minExecs},
		} {
			if got := int64(pcNum(t, b[c.key], name+"."+c.key)); got != c.want {
				t.Errorf("band %s: %s = %d, want %d", name, c.key, got, c.want)
			}
		}
		for _, c := range []struct {
			key string
			us  float64
		}{{"cpu_s", a.cpu}, {"parallel_cpu_s", a.parCPU}, {"parallel_cpu_s_min", a.minCPU}} {
			if got := pcNum(t, b[c.key], name+"."+c.key); math.Abs(got-pcSeconds(c.us)) > 0.1+1e-9 {
				t.Errorf("band %s: %s = %.1f, want %.1f", name, c.key, got, pcSeconds(c.us))
			}
		}
		if a.statements == 0 {
			if b["max_dop"] != nil || b["avg_dop"] != nil {
				t.Errorf("band %s is empty and carries max_dop %v, avg_dop %v; want null", name, b["max_dop"], b["avg_dop"])
			}
			continue
		}
		if got := int64(pcNum(t, b["max_dop"], name+".max_dop")); got != a.maxDOP {
			t.Errorf("band %s: max_dop = %d, want %d", name, got, a.maxDOP)
		}
		if a.parExecs == 0 {
			if b["avg_dop"] != nil {
				t.Errorf("band %s: avg_dop = %v with no parallel execution, want null", name, b["avg_dop"])
			}
		} else if got, w := pcNum(t, b["avg_dop"], name+".avg_dop"), a.dopWeighted/float64(a.parExecs); math.Abs(got-w) > 0.01 {
			t.Errorf("band %s: avg_dop = %.2f, want %.2f", name, got, w)
		}
	}
}

// pcCheckShare checks that examined.share_of_parallel_cpu_pct is the bands'
// parallel CPU over window.parallel_cpu_s. Every figure is rounded to 0.1 s,
// the share to 0.1 %, so the share must lie inside the interval those
// roundings allow, and nowhere else.
func pcCheckShare(t *testing.T, doc map[string]any) {
	t.Helper()
	root := pcFlat(doc)
	total := pcNum(t, root["window.parallel_cpu_s"], "window.parallel_cpu_s")
	if total == 0 {
		if root["examined.share_of_parallel_cpu_pct"] != nil {
			t.Errorf("no parallel CPU in the window and a share of %v; want null", root["examined.share_of_parallel_cpu_pct"])
		}
		return
	}
	var sum float64
	var filled int
	for _, b := range pcBands(t, doc) {
		v := pcNum(t, b["parallel_cpu_s"], "parallel_cpu_s")
		sum += v
		if pcNum(t, b["statements"], "statements") > 0 {
			filled++
		}
	}
	slack := 0.05 * float64(filled)
	lo := 100*max(sum-slack, 0)/(total+0.05) - 0.05
	hi := 100*(sum+slack)/max(total-0.05, 0.05) + 0.05
	got := pcNum(t, root["examined.share_of_parallel_cpu_pct"], "examined.share_of_parallel_cpu_pct")
	if got < lo || got > hi {
		t.Errorf("share_of_parallel_cpu_pct = %.1f; the bands hold %.1f s of %.1f s, which allows %.1f to %.1f",
			got, sum, total, lo, hi)
	}
}

// pcSettings is the instance's threshold and scheduler count, for the
// message of a fixture that came out too thin.
func pcSettings(t *testing.T, admin *sql.DB) string {
	t.Helper()
	var threshold, schedulers int
	if err := admin.QueryRow("SELECT CAST(value_in_use AS int) FROM sys.configurations " +
		"WHERE name = N'cost threshold for parallelism';").Scan(&threshold); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow("SELECT scheduler_count FROM sys.dm_os_sys_info;").Scan(&schedulers); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("cost threshold for parallelism %d, %d schedulers", threshold, schedulers)
}

// pcReplicasKnown says whether this instance can tell replica groups apart:
// the column (SQL Server 2022) and the view (SQL Server 2025) both exist.
func pcReplicasKnown(t *testing.T, admin *sql.DB) (known bool, major int) {
	t.Helper()
	var col, view sql.NullInt64
	var version string
	if err := admin.QueryRow("SELECT COL_LENGTH('sys.query_store_runtime_stats', 'replica_group_id'), "+
		"OBJECT_ID('sys.query_store_replicas'), CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128));").
		Scan(&col, &view, &version); err != nil {
		t.Fatal(err)
	}
	major, _ = strconv.Atoi(strings.SplitN(version, ".", 2)[0])
	return col.Valid && view.Valid, major
}

func TestLiveQueryStoreParallelCost(t *testing.T) {
	cfg, admin := pcAdmin(t)
	name := "ZzAvgDopLive" + pcSuffix()
	pcCreateDatabase(t, admin, name, "", false)
	pcLadder(t, cfg, name)

	// Test 3 of the spec, its assertions in its order.
	t.Run("bands", func(t *testing.T) {
		before := pcOracle(t, admin, name)
		if n := len(pcParallel(before)); n < 5 {
			t.Fatalf("the store holds %d parallel plans, want at least 5 (%s): the fixture is too thin "+
				"on this instance to tell the bands apart", n, pcSettings(t, admin))
		}
		// The premise is read from the store, and the document is then held
		// to it: read from the document alone, a 043 that excluded every row
		// would blame the fixture.
		anomalies := pcSerialAboveOne(before)
		if anomalies < 1 {
			t.Fatalf("the store holds no serial plan with max_dop above 1: the #savings anomaly did not "+
				"reproduce (%s), so a selection on max_dop could not be told from one on is_parallel_plan",
				pcSettings(t, admin))
		}
		_, doc := pcRun(t, cfg, parallelCostScript(t, nil), name)
		root := pcFlat(doc)
		if n := pcNum(t, root["window.serial_plans_dop_above_1"], "window.serial_plans_dop_above_1"); n < 1 {
			t.Errorf("window.serial_plans_dop_above_1 = %v while the store holds %d such plans", n, anomalies)
		}
		known, major := pcReplicasKnown(t, admin)
		if major >= 17 && !known {
			t.Errorf("SQL Server %d and the replica column or view was not found", major)
		}
		switch got := root["excluded.other_replicas_executions"]; {
		case known && got != float64(0):
			t.Errorf("excluded.other_replicas_executions = %v, want 0: the groups can be told apart here and the lab has no other replica", got)
		case !known && got != nil:
			t.Errorf("excluded.other_replicas_executions = %v, want null: this instance cannot tell replica groups apart", got)
		}
		plans := pcOracle(t, admin, name)
		parallel := pcParallel(plans)
		pcCompareBands(t, doc, parallel)
		if got := int(pcNum(t, root["window.parallel_plans"], "window.parallel_plans")); got != len(parallel) {
			t.Errorf("window.parallel_plans = %d, the store holds %d", got, len(parallel))
		}
		for _, p := range plans {
			if p.costed > 1 {
				t.Errorf("plan %d holds %d costed statement elements; the text search reads the first one only", p.id, p.costed)
			}
			// 042 calls a plan parallel when an operator carries Parallel="1";
			// 043 reads is_parallel_plan. On a plan with a cost, they must agree.
			if p.cost.Valid && p.parallel != p.marker {
				t.Errorf("plan %d: is_parallel_plan %v but an operator marked Parallel=\"1\" %v", p.id, p.parallel, p.marker)
			}
		}
		for _, b := range pcBands(t, doc) {
			if pcNum(t, b["parallel_executions_min"], "min") > pcNum(t, b["parallel_executions"], "max") {
				t.Errorf("band %v: the lower bound is above the upper one", b["band"])
			}
		}
		pcCheckShare(t, doc)
	})
}
```

- [ ] Step 3: Run the offline test and count, in one invocation.

```bash
cd <worktree> && export GOTMPDIR=/var/tmp/sqa-avgdop && gofmt -l collect ; /usr/local/go/bin/go vet ./collect/ && /usr/local/go/bin/go test ./collect/ -run '^TestParallelCostConstantsAreRewrittenOnce$' -count=1 -v 2>&1 | /usr/bin/grep -c -- '--- PASS' && /usr/local/go/bin/go test -list '.*' ./collect/ | /usr/bin/grep -c '^Test'
```

Expected: 1, then the `collect` count measured in Task 1 plus 2 (520 on `f83d882`).

- [ ] Step 4: Run the live test, in one invocation. Nothing else may run on the lab meanwhile.

```bash
cd <worktree> && export TMPDIR=/var/tmp/sqa-avgdop GOTMPDIR=/var/tmp/sqa-avgdop SQL_AUDITOR_LIVE_SERVER=localhost,11533 SQL_AUDITOR_LIVE_USER=sa SQL_AUDITOR_LIVE_PASSWORD="$(podman exec sql2025 printenv MSSQL_SA_PASSWORD)" && /usr/local/go/bin/go test ./collect/ -run '^TestLiveQueryStoreParallelCost$' -count=1 -v -timeout 20m 2>&1 | tee /var/tmp/sqa-avgdop/live.log | /usr/bin/grep -E -- '--- (PASS|FAIL|SKIP)'
```

Expected: exactly 2 `--- PASS` lines (the test and `bands`), no FAIL, no SKIP; about 10 s. A SKIP means the variables did not reach the test. If the first assertion fails on the fixture's size, report the message (it names the threshold and the scheduler count) and stop: do not shrink the fixture.

- [ ] Step 5: Break steps, each against `-run '^TestLiveQueryStoreParallelCost$/^bands$'` with the environment of Step 4, each undone from a copy kept aside.

| Mutation (in 043) | Measured failure |
| --- | --- |
| in the pin, `WHERE r.is_parallel_plan = 1` written `WHERE r.max_dop > 1` | `band lt_5: statements = 1, want 0` (the anomaly enters a band) |
| `'replica_group_id'` written `'replica_group_idx'` | `excluded.other_replicas_executions = <nil>, want 0` |
| in the literal, `r.role_type <> 1` written `r.role_type = 1` | `window.serial_plans_dop_above_1 = 0 while the store holds 1 such plans`, `excluded.other_replicas_executions = 61, want 0`, `band 5_25: statements = 0, want 8` |

The fourth mutation of the spec's test 3, a share computed from anything but the bands, cannot fail here: the store is read whole, so every such share is 100 (measured). Task 5 catches it, on a store read in part.

- [ ] Step 6: Check that the lab holds no test database, then commit.

```bash
podman exec sql2025 bash -c '/opt/mssql-tools18/bin/sqlcmd -C -I -S localhost -U sa -P "$MSSQL_SA_PASSWORD" -W -h -1 -Q "SELECT name FROM sys.databases WHERE name LIKE N'"'"'ZzAvgDop%'"'"';"'
git add collect/parallelcost_test.go collect/parallelcost_live_test.go
git commit -m "Test the Query Store parallel cost bands against the store itself

The bands are what a cost threshold is chosen from, so the live test builds
the spec's ladder of heaps in a database of its own, runs the collector
through runUnit, and recomputes every band from the store with a reading
that shares nothing with the collector's: the cost through the xml path on
the first costed statement element, the runtime figures summed by the test.
Its premises are read from the store before anything else, so a fixture too
thin on another instance fails with the instance's threshold and scheduler
count in its message rather than passing on nothing."
```

Expected first line of the check: `(0 rows affected)`.

### Task 4: The document's keys and the planted literal

Files:
- Modify: `collect/parallelcost_live_test.go`

Interfaces:
- Consumes: Task 3's helpers and `pcRootKeys`, `pcBandKeys`.

- [ ] Step 1: Add `"bytes"` to the imports of `collect/parallelcost_live_test.go`, first in the list, and add this subtest inside `TestLiveQueryStoreParallelCost`, after the `bands` subtest and before the function's closing brace:

```go
	// Test 2 of the spec: nothing that names a query leaves the server.
	t.Run("no text leaves", func(t *testing.T) {
		q := quoteName(name)
		var kept int
		if err := admin.QueryRow("SELECT COUNT(*) FROM " + q + ".sys.query_store_query_text AS qt " +
			"JOIN " + q + ".sys.query_store_query AS qq ON qq.query_text_id = qt.query_text_id " +
			"JOIN " + q + ".sys.query_store_plan AS p ON p.query_id = qq.query_id " +
			"WHERE p.is_parallel_plan = 1 AND qt.query_sql_text LIKE N'%ZZ043[_]PLANTED[_]TEXT%' " +
			"OPTION (MAXDOP 1);").Scan(&kept); err != nil {
			t.Fatal(err)
		}
		if kept == 0 {
			t.Fatal("the store kept the planted literal in no parallel plan's query: a literal the store " +
				"never held cannot leak, so this would test nothing")
		}
		raw, doc := pcRun(t, cfg, parallelCostScript(t, nil), name)
		var got []string
		for k := range pcFlat(doc) {
			got = append(got, k)
		}
		slices.Sort(got)
		want := slices.Sorted(slices.Values(pcRootKeys))
		if !slices.Equal(got, want) {
			t.Errorf("root keys\n got  %v\n want %v", got, want)
		}
		wantBand := slices.Sorted(slices.Values(pcBandKeys))
		for _, b := range pcBands(t, doc) {
			var keys []string
			for k := range b {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			if !slices.Equal(keys, wantBand) {
				t.Errorf("band %v keys\n got  %v\n want %v", b["band"], keys, wantBand)
			}
		}
		if bytes.Contains(raw, []byte("ZZ043_PLANTED_TEXT")) {
			t.Error("the planted literal is in the document")
		}
	})
```

- [ ] Step 2: Run, in one invocation, the command of Task 3 Step 4. Expected: exactly 3 `--- PASS` lines, no FAIL, no SKIP. The `collect` test count is unchanged from Task 3.

- [ ] Step 3: Break steps, against `-run '^TestLiveQueryStoreParallelCost$/^no text leaves$'`, each undone.

| Mutation (in 043) | Measured failure |
| --- | --- |
| in the bands, a line `MAX(z.plan_id) AS [plan_id],` added before `COUNT(z.plan_id) ... AS [statements],` (the spec's mutation: a plan id projected) | `band lt_5 keys` and the six other bands |
| `@not_read AS [state.not_read_because],` replaced by `(SELECT TOP (1) qt.query_sql_text FROM sys.query_store_query_text AS qt WHERE qt.query_sql_text LIKE N'%PLANTED%') AS [state.not_read_because],` | `the planted literal is in the document`, alone: the key set is unchanged, so this proves the literal check checks something on its own |

- [ ] Step 4: Commit.

```bash
git add collect/parallelcost_live_test.go
git commit -m "Hold the Query Store parallel cost document to its declared keys

The collector runs by default because nothing that names a query leaves the
server, so that is tested on the output, not on the SQL: the root and every
band must carry exactly the keys the spec lists, and a literal planted in a
parallel query must appear nowhere in the document. The test first checks
that the store kept the literal, since a literal it never held could not
leak and the test would assert nothing."
```

### Task 5: The stop rules

Files:
- Modify: `collect/parallelcost_live_test.go`

Interfaces:
- Consumes: `parallelCostScript` with `@chunk`, `@budget_bytes`, `@budget_ms` and `@cap` rewritten; `pcCompareBands`, `pcCheckShare`, `pcParallel`.

- [ ] Step 1: Add this subtest inside `TestLiveQueryStoreParallelCost`, after `no text leaves` and before the closing brace:

```go
	// Test 5 of the spec: the stop rules, on the same store.
	t.Run("stop", func(t *testing.T) {
		ranked := pcParallel(pcOracle(t, admin, name))
		if len(ranked) < 5 {
			t.Fatalf("the store holds %d parallel plans, want at least 5: the rules could not be told apart", len(ranked))
		}
		check := func(t *testing.T, doc map[string]any, stoppedBy any, read int, truncated float64) {
			t.Helper()
			root := pcFlat(doc)
			if got := root["examined.stopped_by"]; got != stoppedBy {
				t.Errorf("examined.stopped_by = %v, want %v", got, stoppedBy)
			}
			if got := int(pcNum(t, root["examined.plans_read"], "examined.plans_read")); got != read {
				t.Errorf("examined.plans_read = %d, want %d", got, read)
			}
			if got := pcNum(t, root["truncated"], "truncated"); got != truncated {
				t.Errorf("truncated = %v, want %v", got, truncated)
			}
		}
		t.Run("bytes after one chunk", func(t *testing.T) {
			_, doc := pcRun(t, cfg, parallelCostScript(t, map[string]int64{"@chunk": 2, "@budget_bytes": 1}), name)
			check(t, doc, "bytes", 2, 1)
			first := pcParallel(pcOracle(t, admin, name))[:2]
			pcCompareBands(t, doc, first)
			want := max(first[0].bytes, first[1].bytes)
			if got := int64(pcNum(t, pcFlat(doc)["examined.largest_plan_bytes"], "examined.largest_plan_bytes")); got != want {
				t.Errorf("examined.largest_plan_bytes = %d, want %d, the larger of the two plans read", got, want)
			}
			// The share is the bands' over the window's. With two plans of
			// several read, it is below 100, so a share taken from anything
			// but the bands shows here, where a store read whole cannot.
			pcCheckShare(t, doc)
		})
		t.Run("bytes budget zero", func(t *testing.T) {
			_, doc := pcRun(t, cfg, parallelCostScript(t, map[string]int64{"@budget_bytes": 0}), name)
			check(t, doc, "bytes", 0, 1)
			pcCompareBands(t, doc, nil)
		})
		t.Run("time budget zero", func(t *testing.T) {
			_, doc := pcRun(t, cfg, parallelCostScript(t, map[string]int64{"@budget_ms": 0}), name)
			check(t, doc, "time", 0, 1)
			pcCompareBands(t, doc, nil)
		})
		t.Run("both budgets zero", func(t *testing.T) {
			_, doc := pcRun(t, cfg, parallelCostScript(t, map[string]int64{"@budget_bytes": 0, "@budget_ms": 0}), name)
			check(t, doc, "time", 0, 1)
		})
		t.Run("cap below the plans", func(t *testing.T) {
			_, doc := pcRun(t, cfg, parallelCostScript(t, map[string]int64{"@cap": 2}), name)
			check(t, doc, "cap", 2, 1)
			if got := pcNum(t, pcFlat(doc)["examined.plans"], "examined.plans"); got != 2 {
				t.Errorf("examined.plans = %v, want the cap, 2", got)
			}
		})
		t.Run("cap at the plans", func(t *testing.T) {
			n := len(pcParallel(pcOracle(t, admin, name)))
			_, doc := pcRun(t, cfg, parallelCostScript(t, map[string]int64{"@cap": int64(n)}), name)
			check(t, doc, nil, n, 0)
		})
	})
```

- [ ] Step 2: Run, in one invocation, the command of Task 3 Step 4. Expected: exactly 10 `--- PASS` lines (the test, `bands`, `no text leaves`, `stop` and its six cases), no FAIL, no SKIP.

- [ ] Step 3: Break steps, against `-run '^TestLiveQueryStoreParallelCost$/^stop$'`, each undone.

| Mutation (in 043) | Measured failure |
| --- | --- |
| `WHERE k.rn >= @lo AND k.rn < @lo + @chunk` written `... < @lo + 1000000` (a loop that ignores `@chunk`) | `examined.plans_read = 8, want 2` |
| `TOP (@cap + 1)` written `TOP (@cap)` | `cap below the plans`: `examined.stopped_by = <nil>, want cap` |
| the bytes check copied before the time check (`IF @bytes_read >= @budget_bytes BEGIN SET @stopped_by = 'bytes'; BREAK; END` inserted above `IF DATEDIFF(...`) | `both budgets zero`: `examined.stopped_by = bytes, want time` |
| `100.0 * ISNULL(b.banded_par_cpu_us, 0)` written `100.0 * ISNULL(a.par_cpu_us, 0)` (a share not taken from the bands) | `bytes after one chunk`: `share_of_parallel_cpu_pct = 100.0; the bands hold 0.9 s of 1.8 s, which allows 45.9 to 54.3` |
| `DECLARE @loop_started datetime2 = SYSDATETIME()` written `datetime2(3)` | `time budget zero`: `examined.stopped_by = <nil>, want time`. This one depends on rounding and fails about half the runs; run it three times and report how many failed |

The fourth row is the spec's test 3 mutation that test 3 cannot see; it is caught here.

- [ ] Step 4: Commit.

```bash
git add collect/parallelcost_live_test.go
git commit -m "Test the stop rules of the Query Store parallel cost collector

The budgets are what let a slow instance return a partial, counted answer
before the file's timeout, and the order of the checks decides which budget
is named. Each rule is run on the test store with its constant rewritten:
one chunk of two plans past a one-byte budget, each budget at zero, both at
zero, and the cap below and at the number of parallel plans. The case read
in part is also where the share of parallel CPU can differ from 100, so it is
where a share taken from anything but the bands is caught."
```

### Task 6: Stores read only, off, missing, and serial only

Files:
- Modify: `collect/parallelcost_live_test.go`

Interfaces:
- Produces: `pcNotReadKeys() []string`, `pcNotRead(t, doc, actual any, reason string)`, `TestLiveQueryStoreParallelCostStoresNotRead`.

- [ ] Step 1: Add these two subtests inside `TestLiveQueryStoreParallelCost`, after `stop` and before the closing brace. `off store` must stay last: it turns the store off.

```go
	// A store in READ_ONLY is read like one in READ_WRITE (Review Focus).
	t.Run("read only store", func(t *testing.T) {
		pcExec(t, admin, "ALTER DATABASE "+quoteName(name)+" SET QUERY_STORE (OPERATION_MODE = READ_ONLY);")
		_, doc := pcRun(t, cfg, parallelCostScript(t, nil), name)
		root := pcFlat(doc)
		if root["state.actual"] != "READ_ONLY" || root["state.not_read_because"] != nil {
			t.Fatalf("state.actual %v, not_read_because %v; want READ_ONLY, read", root["state.actual"], root["state.not_read_because"])
		}
		pcCompareBands(t, doc, pcParallel(pcOracle(t, admin, name)))
	})

	// Test 6 of the spec, its second case: a store that held parallel plans,
	// then set OFF, still returns them, and must not be read. Last, since it
	// turns the store off.
	t.Run("off store", func(t *testing.T) {
		pcExec(t, admin, "ALTER DATABASE "+quoteName(name)+" SET QUERY_STORE = OFF;")
		_, doc := pcRun(t, cfg, parallelCostScript(t, nil), name)
		pcNotRead(t, doc, "OFF", "off")
	})
```

and append at the end of the file:

```go
// pcNotReadKeys are the root keys that are NULL when a store is not read:
// everything but the identity, the state, the constants and the window's
// definition.
func pcNotReadKeys() []string {
	keep := map[string]bool{
		"database": true, "collected_at": true, "schedulers": true,
		"state.actual": true, "state.capture_mode": true, "state.interval_minutes": true,
		"state.not_read_because": true, "window.days": true, "window.from": true,
		"cap": true, "chunk": true, "budget.bytes": true, "budget.ms": true,
	}
	var out []string
	for _, k := range pcRootKeys {
		if !keep[k] {
			out = append(out, k)
		}
	}
	return out
}

// pcNotRead checks a document of a store that was not read: the state and
// the reason, every count NULL, and seven empty bands.
func pcNotRead(t *testing.T, doc map[string]any, actual any, reason string) {
	t.Helper()
	root := pcFlat(doc)
	if root["state.actual"] != actual || root["state.not_read_because"] != reason {
		t.Errorf("state.actual %v, not_read_because %v; want %v, %s", root["state.actual"], root["state.not_read_because"], actual, reason)
	}
	for _, k := range pcNotReadKeys() {
		if root[k] != nil {
			t.Errorf("%s = %v on a store that was not read, want null", k, root[k])
		}
	}
	pcCompareBands(t, doc, nil)
}

// Test 6 of the spec, its first and third cases.
func TestLiveQueryStoreParallelCostStoresNotRead(t *testing.T) {
	cfg, admin := pcAdmin(t)

	// master has no row in the options view: the "never enabled" case,
	// which a new database cannot be on SQL Server 2022 and later.
	t.Run("master", func(t *testing.T) {
		_, doc := pcRun(t, cfg, parallelCostScript(t, nil), "master")
		pcNotRead(t, doc, nil, "no store")
	})

	// A store that is on and holds serial work only. Case-sensitive, so that
	// an identifier written in the wrong case fails here, and named with a
	// space and a closing bracket, so that a name quoted wrongly does
	// (Review Focus). Automatic statistics are off: a StatMan query can be
	// stored as a parallel plan, and this store must hold none.
	t.Run("serial only", func(t *testing.T) {
		name := "ZzAvgDopLive Serial]" + pcSuffix()
		pcCreateDatabase(t, admin, name, "Latin1_General_CS_AS", true)
		w := pcOpen(t, cfg, name)
		pcExec(t, w, "CREATE TABLE dbo.Serial1 (id bigint NOT NULL, pad char(100) NOT NULL);")
		pcExec(t, w, "INSERT INTO dbo.Serial1 WITH (TABLOCK) (id, pad) "+
			"SELECT TOP (500000) ROW_NUMBER() OVER (ORDER BY (SELECT NULL)), 'x' "+
			"FROM sys.all_columns AS a CROSS JOIN sys.all_columns AS b CROSS JOIN sys.all_columns AS c "+
			"OPTION (MAXDOP 1);")
		for range 5 {
			pcExec(t, w, "SELECT COUNT_BIG(*) FROM dbo.Serial1 WHERE id % 7 = 3 OPTION (MAXDOP 1);")
		}
		pcExec(t, w, "EXEC sys.sp_query_store_flush_db;")
		if n := len(pcParallel(pcOracle(t, admin, name))); n != 0 {
			t.Fatalf("the serial store holds %d parallel plans; the case needs none", n)
		}
		_, doc := pcRun(t, cfg, parallelCostScript(t, nil), name)
		root := pcFlat(doc)
		if root["state.actual"] != "READ_WRITE" || root["state.not_read_because"] != nil {
			t.Fatalf("state.actual %v, not_read_because %v; want READ_WRITE, read", root["state.actual"], root["state.not_read_because"])
		}
		for _, k := range []string{
			"window.parallel_plans", "window.parallel_plan_cpu_s", "window.parallel_executions",
			"window.parallel_cpu_s", "window.parallel_executions_min", "window.parallel_cpu_s_min",
			"nested.parallel_plans", "nested.parallel_cpu_s",
			"examined.plans", "examined.plans_read", "examined.bytes_read",
		} {
			if root[k] != float64(0) {
				t.Errorf("%s = %v, want 0 and not null: the store was read and holds no parallel plan", k, root[k])
			}
		}
		if root["examined.share_of_parallel_cpu_pct"] != nil {
			t.Errorf("examined.share_of_parallel_cpu_pct = %v, want null with no parallel CPU", root["examined.share_of_parallel_cpu_pct"])
		}
		for _, k := range []string{"window.executions", "window.cpu_s"} {
			if pcNum(t, root[k], k) <= 0 {
				t.Errorf("%s = %v, want above 0: the serial work and 043's own statements are in it", k, root[k])
			}
		}
		pcCompareBands(t, doc, nil)
	})
}
```

- [ ] Step 2: Run both live tests, in one invocation.

```bash
cd <worktree> && export TMPDIR=/var/tmp/sqa-avgdop GOTMPDIR=/var/tmp/sqa-avgdop SQL_AUDITOR_LIVE_SERVER=localhost,11533 SQL_AUDITOR_LIVE_USER=sa SQL_AUDITOR_LIVE_PASSWORD="$(podman exec sql2025 printenv MSSQL_SA_PASSWORD)" && /usr/local/go/bin/go test ./collect/ -run '^TestLiveQueryStoreParallelCost' -count=1 -v -timeout 20m 2>&1 | tee /var/tmp/sqa-avgdop/live.log | /usr/bin/grep -E -- '--- (PASS|FAIL|SKIP)' && /usr/local/go/bin/go test -list '.*' ./collect/ | /usr/bin/grep -c '^Test'
```

Expected: exactly 15 `--- PASS` lines (12 for the first test, 3 for the second), no FAIL, no SKIP, about 30 s; then the `collect` count of Task 3 plus 1 (521 on `f83d882`).

- [ ] Step 3: Break steps, each undone.

| Mutation (in 043) | Filter | Measured failure |
| --- | --- | --- |
| `DECLARE @read bit = CASE WHEN @not_read IS NULL THEN 1 ELSE 0 END;` written `... WHEN @not_read IS NULL OR @not_read = N'off' THEN 1 ...` (the OFF store read into the bands) | `'^TestLiveQueryStoreParallelCost$/^off store$'` | `window.oldest_interval = ... on a store that was not read, want null`, every count after it, and the bands |
| `NULLIF(ISNULL(a.par_cpu_us, 0), 0)` written `ISNULL(a.par_cpu_us, 0)` | `'^TestLiveQueryStoreParallelCostStoresNotRead$'` | `serial only`: `mssql: Divide by zero error encountered.` |
| `JOIN sys.query_store_plan  AS p` written `JOIN sys.Query_Store_Plan  AS p` | the same | `serial only`: `Invalid object name 'sys.Query_Store_Plan'` (Review Focus 1) |
| `WHEN @has_options = 0        THEN N'no store'` written `WHEN 1 = 0 THEN N'no store'` | the same | `master`: `not_read_because <nil>; want <nil>, no store` |

- [ ] Step 4: Check the lab holds no `ZzAvgDop` database (Task 3 Step 6), then run `/usr/local/go/bin/go test ./... -count=1` and commit.

```bash
git add collect/parallelcost_live_test.go
git commit -m "Test the stores the Query Store parallel cost collector must not read

A store that is off still returns its data, so the rule is a predicate in
the file and is tested on a store that held parallel plans before it was
switched off. master stands for a database with no store, which a new
database cannot be on SQL Server 2022 and later. A store that is on and
holds only serial work must give zeros and not NULLs, and a share that is
NULL rather than a division by zero, which would lose the whole root. That
store is case-sensitive and its name needs quoting, so an identifier in the
wrong case or a name quoted wrongly fails there; a read-only store is read
like any other."
```

### Task 7: CI for ci_probe's document and the live tests

Files:
- Modify: `.github/workflows/ci.yml`

The owner decided that max-duration's live tests join the integration job; 043's join it the same way, as a separate step after `max-duration live tests`. They create and drop their own databases (`ZzAvgDopLive...`), not `ci_probe`. The fixture's premises were measured on SQL Server 2025 with 22 schedulers only; the CI matrix is 2017 and 2022 on runners of a few cores. If the step fails there on a premise (fewer than five parallel plans, or no `#savings` anomaly), that is a measurement: report it to the owner with the log, and do not weaken the test (ambiguity 8).

- [ ] Step 1: In the step `assert the run is complete`, after the `jq` block that checks `029.query-store-load-profile.json` (it ends with `jq -c '.intervals[-3:]' ...; exit 1; }`) and before the comment that begins `# An \`if\`, not \`cmd && { ... }\``, insert:

```yaml
          # 043 is a third Query Store collector without @discloses, run by
          # default, so its key sets are asserted exactly, as 027's and 029's
          # are: a text or id column added anywhere fails here. ci_probe's
          # store is on and small, so it is read whole: nothing stops the read,
          # and every band statement is a plan the loop reached. excluded is
          # null on 2017 and 2022, which cannot tell replica groups apart, and
          # would be 0 on a 2025 image.
          jq -e '(keys_unsorted | sort) == (["bands","budget","cap","chunk","collected_at","database","examined",
                     "excluded","nested","schedulers","selection","state","truncated","unknown","window"] | sort)
                 and (.state | keys_unsorted | sort) == (["actual","capture_mode","interval_minutes","not_read_because"] | sort)
                 and (.window | keys_unsorted | sort) == (["cpu_s","days","dop_above_schedulers","executions","from",
                     "intervals","newest_interval","oldest_interval","parallel_cpu_s","parallel_cpu_s_min",
                     "parallel_executions","parallel_executions_min","parallel_plan_cpu_s","parallel_plans",
                     "runtime_rows","scalar_function_cpu_s","serial_plans_dop_above_1","store_oldest_interval"] | sort)
                 and (.examined | keys_unsorted | sort) == (["bytes_read","duration_ms","largest_plan_bytes","plans",
                     "plans_read","share_of_parallel_cpu_pct","stopped_by"] | sort)
                 and (.nested | keys_unsorted | sort) == (["parallel_cpu_s","parallel_plans"] | sort)
                 and (.excluded | keys_unsorted | sort) == (["other_replicas_cpu_s","other_replicas_executions"] | sort)
                 and (.budget | keys_unsorted | sort) == (["bytes","ms"] | sort)
                 and (.selection | keys_unsorted) == ["duration_ms"]
                 and (.unknown | keys_unsorted | sort) == (["no_cost","no_plan"] | sort)
                 and ([.bands[].band] == ["lt_5","5_25","25_50","50_100","100_500","ge_500","unknown"])
                 and all(.bands[]; (keys_unsorted | sort) == (["avg_dop","band","cost_from","cost_to","cpu_s",
                     "executions","max_dop","parallel_cpu_s","parallel_cpu_s_min","parallel_executions",
                     "parallel_executions_min","parallel_statements","statements"] | sort)
                     and .parallel_statements <= .statements
                     and .parallel_executions_min <= .parallel_executions)
                 and .state.actual == "READ_WRITE" and .state.not_read_because == null
                 and .cap == 1000 and .chunk == 100
                 and .window.days == 7 and .window.executions >= 1 and .window.intervals >= 1
                 and (.window.serial_plans_dop_above_1 | type) == "number"
                 and ((.excluded.other_replicas_executions | type) == "null"
                     or .excluded.other_replicas_executions == 0)
                 and .examined.plans <= .cap and .examined.plans_read <= .examined.plans
                 and ([.bands[].statements] | add) == .examined.plans_read + .unknown.no_plan
                 and .examined.stopped_by == null and .truncated == 0' \
            "$run_dir/80.workload/ci_probe/043.query-store-parallel-cost.json" \
            || { jq '.' "$run_dir/80.workload/ci_probe/043.query-store-parallel-cost.json"; exit 1; }
          if grep -q 'sqlserver/2004/07/showplan' \
               "$run_dir/80.workload/ci_probe/043.query-store-parallel-cost.json"; then
            echo "043 emitted showplan XML" >&2
            exit 1
          fi
```

The `jq` expression is a single-quoted string spanning lines, as the 027 and 029 blocks are; keep its indentation inside the quotes as above. It was run on a real 043 document from the lab (`true`), and refused one with `cap` 999 and one with an extra `window.plan_id` (`false`).

- [ ] Step 2: After the step `max-duration live tests`, at the end of the job, add:

```yaml
      # 043's live tests, against the same container. They build their own
      # databases (ZzAvgDopLive...) with a ladder of heaps up to 2 000 000
      # rows and drop them; ci_probe is not touched. The fixture's premises
      # were measured on SQL Server 2025 only: a failure on them here is a
      # measurement of this image to report, not a test to loosen. Subtests
      # are indented in -v output, so SKIP and FAIL are matched anywhere.
      - name: query store parallel cost live tests
        env:
          SQL_AUDITOR_LIVE_SERVER: localhost,1433
          SQL_AUDITOR_LIVE_USER: sa
          SQL_AUDITOR_LIVE_PASSWORD: ${{ env.SQL_PASSWORD }}
        run: |
          set -o pipefail
          go test ./collect/ -run '^TestLiveQueryStoreParallelCost' -count=1 -v -timeout 20m | tee "$RUNNER_TEMP/avgdop.log"
          grep -q '^--- PASS' "$RUNNER_TEMP/avgdop.log"
          if grep -q -- '--- SKIP' "$RUNNER_TEMP/avgdop.log"; then exit 1; fi
          if grep -q -- '--- FAIL' "$RUNNER_TEMP/avgdop.log"; then exit 1; fi
```

- [ ] Step 3: Check that the file still parses.

```bash
cd <worktree> && python3 -c "import yaml; yaml.safe_load(open('.github/workflows/ci.yml')); print('yaml ok')"
```

Expected: `yaml ok`. The `jq` expression was verified when this plan was written. If you change it, verify it on a real document before committing: run `sql-auditor collect` against the lab with `DB_INCLUDE` set to a `ZzAvgDop...` database whose store is on, run `jq -e '<expression>'` on `80.workload/<db>/043.query-store-parallel-cost.json` of the run folder, and drop the database.

- [ ] Step 4: Commit. CI runs only once the owner pushes; say so in the report.

```bash
git add .github/workflows/ci.yml
git commit -m "Check the Query Store parallel cost collector in the integration job

The collector runs by default and promises that nothing naming a query
leaves the server, so the CI collection asserts its key sets exactly on
ci_probe, as it does for the two other Query Store collectors without a
disclosure. Its live tests join the job as the max-duration ones did, in a
step of their own that fails on a skip, and run on SQL Server 2017 and 2022,
where the fixture has not been measured yet."
```

### Task 8: The documents

Files:
- Modify: `docs/dba-guide.md`, `docs/caps-inventory.md`, `README.md`, `CHANGELOG.md`, `docs/query-store-parallel-cost-spec.md`

No bold, no em or en dash in any line added. Run `/usr/local/go/bin/go test . -count=1` after the guide edits: `TestTheGuideCountsTheTimeoutTiersOfTheCorpus` and `TestTheGuideListsEveryOptionalPermission` read that file.

- [ ] Step 1: `docs/dba-guide.md`, the illustrative `check` listing. After the line `  80.workload/042.parallel-cost-distribution.sql SQL Server 13+` add:

```text
  80.workload/043.query-store-parallel-cost.sql per database, SQL Server 13+
```

- [ ] Step 2: `docs/dba-guide.md`, "### What the default run costs a large instance". After the row whose first cell is `Statement costs read from cached plans`, add the row the spec gives in "The cost, measured", verbatim:

```text
| Parallel plan costs read out of the Query Store | `80.workload/043.query-store-parallel-cost.sql` | in each database whose store is not off, and which is not an availability group secondary, two parts. First one aggregation of the last seven days of `sys.query_store_runtime_stats`, grouped by plan into a temporary table; it reads no plan text and has no budget of its own, so its cost follows the number of runtime rows in the window: 29 to 105 ms on six lab stores of up to 9 086 rows. Then the text of up to 1 000 parallel plans, those with the most parallel CPU first, copied a hundred at a time into a table variable and searched for one attribute, never converted to `xml`: about 20 ms per MB of plan text on one 2025 lab build. The read stops before the next hundred once 100 MB have been read or 10 s have passed, so one hundred plans can pass either, by as much as their size; the 120-second timeout is the hard bound of the whole file. The store's shared lock is released between chunks. The root projects `selection.duration_ms`, `window.runtime_rows`, `examined.duration_ms`, `examined.bytes_read`, `examined.largest_plan_bytes` and `examined.stopped_by`, so the cost on your instance is in the archive. Nothing that names a query leaves the server. |
```

- [ ] Step 3: `docs/dba-guide.md`, "#### Narrowing the databases". After the paragraph that begins "`--query-store-databases` narrows which of the collected databases the extraction reads", add:

```text
It narrows the extraction only. The Query Store collectors that run by
default, `80.workload/043.query-store-parallel-cost.sql` among them, read
every collected database whose store is on; `DB_INCLUDE` and `DB_EXCLUDE`
narrow them as they narrow every per-database collector.
```

- [ ] Step 4: `docs/dba-guide.md`, a section after "## When the load happens" and before "## `--query-store-compare-at`":

```text
## Parallel plan costs from the Query Store

`80.workload/042.parallel-cost-distribution.sql` puts the cached statements
with the most CPU into bands of estimated cost and says, per band, how much
of their work ran in parallel: it is what a value for `cost threshold for
parallelism` is chosen from. A cache under memory pressure holds little, and
a restart or most `sp_configure` changes empty it, so
`80.workload/043.query-store-parallel-cost.sql` reads the same bands from the
Query Store of each database, over the last seven days.

Read it with these in mind:

- Only plans the store marks parallel (`is_parallel_plan`) are banded. A
  serial plan can report a degree above 1; such plans are counted in
  `window.serial_plans_dop_above_1` and never banded.
- Each band gives two bounds. `parallel_executions` and `parallel_cpu_s` are
  over the store's rows that reached a degree above 1, an upper bound;
  `parallel_executions_min` and `parallel_cpu_s_min` over the rows whose
  every execution ran parallel, a lower bound. The cache can only give the
  first.
- A store that is off, a database without a store, and an availability group
  secondary are not read, and `state.not_read_because` says which. From SQL
  Server 2025, rows recorded for another replica role are left out and
  counted under `excluded`.
- At most 1 000 plans are read per database, a hundred at a time, and the
  read stops between hundreds after 100 MB of plan text or 10 seconds.
  `examined.stopped_by` says whether it stopped, and
  `examined.share_of_parallel_cpu_pct` what share of the parallel work the
  bands hold.
- The store records the audit's own statements. They are in the serial
  totals and in `window.intervals` and `window.newest_interval`, so a recent
  newest interval is not evidence that the applications ran recently. Every
  statement of the corpus that reads rows is held to `MAXDOP 1` by the
  collector lint, which keeps them out of the bands, unless a Query Store
  hint set on one of them overrides it, or `--estimate-compression` runs
  `sp_estimate_data_compression_savings`, whose sample copy cannot be
  hinted.
- The two files describe different sets, the instance's cache and the stores
  of the collected databases. Read them side by side; never add them.

No query text, plan or identifier leaves the server.
```

- [ ] Step 5: `docs/caps-inventory.md`, "## Caps for server cost". After the row of `80.workload/027.query-store-stats-usage`, add:

```text
| `80.workload/043.query-store-parallel-cost` | 1 000 parallel plans per database by parallel CPU, read 100 at a time within 100 MB of plan text and 10 s | "THE CAP AND THE BUDGETS ARE CONSTANTS AND NOT OPTIONS, as in 027" | `seuil_parallelisme.py` qualifies a database by `examined.share_of_parallel_cpu_pct` rather than rejecting it | yes: `cap`, `chunk`, `budget.*`, `examined.*`, `truncated` | not in any collection (added 6 October 2026) | Keep. The archive's own `selection.duration_ms`, `examined.duration_ms` and `examined.bytes_read` give the price |
```

- [ ] Step 6: `README.md`, the bullet "read-only is not the same as free or lock-free". Replace the line `  those plans, and every read holds the locks a read holds while it runs. Run` with:

```text
  those plans, one more searches the text of up to a thousand Query Store
  plans per database, within 100 MB and ten seconds of reading there, and
  every read holds the locks a read holds while it runs. Run
```

If the line reads otherwise on your base, make the same addition to the sentence that lists what costs CPU, and say so.

- [ ] Step 7: `CHANGELOG.md`, at the end of `## [Unreleased]` / `### Added`:

```text
- `80.workload/043.query-store-parallel-cost.sql` reads the cost bands of `042.parallel-cost-distribution.sql` from the Query Store of each database, by default, so the distribution a `cost threshold for parallelism` is chosen from no longer depends on what the plan cache still holds. It bands the plans with `is_parallel_plan = 1` executed in the last seven days, with 042's seven bands and column names, and adds `parallel_executions_min` and `parallel_cpu_s_min`, a lower bound from the rows whose every execution ran parallel, and `avg_dop`. The cost is found by a text search, as in 042; at most 1 000 plans are read per database, those with the most parallel CPU first, a hundred at a time, within 100 MB and 10 s, and the root projects what the reading cost. A store that is off, a database without a store and an availability group secondary are not read and say why; from SQL Server 2025 the rows of other replica roles are left out and counted. The serial plans that report a degree above 1 are counted and never banded. No query text and no identifier leaves the server. The audit's own statements are in the store and in the serial totals; the contract lint keeps them out of the bands.
```

- [ ] Step 8: `docs/query-store-parallel-cost-spec.md`. Replace the first line of the status paragraph, `Status: proposed on 4 October 2026, not implemented.`, by `Status: proposed on 4 October 2026; implemented on <date of this commit> by the plan docs/superpowers/plans/2026-10-06-query-store-parallel-cost.md.` The rulings on ambiguities 1 and 2 are already in the spec (amended with this plan on 6 October 2026); check that its text still matches the file as implemented, and amend it, dated, where it does not.

- [ ] Step 9: Check and commit.

```bash
cd <worktree> && /usr/bin/grep -nP '\x{2014}|\x{2013}|\*\*' <(git diff -U0 -- docs README.md CHANGELOG.md | /usr/bin/grep '^+') ; export GOTMPDIR=/var/tmp/sqa-avgdop && /usr/local/go/bin/go test . -count=1 2>&1 | tail -2
git add docs/dba-guide.md docs/caps-inventory.md README.md CHANGELOG.md docs/query-store-parallel-cost-spec.md
git commit -m "Document the Query Store parallel cost collector

The guide gains what the collector reads and what it costs a large
instance, the README's list of what costs CPU gains it, the caps inventory
records its cap and budgets, and the changelog says what a reader of the
archive will find. The spec now says it is implemented, and how the rows of
other replica roles are left out on every version."
```

Expected: the `grep` prints nothing, and the root package is `ok`.

### Task 9: PRIVATE REPOSITORY, the reader of 043 in `seuil_parallelisme.py`

This task is executed in `/home/rudi/Sources/Repos/sql-auditor-workspace/sql-auditor-private`, not in the public repository, under that repository's own `CLAUDE.md`: read it first. Its commit messages are in French, with no attribution trailer, and every change to a skill writes its entry in the skill's `HISTORY.md` in the same commit (`memory/AGENTS.md`, "L'historique d'un skill"). Start from that repository's current `HEAD`; `02a4a185` (6 October 2026) already changed the script's docstring for the 042 fix, so re-read the file rather than this plan's excerpts of it.

How the consumer reads 043 (spec, "What the analysis does with it"):

- 043's root is not 042's. `lire` keeps reading 042 and now refuses a document without `examined.statements` (a 043 document would read as zeros); 043 gets its own reader, `lire_qs`.
- The per-database documents are found with `preuves.documents(collecte, "80.workload/043.query-store-parallel-cost")`, as `profil_charge.py` finds 029's.
- Over the databases read, each band's counts and CPU are summed (same boundaries), `max_dop` takes the maximum, and `avg_dop` is re-weighted by `parallel_executions`. The total is `window.parallel_cpu_s` summed over the same databases; coverage is the bands' `parallel_cpu_s` over that total.
- The shared functions `tranches`, `serialisable` and `inconnue` read the summed bands; `serialisable_min` gives the lower bound from the `_min` columns, so each candidate threshold is printed as a range.
- Per database, the reader says when the store was not read and why, when the history is short (`window.store_oldest_interval` after `window.from`) or thin (`window.intervals` under half of what seven days of the store's interval length hold, a figure that counts the audit's own activity), when other replica roles were left out, and when a budget or the cap stopped the read, with the share the bands hold. A database is qualified, never rejected, on `truncated`.
- When `_run.json` has `config.estimate_compression` at `"true"`, it says the sample copies of `sp_estimate_data_compression_savings` may be in the bands.
- `main` prints 043's reading beside 042's, per collection, and prints 043's even when 042 is absent.

Files:
- Modify: `.claude/skills/analyser-collecte/scripts/seuil_parallelisme.py`, `.claude/skills/analyser-collecte/tests/test_seuil_parallelisme.py`, `.claude/skills/analyser-collecte/SKILL.md`, `.claude/skills/analyser-collecte/HISTORY.md`

- [ ] Step 1: Measure: `cd <private repo> && python -m pytest .claude/skills/analyser-collecte/tests/test_seuil_parallelisme.py -q 2>&1 | tail -1`. Expected: `20 passed` on `02a4a185`; record the number.

- [ ] Step 2: Write the failing tests: append to `tests/test_seuil_parallelisme.py`:

```python
# --- 80.workload/043.query-store-parallel-cost : les mêmes tranches, par base,
# lues dans le Query Store. Les chiffres sont inventés à la forme du collecteur.

def _base_qs(parallele: dict, total_cpu=None, lu=None, store_oldest="2026-09-29T00:00:00Z",
             intervals=10080, stopped_by=None, share=100.0, other=0, avg_dop=8.0):
    """`parallele` : {tranche: (plans, exécutions, cpu_s, exécutions_min, cpu_s_min)}."""
    bandes = []
    for nom, (de, a) in zip(NOMS, BORNES):
        n, e, c, emin, cmin = parallele.get(nom, (0, 0, 0.0, 0, 0.0))
        bandes.append({"band": nom, "cost_from": de, "cost_to": a, "statements": n,
                       "parallel_statements": n, "executions": e, "parallel_executions": e,
                       "cpu_s": c, "parallel_cpu_s": c, "max_dop": 8 if n else None,
                       "parallel_executions_min": emin, "parallel_cpu_s_min": cmin,
                       "avg_dop": avg_dop if e else None})
    total = total_cpu if total_cpu is not None else sum(v[2] for v in parallele.values())
    return {"database": "SALESDB",
            "state": {"actual": "READ_WRITE" if lu is None else "OFF", "capture_mode": "AUTO",
                      "interval_minutes": 1, "not_read_because": lu},
            "window": {"days": 7, "from": "2026-09-29T09:00:00Z",
                       "store_oldest_interval": store_oldest, "intervals": intervals,
                       "parallel_cpu_s": total},
            "excluded": {"other_replicas_executions": other, "other_replicas_cpu_s": 1.5 if other else 0},
            "cap": 1000,
            "examined": {"plans": 3, "stopped_by": stopped_by, "share_of_parallel_cpu_pct": share},
            "bands": bandes}


def test_lire_refuse_un_document_de_043():
    try:
        sp.lire(_base_qs({"5_25": (2, 100, 50.0, 80, 40.0)}), _config(5), sp.attentes({}))
    except ValueError as e:
        assert "lire_qs" in str(e)
    else:
        raise AssertionError("lire a lu un document de 043 sans examined.statements")


def test_bandes_qs_additionne_et_repondere_avg_dop():
    a = _base_qs({"5_25": (2, 100, 50.0, 80, 40.0)}, avg_dop=4.0)
    b = _base_qs({"5_25": (1, 300, 30.0, 300, 30.0)}, avg_dop=8.0)
    t = next(x for x in sp.bandes_qs([("A", a), ("B", b)]) if x["band"] == "5_25")
    assert (t["statements"], t["parallel_executions"], t["parallel_executions_min"]) == (3, 400, 380)
    assert t["parallel_cpu_s"] == 80.0 and t["max_dop"] == 8
    # (4 x 100 + 8 x 300) / 400, et non (4 + 8) / 2.
    assert t["avg_dop"] == 7.0


def test_lire_qs_donne_un_minorant_et_un_majorant():
    a = _base_qs({"5_25": (2, 100, 50.0, 80, 40.0), "ge_500": (1, 10, 50.0, 10, 50.0)})
    r = sp.lire_qs([("A", a)], _config(5))
    k = next(c for c in r["candidats"] if c["seuil"] == 25.0)
    assert (k["minorant"]["executions"], k["majorant"]["executions"]) == (80, 100)
    assert (k["minorant"]["cpu_s"], k["majorant"]["cpu_s"]) == (40.0, 50.0)
    assert r["couverture"] == 100.0 and r["total_cpu_s"] == 100.0


def test_lire_qs_dit_pourquoi_une_base_n_est_pas_lue():
    lue = _base_qs({"5_25": (2, 100, 50.0, 80, 40.0)})
    eteinte = _base_qs({"5_25": (9, 900, 900.0, 900, 900.0)}, lu="off")
    r = sp.lire_qs([("A", lue), ("B", eteinte)], _config(5))
    assert (r["bases"], r["lues"]) == (2, 1)
    assert r["total_cpu_s"] == 50.0
    assert any("B : magasin non lu (off)" in m for m in r["marques"])


def test_lire_qs_historique_court_et_mince():
    court = _base_qs({"5_25": (1, 1, 1.0, 1, 1.0)}, store_oldest="2026-10-05T00:00:00Z", intervals=1500)
    r = sp.lire_qs([("A", court)], _config(5))
    assert any("historique court" in m for m in r["marques"])
    assert any("historique mince, 1500 intervalles sur 10080" in m for m in r["marques"])


def test_lire_qs_budget_plafond_et_autres_repliques():
    a = _base_qs({"5_25": (1, 1, 1.0, 1, 1.0)}, stopped_by="time", share=62.5)
    b = _base_qs({"5_25": (1, 1, 1.0, 1, 1.0)}, stopped_by="cap", share=97.0, other=12)
    r = sp.lire_qs([("A", a), ("B", b)], _config(5))
    assert any("A : lecture arrêtée par le budget de temps" in m and "62.5 %" in m for m in r["marques"])
    assert any("B : plafond de 1000 plans atteint" in m and "97.0 %" in m for m in r["marques"])
    assert any("B : 12 exécutions d'autres rôles" in m for m in r["marques"])


def test_lire_qs_signale_estimate_compression():
    a = _base_qs({"5_25": (1, 1, 1.0, 1, 1.0)})
    r = sp.lire_qs([("A", a)], _config(5), {"config": {"estimate_compression": "true"}})
    assert any("--estimate-compression" in m for m in r["marques"])
    r = sp.lire_qs([("A", a)], _config(5), {"config": {"estimate_compression": "false"}})
    assert not any("--estimate-compression" in m for m in r["marques"])


def test_main_lit_les_deux_sources(tmp_path, capsys):
    d = tmp_path / "INST_B"
    (d / "80.workload" / "SALESDB").mkdir(parents=True)
    (d / "80.workload" / "042.parallel-cost-distribution.json").write_text(json.dumps(INST_B))
    (d / "80.workload" / "SALESDB" / "043.query-store-parallel-cost.json").write_text(
        json.dumps(_base_qs({"100_500": (3, 300, 360.0, 200, 300.0)})))
    (d / "10.system").mkdir()
    (d / "10.system" / "010.properties.json").write_text(json.dumps(
        {"configuration": [{"setting": "cost threshold for parallelism", "value_in_use": 50},
                           {"setting": "max degree of parallelism", "value_in_use": 8}]}))
    assert sp.main([str(d)]) == 0
    out = capsys.readouterr().out
    assert "(Query Store, 043) : 1 base(s) lue(s) sur 1" in out
    assert "seuil 100 : entre 0 et 0" in out
    assert "INST_B : seuil 50" in out
```

Run Step 1's command. Expected: the new tests fail (`AttributeError: module 'seuil_parallelisme' has no attribute 'bandes_qs'` and the like); the 20 old ones pass.

- [ ] Step 3: Implement, in `scripts/seuil_parallelisme.py`.

a. Imports and constants. Add `from datetime import datetime` after `import sys`, then after the imports:

```python
sys.path.insert(0, str(Path(__file__).resolve().parent))
import preuves as pr  # noqa: E402
```

after `COLLECTEUR = ...`:

```python
# Les mêmes tranches lues dans le Query Store de chaque base (spec publique
# docs/query-store-parallel-cost-spec.md). Sa racine n'est pas celle de 042 :
# elle se lit par `lire_qs`, jamais par `lire`.
COLLECTEUR_QS = "80.workload/043.query-store-parallel-cost"
```

and after `VALEUR_MAISON = 80`:

```python
# Sous cette part des intervalles qu'une semaine peut tenir, l'historique d'une
# base est dit mince. Un chiffre pour le lecteur, pas un verdict : une nuit
# creuse lui ressemble, et les intervalles comptent l'activité de l'audit.
INTERVALLES_MINCES = 0.5
```

b. At the top of `lire`, replace its one-line docstring and add the refusal before `ts = tranches(doc)`:

```python
    """Les chiffres, les candidats et les marques d'une instance (042).

    Un document sans `examined.statements` n'est pas de 042 : celui de 043 y
    lirait des zéros sans erreur. Il est refusé, et se lit par `lire_qs`.
    """
    if "statements" not in (doc.get("examined") or {}):
        raise ValueError(f"document sans examined.statements : ce n'est pas {COLLECTEUR}, "
                         f"et {COLLECTEUR_QS} se lit par lire_qs")
```

c. Before `def par_hote(`, add:

```python
def _instant(texte) -> datetime | None:
    try:
        return datetime.fromisoformat(str(texte))
    except (TypeError, ValueError):
        return None


def bandes_qs(docs: list[tuple[str | None, dict]]) -> list[dict]:
    """Les tranches de 043 additionnées sur les bases.

    Les bornes sont les mêmes partout, donc comptes et CPU s'additionnent ;
    `max_dop` prend le maximum ; `avg_dop` est une moyenne et se re-pondère
    par `parallel_executions`, son poids dans chaque base.
    """
    entiers = ("statements", "parallel_statements", "executions",
               "parallel_executions", "parallel_executions_min")
    reels = ("cpu_s", "parallel_cpu_s", "parallel_cpu_s_min")
    somme: dict[str, dict] = {}
    for _, doc in docs:
        for t in doc.get("bands") or []:
            if not isinstance(t, dict):
                continue
            s = somme.setdefault(t.get("band"), {
                "band": t.get("band"), "cost_from": t.get("cost_from"),
                "cost_to": t.get("cost_to"), **{k: 0 for k in entiers},
                **{k: 0.0 for k in reels}, "max_dop": None, "_dop": 0.0})
            for k in entiers:
                s[k] += int(t.get(k) or 0)
            for k in reels:
                s[k] += float(t.get(k) or 0)
            if t.get("max_dop") is not None:
                s["max_dop"] = max(s["max_dop"] or 0, t["max_dop"])
            if t.get("avg_dop") is not None:
                s["_dop"] += float(t["avg_dop"]) * int(t.get("parallel_executions") or 0)
    out = []
    for s in somme.values():
        dop = s.pop("_dop")
        s["avg_dop"] = round(dop / s["parallel_executions"], 2) if s["parallel_executions"] else None
        out.append(s)
    return out


def serialisable_min(ts: list[dict], candidat: float) -> dict | None:
    """Le minorant de `serialisable` : les lignes où min_dop > 1, dont chaque
    exécution a tourné en parallèle. None hors des bornes, comme lui."""
    if float(candidat) not in BORNES:
        return None
    sous = [t for t in ts if t.get("cost_to") is not None and float(t["cost_to"]) <= candidat]
    return {"executions": sum(t.get("parallel_executions_min") or 0 for t in sous),
            "cpu_s": sum(float(t.get("parallel_cpu_s_min") or 0) for t in sous)}


def _marques_base(base, doc: dict) -> list[str]:
    """Ce qu'il faut savoir d'une base avant de lire ses tranches."""
    st, w = doc.get("state") or {}, doc.get("window") or {}
    ex, exc = doc.get("examined") or {}, doc.get("excluded") or {}
    raison = st.get("not_read_because")
    if raison:
        return [f"{base} : magasin non lu ({raison})"]
    m = []
    debut, depuis = _instant(w.get("store_oldest_interval")), _instant(w.get("from"))
    if debut and depuis and debut > depuis:
        m.append(f"{base} : historique court, le magasin commence le "
                 f"{w['store_oldest_interval']}, après le début de la fenêtre")
    if w.get("intervals") is not None and st.get("interval_minutes") and w.get("days"):
        possibles = int(w["days"]) * 1440 // int(st["interval_minutes"])
        if possibles and w["intervals"] < INTERVALLES_MINCES * possibles:
            m.append(f"{base} : historique mince, {w['intervals']} intervalles sur "
                     f"{possibles} possibles, activité de l'audit comprise")
    if exc.get("other_replicas_executions"):
        m.append(f"{base} : {exc['other_replicas_executions']} exécutions d'autres rôles "
                 f"de réplica écartées ({exc.get('other_replicas_cpu_s')} s de CPU)")
    arret = ex.get("stopped_by")
    part = ex.get("share_of_parallel_cpu_pct")
    if arret in ("bytes", "time"):
        m.append(f"{base} : lecture arrêtée par le budget de "
                 f"{'volume' if arret == 'bytes' else 'temps'}, les tranches portent "
                 f"{part} % du CPU parallèle de la base")
    elif arret == "cap":
        m.append(f"{base} : plafond de {doc.get('cap')} plans atteint, les plans lus "
                 f"portent {part} % du CPU parallèle de la base")
    return m


def lire_qs(docs: list[tuple[str | None, dict]], config: dict, run: dict | None = None) -> dict:
    """Les tranches de 043 sur les bases lues, leurs bornes et les marques.

    La décision repose sur les tranches et `window.parallel_cpu_s`, que
    l'audit n'atteint pas hors des cas que dit l'en-tête de 043.
    `window.intervals` et `window.newest_interval` comptent l'activité de
    l'audit et ne prouvent pas que la charge du client a tourné récemment.
    """
    lues = [(b, d) for b, d in docs if not (d.get("state") or {}).get("not_read_because")]
    bandes = bandes_qs(lues)
    ts = tranches({"bands": bandes})
    total = sum(float((d.get("window") or {}).get("parallel_cpu_s") or 0) for _, d in lues)
    dans = sum(float(t.get("parallel_cpu_s") or 0) for t in bandes)
    seuil = config.get("cost threshold for parallelism")
    candidats = []
    for c in CANDIDATS:
        if seuil is not None and c <= seuil:
            candidats.append({"seuil": c, "deja": True})
            continue
        candidats.append({"seuil": c, "deja": False, "majorant": serialisable(ts, c),
                          "minorant": serialisable_min(ts, c)})
    marques = [m for b, d in docs for m in _marques_base(b, d)]
    if ((run or {}).get("config") or {}).get("estimate_compression") == "true":
        marques.append("--estimate-compression actif : les copies d'échantillon de "
                       "sp_estimate_data_compression_savings peuvent être dans les tranches")
    return {"bases": len(docs), "lues": len(lues), "total_cpu_s": total,
            "couverture": _pct(dans, total), "seuil": seuil, "tranches": ts,
            "inconnue": inconnue({"bands": bandes}), "candidats": candidats,
            "marques": marques}


def afficher_qs(nom: str, r: dict) -> None:
    cv = f"{r['couverture']:.0f} %" if r["couverture"] is not None else "?"
    print(f"{nom} (Query Store, 043) : {r['lues']} base(s) lue(s) sur {r['bases']}, "
          f"{_h(r['total_cpu_s'])} de CPU parallèle sur sept jours, {cv} dans les tranches")
    for k in r["candidats"]:
        if k["deja"]:
            print(f"  seuil {k['seuil']:.0f} : déjà atteint")
            continue
        mx, mn = k["majorant"], k["minorant"]
        print(f"  seuil {k['seuil']:.0f} : entre {mn['executions']} et {mx['executions']} "
              f"exécutions parallèles, entre {_h(mn['cpu_s'])} et {_h(mx['cpu_s'])} de CPU, "
              f"pour au plus {mx['instructions']} plans")
    inc = r["inconnue"]
    if inc.get("statements"):
        print(f"  !! {inc['statements']} plan(s) parallèle(s) de coût inconnu, hors de toute tranche")
    for m in r["marques"]:
        print(f"  !! {m}")
```

d. In `main`, replace the loop's opening, from `    lectures = []` to the line before `        machine = (_lire(collecte, HOTE)`, with:

```python
    lectures = []
    for collecte in args.collectes:
        prop = _lire(collecte, PROPRIETES)
        run = _lire(collecte, "_run")
        nom = str((run.get("server") or {}).get("name")
                  or (prop.get("instance") or {}).get("instance_name") or collecte.name)
        # Les deux sources côte à côte : le cache couvre les bases dont le
        # magasin est éteint et master, le magasin ce que le cache a évincé.
        docs_qs = pr.documents(collecte, COLLECTEUR_QS)
        if docs_qs:
            afficher_qs(nom, lire_qs(docs_qs, configuration(prop), run))
        else:
            print(f"{nom} : {COLLECTEUR_QS} n'est pas dans l'archive (collecte antérieure "
                  f"au collecteur, ou aucune base par base)")
        doc = _lire(collecte, COLLECTEUR)
        if not doc:
            print(f"{collecte.name} : {COLLECTEUR} n'est pas dans l'archive : instance "
                  f"antérieure à SQL Server 2016, collecte antérieure au collecteur, ou "
                  f"VIEW SERVER STATE refusé (le refus est une erreur dans _run.json).")
            continue
```

- [ ] Step 4: Run Step 1's command. Expected: the count of Step 1 plus 8 (28 on `02a4a185`), all passed. Then run the whole private suite as that repository's `pytest.ini` declares it (`python -m pytest -q`), which must stay green.

- [ ] Step 5: Break steps, each undone from a copy kept aside, each with the one test that must fail.

| Mutation (in `seuil_parallelisme.py`) | Test that fails |
| --- | --- |
| in `bandes_qs`, `* int(t.get("parallel_executions") or 0)` written `* (1 if t.get("parallel_executions") else 0)` (a plain average) | `test_bandes_qs_additionne_et_repondere_avg_dop` |
| in `lire_qs`, `lues = [...]` written `lues = list(docs)` (an unread store counted) | `test_lire_qs_dit_pourquoi_une_base_n_est_pas_lue` |
| in `lire`, `if "statements" not in (doc.get("examined") or {}):` written `if False:` | `test_lire_refuse_un_document_de_043` |
| `== "true":` written `== "yes":` | `test_lire_qs_signale_estimate_compression` |
| in `_marques_base`, `    if debut and depuis and debut > depuis:` written `    if False:` | `test_lire_qs_historique_court_et_mince` |

- [ ] Step 6: `SKILL.md`, at the end of the section "## Le seuil de parallélisme se choisit sur la distribution des coûts", add:

```text
Le script lit aussi `80.workload/043.query-store-parallel-cost`, les mêmes
tranches relevées dans le Query Store de chaque base sur sept jours, et
l'imprime à côté de 042. Les tranches des bases lues s'additionnent ; chaque
candidat se donne entre un minorant (les lignes dont toutes les exécutions
ont tourné en parallèle, colonnes `_min`) et un majorant (les lignes qui ont
atteint un degré au-dessus de 1). Par base, il dit le magasin non lu et
pourquoi, un historique court ou mince, les exécutions d'autres rôles de
réplica écartées, et la lecture arrêtée par un budget ou par le plafond, avec
la part du CPU parallèle que les tranches portent : une base se qualifie,
elle ne s'écarte pas. `window.intervals` et `window.newest_interval` comptent
l'activité de l'audit lui-même et ne prouvent pas que la charge du client a
tourné récemment. Quand `_run.json` montre `--estimate-compression`, les
copies d'échantillon de `sp_estimate_data_compression_savings` peuvent être
dans les tranches. Les deux sources ne s'additionnent jamais : le cache
couvre master et les bases dont le magasin est éteint, le magasin ce que le
cache a évincé.
```

- [ ] Step 7: `HISTORY.md`, at the top of the entries, in that file's form:

```text
## 2026-10-06 : seuil_parallelisme lit aussi 043, le Query Store par base
Statut : retenu
Pourquoi : le dépôt public ajoute `80.workload/043.query-store-parallel-cost`, les tranches de 042 lues dans le Query Store de chaque base, qui garde ce que le cache évince. Sa racine n'est pas celle de 042 : `lire` y aurait lu des zéros sans erreur.
Effet : `lire_qs` additionne les tranches des bases lues (comptes et CPU, `max_dop` au maximum, `avg_dop` re-pondéré par `parallel_executions`), rapporte le total et la couverture, donne chaque candidat comme un intervalle entre le minorant (`_min`) et le majorant, et marque par base le magasin non lu, l'historique court ou mince, les autres rôles de réplica écartés, le budget ou le plafond atteint, et `--estimate-compression`. `lire` refuse un document sans `examined.statements`. `main` imprime les deux sources côte à côte. Huit tests ; cinq cassures, chacune fait tomber le sien.
```

- [ ] Step 8: Commit, in that repository, by explicit paths:

```bash
git add .claude/skills/analyser-collecte/scripts/seuil_parallelisme.py .claude/skills/analyser-collecte/tests/test_seuil_parallelisme.py .claude/skills/analyser-collecte/SKILL.md .claude/skills/analyser-collecte/HISTORY.md
git commit -m "seuil_parallelisme lit 043, les tranches du Query Store base par base

Le cache que lit 042 garde peu sous pression mémoire ; 043 lit les mêmes tranches dans le Query Store de chaque base. Sa racine n'étant pas celle de 042, il a son propre lecteur, et lire refuse désormais un document qui n'est pas de 042 plutôt que d'y lire des zéros. Les tranches s'additionnent sur les bases, avg_dop se re-pondère, et chaque candidat se donne entre le minorant et le majorant que le Query Store permet."
```

Whether to push that repository follows its own rules and the owner's standing instruction for it; this plan does not push.

---

## Ambiguities and contradictions

Flagged, not silently resolved. Where the plan had to choose to stay executable, it says what it chose and the alternative.

1. Ruled on 6 October 2026: accepted, and the spec amended. The aggregation and SQL Server 2016 to 2019. The spec runs the aggregation into `#runtime` inside the `sp_executesql` literal that carries the replica predicate ("The aggregation of the runtime rows", "The collector": "The dynamic aggregation is one literal ... and fills `#runtime` from inside"). A statement naming `rs.replica_group_id` does not compile before SQL Server 2022, and the spec gives those versions no other path, while its floor is 2016. The plan stages the other-role rows into `#other_rows` through the literal, only where the column and `sys.query_store_replicas` both exist, and runs one aggregation, the same on every version, with `NOT EXISTS`. Alternative: two copies of the aggregation, one dynamic and one inline, which every later change would have to keep in step. The owner accepted the staging through `#other_rows` and one static aggregation; "The aggregation of the runtime rows" and "The collector" in the spec now say so.
2. Ruled on 6 October 2026: accepted, and the spec amended. "Reported as an error, as 050 does." For a database whose replica state cannot be read, the spec says it is treated as a secondary "and reported as an error, as 050 does". 050 (`70.schema/050.heaps.sql`) reports through `errors.*` keys, which the runner turns into a partial-run warning in `MANIFEST.txt`. 043's root list has no `errors.*` key, and test 2 requires the key set to equal that list exactly. The plan follows the list: the reason is `state.not_read_because = 'replica state unreadable'` and the run is not marked partial. The owner accepted this; the spec's "Other replicas" now says the reason goes in the root and no `errors.*` key is added.
3. Spec test 3 cannot catch "computing the share from anything but the bands". On a store read whole, the bands hold every parallel plan, so any such share is 100 (measured: the mutation gives 100.0 either way). The plan asserts the share in Task 3 and catches the mutation in Task 5's partial read (measured 100.0 against an allowed 45.9 to 54.3).
4. Spec test 3's premise "`window.serial_plans_dop_above_1` is at least 1, and stops there if not", read from the document, fails a wrongly excluding 043 with a message that blames the fixture (measured with the role mutation). The plan reads the premise from the store, fatally, then holds the document to it with an ordinary error.
5. Vocabulary against 042 since `98e6d14`. 042's bands now call a statement parallel when its cached plan holds an operator with `Parallel="1"`; in 042 `statements` counts every statement of the band and `parallel_statements` the parallel plans, and `parallel_executions` is every execution of a parallel plan. In 043, by the spec, `statements` counts parallel plans, `parallel_statements` those that ran at a degree above 1, and `parallel_executions` the executions of rows at a degree above 1. Same names, different quantities; the consumer's shared `serialisable` sums them alike and prints both as "instructions". The leaf name `serial_plans_dop_above_1` is aligned (042 `examined.`, 043 `window.`). The plan adds an assertion that `is_parallel_plan` agrees with a `Parallel="1"` operator on every costed plan of the fixture (it did); a parallel `StatMan` plan with no statement (spec point 5) is parallel for 043 and would be serial by 042's reading.
6. The spec's spelling `Parallel="true"` (point 4, paragraph after point 9) is not what the store or the cache writes: the lab shows `Parallel="1"` and `Parallel="0"`, as the 042 fix recorded in "Open questions". Corrected in the spec on 6 October 2026, with this plan.
7. `truncated`, and every count, on a store that is not read. The spec says the counts are NULL and that `truncated` is 1 when `stopped_by` is not NULL; it does not say what `truncated` is for a store not read. The plan makes it NULL with the counts, so that a reader who checks only `truncated` cannot take an unread store for a complete one.
8. CI. The spec does not mention CI; the owner's decision for max-duration was applied (a separate live step, failing on a skip), plus an exact key-set `jq` on ci_probe's document, which the spec does not ask for. The fixture's premises (at least five parallel plans, the `#savings` anomaly, `excluded` 0 on 2025) were measured on SQL Server 2025 with 22 schedulers only; on the 2017 and 2022 images with a few cores they are unmeasured, and the step may fail on them. That is to be reported, not loosened.
9. "Left out of everything above" and `window.runtime_rows`. The root table defines `excluded.*` as "left out of everything above", and lists above it `window.runtime_rows`, "before any exclusion". The plan keeps the row's own definition (other-role rows included in `window.runtime_rows`) and counts `window.intervals`, `oldest_interval` and `newest_interval` over the kept rows only.
10. Several reasons not to read at once (an OFF store on a secondary). The spec lists four values and no order. The plan reports, first that applies: `no store`, `off`, `replica state unreadable`, `secondary`.
11. `examined.largest_plan_bytes` when nothing is copied: the spec is silent; the plan gives NULL.
12. Spec test 4 ("nothing is added to it") and this plan add no test for the three 050 mutations; Task 2 Step 8 re-measures them on the tree, and they were all refused on 6 October 2026. The spec's own unease stands: "to be measured again when the scanner changes or a collector reads a client table" is a rule nothing enforces.
13. Documents beyond the spec's list. "Running by default: what changes in the tree" names the guide's tier and cost row, the `check` listing and the caps inventory. The plan also adds a guide section, a sentence under "Narrowing the databases", a README sentence and a CHANGELOG entry, as requested for this plan. `.env.example` stays unchanged, as the spec says.
14. A loop timer at `datetime2(3)` is a trap the spec's text does not name and the shape of 027 and 042 invites (both declare their timers that way, harmlessly, for durations only). It made a time budget of 0 never fire. The plan's file uses full precision and says why in a comment, and Task 5 has the break.
15. The rules the spec itself is least sure of are unchanged by this plan: that the lint is the guarantee (the parenthesis form still passes `hintlint.go`); the compression sample copy's seriality; the fixture's margin on other instances (ambiguity 8 is where it will show); the unobserved cases outside the lint; the three allowed procedures when `SQL_DATABASE` is not `master`; and leaving the interval figures to the analysis' care, which Task 9 implements as a sentence, not a rule.
