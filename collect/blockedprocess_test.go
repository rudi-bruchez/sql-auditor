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

// bprSets builds what 063 returns when a capture exists and has reports in it.
// The overrides in the tests below turn it into each of the four ways the
// directory ends up empty.
func bprSets(threshold int64, session string, rows [][]any) []NamedResultSet {
	root := ResultSet{
		Columns: []string{"source.session", "source.path", "source.readable",
			"source.error_number", "source.error_message",
			"blocked_process.threshold_seconds", "capture.reports_in_files",
			"capture.earliest", "capture.latest", "caps.reports", "caps.report_bytes"},
		Types: []string{"NVARCHAR", "NVARCHAR", "BIT", "INT", "NVARCHAR", "INT", "INT",
			"NVARCHAR", "NVARCHAR", "INT", "INT"},
		Rows: [][]any{{session, `D:\MSSQL\Log\blocked_processes*.xel`, true, nil, nil,
			threshold, int64(len(rows)), "2026-08-13T17:07:25.963", "2026-08-13T17:07:55.977",
			int64(maxBlockedProcessReports), int64(maxBlockedProcessBytes)}},
	}
	reports := ResultSet{
		Columns: []string{"report.rank", "report.count", "occurred_at", "file_name", "report", "report_bytes"},
		Types:   []string{"BIGINT", "BIGINT", "NVARCHAR", "NVARCHAR", "NVARCHAR", "BIGINT"},
		Rows:    rows,
	}
	return []NamedResultSet{
		{Spec: ResultSpec{Name: "root", Shape: ShapeObject}, Set: root},
		{Spec: ResultSpec{Name: "reports", Shape: ShapeArray}, Set: reports},
	}
}

func bprRows() [][]any {
	body := `<blocked-process-report><blocked-process/></blocked-process-report>`
	return [][]any{
		{int64(1), int64(4), "2026-08-13T17:07:55.977", `D:\MSSQL\Log\bp_0_1.xel`, body, int64(len(body))},
		{int64(2), int64(4), "2026-08-13T17:07:45.972", `D:\MSSQL\Log\bp_0_1.xel`, body, int64(len(body))},
		// Above the byte cap: the SQL nulled it, the size still arrives.
		{int64(3), int64(4), "2026-08-13T17:07:35.967", `D:\MSSQL\Log\bp_0_1.xel`, nil, int64(maxBlockedProcessBytes + 1)},
		// Past the count cap.
		{int64(maxBlockedProcessReports + 1), int64(4), "2026-08-13T17:07:25.963", `D:\MSSQL\Log\bp_0_1.xel`, nil, int64(400)},
	}
}

func runBPRWriter(t *testing.T, sets []NamedResultSet, budget int) (root, rel string, res WriteResult, warnings []string) {
	t.Helper()
	root = t.TempDir()
	req := WriteRequest{
		Out:    newRunWriter(root, budget),
		Script: Script{Path: "10.system/063.blocked-process-reports.sql", Dir: "10.system", Base: "063.blocked-process-reports"},
		Unit:   DatabaseFolder{},
		Sets:   sets,
		State:  NewQueryStoreState(),
		Warn:   func(s string) { warnings = append(warnings, s) },
	}
	w := writerFor("blocked-process-reports")
	if w == nil {
		t.Fatal("writerFor(blocked-process-reports) = nil")
	}
	res, err := w(req)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	return root, res.Rel, res, warnings
}

type bprIndexFile struct {
	Source struct {
		Session          string `json:"session"`
		Path             string `json:"path"`
		ThresholdSeconds int    `json:"threshold_seconds"`
		ErrorNumber      int    `json:"error_number"`
	} `json:"source"`
	Counts struct {
		InFiles int `json:"in_files"`
		Written int `json:"written"`
	} `json:"counts"`
	Reports []struct {
		Rank     int64  `json:"rank"`
		FromFile string `json:"from_file"`
		File     string `json:"file"`
	} `json:"reports"`
	Notes     []string `json:"notes"`
	Omissions []struct {
		Rank   int64  `json:"rank"`
		Reason string `json:"reason"`
	} `json:"omissions"`
}

func readBPRIndex(t *testing.T, root, rel string) bprIndexFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), "_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx bprIndexFile
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestBPRWriterWritesOneFilePerReport(t *testing.T) {
	root, rel, res, _ := runBPRWriter(t, bprSets(10, "blocked_processes", bprRows()), maxRunBytes)
	if rel != "10.system/063.blocked-process-reports" {
		t.Fatalf("rel = %q", rel)
	}
	dir := filepath.Join(root, filepath.FromSlash(rel))
	for _, n := range []string{"_index.json", "blocked_process_0001.xml", "blocked_process_0002.xml"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("missing %s: %v", n, err)
		}
	}
	if res.ReportFiles != 2 {
		t.Errorf("ReportFiles = %d, want 2", res.ReportFiles)
	}
	// The four other counters latch four other disclosures. A run that exported
	// blocking must not have MANIFEST.txt announce deadlock graphs, module
	// source, plans or session text.
	if res.GraphFiles != 0 || res.DefinitionFiles != 0 || res.PlanFiles != 0 || res.TextFiles != 0 {
		t.Errorf("Graph=%d Definition=%d Plan=%d Text=%d, want all 0",
			res.GraphFiles, res.DefinitionFiles, res.PlanFiles, res.TextFiles)
	}
}

