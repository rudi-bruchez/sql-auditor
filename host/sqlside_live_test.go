package host

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/rudi-bruchez/sql-auditor/collect"
)

// liveConfig is the instance the live tests run against, or a skip. It is
// collect's helper of the same name, which a test of another package cannot call.
func liveConfig(t *testing.T) *collect.Config {
	t.Helper()
	server := os.Getenv("SQL_AUDITOR_LIVE_SERVER")
	if server == "" {
		t.Skip("SQL_AUDITOR_LIVE_SERVER is not set")
	}
	return &collect.Config{
		Server: server, User: os.Getenv("SQL_AUDITOR_LIVE_USER"),
		Password: os.Getenv("SQL_AUDITOR_LIVE_PASSWORD"),
		AppName:  "sql-auditor-live-test", Encrypt: true, TrustCert: true,
		ConnectTimeout: 5 * time.Second,
	}
}

// The lab instance is a default instance, so InstanceName comes back NULL.
func TestLiveReadFactsAndJobSteps(t *testing.T) {
	in := NewInstance(liveConfig(t))
	defer in.Close()
	ctx := context.Background()
	f, err := in.Facts(ctx)
	if err != nil {
		t.Fatalf("facts: %v", err)
	}
	if f.ServerName == "" || f.NetBIOS == "" {
		t.Errorf("facts without a name: %+v", f)
	}
	if f.InstanceName != nil {
		t.Errorf("InstanceName = %q on a default instance, want nil", *f.InstanceName)
	}
	steps, refused, err := in.Steps(ctx)
	if err != nil || refused {
		t.Fatalf("job steps: refused %v, %v", refused, err)
	}
	t.Logf("%s on %s, version %s, %d job steps with an output file", f.ServerName, f.NetBIOS, f.Version, len(steps))
}

// A step written in a transaction still open holds its key locked. Read
// uncommitted, the job steps are read without waiting for it (the harm review
// measured 23 s behind such a transaction), and the session runs under the
// preamble. Measured on SQL Server 2022: a schema modification lock still
// holds the query at compile time whatever LOCK_TIMEOUT says, so 1222 cannot
// be produced here and is tested away from the instance.
func TestLiveJobStepsNotHeldByOpenTransaction(t *testing.T) {
	cfg := liveConfig(t)
	ctx := context.Background()
	db, err := collect.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	holder, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(ctx, `SET NOCOUNT ON; BEGIN TRAN; DECLARE @id uniqueidentifier;
EXEC msdb.dbo.sp_add_job @job_name = N'sql-auditor live lock probe', @job_id = @id OUTPUT;
EXEC msdb.dbo.sp_add_jobstep @job_id = @id, @step_name = N'probe', @command = N'SELECT 1', @output_file_name = N'C:\probe\out.txt';`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := holder.ExecContext(ctx, "IF @@TRANCOUNT > 0 ROLLBACK;"); err != nil {
			t.Errorf("rollback: %v", err)
		}
		var left int
		if err := holder.QueryRowContext(ctx, "SELECT COUNT(*) FROM msdb.dbo.sysjobs WHERE name = N'sql-auditor live lock probe';").Scan(&left); err != nil || left != 0 {
			t.Errorf("probe job left behind: %d (%v)", left, err)
		}
	}()

	in := NewInstance(cfg)
	defer in.Close()
	start := time.Now()
	_, refused, err := in.Steps(ctx)
	if took := time.Since(start); err != nil || refused || took > 11*time.Second {
		t.Fatalf("job steps after %v: refused %v, %v", took, refused, err)
	}
	var lockTimeout int
	var isolation int16
	if err := in.conn.QueryRowContext(ctx, "SELECT lock_timeout, transaction_isolation_level FROM sys.dm_exec_sessions WHERE session_id = @@SPID;").Scan(&lockTimeout, &isolation); err != nil {
		t.Fatal(err)
	}
	if lockTimeout != 10000 || isolation != 1 {
		t.Errorf("session lock_timeout %d, isolation %d; want 10000 and 1 (read uncommitted)", lockTimeout, isolation)
	}
}
