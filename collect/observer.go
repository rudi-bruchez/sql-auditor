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
	// Finished reports the run's own verdict on whether the operator stopped
	// it, at the moment the manifest carrying that verdict is written. A caller
	// cannot derive it: its context being cancelled says a key was pressed, not
	// that anything was cut short — a Ctrl-C after the last unit fails no unit,
	// and the archive is whole. Calling an archive partial when the manifest
	// inside it says otherwise leaves the two documents of one run
	// contradicting each other.
	Finished(cancelled bool)
}

// unit is a script paired with the target it will run against. Instance scope
// gives one unit with an empty Target; database scope gives one per retained
// database, after the @writer scripts have been narrowed.
type unit struct {
	Script Script
	Target DatabaseFolder
}

// planUnits unfolds a resolved plan into the exact list of units the run will
// execute, and the skips that unfolding produced.
//
// It exists because the narrowing used to happen inside the execution loop,
// script by script, which left nobody able to state a total before the first
// unit ran. A total obtained by multiplying scripts by databases over-counts
// the moment a @writer script is narrowed: twelve databases and a pattern
// matching one give one unit, not twelve.
//
// The second return is ordered exactly as the plan is — a script's own skip
// first, then the per-database skips it produced, then the next script — so it
// can be appended to m.Skipped in one go without changing the list a human
// reads to write the audit up. Grouping the targeted skips together would read
// as a different run.
func planUnits(plan []plannedScript, folders []DatabaseFolder, cfg *Config) ([]unit, []SkippedScript) {
	var units []unit
	var skipped []SkippedScript
	for _, p := range plan {
		s := p.Script
		// A lint error is an error, not a skip. It is recorded by the caller,
		// where it can also set exit 2; turning it into a skip line here would
		// report a broken collector as a deliberate omission.
		if s.LintError != "" {
			continue
		}
		if p.Skip != "" {
			skipped = append(skipped, SkippedScript{Script: s.Path, Reason: p.Skip})
			continue
		}
		targets := []DatabaseFolder{{}}
		if s.Scope == ScopeDatabase {
			var narrowed []SkippedScript
			targets, narrowed = queryStoreUnits(cfg, s, folders)
			skipped = append(skipped, narrowed...)
		}
		for _, t := range targets {
			units = append(units, unit{Script: s, Target: t})
		}
	}
	return units, skipped
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

func (o observer) Finished(cancelled bool) {
	if o.Observer != nil {
		o.Observer.Finished(cancelled)
	}
}
