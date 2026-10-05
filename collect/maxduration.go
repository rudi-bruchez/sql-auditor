package collect

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// errMaxDurationReached is the cause the bound's context is cancelled with.
// Every question this package asks about the bound is whether a context's
// first cause is this value: a context keeps the first cause it was cancelled
// with, so the answer does not change when the operator stops the run after
// the bound fired, and is not the bound's when the stop came first.
var errMaxDurationReached = errors.New("the collection reached its maximum duration")

// settled returns ctx once its state can be read. A context whose deadline
// has passed by the clock is cancelled an instant later, by a runtime timer,
// and a dial bounded by that deadline does not wait for it: net.Dialer turns
// the deadline into a socket deadline and returns i/o timeout, which
// errors.Is(err, context.DeadlineExceeded), while ctx.Err() and
// context.Cause(ctx) are still nil. Read in that instant, a Connect the bound
// cut reads as an unreachable instance (exit 1, no flag; measured on
// 5 October 2026). So the fact is read only once Done is closed when the
// deadline has passed. It needs a deadline that keeps its monotonic reading
// (no Round(0), UTC() or Truncate on it). The wait is the timer's lag, microseconds; a context
// with no deadline, or one not yet passed, is returned at once.
func settled(ctx context.Context) context.Context {
	if d, ok := ctx.Deadline(); ok && !time.Now().Before(d) {
		<-ctx.Done()
	}
	return ctx
}

// boundReached says whether the bound fired before anything else cancelled
// bound. The cause and not bound.Err(): a ctrl-c cancels bound too, with the
// cause context.Canceled. Through settled, since every caller asks right after
// a step returned an error, and a failed dial returns before the bound's timer
// has run.
func boundReached(bound context.Context) bool {
	return context.Cause(settled(bound)) == errMaxDurationReached
}

// maxDurationText is the one sentence every message of this feature is built
// on. The cause is a constant and the deadline an instant, so neither carries
// the duration the operator set, and the caller passes it.
func maxDurationText(limit time.Duration) string {
	return "the collection reached its maximum duration of " + formatCeiling(limit)
}

// maxDurationOr writes the words of a failed server call, and only the words:
// whether the bound cut the run is decided elsewhere, from the call's cause,
// and never from what this returns.
//
// The order is the rule. A dead run context is the operator's stop, which
// recordUnitFailure files and drops. A call whose first cause is not the
// bound was stopped by something else first (its own deadline, the blocking
// watch), and its error keeps its words even when the bound passed during the
// driver's wait for the cancellation. A SQL Server error number is a failure
// of the server's own, as outOfTime already rules for its deadline. call is
// the context the failing call ran on, the innermost one, never the unit's.
func maxDurationOr(parent, call context.Context, limit time.Duration, err error) error {
	if err == nil || parent.Err() != nil {
		return err
	}
	if context.Cause(call) != errMaxDurationReached {
		return err
	}
	if sqlErrorNumber(err) != 0 {
		return err
	}
	return fmt.Errorf("stopped when %s: %w", maxDurationText(limit), err)
}

// maxDurationSkipReason is the reason of every unit the bound kept from
// starting: one string per run, because MANIFEST.txt groups on it.
func maxDurationSkipReason(limit time.Duration) string {
	return maxDurationText(limit) + " before this collector started"
}

// skipBefore is the loop's three questions before a unit, in their order: a
// database where the watch cancelled a collector, a database found dropped,
// then the bound. byBound says the bound answered; asking boundReached again
// after this returns would claim a held-back unit for the bound once the
// bound has passed, and count it in the note while MANIFEST.txt files it
// under its own reason.
func skipBefore(cancelledOn map[string]string, droppedOn map[string]bool, bound context.Context, limit time.Duration, target string) (reason string, byBound, skip bool) {
	if r, ok := heldBack(cancelledOn, target); ok {
		return r, false, true
	}
	if r, ok := droppedBefore(droppedOn, target); ok {
		return r, false, true
	}
	if boundReached(bound) {
		return maxDurationSkipReason(limit), true, true
	}
	return "", false, false
}

// maxDurationNote is the one line the command line prints after the loop of
// a run the bound cut. notStarted counts the units skipped for the bound
// (skipBefore's byBound), so that it agrees with MANIFEST.txt's grouped
// entry; stopped says a unit was cut. Its second half depends on what the
// bound cut, since a bound that passed between units stopped nothing.
func maxDurationNote(limit time.Duration, notStarted int, stopped bool) string {
	n := "note: " + maxDurationText(limit) + "; "
	switch notStarted {
	case 0:
		return n + "the collector running then was stopped"
	case 1:
		n += "1 collector was not started"
	default:
		n += fmt.Sprintf("%d collectors were not started", notStarted)
	}
	if stopped {
		n += ", and the one running then was stopped"
	}
	return n
}

// pauseHook is a test seam, nil outside tests. Run calls it at five named
// points, so that a test can wait there on bound.Done(), which places the
// bound exactly at that point, and then cancel the run's context, which
// places a stop after it. Nothing outside tests sets it; each call costs a
// nil check.
var pauseHook func(point string, bound context.Context)

func pause(point string, bound context.Context) {
	if pauseHook != nil {
		pauseHook(point, bound)
	}
}
