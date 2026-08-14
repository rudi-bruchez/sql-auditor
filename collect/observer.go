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
	// Planned announces the gauge's denominator, once, before the first unit.
	// It is a number of UNITS and never of scripts: the corpus holds 55 files
	// and a run over twelve databases is 223 units.
	Planned(units int)
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
// The second and third returns are ordered exactly as the plan is — a script's
// own skip first, then the per-database skips it produced, then the next
// script — so each can be appended to its manifest list in one go without
// changing the list a human reads to write the audit up. Grouping the targeted
// skips together would read as a different run, and the same argument applies
// to the lint errors: they are produced here, in plan order, rather than by a
// second walk of the plan at the call site that would sort them by nothing.
func planUnits(plan []plannedScript, folders []DatabaseFolder, cfg *Config) ([]unit, []SkippedScript, []ErrorEntry) {
	var units []unit
	var skipped []SkippedScript
	var errs []ErrorEntry
	for _, p := range plan {
		s := p.Script
		// A lint error is an error, not a skip: turning it into a skip line
		// would report a broken collector as a deliberate omission. It is
		// returned separately because the caller also prices it — a corpus
		// that does not lint is exit 2 — and this function decides nothing
		// about exit codes.
		if s.LintError != "" {
			errs = append(errs, ErrorEntry{Script: s.Path, Message: s.LintError})
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
	return units, skipped, errs
}

// observer is the nil-safe wrapper every call site inside this package uses.
// Keeping the nil test here rather than at the twenty sites is the whole
// point: a forgotten guard would panic on the default path — the one with no
// observer at all — which is every command-line run.
//
// The Observer is held in a NAMED field and never embedded. Embedding would
// promote the interface's own methods onto the wrapper, so a method added to
// Observer later and not redefined below would compile, satisfy the interface,
// and dispatch straight onto the nil embedded value — a panic on the one path
// this type exists to protect. With a named field, forgetting a method is a
// compile error at the call site instead.
type observer struct{ o Observer }

func (w observer) Planned(units int) {
	if w.o != nil {
		w.o.Planned(units)
	}
}

func (w observer) UnitStarted(script, database string) {
	if w.o != nil {
		w.o.UnitStarted(script, database)
	}
}

func (w observer) UnitDone(script, database string, bytes int64, d time.Duration, err error) {
	if w.o != nil {
		w.o.UnitDone(script, database, bytes, d, err)
	}
}

func (w observer) ScriptSkipped(script, database, reason string) {
	if w.o != nil {
		w.o.ScriptSkipped(script, database, reason)
	}
}

func (w observer) Phase(name string) {
	if w.o != nil {
		w.o.Phase(name)
	}
}

func (w observer) Finished(cancelled bool) {
	if w.o != nil {
		w.o.Finished(cancelled)
	}
}
