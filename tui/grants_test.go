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

func TestCreateFreeSuffixesATakenNameAndClaimsIt(t *testing.T) {
	dir := t.TempDir()
	first, err := createFree(dir, "grants-SRV-2026-08-13.sql")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if filepath.Base(first.Name()) != "grants-SRV-2026-08-13.sql" {
		t.Fatalf("createFree in an empty directory = %q", first.Name())
	}
	// The name is taken by the act of asking for it, not by a later write:
	// between a stat that found it free and a WriteFile there was a window, and
	// two wizards pointed at one output directory a second apart is an ordinary
	// afternoon. Nothing is written here on purpose — the previous version of
	// this test had to write the file to make the second call move on.
	second, err := createFree(dir, "grants-SRV-2026-08-13.sql")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	// Never overwriting: the file already there may be the one a DBA is
	// halfway through reviewing, and the wizard has no way to know.
	if second.Name() == first.Name() {
		t.Fatalf("createFree returned the taken name %q", second.Name())
	}
	if filepath.Base(second.Name()) != "grants-SRV-2026-08-13-2.sql" {
		t.Errorf("createFree = %q, want the suffix before the extension", second.Name())
	}
	// And a file that was there before this process started is respected too.
	third := filepath.Join(dir, "grants-SRV-2026-08-13-3.sql")
	if err := os.WriteFile(third, []byte("-- a DBA is reading this"), 0o600); err != nil {
		t.Fatal(err)
	}
	fourth, err := createFree(dir, "grants-SRV-2026-08-13.sql")
	if err != nil {
		t.Fatal(err)
	}
	defer fourth.Close()
	if filepath.Base(fourth.Name()) != "grants-SRV-2026-08-13-4.sql" {
		t.Errorf("createFree = %q, want it to step over the file already on disk", fourth.Name())
	}
	body, rerr := os.ReadFile(third)
	if rerr != nil || string(body) != "-- a DBA is reading this" {
		t.Errorf("the existing file was touched: %q, %v", body, rerr)
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

// A probe that did not complete without folding in a cause: %w on a nil error
// prints "%!w(<nil>)", and that string was what the operator got as the reason
// they could not have their grant script.
func TestGrantsRefusalReadsAsASentenceWhenNoCauseWasFolded(t *testing.T) {
	_, err := writeGrants(collect.VerifyResult{Probed: false}, t.TempDir(), "0.19.0", time.Now())
	if err == nil {
		t.Fatal("an unprobed server produced a grant script")
	}
	if strings.Contains(err.Error(), "%!w") {
		t.Errorf("the refusal is a formatting artefact: %q", err)
	}
	if !strings.Contains(err.Error(), "probe") {
		t.Errorf("the refusal does not say what failed: %q", err)
	}
}

// hang is asked for a label with nothing after it. No caller does that today;
// what makes it worth a line is where it would happen — a panic in the renderer
// means the frame is never painted and the terminal stays in raw mode.
func TestHangSurvivesAnEmptyBody(t *testing.T) {
	got := hang("page verify", 20, "", 80)
	if len(got) != 1 || !strings.HasPrefix(got[0], "page verify") {
		t.Errorf("hang with no body = %#v, want the label alone", got)
	}
}
