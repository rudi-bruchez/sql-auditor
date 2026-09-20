package collect

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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

// The identity of a waiter: what it calls itself and where it is connected.
// No login name and no host name. 046.local-sessions aggregates and projects
// neither, and the manifest promises them only under --include-session-text,
// so writing them here by default would make its disclosure paragraph false.
// docs/blocking-record-spec.md is the design.
const identifyQuery = `SELECT s.program_name, DB_NAME(r.database_id) AS request_database
FROM sys.dm_exec_sessions AS s
LEFT JOIN sys.dm_exec_requests AS r ON r.session_id = s.session_id
WHERE s.session_id = @waiter;`

// At most this many identity reads per unit, so a convoy cannot turn the
// watch into a querying loop of its own.
const watchIdentifyPerUnit = 3

// identifyFunc names one session. It is a function for the same reason
// pollFunc is: the decision logic is testable without a server.
type identifyFunc func(ctx context.Context, session int) (waiterIdentity, error)

// waiterIdentity is one answer of identifyQuery. Database is nil when the
// session has no request, which is a sleeping session with an open
// transaction, and is not the same as an unknown database.
type waiterIdentity struct {
	// Program is nil when the column is NULL, which is most sessions: 65 of
	// 67 on an idle lab instance. An empty string is the other ordinary case,
	// a default SqlClient connection, and the two are not the same answer.
	Program  *string
	Database *string
}

// waitSample is one waiter, as the watch last read it. The zero value means
// nobody was waiting.
type waitSample struct {
	Session  int
	WaitType string
	Waited   time.Duration
	Resource string
}

func (w waitSample) seen() bool { return w.Session != 0 }

// waiterRecord is what the watch learned about one session while a unit was
// armed. Status is the incident's identified field: it names why the identity
// is missing rather than leaving the reader to guess.
type waiterRecord struct {
	Identity waiterIdentity
	Status   string
	Detail   string
}

const (
	identifyOK           = "ok"
	identifyNotAttempted = "not_attempted"
	identifyNoSession    = "no_session"
	identifyFailed       = "failed"
)

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
	poll        pollFunc
	identify    identifyFunc
	identifyMax int
	every       time.Duration
	after       time.Duration
	deadline    time.Duration

	mu        sync.Mutex
	armed     bool
	spid      int
	cancel    context.CancelCauseFunc
	worst     waitSample
	fired     bool
	firstSeen time.Time
	// One entry per session seen waiting on the armed unit, whether or not it
	// could be identified. Its length is waiters_seen.
	waiters map[int]waiterRecord
	stopped string // set once, by the goroutine, when a poll fails

	quit chan struct{}
	done chan struct{}
}

func newBlockingWatch(poll pollFunc, every, after time.Duration) *blockingWatch {
	return &blockingWatch{poll: poll, every: every, after: after, deadline: watchPollDeadline,
		identifyMax: watchIdentifyPerUnit,
		waiters:     map[int]waiterRecord{},
		quit:        make(chan struct{}), done: make(chan struct{})}
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
	if w.firstSeen.IsZero() {
		w.firstSeen = time.Now()
	}
	if s.Waited > w.worst.Waited {
		w.worst = s
	}
	// The cancel comes first, always. Nothing of the watch's own may sit
	// between a wait that has reached the limit and the cancel that ends it,
	// and a first sighting can already be past the limit: the poll returns
	// the longest waiter only, so the next one becomes visible at whatever
	// duration it has already reached.
	if s.Waited >= w.after && !w.fired {
		w.fired = true
		w.cancel(&blockedError{Sample: s})
	}
	// A session is named the first time it is seen waiting. Cancelling the
	// collector does not end the waiter, which goes on to take its lock, so
	// the identity survives the cancel; reading it early is what keeps it out
	// of the way of the cancel.
	if _, known := w.waiters[s.Session]; !known {
		rec := w.identifyLocked(s.Session)
		// The lock was released for the query, so the unit may have finished
		// and the next one been armed meanwhile. Crediting this waiter to
		// that one would put a session that never waited on it in its record.
		if w.armed && w.spid == spid {
			w.waiters[s.Session] = rec
		}
	}
	return true
}

// identifyLocked reads one session's identity. It is called with mu held and
// releases it for the query, which keeps the poll loop's lock discipline
// intact without holding the mutex across a round trip. A failure is recorded
// and never stops the watch: an identity is a nicety, the cancel is not.
func (w *blockingWatch) identifyLocked(session int) waiterRecord {
	if w.identify == nil || len(w.waiters) >= w.identifyMax {
		return waiterRecord{Status: identifyNotAttempted}
	}
	w.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), w.deadline)
	id, err := w.identify(ctx, session)
	cancel()
	w.mu.Lock()
	switch {
	case err == sql.ErrNoRows:
		return waiterRecord{Status: identifyNoSession}
	case err != nil:
		return waiterRecord{Status: identifyFailed, Detail: err.Error()}
	}
	return waiterRecord{Identity: id, Status: identifyOK}
}

