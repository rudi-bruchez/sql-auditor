package collect

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	sqlauditor "github.com/rudi-bruchez/sql-auditor"
)

func TestExportQueriesWritesTree(t *testing.T) {
	corpus := fstest.MapFS{
		"queries/10.system/010.properties.sql": {Data: []byte("SELECT 1;")},
	}
	dest := t.TempDir()
	if err := ExportQueries(corpus, "queries", dest); err != nil {
		t.Fatalf("ExportQueries: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dest, "10.system", "010.properties.sql"))
	if err != nil {
		t.Fatalf("exported file missing: %v", err)
	}
	if string(b) != "SELECT 1;" {
		t.Errorf("content = %q", b)
	}
}

func TestPrepareRunFolderClearsStaleFiles(t *testing.T) {
	// A same-day rerun must not leave results from the previous run behind:
	// the folder would mix two runs while _run.json described only the last.
	dir := t.TempDir()
	run := filepath.Join(dir, "SRV01-2026-08-08")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(run, "stale.json")
	if err := os.WriteFile(stale, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareRunFolder(run, false, os.Stderr); err != nil {
		t.Fatalf("prepareRunFolder: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale file survived; the run folder was not cleared")
	}
}

// The warning has to name the archive too. The zip sits beside the folder, not
// inside it, so an operator told only about the folder can still lose the
// previous run's archive without having been warned.
func TestPrepareRunFolderRemovesTheStaleArchive(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "SRV01-2026-08-08")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	zip := run + ".zip"
	if err := os.WriteFile(zip, []byte("PK"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareRunFolder(run, false, os.Stderr); err != nil {
		t.Fatalf("prepareRunFolder: %v", err)
	}
	if _, err := os.Stat(zip); !os.IsNotExist(err) {
		t.Error("stale archive survived a replacing rerun; it no longer matches the folder beside it")
	}
}

// --keep must never write into a folder an earlier run already used. Merging
// them leaves one run's manifest beside another run's result files: the
// document then describes an archive it does not match, and a run without
// --include-session-text can ship session text under a manifest denying it.
func TestPrepareRunFolderRefusesAnOccupiedKeepTarget(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "SRV01-2026-08-08-1200")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	keeper := filepath.Join(run, "earlier-run.json")
	if err := os.WriteFile(keeper, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareRunFolder(run, true, os.Stderr); err == nil {
		t.Error("--keep silently merged this run into an existing folder")
	}
	if _, err := os.Stat(keeper); err != nil {
		t.Errorf("--keep destroyed the earlier run it was supposed to preserve: %v", err)
	}

	// The archive alone is enough to make the name taken: it sits beside the
	// folder, so a folder moved away while its .zip stayed still leaves a name
	// a second run would appear to have produced.
	fresh := filepath.Join(dir, "SRV01-2026-08-08-1300")
	if err := os.WriteFile(fresh+".zip", []byte("PK"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareRunFolder(fresh, true, os.Stderr); err == nil {
		t.Error("--keep accepted a name whose archive already exists")
	}
}

func TestPrepareRunFolderCreatesAFreeKeepTarget(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "SRV01-2026-08-08-1200")
	if err := prepareRunFolder(run, true, os.Stderr); err != nil {
		t.Fatalf("prepareRunFolder: %v", err)
	}
	if fi, err := os.Stat(run); err != nil || !fi.IsDir() {
		t.Errorf("the run folder was not created: %v", err)
	}
}

// The warning is the whole protection against a silent replacement, so assert
// its text rather than only the deletion it precedes. It has to name the
// archive too: the .zip is what gets mailed onward and it does not live inside
// the folder, so an operator told only about the folder has no reason to
// expect it gone.
func TestReplacingRunWarningNamesTheFolderAndTheArchive(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "SRV01-2026-08-08")

	if w := replacingRunWarning(run); w != "" {
		t.Errorf("nothing exists yet, so nothing is being replaced; got %q", w)
	}
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	if w := replacingRunWarning(run); !strings.Contains(w, run) {
		t.Errorf("warning %q does not name the folder", w)
	}
	if err := os.WriteFile(run+".zip", []byte("PK"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := replacingRunWarning(run)
	if !strings.Contains(w, run) || !strings.Contains(w, run+".zip") {
		t.Errorf("warning %q does not name both the folder and the archive", w)
	}
	if !strings.Contains(w, "--keep") {
		t.Errorf("warning %q does not say how to avoid the replacement", w)
	}
}

func TestOutputWritableRejectsAReadOnlyDirectory(t *testing.T) {
	// os.MkdirAll returns nil for a directory that already exists whatever its
	// mode, so the check has to try to create a file.
	dir := t.TempDir()
	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	if !outputWritable(dir) {
		t.Error("a fresh temp dir was reported unwritable")
	}
	// Windows ignores the mode bits on directories, so the negative half of
	// this only means anything where they are honoured.
	if runtime.GOOS != "windows" && outputWritable(ro) {
		t.Error("a read-only directory was reported writable")
	}
	// Whichever platform, the probe must leave nothing behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "ro" {
			t.Errorf("the writability probe left %s behind", e.Name())
		}
	}
}

// A portable negative: a path occupied by a regular file cannot be an output
// directory on any platform.
func TestOutputWritableRejectsAPathThatIsAFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if outputWritable(file) {
		t.Error("a regular file was accepted as a writable output directory")
	}
}

// Where the probe is written is the whole test. A probe created in
// os.TempDir() reports on the temp directory, succeeds on any normal machine,
// and would pass every other assertion in this file while saying nothing about
// the output directory the run is actually about to use.
func TestWriteProbeIsCreatedInsideTheDirectoryUnderTest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "output")
	path, err := writeProbe(dir)
	if err != nil {
		t.Fatalf("writeProbe: %v", err)
	}
	if got := filepath.Dir(path); got != dir {
		t.Errorf("the probe was written to %s, not into %s — it tested the wrong filesystem", got, dir)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the probe file was left behind")
	}
}

// SQL_CONNECT_TIMEOUT_SEC bounds dialling and nothing after it. Every server
// call the pipeline makes outside runUnit's own query goes through this, so a
// USE blocked behind a single-user database, or a ping down a half-open
// socket, ends rather than hanging the run with no exit but Ctrl-C.
func TestDeadlineBoundsAServerCall(t *testing.T) {
	cfg := &Config{QueryTimeout: 250 * time.Millisecond}
	ctx, cancel := deadline(context.Background(), cfg)
	defer cancel()
	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("no deadline was set; a blocked statement would hang the run forever")
	}
	if remaining := time.Until(dl); remaining <= 0 || remaining > cfg.QueryTimeout {
		t.Errorf("deadline is %v away, want (0, %v]", remaining, cfg.QueryTimeout)
	}
	<-ctx.Done()
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Errorf("ctx.Err() = %v, want DeadlineExceeded", ctx.Err())
	}
}

