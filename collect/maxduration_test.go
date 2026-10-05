package collect

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
)

// passedBound is a bound that has already fired, built as Run builds it.
func passedBound(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithDeadlineCause(parent, time.Now().Add(-time.Second), errMaxDurationReached)
}

// pastDeadline is a context whose deadline has passed by the clock while its
// Done is still open. It holds open, for as long as a test needs, the instant
// between a bound's deadline and the runtime timer that cancels the bound: a
// dial bounded by that deadline fails with i/o timeout inside that instant,
// while Err() and Cause() are still nil (measured on 5 October 2026, 200 dials
// out of 200 once the deadline had passed, and 1 run in 300 of criterion 5a's
// test on the reviewer's prototype).
type pastDeadline struct {
	context.Context
	at time.Time
}

func (p pastDeadline) Deadline() (time.Time, bool) { return p.at, true }

// boundReached must not answer while the bound's deadline has passed and its
// Done is still open: it would answer false about a bound that is firing, and
// a dial the bound cut would be filed as an unreachable instance. Deterministic:
// the bound fires only after the answer has been waited for, so an answer given
// before is always the wrong one.
func TestBoundReachedWaitsForTheBoundsTimerOnceItsDeadlineHasPassed(t *testing.T) {
	inner, fire := context.WithCancelCause(context.Background())
	defer fire(nil)
	bound := pastDeadline{inner, time.Now().Add(-time.Millisecond)}
	answer := make(chan bool, 1)
	go func() { answer <- boundReached(bound) }()
	select {
	case got := <-answer:
		t.Fatalf("boundReached answered %v before the bound's Done closed", got)
	case <-time.After(100 * time.Millisecond):
	}
	fire(errMaxDurationReached)
	if !<-answer {
		t.Error("boundReached answered false once the bound fired")
	}
	if boundReached(context.Background()) {
		t.Error("a context with no deadline reads as the bound")
	}
}

func TestMaxDurationTextUsesTheCeilingsFormat(t *testing.T) {
	for limit, want := range map[time.Duration]string{
		2 * time.Hour:   "the collection reached its maximum duration of 2h00m (7200 s)",
		2 * time.Second: "the collection reached its maximum duration of 0m02s (2 s)",
		time.Minute:     "the collection reached its maximum duration of 1m00s (60 s)",
	} {
		if got := maxDurationText(limit); got != want {
			t.Errorf("maxDurationText(%s) = %q, want %q", limit, got, want)
		}
	}
}

// maxDurationOr writes the words, and only the words, in four steps: a dead
// run context, a call whose first cause is not the bound, a SQL Server error
// number, and only then the bound's sentence. Each row below is built with
// contexts in the order the case names, because the order is what the rows
// test.
func TestMaxDurationOrNamesTheBoundOnlyWhenTheCallsFirstCauseIsTheBound(t *testing.T) {
	const limit = 2 * time.Hour
	bare := context.DeadlineExceeded
	type row struct {
		name    string
		build   func() (parent, call context.Context, done func())
		err     error
		changed string // "" means err comes back unchanged
	}
	rows := []row{
		{"a stop after the bound", func() (context.Context, context.Context, func()) {
			run, stop := context.WithCancel(context.Background())
			bound, cancelBound := passedBound(run)
			unit, cancelUnit := context.WithCancelCause(bound)
			call, cancelCall := context.WithTimeout(unit, time.Hour)
			<-call.Done()
			stop() // the bound first, then the operator: Cause(call) stays the bound's
			return run, call, func() { cancelCall(); cancelUnit(nil); cancelBound() }
		}, bare, ""},
		{"the watch first", func() (context.Context, context.Context, func()) {
			run := context.Background()
			bound, cancelBound := context.WithDeadlineCause(run, time.Now().Add(50*time.Millisecond), errMaxDurationReached)
			unit, cancelUnit := context.WithCancelCause(bound)
			call, cancelCall := context.WithTimeout(unit, time.Hour)
			cancelUnit(&blockedError{})
			<-bound.Done()
			return run, call, func() { cancelCall(); cancelBound() }
		}, bare, ""},
		{"the call's own timeout, the bound an hour away", func() (context.Context, context.Context, func()) {
			run := context.Background()
			bound, cancelBound := context.WithDeadlineCause(run, time.Now().Add(time.Hour), errMaxDurationReached)
			unit, cancelUnit := context.WithCancelCause(bound)
			call, cancelCall := context.WithTimeout(unit, time.Millisecond)
			<-call.Done()
			return run, call, func() { cancelCall(); cancelUnit(nil); cancelBound() }
		}, bare, ""},
		{"straddling: the call's own timeout, then the bound", func() (context.Context, context.Context, func()) {
			run := context.Background()
			bound, cancelBound := context.WithDeadlineCause(run, time.Now().Add(100*time.Millisecond), errMaxDurationReached)
			unit, cancelUnit := context.WithCancelCause(bound)
			call, cancelCall := context.WithTimeout(unit, time.Millisecond)
			<-call.Done()
			<-bound.Done() // the driver's wait for the cancellation
			return run, call, func() { cancelCall(); cancelUnit(nil); cancelBound() }
		}, bare, ""},
		{"a SQL Server error number with the bound", func() (context.Context, context.Context, func()) {
			run := context.Background()
			bound, cancelBound := passedBound(run)
			unit, cancelUnit := context.WithCancelCause(bound)
			call, cancelCall := context.WithTimeout(unit, time.Hour)
			return run, call, func() { cancelCall(); cancelUnit(nil); cancelBound() }
		}, mssql.Error{Number: 911, Message: "Database 'SALESDB' does not exist."}, ""},
		{"a bare context error after the bound", func() (context.Context, context.Context, func()) {
			run := context.Background()
			bound, cancelBound := passedBound(run)
			unit, cancelUnit := context.WithCancelCause(bound)
			call, cancelCall := context.WithTimeout(unit, time.Hour)
			return run, call, func() { cancelCall(); cancelUnit(nil); cancelBound() }
		}, bare, "stopped when the collection reached its maximum duration of 2h00m (7200 s): context deadline exceeded"},
		{"a driver error of other words after the bound", func() (context.Context, context.Context, func()) {
			run := context.Background()
			bound, cancelBound := passedBound(run)
			unit, cancelUnit := context.WithCancelCause(bound)
			call, cancelCall := context.WithTimeout(unit, time.Hour)
			return run, call, func() { cancelCall(); cancelUnit(nil); cancelBound() }
		}, errors.New("Invalid TDS stream: did not get cancellation confirmation from the server (current response: context deadline exceeded)"),
			"stopped when the collection reached its maximum duration of 2h00m (7200 s): Invalid TDS stream: did not get cancellation confirmation from the server (current response: context deadline exceeded)"},
	}
	for _, r := range rows {
		parent, call, done := r.build()
		got := maxDurationOr(parent, call, limit, r.err)
		done()
		want := r.changed
		if want == "" {
			want = r.err.Error()
		}
		if got == nil || got.Error() != want {
			t.Errorf("%s: got %v, want %q", r.name, got, want)
		}
	}
	if maxDurationOr(context.Background(), context.Background(), limit, nil) != nil {
		t.Error("a nil error came back non-nil")
	}
}

