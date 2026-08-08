package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
)

// FlagIncludeSessionText is the one @requires_flag the corpus uses today. It
// is named here rather than spelled inline because two things must agree on
// it: the gate that runs 10.system/052.session-text.sql, and the manifest
// field that discloses the result to whoever is asked to approve the transfer.
const FlagIncludeSessionText = "include_session_text"

type Options struct {
	Config *Config
	Corpus fs.FS
	Root   string
	Now    time.Time
	Keep   bool
	// Flags holds the opt-ins a script may name in @requires_flag. A flag
	// absent from the map is off, so the default is always the narrow one.
	Flags           map[string]bool
	Version, Commit string
}

// prepareRunFolder creates the run folder, clearing it and the archive beside
// it first unless keeping history. New-Item -Force in the PowerShell version
// was a no-op on an existing directory, so stale results survived a rerun
// while the warning claimed replacement.
//
// The archive goes with the folder. Leaving last run's .zip next to this run's
// results is worse than deleting it: it is named for the same server and day,
// so whoever picks it up has no way to tell it is not the run they just
// watched finish.
func prepareRunFolder(path string, keep bool) error {
	if keep {
		return os.MkdirAll(path, 0o755)
	}
	zip := path + ".zip"
	_, dirErr := os.Stat(path)
	_, zipErr := os.Stat(zip)
	if dirErr == nil || zipErr == nil {
		var existing []string
		if dirErr == nil {
			existing = append(existing, path)
		}
		if zipErr == nil {
			existing = append(existing, zip)
		}
		fmt.Fprintf(os.Stderr, "replacing the previous run of the same day: %s\n"+
			"pass --keep to write this run alongside it instead\n",
			strings.Join(existing, " and "))
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	if err := os.Remove(zip); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(path, 0o755)
}

// runFolderFor applies the collision policy: a same-day rerun replaces the
// previous one, unless --keep, in which case this run is suffixed with the
// time and both survive. The archive follows the folder, so there is one rule
// to explain rather than two.
func runFolderFor(outputDir, server string, now time.Time, keep bool) string {
	base := filepath.Join(outputDir, RunFolderName(server, now))
	if !keep {
		return base
	}
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return base
	}
	return base + "-" + now.Format("1504")
}

// outputWritable reports whether the process can actually create files in dir.
//
// It creates and removes a temporary file rather than trusting os.MkdirAll:
// MkdirAll returns nil for a directory that already exists whatever its
// permissions, so a read-only output directory passed the old check and the
// run only discovered it after connecting, querying and having nowhere to put
// the answers.
func outputWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".sql-auditor-write-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

func ExportQueries(corpus fs.FS, root, dest string) error {
	return fs.WalkDir(corpus, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepathRel(root, p)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := fs.ReadFile(corpus, p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}

// plannedScript pairs a script with the decision taken about it before
// anything ran. Skip is empty for a script that will run, and otherwise
// carries the explanation that goes into the manifest.
//
// The plan is built in one pass and then consulted twice — once to execute,
// once to write collected.session_text — so the archive's disclosure paragraph
// and the set of queries that actually ran cannot come apart.
type plannedScript struct {
	Script Script
	Skip   string
}

// skipReason applies the three gates that keep a script from running. None of
// them is an error: a degraded run is a success, so each returns an
// explanation for the manifest and the run carries on.
//
// The operator's own choice is reported first, then the server's version, then
// the login's rights — most actionable first, so a DBA reading the list starts
// with the thing they can change without asking anyone.
//
// denied must not contain "connect": an unreachable instance abandons the run
// before any script is considered, and treating it as an ordinary skip would
// emit an archive describing a server that was never reached.
func skipReason(s Script, denied map[string]bool, serverVersion []int, enabled map[string]bool) (string, bool) {
	if s.RequiresFlag != "" && !enabled[s.RequiresFlag] {
		flag := KnownFlags[s.RequiresFlag]
		if flag == "" {
			flag = s.RequiresFlag
		}
		return fmt.Sprintf("not collected by default; pass %s to include it", flag), true
	}
	// An unparseable or absent ProductVersion is not evidence that the server
	// is old. Gating on it would silently drop every version-gated collector
	// from an instance that can run them all.
	if len(serverVersion) > 0 && !VersionAtLeast(serverVersion, s.MinVersion) {
		return fmt.Sprintf("needs SQL Server %s or later; this instance reports %s",
			joinVersion(s.MinVersion), joinVersion(serverVersion)), true
	}
	for _, p := range s.Permissions {
		if denied[p] {
			return fmt.Sprintf("the login lacks %s, which this query declares in @permissions", p), true
		}
	}
	return "", false
}

func joinVersion(v []int) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = fmt.Sprint(n)
	}
	return strings.Join(parts, ".")
}

