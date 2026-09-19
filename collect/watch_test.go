package collect

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// A watch whose poll answers from a script, one sample per tick. It is never
// started: the tests drive tick directly, so nothing depends on the clock.
func scriptedWatch(samples ...waitSample) *blockingWatch {
	i := 0
	return newBlockingWatch(func(ctx context.Context, spid int) (waitSample, error) {
		if i >= len(samples) {
			return waitSample{}, nil
		}
		s := samples[i]
		i++
		return s, nil
	}, time.Second, watchCancelAfter)
}

func waiter(ms int) waitSample {
	return waitSample{Session: 78, WaitType: "LCK_M_SCH_M",
		Waited: time.Duration(ms) * time.Millisecond, Resource: "objectlock objid=1"}
}

func TestWatchCancelsTheArmedUnitAtTheLimit(t *testing.T) {
	w := scriptedWatch(waiter(1000), waiter(4999), waiter(5000), waiter(6000))
	ctx, cancel := context.WithCancelCause(context.Background())
	w.arm(55, cancel)

	w.tick()
	w.tick()
	if ctx.Err() != nil {
		t.Fatalf("cancelled at 4999 ms, under the limit")
	}
	w.tick()
	var be *blockedError
	if !errors.As(context.Cause(ctx), &be) {
		t.Fatalf("cause = %v, want a *blockedError", context.Cause(ctx))
	}
	if be.Sample.Session != 78 || be.Sample.Waited != 5*time.Second {
		t.Errorf("cause carries %+v", be.Sample)
	}
	w.tick()
	worst, fired := w.disarm()
	if !fired || worst.Waited != 6*time.Second {
		t.Errorf("disarm = %+v, %v; want the 6000 ms sample and fired", worst, fired)
	}
}

func TestWatchUnderTheLimitReportsTheLongestWait(t *testing.T) {
	w := scriptedWatch(waiter(1200), waiter(3400), waitSample{}, waiter(2100))
	ctx, cancel := context.WithCancelCause(context.Background())
	w.arm(55, cancel)
	for range 4 {
		w.tick()
	}
	worst, fired := w.disarm()
	if fired || ctx.Err() != nil {
		t.Fatalf("fired under the limit")
	}
	if worst.Waited != 3400*time.Millisecond {
		t.Errorf("worst = %v, want 3.4s", worst.Waited)
	}
	// The next unit starts clean.
	w.arm(55, cancel)
	if worst, _ := w.disarm(); worst.seen() {
		t.Errorf("a new arming inherited %+v", worst)
	}
}

// The poll runs outside the lock. A unit that finishes while it is in flight,
// and the next one armed, must not receive a cancel meant for the first.
func TestWatchDropsASampleThatOutlivedItsUnit(t *testing.T) {
	var w *blockingWatch
	next, nextCancel := context.WithCancelCause(context.Background())
	w = newBlockingWatch(func(ctx context.Context, spid int) (waitSample, error) {
		w.disarm()
		w.arm(spid+1, nextCancel) // a reconnect gave the next unit a new session
		return waiter(9000), nil
	}, time.Second, watchCancelAfter)
	_, cancel := context.WithCancelCause(context.Background())
	w.arm(55, cancel)
	w.tick()
	if next.Err() != nil {
		t.Fatalf("the next unit was cancelled by a sample about the previous one")
	}
	// Same session, disarmed and not rearmed: nothing to cancel either.
	w = newBlockingWatch(func(ctx context.Context, spid int) (waitSample, error) {
		w.disarm()
		return waiter(9000), nil
	}, time.Second, watchCancelAfter)
	w.arm(55, nextCancel)
	w.tick()
	if next.Err() != nil {
		t.Fatalf("a disarmed unit was cancelled")
	}
}

func TestWatchIdleDoesNotPoll(t *testing.T) {
	polled := false
	w := newBlockingWatch(func(ctx context.Context, spid int) (waitSample, error) {
		polled = true
		return waitSample{}, nil
	}, time.Second, watchCancelAfter)
	if !w.tick() || polled {
		t.Errorf("polled with nothing armed")
	}
}

func TestWatchStopsOnAFailedPoll(t *testing.T) {
	w := newBlockingWatch(func(ctx context.Context, spid int) (waitSample, error) {
		return waitSample{}, errors.New("mssql: VIEW SERVER PERFORMANCE STATE permission was denied")
	}, time.Second, watchCancelAfter)
	_, cancel := context.WithCancelCause(context.Background())
	w.arm(55, cancel)
	if w.tick() {
		t.Fatalf("the watch went on after a failed poll")
	}
	if r := w.stoppedReason(); !strings.Contains(r, "permission was denied") {
		t.Errorf("stoppedReason = %q", r)
	}
}

