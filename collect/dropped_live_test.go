package collect

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

const droppedLiveDB = "ZzDroppedDuringRun"

// dropOnObserver drops the test database the moment the first collector on it
// is done, which is the window another job's DROP DATABASE falls into: after
// the run listed the database, before it was finished with it.
type dropOnObserver struct {
	t       *testing.T
	admin   *sql.DB
	after   string
	started []string
	done    map[string]error
}

func (o *dropOnObserver) Planned(int) {}
func (o *dropOnObserver) UnitStarted(script, db string) {
	o.started = append(o.started, script+" on "+db)
}
func (o *dropOnObserver) UnitDone(script, db string, _ int64, _ time.Duration, err error) {
	o.done[script+" on "+db] = err
	if script != o.after || db != droppedLiveDB || err != nil {
		return
	}
	if _, derr := o.admin.Exec("ALTER DATABASE " + quoteName(droppedLiveDB) +
		" SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE " + quoteName(droppedLiveDB) + ";"); derr != nil {
		o.t.Errorf("dropping the database mid-run: %v", derr)
	}
}
func (o *dropOnObserver) ScriptSkipped(string, string, string) {}
func (o *dropOnObserver) Phase(string)                         {}
func (o *dropOnObserver) Finished(Verdict)                     {}

// droppedLiveLab creates the test database, dropping any left by an earlier
// run first, and drops it again when the test ends. It returns the
// administrative handle that the observer drops it through.
func droppedLiveLab(t *testing.T) (*Config, *sql.DB, string) {
	t.Helper()
	cfg := liveConfig(t)
	cfg.Database = "master"
	cfg.QueryTimeout = 30 * time.Second
	admin, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// A cleanup and not a defer: the drop below needs the handle, and
	// cleanups run in reverse order of registration.
	t.Cleanup(func() { admin.Close() })
	drop := "IF DB_ID(N'" + droppedLiveDB + "') IS NOT NULL BEGIN ALTER DATABASE " + quoteName(droppedLiveDB) +
		" SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE " + quoteName(droppedLiveDB) + "; END"
	if _, err := admin.Exec(drop); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(drop); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	return cfg, admin, drop
}

func createDroppedLiveDB(t *testing.T, admin *sql.DB) {
	t.Helper()
	if _, err := admin.Exec("CREATE DATABASE " + quoteName(droppedLiveDB) + ";"); err != nil {
		t.Fatal(err)
	}
}

const droppedFirst, droppedSecond, droppedThird = "20.databases/901.a.sql", "20.databases/902.b.sql", "20.databases/903.c.sql"

// runDroppedLive runs three database collectors over the test database alone,
// into out, and returns the exit code and the manifest the run wrote.
func runDroppedLive(t *testing.T, cfg *Config, out string, obs Observer) (int, Manifest) {
	t.Helper()
	body := "-- @scope:       database\n-- @resultsets:  root:object\n-- @timeout:     60\n" +
		contractPreamble + "SELECT DB_NAME() AS [name] OPTION (RECOMPILE, MAXDOP 1);\n"
	corpus := fstest.MapFS{}
	for _, p := range []string{droppedFirst, droppedSecond, droppedThird} {
		corpus["queries/"+p] = &fstest.MapFile{Data: []byte(body)}
	}
	run := *cfg
	run.OutputDir = out
	run.DBInclude = droppedLiveDB
	run.QueryStoreDays, run.QueryStoreTop = 7, 50
	code, err := Run(context.Background(), Options{
		Config: &run, Corpus: corpus, Root: "queries", Now: time.Now(),
		Observer: obs, Progress: io.Discard,
	})
	if err != nil {
		t.Errorf("Run = %d, %v", code, err)
	}
	// The run just made, not one an earlier run left set aside beside it.
	all, _ := filepath.Glob(filepath.Join(out, "*", manifestJSONName))
	var paths []string
	for _, p := range all {
		if !strings.Contains(p, ".superseded-") {
			paths = append(paths, p)
		}
	}
	if len(paths) != 1 {
		t.Fatalf("want one %s, found %v", manifestJSONName, paths)
	}
	b, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return code, m
}

