package host

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
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

func acceptAll(string) (bool, string) { return true, "" }

// sizedFS is a flat file system whose files report a size and read as zeros,
// so a 30 MiB file costs no 30 MiB of memory.
type sizedFS map[string]sizedInfo

type sizedInfo struct {
	name string
	size int64
	mod  time.Time
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
	return &zeroFile{info: i, left: i.size}, nil
}

type zeroFile struct {
	info sizedInfo
	left int64
}

func (z *zeroFile) Stat() (fs.FileInfo, error) { return z.info, nil }
func (z *zeroFile) Close() error               { return nil }
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
	listed, err := CopyExtensions(fsys, ".", since, 50*mib, acceptAll, rec.sink(false))
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
		over := l.Path == "c.log"
		if l.Copied == over || (over && l.Reason != "over the 50 MiB budget") || (!over && l.Reason != "") {
			t.Errorf("listed %+v", l)
		}
	}
}

func TestCopyExtensionsOldFilesListedNotCopied(t *testing.T) {
	since := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	fsys := fstest.MapFS{
		"Plugins/Ext/1.0/old.log": {Data: []byte("old"), ModTime: since.Add(-time.Second)},
		"Plugins/Ext/1.0/new.log": {Data: []byte("new"), ModTime: since},
	}
	var rec recordingSink
	listed, err := CopyExtensions(fsys, "Plugins", since, 50*mib, acceptAll, rec.sink(true))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rec.names, []string{"060.vm-extensions/Ext/1.0/new.log"}) {
		t.Errorf("copied %v, want only new.log", rec.names)
	}
	want := []Listed{
		{Path: "Plugins/Ext/1.0/new.log", Size: 3, Modified: "2026-07-22T00:00:00Z", Copied: true},
		{Path: "Plugins/Ext/1.0/old.log", Size: 3, Modified: "2026-07-21T23:59:59Z", Reason: "before the window"},
	}
	if !reflect.DeepEqual(listed, want) {
		t.Errorf("listed\n got %+v\nwant %+v", listed, want)
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

func openMap(m fstest.MapFS) func(string) (fs.FS, error) {
	return func(string) (fs.FS, error) { return m, nil }
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
	copied, notes, err := CopyJobOutputs(openMap(m), olaPattern(t, `X:\Logs`), "Backup_1", since, acceptAll, rec.sink(true))
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
	_, notes, err := CopyJobOutputs(openMap(m), olaPattern(t, `X:\Logs`), "Backup_1", since, acceptAll, rec.sink(false))
	if err != nil || len(notes) != 0 {
		t.Fatalf("err %v, notes %v", err, notes)
	}
	want := []string{"070.job-outputs/Backup_1/DatabaseBackup - USER_DATABASES - FULL_1_20260723_10000.txt"}
	if !reflect.DeepEqual(rec.names, want) {
		t.Errorf("copied %v, want %v", rec.names, want)
	}

	_, notes, err = CopyJobOutputs(openMap(fstest.MapFS{}), olaPattern(t, `X:\Logs`), "Backup_1", since, acceptAll, rec.sink(false))
	if err != nil || len(notes) != 1 || !strings.Contains(notes[0], "matches nothing") {
		t.Errorf("empty directory: err %v, notes %v", err, notes)
	}
}

func TestCopyJobOutputsRefusesUNC(t *testing.T) {
	opened := false
	open := func(string) (fs.FS, error) { opened = true; return fstest.MapFS{}, nil }
	var rec recordingSink
	copied, notes, err := CopyJobOutputs(open, olaPattern(t, `\\FS01\logs`), "Backup_1", time.Time{}, acceptAll, rec.sink(false))
	if err != nil || opened || len(copied) != 0 || len(notes) != 1 || !strings.Contains(notes[0], "UNC") {
		t.Errorf("err %v, opened %v, copied %v, notes %v", err, opened, copied, notes)
	}
}

func TestCopyJobOutputsUnreadablePattern(t *testing.T) {
	p := ExpandOutputFile(`$(SQLDIR)\Log\out.txt`, TokenValues{})
	opened := false
	open := func(string) (fs.FS, error) { opened = true; return fstest.MapFS{}, nil }
	copied, notes, err := CopyJobOutputs(open, p, "Backup_1", time.Time{}, acceptAll, (&recordingSink{}).sink(false))
	if err != nil || opened || len(copied) != 0 || !reflect.DeepEqual(notes, []string{"token SQLDIR in the directory"}) {
		t.Errorf("err %v, opened %v, copied %v, notes %v", err, opened, copied, notes)
	}
}

func TestPathCheckRefusalIsRecorded(t *testing.T) {
	since := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	n := "DatabaseBackup - USER_DATABASES - FULL_1_20260723_10000.txt"
	m := fstest.MapFS{n: {Data: []byte("x"), ModTime: since.Add(time.Hour)}}
	var asked []string
	check := func(path string) (bool, string) { asked = append(asked, path); return false, "network drive" }
	var rec recordingSink
	copied, notes, err := CopyJobOutputs(openMap(m), olaPattern(t, `X:\Logs`), "Backup_1", since, check, rec.sink(false))
	if err != nil || len(copied) != 0 || len(rec.names) != 0 {
		t.Fatalf("err %v, copied %v, written %v", err, copied, rec.names)
	}
	if !reflect.DeepEqual(asked, []string{`X:\Logs\` + n}) || !reflect.DeepEqual(notes, []string{`X:\Logs\` + n + ": network drive"}) {
		t.Errorf("asked %v, notes %v", asked, notes)
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