// The same orders through outOfTime, the query path's one classification:
// the bound first names the bound, the call's own @timeout first names the
// @timeout, whatever happened after.
func TestOutOfTimeTellsTheBoundFromTheUnitsOwnTimeout(t *testing.T) {
	const limit, timeout = 2 * time.Hour, 30 * time.Minute
	bare := context.DeadlineExceeded

	run := context.Background()
	bound, cancelBound := passedBound(run)
	unit, cancelUnit := context.WithCancelCause(bound)
	call, cancelCall := context.WithTimeout(unit, timeout)
	got := outOfTime(run, call, timeout, limit, "@timeout", bare)
	cancelCall()
	cancelUnit(nil)
	cancelBound()
	if want := "stopped when the collection reached its maximum duration of 2h00m (7200 s): context deadline exceeded"; got.Error() != want {
		t.Errorf("the bound first: %q, want %q", got, want)
	}

	bound, cancelBound = context.WithDeadlineCause(run, time.Now().Add(100*time.Millisecond), errMaxDurationReached)
	unit, cancelUnit = context.WithCancelCause(bound)
	call, cancelCall = context.WithTimeout(unit, time.Millisecond)
	<-call.Done()
	<-bound.Done()
	got = outOfTime(run, call, timeout, limit, "@timeout", bare)
	cancelCall()
	cancelUnit(nil)
	cancelBound()
	if want := "still running when @timeout of 30m0s expired: context deadline exceeded"; got.Error() != want {
		t.Errorf("own timeout, then the bound: %q, want %q", got, want)
	}

	stopped, stop := context.WithCancel(run)
	bound, cancelBound = passedBound(stopped)
	unit, cancelUnit = context.WithCancelCause(bound)
	call, cancelCall = context.WithTimeout(unit, timeout)
	stop()
	got = outOfTime(stopped, call, timeout, limit, "@timeout", bare)
	cancelCall()
	cancelUnit(nil)
	cancelBound()
	if got != bare {
		t.Errorf("a stop after the bound: %v, want the error unchanged", got)
	}

	bound, cancelBound = passedBound(run)
	unit, cancelUnit = context.WithCancelCause(bound)
	call, cancelCall = context.WithTimeout(unit, timeout)
	boom := mssql.Error{Number: 1222, Message: "Lock request time out period exceeded."}
	got = outOfTime(run, call, timeout, limit, "@timeout", boom)
	cancelCall()
	cancelUnit(nil)
	cancelBound()
	if got.Error() != boom.Error() || strings.Contains(got.Error(), "stopped when") {
		t.Errorf("a SQL error with the bound: %v, want it unchanged", got)
	}
}

