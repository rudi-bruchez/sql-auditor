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
		Columns: []string{"report.rank", "report.count", "report.sample_rank", "occurred_at", "file_name",
			"monitor_loop", "blocked.spid", "blocked.owner_id", "blocked.wait_ms", "blocked.trancount", "blocked.lock_mode",
			"blocked.wait_resource", "blocking.spid", "blocking.status", "blocking.trancount",
			"report", "report_bytes"},
		Types: []string{"BIGINT", "BIGINT", "BIGINT", "NVARCHAR", "NVARCHAR",
			"BIGINT", "INT", "BIGINT", "BIGINT", "INT", "NVARCHAR", "NVARCHAR", "INT", "NVARCHAR", "INT",
			"NVARCHAR", "BIGINT"},
		Rows: rows,
	}
	return []NamedResultSet{
		{Spec: ResultSpec{Name: "root", Shape: ShapeObject}, Set: root},
		{Spec: ResultSpec{Name: "reports", Shape: ShapeArray}, Set: reports},
	}
}

// bprRow is one row of 063's reports set. sample is nil for a report that is
// not its episode's longest, which is how 063 marks every such row.
func bprRow(rank int64, sample any, at string, blocked, owner, wait, blocking int64,
	resource string, body any, size int64) []any {
	return []any{rank, int64(0), sample, at, `D:\MSSQL\Log\bp_0_1.xel`, nil,
		blocked, owner, wait, int64(0), "S", resource, blocking, "suspended", int64(1),
		body, size}
}

