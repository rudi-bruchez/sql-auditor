package collect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// planCacheSets builds what 041.plan-cache-plans.sql returns: two ordinary
// plans, one the engine declined to render, one above the byte cap, and one
// past the count cap. The absences are the point of the fixture — each of the
// four is a different fact about the server, and a writer that collapsed them
// would turn "too large to collect" into "the cache holds no plan".
func planCacheSets() []NamedResultSet {
	root := ResultSet{
		Columns: []string{"collected_at", "cache.statements", "cache.plans",
			"cache.oldest_plan", "cache.newest_execution",
			"cap.plans", "cap.per_metric", "cap.plan_bytes"},
		Types: []string{"NVARCHAR", "INT", "INT", "NVARCHAR", "NVARCHAR", "INT", "INT", "INT"},
		Rows: [][]any{{"2026-09-04T14:02:11.400", int64(8134), int64(2210),
			"2026-08-29T03:11:02.000", "2026-09-04T14:01:58.000",
			int64(maxPlanCachePlans), int64(maxPlanCachePerMetric), int64(maxPlanCacheBytes)}},
	}
	xml := `<ShowPlanXML><BatchSequence/></ShowPlanXML>`
	cols := []string{"plan.rank", "plan_handle", "statements", "execution_count",
		"total_worker_time_us", "total_elapsed_time_us", "total_logical_reads",
		"database_name", "object_name", "statement_text", "plan_bytes", "query_plan"}
	types := []string{"BIGINT", "VARCHAR", "BIGINT", "BIGINT", "BIGINT", "BIGINT",
		"BIGINT", "NVARCHAR", "NVARCHAR", "NVARCHAR", "BIGINT", "NVARCHAR"}
	plans := ResultSet{
		Columns: cols,
		Types:   types,
		Rows: [][]any{
			{int64(1), "0x06000100", int64(4), int64(19022), int64(900100), int64(1200400),
				int64(88123), "SALESDB", "dbo.p_invoice", "SELECT 1", int64(len(xml)), xml},
			{int64(2), "0x06000200", int64(1), int64(4), int64(70100), int64(90400),
				int64(12), "SALESDB", nil, "SELECT 2", int64(len(xml)), xml},
			// Above the byte cap: 041 nulled the XML, the true size still arrives.
			{int64(3), "0x06000300", int64(1), int64(2), int64(500), int64(700),
				int64(3), "SALESDB", nil, "SELECT 3", int64(maxPlanCacheBytes + 1), nil},
			// In the cache, and dm_exec_query_plan declined to render it.
			{int64(4), "0x06000400", int64(1), int64(2), int64(400), int64(600),
				int64(2), "SALESDB", nil, "SELECT 4", nil, nil},
			// Past the count cap.
			{int64(maxPlanCachePlans + 1), "0x06009900", int64(1), int64(1), int64(10),
				int64(20), int64(1), "SALESDB", nil, "SELECT 5", int64(900), nil},
		},
	}
	return []NamedResultSet{
		{Spec: ResultSpec{Name: "root", Shape: ShapeObject}, Set: root},
		{Spec: ResultSpec{Name: "plans", Shape: ShapeArray}, Set: plans},
	}
}

func runPlanCacheWriter(t *testing.T, sets []NamedResultSet, budget int) (root, rel string, res WriteResult, warnings []string) {
	t.Helper()
	root = t.TempDir()
	req := WriteRequest{
		Out:    newRunWriter(root, budget),
		Script: Script{Path: "80.workload/041.plan-cache-plans.sql", Dir: "80.workload", Base: "041.plan-cache-plans"},
		// Instance-scoped: runUnit hands a writer an empty DatabaseFolder, and
		// the fixture reproduces that rather than inventing a database.
		Unit:  DatabaseFolder{},
		Sets:  sets,
		State: NewQueryStoreState(),
		Warn:  func(s string) { warnings = append(warnings, s) },
	}
	w := writerFor("plan-cache-plans")
	if w == nil {
		t.Fatal("writerFor(plan-cache-plans) returned nil")
	}
	var err error
	res, err = w(req)
	if err != nil {
		t.Fatalf("writePlanCachePlans: %v", err)
	}
	return root, res.Rel, res, warnings
}

func readPlanCacheIndex(t *testing.T, root, rel string) planCacheIndex {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), "_index.json"))
	if err != nil {
		t.Fatalf("reading _index.json: %v", err)
	}
	var idx planCacheIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatalf("decoding _index.json: %v", err)
	}
	return idx
}

