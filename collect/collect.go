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
	"unicode"

	mssql "github.com/microsoft/go-mssqldb"
)

// FlagIncludeSessionText is the one @requires_flag the corpus uses today. It
// is named here rather than spelled inline because two things must agree on
// it: the gate that runs 10.system/052.session-text.sql, and the manifest
// field that discloses the result to whoever is asked to approve the transfer.
const FlagIncludeSessionText = "include_session_text"

// FlagEstimateCompression gates the compression-savings estimate. It guards
// two things at once, and an earlier version of this comment got it wrong by
// naming only the first.
//
// Cost: sp_estimate_data_compression_savings copies sampled data into tempdb,
// compresses it and measures. On the objects worth asking about that is the
// most expensive thing this corpus can do, on the instance being audited.
//
// Data access: it does that by SELECTing the rows. Every other collector in
// this corpus reads catalog views and DMVs only, and MANIFEST.txt states
// plainly that no user or application table is read — which is why a
// read-only audit login normally has no SELECT anywhere and this collector
// fails with error 229 on such a login, by design. Turning the flag on is
// therefore a decision about touching client data, not only about spending
// I/O, and it needs the same deliberateness as session text.
const FlagEstimateCompression = "estimate_compression"

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
	// GrantScript, when set, is where "check" writes the T-SQL that grants
	// the permissions the probe found missing. Empty means write nothing.
	GrantScript string
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
		// --keep must never land on an occupied name. runFolderFor already
		// looks for a free one, but if it ever hands back a taken path the
		// two runs would merge into one folder: this run's manifest beside
		// the previous run's result files, describing an archive it does not
		// match. A run collected under --include-session-text merging into a
		// run collected without it puts session text inside an archive whose
		// own MANIFEST.txt says there is none. Refuse loudly instead.
		if taken, what := runNameTaken(path); taken {
			return fmt.Errorf("--keep: %s already exists; this run would be written into "+
				"the same place as an earlier one and the two would be indistinguishable", what)
		}
		return os.MkdirAll(path, 0o755)
	}
	if warning := replacingRunWarning(path); warning != "" {
		fmt.Fprintln(os.Stderr, warning)
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	if err := os.Remove(path + ".zip"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(path, 0o755)
}

// runNameTaken reports whether anything from an earlier run already occupies
// this name. The archive counts: it sits beside the folder rather than inside
// it, so a folder that was moved away while its .zip stayed still leaves a
// name that a second run would appear to have produced.
func runNameTaken(path string) (bool, string) {
	_, dirErr := os.Stat(path)
	_, zipErr := os.Stat(path + ".zip")
	switch {
	case dirErr == nil && zipErr == nil:
		return true, path + " and " + path + ".zip"
	case dirErr == nil:
		return true, path
	case zipErr == nil:
		return true, path + ".zip"
	}
	return false, ""
}

// replacingRunWarning is the notice printed before a same-day rerun destroys
// its predecessor, or "" when there is nothing to destroy. It names the
// archive as well as the folder: the .zip is what gets mailed onward, it is
// named for the same server and day, and an operator told only about the
// folder has no reason to expect it gone.
func replacingRunWarning(path string) string {
	taken, what := runNameTaken(path)
	if !taken {
		return ""
	}
	return "replacing the previous run of the same day: " + what +
		"\npass --keep to write this run alongside it instead"
}

// runFolderFor applies the collision policy: a same-day rerun replaces the
// previous one, unless --keep, in which case this run is suffixed with the
// time and both survive. The archive follows the folder, so there is one rule
// to explain rather than two.
//
// The search continues past the time suffix. Three --keep runs inside one
// minute all produce the same HHMM, and a version that suffixed once returned
// the same occupied name to each of them — the second and third runs then
// wrote their results into the first run's folder. Nothing failed: each run
// left its own manifest, describing only its own queries, next to the union of
// everybody's output files. A run collected without --include-session-text
// ended up shipping a zip containing session text under a MANIFEST.txt that
// denied it.
func runFolderFor(outputDir, server string, now time.Time, keep bool) string {
	base := filepath.Join(outputDir, RunFolderName(server, now))
	if !keep {
		return base
	}
	if taken, _ := runNameTaken(base); !taken {
		return base
	}
	withTime := base + "-" + now.Format("1504")
	candidate := withTime
	for i := 2; ; i++ {
		if taken, _ := runNameTaken(candidate); !taken {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", withTime, i)
	}
}

// outputWritable reports whether the process can actually create files in dir.
//
// It creates and removes a temporary file rather than trusting os.MkdirAll:
// MkdirAll returns nil for a directory that already exists whatever its
// permissions, so a read-only output directory passed the old check and the
// run only discovered it after connecting, querying and having nowhere to put
// the answers.
func outputWritable(dir string) bool {
	_, err := writeProbe(dir)
	return err == nil
}

// writeProbe does the work and returns the path it used, so a test can assert
// the probe was written into the directory under test. That is not a detail: a
// probe created in os.TempDir() reports on the temp directory, always succeeds
// on a normal machine, and passes every assertion about the return value while
// testing nothing about the output directory at all.
func writeProbe(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".sql-auditor-write-probe-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	f.Close()
	// Removed immediately: the run folder is archived, and a stray probe file
	// in the output directory is one more thing for a reader to explain.
	return name, os.Remove(name)
}

// deadline bounds one server round trip. SQL_CONNECT_TIMEOUT_SEC covers
// dialling and nothing after it, so without this every statement outside
// runUnit's own query ran on a bare context: a USE against a database in
// single-user mode, a preflight probe behind a lock, or a ping down a
// half-open socket would block collect indefinitely with no exit but Ctrl-C.
// The whole point of this tool is that it finishes and leaves a manifest.
func deadline(ctx context.Context, cfg *Config) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, cfg.QueryTimeout)
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
			return fmt.Sprintf("the login cannot %s, which this query declares in @permissions",
				lowerFirst(capabilityLabel(p))), true
		}
	}
	return "", false
}

