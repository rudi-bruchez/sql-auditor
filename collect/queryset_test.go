package collect

import (
	"strings"
	"testing"
	"testing/fstest"
)

// contractPreamble is the two SET statements the auditor contract demands of
// every collector. Fixtures that assert a clean lint have to carry them, and
// an OPTION (RECOMPILE, MAXDOP 1) per declared result set, or they fail the
// contract check rather than the thing they are testing.
const contractPreamble = "SET NOCOUNT ON;\nSET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;\n"

const goodSQL = `-- @scope:       instance
-- @resultsets:  instance:object, waits:array
-- @permissions: VIEW SERVER STATE
SET NOCOUNT ON;
SELECT 1 AS [instance.x];
SELECT 2 AS t;
`

func TestDiscoverParsesDirectives(t *testing.T) {
	fsys := fstest.MapFS{
		"queries/10.system/010.properties.sql":    {Data: []byte(goodSQL)},
		"queries/20.databases/020.properties.sql": {Data: []byte("-- @scope: database\n-- @resultsets: db:object\nSELECT 1 AS x;")},
		"queries/10.system/050.tempdb.sql":        {Data: []byte("-- @resultsets: t:object\nSELECT 1 AS x;")},
	}
	got, err := Discover(fsys, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d scripts, want 3", len(got))
	}
	// Numeric prefixes drive order: 10.system before 20.databases, 010 before 050.
	wantOrder := []string{
		"10.system/010.properties.sql",
		"10.system/050.tempdb.sql",
		"20.databases/020.properties.sql",
	}
	for i, w := range wantOrder {
		if got[i].Path != w {
			t.Errorf("position %d = %s, want %s", i, got[i].Path, w)
		}
	}
	s := got[0]
	if s.Scope != ScopeInstance {
		t.Errorf("scope = %v, want instance", s.Scope)
	}
	if len(s.Results) != 2 ||
		s.Results[0] != (ResultSpec{"instance", ShapeObject}) ||
		s.Results[1] != (ResultSpec{"waits", ShapeArray}) {
		t.Errorf("results = %+v", s.Results)
	}
	// Permissions are stored as normalised capability keys, not as the text
	// the query author wrote.
	if len(s.Permissions) != 1 || s.Permissions[0] != "view_server_state" {
		t.Errorf("permissions = %v, want [view_server_state]", s.Permissions)
	}
	if got[2].Scope != ScopeDatabase {
		t.Errorf("020.properties scope = %v, want database", got[2].Scope)
	}
}

func TestDiscoverLintErrors(t *testing.T) {
	tests := []struct {
		name, path, body, want string
	}{
		{"GO batch separator", "queries/10.system/010.a.sql",
			"-- @resultsets: a:object\nSELECT 1;\nGO\n", "GO"},
		{"missing resultsets directive", "queries/10.system/010.a.sql",
			"SELECT 1;", "@resultsets"},
		{"unknown shape", "queries/10.system/010.a.sql",
			"-- @resultsets: a:thing\nSELECT 1;", "thing"},
		{"FOR JSON is no longer allowed", "queries/10.system/010.a.sql",
			"-- @resultsets: a:object\nSELECT 1 FOR JSON PATH;", "FOR JSON"},
		{"correlated result set", "queries/10.system/010.a.sql",
			"-- @resultsets: a:object\n-- @correlated\nSELECT 1;", "correlated"},
		{"root must be an object", "queries/10.system/010.a.sql",
			"-- @resultsets: root:array\nSELECT 1;", "root"},
		{"unknown permission name", "queries/10.system/010.a.sql",
			"-- @resultsets: a:object\n-- @permissions: SELECT ANY DICTIONARY\nSELECT 1;",
			"SELECT ANY DICTIONARY"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{tc.path: {Data: []byte(tc.body)}}
			got, err := Discover(fsys, "queries")
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if len(got) != 1 || got[0].LintError == "" {
				t.Fatalf("expected a lint error, got %+v", got)
			}
			if !strings.Contains(got[0].LintError, tc.want) {
				t.Errorf("lint error %q should mention %q", got[0].LintError, tc.want)
			}
		})
	}
}

