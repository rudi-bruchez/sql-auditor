package tui

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rudi-bruchez/sql-auditor/collect"
	"github.com/rudi-bruchez/sql-auditor/tui/screen"
)

// fixedSize is the terminal every test below pretends to have. The loop asks
// for it on every frame because a terminal can be resized between two of them.
func fixedSize(w, h int) func() (int, int) {
	return func() (int, int) { return w, h }
}

// frames records what the loop painted. Counting them is how the tests assert
// that the wizard repaints on every event and, more importantly, that it
// paints once BEFORE the first one.
type frames struct {
	n    int
	last []string
}

func (f *frames) draw(lines []string) { f.n++; f.last = lines }

// feed sends a sequence into a buffered channel and closes it, which is what
// makes these tests finite: loop returns when its channel is drained, so no
// test can hang waiting for a producer that will never come back.
func feed(es ...event) <-chan event {
	ch := make(chan event, len(es))
	for _, e := range es {
		ch <- e
	}
	close(ch)
	return ch
}

func key(k screen.NamedKey) pressEvent { return pressEvent{key: screen.Key{Named: k}} }
func rune_(r rune) pressEvent          { return pressEvent{key: screen.Key{Rune: r}} }

// drive runs the loop and reports the state it ended on, which is what these
// tests assert about. Production has no use for it — run.go only wants the exit
// code — so the loop does not return one, and the hook it does take is enough
// to observe every state it went through.
func drive(events <-chan event, draw func([]string), size func() (int, int), s State) (State, int) {
	last := s
	code := loop(events, draw, size, s, func(_, next State) { last = next })
	return last, code
}

// runFinished stands in for the event run.go sends when collect.Run returns.
// It lives in the test because task 21 owns the real one; what the loop has to
// honour is only the optional interface, which is what this exercises.
type runFinished struct {
	code int
	zip  string
}

func (e runFinished) apply(s State) State {
	s.Step = StepDone
	s.ZipPath = e.zip
	return s
}
func (e runFinished) exitStatus() int { return e.code }