// Seen on the lab on 4 October 2026: a database created and dropped by
// another job while a --profile space collection ran gave one "Database does
// not exist" error per collector left on it, the run exited 2, and the day's
// rerun kept the previous archive. The disappearance is now one warning and a
// skip per collector, and the run is complete.
func TestLiveADatabaseDroppedDuringTheRunIsSkippedNotFailed(t *testing.T) {
	cfg, admin, _ := droppedLiveLab(t)
	createDroppedLiveDB(t, admin)
	obs := &dropOnObserver{t: t, admin: admin, after: droppedFirst, done: map[string]error{}}
	code, m := runDroppedLive(t, cfg, t.TempDir(), obs)
	t.Logf("exit %d; started %v; skipped %v; errors %v; warnings %v",
		code, obs.started, m.Skipped, m.Errors, m.Warnings)
	if code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	if len(m.Errors) != 0 {
		t.Errorf("errors %v, want none", m.Errors)
	}
	skipped := map[string]string{}
	for _, s := range m.Skipped {
		skipped[s.Script+" on "+s.Target] = s.Reason
	}
	for _, p := range []string{droppedSecond, droppedThird} {
		if r := skipped[p+" on "+droppedLiveDB]; r != skipDroppedDuringRun {
			t.Errorf("%s on %s: skip reason %q, want %q", p, droppedLiveDB, r, skipDroppedDuringRun)
		}
		var skip *UnitSkipped
		if !errors.As(obs.done[p+" on "+droppedLiveDB], &skip) {
			t.Errorf("the screen was told %v for %s, want a skip", obs.done[p+" on "+droppedLiveDB], p)
		}
	}
	// The second collector met the missing database; the third is never
	// sent to it.
	for _, s := range obs.started {
		if strings.HasPrefix(s, droppedThird) {
			t.Errorf("%s was run on a database already found dropped", s)
		}
	}
	told := 0
	for _, w := range m.Warnings {
		if strings.Contains(w, droppedLiveDB) {
			told++
		}
	}
	if told != 1 {
		t.Errorf("%d warning(s) name %s, want 1: %v", told, droppedLiveDB, m.Warnings)
	}
	listed := false
	for _, d := range m.Targets.Databases {
		listed = listed || d.Name == droppedLiveDB
	}
	if !listed {
		t.Errorf("targets.databases %v does not list %s, whose first collector ran", m.Targets.Databases, droppedLiveDB)
	}
}

// What the rerun guard makes of a run that lost a database on the way, run
// after a run of the same day. When the earlier run read the database, its
// archive is kept, as it is when the database goes between the two runs, and
// the database is named once with the reason. When the earlier run never saw
// it, which is the lab's incident, nothing was lost and the earlier run goes.
func TestLiveTheRerunGuardAfterADatabaseDroppedDuringTheRun(t *testing.T) {
	// Asked here as well as in each case, so that without a server the test
	// is reported skipped rather than passed over three skipped cases.
	liveConfig(t)
	cases := []struct {
		name      string
		prevSawIt bool
		between   bool // dropped between the two runs rather than during the second
		kept      bool
		named     string
	}{
		{"the previous run read the database", true, false, true,
			"database " + droppedLiveDB + " (" + skipDroppedDuringRun + ")"},
		{"the previous run never saw the database", false, false, false, ""},
		// The comparison point: the same loss, one run earlier.
		{"the database was dropped between the two runs", true, true, true,
			"database " + droppedLiveDB + ", which"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, admin, drop := droppedLiveLab(t)
			out := t.TempDir()
			if c.prevSawIt {
				createDroppedLiveDB(t, admin)
			}
			if code, prev := runDroppedLive(t, cfg, out, nil); code != 0 {
				t.Fatalf("the first run exited %d: %v", code, prev.Errors)
			}
			if _, err := admin.Exec(drop); err != nil {
				t.Fatal(err)
			}
			var obs Observer
			if !c.between {
				createDroppedLiveDB(t, admin)
				obs = &dropOnObserver{t: t, admin: admin, after: droppedFirst, done: map[string]error{}}
			}
			code, m := runDroppedLive(t, cfg, out, obs)
			if code != 0 {
				t.Errorf("the rerun exited %d, want 0: %v", code, m.Errors)
			}
			aside, _ := filepath.Glob(filepath.Join(out, "*.superseded-*"))
			kept := ""
			for _, w := range m.Warnings {
				if strings.Contains(w, "the run it replaced was kept") {
					kept = w
				}
			}
			t.Logf("set aside %v; warning %q", aside, kept)
			if (len(aside) > 0) != c.kept || (kept != "") != c.kept {
				t.Fatalf("set aside %v, warning %q; want kept %v", aside, kept, c.kept)
			}
			if c.kept && (strings.Count(kept, droppedLiveDB) != 1 || !strings.Contains(kept, c.named)) {
				t.Errorf("the warning must name %q once: %q", c.named, kept)
			}
		})
	}
}

