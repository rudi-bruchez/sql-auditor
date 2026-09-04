package collect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ToolInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

type RunInfo struct {
	StartedUTC  string `json:"started_utc"`
	FinishedUTC string `json:"finished_utc"`
	DurationSec int    `json:"duration_sec"`
	ExitCode    int    `json:"exit_code"`
	// Cancelled marks a run the operator stopped. It matters because such a
	// run still produces its archive: without this field a short archive and a
	// complete one are indistinguishable, and exit_code 0 would read as "the
	// instance simply had little to give". Omitted when false so every
	// manifest written before this field existed stays byte-identical.
	Cancelled bool `json:"cancelled,omitempty"`
}

type ServerBlock struct {
	Name             string `json:"name"`
	Version          string `json:"version"`
	Edition          string `json:"edition"`
	Auth             string `json:"auth"`
	UTCOffsetMinutes int    `json:"utc_offset_minutes"`
}

// SourceInfo records where a corpus came from and what it hashed to, so an
// audit can state which questions were asked and prove they were the published
// ones.
type SourceInfo struct {
	From   string `json:"from"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type TargetBlock struct {
	Databases []DatabaseFolder `json:"databases"`
	Skipped   []SkipReason     `json:"skipped"`
}

type ResultEntry struct {
	Script     string `json:"script"`
	Scope      string `json:"scope"`
	Target     string `json:"target"`
	Output     string `json:"output"`
	Bytes      int    `json:"bytes"`
	DurationMS int    `json:"duration_ms"`
	Status     string `json:"status"`
}

// SkippedScript records a collector that was deliberately not run, and why.
//
// The three reasons — a permission the login was refused, a server too old for
// the query, an opt-in flag left off — are not errors and must not set exit 2.
// A degraded run is a success. But an absent output file is indistinguishable
// from a collector that never existed unless the omission is written down, so
// the analysis layer would read "not collected" as "nothing there", which is
// the same silent failure CoverageBlock exists to prevent.
type SkippedScript struct {
	Script string `json:"script"`
	// Target names the database the skip applies to, when the skip is per
	// database rather than per script. Without it, N databases filtered out by
	// QUERY_STORE_DB_INCLUDE produce N identical lines naming none of them, and
	// a Query Store found switched off in one database is unreportable. It is
	// omitempty, so every skip that is about the script alone is unchanged.
	Target string `json:"target,omitempty"`
	Reason string `json:"reason"`
}

type ErrorEntry struct {
	Script   string `json:"script"`
	Target   string `json:"target"`
	Message  string `json:"message"`
	SQLError int    `json:"sql_error"`
}

// CoverageBlock is the verdict on whether this archive describes the whole
// instance. It exists because the interesting failure is silent.
//
// A login without VIEW ANY DEFINITION is not refused by SQL Server: metadata
// visibility filters catalog views row by row, so sys.databases simply returns
// fewer rows. Measured live, it dropped from 4 rows to 2, and since database
// selection keeps only database_id > 4, such a login yields zero user
// databases while every query reports success. Nothing in Results, Errors or
// Warnings would show it. Without this block, an analysis layer reading an
// empty Targets.Databases would conclude the instance hosts no databases.
//
// Status is one of:
//
//	"complete"   every probed capability was available
//	"incomplete" at least one was denied or unanswered; data is missing
//	"unknown"    no preflight was recorded, so no claim can be made
//
// DatabaseListMayBeIncomplete is the decisive flag and is set only on
// evidence: the view_any_definition probe did not come back "ok". False means
// "no reason to doubt the list", never "the list was verified".
type CoverageBlock struct {
	Status                      string   `json:"status"`
	DatabaseListMayBeIncomplete bool     `json:"database_list_may_be_incomplete"`
	Denied                      []string `json:"denied_capabilities"`
	Notes                       []string `json:"notes"`
}

// CollectedKinds records categories of content that are not always present and
// that change what the archive discloses. The manifest's disclosure paragraph
// is written from this, not from a constant: MANIFEST.txt is the document a
// DBA shows their security team, so a claim in it that does not match what the
// run actually collected is the worst defect this file can have.
//
// The zero value is the narrow, safe wording. A collector that starts
// gathering something more revealing has to say so here to be honest, and
// forgetting to set the flag cannot silently widen an existing claim.
type CollectedKinds struct {
	// SessionText marks that statement text captured from running sessions is
	// in the archive — sys.dm_exec_sql_text and the session's login, host and
	// program names. That text is the verbatim SQL of live batches and can
	// carry literals copied out of application tables, so it is the one thing
	// the collector gathers that is not purely metadata.
	SessionText bool `json:"session_text"`

	// QueryStoreDetail marks that the full text and the execution plans of the
	// heaviest Query Store queries are in the archive. A plan carries more than
	// the statement: compiled parameter values, literal predicates and the
	// names of every object it touches. It is therefore a wider disclosure than
	// SessionText, and it has its own flag rather than sharing one.
	QueryStoreDetail bool `json:"query_store_detail"`

	// QueryStoreProfiledPlans marks that the last profiled plan was looked up
	// as well. It is separate because the lookup is not: it reads the plan
	// cache of the whole instance through sys.dm_exec_query_stats, where every
	// other per-database collector sees only the database it was pointed at.
	QueryStoreProfiledPlans bool `json:"query_store_profiled_plans"`

	// ObjectDefinitions marks that the source of views, procedures, functions
	// and triggers is in the archive. It is the client's own code rather than
	// anything the server derived: it can name linked servers and their
	// addresses, embed literals, and — in the old procedures this exports
	// alongside the new — carry a credential in clear inside an OPENQUERY.
	ObjectDefinitions bool `json:"object_definitions"`

	// DeadlockGraphs marks that deadlock reports are in the archive. A graph
	// carries the verbatim SQL of both victims, so it is the same class of
	// content as SessionText reached from a different source: the always-on
	// system_health ring buffer rather than a live session.
	DeadlockGraphs bool `json:"deadlock_graphs"`

	// BlockedProcessReports marks that blocked process reports are in the
	// archive. Like a deadlock graph it carries the SQL of the sessions
	// involved — including the blocker's, which is a session doing nothing
	// wrong — and unlike everything else in this corpus it was read off the
	// file system rather than out of a view.
	BlockedProcessReports bool `json:"blocked_process_reports"`
}

type Manifest struct {
	Tool      ToolInfo              `json:"tool"`
	Run       RunInfo               `json:"run"`
	Server    ServerBlock           `json:"server"`
	Config    map[string]string     `json:"config"`
	Sources   map[string]SourceInfo `json:"sources"`
	Preflight []CapabilityCheck     `json:"preflight"`
	Coverage  CoverageBlock         `json:"coverage"`
	Collected CollectedKinds        `json:"collected"`
	Targets   TargetBlock           `json:"targets"`
	Results   []ResultEntry         `json:"results"`
	Skipped   []SkippedScript       `json:"skipped_scripts"`
	Warnings  []string              `json:"warnings"`
	Errors    []ErrorEntry          `json:"errors"`
}

// NewManifest starts a manifest with the tool identity and a start timestamp
// already recorded, so a run that dies early still produces a document that
// says when it was attempted and by what.
func NewManifest(name, version, commit string) *Manifest {
	return &Manifest{
		Tool: ToolInfo{Name: name, Version: version, Commit: commit},
		Run:  RunInfo{StartedUTC: nowUTC()},
	}
}

const (
	manifestJSONName  = "_run.json"
	manifestHumanName = "MANIFEST.txt"
)

// WriteJSON writes _run.json into runFolder. Coverage is recomputed first: it
// is derived from Preflight, and a stale verdict is worse than none.
func (m *Manifest) WriteJSON(runFolder string) error {
	m.refreshCoverage()
	b, err := m.marshalJSON()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(runFolder, manifestJSONName), b, 0o644)
}

// WriteHuman writes MANIFEST.txt into runFolder.
func (m *Manifest) WriteHuman(runFolder string) error {
	return os.WriteFile(filepath.Join(runFolder, manifestHumanName), []byte(m.Human()), 0o644)
}

// marshalJSON renders the manifest with the configuration redacted. The
// redaction works on a copy: the same Manifest is written more than once in a
// run, and a caller's map must not be quietly emptied by the first write.
func (m *Manifest) marshalJSON() ([]byte, error) {
	safe := *m
	safe.Config = redactConfig(m.Config)
	return json.MarshalIndent(&safe, "", "  ")
}

// secretKeyHints are the substrings that mark a configuration key whose value
// must never leave the client's site inside the archive.
var secretKeyHints = []string{"password", "passwd", "pwd", "secret", "token", "credential", "apikey", "api_key", "connectionstring", "connection_string"}

func redactConfig(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		lower := strings.ToLower(k)
		redacted := false
		for _, hint := range secretKeyHints {
			if strings.Contains(lower, hint) {
				redacted = true
				break
			}
		}
		if redacted && v != "" {
			out[k] = "(redacted)"
		} else {
			out[k] = v
		}
	}
	return out
}

// WriteManifestWithFallback honours the rule that a manifest exists for every
// run. When the run folder is unwritable — which is exactly the fatal path
// where a manifest matters most — it falls back to a temporary directory and
// returns where it landed.
//
// MANIFEST.txt is written alongside in both cases. A failure to write it is
// reported on progress but is not fatal: _run.json carries a superset of the
// same facts, and refusing the whole write because the prose copy failed would
// throw away the record of the run.
//
// progress is where this chain narrates itself; it is os.Stderr for every
// command-line caller. It is a parameter rather than a package-level default
// because the caller that most needs to read this narration — one that has
// taken over the terminal — is exactly the one that cannot see stderr.
func WriteManifestWithFallback(m *Manifest, runFolder string, progress io.Writer) (string, error) {
	if err := m.WriteJSON(runFolder); err == nil {
		if herr := m.WriteHuman(runFolder); herr != nil {
			fmt.Fprintf(progress, "warning: could not write %s: %v\n", manifestHumanName, herr)
		}
		return filepath.Join(runFolder, manifestJSONName), nil
	}
	b, err := m.marshalJSON()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "sql-auditor-run-*")
	if err != nil {
		return "", lastResort(b, err, progress)
	}
	path := filepath.Join(dir, manifestJSONName)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", lastResort(b, err, progress)
	}
	if herr := m.WriteHuman(dir); herr != nil {
		// progress, like its twin on the successful branch above. On stderr it
		// would land in the middle of a painted frame under a caller that owns
		// the terminal, shifting every line below it — which is the one thing
		// this parameter exists to prevent.
		fmt.Fprintf(progress, "warning: could not write %s: %v\n", manifestHumanName, herr)
	}
	fmt.Fprintf(progress, "output directory unwritable; manifest written to %s\n", path)
	return path, nil
}

// lastResort prints the manifest to progress when no filesystem the process
// can reach will take it. Both the run folder and the temp directory being
// unwritable is a strange machine, but the point of the fallback chain is that
// a run always leaves a record, and a record on the operator's terminal can
// still be copied out. Returning the error alone would leave nothing anywhere.
//
// This is the reason progress must never be io.Discard: on this path what it
// receives is the only copy of the manifest in existence. A caller that
// substitutes a buffer owes it a flush to somewhere durable.
func lastResort(manifestJSON []byte, cause error, progress io.Writer) error {
	// "it follows", not "it follows on stderr": progress is stderr only for the
	// command-line callers. Under the wizard it is an in-memory buffer flushed
	// to a file, and naming a stream the reader is not looking at would send
	// them to the wrong place for the only copy of the manifest.
	fmt.Fprintf(progress, "cannot write the manifest anywhere (%v); it follows:\n%s\n", cause, manifestJSON)
	return cause
}

// refreshCoverage derives Coverage from Preflight. It is idempotent and is
// called before every write, so the two can never disagree.
func (m *Manifest) refreshCoverage() {
	c := CoverageBlock{Status: "unknown"}
	if len(m.Preflight) == 0 {
		m.Coverage = c
		return
	}
	c.Status = "complete"
	for _, chk := range m.Preflight {
		if chk.Status == "ok" {
			continue
		}
		c.Status = "incomplete"
		c.Denied = append(c.Denied, chk.Name)
		if chk.Name == "view_any_definition" {
			// The list is untrustworthy either way, but the reason differs and
			// the manifest must not report a dropped connection as a refused
			// permission: that sends a DBA hunting for a GRANT that was never
			// the problem.
			c.DatabaseListMayBeIncomplete = true
			if chk.Status == "denied" {
				c.Notes = append(c.Notes, "VIEW ANY DEFINITION was refused for this login. SQL Server does not raise on that: catalog views silently return fewer rows, so the list of databases in targets.databases may be short or empty. An empty list means \"not visible to this login\", not \"not present on this server\".")
			} else {
				c.Notes = append(c.Notes, "The view_any_definition probe got no answer, so it is not known whether this login could see every database. The list in targets.databases is what was collected before contact was lost, not necessarily what the instance holds.")
			}
		}
		if chk.Status == "error" {
			c.Notes = append(c.Notes, fmt.Sprintf("The %s probe got no answer from the server, which is a transport failure rather than a refusal: the collection stopped short of what the login was actually allowed to read.", chk.Name))
		}
	}
	m.Coverage = c
}

// Human renders MANIFEST.txt: what a DBA shows their security team to get the
// transfer approved. It has to answer, without the author present, which
// server this is, when it was read, what was read, and whether any business
// data is in the archive.
func (m *Manifest) Human() string {
	m.refreshCoverage()
	var b strings.Builder

	b.WriteString("sql-auditor collection\n")
	b.WriteString("======================\n\n")
	fmt.Fprintf(&b, "Server       : %s\n", orUnknown(m.Server.Name))
	fmt.Fprintf(&b, "Version      : %s\n", orUnknown(strings.TrimSpace(m.Server.Version+" "+m.Server.Edition)))
	// The timestamp keeps its RFC 3339 form but gets the zone named in words
	// beside it: the reader of this file does not necessarily know that the
	// trailing Z means UTC.
	collected := orUnknown(m.Run.StartedUTC)
	if m.Run.StartedUTC != "" {
		collected += " (UTC)"
	}
	fmt.Fprintf(&b, "Collected    : %s\n", collected)
	if m.Run.DurationSec > 0 {
		fmt.Fprintf(&b, "Duration     : %d s\n", m.Run.DurationSec)
	}
	if m.Server.Auth != "" {
		fmt.Fprintf(&b, "Authenticated: %s\n", m.Server.Auth)
	}
	tool := strings.TrimSpace(m.Tool.Name + " " + m.Tool.Version)
	if m.Tool.Commit != "" {
		tool = strings.TrimSpace(tool + " (" + m.Tool.Commit + ")")
	}
	fmt.Fprintf(&b, "Tool         : %s\n", orUnknown(tool))
	// How big is it and how many files — the first two things anyone asked to
	// approve a transfer wants to know, and the only way to tell at a glance
	// that the archive is the size a metadata collection should be.
	files, bytes := m.contents()
	fmt.Fprintf(&b, "Contents     : %d data files, %s\n", files, HumanBytes(bytes))

	m.writeDataNature(&b)
	m.writeCoverage(&b)
	m.writeTargets(&b)
	m.writeWhatWasRead(&b)
	m.writeNotRun(&b)
	m.writeProblems(&b)
	m.writeSources(&b)

	b.WriteString("\nThe machine-readable form of everything above, including the full list of\n")
	b.WriteString("output files, is in _run.json next to this file.\n")
	return b.String()
}

// writeDataNature is the paragraph the whole document exists to support: the
// one a security officer reads before releasing the archive. Every sentence in
// it has to be true of THIS run, which is why the disclosure list is driven by
// m.Collected and the provenance sentence by m.Sources rather than by prose
// fixed at compile time.
func (m *Manifest) writeDataNature(b *strings.Builder) {
	b.WriteString(`
What this archive contains
--------------------------
Server and database metadata, and performance counters: configuration
settings, database options, file layout and sizes, wait statistics, index
and backup metadata.

The collector issues only read-only SELECT statements against system
catalog views and dynamic management views, and it does not read any user
or application table. A few diagnostics exist only as a command rather
than a view — DBCC, sp_readerrorlog — and for those it runs the command
and captures its output into scratch storage of its own, in tempdb.

It creates no permanent object: nothing that belongs to this server or
its databases is created, altered or deleted, and no data of yours is
written anywhere by this tool.

What is in here that names things:
  - this server's name, version, edition and file paths
  - database, schema and object names
  - the Windows or SQL login names of database owners
`)
	if m.Collected.SessionText {
		b.WriteString(`  - the SQL text of statements running during collection, which may
    contain values from application tables, together with the login,
    host and program names of the sessions running them
`)
	}
	if m.Collected.QueryStoreDetail {
		fmt.Fprintln(b, "  - The full text of the heaviest queries recorded by the Query Store,")
		fmt.Fprintln(b, "    their execution plans in XML, and their runtime statistics per")
		fmt.Fprintln(b, "    interval. A plan carries the compiled parameter values, the literal")
		fmt.Fprintln(b, "    predicates and the name of every object the query touches.")
		fmt.Fprintln(b, "    Collected because --query-store-detail was passed.")
	}
	if m.Collected.QueryStoreProfiledPlans {
		fmt.Fprintln(b, "  - For those same queries, the last plan the engine still holds with")
		fmt.Fprintln(b, "    its real row counts. Finding it reads the plan cache of the whole")
		fmt.Fprintln(b, "    instance, not only the databases listed above; only the plans")
		fmt.Fprintln(b, "    belonging to those databases are kept.")
		fmt.Fprintln(b, "    Collected because --query-store-plan-stats was passed.")
	}
	if m.Collected.ObjectDefinitions {
		fmt.Fprintln(b, "  - The source of the database's views, stored procedures, functions and")
		fmt.Fprintln(b, "    triggers, one file each. This is code written here rather than")
		fmt.Fprintln(b, "    anything the server derived: it can name linked servers and their")
		fmt.Fprintln(b, "    addresses, embed values as literals, and carry a credential in clear")
		fmt.Fprintln(b, "    inside an OPENQUERY or an EXECUTE AS. Encrypted modules are listed")
		fmt.Fprintln(b, "    but their source is not, because the server does not return it.")
		fmt.Fprintln(b, "    Collected because --include-object-definitions was passed.")
	}
	if m.Collected.DeadlockGraphs {
		fmt.Fprintln(b, "  - The deadlock reports the system_health session still held, one")
		fmt.Fprintln(b, "    .xdl file each. A report names the two statements that deadlocked")
		fmt.Fprintln(b, "    and the resource they contended for, and it carries their SQL")
		fmt.Fprintln(b, "    verbatim — which can hold literals copied out of application")
		fmt.Fprintln(b, "    tables. Nothing on the instance was modified or cleared to read")
		fmt.Fprintln(b, "    them. Collected because --include-deadlock-graphs was passed.")
	}
	if m.Collected.BlockedProcessReports {
		fmt.Fprintln(b, "  - The blocked process reports captured by an Extended Events session")
		fmt.Fprintln(b, "    on this instance, one .xml file each. A report names the blocked")
		fmt.Fprintln(b, "    session and the session blocking it, with the SQL of both — the")
		fmt.Fprintln(b, "    blocker included, which was doing nothing but holding a lock.")
		fmt.Fprintln(b, "    These were read from the session's .xel files on the server's own")
		fmt.Fprintln(b, "    file system, by the SQL Server service account; no file was")
		fmt.Fprintln(b, "    modified, moved or removed.")
		fmt.Fprintln(b, "    Collected because --include-blocked-process-reports was passed.")
	}
	// Scoped deliberately. The redaction this describes applies to the run
	// settings block and to nothing else, because that is the only place the
	// collector masks anything. An unqualified "secrets are masked" would be a
	// claim about the whole archive that no code in this program enforces, and
	// it would turn into a false one the first time a collector reads a job
	// step or a linked-server definition.
	b.WriteString(`
The password of the login used for this run is recorded nowhere in this
archive. The run settings in _run.json are the query and output directories,
the database name filters, which optional collections were switched on, and
the window and per-database limits the Query Store extraction was given; any
setting whose name marks it as a password, token or other secret is replaced
with "(redacted)" before that block is written.
`)
	if m.Collected.SessionText || m.Collected.QueryStoreDetail || m.Collected.QueryStoreProfiledPlans ||
		m.Collected.ObjectDefinitions || m.Collected.DeadlockGraphs ||
		m.Collected.BlockedProcessReports {
		b.WriteString(`
Most of this is metadata about the estate rather than the data held in it,
but the captured statement text can carry values copied from application
tables, and the login names are attributable to people. Treat this archive
as potentially containing personal data and handle it on that basis.
`)
	} else {
		b.WriteString(`
That is metadata about the estate rather than the data held in it, but the
login names above are attributable to people, so treat this as internal
infrastructure documentation rather than public material.
`)
	}
	m.writeCorpusProvenance(b)
}

// writeCorpusProvenance says where the questions came from. The published-
// corpus claim is the reader's way of checking the paragraph above for
// themselves, so it must not be made when --queries-dir supplied a corpus this
// project has never seen.
func (m *Manifest) writeCorpusProvenance(b *strings.Builder) {
	src, ok := m.Sources["queries"]
	switch {
	case ok && src.From == "embedded":
		b.WriteString(`
Every query the collector runs is published at
github.com/rudi-bruchez/sql-auditor, and the exact corpus used for this run
can be written out with "sql-auditor queries export --to DIR" and read.
`)
	case ok:
		b.WriteString(`
The queries used for this run did not come from the published corpus: they
were supplied from a local directory, so their content is vouched for only
by the SHA-256 recorded under "Query corpus" below. Verify that hash against
the directory the run was given before relying on the description above.
`)
	}
}

func (m *Manifest) writeCoverage(b *strings.Builder) {
	b.WriteString("\nCoverage\n--------\n")
	switch m.Coverage.Status {
	case "complete":
		b.WriteString("COMPLETE - every permission the collector checks for was available to the\n")
		b.WriteString("login used for this run. Nothing below was omitted for want of a right.\n")
	case "unknown":
		b.WriteString("UNKNOWN - no permission check was recorded for this run, so this document\n")
		b.WriteString("cannot state whether the collector read everything it asked for. Treat the\n")
		b.WriteString("lists below as what was collected, not as what exists.\n")
	default:
		b.WriteString("INCOMPLETE - the login used for this run was refused, or got no answer for,\n")
		b.WriteString("some of what the collector needs. Parts of this instance were not read:\n\n")
		for _, chk := range m.Preflight {
			if chk.Status == "ok" {
				continue
			}
			state := "refused for this login"
			if chk.Status == "error" {
				state = "no answer - the instance was unreachable at that point"
			}
			// The label, not the identifier: this reader has never seen the
			// permission vocabulary. _run.json keeps the identifier.
			name := chk.Label
			if name == "" {
				name = chk.Name
			}
			fmt.Fprintf(b, "  - %s\n      %s\n", name, state)
			if chk.Impact != "" {
				fmt.Fprintf(b, "      consequence: %s\n", chk.Impact)
			}
		}
	}
	if m.Coverage.DatabaseListMayBeIncomplete {
		b.WriteString("\nWhy the database list below may be short or empty\n")
		b.WriteString("-------------------------------------------------\n")
		if m.checkStatus("view_any_definition") == "error" {
			b.WriteString(`The check for VIEW ANY DEFINITION got no answer from the server, so it is not
known whether this login could see every database on it. The connection failed;
the permission was not refused. Read the list below as what had been collected
when contact was lost, not as what this instance holds.
`)
		} else {
			b.WriteString(`Missing VIEW ANY DEFINITION does not produce an error in SQL Server. Metadata
visibility filters catalog views row by row, so a query on sys.databases still
succeeds and simply returns fewer rows: only the databases this login owns or
is mapped to. Because system databases are excluded from collection, such a
login can end up with no user databases at all while every query reports
success.

So read an empty or short list below as "not visible to this login", never as
"not present on this server". What this instance actually hosts cannot be
determined from this archive. Re-run with VIEW ANY DEFINITION granted at the
server level to get a complete picture.
`)
		}
	}
}

func (m *Manifest) checkStatus(name string) string {
	for _, chk := range m.Preflight {
		if chk.Name == name {
			return chk.Status
		}
	}
	return ""
}

func (m *Manifest) writeTargets(b *strings.Builder) {
	// Coverage UNKNOWN means the run never got far enough to probe anything,
	// so it never listed the databases either. "(none matched the selection
	// for this run)" would be a statement about an instance that was never
	// contacted — a run against a dead port printed exactly that. An empty
	// list is only a finding once there is a connection behind it.
	switch {
	case m.Coverage.Status == "unknown" && len(m.Targets.Databases) == 0:
		b.WriteString("\nDatabases covered\n-----------------\n")
		b.WriteString("No database list was collected. This run recorded no permission check,\n")
		b.WriteString("so nothing here describes what this instance holds or does not hold.\n")
		b.WriteString("See the Coverage section above.\n")
	case len(m.Targets.Databases) > 0:
		fmt.Fprintf(b, "\nDatabases covered (%d):\n", len(m.Targets.Databases))
		for _, d := range m.Targets.Databases {
			fmt.Fprintf(b, "  - %s\n", d.Name)
			// A database the operator's selection did not name is here for a
			// reason, and that reason belongs beside it. A reader comparing
			// this list to the DB_INCLUDE they wrote would otherwise find one
			// name too many and no account of it.
			if d.RetentionReason != "" {
				fmt.Fprintf(b, "      kept because: %s\n", d.RetentionReason)
			}
		}
	case m.Coverage.DatabaseListMayBeIncomplete:
		b.WriteString("\nDatabases covered (0):\n")
		b.WriteString("  (none were visible to this login - see the section above)\n")
	default:
		b.WriteString("\nDatabases covered (0):\n")
		b.WriteString("  (none matched the selection for this run)\n")
	}
	if len(m.Targets.Skipped) > 0 {
		fmt.Fprintf(b, "\nDatabases skipped (%d):\n", len(m.Targets.Skipped))
		for _, s := range m.Targets.Skipped {
			fmt.Fprintf(b, "  - %s (%s)\n", s.Name, s.Reason)
		}
	}
}

// writeWhatWasRead lists each query once with the number of times it ran,
// rather than one line per database. A per-database corpus over twenty
// databases would otherwise bury the document in hundreds of repeated lines
// and nobody would read to the end.
func (m *Manifest) writeWhatWasRead(b *strings.Builder) {
	// There is no failure tally. Results holds successes only — runUnit is its
	// sole producer and appends nothing on the error path, which goes to
	// Errors instead — so a "%d failed" count here could never be anything but
	// zero, and a line that cannot fire is a line nobody maintains. Failures
	// are reported under Errors below.
	type tally struct {
		scope string
		runs  int
	}
	byScript := map[string]*tally{}
	var order []string
	for _, r := range m.Results {
		t, ok := byScript[r.Script]
		if !ok {
			t = &tally{scope: r.Scope}
			byScript[r.Script] = t
			order = append(order, r.Script)
		}
		t.runs++
	}
	sort.Strings(order)
	fmt.Fprintf(b, "\nQueries run (%d distinct, %d executions):\n", len(order), len(m.Results))
	if len(order) == 0 {
		b.WriteString("  (none)\n")
		return
	}
	for _, name := range order {
		t := byScript[name]
		line := "  - " + name
		if t.scope != "" {
			line += " [" + t.scope + "]"
		}
		if t.runs > 1 {
			line += fmt.Sprintf(" x%d", t.runs)
		}
		b.WriteString(line + "\n")
	}
}

// writeNotRun lists the collectors that were deliberately skipped. It sits
// next to the list of what ran because the two are read together: a reader
// asking why a section is missing from the report gets the answer here rather
// than concluding the tool is broken.
func (m *Manifest) writeNotRun(b *strings.Builder) {
	if len(m.Skipped) == 0 {
		return
	}
	fmt.Fprintf(b, "\nQueries not run (%d):\n", len(m.Skipped))
	for _, s := range m.Skipped {
		if s.Target != "" {
			fmt.Fprintf(b, "  - %s on %s\n      %s\n", s.Script, s.Target, s.Reason)
			continue
		}
		fmt.Fprintf(b, "  - %s\n      %s\n", s.Script, s.Reason)
	}
}

func (m *Manifest) writeProblems(b *strings.Builder) {
	if len(m.Errors) > 0 {
		fmt.Fprintf(b, "\nErrors (%d):\n", len(m.Errors))
		for _, e := range m.Errors {
			// An error raised by the run itself rather than by a collector has
			// no script, and the "<script> on <target>:" form then renders as
			// "  -  on instance: ..." — a double space and a dangling "on".
			// Every fatal-path manifest is made of exactly those errors, and
			// those are the manifests that get mailed back.
			switch {
			case e.Script == "":
				fmt.Fprintf(b, "  - %s\n", e.Message)
			case e.Target == "":
				fmt.Fprintf(b, "  - %s on instance: %s\n", e.Script, e.Message)
			default:
				fmt.Fprintf(b, "  - %s on %s: %s\n", e.Script, e.Target, e.Message)
			}
		}
	}
	if len(m.Warnings) > 0 {
		fmt.Fprintf(b, "\nWarnings (%d):\n", len(m.Warnings))
		for _, w := range m.Warnings {
			fmt.Fprintf(b, "  - %s\n", w)
		}
	}
}

func (m *Manifest) writeSources(b *strings.Builder) {
	if len(m.Sources) == 0 {
		return
	}
	names := make([]string, 0, len(m.Sources))
	for n := range m.Sources {
		names = append(names, n)
	}
	sort.Strings(names)
	b.WriteString("\nQuery corpus\n------------\n")
	for _, n := range names {
		s := m.Sources[n]
		fmt.Fprintf(b, "  %s: from=%s path=%s\n    sha256=%s\n", n, orUnknown(s.From), orUnknown(s.Path), orUnknown(s.SHA256))
	}
}

// contents counts the distinct output files and their total size. Distinct,
// because a script that writes one document per database has one Results entry
// per database but a run that appended to the same file would otherwise be
// counted twice. The counts exclude _run.json and MANIFEST.txt themselves,
// which are the two files describing the rest.
func (m *Manifest) contents() (files int, bytes int64) {
	seen := map[string]bool{}
	for _, r := range m.Results {
		if r.Output == "" || seen[r.Output] {
			continue
		}
		seen[r.Output] = true
		files++
		bytes += int64(r.Bytes)
	}
	return files, bytes
}

// HumanBytes spells a size the way the manifest and the wizard's screens both
// show it. Exported for the wizard: the archive and the screen must not report
// two different sizes for the same file, and a second copy of this body is how
// that happens.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d bytes", n)
	}
	// The exponent stops at the last letter there is. A size read from an
	// unbounded os.Stat would otherwise index "KMGT"[4] and panic — in the
	// renderer, which is documented as total on its inputs because a panic
	// there leaves the terminal in raw mode with no wizard left to restore it.
	// A petabyte archive then reads as "1024.0 TB", which is true.
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < len("KMGT")-1; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(not recorded)"
	}
	return s
}

// CorpusSHA256 hashes the corpus so an audit can state which questions were
// asked, and whether they came from the binary or from --queries-dir.
//
// Paths are made relative to root before hashing. The embedded corpus is
// reached as "queries/..." and an external one through os.DirFS as "...", and
// the hash is worthless for comparing the two if the root it was reached
// through changes the result.
//
// Each entry is framed as "<len(path)> <path> <len(content)>\n<content>", so a
// rename changes the hash and no two different corpora can produce the same
// byte stream. Both lengths are needed for that. Length-prefixing the content
// alone is not injective, because a path is free to contain the separator and
// the digits: {"a": "", "b": ""} and {"a 0\nb": ""} both frame to "a 0\nb 0\n".
func CorpusSHA256(fsys fs.FS, root string) (string, error) {
	var paths []string
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return "", err
		}
		rel := relativeTo(root, p)
		fmt.Fprintf(h, "%d %s %d\n", len(rel), rel, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func relativeTo(root, p string) string {
	if root == "" || root == "." {
		return p
	}
	return strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }
