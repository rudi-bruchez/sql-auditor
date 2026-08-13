package sqlauditor_test

import (
	"testing"

	sqlauditor "github.com/rudi-bruchez/sql-auditor"
	"github.com/rudi-bruchez/sql-auditor/collect"
)

func TestEmbeddedCorpusIsValid(t *testing.T) {
	scripts, err := collect.Discover(sqlauditor.Queries, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(scripts) != 30 {
		t.Fatalf("got %d scripts, want 30", len(scripts))
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
