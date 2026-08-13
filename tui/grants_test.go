package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/sql-auditor/collect"
)

func TestGrantFileNameIsSafeOnEveryFileSystem(t *testing.T) {
	now := time.Date(2026, 8, 13, 14, 5, 0, 0, time.UTC)
	// An instance name carries a backslash on every named instance in
	// existence, and the file is written into a directory the operator will
	// browse: it goes through the same sanitiser as the run folders.
	if got := grantFileName(`SQL01\PROD`, now); got != "grants-SQL01_PROD-2026-08-13.sql" {
		t.Errorf("grantFileName = %q, want %q", got, "grants-SQL01_PROD-2026-08-13.sql")
	}
}

func TestFreeNameSuffixesATakenName(t *testing.T) {
	dir := t.TempDir()
	first, err := freeName(dir, "grants-SRV-2026-08-13.sql")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(first) != "grants-SRV-2026-08-13.sql" {
		t.Fatalf("freeName in an empty directory = %q", first)
	}
	if err := os.WriteFile(first, []byte("--"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := freeName(dir, "grants-SRV-2026-08-13.sql")
	if err != nil {
		t.Fatal(err)
	}
	// Never overwriting: the file already there may be the one a DBA is
	// halfway through reviewing, and the wizard has no way to know.
	if second == first {
		t.Fatalf("freeName returned the taken name %q", second)
	}
	if filepath.Base(second) != "grants-SRV-2026-08-13-2.sql" {
		t.Errorf("freeName = %q, want the suffix before the extension", second)
	}
}

// verifyForGrants is a probe that succeeded and refused one capability, which
// is the only shape in which [g] has anything to write.
func verifyForGrants() collect.VerifyResult {
	return collect.VerifyResult{
		Probed: true,
		Server: collect.ServerInfo{
			Name: `SQL01\PROD`, Version: "15.0.4345.5",
			Edition: "Standard Edition", Login: "AUDIT_RO",
		},
		Checks: []collect.CapabilityCheck{
			{Name: "connect", Status: "ok"},
			{Name: "view_server_state", Status: "denied", Impact: "wait statistics not collected"},
		},
		Scripts:  []collect.Script{{Path: "10.system/010.instance.sql"}},
		NoAccess: []string{"FACTURATION"},
	}
}

func TestWriteGrantsPutsTheScriptInTheOutputDirectory(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 13, 14, 5, 0, 0, time.UTC)
	path, err := writeGrants(verifyForGrants(), dir, "0.18.0", now)
	if err != nil {
		t.Fatalf("writeGrants: %v", err)
	}
	// An absolute path, because a double-clicked binary can have any working
	// directory at all — C:\Windows\System32 included — and a relative path
	// on screen would not tell the operator where the file went.
	if !filepath.IsAbs(path) {
		t.Errorf("path %q is not absolute", path)
	}
	if got := filepath.Dir(path); got != dir {
		t.Errorf("written to %q, want %q", got, dir)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "AUDIT_RO") {
		t.Error("the script does not name the login it grants to")
	}
	if runtime.GOOS != "windows" {
		// 0o600: it names a login and the permissions it lacks, on a machine
		// that may be shared. Windows has no such mode bits to check.
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", info.Mode().Perm())
		}
	}
}

func TestWriteGrantsRefusesWithoutASuccessfulProbe(t *testing.T) {
	v := verifyForGrants()
	v.Probed = false
	// Without a probe there is no login and no version, so the script would
	// either fail on its first statement or grant to a principal nobody uses.
	if _, err := writeGrants(v, t.TempDir(), "0.18.0", time.Now()); err == nil {
		t.Fatal("writeGrants accepted an unprobed instance")
	}
}
