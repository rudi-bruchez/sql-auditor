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

// deadlockSets builds what 061.deadlock-graphs.sql returns: two ordinary
// deadlocks, one past the count cap, one above the byte cap, and one the ring
// recorded without a report. The absences are the point of the fixture.
func deadlockSets() []NamedResultSet {
	root := ResultSet{
		Columns: []string{"session.running", "session.deadlocks",
			"session.earliest_deadlock", "session.latest_deadlock",
			"caps.graphs", "caps.graph_bytes"},
		Types: []string{"BIT", "INT", "NVARCHAR", "NVARCHAR", "INT", "INT"},
		Rows: [][]any{{true, int64(412), "2026-08-11T02:14:07.100", "2026-08-13T09:44:51.883",
			int64(maxDeadlockGraphs), int64(maxDeadlockBytes)}},
	}
	graph := `<deadlock><victim-list/></deadlock>`
	deadlocks := ResultSet{
		Columns: []string{"graph.rank", "graph.count", "occurred_at", "graph", "graph_bytes"},
		Types:   []string{"BIGINT", "BIGINT", "NVARCHAR", "NVARCHAR", "BIGINT"},
		Rows: [][]any{
			{int64(1), int64(412), "2026-08-13T09:44:51.883", graph, int64(len(graph))},
			{int64(2), int64(412), "2026-08-13T09:41:12.007", graph, int64(len(graph))},
			// Above the byte cap: the SQL nulled it, the size still arrives.
			{int64(3), int64(412), "2026-08-13T08:02:44.512", nil, int64(maxDeadlockBytes + 1)},
			// The ring recorded the event and holds no report for it.
			{int64(4), int64(412), "2026-08-12T22:19:03.774", nil, nil},
			// Past the count cap.
			{int64(maxDeadlockGraphs + 1), int64(412), "2026-08-11T02:14:07.100", nil, int64(900)},
		},
	}
	return []NamedResultSet{
		{Spec: ResultSpec{Name: "root", Shape: ShapeObject}, Set: root},
		{Spec: ResultSpec{Name: "deadlocks", Shape: ShapeArray}, Set: deadlocks},
	}
}

func runDeadlockWriter(t *testing.T, sets []NamedResultSet, budget int) (root, rel string, res WriteResult, warnings []string) {
	t.Helper()
	root = t.TempDir()
	req := WriteRequest{
		Out:    newRunWriter(root, budget),
		Script: Script{Path: "10.system/061.deadlock-graphs.sql", Dir: "10.system", Base: "061.deadlock-graphs"},
		// Instance-scoped: runUnit hands a writer an empty DatabaseFolder, and
		// the fixture reproduces that rather than inventing a database.
		Unit:  DatabaseFolder{},
		Sets:  sets,
		State: NewQueryStoreState(),
		Warn:  func(s string) { warnings = append(warnings, s) },
	}
	w := writerFor("deadlock-graphs")
	if w == nil {
		t.Fatal("writerFor(deadlock-graphs) = nil")
	}
	res, err := w(req)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	return root, res.Rel, res, warnings
}

// The directory sits directly under the area, with no database level: the
// system_health ring buffer belongs to the instance. A path with an empty
// segment in it would be the symptom of a writer reading a DatabaseFolder it
// was never given.
func TestDeadlockWriterWritesOutsideAnyDatabaseFolder(t *testing.T) {
	root, rel, res, _ := runDeadlockWriter(t, deadlockSets(), maxRunBytes)
	if rel != "10.system/061.deadlock-graphs" {
		t.Fatalf("rel = %q, want 10.system/061.deadlock-graphs", rel)
	}
	dir := filepath.Join(root, filepath.FromSlash(rel))
	for _, name := range []string{"_index.json", "deadlock_001.xdl", "deadlock_002.xdl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	if res.GraphFiles != 2 {
		t.Errorf("GraphFiles = %d, want 2", res.GraphFiles)
	}
	// The three counters that latch other disclosures must stay at zero, or an
	// archive of deadlock reports would announce plans, query text or module
	// source it does not hold.
	if res.PlanFiles != 0 || res.TextFiles != 0 || res.DefinitionFiles != 0 {
		t.Errorf("PlanFiles=%d TextFiles=%d DefinitionFiles=%d, want all 0: each latches a different disclosure",
			res.PlanFiles, res.TextFiles, res.DefinitionFiles)
	}
}

func TestDeadlockWriterWritesTheGraphVerbatim(t *testing.T) {
	root, rel, _, _ := runDeadlockWriter(t, deadlockSets(), maxRunBytes)
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), "deadlock_001.xdl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `<deadlock><victim-list/></deadlock>` {
		t.Errorf("deadlock_001.xdl = %q, want the report unchanged", string(b))
	}
}

type deadlockIndexFile struct {
	Counts struct {
		InRing   int    `json:"in_ring"`
		Written  int    `json:"written"`
		Earliest string `json:"earliest"`
		Latest   string `json:"latest"`
	} `json:"counts"`
	Deadlocks []struct {
		Rank int64  `json:"rank"`
		File string `json:"file"`
	} `json:"deadlocks"`
	Omissions []struct {
		Rank   int64  `json:"rank"`
		Reason string `json:"reason"`
		Bytes  int64  `json:"bytes"`
	} `json:"omissions"`
}

func readDeadlockIndex(t *testing.T, root, rel string) deadlockIndexFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), "_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx deadlockIndexFile
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	return idx
}