// arm watches spid on behalf of the unit whose context cancel cancels.
func (w *blockingWatch) arm(spid int, cancel context.CancelCauseFunc) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.armed, w.spid, w.cancel = true, spid, cancel
	w.worst, w.fired, w.firstSeen = waitSample{}, false, time.Time{}
	w.waiters = map[int]waiterRecord{}
}

// disarm ends the unit's watch and returns the longest wait seen while armed
// and whether the watch fired.
func (w *blockingWatch) disarm() (waitSample, bool) {
	s, _, fired := w.disarmed()
	return s, fired
}

// disarmed ends the unit's watch and returns everything the incident needs:
// the longest wait, what is known of the sessions that waited, and whether
// the watch fired.
func (w *blockingWatch) disarmed() (waitSample, waitRound, bool) {
	if w == nil {
		return waitSample{}, waitRound{}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.armed, w.cancel = false, nil
	r := waitRound{FirstSeen: w.firstSeen, Waiters: len(w.waiters)}
	// Keyed by session, so the identity attached to an incident is always the
	// one of the session that incident reports. A record is kept for every
	// session seen, including the ones the per-unit bound refused to read.
	r.Record = w.waiters[w.worst.Session]
	return w.worst, r, w.fired
}

// waitRound is what a unit's watch knew when it was disarmed, beside the
// longest sample.
type waitRound struct {
	FirstSeen time.Time
	Waiters   int
	Record    waiterRecord
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

// sqlIdentify names a waiter on the watch's own connection. sql.ErrNoRows is
// returned as is: a session that has gone is a case of its own, not a failure.
func sqlIdentify(c *sql.Conn) identifyFunc {
	return func(ctx context.Context, session int) (waiterIdentity, error) {
		var program, database sql.NullString
		err := c.QueryRowContext(ctx, identifyQuery, sql.Named("waiter", session)).
			Scan(&program, &database)
		if err != nil {
			return waiterIdentity{}, err
		}
		var id waiterIdentity
		if program.Valid {
			p := strings.TrimSpace(program.String)
			id.Program = &p
		}
		if database.Valid {
			db := database.String
			id.Database = &db
		}
		return id, nil
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
	// The identity read gets a connection of its own. A query whose context
	// deadline expires leaves its connection dead, so sharing one would let a
	// slow identity read stop the watch: measured, the next poll on the same
	// connection returns "driver: bad connection" and then "connection is
	// already closed". Failing to open it costs the identity, not the watch.
	idConn, idErr := db.Conn(dctx)
	pctx, pcancel := context.WithTimeout(ctx, watchPollDeadline)
	_, err = poll(pctx, spid)
	pcancel()
	if err != nil {
		c.Close()
		db.Close()
		return nil, func() {}, "its first poll failed: " + err.Error()
	}
	w := newBlockingWatch(poll, watchPollEvery, watchCancelAfter)
	if idErr == nil {
		w.identify = sqlIdentify(idConn)
	}
	w.start()
	return w, func() {
		w.close()
		if idErr == nil {
			idConn.Close()
		}
		c.Close()
		db.Close()
	}, ""
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

// incidentOf turns what the watch kept about one unit into the record the run
// file carries. The identity is attached only when it belongs to the session
// the incident reports; the status field says so when it does not.
func incidentOf(script, target string, worst waitSample, round waitRound, cancelled bool) BlockedWait {
	first := round.FirstSeen
	if first.IsZero() {
		first = time.Now()
	}
	in := BlockedWait{
		Script:      script,
		Target:      target,
		FirstSeen:   first.Format(time.RFC3339),
		WaitedMS:    int(worst.Waited / time.Millisecond),
		Cancelled:   cancelled,
		WaitersSeen: round.Waiters,
		Waiter: BlockedWaiter{
			SessionID:           worst.Session,
			WaitType:            worst.WaitType,
			ResourceDescription: worst.Resource,
			Identified:          round.Record.Status,
			IdentifiedDetail:    round.Record.Detail,
		},
	}
	if in.Waiter.Identified == "" {
		in.Waiter.Identified = identifyNotAttempted
	}
	if round.Record.Status == identifyOK {
		in.Waiter.ProgramName = round.Record.Identity.Program
		in.Waiter.Database = round.Record.Identity.Database
	}
	return in
}