// capabilityLabel turns a capability key into the English label the preflight
// carries for it. The rule is stated in preflight.go: MANIFEST.txt gets the
// Label, _run.json keeps the Name. This list goes into MANIFEST.txt, so
// "view_any_definition" — an internal key from a vocabulary its reader has
// never seen — must not appear there. An unknown key falls back to itself
// rather than to nothing.
func capabilityLabel(name string) string {
	for _, c := range Capabilities() {
		if c.Name == name {
			return c.Label
		}
	}
	return name
}

// lowerFirst downcases the first letter so a label written to stand alone
// ("Read backup history from msdb") reads correctly mid-sentence. The
// permission name in parentheses is upper-case and unaffected.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

func scriptByPath(scripts []Script, path string) Script {
	for _, s := range scripts {
		if s.Path == path {
			return s
		}
	}
	return Script{}
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
//
// It returns the scripts responsible, not just a boolean, so the caller can
// name an ungated one in a warning.
func collectsSessionText(plan []plannedScript) []string {
	var by []string
	for _, p := range plan {
		if p.Skip != "" || p.Script.LintError != "" {
			continue
		}
		if p.Script.RequiresFlag == FlagIncludeSessionText || readsSessionText(p.Script) {
			by = append(by, p.Script.Path)
		}
	}
	return by
}

// readsSessionText looks for the DMF that turns a plan handle into the
// verbatim SQL of a live batch.
//
// The directive alone is not enough. --queries-dir accepts a corpus this
// project has never seen, and a file there reading sys.dm_exec_sql_text
// without declaring @requires_flag would put application literals in the
// archive under a manifest saying session_text: false — the exact defect the
// gate exists to prevent, reintroduced by the one path that bypasses the
// embedded corpus. So the claim is made from what the SQL reads, and the
// directive only decides whether it runs.
//
// Comments are stripped first: 050.tempdb.sql explains in prose why it no
// longer reads this, and prose must not trip the disclosure.
func readsSessionText(s Script) bool {
	return strings.Contains(strings.ToLower(StripSQLComments(s.SQL)), "dm_exec_sql_text")
}

// corpusError names the directory the operator actually typed. os.DirFS roots
// the corpus at ".", so every failure below it surfaces as "GetFileAttributesEx
// .:" — a message that describes a path the operator never wrote and cannot act
// on. The embedded corpus has no such directory, so the wrapping only applies
// when one was supplied.
func corpusError(cfg *Config, err error) error {
	if cfg.QueriesDir == "" {
		return fmt.Errorf("reading the query corpus: %w", err)
	}
	return fmt.Errorf("reading the query corpus from %s: %w", cfg.QueriesDir, err)
}

// openExitCode grades an Open failure. Open does not contact the server, so
// everything it can refuse is a configuration mistake — but only the address
// parse is certain to be one, and it is the case that actually occurs: an
// empty SQL_SERVER, a trailing backslash with no instance name, an
// unbracketed IPv6 literal. Reporting those as 1 tells the operator "the
// instance could not be reached" about a socket that was never opened, which
// is the same defect already fixed for an unreadable --queries-dir.
func openExitCode(err error) int {
	if IsBadServerAddress(err) {
		return 2
	}
	return 1
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
		return 2, corpusError(o.Config, err)
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
		return openExitCode(err), err
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
	checks := runPreflightWithDeadline(ctx, conn, o.Config)
	for _, c := range checks {
		if c.Status == "ok" {
			fmt.Printf("  ok      %s\n", c.Name)
			continue
		}
		fmt.Printf("  %-7s %s — %s\n", c.Status, c.Name, c.Impact)
	}

	si, err := probeWithDeadline(ctx, conn, o.Config)
	if err == nil {
		fmt.Printf("\nServer   : %s  %s  %s\n", si.Name, si.Version, si.Edition)
		fmt.Printf("Login    : %s\n", si.Login)
	}

	// The database list is the blast radius. It comes last because it is the
	// part a reader scrolls back to, and it is printed even when it is empty —
	// an empty list is itself the finding when VIEW ANY DEFINITION is missing.
	var noAccess []string
	cands, cerr := candidatesWithDeadline(ctx, conn, o.Config)
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
		for _, s := range sel.Skipped {
			if s.Reason == SkipNoAccess {
				noAccess = append(noAccess, s.Name)
			}
		}
	}

	// The grant script comes last because it needs everything above: the
	// probe results, the version that selects the permission vocabulary, and
	// the databases the run would skip for lack of access. That last one is
	// the reason it cannot be written earlier — it is also the gap the probes
	// cannot see, and the one that silently costs two thirds of the
	// collectors.
	if o.GrantScript != "" {
		if werr := writeGrantScript(o, scripts, checks, si, err, noAccess); werr != nil {
			fmt.Fprintf(os.Stderr, "could not write %s: %v\n", o.GrantScript, werr)
			return 2, nil
		}
	} else if anyDenied(checks) || len(noAccess) > 0 {
		fmt.Print("\nSomething is missing above. To generate the T-SQL that grants\n" +
			"exactly what is missing, and nothing more, for a DBA to review:\n\n" +
			"  sql-auditor check --grant-script grants.sql\n")
	}
	return PreflightExitCode(checks, lintFailures, writable), nil
}