// The catalog check behind databaseDropped, on a real connection: a database
// that is there and one that is not. Without it a 911 on a database that
// still exists would be taken for a drop.
func TestLiveDatabaseExistsAsksTheCatalog(t *testing.T) {
	cfg, admin, _ := droppedLiveLab(t)
	ctx := context.Background()
	conn, err := admin.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for name, want := range map[string]bool{"master": true, droppedLiveDB: false} {
		got, err := databaseExists(ctx, conn, cfg, name)
		if err != nil || got != want {
			t.Errorf("databaseExists(%s) = %v, %v; want %v", name, got, err, want)
		}
	}
}

// The rerun guard on a real run: _run.json records the address the run was
// given, a rerun at the same address spelled otherwise replaces the run, and
// a rerun of the same server with another SQL_DATABASE keeps it. Nothing is
// created on the server; the corpus is one instance collector.
func TestLiveRunAddressReachesTheRerunGuard(t *testing.T) {
	cfg := liveConfig(t)
	body := "-- @scope:       instance\n-- @resultsets:  root:object\n-- @timeout:     60\n" +
		contractPreamble + "SELECT @@VERSION AS [version] OPTION (RECOMPILE, MAXDOP 1);\n"
	corpus := fstest.MapFS{"queries/10.system/901.a.sql": &fstest.MapFile{Data: []byte(body)}}
	out, now := t.TempDir(), time.Now()
	run := func(server, database string) Manifest {
		t.Helper()
		c := *cfg
		c.Server, c.Database, c.OutputDir = server, database, out
		c.QueryTimeout = 30 * time.Second
		c.QueryStoreDays, c.QueryStoreTop = 7, 50
		code, runErr := Run(context.Background(), Options{
			Config: &c, Corpus: corpus, Root: "queries", Now: now, Progress: io.Discard,
		})
		all, _ := filepath.Glob(filepath.Join(out, "*", manifestJSONName))
		for _, p := range all {
			if strings.Contains(p, ".superseded-") {
				continue
			}
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			var m Manifest
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatal(err)
			}
			if runErr != nil || code != 0 {
				t.Fatalf("Run(%s, %q) = %d, %v: %+v", server, database, code, runErr, m.Errors)
			}
			return m
		}
		t.Fatalf("no %s in %s", manifestJSONName, out)
		return Manifest{}
	}
	keptWarning := func(m Manifest) string {
		for _, w := range m.Warnings {
			if strings.Contains(w, "the run it replaced was kept") {
				return w
			}
		}
		return ""
	}
	first := run(cfg.Server, "master")
	t.Logf("first run: address %q, database %q, name %q", first.Server.Address, first.Server.Database, first.Server.Name)
	if first.Server.Address != strings.TrimSpace(cfg.Server) {
		t.Fatalf("_run.json records address %q, want %q", first.Server.Address, cfg.Server)
	}

	respelled := " TCP:" + strings.ToUpper(strings.TrimSpace(cfg.Server))
	second := run(respelled, "master")
	aside, _ := filepath.Glob(filepath.Join(out, "*.superseded-*"))
	t.Logf("same address as %q: set aside %v, warning %q", respelled, aside, keptWarning(second))
	if len(aside) != 0 || keptWarning(second) != "" {
		t.Fatalf("a rerun at the same address kept the run it replaced: %v, %q", aside, keptWarning(second))
	}

	third := run(cfg.Server, "tempdb")
	aside, _ = filepath.Glob(filepath.Join(out, "*.superseded-*"))
	w := keptWarning(third)
	t.Logf("another SQL_DATABASE: set aside %v, warning %q", aside, w)
	if len(aside) == 0 || !strings.Contains(w, "connected to") || !strings.Contains(w, "database tempdb") {
		t.Fatalf("a rerun with another SQL_DATABASE: set aside %v, warning %q; want it kept", aside, w)
	}
}
