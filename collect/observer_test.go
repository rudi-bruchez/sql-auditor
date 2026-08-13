package collect

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// recordingObserver keeps what it was told, in order. Order matters more than
// the values here: the screens are driven by a sequence, and a callback that
// arrives after the one it should precede would render a gauge that moves
// backwards.
type recordingObserver struct {
	planned []string
	started []string
	done    []string
	skipped []string
	phases  []string
}

func (r *recordingObserver) Planned(units, databases int) {
	r.planned = append(r.planned, fmtPair(units, databases))
}

func (r *recordingObserver) UnitStarted(script, database string) {
	r.started = append(r.started, script+"|"+database)
}

func (r *recordingObserver) UnitDone(script, database string, bytes int64, d time.Duration, err error) {
	r.done = append(r.done, script+"|"+database)
}

func (r *recordingObserver) ScriptSkipped(script, database, reason string) {
	r.skipped = append(r.skipped, script+"|"+database+"|"+reason)
}

func (r *recordingObserver) Phase(name string) { r.phases = append(r.phases, name) }

func fmtPair(a, b int) string {
	return string(rune('0'+a)) + "/" + string(rune('0'+b))
}

// The zero value is the path every non-TUI caller takes, so it is the one that
// must not panic. Testing it once here is what buys the right to call the
// callbacks unguarded at every site inside Run.
func TestObserverCallbacksAreSafeOnTheZeroValue(t *testing.T) {
	var o observer
	o.Planned(3, 2)
	o.UnitStarted("10.system/010.foo.sql", "")
	o.UnitDone("10.system/010.foo.sql", "", 42, time.Second, errors.New("boom"))
	o.ScriptSkipped("10.system/010.foo.sql", "RH", "not matched")
	o.Phase("archiving")
}

func TestObserverForwardsToTheWrappedImplementation(t *testing.T) {
	rec := &recordingObserver{}
	o := observer{Observer: rec}

	o.Planned(5, 3)
	o.UnitStarted("80.workload/020.query-store.sql", "SALESDB")
	o.UnitDone("80.workload/020.query-store.sql", "SALESDB", 10, time.Second, nil)
	o.ScriptSkipped("80.workload/021.query-store-detail.sql", "RH", "not matched by QUERY_STORE_DB_INCLUDE")
	o.Phase("writing manifest")

	if len(rec.planned) != 1 || rec.planned[0] != "5/3" {
		t.Fatalf("Planned not forwarded: %v", rec.planned)
	}
	if len(rec.started) != 1 || rec.started[0] != "80.workload/020.query-store.sql|SALESDB" {
		t.Fatalf("UnitStarted not forwarded: %v", rec.started)
	}
	if len(rec.done) != 1 {
		t.Fatalf("UnitDone not forwarded: %v", rec.done)
	}
	if len(rec.skipped) != 1 ||
		rec.skipped[0] != "80.workload/021.query-store-detail.sql|RH|not matched by QUERY_STORE_DB_INCLUDE" {
		t.Fatalf("ScriptSkipped lost its target: %v", rec.skipped)
	}
	if len(rec.phases) != 1 || rec.phases[0] != "writing manifest" {
		t.Fatalf("Phase not forwarded: %v", rec.phases)
	}
}

