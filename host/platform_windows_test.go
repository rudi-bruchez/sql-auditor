package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
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

// A symbolic link is refused by the handle, whatever it points to.
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
