package collect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A run's final code and whether the run it replaced may be deleted are one
// decision. Measured on 0.23.0: a same-day rerun stopped four seconds in exited
// 0 and deleted the morning's complete archive, leaving a partial one in its
// place.
func TestSettleRun(t *testing.T) {
	cases := []struct {
		name      string
		exit      int
		cancelled bool
		code      int
		discard   bool
	}{
		{"a complete run", 0, false, 0, true},
		{"a stopped run", 0, true, 2, false},
		{"a run with a failed collector", 2, false, 2, false},
		{"a stopped run that had already failed a collector", 2, true, 2, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, discard := settleRun(c.exit, c.cancelled)
			if code != c.code || discard != c.discard {
				t.Errorf("settleRun(%d, %v) = (%d, %v), want (%d, %v)",
					c.exit, c.cancelled, code, discard, c.code, c.discard)
			}
		})
	}
}

// What a partial run keeps must still be there, and the operator must be told
// where.
func TestAPartialRerunKeepsTheRunItReplaced(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "SRV01-2026-08-08")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(run+".zip", []byte("PK complete"), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 8, 15, 4, 5, 0, time.UTC)
	aside, err := prepareRunFolder(run, false, at, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if _, discard := settleRun(0, true); discard {
		discardSuperseded(aside)
	} else {
		keepSuperseded(aside, &out)
	}
	for _, p := range aside {
		if _, serr := os.Stat(p); serr != nil {
			t.Errorf("%s was deleted after a stopped run: %v", p, serr)
		}
		if !strings.Contains(out.String(), p) {
			t.Errorf("the notice does not name %s: %q", p, out.String())
		}
	}
}