// The narrowing has to happen once, before the loop, or the total announced to
// the gauge is a multiplication that over-counts: three databases and a pattern
// matching one give one unit for a @writer script, not three.
func TestPlanUnitsAppliesQueryStoreNarrowingBeforeTheLoop(t *testing.T) {
	folders := []DatabaseFolder{
		{Name: "SALESDB", Folder: "SALESDB"},
		{Name: "RH", Folder: "RH"},
		{Name: "FACTURATION", Folder: "FACTURATION"},
	}
	plan := []plannedScript{
		{Script: Script{Path: "10.system/010.version.sql", Scope: ScopeInstance}},
		{Script: Script{Path: "20.databases/022.query-store.sql", Scope: ScopeDatabase}},
		{Script: Script{Path: "80.workload/021.query-store-detail.sql",
			Scope: ScopeDatabase, Writer: "query-store-detail"}},
		{Script: Script{Path: "10.system/072.resource-governor.sql", Scope: ScopeInstance},
			Skip: "needs 2016"},
	}
	cfg := &Config{QueryStoreDBInclude: "SALES*"}

	units, skipped := planUnits(plan, folders, cfg)

	// 1 instance + 3 databases + 1 narrowed writer. Seven would be the answer
	// of a total computed by multiplying scripts by databases.
	if len(units) != 5 {
		t.Fatalf("units = %d, want 5: %+v", len(units), units)
	}
	var targeted []SkippedScript
	for _, s := range skipped {
		if s.Target != "" {
			targeted = append(targeted, s)
		}
	}
	if len(targeted) != 2 {
		t.Fatalf("targeted skips = %+v, want one per database removed", targeted)
	}
	if targeted[0].Target != "RH" || targeted[1].Target != "FACTURATION" {
		t.Errorf("targeted skips do not name their databases: %+v", targeted)
	}
}

func TestPlanUnitsExcludesScriptsThatWillNotRun(t *testing.T) {
	folders := []DatabaseFolder{{Name: "SALESDB", Folder: "SALESDB"}}
	plan := []plannedScript{
		{Script: Script{Path: "10.system/072.resource-governor.sql", Scope: ScopeInstance},
			Skip: "needs 2016"},
		{Script: Script{Path: "10.system/099.broken.sql", Scope: ScopeInstance,
			LintError: "missing @resultsets"}},
	}

	units, skipped := planUnits(plan, folders, &Config{})

	if len(units) != 0 {
		t.Fatalf("units = %+v, want none: neither entry will run", units)
	}
	// A lint error is an error, not a skip: it belongs to m.Errors and sets
	// exit 2, so planUnits must not quietly turn it into a skip line.
	if len(skipped) != 1 || skipped[0].Script != "10.system/072.resource-governor.sql" {
		t.Fatalf("skipped = %+v, want only the version gate", skipped)
	}
}

// m.Skipped is the list a human reads to write the audit up, and it fills in
// plan order today: a script's own skip, then its per-database skips, then the
// next script. Nothing else in the package pins that order down, so piling the
// targeted skips ahead of the global ones would regress it in silence.
func TestPlanUnitsKeepsTheManifestSkipOrder(t *testing.T) {
	folders := []DatabaseFolder{
		{Name: "SALESDB", Folder: "SALESDB"},
		{Name: "RH", Folder: "RH"},
		{Name: "FACTURATION", Folder: "FACTURATION"},
	}
	plan := []plannedScript{
		{Script: Script{Path: "10.system/072.resource-governor.sql", Scope: ScopeInstance},
			Skip: "needs 2016"},
		{Script: Script{Path: "80.workload/021.query-store-detail.sql",
			Scope: ScopeDatabase, Writer: "query-store-detail"}},
		{Script: Script{Path: "80.workload/022.query-store-profiled.sql", Scope: ScopeInstance},
			Skip: "not collected by default"},
	}
	cfg := &Config{QueryStoreDBInclude: "SALES*"}

	_, skipped := planUnits(plan, folders, cfg)

	want := []SkippedScript{
		{Script: "10.system/072.resource-governor.sql", Reason: "needs 2016"},
		{Script: "80.workload/021.query-store-detail.sql", Target: "RH",
			Reason: "not matched by QUERY_STORE_DB_INCLUDE"},
		{Script: "80.workload/021.query-store-detail.sql", Target: "FACTURATION",
			Reason: "not matched by QUERY_STORE_DB_INCLUDE"},
		{Script: "80.workload/022.query-store-profiled.sql", Reason: "not collected by default"},
	}
	if !reflect.DeepEqual(skipped, want) {
		t.Errorf("skip order changed:\n got %+v\nwant %+v", skipped, want)
	}
}
