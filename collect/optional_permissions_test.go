package collect

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// optionalScript is a lint-clean collector whose header carries perms, the
// directive lines as written.
func optionalScript(perms string) Script {
	body := "-- @scope: instance\n-- @resultsets: a:object\n-- @timeout: 60\n" + perms +
		contractPreamble + "SELECT 1 AS [x] OPTION (RECOMPILE, MAXDOP 1);\n"
	got, err := Discover(fstest.MapFS{"queries/40.security/040.a.sql": {Data: []byte(body)}}, "queries")
	if err != nil {
		panic(err)
	}
	return got[0]
}

func TestDiscoverParsesOptionalPermissions(t *testing.T) {
	s := optionalScript("-- @permissions: CONNECT, VIEW ANY DEFINITION\n" +
		"-- @optional_permissions: view server security state,  MSDB   READ\n")
	if s.LintError != "" {
		t.Fatalf("unexpected lint error: %s", s.LintError)
	}
	if want := []string{"connect", "view_any_definition"}; !slices.Equal(s.Permissions, want) {
		t.Errorf("permissions = %v, want %v", s.Permissions, want)
	}
	if want := []string{"view_server_security_state", "msdb_read"}; !slices.Equal(s.OptionalPermissions, want) {
		t.Errorf("optional permissions = %v, want %v", s.OptionalPermissions, want)
	}
}

// The vocabulary is closed exactly as @permissions' is: a misspelt optional
// capability would otherwise be dropped, and the file would run without the
// grant script ever asking for it.
func TestOptionalPermissionsLint(t *testing.T) {
	for _, tc := range []struct{ name, perms, want string }{
		{"unknown", "-- @permissions: CONNECT\n-- @optional_permissions: VIEW SERVER SECURITY STAT\n",
			`@optional_permissions: unknown permission "VIEW SERVER SECURITY STAT"`},
		{"connect", "-- @permissions: VIEW ANY DEFINITION\n-- @optional_permissions: CONNECT\n",
			"@optional_permissions: CONNECT cannot be optional"},
		{"both, required first", "-- @permissions: CONNECT, MSDB READ\n-- @optional_permissions: MSDB READ\n",
			"@optional_permissions: MSDB READ is also in @permissions"},
		{"both, optional first", "-- @optional_permissions: MSDB READ\n-- @permissions: CONNECT, MSDB READ\n",
			"@optional_permissions: MSDB READ is also in @permissions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := optionalScript(tc.perms)
			if !strings.Contains(s.LintError, tc.want) {
				t.Errorf("lint = %q, want it to contain %q", s.LintError, tc.want)
			}
		})
	}
}

// A denied optional capability never skips. The script runs, and the plan
// carries the capability so the manifest can say the document is short of it.
// The controls are the two other ways a script with the same optional line can
// fare: skipped for a required capability, it is not reported as reduced; and
// with nothing denied, it is not reported at all.
func TestADeniedOptionalCapabilityDoesNotSkipAndIsRecorded(t *testing.T) {
	s := Script{Path: "40.security/040.encryption-certificates.sql",
		Permissions:         []string{"connect", "view_any_definition"},
		OptionalPermissions: []string{"view_server_security_state", "msdb_read"}}

	plan := planScripts([]Script{s}, "", map[string]bool{"view_server_security_state": true}, nil, nil)
	if plan[0].Skip != "" {
		t.Fatalf("skipped for an optional capability: %s", plan[0].Skip)
	}
	if !slices.Equal(plan[0].Without, []string{"view_server_security_state"}) {
		t.Errorf("without = %v, want the denied optional capability alone", plan[0].Without)
	}
	reduced := plannedReductions(plan)
	if len(reduced) != 1 || reduced[0].Script != s.Path || reduced[0].Capability != "view_server_security_state" {
		t.Fatalf("reduced = %+v", reduced)
	}
	if !strings.Contains(reduced[0].Reason, "sys.dm_database_encryption_keys") ||
		!strings.Contains(reduced[0].Reason, "@optional_permissions") {
		t.Errorf("reason does not name the capability and the directive: %q", reduced[0].Reason)
	}

	m := &Manifest{Reduced: reduced}
	human := m.Human()
	if !strings.Contains(human, "Queries run without an optional permission (1):\n  - "+s.Path) {
		t.Errorf("MANIFEST.txt does not list the reduced collector:\n%s", human)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"reduced_scripts":[{"script":"40.security/040.encryption-certificates.sql","capability":"view_server_security_state"`) {
		t.Errorf("_run.json does not carry reduced_scripts: %s", b)
	}

	skipped := planScripts([]Script{s}, "",
		map[string]bool{"view_any_definition": true, "view_server_security_state": true}, nil, nil)
	if skipped[0].Skip == "" || len(plannedReductions(skipped)) != 0 {
		t.Errorf("a script skipped for a required capability is reported reduced: %+v", skipped[0])
	}
	if got := plannedReductions(planScripts([]Script{s}, "", nil, nil, nil)); len(got) != 0 {
		t.Errorf("nothing denied, reduced = %+v", got)
	}
}

// Under a profile, a capability a member uses optionally is needed: calling
// its denial "not needed" would let the manifest say COMPLETE over a document
// the login could only partly fill, and drop the grant from the profile's
// script.
func TestProfileChecksCountsOptionalCapabilitiesAsNeeded(t *testing.T) {
	scripts := []Script{{Path: "40.security/040.a.sql", Profiles: []string{"space"},
		Permissions: []string{"connect"}, OptionalPermissions: []string{"view_server_security_state"}}}
	checks := []CapabilityCheck{{Name: "view_server_security_state", Status: "denied"}}
	if got := ProfileChecks(checks, scripts, "space")[0].Status; got != "denied" {
		t.Errorf("status = %q, want denied: a member uses it", got)
	}
}

// The grant script asks for an optional need like any other, names the
// collector under it, and says it is optional.
func TestGrantScriptIncludesOptionalNeeds(t *testing.T) {
	in := baseInput("view_server_security_state")
	in.Version = "17.0.4065.4"
	in.Scripts = append(in.Scripts, Script{
		Path:                "queries/40.security/040.encryption-certificates.sql",
		Permissions:         []string{"connect", "view_any_definition"},
		OptionalPermissions: []string{"view_server_security_state", "msdb_read"},
	})
	body, has := BuildGrantScript(in)
	if !has || !strings.Contains(statements(body), "GRANT VIEW SERVER SECURITY STATE TO [svc_audit];") {
		t.Fatalf("an optional need was not granted:\n%s", body)
	}
	if !strings.Contains(body, "--   queries/40.security/040.encryption-certificates.sql   (optional: runs without it") {
		t.Errorf("the collector is not listed as an optional user of the grant:\n%s", body)
	}
}

// check's query list says which capabilities a query runs without, by the
// keys the Permissions block prints.
func TestCheckNotesOptionalPermissions(t *testing.T) {
	s := Script{OptionalPermissions: []string{"view_server_security_state", "msdb_read"}}
	if got := scriptNote(s, nil); !strings.Contains(got, "runs without view_server_security_state, msdb_read if refused") {
		t.Errorf("note = %q", got)
	}
}
