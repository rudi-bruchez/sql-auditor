package collect

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The blocking watch against a real instance. It is skipped unless
// SQL_AUDITOR_LIVE_SERVER is set, because it needs a server it may create a
// database on:
//
//	SQL_AUDITOR_LIVE_SERVER=localhost,11533 SQL_AUDITOR_LIVE_USER=sa \
//	SQL_AUDITOR_LIVE_PASSWORD=... go test ./collect/ -run Live -v
//
// It exists because the interesting half of this feature cannot be reached
// from the scripted poll: the query text, the way a wait appears in
// sys.dm_os_waiting_tasks, and what the identity read answers while the
// waiter is still waiting. A full collection is not a reliable way to drive
// it either, measured: the collectors that read user data sample rather than
// scan, and none of them held a lock for as long as one poll interval.
func TestLiveWatchRecordsAWait(t *testing.T) {
	server := os.Getenv("SQL_AUDITOR_LIVE_SERVER")
	if server == "" {
		t.Skip("SQL_AUDITOR_LIVE_SERVER is not set")
	}
	cfg := &Config{
		Server: server, User: os.Getenv("SQL_AUDITOR_LIVE_USER"),
		Password: os.Getenv("SQL_AUDITOR_LIVE_PASSWORD"),
		AppName:  "sql-auditor-live-test", Encrypt: true, TrustCert: true,
		ConnectTimeout: 10 * time.Second,
	}
	ctx := context.Background()
	// Open gives every handle a pool of one connection, which is what the
	// collector wants and what makes a second connection a second handle.
	dial := func(app string) *sql.DB {
		t.Helper()
		c := *cfg
		c.AppName = app
		db, err := Open(&c)
		if err != nil {
			t.Fatalf("open %s: %v", app, err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	db := dial("sql-auditor-live-setup")

	const dbName = "ZzWatchLive"
	exec := func(conn *stmtRunner, sqlText string) {
		t.Helper()
		if err := conn.exec(ctx, sqlText); err != nil {
			t.Fatalf("%s: %v", strings.SplitN(sqlText, "\n", 2)[0], err)
		}
	}
	setup := newStmtRunner(t, db)
	defer setup.close()
	exec(setup, "IF DB_ID('"+dbName+"') IS NOT NULL BEGIN ALTER DATABASE ["+dbName+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE ["+dbName+"]; END")
	exec(setup, "CREATE DATABASE ["+dbName+"]")
	t.Cleanup(func() {
		c := newStmtRunner(t, db)
		defer c.close()
		_ = c.exec(context.Background(), "ALTER DATABASE ["+dbName+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE ["+dbName+"]")
	})
	exec(setup, "USE ["+dbName+"]; CREATE TABLE dbo.T(id int identity primary key, pad char(50) not null)")
	exec(setup, "USE ["+dbName+"]; INSERT dbo.T(pad) VALUES ('x')")

	// The collector: a session holding Sch-S on dbo.T inside a transaction,
	// which is what any collector reading a table does for the length of its
	// statement.
	collector := newStmtRunner(t, dial("sql-auditor-live-collector"))
	defer collector.close()
	var spid int
	if err := collector.conn.QueryRowContext(ctx, "SELECT @@SPID").Scan(&spid); err != nil {
		t.Fatalf("spid: %v", err)
	}
	// A Sch-S lock lives for the length of the STATEMENT, not of the
	// transaction, so the collector has to be busy rather than merely open.
	// This is the reproduction of docs/blocking-watch-spec.md: the CROSS APPLY
	// makes it reread dbo.T for the whole statement.
	const longRead = `SET LOCK_TIMEOUT 10000;
SELECT COUNT_BIG(x.id)
FROM (SELECT TOP (3000000000) a.object_id
      FROM sys.all_objects a CROSS JOIN sys.all_objects b CROSS JOIN sys.all_objects c) o
CROSS APPLY (SELECT TOP (1) t.id FROM dbo.T AS t
             WHERE t.id >= ABS(CHECKSUM(NEWID())) % 2) x
OPTION (MAXDOP 1);`
	unitCtx, cancel := context.WithCancelCause(context.Background())
	read := make(chan error, 1)
	go func() { read <- collector.exec(unitCtx, "USE ["+dbName+"];\n"+longRead) }()
	time.Sleep(time.Second)

	// The waiter: a schema change that queues behind it, on its own
	// connection, named so the identity read has something to find.
	waiter := newStmtRunner(t, dial("the-deployment"))
	defer waiter.close()
	go waiter.exec(ctx, "USE ["+dbName+"]; ALTER TABLE dbo.T ADD c int NULL")

	// Two connections, as the watch itself takes: an identity read that hits
	// its deadline leaves its connection dead, and the poll must survive that.
	pollConn, err := dial("sql-auditor-live-watch").Conn(ctx)
	if err != nil {
		t.Fatalf("watch conn: %v", err)
	}
	defer pollConn.Close()
	idConn, err := dial("sql-auditor-live-identify").Conn(ctx)
	if err != nil {
		t.Fatalf("identify conn: %v", err)
	}
	defer idConn.Close()
	w := newBlockingWatch(sqlPoll(pollConn), 100*time.Millisecond, 500*time.Millisecond)
	w.identify = sqlIdentify(idConn)
	w.arm(spid, cancel)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if !w.tick() {
			t.Fatalf("the watch stopped: %s", w.stoppedReason())
		}
		if unitCtx.Err() != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	worst, round, fired := w.disarmed()
	// The watch cancelled the collector's statement, which is what releases
	// the waiter. blockedOr turns the driver's bare "context canceled" into
	// the reason, which is the message an operator reads.
	if err := blockedOr(unitCtx, <-read); err == nil || !strings.Contains(err.Error(), "cancelled by the blocking watch") {
		t.Errorf("the collector's error was %v", err)
	}

	if !fired || !worst.seen() {
		t.Fatalf("nothing was seen waiting on session %d", spid)
	}
	in := incidentOf("70.schema/055.page-density.sql", dbName, worst, round, true)
	if in.Waiter.WaitType != "LCK_M_SCH_M" {
		t.Errorf("wait type = %q, want the schema-modification lock", in.Waiter.WaitType)
	}
	if in.Waiter.Identified != identifyOK {
		t.Fatalf("identified = %q (%s)", in.Waiter.Identified, in.Waiter.IdentifiedDetail)
	}
	if in.Waiter.ProgramName == nil || *in.Waiter.ProgramName != "the-deployment" {
		t.Errorf("program = %v, want the waiter's application name", in.Waiter.ProgramName)
	}
	if in.Waiter.Database == nil || *in.Waiter.Database != dbName {
		t.Errorf("database = %v, want %s", in.Waiter.Database, dbName)
	}
	if in.WaitersSeen != 1 || in.WaitedMS < 400 {
		t.Errorf("incident = %+v", in)
	}
	t.Logf("wait: session %d waited %d ms (%s), program %q, database %v",
		in.Waiter.SessionID, in.WaitedMS, in.Waiter.WaitType, *in.Waiter.ProgramName, *in.Waiter.Database)

	// An identity read that hits its deadline leaves its connection dead. The
	// watch survives that only because the poll has a connection of its own,
	// which is the whole reason startBlockingWatch opens two.
	dead, cancelRead := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancelRead()
	if _, err := idConn.ExecContext(dead, "WAITFOR DELAY '00:00:05'"); err == nil {
		t.Fatal("the deliberate deadline did not fire")
	}
	if _, err := sqlIdentify(idConn)(ctx, spid); err == nil {
		t.Log("the identity connection survived its deadline on this driver")
	}
	if _, err := sqlPoll(pollConn)(ctx, spid); err != nil {
		t.Errorf("the poll died with the identity read: %v", err)
	}
}

// stmtRunner is one connection kept open for the length of a step, so a
// transaction and its locks live where the test expects them.
type stmtRunner struct {
	t    *testing.T
	conn *sql.Conn
}

func newStmtRunner(t *testing.T, db *sql.DB) *stmtRunner {
	t.Helper()
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	return &stmtRunner{t: t, conn: c}
}

func (r *stmtRunner) exec(ctx context.Context, text string) error {
	_, err := r.conn.ExecContext(ctx, text)
	return err
}

func (r *stmtRunner) close() { r.conn.Close() }

// liveConfig is the instance the live tests run against, or a skip.
func liveConfig(t *testing.T) *Config {
	t.Helper()
	server := os.Getenv("SQL_AUDITOR_LIVE_SERVER")
	if server == "" {
		t.Skip("SQL_AUDITOR_LIVE_SERVER is not set")
	}
	return &Config{
		Server: server, User: os.Getenv("SQL_AUDITOR_LIVE_USER"),
		Password: os.Getenv("SQL_AUDITOR_LIVE_PASSWORD"),
		AppName:  "sql-auditor-live-test", Encrypt: true, TrustCert: true,
		ConnectTimeout: 5 * time.Second,
	}
}

// startBlockingWatch opens two connections on one handle. With the pool of
// one that Open gives, the second waited out its deadline: the watch started
// seven seconds late on the lab and never had an identity read.
func TestLiveStartBlockingWatchOpensBothConnections(t *testing.T) {
	cfg := liveConfig(t)
	began := time.Now()
	w, stop, reason := startBlockingWatch(context.Background(), cfg, map[string]bool{}, 1)
	took := time.Since(began)
	if w == nil {
		t.Fatalf("the watch did not start: %s", reason)
	}
	defer stop()
	if w.identify == nil {
		t.Errorf("the watch started without its identity connection")
	}
	if took >= cfg.ConnectTimeout {
		t.Errorf("the watch took %v to start: a connection waited out its deadline", took)
	}
	t.Logf("started in %v", took)
}

// A watch whose connection is killed under it reconnects and goes on, and
// the manifest's warnings say it happened. KILL is the closest the lab comes
// to a connection a failover or a network blip has cut.
func TestLiveWatchReconnectsAfterItsConnectionIsKilled(t *testing.T) {
	cfg := liveConfig(t)
	cfg.AppName = "sql-auditor-live-reconnect"
	ctx := context.Background()
	setupDB, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer setupDB.Close()
	setup := newStmtRunner(t, setupDB)
	defer setup.close()
	var spid int
	if err := setup.conn.QueryRowContext(ctx, "SELECT @@SPID").Scan(&spid); err != nil {
		t.Fatal(err)
	}
	w, stop, reason := startBlockingWatch(ctx, cfg, map[string]bool{}, spid)
	if w == nil {
		t.Fatalf("the watch did not start: %s", reason)
	}
	defer stop()
	_, cancel := context.WithCancelCause(ctx)
	w.arm(spid, cancel)
	defer w.disarm()

	rows, err := setup.conn.QueryContext(ctx, "SELECT session_id FROM sys.dm_exec_sessions WHERE program_name = @p",
		sql.Named("p", cfg.AppName+watchAppSuffix))
	if err != nil {
		t.Fatal(err)
	}
	var watchers []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		watchers = append(watchers, id)
	}
	rows.Close()
	if len(watchers) == 0 {
		t.Fatal("no session carries the watch's application name")
	}
	for _, id := range watchers {
		if err := setup.exec(ctx, fmt.Sprintf("KILL %d", id)); err != nil {
			t.Fatalf("KILL %d: %v", id, err)
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && len(w.warnings()) == 0 {
		time.Sleep(100 * time.Millisecond)
	}
	if r := w.stoppedReason(); r != "" {
		t.Fatalf("the watch stopped: %s", r)
	}
	warns := w.warnings()
	if len(warns) != 1 || !strings.Contains(warns[0], "reconnected") {
		t.Fatalf("warnings = %q", warns)
	}
	// And the new connection answers the polls after it: two more ticks.
	time.Sleep(2500 * time.Millisecond)
	if r, n := w.stoppedReason(), len(w.warnings()); r != "" || n != 1 {
		t.Errorf("after the reconnect: stopped = %q, %d warning(s)", r, n)
	}
	t.Logf("killed %v; %s", watchers, warns[0])
}
