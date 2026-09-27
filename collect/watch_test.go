package collect

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
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
	w.backoff = time.Millisecond
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
	w.backoff = time.Millisecond
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

// flakyWatch fails its first n polls, then answers sample. It counts polls and
// reconnects so a test can tell a retry from a stop.
func flakyWatch(n int, sample waitSample) (w *blockingWatch, polls, reconnects *int) {
	polls, reconnects = new(int), new(int)
	var poll pollFunc = func(ctx context.Context, spid int) (waitSample, error) {
		*polls++
		if *polls <= n {
			return waitSample{}, errors.New("driver: bad connection")
		}
		return sample, nil
	}
	w = newBlockingWatch(poll, time.Second, watchCancelAfter)
	w.backoff = time.Millisecond
	w.reconnect = func(ctx context.Context) (pollFunc, error) {
		*reconnects++
		return poll, nil
	}
	return w, polls, reconnects
}

func TestWatchReconnectsAfterFailedPolls(t *testing.T) {
	w, polls, reconnects := flakyWatch(watchRetries, waiter(6000))
	unitCtx, cancel := context.WithCancelCause(context.Background())
	w.arm(55, cancel)
	if !w.tick() {
		t.Fatalf("the watch stopped: %s", w.stoppedReason())
	}
	if *polls != watchRetries+1 || *reconnects != watchRetries {
		t.Errorf("polls = %d, reconnects = %d", *polls, *reconnects)
	}
	// The sample the recovered poll read is acted on like any other.
	if _, ok := context.Cause(unitCtx).(*blockedError); !ok {
		t.Errorf("the recovered poll's sample did not cancel the unit")
	}
	warns := w.warnings()
	if w.stoppedReason() != "" || len(warns) != 1 ||
		!strings.Contains(warns[0], "reconnected") || !strings.Contains(warns[0], "bad connection") {
		t.Errorf("stopped = %q, warnings = %q", w.stoppedReason(), warns)
	}
}

func TestWatchStopsAfterBoundedRetries(t *testing.T) {
	w, polls, reconnects := flakyWatch(1<<30, waitSample{})
	_, cancel := context.WithCancelCause(context.Background())
	w.arm(55, cancel)
	if w.tick() {
		t.Fatalf("the watch went on after every retry failed")
	}
	if *polls != watchRetries+1 || *reconnects != watchRetries {
		t.Errorf("polls = %d, reconnects = %d, want %d and %d", *polls, *reconnects, watchRetries+1, watchRetries)
	}
	warns := w.warnings()
	if !strings.Contains(w.stoppedReason(), "bad connection") || len(warns) != 1 ||
		!strings.Contains(warns[0], "stopped") || !strings.Contains(warns[0], "unwatched") {
		t.Errorf("stopped = %q, warnings = %q", w.stoppedReason(), warns)
	}
}

// A failed reconnect is an attempt like a failed poll, and its error is the
// one the stop reports last.
func TestWatchCountsAFailedReconnectAsAnAttempt(t *testing.T) {
	w, polls, reconnects := flakyWatch(1, waitSample{})
	inner := w.reconnect
	w.reconnect = func(ctx context.Context) (pollFunc, error) {
		inner(ctx)
		return nil, errors.New("login timeout expired")
	}
	_, cancel := context.WithCancelCause(context.Background())
	w.arm(55, cancel)
	if w.tick() {
		t.Fatalf("the watch went on with no connection")
	}
	if *polls != 1 || *reconnects != watchRetries ||
		!strings.Contains(w.stoppedReason(), "login timeout expired") {
		t.Errorf("polls = %d, reconnects = %d, stopped = %q", *polls, *reconnects, w.stoppedReason())
	}
}

