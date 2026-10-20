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