// writeGrantScript builds the permission script and puts it on disk. It is
// a function rather than four lines inline because it has one judgement to
// make: what to do when the probe that yields the login and the version
// failed.
//
// It refuses. A script naming the login from the configuration rather than
// the one the server reports would be a plausible-looking file granting
// permissions to a principal nobody connects with, and the operator would
// find out only when the next run is still denied.
func writeGrantScript(o Options, scripts []Script, checks []CapabilityCheck, si ServerInfo, probeErr error, noAccess []string) error {
	if probeErr != nil {
		return fmt.Errorf("the server probe failed, so the login and version are unknown: %w", probeErr)
	}
	if strings.TrimSpace(si.Login) == "" {
		return errors.New("the server did not report a login name for this connection")
	}
	body, hasStatements := BuildGrantScript(GrantScriptInput{
		Login: si.Login, Instance: si.Name, Version: si.Version, Edition: si.Edition,
		Checks: checks, Scripts: scripts, NoAccessDatabases: noAccess, Tool: o.Version,
	})
	if err := os.WriteFile(o.GrantScript, []byte(body), 0o600); err != nil {
		return err
	}
	if hasStatements {
		fmt.Printf("\nGrants   : %s — review it, then have a DBA run it\n", o.GrantScript)
	} else {
		fmt.Printf("\nGrants   : %s — nothing to grant, and the file says so\n", o.GrantScript)
	}
	return nil
}

// anyDenied reports whether the probe refused anything, which is the only
// case where suggesting the grant script is useful.
func anyDenied(checks []CapabilityCheck) bool {
	for _, c := range checks {
		if c.Status == "denied" {
			return true
		}
	}
	return false
}

