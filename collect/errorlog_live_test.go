package collect

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// errorLogStatus runs 10.system/040.error-log.sql on conn and returns its
// status row, the second result set, as column name to value.
func errorLogStatus(t *testing.T, ctx context.Context, conn *sql.Conn) map[string]any {
	t.Helper()
	sqlText, err := os.ReadFile(filepath.Join("..", "queries", "10.system", "040.error-log.sql"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := conn.QueryContext(ctx, string(sqlText))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for set := 0; ; set++ {
		if set == 1 {
			cols, err := rows.Columns()
			if err != nil {
				t.Fatal(err)
			}
			if !rows.Next() {
				t.Fatalf("the status set is empty: %v", rows.Err())
			}
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			out := map[string]any{}
			for i, c := range cols {
				out[c] = vals[i]
			}
			return out
		}
		for rows.Next() {
		}
		if !rows.NextResultSet() {
			t.Fatalf("the script returned %d result sets: %v", set+1, rows.Err())
		}
	}
}

func asInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case bool:
		if x {
			return 1
		}
		return 0
	}
	return -1
}

// The size guard of 040 read the whole log when sp_enumerrorlogs failed, so the
// 50 MB bound held only where the size was known. The two procedures can be
// granted apart: a login with EXECUTE on sp_readerrorlog and a DENY on
// sp_enumerrorlogs fails the size read with error 229 and could still read the
// log. The test plants that login, runs the collector as it, and requires the
// log to be left unread with the reason in status; then runs it as the test
// login to show the ordinary path still reads and measures.
func TestLiveErrorLogIsNotReadWithoutItsSize(t *testing.T) {
	cfg := liveConfig(t)
	cfg.Database = "master"
	cfg.QueryTimeout = 120 * time.Second
	ctx := context.Background()
	db, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	t.Run("size read", func(t *testing.T) {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		st := errorLogStatus(t, ctx, conn)
		if asInt(st["collected"]) != 1 || asInt(st["skipped_size_unknown"]) != 0 || st["log_size_bytes"] == nil {
			t.Fatalf("a login that can measure the log should read it: %v", st)
		}
	})

	t.Run("size refused", func(t *testing.T) {
		const login = "ZzDisclErrLogLive"
		// Open gives a pool of one connection, so each Conn is closed before
		// the next is asked for.
		admin, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		cleanup := "IF USER_ID(N'" + login + "') IS NOT NULL DROP USER " + login + "; " +
			"IF SUSER_ID(N'" + login + "') IS NOT NULL DROP LOGIN " + login + ";"
		if _, err := admin.ExecContext(ctx, cleanup+
			" CREATE LOGIN "+login+" WITH PASSWORD = N'ZzDiscl!Live-7c1e', CHECK_POLICY = OFF;"+
			" GRANT VIEW SERVER STATE TO "+login+";"+
			" CREATE USER "+login+" FOR LOGIN "+login+";"+
			" GRANT EXECUTE ON sys.sp_readerrorlog TO "+login+";"+
			" DENY EXECUTE ON sys.sp_enumerrorlogs TO "+login+";"); err != nil {
			admin.Close()
			t.Fatalf("planting the login: %v", err)
		}
		admin.Close()
		t.Cleanup(func() {
			c, err := db.Conn(context.Background())
			if err != nil {
				t.Errorf("dropping %s: %v", login, err)
				return
			}
			defer c.Close()
			if _, err := c.ExecContext(context.Background(), "USE master; "+cleanup); err != nil {
				t.Errorf("dropping %s: %v", login, err)
			}
		})

		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, "EXECUTE AS LOGIN = N'"+login+"';"); err != nil {
			t.Fatal(err)
		}
		defer conn.ExecContext(context.Background(), "REVERT;")
		st := errorLogStatus(t, ctx, conn)
		if asInt(st["collected"]) != 0 {
			t.Errorf("the log was read although its size could not be: %v", st)
		}
		if asInt(st["skipped_size_unknown"]) != 1 || asInt(st["skipped_for_size"]) != 0 {
			t.Errorf("status should say the size was unknown, not too large: %v", st)
		}
		if msg, _ := st["size_error_message"].(string); msg == "" {
			t.Errorf("status should carry why the size is missing: %v", st)
		}
		if st["error_message"] != nil {
			t.Errorf("no read was attempted, so no read error belongs in status: %v", st)
		}
	})
}
