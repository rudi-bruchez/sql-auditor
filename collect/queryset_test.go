package collect

import (
	"strings"
	"testing"
	"testing/fstest"
)

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
		"SELECT 1 AS x;"
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
		"SELECT 1;"
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
