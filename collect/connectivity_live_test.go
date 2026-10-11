package collect

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 041 reported the exception buffer as one row per error, which could not say
// whether a full buffer of one error was a burst or a trickle. The test raises
// divisions by zero, then requires the hourly set to account for every record
// of the per-error set, each of its rows to start on the hour, the errors just
// raised to be there, and the root to count the buffer.
func TestLiveConnectivityExceptionsByHour(t *testing.T) {
	cfg := liveConfig(t)
	cfg.Database = "master"
	cfg.QueryTimeout = 120 * time.Second
	ctx := context.Background()
	db, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	const raised = 5
	for i := 0; i < raised; i++ {
		if _, err := conn.ExecContext(ctx, "SELECT 1/0;"); err == nil {
			t.Fatal("a division by zero did not fail")
		}
	}

	sqlText, err := os.ReadFile(filepath.Join("..", "queries", "10.system", "041.connectivity.sql"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := conn.QueryContext(ctx, string(sqlText))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var sets [][]map[string]any
	for {
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var set []map[string]any
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			row := map[string]any{}
			for i, c := range cols {
				row[c] = vals[i]
			}
			set = append(set, row)
		}
		sets = append(sets, set)
		if !rows.NextResultSet() {
			break
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(sets) != 5 {
		t.Fatalf("041 returned %d result sets, want root, ring_buffers, connectivity, exceptions, exceptions_by_hour", len(sets))
	}
	root, exceptions, byHour := sets[0][0], sets[3], sets[4]

	if asInt(root["exception_records"]) < raised {
		t.Errorf("root exception_records = %v after %d errors", root["exception_records"], raised)
	}
	var perError, perHour, divByZero int64
	for _, r := range exceptions {
		perError += asInt(r["occurrences"])
	}
	for _, r := range byHour {
		perHour += asInt(r["occurrences"])
		h, ok := r["hour"].(time.Time)
		if !ok || h.Minute() != 0 || h.Second() != 0 || h.Nanosecond() != 0 {
			t.Errorf("hour %v is not the start of an hour", r["hour"])
		}
		if asInt(r["error_number"]) == 8134 {
			divByZero += asInt(r["occurrences"])
		}
	}
	if perHour != perError {
		t.Errorf("exceptions_by_hour holds %d records, exceptions %d: the two sets describe different records", perHour, perError)
	}
	if divByZero < raised {
		t.Errorf("exceptions_by_hour holds %d 8134 records, want at least %d", divByZero, raised)
	}
}