func TestSkipReasonForRequiredFlag(t *testing.T) {
	s := Script{Path: "10.system/052.session-text.sql", RequiresFlag: FlagIncludeSessionText}
	reason, skip := skipReason(s, nil, nil, nil)
	if !skip {
		t.Fatal("a script gated on an unset flag must be skipped")
	}
	if !strings.Contains(reason, "--include-session-text") {
		t.Errorf("reason %q does not say which flag would enable it", reason)
	}
	if _, skip := skipReason(s, nil, nil, map[string]bool{FlagIncludeSessionText: true}); skip {
		t.Error("the flag was set; the script must run")
	}
}

func TestSkipReasonForDeniedPermission(t *testing.T) {
	s := Script{Path: "10.system/050.tempdb.sql", Permissions: []string{"view_server_state"}}
	reason, skip := skipReason(s, map[string]bool{"view_server_state": true}, nil, nil)
	if !skip {
		t.Fatal("a script declaring a denied permission must be skipped")
	}
	// The label, not the capability key. This reason is printed into
	// MANIFEST.txt, whose reader has never seen "view_server_state" and has no
	// way to look it up; the same rule the Coverage block follows.
	if !strings.Contains(reason, "VIEW SERVER STATE") {
		t.Errorf("reason %q does not name the permission", reason)
	}
	if strings.Contains(reason, "view_server_state") {
		t.Errorf("reason %q leaks the internal capability key", reason)
	}
	// "connect" is handled before any script is considered: an unreachable
	// instance abandons the run rather than skipping the scripts that declare
	// CONNECT while the rest carry on describing a server never reached.
	c := Script{Path: "10.system/010.properties.sql", Permissions: []string{"connect"}}
	if _, skip := skipReason(c, map[string]bool{}, nil, nil); skip {
		t.Error("connect was not in the denied set; the script must run")
	}
}

func TestSkipReasonForVersionGate(t *testing.T) {
	s := Script{Path: "10.system/012.soft-numa.sql", MinVersion: []int{13}}
	reason, skip := skipReason(s, nil, []int{11, 0, 7001}, nil)
	if !skip {
		t.Fatal("a script gated above the instance's version must be skipped")
	}
	if !strings.Contains(reason, "13") {
		t.Errorf("reason %q does not name the required version", reason)
	}
	if _, skip := skipReason(s, nil, []int{13, 0, 5026}, nil); skip {
		t.Error("the instance is new enough; the script must run")
	}
	// An unparseable ProductVersion is not evidence that the server is old.
	// Skipping on it would silently drop every gated collector.
	if _, skip := skipReason(s, nil, nil, nil); skip {
		t.Error("an unknown server version must not gate a script out")
	}
}

const sessionTextDisclosure = "SQL text of statements running during collection"

