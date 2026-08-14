package sqlauditor_test

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	sqlauditor "github.com/rudi-bruchez/sql-auditor"
	"github.com/rudi-bruchez/sql-auditor/collect"
)

func TestEmbeddedCorpusIsValid(t *testing.T) {
	scripts, err := collect.Discover(sqlauditor.Queries, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(scripts) != 55 {
		t.Fatalf("got %d scripts, want 55", len(scripts))
	}
	for _, s := range scripts {
		if s.LintError != "" {
			t.Errorf("%s: %s", s.Path, s.LintError)
		}
		if len(s.Results) == 0 {
			t.Errorf("%s: no @resultsets declared", s.Path)
		}
		if len(s.Permissions) == 0 {
			t.Errorf("%s: no @permissions declared", s.Path)
		}
	}
}

// TestEmbeddedCorpusClaimsEveryWriter checks the other direction of the @writer
// vocabulary. collect.KnownWriters names what the directive may say and the Go
// side has a test that every name has an implementation; nothing until here
// checked that a name is actually claimed by a file. A writer implemented,
// declared and used by no collector is dead code that reads as a feature, and
// the corpus is where that would go unnoticed.
func TestEmbeddedCorpusClaimsEveryWriter(t *testing.T) {
	scripts, err := collect.Discover(sqlauditor.Queries, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	claimed := map[string][]string{}
	for _, s := range scripts {
		if s.Writer == "" {
			continue
		}
		claimed[s.Writer] = append(claimed[s.Writer], s.Path)
		// Each writer declares the scope it needs, because the scope follows
		// from what the writer reads: a per-database directory needs a
		// database, and a read of the instance's system_health ring buffer must
		// not have one or it would collect the same graphs once per database.
		// parseScript lints this; asserting it on the real corpus is what makes
		// the lint's coverage of the shipped files evident.
		if want := collect.KnownWriters[s.Writer].Scope; s.Scope != want {
			t.Errorf("%s: @writer %q declares a scope its writer does not want", s.Path, s.Writer)
		}
		// Every writer emits something the archive has to disclose — query
		// text, plans, module source, deadlock reports. A writer script that
		// lost its flag would collect it on every run.
		if s.RequiresFlag == "" {
			t.Errorf("%s: @writer %q without a @requires_flag", s.Path, s.Writer)
		}
	}
	for name := range collect.KnownWriters {
		switch len(claimed[name]) {
		case 1: // as intended
		case 0:
			t.Errorf("writer %q is implemented and declared but no collector uses it", name)
		default:
			t.Errorf("writer %q is claimed by %d collectors: %v — the state it carries "+
				"between them assumes one", name, len(claimed[name]), claimed[name])
		}
	}
}

// TestEmbeddedCorpusHasNoTopLevelKeyCollision checks a rule the encoder
// enforces at run time and nothing checked before it shipped.
//
// The root result set is merged into the document, so its dotted column
// prefixes become top-level keys. Every other result set is placed under a
// top-level key equal to its own name. A root column named [nodes.something]
// therefore claims the same key as a result set called "nodes", and the
// encoder refuses the document — the collector produces nothing at all, on
// every instance, for as long as the collision stands.
//
// That happened: 014.cpu-topology.sql projected [nodes.*] into root beside a
// "nodes" array, and the failure only surfaced in a client's archive. The lint
// cannot see it because it never looks at column aliases, and no unit test
// reaches the corpus. This does both, statically, for every file.
//
// Statements are matched to result sets by position, after dropping the ones
// that emit nothing. contractLint requires one OPTION (RECOMPILE, MAXDOP 1)
// per declared set, but a variable assignment carries the hint too and returns
// no rows — 050.tempdb.sql has one — so the count only lines up once those are
// removed. When it still does not line up the test says so and stops rather
// than comparing the wrong statement against the wrong set.
func TestEmbeddedCorpusHasNoTopLevelKeyCollision(t *testing.T) {
	scripts, err := collect.Discover(sqlauditor.Queries, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	alias := regexp.MustCompile(`(?i)\bAS\s+\[([^\]]+)\]`)
	// A SELECT that assigns into a variable returns no rows, so it consumes a
	// hint without consuming a result set.
	assignment := regexp.MustCompile(`(?is)\bSELECT\s+@\w+\s*=`)

	for _, s := range scripts {
		rootAt := -1
		for i, r := range s.Results {
			if r.Name == collect.RootSetName {
				rootAt = i
			}
		}
		if rootAt < 0 {
			continue // no root set, so nothing merges into the top level
		}
		chunks := strings.Split(s.SQL, "OPTION (RECOMPILE, MAXDOP 1)")
		chunks = chunks[:len(chunks)-1] // the tail after the last hint emits nothing
		var parts []string
		for _, c := range chunks {
			// The statement that owns this hint is the one after the last ";".
			// Testing the whole chunk would drop a producing SELECT that merely
			// happens to sit behind an earlier assignment.
			if assignment.MatchString(c[strings.LastIndex(c, ";")+1:]) {
				continue
			}
			parts = append(parts, c)
		}
		if len(parts) != len(s.Results) {
			t.Errorf("%s: %d emitting statements for %d result sets; the positional "+
				"match below is unreliable", s.Path, len(parts), len(s.Results))
			continue
		}
		others := map[string]bool{}
		for i, r := range s.Results {
			if i != rootAt {
				others[strings.ToLower(r.Name)] = true
			}
		}
		for _, m := range alias.FindAllStringSubmatch(parts[rootAt], -1) {
			key := strings.ToLower(strings.SplitN(m[1], ".", 2)[0])
			if others[key] {
				t.Errorf("%s: root column [%s] claims the top-level key %q, "+
					"which is also a result set. The encoder refuses this and the "+
					"collector writes nothing. Rename the column prefix.",
					s.Path, m[1], key)
			}
		}
	}
}

// The template shipped inside the binary is the only documentation of the
// closed key set that a user receiving the executable alone can read, and
// `env init` writes it verbatim. A key renamed in config.go without the
// template following would hand that user a file the tool then refuses.
//
// Both halves are checked. The active lines are what a verbatim copy resolves
// to. The commented ones matter just as much: they are there to be uncommented,
// and a stale name among them fails only in the user's hands.
func TestEmbeddedEnvTemplateIsAcceptedByTheResolver(t *testing.T) {
	uncommented := regexp.MustCompile(`(?m)^# ([A-Z][A-Z0-9_]*=)`)
	for _, c := range []struct{ name, body string }{
		{"as written", sqlauditor.EnvExample},
		{"with every commented key uncommented", uncommented.ReplaceAllString(sqlauditor.EnvExample, "$1")},
	} {
		parsed, err := collect.ParseDotEnv(strings.NewReader(c.body))
		if err != nil {
			t.Fatalf("%s: the template does not parse: %v", c.name, err)
		}
		if len(parsed) == 0 {
			t.Fatalf("%s: the template set no keys at all", c.name)
		}
		if _, err := collect.Resolve(nil, parsed, func(string) string { return "" }); err != nil {
			t.Errorf("%s: the resolver refuses the template it ships: %v", c.name, err)
		}
	}
}

// sys.master_files.size and sys.database_files.size are int page counts, and
// SUM over int returns int. Multiplying that by 8 to reach kilobytes overflows
// at 281 million pages — 2.1 TB — and the failure is not a NULL column but a
// dead statement: "Arithmetic overflow error converting expression to data type
// int" takes the whole SELECT with it. When that SELECT is the one projecting
// compatibility level, page verify, RCSI, collation and owner, one oversized
// database empties those facts for every database on the instance.
//
// This was found on a client run, not by a test, because no test in this
// repository has a 2.1 TB database to collect. What a test can do is refuse the
// shape: a SUM taken directly over a column named "size" is the bug, and
// SUM(CAST(size AS BIGINT)) is the fix.
func TestNoSumOverAnIntPageCountWithoutWidening(t *testing.T) {
	scripts, err := collect.Discover(sqlauditor.Queries, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	bare := regexp.MustCompile(`(?i)SUM\(\s*(\w+\.)?size\s*\)`)
	for _, s := range scripts {
		body, err := fs.ReadFile(sqlauditor.Queries, "queries/"+s.Path)
		if err != nil {
			t.Fatalf("%s: %v", s.Path, err)
		}
		for _, m := range bare.FindAllString(string(body), -1) {
			t.Errorf("%s: %s sums an int page count directly. SUM over int returns "+
				"int and overflows past 2.1 TB, killing the whole statement. Write "+
				"SUM(CAST(size AS BIGINT)) instead.", s.Path, m)
		}
	}
}
