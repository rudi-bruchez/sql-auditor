package collect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// detailSets builds what 021.query-store-detail.sql returns for one database:
// two queries, one of which has two plans, and one plan that is NULL.
func detailSets() []NamedResultSet {
	root := ResultSet{
		Columns: []string{"database", "state.actual", "state.readonly_reason",
			"window.requested_from", "window.requested_to",
			"window.effective_from", "window.effective_to", "window.intervals", "selection.cap"},
		Types: []string{"NVARCHAR", "NVARCHAR", "INT", "DATETIME2", "DATETIME2",
			"DATETIME2", "DATETIME2", "BIGINT", "INT"},
		// time.Time, not strings: the driver delivers DATETIME2 that way, and a
		// fixture that disagrees tests the encoder against data it never sees.
		Rows: [][]any{{"Sales", "READ_WRITE", int64(0),
			time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC), int64(168), int64(50)}},
	}
	// Raw ranks, always present, never capped: the round robin can retain a
	// query whose best rank exceeds the cap, and nulling those out would show
	// "not ranked" for the very metric that let it in.
	selected := ResultSet{
		Columns: []string{"query_id", "plan_id", "text", "query_plan", "query_plan_bytes", "is_forced",
			"rank.duration", "rank.cpu", "rank.logical_reads", "rank.executions"},
		Types: []string{"BIGINT", "BIGINT", "NVARCHAR", "NVARCHAR", "BIGINT", "BIT", "BIGINT", "BIGINT", "BIGINT", "BIGINT"},
		Rows: [][]any{
			{int64(11), int64(101), "SELECT a FROM t", "<ShowPlanXML>a</ShowPlanXML>", int64(28), false, int64(1), int64(2), int64(40), int64(900)},
			{int64(11), int64(102), "SELECT a FROM t", "<ShowPlanXML>b</ShowPlanXML>", int64(28), true, int64(1), int64(2), int64(40), int64(900)},
			// query_plan NULL with query_plan_bytes 0: the Query Store holds no
			// plan at all, which is not the same as one dropped by the cap.
			{int64(22), int64(201), "SELECT b FROM u", nil, int64(0), false, int64(77), int64(3), int64(5), int64(12)},
		},
	}
	intervals := ResultSet{
		Columns: []string{"query_id", "plan_id", "start_time", "count_executions"},
		Types:   []string{"BIGINT", "BIGINT", "DATETIME2", "BIGINT"},
		Rows: [][]any{
			{int64(11), int64(101), time.Date(2026, 8, 6, 1, 0, 0, 0, time.UTC), int64(9)},
			{int64(22), int64(201), time.Date(2026, 8, 6, 1, 0, 0, 0, time.UTC), int64(3)},
		},
	}
	return []NamedResultSet{
		{Spec: ResultSpec{Name: "root", Shape: ShapeObject}, Set: root},
		{Spec: ResultSpec{Name: "selected", Shape: ShapeArray}, Set: selected},
		{Spec: ResultSpec{Name: "intervals", Shape: ShapeArray}, Set: intervals},
	}
}

func runDetailWriter(t *testing.T, sets []NamedResultSet) (root, rel string, st *QueryStoreState, warnings []string) {
	t.Helper()
	root = t.TempDir()
	st = NewQueryStoreState()
	req := WriteRequest{
		Out:    newRunWriter(root, maxRunBytes, func(string) {}),
		Script: Script{Path: "80.workload/021.query-store-detail.sql", Dir: "80.workload", Base: "021.query-store-detail"},
		Unit:   DatabaseFolder{Name: "Sales", Folder: "Sales"},
		Sets:   sets,
		State:  st,
		Warn:   func(s string) { warnings = append(warnings, s) },
	}
	w := writerFor("query-store-detail")
	if w == nil {
		t.Fatal("writerFor(query-store-detail) = nil")
	}
	res, err := w(req)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	return root, res.Rel, st, warnings
}

func TestDetailWriterLaysOutOneDirectoryPerDatabase(t *testing.T) {
	root, rel, _, _ := runDetailWriter(t, detailSets())
	if rel != "80.workload/Sales/021.query-store-detail" {
		t.Fatalf("rel = %q, want 80.workload/Sales/021.query-store-detail", rel)
	}
	dir := filepath.Join(root, filepath.FromSlash(rel))
	for _, name := range []string{
		"_index.json",
		"query_11.sql", "query_11.stats.json", "query_11.plan_101.sqlplan", "query_11.plan_102.sqlplan",
		"query_22.sql", "query_22.stats.json",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "query_22.plan_201.sqlplan")); err == nil {
		t.Error("wrote a .sqlplan for a NULL plan; a missing plan must leave no file")
	}
}

func TestDetailWriterWritesTheTextVerbatim(t *testing.T) {
	root, rel, _, _ := runDetailWriter(t, detailSets())
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), "query_11.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "SELECT a FROM t" {
		t.Errorf("query_11.sql = %q, want the text unchanged", string(b))
	}
}

func TestDetailWriterKeepsPlanXMLOutOfTheJSON(t *testing.T) {
	root, rel, _, _ := runDetailWriter(t, detailSets())
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), "query_11.stats.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "ShowPlanXML") {
		t.Error("query_11.stats.json repeats the plan XML that already has its own file")
	}
	if !strings.Contains(string(b), "count_executions") {
		t.Error("query_11.stats.json is missing the per-interval series")
	}
}

