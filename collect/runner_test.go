package collect

import (
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
	got := SelectTargets(cands, "App*", "*Test")
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
	got := SelectTargets([]DatabaseInfo{{Name: "X", State: "ONLINE", HasAccess: true}}, "", "")
	if len(got.Included) != 1 {
		t.Errorf("included = %v, want [X]", got.Included)
	}
}

func TestQuoteNameEscapesBracket(t *testing.T) {
	if got := quoteName("we[i]rd"); got != "[we[i]]rd]" {
		t.Errorf("quoteName = %q", got)
	}
}
