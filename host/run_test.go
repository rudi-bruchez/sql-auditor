package host

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var runNow = time.Date(2026, 10, 20, 3, 4, 5, 0, time.UTC)

// fakePlatform answers as SQL01, a default instance on the machine it runs on.
func fakePlatform(sources ...Source) Platform {
	return Platform{
		Check:    func() error { return nil },
		NetBIOS:  func() (string, error) { return "SQL01", nil },
		Services: func() ([]string, error) { return []string{"MSSQLSERVER"}, nil },
		Facts: func(context.Context) (*InstanceFacts, error) {
			return &InstanceFacts{ServerName: "SQL01", NetBIOS: "SQL01"}, nil
		},
		Steps:   func(context.Context) ([]JobStep, bool, error) { return nil, false, nil },
		Sources: sources,
	}
}

func readSource(name string, count int) Source {
	return Source{Name: name, Read: func(ctx context.Context, e Env, put FileSink) SourceResult {
		if err := put(name+".json", strings.NewReader("{}")); err != nil {
			return SourceResult{Status: "failed", Reason: err.Error()}
		}
		return SourceResult{Status: "read", Count: count}
	}}
}

// jobOutputs follows the rule of the real source over the Env it is given.
func jobOutputs(seen *Env) Source {
	return Source{Name: "070.job-outputs", Read: func(ctx context.Context, e Env, put FileSink) SourceResult {
		*seen = e
		switch {
		case e.Facts == nil || !e.MachineMatch:
			return SourceResult{Status: "skipped", Reason: "instance"}
		case e.StepsRefused:
			return SourceResult{Status: "refused", Reason: "msdb"}
		}
		return SourceResult{Status: "read"}
	}}
}

func runOptions(t *testing.T, p Platform) Options {
	t.Helper()
	return Options{
		To: t.TempDir(), EnvServer: "SQL01", Version: "test",
		Since: runNow.AddDate(0, 0, -90), Now: func() time.Time { return runNow },
		Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Platform: p,
	}
}

func stdout(o Options) string { return o.Stdout.(*bytes.Buffer).String() }
func stderr(o Options) string { return o.Stderr.(*bytes.Buffer).String() }

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

func zipEntries(t *testing.T, path string) map[string]string {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer r.Close()
	m := map[string]string{}
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open entry %s: %v", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read entry %s: %v", f.Name, err)
		}
		m[f.Name] = string(b)
	}
	return m
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range des {
		names = append(names, d.Name())
	}
	return names
}

// manifestOf runs o, expects an archive and returns its MANIFEST.txt and the
// exit code.
func manifestOf(t *testing.T, o Options) (string, int) {
	t.Helper()
	code := Run(context.Background(), o)
	path := lastLine(stdout(o))
	if !strings.HasSuffix(path, ".host.zip") {
		t.Fatalf("no archive line on stdout:\n%s\nstderr:\n%s", stdout(o), stderr(o))
	}
	for name, body := range zipEntries(t, path) {
		if strings.HasSuffix(name, "/MANIFEST.txt") {
			return body, code
		}
	}
	t.Fatal("no MANIFEST.txt in the archive")
	return "", code
}

func TestRunAllReadExitsZeroAndPrintsPath(t *testing.T) {
	o := runOptions(t, fakePlatform(readSource("010.a", 3), readSource("020.b", 0)))
	if code := Run(context.Background(), o); code != 0 {
		t.Fatalf("exit %d, want 0; stderr:\n%s", code, stderr(o))
	}
	want := filepath.Join(o.To, "SQL01-2026-10-20-030405.host.zip")
	if got := lastLine(stdout(o)); got != want {
		t.Fatalf("last stdout line %q, want %q", got, want)
	}
	if !strings.Contains(stdout(o), "010.a: read (3)\n") {
		t.Errorf("no source line in stdout:\n%s", stdout(o))
	}
	entries := zipEntries(t, want)
	for name := range entries {
		if !strings.HasPrefix(name, "SQL01-2026-10-20-030405.host/") {
			t.Errorf("entry %q outside the top-level directory", name)
		}
	}
	if _, ok := entries["SQL01-2026-10-20-030405.host/MANIFEST.txt"]; !ok {
		t.Errorf("no MANIFEST.txt in %v", entries)
	}
}

