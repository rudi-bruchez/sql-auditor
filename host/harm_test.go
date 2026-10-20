package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// What the elevated run opens: the rules found by the harm review of the host
// command, each pinned away from Windows through Calls.

func TestHarmA_EveryScriptStartsWithPreamble(t *testing.T) {
	if !strings.Contains(psPreamble, "$PSModuleAutoLoadingPreference='None'") {
		t.Errorf("psPreamble does not turn module autoload off: %s", psPreamble)
	}
	c := fakeCalls()
	var scripts []string
	c.PowerShell = func(_ context.Context, script string) ([]byte, error) {
		scripts = append(scripts, script)
		return nil, fs.ErrNotExist
	}
	for _, src := range BuildSources(c) {
		src.Read(context.Background(), matchedEnv(), (&recordingSink{}).sink(false))
	}
	if len(scripts) == 0 {
		t.Fatal("no source ran a script")
	}
	for _, s := range scripts {
		if !strings.HasPrefix(s, psPreamble) {
			t.Errorf("script does not start with psPreamble:\n%.120s", s)
		}
	}
}

func TestHarmA_PowerShellEnv(t *testing.T) {
	env := psEnv([]string{
		`PATH=C:\Windows`,
		`PSModulePath=C:\Users\dba\Documents\WindowsPowerShell\Modules;C:\Program Files\WindowsPowerShell\Modules`,
		`psmodulepath=C:\planted`,
		`=C:=C:\audit`,
	}, `C:\Windows\system32`)
	var got []string
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); strings.EqualFold(k, "PSModulePath") {
			got = append(got, v)
		}
	}
	if want := []string{`C:\Windows\system32\WindowsPowerShell\v1.0\Modules`}; !reflect.DeepEqual(got, want) {
		t.Errorf("PSModulePath %q, want %q", got, want)
	}
	if !slices.Contains(env, `PATH=C:\Windows`) || !slices.Contains(env, `=C:=C:\audit`) {
		t.Errorf("env %q lost a variable", env)
	}
}

