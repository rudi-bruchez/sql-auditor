package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/rudi-bruchez/sql-auditor/collect"
)

// progress is the collect.Observer for the command line. The wizard has had one
// since the Observer interface existed; `sql-auditor collect` had none, so a run
// that takes four minutes said nothing at all between its version banner and its
// archive path, and the operator had no way to tell a slow collector from a
// hung one.
//
// It writes to stderr and never to stdout. `sql-auditor collect | tail -1` is
// how a script picks up the archive path, and that contract is older than this
// type.
//
// Everything it needs from the world is injected: the writer, whether that
// writer is a terminal, the width, and the clock. None of the four can be had
// from a test otherwise, and a progress display that is only exercised by
// running the program against a real server is one that breaks quietly.
type progress struct {
	out   io.Writer
	tty   bool
	width func() int
	now   func() time.Time

	// The ticker goroutine repaints between events, so the mutex guards
	// everything below it. Without the ticker there would be nothing to guard:
	// collect calls the observer from one goroutine.
	mu      sync.Mutex
	start   time.Time
	planned int
	done    int
	label   string
	// dirty records that a transient line is on screen and must be erased
	// before anything permanent is written under it.
	dirty bool
}

func newProgress(out io.Writer, tty bool, width func() int, now func() time.Time) *progress {
	return &progress{out: out, tty: tty, width: width, now: now}
}

// elapsed formats a duration for a column that is read at a glance. Seconds
// disappear past the hour: a run in its fourth hour is watched for whether it
// is still moving, and the two columns are worth more to the collector's name.
func elapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	default:
		return fmt.Sprintf("%dh%02dm", s/3600, (s%3600)/60)
	}
}

// gauge is the line itself, as a pure function of what it displays. The
// numerator is padded to the width of the denominator so the columns after it
// hold still as the count gains digits, and the whole line is cut one column
// short of the terminal: the last cell is where the cursor sits, and a line
// that filled it would wrap and leave its tail behind at the next repaint.
func gauge(done, planned int, since time.Duration, label string, width int) string {
	var b strings.Builder
	if planned > 0 {
		digits := len(fmt.Sprint(planned))
		fmt.Fprintf(&b, "[%*d/%d] %3d%% ", digits, done, planned, done*100/planned)
	}
	fmt.Fprintf(&b, "%6s  %s", elapsed(since), label)
	line := b.String()
	// A width of 0 is what GetSize reports while an RDP window is being
	// dragged, and it must not become a negative bound.
	if width > 1 && len(line) > width-1 {
		line = line[:width-1]
	}
	return line
}

// paint rewrites the transient line. It is a no-op without a terminal, where
// there is no line to rewrite — a redirected stream gets one plain line per
// finished unit instead, from UnitDone.
//
// The caller holds the mutex.
func (p *progress) paint() {
	if !p.tty {
		return
	}
	line := gauge(p.done, p.planned, p.now().Sub(p.start), p.label, p.width())
	// Erase to the end of the line rather than padding with spaces: the
	// sequence is one the console already understands — the wizard could not
	// have drawn a frame otherwise — and it costs nothing on a narrow window.
	fmt.Fprintf(p.out, "\r\x1b[K%s\r", line)
	p.dirty = true
}

// clear takes the transient line off the screen so that something permanent can
// be written where it was. The caller holds the mutex.
func (p *progress) clear() {
	if p.tty && p.dirty {
		fmt.Fprint(p.out, "\r\x1b[K")
		p.dirty = false
	}
}

// Tick repaints with nothing else having happened, which is what keeps the
// clock moving while one collector holds the connection for a minute. That is
// the case the whole display exists for: a frozen number in front of a frozen
// instance is the symptom, not the diagnosis.
func (p *progress) Tick() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.start.IsZero() {
		p.paint()
	}
}

// StartTicking runs the repaint on its own goroutine and returns the stop. It
// is separate from the constructor so that every test drives the display
// synchronously: a goroutine writing into the buffer a test is reading would
// make the whole file flaky, and none of these tests needs one.
func (p *progress) StartTicking(every time.Duration) func() {
	if !p.tty {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				p.Tick()
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}

func (p *progress) Planned(units int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.planned = units
	p.start = p.now()
	p.label = "starting"
	p.paint()
}

func (p *progress) UnitStarted(script, database string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.label = unitLabel(script, database)
	p.paint()
}

func (p *progress) UnitDone(script, database string, bytes int64, d time.Duration, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done++
	if err != nil {
		// The one thing that must outlive the next repaint. Everything else the
		// gauge says is replaced a second later; a failure is a fact about this
		// run, and the manifest recording it too is no help to somebody watching
		// the screen decide whether to let it finish.
		p.clear()
		fmt.Fprintf(p.out, "!! %s: %v\n", unitLabel(script, database), err)
	} else if !p.tty {
		fmt.Fprintf(p.out, "[%d/%d] %s  %s  %s\n",
			p.done, p.planned, unitLabel(script, database),
			elapsed(d.Round(time.Second)), collect.HumanBytes(bytes))
	}
	p.paint()
}

// ScriptSkipped is deliberately silent. A run narrowed by QUERY_STORE_DB_INCLUDE
// skips one script per database, and naming each of them would bury the thing
// actually being waited on. The manifest lists every skip with its reason, which
// is where that belongs.
func (p *progress) ScriptSkipped(script, database, reason string) {}

func (p *progress) Phase(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.label = name
	p.paint()
}

// Finished leaves the line clean and says nothing. collect.Run prints the
// summary and the archive path to stdout immediately afterwards, and a second
// account of the same run on stderr would only invite the two to disagree.
func (p *progress) Finished(cancelled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clear()
}

// unitLabel is how a unit is named on one line: the script, and the database
// when there is one. An instance-scoped collector has no target, and "(  )"
// after its name would be a column of nothing on two thirds of the run.
func unitLabel(script, database string) string {
	if database == "" {
		return script
	}
	return script + " (" + database + ")"
}
