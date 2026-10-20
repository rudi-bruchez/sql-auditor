package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// The guards only Windows can run, named TestWin* for the windows-latest job
// of ci.yml. None needs an administrator or SQL Server.

// A file under the test's temporary directory is on a local fixed drive; the
// same machine's C: reached as an administrative share is a network path.
func TestWinLocalFixed(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "log.txt")
	if err := os.WriteFile(name, []byte("log"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	_, why := localFixed(syscall.Handle(f.Fd()))
	f.Close()
	if why != "" {
		t.Errorf("localFixed refused %s: %s", name, why)
	}
	if r, size, _, why, err := openFile(name); err != nil || why != "" || size != 3 {
		t.Errorf("openFile(%s): size %d, reason %q, %v; want it opened", name, size, why, err)
	} else {
		r.Close()
	}
	if _, _, _, why, err := openFile(dir); err != nil || why != "a directory" {
		t.Errorf("openFile(%s): reason %q, %v; want a directory", dir, why, err)
	}
	if _, ok, why := dirCheck(dir); !ok {
		t.Errorf("dirCheck refused %s: %s", dir, why)
	}

	if _, ok, _ := dirCheck(`\\localhost\C$\Windows`); ok {
		t.Errorf("dirCheck accepted an administrative share")
	}

	share := `\\localhost\C$\Windows\System32\drivers\etc\hosts`
	r, _, _, why, err := openFile(share)
	switch {
	case err != nil:
		t.Fatalf("openFile(%s): %v", share, err)
	case strings.HasPrefix(why, "not opened"):
		t.Skipf("%s cannot be opened on this runner (%s): the share case is not tested", share, why)
	case why == "":
		r.Close()
		t.Errorf("openFile accepted %s, a network path", share)
	case !strings.Contains(why, "network path"):
		t.Errorf("openFile(%s): reason %q, want a network path", share, why)
	}
}

// A symbolic link and a hard link are refused by the handle, whatever they
// point to.
func TestWinOpenFileRefusesLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "log.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symbolic link on this runner (%v): the link case is not tested", err)
	}
	if r, _, _, why, err := openFile(link); err != nil || why != "link, not followed" {
		if r != nil {
			r.Close()
		}
		t.Errorf("openFile(link): reason %q, %v; want link, not followed", why, err)
	}
	hard := filepath.Join(dir, "hard.txt")
	if err := os.Link(target, hard); err != nil {
		t.Fatal(err)
	}
	if r, _, _, why, err := openFile(hard); err != nil || why != "hard link, not read" {
		if r != nil {
			r.Close()
		}
		t.Errorf("openFile(hard link): reason %q, %v; want hard link, not read", why, err)
	}
}

// junction makes link a directory junction to target, as any user can.
func junction(t *testing.T, link, target string) {
	t.Helper()
	if out, err := exec.Command(filepath.Join(systemDir(), "cmd.exe"), "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v: %s", err, out)
	}
}

// dirCheck accepts a plain directory and refuses one reached through a
// symbolic link or a directory junction.
func TestWinDirCheckRefusesLinkComponents(t *testing.T) {
	base := t.TempDir()
	private := filepath.Join(base, "private")
	if err := os.MkdirAll(filepath.Join(private, "Logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if final, ok, why := dirCheck(filepath.Join(private, "Logs")); !ok || final == "" {
		t.Errorf("dirCheck refused a plain directory: %q, %s", final, why)
	}
	junction(t, filepath.Join(base, "junction"), private)
	if _, ok, why := dirCheck(filepath.Join(base, "junction", "Logs")); ok || !strings.Contains(why, "junction, not followed") {
		t.Errorf("dirCheck through a junction: ok %v, %q; want junction, not followed", ok, why)
	}
	if err := os.Symlink(private, filepath.Join(base, "symlink")); err != nil {
		t.Fatal(err)
	}
	if _, ok, why := dirCheck(filepath.Join(base, "symlink", "Logs")); ok || !strings.Contains(why, "symbolic link, not followed") {
		t.Errorf("dirCheck through a symbolic link: ok %v, %q; want symbolic link, not followed", ok, why)
	}
}

// A directory checked, then moved away and replaced by a junction to another
// directory holding a file of the same name: the file opened is refused.
func TestWinOpenFileMovedUnderJunction(t *testing.T) {
	base := t.TempDir()
	logs, private := filepath.Join(base, "Logs"), filepath.Join(base, "private")
	for _, d := range []string{logs, private} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "out.txt"), []byte(d), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	final, ok, why := dirCheck(logs)
	if !ok {
		t.Fatalf("dirCheck refused %s: %s", logs, why)
	}
	if f, _, why, err := openIn(openFile, filepath.Join(logs, "out.txt"), final+`\out.txt`); err != nil || why != "" {
		t.Fatalf("before the swap: reason %q, %v; want it opened", why, err)
	} else {
		f.Close()
	}
	if err := os.Rename(logs, logs+".old"); err != nil {
		t.Fatal(err)
	}
	junction(t, logs, private)
	if f, _, why, err := openIn(openFile, filepath.Join(logs, "out.txt"), final+`\out.txt`); err != nil || why != "moved under a link after the check" {
		if f != nil {
			f.Close()
		}
		t.Errorf("after the swap: reason %q, %v; want moved under a link after the check", why, err)
	}
}