func TestRunInstanceDownStillReadsMachine(t *testing.T) {
	var seen Env
	machineRead := false
	p := fakePlatform(Source{Name: "010.events-system", Read: func(ctx context.Context, e Env, put FileSink) SourceResult {
		machineRead = true
		return SourceResult{Status: "read"}
	}}, jobOutputs(&seen))
	p.Facts = func(context.Context) (*InstanceFacts, error) { return nil, errors.New("login timeout") }
	p.NetBIOS = func() (string, error) { return "HOST7", nil }
	o := runOptions(t, p)
	if code := Run(context.Background(), o); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !machineRead {
		t.Error("the machine source was not read")
	}
	if seen.Facts != nil || !strings.Contains(stdout(o), "070.job-outputs: skipped") {
		t.Errorf("job outputs saw Facts %v; stdout:\n%s", seen.Facts, stdout(o))
	}
	want := filepath.Join(o.To, "HOST7-2026-10-20-030405.host.zip")
	if got := lastLine(stdout(o)); got != want {
		t.Fatalf("last stdout line %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
}

func TestRunMismatchSkipsInstanceSources(t *testing.T) {
	var seen Env
	p := fakePlatform(readSource("010.a", 1), jobOutputs(&seen))
	p.Facts = func(context.Context) (*InstanceFacts, error) {
		return &InstanceFacts{ServerName: "AGL01", NetBIOS: "SQL02"}, nil
	}
	stepsCalled := false
	p.Steps = func(context.Context) ([]JobStep, bool, error) { stepsCalled = true; return nil, false, nil }
	o := runOptions(t, p)
	m, code := manifestOf(t, o)
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if stepsCalled {
		t.Error("Steps was called on a mismatch")
	}
	if seen.MachineMatch || !strings.Contains(stdout(o), "070.job-outputs: skipped") {
		t.Errorf("job outputs saw MachineMatch %v; stdout:\n%s", seen.MachineMatch, stdout(o))
	}
	line := manifestLine(m, "Machine check:")
	if !strings.Contains(line, "SQL02") || !strings.Contains(line, "SQL01") {
		t.Errorf("the machine check does not name both machines: %q", line)
	}
}

// manifestLine is the manifest's line starting with prefix, "" when none does.
func manifestLine(m, prefix string) string {
	for _, l := range strings.Split(m, "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}

func TestRunStepsErrorIsTwo(t *testing.T) {
	var seen Env
	p := fakePlatform(readSource("010.a", 1), Source{Name: "070.job-outputs", Read: func(ctx context.Context, e Env, put FileSink) SourceResult {
		seen = e
		return SourceResult{Status: "read"}
	}})
	p.Steps = func(context.Context) ([]JobStep, bool, error) { return nil, false, errors.New("connection reset") }
	o := runOptions(t, p)
	m, code := manifestOf(t, o)
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if seen.StepsErr == nil {
		t.Error("job outputs saw no StepsErr")
	}
	if !strings.Contains(manifestLine(m, "Job steps:"), "connection reset") {
		t.Errorf("the manifest does not name the error:\n%s", m)
	}
}

func TestRunNamedInstanceArchiveName(t *testing.T) {
	p := fakePlatform(readSource("010.a", 1))
	p.Facts = func(context.Context) (*InstanceFacts, error) {
		return &InstanceFacts{ServerName: `SQL01\PROD`, NetBIOS: "SQL01"}, nil
	}
	p.Services = func() ([]string, error) { return []string{"MSSQL$PROD"}, nil }
	o := runOptions(t, p)
	if code := Run(context.Background(), o); code != 0 {
		t.Fatalf("exit %d, want 0; stderr:\n%s", code, stderr(o))
	}
	want := filepath.Join(o.To, "SQL01_PROD-2026-10-20-030405.host.zip")
	if got := lastLine(stdout(o)); got != want {
		t.Fatalf("last stdout line %q, want %q", got, want)
	}
	for name := range zipEntries(t, want) {
		if !strings.HasPrefix(name, "SQL01_PROD-2026-10-20-030405.host/") {
			t.Errorf("entry %q outside the top-level directory", name)
		}
	}
}

func TestRunRefusedStepsIsZero(t *testing.T) {
	var seen Env
	p := fakePlatform(readSource("010.a", 1), jobOutputs(&seen))
	p.Steps = func(context.Context) ([]JobStep, bool, error) { return nil, true, errors.New("229") }
	o := runOptions(t, p)
	if code := Run(context.Background(), o); code != 0 {
		t.Fatalf("exit %d, want 0; stdout:\n%s", code, stdout(o))
	}
	if !seen.StepsRefused || !strings.Contains(stdout(o), "070.job-outputs: refused") {
		t.Errorf("StepsRefused %v; stdout:\n%s", seen.StepsRefused, stdout(o))
	}
}

func TestRunSourcePanicIsFailed(t *testing.T) {
	nextRead := false
	p := fakePlatform(Source{Name: "010.a", Read: func(context.Context, Env, FileSink) SourceResult {
		panic("index out of range")
	}}, Source{Name: "020.b", Read: func(context.Context, Env, FileSink) SourceResult {
		nextRead = true
		return SourceResult{Status: "read"}
	}})
	o := runOptions(t, p)
	m, code := manifestOf(t, o)
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !nextRead {
		t.Error("the source after the panic was not read")
	}
	if !strings.Contains(m, "010.a: failed") || !strings.Contains(m, "index out of range") {
		t.Errorf("the manifest does not name the failure:\n%s", m)
	}
}

func TestRunInterruptRemovesArchive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	secondRead := false
	p := fakePlatform(Source{Name: "010.a", Read: func(ctx context.Context, e Env, put FileSink) SourceResult {
		put("010.a.json", strings.NewReader("{}"))
		cancel()
		return SourceResult{Status: "read"}
	}}, Source{Name: "020.b", Read: func(context.Context, Env, FileSink) SourceResult {
		secondRead = true
		return SourceResult{Status: "read"}
	}})
	o := runOptions(t, p)
	if code := Run(ctx, o); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if secondRead {
		t.Error("the second source was read after the interrupt")
	}
	if names := dirEntries(t, o.To); len(names) != 0 {
		t.Errorf("left in --to: %v", names)
	}
	if strings.Contains(stdout(o), ".zip") {
		t.Errorf("archive line printed:\n%s", stdout(o))
	}
}

func TestRunRefusesMissingTo(t *testing.T) {
	read := false
	p := fakePlatform(Source{Name: "010.a", Read: func(context.Context, Env, FileSink) SourceResult {
		read = true
		return SourceResult{Status: "read"}
	}})
	factsCalled := false
	p.Facts = func(context.Context) (*InstanceFacts, error) {
		factsCalled = true
		return nil, errors.New("not reached")
	}
	o := runOptions(t, p)
	o.To = filepath.Join(o.To, "missing")
	if code := Run(context.Background(), o); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if factsCalled {
		t.Error("Facts was called before --to was checked")
	}
	if read || stdout(o) != "" {
		t.Errorf("read %v, stdout %q", read, stdout(o))
	}
	if _, err := os.Stat(o.To); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("--to was created: %v", err)
	}
}

func TestRunRefusesExistingArchive(t *testing.T) {
	read := false
	p := fakePlatform(Source{Name: "010.a", Read: func(context.Context, Env, FileSink) SourceResult {
		read = true
		return SourceResult{Status: "read"}
	}})
	o := runOptions(t, p)
	existing := filepath.Join(o.To, "SQL01-2026-10-20-030405.host.zip")
	if err := os.WriteFile(existing, []byte("earlier"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := Run(context.Background(), o); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if read || stdout(o) != "" {
		t.Errorf("read %v, stdout %q", read, stdout(o))
	}
	if b, _ := os.ReadFile(existing); string(b) != "earlier" {
		t.Errorf("the existing file was changed: %q", b)
	}
}

func TestRunCheckFailureWritesNothing(t *testing.T) {
	p := fakePlatform(readSource("010.a", 1))
	p.Check = func() error { return ErrNotWindows }
	o := runOptions(t, p)
	if code := Run(context.Background(), o); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(stderr(o), ErrNotWindows.Error()) || stdout(o) != "" {
		t.Errorf("stderr %q, stdout %q", stderr(o), stdout(o))
	}
	if names := dirEntries(t, o.To); len(names) != 0 {
		t.Errorf("left in --to: %v", names)
	}
}

func TestRunRefusesUnwritableTo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode 0500 does not stop writes to a Windows directory")
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes through mode 0500")
	}
	read := false
	p := fakePlatform(Source{Name: "010.a", Read: func(context.Context, Env, FileSink) SourceResult {
		read = true
		return SourceResult{Status: "read"}
	}})
	o := runOptions(t, p)
	if err := os.Chmod(o.To, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(o.To, 0o700)
	if code := Run(context.Background(), o); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if read || stdout(o) != "" {
		t.Errorf("read %v, stdout %q", read, stdout(o))
	}
	if names := dirEntries(t, o.To); len(names) != 0 {
		t.Errorf("left in --to: %v", names)
	}
}

// failingWriter fails every write once after bytes have passed through it, or
// once fail is set. An after of 0 sets no budget.
type failingWriter struct {
	w     io.Writer
	after int
	fail  bool
}

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.after > 0 && len(p) > f.after {
		f.fail = true
	}
	if f.fail {
		return 0, errors.New("disk full")
	}
	if f.after > 0 {
		f.after -= len(p)
		f.fail = f.after == 0
	}
	return f.w.Write(p)
}

func TestRunZipErrorRemovesArchive(t *testing.T) {
	secondRead := false
	p := fakePlatform(Source{Name: "010.a", Read: func(ctx context.Context, e Env, put FileSink) SourceResult {
		if err := put("010.a.bin", bytes.NewReader(incompressible())); err != nil {
			return SourceResult{Status: "failed", Reason: err.Error()}
		}
		return SourceResult{Status: "read"}
	}}, Source{Name: "020.b", Read: func(context.Context, Env, FileSink) SourceResult {
		secondRead = true
		return SourceResult{Status: "read"}
	}})
	o := runOptions(t, p)
	o.wrapArchive = func(w io.Writer) io.Writer { return &failingWriter{w: w, after: 1 << 10} }
	if code := Run(context.Background(), o); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if secondRead {
		t.Error("the second source was read after a zip error")
	}
	if names := dirEntries(t, o.To); len(names) != 0 {
		t.Errorf("left in --to: %v", names)
	}
	if strings.Contains(stdout(o), ".zip") {
		t.Errorf("archive line printed:\n%s", stdout(o))
	}
}

// incompressible is 64 KiB that deflate cannot shrink: zeros compress under
// 1 KiB and stay in the zip writer's buffers, never reaching a failing writer.
func incompressible() []byte {
	b := make([]byte, 64<<10)
	r := rand.New(rand.NewPCG(1, 2))
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("sharing violation") }

func TestRunSourceReaderErrorKeepsArchive(t *testing.T) {
	p := fakePlatform(Source{Name: "010.a", Read: func(ctx context.Context, e Env, put FileSink) SourceResult {
		if err := put("010.a.txt", failingReader{}); err != nil {
			return SourceResult{Status: "failed", Reason: err.Error()}
		}
		return SourceResult{Status: "read"}
	}}, readSource("020.b", 2))
	o := runOptions(t, p)
	if code := Run(context.Background(), o); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(stdout(o), "010.a: failed") || !strings.Contains(stdout(o), "020.b: read (2)") {
		t.Errorf("stdout:\n%s", stdout(o))
	}
	entries := zipEntries(t, lastLine(stdout(o)))
	if _, ok := entries["SQL01-2026-10-20-030405.host/020.b.json"]; !ok {
		t.Errorf("entries %v", entries)
	}
}

func TestManifestDisclosure(t *testing.T) {
	m, _ := manifestOf(t, runOptions(t, fakePlatform(readSource("010.a", 1))))
	for _, s := range []string{"password", "other instances"} {
		if !strings.Contains(m, s) {
			t.Errorf("the manifest lacks %q:\n%s", s, m)
		}
	}
}

func TestManifestCopiedAndNotes(t *testing.T) {
	p := fakePlatform(Source{Name: "060.x", Read: func(context.Context, Env, FileSink) SourceResult {
		return SourceResult{Status: "read", Count: 1,
			Copied: []CopiedFile{{Source: `C:\x\a.txt`, Archived: "060.x/a.txt", Size: 12}},
			Notes:  []string{"auto-backup"}}
	}})
	m, _ := manifestOf(t, runOptions(t, p))
	for _, s := range []string{`C:\x\a.txt`, "12", "auto-backup"} {
		if !strings.Contains(m, s) {
			t.Errorf("the manifest lacks %q:\n%s", s, m)
		}
	}
}

func TestArchiveWriteErrorIsSticky(t *testing.T) {
	dir := t.TempDir()
	a, err := createArchive(dir, "n", func(w io.Writer) io.Writer { return &failingWriter{w: w, fail: true} })
	if err != nil {
		t.Fatal(err)
	}
	defer a.Remove()
	if err := a.Put("a", bytes.NewReader(incompressible())); err == nil {
		t.Fatal("Put succeeded over a failing writer")
	}
	if a.Err() == nil {
		t.Fatal("Err is nil after a write error")
	}
	if err := a.Put("b", strings.NewReader("x")); err == nil {
		t.Error("a later Put succeeded")
	}
}

func TestArchiveReaderErrorIsNot(t *testing.T) {
	dir := t.TempDir()
	a, err := CreateArchive(dir, "n")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Put("a", failingReader{}); err == nil {
		t.Fatal("Put hid the reader's error")
	}
	if a.Err() != nil {
		t.Fatalf("Err %v after a reader error", a.Err())
	}
	if err := a.Put("b", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if got := zipEntries(t, filepath.Join(dir, "n.zip"))["n/b"]; got != "x" {
		t.Errorf("n/b = %q", got)
	}
}

func TestArchiveCloseErrorIsSticky(t *testing.T) {
	dir := t.TempDir()
	fw := &failingWriter{}
	a, err := createArchive(dir, "n", func(w io.Writer) io.Writer { fw.w = w; return fw })
	if err != nil {
		t.Fatal(err)
	}
	defer a.Remove()
	if err := a.Put("a", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	fw.fail = true
	if err := a.Close(); err == nil {
		t.Fatal("Close succeeded over a failing writer")
	}
	if a.Err() == nil {
		t.Fatal("Err is nil after a Close error")
	}
}
