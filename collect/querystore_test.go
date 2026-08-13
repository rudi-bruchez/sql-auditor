package collect

import (
	"encoding/json"
	"fmt"
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
	assertPlanOmitted(t, sets, "query_11.plan_101.sqlplan", 101, "exceeds the 8388608 byte per-plan cap and was not sent")
}

func TestDetailWriterCapsAnOversizedPlanThatReachedTheWriter(t *testing.T) {
	sets := detailSets()
	sel, _ := setByName(sets, "selected")
	sel.Rows[0][3] = strings.Repeat("x", maxPlanBytes+1)
	assertPlanOmitted(t, sets, "query_11.plan_101.sqlplan", 101, "exceeds the 8388608 byte per-plan cap")
}

// indexOmissions reads back the omissions _index.json records. Matching on the
// raw text would not do: every index carries "selection": {"cap": …}
// unconditionally, so a substring check for "cap" passes against a writer that
// records no omission at all.
type indexOmission struct {
	QueryID int64  `json:"query_id"`
	PlanID  int64  `json:"plan_id"`
	Reason  string `json:"reason"`
	Bytes   int64  `json:"bytes"`
}

func indexOmissions(t *testing.T, root, rel string) []indexOmission {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), "_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Omissions []indexOmission `json:"omissions"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	return idx.Omissions
}

func assertPlanOmitted(t *testing.T, sets []NamedResultSet, file string, planID int64, reasonFragment string) {
	t.Helper()
	root, rel, _, _ := runDetailWriter(t, sets)
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel), file)); err == nil {
		t.Errorf("wrote %s instead of recording the omission", file)
	}
	for _, o := range indexOmissions(t, root, rel) {
		if o.PlanID == planID && strings.Contains(o.Reason, reasonFragment) {
			return
		}
	}
	t.Errorf("_index.json records no omission for plan %d mentioning %q: %+v",
		planID, reasonFragment, indexOmissions(t, root, rel))
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

// KnownWriters and writerFor are two closed sets that have to stay in step. A
// name declared in the corpus but unresolved here falls back to the ordinary
// encoder and emits one JSON document where a directory of plans was expected,
// with nothing anywhere saying so — the failure @writer exists to prevent.
func TestWriterForCoversKnownWriters(t *testing.T) {
	// No gap left: both known writers resolve. Kept as a map so a future
	// writer added to KnownWriters without a writerFor case fails loudly here
	// instead of falling back to the plain encoder.
	pending := map[string]bool{}

	for name := range KnownWriters {
		w := writerFor(name)
		if pending[name] {
			if w != nil {
				t.Errorf("writerFor(%q) now resolves; drop it from the pending set", name)
			}
			continue
		}
		if w == nil {
			t.Errorf("KnownWriters declares %q but writerFor returns nil for it", name)
		}
	}
	for name := range pending {
		if _, ok := KnownWriters[name]; !ok {
			t.Errorf("pending writer %q is not declared in KnownWriters", name)
		}
	}
	if writerFor("query-store-nonesuch") != nil {
		t.Error("writerFor resolved a name that is in no closed set")
	}
}

