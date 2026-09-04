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
	if len(scripts) != 70 {
		t.Fatalf("got %d scripts, want 70", len(scripts))
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
	// hint without consuming a result set. So does an INSERT that buffers a
	// read into a table variable, which is the guard pattern the four
	// blockable collectors use: they read into @tables inside TRY/CATCH and
	// emit from them at the bottom, so the emitting statements are the ones
	// selecting FROM a table variable and the buffering ones return nothing.
	assignment := regexp.MustCompile(`(?is)\bSELECT\s+@\w+\s*=`)
	buffering := regexp.MustCompile(`(?is)\bINSERT\s+INTO\s+@\w+`)

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
		// Comments are stripped as well as literals blanked. The statement that
		// owns a hint is found by looking back to the last ";", and an alias is
		// found by looking for AS [x]; a banner comment between SELECT and its
		// first assignment hid an assignment from the filter below, and a ";" or
		// an [alias] inside prose would mislead both.
		chunks := strings.Split(collect.BlankSQLStrings(collect.StripSQLComments(s.SQL)), "OPTION (RECOMPILE, MAXDOP 1)")
		chunks = chunks[:len(chunks)-1] // the tail after the last hint emits nothing
		var parts []string
		for _, c := range chunks {
			// The statement that owns this hint is the one after the last ";".
			// Testing the whole chunk would drop a producing SELECT that merely
			// happens to sit behind an earlier assignment.
			if stmt := c[strings.LastIndex(c, ";")+1:]; assignment.MatchString(stmt) || buffering.MatchString(stmt) {
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

// Page counts are int in sys.master_files, sys.database_files and
// FILEPROPERTY(...,'SpaceUsed'), and multiplying an int by 8 to reach kilobytes
// overflows at 268 435 456 pages — 2 TiB. The failure is not a NULL column but
// a dead statement: "Arithmetic overflow error converting expression to data
// type int" takes the whole SELECT with it, and with it every other fact that
// SELECT was projecting.
//
// This was found on a client run at 2.1 TB, fixed for the aggregated form, and
// then found AGAIN by a reviewer twenty-five lines below the fix, in the same
// file: the per-file projection has the same shape and was not touched. The
// first version of this test looked for SUM(size) and could not see df.size * 8.
//
// So the rule is now about the multiplication rather than about the aggregate:
// wherever a page count is multiplied by 8, the widening must already have
// happened. Bigint sources — the *_page_count columns of dm_db_file_space_usage
// and dm_db_partition_stats — do not name size, growth or FILEPROPERTY, so they
// are not caught, and multiplying by 8.0 is float arithmetic and cannot
// overflow an int.
func TestNoIntPageCountIsMultipliedBeforeItIsWidened(t *testing.T) {
	scripts, err := collect.Discover(sqlauditor.Queries, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// "* 8" not followed by a digit or a decimal point: "* 8.0" is float and
	// safe by construction.
	mul := regexp.MustCompile(`\*\s*8([^0-9.]|$)`)
	intPages := regexp.MustCompile(`(?i)size|growth|FILEPROPERTY`)
	for _, s := range scripts {
		body, err := fs.ReadFile(sqlauditor.Queries, "queries/"+s.Path)
		if err != nil {
			t.Fatalf("%s: %v", s.Path, err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			// Comments explain this very rule and would flag themselves.
			if c := strings.Index(line, "--"); c >= 0 {
				line = line[:c]
			}
			if !mul.MatchString(line) || !intPages.MatchString(line) {
				continue
			}
			if strings.Contains(strings.ToUpper(line), "BIGINT") {
				continue
			}
			t.Errorf("%s:%d multiplies an int page count by 8 before widening it, "+
				"which kills the whole statement past 2 TiB. Wrap the operand in "+
				"CAST(... AS BIGINT):\n  %s", s.Path, i+1, strings.TrimSpace(line))
		}
	}
}

// Blanking must erase nothing that is really in code. The failure mode is
// silent: a version of BlankSQLStrings that does not know what a comment is
// flips into "inside a literal" on the first apostrophe in French prose and
// wipes the hints after it. Measured on this corpus, that version loses hints
// in thirty of these files — 013.memory-model.sql 1 to 0, 050.tempdb.sql 11 to
// 5 — while the collision test above still passes, because it counts what is
// left rather than what was lost.
//
// The invariant is that blanking and stripping COMMUTE, not that blanking
// erases nothing. Blanking erases the hint inside an sp_executesql literal on
// purpose — that is the entire reason the function exists — so "no hint is
// ever lost" would be a true statement about today's corpus and a false one
// about the first collector using the guard pattern, failing in the hands of
// whoever adds it with a message about a file they did not touch.
//
// Stripping first removes the comments; blanking first must behave as if they
// were not there. A scanner that mistakes prose for a literal makes the two
// orders disagree, and nothing else in this corpus does.
func TestBlankSQLStringsAndCommentStrippingCommute(t *testing.T) {
	const hint = "OPTION (RECOMPILE, MAXDOP 1)"
	scripts, err := collect.Discover(sqlauditor.Queries, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, s := range scripts {
		stripThenBlank := strings.Count(collect.BlankSQLStrings(collect.StripSQLComments(s.SQL)), hint)
		blankThenStrip := strings.Count(collect.StripSQLComments(collect.BlankSQLStrings(s.SQL)), hint)
		if stripThenBlank != blankThenStrip {
			t.Errorf("%s: %d hints in code when comments are stripped first, %d when blanked first; "+
				"blanking is reading something that is not a string literal",
				s.Path, stripThenBlank, blankThenStrip)
		}
	}
}
