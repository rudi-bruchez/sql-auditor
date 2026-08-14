package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/sql-auditor/collect"
)

// baseOptions is a resolved configuration as buildOptions would hand one over.
// invalid.invalid is reserved by RFC 2606 and never resolves, and the one-second
// connect timeout means nothing here can wait on a network even by accident.
func baseOptions() collect.Options {
	return collect.Options{
		Config: &collect.Config{
			Server:         "invalid.invalid",
			Database:       "master",
			User:           "auditor",
			Password:       "from-dotenv",
			ConnectTimeout: time.Second,
			OutputDir:      "output",
		},
		Version: "0.18.0",
		Flags:   map[string]bool{},
	}
}

func TestApplyStateCarriesTheTypedServerAndPasswordIntoTheConfig(t *testing.T) {
	o := baseOptions()
	s := State{Server: "SRV\\BI_PROD", Password: "typed"}

	got := applyState(s, o)

	if got.Config.Server != "SRV\\BI_PROD" {
		t.Errorf("Server = %q", got.Config.Server)
	}
	if got.Config.Password != "typed" {
		t.Errorf("Password = %q, want the typed one", got.Config.Password)
	}
	if o.Config.Server != "invalid.invalid" || o.Config.Password != "from-dotenv" {
		t.Errorf("the caller's Config was mutated: %+v", *o.Config)
	}
}

// An untouched password field means "use what .env resolved", not "connect with
// no password": the field starts empty because the wizard never reads a secret
// back onto a screen.
func TestApplyStateKeepsTheResolvedPasswordWhenNothingWasTyped(t *testing.T) {
	got := applyState(State{Server: "invalid.invalid"}, baseOptions())
	if got.Config.Password != "from-dotenv" {
		t.Errorf("Password = %q, want the resolved one", got.Config.Password)
	}
}

func TestApplyStateCarriesTheSevenFlagsAndKeep(t *testing.T) {
	s := State{Keep: true, Flags: map[string]bool{}}
	for _, name := range flagOrder {
		s.Flags[name] = true
	}
	got := applyState(s, baseOptions())

	if !got.Keep {
		t.Error("Keep did not reach the options")
	}
	for _, name := range flagOrder {
		if !got.Flags[name] {
			t.Errorf("flag %q did not reach Options.Flags", name)
		}
	}
	// A copy, not the state's own map: toggleFlag hands out fresh maps and a
	// shared one would let a keystroke reach into a collection already running.
	got.Flags[flagOrder[0]] = false
	if !s.Flags[flagOrder[0]] {
		t.Error("Options.Flags aliases the state's map")
	}
}

func TestInitialStateShowsTheResolvedConnectionWithoutThePassword(t *testing.T) {
	o := baseOptions()
	o.Config.QueryStoreDays = 7

	s := initialState(o, true)

	if s.Step != StepConnection || s.Field != fieldServer {
		t.Errorf("step %v, field %d", s.Step, s.Field)
	}
	if s.Server != "invalid.invalid" || s.Catalog != "master" || s.User != "auditor" {
		t.Errorf("connection facts not carried over: %+v", s)
	}
	if s.Password != "" {
		t.Error("the resolved password was copied onto the screen's field")
	}
	if !s.ASCII {
		t.Error("the terminal's ASCII-only verdict was not carried over")
	}
	if s.QueryStoreWindow != "last 7 days" {
		t.Errorf("QueryStoreWindow = %q", s.QueryStoreWindow)
	}
}

// The bounds exist for the incident an average hides, and the clock they are
// read in is the server's. A window shown without that detail is a window read
// in the wrong time zone.
func TestQueryStoreWindowNamesTheServersClockForExplicitBounds(t *testing.T) {
	cfg := &collect.Config{QueryStoreFrom: "2026-07-26T14:00", QueryStoreTo: "2026-07-26T15:00"}
	if got := queryStoreWindow(cfg); got != "2026-07-26T14:00 to 2026-07-26T15:00, server local time" {
		t.Errorf("window = %q", got)
	}
}

