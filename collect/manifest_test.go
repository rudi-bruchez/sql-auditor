package collect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// flatten collapses the hard wrapping so an assertion is about the wording
// rather than about where a line happens to break.
func flatten(s string) string { return strings.Join(strings.Fields(s), " ") }

func TestManifestHumanStatesDataNature(t *testing.T) {
	m := &Manifest{}
	m.Server.Name, m.Server.Version = "SRV01", "11.0.7001.0"
	m.Targets.Databases = []DatabaseFolder{{Name: "AppProd", Folder: "AppProd"}}
	// The embedded corpus, so the paragraph that tells the reader how to check
	// the queries for themselves is rendered. That instruction is the one
	// self-verification step in the document, and it has to be a command that
	// works: bare "queries export" exits 2 asking for --to.
	m.Sources = map[string]SourceInfo{"queries": {From: "embedded", SHA256: "abc"}}
	h := flatten(m.Human())
	for _, want := range []string{
		"SRV01", "11.0.7001.0", "AppProd",
		"read-only SELECT statements",
		"does not read any user or application table",
		// Scoped to what the collector actually masks. An unqualified claim
		// that "secrets are masked" describes nothing this program does.
		"The password of the login used for this run is recorded nowhere",
		`replaced with "(redacted)"`,
		"queries export --to DIR",
		"login names of database owners",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("MANIFEST.txt should mention %q:\n%s", want, m.Human())
		}
	}
}

// The corpus captures est.text from sys.dm_exec_sql_text, plus session login,
// host and program names, and that text is the verbatim SQL of live batches:
// it routinely carries literals such as names and email addresses. A claim of
// "no personal data" is therefore false whenever that collector ran, and this
// is the one sentence in the tool a security officer relies on. The archive
// must never say less than it holds.
func TestHumanNeverClaimsAbsenceOfDataItMayHold(t *testing.T) {
	forbidden := []string{
		"no business data",
		"no table contents",
		"no personal data",
		"no rows from application tables",
	}
	for _, collectedSessionText := range []bool{false, true} {
		m := &Manifest{Collected: CollectedKinds{SessionText: collectedSessionText}}
		h := flatten(m.Human())
		for _, phrase := range forbidden {
			if strings.Contains(h, phrase) {
				t.Errorf("session_text=%v: MANIFEST.txt claims %q, which the corpus cannot guarantee:\n%s",
					collectedSessionText, phrase, m.Human())
			}
		}
	}
}

// When statement text WAS captured, the disclosure has to say so in the list a
// security officer reads, not in a footnote.
func TestHumanDisclosesCapturedStatementText(t *testing.T) {
	with := flatten((&Manifest{Collected: CollectedKinds{SessionText: true}}).Human())
	for _, want := range []string{
		"the SQL text of statements running during collection",
		"may contain values from application tables",
		"potentially containing personal data",
	} {
		if !strings.Contains(with, want) {
			t.Errorf("MANIFEST.txt should disclose %q:\n%s", want, with)
		}
	}
	// And the safe default must not make the wider disclosure, which would be
	// just as wrong in the other direction.
	without := flatten((&Manifest{}).Human())
	if strings.Contains(without, "the SQL text of statements running during collection") {
		t.Errorf("statement text disclosed for a run that did not collect it:\n%s", without)
	}
	if !strings.Contains(without, "metadata about the estate rather than the data held in it") {
		t.Errorf("the default run should be described as metadata:\n%s", without)
	}
}

// "Every query is published at github.com/…" is false when --queries-dir
// supplied the corpus. The reader's only way to check the disclosure above is
// to read the queries, so pointing them at the wrong ones is worse than
// pointing them nowhere.
func TestHumanClaimsThePublishedCorpusOnlyWhenItWasUsed(t *testing.T) {
	embedded := &Manifest{Sources: map[string]SourceInfo{
		"queries": {From: "embedded", Path: "queries", SHA256: "abc123"},
	}}
	if h := flatten(embedded.Human()); !strings.Contains(h, "Every query the collector runs is published at") {
		t.Errorf("an embedded corpus should point at the published queries:\n%s", h)
	}
	local := &Manifest{Sources: map[string]SourceInfo{
		"queries": {From: "queries-dir", Path: "/opt/site-queries", SHA256: "def456"},
	}}
	h := flatten(local.Human())
	if strings.Contains(h, "Every query the collector runs is published at") {
		t.Errorf("a local corpus must not be claimed as the published one:\n%s", h)
	}
	if strings.Contains(h, "sql-auditor queries export") {
		t.Errorf("the export command lists the built-in corpus, not the one used:\n%s", h)
	}
	for _, want := range []string{"did not come from the published corpus", "SHA-256"} {
		if !strings.Contains(h, want) {
			t.Errorf("MANIFEST.txt should say %q:\n%s", want, h)
		}
	}
	// With no recorded source, neither claim can be made.
	if h := flatten((&Manifest{}).Human()); strings.Contains(h, "published at") || strings.Contains(h, "did not come from the published corpus") {
		t.Errorf("no source recorded, so no provenance claim belongs in the file:\n%s", h)
	}
}

