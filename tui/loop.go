package tui

import (
	"fmt"
	"time"

	"github.com/rudi-bruchez/sql-auditor/tui/screen"
)

// loop is the wizard, minus its terminal.
//
// Three producers write into events — the keyboard reader, the ticker, and the
// collection's observer — and this function is the only consumer. That is the
// whole concurrency design of the package: the state is a value owned by this
// one goroutine, every change arrives as an event, and there is no mutex
// anywhere because there is nothing for two goroutines to share.
//
// draw and size are injected rather than taken from a *screen.Terminal for the
// same reason Render takes a width instead of a writer: this is the riskiest
// code in the batch, and a test that cannot allocate a pseudo-terminal is the
// only kind this repository has. With these two parameters the loop is
// exercisable end to end — keystrokes, ticks, progress, panics and the exit
// code — with a channel and two closures.
//
// It returns the final state and the process's exit code.
func loop(events <-chan event, draw func([]string), size func() (int, int), s State) (State, int) {
	return loopWith(events, draw, size, s, nil)
}

// loopWith is loop plus the one hook run.go needs, and the reason the two are
// separate functions is that the hook has no meaning without a terminal: it is
// what starts the connection, the probe, the collection and the ticker when the
// state enters a step that needs them, and stops them when it leaves.
//
// It cannot be expressed as an event, because an event is applied by this
// goroutine and a producer sending from here would block on a channel this
// goroutine is no longer reading. It cannot be expressed through draw either,
// which sees rendered lines and not the state. So it is a callback, invoked on
// THIS goroutine after every apply and before the frame is painted — which is
// also what makes it free of locks: everything it touches is reached from here
// and nowhere else.
//
// hook is nil for every test of the loop itself, which is the whole point of
// keeping loop's signature as the plan fixed it.
func loopWith(events <-chan event, draw func([]string), size func() (int, int), s State, hook func(prev, next State)) (State, int) {
	// The code the last event that knew one reported. It is remembered rather
	// than returned on the spot because the run's outcome arrives while the
	// operator is still looking at the final screen: collect.Run comes back
	// with 2, the wizard shows what happened, and the exit code has to survive
	// until [enter] ends the process.
	code := 0

	paint := func() {
		w, h := size()
		// A width of zero is not a bug to guard against here: GetSize reports
		// 0x0 while an RDP window is being dragged, and Render is total on its
		// inputs precisely so this loop can keep repainting through it.
		draw(Render(s, w, h))
	}
	paint()

	for e := range events {
		if c, ok := e.(coded); ok {
			code = c.exitStatus()
		}
		prev := s
		s = e.apply(s)
		if hook != nil {
			// Before the quit test, so that entering StepQuit is the transition
			// that stops the ticker and cancels whatever is still running. A
			// hook called only on the surviving paths would leave a ticker
			// ticking into a channel nobody reads.
			hook(prev, s)
		}
		if s.Step == StepQuit {
			// No last frame: the next thing on this terminal is the operator's
			// shell prompt, and painting a screen that is about to be erased
			// only risks leaving half of it behind.
			return s, code
		}
		paint()
	}
	// The channel closed with no quit: every producer is gone, which under
	// run.go means the terminal was closed under the wizard. Returning is the
	// only sensible answer, and the state is returned intact so the caller can
	// still flush what it has.
	return s, code
}

// coded is implemented by the events that carry a process exit status. Only
// the end of the collection has one to report; a keystroke never does, which
// is why this is an optional interface rather than a field on event.
type coded interface{ exitStatus() int }

// keyEvent is one keystroke, read off the terminal by its own goroutine and
// serialised here with everything else. It carries no logic of its own: the
// decision belongs to State.Key, which is pure and tested on its own.
type keyEvent struct{ key screen.Key }

func (e keyEvent) apply(s State) State { return s.Key(e.key) }

// tickEvent is the heartbeat of the three waiting steps. Something has to move
// on screen while the program waits on a server that may never answer, and a
// frozen screen in front of a frozen instance is the symptom this package
// exists to remove.
//
// It carries the elapsed time rather than adding an interval to a counter: the
// ticker goroutine knows when the wait started, and a screen that accumulated
// its own ticks would drift away from the clock exactly on the runs that last
// long enough for the number to matter.
type tickEvent struct{ elapsed time.Duration }

func (e tickEvent) apply(s State) State {
	s.Spinner++
	s.Elapsed = e.elapsed
	return s
}

// panicEvent is a goroutine that died. Every goroutine the wizard starts
// carries its own deferred recover — recover is per goroutine, and a panic in
// the ticker would otherwise take the process down without running a single
// defer, leaving the terminal in raw mode and the operator with a shell that
// echoes nothing.
//
// Converting it to an event rather than re-panicking on the main goroutine is
// what buys the orderly exit: the terminal gets restored, the progress buffer
// gets flushed, and the operator gets told rather than dropped.
type panicEvent struct {
	value any
	// where names the goroutine, since the message the operator can copy into
	// a bug report is otherwise indistinguishable between the three.
	where string
}

func (e panicEvent) apply(s State) State {
	err := fmt.Errorf("internal error in %s: %v", e.where, e.value)
	switch s.Step {
	case StepConnection, StepConnecting, StepVerification, StepVerifying, StepOptions:
		// Nothing has been collected yet, so the wizard can hand the keyboard
		// back to screen 1 and let the operator try again. ConnError is where
		// that screen already prints a refusal in full.
		s.Step = StepConnection
		s.Field = fieldServer
		s.ConnError = err
	case StepCollecting:
		// A collection is still running, and the screen STAYS on it. Whatever
		// died is not necessarily collect.Run: the cancellation this sets in
		// motion reaches it through its context, but it does not return on the
		// spot — it still writes its manifest and builds its archive. A wizard
		// that painted the final screen here would take [enter], return from
		// Run, and let main call os.Exit in the middle of Zip, leaving a
		// truncated .zip on disk under a perfectly ordinary name, impossible to
		// tell from a good one. The collectDoneEvent that Run will send is what
		// ends this screen.
		s.Stopping = true
		s.ErrorCount++
		s.Notes = note(s.Notes, err.Error())
	default:
		// The collection is over — StepDone or StepQuit — so there is nothing
		// left to truncate, and the final screen is where the wizard says what
		// happened, with whatever counters and archive path it had recorded.
		s.Step = StepDone
		s.Stopping = false
		s.ErrorCount++
		s.Notes = note(s.Notes, err.Error())
	}
	return s
}

// exitStatus makes a panic an exit-2 run. 2 is what collect already uses for
// "the run happened but something in it failed", which is the closest true
// statement available: a wizard that crashed and exited 0 would tell a script
// wrapping it that everything went fine.
func (e panicEvent) exitStatus() int { return 2 }
