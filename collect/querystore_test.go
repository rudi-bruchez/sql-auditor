package collect

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
		// The four window bounds are DATETIMEOFFSET, and not by choice: two of
		// them are @qs_from and @qs_to projected straight back, and the driver
		// sends a time.Time as datetimeoffset(7) on every TDS version this tool
		// speaks; the other two are those parameters compared against
		// sys.query_store_runtime_stats_interval, which is datetimeoffset as
		// well. window.intervals is a COUNT(*), an int, and selection.cap is
		// @qs_top, which reaches the server as a bigint because the driver
		// widens Go's int.
		Types: []string{"NVARCHAR", "NVARCHAR", "INT", "DATETIMEOFFSET", "DATETIMEOFFSET",
			"DATETIMEOFFSET", "DATETIMEOFFSET", "INT", "BIGINT"},
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
	// is_forced_selection is last, and the tests that poke at this fixture by
	// column position depend on that: it says why the QUERY is in the
	// selection, where is_forced says what one PLAN is.
	selected := ResultSet{
		Columns: []string{"query_id", "plan_id", "text", "query_plan", "query_plan_bytes", "is_forced",
			"rank.duration", "rank.cpu", "rank.logical_reads", "rank.executions", "is_forced_selection"},
		Types: []string{"BIGINT", "BIGINT", "NVARCHAR", "NVARCHAR", "BIGINT", "BIT", "BIGINT", "BIGINT", "BIGINT", "BIGINT", "BIT"},
		Rows: [][]any{
			{int64(11), int64(101), "SELECT a FROM t", "<ShowPlanXML>a</ShowPlanXML>", int64(28), false, int64(1), int64(2), int64(40), int64(900), true},
			{int64(11), int64(102), "SELECT a FROM t", "<ShowPlanXML>b</ShowPlanXML>", int64(28), true, int64(1), int64(2), int64(40), int64(900), true},
			// query_plan NULL with query_plan_bytes 0: the Query Store holds no
			// plan at all, which is not the same as one dropped by the cap.
			{int64(22), int64(201), "SELECT b FROM u", nil, int64(0), false, int64(77), int64(3), int64(5), int64(12), false},
		},
	}
	intervals := ResultSet{
		// start_time is projected raw from
		// sys.query_store_runtime_stats_interval, which declares it
		// datetimeoffset — the Query Store stores the offset, and the archive
		// keeps it.
		Columns: []string{"query_id", "plan_id", "start_time", "count_executions"},
		Types:   []string{"BIGINT", "BIGINT", "DATETIMEOFFSET", "BIGINT"},
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
		Out:    newRunWriter(root, maxRunBytes),
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

// detailSetsWithPlanRanks is detailSets with 021's planRank columns attached and
// query 11 grown to four plans, the last of which sits past the per-query cap.
//
// The columns are appended rather than added to detailSets itself because the
// tests that reach into that fixture by column position document
// is_forced_selection as its last column.
func detailSetsWithPlanRanks() []NamedResultSet {
	sets := detailSets()
	sel, _ := setByName(sets, "selected")
	sel.Columns = append(sel.Columns, "plan.rank", "plan.count")
	sel.Types = append(sel.Types, "BIGINT", "BIGINT")
	// query 11 has four plans now, query 22 still one.
	ranks := [][]any{{int64(1), int64(4)}, {int64(2), int64(4)}, {int64(1), int64(1)}}
	for r := range sel.Rows {
		sel.Rows[r] = append(sel.Rows[r], ranks[r]...)
	}
	sel.Rows = append(sel.Rows,
		[]any{int64(11), int64(103), "SELECT a FROM t", "<ShowPlanXML>c</ShowPlanXML>", int64(28), false,
			int64(1), int64(2), int64(40), int64(900), true, int64(3), int64(4)},
		// Rank 4 of 4: the XML nulled by the SQL's CASE, the size still reported.
		[]any{int64(11), int64(104), "SELECT a FROM t", nil, int64(4096), false,
			int64(1), int64(2), int64(40), int64(900), true, int64(4), int64(4)},
	)
	for i := range sets {
		if sets[i].Spec.Name == "selected" {
			sets[i].Set = sel
		}
	}
	return sets
}

// A plan past the per-query cap arrives with the same NULL query_plan as a plan
// the Query Store does not hold, and the two must never be reported as the same
// thing: one is a decision this collector took, the other a fact about the
// server. The rank is what separates them, and this is the test that says so.
func TestDetailWriterRecordsAPlanBeyondThePerQueryCap(t *testing.T) {
	root, rel, _, _ := runDetailWriter(t, detailSetsWithPlanRanks())
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel), "query_11.plan_104.sqlplan")); err == nil {
		t.Error("wrote a .sqlplan for a plan beyond the per-query cap")
	}
	var got indexOmission
	for _, o := range indexOmissions(t, root, rel) {
		if o.PlanID == 104 {
			got = o
		}
		if o.PlanID == 104 && strings.Contains(o.Reason, "holds no plan XML") {
			t.Errorf("a plan dropped by the per-query cap is reported as one the Query Store does not hold: %q", o.Reason)
		}
	}
	if got.PlanID != 104 {
		t.Fatalf("_index.json records no omission for plan 104: %+v", indexOmissions(t, root, rel))
	}
	if !strings.Contains(got.Reason, fmt.Sprintf("per-query cap of %d plans", maxPlansPerQuery)) {
		t.Errorf("omission reason does not name the cap: %q", got.Reason)
	}
	if got.Bytes != 4096 {
		t.Errorf("bytes = %d, want the size the server reported for the plan that was not fetched", got.Bytes)
	}
	// The plan stays in the index with no file: dropping the row would erase the
	// record that the plan exists.
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), "_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Selection struct {
			PlansBeyondCap int `json:"plans_beyond_cap"`
		} `json:"selection"`
		Queries []struct {
			QueryID   int64    `json:"query_id"`
			PlanIDs   []int64  `json:"plan_ids"`
			PlanFiles []string `json:"plan_files"`
		} `json:"queries"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	if idx.Selection.PlansBeyondCap != 1 {
		t.Errorf("selection.plans_beyond_cap = %d, want 1", idx.Selection.PlansBeyondCap)
	}
	for _, q := range idx.Queries {
		if q.QueryID != 11 {
			continue
		}
		if len(q.PlanIDs) != 4 {
			t.Errorf("query 11 lists %v, want all four plan ids including the one with no file", q.PlanIDs)
		}
		for _, f := range q.PlanFiles {
			if strings.Contains(f, "plan_104") {
				t.Errorf("plan_files names %s, which was never written", f)
			}
		}
	}
}

// indexOmissions reads back the omissions _index.json records. Matching on the
// raw text would not do: every index carries "selection": {"cap": …}
// unconditionally, so a substring check for "cap" passes against a writer that
// records no omission at all.
type indexOmission struct {
	QueryID int64  `json:"query_id"`
	PlanID  int64  `json:"plan_id"`
	File    string `json:"file"`
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

// A database on which the Query Store was never enabled returns NO row in
// sys.database_query_store_options, and 021's LEFT JOIN from sys.databases
// therefore delivers state.actual as NULL. The SQL says so in as many words —
// "the writer reads exactly like OFF" — and this pins that promise: a writer
// testing for the literal "OFF" alone takes the full path here and writes an
// index whose state is null, over a database it never actually read.
func TestDetailWriterReadsANullStateAsOff(t *testing.T) {
	sets := detailSets()
	rt, _ := setByName(sets, "root")
	rt.Rows[0][1] = nil // state.actual, the LEFT JOIN's NULL
	sel, _ := setByName(sets, "selected")
	sel.Rows = nil
	root, rel, st, _ := runDetailWriter(t, sets)
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "_index.json" {
		t.Fatalf("got %d entries, want only _index.json — a NULL state was read as a running store", len(entries))
	}
	assertSelectionPublishedEmpty(t, st)
}

// assertSelectionPublishedEmpty pins the invariant queryStoreArgs relies on: an
// entry that is empty means 021 ran here and retained nothing, which its own
// index already records; NO entry at all means 021 delivered nothing, and the
// manifest carries a warning saying so. A writer that ran, wrote its index and
// left no entry makes that warning accuse a collector which did its job.
func assertSelectionPublishedEmpty(t *testing.T, st *QueryStoreState) {
	t.Helper()
	got, ok := st.Selected["Sales"]
	if !ok {
		t.Error("the writer ran and wrote its index but published no selection at all; the manifest will warn that 021 delivered nothing")
		return
	}
	if len(got) != 0 {
		t.Errorf("Selected[Sales] = %v, want an empty selection", got)
	}
}

func TestDetailWriterPublishesAnEmptySelectionWhenTheStoreIsOff(t *testing.T) {
	sets := detailSets()
	rt, _ := setByName(sets, "root")
	rt.Rows[0][1] = "OFF"
	sel, _ := setByName(sets, "selected")
	sel.Rows = nil
	_, _, st, _ := runDetailWriter(t, sets)
	assertSelectionPublishedEmpty(t, st)
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
		Out:    newRunWriter(root, maxRunBytes),
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
	out := newRunWriter(root, len("SELECT a FROM t"))
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
	// The files the budget refused are named one by one: a refusal on
	// query_11.stats.json and one on query_22.sql are different losses, and
	// query id 11 with plan id 0 says neither of them.
	refusedFiles := map[string]bool{}
	for _, o := range indexOmissions(t, root, res.Rel) {
		if o.Reason == budgetReason(out.budget) {
			refused++
			refusedFiles[o.File] = true
		}
	}
	if refused == 0 {
		t.Error("_index.json records nothing about the files the budget refused")
	}
	for _, name := range []string{"query_11.stats.json", "query_22.sql"} {
		if !refusedFiles[name] {
			t.Errorf("no budget omission names %s: %+v", name, indexOmissions(t, root, res.Rel))
		}
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

// detailIndexSelection reads back the fields of _index.json that say why a
// query is in the archive at all: the selection counts, and each query's own
// forced flag.
func detailIndexSelection(t *testing.T, root, rel string) (counts struct {
	Cap    int64 `json:"cap"`
	Ranked int   `json:"ranked"`
	Forced int   `json:"forced"`
}, byQuery map[int64]bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), "_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Selection struct {
			Cap    int64 `json:"cap"`
			Ranked int   `json:"ranked"`
			Forced int   `json:"forced"`
		} `json:"selection"`
		Queries []struct {
			QueryID int64 `json:"query_id"`
			Forced  bool  `json:"forced"`
		} `json:"queries"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	byQuery = map[int64]bool{}
	for _, q := range idx.Queries {
		byQuery[q.QueryID] = q.Forced
	}
	return idx.Selection, byQuery
}

