package collect

import (
	"strings"
	"testing"
)

func TestPreflightExitCode(t *testing.T) {
	ok := []Check{{Name: "connect", Status: "ok"}}
	denied := []Check{{Name: "connect", Status: "ok"}, {Name: "msdb_read", Status: "denied"}}
	dead := []Check{{Name: "connect", Status: "error"}}

	tests := []struct {
		name     string
		checks   []Check
		lint     int
		writable bool
		want     int
	}{
		// A degraded run is a warning. If a denied permission returned 2, a DBA
		// without VIEW ANY DEFINITION would conclude the tool is broken and stop.
		{"all clear", ok, 0, true, 0},
		{"permission denied still succeeds", denied, 0, true, 0},
		{"lint failure", ok, 1, true, 2},
		{"output not writable", ok, 0, false, 2},
		{"cannot connect", dead, 0, true, 1},
		// An unreachable instance outranks a lint failure: fixing the query
		// corpus does not help a DBA who cannot reach the server.
		{"cannot connect outranks lint", dead, 1, false, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := PreflightExitCode(tc.checks, tc.lint, tc.writable); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestCapabilitiesCoverDeclaredPermissions(t *testing.T) {
	// Every permission a query declares must have a probe, or the preflight
	// silently promises coverage it does not have.
	names := map[string]bool{}
	for _, p := range Capabilities() {
		names[p.Name] = true
	}
	for _, want := range []string{"connect", "view_server_state", "view_any_definition", "msdb_read"} {
		if !names[want] {
			t.Errorf("no capability named %q", want)
		}
	}
	for _, p := range Capabilities() {
		if p.Impact == "" {
			t.Errorf("capability %q has no stated impact; the DBA needs the consequence, not the permission name", p.Name)
		}
		if p.SQL == "" {
			t.Errorf("capability %q has no probe query; permissions are probed, not deduced", p.Name)
		}
	}
}

// The capability names and the permission vocabulary a script writes in
// @permissions are the same namespace. If they drift, matching a denied
// capability to the scripts that need it silently never fires.
func TestCapabilityNamesMatchNormalisedPermissions(t *testing.T) {
	caps := map[string]bool{}
	for _, p := range Capabilities() {
		caps[p.Name] = true
	}
	for _, written := range []string{
		"CONNECT", "VIEW SERVER STATE", "VIEW ANY DEFINITION", "MSDB READ",
	} {
		key, ok := NormalisePermission(written)
		if !ok {
			t.Fatalf("NormalisePermission(%q) not recognised", written)
		}
		if !caps[key] {
			t.Errorf("permission %q normalises to %q, which no capability probes", written, key)
		}
	}
	// And the other direction: a probe nobody can declare is dead weight.
	for name := range caps {
		if key, ok := NormalisePermission(nameToPermission(name)); !ok || key != name {
			t.Errorf("capability %q has no @permissions spelling that normalises back to it", name)
		}
	}
}

// nameToPermission is the test's inverse of NormalisePermission: it is here,
// not in the package, because production code has no reason to go backwards.
func nameToPermission(name string) string {
	switch name {
	case "connect":
		return "CONNECT"
	case "view_server_state":
		return "VIEW SERVER STATE"
	case "view_any_definition":
		return "VIEW ANY DEFINITION"
	case "msdb_read":
		return "MSDB READ"
	}
	return name
}

// A capability with no impact statement is useless to the reader, and an
// impact that merely restates the permission name is the same failure wearing
// a sentence. Every probe must name what data is lost.
func TestCapabilityImpactNamesTheConsequence(t *testing.T) {
	for _, p := range Capabilities() {
		if len(p.Impact) < 12 {
			t.Errorf("capability %q impact %q is too terse to tell a DBA what is lost", p.Name, p.Impact)
		}
	}
}

// VIEW ANY DEFINITION is denied silently: metadata visibility drops the rows
// instead of raising, so a login holding an explicit DENY reads
// sys.configurations in full and a probe against it reports "ok". Measured on
// SQL Server 2022: all 97 rows returned under DENY VIEW ANY DEFINITION, while
// sys.master_files went from 15 rows to 0. The probe must therefore be one
// whose object is never legitimately empty, and it must count rows.
func TestViewAnyDefinitionProbeDetectsSilentDenial(t *testing.T) {
	for _, p := range Capabilities() {
		if p.Name != "view_any_definition" {
			continue
		}
		if !p.NeedsRows {
			t.Error("the view_any_definition probe must require rows: the denial " +
				"raises no error, it returns an empty result set")
		}
		if strings.Contains(p.SQL, "sys.configurations") {
			t.Error("sys.configurations is readable in full without VIEW ANY DEFINITION; " +
				"this probe cannot observe the denial")
		}
		return
	}
	t.Fatal("no view_any_definition capability")
}

// A probe that raises on denial must not also demand rows, or an instance
// that genuinely has no backup history would be reported as a permission
// problem.
func TestRaisingProbesDoNotRequireRows(t *testing.T) {
	for _, p := range Capabilities() {
		if p.Name == "msdb_read" && p.NeedsRows {
			t.Error("msdb_read must not require rows: a server with no backups yet " +
				"has an empty backupset, and that is not a denial")
		}
	}
}

func TestDeniedCapabilities(t *testing.T) {
	checks := []Check{
		{Name: "connect", Status: "ok"},
		{Name: "view_server_state", Status: "denied"},
		{Name: "msdb_read", Status: "error"},
		{Name: "view_any_definition", Status: "ok"},
	}
	got := DeniedCapabilities(checks)
	if len(got) != 2 || !got["view_server_state"] || !got["msdb_read"] {
		t.Errorf("DeniedCapabilities = %v, want view_server_state and msdb_read", got)
	}
}