// Four different things produce an empty directory, and an archive that cannot
// tell them apart lets a reader conclude "no blocking occurred" — which nobody
// measured. This is the test that matters most in this file.
func TestBPRWriterExplainsEveryWayOfBeingEmpty(t *testing.T) {
	for _, c := range []struct {
		name string
		sets []NamedResultSet
		want string
	}{
		{"nothing captures it", bprSets(10, "", nil), "no Extended Events session"},
		{"capture exists and is empty", bprSets(10, "blocked_processes", nil), "hold no blocked process report"},
		{"threshold is zero", bprSets(0, "blocked_processes", nil), "never fires"},
	} {
		root, rel, _, warnings := runBPRWriter(t, c.sets, maxRunBytes)
		idx := readBPRIndex(t, root, rel)
		got := strings.Join(idx.Notes, "\n")
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: notes %q do not say %q", c.name, got, c.want)
		}
		// Every note also reaches the manifest: the operator asked for these on
		// the command line and is owed the explanation at run time, not on the
		// day they open the archive.
		if len(warnings) == 0 {
			t.Errorf("%s: nothing was said to the manifest", c.name)
		}
	}
}

// The threshold note compounds rather than replaces. A session can exist, be
// readable, and still never have received an event.
func TestBPRWriterReportsThresholdAlongsideTheOtherReason(t *testing.T) {
	root, rel, _, _ := runBPRWriter(t, bprSets(0, "", nil), maxRunBytes)
	idx := readBPRIndex(t, root, rel)
	got := strings.Join(idx.Notes, "\n")
	if !strings.Contains(got, "no Extended Events session") || !strings.Contains(got, "never fires") {
		t.Errorf("only one of the two reasons was recorded:\n%s", got)
	}
}

// A read that raised is not a read that found nothing. The file is opened by the
// SQL Server service account, so a path that exists is not necessarily readable.
func TestBPRWriterKeepsAFileErrorApartFromAnEmptyCapture(t *testing.T) {
	sets := bprSets(10, "blocked_processes", nil)
	rootSet, _ := setByName(sets, "root")
	rootSet.Rows[0][3] = int64(25718)
	rootSet.Rows[0][4] = "The log file name is invalid."
	sets[0] = NamedResultSet{Spec: ResultSpec{Name: "root", Shape: ShapeObject}, Set: rootSet}

	root, rel, _, _ := runBPRWriter(t, sets, maxRunBytes)
	idx := readBPRIndex(t, root, rel)
	got := strings.Join(idx.Notes, "\n")
	if !strings.Contains(got, "25718") {
		t.Errorf("the SQL error was not reported: %q", got)
	}
	if strings.Contains(got, "hold no blocked process report") {
		t.Error("a failed read was reported as a capture that found nothing")
	}
	if idx.Source.ErrorNumber != 25718 {
		t.Errorf("source.error_number = %d, want 25718", idx.Source.ErrorNumber)
	}
}

func TestBPRWriterTellsTheTwoCapsApart(t *testing.T) {
	root, rel, _, _ := runBPRWriter(t, bprSets(10, "blocked_processes", bprRows()), maxRunBytes)
	idx := readBPRIndex(t, root, rel)
	reasons := map[int64]string{}
	for _, o := range idx.Omissions {
		reasons[o.Rank] = o.Reason
	}
	if !strings.Contains(reasons[3], "byte cap") {
		t.Errorf("the oversized report: %q", reasons[3])
	}
	if !strings.Contains(reasons[maxBlockedProcessReports+1], "cap of") {
		t.Errorf("the capped report: %q", reasons[maxBlockedProcessReports+1])
	}
	if strings.Contains(reasons[maxBlockedProcessReports+1], "holds no report body") {
		t.Error("a report dropped by the cap was reported as one the capture does not hold")
	}
	// Every report is listed whether or not its body was written, with the .xel
	// it came from — that is what says how far back the capture reaches.
	if len(idx.Reports) != 4 {
		t.Errorf("reports = %d entries, want all 4 listed", len(idx.Reports))
	}
	for _, r := range idx.Reports {
		if r.FromFile == "" {
			t.Errorf("report %d does not name the .xel it was read from", r.Rank)
		}
	}
}

func TestBlockedProcessCapsAreTheSameNumbersInTheCorpus(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "queries", "10.system", "063.blocked-process-reports.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	for _, c := range []struct {
		what    string
		pattern string
		want    int
	}{
		{"the report count cap", `ORDER BY r\.event_time DESC\) <= (\d+)`, maxBlockedProcessReports},
		{"the per-report byte cap", `DATALENGTH\(r\.report\) <= (\d+)`, maxBlockedProcessBytes},
	} {
		m := regexp.MustCompile(c.pattern).FindAllStringSubmatch(sql, -1)
		if len(m) != 1 {
			t.Errorf("063 has %d guards for %s, want exactly 1", len(m), c.what)
			continue
		}
		got, err := strconv.Atoi(m[0][1])
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("063 applies %d for %s, Go applies %d — the two are one rule and have drifted",
				got, c.what, c.want)
		}
	}
}