func TestStripSQLComments(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"line comment", "SELECT 1; -- FOR JSON was removed\n", "SELECT 1; \n"},
		{"block comment", "SELECT /* FOR JSON */ 1;", "SELECT  1;"},
		{"string literal survives", "SELECT '-- not a comment';", "SELECT '-- not a comment';"},
		{"string literal with block marker", "SELECT '/* keep */';", "SELECT '/* keep */';"},
		{"escaped quote inside literal", "SELECT 'it''s -- fine';", "SELECT 'it''s -- fine';"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripSQLComments(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLintIgnoresForJSONInsideComments(t *testing.T) {
	// A collector documenting why FOR JSON was removed must not fail its own
	// lint. This is the whole reason the lint runs on stripped SQL.
	body := "-- @resultsets: a:object\n" +
		"-- FOR JSON was removed: the collector must work on SQL Server 2012.\n" +
		contractPreamble +
		"SELECT 1 AS x OPTION (RECOMPILE, MAXDOP 1);"
	fsys := fstest.MapFS{"queries/10.system/010.a.sql": {Data: []byte(body)}}
	got, err := Discover(fsys, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got[0].LintError != "" {
		t.Errorf("unexpected lint error: %s", got[0].LintError)
	}
}

func TestDiscoverNormalisesPermissions(t *testing.T) {
	body := "-- @resultsets: a:object\n" +
		"-- @permissions: VIEW SERVER STATE, view any definition, msdb read\n" +
		contractPreamble +
		"SELECT 1 OPTION (RECOMPILE, MAXDOP 1);"
	fsys := fstest.MapFS{"queries/10.system/010.a.sql": {Data: []byte(body)}}
	got, err := Discover(fsys, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got[0].LintError != "" {
		t.Fatalf("unexpected lint error: %s", got[0].LintError)
	}
	want := []string{"view_server_state", "view_any_definition", "msdb_read"}
	if len(got[0].Permissions) != len(want) {
		t.Fatalf("permissions = %v, want %v", got[0].Permissions, want)
	}
	for i, w := range want {
		if got[0].Permissions[i] != w {
			t.Errorf("permission %d = %q, want %q", i, got[0].Permissions[i], w)
		}
	}
}

func TestDiscoverRejectsStrayFiles(t *testing.T) {
	// go:embed without all: already hides dot-prefixed files, but a .bak or a
	// misnamed file would otherwise become a phantom collector.
	fsys := fstest.MapFS{
		"queries/10.system/010.properties.sql":     {Data: []byte("-- @resultsets: a:object\nSELECT 1;")},
		"queries/10.system/010.properties.sql.bak": {Data: []byte("junk")},
		"queries/notes.txt":                        {Data: []byte("junk")},
	}
	_, err := Discover(fsys, "queries")
	if err == nil {
		t.Fatal("expected an error naming the stray files")
	}
	if !strings.Contains(err.Error(), ".bak") {
		t.Errorf("error should name the stray file, got: %v", err)
	}
}

func TestDiscoverRejectsFileAtQueryRoot(t *testing.T) {
	// A correctly named file with no NN.area/ directory level must not be
	// silently accepted as a collector — that is worse than a silent skip.
	fsys := fstest.MapFS{
		"queries/010.properties.sql": {Data: []byte("-- @resultsets: a:object\nSELECT 1;")},
	}
	_, err := Discover(fsys, "queries")
	if err == nil {
		t.Fatal("expected an error naming the root-level file")
	}
	if !strings.Contains(err.Error(), "010.properties.sql") {
		t.Errorf("error should name the stray file, got: %v", err)
	}
}

func TestStripSQLCommentsNestedBlocks(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"nested block comment", "SELECT /* outer /* inner */ still commented? */ 1;", "SELECT  1;"},
		{"unterminated block comment does not panic",
			"SELECT 1; /* never closed", "SELECT 1; "},
		{"unterminated string literal does not panic",
			"SELECT 'never closed", "SELECT 'never closed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripSQLComments(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseScriptReportsFirstDirectiveErrorOnly(t *testing.T) {
	// @permissions appears before @resultsets in document order, so its
	// problem is the one reported; fixing top-down converges.
	body := "-- @permissions: SELECT ANY DICTIONARY\n" +
		"-- @resultsets: a:thing\n" +
		"SELECT 1;"
	fsys := fstest.MapFS{"queries/10.system/010.a.sql": {Data: []byte(body)}}
	got, err := Discover(fsys, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !strings.Contains(got[0].LintError, "SELECT ANY DICTIONARY") {
		t.Errorf("lint error = %q, want it to mention the first problem (@permissions)", got[0].LintError)
	}
	if strings.Contains(got[0].LintError, "thing") {
		t.Errorf("lint error = %q, should not report the later @resultsets problem", got[0].LintError)
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in   string
		want []int
	}{
		{"13", []int{13}},
		{"13.0.5026", []int{13, 0, 5026}},
		{"11.0.7001.0", []int{11, 0, 7001, 0}},
		{"", nil},
		{"not.a.version", nil},
	}
	for _, tc := range tests {
		got := ParseVersion(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("ParseVersion(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("ParseVersion(%q) = %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}

func TestVersionAtLeast(t *testing.T) {
	tests := []struct {
		name       string
		have, want string
		ok         bool
	}{
		{"2012 does not satisfy 2016 SP2", "11.0.7001.0", "13.0.5026", false},
		{"2016 RTM does not satisfy 2016 SP2", "13.0.1601.5", "13.0.5026", false},
		{"2016 SP2 satisfies itself", "13.0.5026.0", "13.0.5026", true},
		{"2019 satisfies 2016 SP2", "15.0.4123.1", "13.0.5026", true},
		{"2014 satisfies a major-only gate of 12", "12.0.6024.0", "12", true},
		{"2012 does not satisfy a major-only gate of 12", "11.0.7001.0", "12", false},
		{"an ungated script always runs", "11.0.7001.0", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := VersionAtLeast(ParseVersion(tc.have), ParseVersion(tc.want)); got != tc.ok {
				t.Errorf("VersionAtLeast(%s, %s) = %v, want %v", tc.have, tc.want, got, tc.ok)
			}
		})
	}
}

func TestDiscoverParsesMinVersion(t *testing.T) {
	fsys := fstest.MapFS{
		"queries/10.system/015.cpu.sql": {Data: []byte(
			"-- @resultsets: root:object\n-- @min_version: 13.0.5026\nSELECT 1 AS x;")},
		"queries/10.system/010.a.sql": {Data: []byte(
			"-- @resultsets: root:object\nSELECT 1 AS x;")},
	}
	got, err := Discover(fsys, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got[0].MinVersion != nil {
		t.Errorf("ungated script has MinVersion %v, want nil", got[0].MinVersion)
	}
	want := []int{13, 0, 5026}
	if len(got[1].MinVersion) != len(want) {
		t.Fatalf("MinVersion = %v, want %v", got[1].MinVersion, want)
	}
	for i := range want {
		if got[1].MinVersion[i] != want[i] {
			t.Fatalf("MinVersion = %v, want %v", got[1].MinVersion, want)
		}
	}
}

func TestDiscoverRejectsBadMinVersion(t *testing.T) {
	fsys := fstest.MapFS{"queries/10.system/010.a.sql": {Data: []byte(
		"-- @resultsets: root:object\n-- @min_version: sixteen\nSELECT 1;")}}
	got, err := Discover(fsys, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got[0].LintError == "" || !strings.Contains(got[0].LintError, "sixteen") {
		t.Errorf("lint error = %q, want it to name the bad value", got[0].LintError)
	}
}

// The auditor contract is what makes a collector safe to run on a production
// instance. Fourteen files comply by review; these tests are so the fifteenth
// complies by rule.
func TestLintEnforcesTheAuditorContract(t *testing.T) {
	const header = "-- @resultsets: a:array, b:array\n"
	tests := []struct {
		name, sql, want string
	}{
		{"missing NOCOUNT",
			header + "SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;\n" +
				"SELECT 1 OPTION (RECOMPILE, MAXDOP 1);\nSELECT 2 OPTION (RECOMPILE, MAXDOP 1);",
			"SET NOCOUNT ON"},
		{"missing isolation level",
			header + "SET NOCOUNT ON;\n" +
				"SELECT 1 OPTION (RECOMPILE, MAXDOP 1);\nSELECT 2 OPTION (RECOMPILE, MAXDOP 1);",
			"READ UNCOMMITTED"},
		{"one result set short of a hint",
			header + contractPreamble + "SELECT 1 OPTION (RECOMPILE, MAXDOP 1);\nSELECT 2;",
			"OPTION (RECOMPILE, MAXDOP 1)"},
		{"hint without MAXDOP",
			header + contractPreamble +
				"SELECT 1 OPTION (RECOMPILE, MAXDOP 1);\nSELECT 2 OPTION (RECOMPILE);",
			"in another form"},
		// The contract is about the code, not the prose. A file explaining why
		// it dropped a hint must not satisfy the check by mentioning it.
		{"prose does not satisfy the contract",
			header + "-- SET NOCOUNT ON and OPTION (RECOMPILE, MAXDOP 1) belong on every query.\n" +
				"SELECT 1;\nSELECT 2;",
			"SET NOCOUNT ON"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{"queries/10.system/010.a.sql": {Data: []byte(tc.sql)}}
			got, err := Discover(fsys, "queries")
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if !strings.Contains(got[0].LintError, tc.want) {
				t.Errorf("lint error = %q, want it to mention %q", got[0].LintError, tc.want)
			}
		})
	}
}

func TestLintAcceptsMoreHintsThanResultSets(t *testing.T) {
	// 050.tempdb.sql assigns into a variable with its own hinted statement, so
	// the count legitimately exceeds the number of result sets.
	body := "-- @resultsets: a:array\n" + contractPreamble +
		"DECLARE @n int;\nSELECT @n = COUNT(*) FROM sys.databases OPTION (RECOMPILE, MAXDOP 1);\n" +
		"SELECT @n AS n OPTION (RECOMPILE, MAXDOP 1);"
	fsys := fstest.MapFS{"queries/10.system/010.a.sql": {Data: []byte(body)}}
	got, err := Discover(fsys, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got[0].LintError != "" {
		t.Errorf("unexpected lint error: %s", got[0].LintError)
	}
}

// @scope: databases used to fall through to the instance default, so a
// per-database collector ran once against master and the run silently held one
// file where it should have held one per database.
func TestDiscoverRejectsBadScopeAndTimeout(t *testing.T) {
	for _, tc := range []struct{ name, header, want string }{
		{"scope", "-- @scope: databases\n", "databases"},
		{"timeout", "-- @timeout: 60s\n", "60s"},
		{"zero timeout", "-- @timeout: 0\n", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "-- @resultsets: a:object\n" + tc.header + contractPreamble +
				"SELECT 1 AS x OPTION (RECOMPILE, MAXDOP 1);"
			fsys := fstest.MapFS{"queries/10.system/010.a.sql": {Data: []byte(body)}}
			got, err := Discover(fsys, "queries")
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if got[0].LintError == "" || !strings.Contains(got[0].LintError, tc.want) {
				t.Errorf("lint error = %q, want it to name the bad value", got[0].LintError)
			}
		})
	}
}

func TestDiscoverAcceptsExplicitInstanceScope(t *testing.T) {
	body := "-- @resultsets: a:object\n-- @scope: instance\n-- @timeout: 90\n" +
		contractPreamble + "SELECT 1 AS x OPTION (RECOMPILE, MAXDOP 1);"
	fsys := fstest.MapFS{"queries/10.system/010.a.sql": {Data: []byte(body)}}
	got, err := Discover(fsys, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got[0].LintError != "" {
		t.Fatalf("unexpected lint error: %s", got[0].LintError)
	}
	if got[0].Scope != ScopeInstance || got[0].TimeoutSec != 90 {
		t.Errorf("scope = %v, timeout = %d", got[0].Scope, got[0].TimeoutSec)
	}
}

func TestParseScriptAcceptsKnownWriter(t *testing.T) {
	sql := `-- @scope:       database
-- @resultsets:  root:object
-- @writer:      query-store-detail
` + contractPreamble + "SELECT 1 AS x OPTION (RECOMPILE, MAXDOP 1);\n"
	s := parseScript("80.workload/021.query-store-detail.sql", sql)
	if s.LintError != "" {
		t.Fatalf("LintError = %q, want none", s.LintError)
	}
	if s.Writer != "query-store-detail" {
		t.Errorf("Writer = %q, want query-store-detail", s.Writer)
	}
}

func TestParseScriptRefusesUnknownWriter(t *testing.T) {
	sql := `-- @resultsets: root:object
-- @writer:     query-store-detials
` + contractPreamble + "SELECT 1 AS x OPTION (RECOMPILE, MAXDOP 1);\n"
	s := parseScript("80.workload/021.x.sql", sql)
	if !strings.Contains(s.LintError, "unknown writer") {
		t.Fatalf("LintError = %q, want it to mention an unknown writer", s.LintError)
	}
	if s.Writer != "" {
		t.Errorf("Writer = %q, want empty on a rejected name", s.Writer)
	}
}

// A writer writes one directory per database, so it needs a database. At
// instance scope it would reach runUnit with an empty DatabaseFolder: the
// directory collapses onto the non-per-database path and every such script
// shares the Selected[""] bucket. Both failures are silent, which is why this
// is a lint and not a runtime check.
func TestParseScriptRefusesAWriterOutsideDatabaseScope(t *testing.T) {
	for _, scope := range []string{"-- @scope: instance\n", ""} {
		sql := scope + `-- @resultsets: root:object
-- @writer:     query-store-detail
` + contractPreamble + "SELECT 1 AS x OPTION (RECOMPILE, MAXDOP 1);\n"
		s := parseScript("80.workload/021.x.sql", sql)
		if !strings.Contains(s.LintError, "@scope: database") {
			t.Errorf("scope %q: LintError = %q, want it to require @scope: database", scope, s.LintError)
		}
	}
}

func TestKnownFlagsCarriesTheQueryStoreFlags(t *testing.T) {
	want := map[string]string{
		"query_store_detail":     "--query-store-detail",
		"query_store_plan_stats": "--query-store-plan-stats",
	}
	for k, v := range want {
		if KnownFlags[k] != v {
			t.Errorf("KnownFlags[%s] = %q, want %q", k, KnownFlags[k], v)
		}
	}
}
