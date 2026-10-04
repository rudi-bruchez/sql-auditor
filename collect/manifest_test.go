package collect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// flatten collapses the hard wrapping so an assertion is about the wording
// rather than about where a line happens to break.
func flatten(s string) string { return strings.Join(strings.Fields(s), " ") }

// A collection is evidence, and the question "was it gathered over a channel
// whose far end was verified?" is asked months later by someone holding the
// archive and not the .env it was run from. The terminal note that says so
// scrolls away; this is the copy that survives.
//
// All three states are asserted, and the encrypted-but-unvalidated one is the
// reason the block exists. Encryption without validation stops an eavesdropper
// and does not stop a machine-in-the-middle, which terminates the TLS itself
// and presents whatever certificate it likes — so recording "encrypted: true"
// alone would be the reassuring half of a two-part answer.
func TestManifestRecordsHowTheConnectionWasSecured(t *testing.T) {
	for _, c := range []struct {
		name      string
		transport TransportBlock
		want      []string
		absent    []string
	}{
		{
			name:      "encrypted and validated",
			transport: TransportBlock{Encrypted: true, CertificateValidated: true},
			want:      []string{"Connection : encrypted, and the server certificate was validated"},
			absent:    []string{"NOT validated"},
		},
		{
			name:      "encrypted, any certificate accepted",
			transport: TransportBlock{Encrypted: true},
			want: []string{
				"encrypted, but the server certificate was NOT validated",
				// The setting is named, so the reader can tell which decision
				// produced this and reverse it.
				"SQL_TRUST_SERVER_CERTIFICATE was on",
				"any certificate answering for that address was accepted",
			},
		},
		{
			name:      "not encrypted",
			transport: TransportBlock{},
			want: []string{
				"not encrypted",
				// Named rather than glossed over: SQL Server encrypts the login
				// packet whatever the setting says, so a flat "not encrypted"
				// would be wrong about the credentials while right about
				// everything else.
				"except the login packet",
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := &Manifest{}
			m.Server.Name = "SRV01"
			m.Transport = c.transport
			h := flatten(m.Human())
			for _, want := range c.want {
				if !strings.Contains(h, want) {
					t.Errorf("MANIFEST.txt does not say %q; it says: %s", want, m.Human())
				}
			}
			for _, no := range c.absent {
				if strings.Contains(h, no) {
					t.Errorf("MANIFEST.txt says %q and should not", no)
				}
			}
		})
	}
}

