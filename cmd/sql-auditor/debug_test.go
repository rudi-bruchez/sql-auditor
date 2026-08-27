package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// clock returns a now() that advances by the supplied steps, one per call, so
// a test can state the timeline it wants instead of sleeping for it. The last
// step repeats, which keeps a test from having to count the calls a change to
// the code under test might add.
func clock(steps ...time.Duration) func() time.Time {
	base := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	i := 0
	return func() time.Time {
		d := steps[i]
		if i < len(steps)-1 {
			i++
		}
		return base.Add(d)
	}
}

func TestADisabledDebugLogWritesNothingAndPanicsNowhere(t *testing.T) {
	// The nil log is the ordinary case — every run without SQL_AUDITOR_DEBUG —
	// and it reaches exactly the same call sites as an enabled one. If any of
	// them needed a guard, this test would be a panic rather than a failure.
	var d *debugLog
	d.printf("this must not be written")
	if d.enabled() {
		t.Error("a nil log reports itself enabled")
	}
	if w := d.Writer(); w != nil {
		t.Errorf("a nil log handed out a writer: %#v", w)
	}
}

func TestNewDebugLogWithNoWriterIsDisabled(t *testing.T) {
	// The trigger is resolved once, in run(), and turns into either a writer or
	// nil. Everything downstream asks the log, not the environment.
	if d := newDebugLog(nil, time.Now); d.enabled() {
		t.Error("newDebugLog(nil) produced an enabled log")
	}
}

func TestDebugLinesCarryTheTimeSinceProcessStart(t *testing.T) {
	var buf bytes.Buffer
	d := newDebugLog(&buf, clock(0, 400*time.Microsecond, 12*time.Millisecond))
	d.printf("start")
	d.printf("mode = %v", ModeUsage)

	want := []string{
		"debug   +0.4ms  start",
		"debug  +12.0ms  mode = usage",
	}
	got := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(got), len(want), buf.String())
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

func TestEveryModeHasAWordRatherThanANumber(t *testing.T) {
	// The mode decision is the first interesting line of the timeline, and "2"
	// sends the reader to the source to find out what it means.
	cases := []struct {
		in   Mode
		want string
	}{
		{ModeSubcommand, "subcommand"},
		{ModeTUI, "wizard"},
		{ModeUsage, "usage"},
		{Mode(9), "Mode(9)"},
	}
	for _, c := range cases {
		if got := c.in.String(); got != c.want {
			t.Errorf("Mode(%d).String() = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSinceStartStaysReadableAtEveryScale(t *testing.T) {
	// The column is read for the GAP between two lines, so the unit has to
	// change where the number would otherwise stop being read at a glance.
	// Sub-millisecond resolution is kept: the whole question this mode answers
	// is whether the time went into the program at all, and a program that
	// finishes in two milliseconds has answered it.
	cases := []struct {
		in   time.Duration
		want string
	}{
		{-time.Second, "+0.0ms"},
		{0, "+0.0ms"},
		{400 * time.Microsecond, "+0.4ms"},
		{999 * time.Millisecond, "+999.0ms"},
		{time.Second, "+1.00s"},
		{15 * time.Second, "+15.00s"},
		{93*time.Second + 400*time.Millisecond, "+93.40s"},
	}
	for _, c := range cases {
		if got := sinceStart(c.in); got != c.want {
			t.Errorf("sinceStart(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTheDebugWriterStampsLinesRatherThanWrites(t *testing.T) {
	// collect and tui write to an io.Writer, not to this type, and they build a
	// line in as many calls as they please. A stamp per Write would put three
	// stamps on one line and one stamp on three.
	var buf bytes.Buffer
	d := newDebugLog(&buf, clock(0, time.Millisecond))
	w := d.Writer()

	if _, err := w.Write([]byte("one line\ntwo li")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("nes\r\nthree\n")); err != nil {
		t.Fatal(err)
	}

	want := "debug   +1.0ms  one line\n" +
		"debug   +1.0ms  two lines\n" +
		"debug   +1.0ms  three\n"
	if buf.String() != want {
		t.Errorf("got:\n%swant:\n%s", buf.String(), want)
	}
}

func TestTheDebugWriterHoldsAnUnfinishedLineBack(t *testing.T) {
	// A tail with no newline is not a line yet. Stamping it early would split
	// one message across two stamps and invent a gap that never happened.
	var buf bytes.Buffer
	d := newDebugLog(&buf, clock(0, time.Millisecond))
	if _, err := d.Writer().Write([]byte("no newline here")); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("an unfinished line was written: %q", buf.String())
	}
}

func TestDebugModeIsAskedForByTheEnvironmentOrByTheFlag(t *testing.T) {
	// The environment variable is not a convenience. The run this mode exists
	// to explain is the one with NO arguments, where run() reads os.Args[1] as
	// a command — so the variable is the only trigger that survives being
	// typed with nothing else on the line.
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "SQL_AUDITOR_DEBUG" {
				return v
			}
			return ""
		}
	}
	cases := []struct {
		name string
		args []string
		env  func(string) string
		want bool
	}{
		{"nothing asked for", nil, noEnv, false},
		{"the variable set", nil, env("1"), true},
		// Same rule as SQL_AUDITOR_NO_TUI: the test is on the variable being
		// set at all, so "false" turns it ON. Written down in usage() too.
		{"the variable set to false", nil, env("false"), true},
		{"the variable set to empty", nil, env(""), false},
		{"the double-dash flag", []string{"collect", "--debug"}, noEnv, true},
		{"the single-dash flag", []string{"check", "-debug"}, noEnv, true},
		{"the flag on its own", []string{"--debug"}, noEnv, true},
		// A value that merely contains the word is not the flag. `--server`
		// pointed at a host called "debug" must not turn the mode on.
		{"a value that looks like it", []string{"collect", "--server", "debug"}, noEnv, false},
		{"an unrelated command line", []string{"collect", "--all"}, noEnv, false},
	}
	for _, c := range cases {
		if got := debugRequested(c.args, c.env); got != c.want {
			t.Errorf("%s: debugRequested = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTheDebugSwitchIsNotMistakenForACommand(t *testing.T) {
	// `sql-auditor --debug` has to be the argument-less run with the log on,
	// because that run is the one the operator wants explained and there is
	// nowhere else to type the switch. Anywhere but the command position it is
	// an ordinary flag and the flag set owns it.
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nothing", nil, nil},
		{"on its own", []string{"--debug"}, nil},
		{"single dash, on its own", []string{"-debug"}, nil},
		{"in front of a command", []string{"--debug", "check"}, []string{"check"}},
		{"twice", []string{"--debug", "--debug"}, nil},
		{"after a command, left alone", []string{"collect", "--debug"}, []string{"collect", "--debug"}},
		{"not there at all", []string{"collect", "--all"}, []string{"collect", "--all"}},
	}
	for _, c := range cases {
		got := stripLeadingDebug(c.in)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: got %q, want %q", c.name, got, c.want)
				break
			}
		}
	}
}
