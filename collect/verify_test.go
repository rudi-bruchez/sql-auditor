package collect

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Verify's contract is that its result is usable whatever failed, so the
// finding that matters here — the output directory will not take the archive —
// survives a run that never reached the instance. An operator told only
// "cannot reach the instance" would fix the network and meet the second
// refusal on the next attempt.
//
// The elapsed-time assertion is part of the test, not decoration. This must
// stay a unit test: "invalid.invalid" is a TLD RFC 2606 reserves precisely so
// that it never resolves, and SQL_CONNECT_TIMEOUT_SEC is one second, so a
// version of Verify that waited on the network would show up here rather than
// as a slow suite nobody attributes to this change.
func TestVerifyReturnsAUsableResultWhenTheOutputDirectoryIsUnwritable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	v, err := Verify(context.Background(), Options{
		Config: &Config{Server: "invalid.invalid", OutputDir: file,
			ConnectTimeout: time.Second, QueryTimeout: 5 * time.Second},
		Corpus: checkCorpus, Root: "queries",
	})
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Errorf("Verify took %v; it must fail on name resolution, not wait on a socket", elapsed)
	}
	if err == nil {
		t.Fatal("Verify returned no error for an instance that cannot be reached")
	}
	if v.OutputWritable {
		t.Error("OutputWritable = true for a path that is a file, so the archive would have nowhere to go")
	}
	if v.ConnErr == nil {
		t.Error("ConnErr is nil, so the caller cannot tell an unreachable instance from an unusable address")
	}
	// Usable means the work done before the failure is still there.
	if len(v.Scripts) != len(checkCorpus) {
		t.Errorf("Scripts = %d, want %d: the corpus was read before the connection was attempted",
			len(v.Scripts), len(checkCorpus))
	}
	if v.LintFailures != 1 {
		t.Errorf("LintFailures = %d, want 1", v.LintFailures)
	}
}

// A connection that never happened is not a clean bill of health. Probed false
// is the flag every screen has to consult, and the fields the plan depends on
// must stay at zero rather than describe a plan built from an unknown version.
func TestVerifyMarksAFailedProbeAsNotProbed(t *testing.T) {
	v, _ := Verify(context.Background(), Options{
		Config: &Config{Server: "invalid.invalid", OutputDir: t.TempDir(),
			ConnectTimeout: time.Second, QueryTimeout: 5 * time.Second},
		Corpus: checkCorpus, Root: "queries",
	})
	if v.Probed {
		t.Error("Probed = true although no connection was ever made")
	}
	if v.Collectors != 0 {
		t.Errorf("Collectors = %d, want 0: the plan needs the server version, which is unknown here", v.Collectors)
	}
	if len(v.Skipped) != 0 {
		t.Errorf("Skipped = %v, want none: an unprobed instance has no skip list", v.Skipped)
	}
	if len(v.Checks) != 0 {
		t.Errorf("Checks = %v, want none: the preflight never ran", v.Checks)
	}
}

// Collectors counts scripts that would run at least once. Neither a gated
// script nor a broken one runs, and a script pointed at twelve databases is
// still one collector — the screen says how much of the corpus applies to this
// instance, not how many queries will be issued.
func TestCountCollectorsExcludesGatedAndSkippedScripts(t *testing.T) {
	plan := []plannedScript{
		{Script: Script{Path: "10.system/010.properties.sql"}},
		{Script: Script{Path: "10.system/052.session-text.sql"},
			Skip: "--include-session-text was not passed"},
		{Script: Script{Path: "20.databases/020.files.sql", Scope: ScopeDatabase}},
		{Script: Script{Path: "20.databases/030.broken.sql",
			LintError: "missing the @resultsets directive"}},
	}
	if got := countCollectors(plan); got != 2 {
		t.Errorf("countCollectors = %d, want 2", got)
	}
}