func planScripts(scripts []Script, denied map[string]bool, serverVersion []int, enabled map[string]bool) []plannedScript {
	out := make([]plannedScript, 0, len(scripts))
	for _, s := range scripts {
		p := plannedScript{Script: s}
		// A lint failure is an error rather than a skip, and Run reports it as
		// one; the gates below would only bury it under a milder explanation.
		if s.LintError == "" {
			p.Skip, _ = skipReason(s, denied, serverVersion, enabled)
		}
		out = append(out, p)
	}
	return out
}

// collectsSessionText reads the plan for the answer MANIFEST.txt needs: does
// this archive contain statement text captured from live sessions. Reading it
// from the plan rather than from the flag is the point — a corpus without such
// a collector must not disclose one because the option was passed, and a
// collector that ran must be disclosed however it came to run.
func collectsSessionText(plan []plannedScript) bool {
	for _, p := range plan {
		if p.Skip == "" && p.Script.LintError == "" && p.Script.RequiresFlag == FlagIncludeSessionText {
			return true
		}
	}
	return false
}

// Check reports what a collection would do without doing it: which queries
// were found, which databases they would be pointed at, what the login is
// allowed to read, and whether the output directory will take the results.
//
// The blast radius is the part a DBA needs before running collect on a
// production instance, so the script and database lists are printed even when
// every probe comes back clean.
func Check(ctx context.Context, o Options) (int, error) {
	scripts, err := Discover(o.Corpus, o.Root)
	if err != nil {
		return 2, err
	}
	lintFailures := 0
	fmt.Printf("Queries (%d):\n", len(scripts))
	for _, s := range scripts {
		switch {
		case s.LintError != "":
			lintFailures++
			fmt.Printf("  !! %-42s %s\n", s.Path, s.LintError)
		default:
			fmt.Printf("  %-42s %s\n", s.Path, scriptNote(s, o.Flags))
		}
	}

	writable := outputWritable(o.Config.OutputDir)
	fmt.Printf("\nOutput   : %s", o.Config.OutputDir)
	if !writable {
		fmt.Print("  !! not writable")
	}
	fmt.Println()

	db, err := Open(o.Config)
	if err != nil {
		return 1, err
	}
	defer db.Close()

	// One connection for everything below. The pool allows exactly one, so a
	// call taking the *sql.DB while this Conn is held would wait for a
	// connection that only this function can return.
	conn, err := db.Conn(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot reach the instance")
		return 1, err
	}
	defer conn.Close()

	fmt.Println("\nPermissions:")
	checks := RunPreflight(ctx, conn, Capabilities())
	for _, c := range checks {
		if c.Status == "ok" {
			fmt.Printf("  ok      %s\n", c.Name)
			continue
		}
		fmt.Printf("  %-7s %s — %s\n", c.Status, c.Name, c.Impact)
	}

	si, err := Probe(ctx, conn)
	if err == nil {
		fmt.Printf("\nServer   : %s  %s  %s\n", si.Name, si.Version, si.Edition)
	}

	// The database list is the blast radius. It comes last because it is the
	// part a reader scrolls back to, and it is printed even when it is empty —
	// an empty list is itself the finding when VIEW ANY DEFINITION is missing.
	cands, cerr := CandidateDatabases(ctx, conn)
	if cerr != nil {
		fmt.Fprintf(os.Stderr, "could not list databases: %v\n", cerr)
	} else {
		sel, serr := SelectTargets(cands, o.Config.DBInclude, o.Config.DBExclude)
		if serr != nil {
			fmt.Fprintln(os.Stderr, serr)
			return 2, nil
		}
		fmt.Printf("\nDatabases that would be collected (%d):\n", len(sel.Included))
		if len(sel.Included) == 0 {
			fmt.Println("  (none)")
		}
		for _, f := range ResolveDatabaseFolders(sel.Included) {
			fmt.Printf("  - %s -> %s/\n", f.Name, f.Folder)
		}
		if len(sel.Skipped) > 0 {
			fmt.Printf("\nDatabases skipped (%d):\n", len(sel.Skipped))
			for _, s := range sel.Skipped {
				fmt.Printf("  - %s (%s)\n", s.Name, s.Reason)
			}
		}
	}
	return PreflightExitCode(checks, lintFailures, writable), nil
}

