package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	"unsafe"
)

// What the fifth batch of the harm review closed that runs away from Windows.

// A mount point is a volume's only when its substitute name is exactly the
// volume's root: a junction to a directory of a volume, or to its root
// followed by anything, is not.
func TestHarmE_VolumeMountExactRoot(t *testing.T) {
	const mountPoint = 0xA0000003
	const g = "3f2504e0-4f89-11d3-9a0c-0305e82c3301"
	for _, tc := range []struct {
		sub, want string // want "": refused
	}{
		{`\??\Volume{` + g + `}\`, `\\?\Volume{` + g + `}\`},
		{`\??\Volume{` + g + `}\Users\Administrator\Documents`, ""},
		{`\??\Volume{` + g + `}`, ""},
		{`\??\Volume{x}\..\..\Windows`, ""},
		{`\??\Volume{` + g + `}\..\..\Windows`, ""},
		{g + `}\`, ""},
		{`\??\Volume{` + strings.Replace(g, "-", "x", 1) + `}\`, ""},
	} {
		vol, ok := isVolumeMount(reparseBuffer(mountPoint, tc.sub))
		if vol != tc.want || ok != (tc.want != "") {
			t.Errorf("isVolumeMount(%s) = %q, %v; want %q", tc.sub, vol, ok, tc.want)
		}
	}
}

// A file refused as moved under a link means the directory listed may have
// been another: nothing of what was listed through it is recorded, no file
// name and no note per file, only one note naming the directory. What was
// copied before, at its checked path, stays recorded.
func TestHarmE_ListingDroppedOnSwap(t *testing.T) {
	mod := sourcesSince.Add(time.Hour)
	// The first file opened is at its checked path; every one after it has
	// moved.
	opener := func() OpenFile {
		n := 0
		return func(p string) (io.ReadSeekCloser, int64, string, string, error) {
			final := finalOf(p)
			if n++; n > 1 {
				final = `\\?\C:\Users\dba\Documents\` + p[strings.LastIndex(p, `\`)+1:]
			}
			return nopReadSeekCloser{strings.NewReader("opened")}, 6, final, "", nil
		}
	}
	files := func(names ...string) fstest.MapFS {
		fsys := fstest.MapFS{}
		for i, n := range names {
			fsys[n] = &fstest.MapFile{Data: []byte("x"), ModTime: mod.Add(-time.Duration(i) * time.Minute)}
		}
		return fsys
	}
	// hidden are the names listed after the first, which no record may hold.
	check := func(t *testing.T, notes []string, listing string, dir string, copied int, hidden ...string) {
		t.Helper()
		want := dir + ": changed under a link during the read, nothing listed"
		if len(notes) != 1 || !strings.HasSuffix(notes[0], want) {
			t.Errorf("notes %q, want one: %s", notes, want)
		}
		for _, h := range hidden {
			if strings.Contains(strings.Join(notes, "\n")+listing, h) {
				t.Errorf("%s, listed through the swapped directory, was recorded: %q %s", h, notes, listing)
			}
		}
		if copied != 1 {
			t.Errorf("%d copied recorded, want the one opened before the swap", copied)
		}
	}
	t.Run("job outputs", func(t *testing.T) {
		c := fakeCalls()
		c.OpenDir = func(string) (fs.FS, error) { return files("20260725.txt", "20260724.txt", "20260723.txt"), nil }
		c.OpenFile = opener()
		e := matchedEnv()
		e.Steps = []JobStep{{Job: "J", StepID: 1, OutputFile: `C:\Logs\$(ESCAPE_SQUOTE(STRTDT)).txt`}}
		s := &recordingSink{}
		res := readNamed(t, c, "070.job-outputs", e, s)
		check(t, res.Notes, string(s.data["070.job-outputs.json"]), `C:\Logs`, len(res.Copied), "20260724", "20260723")
	})
	t.Run("SQL Writer", func(t *testing.T) {
		c := fakeCalls()
		c.ReadFile = func(string) ([]byte, error) { return []byte("[Logging]\r\n"), nil }
		c.OpenDir = func(string) (fs.FS, error) {
			return files("SqlWriterLogger.txt", "SqlWriterLogger1.txt", "SqlWriterLogger2.txt"), nil
		}
		c.OpenFile = opener()
		s := &recordingSink{}
		res := readNamed(t, c, "035.sqlwriter", Env{Since: sourcesSince}, s)
		check(t, res.Notes, "", c.ProgramFiles+`\Microsoft SQL Server\90\Shared`, len(res.Copied), "SqlWriterLogger1", "SqlWriterLogger2")
	})
	t.Run("VM extensions", func(t *testing.T) {
		c := fakeCalls()
		c.AzurePlugins = `C:\WindowsAzure\Logs\Plugins`
		c.OpenDir = func(string) (fs.FS, error) {
			return files("Ext/1.0/a.log", "Ext/1.0/salaries.log", "Ext/1.0/c.log"), nil
		}
		c.OpenFile = opener()
		s := &recordingSink{}
		res := readNamed(t, c, "060.vm-extensions", Env{Since: sourcesSince}, s)
		check(t, res.Notes, string(s.data["060.vm-extensions.json"]), c.AzurePlugins, len(res.Copied), "salaries", "c.log")
		if l := listedIn(t, s); len(l) != 1 || !l[0].Copied {
			t.Errorf("listing %+v, want only the file copied", l)
		}
	})
}

// Empty Data values are charged too: an event of 5,000 of them keeps what
// dataBytes allows, in an array of its own rather than the parser's.
func TestHarmE_EmptyDataBounded(t *testing.T) {
	ev, err := ParseEventXML(eventWithData(5000, ""))
	if err != nil {
		t.Fatal(err)
	}
	ev.Keep("")
	want := dataBytes / dataValueOverhead
	if len(ev.Data) != want || ev.DataDropped != 5000-want {
		t.Errorf("kept %d, dropped %d; want %d kept", len(ev.Data), ev.DataDropped, want)
	}
	if cap(ev.Data) > 2*want {
		t.Errorf("Data's array holds %d values for %d kept: the parser's is still alive", cap(ev.Data), len(ev.Data))
	}
}

// A value not cut is copied, so that the builder's spare capacity behind it
// is not kept with the event.
func TestHarmE_DataValueCloned(t *testing.T) {
	var b strings.Builder
	b.Grow(1 << 10)
	b.WriteString("x")
	v := b.String()
	ev := Event{Data: []string{v}}
	ev.Keep("")
	if len(ev.Data) != 1 || ev.Data[0] != "x" || unsafe.StringData(ev.Data[0]) == unsafe.StringData(v) {
		t.Errorf("Data %q shares the builder's buffer", ev.Data)
	}
}

// eventWithData is an event whose EventData holds n Data elements of value v.
func eventWithData(n int, v string) []byte {
	var b strings.Builder
	b.WriteString(`<Event xmlns="http://schemas.microsoft.com/win/2004/08/events/event"><System><Provider Name="MSSQLSERVER"/><EventID>17137</EventID><Level>4</Level><TimeCreated SystemTime="2026-10-20T03:04:05.0000000Z"/><EventRecordID>1</EventRecordID><Channel>Application</Channel></System><EventData>`)
	for range n {
		b.WriteString("<Data>" + v + "</Data>")
	}
	b.WriteString("</EventData></Event>")
	return []byte(b.String())
}

// The active log is copied first whatever its time: rollovers given a time in
// the future do not push it past the budget.
func TestHarmE_SQLWriterActiveFirst(t *testing.T) {
	fsys := sizedFS{"SqlWriterLogger.txt": {name: "SqlWriterLogger.txt", size: 10 * mib, mod: sourcesSince.Add(time.Hour)}}
	want := []string{"035.sqlwriter/SqlWriterLogger.txt"}
	for i := 1; i <= 6; i++ {
		n := fmt.Sprintf("SqlWriterLogger%d.txt", i)
		fsys[n] = sizedInfo{name: n, size: 10 * mib, mod: time.Now().AddDate(1, 0, i)}
	}
	for i := 6; len(want) < sqlWriterBudget/sqlWriterTail; i-- {
		want = append(want, fmt.Sprintf("035.sqlwriter/SqlWriterLogger%d.txt", i))
	}
	c := fakeCalls()
	c.ReadFile = func(string) ([]byte, error) { return []byte("[Logging]\r\n"), nil }
	c.OpenDir = func(string) (fs.FS, error) { return fsys, nil }
	s := &recordingSink{}
	readNamed(t, c, "035.sqlwriter", Env{Since: sourcesSince}, s)
	if fmt.Sprint(s.names) != fmt.Sprint(want) {
		t.Errorf("copied %v, want %v", s.names, want)
	}
}

// The connection is opened outside sqlTimeout: an instance that does not
// answer keeps its own reason, not the msdb lock, and a slow login does not
// cost the queries their time.
func TestHarmE_ConnectOutsideSQLTimeout(t *testing.T) {
	defer func(d time.Duration) { sqlTimeout = d }(sqlTimeout)
	sqlTimeout = 50 * time.Millisecond
	slow := func(err error) func(context.Context) error {
		return func(context.Context) error {
			time.Sleep(3 * sqlTimeout)
			return err
		}
	}
	t.Run("connect fails", func(t *testing.T) {
		p := fakePlatform()
		p.Connect = slow(errors.New("login: no answer from SQL01"))
		m, code := manifestOf(t, runOptions(t, p))
		line := m[strings.Index(m, "Instance:"):]
		line = line[:strings.Index(line, "\n")]
		if code != 2 || line != "Instance:      did not answer (login: no answer from SQL01)" {
			t.Errorf("exit %d, %q; want the login's reason alone, not the msdb lock", code, line)
		}
	})
	t.Run("slow login", func(t *testing.T) {
		var seen Env
		p := fakePlatform(jobOutputs(&seen))
		p.Connect = slow(nil)
		facts := p.Facts
		p.Facts = func(ctx context.Context) (*InstanceFacts, error) {
			if err := ctx.Err(); err != nil {
				return nil, err // as the driver does with a deadline passed
			}
			return facts(ctx)
		}
		Run(context.Background(), runOptions(t, p))
		if seen.Facts == nil {
			t.Errorf("facts not read after a login slower than sqlTimeout")
		}
	})
}
