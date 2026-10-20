package host

import "strings"

// Filter is one file system filter driver, as listed by fltmc.
type Filter struct {
	Name      string `json:"name"`
	Altitude  string `json:"altitude"`
	Instances int    `json:"instances"`
	Frame     int    `json:"frame"`
	Legacy    bool   `json:"legacy"`
}

// PowerPlan is one power plan of the host, with its standard type if it has one.
type PowerPlan struct {
	GUID string `json:"guid"`
	Name string `json:"name"`
	Type string `json:"type"`
}

var planTypes = map[string]string{
	"381b4222-f694-41f0-9685-ff5bb260df2e": "balanced",
	"8c5e7fda-e8bf-4a96-9a85-a6e23a8c635c": "high_performance",
	"a1841308-3541-4fab-bc81-f71556f20b4a": "power_saver",
	"e9a42b02-d5df-448d-aa00-03f14749eb61": "ultimate_performance",
}

// PlanType names a power plan by its GUID, case-insensitively and with or
// without braces: "other" for any GUID that is not one of the four standard plans.
func PlanType(guid string) string {
	g := strings.ToLower(strings.Trim(strings.TrimSpace(guid), "{}"))
	if t, ok := planTypes[g]; ok {
		return t
	}
	return "other"
}