// Three different things leave a graph out, and reading one as another states
// something false about the server: a graph dropped by the cap reported as one
// the ring does not hold denies the existence of a report that is right there,
// and it is the one an auditor would then stop looking for.
func TestDeadlockWriterTellsTheThreeAbsencesApart(t *testing.T) {
	root, rel, _, warnings := runDeadlockWriter(t, deadlockSets(), maxRunBytes)
	idx := readDeadlockIndex(t, root, rel)

	reasons := map[int64]string{}
	for _, o := range idx.Omissions {
		reasons[o.Rank] = o.Reason
	}
	if len(idx.Omissions) != 3 {
		t.Fatalf("omissions = %+v, want exactly 3", idx.Omissions)
	}
	if got := reasons[3]; !strings.Contains(got, "byte cap") {
		t.Errorf("the oversized graph: reason %q does not name the byte cap", got)
	}
	if got := reasons[4]; !strings.Contains(got, "holds no graph") {
		t.Errorf("the report-less event: reason %q does not say the ring holds no graph", got)
	}
	if got := reasons[maxDeadlockGraphs+1]; !strings.Contains(got, "cap of") {
		t.Errorf("the capped graph: reason %q does not name the count cap", got)
	}
	if strings.Contains(reasons[maxDeadlockGraphs+1], "holds no graph") {
		t.Error("a graph dropped by the cap was reported as one the ring does not hold")
	}
	if strings.Contains(reasons[3], "holds no graph") {
		t.Error("an oversized graph was reported as one the ring does not hold")
	}
	if len(warnings) != 3 {
		t.Errorf("got %d warnings, want one per omission", len(warnings))
	}
}

// The window travels with the counts. "412 deadlocks" says nothing until a
// reader knows the ring reaches back two days rather than twenty minutes, and
// the ring is overwritten rather than archived.
func TestDeadlockIndexCarriesTheRingWindow(t *testing.T) {
	root, rel, _, _ := runDeadlockWriter(t, deadlockSets(), maxRunBytes)
	idx := readDeadlockIndex(t, root, rel)
	if idx.Counts.InRing != 412 {
		t.Errorf("in_ring = %d, want the ring's own count 412", idx.Counts.InRing)
	}
	if idx.Counts.Written != 2 {
		t.Errorf("written = %d, want the 2 files on disk", idx.Counts.Written)
	}
	if idx.Counts.Earliest == "" || idx.Counts.Latest == "" {
		t.Error("the index reports counts without the window they are counted over")
	}
	if len(idx.Deadlocks) != 5 {
		t.Errorf("deadlocks = %d entries, want all 5 listed", len(idx.Deadlocks))
	}
}

func TestDeadlockWriterRecordsWhatTheBudgetRefused(t *testing.T) {
	root, rel, res, _ := runDeadlockWriter(t, deadlockSets(), 20)
	idx := readDeadlockIndex(t, root, rel)
	if res.GraphFiles != 0 {
		t.Errorf("GraphFiles = %d with a 20 byte budget, want 0", res.GraphFiles)
	}
	var budgeted int
	for _, o := range idx.Omissions {
		if strings.Contains(o.Reason, "cap on what the whole collection may write") {
			budgeted++
		}
	}
	if budgeted != 2 {
		t.Errorf("%d budget omissions, want the 2 graphs that could not be written", budgeted)
	}
}

// A result set without graph.rank — an older export replayed through this
// writer — must not have a missing rank read as 0 and every graph reported as
// beyond the cap.
func TestDeadlockWriterDoesNotInventARankItWasNotGiven(t *testing.T) {
	sets := deadlockSets()
	d, _ := setByName(sets, "deadlocks")
	cols := []string{}
	for _, c := range d.Columns {
		if c != "graph.rank" {
			cols = append(cols, c)
		}
	}
	stripped := ResultSet{Columns: cols, Types: d.Types[1:]}
	for _, row := range d.Rows {
		stripped.Rows = append(stripped.Rows, row[1:])
	}
	sets[1] = NamedResultSet{Spec: ResultSpec{Name: "deadlocks", Shape: ShapeArray}, Set: stripped}

	root, rel, res, _ := runDeadlockWriter(t, sets, maxRunBytes)
	if res.GraphFiles != 2 {
		t.Errorf("GraphFiles = %d, want 2: a missing rank must not be read as beyond the cap", res.GraphFiles)
	}
	idx := readDeadlockIndex(t, root, rel)
	for _, o := range idx.Omissions {
		if strings.Contains(o.Reason, "beyond the cap") {
			t.Errorf("a result set without graph.rank produced a cap omission: %q", o.Reason)
		}
	}
}

// Both caps are written twice — a Go constant and a literal in 061 — and only
// one of the two decides anything.
func TestDeadlockCapsAreTheSameNumbersInTheCorpus(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "queries", "10.system", "061.deadlock-graphs.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	for _, c := range []struct {
		what    string
		pattern string
		want    int
	}{
		{"the graph count cap", `ORDER BY d\.event_time DESC\) <= (\d+)`, maxDeadlockGraphs},
		{"the per-graph byte cap", `DATALENGTH\(d\.graph\) <= (\d+)`, maxDeadlockBytes},
	} {
		m := regexp.MustCompile(c.pattern).FindAllStringSubmatch(sql, -1)
		if len(m) != 1 {
			t.Errorf("061 has %d guards for %s, want exactly 1", len(m), c.what)
			continue
		}
		got, err := strconv.Atoi(m[0][1])
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("061 applies %d for %s, Go applies %d — the two are one rule and have drifted",
				got, c.what, c.want)
		}
	}
}
