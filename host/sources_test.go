package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	"unicode/utf8"
)

var sourcesSince = time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)

// fakeCalls answers every primitive with nothing: no event, no file.
func fakeCalls() Calls {
	return Calls{
		Query:      func(context.Context, string, string, bool, func([]byte, string) bool) error { return nil },
		Coverage:   func(string) (Coverage, error) { return Coverage{}, nil },
		PowerShell: func(context.Context, string) ([]byte, error) { return nil, fmt.Errorf("no PowerShell here") },
		Filters:    func() ([]Filter, error) { return nil, nil },
		Power:      func() (PowerPlan, error) { return PowerPlan{}, nil },
		OpenDir:    func(string) (fs.FS, error) { return fstest.MapFS{}, nil },
		ReadFile:   func(string) ([]byte, error) { return nil, fs.ErrNotExist },
		DirCheck:   acceptAll,
	}
}

// openThroughDir is an OpenFile that opens a file in the directory OpenDir
// gives for the part of its path before the last backslash.
func openThroughDir(openDir func(string) (fs.FS, error)) OpenFile {
	return func(p string) (io.ReadSeekCloser, int64, string, string, error) {
		dir := p[:strings.LastIndex(p, `\`)]
		fsys, err := openDir(dir)
		if err != nil {
			return nil, 0, "", "", err
		}
		return fsOpen(fsys, dir)(p)
	}
}

// readNamed runs the source called name.
func readNamed(t *testing.T, c Calls, name string, e Env, s *recordingSink) SourceResult {
	t.Helper()
	if c.OpenFile == nil {
		c.OpenFile = openThroughDir(c.OpenDir)
	}
	for _, src := range BuildSources(c) {
		if src.Name == name {
			return src.Read(context.Background(), e, s.sink(true))
		}
	}
	t.Fatalf("no source %s", name)
	return SourceResult{}
}

func matchedEnv() Env {
	return Env{
		Facts:        &InstanceFacts{ServerName: "SQL01", NetBIOS: "SQL01", ErrorLog: `C:\Log\ERRORLOG`, Version: "16.0.4135.4"},
		MachineMatch: true,
		Since:        sourcesSince,
	}
}

func TestJobOutputsSkippedWithoutFacts(t *testing.T) {
	res := readNamed(t, fakeCalls(), "070.job-outputs", Env{Since: sourcesSince}, &recordingSink{})
	if res.Status != StatusSkipped {
		t.Errorf("status = %s, want skipped", res.Status)
	}
}

func TestJobOutputsSkippedOnMismatch(t *testing.T) {
	c := fakeCalls()
	c.OpenDir = func(string) (fs.FS, error) {
		t.Error("OpenDir called on a machine mismatch")
		return fstest.MapFS{}, nil
	}
	e := matchedEnv()
	e.MachineMatch = false
	e.Steps = []JobStep{{Job: "J", StepID: 1, OutputFile: `C:\out\j.txt`}}
	if res := readNamed(t, c, "070.job-outputs", e, &recordingSink{}); res.Status != StatusSkipped {
		t.Errorf("status = %s, want skipped", res.Status)
	}
}

func TestJobOutputsRefused(t *testing.T) {
	e := matchedEnv()
	e.StepsRefused = true
	if res := readNamed(t, fakeCalls(), "070.job-outputs", e, &recordingSink{}); res.Status != StatusRefused {
		t.Errorf("status = %s, want refused", res.Status)
	}
}

func TestJobOutputsFailedOnStepsError(t *testing.T) {
	e := matchedEnv()
	e.StepsErr = fmt.Errorf("connection reset by peer")
	res := readNamed(t, fakeCalls(), "070.job-outputs", e, &recordingSink{})
	if res.Status != StatusFailed || res.Reason != "connection reset by peer" {
		t.Errorf("got %s %q, want failed with the error text", res.Status, res.Reason)
	}
}

const olaTemplate = `$(ESCAPE_SQUOTE(SQLLOGDIR))\$(ESCAPE_SQUOTE(JOBNAME))_$(ESCAPE_SQUOTE(STEPID))_$(ESCAPE_SQUOTE(DATE))_$(ESCAPE_SQUOTE(TIME)).txt`

func TestJobOutputsNoSQLLogDirBefore2014(t *testing.T) {
	e := matchedEnv()
	e.Facts.Version = "11.0.7001.0"
	e.Steps = []JobStep{{Job: "DatabaseIntegrityCheck - SYSTEM_DATABASES", StepID: 1, OutputFile: olaTemplate}}
	s := &recordingSink{}
	readNamed(t, fakeCalls(), "070.job-outputs", e, s)
	var steps []struct {
		Readable bool
		Notes    []string
	}
	if err := json.Unmarshal(s.data["070.job-outputs.json"], &steps); err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Readable || !slices.Contains(steps[0].Notes, "SQLLOGDIR unknown before SQL Server 2014") {
		t.Errorf("steps = %+v, want one not readable, SQLLOGDIR unknown before SQL Server 2014", steps)
	}
}

func TestJobOutputFoldersDistinct(t *testing.T) {
	mod := sourcesSince.Add(48 * time.Hour)
	c := fakeCalls()
	c.OpenDir = func(dir string) (fs.FS, error) {
		if dir != `C:\out` {
			t.Errorf("OpenDir(%q)", dir)
		}
		return fstest.MapFS{
			"Backup: full_1.txt": {Data: []byte("a"), ModTime: mod},
			"Backup? full_1.txt": {Data: []byte("b"), ModTime: mod},
		}, nil
	}
	tmpl := `C:\out\$(ESCAPE_SQUOTE(JOBNAME))_$(ESCAPE_SQUOTE(STEPID)).txt`
	e := matchedEnv()
	e.Steps = []JobStep{{Job: "Backup: full", StepID: 1, OutputFile: tmpl}, {Job: "Backup? full", StepID: 1, OutputFile: tmpl}}
	s := &recordingSink{}
	res := readNamed(t, c, "070.job-outputs", e, s)
	var got []string
	for _, n := range s.names {
		if strings.HasPrefix(n, "070.job-outputs/") {
			got = append(got, n)
		}
	}
	want := []string{"070.job-outputs/Backup_ full_1/Backup: full_1.txt", "070.job-outputs/Backup_ full_1~2/Backup? full_1.txt"}
	if !slices.Equal(got, want) || res.Status != StatusRead {
		t.Errorf("entries = %q (%s), want %q", got, res.Status, want)
	}
}

func TestExtensionsAbsentWhenRootMissing(t *testing.T) {
	c := fakeCalls()
	c.AzurePlugins = `C:\WindowsAzure\Logs\Plugins`
	missing := filepath.Join(t.TempDir(), "Plugins")
	c.OpenDir = func(string) (fs.FS, error) { return os.DirFS(missing), nil }
	s := &recordingSink{}
	if res := readNamed(t, c, "060.vm-extensions", Env{Since: sourcesSince}, s); res.Status != StatusAbsent {
		t.Errorf("status = %s (%s), want absent", res.Status, res.Reason)
	}
	if len(s.names) != 0 {
		t.Errorf("wrote %q", s.names)
	}
}

// eventAt is a System event with the given record id and time.
func eventAt(rec int, at time.Time) []byte {
	return fmt.Appendf(nil, "<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='P'/><EventID>7</EventID><Level>2</Level><TimeCreated SystemTime='%s'/><EventRecordID>%d</EventRecordID><Channel>System</Channel></System></Event>", at.Format(time.RFC3339Nano), rec)
}

func TestEventsCapDetectsTruncation(t *testing.T) {
	for _, n := range []int{4, 3} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			c := fakeCalls()
			c.capOverride = 3
			c.Query = func(_ context.Context, channel, xpath string, _ bool, each func([]byte, string) bool) error {
				if !strings.Contains(xpath, "Level=1") {
					return nil
				}
				for i := range n { // newest first
					if !each(eventAt(100-i, sourcesSince.Add(time.Duration(100-i)*time.Hour)), "") {
						break
					}
				}
				return nil
			}
			s := &recordingSink{}
			readNamed(t, c, "010.events-system", Env{Since: sourcesSince}, s)
			var f EventsFile
			if err := json.Unmarshal(s.data["010.events-system.json"], &f); err != nil {
				t.Fatal(err)
			}
			third := sourcesSince.Add(98 * time.Hour).Format(timeLayout)
			switch {
			case len(f.Events) != 3:
				t.Errorf("kept %d events, want 3", len(f.Events))
			case n == 4 && f.Truncated["1"] != third:
				t.Errorf("Truncated = %v, want 1: %s", f.Truncated, third)
			case n == 3 && len(f.Truncated) != 0:
				t.Errorf("Truncated = %v, want none for exactly the cap", f.Truncated)
			}
		})
	}
}

func TestEventsChannelNotFoundIsAbsent(t *testing.T) {
	c := fakeCalls()
	c.Coverage = func(ch string) (Coverage, error) {
		if ch == ClusterChannel {
			return Coverage{}, ErrChannelNotFound
		}
		return Coverage{}, nil
	}
	c.Query = func(_ context.Context, ch, _ string, _ bool, _ func([]byte, string) bool) error {
		if ch == ClusterChannel {
			return ErrChannelNotFound
		}
		return nil
	}
	s := &recordingSink{}
	if res := readNamed(t, c, "025.events-cluster", Env{Since: sourcesSince}, s); res.Status != StatusAbsent {
		t.Errorf("status = %s (%s), want absent", res.Status, res.Reason)
	}
	if len(s.names) != 0 {
		t.Errorf("wrote %q", s.names)
	}
}

// Only Coverage reports the missing channel: the source is absent all the same.
func TestEventsCoverageChannelNotFoundIsAbsent(t *testing.T) {
	c := fakeCalls()
	c.Coverage = func(string) (Coverage, error) { return Coverage{}, ErrChannelNotFound }
	s := &recordingSink{}
	if res := readNamed(t, c, "025.events-cluster", Env{Since: sourcesSince}, s); res.Status != StatusAbsent {
		t.Errorf("status = %s (%s), want absent", res.Status, res.Reason)
	}
	if len(s.names) != 0 {
		t.Errorf("wrote %q", s.names)
	}
}

func TestEventsMessageIsCut(t *testing.T) {
	c := fakeCalls()
	c.Query = func(_ context.Context, _, xpath string, _ bool, each func([]byte, string) bool) error {
		if strings.Contains(xpath, "Level=1") {
			each(eventAt(1, sourcesSince.Add(time.Hour)), strings.Repeat("é", 2001))
		}
		return nil
	}
	s := &recordingSink{}
	readNamed(t, c, "010.events-system", Env{Since: sourcesSince}, s)
	var f EventsFile
	if err := json.Unmarshal(s.data["010.events-system.json"], &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Events) != 1 || utf8.RuneCountInString(f.Events[0].Message) != 2000 {
		t.Errorf("events = %+v, want one with a 2000-rune message", f.Events)
	}
}

func TestEventsAutoBackupNote(t *testing.T) {
	c := fakeCalls()
	c.Coverage = func(string) (Coverage, error) { return Coverage{AutoBackup: true}, nil }
	res := readNamed(t, c, "020.events-application", Env{Since: sourcesSince, EventSource: "MSSQLSERVER"}, &recordingSink{})
	if res.Status != StatusRead || !strings.Contains(strings.Join(res.Notes, "\n"), "Archive-") {
		t.Errorf("got %s, notes %q, want read with a note on Archive-*.evtx", res.Status, res.Notes)
	}
}

func TestSQLWriterCopiesRollovers(t *testing.T) {
	shared := `C:\Program Files\Microsoft SQL Server\90\Shared`
	c := fakeCalls()
	c.ProgramFiles = `C:\Program Files`
	c.ReadFile = func(p string) ([]byte, error) {
		if p != shared+`\SqlWriterConfig.ini` {
			return nil, fs.ErrNotExist
		}
		return []byte("[Logging]\r\nEnableTracing=1\r\n"), nil
	}
	c.OpenDir = func(dir string) (fs.FS, error) {
		if dir != shared {
			return nil, fs.ErrNotExist
		}
		return fstest.MapFS{
			"SqlWriterLogger.txt":  {Data: []byte("current")},
			"SqlWriterLogger1.txt": {Data: []byte("previous")},
			"SqlWriterConfig.ini":  {Data: []byte("[Logging]")},
		}, nil
	}
	s := &recordingSink{}
	res := readNamed(t, c, "035.sqlwriter", Env{Since: sourcesSince}, s)
	if res.Status != StatusRead || len(res.Copied) != 2 {
		t.Errorf("got %s, %d copied, want read and 2", res.Status, len(res.Copied))
	}
	if string(s.data["035.sqlwriter/SqlWriterLogger.txt"]) != "current" || string(s.data["035.sqlwriter/SqlWriterLogger1.txt"]) != "previous" || len(s.names) != 2 {
		t.Errorf("entries = %q", s.names)
	}
}

func TestSQLWriterIniMissingIsAbsent(t *testing.T) {
	c := fakeCalls()
	c.ProgramFiles = `C:\Program Files`
	c.ReadFile = func(string) ([]byte, error) {
		return nil, &fs.PathError{Op: "open", Path: "SqlWriterConfig.ini", Err: fs.ErrNotExist}
	}
	c.OpenDir = func(string) (fs.FS, error) {
		t.Error("OpenDir called without a config file")
		return fstest.MapFS{}, nil
	}
	if res := readNamed(t, c, "035.sqlwriter", Env{Since: sourcesSince}, &recordingSink{}); res.Status != StatusAbsent {
		t.Errorf("status = %s (%s), want absent", res.Status, res.Reason)
	}
}

func TestJobOutputsStepCopyFailureFails(t *testing.T) {
	c := fakeCalls()
	c.OpenDir = func(dir string) (fs.FS, error) {
		if dir == `C:\out1` {
			return nil, fmt.Errorf("access denied")
		}
		return fstest.MapFS{"b.txt": {Data: []byte("b"), ModTime: sourcesSince.Add(48 * time.Hour)}}, nil
	}
	e := matchedEnv()
	e.Steps = []JobStep{{Job: "J1", StepID: 1, OutputFile: `C:\out1\a.txt`}, {Job: "J2", StepID: 1, OutputFile: `C:\out2\b.txt`}}
	s := &recordingSink{}
	res := readNamed(t, c, "070.job-outputs", e, s)
	if res.Status != StatusFailed || !strings.HasPrefix(res.Reason, "J1 step 1: ") || !strings.Contains(res.Reason, "access denied") {
		t.Errorf("got %s %q, want failed with the first step's error", res.Status, res.Reason)
	}
	if len(res.Copied) != 1 || len(s.data) != 2 { // b.txt and the JSON
		t.Errorf("copied %d, wrote %q, want the second step read", len(res.Copied), s.names)
	}
}