// The watch's and the drop's reasons are more specific than the bound's, and
// scopeLost matches on them, so they are asked first. byBound says which
// question answered, never whether the bound has passed.
func TestSkipBeforeAsksTheBoundLast(t *testing.T) {
	passed, cancelPassed := passedBound(context.Background())
	defer cancelPassed()
	far, cancelFar := context.WithDeadlineCause(context.Background(), time.Now().Add(time.Hour), errMaxDurationReached)
	defer cancelFar()
	stopped, stop := context.WithCancel(context.Background())
	stop()
	cancelledOn := map[string]string{"SALESDB": "70.schema/055.page-density.sql"}
	droppedOn := map[string]bool{"HRDB": true}
	watchReason := "the blocking watch cancelled 70.schema/055.page-density.sql on this database"
	for _, c := range []struct {
		name    string
		bound   context.Context
		limit   time.Duration
		target  string
		reason  string
		byBound bool
		skip    bool
	}{
		{"held back, bound passed", passed, 2 * time.Hour, "SALESDB", watchReason, false, true},
		{"held back, bound not passed", far, 2 * time.Hour, "SALESDB", watchReason, false, true},
		{"dropped, bound passed", passed, 2 * time.Hour, "HRDB", skipDroppedDuringRun, false, true},
		{"dropped, bound not passed", far, 2 * time.Hour, "HRDB", skipDroppedDuringRun, false, true},
		{"another database, bound passed", passed, 2 * time.Hour, "OPSDB",
			"the collection reached its maximum duration of 2h00m (7200 s) before this collector started", true, true},
		{"an instance unit, bound passed", passed, 90 * time.Minute, "",
			"the collection reached its maximum duration of 1h30m (5400 s) before this collector started", true, true},
		{"another database, bound not passed", far, 2 * time.Hour, "OPSDB", "", false, false},
		{"another database, a ctrl-c", stopped, 2 * time.Hour, "OPSDB", "", false, false},
	} {
		reason, byBound, skip := skipBefore(cancelledOn, droppedOn, c.bound, c.limit, c.target)
		if reason != c.reason || byBound != c.byBound || skip != c.skip {
			t.Errorf("%s: got (%q, %v, %v), want (%q, %v, %v)", c.name, reason, byBound, skip, c.reason, c.byBound, c.skip)
		}
	}
}

func TestMaxDurationNoteSaysWhatTheBoundCut(t *testing.T) {
	const pre = "note: the collection reached its maximum duration of 2h00m (7200 s); "
	for _, c := range []struct {
		notStarted int
		stopped    bool
		want       string
	}{
		{198, true, pre + "198 collectors were not started, and the one running then was stopped"},
		{198, false, pre + "198 collectors were not started"},
		{1, false, pre + "1 collector was not started"},
		{0, true, pre + "the collector running then was stopped"},
		{2, false, pre + "2 collectors were not started"},
	} {
		if got := maxDurationNote(2*time.Hour, c.notStarted, c.stopped); got != c.want {
			t.Errorf("(%d, %v) = %q, want %q", c.notStarted, c.stopped, got, c.want)
		}
	}
}

// The tokens of the line scripts parse, in the order things happened: the
// bound before the stop, since a stop first would have kept the bound from
// being recorded.
func TestSummaryTailOrdersTheBoundBeforeTheStop(t *testing.T) {
	for _, c := range []struct {
		name             string
		partial          int
		bound, cancelled bool
		want             string
	}{
		{"nothing", 0, false, false, ""},
		{"partial units", 3, false, false, ", 3 partial"},
		{"the bound", 0, true, false, ", max duration reached"},
		{"a stop", 0, false, true, ", cancelled"},
		{"the bound then a stop", 0, true, true, ", max duration reached, cancelled"},
		{"partial units, the bound and a stop", 2, true, true, ", 2 partial, max duration reached, cancelled"},
	} {
		m := &Manifest{PartialUnits: c.partial}
		m.Run.MaxDurationReached, m.Run.Cancelled = c.bound, c.cancelled
		if got := summaryTail(m); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// Once the bound has fired no collector will start, so a warning that
// nothing will cancel one describes a risk the run no longer runs. The
// question is the bound, asked when the warning would be written, and never
// the reason's text: a watch whose start failed after the bound keeps its own
// reason.
func TestWatchOffNoticeKeysOnTheBoundNotTheReason(t *testing.T) {
	reasons := []string{
		"not started: " + maxDurationText(2*time.Hour),
		"the collection session's id could not be read: context deadline exceeded",
		"its connection could not be opened: dial tcp 192.0.2.1:1433: i/o timeout",
	}
	for _, r := range reasons {
		if w, n := watchOffNotice(r, true); w != "" || n != "" {
			t.Errorf("bound fired, %q: got (%q, %q), want nothing", r, w, n)
		}
	}
	for _, r := range reasons[1:] {
		w, n := watchOffNotice(r, false)
		if want := "the blocking watch is off, " + r + ": nothing will cancel a collector that other sessions are waiting on"; w != want {
			t.Errorf("bound not fired, warning %q, want %q", w, want)
		}
		if want := "note: the blocking watch is off, " + r; n != want {
			t.Errorf("bound not fired, note %q, want %q", n, want)
		}
	}
}