func TestDetailWriterRecordsTheNullPlanAsAnOmission(t *testing.T) {
	root, rel, _, warnings := runDetailWriter(t, detailSets())
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), "_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Omissions []struct {
			QueryID int64  `json:"query_id"`
			PlanID  int64  `json:"plan_id"`
			Reason  string `json:"reason"`
		} `json:"omissions"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Omissions) != 1 || idx.Omissions[0].PlanID != 201 {
		t.Fatalf("omissions = %+v, want the NULL plan 201 recorded", idx.Omissions)
	}
	if len(warnings) == 0 {
		t.Error("an omission was recorded in _index.json but nothing warned the manifest")
	}
}

// The cap is applied twice: the SQL nulls out an oversized plan so it never
// crosses the wire, and the writer refuses one anyway. Both paths must record
// the omission, and both must be distinguishable from a plan the Query Store
// simply does not hold.
func TestDetailWriterCapsAnOversizedPlanFilteredByTheServer(t *testing.T) {
	sets := detailSets()
	sel, _ := setByName(sets, "selected")
	sel.Rows[0][3] = nil                     // query_plan, nulled by the DATALENGTH guard
	sel.Rows[0][4] = int64(maxPlanBytes + 1) // query_plan_bytes, the size that got it dropped
	assertPlanOmitted(t, sets, "query_11.plan_101.sqlplan", "cap")
}

func TestDetailWriterCapsAnOversizedPlanThatReachedTheWriter(t *testing.T) {
	sets := detailSets()
	sel, _ := setByName(sets, "selected")
	sel.Rows[0][3] = strings.Repeat("x", maxPlanBytes+1)
	assertPlanOmitted(t, sets, "query_11.plan_101.sqlplan", "cap")
}

func assertPlanOmitted(t *testing.T, sets []NamedResultSet, file, reasonFragment string) {
	t.Helper()
	root, rel, _, _ := runDetailWriter(t, sets)
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel), file)); err == nil {
		t.Errorf("wrote %s instead of recording the omission", file)
	}
	b, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), "_index.json"))
	if !strings.Contains(string(b), reasonFragment) {
		t.Errorf("_index.json does not explain the omission:\n%s", b)
	}
}

func TestDetailWriterWritesOnlyAnIndexWhenTheStoreIsOff(t *testing.T) {
	sets := detailSets()
	rt, _ := setByName(sets, "root")
	rt.Rows[0][1] = "OFF"
	sel, _ := setByName(sets, "selected")
	sel.Rows = nil
	root, rel, _, _ := runDetailWriter(t, sets)
	dir := filepath.Join(root, filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "_index.json" {
		t.Fatalf("got %d entries, want only _index.json", len(entries))
	}
	b, _ := os.ReadFile(filepath.Join(dir, "_index.json"))
	if !strings.Contains(string(b), "OFF") {
		t.Errorf("_index.json does not say the Query Store was off:\n%s", b)
	}
}

func TestDetailWriterRecordsTheRankPerMetric(t *testing.T) {
	root, rel, _, _ := runDetailWriter(t, detailSets())
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), "_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Queries []struct {
			QueryID int64            `json:"query_id"`
			Ranks   map[string]int64 `json:"ranks"`
		} `json:"queries"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Queries) != 2 {
		t.Fatalf("got %d queries in the index, want 2", len(idx.Queries))
	}
	// All four are recorded, capped or not: the profile is the information.
	// Query 22 entered on cpu rank 3 while sitting at 77 on duration — exactly
	// the query the union exists to catch, and its 77 must be visible.
	for metric, want := range map[string]int64{"duration": 1, "cpu": 2, "logical_reads": 40, "executions": 900} {
		if got := idx.Queries[0].Ranks[metric]; got != want {
			t.Errorf("query 11 %s rank = %d, want %d", metric, got, want)
		}
	}
	if idx.Queries[1].Ranks["duration"] != 77 || idx.Queries[1].Ranks["cpu"] != 3 {
		t.Errorf("query 22 ranks = %v, want duration 77 and cpu 3", idx.Queries[1].Ranks)
	}
}

// The archive holds the verbatim SQL of a production workload whether or not a
// single plan came with it. This is the disclosure defect reached from the
// opposite side, and it is reachable: natively compiled procedures have a NULL
// query_plan, and an exhausted budget writes texts and nothing else.
func TestDetailWriterCountsTextFilesEvenWithNoPlans(t *testing.T) {
	sets := detailSets()
	sel, _ := setByName(sets, "selected")
	for i := range sel.Rows {
		sel.Rows[i][3] = nil      // query_plan
		sel.Rows[i][4] = int64(0) // query_plan_bytes
	}
	root := t.TempDir()
	res, err := writerFor("query-store-detail")(WriteRequest{
		Out:    newRunWriter(root, maxRunBytes, func(string) {}),
		Script: Script{Path: "80.workload/021.query-store-detail.sql", Dir: "80.workload", Base: "021.query-store-detail"},
		Unit:   DatabaseFolder{Name: "Sales", Folder: "Sales"},
		Sets:   sets,
		State:  NewQueryStoreState(),
		Warn:   func(string) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.PlanFiles != 0 {
		t.Fatalf("PlanFiles = %d, want 0", res.PlanFiles)
	}
	if res.TextFiles != 2 {
		t.Fatalf("TextFiles = %d, want 2 — a run that writes only texts must still disclose", res.TextFiles)
	}
}

func TestDetailWriterPublishesTheSelection(t *testing.T) {
	_, _, st, _ := runDetailWriter(t, detailSets())
	got := st.Selected["Sales"]
	if len(got) != 2 || got[0] != 11 || got[1] != 22 {
		t.Fatalf("Selected[Sales] = %v, want [11 22]", got)
	}
}
