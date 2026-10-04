package collect

import (
	"reflect"
	"testing"
	"time"
)

// durationVerify is a verified instance of three databases, with one script of
// each shape the ceiling has to price: an instance script, an ordinary
// per-database script with its own @timeout, a per-database script behind a
// cost opt-in, and one behind a disclosure opt-in whose @timeout is long too.
// Every figure below is derived from these timeouts by hand.
func durationVerify() VerifyResult {
	return VerifyResult{
		Probed: true,
		Server: ServerInfo{Version: "16.0.4135.4"},
		Checks: []CapabilityCheck{{Name: "connect", Status: "ok"}},
		Scripts: []Script{
			{Path: "10.system/010.instance.sql", Scope: ScopeInstance, TimeoutSec: 60},
			{Path: "20.databases/020.properties.sql", Scope: ScopeDatabase, TimeoutSec: 300},
			{Path: "70.schema/055.page-density.sql", Scope: ScopeDatabase, TimeoutSec: 1800,
				RequiresFlag: FlagMeasurePageDensity},
			{Path: "80.workload/022.query-store-profiled.sql", Scope: ScopeDatabase, TimeoutSec: 600,
				RequiresFlag: FlagQueryStorePlanStats},
		},
		Selection: Selection{Included: []string{"A", "B", "C"}},
		Folders:   []DatabaseFolder{{Name: "A", Folder: "A"}, {Name: "B", Folder: "B"}, {Name: "C", Folder: "C"}},
		config:    &Config{QueryTimeout: 60 * time.Second},
	}
}

// A per-database collector runs once per database, so its @timeout is paid
// once per database. Counting it once would announce a ceiling three times too
// low on the very collector the line exists to warn about.
func TestPlannedDurationCountsADatabaseUnitOncePerDatabase(t *testing.T) {
	b, ok := PlannedDuration(durationVerify(), "", map[string]bool{FlagMeasurePageDensity: true})
	if !ok {
		t.Fatal("PlannedDuration reported no plan on a probed instance")
	}
	want := DurationBound{
		Databases:     3,
		Costly:        []string{"70.schema/055.page-density.sql"},
		CostlyUnits:   3,
		CostlyCeiling: 3 * 1800 * time.Second,
		Units:         1 + 3 + 3,
		Ceiling:       (60 + 3*300 + 3*1800) * time.Second,
	}
	if !reflect.DeepEqual(b, want) {
		t.Errorf("bound = %+v\nwant    %+v", b, want)
	}
}

// The ceiling of a unit is the limit runUnit gives it, which is the script's
// own @timeout and only falls back to SQL_QUERY_TIMEOUT_SEC when there is none.
// Pricing every unit at the default would understate 020.properties fivefold.
func TestPlannedDurationUsesTheScriptsOwnTimeout(t *testing.T) {
	v := durationVerify()
	v.Scripts = []Script{
		{Path: "a.sql", Scope: ScopeInstance, TimeoutSec: 300},
		{Path: "b.sql", Scope: ScopeInstance}, // no @timeout: the default applies
	}
	b, ok := PlannedDuration(v, "", nil)
	if !ok {
		t.Fatal("PlannedDuration reported no plan on a probed instance")
	}
	if want := (300 + 60) * time.Second; b.Ceiling != want {
		t.Errorf("Ceiling = %s, want %s", b.Ceiling, want)
	}
}

// A cost opt-in that was not passed leaves its collector out of the plan, so
// it is neither named nor priced. Naming it would tell the operator that a
// collection they kept cheap is about to spend 1800 seconds per database.
func TestPlannedDurationLeavesOutACostlyCollectorWhoseFlagIsOff(t *testing.T) {
	b, ok := PlannedDuration(durationVerify(), "", nil)
	if !ok {
		t.Fatal("PlannedDuration reported no plan on a probed instance")
	}
	if len(b.Costly) != 0 || b.CostlyUnits != 0 || b.CostlyCeiling != 0 {
		t.Errorf("costly part = %v, %d units, %s; want none", b.Costly, b.CostlyUnits, b.CostlyCeiling)
	}
	if want := (60 + 3*300) * time.Second; b.Ceiling != want {
		t.Errorf("Ceiling = %s, want %s: the gated collector must not be priced", b.Ceiling, want)
	}
}

// Costly means off for cost, not long @timeout: --query-store-plan-stats is a
// disclosure opt-in, priced in the whole plan but not named among the costly.
func TestPlannedDurationNamesOnlyTheCostOptIns(t *testing.T) {
	b, _ := PlannedDuration(durationVerify(), "", map[string]bool{FlagQueryStorePlanStats: true})
	if len(b.Costly) != 0 {
		t.Errorf("Costly = %v, want none: plan stats is off for disclosure", b.Costly)
	}
	if want := (60 + 3*300 + 3*600) * time.Second; b.Ceiling != want {
		t.Errorf("Ceiling = %s, want %s", b.Ceiling, want)
	}
}

// Without a probe there is no plan: the version gates and the denials are
// unknown. A ceiling computed anyway would price collectors that will not run.
func TestPlannedDurationIsUnknownWithoutAProbe(t *testing.T) {
	v := durationVerify()
	v.Probed = false
	if _, ok := PlannedDuration(v, "", nil); ok {
		t.Error("PlannedDuration claimed a plan on an instance that was never probed")
	}
}

// The wording is the contract with the operator: a ceiling, said to be one.
func TestPlannedDurationLines(t *testing.T) {
	b := DurationBound{
		Databases:     4,
		Costly:        []string{"70.schema/041.compression-savings.sql", "70.schema/055.page-density.sql"},
		CostlyUnits:   8,
		CostlyCeiling: 8 * 1800 * time.Second,
		Units:         85,
		Ceiling:       25440 * time.Second,
	}
	want := []string{
		"4 databases; costly collectors on: 70.schema/041.compression-savings.sql, 70.schema/055.page-density.sql;",
		"  at most 4h00m (14400 s) if every one of their 8 units runs to its @timeout",
		"all 85 units: at most 7h04m (25440 s) if every one runs to its @timeout",
	}
	if got := b.Lines(); !reflect.DeepEqual(got, want) {
		t.Errorf("Lines =\n%q\nwant\n%q", got, want)
	}
	b = DurationBound{Databases: 1, Units: 3, Ceiling: 150 * time.Second}
	want = []string{
		"1 database; no costly collector on (--estimate-compression, --measure-page-density are off)",
		"all 3 units: at most 2m30s (150 s) if every one runs to its @timeout",
	}
	if got := b.Lines(); !reflect.DeepEqual(got, want) {
		t.Errorf("Lines =\n%q\nwant\n%q", got, want)
	}
}
