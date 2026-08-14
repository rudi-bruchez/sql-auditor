package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/sql-auditor/collect"
)

// The adapter must satisfy the interface collect.Run takes, and nothing here
// would catch it if it stopped: run.go is the only production call site and no
// test in this repository reaches it.
var _ collect.Observer = observer{}

// fold drains a buffered channel and applies everything in it, which is what
// the render loop does. Tests of the adapter go through it rather than calling
// apply directly, so that the packaging half — the part that could send the
// wrong event — is exercised too.
func fold(t *testing.T, s State, ch chan event) State {
	t.Helper()
	close(ch)
	for e := range ch {
		s = e.apply(s)
	}
	return s
}

func TestObserverPlannedCarriesTheGaugeDenominator(t *testing.T) {
	ch := make(chan event, 4)
	observer{ch: ch}.Planned(223)
	got := fold(t, State{}, ch)
	if got.Units != 223 {
		t.Errorf("after Planned(223): Units = %d", got.Units)
	}
}

func TestObserverUnitDoneAdvancesTheGaugeAndSumsTheBytes(t *testing.T) {
	ch := make(chan event, 4)
	o := observer{ch: ch}
	o.UnitDone("queries/01/a.sql", "", 1200, time.Second, nil)
	o.UnitDone("queries/01/b.sql", "SALES", 800, time.Second, nil)
	got := fold(t, State{}, ch)
	if got.DoneUnits != 2 {
		t.Errorf("DoneUnits = %d, want 2", got.DoneUnits)
	}
	if got.Bytes != 2000 {
		t.Errorf("Bytes = %d, want 2000", got.Bytes)
	}
	if got.ErrorCount != 0 {
		t.Errorf("ErrorCount = %d on two successful units", got.ErrorCount)
	}
}

func TestObserverCountsAFailedUnitAsFinished(t *testing.T) {
	ch := make(chan event, 4)
	// A unit that failed is a unit that is over. A gauge that only advanced on
	// success would stop short of its own denominator on any run with one
	// denied collector, which reads as a program that hung.
	observer{ch: ch}.UnitDone("queries/02/waits.sql", "", 0, time.Second,
		errors.New("VIEW SERVER STATE permission was denied"))
	got := fold(t, State{}, ch)
	if got.DoneUnits != 1 {
		t.Errorf("DoneUnits = %d after a failed unit, want 1", got.DoneUnits)
	}
	if got.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1", got.ErrorCount)
	}
	if len(got.Notes) != 1 || !strings.Contains(got.Notes[0], "VIEW SERVER STATE") {
		t.Errorf("Notes = %q, want the server's own message", got.Notes)
	}
}

func TestObserverNamesTheDatabaseOfATargetedSkip(t *testing.T) {
	ch := make(chan event, 4)
	// This is what QUERY_STORE_DB_INCLUDE produces, and N identical lines
	// naming no database is not a report.
	observer{ch: ch}.ScriptSkipped("queries/07/qs-top.sql", "RH", "not matched by QUERY_STORE_DB_INCLUDE")
	got := fold(t, State{}, ch)
	if got.SkippedCount != 1 {
		t.Errorf("SkippedCount = %d, want 1", got.SkippedCount)
	}
	if len(got.Notes) != 1 {
		t.Fatalf("Notes = %q, want one line", got.Notes)
	}
	if !strings.Contains(got.Notes[0], "RH") {
		t.Errorf("Notes[0] = %q, want the database named", got.Notes[0])
	}
	if !strings.Contains(got.Notes[0], "QUERY_STORE_DB_INCLUDE") {
		t.Errorf("Notes[0] = %q, want the reason kept verbatim", got.Notes[0])
	}
}

func TestObserverGlobalSkipDoesNotInventADatabase(t *testing.T) {
	ch := make(chan event, 4)
	observer{ch: ch}.ScriptSkipped("queries/09/compression.sql", "", "requires --estimate-compression")
	got := fold(t, State{}, ch)
	if len(got.Notes) != 1 || strings.Contains(got.Notes[0], " on ") {
		t.Errorf("Notes = %q, want no database in a whole-script skip", got.Notes)
	}
}

