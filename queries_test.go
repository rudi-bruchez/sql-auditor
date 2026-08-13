package sqlauditor_test

import (
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
	if len(scripts) != 38 {
		t.Fatalf("got %d scripts, want 38", len(scripts))
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
		// A writer produces a directory per database and has nowhere to put one
		// otherwise. parseScript lints this; asserting it on the real corpus is
		// what makes the lint's coverage of the shipped files evident.
		if s.Scope != collect.ScopeDatabase {
			t.Errorf("%s: @writer %q without @scope: database", s.Path, s.Writer)
		}
		// Both writers emit query text and execution plans, which is the
		// disclosure the flag exists for. A writer script that lost its flag
		// would collect them on every run.
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
