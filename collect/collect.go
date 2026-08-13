package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
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

// FlagQueryStoreDetail gates the deep read of the Query Store: the full,
// untruncated text of the heaviest queries and their execution plans.
//
// It is not FlagIncludeSessionText, and merging the two would be wrong in both
// directions. Session text is what was running during the collection; this is
// the recorded history of what the workload has been doing, whether or not it
// was running when the collector connected. And a plan discloses more than the
// text does: it carries the compiled parameter values, the literal predicates
// and every object name the statement touched.
const FlagQueryStoreDetail = "query_store_detail"

// FlagObjectDefinitions gates the export of module source: the body of every
// view, stored procedure, function and trigger.
//
// It is a third decision and not a widening of either of the others, because
// what it discloses comes from somewhere else again. Session text is what was
// running; the Query Store is what has run; a module body is code the client
// wrote and that no execution needs to have touched at all. It routinely names
// linked servers, and an OPENQUERY or an EXECUTE AS can carry a credential in
// clear — in a procedure nobody has opened in years, which is exactly the kind
// this exports first.
const FlagObjectDefinitions = "object_definitions"

// FlagDeadlockGraphs gates the export of the deadlock reports system_health
// still holds.
//
// A fourth decision, and the narrowest of them: a graph names two statements
// and the resource they fought over, and it carries their SQL verbatim — the
// same literals --include-session-text exists for. 060.system-health.sql
// collects the count and the timestamps by default and stops exactly there,
// which is what makes this a separate opt-in rather than a widening of it.
const FlagDeadlockGraphs = "deadlock_graphs"

// FlagBlockedProcessReports gates the export of blocked process reports out of
// whatever Extended Events session captures them.
//
// A fifth decision, and it is not the deadlock one. A deadlock report describes
// a conflict the engine resolved by killing somebody; a blocked process report
// describes one still in progress when the threshold expired, and it names the
// blocker — the session that was doing nothing wrong and holding a lock. Both
// carry statement text, so both are gated, and separately because an operator
// who agreed to one has not agreed to the other.
//
// It reads the file system, through sys.fn_xe_file_target_read_file and as the
// SQL Server service account rather than as the connected login. That is a
// wider reach than any other collector in this corpus and is a second reason
// the decision is its own.
const FlagBlockedProcessReports = "blocked_process_reports"

// FlagQueryStorePlanStats gates the search for the last profiled plan of each
// extracted query.
//
// It is a second decision rather than a widening of the first because of where
// it reads. sys.dm_exec_query_plan_stats is reached through the plan cache of
// the whole instance; every other per-database collector in this corpus sees
// only the database it was pointed at. The dbid filter restricts what is kept,
// not what is read, and an operator authorising a per-database extraction has
// not thereby authorised that.
const FlagQueryStorePlanStats = "query_store_plan_stats"

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
	// QueryStore carries the resolved collection window and the selection made
	// by 021 for 022 to read. Run creates it; a caller that leaves it nil gets
	// one rather than a panic, because the two @writer scripts are the only
	// things that consult it and a run without them must behave as before.
	QueryStore *QueryStoreState
	// Observer watches the run go by, for a caller that displays progress.
	// Nil is the ordinary case and means exactly the behaviour this package
	// had before the interface existed: every call site goes through the
	// nil-safe wrapper, so there is no second code path to keep in step.
	Observer Observer
	// Progress is where this package's running commentary goes: the run
	// replacement warning, the reconnect notice, and the manifest's fallback
	// chain down to lastResort. Nil means os.Stderr, so a subcommand behaves
	// exactly as before.
	//
	// It captures ONLY what already went to stderr. It is deliberately not a
	// general output hook: check writes its listing to stdout because
	// `sql-auditor check > report.txt` is how an operator keeps it, and Run
	// writes the archive path to stdout because `sql-auditor collect | tail -1`
	// is how a script reads it. Routing either here would empty a file a user
	// asked for. cmd/sql-auditor/main.go states the same distinction in words
	// where it prints the build stamp.
	Progress io.Writer
}

