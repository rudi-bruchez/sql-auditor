package collect

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"testing"
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
