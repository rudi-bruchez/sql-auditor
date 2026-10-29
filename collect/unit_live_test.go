package collect

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A lock timeout in the middle of a batch, with result sets still to come after
// it: the shape of 060.columns behind a Sch-M, which held a client run for
// hours. Without discardConn the next statement on the session waits for the
// driver's abandoned reader for ever; the test gives it 20 seconds.
func TestLiveAMidBatchErrorDoesNotHangTheSession(t *testing.T) {
	cfg := liveConfig(t)
	cfg.Database = "master"
	cfg.QueryTimeout = 30 * time.Second
	ctx := context.Background()
	// A committed table under an open ALTER: a table created in the open
	// transaction is not resolved at all, and its wait ignores LOCK_TIMEOUT.
	table := fmt.Sprintf("dbo.zz_lock_probe_%d", time.Now().UnixNano())

	holderDB, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer holderDB.Close()
	holder, err := holderDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(ctx, "CREATE TABLE "+table+" (i int);"); err != nil {
		t.Fatal(err)
	}
	defer holder.ExecContext(ctx, "IF @@TRANCOUNT > 0 ROLLBACK; DROP TABLE "+table+";")
	if _, err := holder.ExecContext(ctx, "BEGIN TRAN; ALTER TABLE "+table+" ADD j int NULL;"); err != nil {
		t.Fatal(err)
	}

	db, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Closing a hung sql.Conn waits for the hung statement: skipped then, so
	// that the test fails instead of holding go test until its own timeout.
	hung := false
	defer func() {
		if !hung {
			conn.Close()
		}
	}()

	s := Script{Path: "70.schema/999.lock-probe.sql", TimeoutSec: 60, SQL: "SET NOCOUNT ON; SET LOCK_TIMEOUT 1000; " +
		// In EXEC so that it compiles after the SET: in the batch, the table's
		// lock is taken at compile time, before LOCK_TIMEOUT applies.
		"CREATE TABLE #t (i int); EXEC (N'INSERT #t SELECT i FROM " + table + "'); SELECT COUNT(*) AS a FROM #t; " +
		"SELECT TOP (500) o.name FROM sys.all_objects AS o; SELECT TOP (500) c.name FROM sys.all_columns AS c;",
		Results: []ResultSpec{{Name: "a", Shape: ShapeArray}, {Name: "c", Shape: ShapeArray},
			{Name: "d", Shape: ShapeArray}}}
	m, rw := &Manifest{}, newRunWriter(t.TempDir(), 1<<20)
	// The unit, then what the run does next on the session: runUnit's own way
	// out moves the session back to master, which is where the hang was found.
	var uerr error
	done := make(chan error, 1)
	go func() {
		_, uerr = runUnit(ctx, ctx, conn, Options{Config: cfg}, m, rw, s, DatabaseFolder{}, nil, 0)
		if connClosed(conn) {
			done <- nil
			return
		}
		done <- ResetSession(ctx, conn, "master")
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the session after the failed unit: %v", err)
		}
	case <-time.After(20 * time.Second):
		hung = true
		t.Fatal("the session hung after a mid-batch lock timeout")
	}
	if sqlErrorNumber(uerr) != 1222 {
		t.Fatalf("the unit returned %v, want error 1222", uerr)
	}
	if !connClosed(conn) {
		t.Error("the failed unit kept its session; the run would reuse it")
	}
}

// A collector that fills a #temp table, then succeeds or fails. The collection keeps
// one connection for the whole run, so whatever a unit leaves in tempdb stays
// there until the run ends unless the unit's failure clears it.
// 10.system/040.error-log is the shape: it copies the whole error log into
// #log before reading it. The table is looked for from a second connection,
// in tempdb itself, which is where it would cost the instance.
func TestLiveAUnitLeavesNoTempTable(t *testing.T) {
	cfg := liveConfig(t)
	cfg.Database = "master"
	cfg.QueryTimeout = 30 * time.Second
	ctx := context.Background()
	observer, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	const fill = "SET NOCOUNT ON; CREATE TABLE #impl_a_probe (i int); INSERT #impl_a_probe VALUES (1); "
	cases := []struct {
		name     string
		sql      string
		timeout  int
		succeeds bool
	}{
		{"a timeout", fill + "WAITFOR DELAY '00:00:05'; SELECT i FROM #impl_a_probe;", 1, false},
		// Error 245 ends the batch and leaves the connection healthy, which is
		// the case the ROLLBACK and USE of ResetSession never reached.
		{"a batch-ending error", fill + "SELECT CAST('x' AS int) AS i;", 0, false},
		// 040 never drops #log, so success left it behind as well.
		{"success", fill + "SELECT i FROM #impl_a_probe;", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, err := Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { conn.Close() }()
			var spid int
			if err := conn.QueryRowContext(ctx, "SELECT @@SPID").Scan(&spid); err != nil {
				t.Fatal(err)
			}
			s := Script{Path: "10.system/999.temp-probe.sql", TimeoutSec: c.timeout, SQL: c.sql,
				Results: []ResultSpec{{Name: "rows", Shape: ShapeArray}}}
			m, rw := &Manifest{}, newRunWriter(t.TempDir(), 1<<20)
			_, uerr := runUnit(ctx, ctx, conn, Options{Config: cfg}, m, rw, s, DatabaseFolder{}, nil, 0)
			if (uerr == nil) != c.succeeds {
				t.Fatalf("the unit returned %v", uerr)
			}
			// What the run loop does after every unit, dead connection or not:
			// the pool dials a new one for a connection the driver marked bad.
			var after int
			conn, after, err = recycleConn(ctx, db, conn, cfg)
			if err != nil {
				t.Fatalf("recycleConn: %v", err)
			}
			// A failed unit discards its session (discardConn): only success keeps it.
			if c.succeeds && after != spid {
				t.Errorf("the session changed from %d to %d on a healthy connection", spid, after)
			}
			var left int
			if err := observer.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM tempdb.sys.tables WHERE name LIKE '#impl[_]a[_]probe%'").Scan(&left); err != nil {
				t.Fatal(err)
			}
			t.Logf("unit error: %v; session %d then %d; #impl_a_probe tables left in tempdb: %d",
				uerr, spid, after, left)
			if left != 0 {
				t.Errorf("the unit's #temp table is still in tempdb")
			}
		})
	}
}
