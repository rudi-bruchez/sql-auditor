package collect

import (
	"strings"
	"testing"
)

func checksWith(denied ...string) []CapabilityCheck {
	d := map[string]bool{}
	for _, n := range denied {
		d[n] = true
	}
	var out []CapabilityCheck
	for _, c := range Capabilities() {
		st := "ok"
		if d[c.Name] {
			st = "denied"
		}
		out = append(out, CapabilityCheck{Name: c.Name, Label: c.Label, Status: st, Impact: c.Impact})
	}
	return out
}

func baseInput(denied ...string) GrantScriptInput {
	return GrantScriptInput{
		Login: "svc_audit", Instance: "SRV\\INST", Version: "14.0.1000.169",
		Edition: "Standard Edition (64-bit)", Checks: checksWith(denied...),
		Scripts: []Script{
			{Path: "queries/10.system/010.properties.sql", Permissions: []string{"connect", "view_any_definition"}},
			{Path: "queries/80.workload/010.wait-stats.sql", Permissions: []string{"connect", "view_server_state"}},
			{Path: "queries/50.agent/010.jobs.sql", Permissions: []string{"connect", "agent_jobs"}},
			{Path: "queries/20.databases/010.all-databases.sql", Permissions: []string{"connect", "msdb_read"}},
		},
		Tool: "1.0.0",
	}
}

// statements returns only the executable lines: the /* */ header and every
// -- comment removed.
//
// The tests need this because the file is mostly prose, and prose about
// permissions necessarily contains the words GRANT, REVOKE and db_datareader.
// A first version of these tests asserted on the whole body and failed on the
// sentence "Replace GRANT with REVOKE" in the header — which would have taught
// me to write a less explanatory script rather than a more correct one.
func statements(body string) string {
	// Every /* */ block, not just the first: the "nothing to grant" file is
	// itself one, and cutting only the header would leave it behind and call
	// it a statement.
	for {
		open := strings.Index(body, "/*")
		if open < 0 {
			break
		}
		close := strings.Index(body[open:], "*/")
		if close < 0 {
			body = body[:open]
			break
		}
		body = body[:open] + body[open+close+2:]
	}
	var out []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, "\n")
}

// Nothing denied must still produce a file, and that file must say so. A tool
// that writes nothing leaves the operator unable to tell "all good" from
// "the write failed".
func TestGrantScriptSaysSoWhenThereIsNothingToGrant(t *testing.T) {
	body, has := BuildGrantScript(baseInput())
	if has {
		t.Fatal("expected no statements when every capability is ok")
	}
	if got := statements(body); got != "" {
		t.Fatalf("a script with nothing denied must contain no statement, got:\n%s", got)
	}
	if !strings.Contains(body, "Nothing to grant") {
		t.Fatalf("the file must say there is nothing to do:\n%s", body)
	}
}

// The whole point of the feature: only what was refused.
func TestGrantScriptGrantsOnlyWhatWasDenied(t *testing.T) {
	body, has := BuildGrantScript(baseInput("view_any_definition"))
	if !has {
		t.Fatal("expected statements")
	}
	stmts := statements(body)
	if !strings.Contains(stmts, "GRANT VIEW ANY DEFINITION TO [svc_audit];") {
		t.Fatalf("missing the grant that was denied:\n%s", stmts)
	}
	for _, unwanted := range []string{"VIEW SERVER STATE", "SQLAgentReaderRole", "backupset", "USE msdb"} {
		if strings.Contains(stmts, unwanted) {
			t.Errorf("granted %q, which was not denied:\n%s", unwanted, stmts)
		}
	}
}

// Least privilege is version-dependent, and getting this backwards would ask a
// 2022 instance for more than it needs.
func TestGrantScriptUsesTheNarrowerPermissionOn2022(t *testing.T) {
	in := baseInput("view_server_state")
	in.Version = "16.0.4035.4"
	body, _ := BuildGrantScript(in)
	stmts := statements(body)
	if !strings.Contains(stmts, "GRANT VIEW SERVER PERFORMANCE STATE TO [svc_audit];") {
		t.Fatalf("2022 must get the narrower permission:\n%s", stmts)
	}
	if strings.Contains(stmts, "GRANT VIEW SERVER STATE TO") {
		t.Fatalf("2022 must not get the wide permission:\n%s", stmts)
	}

	in.Version = "15.0.4000.1"
	body, _ = BuildGrantScript(in)
	stmts = statements(body)
	if !strings.Contains(stmts, "GRANT VIEW SERVER STATE TO [svc_audit];") {
		t.Fatalf("2019 must get VIEW SERVER STATE, the only one it has:\n%s", stmts)
	}
	if strings.Contains(stmts, "PERFORMANCE STATE") {
		t.Fatalf("2019 has no VIEW SERVER PERFORMANCE STATE:\n%s", stmts)
	}
}

