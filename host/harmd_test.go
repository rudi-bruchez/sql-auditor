package host

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	"unicode/utf16"
)

// What the fourth batch of the harm review closed that runs away from Windows.

// A directory whose final path changed after DirCheck, because a component
// was swapped for a junction, gives files whose final path is not under the
// one DirCheck took: every source refuses them and writes nothing. The same
// files are read when their final path matches, whatever its case.
func TestHarmD_FileUnderTheCheckedFinalPath(t *testing.T) {
	mod := sourcesSince.Add(time.Hour)
	for _, tc := range []struct {
		name  string
		final func(p string) string // the opened file's final path
		read  bool
	}{
		{"same directory", func(p string) string { return strings.ToUpper(finalOf(p)) }, true},
		{"moved under a link", func(p string) string { return `\\?\C:\Users\dba\Documents\` + p[strings.LastIndex(p, `\`)+1:] }, false},
	} {
		open := func(p string) (io.ReadSeekCloser, int64, string, string, error) {
			return nopReadSeekCloser{strings.NewReader("opened")}, 6, tc.final(p), "", nil
		}
		check := func(t *testing.T, s *recordingSink, reasons []string, data string) {
			t.Helper()
			_, wrote := s.data[data]
			// One file, refused as moved, or the directory noted as changed.
			refused := strings.Contains(strings.Join(reasons, "\n"), "under a link")
			if wrote != tc.read || refused == tc.read {
				t.Errorf("%s written %v, refusal recorded %v (%q); want read %v", data, wrote, refused, reasons, tc.read)
			}
		}
		t.Run(tc.name+"/job outputs", func(t *testing.T) {
			c := fakeCalls()
			c.OpenDir = func(string) (fs.FS, error) { return fstest.MapFS{"out.txt": {Data: []byte("x"), ModTime: mod}}, nil }
			c.OpenFile = open
			e := matchedEnv()
			e.Steps = []JobStep{{Job: "J", StepID: 1, OutputFile: `C:\Logs\out.txt`}}
			s := &recordingSink{}
			res := readNamed(t, c, "070.job-outputs", e, s)
			check(t, s, res.Notes, "070.job-outputs/J_1/out.txt")
		})
		t.Run(tc.name+"/SQL Writer", func(t *testing.T) {
			c := fakeCalls()
			c.ReadFile = func(string) ([]byte, error) { return []byte("[Logging]\r\n"), nil }
			c.OpenDir = func(string) (fs.FS, error) {
				return fstest.MapFS{"SqlWriterLogger.txt": {Data: []byte("x"), ModTime: mod}}, nil
			}
			c.OpenFile = open
			s := &recordingSink{}
			res := readNamed(t, c, "035.sqlwriter", Env{Since: sourcesSince}, s)
			check(t, s, res.Notes, "035.sqlwriter/SqlWriterLogger.txt")
		})
		t.Run(tc.name+"/VM extensions", func(t *testing.T) {
			c := fakeCalls()
			c.AzurePlugins = `C:\WindowsAzure\Logs\Plugins`
			c.OpenDir = func(string) (fs.FS, error) {
				return fstest.MapFS{"Ext/1.0/a.log": {Data: []byte("x"), ModTime: mod}}, nil
			}
			c.OpenFile = open
			s := &recordingSink{}
			res := readNamed(t, c, "060.vm-extensions", Env{Since: sourcesSince}, s)
			reasons := res.Notes
			for _, l := range listedIn(t, s) {
				reasons = append(reasons, l.Reason)
			}
			check(t, s, reasons, "060.vm-extensions/Ext/1.0/a.log")
		})
	}
}

// reparseBuffer is a REPARSE_DATA_BUFFER of a mount point tag, or of tag.
func reparseBuffer(tag uint32, substitute string) []byte {
	name := utf16.Encode([]rune(substitute))
	b := make([]byte, 16+2*len(name)+2)
	binary.LittleEndian.PutUint32(b, tag)
	binary.LittleEndian.PutUint16(b[4:], uint16(len(b)-8))
	binary.LittleEndian.PutUint16(b[8:], 0)                    // substitute name offset
	binary.LittleEndian.PutUint16(b[10:], uint16(2*len(name))) // and length
	binary.LittleEndian.PutUint16(b[12:], uint16(2*len(name))) // print name offset, empty
	for i, u := range name {
		binary.LittleEndian.PutUint16(b[16+2*i:], u)
	}
	return b
}

// Only a volume mount point is followed: a junction to a directory, whatever
// the directory, is not.
func TestHarmD_OnlyVolumeMountPoints(t *testing.T) {
	const mountPoint, symlink = 0xA0000003, 0xA000000C
	for _, tc := range []struct {
		b    []byte
		want bool
	}{
		{reparseBuffer(mountPoint, `\??\Volume{3f2504e0-4f89-11d3-9a0c-0305e82c3301}\`), true},
		{reparseBuffer(mountPoint, `\??\C:\ProgramData\Microsoft\Crypto\RSA\MachineKeys`), false},
		{reparseBuffer(mountPoint, `\??\UNC\server\share`), false},
		{reparseBuffer(symlink, `\??\Volume{3f2504e0-4f89-11d3-9a0c-0305e82c3301}\`), false},
		{reparseBuffer(mountPoint, `\??\Volume{3f2504e0-4f89-11d3-9a0c-0305e82c3301}\`)[:20], false},
		{nil, false},
	} {
		if _, got := isVolumeMount(tc.b); got != tc.want {
			t.Errorf("isVolumeMount(% x...) = %v, want %v", tc.b[:min(len(tc.b), 24)], got, tc.want)
		}
	}
}

// \\?\C:\Logs is a local absolute path: it goes to DirCheck rather than being
// refused as relative.
func TestHarmD_LongPathPrefixIsLocal(t *testing.T) {
	for _, p := range []string{`\\?\C:\Logs`, `\\?\c:/Logs`} {
		if !isDriveAbsolute(p) {
			t.Errorf("isDriveAbsolute(%s) = false", p)
		}
	}
	for _, p := range []string{`\\?\Logs`, `\\?\C:Logs`, `\\?\UNC\server\share`} {
		if isDriveAbsolute(p) {
			t.Errorf("isDriveAbsolute(%s) = true", p)
		}
	}
	c := fakeCalls()
	var asked []string
	c.DirCheck = func(dir string) (string, bool, string) { asked = append(asked, dir); return "", false, "checked" }
	e := matchedEnv()
	e.Steps = []JobStep{{Job: "J", StepID: 1, OutputFile: `\\?\C:\Logs\out.txt`}}
	res := readNamed(t, c, "070.job-outputs", e, &recordingSink{})
	if len(asked) != 1 || asked[0] != `\\?\C:\Logs` || strings.Contains(strings.Join(res.Notes, "\n"), "not an absolute path") {
		t.Errorf("DirCheck asked %q, notes %q", asked, res.Notes)
	}
}

// SQL Writer's logs share one budget, newest first: the files past it are
// noted, as a directory any user can write may hold thousands of them.
func TestHarmD_SQLWriterBudget(t *testing.T) {
	fsys := sizedFS{}
	var newest []string
	for i := range 8 {
		n := "SqlWriterLogger.txt"
		if i > 0 {
			n = fmt.Sprintf("SqlWriterLogger%d.txt", i)
		}
		// The higher the number, the newer, so name order is not time order.
		fsys[n] = sizedInfo{name: n, size: 10 * mib, mod: sourcesSince.Add(time.Duration(i) * time.Hour)}
		newest = append([]string{"035.sqlwriter/" + n}, newest...)
	}
	c := fakeCalls()
	c.ReadFile = func(string) ([]byte, error) { return []byte("[Logging]\r\n"), nil }
	c.OpenDir = func(string) (fs.FS, error) { return fsys, nil }
	s := &recordingSink{}
	res := readNamed(t, c, "035.sqlwriter", Env{Since: sourcesSince}, s)
	// The active log, the oldest here, first (TestHarmE_SQLWriterActiveFirst).
	want := append(newest[len(newest)-1:], newest[:4]...)
	if fmt.Sprint(s.names) != fmt.Sprint(want) {
		t.Errorf("copied %v, want the active log and the four newest %v", s.names, want)
	}
	budget := 0
	for _, n := range res.Notes {
		if strings.HasSuffix(n, ": budget reached, not copied") {
			budget++
		}
	}
	if budget != 3 || res.Status != StatusRead {
		t.Errorf("status %s, notes %q; want read, the three oldest noted", res.Status, res.Notes)
	}
}

// Every query that keeps events is capped, the restarts of 3a and 3b
// included: any local user can write events under the instance's source.
func TestHarmD_EveryKeepingQueryCapped(t *testing.T) {
	for _, q := range BuildEventQueries("MSSQLSERVER", sourcesSince) {
		if !q.CountOnly && q.Cap <= 0 {
			t.Errorf("query %s on %s keeps events without a cap", q.ID, q.Channel)
		}
	}
}

type panicky struct{}

func (panicky) MarshalJSON() ([]byte, error) { panic("marshal") }

// A panic in the goroutine that writes a streamed file is that file's error,
// not the end of the process.
func TestHarmD_StreamedWriterPanic(t *testing.T) {
	err := streamed((&recordingSink{}).sink(false), "f.json", func(w io.Writer) error {
		return json.NewEncoder(w).Encode(panicky{})
	})
	if err == nil || !strings.Contains(err.Error(), "panic: marshal") {
		t.Errorf("err = %v, want the panic", err)
	}
}

// A query held past sqlTimeout ends, and the job outputs fail with a reason
// naming the timeout, as they do for error 1222.
func TestHarmD_SQLTimeoutNamed(t *testing.T) {
	defer func(d time.Duration) { sqlTimeout = d }(sqlTimeout)
	sqlTimeout = 50 * time.Millisecond
	// Held until the context ends, or 5 s, so that a missing deadline fails
	// rather than hanging the run.
	held := func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return fmt.Errorf("held 5 s: no deadline")
		}
	}
	t.Run("job steps", func(t *testing.T) {
		var seen Env
		p := fakePlatform(jobOutputs(&seen))
		p.Steps = func(ctx context.Context) ([]JobStep, bool, error) { return nil, false, held(ctx) }
		o := runOptions(t, p)
		start := time.Now()
		code := Run(context.Background(), o)
		if took := time.Since(start); code != 2 || took > 5*time.Second {
			t.Errorf("exit %d after %v; want 2 within the timeout", code, took)
		}
		if seen.StepsErr == nil || !strings.Contains(seen.StepsErr.Error(), "timeout: the instance did not answer within 50ms") {
			t.Errorf("StepsErr = %v, want the timeout named", seen.StepsErr)
		}
		res := readNamed(t, fakeCalls(), "070.job-outputs", Env{Facts: &InstanceFacts{}, MachineMatch: true, StepsErr: seen.StepsErr}, &recordingSink{})
		if res.Status != StatusFailed || !strings.Contains(res.Reason, "timeout") {
			t.Errorf("job outputs %s, %q; want failed naming the timeout", res.Status, res.Reason)
		}
	})
	t.Run("facts", func(t *testing.T) {
		var seen Env
		p := fakePlatform(jobOutputs(&seen))
		p.Facts = func(ctx context.Context) (*InstanceFacts, error) { return nil, held(ctx) }
		o := runOptions(t, p)
		if code := Run(context.Background(), o); code != 2 || seen.Facts != nil {
			t.Errorf("exit %d, facts %v; want 2 and none", code, seen.Facts)
		}
	})
	t.Run("interrupt is not a timeout", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := sqlBounded(ctx, held); err != context.Canceled {
			t.Errorf("err = %v, want context.Canceled as is", err)
		}
	})
}