// scriptNote annotates a discovered script with the conditions attached to it,
// so `check` shows why a collector might not produce a file.
func scriptNote(s Script, enabled map[string]bool) string {
	var notes []string
	if s.Scope == ScopeDatabase {
		notes = append(notes, "per database")
	}
	if len(s.MinVersion) > 0 {
		notes = append(notes, "SQL Server "+joinVersion(s.MinVersion)+"+")
	}
	if s.RequiresFlag != "" {
		state := "off"
		if enabled[s.RequiresFlag] {
			state = "on"
		}
		notes = append(notes, KnownFlags[s.RequiresFlag]+" ("+state+")")
	}
	return strings.Join(notes, ", ")
}

// Run executes the full pipeline. It returns an exit code rather than
// deciding the process's fate, so the CLI owns that.
func Run(ctx context.Context, o Options) (int, error) {
	m := NewManifest("sql-auditor", o.Version, o.Commit)
	m.Config = map[string]string{
		"queries_dir":          o.Config.QueriesDir,
		"output_dir":           o.Config.OutputDir,
		"db_include":           o.Config.DBInclude,
		"db_exclude":           o.Config.DBExclude,
		"include_session_text": fmt.Sprint(o.Flags[FlagIncludeSessionText]),
	}
	m.Sources = map[string]SourceInfo{}
	started := time.Now()
	exit := 0

	// finish is the only exit from this function that matters: a manifest is
	// written on every path, including the fatal ones, because a run that
	// produced nothing still has to leave a record of having been attempted.
	// With no run folder yet, it falls back to the output directory, and
	// WriteManifestWithFallback falls back again to a temp directory and
	// finally to stderr.
	finish := func(runFolder string, code int) (int, error) {
		m.Run.FinishedUTC = nowUTC()
		m.Run.DurationSec = int(time.Since(started).Seconds())
		m.Run.ExitCode = code
		dest := runFolder
		if dest == "" {
			dest = o.Config.OutputDir
			_ = os.MkdirAll(dest, 0o755)
		}
		if _, err := WriteManifestWithFallback(m, dest); err != nil {
			return code, err
		}
		return code, nil
	}

	if !outputWritable(o.Config.OutputDir) {
		m.Errors = append(m.Errors, ErrorEntry{
			Message: "output directory " + o.Config.OutputDir + " is not writable"})
		return finish("", 2)
	}

	sum, err := CorpusSHA256(o.Corpus, o.Root)
	if err != nil {
		m.Errors = append(m.Errors, ErrorEntry{Message: "reading the query corpus: " + err.Error()})
		return finish("", 1)
	}
	from := "embedded"
	if o.Config.QueriesDir != "" {
		from = "filesystem"
	}
	m.Sources["queries"] = SourceInfo{From: from, Path: o.Config.QueriesDir, SHA256: sum}

	scripts, err := Discover(o.Corpus, o.Root)
	if err != nil {
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finish("", 1)
	}

	db, err := Open(o.Config)
	if err != nil {
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finish("", 1)
	}
	defer db.Close()

	// Everything from here runs on this one connection. The pool allows one,
	// so any call handed the *sql.DB while this is held blocks until its
	// context expires — reproduced as "context deadline exceeded", which is
	// not a diagnosis anybody would arrive at from the message.
	conn, err := db.Conn(ctx)
	if err != nil {
		m.Errors = append(m.Errors, ErrorEntry{Message: "cannot reach the instance: " + err.Error()})
		return finish("", 1)
	}
	defer func() { conn.Close() }()

	// Populated before anything can return, so Coverage is never "unknown"
	// while the manifest goes on to make claims about the database list.
	m.Preflight = RunPreflight(ctx, conn, Capabilities())
	if PreflightExitCode(m.Preflight, 0, true) == 1 {
		m.Errors = append(m.Errors, ErrorEntry{
			Message: "the instance did not answer the preflight; nothing was collected"})
		return finish("", 1)
	}
	denied := DeniedCapabilities(m.Preflight)
	// connect is not a per-script gate. Reaching here means it answered.
	delete(denied, "connect")

	si, err := Probe(ctx, conn)
	if err != nil {
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finish("", 1)
	}
	m.Server = ServerBlock{Name: si.Name, Version: si.Version, Edition: si.Edition,
		UTCOffsetMinutes: si.UTCOffsetMinutes, Auth: authLabel(o.Config)}

	cands, err := CandidateDatabases(ctx, conn)
	if err != nil {
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finish("", 1)
	}
	sel, err := SelectTargets(cands, o.Config.DBInclude, o.Config.DBExclude)
	if err != nil {
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finish("", 2)
	}
	folders := ResolveDatabaseFolders(sel.Included)
	m.Targets = TargetBlock{Databases: folders, Skipped: sel.Skipped}

	plan := planScripts(scripts, denied, ParseVersion(si.Version), o.Flags)
	// The disclosure paragraph and the queries that run come from this one
	// decision. Split them and the manifest eventually describes a different
	// archive from the one beside it.
	m.Collected.SessionText = collectsSessionText(plan)

	runFolder := runFolderFor(o.Config.OutputDir, si.Name, o.Now, o.Keep)
	if err := prepareRunFolder(runFolder, o.Keep); err != nil {
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finish("", 1)
	}

	for _, p := range plan {
		s := p.Script
		if s.LintError != "" {
			m.Errors = append(m.Errors, ErrorEntry{Script: s.Path, Message: s.LintError})
			exit = 2
			continue
		}
		if p.Skip != "" {
			m.Skipped = append(m.Skipped, SkippedScript{Script: s.Path, Reason: p.Skip})
			continue
		}
		units := []DatabaseFolder{{}}
		if s.Scope == ScopeDatabase {
			units = folders
		}
		for _, u := range units {
			err := runUnit(ctx, conn, o, m, runFolder, s, u)
			if err == nil {
				continue
			}
			m.Errors = append(m.Errors, ErrorEntry{
				Script: s.Path, Target: u.Name, Message: err.Error(), SQLError: sqlErrorNumber(err),
			})
			exit = 2

			// One reconnect attempt on a dead connection. The replacement is
			// reset before the next unit uses it — the PowerShell version
			// skipped that step and quietly broke its own invariant.
			if !connAlive(ctx, conn) {
				fmt.Fprintln(os.Stderr, "connection lost; attempting one reconnect")
				conn.Close()
				fresh, cerr := db.Conn(ctx)
				if cerr != nil {
					m.Errors = append(m.Errors, ErrorEntry{Message: "reconnect failed: " + cerr.Error()})
					return finish(runFolder, 1)
				}
				conn = fresh
				if rerr := ResetSession(ctx, conn, o.Config.Database); rerr != nil {
					m.Errors = append(m.Errors, ErrorEntry{Message: "session reset after reconnect failed: " + rerr.Error()})
					return finish(runFolder, 1)
				}
			}
		}
	}

	code, ferr := finish(runFolder, exit)
	if ferr != nil {
		return code, ferr
	}
	// The archive is built after the manifest so it contains it. A failure
	// here leaves the run folder intact and readable; only the transport
	// packaging is missing, which is a partial failure, not a fatal one.
	zipPath := runFolder + ".zip"
	if err := Zip(runFolder, zipPath); err != nil {
		return 2, err
	}
	fmt.Printf("%d result(s), %d skipped, %d error(s)\n%s\n",
		len(m.Results), len(m.Skipped), len(m.Errors), zipPath)
	return code, nil
}

