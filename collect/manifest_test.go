package collect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestManifestHumanStatesDataNature(t *testing.T) {
	m := &Manifest{}
	m.Server.Name, m.Server.Version = "SRV01", "11.0.7001.0"
	m.Targets.Databases = []DatabaseFolder{{Name: "AppProd", Folder: "AppProd"}}
	h := m.Human()
	for _, want := range []string{"SRV01", "11.0.7001.0", "AppProd", "no business data", "no table contents"} {
		if !strings.Contains(h, want) {
			t.Errorf("MANIFEST.txt should mention %q:\n%s", want, h)
		}
	}
}

func TestWriteManifestFallsBackWhenRunFolderUnwritable(t *testing.T) {
	m := &Manifest{}
	// A path that cannot exist forces the fallback. The run must still leave
	// a manifest somewhere, or it cannot be reasoned about afterwards.
	path, err := WriteManifestWithFallback(m, filepath.Join(t.TempDir(), "nope", "deeper"))
	if err != nil {
		t.Fatalf("fallback failed entirely: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("reported path %s does not exist: %v", path, err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(path)) })
}

func TestWriteManifestWritesBothFilesInTheRunFolder(t *testing.T) {
	dir := t.TempDir()
	m := &Manifest{}
	m.Server.Name = "SRV01"
	got, err := WriteManifestWithFallback(m, dir)
	if err != nil {
		t.Fatalf("WriteManifestWithFallback: %v", err)
	}
	if want := filepath.Join(dir, "_run.json"); got != want {
		t.Errorf("path = %s, want %s", got, want)
	}
	for _, name := range []string{"_run.json", "MANIFEST.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s not written: %v", name, err)
		}
	}
}

