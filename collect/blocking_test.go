package collect

import (
	"errors"
	"strings"
	"testing"
)

// ready is the state of an instance that is losing time to locks and can say
// why. Each test below breaks exactly one of its facts.
func ready() BlockingReadiness {
	return BlockingReadiness{
		Probed:            true,
		LockWaitMS:        15377468623,
		LockWaitTasks:     2003569,
		MaxLockWaitMS:     12834600,
		TotalWaitMS:       1213920400999,
		ThresholdSeconds:  10,
		SessionsCapturing: 1,
		SessionsRunning:   1,
		WritesToFile:      1,
	}
}

func joined(lines []string) string { return strings.Join(lines, "\n") }

// An instance with no lock waits gets no paragraph. Saying "you are not
// capturing blocked process reports" to somebody with nothing to capture trains
// them to skip the section on the day it matters.
func TestBlockingNoticeStaysSilentWithoutLockWaits(t *testing.T) {
	r := ready()
	r.LockWaitMS = 0
	r.ThresholdSeconds = 0
	r.SessionsCapturing = 0
	if lines := BlockingNotice(r); lines != nil {
		t.Errorf("said something about an instance with no lock waits: %v", lines)
	}
}

// A probe that could not run must not be reported as a finding: "nothing is
// capturing blocking" and "we could not tell" are different statements.
func TestBlockingNoticeSeparatesUnknownFromNegative(t *testing.T) {
	got := joined(BlockingNotice(BlockingReadiness{Err: errors.New("permission denied")}))
	if !strings.Contains(got, "could not check") {
		t.Errorf("a failed probe reported as %q, want it named as unchecked", got)
	}
	if strings.Contains(got, BlockingHowTo) {
		t.Error("a failed probe produced advice about a state nobody established")
	}
}

func TestBlockingNoticeConfirmsAWorkingCapture(t *testing.T) {
	got := joined(BlockingNotice(ready()))
	if !strings.Contains(got, "ARE being captured") {
		t.Errorf("a working capture reported as %q", got)
	}
	if !strings.Contains(got, "--include-blocked-process-reports") {
		t.Error("a working capture did not say which option exports it")
	}
	if strings.Contains(got, BlockingHowTo) {
		t.Error("told an operator to create a session they already have")
	}
}

// The four ways a capture fails, each with its own line. They are not
// interchangeable: a session that exists and is stopped is a different job from
// one that does not exist, and a ring-buffer-only session looks correct in SSMS
// while being unexportable.
func TestBlockingNoticeNamesTheMissingPiece(t *testing.T) {
	for _, c := range []struct {
		name   string
		break_ func(*BlockingReadiness)
		want   string
	}{
		{"no session", func(r *BlockingReadiness) { r.SessionsCapturing, r.SessionsRunning, r.WritesToFile = 0, 0, 0 },
			"no Extended Events session subscribes"},
		{"stopped session", func(r *BlockingReadiness) { r.SessionsRunning = 0 },
			"none is running"},
		{"ring buffer only", func(r *BlockingReadiness) { r.WritesToFile = 0 },
			"ring buffer only"},
		{"threshold zero", func(r *BlockingReadiness) { r.ThresholdSeconds = 0 },
			"never fires at all"},
	} {
		r := ready()
		c.break_(&r)
		got := joined(BlockingNotice(r))
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: notice does not say %q:\n%s", c.name, c.want, got)
		}
		if !strings.Contains(got, BlockingHowTo) {
			t.Errorf("%s: a broken capture did not point at the script that fixes it", c.name)
		}
		if !strings.Contains(got, "will not make either change") {
			t.Errorf("%s: did not say the tool leaves the change to the DBA", c.name)
		}
	}
}

// A threshold of 0 is compounding rather than exclusive: a session can exist,
// run, write to a file and still never have received an event. Both lines have
// to appear, or the operator fixes one thing and comes back.
func TestBlockingNoticeReportsThresholdAndSessionTogether(t *testing.T) {
	r := ready()
	r.SessionsCapturing, r.SessionsRunning, r.WritesToFile = 0, 0, 0
	r.ThresholdSeconds = 0
	got := joined(BlockingNotice(r))
	if !strings.Contains(got, "no Extended Events session subscribes") || !strings.Contains(got, "never fires at all") {
		t.Errorf("only one of the two missing pieces was named:\n%s", got)
	}
}

// The totals are cumulative since the counters were cleared. A reader who does
// not know that reads a two-year total as a description of this afternoon.
func TestBlockingNoticeSaysTheTotalsAreCumulative(t *testing.T) {
	got := joined(BlockingNotice(ready()))
	if !strings.Contains(got, "accumulate since the instance started") {
		t.Errorf("the wait totals are printed without saying what period they cover:\n%s", got)
	}
}

func TestLockShareIsZeroRatherThanNaNOnAnIdleInstance(t *testing.T) {
	if got := (BlockingReadiness{Probed: true}).LockShare(); got != 0 {
		t.Errorf("LockShare on an instance with no recorded waits = %v, want 0", got)
	}
}
