package host

import "testing"

func TestPlanType(t *testing.T) {
	cases := map[string]string{
		"381b4222-f694-41f0-9685-ff5bb260df2e":   "balanced",
		"8c5e7fda-e8bf-4a96-9a85-a6e23a8c635c":   "high_performance",
		"a1841308-3541-4fab-bc81-f71556f20b4a":   "power_saver",
		"e9a42b02-d5df-448d-aa00-03f14749eb61":   "ultimate_performance",
		"{381B4222-F694-41F0-9685-FF5BB260DF2E}": "balanced",
		"{E9A42B02-D5DF-448D-AA00-03F14749EB61}": "ultimate_performance",
		"00000000-0000-0000-0000-000000000000":   "other",
		"":                                       "other",
	}
	for guid, want := range cases {
		if got := PlanType(guid); got != want {
			t.Errorf("PlanType(%q) = %q, want %q", guid, got, want)
		}
	}
}
