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
	LintError       string
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
			if val == "database" {
				s.Scope = ScopeDatabase
			}
		case "timeout":
			if n, err := strconv.Atoi(val); err == nil {
				s.TimeoutSec = n
			}
		case "permissions":
			for _, p := range strings.Split(val, ",") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				key, ok := NormalisePermission(p)
				if !ok {
					if s.LintError == "" {
						s.LintError = fmt.Sprintf("@permissions: unknown permission %q; "+
							"expected one of VIEW SERVER STATE, VIEW ANY DEFINITION, MSDB READ, CONNECT", p)
					}
					continue
				}
				s.Permissions = append(s.Permissions, key)
			}
		case "resultsets":
			specs, err := parseResultSets(val)
			if err != nil && s.LintError == "" {
				s.LintError = err.Error()
			}
			s.Results = specs
		case "correlated":
			if s.LintError == "" {
				s.LintError = "correlated result sets are not supported: a result set must not " +
					"reference a column of another; split it into its own query"
			}
		}
	}
	if s.LintError == "" {
		s.LintError = lint(sql, s.Results)
	}
	return s
}

// permissionKeys maps what a query author writes to the capability keys the
// preflight uses. One vocabulary, checked at discovery, so a denied capability
// can be matched to the scripts that need it.
var permissionKeys = map[string]string{
	"connect":             "connect",
	"view server state":   "view_server_state",
	"view any definition": "view_any_definition",
	"msdb read":           "msdb_read",
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
	return ""
}