// An unreadable version must not silently pick the narrow 2022 permission,
// which the instance may not have: the statement would fail with an error the
// DBA cannot act on.
func TestGrantScriptFallsBackToTheWidePermissionWhenVersionIsUnknown(t *testing.T) {
	in := baseInput("view_server_state")
	in.Version = ""
	body, _ := BuildGrantScript(in)
	if !strings.Contains(body, "GRANT VIEW SERVER STATE TO [svc_audit];") {
		t.Fatalf("unknown version must use the permission that exists everywhere:\n%s", body)
	}
	if !strings.Contains(body, "product version could not be read") {
		t.Fatalf("the fallback must be disclosed in the file:\n%s", body)
	}
}

// Below 2022 the error log rides on VIEW SERVER STATE. Emitting a separate
// grant for it would name a permission that does not exist on that version.
func TestErrorLogNeedsNoSeparateGrantBefore2022(t *testing.T) {
	body, _ := BuildGrantScript(baseInput("error_log"))
	if strings.Contains(body, "VIEW ANY ERROR LOG") {
		t.Fatalf("VIEW ANY ERROR LOG does not exist before 2022:\n%s", body)
	}
	if !strings.Contains(body, "GRANT VIEW SERVER STATE TO [svc_audit];") {
		t.Fatalf("the error log needs VIEW SERVER STATE on this version:\n%s", body)
	}

	in := baseInput("error_log")
	in.Version = "16.0.1000.6"
	body, _ = BuildGrantScript(in)
	if !strings.Contains(body, "GRANT VIEW ANY ERROR LOG TO [svc_audit];") {
		t.Fatalf("2022 has a dedicated, narrower permission:\n%s", body)
	}
}

// msdb permissions are database-scoped, so they need a user and a context
// switch. Granting in the wrong database is the classic way this fails.
func TestMsdbGrantsRunInMsdbAndCreateTheUserFirst(t *testing.T) {
	body, _ := BuildGrantScript(baseInput("msdb_read", "agent_jobs"))
	useIdx := strings.Index(body, "USE msdb;")
	if useIdx < 0 {
		t.Fatal("msdb grants must switch database")
	}
	createIdx := strings.Index(body, "CREATE USER [svc_audit] FOR LOGIN [svc_audit];")
	grantIdx := strings.Index(body, "GRANT SELECT ON OBJECT::dbo.backupset TO [svc_audit];")
	roleIdx := strings.Index(body, "ALTER ROLE SQLAgentReaderRole ADD MEMBER [svc_audit];")
	if createIdx < 0 || grantIdx < 0 || roleIdx < 0 {
		t.Fatalf("missing one of the msdb statements:\n%s", body)
	}
	if !(useIdx < createIdx && createIdx < grantIdx && createIdx < roleIdx) {
		t.Fatalf("USE msdb, then CREATE USER, then the grants:\n%s", body)
	}
	if !strings.Contains(body, "IF NOT EXISTS (SELECT 1 FROM sys.database_principals WHERE name = 'svc_audit')") {
		t.Fatalf("the user must be created only if absent, so the script can be run twice:\n%s", body)
	}
	// db_datareader on msdb would also make the collector work, and would hand
	// out Database Mail contents and job step commands with it. The word is
	// allowed to appear in the prose that explains why it was not used; what
	// must not appear is a statement adding the login to it.
	for _, stmt := range []string{
		"ALTER ROLE db_datareader ADD MEMBER",
		"sp_addrolemember 'db_datareader'",
		"sp_addrolemember N'db_datareader'",
	} {
		if strings.Contains(body, stmt) {
			t.Errorf("db_datareader is not least privilege on msdb:\n%s", body)
		}
	}
}

// SQLAgentReaderRole implies SQLAgentUserRole, which can run jobs through
// proxies. A script that hands that out without saying so is not honest.
func TestAgentRoleCarriesItsCaveat(t *testing.T) {
	body, _ := BuildGrantScript(baseInput("agent_jobs"))
	if !strings.Contains(body, "WEIGH THIS ONE") {
		t.Fatalf("the Agent role must be flagged for a deliberate decision:\n%s", body)
	}
	if !strings.Contains(body, "SQLAgentUserRole") {
		t.Fatalf("the caveat must name the implied role:\n%s", body)
	}
}

// The login is an arbitrary string from elsewhere. A "]" in it would close the
// identifier early and turn the remainder into statement text.
func TestGrantScriptEscapesTheLoginName(t *testing.T) {
	in := baseInput("view_any_definition", "msdb_read")
	in.Login = "ev]il; DROP DATABASE x--"
	body, _ := BuildGrantScript(in)
	if !strings.Contains(body, "[ev]]il; DROP DATABASE x--]") {
		t.Fatalf("the closing bracket must be doubled:\n%s", body)
	}
	if strings.Contains(body, "[ev]il;") {
		t.Fatalf("the identifier was not escaped:\n%s", body)
	}
	// The same string reaches the IF NOT EXISTS as a literal, where the quote
	// is the character that has to be doubled.
	in.Login = "o'brien"
	body, _ = BuildGrantScript(in)
	if !strings.Contains(body, "WHERE name = 'o''brien'") {
		t.Fatalf("the literal must double the quote:\n%s", body)
	}
	if !strings.Contains(body, "[o'brien]") {
		t.Fatalf("a quote needs no escaping inside an identifier:\n%s", body)
	}
}