func TestAStaleConnectionResultDoesNotDragTheOperatorBackIntoTheProbe(t *testing.T) {
	// The operator pressed ctrl-c during the wait and is back on screen 1; the
	// connection that was already in flight then succeeds.
	s := State{Step: StepConnection, Field: fieldServer}
	if got := (connectedEvent{}).apply(s); got.Step != StepConnection {
		t.Errorf("step = %v, want the cancelled attempt to be ignored", got.Step)
	}
	if got := (connectFailedEvent{err: errStub{}}).apply(s); got.ConnError != nil {
		t.Error("a cancelled attempt reported its failure onto screen 1")
	}
}

func TestACancelledCollectionExitsZero(t *testing.T) {
	e := collectDoneEvent{code: 2, ctxCancelled: true}
	if e.exitStatus() != 0 {
		t.Errorf("exit status = %d, want 0 for an operator's stop", e.exitStatus())
	}
	if (collectDoneEvent{code: 2}).exitStatus() != 2 {
		t.Error("a genuine failure lost its exit code")
	}
	s := e.apply(State{Step: StepCollecting, Stopping: true})
	if s.Step != StepDone || s.Stopping {
		t.Errorf("final state = %+v", s)
	}
}

// The word "partial" on the final screen is the RUN's, never the wizard's.
//
// A ctrl-c pressed after the last unit — while finish() writes the manifest and
// Zip builds the archive — cancels the wizard's context but fails no unit: the
// manifest inside the archive says cancelled=false and the run exits 0. A
// screen deriving the flag from its own context would call that archive partial
// and contradict the document inside it.
func TestThePartialArchiveIsAnnouncedOnlyByTheRunItself(t *testing.T) {
	late := collectDoneEvent{ctxCancelled: true, zipPath: `C:\out\a.zip`}
	if s := late.apply(State{Step: StepCollecting}); s.Cancelled {
		t.Error("the wizard called the archive partial on its own authority")
	}

	// And when the run does say so, the screen says so.
	stopped := late.apply(finishedEvent{cancelled: true}.apply(State{Step: StepCollecting}))
	if !stopped.Cancelled {
		t.Error("the run reported a stop and the screen did not carry it")
	}
	contains(t, Render(stopped, testWidth, 0), "Collection stopped. This archive is partial:")

	// A complete run keeps the ordinary sentence even though ctrl-c was pressed.
	whole := late.apply(finishedEvent{cancelled: false}.apply(State{Step: StepCollecting}))
	contains(t, Render(whole, testWidth, 0), "Send this file to whoever requested the audit:")
}

// TestARefusedConnectionHandsTheKeyboardBackToTheServerField is the offline
// end-to-end check of step 1: the address is reserved by RFC 2606 and never
// resolves, so the connection goroutine fails for real, over the real driver,
// without a database and without waiting on anything.
//
// What it pins is the one error in this wizard that does not end a step. The
// server's own words reach the screen, and the cursor goes back to the field
// most likely to be wrong.
func TestARefusedConnectionHandsTheKeyboardBackToTheServerField(t *testing.T) {
	r := &runner{opts: baseOptions(), events: make(chan event, 1), done: make(chan struct{})}
	defer close(r.done)

	r.connect(context.Background(), State{Step: StepConnecting, Server: "invalid.invalid"})

	select {
	case e := <-r.events:
		failed, ok := e.(connectFailedEvent)
		if !ok {
			t.Fatalf("event = %T, want a refusal", e)
		}
		s := failed.apply(State{Step: StepConnecting, Field: fieldPassword})
		if s.Step != StepConnection {
			t.Errorf("step = %v, want screen 1", s.Step)
		}
		if s.Field != fieldServer {
			t.Errorf("field = %d, want the cursor back in the server field", s.Field)
		}
		if s.ConnError == nil {
			t.Error("the server's message did not reach the screen")
		}
	default:
		t.Fatal("the connection goroutine reported nothing")
	}
}

