package collect

import (
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSelectTargetsWildcards(t *testing.T) {
	cands := []DatabaseInfo{
		{Name: "AppProd", State: "ONLINE", HasAccess: true},
		{Name: "AppTest", State: "ONLINE", HasAccess: true},
		{Name: "Restoring", State: "RESTORING", HasAccess: true},
		{Name: "Snap", State: "ONLINE", HasAccess: true, IsSnapshot: true},
		{Name: "NoRights", State: "ONLINE", HasAccess: false},
	}
	got, err := SelectTargets(cands, "App*", "*Test")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Included) != 1 || got.Included[0] != "AppProd" {
		t.Fatalf("included = %v, want [AppProd]", got.Included)
	}
	reasons := map[string]string{}
	for _, s := range got.Skipped {
		reasons[s.Name] = s.Reason
	}
	for _, name := range []string{"AppTest", "Restoring", "Snap", "NoRights"} {
		if reasons[name] == "" {
			t.Errorf("%s skipped without a recorded reason", name)
		}
	}
	if !strings.Contains(reasons["Restoring"], "RESTORING") {
		t.Errorf("state reason = %q", reasons["Restoring"])
	}
}

func TestSelectTargetsEmptyIncludeMeansAll(t *testing.T) {
	got, err := SelectTargets([]DatabaseInfo{{Name: "X", State: "ONLINE", HasAccess: true}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Included) != 1 {
		t.Errorf("included = %v, want [X]", got.Included)
	}
}

// A malformed pattern must be reported, not treated as a pattern that matches
// nothing: silently collecting a database the user excluded is the bad outcome.
func TestSelectTargetsRejectsMalformedPattern(t *testing.T) {
	cands := []DatabaseInfo{{Name: "X", State: "ONLINE", HasAccess: true}}
	for _, tc := range []struct{ include, exclude string }{
		{"[Ab", ""},
		{"", "[Ab"},
	} {
		if _, err := SelectTargets(cands, tc.include, tc.exclude); err == nil {
			t.Errorf("SelectTargets(include=%q, exclude=%q) = nil error, want a syntax error",
				tc.include, tc.exclude)
		}
	}
}

// The driver's URL parser accepts only host:port. Every other canonical way of
// writing a SQL Server address has to be translated before it reaches the URL,
// and the named-instance forms fail at parse time, before any socket is opened.
func TestParseServerForms(t *testing.T) {
	tests := []struct {
		in       string
		hostport string
		instance string
	}{
		{"localhost", "localhost", ""},
		{"localhost:11433", "localhost:11433", ""},
		{"localhost,11433", "localhost:11433", ""},
		{`HOST\SQLEXPRESS`, "HOST", "SQLEXPRESS"},
		{`HOST\SQLEXPRESS,1433`, "HOST:1433", "SQLEXPRESS"},
		{`HOST\SQLEXPRESS:1433`, "HOST:1433", "SQLEXPRESS"},
		{"tcp:localhost,11433", "localhost:11433", ""},
		{`tcp:HOST\SQLEXPRESS,1433`, "HOST:1433", "SQLEXPRESS"},
		{"  localhost , 11433 ", "localhost:11433", ""},
		// An IPv6 literal is bracketed and full of colons; the port separator is
		// only what follows the closing bracket.
		{"[::1]", "[::1]", ""},
		{"[::1],1433", "[::1]:1433", ""},
		{"[::1]:1433", "[::1]:1433", ""},
		{`[fe80::1]\SQLEXPRESS,1433`, "[fe80::1]:1433", "SQLEXPRESS"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			hostport, instance, err := parseServer(tc.in)
			if err != nil {
				t.Fatalf("parseServer(%q) error: %v", tc.in, err)
			}
			if hostport != tc.hostport || instance != tc.instance {
				t.Errorf("parseServer(%q) = (%q, %q), want (%q, %q)",
					tc.in, hostport, instance, tc.hostport, tc.instance)
			}
			// The values must survive into the URL the driver actually parses.
			cfg := &Config{Server: tc.in, Database: "master", AppName: "t"}
			db, err := Open(cfg)
			if err != nil {
				t.Fatalf("Open(%q) error: %v", tc.in, err)
			}
			defer db.Close()
		})
	}
}

func TestParseServerURLShape(t *testing.T) {
	hostport, instance, err := parseServer(`HOST\SQLEXPRESS,1433`)
	if err != nil {
		t.Fatal(err)
	}
	u := &url.URL{Scheme: "sqlserver", Host: hostport}
	if instance != "" {
		u.Path = "/" + instance
	}
	// The instance belongs in the path, which is where the sqlserver:// scheme
	// looks for it.
	if got, want := u.String(), "sqlserver://HOST:1433/SQLEXPRESS"; got != want {
		t.Errorf("URL = %q, want %q", got, want)
	}
}

