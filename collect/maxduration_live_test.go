package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

// The bound against a real instance. Skipped unless SQL_AUDITOR_LIVE_SERVER
// is set (liveConfig):
//
//	SQL_AUDITOR_LIVE_SERVER=localhost,11533 SQL_AUDITOR_LIVE_USER=sa \
//	SQL_AUDITOR_LIVE_PASSWORD=... go test ./collect/ -run '^TestLiveMaxDuration' -v
//
// Nothing here creates a database or a table: every corpus is read-only
// instance collectors, and dbo.ZzMaxDurMissing is a name that is never
// created. The tests that set pauseHook reset it with a defer and never call
// t.Parallel, since the hook is a package variable.

// lockedBuf is a writer the run and the hook can share.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// maxDurRecorder keeps what Run hands its Observer that these tests assert
// on: the skips, the verdicts, and when the first unit came back.
type maxDurRecorder struct {
	mu        sync.Mutex
	skips     []*UnitSkipped
	verdicts  []Verdict
	firstDone time.Time
}

func (r *maxDurRecorder) Planned(int)                {}
func (r *maxDurRecorder) UnitStarted(string, string) {}
func (r *maxDurRecorder) UnitDone(_, _ string, _ int64, _ time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.firstDone.IsZero() {
		r.firstDone = time.Now()
	}
	var sk *UnitSkipped
	if errors.As(err, &sk) {
		r.skips = append(r.skips, sk)
	}
}
func (r *maxDurRecorder) ScriptSkipped(string, string, string) {}
func (r *maxDurRecorder) Phase(string)                         {}
func (r *maxDurRecorder) Finished(v Verdict) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.verdicts = append(r.verdicts, v)
}

func maxDurScript(timeout, sql string) []byte {
	return []byte("-- @scope:       instance\n-- @resultsets:  root:object\n-- @timeout:     " + timeout + "\n" +
		contractPreamble + sql + "\n")
}

const maxDurSelect = "SELECT @@VERSION AS [version] OPTION (RECOMPILE, MAXDOP 1);"

var (
	maxDurOne = fstest.MapFS{
		"queries/10.system/901.a.sql": {Data: maxDurScript("60", maxDurSelect)},
	}
	maxDurFast = fstest.MapFS{
		"queries/10.system/901.a.sql": {Data: maxDurScript("60", maxDurSelect)},
		"queries/10.system/902.b.sql": {Data: maxDurScript("60", maxDurSelect)},
	}
	// A first unit that fails on its own at once with 208, then a second.
	maxDurMissing = fstest.MapFS{
		"queries/10.system/901.a.sql": {Data: maxDurScript("60", "SELECT * FROM dbo.ZzMaxDurMissing OPTION (RECOMPILE, MAXDOP 1);")},
		"queries/10.system/902.b.sql": {Data: maxDurScript("60", maxDurSelect)},
	}
	// A first unit that would wait a minute under a 30-minute @timeout.
	maxDurWait = fstest.MapFS{
		"queries/10.system/901.a.sql": {Data: maxDurScript("1800", "WAITFOR DELAY '00:01:00';\n"+maxDurSelect)},
		"queries/10.system/902.b.sql": {Data: maxDurScript("60", maxDurSelect)},
	}
	// A first unit whose own one-second @timeout expires first.
	maxDurOwnTimeout = fstest.MapFS{
		"queries/10.system/901.a.sql": {Data: maxDurScript("1", "WAITFOR DELAY '00:00:20';\n"+maxDurSelect)},
		"queries/10.system/902.b.sql": {Data: maxDurScript("60", maxDurSelect)},
	}
)

func maxDurConfig(t *testing.T, out string, bound time.Duration) Config {
	c := *liveConfig(t)
	// master, as TestLiveRunAddressReachesTheRerunGuard sets it: an
	// instance-scope unit run with an empty Database fails with 911 on the lab.
	c.Database, c.OutputDir = "master", out
	c.QueryTimeout = 30 * time.Second
	c.QueryStoreDays, c.QueryStoreTop = 7, 50
	c.MaxDuration = bound
	return c
}