// A pipe is never a terminal, on either platform, so this exercises the third
// exit door of the spec: raw mode refused. The wizard must not degrade — without
// raw mode the password field would echo, and a password left in the scrollback
// of a shared RDP session is worse than no wizard at all — and it must point at
// the non-interactive path rather than leaving the operator without a tool.
func TestRunRefusesATerminalItCannotPutIntoRawMode(t *testing.T) {
	in, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	defer inW.Close()
	outR, out, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outR.Close()
	defer out.Close()

	if code := Run(baseOptions(), in, out); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

type errStub struct{}

func (errStub) Error() string { return "refused" }

// The buffer is emptied from a defer, and this is what that buys. flushProgress
// used to be called in line at the end of Run: a panic in Render, in Draw or in
// stateChanged unwinds straight past such a call, and the buffer dies with the
// process. When lastResort has fired — an output directory that is full or
// read-only — that buffer holds the only copy of the manifest, so the run would
// leave no trace at all, which is precisely what the whole fallback chain
// exists to prevent.
func TestTheRunLogSurvivesAPanicOnTheWayOut(t *testing.T) {
	dir := t.TempDir()
	r := &runner{progress: &syncBuffer{}}
	const lastResortLine = `{"tool":"sql-auditor","run":{"exit_code":0}}`
	fmt.Fprintln(r.progress, lastResortLine)

	now := time.Date(2026, 8, 13, 9, 30, 0, 0, time.UTC)
	func() {
		// The recover stands in for the process: what matters is that the defer
		// below ran while the stack was unwinding.
		defer func() { _ = recover() }()
		defer r.flushOnExit(nil, dir, now)
		panic("the renderer blew up")
	}()

	written, err := os.ReadFile(filepath.Join(dir, progressFileName(now)))
	if err != nil {
		t.Fatalf("the run log was lost with the panic: %v", err)
	}
	if !strings.Contains(string(written), lastResortLine) {
		t.Errorf("run log = %q, want the buffered manifest", written)
	}
}

// A panic inside collect.Run must produce BOTH events. Only the first was sent
// before, and StepCollecting ends on the second: the wizard sat on "stopping…"
// until the process was killed, terminal still in raw mode.
//
// The recover is exercised directly rather than through collect.Run, which
// cannot be made to panic from a test — which is precisely how the gap survived
// a test suite that looked like it covered this.
func TestAPanickedCollectionReportsItselfAndThenReportsTheRunOver(t *testing.T) {
	r := &runner{events: make(chan event, 4), done: make(chan struct{})}
	func() {
		defer r.guardCollection()
		panic("index out of range [3] with length 3")
	}()
	close(r.events)

	var got []event
	for e := range r.events {
		got = append(got, e)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want the panic and the end of the run: %#v", len(got), got)
	}
	p, ok := got[0].(panicEvent)
	if !ok {
		t.Fatalf("first event is %T, want panicEvent", got[0])
	}
	if p.where != "the collection" {
		t.Errorf("where = %q, want the goroutine named", p.where)
	}
	d, ok := got[1].(collectDoneEvent)
	if !ok {
		t.Fatalf("second event is %T, want collectDoneEvent — the screen ends on nothing else", got[1])
	}
	if d.code != 2 {
		t.Errorf("code = %d, want 2: the run happened and something in it failed", d.code)
	}
	if d.err == nil || !strings.Contains(d.err.Error(), "index out of range") {
		t.Errorf("err = %v, want the panic value carried through", d.err)
	}
	// And it must not claim the operator cancelled: nothing was cancelled, the
	// code crashed.
	if d.ctxCancelled {
		t.Error("the crash was reported as a cancellation")
	}
}

// A collection that returns normally still sends exactly one event. The recover
// must not fire on the ordinary path.
func TestAnUnpanickedGuardSendsNothing(t *testing.T) {
	r := &runner{events: make(chan event, 4), done: make(chan struct{})}
	func() { defer r.guardCollection() }()
	if len(r.events) != 0 {
		t.Errorf("%d events sent with no panic", len(r.events))
	}
}