// A probe that never answered is not a refusal, and granting on the strength
// of it would be guessing.
func TestErroredCapabilitiesGrantNothing(t *testing.T) {
	in := baseInput()
	for i := range in.Checks {
		if in.Checks[i].Name == "view_server_state" {
			in.Checks[i].Status = "error"
		}
	}
	body, has := BuildGrantScript(in)
	if has {
		t.Fatalf("an unreachable probe must not produce a grant:\n%s", body)
	}
	if !strings.Contains(body, "unknown") {
		t.Fatalf("the file must report the capability as unknown:\n%s", body)
	}
}

// The header is the part a DBA reads before running anything.
func TestGrantScriptHeaderNamesTheInstanceAndTheLogin(t *testing.T) {
	body, _ := BuildGrantScript(baseInput("view_any_definition"))
	for _, want := range []string{"SRV\\INST", "14.0.1000.169", "svc_audit", "sql-auditor check"} {
		if !strings.Contains(body, want) {
			t.Errorf("the header must carry %q:\n%s", want, body)
		}
	}
}

// Each section says which collectors the grant unlocks, and that list comes
// from the corpus rather than a hard-coded table, so it cannot drift.
func TestGrantScriptNamesTheCollectorsFromTheCorpus(t *testing.T) {
	body, _ := BuildGrantScript(baseInput("agent_jobs"))
	if !strings.Contains(body, "queries/50.agent/010.jobs.sql") {
		t.Fatalf("the section must name the collector that declared the permission:\n%s", body)
	}
	if strings.Contains(body, "queries/80.workload/010.wait-stats.sql") {
		t.Fatalf("it must name only the collectors for THIS capability:\n%s", body)
	}
}

// A script that failed lint never runs, so promising it would be a lie.
func TestGrantScriptIgnoresScriptsThatFailedLint(t *testing.T) {
	in := baseInput("agent_jobs")
	in.Scripts = append(in.Scripts, Script{
		Path: "queries/50.agent/999.broken.sql", Permissions: []string{"agent_jobs"},
		LintError: "missing @resultsets",
	})
	body, _ := BuildGrantScript(in)
	if strings.Contains(body, "999.broken.sql") {
		t.Fatalf("a script that cannot run must not be listed as unlocked:\n%s", body)
	}
}

// Every capability the preflight probes must have a grant. A capability added
// to Capabilities() without a branch here would be reported as missing by the
// check and then be absent from the file that is supposed to fix it.
func TestEveryProbedCapabilityCanBeGranted(t *testing.T) {
	for _, c := range Capabilities() {
		if c.Name == "connect" {
			// Denied connect means the instance was never reached, and the
			// script is not written at all.
			continue
		}
		in := baseInput(c.Name)
		body, has := BuildGrantScript(in)
		if !has {
			t.Errorf("capability %q is probed but nothing grants it", c.Name)
			continue
		}
		if !strings.Contains(body, "GRANT ") && !strings.Contains(body, "ALTER ROLE ") {
			t.Errorf("capability %q produced no grant statement:\n%s", c.Name, body)
		}
		// And that every grant names THE PROBED LOGIN. Checking only that the
		// word GRANT appears is what let a section ship with a hardcoded
		// "sqlauditor" principal: the script ran green, and on a client instance
		// that happens to have a login of that name the rights would have landed
		// on it instead — silently. Every line that grants something must name
		// the login baseInput says the server reported.
		// Statement lines only: the prose around them says things like "HOW TO
		// UNDO IT", and matching on " TO " alone would flag the commentary.
		for _, line := range strings.Split(body, "\n") {
			stmt := strings.TrimSpace(line)
			if !strings.HasPrefix(stmt, "GRANT ") && !strings.HasPrefix(stmt, "ALTER ROLE ") {
				continue
			}
			if !strings.Contains(line, quoteIdent(in.Login)) {
				t.Errorf("capability %q grants to a principal that is not the probed login:\n  %s",
					c.Name, stmt)
			}
		}
	}
}

func TestMajorVersion(t *testing.T) {
	for in, want := range map[string]int{
		"14.0.1000.169": 14, "16.0.4035.4": 16, "9.00.5000.00": 9,
		"": 0, "notaversion": 0, " 15.0.2000.5": 15,
	} {
		if got := majorVersion(in); got != want {
			t.Errorf("majorVersion(%q) = %d, want %d", in, got, want)
		}
	}
}