// The disclosure paragraph and the decision to run the session-text collector
// have to come from one place. This pins them together: the plan that runs the
// script is the plan that sets the manifest field.
func TestSessionTextFlagDrivesTheDisclosure(t *testing.T) {
	corpus := []Script{{
		Path:         "10.system/052.session-text.sql",
		RequiresFlag: FlagIncludeSessionText,
		SQL:          "SELECT est.text FROM sys.dm_exec_sql_text(@h) AS est;",
	}}

	off := &Manifest{}
	off.Collected.SessionText = len(collectsSessionText(planScripts(corpus, nil, nil, nil))) > 0
	if off.Collected.SessionText {
		t.Error("the flag was off; the manifest must not claim session text is present")
	}
	if strings.Contains(off.Human(), sessionTextDisclosure) {
		t.Error("MANIFEST.txt disclosed session text for a run that did not collect it")
	}

	on := &Manifest{}
	on.Collected.SessionText = len(collectsSessionText(
		planScripts(corpus, nil, nil, map[string]bool{FlagIncludeSessionText: true}))) > 0
	if !on.Collected.SessionText {
		t.Fatal("the flag was on; the manifest must disclose session text")
	}
	if !strings.Contains(on.Human(), sessionTextDisclosure) {
		t.Error("MANIFEST.txt understated what the archive contains")
	}
}

// --queries-dir accepts a corpus this project has never seen. One reading
// dm_exec_sql_text without the directive would otherwise put application
// literals in the archive under a manifest saying session_text: false — the
// gate defeated by the one path that bypasses the embedded corpus.
func TestSessionTextClaimFollowsTheSQLNotOnlyTheDirective(t *testing.T) {
	ungated := []Script{{
		Path: "10.system/099.local.sql",
		SQL: "SELECT es.login_name, est.text\n" +
			"FROM sys.dm_exec_requests r\n" +
			"JOIN sys.dm_exec_sessions es ON es.session_id = r.session_id\n" +
			"CROSS APPLY sys.dm_exec_sql_text(r.sql_handle) AS est;",
	}}
	by := collectsSessionText(planScripts(ungated, nil, nil, nil))
	if len(by) != 1 || by[0] != "10.system/099.local.sql" {
		t.Fatalf("an ungated collector reading dm_exec_sql_text was not detected: %v", by)
	}
	m := &Manifest{}
	m.Collected.SessionText = len(by) > 0
	if !strings.Contains(m.Human(), sessionTextDisclosure) {
		t.Error("MANIFEST.txt understated an archive that does contain session text")
	}
}

// The counterpart: prose mentioning the DMF must not trip the disclosure.
// 050.tempdb.sql explains in a comment why it no longer reads it, and a
// manifest that over-discloses on every run teaches its readers to ignore the
// paragraph.
func TestSessionTextClaimIgnoresComments(t *testing.T) {
	commented := []Script{{
		Path: "10.system/050.tempdb.sql",
		SQL: "-- The statement text once read here via sys.dm_exec_sql_text moved out.\n" +
			"/* sys.dm_exec_sql_text is deliberately not used below. */\n" +
			"SELECT session_id FROM sys.dm_tran_active_snapshot_database_transactions;",
	}}
	if by := collectsSessionText(planScripts(commented, nil, nil, nil)); len(by) > 0 {
		t.Errorf("a comment mentioning the DMF was read as a collector: %v", by)
	}
}

// A corpus with no session-text collector in it must not disclose session
// text however the flag is set: the claim follows what ran, not the option.
func TestSessionTextNotClaimedWhenNoScriptCollectsIt(t *testing.T) {
	plain := []Script{{Path: "10.system/010.properties.sql", SQL: "SELECT 1;"}}
	on := map[string]bool{FlagIncludeSessionText: true}
	if by := collectsSessionText(planScripts(plain, nil, nil, on)); len(by) > 0 {
		t.Errorf("no script collects session text; the flag alone must not set the claim: %v", by)
	}
}

// A session-text collector kept out by a denied permission or an old server is
// not in the archive either, so the manifest must not disclose it.
func TestSessionTextNotClaimedWhenTheScriptWasSkippedAnyway(t *testing.T) {
	gated := []Script{{
		Path:         "10.system/052.session-text.sql",
		RequiresFlag: FlagIncludeSessionText,
		Permissions:  []string{"view_server_state"},
		SQL:          "SELECT est.text FROM sys.dm_exec_sql_text(@h) AS est;",
	}}
	on := map[string]bool{FlagIncludeSessionText: true}
	plan := planScripts(gated, map[string]bool{"view_server_state": true}, nil, on)
	if by := collectsSessionText(plan); len(by) > 0 {
		t.Errorf("the collector was skipped for want of a permission; nothing was captured to disclose: %v", by)
	}
}

