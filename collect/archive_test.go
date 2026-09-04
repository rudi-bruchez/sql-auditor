package collect

import (
	"archive/zip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestZipRoundTrip(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "SRV01-2026-08-08")
	if err := os.MkdirAll(filepath.Join(run, "10.system"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(run, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("_run.json", `{"tool":{}}`)
	write("MANIFEST.txt", "readable")
	write(filepath.Join("10.system", "010.properties.json"), `{"instance":{}}`)

	dest := filepath.Join(dir, "SRV01-2026-08-08.zip")
	if err := Zip(run, dest); err != nil {
		t.Fatalf("Zip: %v", err)
	}
	r, err := zip.OpenReader(dest)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()
	got := map[string]bool{}
	for _, f := range r.File {
		got[filepath.ToSlash(f.Name)] = true
	}
	for _, want := range []string{
		"SRV01-2026-08-08/_run.json",
		"SRV01-2026-08-08/MANIFEST.txt",
		"SRV01-2026-08-08/10.system/010.properties.json",
	} {
		if !got[want] {
			t.Errorf("archive missing %s (has %v)", want, got)
		}
	}
}

// The bytes have to survive the trip, not just the names: an archive whose
// entries are empty would pass a name-only check and be useless to the analyst.
func TestZipPreservesContent(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "run")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"instance":{"name":"SRV01"}}`
	if err := os.WriteFile(filepath.Join(run, "010.properties.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "run.zip")
	if err := Zip(run, dest); err != nil {
		t.Fatalf("Zip: %v", err)
	}
	r, err := zip.OpenReader(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if len(r.File) != 1 {
		t.Fatalf("want 1 entry, got %d", len(r.File))
	}
	rc, err := r.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("content = %q, want %q", got, body)
	}
}

// Writing the archive inside the folder being archived is the natural mistake
// (dest defaults next to the run folder, and a caller may drop the ".." by
// accident). The zip must not try to add itself, which either grows without
// bound or embeds a half-written copy.
func TestZipSkipsDestinationInsideRunFolder(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "run")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "_run.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(run, "run.zip")
	if err := Zip(run, dest); err != nil {
		t.Fatalf("Zip: %v", err)
	}
	r, err := zip.OpenReader(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, f := range r.File {
		if filepath.Base(f.Name) == "run.zip" {
			t.Fatalf("archive contains itself: %s", f.Name)
		}
	}
	if len(r.File) != 1 {
		t.Errorf("want only _run.json, got %d entries", len(r.File))
	}
}

// fakeFileInfo carries a mode and nothing else, so the archiving rule can be
// checked on a platform where the test below cannot create a symlink.
type fakeFileInfo struct {
	fs.FileInfo
	mode fs.FileMode
}

func (f fakeFileInfo) Mode() fs.FileMode { return f.mode }

func TestArchivableAdmitsOnlyRegularFiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode fs.FileMode
		want bool
	}{
		{"regular", 0o644, true},
		{"symlink", fs.ModeSymlink | 0o777, false},
		{"directory", fs.ModeDir | 0o755, false},
		{"device", fs.ModeDevice, false},
		{"named pipe", fs.ModeNamedPipe, false},
		{"socket", fs.ModeSocket, false},
	} {
		if got := archivable(fakeFileInfo{mode: tc.mode}); got != tc.want {
			t.Errorf("archivable(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// WalkDir lstats, so a symlink is not skipped by anything else in the walk:
// the header would carry the link bit while os.Open follows the link, pulling
// the target's entire content into an archive whose own manifest says it holds
// nothing but metadata.
func TestZipDoesNotFollowSymlinksOutOfTheRunFolder(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "run")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(dir, "customers.csv")
	if err := os.WriteFile(secret, []byte("alice@example.com,4111111111111111"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "_run.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(run, "link.csv")); err != nil {
		// Unprivileged Windows without developer mode cannot create one.
		t.Skipf("cannot create a symlink here: %v", err)
	}
	dest := filepath.Join(dir, "run.zip")
	if err := Zip(run, dest); err != nil {
		t.Fatalf("Zip: %v", err)
	}
	r, err := zip.OpenReader(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, f := range r.File {
		if filepath.Base(f.Name) == "link.csv" {
			t.Fatalf("archive contains the symlink %s", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "alice@example.com") {
			t.Fatalf("%s carries the symlink target's content", f.Name)
		}
	}
}

// A failed archive must not be left behind looking like a finished one.
func TestZipRemovesPartialArchiveOnError(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "run.zip")
	if err := Zip(filepath.Join(dir, "does-not-exist"), dest); err == nil {
		t.Fatal("Zip of a missing folder should fail")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("failed Zip left %s behind (stat err = %v)", dest, err)
	}
}

// Two collections of the same instance on the same day used to run straight
// into each other: both rename the predecessor aside, both write into the same
// folder, and both exit 0 printing the same archive path. The operator is
// handed one archive that is two runs interleaved, described by whichever
// manifest was written last, with no error anywhere.
func TestASecondRunCannotClaimAFolderARunIsUsing(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "SQL01-2026-09-04")
	now := time.Date(2026, 9, 4, 14, 2, 0, 0, time.UTC)

	release, err := lockRun(run, now)
	if err != nil {
		t.Fatalf("the first run could not claim the folder: %v", err)
	}

	if _, err := lockRun(run, now); err == nil {
		t.Fatal("a second run claimed a folder the first one is writing")
	} else {
		// The message has to be actionable: which folder, which file to look
		// at, and what to do when nothing is in fact running.
		for _, want := range []string{run, ".lock", "interrupted"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not mention %q: %v", want, err)
			}
		}
		// And it names the run to go and look at, which is the only useful
		// thing to tell somebody who hits this.
		if !strings.Contains(err.Error(), "pid ") {
			t.Errorf("the refusal does not say which process holds it: %v", err)
		}
	}

	// Released, the name is free again — a run that finished must not block the
	// next one.
	release()
	release2, err := lockRun(run, now)
	if err != nil {
		t.Fatalf("the folder stayed locked after the run finished: %v", err)
	}
	release2()

	// A stale lock is deliberately NOT cleaned up: a process killed mid-run
	// leaves a half-written folder, and guessing the lock is stale would delete
	// the evidence of the run that died.
	if _, err := lockRun(run, now); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if _, err := os.Stat(run + ".lock"); err != nil {
		t.Errorf("the lock file is not where the message says it is: %v", err)
	}
}

// The lock sits beside the run folder, like the .zip, so it is never collected
// into the archive it is protecting.
func TestTheLockIsNotInsideTheArchive(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "SQL01-2026-09-04")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	release, err := lockRun(run, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	entries, err := os.ReadDir(run)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".lock") {
			t.Errorf("the lock is inside the run folder and would be archived: %s", e.Name())
		}
	}
}