// ConnectTimeout must reach the driver as "dial timeout" and never as
// "connection timeout". The driver hands "connection timeout" to a wrapper
// that re-arms a socket read deadline for the life of the session, so setting
// it caps every query at ConnectTimeout and silently overrides the @timeout
// each collector declares — a 300 s collector dies at 15 s, on whichever
// databases happen to be slow that day. This test is the regression net: the
// symptom is invisible until a query runs long on a real instance.
func TestConnectTimeoutIsDialOnly(t *testing.T) {
	u, err := connURL(&Config{
		Server: "localhost", Database: "master", AppName: "t",
		ConnectTimeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if got := q.Get("dial timeout"); got != "15" {
		t.Errorf("dial timeout = %q, want %q", got, "15")
	}
	if _, ok := q["connection timeout"]; ok {
		t.Error(`"connection timeout" is set; it caps every query at ConnectTimeout ` +
			`and defeats the @timeout collectors declare`)
	}
}

func TestParseServerRejectsGarbage(t *testing.T) {
	for _, in := range []string{
		"",
		"   ",
		`HOST\`,
		"localhost,notaport",
		"[::1",
		"::1,1433", // an unbracketed IPv6 literal, not a host:port
		`\SQLEXPRESS`,
	} {
		if hostport, instance, err := parseServer(in); err == nil {
			t.Errorf("parseServer(%q) = (%q, %q), nil error; want an error", in, hostport, instance)
		}
	}
}

func TestQuoteNameEscapesBracket(t *testing.T) {
	if got := quoteName("we[i]rd"); got != "[we[i]]rd]" {
		t.Errorf("quoteName = %q", got)
	}
}

// An address the parser refuses is a typo, not an unreachable server: nothing
// has been dialled at the point it is refused. Exit 1 is documented as "the
// instance could not be reached" and would send a DBA to check a machine that
// was never contacted, so Run and Check ask this before choosing a code.
func TestBadServerAddressIsMarkedAsConfiguration(t *testing.T) {
	for _, addr := range []string{"", "HOST\\", "::1,1433", "localhost,not-a-port"} {
		_, err := Open(&Config{Server: addr, Database: "master"})
		if err == nil {
			t.Errorf("Open(%q): want an error", addr)
			continue
		}
		if !IsBadServerAddress(err) {
			t.Errorf("Open(%q): %v is not reported as a bad address", addr, err)
		}
		if openExitCode(err) != 2 {
			t.Errorf("Open(%q): exit code = %d, want 2", addr, openExitCode(err))
		}
	}
	// A well-formed address that nothing answers on is the other case, and it
	// must keep exit 1. Open does not dial, so there is no error to grade here
	// — the grading only has to not claim a typo.
	if IsBadServerAddress(nil) {
		t.Error("a nil error must not be reported as a bad address")
	}
}

// The second pass keeps a distribution database a narrowed run would lose.
// "Retained after filtering" is the exact trigger, and these five cases pin
// each half of it.

func TestSelectTargetsWidensToDistributor(t *testing.T) {
	cands := []DatabaseInfo{
		{Name: "SALESDB", State: "ONLINE", HasAccess: true, IsPublished: true},
		{Name: "DISTDB", State: "ONLINE", HasAccess: true, IsDistributor: true},
	}
	sel, err := SelectTargets(cands, "SALESDB", "")
	if err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	if !slices.Contains(sel.Included, "DISTDB") {
		t.Errorf("DISTDB should be retained; Included = %v", sel.Included)
	}
	if sel.Widened["DISTDB"].Reason == "" {
		t.Errorf("DISTDB should carry a retention reason; Widened = %v", sel.Widened)
	}
	// The superseded skip must be gone, or the manifest lists the database
	// twice with contradictory reasons.
	for _, s := range sel.Skipped {
		if s.Name == "DISTDB" {
			t.Errorf("DISTDB is both included and skipped: %q", s.Reason)
		}
	}
}

func TestSelectTargetsDoesNotWidenWithoutARetainedPublisher(t *testing.T) {
	cands := []DatabaseInfo{
		{Name: "SALESDB", State: "ONLINE", HasAccess: true, IsPublished: true},
		{Name: "DISTDB", State: "ONLINE", HasAccess: true, IsDistributor: true},
	}
	sel, err := SelectTargets(cands, "OTHERDB", "SALESDB")
	if err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	if slices.Contains(sel.Included, "DISTDB") {
		t.Errorf("no retained publisher, so DISTDB must not be widened in")
	}
}

func TestSelectTargetsExcludeBeatsWidening(t *testing.T) {
	cands := []DatabaseInfo{
		{Name: "SALESDB", State: "ONLINE", HasAccess: true, IsPublished: true},
		{Name: "DISTDB", State: "ONLINE", HasAccess: true, IsDistributor: true},
	}
	sel, err := SelectTargets(cands, "SALESDB", "DISTDB")
	if err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	if slices.Contains(sel.Included, "DISTDB") {
		t.Errorf("DB_EXCLUDE must win over widening")
	}
}

func TestSelectTargetsDoesNotWidenAnInaccessibleDistributor(t *testing.T) {
	cands := []DatabaseInfo{
		{Name: "SALESDB", State: "ONLINE", HasAccess: true, IsPublished: true},
		{Name: "DISTDB", State: "ONLINE", HasAccess: false, IsDistributor: true},
	}
	sel, err := SelectTargets(cands, "SALESDB", "")
	if err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	if slices.Contains(sel.Included, "DISTDB") {
		t.Errorf("an inaccessible distributor must stay skipped")
	}
}

// A stale is_published flag on a restored database widens the run. The spec
// accepts this and says so; the test exists so the behaviour is deliberate
// rather than discovered by the first operator it surprises.
func TestSelectTargetsWidensOnAStaleFlag(t *testing.T) {
	cands := []DatabaseInfo{
		{Name: "RESTOREDDB", State: "ONLINE", HasAccess: true, IsPublished: true},
		{Name: "DISTDB", State: "ONLINE", HasAccess: true, IsDistributor: true},
	}
	sel, err := SelectTargets(cands, "RESTOREDDB", "")
	if err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	if !slices.Contains(sel.Included, "DISTDB") {
		t.Errorf("a stale flag widens; the spec accepts this")
	}
}

// The purpose and the reason are two different strings and must not share a
// field. planUnits matches the purpose against a script's @widened value; the
// manifest prints the reason to a human. Conflating them makes every match
// fail, silently, and no isolated test catches it because each one builds its
// own fixture.
func TestSelectTargetsSeparatesPurposeFromReason(t *testing.T) {
	cands := []DatabaseInfo{
		{Name: "SALESDB", State: "ONLINE", HasAccess: true, IsPublished: true},
		{Name: "DISTDB", State: "ONLINE", HasAccess: true, IsDistributor: true},
	}
	sel, err := SelectTargets(cands, "SALESDB", "")
	if err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	w, ok := sel.Widened["DISTDB"]
	if !ok {
		t.Fatalf("DISTDB should be widened in; Widened = %v", sel.Widened)
	}
	if w.Purpose != "replication" {
		t.Errorf("Purpose = %q, want %q — this is what planUnits matches on",
			w.Purpose, "replication")
	}
	if !strings.Contains(w.Reason, "local distributor") {
		t.Errorf("Reason = %q, want it to explain itself to a human", w.Reason)
	}
}

// An instance can host more than one distribution database: sp_adddistributiondb
// may be called repeatedly. Each one is kept, because choosing between them
// would mean guessing which publisher uses which, and the archive would then be
// missing the agent history for whichever guess was wrong.
func TestSelectTargetsWidensToEveryDistributor(t *testing.T) {
	cands := []DatabaseInfo{
		{Name: "SALESDB", State: "ONLINE", HasAccess: true, IsPublished: true},
		{Name: "DISTDB", State: "ONLINE", HasAccess: true, IsDistributor: true},
		{Name: "DISTDB2", State: "ONLINE", HasAccess: true, IsDistributor: true},
	}
	sel, err := SelectTargets(cands, "SALESDB", "")
	if err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	for _, name := range []string{"DISTDB", "DISTDB2"} {
		if !slices.Contains(sel.Included, name) {
			t.Errorf("%s should be retained; Included = %v", name, sel.Included)
		}
		if sel.Widened[name].Purpose != "replication" {
			t.Errorf("%s should carry the replication purpose; Widened = %v", name, sel.Widened)
		}
	}
}

// A merge publisher sets is_merge_published and leaves is_published at 0 —
// they are different flags for different replication types, and 040 has always
// reported them separately. Widening on is_published alone therefore left a
// merge shop's distribution database behind, with nothing saying so.
func TestSelectTargetsWidensForAMergePublisher(t *testing.T) {
	cands := []DatabaseInfo{
		{Name: "MERGEDB", State: "ONLINE", HasAccess: true, IsMergePublished: true},
		{Name: "DISTDB", State: "ONLINE", HasAccess: true, IsDistributor: true},
	}
	sel, err := SelectTargets(cands, "MERGEDB", "")
	if err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	if !slices.Contains(sel.Included, "DISTDB") {
		t.Errorf("a merge publisher keeps its distributor too; Included = %v", sel.Included)
	}
}
