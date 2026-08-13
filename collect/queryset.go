package collect

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Scope int

const (
	ScopeInstance Scope = iota
	ScopeDatabase
)

type Script struct {
	Path, Dir, Base string
	SQL             string
	Scope           Scope
	TimeoutSec      int
	Results         []ResultSpec
	Permissions     []string
	// MinVersion is the dotted ProductVersion prefix below which this script
	// must not run. nil means ungated.
	MinVersion []int
	// RequiresFlag names an opt-in the operator must have switched on for this
	// script to run. Empty means the script always runs. It exists for
	// collectors whose output is more revealing than metadata — the archive's
	// disclosure paragraph is written from the same decision — so the default
	// has to be "not collected" and the gate has to be visible in the file
	// itself rather than in a list held somewhere else.
	RequiresFlag string
	// Writer names the Go writer that turns this script's result sets into
	// files, instead of the one JSON document every other collector produces.
	// Empty means the ordinary encoder. The vocabulary is closed for the same
	// reason @requires_flag's is: a misspelt name would silently fall back to
	// the encoder and emit a single JSON file where a directory of plans was
	// expected, with nothing anywhere saying so.
	Writer    string
	LintError string
}

// KnownFlags is the closed set of names @requires_flag accepts. A typo would
// otherwise gate a script on a flag no command line can ever set, silently
// dropping a collector from every run.
var KnownFlags = map[string]string{
	"include_session_text":   "--include-session-text",
	"estimate_compression":   "--estimate-compression",
	"query_store_detail":     "--query-store-detail",
	"query_store_plan_stats": "--query-store-plan-stats",
	"object_definitions":     "--include-object-definitions",
}

// KnownWriters is the closed set of names @writer accepts, mapped to the
// one-line description `check` prints. scriptNote reads it; a description no
// command ever shows would be a comment pretending to be data.
var KnownWriters = map[string]string{
	"query-store-detail":   "one directory per database: query text, plans and per-interval statistics",
	"query-store-profiled": "the last profiled plan, when the instance still holds one",
	"object-definitions":   "one directory per database: the source of each view, procedure, function and trigger",
}

var (
	// NN.area/NNN.name.sql — anything else is an editor dropping or a mistake.
	dirPattern  = regexp.MustCompile(`^\d{2}\.[a-z0-9-]+$`)
	filePattern = regexp.MustCompile(`^\d{3}\.[a-z0-9-]+\.sql$`)
	// Matched only against StripSQLComments output, where -- comments no
	// longer exist, so no trailing-comment allowance is needed here.
	goPattern = regexp.MustCompile(`(?im)^\s*GO\s*$`)
	forJSON   = regexp.MustCompile(`(?i)\bFOR\s+JSON\b`)
	directive = regexp.MustCompile(`^\s*--\s*@(\w+)\s*:?\s*(.*)$`)

	// The auditor contract, as patterns. Whitespace is loose because the
	// contract is about what the server is asked to do, not about how the
	// author laid the statement out.
	nocountPattern    = regexp.MustCompile(`(?i)\bSET\s+NOCOUNT\s+ON\b`)
	isolationPattern  = regexp.MustCompile(`(?i)\bSET\s+TRANSACTION\s+ISOLATION\s+LEVEL\s+READ\s+UNCOMMITTED\b`)
	queryHintPattern  = regexp.MustCompile(`(?i)\bOPTION\s*\(\s*RECOMPILE\s*,\s*MAXDOP\s+1\s*\)`)
	optionWordPattern = regexp.MustCompile(`(?i)\bOPTION\s*\(`)
)

