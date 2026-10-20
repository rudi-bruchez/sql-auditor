package host

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

const mib = 1 << 20

// recordingSink keeps the entries written and the number of bytes of each.
type recordingSink struct {
	names []string
	sizes []int64
	data  map[string][]byte
}

func (s *recordingSink) sink(keep bool) FileSink {
	return func(archived string, r io.Reader) error {
		var b bytes.Buffer
		var w io.Writer = io.Discard
		if keep {
			w = &b
		}
		n, err := io.Copy(w, r)
		if err != nil {
			return err
		}
		s.names = append(s.names, archived)
		s.sizes = append(s.sizes, n)
		if keep {
			if s.data == nil {
				s.data = map[string][]byte{}
			}
			s.data[archived] = b.Bytes()
		}
		return nil
	}
}

// acceptAll accepts every directory, giving as its final path what finalOf
// gives, which fsOpen gives for the files under it.
func acceptAll(dir string) (string, bool, string) {
	return finalOf(strings.TrimRight(dir, `\/`)), true, ""
}

// finalOf is the final path the doubles give for a path as typed.
func finalOf(p string) string { return `\\?\` + p }

// fsOpen is the portable double of the Windows OpenFile over fsys, rooted at
// the Windows directory root: a link is refused by Lstat, then the file is
// opened, and its size read from what was opened.
func fsOpen(fsys fs.FS, root string) OpenFile {
	return func(p string) (io.ReadSeekCloser, int64, string, string, error) {
		rel := strings.ReplaceAll(strings.TrimPrefix(strings.TrimPrefix(p, root), `\`), `\`, "/")
		if info, err := fs.Lstat(fsys, rel); err == nil && info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return nil, 0, "", "link, not followed", nil
		}
		f, err := fsys.Open(rel)
		if err != nil {
			return nil, 0, "", "not opened: " + err.Error(), nil
		}
		info, err := f.Stat()
		rs, ok := f.(io.ReadSeekCloser)
		if err != nil || !ok {
			f.Close()
			return nil, 0, "", "", fmt.Errorf("%s: no size or no seek (%v)", p, err)
		}
		return rs, info.Size(), finalOf(p), "", nil
	}
}

// jobCalls lists and opens the job output directory root through fsys.
func jobCalls(fsys fs.FS, root string) Calls {
	return Calls{
		OpenDir:  func(string) (fs.FS, error) { return fsys, nil },
		DirCheck: acceptAll,
		OpenFile: fsOpen(fsys, root),
	}
}

// sizedFS is a flat file system whose files report a size and read as zeros,
// so a 30 MiB file costs no 30 MiB of memory.
type sizedFS map[string]sizedInfo

type sizedInfo struct {
	name  string
	size  int64
	mod   time.Time
	extra int64 // bytes written after it was listed
}

func (i sizedInfo) Name() string { return i.name }
func (i sizedInfo) Size() int64  { return i.size }
func (i sizedInfo) Mode() fs.FileMode {
	if i.name == "." {
		return fs.ModeDir | 0o555
	}
	return 0o444
}
func (i sizedInfo) ModTime() time.Time { return i.mod }
func (i sizedInfo) IsDir() bool        { return i.name == "." }
func (i sizedInfo) Sys() any           { return nil }

func (f sizedFS) Stat(name string) (fs.FileInfo, error) {
	if name == "." {
		return sizedInfo{name: "."}, nil
	}
	if i, ok := f[name]; ok {
		return i, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

func (f sizedFS) ReadDir(name string) ([]fs.DirEntry, error) {
	var out []fs.DirEntry
	for _, i := range f {
		out = append(out, fs.FileInfoToDirEntry(i))
	}
	return out, nil
}

func (f sizedFS) Open(name string) (fs.File, error) {
	i, ok := f[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &zeroFile{info: i, left: i.size + i.extra}, nil
}

type zeroFile struct {
	info sizedInfo
	left int64
}

func (z *zeroFile) Stat() (fs.FileInfo, error) { return z.info, nil }
func (z *zeroFile) Close() error               { return nil }
func (z *zeroFile) Seek(off int64, whence int) (int64, error) {
	if whence != io.SeekStart {
		return 0, fmt.Errorf("whence %d not supported", whence)
	}
	z.left = z.info.size + z.info.extra - off
	return off, nil
}
func (z *zeroFile) Read(p []byte) (int, error) {
	if z.left == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), z.left)
	clear(p[:n])
	z.left -= n
	return int(n), nil
}

func TestCopyExtensionsBudgetNewestFirst(t *testing.T) {
	since := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	fsys := sizedFS{
		"a.log": {name: "a.log", size: 30 * mib, mod: since.Add(3 * time.Hour)},
		"b.log": {name: "b.log", size: 20 * mib, mod: since.Add(2 * time.Hour)},
		"c.log": {name: "c.log", size: 10 * mib, mod: since.Add(1 * time.Hour)},
	}
	var rec recordingSink
	listed, err := CopyExtensions(fsys, `C:\P`, finalOf(`C:\P`), since, 50*mib, fsOpen(fsys, `C:\P`), rec.sink(false))
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"060.vm-extensions/a.log", "060.vm-extensions/b.log"}
	if !reflect.DeepEqual(rec.names, wantNames) || !reflect.DeepEqual(rec.sizes, []int64{30 * mib, 20 * mib}) {
		t.Errorf("copied %v %v, want %v of 30 and 20 MiB", rec.names, rec.sizes, wantNames)
	}
	if len(listed) != 3 {
		t.Fatalf("listed %d files, want every file: %+v", len(listed), listed)
	}
	for _, l := range listed {
		over := l.Path == `C:\P\c.log`
		if l.Copied == over || (over && l.Reason != "over the 50 MiB budget") || (!over && l.Reason != "") {
			t.Errorf("listed %+v", l)
		}
	}
}

func TestCopyExtensionsOldFilesListedNotCopied(t *testing.T) {
	since := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	fsys := fstest.MapFS{
		"Ext/1.0/old.log": {Data: []byte("old"), ModTime: since.Add(-time.Second)},
		"Ext/1.0/new.log": {Data: []byte("new"), ModTime: since},
	}
	var rec recordingSink
	listed, err := CopyExtensions(fsys, `C:\WindowsAzure\Logs\Plugins`, finalOf(`C:\WindowsAzure\Logs\Plugins`), since, 50*mib, fsOpen(fsys, `C:\WindowsAzure\Logs\Plugins`), rec.sink(true))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rec.names, []string{"060.vm-extensions/Ext/1.0/new.log"}) {
		t.Errorf("copied %v, want only new.log", rec.names)
	}
	want := []Listed{
		{Path: `C:\WindowsAzure\Logs\Plugins\Ext\1.0\new.log`, Size: 3, Modified: "2026-07-22T00:00:00Z", Copied: true},
		{Path: `C:\WindowsAzure\Logs\Plugins\Ext\1.0\old.log`, Size: 3, Modified: "2026-07-21T23:59:59Z", Reason: "before the window"},
	}
	if !reflect.DeepEqual(listed, want) {
		t.Errorf("listed\n got %+v\nwant %+v", listed, want)
	}
}

// failDirFS is a MapFS whose ReadDir fails for one directory.
type failDirFS struct {
	fstest.MapFS
	bad string
}

func (f failDirFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name == f.bad {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrPermission}
	}
	return f.MapFS.ReadDir(name)
}

func TestCopyExtensionsUnreadableDirListed(t *testing.T) {
	since := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	fsys := failDirFS{MapFS: fstest.MapFS{
		"Bad/1.0/x.log":  {Data: []byte("x"), ModTime: since},
		"Good/1.0/y.log": {Data: []byte("y"), ModTime: since},
	}, bad: "Bad"}
	var rec recordingSink
	listed, err := CopyExtensions(fsys, `C:\P`, finalOf(`C:\P`), since, 50*mib, fsOpen(fsys, `C:\P`), rec.sink(false))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rec.names, []string{"060.vm-extensions/Good/1.0/y.log"}) {
		t.Errorf("copied %v, want y.log", rec.names)
	}
	if len(listed) != 2 || listed[0].Path != `C:\P\Bad` || !strings.HasPrefix(listed[0].Reason, "unreadable: ") ||
		listed[0].Copied || !listed[1].Copied {
		t.Errorf("listed %+v", listed)
	}
}

func TestCopyExtensionsLinksNotFollowed(t *testing.T) {
	since := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	fsys := fstest.MapFS{
		"Ext/a.log":    {Data: []byte("a"), ModTime: since},
		"Ext/link.log": {Data: []byte("a.log"), Mode: fs.ModeSymlink, ModTime: since},
		"Ext/junction": {Mode: fs.ModeIrregular, ModTime: since}, // a junction since Go 1.23
		"Ext/dirlink":  {Data: []byte("../Other"), Mode: fs.ModeSymlink, ModTime: since},
		"Other/b.log":  {Data: []byte("b"), ModTime: since},
	}
	var rec recordingSink
	listed, err := CopyExtensions(fsys, `C:\P`, finalOf(`C:\P`), since, 50*mib, fsOpen(fsys, `C:\P`), rec.sink(false))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"060.vm-extensions/Ext/a.log", "060.vm-extensions/Other/b.log"}
	if !reflect.DeepEqual(rec.names, want) {
		t.Errorf("copied %v, want %v", rec.names, want)
	}
	links := map[string]bool{}
	for _, l := range listed {
		if l.Reason == "link, not followed" && !l.Copied {
			links[l.Path] = true
		}
	}
	if !reflect.DeepEqual(links, map[string]bool{`C:\P\Ext\link.log`: true, `C:\P\Ext\junction`: true, `C:\P\Ext\dirlink`: true}) {
		t.Errorf("listed %+v", listed)
	}
}

func TestCopyExtensionsRefusedUsesNoBudget(t *testing.T) {
	since := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	fsys := sizedFS{
		"a.log": {name: "a.log", size: 40 * mib, mod: since.Add(2 * time.Hour)},
		"b.log": {name: "b.log", size: 20 * mib, mod: since.Add(1 * time.Hour)},
	}
	var asked []string
	open := func(p string) (io.ReadSeekCloser, int64, string, string, error) {
		asked = append(asked, p)
		if p == `C:\P\a.log` {
			return nil, 0, "", "network drive", nil
		}
		return fsOpen(fsys, `C:\P`)(p)
	}
	var rec recordingSink
	listed, err := CopyExtensions(fsys, `C:\P`, finalOf(`C:\P`), since, 50*mib, open, rec.sink(false))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rec.names, []string{"060.vm-extensions/b.log"}) || !reflect.DeepEqual(asked, []string{`C:\P\a.log`, `C:\P\b.log`}) {
		t.Errorf("copied %v, asked %v", rec.names, asked)
	}
	for _, l := range listed {
		if l.Path == `C:\P\a.log` && (l.Copied || l.Reason != "network drive") {
			t.Errorf("listed %+v", l)
		}
	}
}

// The copy stops at the first file that does not fit, newest first: an older
// file small enough for what is left of the budget is not copied either.
func TestCopyExtensionsStopsAtFirstOverBudget(t *testing.T) {
	since := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	fsys := sizedFS{
		"a.log": {name: "a.log", size: 40 * mib, mod: since.Add(3 * time.Hour)},
		"b.log": {name: "b.log", size: 20 * mib, mod: since.Add(2 * time.Hour)},
		"c.log": {name: "c.log", size: 5 * mib, mod: since.Add(1 * time.Hour)},
	}
	var rec recordingSink
	listed, err := CopyExtensions(fsys, `C:\P`, finalOf(`C:\P`), since, 50*mib, fsOpen(fsys, `C:\P`), rec.sink(false))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rec.names, []string{"060.vm-extensions/a.log"}) {
		t.Errorf("copied %v, want only a.log", rec.names)
	}
	for _, l := range listed {
		if l.Path == `C:\P\c.log` && l.Reason != "over the 50 MiB budget" {
			t.Errorf("listed %+v", l)
		}
	}
}

func TestCopyExtensionsGrowingFileCutAtListedSize(t *testing.T) {
	since := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	fsys := sizedFS{"a.log": {name: "a.log", size: 10, mod: since, extra: mib}}
	var rec recordingSink
	if _, err := CopyExtensions(fsys, `C:\P`, finalOf(`C:\P`), since, 50*mib, fsOpen(fsys, `C:\P`), rec.sink(false)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rec.sizes, []int64{10}) {
		t.Errorf("copied %v bytes, want 10", rec.sizes)
	}
}

func TestCopyExtensionsMissingRoot(t *testing.T) {
	_, err := CopyExtensions(os.DirFS(filepath.Join(t.TempDir(), "missing")), `C:\P`, finalOf(`C:\P`), time.Time{}, 50*mib, fsOpen(fstest.MapFS{}, `C:\P`), (&recordingSink{}).sink(false))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err %v, want fs.ErrNotExist", err)
	}
}

// olaPattern is the output file name of Ola Hallengren's current version.
func olaPattern(t *testing.T, dir string) OutputPattern {
	t.Helper()
	p := ExpandOutputFile(dir+`\$(ESCAPE_SQUOTE(JOBNAME))_$(ESCAPE_SQUOTE(STEPID))_$(ESCAPE_SQUOTE(DATE))_$(ESCAPE_SQUOTE(TIME)).txt`,
		TokenValues{JobName: "DatabaseBackup - USER_DATABASES - FULL", StepID: 1})
	if !p.Readable {
		t.Fatalf("pattern not readable: %s", p.Reason)
	}
	return p
}

func TestCopyJobOutputsFiveNewest(t *testing.T) {
	since := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	m := fstest.MapFS{}
	var names []string
	for d := 1; d <= 7; d++ {
		n := fmt.Sprintf("DatabaseBackup - USER_DATABASES - FULL_1_202607%02d_10000.txt", 20+d)
		m[n] = &fstest.MapFile{Data: []byte(n), ModTime: since.Add(time.Duration(d) * time.Hour)}
		names = append([]string{n}, names...)
	}
	var rec recordingSink
	copied, notes, err := CopyJobOutputs(jobCalls(m, `X:\Logs`), olaPattern(t, `X:\Logs`), "Backup_1", since, rec.sink(true))
	if err != nil || len(notes) != 0 {
		t.Fatalf("err %v, notes %v", err, notes)
	}
	var want []string
	for _, n := range names[:5] {
		want = append(want, "070.job-outputs/Backup_1/"+n)
	}
	if !reflect.DeepEqual(rec.names, want) {
		t.Errorf("copied\n got %v\nwant %v", rec.names, want)
	}
	if len(copied) != 5 || copied[0].Source != `X:\Logs\`+names[0] || copied[0].Archived != want[0] ||
		copied[0].Size != int64(len(names[0])) || copied[0].Cut || copied[0].Modified != "2026-07-22T07:00:00Z" {
		t.Errorf("copied %+v", copied)
	}
}

func TestCopyJobOutputsSkipsNonMatching(t *testing.T) {
	since := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	m := fstest.MapFS{
		"DatabaseBackup - USER_DATABASES - FULL_1_20260723_10000.txt": {Data: []byte("x"), ModTime: since.Add(time.Hour)},
		"DatabaseBackup - USER_DATABASES - FULL_2_20260723_10000.txt": {Data: []byte("x"), ModTime: since.Add(time.Hour)},
		"DatabaseBackup - USER_DATABASES - LOG_1_20260723_10000.txt":  {Data: []byte("x"), ModTime: since.Add(time.Hour)},
		"DatabaseBackup - USER_DATABASES - FULL_1_20260720_10000.txt": {Data: []byte("x"), ModTime: since.Add(-time.Hour)},
		"sub/DatabaseBackup - USER_DATABASES - FULL_1_20260723_1.txt": {Data: []byte("x"), ModTime: since.Add(time.Hour)},
	}
	var rec recordingSink
	_, notes, err := CopyJobOutputs(jobCalls(m, `X:\Logs`), olaPattern(t, `X:\Logs`), "Backup_1", since, rec.sink(false))
	if err != nil || len(notes) != 0 {
		t.Fatalf("err %v, notes %v", err, notes)
	}
	want := []string{"070.job-outputs/Backup_1/DatabaseBackup - USER_DATABASES - FULL_1_20260723_10000.txt"}
	if !reflect.DeepEqual(rec.names, want) {
		t.Errorf("copied %v, want %v", rec.names, want)
	}

	_, notes, err = CopyJobOutputs(jobCalls(fstest.MapFS{}, `X:\Logs`), olaPattern(t, `X:\Logs`), "Backup_1", since, rec.sink(false))
	if err != nil || len(notes) != 1 || !strings.Contains(notes[0], "matches nothing") {
		t.Errorf("empty directory: err %v, notes %v", err, notes)
	}
}

func TestCopyJobOutputsRefusesUNC(t *testing.T) {
	opened := false
	c := jobCalls(fstest.MapFS{}, `\\FS01\logs`)
	c.OpenDir = func(string) (fs.FS, error) { opened = true; return fstest.MapFS{}, nil }
	var rec recordingSink
	copied, notes, err := CopyJobOutputs(c, olaPattern(t, `\\FS01\logs`), "Backup_1", time.Time{}, rec.sink(false))
	if err != nil || opened || len(copied) != 0 || len(notes) != 1 || !strings.Contains(notes[0], "UNC") {
		t.Errorf("err %v, opened %v, copied %v, notes %v", err, opened, copied, notes)
	}
}

func TestCopyJobOutputsUnreadablePattern(t *testing.T) {
	p := ExpandOutputFile(`$(SQLDIR)\Log\out.txt`, TokenValues{})
	opened := false
	c := jobCalls(fstest.MapFS{}, "")
	c.OpenDir = func(string) (fs.FS, error) { opened = true; return fstest.MapFS{}, nil }
	copied, notes, err := CopyJobOutputs(c, p, "Backup_1", time.Time{}, (&recordingSink{}).sink(false))
	if err != nil || opened || len(copied) != 0 || !reflect.DeepEqual(notes, []string{"token SQLDIR in the directory"}) {
		t.Errorf("err %v, opened %v, copied %v, notes %v", err, opened, copied, notes)
	}
}

func TestOpenFileRefusalIsRecorded(t *testing.T) {
	since := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	n := "DatabaseBackup - USER_DATABASES - FULL_1_20260723_10000.txt"
	m := fstest.MapFS{n: {Data: []byte("x"), ModTime: since.Add(time.Hour)}}
	var asked []string
	c := jobCalls(m, `X:\Logs`)
	c.OpenFile = func(path string) (io.ReadSeekCloser, int64, string, string, error) {
		asked = append(asked, path)
		return nil, 0, "", "network drive", nil
	}
	var rec recordingSink
	copied, notes, err := CopyJobOutputs(c, olaPattern(t, `X:\Logs`), "Backup_1", since, rec.sink(false))
	if err != nil || len(copied) != 0 || len(rec.names) != 0 {
		t.Fatalf("err %v, copied %v, written %v", err, copied, rec.names)
	}
	if !reflect.DeepEqual(asked, []string{`X:\Logs\` + n}) || !reflect.DeepEqual(notes, []string{`X:\Logs\` + n + ": network drive"}) {
		t.Errorf("asked %v, notes %v", asked, notes)
	}
}

func TestCopyJobOutputsRefusesRelativeDir(t *testing.T) {
	for _, dir := range []string{"", "Logs", "C:Logs"} {
		opened := false
		c := jobCalls(fstest.MapFS{}, dir)
		c.OpenDir = func(string) (fs.FS, error) { opened = true; return fstest.MapFS{}, nil }
		copied, notes, err := CopyJobOutputs(c, olaPattern(t, dir), "Backup_1", time.Time{}, (&recordingSink{}).sink(false))
		if err != nil || opened || len(copied) != 0 || !reflect.DeepEqual(notes, []string{dir + ": not an absolute path"}) {
			t.Errorf("%q: err %v, opened %v, copied %v, notes %v", dir, err, opened, copied, notes)
		}
	}
}

func TestCopyJobOutputsWildcardNoted(t *testing.T) {
	since := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	p := ExpandOutputFile(`X:\Logs\$(MACH)_out.txt`, TokenValues{})
	if !p.Readable || p.Reason == "" {
		t.Fatalf("pattern %+v", p)
	}
	m := fstest.MapFS{"SQL01_out.txt": {Data: []byte("x"), ModTime: since}}
	var rec recordingSink
	copied, notes, err := CopyJobOutputs(jobCalls(m, `X:\Logs`), p, "Backup_1", since, rec.sink(false))
	if err != nil || len(copied) != 1 || !reflect.DeepEqual(notes, []string{p.Reason}) {
		t.Errorf("err %v, copied %v, notes %v", err, copied, notes)
	}
}

func TestCopyJobOutputsKeepsFileAtSince(t *testing.T) {
	since := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	n := "DatabaseBackup - USER_DATABASES - FULL_1_20260722_0.txt"
	m := fstest.MapFS{n: {Data: []byte("x"), ModTime: since}}
	var rec recordingSink
	copied, notes, err := CopyJobOutputs(jobCalls(m, `X:\Logs`), olaPattern(t, `X:\Logs`), "Backup_1", since, rec.sink(false))
	if err != nil || len(copied) != 1 || len(notes) != 0 {
		t.Errorf("err %v, copied %v, notes %v", err, copied, notes)
	}
}

func TestCopyJobOutputsGrowingFileCutAtListedSize(t *testing.T) {
	since := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	n := "DatabaseBackup - USER_DATABASES - FULL_1_20260722_0.txt"
	fsys := sizedFS{n: {name: n, size: 10, mod: since, extra: mib}}
	var rec recordingSink
	if _, _, err := CopyJobOutputs(jobCalls(fsys, `X:\Logs`), olaPattern(t, `X:\Logs`), "Backup_1", since, rec.sink(false)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rec.sizes, []int64{10}) {
		t.Errorf("copied %v bytes, want 10", rec.sizes)
	}
}

func TestCopyJobOutputsMissingDir(t *testing.T) {
	missing := os.DirFS(filepath.Join(t.TempDir(), "missing"))
	copied, notes, err := CopyJobOutputs(jobCalls(missing, `X:\Logs`), olaPattern(t, `X:\Logs`), "Backup_1", time.Time{}, (&recordingSink{}).sink(false))
	if err != nil || len(copied) != 0 || !reflect.DeepEqual(notes, []string{`X:\Logs: matches nothing in the window`}) {
		t.Errorf("err %v, copied %v, notes %v", err, copied, notes)
	}
}

// seekFirst fails the test when it is read before being positioned at want.
type seekFirst struct {
	t      *testing.T
	r      *bytes.Reader
	want   int64
	seeked bool
}

func (s *seekFirst) Read(p []byte) (int, error) {
	if !s.seeked {
		s.t.Fatalf("Read before a Seek to %d", s.want)
	}
	return s.r.Read(p)
}

func (s *seekFirst) Seek(off int64, whence int) (int64, error) {
	pos, err := s.r.Seek(off, whence)
	if pos == s.want {
		s.seeked = true
	}
	return pos, err
}

func TestTailReaderSeeksNotReads(t *testing.T) {
	data := make([]byte, 3*mib)
	for i := range data {
		data[i] = byte(i / mib) // 0, 1, 2 per MiB
	}
	f := &seekFirst{t: t, r: bytes.NewReader(data), want: 2 * mib}
	r, cut, err := TailReader(f, 3*mib, mib)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !cut || !bytes.Equal(got, data[2*mib:]) {
		t.Errorf("cut %v, read %d bytes, want the last MiB", cut, len(got))
	}
}

func TestTailReaderSmallFileWhole(t *testing.T) {
	r, cut, err := TailReader(strings.NewReader("whole file"), 10, mib)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	if cut || string(got) != "whole file" {
		t.Errorf("cut %v, got %q", cut, got)
	}
}

func TestIsUNC(t *testing.T) {
	for p, want := range map[string]bool{
		`\\server\share\logs`:         true,
		`\\?\UNC\server\share\logs`:   true,
		`\\?\unc\server\share`:        true,
		`//server/share/logs`:         true,
		`\\?\C:\Logs`:                 false,
		`\\.\C:\Logs`:                 false,
		`C:\Logs`:                     false,
		`C:/Logs`:                     false,
		`\Logs`:                       false,
		`D:\MSSQL\Log\$(JOBNAME).txt`: false,
		``:                            false,
	} {
		if got := IsUNC(p); got != want {
			t.Errorf("IsUNC(%q) = %v, want %v", p, got, want)
		}
	}
}
