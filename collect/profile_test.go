package collect

import (
	"strings"
	"testing"
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