// progress is the single place that decides where the commentary goes, so the
// twenty-odd call sites can be written without a nil test. io.Discard is
// deliberately NOT the nil default: lastResort's output is sometimes the only
// surviving record of a run, and silence would destroy it.
func (o Options) progress() io.Writer {
	if o.Progress == nil {
		return os.Stderr
	}
	return o.Progress
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
func prepareRunFolder(path string, keep bool, progress io.Writer) error {
	if keep {
		// --keep must never land on an occupied name. RunFolderFor already
		// looks for a free one, but if it ever hands back a taken path the
		// two runs would merge into one folder: this run's manifest beside
		// the previous run's result files, describing an archive it does not
		// match. A run collected under --include-session-text merging into a
		// run collected without it puts session text inside an archive whose
		// own MANIFEST.txt says there is none. Refuse loudly instead.
		if taken, what := RunNameTaken(path); taken {
			return fmt.Errorf("--keep: %s already exists; this run would be written into "+
				"the same place as an earlier one and the two would be indistinguishable", what)
		}
		return os.MkdirAll(path, 0o755)
	}
	if warning := replacingRunWarning(path); warning != "" {
		fmt.Fprintln(progress, warning)
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	if err := os.Remove(path + ".zip"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(path, 0o755)
}

// RunNameTaken and RunFolderFor are exported for one caller and one reason:
// the wizard has to ask before this package destroys anything. Without --keep,
// prepareRunFolder does os.RemoveAll on the folder AND removes the .zip beside
// it, warning only on stderr — which a full-screen wizard has covered up. An
// operator rerunning the same day to add one option would lose the archive
// just mailed. The wizard calls these two before Run and puts the answer on
// screen; nothing is deleted without a keystroke that names the choice.
//
// RunNameTaken reports whether anything from an earlier run already occupies
// this name. The archive counts: it sits beside the folder rather than inside
// it, so a folder that was moved away while its .zip stayed still leaves a
// name that a second run would appear to have produced.
func RunNameTaken(path string) (bool, string) {
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
	taken, what := RunNameTaken(path)
	if !taken {
		return ""
	}
	return "replacing the previous run of the same day: " + what +
		"\npass --keep to write this run alongside it instead"
}

// RunFolderFor applies the collision policy: a same-day rerun replaces the
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
func RunFolderFor(outputDir, server string, now time.Time, keep bool) string {
	base := filepath.Join(outputDir, RunFolderName(server, now))
	if !keep {
		return base
	}
	if taken, _ := RunNameTaken(base); !taken {
		return base
	}
	withTime := base + "-" + now.Format("1504")
	candidate := withTime
	for i := 2; ; i++ {
		if taken, _ := RunNameTaken(candidate); !taken {
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

// readsObjectDefinitions looks for the two ways a script can obtain the body of
// a module. It exists for the reason readsSessionText does: --queries-dir
// accepts a corpus this project has never seen, and a file there selecting
// sys.sql_modules.definition without declaring @requires_flag would put the
// client's own code into an archive whose manifest says object_definitions:
// false.
//
// It only produces a WARNING, never the disclosure. The disclosure is latched
// from the definition files actually written, because reading is not emitting:
// 010.objects.sql could one day count modules by joining sys.sql_modules
// without exporting a line of one, and a matcher alone would have MANIFEST.txt
// announce source code the archive does not hold.
//
// Comments are stripped first, so a file explaining in prose why it does not
// read definitions does not trip the warning.
func readsObjectDefinitions(s Script) bool {
	sql := strings.ToLower(StripSQLComments(s.SQL))
	return strings.Contains(sql, "sql_modules") || strings.Contains(sql, "object_definition")
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
	// Everything is gathered first and printed afterwards. Check used to
	// interleave the two, which is exactly why nothing else could reuse any of
	// it: the facts existed only as text on their way to stdout.
	v, err := Verify(ctx, o)
	if v.CorpusErr != nil {
		// Nothing has been printed on this path, deliberately. The CLI prints
		// the returned error, and a "Queries (0):" above it would report an
		// empty corpus where in fact none was found.
		return 2, err
	}
	fmt.Printf("Queries (%d):\n", len(v.Scripts))
	for _, s := range v.Scripts {
		switch {
		case s.LintError != "":
			fmt.Printf("  !! %-42s %s\n", s.Path, s.LintError)
		default:
			fmt.Printf("  %-42s %s\n", s.Path, scriptNote(s, o.Flags))
		}
	}

	// The window conflict is priced by the flags, so a collection that never
	// reads the Query Store only warns about it. But check exists to say what
	// a collection would do, and staying silent here would let an operator
	// verify a configuration, see nothing, then meet the refusal on the run
	// they came to prepare for.
	if o.Config.QueryStoreWindowConflict != "" {
		fmt.Printf("\n  !! %s\n", o.Config.QueryStoreWindowConflict)
		if o.Flags[FlagQueryStoreDetail] || o.Flags[FlagQueryStorePlanStats] {
			fmt.Println("     with the options above, a collection would stop on this.")
		} else {
			fmt.Println("     no Query Store option is on, so a collection would warn and continue.")
		}
	}

	fmt.Printf("\nOutput   : %s", o.Config.OutputDir)
	if !v.OutputWritable {
		fmt.Print("  !! not writable")
	}
	fmt.Println()

	// The two failures that end the listing, in the order Verify meets them.
	// Both are handed back so the CLI can print them; the unreachable instance
	// also says so in words, because "1" alone reads as a query that failed.
	if v.OpenErr != nil {
		return openExitCode(v.OpenErr), err
	}
	if v.ConnErr != nil {
		fmt.Fprintln(o.progress(), "cannot reach the instance")
		return 1, err
	}

	fmt.Println("\nPermissions:")
	for _, c := range v.Checks {
		if c.Status == "ok" {
			fmt.Printf("  ok      %s\n", c.Name)
			continue
		}
		fmt.Printf("  %-7s %s — %s\n", c.Status, c.Name, c.Impact)
	}

	if v.Probed {
		fmt.Printf("\nServer   : %s  %s  %s\n", v.Server.Name, v.Server.Version, v.Server.Edition)
		fmt.Printf("Login    : %s\n", v.Server.Login)
	}

	// Advice, not a finding, and it is here rather than in the archive on
	// purpose: check exists to help an operator prepare a run, while the
	// archive states what the instance is. It stays silent on an instance with
	// no lock waits.
	if lines := BlockingNotice(v.Blocking); len(lines) > 0 {
		fmt.Println("\nBlocking:")
		for _, l := range lines {
			fmt.Printf("  %s\n", l)
		}
	}

	// The database list is the blast radius. It comes last because it is the
	// part a reader scrolls back to, and it is printed even when it is empty —
	// an empty list is itself the finding when VIEW ANY DEFINITION is missing.
	switch {
	case v.CandidatesErr != nil:
		fmt.Fprintf(os.Stderr, "could not list databases: %v\n", v.CandidatesErr)
	case v.SelectErr != nil:
		// A malformed DB_INCLUDE/DB_EXCLUDE is the operator's typo, and it
		// stops here: reporting the grant advice below on a selection nobody
		// could compute would name the wrong databases.
		fmt.Fprintln(os.Stderr, v.SelectErr)
		return 2, nil
	default:
		fmt.Printf("\nDatabases that would be collected (%d):\n", len(v.Selection.Included))
		if len(v.Selection.Included) == 0 {
			fmt.Println("  (none)")
		}
		for _, f := range v.Folders {
			fmt.Printf("  - %s -> %s/\n", f.Name, f.Folder)
		}
		if len(v.Selection.Skipped) > 0 {
			fmt.Printf("\nDatabases skipped (%d):\n", len(v.Selection.Skipped))
			for _, s := range v.Selection.Skipped {
				fmt.Printf("  - %s (%s)\n", s.Name, s.Reason)
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
		if werr := writeGrantScript(o, v.Scripts, v.Checks, v.Server, v.ServerErr, v.NoAccess); werr != nil {
			fmt.Fprintf(os.Stderr, "could not write %s: %v\n", o.GrantScript, werr)
			return 2, nil
		}
	} else if anyDenied(v.Checks) || len(v.NoAccess) > 0 {
		fmt.Print("\nSomething is missing above. To generate the T-SQL that grants\n" +
			"exactly what is missing, and nothing more, for a DBA to review:\n\n" +
			"  sql-auditor check --grant-script grants.sql\n")
	}
	return PreflightExitCode(v.Checks, v.LintFailures, v.OutputWritable), nil
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

// The read-only calls the pipeline makes outside runUnit, each bounded. They
// are wrappers rather than inline WithTimeout blocks because both Run and
// Verify make them, and a deadline missing from one copy is invisible until an
// instance hangs. The fourth, blockingWithDeadline, lives in verify.go beside
// the caller that revealed it had none.
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
	// A writer script produces a directory, not a document, and `check` is what
	// a DBA reads before authorising one. "one directory per database: query
	// text, plans and per-interval statistics" is the sentence that lets them
	// decide; the file name alone does not.
	if s.Writer != "" {
		notes = append(notes, KnownWriters[s.Writer].Description)
	}
	return strings.Join(notes, ", ")
}

// defaultQueryStoreDays repeats the default Resolve applies to
// QUERY_STORE_DAYS, for the one case Resolve cannot express: a lone
// QUERY_STORE_TO leaves the days at 0 — the bound is present, so the sliding
// default was suppressed — and says nothing about where the window starts.
const defaultQueryStoreDays = 7

// resolveWindow turns the configured window into two instants. It runs after
// the server probe because that is the only moment the server's UTC offset is
// known, and the bounds are written in the server's wall clock: the operator
// types what the client said, "between 14:00 and 15:00 on the 26th", which is
// neither the auditor's laptop's time nor UTC.
//
// It translates, and it reports the one conflict Resolve could see but could
// not price: QUERY_STORE_DAYS typed alongside a bound. Resolve detects that on
// the raw keys, where "typed" and "defaulted" can still be told apart, and
// leaves the sentence on the Config; the decision about what it costs is
// windowForRun's, exactly like the two refusals below — which only become
// visible once the values are instants, and which both produce a run that looks
// successful and answers nothing.
func resolveWindow(cfg *Config, now time.Time, loc *time.Location) (time.Time, time.Time, error) {
	if cfg.QueryStoreWindowConflict != "" {
		return time.Time{}, time.Time{}, errors.New(cfg.QueryStoreWindowConflict)
	}

	var from, to time.Time
	if cfg.QueryStoreDays > 0 {
		// The sliding form falls through to the refusals below rather than
		// returning here. A days count large enough to overflow the duration
		// arithmetic yields a start after the end, and a branch that returned
		// early handed that window on as usable — ahead of the very checks
		// written to catch it. Resolve now caps the value (maxQueryStoreDays),
		// so this is the second lock on the same door, not the only one.
		to = now
		from = now.Add(-time.Duration(cfg.QueryStoreDays) * 24 * time.Hour)
	} else {
		to = now
		if cfg.QueryStoreTo != "" {
			t, err := parseServerLocal(cfg.QueryStoreTo, loc)
			if err != nil {
				return time.Time{}, time.Time{}, fmt.Errorf("QUERY_STORE_TO: %w", err)
			}
			to = t
		}
		from = to.Add(-defaultQueryStoreDays * 24 * time.Hour)
		if cfg.QueryStoreFrom != "" {
			f, err := parseServerLocal(cfg.QueryStoreFrom, loc)
			if err != nil {
				return time.Time{}, time.Time{}, fmt.Errorf("QUERY_STORE_FROM: %w", err)
			}
			from = f
		}
	}

	if !from.Before(to) {
		return time.Time{}, time.Time{}, fmt.Errorf(
			"the Query Store window is empty: %s is not before %s (both in the server's local time); "+
				"a window that contains no interval returns nothing and reads as a quiet instance",
			from.In(loc).Format(time.RFC3339), to.In(loc).Format(time.RFC3339))
	}
	// A bound in the future is almost always a bound typed in the wrong zone,
	// and the Query Store has nothing to say about it either way.
	if late := latest(from, to); late.After(now) {
		return time.Time{}, time.Time{}, fmt.Errorf(
			"the Query Store window ends in the future: %s, and the collection is running at %s "+
				"(both in the server's local time); check the bound against the server's clock, "+
				"not the collecting machine's",
			late.In(loc).Format(time.RFC3339), now.In(loc).Format(time.RFC3339))
	}
	return from, to, nil
}

// serverNow is the instant the future-bound check compares against: the
// server's, when the probe returned one, and the collecting machine's only as a
// fallback. The bounds are the server's wall clock, and a server whose clock
// runs a few minutes ahead of the auditor's laptop would otherwise have a bound
// typed as "just now" refused as being in the future — by a message telling the
// operator to check it against the server's clock, which is the clock that
// accepted it.
func serverNow(si ServerInfo, local time.Time) time.Time {
	if !si.NowUTC.IsZero() {
		return si.NowUTC
	}
	if local.IsZero() {
		return time.Now()
	}
	return local
}

// windowForRun resolves the window and decides what a refusal costs.
//
// A window nobody can answer is fatal only when something is going to read it.
// Neither Query Store flag being on means no collector will look at these
// bounds at all, and killing an ordinary collection over a stale QUERY_STORE_TO
// left in a .env would be a refusal about a feature the operator did not ask
// for. The run carries on and the warning says why the setting was ignored, so
// the next run with the flag on is not a surprise.
//
// This is the one place where every reason a window can be unusable is priced,
// the days-versus-bounds conflict included. Resolve detects that conflict but
// no longer refuses it: a refusal taken before any flag is read cost an
// ordinary collection, and a check, over a setting nothing in either was going
// to consult.
func windowForRun(cfg *Config, flags map[string]bool, now time.Time, loc *time.Location) (time.Time, time.Time, string, error) {
	from, to, err := resolveWindow(cfg, now, loc)
	if err == nil {
		return from, to, "", nil
	}
	if flags[FlagQueryStoreDetail] || flags[FlagQueryStorePlanStats] {
		return time.Time{}, time.Time{}, "", err
	}
	return time.Time{}, time.Time{}, fmt.Sprintf(
		"the configured Query Store window was not usable and was ignored: %v. "+
			"Nothing in this run reads it — neither %s nor %s is on — but the next run that "+
			"turns one of them on will stop on it.",
		err, KnownFlags[FlagQueryStoreDetail], KnownFlags[FlagQueryStorePlanStats]), nil
}

func latest(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// parseServerLocal reads a bound in the server's zone. Resolve already checked
// the shape, so a failure here means the value reached the config by some other
// route; it is reported rather than defaulted, because a window silently moved
// to "now" is the failure this whole path exists to avoid.
func parseServerLocal(raw string, loc *time.Location) (time.Time, error) {
	if t, err := time.ParseInLocation("2006-01-02T15:04", raw, loc); err == nil {
		return t, nil
	}
	t, err := time.ParseInLocation("2006-01-02", raw, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid value %q, want 2006-01-02T15:04 or 2006-01-02", raw)
	}
	return t, nil
}

// joinInt64 renders the selected query ids as the comma-wrapped list 022
// matches on: ",11,22,". The leading and trailing commas are what make its
// CHARINDEX match whole ids — without them 11 matches inside 211 — and the
// empty string for an empty slice is what makes the collector inert rather
// than erroneous when 021 selected nothing.
func joinInt64(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte(',')
	for _, id := range ids {
		fmt.Fprintf(&b, "%d,", id)
	}
	return b.String()
}

// queryStoreUnits applies QUERY_STORE_DB_INCLUDE to the databases a writer
// script will run against, and names every database it removes.
//
// It filters the selection DB_INCLUDE and DB_EXCLUDE already made; it never
// widens it. A database excluded from the run cannot be brought back by asking
// the Query Store extraction for it, and a database removed here is recorded
// with its name — N identical lines naming no database is the gap this
// setting's argument would later be had over.
func queryStoreUnits(cfg *Config, s Script, folders []DatabaseFolder) ([]DatabaseFolder, []SkippedScript) {
	if s.Writer == "" || cfg.QueryStoreDBInclude == "" {
		return folders, nil
	}
	patterns := splitPatterns(cfg.QueryStoreDBInclude)
	var kept []DatabaseFolder
	var skipped []SkippedScript
	for _, f := range folders {
		if matchAny(patterns, f.Name) {
			kept = append(kept, f)
			continue
		}
		skipped = append(skipped, SkippedScript{
			Script: s.Path, Target: f.Name,
			Reason: "not matched by QUERY_STORE_DB_INCLUDE",
		})
	}
	return kept, skipped
}

// discloseWrites sets the manifest's Query Store disclosures from what the unit
// actually put on disk, and from nothing else.
//
// There are two independent observations and both are needed. The choke point
// reports a Showplan namespace in any payload it wrote, which catches a foreign
// corpus dumping plan XML through the ordinary encoder. The writers report the
// files they created, which catches the case the bytes cannot: query text is a
// disclosure with no plan in it — a natively compiled procedure has a NULL
// query_plan, and a run that exhausts the budget writes texts and no plans at
// all.
//
// What is deliberately absent is a matcher over the SQL. Two collectors in this
// corpus read sys.query_store_plan without emitting a plan, both ungated and
// both running by default, so a matcher would have MANIFEST.txt announce
// execution plans in an archive that holds none. Reading is not emitting.
//
// Every field it touches is set-only, and that is load-bearing rather than
// incidental. takeShowplan consumes the choke point's flag, so the fact it
// carries survives exactly one call: it is latched here, in a manifest nothing
// ever clears, before the next unit runs. A version of this function that could
// set a Collected field back to false would let a database with the Query Store
// off retract the disclosure of the database collected before it.
func discloseWrites(m *Manifest, rw *runWriter, s Script, res WriteResult) {
	if rw.takeShowplan() {
		m.Collected.QueryStoreDetail = true
		// The latch stays as it is, and the instruction goes.
		//
		// The check is over the bytes written, and it is deliberately coarse:
		// over-disclosure is the safe side, so a payload that merely carries the
		// Showplan namespace latches the disclosure. But the namespace reaches a
		// payload two ways. It is in a plan, and it is also in captured
		// application SQL — 020 and 023 put up to 500 characters of collected
		// query text into their JSON, and a query written with the standard
		// WITH XMLNAMESPACES('…/showplan' AS p) idiom, common in exactly the
		// workloads whose Query Store is being read, puts those bytes there
		// verbatim. Telling the operator to add @requires_flag to a collector
		// that emits no plan is an instruction that cannot be right in that
		// case, and it names the wrong file. So the warning reports the
		// observation and leaves the reading of it to whoever knows the corpus.
		if s.RequiresFlag == "" {
			m.Warnings = append(m.Warnings, fmt.Sprintf(
				"%s: a payload written by this collector carries the Showplan XML namespace, "+
					"and the script declares no @requires_flag. The archive discloses execution "+
					"plans on that basis, which is the safe side of the check. Two things look "+
					"alike here: the collector really emits plan XML, or it collected query text "+
					"that itself mentions the namespace (WITH XMLNAMESPACES over a plan is an "+
					"ordinary thing for an audited query to do). Read the file before concluding "+
					"the corpus emits plans by default.",
				s.Path))
		}
	}
	if res.PlanFiles > 0 || res.TextFiles > 0 {
		m.Collected.QueryStoreDetail = true
	}
	// From the plan written, never from the flag passed: a run with the option
	// on and nothing in the cache to match discloses nothing, because it
	// collected nothing.
	if s.Writer == "query-store-profiled" && res.PlanFiles > 0 {
		m.Collected.QueryStoreProfiledPlans = true
	}
	// Same rule, third disclosure: from the definitions written, never from the
	// flag passed. A run with the option on against a database of nothing but
	// tables discloses no module source, because it collected none.
	if res.DefinitionFiles > 0 {
		m.Collected.ObjectDefinitions = true
	}
	// And the fourth. A run with the option on against an instance whose ring
	// holds no deadlock discloses nothing, because it collected nothing.
	if res.GraphFiles > 0 {
		m.Collected.DeadlockGraphs = true
	}
	if res.ReportFiles > 0 {
		m.Collected.BlockedProcessReports = true
	}
}

// Run executes the full pipeline. It returns an exit code rather than
// deciding the process's fate, so the CLI owns that.
func Run(ctx context.Context, o Options) (int, error) {
	if o.QueryStore == nil {
		o.QueryStore = NewQueryStoreState()
	}
	// Wrapped once, here, so every callback below can be written unguarded.
	// A nil Observer — every command-line run — makes all of them no-ops.
	obs := observer{Observer: o.Observer}
	m := NewManifest("sql-auditor", o.Version, o.Commit)
	m.Config = map[string]string{
		"queries_dir":          o.Config.QueriesDir,
		"output_dir":           o.Config.OutputDir,
		"db_include":           o.Config.DBInclude,
		"db_exclude":           o.Config.DBExclude,
		"include_session_text": fmt.Sprint(o.Flags[FlagIncludeSessionText]),
		"object_definitions":   fmt.Sprint(o.Flags[FlagObjectDefinitions]),
		"deadlock_graphs":      fmt.Sprint(o.Flags[FlagDeadlockGraphs]),

		"query_store_detail":     fmt.Sprint(o.Flags[FlagQueryStoreDetail]),
		"query_store_plan_stats": fmt.Sprint(o.Flags[FlagQueryStorePlanStats]),
		"query_store_days":       fmt.Sprint(o.Config.QueryStoreDays),
		"query_store_top":        fmt.Sprint(o.Config.QueryStoreTop),
		// A setting that changed which databases were read and is absent from
		// the record is the one that will be argued about later.
		"query_store_db_include": o.Config.QueryStoreDBInclude,
	}
	// The bounds as typed, beside the bounds as resolved further down. It is
	// having the pair side by side that makes a timezone mistake visible after
	// the run rather than never: "14:00" and "2026-07-26T12:00:00Z" together
	// say which offset was applied.
	if o.Config.QueryStoreFrom != "" {
		m.Config["query_store_from_requested"] = o.Config.QueryStoreFrom
	}
	if o.Config.QueryStoreTo != "" {
		m.Config["query_store_to_requested"] = o.Config.QueryStoreTo
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
		if _, err := WriteManifestWithFallback(m, dest, o.progress()); err != nil {
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

	// The window is resolved here and nowhere earlier: this is the first moment
	// the server's UTC offset is known, and the operator types what the client
	// said — "between 14:00 and 15:00 on the 26th" — which is the server's wall
	// clock, not the auditor's laptop's and not UTC. Getting this wrong shifts a
	// seven-day window by hours; on a one-hour window it misses the incident
	// entirely, returns rows from the wrong hour, and looks exactly like a
	// successful answer.
	loc := time.FixedZone("server", si.UTCOffsetMinutes*60)
	windowFrom, windowTo, windowNote, err := windowForRun(o.Config, o.Flags, serverNow(si, o.Now), loc)
	if err != nil {
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finishWith("", 2, err)
	}
	if windowNote != "" {
		m.Warnings = append(m.Warnings, windowNote)
	}
	o.QueryStore.From, o.QueryStore.To = windowFrom, windowTo
	// Rendered in the server's zone, so the offset that was applied is on the
	// page: "14:00" typed and "2026-07-26T14:00:00+02:00" resolved is a reader's
	// proof that the bound was not read as the collecting machine's local time.
	if windowFrom.IsZero() {
		// The window was refused and ignored. Recording "0001-01-01" as the
		// resolved value would be a resolved window that nothing resolved.
		m.Config["query_store_from"] = "not resolved"
		m.Config["query_store_to"] = "not resolved"
	} else {
		m.Config["query_store_from"] = windowFrom.In(loc).Format(time.RFC3339)
		m.Config["query_store_to"] = windowTo.In(loc).Format(time.RFC3339)
	}

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
	// Validated once, here, rather than silently matching nothing later: a
	// malformed pattern that quietly excluded every database would produce an
	// extraction that ran nowhere and said only that it had been narrowed.
	if err := checkPatterns("QUERY_STORE_DB_INCLUDE", splitPatterns(o.Config.QueryStoreDBInclude)); err != nil {
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
	// The same check for module source. Only a warning: the disclosure itself
	// is latched from the files written, in discloseWrites.
	for _, p := range plan {
		if p.Skip != "" || p.Script.LintError != "" || p.Script.RequiresFlag == FlagObjectDefinitions {
			continue
		}
		if !readsObjectDefinitions(p.Script) {
			continue
		}
		m.Warnings = append(m.Warnings, fmt.Sprintf(
			"%s reads module definitions without declaring @requires_flag: %s. "+
				"If it exports them, this archive holds source code written here — which "+
				"can name linked servers and embed credentials — and the query should carry "+
				"the gate so the default run does not collect it.",
			p.Script.Path, FlagObjectDefinitions))
	}

	// A run folder that cannot be prepared is the operator's to fix — a --keep
	// collision, or a path the process may not create — so it exits 2 like the
	// other configuration refusals rather than 1, which claims the instance was
	// unreachable when it has in fact just been read successfully.
	runFolder := RunFolderFor(o.Config.OutputDir, si.Name, o.Now, o.Keep)
	if err := prepareRunFolder(runFolder, o.Keep, o.progress()); err != nil {
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finishWith("", 2, err)
	}

	// One run writer for the whole run, handed to every unit. It is the single
	// choke point: both branches of runUnit write through it and nothing else,
	// so a .sqlplan whose bytes never form a JSON payload is inspected exactly
	// like one that does. MANIFEST.txt and _run.json stay outside it — they are
	// the run's own record and must be written even when the budget is gone.
	rw := newRunWriter(runFolder, maxRunBytes)

	// A lint error stops its own script and nothing else, so it is recorded
	// ahead of the loop rather than inside it. planUnits has already left
	// those scripts out of the unit list, and a broken collector is an error
	// the operator can act on before the run's first result arrives.
	for _, p := range plan {
		if p.Script.LintError != "" {
			m.Errors = append(m.Errors, ErrorEntry{Script: p.Script.Path, Message: p.Script.LintError})
			exit = 2
		}
	}

	// The whole plan is unfolded before anything runs. The list of units is
	// what the loop walks and what a total can be stated from; the skips come
	// back in plan order, so appending them wholesale leaves the manifest
	// reading exactly as it did when they were collected along the way.
	units, planSkipped := planUnits(plan, folders, o.Config)
	m.Skipped = append(m.Skipped, planSkipped...)

	// The total is announced before the first unit runs, and it is the same
	// list the loop below walks — not a product of scripts and databases.
	obs.Planned(len(units), len(folders))
	for _, s := range planSkipped {
		obs.ScriptSkipped(s.Script, s.Target, s.Reason)
	}

	for _, u := range units {
		s, target := u.Script, u.Target
		obs.UnitStarted(s.Path, target.Name)
		before, started := rw.Spent(), time.Now()
		err := runUnit(ctx, conn, o, m, rw, s, target)
		// The bytes of this unit are the difference across a run-level total,
		// which is what the writer offers. A unit that failed still reports
		// what it managed to write before failing.
		obs.UnitDone(s.Path, target.Name, int64(rw.Spent()-before), time.Since(started), err)
		if err == nil {
			continue
		}
		// The context is consulted before a single word is written down. Were
		// this after the ErrorEntry, a stopped run would carry the phantom
		// "context canceled" failure for ever; were it after the reconnect
		// below, it would never be reached at all, because a ping on a dead
		// context cannot succeed.
		code, cancelled := recordUnitFailure(ctx, m, s.Path, target.Name, err)
		if cancelled {
			// Out of the loop, but not out of the function: the manifest and
			// the archive are still written below. A DBA who stopped after
			// three minutes keeps what those three minutes collected, and the
			// manifest's cancelled flag is what says the archive is partial.
			// exit is left as it stands — 0 for the ordinary stop, still 2 if
			// a lint error had already failed the run on its own.
			break
		}
		exit = code

		// One reconnect attempt on a dead connection. The replacement is
		// reset before the next unit uses it — the PowerShell version
		// skipped that step and quietly broke its own invariant.
		if !connAlive(ctx, conn, o.Config) {
			fmt.Fprintln(o.progress(), "connection lost; attempting one reconnect")
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

	// The two phases after the loop are announced because they are the only
	// stretches where the gauge is full and the tool is still working: a
	// screen showing 223/223 and nothing else reads as a hang while a large
	// run folder is being zipped.
	obs.Phase("writing manifest")
	code, ferr := finish(runFolder, exit)
	if ferr != nil {
		return code, ferr
	}
	// The archive is built after the manifest so it contains it. A failure
	// here leaves the run folder intact and readable; only the transport
	// packaging is missing, which is a partial failure, not a fatal one.
	zipPath := runFolder + ".zip"
	obs.Phase("archiving")
	if err := Zip(runFolder, zipPath); err != nil {
		return 2, err
	}
	// Silenced under an Observer, not redirected. `sql-auditor collect | tail -1`
	// is how a script picks up the archive path, so on the command line these
	// two lines must keep going to stdout exactly where they always went. A
	// caller that owns the screen counted every unit through the Observer and
	// derives the same path from RunFolderName, so printing these would smear
	// its frame with facts it already holds.
	if o.Observer == nil {
		fmt.Printf("%d result(s), %d skipped, %d error(s)\n%s\n",
			len(m.Results), len(m.Skipped), len(m.Errors), zipPath)
	}
	return code, nil
}

func runUnit(ctx context.Context, conn *sql.Conn, o Options, m *Manifest,
	rw *runWriter, s Script, u DatabaseFolder) error {

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

	args, note := queryStoreArgs(o, s, u)
	if note != "" {
		m.Warnings = append(m.Warnings, note)
	}

	start := time.Now()
	rows, err := conn.QueryContext(qctx, s.SQL, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	sets, err := ReadResultSets(rows, s.Results)
	if err != nil {
		return err
	}

	var res WriteResult
	var writeErr error
	if s.Writer != "" {
		w := writerFor(s.Writer)
		if w == nil {
			// Never a silent fall back to the encoder. A script declaring
			// @writer expects a directory of files; producing one JSON instead
			// would leave an archive that looks collected and holds none of what
			// was asked for.
			return fmt.Errorf("@writer: no writer registered for %q", s.Writer)
		}
		// The counts come back valid even with a non-nil error: a writer that
		// failed halfway has already put files on disk, and computing the
		// disclosure from a subset of what the archive holds is the defect this
		// whole design is built around.
		res, writeErr = w(WriteRequest{
			Out: rw, Script: s, Unit: u, Sets: sets,
			State: o.QueryStore,
			Warn:  func(msg string) { m.Warnings = append(m.Warnings, msg) },
		})
	} else {
		payload, warnings, encErr := Encode(sets)
		if encErr != nil {
			return encErr
		}
		m.Warnings = append(m.Warnings, warnings...)
		rel := ResultRelativePath(s.Dir, s.Base, u.Folder)
		n, werr := rw.write(rel, payload)
		res, writeErr = WriteResult{Rel: rel, Bytes: n}, werr
	}

	// Before any error return: what reached disk is disclosed whether or not
	// the collector that wrote it finished.
	discloseWrites(m, rw, s, res)
	if writeErr != nil {
		// A writer that fails partway leaves files behind. Disclosing them is
		// not enough on its own: MANIFEST.txt sizes the archive from the
		// entries below, so returning here would leave the run holding bytes
		// it never counted — an archive containing more than it declares,
		// which is the one thing this whole path exists to prevent. The entry
		// is recorded with what actually landed and a status that says the
		// collector did not finish, so the count is right and nobody reads it
		// as a complete result.
		if res.Bytes > 0 {
			m.Results = append(m.Results, ResultEntry{
				Script: s.Path, Scope: scopeName(s), Target: u.Name, Output: res.Rel,
				Bytes: res.Bytes, DurationMS: int(time.Since(start).Milliseconds()),
				Status: "incomplete",
			})
		}
		return writeErr
	}

	m.Results = append(m.Results, ResultEntry{
		Script: s.Path, Scope: scopeName(s), Target: u.Name, Output: res.Rel,
		Bytes: res.Bytes, DurationMS: int(time.Since(start).Milliseconds()), Status: "ok",
	})
	return nil
}

func scopeName(s Script) string {
	if s.Scope == ScopeDatabase {
		return "database"
	}
	return "instance"
}

// queryStoreArgs supplies the named parameters a writer script declares, and
// nothing for anything else. Sending them for every collector would switch all
// twenty-eight to sp_executesql for the sake of two, changing how the whole
// corpus is executed.
//
// Each writer gets the parameters its own SQL declares. A parameter declared
// but unused is legal under sp_executesql, but a reader of 021 who finds
// @qs_query_ids on the wire has to work out that it means nothing there.
//
// The second return is a warning for the manifest, and it exists for the one
// place in this feature where an archive can under-report without saying so.
// An empty @qs_query_ids is the same value on the wire whether 021 selected
// nothing, 021 failed, or 021 never ran at all, and 022's index then says
// "nothing matched" — indistinguishable from a genuine no-match on an instance
// without LAST_QUERY_PLAN_STATS. The two cases are told apart by the map, not
// by the list: an entry that is empty means 021 ran here and retained nothing,
// which its own index already records; NO entry means 021 delivered nothing at
// all, and only this warning says so.
func queryStoreArgs(o Options, s Script, u DatabaseFolder) ([]any, string) {
	switch s.Writer {
	case "query-store-detail":
		return []any{
			sql.Named("qs_from", o.QueryStore.From),
			sql.Named("qs_to", o.QueryStore.To),
			sql.Named("qs_top", o.Config.QueryStoreTop),
		}, ""
	case "query-store-profiled":
		ids, delivered := o.QueryStore.Selected[u.Name]
		note := ""
		if !delivered {
			note = fmt.Sprintf(
				"%s: the Query Store detail collector delivered no selection for %s — it was "+
					"not run, it failed, or the Query Store is off there — so this collector was "+
					"asked for nothing and will report that nothing matched. Read that zero as "+
					"an absence of input, not as a plan cache holding no profiled plan.",
				s.Path, u.Name)
		}
		return []any{sql.Named("qs_query_ids", joinInt64(ids))}, note
	}
	return nil, ""
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
