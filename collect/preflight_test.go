package collect

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	mssql "github.com/microsoft/go-mssqldb"
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
		// A server can go away AFTER the connect probe answers. Keying on
		// connect alone would report "usable, possibly degraded" for an
		// instance that is no longer there.
		{
			"server disappeared after connecting",
			[]Check{
				{Name: "connect", Status: "ok"},
				{Name: "view_any_definition", Status: "error"},
				{Name: "view_server_state", Status: "error"},
			},
			0, true, 1,
		},
		// The widened check must not swallow a denial. "error" is assigned
		// only to a transport failure, never to a refusal, so a run that is
		// merely degraded still exits 0.
		{
			"every capability denied is still exit 0",
			[]Check{
				{Name: "connect", Status: "ok"},
				{Name: "view_any_definition", Status: "denied"},
				{Name: "view_server_state", Status: "denied"},
				{Name: "msdb_read", Status: "denied"},
			},
			0, true, 0,
		},
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

// --- a fake driver, so the classification logic is exercised in CI ---
//
// RunPreflight's whole job is deciding what a failure means: an mssql.Error is
// the server refusing, anything else is the server not answering, and for a
// NeedsRows probe an empty result set is itself the refusal. That decision was
// verified against a live instance, but a live instance is not available to
// CI, so a regression that dropped the NeedsRows branch or widened the
// errors.As check would otherwise ship green.

type fakeAnswer struct {
	rows int
	// err fails the query outright, at QueryContext.
	err error
	// rowsErr fails the query partway through the result set, after rows have
	// already been delivered. SQL Server does this: a batch can begin
	// streaming and then raise, so an error is not only something that happens
	// instead of a result set. probeCapability exists to catch these — it
	// drains the rows and returns rows.Err() — and without this field the
	// fake's Next could only ever return io.EOF, leaving that behaviour
	// documented but unexercised.
	rowsErr error
}

type fakeDriver struct {
	answers map[string]fakeAnswer
	asked   []string
}

func (d *fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{d: d}, nil }

type fakeConn struct{ d *fakeDriver }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("fake driver: Prepare is not used; probes go through QueryContext")
}
func (c *fakeConn) Close() error              { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) { return nil, fmt.Errorf("fake driver: no transactions") }

func (c *fakeConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.d.asked = append(c.d.asked, query)
	a, ok := c.d.answers[query]
	if !ok {
		return nil, fmt.Errorf("fake driver: no answer configured for %q", query)
	}
	if a.err != nil {
		return nil, a.err
	}
	return &fakeRows{left: a.rows, err: a.rowsErr}, nil
}

type fakeRows struct {
	left int
	err  error
}

func (r *fakeRows) Columns() []string { return []string{"c"} }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.left <= 0 {
		// The configured failure arrives after the rows, which is how a
		// mid-stream error reaches a client: some data, then the error.
		if r.err != nil {
			return r.err
		}
		return io.EOF
	}
	r.left--
	dest[0] = int64(1)
	return nil
}

// fakeSeq names each registered driver uniquely. database/sql has no way to
// unregister, and registering the same name twice panics, so a name derived
// from the test would blow up under -count=2 — precisely the invocation used
// to hunt flakes.
var fakeSeq atomic.Int64

// openFake registers a driver under a name unique to this call and hands back
// a single connection, matching how RunPreflight is really called.
func openFake(t *testing.T, answers map[string]fakeAnswer) (*sql.Conn, *fakeDriver) {
	t.Helper()
	d := &fakeDriver{answers: answers}
	name := fmt.Sprintf("preflight-fake-%d", fakeSeq.Add(1))
	sql.Register(name, d)
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, d
}

func statusOf(checks []Check, name string) string {
	for _, c := range checks {
		if c.Name == name {
			return c.Status
		}
	}
	return "<missing>"
}

// The four outcomes, one probe each.
func TestRunPreflightClassifiesEachOutcome(t *testing.T) {
	caps := []Capability{
		{Name: "connect", SQL: "q-ok", Impact: "nothing can run"},
		{Name: "view_server_state", SQL: "q-refused", Impact: "no waits"},
		{Name: "view_any_definition", SQL: "q-empty", Impact: "no file layout", NeedsRows: true},
		{Name: "msdb_read", SQL: "q-rows", Impact: "no backup history"},
	}
	c, _ := openFake(t, map[string]fakeAnswer{
		"q-ok":    {rows: 1},
		"q-empty": {rows: 0},
		"q-rows":  {rows: 1},
		// A refusal: the server was reached, understood the query and declined.
		"q-refused": {err: mssql.Error{Number: 297,
			Message: "The user does not have permission to perform this action."}},
	})
	got := RunPreflight(context.Background(), c, caps)
	if len(got) != len(caps) {
		t.Fatalf("got %d checks, want %d", len(got), len(caps))
	}
	want := map[string]string{
		"connect":             "ok",
		"view_server_state":   "denied", // an mssql.Error is a refusal
		"view_any_definition": "denied", // zero rows from a NeedsRows probe
		"msdb_read":           "ok",
	}
	for name, status := range want {
		if got := statusOf(got, name); got != status {
			t.Errorf("%s = %q, want %q", name, got, status)
		}
	}
	// A denied check must carry its impact, and an ok one must not.
	for _, ch := range got {
		if ch.Status == "ok" && ch.Impact != "" {
			t.Errorf("%s is ok but carries impact %q", ch.Name, ch.Impact)
		}
		if ch.Status != "ok" && ch.Impact == "" {
			t.Errorf("%s is %q but states no impact", ch.Name, ch.Status)
		}
	}
}