// The four absences stay four different facts, all the way into the index. A
// report that read "too large to collect" as "the cache holds no plan for this"
// would draw the opposite conclusion about the server.
func TestPlanCacheWriterTellsTheFourAbsencesApart(t *testing.T) {
	root, rel, res, _ := runPlanCacheWriter(t, planCacheSets(), 1<<20)

	if res.CachedPlanFiles != 2 {
		t.Errorf("wrote %d plan files, want 2", res.CachedPlanFiles)
	}
	// The counter must be its own: borrowing PlanFiles would make MANIFEST.txt
	// announce Query Store text and per-interval statistics that are not here.
	if res.PlanFiles != 0 || res.TextFiles != 0 {
		t.Errorf("cached plans leaked into the Query Store counters: PlanFiles=%d TextFiles=%d",
			res.PlanFiles, res.TextFiles)
	}

	idx := readPlanCacheIndex(t, root, rel)
	if idx.Counts.Written != 2 {
		t.Errorf("index says %d written, want 2", idx.Counts.Written)
	}
	if idx.Counts.Selected != 5 {
		t.Errorf("index says %d selected, want 5", idx.Counts.Selected)
	}
	if idx.Counts.NullPlans != 1 {
		t.Errorf("index says %d null plans, want 1", idx.Counts.NullPlans)
	}
	if len(idx.Plans) != 5 {
		t.Fatalf("index lists %d plans, want all 5 — a plan whose file was not written is still a fact", len(idx.Plans))
	}
	// Every selected plan keeps its statistics whether or not its file exists.
	// Dropping the row would erase the shape of the workload while keeping its
	// worst example.
	for _, p := range idx.Plans {
		if p.PlanHandle == "" {
			t.Errorf("plan %d lost its handle", p.Rank)
		}
	}

	want := map[int64]string{
		int64(maxPlanCachePlans + 1): "beyond the cap",
		3:                            "above the",
		4:                            "returned nothing",
	}
	got := map[int64]string{}
	for _, o := range idx.Omissions {
		got[o.Rank] = o.Reason
	}
	if len(got) != len(want) {
		t.Fatalf("index carries %d omissions, want %d: %#v", len(got), len(want), got)
	}
	for rank, fragment := range want {
		if !strings.Contains(got[rank], fragment) {
			t.Errorf("omission for rank %d reads %q, want it to explain %q", rank, got[rank], fragment)
		}
	}
	// The one the engine refused must not read as an absent plan: that is the
	// distinction the whole switch exists for.
	if strings.Contains(got[4], "does not hold") {
		t.Errorf("the unrenderable plan is reported as absent from the cache: %q", got[4])
	}
}

// The caveats travel with the files. Whoever opens a .sqlplan six months from
// now has the directory and not the corpus, and two of the three invert a
// finding if they are missed.
func TestPlanCacheIndexCarriesTheCaveatsAndTheWindow(t *testing.T) {
	root, rel, _, _ := runPlanCacheWriter(t, planCacheSets(), 1<<20)
	idx := readPlanCacheIndex(t, root, rel)

	if len(idx.Caveats) != len(planCacheCaveats) {
		t.Errorf("index carries %d caveats, want %d", len(idx.Caveats), len(planCacheCaveats))
	}
	if idx.Cache.OldestPlan == "" || idx.Cache.NewestExecution == "" {
		t.Error("the index lost the cache window; a hundred plans since a restart nine " +
			"minutes ago and a hundred over three weeks are different evidence")
	}
	if idx.Cache.Statements == 0 || idx.Cache.Plans == 0 {
		t.Error("the index lost the size of the cache the selection was drawn from")
	}
	if idx.Counts.CapPlans != maxPlanCachePlans || idx.Counts.CapBytes != maxPlanCacheBytes ||
		idx.Counts.CapPerRank != maxPlanCachePerMetric {
		t.Errorf("the index does not record the caps actually applied: %+v", idx.Counts)
	}
}

// The index is written even when nothing else is, and outside the budget. An
// empty directory and an absent one are different facts, and only the index
// says which happened.
func TestPlanCacheIndexSurvivesAnExhaustedBudget(t *testing.T) {
	root, rel, res, warnings := runPlanCacheWriter(t, planCacheSets(), 0)
	if res.CachedPlanFiles != 0 {
		t.Errorf("wrote %d files under a zero budget, want 0", res.CachedPlanFiles)
	}
	idx := readPlanCacheIndex(t, root, rel)
	if len(idx.Plans) != 5 {
		t.Errorf("index lists %d plans under a zero budget, want 5", len(idx.Plans))
	}
	if len(warnings) == 0 {
		t.Error("nothing warned that the budget stopped the plans being written")
	}
}

// The caps are one rule expressed in two languages. A Go constant raised above
// a stale SQL literal would make plan 101 arrive NULL and be reported as a plan
// the cache does not hold — a false fact about the server.
func TestPlanCacheCapsAreTheSameNumbersInTheCorpus(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "queries", "80.workload", "041.plan-cache-plans.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	for _, c := range []struct {
		what    string
		pattern string
		want    int
		times   int
	}{
		{"the plan count cap", `s\.plan_rank <= (\d+)`, maxPlanCachePlans, 1},
		{"the per-plan byte cap", `DATALENGTH\(qp\.query_plan\) <= (\d+)`, maxPlanCacheBytes, 1},
		// Four rankings, one threshold. A drift in any one of them would
		// silently change which plans the archive holds while the totals in the
		// root object went on claiming the old selection.
		{"the per-metric depth", `r\.r_(?:cpu|duration|reads|executions) <= (\d+)`, maxPlanCachePerMetric, 4},
		// And the numbers the root object reports to the reader, which are a
		// third copy and the one an analysis actually sees.
		{"the reported plan cap", `(\d+)\s+AS \[cap\.plans\]`, maxPlanCachePlans, 1},
		{"the reported per-metric depth", `(\d+)\s+AS \[cap\.per_metric\]`, maxPlanCachePerMetric, 1},
		{"the reported byte cap", `(\d+)\s+AS \[cap\.plan_bytes\]`, maxPlanCacheBytes, 1},
	} {
		m := regexp.MustCompile(c.pattern).FindAllStringSubmatch(sql, -1)
		if len(m) != c.times {
			t.Errorf("041 has %d guards for %s, want exactly %d", len(m), c.what, c.times)
			continue
		}
		for _, hit := range m {
			got, err := strconv.Atoi(hit[1])
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("041 applies %d for %s, Go applies %d — the two are one rule and have drifted",
					got, c.what, c.want)
			}
		}
	}
}