// TestDetailWriterReadsForcedFromTheSelection is the case is_forced cannot
// answer: query 33 has one plan with is_forced_plan = 0, and it is in the
// archive only because force_failure_count > 0 put it there — a forcing
// decision that has silently stopped being applied, which is precisely why the
// forced CTE exists. Reading the flag from the plan row would file it as an
// ordinary query and count it nowhere.
//
// Query 11, forced with TWO plans, pins the other half: the count is of
// queries, not of plan rows.
func TestDetailWriterReadsForcedFromTheSelection(t *testing.T) {
	sets := detailSets()
	sel := &sets[1].Set
	sel.Rows = append(sel.Rows,
		[]any{int64(33), int64(301), "SELECT c FROM v", "<ShowPlanXML>c</ShowPlanXML>", int64(28),
			false, nil, nil, nil, nil, true})
	root, rel, _, _ := runDetailWriter(t, sets)

	counts, byQuery := detailIndexSelection(t, root, rel)
	if !byQuery[33] {
		t.Error("query 33 is recorded as not forced, but it is in the selection only because a plan of its own can no longer be forced")
	}
	if !byQuery[11] {
		t.Error("query 11 is recorded as not forced although one of its plans is")
	}
	if byQuery[22] {
		t.Error("query 22 is recorded as forced; it entered on its ranks alone")
	}
	if counts.Forced != 2 {
		t.Errorf("selection.forced = %d, want 2 — it counts queries, and query 11's two plans are one query", counts.Forced)
	}
	// Query 33 has no rank at all: the SQL LEFT JOINs the rankings, and a
	// forced-only query arrives with all four NULL. Counting it as ranked would
	// report a ranked population of 3 against a cap of 50 that only ever
	// admitted 2 — and, on a full run, a ranked count above the cap, which
	// reads as a leak. Query 11 is both ranked and forced, and belongs in both.
	if counts.Ranked != 2 {
		t.Errorf("selection.ranked = %d, want 2 — a query with no rank did not enter on the ranking", counts.Ranked)
	}
}