type maxDurOut struct {
	code     int
	err      error
	m        Manifest
	human    string
	progress string
	debug    string
	rec      *maxDurRecorder
	began    time.Time
}

// maxDurRun runs one collection and reads back the manifest it wrote: the run
// folder's, or the failed-run folder's for a run that ended before it, never
// a run set aside as superseded.
func maxDurRun(t *testing.T, ctx context.Context, corpus fstest.MapFS, out string, now time.Time, bound time.Duration, dbg *lockedBuf) maxDurOut {
	t.Helper()
	c := maxDurConfig(t, out, bound)
	if dbg == nil {
		dbg = &lockedBuf{}
	}
	var prog lockedBuf
	rec := &maxDurRecorder{}
	began := time.Now()
	code, err := Run(ctx, Options{Config: &c, Corpus: corpus, Root: "queries", Now: now,
		Progress: &prog, Debug: dbg, Observer: rec})
	all, _ := filepath.Glob(filepath.Join(out, "*", manifestJSONName))
	var dirs []string
	for _, p := range all {
		if !strings.Contains(p, ".superseded-") {
			dirs = append(dirs, filepath.Dir(p))
		}
	}
	if len(dirs) != 1 {
		t.Fatalf("want one manifest of this run in %s, found %v", out, dirs)
	}
	b, rerr := os.ReadFile(filepath.Join(dirs[0], manifestJSONName))
	if rerr != nil {
		t.Fatal(rerr)
	}
	var m Manifest
	if jerr := json.Unmarshal(b, &m); jerr != nil {
		t.Fatal(jerr)
	}
	human, _ := os.ReadFile(filepath.Join(dirs[0], manifestHumanName))
	o := maxDurOut{code, err, m, string(human), prog.String(), dbg.String(), rec, began}
	t.Logf("code=%d err=%v cancelled=%v reached=%v verdicts=%+v watch=%v/%q",
		code, err, m.Run.Cancelled, m.Run.MaxDurationReached, rec.verdicts, m.BlockingWatch.Enabled, m.BlockingWatch.Reason)
	for _, e := range m.Errors {
		t.Logf("  error: %s: %s (sql %d)", e.Script, e.Message, e.SQLError)
	}
	for _, s := range m.Skipped {
		t.Logf("  skipped: %s: %s", s.Script, s.Reason)
	}
	t.Logf("  warnings: %q", m.Warnings)
	t.Logf("  progress: %q", o.progress)
	return o
}

func (o maxDurOut) verdict(t *testing.T) Verdict {
	t.Helper()
	if len(o.rec.verdicts) != 1 {
		t.Fatalf("Finished was called %d times, want once", len(o.rec.verdicts))
	}
	return o.rec.verdicts[0]
}

// noteLines are the lines of Progress that the bound's note could be.
func noteLines(progress string) []string {
	var out []string
	for _, l := range strings.Split(progress, "\n") {
		if strings.HasPrefix(l, "note: the collection reached") {
			out = append(out, l)
		}
	}
	return out
}

// The verdict of a run with no bound: the count of collected units is Run's,
// a failed unit is not collected, and a failure of the run's own is Failed.
func TestLiveMaxDurationVerdictOfAnUnboundedRun(t *testing.T) {
	clean := maxDurRun(t, context.Background(), maxDurFast, t.TempDir(), time.Now(), 0, nil)
	if v := clean.verdict(t); clean.code != 0 || v != (Verdict{Collected: 2}) {
		t.Errorf("clean run: code %d, verdict %+v; want 0 and {Collected: 2}", clean.code, v)
	}
	failed := maxDurRun(t, context.Background(), maxDurMissing, t.TempDir(), time.Now(), 0, nil)
	if v := failed.verdict(t); failed.code != 2 || v != (Verdict{Failed: true, Collected: 1}) {
		t.Errorf("a failed unit: code %d, verdict %+v; want 2 and {Failed: true, Collected: 1}", failed.code, v)
	}
	if _, ok := clean.m.Config["max_duration_sec"]; ok {
		t.Error("an unbounded run recorded max_duration_sec")
	}
}