// The invariant the entire gauge rests on. planUnits excludes skipped scripts
// from the units it counts, so a skip that advanced DoneUnits would end a
// perfectly ordinary run above 100% — and a final screen claiming 231/223 is
// worse than no gauge at all.
func TestObserverACompleteRunEndsAtExactlyOneHundredPercent(t *testing.T) {
	const units = 5
	ch := make(chan event, 32)
	o := observer{ch: ch}
	o.Planned(units)
	for i := 0; i < units; i++ {
		o.UnitStarted("queries/01/a.sql", "")
		o.UnitDone("queries/01/a.sql", "", 10, time.Second, nil)
	}
	// Skips interleaved with the units, as a real plan produces them: gated
	// scripts and per-database narrowing both land here.
	o.ScriptSkipped("queries/09/compression.sql", "", "requires --estimate-compression")
	o.ScriptSkipped("queries/07/qs-top.sql", "RH", "not matched by QUERY_STORE_DB_INCLUDE")
	o.ScriptSkipped("queries/07/qs-top.sql", "FACT", "not matched by QUERY_STORE_DB_INCLUDE")
	o.Phase("writing manifest")
	o.Phase("archiving")
	got := fold(t, State{}, ch)

	if got.DoneUnits != got.Units {
		t.Errorf("DoneUnits = %d, Units = %d: a complete run must end at 100%%", got.DoneUnits, got.Units)
	}
	if got.SkippedCount != 3 {
		t.Errorf("SkippedCount = %d, want 3", got.SkippedCount)
	}
	// And the rendered gauge agrees, which is the number the operator sees.
	if _, pct := bar(got.DoneUnits, got.Units, 30); pct != 100 {
		t.Errorf("bar() reports %d%%, want 100%%", pct)
	}
}

func TestObserverPhaseReplacesTheCollectorLine(t *testing.T) {
	ch := make(chan event, 4)
	o := observer{ch: ch}
	o.UnitStarted("queries/01/a.sql", "SALES")
	o.Phase("archiving")
	got := fold(t, State{}, ch)
	if got.Script != "archiving" || got.Database != "" {
		t.Errorf("after Phase: Script = %q, Database = %q", got.Script, got.Database)
	}
}

// The adapter's whole contract: wrap and send, touch nothing. Anything it
// computed would be computed off the render loop's goroutine, and this package
// has no mutex to protect that.
func TestObserverOnlySendsAndNeverTouchesTheState(t *testing.T) {
	ch := make(chan event, 8)
	o := observer{ch: ch}
	o.Planned(3)
	o.UnitStarted("a.sql", "")
	o.UnitDone("a.sql", "", 1, time.Second, nil)
	o.ScriptSkipped("b.sql", "", "gated")
	o.Phase("archiving")
	if len(ch) != 5 {
		t.Errorf("the adapter sent %d events for 5 callbacks", len(ch))
	}
}

func TestObserverNotesKeepTheMostRecentLines(t *testing.T) {
	ch := make(chan event, 64)
	o := observer{ch: ch}
	for i := 0; i < maxNotes+3; i++ {
		o.ScriptSkipped("queries/07/qs.sql", "DB"+string(rune('A'+i)), "narrowed")
	}
	got := fold(t, State{}, ch)
	if len(got.Notes) != maxNotes {
		t.Fatalf("Notes has %d lines, want the last %d", len(got.Notes), maxNotes)
	}
	// The tail, not the head: the note that belongs to what is running now is
	// the one worth a line on a screen that has no scrollback.
	last := "DB" + string(rune('A'+maxNotes+2))
	if !strings.Contains(got.Notes[len(got.Notes)-1], last) {
		t.Errorf("last note = %q, want the most recent skip (%s)", got.Notes[len(got.Notes)-1], last)
	}
}

// State is a value everywhere in this package. An event that appended into a
// shared backing array would let a note written for one frame appear in a copy
// taken before it.
func TestObserverEventsDoNotShareTheNoteSlice(t *testing.T) {
	base := State{}
	base = scriptSkippedEvent{script: "a.sql", reason: "gated"}.apply(base)
	left := scriptSkippedEvent{script: "left.sql", reason: "gated"}.apply(base)
	right := scriptSkippedEvent{script: "right.sql", reason: "gated"}.apply(base)
	if strings.Contains(left.Notes[1], "right") || strings.Contains(right.Notes[1], "left") {
		t.Errorf("two states share a slice: left = %q, right = %q", left.Notes, right.Notes)
	}
}
