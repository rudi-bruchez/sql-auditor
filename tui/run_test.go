package tui

import (
	"context"
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
		Version: "0.17.0",
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
	if len(flagOrder) != 7 {
		t.Fatalf("flagOrder has %d entries, want the seven opt-ins", len(flagOrder))
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
	e := collectDoneEvent{code: 2, cancelled: true}
	if e.exitStatus() != 0 {
		t.Errorf("exit status = %d, want 0 for an operator's stop", e.exitStatus())
	}
	if (collectDoneEvent{code: 2}).exitStatus() != 2 {
		t.Error("a genuine failure lost its exit code")
	}
	s := e.apply(State{Step: StepCollecting, Stopping: true})
	if s.Step != StepDone || !s.Cancelled || s.Stopping {
		t.Errorf("final state = %+v", s)
	}
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

type errStub struct{}

func (errStub) Error() string { return "refused" }