// TestDetailIndexRendersTimestampsWithTheirOffset pins the rendered strings of
// the timestamps that reach the archive through the generic encoder rather than
// through encodedAt — the window block of _index.json, and the interval start
// times inside a query's statistics file. Nothing else asserted them, so the
// declared column type could drift from the real one and every test would stay
// green while production emitted a different shape: DATETIME2 renders without an
// offset, DATETIMEOFFSET as RFC3339 with a Z, and both of these columns are
// datetimeoffset at the server.
func TestDetailIndexRendersTimestampsWithTheirOffset(t *testing.T) {
	root, rel, _, _ := runDetailWriter(t, detailSets())
	dir := filepath.Join(root, filepath.FromSlash(rel))

	b, err := os.ReadFile(filepath.Join(dir, "_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Window struct {
			RequestedFrom string `json:"requested_from"`
			EffectiveTo   string `json:"effective_to"`
		} `json:"window"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	if idx.Window.RequestedFrom != "2026-08-06T00:00:00Z" || idx.Window.EffectiveTo != "2026-08-13T00:00:00Z" {
		t.Errorf("window = %+v, want the bounds rendered as datetimeoffset — the parameters reach the server as one", idx.Window)
	}

	b, err = os.ReadFile(filepath.Join(dir, "query_11.stats.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stats struct {
		Intervals []struct {
			StartTime string `json:"start_time"`
		} `json:"intervals"`
	}
	if err := json.Unmarshal(b, &stats); err != nil {
		t.Fatal(err)
	}
	if len(stats.Intervals) != 1 {
		t.Fatalf("got %d intervals in the statistics file, want 1", len(stats.Intervals))
	}
	if stats.Intervals[0].StartTime != "2026-08-06T01:00:00Z" {
		t.Errorf("interval start_time = %q, want the offset the Query Store stores", stats.Intervals[0].StartTime)
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
		// DATETIME, not DATETIMEOFFSET: the column is
		// sys.dm_exec_query_stats.last_execution_time, which is a datetime, and
		// the encoder renders the two differently on purpose. The
		// datetimeoffset one is the Query Store's, which 021 reads.
		Columns: []string{"query_id", "plan_id", "match", "candidates", "last_execution_time", "query_plan"},
		Types:   []string{"BIGINT", "BIGINT", "NVARCHAR", "INT", "DATETIME", "NVARCHAR"},
		Rows: [][]any{
			{int64(11), int64(101), "plan_hash", int64(3),
				time.Date(2026, 8, 13, 9, 30, 0, 0, time.UTC), "<ShowPlanXML>profiled</ShowPlanXML>"},
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
		Out:    newRunWriter(root, maxRunBytes),
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
		Out:    newRunWriter(root, maxRunBytes),
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
		Out:    newRunWriter(root, maxRunBytes),
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

// TestProfiledWriterWritesEveryDistinctPlanOfAQuery pins the >1-row branch. The
// result set's grain is one row per (query_id, plan_id), so a second row for
// query 11 is a SECOND QUERY STORE PLAN still resident in cache — a different
// plan for the same query, which is the whole finding when a query has two.
// It is written, like the detail writer writes every plan of a query.
//
// The hash-collision case is a different one and never reaches here: several
// cached plan_handles for ONE plan_id are counted by `candidates`, inside the
// SQL, and only the most recently executed of them is returned.
// The two rows carry DIFFERENT candidates and different timestamps on purpose:
// each entry must take its metadata from its OWN row. Reading them from row 0
// — which is what this fix replaced — would write two files and describe the
// second with the first's numbers, and identical fixture values would let that
// through.
func TestProfiledWriterWritesEveryDistinctPlanOfAQuery(t *testing.T) {
	sets := profiledSets()
	sets[1].Set.Rows = append(sets[1].Set.Rows,
		[]any{int64(11), int64(102), "plan_hash", int64(7),
			time.Date(2026, 8, 13, 9, 45, 0, 0, time.UTC), "<ShowPlanXML>other</ShowPlanXML>"})
	root, res, _ := runProfiledWriter(t, sets, []int64{11})
	dir := filepath.Join(root, filepath.FromSlash(res.Rel))
	for _, name := range []string{"query_11.plan_101.actual.sqlplan", "query_11.plan_102.actual.sqlplan"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing %s: a distinct plan_id was discarded: %v", name, err)
		}
	}
	if o := indexOmissions(t, root, res.Rel); len(o) != 0 {
		t.Errorf("a distinct plan was recorded as an omission: %+v", o)
	}
	// The disclosure count, now that one query can contribute several plans.
	if res.PlanFiles != 2 {
		t.Errorf("PlanFiles = %d, want 2 — the manifest would under-report what is on disk", res.PlanFiles)
	}
	plans := profiledIndexPlans(t, root, res.Rel)
	if len(plans) != 2 {
		t.Fatalf("_index.json lists %d plans, want both", len(plans))
	}
	for _, p := range plans {
		wantCandidates, wantLast := int64(3), "2026-08-13T09:30:00"
		if p.PlanID == 102 {
			wantCandidates, wantLast = 7, "2026-08-13T09:45:00"
		}
		if p.Candidates != wantCandidates || p.LastExecution != wantLast {
			t.Errorf("plan %d = candidates %d, last_execution %q; want %d and %q — its metadata was read from another row",
				p.PlanID, p.Candidates, p.LastExecution, wantCandidates, wantLast)
		}
	}
}

// A repeated plan_id cannot come out of the SQL as written — it keeps one
// cached plan per plan_id — so this pins the guard for the day that ranking is
// edited away: the file is written once and the repeat is named, rather than
// overwriting a file and being counted twice.
func TestProfiledWriterRecordsARepeatedPlanIDAsAnOmission(t *testing.T) {
	sets := profiledSets()
	sets[1].Set.Rows = append(sets[1].Set.Rows,
		[]any{int64(11), int64(101), "plan_hash", int64(3),
			time.Date(2026, 8, 13, 9, 30, 0, 0, time.UTC), "<ShowPlanXML>again</ShowPlanXML>"})
	root, res, _ := runProfiledWriter(t, sets, []int64{11})
	rel := res.Rel
	if got := len(profiledIndexPlans(t, root, rel)); got != 1 {
		t.Errorf("_index.json lists %d plans for one plan_id", got)
	}
	if res.PlanFiles != 1 {
		t.Errorf("PlanFiles = %d, want 1 — the repeat was counted as a file of its own", res.PlanFiles)
	}
	found := false
	for _, o := range indexOmissions(t, root, rel) {
		if o.QueryID == 11 && o.PlanID == 101 &&
			strings.Contains(o.Reason, "a further row was returned for this plan_id") {
			found = true
		}
	}
	if !found {
		t.Errorf("the repeated plan_id was dropped without a trace: %+v", indexOmissions(t, root, rel))
	}
}

// profiledIndexPlans reads back the plans array of a profiled _index.json.
func profiledIndexPlans(t *testing.T, root, rel string) []struct {
	QueryID       int64  `json:"query_id"`
	PlanID        int64  `json:"plan_id"`
	Match         string `json:"match"`
	Candidates    int64  `json:"candidates"`
	LastExecution string `json:"last_execution"`
	PlanFile      string `json:"plan_file"`
} {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), "_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Plans []struct {
			QueryID       int64  `json:"query_id"`
			PlanID        int64  `json:"plan_id"`
			Match         string `json:"match"`
			Candidates    int64  `json:"candidates"`
			LastExecution string `json:"last_execution"`
			PlanFile      string `json:"plan_file"`
		} `json:"plans"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	return idx.Plans
}

// A profiled plan comes out of a live cache, and when it last ran is what says
// whether it is the plan from the incident being investigated. It is rendered
// by the corpus encoder, like every other timestamp in the archive.
func TestProfiledIndexRecordsWhenTheCachedPlanLastRan(t *testing.T) {
	root, res, _ := runProfiledWriter(t, profiledSets(), []int64{11})
	plans := profiledIndexPlans(t, root, res.Rel)
	if len(plans) != 1 {
		t.Fatalf("got %d plans in the index, want 1", len(plans))
	}
	// No Z: the DMV's column is a datetime, so this is server local time and
	// the encoder refuses to assert a UTC conversion nobody performed.
	if plans[0].LastExecution != "2026-08-13T09:30:00" {
		t.Errorf("last_execution = %q, want the datetime the encoder renders, without an offset", plans[0].LastExecution)
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
		Types:   []string{"BIGINT", "BIGINT", "NVARCHAR", "INT", "NVARCHAR", "BIGINT"},
		Rows: [][]any{
			{int64(11), int64(101), "plan_hash", int64(1), plan, planBytes},
		},
	}
	return []NamedResultSet{
		{Spec: ResultSpec{Name: "root", Shape: ShapeObject}, Set: root},
		{Spec: ResultSpec{Name: "profiled", Shape: ShapeArray}, Set: plans},
	}
}

// runProfiledWriter returns the WriteResult whole rather than just its Rel: the
// counts in it are what the manifest discloses, and a helper that dropped them
// would leave the disclosure untestable from here.
func runProfiledWriter(t *testing.T, sets []NamedResultSet, ids []int64) (root string, res WriteResult, warnings []string) {
	t.Helper()
	root = t.TempDir()
	st := NewQueryStoreState()
	st.Selected["Sales"] = ids
	res, err := writerFor("query-store-profiled")(WriteRequest{
		Out:    newRunWriter(root, maxRunBytes),
		Script: Script{Dir: "80.workload", Base: "022.query-store-profiled"},
		Unit:   DatabaseFolder{Name: "Sales", Folder: "Sales"},
		Sets:   sets,
		State:  st,
		Warn:   func(s string) { warnings = append(warnings, s) },
	})
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	return root, res, warnings
}

// The cap is applied twice, exactly as it is for the detail writer: a guard
// column can null the plan out before it crosses the wire, or the plan can
// reach the writer and still be too large. Both must be distinguishable from
// a plan the DMF simply never returned.
func TestProfiledWriterCapsAnOversizedPlanFilteredByTheServer(t *testing.T) {
	root, res, _ := runProfiledWriter(t, profiledPlanRow(nil, maxPlanBytes+1), []int64{11})
	rel := res.Rel
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
	root, res, _ := runProfiledWriter(t, profiledPlanRow(strings.Repeat("x", maxPlanBytes+1), 0), []int64{11})
	rel := res.Rel
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
	root, res, warnings := runProfiledWriter(t, profiledPlanRow(nil, 0), []int64{11})
	rel := res.Rel
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
	out := newRunWriter(root, 4) // room for no plan at all
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
		if o.PlanID == 101 && o.Reason == budgetReason(out.budget) {
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

// TestProfiledIndexKeepsAnEntryForAPlanItCouldNotWrite is finding 1 of the
// final review: a plan that was NULL, oversized or refused by the budget used
// to be dropped from `plans` entirely, so the rows most in need of explaining
// were the ones the index said nothing about. `match`, `candidates` and above
// all `last_execution` — which says whether the cached plan is the one from
// the incident — exist per plan and nowhere else, and the omission beside it
// carries none of them. The entry goes in either way, with plan_file empty.
func TestProfiledIndexKeepsAnEntryForAPlanItCouldNotWrite(t *testing.T) {
	sets := profiledSets()
	sets[1].Set.Rows[0][5] = nil // query_plan: the DMF returned none
	root, res, _ := runProfiledWriter(t, sets, []int64{11})
	plans := profiledIndexPlans(t, root, res.Rel)
	if len(plans) != 1 {
		t.Fatalf("_index.json lists %d plans, want the unwritten plan listed with what is known about it", len(plans))
	}
	p := plans[0]
	if p.PlanFile != "" {
		t.Errorf("plan_file = %q, but no file was written", p.PlanFile)
	}
	if p.PlanID != 101 || p.Match != "plan_hash" || p.Candidates != 3 {
		t.Errorf("plan = %+v, want plan 101 with its match and its 3 candidates", p)
	}
	if p.LastExecution != "2026-08-13T09:30:00" {
		t.Errorf("last_execution = %q, want the time the cached plan last ran", p.LastExecution)
	}
	// The omission stays: the two carry different things, and only it says why
	// there is no file.
	found := false
	for _, o := range indexOmissions(t, root, res.Rel) {
		if o.PlanID == 101 && o.Reason == "no plan XML returned for this plan_id" {
			found = true
		}
	}
	if !found {
		t.Errorf("the entry replaced the omission instead of joining it: %+v", indexOmissions(t, root, res.Rel))
	}
}

// TestProfiledWriterDoesNotWarnAboutAnOrdinaryNoMatch is finding 4. 022's own
// header states that the four conditions the DMF needs being false is the
// ordinary case and not a fault, and LAST_QUERY_PLAN_STATS is off by default:
// warning per query puts one line per selected query per database in the
// manifest — a thousand of them on a twenty-database run at the default cap —
// all restating what root.last_query_plan_stats says once. It belongs in the
// index, which is what this asserts, and not in the operator's warnings.
func TestProfiledWriterDoesNotWarnAboutAnOrdinaryNoMatch(t *testing.T) {
	root, res, warnings := runProfiledWriter(t, profiledSets(), []int64{11, 22})
	found := false
	for _, o := range indexOmissions(t, root, res.Rel) {
		if o.QueryID == 22 && o.Reason == "no profiled plan matched this query_id by query_plan_hash" {
			found = true
		}
	}
	if !found {
		t.Errorf("query 22's no-match left no trace in _index.json: %+v", indexOmissions(t, root, res.Rel))
	}
	for _, w := range warnings {
		if strings.Contains(w, "no profiled plan matched") {
			t.Errorf("the ordinary no-match reached the manifest: %q", w)
		}
	}
}

// TestPlanCapIsTheSameNumberInTheCorpus binds maxPlanBytes to the literal in
// 021 and 022. The three are one rule written three times, and a comment
// saying so is not enforcement: raise the Go constant alone and an oversized
// plan arrives NULL with a size the writer no longer considers oversized, so
// the archive states "the Query Store holds no plan XML for this plan_id" —
// a false fact about the server, which is the failure the omissions exist to
// prevent. Lower it alone and every large plan is refused twice over with the
// wrong size reported. The corpus is read from disk, like Discover's own test
// does, because that is the file a DBA exports and reads.
func TestPlanCapIsTheSameNumberInTheCorpus(t *testing.T) {
	guard := regexp.MustCompile(`DATALENGTH\(\w+\.query_plan\) <= (\d+)`)
	for _, name := range []string{
		"021.query-store-detail.sql",
		"022.query-store-profiled.sql",
	} {
		b, err := os.ReadFile(filepath.Join("..", "queries", "80.workload", name))
		if err != nil {
			t.Fatal(err)
		}
		m := guard.FindAllStringSubmatch(string(b), -1)
		if len(m) != 1 {
			t.Errorf("%s has %d DATALENGTH guards on query_plan, want exactly 1", name, len(m))
			continue
		}
		got, err := strconv.Atoi(m[0][1])
		if err != nil {
			t.Fatal(err)
		}
		if got != maxPlanBytes {
			t.Errorf("%s caps the plan at %d bytes, maxPlanBytes is %d — the two are one rule and have drifted", name, got, maxPlanBytes)
		}
	}
}

// The sibling of the test above, for the other literal in the same CASE. Only
// 021 carries it: 022 fetches one cached plan per Query Store plan the detail
// writer already selected, so there is no per-query population there to cap.
//
// The drift this catches is the same one, reached the other way: maxPlansPerQuery
// raised above a stale SQL literal makes the fourth plan arrive NULL and be
// reported as a plan the Query Store does not hold, which is a false fact about
// the server.
func TestPlanCountCapIsTheSameNumberInTheCorpus(t *testing.T) {
	const name = "021.query-store-detail.sql"
	b, err := os.ReadFile(filepath.Join("..", "queries", "80.workload", name))
	if err != nil {
		t.Fatal(err)
	}
	guard := regexp.MustCompile(`\w+\.plan_rank <= (\d+)`)
	m := guard.FindAllStringSubmatch(string(b), -1)
	if len(m) != 1 {
		t.Fatalf("%s has %d plan_rank guards, want exactly 1", name, len(m))
	}
	got, err := strconv.Atoi(m[0][1])
	if err != nil {
		t.Fatal(err)
	}
	if got != maxPlansPerQuery {
		t.Errorf("%s keeps %d plans per query, maxPlansPerQuery is %d — the two are one rule and have drifted", name, got, maxPlansPerQuery)
	}
}

// A write can fail for reasons that have nothing to do with the budget: a full
// disk, a share withdrawn under a running collection, permissions dropped
// mid-run. By then query text and plans are already on disk, and a writer that
// returned the error without writing _index.json would leave exactly the
// undescribed directory the index exists to prevent — the failure the budget
// path has always handled and only the error path leaked.
//
// The failure is provoked with a DIRECTORY sitting where a plan file must go:
// os.WriteFile refuses that on every platform this runs on, after the query's
// text file has already landed.
func TestDetailWriterLeavesAnIndexWhenAWriteFails(t *testing.T) {
	root := t.TempDir()
	rel := "80.workload/Sales/021.query-store-detail"
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel), "query_11.plan_101.sqlplan"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := writerFor("query-store-detail")(WriteRequest{
		Out:    newRunWriter(root, maxRunBytes),
		Script: Script{Path: "80.workload/021.query-store-detail.sql", Dir: "80.workload", Base: "021.query-store-detail"},
		Unit:   DatabaseFolder{Name: "Sales", Folder: "Sales"},
		Sets:   detailSets(),
		State:  NewQueryStoreState(),
		Warn:   func(string) {},
	})
	if err == nil {
		t.Fatal("a failed write must still be an error")
	}
	dir := filepath.Join(root, filepath.FromSlash(res.Rel))
	if _, err := os.Stat(filepath.Join(dir, "query_11.sql")); err != nil {
		t.Fatalf("the text file that did land is missing, so this tests nothing: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "_index.json"))
	if err != nil {
		t.Fatalf("a directory of query text and plans was left with no index: %v", err)
	}
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
	if len(idx.Queries) != 1 || idx.Queries[0].QueryID != 11 {
		t.Fatalf("_index.json lists %+v, want the query whose text is on disk", idx.Queries)
	}
	if idx.Queries[0].TextFile != "query_11.sql" {
		t.Errorf("text_file = %q, want the file that is actually there", idx.Queries[0].TextFile)
	}
	// Nothing the writer never wrote is claimed: the plan write is what failed,
	// and the statistics file never got its turn.
	if len(idx.Queries[0].PlanFiles) != 0 || idx.Queries[0].StatsFile != "" {
		t.Errorf("query 11 = %+v, want no plan file and no statistics file named", idx.Queries[0])
	}
}

// The profiled writer's half of the same finding.
func TestProfiledWriterLeavesAnIndexWhenAWriteFails(t *testing.T) {
	root := t.TempDir()
	rel := "80.workload/Sales/022.query-store-profiled"
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel), "query_11.plan_101.actual.sqlplan"), 0o755); err != nil {
		t.Fatal(err)
	}
	st := NewQueryStoreState()
	st.Selected["Sales"] = []int64{11}
	res, err := writerFor("query-store-profiled")(WriteRequest{
		Out:    newRunWriter(root, maxRunBytes),
		Script: Script{Dir: "80.workload", Base: "022.query-store-profiled"},
		Unit:   DatabaseFolder{Name: "Sales", Folder: "Sales"},
		Sets:   profiledSets(),
		State:  st,
		Warn:   func(string) {},
	})
	if err == nil {
		t.Fatal("a failed write must still be an error")
	}
	plans := profiledIndexPlans(t, root, res.Rel)
	if len(plans) != 1 || plans[0].PlanID != 101 {
		t.Fatalf("_index.json lists %+v, want the plan it was writing when the disk refused it", plans)
	}
	if plans[0].PlanFile != "" {
		t.Errorf("plan_file = %q, but that write is what failed", plans[0].PlanFile)
	}
}
