package collect

import "time"

// Observer is how a caller watches a run go by. It is defined here, in the
// package that produces the events, and implemented by whoever displays them:
// the dependency never runs the other way, so collect stays buildable and
// testable with no interface layer above it at all.
//
// Nothing here returns anything. An observer that could refuse or redirect a
// unit would be a second scheduler beside the plan, and the plan is the thing
// the manifest is written from.
type Observer interface {
	Planned(units, databases int)
	UnitStarted(script, database string)
	// UnitDone carries bytes but no row count. runUnit never counts rows, and
	// producing one would have to cross ReadResultSets and six writers for a
	// figure no screen shows.
	UnitDone(script, database string, bytes int64, d time.Duration, err error)
	// ScriptSkipped reports a collector that will not run. An empty database
	// is a whole-script skip; a named one is a per-database skip, which is
	// what QUERY_STORE_DB_INCLUDE produces. SkippedScript carries Target for
	// the same reason: N identical lines naming no database is not a report.
	ScriptSkipped(script, database, reason string)
	Phase(name string)
}

// unit is a script paired with the target it will run against. Instance scope
// gives one unit with an empty Target; database scope gives one per retained
// database, after the @writer scripts have been narrowed.
type unit struct {
	Script Script
	Target DatabaseFolder
}

// observer is the nil-safe wrapper every call site inside this package uses.
// Keeping the nil test here rather than at the twenty sites is the whole
// point: a forgotten guard would panic on the default path — the one with no
// observer at all — which is every command-line run.
type observer struct{ Observer }

func (o observer) Planned(units, databases int) {
	if o.Observer != nil {
		o.Observer.Planned(units, databases)
	}
}

func (o observer) UnitStarted(script, database string) {
	if o.Observer != nil {
		o.Observer.UnitStarted(script, database)
	}
}

func (o observer) UnitDone(script, database string, bytes int64, d time.Duration, err error) {
	if o.Observer != nil {
		o.Observer.UnitDone(script, database, bytes, d, err)
	}
}

func (o observer) ScriptSkipped(script, database, reason string) {
	if o.Observer != nil {
		o.Observer.ScriptSkipped(script, database, reason)
	}
}

func (o observer) Phase(name string) {
	if o.Observer != nil {
		o.Observer.Phase(name)
	}
}
