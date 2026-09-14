package collect

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestSkipReasonForProfile(t *testing.T) {
	member := Script{Path: "70.schema/050.heaps.sql", Profiles: []string{"space"}}
	outsider := Script{Path: "80.workload/010.wait-stats.sql",
		RequiresFlag: FlagIncludeSessionText, Permissions: []string{"view_server_state"}}

	// The operator's choice is reported first: a flag or a permission would
	// send them to an option that changes nothing for this run.
	reason, skip := skipReason(outsider, "space", map[string]bool{"view_server_state": true}, nil, nil)
	if !skip || reason != "not in profile space" {
		t.Errorf("outsider: skip=%v reason=%q, want the profile reason", skip, reason)
	}
	if _, skip := skipReason(member, "space", nil, nil, nil); skip {
		t.Error("a member with no other gate must run")
	}
	if _, skip := skipReason(outsider, "", nil, nil, map[string]bool{FlagIncludeSessionText: true}); skip {
		t.Error("without a profile, nothing changes for a script whose flag is on")
	}
	gated := Script{Path: "70.schema/041.compression-savings.sql",
		Profiles: []string{"space"}, RequiresFlag: FlagEstimateCompression}
	reason, skip = skipReason(gated, "space", nil, nil, nil)
	if !skip || !strings.Contains(reason, "--estimate-compression") {
		t.Errorf("a gated member falls through to its flag: skip=%v reason=%q", skip, reason)
	}
}

func TestPlanScriptsAppliesTheProfile(t *testing.T) {
	plan := planScripts([]Script{
		{Path: "a.sql", Profiles: []string{"space"}},
		{Path: "b.sql"},
	}, "space", nil, nil, nil)
	if plan[0].Skip != "" || plan[1].Skip != ProfileSkipReason("space") {
		t.Errorf("plan = %+v", plan)
	}
}

func TestCheckProfile(t *testing.T) {
	member := Script{Path: "70.schema/050.heaps.sql", Profiles: []string{"space"}}
	gated := Script{Path: "70.schema/041.compression-savings.sql", Profiles: []string{"space"},
		RequiresFlag: FlagEstimateCompression}
	broken := Script{Path: "70.schema/099.broken.sql", Profiles: []string{"space"},
		RequiresFlag: FlagQueryStoreDetail, LintError: "@timeout: missing"}
	outsider := Script{Path: "80.workload/021.query-store-detail.sql", RequiresFlag: FlagQueryStoreDetail}

	cases := []struct {
		name    string
		scripts []Script
		profile string
		flags   map[string]bool
		want    string // "" means no error
	}{
		{"no profile is never refused", []Script{outsider}, "", map[string]bool{FlagQueryStoreDetail: true}, ""},
		{"unknown name", []Script{member}, "spaec", nil, `unknown profile "spaec"; expected one of space`},
		{"no declaring script", []Script{outsider}, "space", nil,
			"profile space has no collector in this corpus; a corpus exported before profiles existed declares none"},
		{"every declaring script failed lint", []Script{{Path: "x.sql", Profiles: []string{"space"}, LintError: "bad"}}, "space", nil,
			"profile space has no usable collector in this corpus: x.sql failed lint (bad)"},
		{"a flag with no member", []Script{member, outsider}, "space", map[string]bool{FlagQueryStoreDetail: false, FlagIncludeSessionText: true},
			"--include-session-text has no collector in profile space, so it would collect nothing; drop the option or the profile"},
		{"a flag whose only member failed lint", []Script{member, broken}, "space", map[string]bool{FlagQueryStoreDetail: true},
			"--query-store-detail has no usable collector in profile space: 70.schema/099.broken.sql failed lint (@timeout: missing)"},
		{"a flag with a member", []Script{member, gated}, "space", map[string]bool{FlagEstimateCompression: true}, ""},
		{"a flag that is off is not judged", []Script{member}, "space", map[string]bool{FlagQueryStoreDetail: false}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckProfile(c.scripts, c.profile, c.flags)
			switch {
			case c.want == "" && err != nil:
				t.Errorf("unexpected refusal: %v", err)
			case c.want != "" && (err == nil || err.Error() != c.want):
				t.Errorf("got %v, want %q", err, c.want)
			}
		})
	}
}