// A denied VIEW ANY DEFINITION is silent: sys.databases returns fewer rows
// instead of raising, and because selection filters database_id > 4 such a
// login can yield zero user databases while every query "succeeds". The
// manifest is the only place that can tell an analysis layer the difference
// between "this instance has no user databases" and "this login could not see
// them", so the machine-readable side must state it as a flag, not bury it in
// the preflight array.
func TestCoverageFlagsSilentlyIncompleteDatabaseList(t *testing.T) {
	dir := t.TempDir()
	m := &Manifest{Preflight: []Check{
		{Name: "connect", Status: "ok"},
		{Name: "view_any_definition", Status: "denied", Impact: "instance configuration and database file layout not collected"},
		{Name: "view_server_state", Status: "ok"},
	}}
	if err := m.WriteJSON(dir); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "_run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Coverage struct {
			Status                      string   `json:"status"`
			DatabaseListMayBeIncomplete bool     `json:"database_list_may_be_incomplete"`
			Denied                      []string `json:"denied_capabilities"`
			Notes                       []string `json:"notes"`
		} `json:"coverage"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("_run.json is not valid JSON: %v", err)
	}
	if doc.Coverage.Status != "incomplete" {
		t.Errorf("coverage.status = %q, want %q", doc.Coverage.Status, "incomplete")
	}
	if !doc.Coverage.DatabaseListMayBeIncomplete {
		t.Error("coverage.database_list_may_be_incomplete should be true when view_any_definition is denied")
	}
	if len(doc.Coverage.Denied) != 1 || doc.Coverage.Denied[0] != "view_any_definition" {
		t.Errorf("coverage.denied_capabilities = %v", doc.Coverage.Denied)
	}
	if len(doc.Coverage.Notes) == 0 {
		t.Error("coverage.notes should explain the silent denial")
	}
}

// "Databases covered: 0" with no explanation reads as a broken tool. The human
// text has to say why the list is empty and that emptiness is not a finding.
func TestHumanExplainsZeroDatabasesUnderDeniedDefinition(t *testing.T) {
	m := &Manifest{Preflight: []Check{
		{Name: "connect", Status: "ok"},
		{Name: "view_any_definition", Status: "denied", Impact: "instance configuration and database file layout not collected"},
	}}
	m.Server.Name = "SRV01"
	h := m.Human()
	// The prose is hard-wrapped, so a phrase may straddle a line break. Assert
	// against the wording, not against where the wrapping happens to fall.
	flat := strings.Join(strings.Fields(h), " ")
	for _, want := range []string{
		"INCOMPLETE",
		"VIEW ANY DEFINITION",
		"not visible",
		"cannot be determined from this archive",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("MANIFEST.txt should contain %q:\n%s", want, h)
		}
	}
	if strings.Contains(flat, "no user databases exist") {
		t.Error("MANIFEST.txt must not assert that no databases exist")
	}
}

func TestHumanReportsCompleteCoverageWhenNothingWasDenied(t *testing.T) {
	m := &Manifest{Preflight: []Check{
		{Name: "connect", Status: "ok"},
		{Name: "view_any_definition", Status: "ok"},
		{Name: "view_server_state", Status: "ok"},
		{Name: "msdb_read", Status: "ok"},
	}}
	m.Targets.Databases = []DatabaseFolder{{Name: "AppProd", Folder: "AppProd"}}
	h := m.Human()
	if !strings.Contains(h, "COMPLETE") {
		t.Errorf("MANIFEST.txt should report complete coverage:\n%s", h)
	}
	if strings.Contains(h, "INCOMPLETE") {
		t.Errorf("MANIFEST.txt should not report incomplete coverage:\n%s", h)
	}
}

// An unreachable instance is a different failure from a refusal, and the
// coverage verdict must not launder one into the other.
func TestCoverageDistinguishesErrorFromDenial(t *testing.T) {
	m := &Manifest{Preflight: []Check{{Name: "connect", Status: "error", Impact: "nothing can run"}}}
	m.refreshCoverage()
	if m.Coverage.Status != "incomplete" {
		t.Errorf("status = %q, want incomplete", m.Coverage.Status)
	}
	h := m.Human()
	if !strings.Contains(h, "unreachable") && !strings.Contains(h, "no answer") {
		t.Errorf("MANIFEST.txt should say the probe got no answer:\n%s", h)
	}
}

// With no preflight recorded there is nothing to base a verdict on. Claiming
// completeness would be a stronger statement than the run can support.
func TestCoverageIsUnknownWithoutPreflight(t *testing.T) {
	m := &Manifest{}
	m.refreshCoverage()
	if m.Coverage.Status != "unknown" {
		t.Errorf("status = %q, want unknown", m.Coverage.Status)
	}
	if m.Coverage.DatabaseListMayBeIncomplete {
		t.Error("no evidence of a denial, so the flag must stay false")
	}
}

// The archive leaves the client's site. A password that reached the config
// block must not leave with it.
func TestWriteJSONRedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	m := &Manifest{Config: map[string]string{
		"SQL_SERVER":   "SRV01",
		"SQL_PASSWORD": "hunter2",
		"SQL_USER":     "auditor",
	}}
	if err := m.WriteJSON(dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "_run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "hunter2") {
		t.Errorf("_run.json leaks the password:\n%s", b)
	}
	if !strings.Contains(string(b), "SRV01") {
		t.Error("redaction removed non-secret configuration")
	}
	// Redaction must not mutate the caller's map: the same Manifest is written
	// twice in a run (once early, once at the end).
	if m.Config["SQL_PASSWORD"] != "hunter2" {
		t.Error("WriteJSON mutated the caller's config map")
	}
}

func TestCorpusSHA256IsStableAndContentSensitive(t *testing.T) {
	a := fstest.MapFS{
		"queries/10.system/010.properties.sql": {Data: []byte("SELECT 1")},
		"queries/20.db/100.tables.sql":         {Data: []byte("SELECT 2")},
	}
	h1, err := CorpusSHA256(a, "queries")
	if err != nil {
		t.Fatal(err)
	}
	h2, err := CorpusSHA256(a, "queries")
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Errorf("hash is not stable: %s vs %s", h1, h2)
	}
	b := fstest.MapFS{
		"queries/10.system/010.properties.sql": {Data: []byte("SELECT 1")},
		"queries/20.db/100.tables.sql":         {Data: []byte("SELECT 3")},
	}
	h3, err := CorpusSHA256(b, "queries")
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h3 {
		t.Error("changed content produced the same hash")
	}
	// Renaming a file changes which question was asked, so it must change the
	// hash even when the bytes are identical.
	c := fstest.MapFS{
		"queries/10.system/010.properties.sql": {Data: []byte("SELECT 1")},
		"queries/20.db/101.tables.sql":         {Data: []byte("SELECT 2")},
	}
	h4, err := CorpusSHA256(c, "queries")
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h4 {
		t.Error("renamed file produced the same hash")
	}
}

// The embedded corpus is rooted at "queries"; a --queries-dir corpus opened
// with os.DirFS is rooted at ".". The hash exists to compare the two, so it
// must not depend on the root it was reached through.
func TestCorpusSHA256IgnoresTheRootPrefix(t *testing.T) {
	embedded := fstest.MapFS{
		"queries/10.system/010.properties.sql": {Data: []byte("SELECT 1")},
	}
	external := fstest.MapFS{
		"10.system/010.properties.sql": {Data: []byte("SELECT 1")},
	}
	h1, err := CorpusSHA256(embedded, "queries")
	if err != nil {
		t.Fatal(err)
	}
	h2, err := CorpusSHA256(external, ".")
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Errorf("same corpus hashed differently through two roots: %s vs %s", h1, h2)
	}
}

func TestHumanListsSkippedDatabasesWithReasons(t *testing.T) {
	m := &Manifest{}
	m.Targets.Skipped = []SkipReason{{Name: "OldArchive", Reason: "state=OFFLINE"}}
	h := m.Human()
	if !strings.Contains(h, "OldArchive") || !strings.Contains(h, "state=OFFLINE") {
		t.Errorf("skipped databases and reasons should appear:\n%s", h)
	}
}

func TestHumanSummarisesScriptsWithoutRepeatingPerDatabase(t *testing.T) {
	m := &Manifest{Results: []ResultEntry{
		{Script: "100.tables", Scope: "database", Target: "A", Status: "ok"},
		{Script: "100.tables", Scope: "database", Target: "B", Status: "ok"},
		{Script: "010.properties", Scope: "server", Status: "ok"},
	}}
	h := m.Human()
	if strings.Count(h, "100.tables") != 1 {
		t.Errorf("a per-database script should be listed once, with a count:\n%s", h)
	}
	if !strings.Contains(h, "x2") {
		t.Errorf("the repeat count should be shown:\n%s", h)
	}
}