// A link matching the pattern is noted and not followed, whatever its target:
// a planted link would otherwise export a private file under the log's name.
func TestHarmA_JobOutputLinksNotFollowed(t *testing.T) {
	since := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	n := "DatabaseBackup - USER_DATABASES - FULL_1_20260723_10000.txt"
	j := "DatabaseBackup - USER_DATABASES - FULL_1_20260723_20000.txt"
	regular := "DatabaseBackup - USER_DATABASES - FULL_1_20260723_30000.txt"
	m := fstest.MapFS{
		n:                    {Data: []byte("private/secret.txt"), Mode: fs.ModeSymlink, ModTime: since.Add(time.Hour)},
		j:                    {Mode: fs.ModeIrregular, ModTime: since.Add(time.Hour)}, // a junction since Go 1.23
		regular:              {Data: []byte("log"), ModTime: since.Add(time.Hour)},
		"private/secret.txt": {Data: []byte("secret"), ModTime: since.Add(time.Hour)},
	}
	var rec recordingSink
	copied, notes, err := CopyJobOutputs(jobCalls(m, `X:\Logs`), olaPattern(t, `X:\Logs`), "Backup_1", since, rec.sink(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(copied) != 1 || copied[0].Source != `X:\Logs\`+regular || !reflect.DeepEqual(rec.names, []string{"070.job-outputs/Backup_1/" + regular}) {
		t.Errorf("copied %+v, written %v, want only the regular file", copied, rec.names)
	}
	slices.Sort(notes)
	if want := []string{n + ": link, not followed", j + ": link, not followed"}; !reflect.DeepEqual(notes, want) {
		t.Errorf("notes %q, want %q", notes, want)
	}
}

// The same on a real directory through os.DirFS: a symbolic link to a file
// outside the directory is noted, and the file it points to is not read.
func TestHarmA_JobOutputLinkOnDisk(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	n := "DatabaseBackup - USER_DATABASES - FULL_1_20260723_10000.txt"
	if err := os.Symlink(secret, filepath.Join(dir, n)); err != nil {
		t.Skip("no symbolic link here:", err)
	}
	var rec recordingSink
	copied, notes, err := CopyJobOutputs(jobCalls(os.DirFS(dir), `X:\Logs`), olaPattern(t, `X:\Logs`), "Backup_1", time.Time{}, rec.sink(true))
	if err != nil || len(copied) != 0 || len(rec.names) != 0 {
		t.Fatalf("err %v, copied %+v, written %v", err, copied, rec.names)
	}
	if !reflect.DeepEqual(notes, []string{n + ": link, not followed", `X:\Logs: matches nothing in the window`}) {
		t.Errorf("notes %q", notes)
	}
}

type nopReadSeekCloser struct{ *strings.Reader }

func (nopReadSeekCloser) Close() error { return nil }

// swappedOpen stands for a file replaced between the listing and the open:
// what is copied is what the judged handle reads, never a second open of the
// listed name, and a refusal of that handle (the replacement is a link) is
// recorded where a refusal is.
func swappedOpen(refused string) OpenFile {
	return func(p string) (io.ReadSeekCloser, int64, string, error) {
		if strings.HasSuffix(p, `\`+refused) {
			return nil, 0, "link, not followed", nil
		}
		return nopReadSeekCloser{strings.NewReader("opened")}, 6, "", nil
	}
}

func TestHarmA_CopiedFromTheJudgedHandle(t *testing.T) {
	mod := sourcesSince.Add(time.Hour)
	listed := func(names ...string) fstest.MapFS {
		m := fstest.MapFS{}
		for _, n := range names {
			m[n] = &fstest.MapFile{Data: []byte("listed"), ModTime: mod}
		}
		return m
	}
	check := func(t *testing.T, s *recordingSink, kept, refused string, reasons []string) {
		t.Helper()
		if got := string(s.data[kept]); got != "opened" {
			t.Errorf("%s holds %q, want what the opened handle read", kept, got)
		}
		for n := range s.data {
			if strings.HasSuffix(n, "/"+refused) {
				t.Errorf("refused file %s written", n)
			}
		}
		if !slices.ContainsFunc(reasons, func(r string) bool { return strings.Contains(r, "link, not followed") }) {
			t.Errorf("refusal not recorded: %q", reasons)
		}
	}

	t.Run("job outputs", func(t *testing.T) {
		c := fakeCalls()
		c.OpenDir = func(string) (fs.FS, error) { return listed("20260723.txt", "20260724.txt"), nil }
		c.OpenFile = swappedOpen("20260724.txt")
		e := matchedEnv()
		e.Steps = []JobStep{{Job: "J", StepID: 1, OutputFile: `C:\out\$(ESCAPE_SQUOTE(STRTDT)).txt`}}
		s := &recordingSink{}
		res := readNamed(t, c, "070.job-outputs", e, s)
		check(t, s, "070.job-outputs/J_1/20260723.txt", "20260724.txt", res.Notes)
	})
	t.Run("SQL Writer", func(t *testing.T) {
		c := fakeCalls()
		c.ReadFile = func(string) ([]byte, error) { return []byte("[Logging]\r\n"), nil }
		c.OpenDir = func(string) (fs.FS, error) { return listed("SqlWriterLogger.txt", "SqlWriterLogger1.txt"), nil }
		c.OpenFile = swappedOpen("SqlWriterLogger1.txt")
		s := &recordingSink{}
		res := readNamed(t, c, "035.sqlwriter", Env{Since: sourcesSince}, s)
		check(t, s, "035.sqlwriter/SqlWriterLogger.txt", "SqlWriterLogger1.txt", res.Notes)
	})
	t.Run("VM extensions", func(t *testing.T) {
		c := fakeCalls()
		c.AzurePlugins = `C:\WindowsAzure\Logs\Plugins`
		c.OpenDir = func(string) (fs.FS, error) { return listed("a.log", "b.log"), nil }
		c.OpenFile = swappedOpen("b.log")
		s := &recordingSink{}
		readNamed(t, c, "060.vm-extensions", Env{Since: sourcesSince}, s)
		var reasons []string
		for _, l := range listedIn(t, s) {
			reasons = append(reasons, l.Reason)
		}
		check(t, s, "060.vm-extensions/a.log", "b.log", reasons)
	})
}

// listedIn reads 060.vm-extensions.json from what s was given.
func listedIn(t *testing.T, s *recordingSink) []Listed {
	t.Helper()
	var l []Listed
	if err := json.Unmarshal(s.data["060.vm-extensions.json"], &l); err != nil {
		t.Fatal(err)
	}
	return l
}

// Each directory is judged as typed before OpenDir lists it: a refused one is
// never opened, so no credential goes out to a share, and is recorded.
func TestHarmA_DirCheckBeforeOpenDir(t *testing.T) {
	setup := func(t *testing.T) (Calls, *[]string) {
		c := fakeCalls()
		var asked []string
		c.DirCheck = func(dir string) (bool, string) { asked = append(asked, dir); return false, "network drive" }
		c.OpenDir = func(dir string) (fs.FS, error) {
			t.Errorf("OpenDir(%q) called on a refused directory", dir)
			return fstest.MapFS{}, nil
		}
		return c, &asked
	}
	t.Run("job outputs", func(t *testing.T) {
		c, asked := setup(t)
		e := matchedEnv()
		e.Steps = []JobStep{{Job: "J", StepID: 1, OutputFile: `M:\Logs\out.txt`}}
		s := &recordingSink{}
		res := readNamed(t, c, "070.job-outputs", e, s)
		if !reflect.DeepEqual(*asked, []string{`M:\Logs`}) || !reflect.DeepEqual(res.Notes, []string{`J step 1: M:\Logs: network drive`}) {
			t.Errorf("asked %q, notes %q", *asked, res.Notes)
		}
	})
	t.Run("SQL Writer", func(t *testing.T) {
		c, asked := setup(t)
		c.ReadFile = func(string) ([]byte, error) { return []byte("[Logging]\r\nTraceFile=M:\\Logs\\w.txt\r\n"), nil }
		res := readNamed(t, c, "035.sqlwriter", Env{Since: sourcesSince}, &recordingSink{})
		if !reflect.DeepEqual(*asked, []string{`M:\Logs`}) || !reflect.DeepEqual(res.Notes, []string{`M:\Logs: network drive`}) || len(res.Copied) != 0 {
			t.Errorf("asked %q, result %+v", *asked, res)
		}
	})
	t.Run("VM extensions", func(t *testing.T) {
		c, asked := setup(t)
		c.AzurePlugins = `C:\WindowsAzure\Logs\Plugins`
		s := &recordingSink{}
		res := readNamed(t, c, "060.vm-extensions", Env{Since: sourcesSince}, s)
		if !reflect.DeepEqual(*asked, []string{c.AzurePlugins}) || !reflect.DeepEqual(res.Notes, []string{c.AzurePlugins + `: network drive`}) ||
			len(listedIn(t, s)) != 0 {
			t.Errorf("asked %q, result %+v", *asked, res)
		}
	})
}

// A TraceFile naming only a directory keeps the default log name there: an
// empty stem with an empty extension would match every file of it.
func TestHarmA_SQLWriterTraceFileDirectoryOnly(t *testing.T) {
	for _, ini := range []string{"[Logging]\r\nTraceFile=D:\\logs\\\r\n", "[Logging]\r\nTraceFile=D:/logs/\r\n"} {
		dir, stem, ext := SQLWriterLog([]byte(ini), `C:\Shared`)
		if stem != "SqlWriterLogger" || ext != ".txt" || !strings.HasPrefix(dir, "D:") {
			t.Errorf("%q: got %q %q %q, want D:\\logs SqlWriterLogger .txt", ini, dir, stem, ext)
		}
		if IsSQLWriterLog("SqlWriterConfig.ini", stem, ext) {
			t.Errorf("%q: SqlWriterConfig.ini taken for the log", ini)
		}
	}
}

func TestHarmA_ArchiveSDDL(t *testing.T) {
	const sid = "S-1-5-21-1-2-3-1001"
	if got, want := archiveSDDL(sid), "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;"+sid+")"; got != want {
		t.Errorf("archiveSDDL = %s, want %s", got, want)
	}
}

// eventsWith reads the System file of one event with the given EventData and
// a message of message.
func eventsWith(t *testing.T, eventData, message string) Event {
	t.Helper()
	c := fakeCalls()
	c.Query = func(_ context.Context, _, xpath string, _ bool, each func([]byte, string) bool) error {
		if strings.Contains(xpath, "Level=1") {
			each(eventWith("2026-10-20T01:50:58Z", "<EventData>"+eventData+"</EventData>"), message)
		}
		return nil
	}
	s := &recordingSink{}
	readNamed(t, c, "010.events-system", Env{Since: sourcesSince}, s)
	var f EventsFile
	if err := json.Unmarshal(s.data["010.events-system.json"], &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Events) != 1 {
		t.Fatalf("%d events, want 1", len(f.Events))
	}
	return f.Events[0]
}

// A Data value and Binary are cut like the message, and the event says so.
func TestHarmB_DataAndBinaryCut(t *testing.T) {
	e := eventsWith(t, "<Data>"+strings.Repeat("é", 15000)+"</Data><Binary>"+strings.Repeat("AB", 5000)+"</Binary>", "short")
	if len(e.Data) != 1 || e.Data[0] != strings.Repeat("é", messageRunes) || e.Binary != strings.Repeat("AB", messageRunes/2) || !e.Cut {
		t.Errorf("data %d runes, binary %d, cut %v; want %d, %d, true", len([]rune(strings.Join(e.Data, ""))), len(e.Binary), e.Cut, messageRunes, messageRunes)
	}
	if e := eventsWith(t, "<Data>a</Data><Binary>AB</Binary>", "short"); e.Cut || e.DataDropped != 0 {
		t.Errorf("nothing cut, got cut %v, dropped %d", e.Cut, e.DataDropped)
	}
}

// Many values do not add up past dataBytes: the ones past it are counted.
func TestHarmB_DataTotalBounded(t *testing.T) {
	e := eventsWith(t, strings.Repeat("<Data>"+strings.Repeat("x", messageRunes)+"</Data>", 20), "")
	total := 0
	for _, v := range e.Data {
		total += len(v)
	}
	if total > dataBytes || e.DataDropped == 0 || len(e.Data)+e.DataDropped != 20 {
		t.Errorf("kept %d values, %d bytes, dropped %d; want at most %d bytes and the rest counted", len(e.Data), total, e.DataDropped, dataBytes)
	}
}

// The streamed file is what json.MarshalIndent wrote, byte for byte.
func TestHarmB_StreamedMatchesMarshalIndent(t *testing.T) {
	ev := Event{Log: "Application", Queries: []string{"1", "2"}, TimeUTC: "2026-10-20T01:50:58.0000000Z", Level: 2, Provider: "MSSQLSERVER", EventID: 833, RecordID: 7, Data: []string{"a<b>&\"", "\n"}, Binary: "AB", Message: "m", Cut: true, DataDropped: 3}
	for name, f := range map[string]EventsFile{
		"none":  {Log: "System", Events: []Event{}},
		"one":   {Log: "System", Events: []Event{ev}},
		"whole": {Log: "Application", Source: `MSSQL$PROD`, Coverage: Coverage{OldestUTC: "x", MaxSizeBytes: 1}, Events: []Event{ev, {Log: "Application", Data: []string{}}}, Counted: []IDCount{{41050, 1, "a", "b"}}, NotRun: map[string]string{"3b": "r"}, Truncated: map[string]string{"1": "t"}},
	} {
		want, err := json.MarshalIndent(f, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		s := &recordingSink{}
		if err := writeEvents(s.sink(true), "f.json", f); err != nil {
			t.Fatal(err)
		}
		if got := s.data["f.json"]; string(got) != string(want) {
			t.Errorf("%s:\n got %s\nwant %s", name, got, want)
		}
	}
}

// A sink that stops and a writer that fails both come back as the error, and
// the writing goroutine ends either way.
func TestHarmB_StreamErrors(t *testing.T) {
	before := runtime.NumGoroutine()
	stop := errors.New("disk full")
	f := EventsFile{Log: "System", Events: make([]Event, 1000)}
	for i := range f.Events {
		f.Events[i] = Event{Data: []string{strings.Repeat("x", 1000)}}
	}
	res := make(chan error, 1)
	go func() {
		res <- writeEvents(func(_ string, r io.Reader) error {
			r.Read(make([]byte, 10))
			return stop
		}, "f.json", f)
	}()
	select {
	case err := <-res:
		if err != stop {
			t.Errorf("sink error: got %v, want %v", err, stop)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writeEvents still waiting for its writer after the sink stopped")
	}
	bad := errors.New("cannot encode")
	var read []byte
	err := streamed(func(_ string, r io.Reader) error {
		read, _ = io.ReadAll(r)
		return nil
	}, "f.json", func(w io.Writer) error {
		w.Write([]byte("{"))
		return bad
	})
	if err != bad || string(read) != "{" {
		t.Errorf("writer error: got %v after %q, want %v", err, read, bad)
	}
	for i := 0; runtime.NumGoroutine() > before && i < 100; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Errorf("%d goroutines, %d before", n, before)
	}
}

// Query 2c counts and keeps nothing: its messages are not formatted.
func TestHarmB_CountOnlyNotFormatted(t *testing.T) {
	c := fakeCalls()
	format := map[string]bool{}
	for _, q := range BuildEventQueries("MSSQLSERVER", sourcesSince) {
		if q.Channel == "Application" {
			format[q.ID] = !q.CountOnly
		}
	}
	got := map[string]bool{}
	c.Query = func(_ context.Context, _, xpath string, f bool, _ func([]byte, string) bool) error {
		for _, q := range BuildEventQueries("MSSQLSERVER", sourcesSince) {
			if q.Channel == "Application" && q.XPath == xpath {
				got[q.ID] = f
			}
		}
		return nil
	}
	readNamed(t, c, "020.events-application", Env{Since: sourcesSince, EventSource: "MSSQLSERVER"}, &recordingSink{})
	if !reflect.DeepEqual(got, format) || got["2c"] {
		t.Errorf("format per query %v, want %v", got, format)
	}
}