func TestProfileMembers(t *testing.T) {
	all := []Script{
		{Path: "a.sql", Profiles: []string{"space"}},
		{Path: "b.sql"},
		{Path: "c.sql", Profiles: []string{"space"}, LintError: "bad"},
	}
	if got := ProfileMembers(all, ""); len(got) != 3 {
		t.Errorf("without a profile the corpus is returned as given, got %d", len(got))
	}
	got := ProfileMembers(all, "space")
	if len(got) != 1 || got[0].Path != "a.sql" {
		t.Errorf("members = %+v, want only a.sql", got)
	}
}

func TestManifestHumanProfileLine(t *testing.T) {
	cases := []struct {
		block ProfileBlock
		want  string
	}{
		{ProfileBlock{}, "Profile : none, the whole corpus"},
		{ProfileBlock{Name: "space", Members: 21, Corpus: 84}, "Profile : space, 21 of the 84 collectors in the corpus belong to it"},
		{ProfileBlock{Name: "space"}, "Profile : space, requested; the corpus was not read"},
	}
	for _, c := range cases {
		m := &Manifest{Profile: c.block}
		if h := flatten(m.Human()); !strings.Contains(h, c.want) {
			t.Errorf("MANIFEST.txt does not say %q:\n%s", c.want, m.Human())
		}
	}
}

func TestManifestHumanGroupsProfileSkips(t *testing.T) {
	m := &Manifest{Profile: ProfileBlock{Name: "space", Members: 2, Corpus: 5}}
	m.Skipped = []SkippedScript{
		{Script: "80.workload/010.wait-stats.sql", Reason: ProfileSkipReason("space")},
		{Script: "80.workload/020.query-store.sql", Reason: ProfileSkipReason("space")},
		{Script: "70.schema/041.compression-savings.sql", Reason: "not collected by default; pass --estimate-compression to include it"},
	}
	h := m.Human()
	if !strings.Contains(h, "Queries not run (3):") {
		t.Errorf("the heading must count every entry of skipped_scripts:\n%s", h)
	}
	if !strings.Contains(h, "  - 2 collectors outside profile space, each listed in _run.json") {
		t.Errorf("the profile skips must collapse into one line:\n%s", h)
	}
	if strings.Contains(h, "80.workload/010.wait-stats.sql") {
		t.Errorf("a collapsed skip must not also be listed:\n%s", h)
	}
	if !strings.Contains(h, "70.schema/041.compression-savings.sql") {
		t.Errorf("a skip for another reason must still be listed:\n%s", h)
	}
}