// A budget that dies mid-run is the case the omissions exist for. What must
// survive it: the index itself, a truthful account of what was left out, and a
// TextFiles count that still discloses the query text already on disk even
// though not one plan came with it.
func TestDetailWriterRecordsWhatTheBudgetRefused(t *testing.T) {
	root := t.TempDir()
	// Room for the first query's text and nothing else.
	out := newRunWriter(root, len("SELECT a FROM t"), func(string) {})
	var warnings []string
	res, err := writerFor("query-store-detail")(WriteRequest{
		Out:    out,
		Script: Script{Path: "80.workload/021.query-store-detail.sql", Dir: "80.workload", Base: "021.query-store-detail"},
		Unit:   DatabaseFolder{Name: "Sales", Folder: "Sales"},
		Sets:   detailSets(),
		State:  NewQueryStoreState(),
		Warn:   func(s string) { warnings = append(warnings, s) },
	})
	if err != nil {
		t.Fatalf("an exhausted budget is a recorded omission, not a failure: %v", err)
	}
	if res.TextFiles != 1 || res.PlanFiles != 0 {
		t.Fatalf("TextFiles = %d, PlanFiles = %d, want 1 and 0", res.TextFiles, res.PlanFiles)
	}

	dir := filepath.Join(root, filepath.FromSlash(res.Rel))
	if _, err := os.Stat(filepath.Join(dir, "query_11.sql")); err != nil {
		t.Errorf("the one file the budget allowed is missing: %v", err)
	}
	for _, gone := range []string{"query_11.plan_101.sqlplan", "query_11.stats.json", "query_22.sql"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("wrote %s past the budget", gone)
		}
	}
	// The index is written outside the budget: it is the only thing that says
	// the files beside it are an incomplete set.
	if _, err := os.Stat(filepath.Join(dir, "_index.json")); err != nil {
		t.Fatalf("the budget swallowed the index that describes the truncation: %v", err)
	}

	var refused int
	for _, o := range indexOmissions(t, root, res.Rel) {
		if o.Reason == budgetReason {
			refused++
		}
	}
	if refused == 0 {
		t.Error("_index.json records nothing about the files the budget refused")
	}
	if len(warnings) == 0 {
		t.Error("the truncation never reached the manifest's warnings")
	}

	// The partial state has to stay coherent: a query whose text was refused
	// must not still advertise one.
	b, _ := os.ReadFile(filepath.Join(dir, "_index.json"))
	var idx struct {
		Queries []struct {
			QueryID   int64    `json:"query_id"`
			TextFile  string   `json:"text_file"`
			StatsFile string   `json:"stats_file"`
			PlanFiles []string `json:"plan_files"`
		} `json:"queries"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Queries) != 2 {
		t.Fatalf("got %d queries in the index, want both listed", len(idx.Queries))
	}
	if idx.Queries[0].TextFile != "query_11.sql" || idx.Queries[0].StatsFile != "" {
		t.Errorf("query 11 = %+v, want its text named and its refused statistics blank", idx.Queries[0])
	}
	if idx.Queries[1].TextFile != "" {
		t.Errorf("query 22 names %q, a text file the budget never let it write", idx.Queries[1].TextFile)
	}
	for _, q := range idx.Queries {
		if len(q.PlanFiles) != 0 {
			t.Errorf("query %d lists plan files that were never written: %v", q.QueryID, q.PlanFiles)
		}
	}
}

func TestDetailWriterPublishesTheSelection(t *testing.T) {
	_, _, st, _ := runDetailWriter(t, detailSets())
	got := st.Selected["Sales"]
	if len(got) != 2 || got[0] != 11 || got[1] != 22 {
		t.Fatalf("Selected[Sales] = %v, want [11 22]", got)
	}
}

func profiledSets() []NamedResultSet {
	root := ResultSet{
		Columns: []string{"database", "requested_queries", "matched_plans", "last_query_plan_stats"},
		Types:   []string{"NVARCHAR", "INT", "INT", "NVARCHAR"},
		Rows:    [][]any{{"Sales", int64(2), int64(1), "ON"}},
	}
	plans := ResultSet{
		Columns: []string{"query_id", "plan_id", "match", "candidates", "query_plan"},
		Types:   []string{"BIGINT", "BIGINT", "NVARCHAR", "BIGINT", "NVARCHAR"},
		Rows: [][]any{
			{int64(11), int64(101), "plan_hash", int64(3), "<ShowPlanXML>profiled</ShowPlanXML>"},
		},
	}
	return []NamedResultSet{
		{Spec: ResultSpec{Name: "root", Shape: ShapeObject}, Set: root},
		{Spec: ResultSpec{Name: "profiled", Shape: ShapeArray}, Set: plans},
	}
}

func TestProfiledWriterNamesTheFileAfterQueryAndPlan(t *testing.T) {
	root := t.TempDir()
	st := NewQueryStoreState()
	st.Selected["Sales"] = []int64{11, 22}
	res, err := writerFor("query-store-profiled")(WriteRequest{
		Out:    newRunWriter(root, maxRunBytes, func(string) {}),
		Script: Script{Dir: "80.workload", Base: "022.query-store-profiled"},
		Unit:   DatabaseFolder{Name: "Sales", Folder: "Sales"},
		Sets:   profiledSets(),
		State:  st,
		Warn:   func(string) {},
	})
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	dir := filepath.Join(root, filepath.FromSlash(res.Rel))
	if _, err := os.Stat(filepath.Join(dir, "query_11.plan_101.actual.sqlplan")); err != nil {
		t.Errorf("missing query_11.plan_101.actual.sqlplan: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "_index.json")); err != nil {
		t.Errorf("missing _index.json: %v", err)
	}
}

// profiledIndexSummary reads back the top-level fields of a profiled-plan
// _index.json that the SKIP and no-match cases must disagree on: a SKIP sets
// Reason and leaves Omissions empty; a no-match leaves Reason empty and
// populates Omissions. Collapsing the two into one signal would hide which
// of "021 never ran here" and "021 ran and 022 found nothing" actually
// happened.
type profiledIndexSummary struct {
	Reason    string          `json:"reason"`
	Omissions []indexOmission `json:"omissions"`
}

func readProfiledIndex(t *testing.T, root, rel string) (profiledIndexSummary, []byte) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), "_index.json"))
	if err != nil {
		t.Fatalf("no index written: %v", err)
	}
	var idx profiledIndexSummary
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	return idx, b
}

// TestProfiledWriterSkipsWhenNothingWasSelected pins the SKIP branch: 021 and
// 022 run independently, and Selected["Sales"] being empty here means 021
// left nothing to match against. The profiled result set is never even read
// in that case, which is exactly why this must not be confused with a
// no-match — see TestProfiledWriterRecordsAQueryWithNoMatchAsAnOmission for
// the other side of that distinction.
func TestProfiledWriterSkipsWhenNothingWasSelected(t *testing.T) {
	root := t.TempDir()
	sets := profiledSets()
	sets[1].Set.Rows = nil
	res, err := writerFor("query-store-profiled")(WriteRequest{
		Out:    newRunWriter(root, maxRunBytes, func(string) {}),
		Script: Script{Dir: "80.workload", Base: "022.query-store-profiled"},
		Unit:   DatabaseFolder{Name: "Sales", Folder: "Sales"},
		Sets:   sets,
		State:  NewQueryStoreState(),
		Warn:   func(string) {},
	})
	if err != nil {
		t.Fatalf("a SKIP must not be an error: %v", err)
	}
	idx, b := readProfiledIndex(t, root, res.Rel)
	if idx.Reason == "" {
		t.Errorf("_index.json does not say why nothing was matched:\n%s", b)
	}
	if len(idx.Omissions) != 0 {
		t.Errorf("a SKIP recorded omissions too, blurring it with a no-match: %+v", idx.Omissions)
	}
	if !strings.Contains(string(b), "matched_plans") {
		t.Errorf("_index.json does not say how many plans matched:\n%s", b)
	}
	// A zero match must be explicable without going back to the server.
	if !strings.Contains(string(b), "last_query_plan_stats") {
		t.Errorf("_index.json does not record whether the feature was even on:\n%s", b)
	}
}

// TestProfiledWriterRecordsAQueryWithNoMatchAsAnOmission is the case the SKIP
// above must not be confused with: 021 did select query 22, 022's plans set
// has nothing for it, and that is a per-query omission with Reason left
// empty at the top level — the database was not skipped, one query in it
// just found no profiled plan.
func TestProfiledWriterRecordsAQueryWithNoMatchAsAnOmission(t *testing.T) {
	root := t.TempDir()
	st := NewQueryStoreState()
	st.Selected["Sales"] = []int64{11, 22} // only 11 has a matching row in profiledSets
	res, err := writerFor("query-store-profiled")(WriteRequest{
		Out:    newRunWriter(root, maxRunBytes, func(string) {}),
		Script: Script{Dir: "80.workload", Base: "022.query-store-profiled"},
		Unit:   DatabaseFolder{Name: "Sales", Folder: "Sales"},
		Sets:   profiledSets(),
		State:  st,
		Warn:   func(string) {},
	})
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	idx, b := readProfiledIndex(t, root, res.Rel)
	if idx.Reason != "" {
		t.Errorf("reason = %q, want empty — this database was not skipped, one query just had no match:\n%s", idx.Reason, b)
	}
	found := false
	for _, o := range idx.Omissions {
		if o.QueryID == 22 && o.PlanID == 0 && o.Reason == "no profiled plan matched this query_id by query_plan_hash" {
			found = true
		}
	}
	if !found {
		t.Errorf("_index.json does not record query 22's no-match: %+v", idx.Omissions)
	}
}

// TestProfiledWriterRecordsExtraCandidatesAsOmissions pins the >1-row branch:
// a second plan_id for the same query, sharing the hash (a recompile, or a
// genuine collision), is named as an omission rather than silently dropped,
// and only the first row's plan is ever written to disk.
func TestProfiledWriterRecordsExtraCandidatesAsOmissions(t *testing.T) {
	sets := profiledSets()
	sets[1].Set.Rows = append(sets[1].Set.Rows,
		[]any{int64(11), int64(102), "plan_hash", int64(2), "<ShowPlanXML>other</ShowPlanXML>"})
	root := t.TempDir()
	st := NewQueryStoreState()
	st.Selected["Sales"] = []int64{11}
	res, err := writerFor("query-store-profiled")(WriteRequest{
		Out:    newRunWriter(root, maxRunBytes, func(string) {}),
		Script: Script{Dir: "80.workload", Base: "022.query-store-profiled"},
		Unit:   DatabaseFolder{Name: "Sales", Folder: "Sales"},
		Sets:   sets,
		State:  st,
		Warn:   func(string) {},
	})
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	dir := filepath.Join(root, filepath.FromSlash(res.Rel))
	if _, err := os.Stat(filepath.Join(dir, "query_11.plan_101.actual.sqlplan")); err != nil {
		t.Errorf("missing the written candidate's file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "query_11.plan_102.actual.sqlplan")); err == nil {
		t.Error("wrote a file for the candidate that should have been recorded as an omission")
	}
	found := false
	for _, o := range indexOmissions(t, root, res.Rel) {
		if o.QueryID == 11 && o.PlanID == 102 &&
			strings.Contains(o.Reason, "an additional candidate plan matched the same query_plan_hash and was not written") {
			found = true
		}
	}
	if !found {
		t.Errorf("_index.json does not record plan 102 as an unwritten extra candidate: %+v", indexOmissions(t, root, res.Rel))
	}
}

// profiledPlanRow builds one root/profiled pair naming a single query and
// plan, so the oversized- and NULL-plan branches can be tested without
// disturbing profiledSets' fixture used elsewhere.
func profiledPlanRow(plan any, planBytes int64) []NamedResultSet {
	root := ResultSet{
		Columns: []string{"database", "requested_queries", "matched_plans", "last_query_plan_stats"},
		Types:   []string{"NVARCHAR", "INT", "INT", "NVARCHAR"},
		Rows:    [][]any{{"Sales", int64(1), int64(1), "ON"}},
	}
	plans := ResultSet{
		Columns: []string{"query_id", "plan_id", "match", "candidates", "query_plan", "query_plan_bytes"},
		Types:   []string{"BIGINT", "BIGINT", "NVARCHAR", "BIGINT", "NVARCHAR", "BIGINT"},
		Rows: [][]any{
			{int64(11), int64(101), "plan_hash", int64(1), plan, planBytes},
		},
	}
	return []NamedResultSet{
		{Spec: ResultSpec{Name: "root", Shape: ShapeObject}, Set: root},
		{Spec: ResultSpec{Name: "profiled", Shape: ShapeArray}, Set: plans},
	}
}

func runProfiledWriter(t *testing.T, sets []NamedResultSet, ids []int64) (root, rel string, warnings []string) {
	t.Helper()
	root = t.TempDir()
	st := NewQueryStoreState()
	st.Selected["Sales"] = ids
	res, err := writerFor("query-store-profiled")(WriteRequest{
		Out:    newRunWriter(root, maxRunBytes, func(string) {}),
		Script: Script{Dir: "80.workload", Base: "022.query-store-profiled"},
		Unit:   DatabaseFolder{Name: "Sales", Folder: "Sales"},
		Sets:   sets,
		State:  st,
		Warn:   func(s string) { warnings = append(warnings, s) },
	})
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	return root, res.Rel, warnings
}

// The cap is applied twice, exactly as it is for the detail writer: a guard
// column can null the plan out before it crosses the wire, or the plan can
// reach the writer and still be too large. Both must be distinguishable from
// a plan the DMF simply never returned.
func TestProfiledWriterCapsAnOversizedPlanFilteredByTheServer(t *testing.T) {
	root, rel, _ := runProfiledWriter(t, profiledPlanRow(nil, maxPlanBytes+1), []int64{11})
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel), "query_11.plan_101.actual.sqlplan")); err == nil {
		t.Error("wrote a file for a plan the server filtered by size")
	}
	found := false
	for _, o := range indexOmissions(t, root, rel) {
		if o.PlanID == 101 && strings.Contains(o.Reason, "exceeds the 8388608 byte per-plan cap and was not sent") {
			found = true
		}
	}
	if !found {
		t.Errorf("_index.json does not record the server-side size cap: %+v", indexOmissions(t, root, rel))
	}
}

func TestProfiledWriterCapsAnOversizedPlanThatReachedTheWriter(t *testing.T) {
	root, rel, _ := runProfiledWriter(t, profiledPlanRow(strings.Repeat("x", maxPlanBytes+1), 0), []int64{11})
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel), "query_11.plan_101.actual.sqlplan")); err == nil {
		t.Error("wrote a file for a plan over the byte cap")
	}
	found := false
	for _, o := range indexOmissions(t, root, rel) {
		if o.PlanID == 101 && o.Reason == fmt.Sprintf("plan XML of %d bytes exceeds the %d byte per-plan cap", maxPlanBytes+1, maxPlanBytes) {
			found = true
		}
	}
	if !found {
		t.Errorf("_index.json does not record the backstop cap: %+v", indexOmissions(t, root, rel))
	}
}

func TestProfiledWriterRecordsANullPlanAsAnOmission(t *testing.T) {
	root, rel, warnings := runProfiledWriter(t, profiledPlanRow(nil, 0), []int64{11})
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel), "query_11.plan_101.actual.sqlplan")); err == nil {
		t.Error("wrote a file for a NULL plan")
	}
	found := false
	for _, o := range indexOmissions(t, root, rel) {
		if o.PlanID == 101 && o.Reason == "no plan XML returned for this plan_id" {
			found = true
		}
	}
	if !found {
		t.Errorf("_index.json does not record the NULL plan: %+v", indexOmissions(t, root, rel))
	}
	if len(warnings) == 0 {
		t.Error("a NULL plan was recorded but nothing warned the manifest")
	}
}

// A budget that dies before a single plan fits must still leave a coherent
// index behind: no file, an omission naming the budget, and the manifest
// warned.
func TestProfiledWriterRecordsWhatTheBudgetRefused(t *testing.T) {
	root := t.TempDir()
	out := newRunWriter(root, 4, func(string) {}) // room for no plan at all
	st := NewQueryStoreState()
	st.Selected["Sales"] = []int64{11}
	var warnings []string
	res, err := writerFor("query-store-profiled")(WriteRequest{
		Out:    out,
		Script: Script{Dir: "80.workload", Base: "022.query-store-profiled"},
		Unit:   DatabaseFolder{Name: "Sales", Folder: "Sales"},
		Sets:   profiledSets(),
		State:  st,
		Warn:   func(s string) { warnings = append(warnings, s) },
	})
	if err != nil {
		t.Fatalf("an exhausted budget is a recorded omission, not a failure: %v", err)
	}
	dir := filepath.Join(root, filepath.FromSlash(res.Rel))
	if _, err := os.Stat(filepath.Join(dir, "query_11.plan_101.actual.sqlplan")); err == nil {
		t.Error("wrote a plan file past the budget")
	}
	if _, err := os.Stat(filepath.Join(dir, "_index.json")); err != nil {
		t.Fatalf("the budget swallowed the index that describes the truncation: %v", err)
	}
	found := false
	for _, o := range indexOmissions(t, root, res.Rel) {
		if o.PlanID == 101 && o.Reason == budgetReason {
			found = true
		}
	}
	if !found {
		t.Error("_index.json does not record the budget refusal")
	}
	if len(warnings) == 0 {
		t.Error("the budget refusal never reached the manifest's warnings")
	}
}
