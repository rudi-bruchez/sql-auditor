package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// debugEnvVar turns the timeline on. It is read from the process environment
// and is deliberately NOT a .env key, for the same reason SQL_AUDITOR_NO_TUI
// is not: that set is closed, an unrecognised key there is a hard failure, and
// this is not a connection setting.
const debugEnvVar = "SQL_AUDITOR_DEBUG"

// debugLog is the timeline of a run: one line per thing the program is about
// to do, stamped with the time since the process started.
//
// The stamp is the whole point, and it is what separates this from a verbose
// mode. A tool that prints what it is doing answers "what is it doing"; the
// question actually asked of this one is "what is it WAITING on", and only the
// gap between two stamps answers that. Counting from process start rather than
// from a wall clock means the reader subtracts nothing — and it makes the one
// gap the program cannot report visible by its absence: if the first line
// itself arrives seconds after the command was typed, the time went into
// loading twelve megabytes of executable past a virus scanner, and no amount
// of instrumentation inside this process would ever have shown it.
//
// A nil *debugLog is a disabled one, and every method tolerates it, so no call
// site carries an `if`. That is the same bargain collect.Options.Progress
// makes with a nil writer, and for the same reason: one code path, always
// taken, so the instrumented run and the ordinary one cannot drift apart.
type debugLog struct {
	// mu guards both the writer and the line buffer below. The wizard runs the
	// collection on its own goroutine while the main goroutine keeps painting,
	// so two goroutines really do write here; without this the mode meant to
	// explain a run would be the thing that corrupted it.
	mu    sync.Mutex
	w     io.Writer
	start time.Time
	now   func() time.Time
	// pending is the tail of a line the writer has been handed but has not
	// been given a newline for yet.
	pending bytes.Buffer
}

// newDebugLog returns a log writing to w, or a disabled one when w is nil.
// The clock is injected for the same reason it is everywhere else in this
// command: a timeline is untestable against the real one.
func newDebugLog(w io.Writer, now func() time.Time) *debugLog {
	if w == nil {
		return nil
	}
	return &debugLog{w: w, start: now(), now: now}
}

// enabled says whether anything is being recorded, for the few call sites that
// would otherwise do real work to build a message nobody will read.
func (d *debugLog) enabled() bool { return d != nil }

func (d *debugLog) printf(format string, args ...any) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.emit(fmt.Sprintf(format, args...))
}

// emit writes one stamped line. The caller holds the lock.
func (d *debugLog) emit(msg string) {
	fmt.Fprintf(d.w, "debug %8s  %s\n", sinceStart(d.now().Sub(d.start)), strings.TrimRight(msg, "\r\n"))
}

// setWriter changes where the timeline goes, once. The wizard uses it: while
// it owns the terminal, a stamped line written to stderr would land in the
// middle of a frame, so the log is held in memory and emptied afterwards.
func (d *debugLog) setWriter(w io.Writer) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.w = w
}

// Writer adapts the log to the plain io.Writer that collect and tui take. Nil
// when the log is disabled, which is exactly what those packages read as
// "write nothing".
func (d *debugLog) Writer() io.Writer {
	if d == nil {
		return nil
	}
	return debugWriter{d}
}

type debugWriter struct{ d *debugLog }

// Write stamps whole LINES, not writes. A caller builds a message in as many
// calls as it pleases and hands over as many lines at once as it pleases;
// stamping per write would put three stamps on one line and one stamp on
// three, and either invents a gap that did not happen or hides one that did.
func (w debugWriter) Write(b []byte) (int, error) {
	w.d.mu.Lock()
	defer w.d.mu.Unlock()
	w.d.pending.Write(b)
	for {
		line, err := w.d.pending.ReadString('\n')
		if err != nil {
			// No newline in what is left: it is the start of a line, not a
			// line. ReadString drained the buffer, so put the tail back and
			// wait for the rest.
			w.d.pending.Reset()
			w.d.pending.WriteString(line)
			return len(b), nil
		}
		w.d.emit(line)
	}
}

// sinceStart formats the elapsed column. Milliseconds with one decimal below a
// second, seconds with two above it: the number is read for the size of the
// gap before it, and "+15000.0ms" is a number the reader has to count digits
// on before it means fifteen seconds.
func sinceStart(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return fmt.Sprintf("+%.1fms", float64(d)/float64(time.Millisecond))
	}
	return fmt.Sprintf("+%.2fs", d.Seconds())
}

// debugRequested resolves the trigger, before the command line has been parsed
// and before anything has been decided.
//
// It has to be answered this early, and from the raw arguments rather than
// from the flag set, because the interesting run is the one with no arguments
// at all: run() reads os.Args[1] as a command name, so there is no parse to
// wait for and the earliest lines of the timeline — the mode decision, the
// .env lookup — happen before any flag set exists.
//
// The environment variable follows SQL_AUDITOR_NO_TUI exactly: any non-empty
// value turns the mode on, "false" and "0" included. That surprises people
// once, so usage() says it in words.
//
// Scanning raw arguments means a switch in VALUE position counts too:
// `collect --output-dir --debug` turns the mode on and then takes "--debug"
// for an output directory. That is accepted rather than fixed. The precise
// answer is to ask the parsed flag set — the way optionsFrom asks it for
// --env — and it costs the earliest lines of the timeline, which are the ones
// worth having; and the command line it gets wrong is already broken in a way
// the operator will see immediately, since the run writes into a directory
// called --debug. A wrong extra line of diagnostics is the cheap half of that.
func debugRequested(args []string, env func(string) string) bool {
	if env(debugEnvVar) != "" {
		return true
	}
	for _, a := range args {
		if isDebugSwitch(a) {
			return true
		}
	}
	return false
}

func isDebugSwitch(s string) bool { return s == "--debug" || s == "-debug" }

// stripLeadingDebug takes the switch off the front of the argument list.
//
// `sql-auditor --debug` must mean "the argument-less run, with the log on" and
// not "a command called --debug": that run is precisely the one an operator
// reaches for this mode to explain, and answering it with `"--debug" is an
// option, not a command` would leave the environment variable as the only way
// in. Past the command position it is left alone — there the flag set owns it,
// which is what puts it in the help.
func stripLeadingDebug(args []string) []string {
	for len(args) > 0 && isDebugSwitch(args[0]) {
		args = args[1:]
	}
	if len(args) == 0 {
		return nil
	}
	return args
}
