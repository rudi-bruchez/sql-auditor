package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlauditor "github.com/rudi-bruchez/sql-auditor"
)

// A same-day rerun without --keep finds the previous run's archive at exactly
// the name the wizard derives for this one, and it stays there until
// prepareRunFolder moves it, which a run that fails earlier never reaches. The
// final screen used to stat that name after Run and present whatever it found
// under "Send this file", so an unreachable instance ended on yesterday
// morning's archive offered as today's.
//
// The failure here is real and offline: invalid.invalid never resolves, so Run
// fails at the connection, before the run folder, over the real driver.
func TestAFailedRunDoesNotOfferThePreviousRunsArchive(t *testing.T) {
	o := baseOptions()
	o.Config.OutputDir = t.TempDir()
	o.Corpus, o.Root = sqlauditor.Queries, "queries"
	o.Now = time.Now()
	s := State{Step: StepCollecting, Server: "invalid.invalid", User: "auditor", Flags: map[string]bool{}}
	s.Verify.Probed = true
	s.Verify.Server.Name = "SQL01"

	previous := runFolderFor(s, applyState(s, o)) + ".zip"
	if err := os.WriteFile(previous, []byte("the morning's archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	written := time.Date(2026, 10, 4, 9, 12, 0, 0, time.Local)
	if err := os.Chtimes(previous, written, written); err != nil {
		t.Fatal(err)
	}

	r := &runner{opts: o, events: make(chan event, 64), done: make(chan struct{})}
	defer close(r.done)
	r.collect(context.Background(), s)
	close(r.events)

	end := s
	var done *collectDoneEvent
	for e := range r.events {
		end = e.apply(end)
		if d, ok := e.(collectDoneEvent); ok {
			done = &d
		}
	}
	if done == nil {
		t.Fatal("the collection reported no end")
	}
	if done.err == nil {
		t.Fatal("Run succeeded against invalid.invalid; the test is not exercising a failure")
	}
	if end.ZipPath != "" {
		t.Errorf("ZipPath = %q, want empty: this run wrote no archive", end.ZipPath)
	}

	lines := Render(end, testWidth, 0)
	text := strings.Join(lines, "\n")
	if strings.Contains(text, "Send this file") || strings.Contains(text, "This archive is partial") {
		t.Errorf("the final screen offers an archive this run did not write:\n%s", text)
	}
	contains(t, lines, "No archive was written by this run.")
	// The earlier archive is named for what it is, with the time it was
	// written, so the operator can tell it from this run's.
	contains(t, lines, "from an earlier run, written 09:12 on 2026-10-04")
}

// The three verdicts archiveOf can reach, without a server. A file written
// during the run, with Run failing, is this run's unfinished archive (a Zip
// that failed half-way): it is neither offered nor called an earlier run's.
func TestArchiveOfTrustsTheRunsVerdictOverTheFile(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "SQL01-2026-10-04")
	zip := folder + ".zip"
	if err := os.WriteFile(zip, []byte("zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	failed := os.ErrDeadlineExceeded

	if p, n, prev, _ := archiveOf(folder, nil, time.Now().Add(-time.Minute)); p == "" || n != 3 || prev != "" {
		t.Errorf("a successful run: path %q, %d bytes, previous %q", p, n, prev)
	}
	if p, _, prev, _ := archiveOf(folder, failed, time.Now().Add(time.Minute)); p != "" || prev == "" {
		t.Errorf("a failed run, older file: path %q, previous %q; want only the previous", p, prev)
	}
	if p, _, prev, _ := archiveOf(folder, failed, time.Now().Add(-time.Minute)); p != "" || prev != "" {
		t.Errorf("a failed run, file written during it: path %q, previous %q; want neither", p, prev)
	}
}