// Discover walks root, parses header directives and lints each file. Lint
// failures are recorded on the Script rather than returned, so one bad file
// does not hide the rest; only structural problems with the directory return
// an error.
func Discover(fsys fs.FS, root string) ([]Script, error) {
	var scripts []Script
	var stray []string

	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepathRel(root, p)
		if d.IsDir() {
			if rel != "." && !dirPattern.MatchString(rel) {
				stray = append(stray, p)
			}
			return nil
		}
		// A file must match NNN.name.sql AND sit directly inside an
		// NN.area directory — a file at the query root (parent ".") or
		// under a misnamed directory is a stray, not a collector.
		if !filePattern.MatchString(path.Base(p)) || !dirPattern.MatchString(path.Dir(rel)) {
			stray = append(stray, p)
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		scripts = append(scripts, parseScript(rel, string(b)))
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(stray) > 0 {
		sort.Strings(stray)
		return nil, fmt.Errorf("unexpected file(s) under %s: %s — expected NN.area/NNN.name.sql",
			root, strings.Join(stray, ", "))
	}
	sort.Slice(scripts, func(i, j int) bool { return scripts[i].Path < scripts[j].Path })
	return scripts, nil
}

func filepathRel(root, p string) (string, error) {
	if p == root {
		return ".", nil
	}
	return strings.TrimPrefix(p, root+"/"), nil
}

func parseScript(rel, sql string) Script {
	s := Script{
		Path: rel,
		Dir:  path.Dir(rel),
		Base: strings.TrimSuffix(path.Base(rel), ".sql"),
		SQL:  sql,
	}
	// setLint keeps the FIRST problem in document order, so fixing a header
	// top-down converges instead of uncovering one error at a time.
	setLint := func(msg string) {
		if s.LintError == "" {
			s.LintError = msg
		}
	}
	for _, line := range strings.Split(sql, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "--") {
			break // header region ends at the first non-comment line
		}
		m := directive.FindStringSubmatch(t)
		if m == nil {
			continue
		}
		key, val := strings.ToLower(m[1]), strings.TrimSpace(m[2])
		switch key {
		case "scope":
			// Linted, not shrugged off. "@scope: databases" used to fall
			// through to the instance default, so a per-database collector ran
			// once against master and produced one file where there should have
			// been one per database — with nothing anywhere saying so.
			switch val {
			case "database":
				s.Scope = ScopeDatabase
			case "instance":
				s.Scope = ScopeInstance
			default:
				setLint(fmt.Sprintf("@scope: unknown scope %q; expected instance or database", val))
			}
		case "timeout":
			// Also linted. A misspelt timeout silently reverted to
			// SQL_QUERY_TIMEOUT_SEC, so the one collector that needs longer
			// than the default was cut off by it.
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				setLint(fmt.Sprintf("@timeout: %q is not a positive whole number of seconds", val))
				continue
			}
			s.TimeoutSec = n
		case "permissions":
			for _, p := range strings.Split(val, ",") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				key, ok := NormalisePermission(p)
				if !ok {
					setLint(fmt.Sprintf("@permissions: unknown permission %q; "+
						"expected one of VIEW SERVER STATE, VIEW ANY DEFINITION, MSDB READ, AGENT JOBS, AGENT JOB STEPS, ERROR LOG, CONNECT", p))
					continue
				}
				s.Permissions = append(s.Permissions, key)
			}
		case "resultsets":
			specs, err := parseResultSets(val)
			if err != nil {
				setLint(err.Error())
			}
			s.Results = specs
		case "min_version":
			v := ParseVersion(val)
			if v == nil {
				setLint(fmt.Sprintf("@min_version: %q is not a dotted version number", val))
				continue
			}
			s.MinVersion = v
		case "requires_flag":
			name := strings.ToLower(strings.TrimSpace(val))
			if _, ok := KnownFlags[name]; !ok {
				setLint(fmt.Sprintf("@requires_flag: unknown flag %q; expected one of %s",
					val, strings.Join(knownFlagNames(), ", ")))
				continue
			}
			s.RequiresFlag = name
		case "writer":
			name := strings.ToLower(strings.TrimSpace(val))
			if _, ok := KnownWriters[name]; !ok {
				setLint(fmt.Sprintf("@writer: unknown writer %q; expected one of %s",
					val, strings.Join(knownWriterNames(), ", ")))
				continue
			}
			s.Writer = name
		case "correlated":
			setLint("correlated result sets are not supported: a result set must not " +
				"reference a column of another; split it into its own query")
		}
	}
	// A writer needs a database to write for. An instance-scope @writer script
	// reaches runUnit with an empty DatabaseFolder: the empty Folder collapses
	// its directory onto the non-per-database path, and the empty Name makes
	// QueryStoreState.Selected[""] a bucket every such script would share. Both
	// are silent, and neither is what anyone declaring @writer meant.
	if s.Writer != "" && s.Scope != ScopeDatabase {
		setLint(fmt.Sprintf("@writer: %q needs @scope: database; a writer produces one "+
			"directory per database and has nowhere to put it otherwise", s.Writer))
	}
	if s.LintError == "" {
		s.LintError = lint(sql, s.Results)
	}
	return s
}