// The machine-readable half has to agree with the sentence, or an analysis
// reading the JSON and a person reading the text would answer the question
// differently.
func TestManifestTransportSurvivesTheJSON(t *testing.T) {
	m := &Manifest{}
	m.Transport = TransportBlock{Encrypted: true}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back Manifest
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !back.Transport.Encrypted || back.Transport.CertificateValidated {
		t.Errorf("the transport block did not survive the round trip: %+v", back.Transport)
	}
	// Both keys are written even though one is false. An absent key and a false
	// one would be told apart by nothing.
	for _, key := range []string{`"encrypted":true`, `"certificate_validated":false`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("the JSON manifest is missing %s: %s", key, b)
		}
	}
}

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
		// The promise has to survive a reader who diffs it against the
		// corpus. Three shipped collectors capture the output of a command
		// that has no view — DBCC, sp_readerrorlog — into scratch storage of
		// their own, which is an INSERT. An absolute "runs no INSERT" is
		// therefore false, and false in the paragraph a security officer
		// reads before approving the tool. The guarantee that is both true
		// and worth as much is about what the server keeps.
		"creates no permanent object",
		"nothing that belongs to this server or its databases is created, altered or deleted",
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
		{1 << 40, "1.0 TB"},
		// A petabyte, which has no letter in the list. The size of an archive
		// comes from an unbounded os.Stat and the wizard spells it with this
		// function, so indexing "KMGT" past its last letter would panic inside
		// a renderer that is documented as total on its inputs — leaving the
		// terminal in raw mode with no wizard left to restore it. Reported in
		// terabytes instead, which is true.
		{1 << 50, "1024.0 TB"},
		{1 << 60, "1048576.0 TB"},
	} {
		if got := HumanBytes(tc.n); got != tc.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", tc.n, got, tc.want)
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
		{Name: "view_server_state", Label: "Read the server state views (VIEW SERVER STATE)",
			Status: "denied", Impact: "wait statistics not collected"},
	}}
	h := m.Human()
	if !strings.Contains(h, "Read the server state views (VIEW SERVER STATE)") {
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

// The disclosure latches on the Showplan namespace in any payload, and default
// collectors copy query text that may carry it. Measured on a lab instance on
// 4 October 2026: a default run's MANIFEST.txt said the option was passed when
// it was not. The sentence now follows the recorded option.
func TestManifestTextNamesTheReasonForTheQueryStoreDisclosure(t *testing.T) {
	m := NewManifest("sql-auditor", "test", "abc")
	m.Collected.QueryStoreDetail = true
	m.Config = map[string]string{FlagQueryStoreDetail: "false"}
	if got := m.Human(); strings.Contains(got, "because --query-store-detail was passed") ||
		!strings.Contains(got, "without --query-store-detail") {
		t.Errorf("MANIFEST.txt credits an option that was not passed:\n%s", got)
	}
	m.Config[FlagQueryStoreDetail] = "true"
	if got := m.Human(); !strings.Contains(got, "because --query-store-detail was passed") {
		t.Errorf("MANIFEST.txt does not credit the option that was passed:\n%s", got)
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

func TestManifestExplainsAWidenedDatabase(t *testing.T) {
	m := NewManifest("SQL01", "11.0.7001.0", "")
	m.Targets.Databases = []DatabaseFolder{
		{Name: "SALESDB", Folder: "SALESDB"},
		{Name: "DISTDB", Folder: "DISTDB", WidenedPurpose: "replication",
			RetentionReason: "local distributor for 1 published database(s) in this selection"},
	}
	h := flatten(m.Human())
	if !strings.Contains(h, "local distributor for 1 published database(s)") {
		t.Errorf("MANIFEST.txt must say why DISTDB is here:\n%s", m.Human())
	}
	// The purpose token is machinery. Rendered into the reason slot it would
	// put the bare word "replication" in front of a reader with nothing to
	// attach it to — which is what happens if the two fields are ever merged
	// back into one.
	if strings.Contains(h, "kept because: replication") {
		t.Errorf("the manifest shows the reason, not the purpose token:\n%s", m.Human())
	}
	// And the reader is told this is not a full collection, or they go looking
	// for an object inventory that was never taken.
	if !strings.Contains(h, "replication metadata only") {
		t.Errorf("MANIFEST.txt must say DISTDB was not fully collected:\n%s", m.Human())
	}
}

// The distribution database is not narrowed by DB_INCLUDE the way the rest of
// the run is: its catalogs describe every publication on the instance, so a
// run cadenced on one database archives the publisher_db and the article names
// of databases the operator never named. That is a disclosure beyond the
// stated scope, into a file that gets mailed onward, and the manifest is where
// whoever opens the archive learns it.
func TestManifestSaysAWidenedDatabaseReachesBeyondTheSelection(t *testing.T) {
	m := NewManifest("SQL01", "11.0.7001.0", "")
	m.Targets.Databases = []DatabaseFolder{
		{Name: "SALESDB", Folder: "SALESDB"},
		{Name: "DISTDB", Folder: "DISTDB", WidenedPurpose: "replication",
			RetentionReason: "local distributor for 1 published database(s) in this selection"},
	}
	h := flatten(m.Human())
	if !strings.Contains(h, "outside this selection") {
		t.Errorf("MANIFEST.txt does not warn that the widened database describes "+
			"databases outside the selection:\n%s", m.Human())
	}
}

// The paragraph a security officer reads before releasing the archive says
// that nothing on the server was created, altered or deleted. That is a claim
// about the queries that ran, and it is true of the published corpus because
// this project wrote it and the statement-class lint enforces it. A corpus
// supplied with --queries-dir is neither: the lint still refuses anything but a
// read, but nobody here reviewed the file, and the sentence has to say which of
// the two vouched for the run. Saying it flatly, as this document did until the
// external panel of 4 September 2026 pointed --queries-dir at a corpus that
// dropped a database, is the manifest attesting the opposite of what happened.
func TestManifestAttributesTheReadOnlyClaimToWhoVouchedForIt(t *testing.T) {
	embedded := NewManifest("sql-auditor", "test", "abc")
	embedded.Sources = map[string]SourceInfo{"queries": {From: "embedded", SHA256: "abc"}}
	h := flatten(embedded.Human())
	if !strings.Contains(h, "creates no permanent object") {
		t.Errorf("the published corpus should carry the flat attestation:\n%s", embedded.Human())
	}
	if strings.Contains(h, "statement-class lint") {
		t.Errorf("the published corpus should not need the lint hedge:\n%s", embedded.Human())
	}

	foreign := NewManifest("sql-auditor", "test", "abc")
	foreign.Sources = map[string]SourceInfo{
		"queries": {From: "filesystem", Path: "C:/scripts", SHA256: "def"}}
	f := flatten(foreign.Human())
	for _, unwanted := range []string{
		"creates no permanent object",
		"nothing that belongs to this server or its databases is created, altered or deleted",
	} {
		if strings.Contains(f, unwanted) {
			t.Errorf("a corpus this project never saw must not carry %q:\n%s", unwanted, foreign.Human())
		}
	}
	for _, want := range []string{
		"did not come from the published corpus",
		"statement-class lint",
		"is not a sandbox",
	} {
		if !strings.Contains(f, want) {
			t.Errorf("a foreign corpus should say %q:\n%s", want, foreign.Human())
		}
	}
}

// The default archive already holds three kinds of captured text that name
// things: the SQL of the heaviest Query Store queries, the first 200 characters
// of every Agent job step, and samples of the error log. None needs a flag, and
// none was in the list a reader checks before releasing the archive.
func TestManifestListsTheTextTheDefaultRunCaptures(t *testing.T) {
	m := NewManifest("sql-auditor", "test", "abc")
	m.Sources = map[string]SourceInfo{"queries": {From: "embedded", SHA256: "abc"}}
	m.Disclosed = []string{"error_log", "job_step_text", "query_text"}
	h := flatten(m.Human())
	for _, want := range []string{
		"first 500 characters",
		"first 200 characters",
		"error log",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("MANIFEST.txt should disclose %q on the default path:\n%s", want, m.Human())
		}
	}
}

// 042.connection-security.sql groups the live connections by host, program
// and login on every default run, to compare each application with the client's
// pool size. Those three name the application servers and the accounts behind
// them, so the collector must declare it and the manifest must print it: a
// declaration dropped from the header would silently take the sentence out of
// MANIFEST.txt while the names stayed in the archive.
func TestConnectionPoolsAreDeclaredAndDisclosed(t *testing.T) {
	scripts, err := Discover(os.DirFS(filepath.Join("..", "queries")), ".")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	found := false
	for _, s := range scripts {
		if s.Path != "10.system/042.connection-security.sql" {
			continue
		}
		found = true
		if !slices.Contains(s.Discloses, "connection_pools") {
			t.Errorf("%s projects host, program and login names and must declare "+
				"@discloses: connection_pools, got %v", s.Path, s.Discloses)
		}
	}
	if !found {
		t.Fatal("10.system/042.connection-security.sql is not in the corpus")
	}
	m := NewManifest("sql-auditor", "test", "abc")
	m.Sources = map[string]SourceInfo{"queries": {From: "embedded", SHA256: "abc"}}
	m.Disclosed = []string{"connection_pools"}
	if h := flatten(m.Human()); !strings.Contains(h, "host names, program names and logins") {
		t.Errorf("MANIFEST.txt should disclose the connection pool names:\n%s", m.Human())
	}
}

// 40.security/032.server-trigger-definitions.sql exports the body of every
// server trigger under --include-object-definitions. MANIFEST.txt's paragraph on
// object definitions is latched from the files 080.modules.sql writes, so a run
// whose databases hold no module would carry a trigger body and say nothing of
// it. The declaration is what closes that, and it must not quietly leave the
// header.
func TestServerTriggerSourceIsGatedAndDisclosed(t *testing.T) {
	scripts, err := Discover(os.DirFS(filepath.Join("..", "queries")), ".")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	const path = "40.security/032.server-trigger-definitions.sql"
	found := false
	for _, s := range scripts {
		if s.Path != path {
			continue
		}
		found = true
		if s.RequiresFlag != FlagObjectDefinitions {
			t.Errorf("%s exports trigger source and must declare @requires_flag: %s, got %q",
				s.Path, FlagObjectDefinitions, s.RequiresFlag)
		}
		if !slices.Contains(s.Discloses, "server_trigger_source") {
			t.Errorf("%s exports trigger source and must declare "+
				"@discloses: server_trigger_source, got %v", s.Path, s.Discloses)
		}
	}
	if !found {
		t.Fatalf("%s is not in the corpus", path)
	}
	m := NewManifest("sql-auditor", "test", "abc")
	m.Sources = map[string]SourceInfo{"queries": {From: "embedded", SHA256: "abc"}}
	m.Disclosed = []string{"server_trigger_source"}
	if h := flatten(m.Human()); !strings.Contains(h, "server-scoped trigger") {
		t.Errorf("MANIFEST.txt should disclose the server trigger source:\n%s", m.Human())
	}
}

// Application-written text that leaves on the default path must be declared by
// the collector that projects it and printed in MANIFEST.txt with its length.
// None of these was declared until 4 October 2026: a job's failure message and
// a replication error can quote the row that made them fail, and a default
// constraint or an index filter can hold a literal copied out of the
// application. Each row names the file, the token it must carry, and a phrase
// the manifest must print for that token, which is how a reader learns how
// much of the text left.
func TestApplicationTextIsDeclaredAndDisclosed(t *testing.T) {
	scripts, err := Discover(os.DirFS(filepath.Join("..", "queries")), ".")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	byPath := map[string]Script{}
	for _, s := range scripts {
		byPath[s.Path] = s
	}
	for _, c := range []struct {
		path, token, phrase string
	}{
		{"50.agent/010.jobs.sql", "job_messages", "last failed run of each SQL Server Agent job, up to 512 characters"},
		{"50.agent/030.alerts.sql", "job_messages", "notification message"},
		{"90.availability/042.replication-distribution.sql", "replication_messages", "up to 512 characters each"},
		{"90.availability/030.log-shipping.sql", "log_shipping_messages", "up to 4 000 characters"},
		{"70.schema/060.columns.sql", "schema_expressions", "computed column definitions, default constraints"},
		{"70.schema/020.index-usage.sql", "schema_expressions", "filtered indexes"},
		{"70.schema/070.index-columns.sql", "schema_expressions", "filtered indexes"},
		{"70.schema/090.statistics.sql", "schema_expressions", "statistics"},
		{"80.workload/020.query-store.sql", "query_text", "first 500 characters"},
		{"80.workload/020.query-store.sql", "query_text", "at most 200 queries in each ranking per database"},
		{"80.workload/023.query-store-most-executed.sql", "query_text", "at most 200 queries"},
		{"80.workload/024.query-store-rowcount.sql", "query_text", "at most 200 queries"},
		{"80.workload/026.query-store-interrupted.sql", "query_text", "at most 200 queries"},
		{"80.workload/028.query-store-resources.sql", "query_text", "at most 200 queries"},
	} {
		s, ok := byPath[c.path]
		if !ok {
			t.Errorf("%s is not in the corpus", c.path)
			continue
		}
		if s.RequiresFlag != "" {
			t.Errorf("%s is gated by %q; this test is about the default path", c.path, s.RequiresFlag)
		}
		if !slices.Contains(s.Discloses, c.token) {
			t.Errorf("%s must declare @discloses: %s, got %v", c.path, c.token, s.Discloses)
		}
		m := NewManifest("sql-auditor", "test", "abc")
		m.Sources = map[string]SourceInfo{"queries": {From: "embedded", SHA256: "abc"}}
		m.Disclosed = s.Discloses
		if h := flatten(m.Human()); !strings.Contains(h, c.phrase) {
			t.Errorf("MANIFEST.txt for %s should say %q:\n%s", c.path, c.phrase, m.Human())
		}
	}
}

// A warning that names a script and not a database is raised by every
// database the script runs on. The copies carry nothing, and on the lab 32 of
// them buried the other 3.
func TestManifestKeepsEachWarningOnce(t *testing.T) {
	m := NewManifest("sql-auditor", "test", "abc")
	s := Script{Path: "90.foreign/010.dump.sql"}
	for range 3 {
		m.Collected.QueryStoreDetail = false
		rw := newRunWriter(t.TempDir(), 1<<20)
		rw.sawShowplan = true
		discloseWrites(m, rw, s, WriteResult{})
	}
	m.warn("another warning", "another warning")
	if len(m.Warnings) != 2 {
		t.Fatalf("want each distinct warning once, got %d: %q", len(m.Warnings), m.Warnings)
	}
	if h := m.Human(); strings.Count(h, "90.foreign/010.dump.sql: a payload") != 1 {
		t.Errorf("MANIFEST.txt repeats the warning:\n%s", h)
	}
}

// Every @discloses token must say which family it belongs to, because the
// family is what decides whether MANIFEST.txt may close on "metadata about the
// estate". A token added to KnownDisclosures without a family would be read as
// names only, and a new kind of application text would leave the archive
// described as metadata again.
func TestEveryDisclosureHasAFamily(t *testing.T) {
	for name := range KnownDisclosures {
		switch disclosureFamilies[name] {
		case DisclosesNames, DisclosesApplicationText:
		default:
			t.Errorf("@discloses %q has no family in disclosureFamilies", name)
		}
	}
	for name := range disclosureFamilies {
		if _, ok := KnownDisclosures[name]; !ok {
			t.Errorf("disclosureFamilies classifies %q, which KnownDisclosures does not know", name)
		}
	}
}

// The last paragraph of "What this archive contains" follows the list above
// it. Until 4 October 2026 it ignored @discloses altogether, so a default run
// that listed Query Store text, job failure messages and error log lines still
// told the reader it was metadata about the estate rather than the data held
// in it.
func TestClosingParagraphFollowsWhatWasDisclosed(t *testing.T) {
	const metadata = "That is metadata about the estate rather than the data held in it"
	const personal = "potentially containing personal data"
	for name, family := range disclosureFamilies {
		m := NewManifest("sql-auditor", "test", "abc")
		m.Sources = map[string]SourceInfo{"queries": {From: "embedded", SHA256: "abc"}}
		m.Disclosed = []string{name}
		h := flatten(m.Human())
		switch family {
		case DisclosesApplicationText:
			if strings.Contains(h, metadata) || !strings.Contains(h, personal) ||
				!strings.Contains(h, "personal data among them") {
				t.Errorf("%s is application text and the closing paragraph should say it can "+
					"quote values and personal data:\n%s", name, m.Human())
			}
		case DisclosesNames:
			if !strings.Contains(h, metadata) || strings.Contains(h, personal) {
				t.Errorf("%s only names things and the archive should still be described "+
					"as metadata:\n%s", name, m.Human())
			}
		}
	}

	m := NewManifest("sql-auditor", "test", "abc")
	m.Sources = map[string]SourceInfo{"queries": {From: "embedded", SHA256: "abc"}}
	m.Collected.PlanCachePlans = true
	if h := flatten(m.Human()); strings.Contains(h, metadata) || !strings.Contains(h, personal) {
		t.Errorf("plans from the plan cache were written and the archive is described as metadata:\n%s",
			m.Human())
	}
}
