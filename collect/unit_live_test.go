package collect

import (
	"context"
	"testing"
	"time"
)

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
			uerr := runUnit(ctx, conn, Options{Config: cfg}, m, rw, s, DatabaseFolder{}, nil, 0)
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
			if c.timeout == 0 && after != spid {
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
