package collect

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// The blocking watch: a second connection that looks, once a second, for
// sessions waiting on the collection's own session, and cancels the running
// unit once one of them has waited watchCancelAfter. docs/blocking-watch-spec.md
// is the design and records what it was measured against.
//
// READ UNCOMMITTED keeps a collector from waiting on the workload; it does not
// keep the workload from waiting on the collector. A collector still takes Sch-S
// on every object it reads, and an ALTER TABLE queued behind that Sch-S holds up
// every reader of the table behind it. Reproduced on SQL Server 2025 with a
// collector-shaped reader, a schema change and an unrelated SELECT.

const (
	watchPollEvery = time.Second
	// Half the collectors' own LOCK_TIMEOUT: the audit gives up waiting on
	// others after ten seconds, and makes others wait on it for less than that.
	// A constant on purpose. A setting would read as an invitation to raise it,
	// and the number exists to bound the harm the audit can do.
	watchCancelAfter = 5 * time.Second
	// A half-open socket does not fail, it hangs; a poll without a deadline
	// would leave a watch protecting nothing while the manifest says it ran.
	watchPollDeadline = 2 * time.Second
	watchAppSuffix    = " (blocking watch)"
)

// The parameter is bound with sql.Named("collector", spid). Direct waiters
// only: cancelling the collector releases whatever waits behind them.
// session_id <> @collector drops the collector's own parallel tasks, whose
// exchange waits name their own session as the blocker.
const watchQuery = `SELECT TOP (1) w.session_id, w.wait_type, w.wait_duration_ms,
       w.resource_description
FROM sys.dm_os_waiting_tasks AS w
WHERE w.blocking_session_id = @collector
  AND w.session_id <> @collector
ORDER BY w.wait_duration_ms DESC;`

// waitSample is one waiter, as the watch last read it. The zero value means
// nobody was waiting.
type waitSample struct {
	Session  int
	WaitType string
	Waited   time.Duration
	Resource string
}

func (w waitSample) seen() bool { return w.Session != 0 }

func (w waitSample) String() string {
	return fmt.Sprintf("session %d had been waiting on this collector for %.1f s (%s, %s)",
		w.Session, w.Waited.Seconds(), w.WaitType, w.Resource)
}

// blockedError is the cause the watch cancels a unit with. The driver does not
// carry a cause through (a cancelled query returns a bare "context canceled"
// whatever was passed), so runUnit asks context.Cause for it.
type blockedError struct{ Sample waitSample }

func (e *blockedError) Error() string {
	return "cancelled by the blocking watch: " + e.Sample.String()
}

// pollFunc reads the longest direct wait on one session. It is a function so
// the decision logic is testable without a server.
type pollFunc func(ctx context.Context, spid int) (waitSample, error)

// blockingWatch is shared between the run loop and one goroutine. The
// goroutine never touches the manifest, the observer or the collection
// connection: its only effect on the run is calling the cancel function of the
// unit that is armed, under mu, so a fire can never land on the next unit.
type blockingWatch struct {
	poll     pollFunc
	every    time.Duration
	after    time.Duration
	deadline time.Duration

	mu      sync.Mutex
	armed   bool
	spid    int
	cancel  context.CancelCauseFunc
	worst   waitSample
	fired   bool
	stopped string // set once, by the goroutine, when a poll fails

	quit chan struct{}
	done chan struct{}
}

func newBlockingWatch(poll pollFunc, every, after time.Duration) *blockingWatch {
	return &blockingWatch{poll: poll, every: every, after: after, deadline: watchPollDeadline,
		quit: make(chan struct{}), done: make(chan struct{})}
}

func (w *blockingWatch) start() { go w.loop() }

// close stops the goroutine and waits for it. Safe on a nil watch, which is
// what a run without one holds.
func (w *blockingWatch) close() {
	if w == nil {
		return
	}
	select {
	case <-w.quit:
	default:
		close(w.quit)
	}
	<-w.done
}

func (w *blockingWatch) loop() {
	defer close(w.done)
	t := time.NewTicker(w.every)
	defer t.Stop()
	for {
		select {
		case <-w.quit:
			return
		case <-t.C:
		}
		if !w.tick() {
			return
		}
	}
}

// tick polls once if a unit is armed. It returns false when the watch has
// stopped for good.
func (w *blockingWatch) tick() bool {
	w.mu.Lock()
	armed, spid := w.armed, w.spid
	w.mu.Unlock()
	if !armed {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), w.deadline)
	s, err := w.poll(ctx, spid)
	cancel()

	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		// No retry: a watch that silently comes and goes would make the
		// manifest's "enabled" mean nothing.
		w.stopped = fmt.Sprintf("%s: %v", time.Now().UTC().Format(time.RFC3339), err)
		return false
	}
	// The unit may have been disarmed, and the next one armed, while the poll
	// was in flight. A sample about another session or a finished unit is
	// dropped rather than credited to whatever is armed now.
	if !w.armed || w.spid != spid || !s.seen() {
		return true
	}
	if s.Waited > w.worst.Waited {
		w.worst = s
	}
	if s.Waited >= w.after && !w.fired {
		w.fired = true
		w.cancel(&blockedError{Sample: s})
	}
	return true
}