func knownFlagNames() []string {
	names := make([]string, 0, len(KnownFlags))
	for n := range KnownFlags {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func knownWriterNames() []string {
	names := make([]string, 0, len(KnownWriters))
	for n := range KnownWriters {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ParseVersion splits a dotted version into components. It returns nil for
// anything that is not a dotted run of integers, so a malformed directive is
// distinguishable from an absent one.
func ParseVersion(s string) []int {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// VersionAtLeast reports whether have >= want, comparing component by
// component. A shorter want is a prefix gate: want 12 is satisfied by any
// 12.x.y. An empty want means ungated.
func VersionAtLeast(have, want []int) bool {
	if len(want) == 0 {
		return true
	}
	for i, w := range want {
		if i >= len(have) {
			return false // have is shorter and equal so far: 13 vs 13.0.5026
		}
		if have[i] != w {
			return have[i] > w
		}
	}
	return true
}

// permissionKeys maps what a query author writes to the capability keys the
// preflight uses. One vocabulary, checked at discovery, so a denied capability
// can be matched to the scripts that need it.
var permissionKeys = map[string]string{
	"connect":             "connect",
	"view server state":   "view_server_state",
	"view any definition": "view_any_definition",
	"msdb read":           "msdb_read",
	"agent jobs":          "agent_jobs",
	"agent job steps":     "agent_job_steps",
	"error log":           "error_log",
}

func NormalisePermission(s string) (string, bool) {
	k, ok := permissionKeys[strings.ToLower(strings.Join(strings.Fields(s), " "))]
	return k, ok
}

// StripSQLComments removes -- line comments and /* */ blocks — including
// nested block comments, which SQL Server allows — while leaving string
// literals untouched, so the GO and FOR JSON lints cannot fire on prose that
// merely mentions them. Doubled quotes inside a literal are handled. An
// unterminated block comment or string literal is not an error here; the
// text before it is kept and stripping simply stops.
func StripSQLComments(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	const (
		code = iota
		lineComment
		blockComment
		literal
	)
	state := code
	blockDepth := 0
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch state {
		case code:
			switch {
			case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
				state = lineComment
				i++
			case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
				state = blockComment
				blockDepth = 1
				i++
			case c == '\'':
				state = literal
				b.WriteByte(c)
			default:
				b.WriteByte(c)
			}
		case lineComment:
			if c == '\n' {
				state = code
				b.WriteByte(c)
			}
		case blockComment:
			switch {
			case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
				blockDepth++
				i++
			case c == '*' && i+1 < len(sql) && sql[i+1] == '/':
				blockDepth--
				i++
				if blockDepth == 0 {
					state = code
				}
			}
		case literal:
			b.WriteByte(c)
			if c == '\'' {
				if i+1 < len(sql) && sql[i+1] == '\'' {
					b.WriteByte('\'')
					i++
					continue
				}
				state = code
			}
		}
	}
	return b.String()
}

func parseResultSets(val string) ([]ResultSpec, error) {
	var out []ResultSpec
	for _, part := range strings.Split(val, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, shape, ok := strings.Cut(part, ":")
		if !ok {
			return out, fmt.Errorf("@resultsets entry %q must be name:object or name:array", part)
		}
		name, shape = strings.TrimSpace(name), strings.TrimSpace(shape)
		switch shape {
		case "object":
			out = append(out, ResultSpec{name, ShapeObject})
		case "array":
			if name == RootSetName {
				return out, fmt.Errorf("@resultsets: %q merges into the document top level "+
					"and must be declared object, not array", RootSetName)
			}
			out = append(out, ResultSpec{name, ShapeArray})
		default:
			return out, fmt.Errorf("@resultsets entry %q: unknown shape %q, want object or array", part, shape)
		}
	}
	return out, nil
}

func lint(sql string, results []ResultSpec) string {
	// Lint the code, not the prose: a collector may legitimately carry a
	// comment explaining why FOR JSON was removed.
	stripped := StripSQLComments(sql)
	if goPattern.MatchString(stripped) {
		return "GO is a client-side batch separator and cannot be sent to the server; remove it"
	}
	if forJSON.MatchString(stripped) {
		return "FOR JSON is not supported: JSON is built client-side so the collector works on SQL Server 2012"
	}
	if len(results) == 0 {
		return "missing the @resultsets directive; declare each result set as name:object or name:array"
	}
	return contractLint(stripped, results)
}

// contractLint enforces the auditor contract: every collector runs with
// NOCOUNT on, at READ UNCOMMITTED, and with OPTION (RECOMPILE, MAXDOP 1) on
// every statement that returns rows.
//
// It exists because the fourteen files in the corpus comply by review rather
// than by rule, and review does not scale to the fifteenth. The rule is the
// point of the contract: NOCOUNT keeps rowcount messages out of the result
// stream the encoder walks, READ UNCOMMITTED means the collector never waits
// on — or blocks — a production workload, and RECOMPILE with MAXDOP 1 keeps a
// diagnostic query from poisoning the plan cache or taking a parallel worker
// off the instance it is auditing.
//
// The OPTION check is an approximation and is deliberately a weak one: telling
// a rowset-producing statement from an assignment or a DECLARE needs a SQL
// parser, which this program does not have. Instead it requires at least as
// many OPTION (RECOMPILE, MAXDOP 1) clauses as there are declared result sets.
// That cannot pass a file which omits one from a SELECT — every result set
// needs its own — but it can pass a file that puts two hints on one statement
// and none on another. It catches the omission that actually happens: an
// author appending a result set and forgetting the hint.
//
// sql must already have had its comments stripped, or a file explaining why it
// removed a hint would satisfy the check by talking about it.
func contractLint(sql string, results []ResultSpec) string {
	if !nocountPattern.MatchString(sql) {
		return "missing SET NOCOUNT ON: rowcount messages travel in the result stream and the collector must not emit them"
	}
	if !isolationPattern.MatchString(sql) {
		return "missing SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED: a collector must never block or be blocked by the workload it audits"
	}
	hints := len(queryHintPattern.FindAllString(sql, -1))
	if hints >= len(results) {
		return ""
	}
	// A malformed hint is the likelier mistake once one is present at all, so
	// say which of the two problems this is.
	if len(optionWordPattern.FindAllString(sql, -1)) > hints {
		return fmt.Sprintf("every result set needs OPTION (RECOMPILE, MAXDOP 1) written exactly so; "+
			"found %d of the %d declared in @resultsets, and at least one OPTION clause in another form",
			hints, len(results))
	}
	return fmt.Sprintf("every result set needs OPTION (RECOMPILE, MAXDOP 1); found %d for the %d declared in @resultsets",
		hints, len(results))
}