func bprRows() [][]any {
	body := `<blocked-process-report><blocked-process/></blocked-process-report>`
	return [][]any{
		bprRow(1, int64(1), "2026-08-13T17:07:55.977", 87, 1001, 30000, 68, "KEY: 7:1 (a)", body, int64(len(body))),
		bprRow(2, int64(2), "2026-08-13T17:07:45.972", 88, 1002, 20000, 87, "KEY: 7:1 (a)", body, int64(len(body))),
		// Above the byte cap: the SQL nulled it, the size still arrives.
		bprRow(3, int64(3), "2026-08-13T17:07:35.967", 90, 1003, 10000, 68, "OBJECT: 7:2", nil, int64(maxBlockedProcessBytes+1)),
		// The longest report of an episode past the episode cap.
		bprRow(4, int64(maxBlockedProcessReports+1), "2026-08-13T17:07:25.963", 91, 1004, 5000, 68, "OBJECT: 7:3", nil, int64(400)),
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
	if !strings.Contains(reasons[4], "episodes kept whole") {
		t.Errorf("the capped report: %q", reasons[4])
	}
	if strings.Contains(reasons[4], "holds no report body") {
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
		{"the report count cap", `s\.sample_rank <= (\d+)`, maxBlockedProcessReports},
		{"the per-report byte cap", `DATALENGTH\(s\.report\) <= (\d+)`, maxBlockedProcessBytes},
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

// The stem is the configured filename without its .xel extension, and the test
// for that extension has to look at the end of the name. Testing REVERSE(name)
// against '.xel' never matches, because the reversed string holds 'lex.'. The
// stem then kept its extension, the path came out as 'Blocked process.xel*.xel'
// where SQL Server writes 'Blocked process_0_133000000000000000.xel', and the
// collection reported an empty capture on an instance that had two reports.
func TestBlockedProcessExtensionIsTestedOnTheEndOfTheName(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "queries", "10.system", "063.blocked-process-reports.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	if regexp.MustCompile(`CHARINDEX\(\s*N?'\.xel'\s*,\s*REVERSE\(`).MatchString(sql) {
		t.Error("063 tests the .xel extension against a reversed string, which never matches; " +
			"use RIGHT(@configured, 4) = N'.xel'")
	}
	// Anchored on RIGHT(@configured, 4) and not on the whole comparison. The
	// first version of this test pinned the exact expression, and the very next
	// correct change — folding the case so a case sensitive collation cannot
	// reject '.XEL' — broke it. A guard that fails on a fix is a guard people
	// learn to delete.
	if !regexp.MustCompile(`RIGHT\(@configured,\s*4\)`).MatchString(sql) {
		t.Error("063 no longer tests the .xel extension on the end of the configured name")
	}
}

// The directory of an .xel is cut at its last separator, and on Linux that
// separator is a slash. Looking for a backslash only kept the whole file name as
// the directory: 063 read an empty capture on an instance holding four reports,
// and 061 read no deadlock from system_health's files, keeping only what the
// ring buffer still had. Measured on SQL Server 2025 CU7 on Linux, September
// 2026.
func TestXelPathsAreCutOnEitherSeparator(t *testing.T) {
	for _, f := range []string{"061.deadlock-graphs.sql", "063.blocked-process-reports.sql"} {
		b, err := os.ReadFile(filepath.Join("..", "queries", "10.system", f))
		if err != nil {
			t.Fatal(err)
		}
		sql := string(b)
		if regexp.MustCompile(`CHARINDEX\(\s*'\\'\s*,\s*REVERSE\(`).MatchString(sql) {
			t.Errorf("%s cuts a path on a backslash only, which misses every Linux path", f)
		}
		if !strings.Contains(sql, `PATINDEX('%[\/]%', REVERSE(@current))`) {
			t.Errorf("%s no longer cuts @current on either separator", f)
		}
	}
}

// The population is every report, not the ones written. Three reports of one
// block and one of another make two episodes, the longer first, with the report
// count and the extremes of each — and a report that is not its episode's
// longest is neither written nor called an omission, since nothing of it is
// lost.
func TestBPRWriterGroupsEveryReportIntoEpisodes(t *testing.T) {
	body := `<blocked-process-report><blocked-process/></blocked-process-report>`
	// The shorter episode's row comes first, so the order of the episodes can
	// only come from their wait and not from the order the rows arrive in.
	rows := [][]any{
		bprRow(4, int64(2), "2026-08-13T17:07:50.000", 88, 2002, 6000, 87, "KEY: 7:1 (a)", body, int64(len(body))),
		bprRow(1, int64(1), "2026-08-13T17:08:05.000", 87, 1001, 15000, 68, "KEY: 7:1 (a)", body, int64(len(body))),
		bprRow(2, nil, "2026-08-13T17:08:00.000", 87, 1001, 10000, 68, "KEY: 7:1 (a)", nil, int64(len(body))),
		bprRow(3, nil, "2026-08-13T17:07:55.000", 87, 1001, 5000, 68, "KEY: 7:1 (a)", nil, int64(len(body))),
	}
	root, rel, res, warnings := runBPRWriter(t, bprSets(5, "blocked_processes", rows), maxRunBytes)
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), "_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Counts   struct{ Episodes, Written int } `json:"counts"`
		Episodes []struct {
			Episode      int    `json:"episode"`
			BlockedSpid  int64  `json:"blocked_spid"`
			BlockingSpid int64  `json:"blocking_spid"`
			Reports      int    `json:"reports"`
			FirstSeen    string `json:"first_seen"`
			LastSeen     string `json:"last_seen"`
			MaxWaitMs    int64  `json:"max_wait_ms"`
			File         string `json:"file"`
		} `json:"episodes"`
		Reports []struct {
			Rank    int64 `json:"rank"`
			Episode int   `json:"episode"`
		} `json:"reports"`
		Omissions []struct{ Rank int64 } `json:"omissions"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	if idx.Counts.Episodes != 2 || len(idx.Episodes) != 2 {
		t.Fatalf("episodes = %d (%d listed), want 2", idx.Counts.Episodes, len(idx.Episodes))
	}
	first := idx.Episodes[0]
	if first.BlockedSpid != 87 || first.BlockingSpid != 68 || first.Reports != 3 || first.MaxWaitMs != 15000 ||
		first.FirstSeen != "2026-08-13T17:07:55.000" || first.LastSeen != "2026-08-13T17:08:05.000" {
		t.Errorf("first episode = %+v", first)
	}
	if first.File != "blocked_process_0001.xml" || idx.Episodes[1].File != "blocked_process_0004.xml" {
		t.Errorf("episode files = %q, %q", first.File, idx.Episodes[1].File)
	}
	for _, r := range idx.Reports {
		want := 1
		if r.Rank == 4 {
			want = 2
		}
		if r.Episode != want {
			t.Errorf("report %d is in episode %d, want %d", r.Rank, r.Episode, want)
		}
	}
	if res.ReportFiles != 2 || idx.Counts.Written != 2 {
		t.Errorf("written = %d files, %d counted, want 2", res.ReportFiles, idx.Counts.Written)
	}
	if len(idx.Omissions) != 0 || len(warnings) != 0 {
		t.Errorf("a report whose episode was kept is not an omission: %v, %v", idx.Omissions, warnings)
	}
}

// Past the episode cap, the omissions still name each report, and the manifest
// hears about it once. A warning per report put 1 135 identical lines in a
// client's manifest in September 2026.
func TestBPRWriterWarnsOnceForEpisodesPastTheCap(t *testing.T) {
	var rows [][]any
	for i := 1; i <= 3; i++ {
		rows = append(rows, bprRow(int64(i), int64(maxBlockedProcessReports+i), "2026-08-13T17:07:50.000",
			int64(100+i), int64(i), 5000, 68, "KEY: 7:1 (a)", nil, 400))
	}
	_, _, _, warnings := runBPRWriter(t, bprSets(5, "blocked_processes", rows), maxRunBytes)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "3 episode(s) past the cap") {
		t.Errorf("warnings = %q, want one line naming 3 episodes", warnings)
	}
}

// A stopped session has no running target, and its configured file name is the
// only path there is. Stripping its directory first sent the read to the LOG
// directory, so a session configured into another folder read as empty.
// Reproduced on SQL Server 2025 CU7 with a stopped session writing to the data
// directory, September 2026.
func TestStoppedSessionKeepsItsConfiguredDirectory(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "queries", "10.system", "063.blocked-process-reports.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`WHEN @current IS NULL THEN @configured_stem \+ N'\*\.xel'`).MatchString(string(b)) {
		t.Error("063 no longer reads a stopped session from its configured path, directory included")
	}
}

// withLoop sets the monitor_loop column of a bprRow, which bprRow leaves NULL
// as a report without the attribute would.
func withLoop(row []any, loop int64) []any {
	row[5] = loop
	return row
}

// The pass of the deadlock monitor is what says two reports were seen at the
// same moment, which is how a blocker is told to be a link of a chain. Only one
// report per episode is kept whole, so the index is the only place every
// report's pass survives, and the episode carries its span.
func TestBPRWriterKeepsTheMonitorLoopOfEveryReport(t *testing.T) {
	body := `<blocked-process-report monitorLoop="41212"><blocked-process/></blocked-process-report>`
	rows := [][]any{
		withLoop(bprRow(1, int64(1), "2026-08-13T17:08:05.000", 87, 1001, 15000, 68, "KEY: 7:1 (a)", body, int64(len(body))), 41212),
		withLoop(bprRow(2, nil, "2026-08-13T17:08:00.000", 87, 1001, 10000, 68, "KEY: 7:1 (a)", nil, int64(len(body))), 41211),
		withLoop(bprRow(3, nil, "2026-08-13T17:07:55.000", 87, 1001, 5000, 68, "KEY: 7:1 (a)", nil, int64(len(body))), 41210),
		// Blocked in pass 41211 while it blocks 87: the fact a chain is read from.
		withLoop(bprRow(4, int64(2), "2026-08-13T17:08:00.000", 68, 2002, 6000, 55, "KEY: 7:1 (b)", body, int64(len(body))), 41211),
		// A report without the attribute stays without it, rather than reading 0.
		bprRow(5, int64(3), "2026-08-13T17:08:10.000", 90, 3003, 5000, 55, "OBJECT: 7:9", body, int64(len(body))),
	}
	root, rel, _, _ := runBPRWriter(t, bprSets(5, "blocked_processes", rows), maxRunBytes)
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), "_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Episodes []struct {
			BlockedSpid int64  `json:"blocked_spid"`
			First       *int64 `json:"first_monitor_loop"`
			Last        *int64 `json:"last_monitor_loop"`
		} `json:"episodes"`
		Reports []struct {
			Rank        int64  `json:"rank"`
			MonitorLoop *int64 `json:"monitor_loop"`
		} `json:"reports"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	want := map[int64]int64{1: 41212, 2: 41211, 3: 41210, 4: 41211}
	for _, r := range idx.Reports {
		w, has := want[r.Rank]
		switch {
		case !has && r.MonitorLoop != nil:
			t.Errorf("report %d has monitor_loop %d, want null", r.Rank, *r.MonitorLoop)
		case has && (r.MonitorLoop == nil || *r.MonitorLoop != w):
			t.Errorf("report %d monitor_loop = %v, want %d", r.Rank, r.MonitorLoop, w)
		}
	}
	if !strings.Contains(string(b), `"monitor_loop": null`) {
		t.Error("a report without the attribute must say null, not omit the key")
	}
	spans := map[int64][2]int64{87: {41210, 41212}, 68: {41211, 41211}}
	for _, e := range idx.Episodes {
		span, has := spans[e.BlockedSpid]
		if !has {
			if e.First != nil || e.Last != nil {
				t.Errorf("episode of %d has a span without any loop: %v, %v", e.BlockedSpid, e.First, e.Last)
			}
			continue
		}
		if e.First == nil || e.Last == nil || *e.First != span[0] || *e.Last != span[1] {
			t.Errorf("episode of %d spans %v..%v, want %d..%d", e.BlockedSpid, e.First, e.Last, span[0], span[1])
		}
	}
}

// 063 reads monitorLoop off the node it already binds to the report, so the
// path is relative to blocked-process-report. Written from the root, or with
// the element name repeated, it returns NULL on every row without an error:
// measured on SQL Server 2025 against a literal report, October 2026.
func TestBlockedProcessMonitorLoopIsReadOffTheBoundReport(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "queries", "10.system", "063.blocked-process-reports.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`d\.value\('\(@monitorLoop\)\[1\]',\s*'bigint'\)`).MatchString(string(b)) {
		t.Error("063 no longer reads monitorLoop off the bound blocked-process-report node")
	}
	if !regexp.MustCompile(`AS \[monitor_loop\]`).MatchString(string(b)) {
		t.Error("063 no longer projects monitor_loop, which the writer reads by that name")
	}
}