// Every DLL the host loads itself comes from the system directory, which is
// System32 under the Windows directory.
func TestWinSystemDLL(t *testing.T) {
	want := filepath.Join(os.Getenv("SystemRoot"), "System32")
	if !strings.EqualFold(systemDir(), want) {
		t.Errorf("systemDir() = %s, want %s", systemDir(), want)
	}
	for _, d := range []*syscall.LazyDLL{advapi32, fltlib, powrprof, wevtapi, systemDLL("kernel32.dll")} {
		if !strings.EqualFold(filepath.Dir(d.Name), systemDir()) {
			t.Errorf("%s is not loaded from %s", d.Name, systemDir())
		}
		if err := d.Load(); err != nil {
			t.Errorf("load %s: %v", d.Name, err)
		}
	}
}

// WindowsPlatform restricts the loader to System32, for the DLLs the host
// loads and for their imports. There is no getter for the default DLL
// directories, so this proves it by its effect: once WindowsPlatform has run,
// a DLL placed beside the test executable is not found by its bare name,
// which the default search order finds there first. It cannot prove the order
// in main, that WindowsPlatform runs before the host's first load: that is by
// reading (main builds Options, WindowsPlatform included, before Run).
func TestWinDLLSearchRestricted(t *testing.T) {
	WindowsPlatform(nil)
	if dllSearchErr != nil {
		t.Fatal(dllSearchErr)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("sqlauditor-probe-%d.dll", os.Getpid())
	probe := filepath.Join(filepath.Dir(exe), name)
	b, err := os.ReadFile(filepath.Join(systemDir(), "version.dll"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(probe, b, 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(probe)
	h, err := syscall.LoadLibrary(name)
	if err == nil {
		syscall.FreeLibrary(h)
		t.Fatalf("%s was loaded from beside the executable", name)
	}
	if err != syscall.Errno(126) { // ERROR_MOD_NOT_FOUND
		t.Errorf("LoadLibrary(%s): %v, want the module not found", name, err)
	}
}

// checkElevated agrees with whoami: elevated when the token's integrity level
// is high (S-1-16-12288) or system (S-1-16-16384).
func TestWinCheckElevated(t *testing.T) {
	out, err := exec.Command(filepath.Join(systemDir(), "whoami.exe"), "/groups").Output()
	if err != nil {
		t.Fatal(err)
	}
	elevated := strings.Contains(string(out), "S-1-16-12288") || strings.Contains(string(out), "S-1-16-16384")
	if err := checkElevated(); (err == nil) != elevated {
		t.Errorf("checkElevated() = %v, whoami says elevated %v", err, elevated)
	}
}

// The preamble runs in Windows PowerShell 5.1: autoload off, the two modules
// imported by path, and what the scripts need (Get-CimInstance, New-Object,
// ConvertTo-Json) still there.
func TestWinPowerShellPreamble(t *testing.T) {
	out, err := powerShell(context.Background(), scriptHead+`Get-CimInstance Win32_OperatingSystem | ForEach-Object { [ordered]@{ Caption = $_.Caption; PS = $PSVersionTable.PSVersion.ToString() } } | ConvertTo-Json`)
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ Caption, PS string }
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if v.Caption == "" || !strings.HasPrefix(v.PS, "5.1") {
		t.Errorf("got %+v, want a caption from Windows PowerShell 5.1", v)
	}
}

// A module exporting Get-CimInstance, first in the PSModulePath the host is
// started with, does not run: the scripts' Get-CimInstance is CimCmdlets'.
// psEnv narrows PSModulePath and psPreamble turns autoload off and imports
// CimCmdlets by path, so this fails only when both guards are gone.
func TestWinPowerShellPlantedModule(t *testing.T) {
	planted := t.TempDir()
	witness := filepath.Join(planted, "ran.txt")
	mod := filepath.Join(planted, "Planted")
	if err := os.Mkdir(mod, 0o700); err != nil {
		t.Fatal(err)
	}
	psm := "function Get-CimInstance { Set-Content -LiteralPath '" + witness + "' -Value ran }\r\nExport-ModuleMember -Function Get-CimInstance\r\n"
	if err := os.WriteFile(filepath.Join(mod, "Planted.psm1"), []byte(psm), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PSModulePath", planted+";"+os.Getenv("PSModulePath"))
	out, err := powerShell(context.Background(), scriptHead+`[ordered]@{ Caption = (Get-CimInstance Win32_OperatingSystem).Caption; Source = (Get-Command Get-CimInstance).Source } | ConvertTo-Json`)
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ Caption, Source string }
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Stat(witness); err == nil {
		t.Errorf("the planted Get-CimInstance ran")
	}
	if v.Caption == "" || v.Source != "CimCmdlets" {
		t.Errorf("got %+v, want a caption from CimCmdlets' Get-CimInstance", v)
	}
}

// The archive's DACL as Windows applied it: protected, and full access to
// SYSTEM, the Administrators and the user, no one else.
func TestWinArchiveDACL(t *testing.T) {
	a, err := CreateArchive(t.TempDir(), "dacl")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	p, err := syscall.UTF16PtrFromString(a.Path())
	if err != nil {
		t.Fatal(err)
	}
	const seFileObject, daclInformation = 1, 4
	var sd uintptr
	if r, _, _ := advapi32.NewProc("GetNamedSecurityInfoW").Call(uintptr(unsafe.Pointer(p)), seFileObject, daclInformation, 0, 0, 0, 0, uintptr(unsafe.Pointer(&sd))); r != 0 {
		t.Fatalf("GetNamedSecurityInfoW: %v", syscall.Errno(r))
	}
	defer procLocalFree.Call(sd)
	var str *uint16
	if r, _, err := advapi32.NewProc("ConvertSecurityDescriptorToStringSecurityDescriptorW").Call(sd, 1, daclInformation, uintptr(unsafe.Pointer(&str)), 0); r == 0 {
		t.Fatalf("ConvertSecurityDescriptorToStringSecurityDescriptorW: %v", err)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(str)))
	sddl := utf16PtrToString(str)

	tok, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	user, err := tu.User.Sid.String()
	if err != nil {
		t.Fatal(err)
	}
	head, aces, _ := strings.Cut(sddl, "(")
	var trustees []string
	for _, ace := range strings.Split(strings.TrimSuffix("("+aces, ")"), ")") {
		f := strings.Split(strings.TrimPrefix(ace, "("), ";")
		if len(f) != 6 || f[0] != "A" || f[2] != "FA" {
			t.Errorf("ACE %q in %s, want an allow of full access", ace, sddl)
			continue
		}
		trustees = append(trustees, f[5])
	}
	slices.Sort(trustees)
	want := []string{"BA", "SY", user}
	slices.Sort(want)
	if !strings.HasPrefix(head, "D:P") || !slices.Equal(trustees, want) {
		t.Errorf("DACL %s, want protected with full access to SY, BA and %s only", sddl, user)
	}
}

// EvtNext's one-second wait keeps its place: a sparse query read through
// evtQuery, and with a 1 ms wait retried on ERROR_TIMEOUT, finds as many
// events as one waiting without a limit, and a call that times out returns
// none.
func TestWinEvtNextTimeoutKeepsPosition(t *testing.T) {
	const xpath = "*[System[EventID=6005]]"
	viaQuery := 0
	if err := evtQuery(context.Background(), "System", xpath, false, func([]byte, string) bool { viaQuery++; return true }); err != nil {
		t.Fatal(err)
	}
	count := func(timeout uintptr) int {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		c, _ := syscall.UTF16PtrFromString("System")
		q, _ := syscall.UTF16PtrFromString(xpath)
		rs, _, e := procEvtQuery.Call(0, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(q)), evtQueryChannelPath|evtQueryReverseDirection)
		if rs == 0 {
			t.Fatalf("EvtQuery: %v", e)
		}
		defer evtClose(rs)
		var events [100]uintptr
		total := 0
		for {
			var n uint32
			r, _, e := procEvtNext.Call(rs, uintptr(len(events)), uintptr(unsafe.Pointer(&events[0])), timeout, 0, uintptr(unsafe.Pointer(&n)))
			if r == 0 {
				switch e {
				case errorNoMoreItems:
					return total
				case errorTimeout:
					if n != 0 {
						t.Errorf("EvtNext timed out with %d events returned", n)
					}
					continue
				}
				t.Fatalf("EvtNext: %v", e)
			}
			for _, ev := range events[:n] {
				evtClose(ev)
			}
			total += int(n)
		}
	}
	const infinite = 0xFFFFFFFF
	direct, short := count(infinite), count(1)
	t.Logf("%s: %d events", xpath, direct)
	if viaQuery != direct || short != direct {
		t.Errorf("evtQuery found %d, a 1 ms wait %d, an unlimited wait %d; want the same", viaQuery, short, direct)
	}
}
