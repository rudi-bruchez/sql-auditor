package collect

import (
	"errors"
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
