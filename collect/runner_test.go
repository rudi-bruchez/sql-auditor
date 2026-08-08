package collect

import (
	"net/url"
	"strings"
	"testing"
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
