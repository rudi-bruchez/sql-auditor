package tui

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var flushDay = time.Date(2026, 8, 13, 14, 5, 0, 0, time.UTC)

// captureStderr runs fn with os.Stderr replaced by a pipe and returns what was
// written. flushProgress writes there by name — its signature takes no writer,
// because its caller is an exit path that has nothing left to inject — so this
// is the only way to assert on the one branch that matters most.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prior := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	os.Stderr = prior
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFlushProgressWritesNothingWhenTheRunSaidNothing(t *testing.T) {
	dir := t.TempDir()
	path, err := flushProgress(&bytes.Buffer{}, dir, flushDay)
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Errorf("flushProgress returned %q for an empty buffer", path)
	}
	// An empty log beside the archive would only raise the question of what
	// went wrong on a run where nothing did.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the output directory holds %d files, want none", len(entries))
	}
}

func TestFlushProgressWritesTheDatedLogFile(t *testing.T) {
	dir := t.TempDir()
	const line = "connection lost; attempting one reconnect\n"
	path, err := flushProgress(bytes.NewBufferString(line), dir, flushDay)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "sql-auditor-2026-08-13.log" {
		t.Errorf("flushProgress wrote %q, want sql-auditor-2026-08-13.log", filepath.Base(path))
	}
	if !filepath.IsAbs(path) {
		// The path goes on the final screen, and a double-clicked binary can
		// have any working directory at all.
		t.Errorf("flushProgress returned the relative path %q", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != line {
		t.Errorf("the log holds %q, want the buffer verbatim", body)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", info.Mode().Perm())
		}
	}
}

func TestFlushProgressNeverOverwritesAnEarlierLog(t *testing.T) {
	dir := t.TempDir()
	first, err := flushProgress(bytes.NewBufferString("first run\n"), dir, flushDay)
	if err != nil {
		t.Fatal(err)
	}
	second, err := flushProgress(bytes.NewBufferString("second run\n"), dir, flushDay)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatalf("the second run overwrote %q", first)
	}
	if filepath.Base(second) != "sql-auditor-2026-08-13-2.log" {
		t.Errorf("second log = %q, want the -2 suffix before the extension", filepath.Base(second))
	}
	body, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "first run\n" {
		t.Errorf("the first log now holds %q", body)
	}
}

// The branch this whole file exists for. lastResort dumps the entire manifest
// into this buffer when no filesystem will take it, so the moment the
// destination refuses the write is the worst possible moment to drop the
// content on the floor.
func TestFlushProgressSpillsToStderrWhenTheDirectoryRefusesTheWrite(t *testing.T) {
	// A file where a directory is expected: portable, unlike chmod, which does
	// nothing on Windows.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	const manifest = `{"run":{"exit_code":2},"errors":["nowhere to write"]}`
	var path string
	var err error
	out := captureStderr(t, func() {
		path, err = flushProgress(bytes.NewBufferString(manifest), blocked, flushDay)
	})
	if err == nil {
		t.Fatal("flushProgress succeeded against a file used as a directory")
	}
	if path != "" {
		t.Errorf("flushProgress returned the path %q on failure", path)
	}
	if !strings.Contains(out, manifest) {
		t.Errorf("stderr holds %q, want the buffer's content — it is the only copy left", out)
	}
}
