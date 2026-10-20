package host

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	mssql "github.com/microsoft/go-mssqldb"
)

// What the host command holds and copies: the bounds added by the third batch
// of the harm review.

// Every query starts with the collector contract's preamble, written out here
// rather than shared, so that a change to either is a decision.
func TestHarmC_QueriesStartWithPreamble(t *testing.T) {
	const want = "SET NOCOUNT ON;\nSET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;\nSET LOCK_TIMEOUT 10000;\n"
	for name, q := range map[string]string{"facts": factsSQL, "job steps": jobStepsSQL} {
		if !strings.HasPrefix(q, want) {
			t.Errorf("%s query does not start with the preamble:\n%.160s", name, q)
		}
	}
}

// Error 1222 fails the source with a reason naming the lock timeout; it is not
// a refusal, and any other error is left as it was.
func TestHarmC_LockTimeoutFailsTheSource(t *testing.T) {
	timeout := fmt.Errorf("job steps: %w", mssql.Error{Number: 1222, Message: "Lock request time out period exceeded."})
	if refusal(timeout) {
		t.Errorf("error 1222 is a refusal")
	}
	e := matchedEnv()
	e.StepsErr = lockTimedOut(timeout)
	res := readNamed(t, fakeCalls(), "070.job-outputs", e, &recordingSink{})
	if res.Status != StatusFailed || !strings.Contains(res.Reason, "lock timeout") || !strings.Contains(res.Reason, "1222") {
		t.Errorf("status %s, reason %q; want failed naming the lock timeout", res.Status, res.Reason)
	}
	other := errors.New("connection reset")
	if lockTimedOut(other) != other {
		t.Errorf("an error other than 1222 was rewritten: %v", lockTimedOut(other))
	}
}

// The job outputs of every step share one budget of 100 MiB, counted in the
// bytes copied (the last MiB of a 3 MiB file counts 1 MiB): 100 steps fit
// exactly, and the files of the steps after are noted, not copied.
func TestHarmC_JobOutputBudgetShared(t *testing.T) {
	const steps = 102
	fsys := sizedFS{}
	e := matchedEnv()
	for i := 1; i <= steps; i++ {
		n := fmt.Sprintf("step%03d.txt", i)
		fsys[n] = sizedInfo{name: n, size: 3 * mib, mod: sourcesSince}
		e.Steps = append(e.Steps, JobStep{Job: "J", StepID: i, OutputFile: `X:\Logs\` + n})
	}
	c := fakeCalls()
	c.OpenDir = func(string) (fs.FS, error) { return fsys, nil }
	c.OpenFile = fsOpen(fsys, `X:\Logs`)
	s := &recordingSink{}
	res := readNamed(t, c, "070.job-outputs", e, s)
	var total int64
	for i, n := range s.names {
		if n != "070.job-outputs.json" {
			total += s.sizes[i]
		}
	}
	if len(res.Copied) != 100 || total != 100*mib {
		t.Errorf("copied %d files, %d bytes; want 100 files, 100 MiB", len(res.Copied), total)
	}
	want := []string{
		`J step 101: X:\Logs\step101.txt: budget reached, not copied`,
		`J step 102: X:\Logs\step102.txt: budget reached, not copied`,
	}
	if fmt.Sprint(res.Notes) != fmt.Sprint(want) {
		t.Errorf("notes %q, want %q", res.Notes, want)
	}
}

// A SQL Writer log is copied through the tail reader: 10 MiB is copied whole,
// one byte more is cut to its last 10 MiB and recorded as cut.
func TestHarmC_SQLWriterLogBounded(t *testing.T) {
	fsys := sizedFS{
		"SqlWriterLogger.txt":  {name: "SqlWriterLogger.txt", size: 10 * mib, mod: sourcesSince},
		"SqlWriterLogger1.txt": {name: "SqlWriterLogger1.txt", size: 10*mib + 1, mod: sourcesSince},
	}
	c := fakeCalls()
	c.ReadFile = func(string) ([]byte, error) { return []byte("[Logging]\r\n"), nil }
	c.OpenDir = func(string) (fs.FS, error) { return fsys, nil }
	s := &recordingSink{}
	res := readNamed(t, c, "035.sqlwriter", Env{Since: sourcesSince}, s)
	got := map[string]string{}
	for _, f := range res.Copied {
		got[f.Archived] = fmt.Sprintf("size %d cut %v", f.Size, f.Cut)
	}
	for i, n := range s.names {
		got[n] += fmt.Sprintf(", %d copied", s.sizes[i])
	}
	want := map[string]string{
		"035.sqlwriter/SqlWriterLogger.txt":  fmt.Sprintf("size %d cut false, %d copied", 10*mib, 10*mib),
		"035.sqlwriter/SqlWriterLogger1.txt": fmt.Sprintf("size %d cut true, %d copied", 10*mib+1, 10*mib),
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

// The manifest says how much of a cut file was kept, which differs by source.
func TestHarmC_ManifestNamesEachCut(t *testing.T) {
	big := sizedFS{
		"SqlWriterLogger.txt": {name: "SqlWriterLogger.txt", size: 11 * mib, mod: sourcesSince},
		"out.txt":             {name: "out.txt", size: 3 * mib, mod: sourcesSince},
	}
	c := fakeCalls()
	c.ReadFile = func(string) ([]byte, error) { return []byte("[Logging]\r\n"), nil }
	c.OpenDir = func(string) (fs.FS, error) { return big, nil }
	e := matchedEnv()
	e.Steps = []JobStep{{Job: "J", StepID: 1, OutputFile: `X:\Logs\out.txt`}}
	var sources []Source
	for _, name := range []string{"035.sqlwriter", "070.job-outputs"} {
		res := readNamed(t, c, name, e, &recordingSink{})
		sources = append(sources, Source{Name: name, Read: func(context.Context, Env, FileSink) SourceResult { return res }})
	}
	m, _ := manifestOf(t, runOptions(t, fakePlatform(sources...)))
	for _, s := range []string{
		fmt.Sprintf(`SqlWriterLogger.txt -> 035.sqlwriter/SqlWriterLogger.txt (%d bytes, last 10 MiB only)`, 11*mib),
		fmt.Sprintf(`X:\Logs\out.txt -> 070.job-outputs/J_1/out.txt (%d bytes, last 1 MiB only)`, 3*mib),
	} {
		if !strings.Contains(m, s) {
			t.Errorf("the manifest lacks %q:\n%s", s, m)
		}
	}
}
