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

// RunServerName is not one to one: a nameless target is filed under its
// address, and the address SQL01 lands where a server calling itself SQL01 is.
// Reading the same databases, the rerun of the one covered the other by every
// measure scopeLost has, and deleted it.
func TestAnotherServerFiledUnderTheSameFolderIsKept(t *testing.T) {
	named := func(name string) *Manifest {
		m := scopeManifest("", nil, "SALESDB")
		m.Server.Name = name
		return m
	}
	cases := []struct {
		name      string
		prev, cur string
		kept      bool
	}{
		{"nameless before a named server", "", "SQL01", true},
		{"a named server before a nameless one", "SQL01", "", true},
		{"the same server", "SQL01", "SQL01", false},
		{"the same server in another case", "SQL01", "sql01", false},
		{"two nameless runs", "", "", false},
	}
	for _, c := range cases {
		for _, where := range []string{"folder", "archive only"} {
			t.Run(c.name+", "+where, func(t *testing.T) {
				aside := writeSetAside(t, named(c.prev), where == "folder", true)
				why := previousRunLost(aside, named(c.cur))
				if !c.kept {
					if why != "" {
						t.Fatalf("previousRunLost = %q, want the run it replaced deleted", why)
					}
					return
				}
				if !strings.Contains(why, "two different targets") {
					t.Fatalf("previousRunLost = %q, want it kept as another target's", why)
				}
				var out strings.Builder
				keepSuperseded(aside, why, &out)
				for _, p := range aside {
					if !strings.Contains(out.String(), p) {
						t.Errorf("the notice does not name %s: %q", p, out.String())
					}
				}
			})
		}
	}
}