// The three read-only calls the pipeline makes outside runUnit, each bounded.
// They are wrappers rather than inline WithTimeout blocks because both Run and
// Check make all three, and a deadline missing from one copy is invisible
// until an instance hangs.
func runPreflightWithDeadline(ctx context.Context, c *sql.Conn, cfg *Config) []CapabilityCheck {
	// One budget for the four probes together: each is a TOP 1 read, so an
	// instance that cannot answer all four inside a single query timeout is
	// exactly the unreachable instance RunPreflight reports as "error".
	dctx, cancel := deadline(ctx, cfg)
	defer cancel()
	return RunPreflight(dctx, c, Capabilities())
}

func probeWithDeadline(ctx context.Context, c *sql.Conn, cfg *Config) (ServerInfo, error) {
	dctx, cancel := deadline(ctx, cfg)
	defer cancel()
	return Probe(dctx, c)
}

func candidatesWithDeadline(ctx context.Context, c *sql.Conn, cfg *Config) ([]DatabaseInfo, error) {
	dctx, cancel := deadline(ctx, cfg)
	defer cancel()
	return CandidateDatabases(dctx, c)
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
	// With no run folder yet, it falls back to a named, timestamped directory
	// under the output directory — not to the output directory itself, where
	// the two documents would sit beside the run folders of every later run
	// and be read as belonging to one of them. WriteManifestWithFallback falls
	// back again to a temp directory and finally to stderr.
	finish := func(runFolder string, code int) (int, error) {
		m.Run.FinishedUTC = nowUTC()
		m.Run.DurationSec = int(time.Since(started).Seconds())
		m.Run.ExitCode = code
		dest := runFolder
		if dest == "" {
			stamp := o.Now
			if stamp.IsZero() {
				stamp = started
			}
			dest = filepath.Join(o.Config.OutputDir, FailedRunFolderName(stamp))
			_ = os.MkdirAll(dest, 0o755)
		}
		if _, err := WriteManifestWithFallback(m, dest); err != nil {
			return code, err
		}
		return code, nil
	}

	// finishWith is finish for a failure the operator has to be told about in
	// words. finish alone returns a nil error, and the CLI prints only what it
	// is handed — so a run killed by an unreadable --queries-dir exited silently
	// and left the operator with a bare status code to interpret. Every caller
	// that knows why it is stopping uses this one.
	//
	// A manifest-write failure outranks the cause: at that point nothing has
	// recorded the run anywhere, which is the more urgent thing to report.
	finishWith := func(runFolder string, code int, cause error) (int, error) {
		c, werr := finish(runFolder, code)
		if werr != nil {
			return c, werr
		}
		return c, cause
	}

	if !outputWritable(o.Config.OutputDir) {
		err := fmt.Errorf("output directory %s is not writable", o.Config.OutputDir)
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finishWith("", 2, err)
	}

	// A corpus that cannot be read or discovered is a bad configuration, not a
	// fatal one: exit 1 is documented as "the instance could not be reached",
	// and reporting a mistyped --queries-dir that way sends a DBA to check a
	// server that was never contacted. Both paths are reachable only through
	// --queries-dir/QUERIES_DIR — the embedded corpus is verified by a test —
	// so both are the operator's typo and both exit 2.
	sum, err := CorpusSHA256(o.Corpus, o.Root)
	if err != nil {
		err = corpusError(o.Config, err)
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finishWith("", 2, err)
	}
	from := "embedded"
	if o.Config.QueriesDir != "" {
		from = "filesystem"
	}
	m.Sources["queries"] = SourceInfo{From: from, Path: o.Config.QueriesDir, SHA256: sum}

	scripts, err := Discover(o.Corpus, o.Root)
	if err != nil {
		err = corpusError(o.Config, err)
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finishWith("", 2, err)
	}

	db, err := Open(o.Config)
	if err != nil {
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finishWith("", openExitCode(err), err)
	}
	defer db.Close()

	// Everything from here runs on this one connection. The pool allows one,
	// so any call handed the *sql.DB while this is held blocks until its
	// context expires — reproduced as "context deadline exceeded", which is
	// not a diagnosis anybody would arrive at from the message.
	conn, err := db.Conn(ctx)
	if err != nil {
		err = fmt.Errorf("cannot reach the instance: %w", err)
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finishWith("", 1, err)
	}
	defer func() { conn.Close() }()

	// Populated before anything can return, so Coverage is never "unknown"
	// while the manifest goes on to make claims about the database list.
	m.Preflight = runPreflightWithDeadline(ctx, conn, o.Config)
	if PreflightExitCode(m.Preflight, 0, true) == 1 {
		err := errors.New("the instance did not answer the preflight; nothing was collected")
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finishWith("", 1, err)
	}
	denied := DeniedCapabilities(m.Preflight)
	// connect is not a per-script gate. Reaching here means it answered.
	delete(denied, "connect")

	si, err := probeWithDeadline(ctx, conn, o.Config)
	if err != nil {
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finishWith("", 1, err)
	}
	m.Server = ServerBlock{Name: si.Name, Version: si.Version, Edition: si.Edition,
		UTCOffsetMinutes: si.UTCOffsetMinutes, Auth: authLabel(o.Config)}

	cands, err := candidatesWithDeadline(ctx, conn, o.Config)
	if err != nil {
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finishWith("", 1, err)
	}
	sel, err := SelectTargets(cands, o.Config.DBInclude, o.Config.DBExclude)
	if err != nil {
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finishWith("", 2, err)
	}
	folders := ResolveDatabaseFolders(sel.Included)
	m.Targets = TargetBlock{Databases: folders, Skipped: sel.Skipped}

	plan := planScripts(scripts, denied, ParseVersion(si.Version), o.Flags)
	// The disclosure paragraph and the queries that run come from this one
	// decision. Split them and the manifest eventually describes a different
	// archive from the one beside it.
	sessionTextBy := collectsSessionText(plan)
	m.Collected.SessionText = len(sessionTextBy) > 0
	for _, path := range sessionTextBy {
		if scriptByPath(scripts, path).RequiresFlag == FlagIncludeSessionText {
			continue
		}
		// An ungated collector cannot come from the embedded corpus — a test
		// forbids it — so this is a --queries-dir corpus. The archive is
		// disclosed correctly either way; the warning is so the operator
		// learns their corpus is collecting more than the flag suggests.
		m.Warnings = append(m.Warnings, fmt.Sprintf(
			"%s reads sys.dm_exec_sql_text without declaring @requires_flag: %s. "+
				"This archive contains session statement text and says so, but the "+
				"query should carry the gate so the default run does not collect it.",
			path, FlagIncludeSessionText))
	}

	// A run folder that cannot be prepared is the operator's to fix — a --keep
	// collision, or a path the process may not create — so it exits 2 like the
	// other configuration refusals rather than 1, which claims the instance was
	// unreachable when it has in fact just been read successfully.
	runFolder := runFolderFor(o.Config.OutputDir, si.Name, o.Now, o.Keep)
	if err := prepareRunFolder(runFolder, o.Keep); err != nil {
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finishWith("", 2, err)
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
			if !connAlive(ctx, conn, o.Config) {
				fmt.Fprintln(os.Stderr, "connection lost; attempting one reconnect")
				conn.Close()
				fresh, cerr := db.Conn(ctx)
				if cerr != nil {
					cerr = fmt.Errorf("reconnect failed: %w", cerr)
					m.Errors = append(m.Errors, ErrorEntry{Message: cerr.Error()})
					return finishWith(runFolder, 1, cerr)
				}
				conn = fresh
				if rerr := resetWithDeadline(ctx, conn, o.Config); rerr != nil {
					rerr = fmt.Errorf("session reset after reconnect failed: %w", rerr)
					m.Errors = append(m.Errors, ErrorEntry{Message: rerr.Error()})
					return finishWith(runFolder, 1, rerr)
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

	if err := resetWithDeadline(ctx, conn, o.Config); err != nil {
		return err
	}
	if u.Name != "" {
		// USE is not free: it takes a lock on the target database and blocks
		// behind a session holding it in single-user or restoring mode. On a
		// bare context that is a collect that never returns.
		uctx, ucancel := deadline(ctx, o.Config)
		_, err := conn.ExecContext(uctx, "USE "+quoteName(u.Name)+";")
		ucancel()
		if err != nil {
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

func resetWithDeadline(ctx context.Context, c *sql.Conn, cfg *Config) error {
	dctx, cancel := deadline(ctx, cfg)
	defer cancel()
	return ResetSession(dctx, c, cfg.Database)
}

// connAlive distinguishes a dead connection from a query that merely failed.
// A missing permission or a bad column must not trigger a reconnect.
//
// The ping is bounded like everything else. A half-open socket answers
// neither way, and an unbounded liveness check is the one call that can hang
// precisely when the connection has already failed.
func connAlive(ctx context.Context, c *sql.Conn, cfg *Config) bool {
	dctx, cancel := deadline(ctx, cfg)
	defer cancel()
	return c.PingContext(dctx) == nil
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
