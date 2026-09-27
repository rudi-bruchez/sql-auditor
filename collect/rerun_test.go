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
		keepSuperseded(aside, "", &out)
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

// scopeManifest is a manifest as runConfig and the target list would leave
// it: every opt-in written, on or off.
func scopeManifest(profile string, on []string, dbs ...string) *Manifest {
	m := NewManifest("sql-auditor", "test", "")
	o := Options{Config: &Config{}, Flags: map[string]bool{}}
	for _, f := range on {
		o.Flags[f] = true
	}
	m.Config = runConfig(o)
	m.Profile.Name = profile
	for _, d := range dbs {
		m.Targets.Databases = append(m.Targets.Databases, DatabaseFolder{Name: d, Folder: d})
	}
	return m
}

// writeSetAside leaves what prepareRunFolder would have set aside for m: the
// folder with its _run.json and, when zipped, the archive of it.
func writeSetAside(t *testing.T, m *Manifest, folder, archive bool) []string {
	t.Helper()
	dir := t.TempDir()
	run := filepath.Join(dir, "SQL01-2026-09-27")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := m.marshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, manifestJSONName), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Zip(run, run+".zip"); err != nil {
		t.Fatal(err)
	}
	aside, err := prepareRunFolder(run, false, time.Date(2026, 9, 27, 15, 4, 5, 0, time.UTC), &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	var keep []string
	for _, p := range aside {
		isZip := strings.HasSuffix(p, ".zip")
		switch {
		case isZip && !archive, !isZip && !folder:
			if err := os.RemoveAll(p); err != nil {
				t.Fatal(err)
			}
		default:
			keep = append(keep, p)
		}
	}
	return keep
}

// Harm review, finding 8: a plain afternoon collect exited 0 and deleted the
// morning's collect --all. A complete run deletes the run it replaced only
// when it collected at least what that run did.
func TestACompleteRerunKeepsAWiderRun(t *testing.T) {
	all := make([]string, 0, len(KnownFlags))
	for f := range KnownFlags {
		all = append(all, f)
	}
	cases := []struct {
		name      string
		prev, cur *Manifest
		lost      []string // each must appear in the reason; none means discard
	}{
		{"same scope", scopeManifest("", nil, "SALESDB"), scopeManifest("", nil, "SALESDB"), nil},
		{"wider rerun", scopeManifest("", nil, "SALESDB"),
			scopeManifest("", all, "SALESDB", "HRDB"), nil},
		{"plain after --all", scopeManifest("", all, "SALESDB"), scopeManifest("", nil, "SALESDB"),
			[]string{"--include-session-text", "--plan-cache-plans", "--include-job-step-commands"}},
		{"many databases dropped", scopeManifest("", nil, "DB1", "DB2", "DB3", "DB4", "DB5", "DB6", "DB7"),
			scopeManifest("", nil), []string{"database DB5", "2 more databases"}},
		{"one option dropped", scopeManifest("", []string{FlagIncludeSessionText, "deadlock_graphs"}, "SALESDB"),
			scopeManifest("", []string{"deadlock_graphs"}, "SALESDB"), []string{"--include-session-text"}},
		{"a database dropped", scopeManifest("", nil, "SALESDB", "HRDB"), scopeManifest("", nil, "SALESDB"),
			[]string{"database HRDB"}},
		// As many databases, not the same ones: a count would call this covered.
		{"other databases", scopeManifest("", nil, "SALESDB"), scopeManifest("", nil, "HRDB"),
			[]string{"database SALESDB"}},
		{"narrowed by a profile", scopeManifest("", nil, "SALESDB"), scopeManifest("space", nil, "SALESDB"),
			[]string{"--profile space"}},
		{"profile after the same profile", scopeManifest("space", nil, "SALESDB"),
			scopeManifest("space", nil, "SALESDB"), nil},
	}
	for _, c := range cases {
		for _, where := range []string{"folder", "archive only"} {
			t.Run(c.name+", "+where, func(t *testing.T) {
				aside := writeSetAside(t, c.prev, where == "folder", true)
				why := previousRunLost(aside, c.cur)
				if (why == "") != (len(c.lost) == 0) {
					t.Fatalf("previousRunLost = %q", why)
				}
				for _, l := range c.lost {
					if !strings.Contains(why, l) {
						t.Errorf("the reason %q does not name %s", why, l)
					}
				}
			})
		}
	}
}

// A previous run whose scope cannot be read is kept: nothing shows this run
// covers it.
func TestAnUnreadablePreviousRunIsKept(t *testing.T) {
	aside := writeSetAside(t, scopeManifest("", nil, "SALESDB"), true, false)
	if err := os.Remove(filepath.Join(aside[0], manifestJSONName)); err != nil {
		t.Fatal(err)
	}
	if why := previousRunLost(aside, scopeManifest("", nil, "SALESDB")); !strings.Contains(why, "could not be compared") {
		t.Errorf("previousRunLost = %q", why)
	}
	if why := previousRunLost(nil, scopeManifest("", nil, "SALESDB")); why != "" {
		t.Errorf("with nothing set aside, previousRunLost = %q", why)
	}
}