// arm watches spid on behalf of the unit whose context cancel cancels.
func (w *blockingWatch) arm(spid int, cancel context.CancelCauseFunc) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.armed, w.spid, w.cancel = true, spid, cancel
	w.worst, w.fired = waitSample{}, false
}

// disarm ends the unit's watch and returns the longest wait seen while armed
// and whether the watch fired.
func (w *blockingWatch) disarm() (waitSample, bool) {
	if w == nil {
		return waitSample{}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.armed, w.cancel = false, nil
	return w.worst, w.fired
}

// stoppedReason is empty while the watch runs.
func (w *blockingWatch) stoppedReason() string {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stopped
}

// sqlPoll is the poll against a real server, on a connection of its own.
func sqlPoll(c *sql.Conn) pollFunc {
	return func(ctx context.Context, spid int) (waitSample, error) {
		var s waitSample
		var ms int64
		var res sql.NullString
		err := c.QueryRowContext(ctx, watchQuery, sql.Named("collector", spid)).
			Scan(&s.Session, &s.WaitType, &ms, &res)
		if err == sql.ErrNoRows {
			return waitSample{}, nil
		}
		if err != nil {
			return waitSample{}, err
		}
		s.Waited, s.Resource = time.Duration(ms)*time.Millisecond, res.String
		return s, nil
	}
}

// startBlockingWatch opens the watch's own connection and proves the query
// runs before anything relies on it. The preflight probes sys.dm_os_wait_stats,
// and a DENY on sys.dm_os_waiting_tasks alone passes that probe and fails here.
// A nil watch and a reason mean the run goes on unwatched: the collection is
// the product, the watch a safeguard.
func startBlockingWatch(ctx context.Context, cfg *Config, denied map[string]bool, spid int) (*blockingWatch, func(), string) {
	// denied holds anything that is not "ok", including the not_needed that
	// ProfileChecks writes over a denial no collector of the profile declares.
	if denied["view_server_state"] {
		return nil, func() {}, "VIEW SERVER STATE is not granted, and sys.dm_os_waiting_tasks needs it"
	}
	wcfg := *cfg
	wcfg.AppName = cfg.AppName + watchAppSuffix
	db, err := Open(&wcfg)
	if err != nil {
		return nil, func() {}, "its connection could not be opened: " + err.Error()
	}
	dctx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout+watchPollDeadline)
	defer cancel()
	c, err := db.Conn(dctx)
	if err != nil {
		db.Close()
		return nil, func() {}, "its connection could not be opened: " + err.Error()
	}
	poll := sqlPoll(c)
	pctx, pcancel := context.WithTimeout(ctx, watchPollDeadline)
	_, err = poll(pctx, spid)
	pcancel()
	if err != nil {
		c.Close()
		db.Close()
		return nil, func() {}, "its first poll failed: " + err.Error()
	}
	w := newBlockingWatch(poll, watchPollEvery, watchCancelAfter)
	w.start()
	return w, func() { w.close(); c.Close(); db.Close() }, ""
}

// orInstance names the target of an instance-scope unit, whose database name
// is empty, in a sentence.
func orInstance(db string) string {
	if db == "" {
		return "the instance"
	}
	return db
}

// blockedOr returns the watch's reason when it cancelled the unit, and err
// otherwise. The driver answers a cancelled query with a bare "context
// canceled" whatever cause was given, so the cause is asked of the context.
func blockedOr(unitCtx context.Context, err error) error {
	if be, ok := context.Cause(unitCtx).(*blockedError); ok {
		return be
	}
	return err
}

// heldBack names the collector the watch cancelled on this unit's database,
// if any: later units there are skipped rather than run into the same
// deployment again. Instance-scope units, with no database, never are.
func heldBack(cancelledOn map[string]string, db string) (string, bool) {
	if db == "" {
		return "", false
	}
	by, ok := cancelledOn[db]
	if !ok {
		return "", false
	}
	return "the blocking watch cancelled " + by + " on this database", true
}

// UnitSkipped is what Observer.UnitDone carries for a planned unit the run
// decided, once running, not to execute. It is not a failure: the gauge counts
// the unit, since Planned did, and the screens show it as a skip.
type UnitSkipped struct{ Reason string }

func (e *UnitSkipped) Error() string { return "skipped: " + e.Reason }
