package collect

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Progress exists so a caller that owns the screen — the wizard — can keep the
// run's stderr chatter out of its own frame. It captures ONLY what already
// went to stderr. Everything a script may parse stays where it was: check's
// listing on stdout, and the zip path collect prints last.
//
// Nothing here needs a server: the one test that calls Check stops at the
// first socket, on "invalid.invalid" with a one-second connect timeout.

// captureStderr is captureStdout's twin. Several tests below need to prove a
// negative — that a line did NOT reach the process's stderr — which cannot be
// asserted by looking at the writer that did receive it.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	saved := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		r.Close()
		done <- string(b)
	}()
	func() {
		defer func() {
			os.Stderr = saved
			w.Close()
		}()
		f()
	}()
	return <-done
}

// Nil is the command line's case and it must keep writing where it always
// wrote. The accessor is the single place that decides, so it is the single
// place worth pinning: were it to return io.Discard on nil, every subcommand
// would go silent at once and no other test in this package would notice.
func TestProgressDefaultsToStderrWhenUnset(t *testing.T) {
	if got := (Options{}).progress(); got != os.Stderr {
		t.Errorf("progress() on an unset Options = %v, want os.Stderr", got)
	}
	var buf bytes.Buffer
	if got := (Options{Progress: &buf}).progress(); got != &buf {
		t.Errorf("progress() = %v, want the writer that was set", got)
	}
}

// The warning that a same-day run is about to be destroyed is the single most
// important line this package writes to stderr, because what follows it is a
// RemoveAll. Under a wizard it must land in the wizard's buffer and nowhere
// else — printed to the real stderr it would be painted over by the next
// repaint and the operator would never see it.
func TestProgressRoutesTheReplacementWarning(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "SRV01-2026-08-13")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(run+".zip", []byte("PK"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	var perr error
	leaked := captureStderr(t, func() {
		perr = prepareRunFolder(run, false, &buf)
	})
	if perr != nil {
		t.Fatalf("prepareRunFolder: %v", perr)
	}
	printed := buf.String()
	if !strings.Contains(printed, run) || !strings.Contains(printed, run+".zip") {
		t.Errorf("the previous run was destroyed without naming both the folder and the archive; got %q", printed)
	}
	if !strings.Contains(printed, "--keep") {
		t.Errorf("the warning %q does not say how to avoid the replacement", printed)
	}
	if leaked != "" {
		t.Errorf("stderr also received %q; the writer is a redirection, not a copy", leaked)
	}
}

// lastResort runs when no filesystem the process can reach will take the
// manifest, so the writer it prints to holds the only surviving copy of the
// run. That is precisely why the wizard is forbidden from handing it
// io.Discard, and why this call site has to honour Progress like the others.
func TestLastResortWritesToTheProgressWriter(t *testing.T) {
	var buf bytes.Buffer
	cause := errors.New("read-only file system")
	manifest := []byte(`{"run":{"exit_code":2}}`)

	var got error
	leaked := captureStderr(t, func() {
		got = lastResort(manifest, cause, &buf)
	})
	if !errors.Is(got, cause) {
		t.Errorf("lastResort returned %v, want the cause it was given", got)
	}
	printed := buf.String()
	if !strings.Contains(printed, string(manifest)) {
		t.Errorf("the manifest itself is missing from the last-resort trace; got %q", printed)
	}
	if !strings.Contains(printed, cause.Error()) {
		t.Errorf("the trace %q does not say why nothing could be written", printed)
	}
	if leaked != "" {
		t.Errorf("stderr also received %q; the writer is a redirection, not a copy", leaked)
	}
}

// The regression test of this task. Check prints its listing to stdout through
// eighteen calls, and `sql-auditor check > report.txt` is how an operator keeps
// it. Routing those to Progress would empty that file — so this replays the
// characterisation of check_test.go with a NON-NIL Progress and demands the
// same bytes on stdout. Only the four stderr lines may move.
func TestCheckStillWritesItsListingToStdout(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "output")
	var progress bytes.Buffer
	var code int
	out := captureStdout(t, func() {
		code, _ = Check(context.Background(), Options{
			Config: checkConfig(dir), Corpus: checkCorpus, Root: "queries",
			Progress: &progress,
		})
	})
	want := checkListing + "\nOutput   : " + dir + "\n"
	if out != want {
		t.Errorf("stdout mismatch under a non-nil Progress\n got: %q\nwant: %q", out, want)
	}
	if code != 1 {
		t.Errorf("exit code = %d, want 1 (the instance could not be reached)", code)
	}
	// The other half of the same statement: the line that always belonged on
	// stderr did move, so this is a redirection of stderr and not a listing
	// that happens to be duplicated.
	if !strings.Contains(progress.String(), "cannot reach the instance") {
		t.Errorf("Progress = %q, want the unreachable-instance line that used to go to stderr", progress.String())
	}
	if strings.Contains(out, "cannot reach the instance") {
		t.Errorf("the unreachable-instance line reached stdout, where a redirected listing would capture it")
	}
}

// The other stdout writer, Run's final summary and zip path, is not covered
// here and cannot be: reaching the end of Run needs a server, and no test in
// this package opens a connection. It is silenced under an Observer rather
// than redirected, so that `sql-auditor collect | tail -1` keeps reading the
// zip path off stdout. That call site is verified by review.