func TestLoopFoldsASequenceOfEventsIntoTheExpectedState(t *testing.T) {
	var f frames
	// A whole collection, in the order collect produces it: the plan, then a
	// unit, then a tick, then the archive.
	end, code := drive(feed(
		plannedEvent{units: 3, databases: 2},
		unitStartedEvent{script: "queries/01/a.sql", database: "SALES"},
		unitDoneEvent{script: "queries/01/a.sql", database: "SALES", bytes: 4096, took: time.Second},
		tickEvent{elapsed: 124 * time.Second},
	), f.draw, fixedSize(80, 24), State{Step: StepCollecting})

	if end.Units != 3 || end.DoneUnits != 1 || end.Databases != 2 {
		t.Errorf("state = %d/%d units over %d databases, want 1/3 over 2", end.DoneUnits, end.Units, end.Databases)
	}
	if end.Bytes != 4096 {
		t.Errorf("Bytes = %d, want 4096", end.Bytes)
	}
	if end.Elapsed != 124*time.Second {
		t.Errorf("Elapsed = %v, want 2m04s", end.Elapsed)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	// One frame before the first event, then one per event: the screen must
	// not stay blank until something happens to it.
	if f.n != 5 {
		t.Errorf("the loop painted %d frames for 4 events, want 5", f.n)
	}
}

func TestLoopPaintsBeforeTheFirstEventArrives(t *testing.T) {
	var f frames
	// The realistic case: the wizard opens on screen 1 and waits on a keyboard
	// that may not be touched for a minute.
	drive(feed(), f.draw, fixedSize(80, 24), State{Step: StepConnection, Server: "SQL01"})
	if f.n != 1 {
		t.Fatalf("the loop painted %d frames with no events, want 1", f.n)
	}
	if !strings.Contains(strings.Join(f.last, "\n"), "SQL01") {
		t.Errorf("the first frame does not show the server:\n%s", strings.Join(f.last, "\n"))
	}
}

func TestLoopStopsTheCollectionOnCtrlCWithoutEndingTheStep(t *testing.T) {
	var f frames
	// Ctrl-C arrives as the byte 0x03 in raw mode, decoded to KeyCtrlC — never
	// as a signal. The run keeps going until it has written its manifest and
	// its archive, so the step must not change.
	end, code := drive(feed(key(screen.KeyCtrlC)), f.draw, fixedSize(80, 24),
		State{Step: StepCollecting, Units: 10, DoneUnits: 4})
	if !end.Stopping {
		t.Error("Ctrl-C during the collection did not set Stopping")
	}
	if end.Step != StepCollecting {
		t.Errorf("Step = %v, want StepCollecting: the run is still writing", end.Step)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(strings.Join(f.last, "\n"), "stopping") {
		t.Errorf("the repainted frame does not say it is stopping:\n%s", strings.Join(f.last, "\n"))
	}
}

func TestLoopExitsWithZeroWhenTheOperatorQuits(t *testing.T) {
	var f frames
	end, code := drive(feed(rune_('q')), f.draw, fixedSize(80, 24),
		State{Step: StepOptions, Verify: collect.VerifyResult{Probed: true, Collectors: 3}})
	if end.Step != StepQuit {
		t.Errorf("Step = %v, want StepQuit", end.Step)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0: quitting is not a failure", code)
	}
	// Nothing is painted for a screen that is about to be erased; the next
	// thing on this terminal is the shell prompt.
	if f.n != 1 {
		t.Errorf("the loop painted %d frames, want only the initial one", f.n)
	}
}

func TestLoopStopsAtTheFirstQuitAndIgnoresWhatFollows(t *testing.T) {
	var f frames
	// The keyboard goroutine stays blocked on Read after the quit and may
	// still push one keystroke; nothing after StepQuit may reach the state.
	end, _ := drive(feed(rune_('q'), plannedEvent{units: 99}), f.draw, fixedSize(80, 24),
		State{Step: StepDone})
	if end.Units != 0 {
		t.Errorf("Units = %d: an event after the quit was applied", end.Units)
	}
}

func TestLoopReturnsTheExitCodeTheRunReported(t *testing.T) {
	var f frames
	// The run comes back with 2 while the operator is still reading the final
	// screen. The code has to survive until [enter] ends the process.
	end, code := drive(feed(
		runFinished{code: 2, zip: `C:\audit\SRV-2026-08-13.zip`},
		key(screen.KeyEnter),
	), f.draw, fixedSize(80, 24), State{Step: StepCollecting})
	if end.Step != StepQuit {
		t.Errorf("Step = %v, want StepQuit", end.Step)
	}
	if code != 2 {
		t.Errorf("exit code = %d, want the 2 collect.Run reported", code)
	}
}

func TestLoopTurnsAPanicIntoAnErrorStateInsteadOfPanicking(t *testing.T) {
	var f frames
	// The recover lives in the goroutine that died — recover is per goroutine
	// — and what reaches the loop is this event. Handling it here is what buys
	// the orderly exit: terminal restored, progress flushed, operator told.
	//
	// The panic did NOT end the collection: it cancels its context, and
	// collect.Run goes on to write its manifest and build its archive before
	// coming back. The screen therefore stays on step 4, and [enter] is not
	// taken — a wizard that returned here would let main call os.Exit in the
	// middle of Zip and leave a truncated archive under an ordinary name.
	end, code := drive(feed(
		panicEvent{value: "index out of range [3] with length 3", where: "the collection"},
		key(screen.KeyEnter),
	), f.draw, fixedSize(80, 24), State{Step: StepCollecting, Units: 10, DoneUnits: 3})

	if end.Step != StepCollecting || !end.Stopping {
		t.Errorf("Step = %v, Stopping = %v: want the collection screen, stopping", end.Step, end.Stopping)
	}
	if end.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1", end.ErrorCount)
	}
	if len(end.Notes) != 1 || !strings.Contains(end.Notes[0], "index out of range") {
		t.Errorf("Notes = %q, want the panic value the operator can quote", end.Notes)
	}
	if code != 2 {
		t.Errorf("exit code = %d, want 2: a wizard that crashed must not report success", code)
	}
}

// The final screen is reached by the one event that proves the archive is
// finished: collect.Run coming back.
func TestLoopEndsAPanickedCollectionOnlyWhenTheRunHasReturned(t *testing.T) {
	var f frames
	end, code := drive(feed(
		panicEvent{value: "nil pointer dereference", where: "the activity indicator"},
		collectDoneEvent{code: 2, zipPath: `C:\out\a.zip`},
		key(screen.KeyEnter),
	), f.draw, fixedSize(80, 24), State{Step: StepCollecting, Units: 10, DoneUnits: 3})

	if end.Step != StepQuit {
		t.Errorf("Step = %v, want StepQuit after the run returned and the operator acknowledged", end.Step)
	}
	if end.ZipPath != `C:\out\a.zip` {
		t.Errorf("ZipPath = %q, want the archive the run finished writing", end.ZipPath)
	}
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

// A panic after the collection is over has nothing left to truncate, so it
// goes straight to the screen that reports it.
func TestLoopAPanicOnTheFinalScreenStaysOnTheFinalScreen(t *testing.T) {
	var f frames
	end, _ := drive(feed(
		panicEvent{value: "slice bounds out of range", where: "the keyboard reader"},
		key(screen.KeyEnter),
	), f.draw, fixedSize(80, 24), State{Step: StepDone})
	if end.Step != StepQuit {
		t.Errorf("Step = %v, want StepQuit", end.Step)
	}
}

func TestLoopAPanicBeforeTheCollectionReturnsToTheFirstScreen(t *testing.T) {
	var f frames
	end, _ := drive(feed(panicEvent{value: "nil map", where: "the probe"}),
		f.draw, fixedSize(80, 24), State{Step: StepVerifying, Field: fieldPassword})
	if end.Step != StepConnection || end.Field != fieldServer {
		t.Errorf("Step = %v, Field = %d: want screen 1 with the cursor in the server field", end.Step, end.Field)
	}
	if end.ConnError == nil || !strings.Contains(end.ConnError.Error(), "nil map") {
		t.Errorf("ConnError = %v, want the panic reported in full", end.ConnError)
	}
}

func TestLoopSurvivesATerminalThatReportsNoSize(t *testing.T) {
	var f frames
	// GetSize answers 0x0 while an RDP window is being dragged, and the loop
	// keeps repainting through it. Every step is exercised: a panic in the
	// renderer would leave the terminal in raw mode with the wizard gone.
	var checks []collect.CapabilityCheck
	for _, c := range collect.Capabilities() {
		checks = append(checks, collect.CapabilityCheck{Name: c.Name, Label: c.Label, Status: "ok"})
	}
	for _, step := range []Step{
		StepConnection, StepConnecting, StepVerification, StepVerifying,
		StepOptions, StepCollecting, StepDone,
	} {
		s := State{
			Step: step, Units: 223, DoneUnits: 147, Flags: map[string]bool{},
			Notes: []string{"skipped queries/07/qs.sql on RH: narrowed"},
			Verify: collect.VerifyResult{
				Probed: true, Collectors: 47, Checks: checks,
			},
		}
		end, code := drive(feed(tickEvent{elapsed: time.Second}, key(screen.KeyTab)),
			f.draw, fixedSize(0, 0), s)
		if end.Step == StepQuit && code != 0 {
			t.Errorf("step %v exited with %d", step, code)
		}
	}
}

// The reason this file exists: three producers, one consumer, no mutex. Run
// under -race it is the only test in the repository that exercises the
// package's actual concurrency design rather than its arithmetic.
func TestLoopSerialisesThreeConcurrentProducers(t *testing.T) {
	var f frames
	ch := make(chan event)
	const units = 50
	var wg sync.WaitGroup
	wg.Add(3)
	// The collection, through the real adapter.
	go func() {
		defer wg.Done()
		o := observer{ch: ch}
		o.Planned(units, 1)
		for i := 0; i < units; i++ {
			o.UnitStarted("queries/01/a.sql", "SALES")
			o.UnitDone("queries/01/a.sql", "SALES", 10, time.Millisecond, nil)
		}
	}()
	// The ticker.
	go func() {
		defer wg.Done()
		for i := 0; i < units; i++ {
			ch <- tickEvent{elapsed: time.Duration(i) * time.Second}
		}
	}()
	// The keyboard, pressing keys the collecting screen ignores.
	go func() {
		defer wg.Done()
		for i := 0; i < units; i++ {
			ch <- key(screen.KeyTab)
		}
	}()
	go func() {
		wg.Wait()
		// The quit comes last, and closing after it proves the loop returned
		// on the state rather than on the drained channel.
		ch <- runFinished{code: 0, zip: "archive.zip"}
		ch <- key(screen.KeyEnter)
		close(ch)
	}()

	end, code := drive(ch, f.draw, fixedSize(80, 24), State{Step: StepCollecting})
	if end.DoneUnits != units || end.Units != units {
		t.Errorf("DoneUnits = %d, Units = %d, want %d for both", end.DoneUnits, end.Units, units)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}