func runUnit(ctx context.Context, conn *sql.Conn, o Options, m *Manifest,
	runFolder string, s Script, u DatabaseFolder) error {

	if err := ResetSession(ctx, conn, o.Config.Database); err != nil {
		return err
	}
	if u.Name != "" {
		if _, err := conn.ExecContext(ctx, "USE "+quoteName(u.Name)+";"); err != nil {
			return err
		}
	}
	timeout := o.Config.QueryTimeout
	if s.TimeoutSec > 0 {
		timeout = time.Duration(s.TimeoutSec) * time.Second
	}
	qctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	rows, err := conn.QueryContext(qctx, s.SQL)
	if err != nil {
		return err
	}
	defer rows.Close()

	sets, err := ReadResultSets(rows, s.Results)
	if err != nil {
		return err
	}
	payload, warnings, err := Encode(sets)
	if err != nil {
		return err
	}
	m.Warnings = append(m.Warnings, warnings...)

	rel := ResultRelativePath(s.Dir, s.Base, u.Folder)
	full := filepath.Join(runFolder, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(full, payload, 0o644); err != nil {
		return err
	}
	scope := "instance"
	if s.Scope == ScopeDatabase {
		scope = "database"
	}
	m.Results = append(m.Results, ResultEntry{
		Script: s.Path, Scope: scope, Target: u.Name, Output: rel,
		Bytes: len(payload), DurationMS: int(time.Since(start).Milliseconds()), Status: "ok",
	})
	return nil
}

func authLabel(c *Config) string {
	if c.User != "" && !c.Integrated {
		return "sql:" + c.User
	}
	return "windows"
}

// connAlive distinguishes a dead connection from a query that merely failed.
// A missing permission or a bad column must not trigger a reconnect.
func connAlive(ctx context.Context, c *sql.Conn) bool {
	return c.PingContext(ctx) == nil
}

// sqlErrorNumber extracts the SQL Server error number so the manifest records
// something the analysis layer can match on, rather than only prose.
func sqlErrorNumber(err error) int {
	var e mssql.Error
	if errors.As(err, &e) {
		return int(e.Number)
	}
	return 0
}