// The retry waits on the run's context: a run that is stopping does not sit
// out the backoff, and does not record the watch as having failed.
func TestWatchRetryNeverOutlivesTheRun(t *testing.T) {
	w, _, reconnects := flakyWatch(1<<30, waitSample{})
	w.backoff = time.Hour
	run, stop := context.WithCancel(context.Background())
	w.parent = run
	_, cancel := context.WithCancelCause(context.Background())
	w.arm(55, cancel)
	done := make(chan bool)
	go func() { done <- w.tick() }()
	time.Sleep(10 * time.Millisecond)
	stop()
	select {
	case goOn := <-done:
		if goOn || *reconnects != 0 || w.stoppedReason() != "" || len(w.warnings()) != 0 {
			t.Errorf("tick = %v, reconnects = %d, stopped = %q", goOn, *reconnects, w.stoppedReason())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the retry waited past the run's context")
	}
}

func TestWatchWarningsCountFrequentRecoveries(t *testing.T) {
	w := newBlockingWatch(nil, time.Second, watchCancelAfter)
	for i := 0; i < watchRecoveriesListed+4; i++ {
		w.recovered = append(w.recovered, "2026-09-19T15:00:00Z: driver: bad connection")
	}
	warns := w.warnings()
	if len(warns) != watchRecoveriesListed+1 || !strings.Contains(warns[len(warns)-1], "4 more time(s)") {
		t.Errorf("warnings = %q", warns)
	}
	var none *blockingWatch
	if none.warnings() != nil {
		t.Errorf("a nil watch has warnings")
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

// scriptedIdentify answers from a table of sessions, and records what it was
// asked, so a test can prove the watch asked once per session and no more.
func scriptedIdentify(known map[int]waiterIdentity, err error) (identifyFunc, *[]int) {
	asked := []int{}
	return func(ctx context.Context, session int) (waiterIdentity, error) {
		asked = append(asked, session)
		if err != nil {
			return waiterIdentity{}, err
		}
		id, ok := known[session]
		if !ok {
			return waiterIdentity{}, sql.ErrNoRows
		}
		return id, nil
	}, &asked
}

func strptr(s string) *string { return &s }

func waiterFrom(session, ms int) waitSample {
	return waitSample{Session: session, WaitType: "LCK_M_SCH_M",
		Waited: time.Duration(ms) * time.Millisecond, Resource: "objectlock objid=1"}
}

func TestBlockedWaitNamesTheLongestWaiterAndCountsTheOthers(t *testing.T) {
	db := "SALESDB"
	w := scriptedWatch(waiterFrom(78, 1000), waiterFrom(91, 2000), waiterFrom(78, 3000))
	id, asked := scriptedIdentify(map[int]waiterIdentity{
		78: {Program: strptr("SQLAgent - TSQL JobStep"), Database: &db},
		91: {Program: strptr("sqlcmd")},
	}, nil)
	w.identify = id
	_, cancel := context.WithCancelCause(context.Background())
	w.arm(55, cancel)
	for range 3 {
		w.tick()
	}
	worst, round, fired := w.disarmed()
	if fired || worst.Session != 78 || worst.Waited != 3*time.Second {
		t.Fatalf("worst = %+v, fired = %v", worst, fired)
	}
	if len(*asked) != 2 {
		t.Errorf("identified %v, want one read per session", *asked)
	}
	in := incidentOf("70.schema/055.page-density.sql", "SALESDB", worst, round, false)
	if in.WaitersSeen != 2 || in.WaitedMS != 3000 || in.Cancelled {
		t.Errorf("incident = %+v", in)
	}
	if in.Waiter.Identified != identifyOK || (in.Waiter.ProgramName == nil || *in.Waiter.ProgramName != "SQLAgent - TSQL JobStep") {
		t.Errorf("waiter = %+v", in.Waiter)
	}
	if in.Waiter.Database == nil || *in.Waiter.Database != "SALESDB" {
		t.Errorf("database = %v", in.Waiter.Database)
	}
	if in.FirstSeen == "" {
		t.Errorf("no first_seen")
	}
}

// The identity read belongs to one session; the longest wait may be another's.
// Pairing them would put a name on a wait that is not its own.
func TestBlockedWaitRefusesAnIdentityFromAnotherSession(t *testing.T) {
	w := scriptedWatch(waiterFrom(78, 1000), waiterFrom(91, 9000))
	w.identify = func(ctx context.Context, session int) (waiterIdentity, error) {
		return waiterIdentity{Program: strptr("sqlcmd")}, nil
	}
	w.identifyMax = 1
	_, cancel := context.WithCancelCause(context.Background())
	w.arm(55, cancel)
	w.tick()
	w.tick()
	worst, round, _ := w.disarmed()
	in := incidentOf("s.sql", "", worst, round, true)
	if worst.Session != 91 {
		t.Fatalf("worst session = %d", worst.Session)
	}
	if in.Waiter.Identified != identifyNotAttempted || in.Waiter.ProgramName != nil {
		t.Errorf("waiter = %+v, want no identity from another session", in.Waiter)
	}
}

func TestIdentityFailureNeitherStopsTheWatchNorDelaysTheCancel(t *testing.T) {
	cases := []struct {
		name string
		id   identifyFunc
		want string
	}{
		{"gone", func(ctx context.Context, s int) (waiterIdentity, error) {
			return waiterIdentity{}, sql.ErrNoRows
		}, identifyNoSession},
		{"failed", func(ctx context.Context, s int) (waiterIdentity, error) {
			return waiterIdentity{}, errors.New("mssql: VIEW SERVER STATE permission was denied")
		}, identifyFailed},
		{"hangs", func(ctx context.Context, s int) (waiterIdentity, error) {
			<-ctx.Done()
			return waiterIdentity{}, ctx.Err()
		}, identifyFailed},
	}
	for _, c := range cases {
		w := scriptedWatch(waiterFrom(78, 6000))
		w.identify, w.deadline = c.id, 20*time.Millisecond
		ctx, cancel := context.WithCancelCause(context.Background())
		w.arm(55, cancel)
		if !w.tick() {
			t.Fatalf("%s: the watch stopped on a failed identity read", c.name)
		}
		var be *blockedError
		if !errors.As(context.Cause(ctx), &be) {
			t.Fatalf("%s: the unit was not cancelled: %v", c.name, context.Cause(ctx))
		}
		worst, round, _ := w.disarmed()
		in := incidentOf("s.sql", "", worst, round, true)
		if in.Waiter.Identified != c.want {
			t.Errorf("%s: identified = %q, want %q", c.name, in.Waiter.Identified, c.want)
		}
		if c.want == identifyFailed && in.Waiter.IdentifiedDetail == "" {
			t.Errorf("%s: no detail on a failure", c.name)
		}
	}
}

// A session with no request has no database, which is not an unknown one.
func TestIdentityWithoutARequestKeepsTheDatabaseNull(t *testing.T) {
	w := scriptedWatch(waiterFrom(78, 1000))
	w.identify = func(ctx context.Context, s int) (waiterIdentity, error) {
		return waiterIdentity{Program: strptr("sqlcmd")}, nil
	}
	_, cancel := context.WithCancelCause(context.Background())
	w.arm(55, cancel)
	w.tick()
	worst, round, _ := w.disarmed()
	in := incidentOf("s.sql", "", worst, round, false)
	if in.Waiter.Identified != identifyOK || in.Waiter.Database != nil {
		t.Errorf("waiter = %+v", in.Waiter)
	}
}

func TestWaitCapKeepsTheCancellations(t *testing.T) {
	var w BlockingWatchBlock
	for i := range maxBlockedWaits {
		w.AddBlockedWait(BlockedWait{Script: "warning.sql", WaitedMS: 1000 + i})
	}
	w.AddBlockedWait(BlockedWait{Script: "over.sql", WaitedMS: 900})
	if n := len(w.Items()); n != maxBlockedWaits {
		t.Fatalf("%d items", n)
	}
	if !w.BlockedWaits.Truncated || w.BlockedWaits.OmittedCount != 1 {
		t.Errorf("truncation not declared: %+v", w.BlockedWaits)
	}
	w.AddBlockedWait(BlockedWait{Script: "cancelled.sql", WaitedMS: 5000, Cancelled: true})
	if w.BlockedWaits.OmittedCount != 2 {
		t.Errorf("omitted = %d", w.BlockedWaits.OmittedCount)
	}
	var kept, shortest bool
	for _, in := range w.Items() {
		if in.Script == "cancelled.sql" {
			kept = true
		}
		if in.WaitedMS == 1000 {
			shortest = true
		}
	}
	if !kept || shortest {
		t.Errorf("a cancellation did not displace the shortest warning: kept=%v shortest still there=%v", kept, shortest)
	}
}

func TestManifestShowsBlockedWaitsAndSlowestCollectors(t *testing.T) {
	m := NewManifest("sql-auditor", "test", "")
	if h := m.Human(); strings.Contains(h, "held up by this collection") {
		t.Errorf("a run with no incident lists some")
	}
	db := "SALESDB"
	m.BlockingWatch.AddBlockedWait(BlockedWait{Script: "70.schema/055.page-density.sql", Target: "SALESDB",
		WaitedMS: 5031, Cancelled: true, WaitersSeen: 2,
		Waiter: BlockedWaiter{SessionID: 78, WaitType: "LCK_M_SCH_M", ProgramName: strptr("deploy.exe"),
			Database: &db, Identified: identifyOK}})
	m.Results = []ResultEntry{
		{Script: "a.sql", DurationMS: 12000, Bytes: 2048},
		{Script: "b.sql", Target: "SALESDB", DurationMS: 500, Bytes: 10},
	}
	h := m.Human()
	for _, want := range []string{
		"Sessions held up by this collection (1):",
		"session 78 waited, and the collector was cancelled after 5.0 s (LCK_M_SCH_M) on 70.schema/055.page-density.sql on SALESDB",
		"it calls itself \"deploy.exe\"",
		"2 sessions waited on this collector",
		"Slowest collectors (the run is longer than their sum):",
		"12.0 s",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("MANIFEST.txt lacks %q\n%s", want, h)
		}
	}
	b, err := m.marshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		BlockingWatch struct {
			BlockedWaits struct {
				Items []struct {
					WaitedMS int `json:"waited_ms"`
					Waiter   struct {
						Database   *string `json:"database"`
						Identified string  `json:"identified"`
					} `json:"waiter"`
				} `json:"items"`
				Truncated bool `json:"truncated"`
			} `json:"waits"`
		} `json:"blocking_watch"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	items := got.BlockingWatch.BlockedWaits.Items
	if len(items) != 1 || items[0].WaitedMS != 5031 || items[0].Waiter.Identified != identifyOK {
		t.Errorf("_run.json waits = %+v", items)
	}
}

// An archive that never recorded a wait and one where nobody waited must
// not read the same.
func TestEmptyBlockedWaitsAreWrittenAsAnEmptyList(t *testing.T) {
	b, err := NewManifest("sql-auditor", "test", "").marshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"waits": {`) || !strings.Contains(string(b), `"items": []`) {
		t.Errorf("_run.json has no empty waits block: %s", b)
	}
}

// Codex review: the first poll bypassed the retry, so a transient failure at
// the start left the whole collection unwatched. It now retries like any
// other poll, records no recovery since no collector was running, and does
// not retry a server's answer, which is the denial the first poll is there to
// catch.
func TestWatchFirstPollRetries(t *testing.T) {
	w, polls, reconnects := flakyWatch(1, waitSample{})
	if err := w.firstPoll(55); err != nil {
		t.Fatalf("firstPoll = %v after one transient failure", err)
	}
	if *polls != 2 || *reconnects != 1 || len(w.warnings()) != 0 {
		t.Errorf("polls = %d, reconnects = %d, warnings = %q", *polls, *reconnects, w.warnings())
	}

	w, polls, reconnects = flakyWatch(1<<30, waitSample{})
	if err := w.firstPoll(55); err == nil || !strings.Contains(err.Error(), "bad connection") {
		t.Errorf("firstPoll = %v with every attempt failing", err)
	}
	if *polls != watchRetries+1 || *reconnects != watchRetries {
		t.Errorf("polls = %d, reconnects = %d, want %d and %d", *polls, *reconnects, watchRetries+1, watchRetries)
	}

	w, _, reconnects = flakyWatch(0, waitSample{})
	polls = new(int)
	w.poll = func(ctx context.Context, spid int) (waitSample, error) {
		*polls++
		return waitSample{}, mssql.Error{Number: 300, Message: "VIEW SERVER STATE permission was denied"}
	}
	if err := w.firstPoll(55); err == nil || *polls != 1 || *reconnects != 0 {
		t.Errorf("firstPoll = %v, polls = %d, reconnects = %d: a denial was retried", err, *polls, *reconnects)
	}
}
