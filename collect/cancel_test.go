package collect

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// An operator who presses Ctrl-C is not an instance that went away, and the
// run has to say which of the two happened. Today it says the wrong one: the
// unit in flight fails with "context canceled", connAlive pings with that same
// dead context and finds nothing, the reconnect fails identically, and the run
// exits 1 — the code the README reserves for "the instance could not be
// reached". Whoever reads that manifest goes looking at a server that was
// answering perfectly.
//
// Nothing here needs a server: the decision is a pure function of the context
// and the error, which is exactly why it was worth extracting.

// The shape of what runUnit hands back on a cancelled context: the cause is
// wrapped by the time the loop sees it, so the decision cannot be made by
// matching on the message.
var cancelledUnitErr = fmt.Errorf("reconnect failed: %w", context.Canceled)

func TestCancellationIsNotReportedAsAnUnreachableInstance(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	code, err := cancellationOutcome(ctx, cancelledUnitErr)
	if code != 0 {
		t.Errorf("exit code = %d, want 0: a deliberate stop is neither an unreachable "+
			"instance (1) nor a refused configuration (2)", code)
	}
	if err != nil {
		t.Errorf("error = %v, want nil: the unit error only says the context was "+
			"cancelled, which the manifest already records as cancelled", err)
	}
}

// A cancelled context also covers a deadline that expired, which is the same
// decision: the caller set the bound, so the run stopping at it is not the
// instance failing to answer.
func TestCancellationIsNotReportedAsAnUnreachableInstanceOnADeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	<-ctx.Done()

	if code, err := cancellationOutcome(ctx, context.DeadlineExceeded); code != 0 || err != nil {
		t.Errorf("cancellationOutcome = (%d, %v), want (0, nil)", code, err)
	}
}

// The other half, and the one that a naive implementation gets wrong by
// testing the error instead of the context: a live context means the unit
// genuinely failed and nothing about it may be softened. The error must come
// back unchanged — not merely a similar one — because the caller records it in
// the manifest verbatim.
func TestALiveContextLeavesTheErrorAlone(t *testing.T) {
	want := errors.New("Invalid object name 'sys.dm_os_wait_stats'")

	code, got := cancellationOutcome(context.Background(), want)
	if code != 2 {
		t.Errorf("exit code = %d, want 2 (partial failure)", code)
	}
	if !errors.Is(got, want) {
		t.Errorf("error = %v, want the error that was passed in", got)
	}
}

// The placement is the whole point of this task, so it is what gets tested.
// Deciding after the ErrorEntry has been appended would leave a manifest that
// says exit_code 0, cancelled true, AND carries an error reading "context
// canceled" — three statements that cannot all be true, and the exact
// ambiguity this change exists to remove.
func TestACancelledRunRecordsNoUnitError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := NewManifest("sql-auditor", "0.18.0", "abc1234")

	exit, cancelled := recordUnitFailure(ctx, m, "80.workload/020.query-store.sql", "SALESDB", cancelledUnitErr)

	if !cancelled {
		t.Error("the run was not marked cancelled, so the loop would carry on to the next unit")
	}
	if exit != 0 {
		t.Errorf("exit code = %d, want 0", exit)
	}
	if len(m.Errors) != 0 {
		t.Errorf("the manifest carries %d error(s) for a run the operator stopped: %+v", len(m.Errors), m.Errors)
	}
	if !m.Run.Cancelled {
		t.Error("_run.json does not say the run was cancelled, so a short archive looks like a complete one")
	}
}

// The same function on a live context does the ordinary thing, so that moving
// the context test to the front has not quietly swallowed real failures.
func TestAFailedUnitIsStillRecordedOnALiveContext(t *testing.T) {
	m := NewManifest("sql-auditor", "0.18.0", "abc1234")
	cause := errors.New("permission denied")

	exit, cancelled := recordUnitFailure(context.Background(), m, "50.agent/020.job-steps.sql", "", cause)

	if cancelled {
		t.Error("a failed unit was reported as a cancellation")
	}
	if exit != 2 {
		t.Errorf("exit code = %d, want 2", exit)
	}
	if len(m.Errors) != 1 {
		t.Fatalf("the manifest carries %d error(s), want 1", len(m.Errors))
	}
	if got := m.Errors[0]; got.Script != "50.agent/020.job-steps.sql" || got.Message != cause.Error() {
		t.Errorf("error entry = %+v, want the script and the message it failed with", got)
	}
	if m.Run.Cancelled {
		t.Error("_run.json claims the operator stopped a run that failed on its own")
	}
}