// The embedded corpus is the thing the default archive's wording is about:
// exactly one collector may gather session text, and only behind the flag.
func TestEmbeddedCorpusGatesSessionTextBehindTheFlag(t *testing.T) {
	scripts, err := Discover(os.DirFS(filepath.Join("..", "queries")), ".")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	gated := 0
	for _, s := range scripts {
		if s.RequiresFlag == FlagIncludeSessionText {
			gated++
		}
		// dm_exec_sql_text is the source of verbatim user SQL. Anything
		// reading it without the gate would make the default MANIFEST.txt
		// untrue, which is the defect this whole split exists to prevent.
		if readsSessionText(s) && s.RequiresFlag != FlagIncludeSessionText {
			t.Errorf("%s reads dm_exec_sql_text without @requires_flag: %s", s.Path, FlagIncludeSessionText)
		}
		// And the converse: the gated file must actually be the one reading
		// it, or the gate guards nothing.
		if s.RequiresFlag == FlagIncludeSessionText && !readsSessionText(s) {
			t.Errorf("%s carries the session-text gate but does not read dm_exec_sql_text", s.Path)
		}
	}
	if gated != 1 {
		t.Errorf("got %d session-text collectors, want exactly 1", gated)
	}
	if by := collectsSessionText(planScripts(scripts, nil, nil, nil)); len(by) > 0 {
		t.Errorf("the default run would collect session text: %v", by)
	}
	if len(collectsSessionText(planScripts(scripts, nil, nil, map[string]bool{FlagIncludeSessionText: true}))) == 0 {
		t.Error("--include-session-text would collect nothing")
	}
}

func TestRunFolderForSuffixesOnlyWhenKeeping(t *testing.T) {
	dir := t.TempDir()
	now, err := time.Parse(time.RFC3339, "2026-08-08T13:45:00Z")
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(dir, RunFolderName("SRV01", now))
	if got := runFolderFor(dir, "SRV01", now, false); got != base {
		t.Errorf("without --keep: got %q, want %q", got, base)
	}
	if got := runFolderFor(dir, "SRV01", now, true); got != base {
		t.Errorf("nothing exists yet, so --keep must still use the plain name: got %q", got)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := runFolderFor(dir, "SRV01", now, true); got != base+"-1345" {
		t.Errorf("--keep over an existing folder: got %q, want %q", got, base+"-1345")
	}
}

// Three --keep runs inside one minute share an HHMM. A version that suffixed
// once handed the same occupied name to the second and third, which then wrote
// their results into the first run's folder: each left its own manifest,
// describing only its own queries, beside the union of everybody's files. A
// run without --include-session-text shipped a zip full of session text under
// a MANIFEST.txt that denied it.
func TestRunFolderForNeverRepeatsWithinTheSameMinute(t *testing.T) {
	dir := t.TempDir()
	now, err := time.Parse(time.RFC3339, "2026-08-08T13:45:00Z")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		got := runFolderFor(dir, "SRV01", now, true)
		if seen[got] {
			t.Fatalf("run %d reused %q; two runs would merge into one folder", i+1, got)
		}
		seen[got] = true
		// Stand in for the run itself claiming the name.
		if err := os.MkdirAll(got, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// The same property stated where it matters: whatever runFolderFor returns
// under --keep, prepareRunFolder must accept it, and nothing may already be
// there. This is the pair that failed together — the search stopped early and
// the create had no check to catch it.
func TestKeepRunsNeverLandOnAnExistingRun(t *testing.T) {
	dir := t.TempDir()
	now, err := time.Parse(time.RFC3339, "2026-08-08T13:45:00Z")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		folder := runFolderFor(dir, "SRV01", now, true)
		if entries, derr := os.ReadDir(folder); derr == nil && len(entries) > 0 {
			t.Fatalf("run %d was pointed at %q, which already holds %d file(s)",
				i+1, folder, len(entries))
		}
		if err := prepareRunFolder(folder, true, os.Stderr); err != nil {
			t.Fatalf("run %d: prepareRunFolder(%q): %v", i+1, folder, err)
		}
		// Each run leaves a result file and an archive behind, as a real one does.
		if err := os.WriteFile(filepath.Join(folder, "result.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(folder+".zip", []byte("PK"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Three runs, three folders, three archives, each holding exactly its own file.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 6 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("want 3 folders and 3 archives, got %d entries: %v", len(entries), names)
	}
}

// An unreadable --queries-dir used to exit 1 and print nothing: Run appended
// the cause to the manifest and then returned a nil error, so the CLI — which
// prints only what it is handed — said nothing at all. Exit 1 is documented as
// "the instance could not be reached", so a DBA who mistyped the path following
// the timeout remedy in docs/dba-guide.md was told their server was down.
//
// Both halves matter and both are asserted here. The code must be 2, the
// configuration refusal, and the error must come back so the operator is told
// which path failed. Reaching this at all requires --queries-dir; the embedded
// corpus is verified by TestEmbeddedCorpusIsValid.
func TestRunReportsAnUnreadableQueriesDirAsBadConfiguration(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-corpus")
	code, err := Run(context.Background(), Options{
		Config: &Config{
			Server:     "localhost",
			OutputDir:  filepath.Join(dir, "output"),
			QueriesDir: missing,
		},
		Corpus: os.DirFS(missing),
		Root:   ".",
		Now:    time.Now(),
	})
	if code != 2 {
		t.Errorf("exit code = %d, want 2 (bad configuration, not 1 'instance unreachable')", code)
	}
	if err == nil {
		t.Fatal("Run returned a nil error, so the CLI prints nothing and the operator sees only a status code")
	}
	if !strings.Contains(err.Error(), "query corpus") {
		t.Errorf("error %q does not say the query corpus was the problem", err)
	}
	// os.DirFS roots the corpus at ".", so the underlying error names "." and
	// not the path the operator typed. Naming the directory is what makes the
	// message actionable.
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error %q does not name the directory that was given (%s)", err, missing)
	}
}

// The manifest is still written for that failure. A run that produced nothing
// must leave a record of having been attempted, and this path returns before a
// run folder exists, so it lands in a named failed-run-* subdirectory of the
// output directory. Not in the output directory itself: MANIFEST.txt lying
// beside the run folders of later, successful runs is read as belonging to one
// of them.
func TestRunLeavesAManifestWhenTheCorpusCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "output")
	missing := filepath.Join(dir, "no-such-corpus")
	if _, err := Run(context.Background(), Options{
		Config: &Config{Server: "localhost", OutputDir: out, QueriesDir: missing},
		Corpus: os.DirFS(missing),
		Root:   ".",
		Now:    time.Now(),
	}); err == nil {
		t.Fatal("want an error")
	}
	if _, err := os.Stat(filepath.Join(out, "_run.json")); err == nil {
		t.Error("the manifest was written into the output directory itself")
	}
	b, err := os.ReadFile(filepath.Join(failedRunDir(t, out), "_run.json"))
	if err != nil {
		t.Fatalf("no manifest written: %v", err)
	}
	if !strings.Contains(string(b), "query corpus") {
		t.Errorf("manifest does not record the cause:\n%s", b)
	}
}

// failedRunDir finds the single failed-run-* directory a fatal run leaves
// under the output directory.
func failedRunDir(t *testing.T, out string) string {
	t.Helper()
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatalf("reading %s: %v", out, err)
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "failed-run-") {
			found = append(found, filepath.Join(out, e.Name()))
		}
	}
	if len(found) != 1 {
		t.Fatalf("want one failed-run-* directory under %s, got %v", out, found)
	}
	return found[0]
}