// A NeedsRows probe that returns rows is not denied — otherwise the rule would
// simply always fire and the capability could never be reported available.
func TestRunPreflightNeedsRowsSatisfiedByRows(t *testing.T) {
	caps := []Capability{{Name: "view_any_definition", SQL: "q", Impact: "no file layout", NeedsRows: true}}
	c, _ := openFake(t, map[string]fakeAnswer{"q": {rows: 3}})
	if got := statusOf(RunPreflight(context.Background(), c, caps), "view_any_definition"); got != "ok" {
		t.Errorf("got %q, want ok", got)
	}
}

// Zero rows from a probe that does NOT set NeedsRows is a real answer, not a
// denial: a server with no backups yet has an empty backupset.
func TestRunPreflightEmptyResultIsNotDeniedWithoutNeedsRows(t *testing.T) {
	caps := []Capability{{Name: "msdb_read", SQL: "q", Impact: "no backup history"}}
	c, _ := openFake(t, map[string]fakeAnswer{"q": {rows: 0}})
	if got := statusOf(RunPreflight(context.Background(), c, caps), "msdb_read"); got != "ok" {
		t.Errorf("got %q, want ok", got)
	}
}

// A failure that is not an mssql.Error means no answer arrived. It must be
// "error", not "denied" — reporting it as denied would send a DBA hunting for
// a GRANT that was never the problem — and the remaining probes must be
// reported without asking, since each would only pay a timeout to repeat it.
func TestRunPreflightTransportFailureErrorsAndShortCircuits(t *testing.T) {
	caps := []Capability{
		{Name: "connect", SQL: "q-ok", Impact: "nothing can run"},
		{Name: "view_server_state", SQL: "q-gone", Impact: "no waits"},
		{Name: "view_any_definition", SQL: "q-never", Impact: "no file layout", NeedsRows: true},
		{Name: "msdb_read", SQL: "q-never-either", Impact: "no backup history"},
	}
	c, d := openFake(t, map[string]fakeAnswer{
		"q-ok":   {rows: 1},
		"q-gone": {err: io.ErrUnexpectedEOF},
		// q-never and q-never-either are deliberately absent: if the
		// short-circuit fails, the fake driver errors on the unknown query and
		// the asked-queries assertion below catches it.
	})
	got := RunPreflight(context.Background(), c, caps)
	for _, name := range []string{"view_server_state", "view_any_definition", "msdb_read"} {
		if s := statusOf(got, name); s != "error" {
			t.Errorf("%s = %q, want error", name, s)
		}
	}
	if s := statusOf(got, "connect"); s != "ok" {
		t.Errorf("connect = %q, want ok", s)
	}
	// This assertion is load-bearing and must not be removed as redundant. It
	// is the ONLY thing that catches the short-circuit being dropped: if the
	// later probes did run, the fake has no answer configured for them and
	// returns an error that is also not an mssql.Error, so they still classify
	// as "error" and every status assertion above still passes.
	if len(d.asked) != 2 {
		t.Errorf("driver was asked %v; the probes after the failure should not have run", d.asked)
	}
	// And this is the state that must not exit 0.
	if code := PreflightExitCode(got, 0, true); code != 1 {
		t.Errorf("exit code = %d, want 1: the instance went away mid-preflight", code)
	}
}

// A refusal on the connect probe is still "error": if SELECT 1 cannot run,
// nothing can, whatever the server called the failure.
func TestRunPreflightConnectRefusalIsError(t *testing.T) {
	caps := []Capability{{Name: "connect", SQL: "q", Impact: "nothing can run"}}
	c, _ := openFake(t, map[string]fakeAnswer{
		"q": {err: mssql.Error{Number: 297, Message: "denied"}},
	})
	got := RunPreflight(context.Background(), c, caps)
	if s := statusOf(got, "connect"); s != "error" {
		t.Errorf("connect = %q, want error", s)
	}
	if code := PreflightExitCode(got, 0, true); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

// An error can arrive after rows have already been delivered: SQL Server can
// begin streaming a batch and then raise. probeCapability drains the result
// set precisely to catch that, and a probe that stopped at QueryContext would
// call such a failure a success.
func TestRunPreflightErrorMidResultSet(t *testing.T) {
	tests := []struct {
		name    string
		rowsErr error
		want    string
	}{
		// The server got partway through, then refused. Still a refusal.
		{"refusal mid-stream", mssql.Error{Number: 297, Message: "denied"}, "denied"},
		// The connection died mid-stream. Not a refusal.
		{"transport failure mid-stream", io.ErrUnexpectedEOF, "error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caps := []Capability{{Name: "view_server_state", SQL: "q", Impact: "no waits"}}
			c, _ := openFake(t, map[string]fakeAnswer{
				"q": {rows: 2, rowsErr: tc.rowsErr},
			})
			got := RunPreflight(context.Background(), c, caps)
			if s := statusOf(got, "view_server_state"); s != tc.want {
				t.Errorf("got %q, want %q — the error arrived after the rows and was dropped", s, tc.want)
			}
			if got[0].Impact == "" {
				t.Error("a failed check must state its impact")
			}
		})
	}
}

// A NeedsRows probe that fails partway must be classified from the error, not
// from the row count it happened to reach. Rows arrived, so a count-only rule
// would call this ok.
func TestRunPreflightNeedsRowsErrorMidResultSetWins(t *testing.T) {
	caps := []Capability{{Name: "view_any_definition", SQL: "q", Impact: "no file layout", NeedsRows: true}}
	c, _ := openFake(t, map[string]fakeAnswer{
		"q": {rows: 5, rowsErr: mssql.Error{Number: 297, Message: "denied"}},
	})
	if s := statusOf(RunPreflight(context.Background(), c, caps), "view_any_definition"); s != "denied" {
		t.Errorf("got %q, want denied", s)
	}
}