// A half-open socket hangs rather than fails. The poll's deadline is what
// turns that into a stop the manifest can report.
func TestWatchStopsOnAPollThatHangs(t *testing.T) {
	w := newBlockingWatch(func(ctx context.Context, spid int) (waitSample, error) {
		<-ctx.Done()
		return waitSample{}, ctx.Err()
	}, time.Second, watchCancelAfter)
	w.deadline = 20 * time.Millisecond
	_, cancel := context.WithCancelCause(context.Background())
	w.arm(55, cancel)
	done := make(chan bool)
	go func() { done <- w.tick() }()
	select {
	case goOn := <-done:
		if goOn || !strings.Contains(w.stoppedReason(), "deadline exceeded") {
			t.Errorf("tick = %v, stoppedReason = %q", goOn, w.stoppedReason())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the poll had no deadline")
	}
}

func TestWatchLoopStopsAndCloses(t *testing.T) {
	w := scriptedWatch()
	w.every = time.Millisecond
	w.start()
	time.Sleep(10 * time.Millisecond)
	w.close()
	w.close() // twice is harmless
	var none *blockingWatch
	none.close()
	none.arm(1, nil)
	if s, f := none.disarm(); s.seen() || f || none.stoppedReason() != "" {
		t.Errorf("a nil watch reported something")
	}
}

// The driver returns a bare "context canceled" whatever the cause. What goes
// into the manifest is the watch's reason, read from the unit's context
// through the query context the collector ran under.
func TestBlockedOrReplacesTheDriversError(t *testing.T) {
	unitCtx, cancel := context.WithCancelCause(context.Background())
	qctx, qcancel := context.WithTimeout(unitCtx, time.Minute)
	defer qcancel()
	driver := errors.New("context canceled")

	if got := blockedOr(unitCtx, driver); got != driver {
		t.Errorf("before any cancel: %v", got)
	}
	cancel(&blockedError{Sample: waiter(5000)})
	<-qctx.Done()
	got := blockedOr(unitCtx, driver)
	if !strings.HasPrefix(got.Error(), "cancelled by the blocking watch: session 78 had been waiting on this collector for 5.0 s (LCK_M_SCH_M") {
		t.Errorf("got %q", got)
	}

	// An operator stop cancels the run's context, above the unit's, with no
	// cause of the watch's: the driver's error is kept for recordUnitFailure.
	run, stop := context.WithCancel(context.Background())
	unit2, cancel2 := context.WithCancelCause(run)
	defer cancel2(nil)
	stop()
	if got := blockedOr(unit2, driver); got != driver {
		t.Errorf("an operator stop was relabelled: %v", got)
	}
}

func TestHeldBackSkipsOnlyTheCancelledDatabase(t *testing.T) {
	on := map[string]string{"SALESDB": "70.schema/055.page-density.sql"}
	if r, ok := heldBack(on, "SALESDB"); !ok || !strings.Contains(r, "055.page-density") {
		t.Errorf("SALESDB: %q, %v", r, ok)
	}
	if _, ok := heldBack(on, "OTHERDB"); ok {
		t.Errorf("another database was held back")
	}
	on[""] = "10.system/010.properties.sql"
	if _, ok := heldBack(on, ""); ok {
		t.Errorf("an instance-scope unit was held back")
	}
}

func TestManifestReportsTheWatch(t *testing.T) {
	cases := []struct {
		name  string
		block func(*BlockingWatchBlock)
		human string
	}{
		{"never started", func(b *BlockingWatchBlock) {
			b.Reason = "VIEW SERVER STATE is not granted, and sys.dm_os_waiting_tasks needs it"
		}, "Block watch  : off, VIEW SERVER STATE is not granted"},
		{"ran to the end", func(b *BlockingWatchBlock) {
			b.Enabled, b.Reason, b.CancelledUnits = true, "", 1
		}, "Block watch  : on, cancels a collector someone has waited on for 5 s; 1 cancelled"},
		{"stopped mid-run", func(b *BlockingWatchBlock) {
			b.Enabled, b.Reason, b.Stopped = true, "", "2026-09-19T15:00:00Z: i/o timeout"
		}, "Block watch  : on until it stopped at 2026-09-19T15:00:00Z: i/o timeout; 0 collector(s) cancelled"},
	}
	for _, c := range cases {
		m := NewManifest("sql-auditor", "test", "")
		c.block(&m.BlockingWatch)
		if h := m.Human(); !strings.Contains(h, c.human) {
			t.Errorf("%s: MANIFEST.txt lacks %q", c.name, c.human)
		}
		b, err := m.marshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			BlockingWatch map[string]any `json:"blocking_watch"`
		}
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		bw := got.BlockingWatch
		if bw["enabled"] != m.BlockingWatch.Enabled || bw["stopped"] != m.BlockingWatch.Stopped ||
			bw["poll_ms"] != float64(1000) || bw["cancel_after_ms"] != float64(5000) {
			t.Errorf("%s: _run.json blocking_watch = %v", c.name, bw)
		}
	}
	// A run that dies before the watch is decided still says why it was off.
	if h := NewManifest("sql-auditor", "test", "").Human(); !strings.Contains(h, "off, the run ended before the first collector") {
		t.Errorf("a fresh manifest does not explain the watch: %s", h)
	}
}