func TestRunRecordsTheRequestedProfileWhenTheCorpusCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "output")
	missing := filepath.Join(dir, "no-such-corpus")
	if _, err := Run(context.Background(), Options{
		Config:  &Config{Server: "localhost", OutputDir: out, QueriesDir: missing},
		Corpus:  os.DirFS(missing),
		Root:    ".",
		Now:     time.Now(),
		Profile: "space",
	}); err == nil {
		t.Fatal("want an error")
	}
	b, err := os.ReadFile(filepath.Join(failedRunDir(t, out), "_run.json"))
	if err != nil {
		t.Fatalf("no manifest written: %v", err)
	}
	var got struct {
		Profile ProfileBlock `json:"profile"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Profile != (ProfileBlock{Name: "space"}) {
		t.Errorf("profile block = %+v, want the requested name and no counts", got.Profile)
	}
}

func TestProfileCounts(t *testing.T) {
	scripts := []Script{
		{Path: "a.sql", Profiles: []string{"space"}},
		{Path: "b.sql"},
		{Path: "c.sql", Profiles: []string{"space"}, LintError: "bad"},
	}
	if m, c := profileCounts(scripts, "space"); m != 1 || c != 2 {
		t.Errorf("members, corpus = %d, %d, want 1, 2", m, c)
	}
	if m, c := profileCounts(scripts, ""); m != 0 || c != 2 {
		t.Errorf("without a profile: members, corpus = %d, %d, want 0, 2", m, c)
	}
}

// noMemberCorpus holds one valid collector that declares no profile.
func noMemberCorpus() fstest.MapFS {
	body := "-- @scope:       instance\n-- @resultsets:  a:object\n-- @permissions: VIEW SERVER STATE\n-- @timeout:     60\n" +
		contractPreamble + "SELECT 1 AS [x] OPTION (RECOMPILE, MAXDOP 1);\n"
	return fstest.MapFS{"queries/10.system/010.a.sql": {Data: []byte(body)}}
}

func TestCheckRefusesTheProfileBeforeListing(t *testing.T) {
	var code int
	var err error
	printed := captureStdout(t, func() {
		code, err = Check(context.Background(), Options{
			Config:  &Config{Server: "localhost", OutputDir: t.TempDir()},
			Corpus:  noMemberCorpus(),
			Root:    "queries",
			Profile: "space",
		})
	})
	if code != 2 || err == nil || !strings.Contains(err.Error(), "declares none") {
		t.Errorf("code %d, err %v; want 2 with the no-member refusal", code, err)
	}
	if strings.Contains(printed, "Queries (") {
		t.Errorf("the listing was printed before the refusal:\n%s", printed)
	}
}

func TestRunRefusesTheProfileAfterDiscoveryAndRecordsIt(t *testing.T) {
	out := filepath.Join(t.TempDir(), "output")
	code, err := Run(context.Background(), Options{
		Config:  &Config{Server: "localhost", OutputDir: out},
		Corpus:  noMemberCorpus(),
		Root:    "queries",
		Now:     time.Now(),
		Profile: "space",
	})
	if code != 2 || err == nil || !strings.Contains(err.Error(), "declares none") {
		t.Fatalf("code %d, err %v; want 2 with the no-member refusal", code, err)
	}
	b, rerr := os.ReadFile(filepath.Join(failedRunDir(t, out), "_run.json"))
	if rerr != nil {
		t.Fatalf("no failed-run record: %v", rerr)
	}
	var got struct {
		Profile ProfileBlock `json:"profile"`
	}
	if jerr := json.Unmarshal(b, &got); jerr != nil {
		t.Fatal(jerr)
	}
	if got.Profile.Name != "space" || got.Profile.Corpus != 1 || got.Profile.Members != 0 {
		t.Errorf("profile block = %+v, want name space, corpus 1, members 0", got.Profile)
	}
}

func TestWideningPurposesFollowsThePlan(t *testing.T) {
	repl := Script{Path: "90.availability/042.replication-distribution.sql", Widened: "replication"}
	plain := Script{Path: "70.schema/010.objects.sql"}
	if got := WideningPurposes([]plannedScript{{Script: repl}, {Script: plain}}); !got["replication"] {
		t.Errorf("a planned replication collector must widen, got %v", got)
	}
	skipped := []plannedScript{{Script: repl, Skip: ProfileSkipReason("space")}, {Script: plain}}
	if got := WideningPurposes(skipped); got["replication"] {
		t.Errorf("a skipped replication collector must not widen, got %v", got)
	}
}

func TestMembershipPurposes(t *testing.T) {
	repl := Script{Path: "r.sql", Widened: "replication"}
	gated := Script{Path: "g.sql", Widened: "replication", Profiles: []string{"space"}, RequiresFlag: FlagQueryStoreDetail}
	if got := membershipPurposes([]Script{repl}, "", nil); !got["replication"] {
		t.Errorf("without a profile, an ungated replication collector widens, got %v", got)
	}
	if got := membershipPurposes([]Script{repl}, "space", nil); got["replication"] {
		t.Errorf("a replication collector outside the profile must not widen, got %v", got)
	}
	if got := membershipPurposes([]Script{gated}, "space", nil); got["replication"] {
		t.Errorf("a member whose flag is off must not widen, got %v", got)
	}
	if got := membershipPurposes([]Script{gated}, "space", map[string]bool{FlagQueryStoreDetail: true}); !got["replication"] {
		t.Errorf("a member whose flag is on widens, got %v", got)
	}
}

func TestProfileChecks(t *testing.T) {
	checks := []CapabilityCheck{
		{Name: "connect", Status: "denied"},
		{Name: "view_any_definition", Status: "denied"},
		{Name: "view_server_state", Status: "denied"},
		{Name: "msdb_read", Status: "ok"},
		{Name: "agent_alerts", Status: "denied"},
		{Name: "log_shipping", Status: "error"},
	}
	scripts := []Script{
		{Path: "a.sql", Profiles: []string{"space"}, Permissions: []string{"connect", "view_server_state"}},
		{Path: "b.sql", Permissions: []string{"agent_alerts", "log_shipping"}},
		{Path: "c.sql", Profiles: []string{"space"}, Permissions: []string{"agent_alerts"}, LintError: "bad"},
	}
	got := ProfileChecks(checks, scripts, "space")
	want := map[string]string{
		"connect":             "denied",        // never rewritten
		"view_any_definition": "denied",        // never rewritten: database discovery needs it
		"view_server_state":   "denied",        // a member declares it
		"msdb_read":           "ok",            // untouched
		"agent_alerts":        StatusNotNeeded, // only a non-member and a lint failure declare it
		"log_shipping":        "error",         // an unanswered probe is never rewritten
	}
	for _, c := range got {
		if c.Status != want[c.Name] {
			t.Errorf("%s = %q, want %q", c.Name, c.Status, want[c.Name])
		}
	}
	if checks[4].Status != "denied" {
		t.Error("ProfileChecks must not modify its input")
	}
	for i, c := range ProfileChecks(checks, scripts, "") {
		if c != checks[i] {
			t.Errorf("without a profile the checks come back unchanged, %s = %q", c.Name, c.Status)
		}
	}
}

func TestCoverageUnderAProfile(t *testing.T) {
	m := &Manifest{Profile: ProfileBlock{Name: "space", Members: 21, Corpus: 84},
		Preflight: []CapabilityCheck{
			{Name: "connect", Status: "ok"},
			{Name: "agent_alerts", Label: "Read the Agent alerts and operators (msdb.dbo.sysalerts)", Status: StatusNotNeeded},
		}}
	m.refreshCoverage()
	if m.Coverage.Status != "complete" {
		t.Errorf("coverage = %q, want complete: the only refusal is not needed", m.Coverage.Status)
	}
	h := flatten(m.Human())
	if !strings.Contains(h, "COMPLETE") || strings.Contains(h, "INCOMPLETE") {
		t.Errorf("MANIFEST.txt must say COMPLETE:\n%s", m.Human())
	}
	if !strings.Contains(h, "Not needed by profile space") ||
		!strings.Contains(h, "Read the Agent alerts and operators") {
		t.Errorf("MANIFEST.txt must list the unneeded right by its label:\n%s", m.Human())
	}

	m.Preflight = append(m.Preflight, CapabilityCheck{Name: "log_shipping", Status: "error"})
	m.refreshCoverage()
	if m.Coverage.Status != "incomplete" {
		t.Errorf("coverage = %q, want incomplete: an unanswered probe is still unanswered", m.Coverage.Status)
	}
}

// Check computes its exit code from the checks after ProfileChecks. What keeps
// a lost instance from passing for success is that "error" is never rewritten,
// so an unanswered probe on a capability no member declares still gives exit 1.
func TestCheckExitsOneWhenAnUnneededProbeGotNoAnswer(t *testing.T) {
	checks := []CapabilityCheck{
		{Name: "connect", Status: "ok"},
		{Name: "log_shipping", Status: "error"},
	}
	scripts := []Script{{Path: "a.sql", Profiles: []string{"space"}, Permissions: []string{"connect"}}}
	if got := PreflightExitCode(ProfileChecks(checks, scripts, "space"), 0, true); got != 1 {
		t.Errorf("exit code = %d, want 1: the instance stopped answering", got)
	}
}

func TestPrintQueriesUnderAProfile(t *testing.T) {
	scripts := []Script{
		{Path: "70.schema/050.heaps.sql", Profiles: []string{"space"}},
		{Path: "70.schema/099.custom.sql", Profiles: []string{"space"}, LintError: "@timeout: missing"},
		{Path: "80.workload/010.wait-stats.sql"},
		{Path: "80.workload/099.other.sql", LintError: "GO batch separator"},
	}
	out := captureStdout(t, func() { printQueries(Options{Profile: "space"}, scripts) })
	for _, want := range []string{
		"Profile: space, 1 of 2 collectors\n",
		"Queries (2):\n",
		"  70.schema/050.heaps.sql",
		"  !! 70.schema/099.custom.sql",
		"Not in profile space: 1 collectors. Run check without --profile to list them.\n",
		"Lint failures outside profile space (1):\n",
		"  !! 80.workload/099.other.sql",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("listing does not contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "80.workload/010.wait-stats.sql") {
		t.Errorf("a clean script outside the profile must not be listed:\n%s", out)
	}
	whole := captureStdout(t, func() { printQueries(Options{}, scripts) })
	if !strings.HasPrefix(whole, "Queries (4):\n") || strings.Contains(whole, "Profile:") {
		t.Errorf("without a profile the listing is today's:\n%s", whole)
	}
}
