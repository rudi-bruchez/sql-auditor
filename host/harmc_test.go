package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"

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

// A refused read of the job steps names both rights it needs: sysjobs_view
// answers 229 without SQLAgentReaderRole, and sysjobsteps without its grant.
func TestHarmC_StepsRefusedNamesBothRights(t *testing.T) {
	e := matchedEnv()
	e.StepsRefused = true
	res := readNamed(t, fakeCalls(), "070.job-outputs", e, &recordingSink{})
	if res.Status != StatusRefused || !strings.Contains(res.Reason, "SQLAgentReaderRole") || !strings.Contains(res.Reason, "SELECT on msdb.dbo.sysjobsteps") {
		t.Errorf("status %s, reason %q; want refused naming both rights", res.Status, res.Reason)
	}
}

// The manifest tells the DBA who chose the job output paths, and what the
// extension logs hold beyond names.
func TestHarmC_DisclosureNamesSysadminAndExtensionSettings(t *testing.T) {
	for _, s := range []string{"only a sysadmin can set them", "check every path under Copied files", "public setting"} {
		if !strings.Contains(strings.Join(strings.Fields(disclosure), " "), s) {
			t.Errorf("the disclosure lacks %q", s)
		}
	}
}

// The guards the harm review could break without a test noticing.

// The PowerShell child: the executable by its absolute path, no profile, the
// script as a visible command line, PSModulePath narrowed, 120 s at most.
func TestHarmC_PowerShellCommand(t *testing.T) {
	if psTimeout != 120*time.Second {
		t.Errorf("psTimeout = %v, want 120 s", psTimeout)
	}
	cmd, ctx, cancel := psCommand(context.Background(), `C:\Windows\system32`, "Get-Date", []string{`PSModulePath=C:\planted`, `PATH=C:\Windows`})
	defer cancel()
	want := []string{`C:\Windows\system32\WindowsPowerShell\v1.0\powershell.exe`, "-NoProfile", "-NonInteractive", "-Command", "Get-Date"}
	if fmt.Sprint(cmd.Args) != fmt.Sprint(want) {
		t.Errorf("args %q, want %q", cmd.Args, want)
	}
	if want := []string{`PATH=C:\Windows`, `PSModulePath=C:\Windows\system32\WindowsPowerShell\v1.0\Modules`}; fmt.Sprint(cmd.Env) != fmt.Sprint(want) {
		t.Errorf("env %q, want %q", cmd.Env, want)
	}
	d, ok := ctx.Deadline()
	if left := time.Until(d); !ok || left > 120*time.Second || left < 110*time.Second {
		t.Errorf("deadline in %v (set %v), want 120 s", left, ok)
	}
}

func TestHarmC_JobStepsQueryHint(t *testing.T) {
	if !strings.HasSuffix(jobStepsSQL, " OPTION (RECOMPILE, MAXDOP 1);") {
		t.Errorf("the job steps query does not end with OPTION (RECOMPILE, MAXDOP 1):\n%s", jobStepsSQL)
	}
}

// A capped query stops the reader at its cap plus one event, the one that
// tells a truncation apart, rather than reading the channel through.
func TestHarmC_CapStopsAtOneMore(t *testing.T) {
	c := fakeCalls()
	c.capOverride = 3
	fed := 0
	c.Query = func(_ context.Context, _, xpath string, _ bool, each func([]byte, string) bool) error {
		if !strings.Contains(xpath, "Level=1") {
			return nil
		}
		for i := range 10 {
			fed++
			if !each(eventAt(100-i, sourcesSince.Add(time.Duration(100-i)*time.Hour)), "") {
				break
			}
		}
		return nil
	}
	readNamed(t, c, "010.events-system", Env{Since: sourcesSince}, &recordingSink{})
	if fed != 4 {
		t.Errorf("the reader was fed %d events, want the cap of 3 plus one", fed)
	}
}

// The extension logs are copied up to 50 MiB: 30 and 20 MiB fit exactly, one
// more byte does not.
func TestHarmC_ExtensionBudgetIsFiftyMiB(t *testing.T) {
	fsys := sizedFS{
		"a.log": {name: "a.log", size: 30 * mib, mod: sourcesSince.Add(3 * time.Hour)},
		"b.log": {name: "b.log", size: 20 * mib, mod: sourcesSince.Add(2 * time.Hour)},
		"c.log": {name: "c.log", size: 1, mod: sourcesSince.Add(time.Hour)},
	}
	c := fakeCalls()
	c.AzurePlugins = `C:\WindowsAzure\Logs\Plugins`
	c.OpenDir = func(string) (fs.FS, error) { return fsys, nil }
	s := &recordingSink{}
	res := readNamed(t, c, "060.vm-extensions", Env{Since: sourcesSince}, s)
	got := map[string]string{}
	for _, l := range listedIn(t, s) {
		got[l.Path] = fmt.Sprintf("copied %v %s", l.Copied, l.Reason)
	}
	want := map[string]string{
		c.AzurePlugins + `\a.log`: "copied true ",
		c.AzurePlugins + `\b.log`: "copied true ",
		c.AzurePlugins + `\c.log`: "copied false over the 50 MiB budget",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) || res.Count != 2 {
		t.Errorf("listed %v, count %d\nwant %v, count 2", got, res.Count, want)
	}
}

// An error that only Close reports, the zip's buffered tail failing to reach
// the disk, removes the archive and exits 2, as a write error does.
func TestHarmC_RunFailsOnCloseError(t *testing.T) {
	var putErr error
	p := fakePlatform(Source{Name: "010.a", Read: func(_ context.Context, _ Env, put FileSink) SourceResult {
		putErr = put("010.a.json", strings.NewReader("{}"))
		return SourceResult{Status: StatusRead, Count: 1}
	}})
	o := runOptions(t, p)
	o.wrapArchive = func(w io.Writer) io.Writer { return &failingWriter{w: w, fail: true} }
	code := Run(context.Background(), o)
	if putErr != nil {
		t.Fatalf("the source's write failed (%v): the error must come from Close for this test", putErr)
	}
	if code != 2 || len(dirEntries(t, o.To)) != 0 || strings.Contains(stdout(o), ".zip") {
		t.Errorf("exit %d, left %v, stdout %q; want 2, nothing, no archive line", code, dirEntries(t, o.To), stdout(o))
	}
}
