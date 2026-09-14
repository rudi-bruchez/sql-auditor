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