// A manifest that does not record the server's name cannot show it is this
// server's run, and is kept like one that cannot be read.
func TestAPreviousRunWithoutAServerNameIsKept(t *testing.T) {
	aside := writeSetAside(t, scopeManifest("", nil, "SALESDB"), true, false)
	body := `{"config":{},"targets":{"databases":[{"name":"SALESDB"}]}}`
	if err := os.WriteFile(filepath.Join(aside[0], manifestJSONName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if why := previousRunLost(aside, scopeManifest("", nil, "SALESDB")); !strings.Contains(why, "no server name") {
		t.Errorf("previousRunLost = %q", why)
	}
}

// Codex review: the settings with a value decide which units run and how much
// they return without changing targets.databases, since queryStoreUnits drops
// the Query Store units after the targets are recorded. A rerun that narrowed
// one of them collected less.
func TestARerunWithNarrowerSettingsKeepsThePreviousRun(t *testing.T) {
	with := func(kv ...string) *Manifest {
		m := scopeManifest("", nil, "SALESDB")
		m.Config["query_store_days"] = "7"
		m.Config["query_store_top"] = "50"
		m.Config["query_store_from"] = "2026-09-20T09:00:00+02:00"
		m.Config["query_store_to"] = "2026-09-27T09:00:00+02:00"
		for i := 0; i < len(kv); i += 2 {
			m.Config[kv[i]] = kv[i+1]
		}
		return m
	}
	// An afternoon rerun of the same sliding window.
	later := []string{"query_store_from", "2026-09-20T15:00:00+02:00", "query_store_to", "2026-09-27T15:00:00+02:00"}
	cases := []struct {
		name      string
		prev, cur *Manifest
		lost      string // "" means the previous run may go
	}{
		{"same settings, later", with(), with(later...), ""},
		{"QUERY_STORE_DB_INCLUDE set", with(), with("query_store_db_include", "SALES*"), "QUERY_STORE_DB_INCLUDE=SALES*"},
		{"QUERY_STORE_DB_INCLUDE changed", with("query_store_db_include", "HR*"),
			with("query_store_db_include", "SALES*"), "QUERY_STORE_DB_INCLUDE=SALES*"},
		{"QUERY_STORE_DB_INCLUDE cleared", with("query_store_db_include", "HR*"), with(), ""},
		{"QUERY_STORE_TOP lowered", with(), with("query_store_top", "20"), "QUERY_STORE_TOP=20"},
		{"QUERY_STORE_TOP raised", with(), with("query_store_top", "200"), ""},
		{"shorter sliding window", with(), with(append(later, "query_store_days", "3")...), "Query Store window"},
		{"longer sliding window", with(), with(append(later, "query_store_days", "30")...), ""},
		{"typed window inside the previous one", with(),
			with("query_store_days", "0", "query_store_from_requested", "2026-09-26 14:00",
				"query_store_from", "2026-09-26T14:00:00+02:00", "query_store_to", "2026-09-27T15:00:00+02:00"),
			"Query Store window 2026-09-20T09:00:00+02:00"},
		{"typed window covering the previous one", with(),
			with("query_store_days", "0", "query_store_from_requested", "2026-09-01 00:00",
				"query_store_from", "2026-09-01T00:00:00+02:00", "query_store_to", "2026-09-27T15:00:00+02:00"), ""},
		{"previous window not resolved", with("query_store_from", "not resolved", "query_store_to", "not resolved"),
			with("query_store_days", "1"), ""},
		// The directory is not the corpus: what each run planned is compared
		// instead, in TestARerunThatPlannedFewerCollectorsKeepsThePreviousRun.
		{"other queries_dir", with(), with("queries_dir", "/srv/corpus"), ""},
		{"other comparison point", with("query_store_compare_at", "2026-09-25T10:00:00+02:00/2026-09-25T11:00:00+02:00"),
			with(), "comparison around 2026-09-25T10:00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			aside := writeSetAside(t, c.prev, true, true)
			why := previousRunLost(aside, c.cur)
			if (why == "") != (c.lost == "") || !strings.Contains(why, c.lost) {
				t.Errorf("previousRunLost = %q, want a reason naming %q", why, c.lost)
			}
		})
	}
}

// Codex review: the corpus was identified by queries_dir, so a rerun from the
// same directory after a collector was removed from it, or from a new binary
// embedding another corpus, compared equal and deleted a run that held more.
// What each run planned is compared instead, collector by collector and
// target by target.
func TestARerunThatPlannedFewerCollectorsKeepsThePreviousRun(t *testing.T) {
	const srv, db = "10.system/001.server.sql", "20.database/021.files.sql"
	run := func(dbs []string, units ...ResultEntry) *Manifest {
		m := scopeManifest("", nil, dbs...)
		m.Results = units
		return m
	}
	both := []string{"SALESDB", "HRDB"}
	full := []ResultEntry{{Script: srv}, {Script: db, Target: "SALESDB"}, {Script: db, Target: "HRDB"}}
	const (
		opt     = FlagDefaultTrace
		denied  = "the login cannot read the server state, which this query declares in @permissions"
		version = "needs SQL Server 16.0 or later; this instance reports 15.0.4430"
		held    = "the blocking watch cancelled 20.database/030.indexes.sql on this database"
	)
	cases := []struct {
		name      string
		prev, cur *Manifest
		lost      []string // each must appear; none means the previous run may go
		absent    []string // must not appear
	}{
		{"same collectors from another directory", run(both, full...),
			func() *Manifest { m := run(both, full...); m.Config["queries_dir"] = "/srv/corpus"; return m }(), nil, nil},
		{"an instance collector gone from the corpus", run(both, full...), run(both, full[1:]...),
			[]string{"collector " + srv}, nil},
		{"a database collector run on fewer databases", run(both, full...), run(both, full[:2]...),
			[]string{"collector " + db + " on HRDB"}, nil},
		{"a collector skipped by an opt-in the previous run had on is named by the option",
			func() *Manifest { m := scopeManifest("", []string{opt}, both...); m.Results = full; return m }(),
			func() *Manifest {
				m := run(both, full[1:]...)
				m.Skipped = []SkippedScript{{Script: srv, Reason: flagSkipReason(opt)}}
				return m
			}(), []string{flagOption(opt)}, []string{"collector " + srv, "skipped:"}},
		{"a collector that became opt-in is lost", run(both, full...),
			func() *Manifest {
				m := run(both, full[1:]...)
				m.Skipped = []SkippedScript{{Script: srv, Reason: flagSkipReason(opt)}}
				return m
			}(), []string{"collector " + srv + " (skipped: " + flagSkipReason(opt) + ")"}, nil},
		{"a collector skipped for a permission the login lost is lost", run(both, full...),
			func() *Manifest {
				m := run(both, full[1:]...)
				m.Skipped = []SkippedScript{{Script: srv, Reason: denied}}
				return m
			}(), []string{"collector " + srv + " (skipped: " + denied + ")"}, nil},
		{"a collector skipped for a permission after it failed before loses nothing",
			func() *Manifest {
				m := run(both, full[1:]...)
				m.Errors = []ErrorEntry{{Script: srv, Message: "permission denied"}}
				return m
			}(),
			func() *Manifest {
				m := run(both, full[1:]...)
				m.Skipped = []SkippedScript{{Script: srv, Reason: denied}}
				return m
			}(), nil, nil},
		{"a database collector skipped as a whole by the version is lost on every database", run(both, full...),
			func() *Manifest {
				m := run(both, full[0])
				m.Skipped = []SkippedScript{{Script: db, Reason: version}}
				return m
			}(), []string{"collector " + db + " on SALESDB, HRDB (skipped: " + version + ")"}, nil},
		{"a database collector skipped as a whole by a dropped opt-in is planned on every database",
			func() *Manifest { m := scopeManifest("", []string{opt}, both...); m.Results = full; return m }(),
			func() *Manifest {
				m := run(both, full[0])
				m.Skipped = []SkippedScript{{Script: db, Reason: flagSkipReason(opt)}}
				return m
			}(), []string{flagOption(opt)}, []string{"collector " + db, "skipped:"}},
		{"a database skipped for a collector by QUERY_STORE_DB_INCLUDE is left to the setting", run(both, full...),
			func() *Manifest {
				m := run(both, full[:2]...)
				m.Skipped = []SkippedScript{{Script: db, Target: "HRDB", Reason: skipNotInQueryStoreInclude}}
				return m
			}(), nil, nil},
		{"a collector moved out of the same profile is lost",
			func() *Manifest { m := scopeManifest("health", nil, both...); m.Results = full; return m }(),
			func() *Manifest {
				m := scopeManifest("health", nil, both...)
				m.Results = full[1:]
				m.Skipped = []SkippedScript{{Script: srv, Reason: ProfileSkipReason("health")}}
				return m
			}(), []string{"collector " + srv + " (skipped: " + ProfileSkipReason("health") + ")"}, nil},
		{"a collector outside a narrower profile is named by the profile", run(both, full...),
			func() *Manifest {
				m := scopeManifest("health", nil, both...)
				m.Results = full[1:]
				m.Skipped = []SkippedScript{{Script: srv, Reason: ProfileSkipReason("health")}}
				return m
			}(), []string{"--profile health"}, []string{"collector " + srv, "skipped:"}},
		{"a unit the blocking watch held back is lost", run(both, full...),
			func() *Manifest {
				m := run(both, full[:2]...)
				m.Skipped = []SkippedScript{{Script: db, Target: "HRDB", Reason: held}}
				return m
			}(), []string{"collector " + db + " on HRDB (skipped: " + held + ")"}, nil},
		{"a skip for a reason the comparison does not know is lost", run(both, full...),
			func() *Manifest {
				m := run(both, full[1:]...)
				m.Skipped = []SkippedScript{{Script: srv, Reason: "a reason added later"}}
				return m
			}(), []string{"collector " + srv + " (skipped: a reason added later)"}, nil},
		{"a database not read is named once", run(both, full...), run([]string{"SALESDB"}, full[:2]...),
			[]string{"database HRDB"}, []string{"collector"}},
		{"a collector that failed before", func() *Manifest {
			m := run(both, full[1:]...)
			m.Errors = []ErrorEntry{{Script: srv, Message: "timeout"}}
			return m
		}(), run(both, full[1:]...), []string{"collector " + srv}, nil},
		{"an error about the run names no collector", func() *Manifest {
			m := run(both, full...)
			m.Errors = []ErrorEntry{{Message: "session reset failed"}}
			return m
		}(), run(both, full...), nil, nil},
		{"many collectors gone", run(nil,
			ResultEntry{Script: "a"}, ResultEntry{Script: "b"}, ResultEntry{Script: "c"}, ResultEntry{Script: "d"},
			ResultEntry{Script: "e"}, ResultEntry{Script: "f"}, ResultEntry{Script: "g"}), run(nil),
			[]string{"collector a", "collector e", "2 more collectors"}, []string{"collector f"}},
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
				for _, a := range c.absent {
					if strings.Contains(why, a) {
						t.Errorf("the reason %q names %s", why, a)
					}
				}
			})
		}
	}
}
