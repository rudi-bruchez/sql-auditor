package collect

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"
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
	if err := prepareRunFolder(run, false); err != nil {
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
	if err := prepareRunFolder(run, false); err != nil {
		t.Fatalf("prepareRunFolder: %v", err)
	}
	if _, err := os.Stat(zip); !os.IsNotExist(err) {
		t.Error("stale archive survived a replacing rerun; it no longer matches the folder beside it")
	}
}

func TestPrepareRunFolderKeepsExistingWhenKeeping(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "SRV01-2026-08-08-1200")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	keeper := filepath.Join(run, "keep.json")
	if err := os.WriteFile(keeper, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareRunFolder(run, true); err != nil {
		t.Fatalf("prepareRunFolder: %v", err)
	}
	if _, err := os.Stat(keeper); err != nil {
		t.Errorf("--keep deleted the existing folder: %v", err)
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
	if !strings.Contains(reason, "view_server_state") {
		t.Errorf("reason %q does not name the permission", reason)
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
	corpus := []Script{{Path: "10.system/052.session-text.sql", RequiresFlag: FlagIncludeSessionText}}

	off := &Manifest{}
	off.Collected.SessionText = collectsSessionText(planScripts(corpus, nil, nil, nil))
	if off.Collected.SessionText {
		t.Error("the flag was off; the manifest must not claim session text is present")
	}
	if strings.Contains(off.Human(), sessionTextDisclosure) {
		t.Error("MANIFEST.txt disclosed session text for a run that did not collect it")
	}

	on := &Manifest{}
	on.Collected.SessionText = collectsSessionText(
		planScripts(corpus, nil, nil, map[string]bool{FlagIncludeSessionText: true}))
	if !on.Collected.SessionText {
		t.Fatal("the flag was on; the manifest must disclose session text")
	}
	if !strings.Contains(on.Human(), sessionTextDisclosure) {
		t.Error("MANIFEST.txt understated what the archive contains")
	}
}

// A corpus with no session-text collector in it must not disclose session
// text however the flag is set: the claim follows what ran, not the option.
func TestSessionTextNotClaimedWhenNoScriptCollectsIt(t *testing.T) {
	plain := []Script{{Path: "10.system/010.properties.sql"}}
	on := map[string]bool{FlagIncludeSessionText: true}
	if collectsSessionText(planScripts(plain, nil, nil, on)) {
		t.Error("no script collects session text; the flag alone must not set the claim")
	}
}

// A session-text collector kept out by a denied permission or an old server is
// not in the archive either, so the manifest must not disclose it.
func TestSessionTextNotClaimedWhenTheScriptWasSkippedAnyway(t *testing.T) {
	gated := []Script{{
		Path:         "10.system/052.session-text.sql",
		RequiresFlag: FlagIncludeSessionText,
		Permissions:  []string{"view_server_state"},
	}}
	on := map[string]bool{FlagIncludeSessionText: true}
	plan := planScripts(gated, map[string]bool{"view_server_state": true}, nil, on)
	if collectsSessionText(plan) {
		t.Error("the collector was skipped for want of a permission; nothing was captured to disclose")
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
		if strings.Contains(strings.ToLower(s.SQL), "dm_exec_sql_text") && s.RequiresFlag != FlagIncludeSessionText {
			t.Errorf("%s reads dm_exec_sql_text without @requires_flag: %s", s.Path, FlagIncludeSessionText)
		}
	}
	if gated != 1 {
		t.Errorf("got %d session-text collectors, want exactly 1", gated)
	}
	if collectsSessionText(planScripts(scripts, nil, nil, nil)) {
		t.Error("the default run would collect session text")
	}
	if !collectsSessionText(planScripts(scripts, nil, nil, map[string]bool{FlagIncludeSessionText: true})) {
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
