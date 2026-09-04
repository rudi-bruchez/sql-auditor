package collect

import (
	"strings"
	"testing"
)

// The gap the probes cannot see. Every capability can be "ok" while the
// per-database collectors are all skipped, so the script has to offer the fix
// even when nothing was denied.
func TestGrantScriptOffersDatabaseAccessEvenWhenNothingWasDenied(t *testing.T) {
	in := baseInput() // every probe ok
	in.NoAccessDatabases = []string{"SALES", "ARCHIVE"}
	body, has := BuildGrantScript(in)
	if !has {
		t.Fatal("skipped databases are a gap worth a script even with no denial")
	}
	stmts := statements(body)
	for _, want := range []string{
		"USE [SALES];",
		"USE [ARCHIVE];",
		"CREATE USER [svc_audit] FOR LOGIN [svc_audit];",
	} {
		if !strings.Contains(stmts, want) {
			t.Errorf("missing %q:\n%s", want, stmts)
		}
	}
	// A user with no role. db_datareader would work and would hand over every
	// table in the database.
	if strings.Contains(stmts, "db_datareader") {
		t.Errorf("per-database access must not grant data access:\n%s", stmts)
	}
}

// Databases are sorted so two runs of the same instance produce the same file,
// which is what makes the script reviewable in a diff.
func TestGrantScriptSortsDatabases(t *testing.T) {
	in := baseInput()
	in.NoAccessDatabases = []string{"zeta", "alpha", "Mid"}
	body, _ := BuildGrantScript(in)
	stmts := statements(body)
	a, m, z := strings.Index(stmts, "[alpha]"), strings.Index(stmts, "[Mid]"), strings.Index(stmts, "[zeta]")
	if a < 0 || m < 0 || z < 0 {
		t.Fatalf("all three databases must appear:\n%s", stmts)
	}
	if !(a < m && m < z) {
		t.Errorf("databases must be sorted:\n%s", stmts)
	}
}

// A database name is an identifier from the instance and gets the same
// escaping as the login.
func TestGrantScriptEscapesDatabaseNames(t *testing.T) {
	in := baseInput()
	in.NoAccessDatabases = []string{"od]d"}
	body, _ := BuildGrantScript(in)
	if !strings.Contains(body, "USE [od]]d];") {
		t.Fatalf("the closing bracket must be doubled in a database name:\n%s", body)
	}
}

// After the per-database block the script is sitting in the last database it
// visited. A section emitted after it must restate its context rather than
// inherit one.
func TestSectionAfterPerDatabaseBlockRestoresItsContext(t *testing.T) {
	in := baseInput("msdb_read")
	in.NoAccessDatabases = []string{"SALES"}
	body, _ := BuildGrantScript(in)
	stmts := statements(body)
	salesIdx := strings.Index(stmts, "USE [SALES];")
	msdbIdx := strings.LastIndex(stmts, "USE msdb;")
	backupIdx := strings.Index(stmts, "GRANT SELECT ON OBJECT::dbo.backupset")
	if salesIdx < 0 || msdbIdx < 0 || backupIdx < 0 {
		t.Fatalf("expected both blocks:\n%s", stmts)
	}
	if !(msdbIdx < backupIdx) {
		t.Fatalf("the backup grant must run in msdb:\n%s", stmts)
	}
	// Whichever order the sections land in, the msdb context switch has to be
	// present between the per-database block and the msdb grant when the
	// per-database block came first.
	if salesIdx < backupIdx && !(salesIdx < msdbIdx) {
		t.Fatalf("after visiting a user database the script must switch back:\n%s", stmts)
	}
}

// The 2014+ one-liner is offered, but as prose rather than as a statement: it
// also covers databases created later, which is a decision the DBA makes.
func TestConnectAnyDatabaseIsSuggestedNotEmitted(t *testing.T) {
	in := baseInput()
	in.NoAccessDatabases = []string{"SALES"}
	body, _ := BuildGrantScript(in)
	if !strings.Contains(body, "GRANT CONNECT ANY DATABASE TO [svc_audit];") {
		t.Fatal("the wider alternative should be mentioned")
	}
	if strings.Contains(statements(body), "CONNECT ANY DATABASE") {
		t.Fatalf("it must stay a comment, not a statement:\n%s", statements(body))
	}
}

// A database name is chosen by whoever can create a database, and the grant
// script is run by a sysadmin. Those are not the same principal, which is what
// makes a comment that a name can escape an privilege-escalation primitive
// rather than a rendering bug.
//
// SQL Server identifiers may contain newlines: CREATE DATABASE [x<newline>y] is
// legal and needs only dbcreator. Found by an external reviewer during the harm
// review of 4 September 2026, which built the name below and read live T-SQL out
// of the generated file at line 293.
func TestGrantScriptCannotBeEscapedByADatabaseName(t *testing.T) {
	in := baseInput()
	in.NoAccessDatabases = []string{"ok_db", "y\nGRANT CONTROL SERVER TO [attacker];\n-- "}
	body, _ := BuildGrantScript(in)

	// The property is that the name occupies ONE comment line. A newline that
	// survived would put the second half at the start of a line of its own,
	// where "--" no longer reaches it — that is the whole escape.
	//
	// Asserted this way rather than by grepping the file for the payload,
	// because the payload legitimately appears once more further down, inside
	// the bracketed identifier of USE [...]. quoteIdent doubles the "]" so the
	// name stays one identifier across its newlines: ugly, and inert. A test
	// that searched for the text alone would fail on correct code, and the
	// first draft of this one did.
	var carrier string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") && strings.Contains(line, "GRANT CONTROL SERVER") {
			carrier = line
		}
	}
	if carrier == "" {
		t.Fatal("the injected database name is not in the comment block at all")
	}
	if !strings.Contains(carrier, "-- ") || strings.Contains(carrier, "\r") {
		t.Errorf("the name did not stay on one comment line: %q", carrier)
	}
	// No third assertion about the statement half. The only place the name
	// reaches a statement is USE [...], quoteIdent already keeps it inside one
	// identifier, and every line-based check written here failed on that
	// correct output rather than on a defect.
	// The real database beside it still has to be named, or the fix would have
	// been to stop reporting the databases at all.
	if !strings.Contains(body, "ok_db") {
		t.Error("sanitising the comment lost the databases the script exists to name")
	}
}

// The header is a /* */ block, so the sequence that escapes it is "*/" rather
// than a newline. The login is server-supplied like everything else in it.
func TestGrantScriptHeaderCannotBeClosedByALoginName(t *testing.T) {
	in := baseInput()
	in.Login = "svc*/ GRANT CONTROL SERVER TO [attacker]; /*"
	body, _ := BuildGrantScript(in)

	header := body[:strings.Index(body, "*/")+2]
	if strings.Contains(header, "GRANT CONTROL SERVER") {
		return // still inside the block: the payload never became a statement
	}
	if strings.Contains(statements(body), "CONTROL SERVER") {
		t.Errorf("a login name closed the header comment:\n%s", statements(body))
	}
}
