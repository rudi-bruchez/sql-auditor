package tui

import (
	"time"
)

// event is one thing that happened, packaged so it can be folded into the
// state by whoever owns it. The three producers — keyboard, ticker, collection
// — all speak this type and nothing else, which is what lets a single channel
// serialise them.
//
// apply takes a State and returns a State rather than mutating one through a
// pointer: the loop holds the only copy that matters, and a value cannot be
// half-updated by a producer that is running while the frame is being painted.
type event interface{ apply(State) State }

// observer is the collect.Observer the wizard hands to collect.Run, and it is
// deliberately the thinnest thing that can implement it: it wraps each callback
// in an event and sends it.
//
// It NEVER touches the state. That is the entire reason the channel exists —
// collect.Run runs in its own goroutine, and an observer that updated State
// directly would be a second writer racing the render loop, which would then
// need a mutex. There is no mutex anywhere in this package, and this type is
// why.
//
// The send is blocking, which is also deliberate: it applies back pressure on
// a collection that produces events faster than the screen can absorb them,
// instead of dropping the events that say what went wrong.
type observer struct{ ch chan<- event }

// send is the one place a nil channel is tolerated. A zero-value observer
// appears in tests of the events themselves, and a send on a nil channel blocks
// forever — a deadlock in a wizard whose whole purpose is to never look frozen
// would be the worst possible way to fail.
func (o observer) send(e event) {
	if o.ch == nil {
		return
	}
	o.ch <- e
}

func (o observer) Planned(units int) { o.send(plannedEvent{units: units}) }

func (o observer) UnitStarted(script, database string) {
	o.send(unitStartedEvent{script: script, database: database})
}

func (o observer) UnitDone(script, database string, bytes int64, d time.Duration, err error) {
	o.send(unitDoneEvent{script: script, database: database, bytes: bytes, took: d, err: err})
}

func (o observer) ScriptSkipped(script, database, reason string) {
	o.send(scriptSkippedEvent{script: script, database: database, reason: reason})
}

func (o observer) Phase(name string) { o.send(phaseEvent{name: name}) }

func (o observer) Finished(cancelled bool) { o.send(finishedEvent{cancelled: cancelled}) }

// plannedEvent carries the gauge's denominator. It arrives once, before the
// first unit, because planUnits resolves the whole plan up front — which is
// exactly why the wizard can show 12/223 rather than a spinner.
type plannedEvent struct{ units int }

func (e plannedEvent) apply(s State) State {
	s.Units = e.units
	return s
}

// unitStartedEvent names what is running now. Screen 4 shows it under the bar,
// because a Query Store extraction can hold the bar at 65% for two minutes and
// the collector's name is the only thing that says why.
type unitStartedEvent struct{ script, database string }

func (e unitStartedEvent) apply(s State) State {
	s.Script, s.Database = e.script, e.database
	return s
}

// unitDoneEvent advances the gauge by one, whatever the outcome.
type unitDoneEvent struct {
	script, database string
	bytes            int64
	took             time.Duration
	err              error
}

func (e unitDoneEvent) apply(s State) State {
	// A unit that failed is a unit that is FINISHED. Advancing only on success
	// would leave a gauge that never reaches its own denominator on any run
	// with a single denied collector — and a bar frozen at 219/223 while the
	// program has plainly moved on to the manifest is precisely the "is it
	// stuck?" question this screen exists to answer.
	s.DoneUnits++
	s.Bytes += e.bytes
	if e.err != nil {
		s.ErrorCount++
		s.Notes = note(s.Notes, status("error")+where(e.script, e.database)+": "+e.err.Error())
	}
	return s
}

// scriptSkippedEvent is a collector that will not run at all.
type scriptSkippedEvent struct{ script, database, reason string }

func (e scriptSkippedEvent) apply(s State) State {
	// It does NOT advance the gauge, and that is the subtle half of this file.
	// planUnits excludes skipped scripts from the units it returns, so Planned
	// never counted this one: crediting it here would push DoneUnits past Units
	// and print 104% on the final screen. The invariant the whole gauge rests
	// on is that a complete run ends with DoneUnits == Units, one UnitDone per
	// unit and nothing else.
	s.SkippedCount++
	s.Notes = note(s.Notes, status("skipped")+where(e.script, e.database)+": "+e.reason)
	return s
}

// phaseEvent is the work that happens between the last unit and the archive:
// writing the manifest, zipping. It reuses the collector line rather than
// adding a screen, since the gauge is legitimately full by then and the only
// remaining question is what the program is still doing.
type phaseEvent struct{ name string }

func (e phaseEvent) apply(s State) State {
	s.Script, s.Database = e.name, ""
	return s
}

// finishedEvent is the run's own verdict on whether it was cut short, taken
// from the manifest it has just written.
//
// It is the ONLY source of the word "partial" on the final screen. Deriving it
// from the wizard's cancelled context looks equivalent and is not: a ctrl-c
// pressed after the last unit, while finish() and Zip() run, fails no unit, so
// the manifest inside the archive says cancelled=false and the run exits 0 —
// while the screen would tell the DBA the archive is partial. Two documents of
// one run contradicting each other is worse than either answer alone.
type finishedEvent struct{ cancelled bool }

func (e finishedEvent) apply(s State) State {
	s.Cancelled = e.cancelled
	return s
}

// where names a script and, when there is one, the database it was aimed at.
// A targeted skip that did not name its database would print N identical lines
// — which is exactly the report QUERY_STORE_DB_INCLUDE makes useless, and why
// collect.Observer carries the database at all.
func where(script, database string) string {
	if database == "" {
		return script
	}
	return script + " on " + database
}

// maxNotes bounds the summary under the gauge. It is a summary and not a log:
// the manifest already records every skip and every error, in full and in
// order, and a frame is a screen rather than a scrollback. Keeping the tail
// rather than the head keeps the note that belongs to what is running now.
const maxNotes = 6

func note(notes []string, line string) []string {
	// A fresh slice each time: State is a value handed around by the loop, and
	// an append that reused the backing array would let a note written for one
	// frame appear in a copy of the state taken before it.
	out := make([]string, 0, len(notes)+1)
	out = append(out, notes...)
	out = append(out, line)
	if len(out) > maxNotes {
		out = out[len(out)-maxNotes:]
	}
	return out
}