// Size and file count are the first two things anyone approving a transfer
// looks for.
func TestHumanReportsFileCountAndTotalSize(t *testing.T) {
	m := &Manifest{Results: []ResultEntry{
		{Script: "010.properties", Output: "10.system/010.properties.json", Bytes: 2048},
		{Script: "100.tables", Output: "AppProd/100.tables.json", Bytes: 1024},
		// Same output written twice must count once, or the figure overstates.
		{Script: "100.tables", Output: "AppProd/100.tables.json", Bytes: 1024},
		// An entry that produced no file contributes nothing.
		{Script: "200.skipped", Output: "", Bytes: 0},
	}}
	h := m.Human()
	if !strings.Contains(h, "2 data files") {
		t.Errorf("file count should be 2 distinct outputs:\n%s", h)
	}
	if !strings.Contains(h, "3.0 KB") {
		t.Errorf("total size should be 3.0 KB:\n%s", h)
	}
}

func TestHumanBytesScales(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want string
	}{
		{0, "0 bytes"},
		{999, "999 bytes"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{5 * 1024 * 1024, "5.0 MB"},
		{3 * 1024 * 1024 * 1024, "3.0 GB"},
	} {
		if got := humanBytes(tc.n); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// A probe that got no answer is not a refused permission. Saying so would send
// a DBA hunting for a GRANT that was never the problem.
func TestCoverageDoesNotReportATransportFailureAsADenial(t *testing.T) {
	m := &Manifest{Preflight: []CapabilityCheck{
		{Name: "connect", Status: "ok"},
		{Name: "view_any_definition", Label: "Read server and database metadata (VIEW ANY DEFINITION)",
			Status: "error", Impact: "instance configuration and database file layout not collected"},
	}}
	m.refreshCoverage()
	if !m.Coverage.DatabaseListMayBeIncomplete {
		t.Error("an unanswered probe leaves the database list just as untrustworthy")
	}
	for _, note := range m.Coverage.Notes {
		if strings.Contains(note, "was refused") {
			t.Errorf("a transport failure reported as a refusal: %q", note)
		}
	}
	h := flatten(m.Human())
	if !strings.Contains(h, "got no answer from the server") {
		t.Errorf("MANIFEST.txt should say the check went unanswered:\n%s", h)
	}
	if strings.Contains(h, "Re-run with VIEW ANY DEFINITION granted") {
		t.Errorf("MANIFEST.txt tells the DBA to fix a permission that was never refused:\n%s", h)
	}
}

// MANIFEST.txt is read by someone who has never seen this project's permission
// vocabulary; _run.json is read by code that matches on it.
func TestHumanPrintsCapabilityLabelsAndJSONKeepsIdentifiers(t *testing.T) {
	dir := t.TempDir()
	m := &Manifest{Preflight: []CapabilityCheck{
		{Name: "view_server_state", Label: "Read performance counters (VIEW SERVER STATE)",
			Status: "denied", Impact: "wait statistics not collected"},
	}}
	h := m.Human()
	if !strings.Contains(h, "Read performance counters (VIEW SERVER STATE)") {
		t.Errorf("MANIFEST.txt should name the capability in English:\n%s", h)
	}
	if strings.Contains(h, "view_server_state (") {
		t.Errorf("MANIFEST.txt should not print the raw identifier as the heading:\n%s", h)
	}
	if err := m.WriteJSON(dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "_run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"name": "view_server_state"`) {
		t.Errorf("_run.json must keep the identifier:\n%s", b)
	}
}

// The design spec shows snake_case throughout _run.json. Three types reached
// the manifest untagged and emitted Go field names.
func TestRunJSONUsesSnakeCaseKeysForEmbeddedTypes(t *testing.T) {
	dir := t.TempDir()
	m := &Manifest{Preflight: []CapabilityCheck{{Name: "connect", Status: "ok"}}}
	m.Targets.Databases = []DatabaseFolder{{Name: "AppProd", Folder: "AppProd"}}
	m.Targets.Skipped = []SkipReason{{Name: "OldArchive", Reason: "state=OFFLINE"}}
	if err := m.WriteJSON(dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "_run.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"name":`, `"status":`, `"folder":`, `"reason":`, `"started_utc":`, `"session_text":`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("_run.json missing key %s:\n%s", want, b)
		}
	}
	for _, unwanted := range []string{`"Name":`, `"Status":`, `"Folder":`, `"Reason":`, `"StartedUTC":`} {
		if strings.Contains(string(b), unwanted) {
			t.Errorf("_run.json emits the Go field name %s:\n%s", unwanted, b)
		}
	}
}

func TestWriteManifestFallsBackWhenRunFolderUnwritable(t *testing.T) {
	m := &Manifest{}
	// A path that cannot exist forces the fallback. The run must still leave
	// a manifest somewhere, or it cannot be reasoned about afterwards.
	path, err := WriteManifestWithFallback(m, filepath.Join(t.TempDir(), "nope", "deeper"), os.Stderr)
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
	got, err := WriteManifestWithFallback(m, dir, os.Stderr)
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
	m := &Manifest{Preflight: []CapabilityCheck{
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
	m := &Manifest{Preflight: []CapabilityCheck{
		{Name: "connect", Status: "ok"},
		{Name: "view_any_definition", Status: "denied", Impact: "instance configuration and database file layout not collected"},
	}}
	m.Server.Name = "SRV01"
	h := m.Human()
	// The prose is hard-wrapped, so a phrase may straddle a line break. Assert
	// against the wording, not against where the wrapping happens to fall.
	flat := flatten(h)
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

// A run that never reached the instance — a wrong port, a refused login —
// records no preflight, so coverage is UNKNOWN. It also never listed the
// databases. Printing "Databases covered (0): (none matched the selection for
// this run)" makes a statement about an instance that was never contacted, on
// the strength of no evidence at all.
func TestHumanMakesNoDatabaseClaimWhenNoPreflightRan(t *testing.T) {
	m := &Manifest{}
	m.Server.Name = "SRV01"
	flat := flatten(m.Human())
	if strings.Contains(flat, "none matched the selection") {
		t.Error("MANIFEST.txt asserted that no database matched, having never asked the server")
	}
	for _, want := range []string{"UNKNOWN", "No database list was collected"} {
		if !strings.Contains(flat, want) {
			t.Errorf("MANIFEST.txt should contain %q:\n%s", want, m.Human())
		}
	}
}

func TestHumanReportsCompleteCoverageWhenNothingWasDenied(t *testing.T) {
	m := &Manifest{Preflight: []CapabilityCheck{
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
	m := &Manifest{Preflight: []CapabilityCheck{{Name: "connect", Status: "error", Impact: "nothing can run"}}}
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

// The framing exists to make the hash injective: two different corpora must
// never produce the same byte stream. Both cases below collide under a weaker
// framing — the first when the path length is left out, the second when the
// framing is dropped altogether — so between them they hold the framing in
// place.
func TestCorpusSHA256FramingIsInjective(t *testing.T) {
	cases := []struct {
		name string
		a, b fstest.MapFS
	}{
		{
			// Without the path length: both frame to "a 0\nb 0\n".
			name: "a path may contain the separator and the digits",
			a:    fstest.MapFS{"a": {Data: []byte("")}, "b": {Data: []byte("")}},
			b:    fstest.MapFS{"a 0\nb": {Data: []byte("")}},
		},
		{
			// With no framing at all: both frame to "xy".
			name: "content alone does not identify a corpus",
			a:    fstest.MapFS{"ab": {Data: []byte("xy")}},
			b:    fstest.MapFS{"a": {Data: []byte("")}, "b": {Data: []byte("xy")}},
		},
		{
			name: "a file split in two is a different corpus",
			a:    fstest.MapFS{"ab": {Data: []byte("xy")}},
			b:    fstest.MapFS{"a": {Data: []byte("b")}, "b": {Data: []byte("xy")}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ha, err := CorpusSHA256(tc.a, ".")
			if err != nil {
				t.Fatal(err)
			}
			hb, err := CorpusSHA256(tc.b, ".")
			if err != nil {
				t.Fatal(err)
			}
			if ha == hb {
				t.Errorf("two different corpora hashed the same: %s", ha)
			}
		})
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

func TestManifestTextDisclosesQueryStoreDetail(t *testing.T) {
	m := NewManifest("sql-auditor", "test", "abc")
	m.Collected.QueryStoreDetail = true
	got := m.Human()
	for _, want := range []string{"execution plan", "parameter values"} {
		if !strings.Contains(strings.ToLower(got), want) {
			t.Errorf("MANIFEST.txt does not mention %q:\n%s", want, got)
		}
	}
}

func TestManifestTextSilentWithoutQueryStoreDetail(t *testing.T) {
	m := NewManifest("sql-auditor", "test", "abc")
	got := strings.ToLower(m.Human())
	if strings.Contains(got, "execution plan") {
		t.Errorf("MANIFEST.txt claims plans are present when none were collected:\n%s", got)
	}
}

func TestManifestTextDisclosesTheInstanceWideCacheRead(t *testing.T) {
	m := NewManifest("sql-auditor", "test", "abc")
	m.Collected.QueryStoreDetail = true
	m.Collected.QueryStoreProfiledPlans = true
	got := strings.ToLower(m.Human())
	if !strings.Contains(got, "plan cache") {
		t.Errorf("MANIFEST.txt does not say the plan cache of the whole instance was read:\n%s", got)
	}
}

func TestManifestTextClassifiesQueryStoreDetailAsPersonalData(t *testing.T) {
	m := NewManifest("sql-auditor", "test", "abc")
	m.Collected.QueryStoreDetail = true
	// SessionText is false, so if the condition only checks SessionText, this will fail
	got := flatten(m.Human())
	if strings.Contains(got, "internal infrastructure documentation rather than public material") {
		t.Errorf("MANIFEST.txt claims internal infrastructure when QueryStoreDetail was collected:\n%s", m.Human())
	}
	if !strings.Contains(got, "potentially containing personal data") {
		t.Errorf("MANIFEST.txt should classify QueryStoreDetail archives as potentially containing personal data:\n%s", m.Human())
	}
}