func TestContainsShowplanIgnoresSQLThatMerelyReadsPlans(t *testing.T) {
	// The payloads 020 and 030 actually produce: plan metadata and a boolean
	// derived from the XML, with no XML in sight.
	for _, payload := range []string{
		`{"counts":{"plans":42,"forced_plans":1}}`,
		`{"queries":[{"plan_handle":"0x06","converts_implicitly":true}]}`,
	} {
		if containsShowplan([]byte(payload)) {
			t.Errorf("a payload with no plan tripped the disclosure: %s", payload)
		}
	}
}

func TestContainsShowplanCatchesAPlanInAnyShape(t *testing.T) {
	payload := `{"plans":[{"query_plan":"<ShowPlanXML xmlns=\"http://schemas.microsoft.com/sqlserver/2004/07/showplan\"><BatchSequence/></ShowPlanXML>"}]}`
	if !containsShowplan([]byte(payload)) {
		t.Error("plan XML emitted through the ordinary encoder was not detected")
	}
}

// The corpus as it stands must produce no disclosure. This is the regression
// test for the defect this design nearly shipped: a matcher over the SQL would
// have made both of these files disclose plans they do not emit.
func TestEmbeddedCorpusHasNoUngatedPlanEmitter(t *testing.T) {
	for _, path := range []string{
		"queries/80.workload/020.query-store.sql",
		"queries/80.workload/030.implicit-conversions.sql",
	} {
		b, err := fs.ReadFile(sqlauditor.Queries, path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		s := parseScript(path, string(b))
		if s.RequiresFlag != "" || s.Writer != "" {
			t.Errorf("%s is now gated; this test's premise has changed", path)
		}
	}
}

func TestResolveWindowUsesServerLocalTime(t *testing.T) {
	// The operator asks for 14:00-15:00 on a server two hours ahead of UTC.
	cfg := &Config{QueryStoreFrom: "2026-07-26T14:00", QueryStoreTo: "2026-07-26T15:00"}
	from, to, err := resolveWindow(cfg, time.Now(), time.FixedZone("server", 2*3600))
	if err != nil {
		t.Fatal(err)
	}
	if got := from.UTC().Format(time.RFC3339); got != "2026-07-26T12:00:00Z" {
		t.Errorf("from = %s, want 2026-07-26T12:00:00Z — the bound was not read as server local time", got)
	}
	if to.Sub(from) != time.Hour {
		t.Errorf("window = %s, want 1h", to.Sub(from))
	}
}

// resolveWindow refuses nothing about the days/bounds combination — Resolve
// already did, on the raw keys, where "typed" and "defaulted" are still
// distinguishable. Here it only translates.
func TestResolveWindowTranslatesTheSlidingForm(t *testing.T) {
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	cfg := &Config{QueryStoreDays: 7}
	from, to, err := resolveWindow(cfg, now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if !to.Equal(now) || to.Sub(from) != 7*24*time.Hour {
		t.Errorf("window = %s .. %s, want the seven days ending now", from, to)
	}
}

// The two refusals it does own. Both produce a window nothing can answer, and
// both look from the outside like a successful collection that found nothing.
func TestResolveWindowRefusesAWindowNobodyCanAnswer(t *testing.T) {
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, from, to, want string
	}{
		{"inverted", "2026-07-26T15:00", "2026-07-26T14:00", "before"},
		{"empty", "2026-07-26T14:00", "2026-07-26T14:00", "before"},
		{"in the future", "2026-09-01T14:00", "2026-09-01T15:00", "future"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{QueryStoreFrom: tc.from, QueryStoreTo: tc.to}
			_, _, err := resolveWindow(cfg, now, time.UTC)
			if err == nil {
				t.Fatalf("%s .. %s was accepted", tc.from, tc.to)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A lone --query-store-to leaves QueryStoreDays at 0 and says nothing about
// where the window starts. Resolve cannot fill that in — it does not know the
// offset — so the sliding default applies here, ending at the bound.
func TestResolveWindowFillsInTheStartWhenOnlyTheEndIsGiven(t *testing.T) {
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	cfg := &Config{QueryStoreTo: "2026-08-10"}
	from, to, err := resolveWindow(cfg, now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if got := to.Format(time.RFC3339); got != "2026-08-10T00:00:00Z" {
		t.Errorf("to = %s, want the bound that was typed", got)
	}
	if to.Sub(from) != 7*24*time.Hour {
		t.Errorf("window = %s, want the seven-day default ending at the bound", to.Sub(from))
	}
}

// The commas are load-bearing: 022 matches with CHARINDEX on the wrapped list,
// which is what keeps 11 from matching inside 211.
func TestJoinInt64WrapsTheListInCommas(t *testing.T) {
	if got := joinInt64([]int64{11, 22}); got != ",11,22," {
		t.Errorf("joinInt64 = %q, want \",11,22,\"", got)
	}
	if got := joinInt64(nil); got != "" {
		t.Errorf("joinInt64(nil) = %q, want the empty string so CHARINDEX matches nothing", got)
	}
}

// QUERY_STORE_DB_INCLUDE narrows the selection DB_INCLUDE already made; it
// never widens it, and every database it removes is named in the manifest.
func TestQueryStoreDatabasesFilterRatherThanWiden(t *testing.T) {
	folders := []DatabaseFolder{{Name: "Sales", Folder: "Sales"}, {Name: "Archive", Folder: "Archive"}}
	s := Script{Path: "80.workload/021.query-store-detail.sql", Writer: "query-store-detail"}
	kept, skipped := queryStoreUnits(&Config{QueryStoreDBInclude: "sal*"}, s, folders)
	if len(kept) != 1 || kept[0].Name != "Sales" {
		t.Fatalf("kept = %v, want only Sales", kept)
	}
	if len(skipped) != 1 || skipped[0].Target != "Archive" {
		t.Fatalf("skipped = %+v, want Archive named", skipped)
	}
	if !strings.Contains(skipped[0].Reason, "QUERY_STORE_DB_INCLUDE") {
		t.Errorf("reason = %q, does not say what removed the database", skipped[0].Reason)
	}
	// An ordinary collector is untouched by it: the setting is about the Query
	// Store extraction, not about which databases the run reads.
	plain := Script{Path: "80.workload/020.query-store.sql"}
	kept, skipped = queryStoreUnits(&Config{QueryStoreDBInclude: "sal*"}, plain, folders)
	if len(kept) != 2 || len(skipped) != 0 {
		t.Errorf("an ungated collector was filtered: kept %v, skipped %v", kept, skipped)
	}
}

// The disclosure comes from the bytes, never from the SQL. Each branch of it
// is one fact the archive would otherwise hold without admitting to.
func TestDiscloseWritesFollowsWhatWasWritten(t *testing.T) {
	gated := Script{Path: "80.workload/021.query-store-detail.sql",
		RequiresFlag: FlagQueryStoreDetail, Writer: "query-store-detail"}

	t.Run("a plan seen by the choke point", func(t *testing.T) {
		m, rw := &Manifest{}, newRunWriter(t.TempDir(), 1<<20)
		rw.sawShowplan = true
		discloseWrites(m, rw, gated, WriteResult{})
		if !m.Collected.QueryStoreDetail {
			t.Error("a plan went to disk and the manifest denies it")
		}
		if len(m.Warnings) != 0 {
			t.Errorf("a gated collector emitting plans is expected, not a warning: %v", m.Warnings)
		}
		if rw.sawShowplan {
			t.Error("the flag was not consumed, so the next unit inherits this one's plan")
		}
	})

	t.Run("a plan from an ungated collector", func(t *testing.T) {
		m, rw := &Manifest{}, newRunWriter(t.TempDir(), 1<<20)
		rw.sawShowplan = true
		discloseWrites(m, rw, Script{Path: "90.foreign/010.dump.sql"}, WriteResult{})
		if !m.Collected.QueryStoreDetail {
			t.Error("the archive holds a plan and does not say so")
		}
		if len(m.Warnings) != 1 || !strings.Contains(m.Warnings[0], "90.foreign/010.dump.sql") {
			t.Errorf("warnings = %v, want one naming the script", m.Warnings)
		}
	})

	t.Run("text without a plan", func(t *testing.T) {
		m, rw := &Manifest{}, newRunWriter(t.TempDir(), 1<<20)
		discloseWrites(m, rw, gated, WriteResult{TextFiles: 3})
		if !m.Collected.QueryStoreDetail {
			t.Error("query text was written and the manifest denies it")
		}
		if m.Collected.QueryStoreProfiledPlans {
			t.Error("the detail writer disclosed profiled plans")
		}
	})

	t.Run("the profiled writer", func(t *testing.T) {
		m, rw := &Manifest{}, newRunWriter(t.TempDir(), 1<<20)
		s := Script{Path: "80.workload/022.query-store-profiled.sql",
			RequiresFlag: FlagQueryStorePlanStats, Writer: "query-store-profiled"}
		discloseWrites(m, rw, s, WriteResult{PlanFiles: 2})
		if !m.Collected.QueryStoreProfiledPlans || !m.Collected.QueryStoreDetail {
			t.Errorf("collected = %+v, want both disclosures", m.Collected)
		}
	})

	// The invariant that makes read-and-reset safe, and the only subtest that
	// uses one Manifest twice. takeShowplan consumes the choke point's flag, so
	// the fact has to be latched here — in a manifest nothing ever clears — or
	// the second database, with its Query Store off, would retract the first
	// database's disclosure and the archive would deny plans it holds.
	t.Run("a second unit cannot retract the first one's disclosure", func(t *testing.T) {
		m, rw := &Manifest{}, newRunWriter(t.TempDir(), 1<<20)

		rw.sawShowplan = true
		discloseWrites(m, rw, gated, WriteResult{PlanFiles: 4, TextFiles: 4})
		if !m.Collected.QueryStoreDetail {
			t.Fatal("the first database's plans were not disclosed")
		}

		// The next database: the Query Store is off, so nothing is written and
		// the choke point saw nothing.
		discloseWrites(m, rw, gated, WriteResult{})
		if !m.Collected.QueryStoreDetail {
			t.Error("a database that collected nothing unset the disclosure of the one before it")
		}
	})

	t.Run("a writer that wrote nothing", func(t *testing.T) {
		m, rw := &Manifest{}, newRunWriter(t.TempDir(), 1<<20)
		discloseWrites(m, rw, gated, WriteResult{})
		if m.Collected.QueryStoreDetail || m.Collected.QueryStoreProfiledPlans {
			t.Errorf("collected = %+v, want nothing disclosed: the flag was passed but "+
				"a database with the Query Store off produced no text and no plan", m.Collected)
		}
	})
}

// A stale QUERY_STORE_TO in a .env must not kill a collection that reads
// neither flag. Nothing in such a run looks at the bounds, and refusing it is
// a refusal about a feature the operator did not ask for.
func TestAnUnusableWindowOnlyStopsARunThatReadsIt(t *testing.T) {
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	// Typed the wrong way round, which resolveWindow refuses.
	cfg := &Config{QueryStoreFrom: "2026-07-26T15:00", QueryStoreTo: "2026-07-26T14:00"}

	_, _, note, err := windowForRun(cfg, map[string]bool{}, now, time.UTC)
	if err != nil {
		t.Fatalf("an ordinary collection was killed by a setting it never reads: %v", err)
	}
	if !strings.Contains(note, "ignored") {
		t.Errorf("note = %q, want it to say the setting was ignored", note)
	}

	for _, flag := range []string{FlagQueryStoreDetail, FlagQueryStorePlanStats} {
		if _, _, _, err := windowForRun(cfg, map[string]bool{flag: true}, now, time.UTC); err == nil {
			t.Errorf("%s is on and the unusable window was accepted: the collection would "+
				"have read a window nobody resolved", flag)
		}
	}
}

// The bounds are the server's wall clock, so the clock the future check reads
// has to be the server's too. A server a few minutes ahead of the auditor's
// laptop otherwise refuses a bound typed as "just now" — with a message telling
// the operator to check it against the clock that accepted it.
func TestTheFutureBoundCheckReadsTheServerClock(t *testing.T) {
	laptop := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	si := ServerInfo{NowUTC: laptop.Add(10 * time.Minute)}
	// A bound five minutes past on the server, five minutes ahead on the laptop.
	cfg := &Config{QueryStoreFrom: "2026-08-13T08:00", QueryStoreTo: "2026-08-13T09:05"}
	flags := map[string]bool{FlagQueryStoreDetail: true}

	if _, _, _, err := windowForRun(cfg, flags, serverNow(si, laptop), time.UTC); err != nil {
		t.Errorf("the server's own clock says this bound is past, and it was refused: %v", err)
	}
	if _, _, _, err := windowForRun(cfg, flags, laptop, time.UTC); err == nil {
		t.Error("the laptop's clock puts the bound in the future; the test's premise is gone")
	}
	// Without an answer from the server, the local instant is all there is.
	if got := serverNow(ServerInfo{}, laptop); !got.Equal(laptop) {
		t.Errorf("serverNow = %s, want the local fallback %s", got, laptop)
	}
}

// 022 asked for nothing looks exactly like 022 asked and found nothing, and
// the archive must not leave the two indistinguishable.
func TestTheProfiledCollectorSaysWhenItWasGivenNothing(t *testing.T) {
	o := Options{Config: &Config{}, QueryStore: NewQueryStoreState()}
	profiled := Script{Path: "80.workload/022.query-store-profiled.sql", Writer: "query-store-profiled"}

	// No entry at all: 021 did not run, failed, or found the store off.
	args, note := queryStoreArgs(o, profiled, DatabaseFolder{Name: "Sales"})
	if note == "" || !strings.Contains(note, "Sales") {
		t.Errorf("note = %q, want one naming the database that was never selected for", note)
	}
	if len(args) != 1 {
		t.Fatalf("args = %v, want the one named parameter 022 declares", args)
	}

	// An entry that is empty is a different fact: 021 ran here and retained
	// nothing, which its own index already records. No warning is owed.
	o.QueryStore.Selected["Sales"] = nil
	if _, note := queryStoreArgs(o, profiled, DatabaseFolder{Name: "Sales"}); note != "" {
		t.Errorf("note = %q, want none: 021 delivered its answer, and the answer was empty", note)
	}

	// And a selection that exists is passed on with no comment.
	o.QueryStore.Selected["Sales"] = []int64{11, 22}
	if _, note := queryStoreArgs(o, profiled, DatabaseFolder{Name: "Sales"}); note != "" {
		t.Errorf("note = %q, want none", note)
	}
	// The detail collector never carries this warning: it is the one that
	// produces the selection, not the one that consumes it.
	detail := Script{Path: "80.workload/021.query-store-detail.sql", Writer: "query-store-detail"}
	if args, note := queryStoreArgs(o, detail, DatabaseFolder{Name: "Absent"}); note != "" || len(args) != 3 {
		t.Errorf("detail args = %v, note = %q; want its three parameters and no warning", args, note)
	}
}

// The overflow that bypassed both refusals. time.Duration(days) * 24 * time.Hour
// wraps int64 for a large enough count and yields a NEGATIVE duration, so
// now.Add(-d) lands after now: the window is inverted, the extraction matches
// nothing, and the run reads as a clean collection of a quiet instance. The
// sliding branch used to return before either refusal could see it.
func TestResolveWindowRefusesASlidingWindowThatOverflows(t *testing.T) {
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	// Resolve caps the value (maxQueryStoreDays); this is the second lock, on a
	// Config built by hand as a foreign caller could.
	if _, _, err := resolveWindow(&Config{QueryStoreDays: 999999}, now, time.UTC); err == nil {
		t.Error("999999 days was accepted: the duration overflowed and the inverted window went through unrefused")
	}
	from, to, err := resolveWindow(&Config{QueryStoreDays: maxQueryStoreDays}, now, time.UTC)
	if err != nil {
		t.Fatalf("the capped maximum was refused: %v", err)
	}
	if !from.Before(to) || to.Sub(from) != maxQueryStoreDays*24*time.Hour {
		t.Errorf("window = %s .. %s, want the %d days ending now", from, to, maxQueryStoreDays)
	}
}

// The days-versus-bounds conflict is priced where every other unusable window
// is priced. Resolve records it — it is the only place that can tell a typed
// QUERY_STORE_DAYS from a defaulted one — and refusing it there aborted a plain
// collect, and a check, over a setting nothing in either was going to read.
func TestTheDaysBoundsConflictOnlyStopsARunThatReadsTheWindow(t *testing.T) {
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	cfg, err := Resolve(nil, map[string]string{
		"SQL_SERVER": "srv", "QUERY_STORE_DAYS": "7", "QUERY_STORE_TO": "2026-08-10T14:00",
	}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	_, _, note, err := windowForRun(cfg, map[string]bool{}, now, time.UTC)
	if err != nil {
		t.Fatalf("a collection that reads no Query Store was killed by the conflict: %v", err)
	}
	if !strings.Contains(note, "ignored") || !strings.Contains(note, "QUERY_STORE_DAYS") {
		t.Errorf("note = %q, want it to say the conflicting setting was ignored and name it", note)
	}

	for _, flag := range []string{FlagQueryStoreDetail, FlagQueryStorePlanStats} {
		if _, _, _, err := windowForRun(cfg, map[string]bool{flag: true}, now, time.UTC); err == nil {
			t.Errorf("%s is on and the conflict was accepted: the collection would have read "+
				"one of two windows with no way to say which", flag)
		}
	}
}
