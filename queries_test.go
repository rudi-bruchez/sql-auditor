package sqlauditor_test

import (
	"io/fs"
	"regexp"
	"strconv"
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
	checkCorpusInventory(t, scripts)
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
	// An INSERT into a #temp table is the same thing: 028 decides its retained
	// queries once, into #retained, so that two listings read one population.
	assignment := regexp.MustCompile(`(?is)\bSELECT\s+@\w+\s*=`)
	buffering := regexp.MustCompile(`(?is)\bINSERT\s+INTO\s+[@#]\w+`)

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
		cfg, err := collect.Resolve(nil, parsed, func(string) string { return "" })
		if err != nil {
			t.Errorf("%s: the resolver refuses the template it ships: %v", c.name, err)
		} else if err := cfg.CheckConnectable(); err != nil {
			t.Errorf("%s: template is not connectable: %v", c.name, err)
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

// TestEveryKnownProfileHasACollector compares two sets decided in different
// places: the names @profiles may say, and the names the embedded corpus does
// say. A profile nobody declares would refuse every run that asks for it.
func TestEveryKnownProfileHasACollector(t *testing.T) {
	scripts, err := collect.Discover(sqlauditor.Queries, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	declared := map[string]bool{}
	for _, s := range scripts {
		for _, p := range s.Profiles {
			declared[p] = true
		}
	}
	for name := range collect.KnownProfiles {
		if !declared[name] {
			t.Errorf("profile %q is known and no embedded collector declares it", name)
		}
	}
}

// A widening purpose nobody declares brings a database into the run that no
// collector reads: MANIFEST.txt lists it as covered and the archive holds
// nothing for it. The other direction, a declared value that is not known, is
// already a lint error.
func TestEveryKnownWideningHasACollector(t *testing.T) {
	scripts, err := collect.Discover(sqlauditor.Queries, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	declared := map[string]bool{}
	for _, s := range scripts {
		if s.LintError == "" && s.Widened != "" {
			declared[s.Widened] = true
		}
	}
	for name := range collect.KnownWidened {
		if !declared[name] {
			t.Errorf("widening %q is known and no embedded collector declares it", name)
		}
	}
}

// The four 70.schema files that carry one row per table, column or statistic
// must cover the same tables, or the archive lists a table whose columns or
// statistics are missing and reads as a defect in the collector. Until October
// 2026 010 and 060 took the 200 tables with the most rows and the 50 with the
// most reserved pages while 090 and 091 took the 200 by rows alone, and the
// test that kept 010 and 060 identical never looked at the other two. The caps
// were then lifted, and put back the same day in another form when a 4 000
// table schema made 060 alone 115 MB and the collector 1.2 GB resident: the
// four files now fill #listed_tables with one statement, largest tables first
// while two running totals stay under two caps, and every listing joins it.
//
// So the test holds four things. The DECLARE that carries the caps and the
// INSERT that fills the selection are the same text in all four files, so one
// file cannot cap differently or order differently. Each file joins
// #listed_tables as often as it has listings, so none reads past the
// selection. None takes a TOP of its own, and every join to sys.tables
// filters on is_ms_shipped = 0. And each root projects the caps and the
// counts that say whether they bound.
func TestSchemaFilesShareOneTableSelection(t *testing.T) {
	type schemaFile struct {
		name   string
		joins  int      // listings, each of which must join #listed_tables
		counts []string // root fields that say what the cap cut
	}
	files := []schemaFile{
		{"010.objects.sql", 1, []string{"counts.tables", "counts.tables_listed"}},
		{"060.columns.sql", 2, []string{"tables_covered", "tables_total", "columns_listed", "columns_total"}},
		// The DMF staging read, the two lists, and statistics_listed.
		{"090.statistics.sql", 5, []string{"tables_covered", "tables_total", "statistics_listed", "statistics_total"}},
		{"091.statistics-density.sql", 1, []string{"counts.statistics", "counts.statistics_total",
			"counts.tables_covered", "counts.tables_total"}},
	}
	caps := []string{"listing_cap.columns", "listing_cap.statistics"}
	space := regexp.MustCompile(`\s+`)
	declare := regexp.MustCompile(`DECLARE @listing_cap_columns int = \d+, @listing_cap_statistics int = \d+;`)
	fill := regexp.MustCompile(`(?s)INSERT INTO #listed_tables \(object_id\).*?;`)
	top := regexp.MustCompile(`(?i)\bTOP\s*\(`)
	join := regexp.MustCompile(`(?i)JOIN\s+sys\.tables\s+AS\s+(\w+)\s+ON\s+([^\n]*)`)
	uses := regexp.MustCompile(`JOIN\s+#listed_tables\s+AS\s+lt\s+ON\s+lt\.object_id\s+=`)
	var firstDeclare, firstFill, firstName string
	for _, f := range files {
		b, err := sqlauditor.Queries.ReadFile("queries/70.schema/" + f.name)
		if err != nil {
			t.Fatal(err)
		}
		code := collect.StripSQLComments(string(b))
		d := declare.FindAllString(code, -1)
		s := fill.FindAllString(code, -1)
		if len(d) != 1 || len(s) != 1 {
			t.Errorf("%s: %d cap declarations and %d selection statements, want one of each",
				f.name, len(d), len(s))
			continue
		}
		sel := space.ReplaceAllString(s[0], " ")
		if !strings.Contains(sel, "ORDER BY p.row_count DESC, t.object_id") ||
			!strings.Contains(sel, "t.is_ms_shipped = 0") ||
			!strings.Contains(sel, "<= @listing_cap_columns") ||
			!strings.Contains(sel, "<= @listing_cap_statistics") {
			t.Errorf("%s: the selection no longer orders by rows then object_id, filters "+
				"shipped tables and applies both caps:\n%s", f.name, sel)
		}
		if firstName == "" {
			firstDeclare, firstFill, firstName = d[0], sel, f.name
		} else {
			if d[0] != firstDeclare {
				t.Errorf("%s caps the selection with\n  %s\nand %s with\n  %s",
					f.name, d[0], firstName, firstDeclare)
			}
			if sel != firstFill {
				t.Errorf("%s selects its tables with a statement that differs from %s's:\n%s\nagainst\n%s",
					f.name, firstName, sel, firstFill)
			}
		}
		if n := len(uses.FindAllString(code, -1)); n != f.joins {
			t.Errorf("%s joins #listed_tables %d times, want %d, one per listing and count of "+
				"what is listed", f.name, n, f.joins)
		}
		if loc := top.FindStringIndex(code); loc != nil {
			end := min(loc[1]+120, len(code))
			t.Errorf("%s takes a TOP, which selects tables the other three files may not:\n%s",
				f.name, code[loc[0]:end])
		}
		for _, j := range join.FindAllStringSubmatch(code, -1) {
			if !strings.Contains(j[2], j[1]+".is_ms_shipped = 0") {
				t.Errorf("%s joins sys.tables without is_ms_shipped = 0, so it lists tables "+
					"the other files leave out:\n%s", f.name, j[0])
			}
		}
		for _, field := range append(append([]string{}, caps...), f.counts...) {
			if !strings.Contains(code, "AS ["+field+"]") {
				t.Errorf("%s: the root no longer projects %s, so a listing cut by the "+
					"shared selection reads as complete", f.name, field)
			}
		}
		for i, field := range caps {
			v := []string{"@listing_cap_columns", "@listing_cap_statistics"}[i]
			if !regexp.MustCompile(regexp.QuoteMeta(v) + `\s+AS \[` + regexp.QuoteMeta(field) + `\]`).MatchString(code) {
				t.Errorf("%s: %s is not projected from %s, so the root can state a cap "+
					"the selection does not apply", f.name, field, v)
			}
		}
	}
}

// The identity of a query and the text of it are two different disclosures,
// and one view in this corpus carries both.
// sys.dm_db_missing_index_group_stats_query has last_sql_handle beside
// last_statement_start_offset and last_statement_end_offset, and together they
// return the statement whole, literals included, while its plan is in cache.
// Its sibling last_statement_sql_handle is refused by sys.dm_exec_sql_text,
// which is what makes the working one look safe to someone reading the column
// list. docs/missing-index-queries-spec.md carries the measurement.
//
// The rule is on what a collector EMITS, not on what its text mentions: a body
// that selects the view's columns with a star and emits that passes any test
// that greps for four names, and a reviewer wrote one to prove it. So the
// check is twofold — the names must not appear in code, and no star may be
// expanded from that view — and it reads the body with comments stripped,
// because the file is asked to name those columns in its header and explain
// why it refuses them.
func TestNoCollectorEmitsAQueryStatementHandle(t *testing.T) {
	scripts, err := collect.Discover(sqlauditor.Queries, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	forbidden := []string{
		"last_sql_handle",
		"last_statement_sql_handle",
		"last_statement_start_offset",
		"last_statement_end_offset",
	}
	// A star taken from the view or from an alias of it, in any of the shapes
	// that reach a projection: SELECT *, SELECT q.*, SELECT TOP (n) x.*.
	star := regexp.MustCompile(`(?i)select\s+(top\s*\(\s*\d+\s*\)\s*)?([a-z_][a-z0-9_]*\s*\.\s*)?\*`)
	for _, s := range scripts {
		code := collect.StripSQLComments(s.SQL)
		if !strings.Contains(strings.ToLower(code), "missing_index_group_stats_query") {
			continue
		}
		low := strings.ToLower(code)
		for _, name := range forbidden {
			if strings.Contains(low, name) {
				t.Errorf("%s names %s in code. That column, with the two offsets, "+
					"returns the statement and its literals; this corpus publishes "+
					"the hash and never the text.", s.Path, name)
			}
		}
		if star.MatchString(code) {
			t.Errorf("%s expands a star while reading "+
				"sys.dm_db_missing_index_group_stats_query. A star carries "+
				"last_sql_handle and the offsets with it, whatever the file says "+
				"elsewhere; project the columns by name.", s.Path)
		}
	}
}

// The untrusted constraint list is read whole and emitted disabled first, then
// the ones a revalidation could fix, then the not-for-replication ones nobody
// can fix, so the rows worth acting on lead and two runs over the same catalog
// list them in the same order. It was capped at 200 a kind until October 2026,
// when the order decided which rows survived; it now decides only what a
// reader sees first, and a TOP coming back would bring the old question back
// with it, which TestSchemaFilesShareOneTableSelection refuses.
func TestUntrustedConstraintListIsOrderedDisabledFirst(t *testing.T) {
	b, err := sqlauditor.Queries.ReadFile("queries/70.schema/010.objects.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := collect.StripSQLComments(string(b))
	emit := regexp.MustCompile(`(?s)FROM @constraints AS c\s+ORDER BY c\.\[kind\] DESC, c\.\[is_disabled\] DESC, c\.\[is_not_for_replication\],` +
		`\s+c\.\[schema_name\], c\.\[table_name\], c\.\[constraint_name\], c\.\[object_id\]`)
	if !emit.MatchString(sql) {
		t.Errorf("the untrusted constraint list is no longer emitted by kind, disabled first, " +
			"not-for-replication last, with object_id breaking ties")
	}
}

// A cap that stays has to say when it bound. These files keep one, and each
// projects the value of its TOP and a count of what the list would hold
// without it; the value must be the TOP's own, or a reader compares the count
// with a number the file does not apply. The files are 70.schema/040, 045 and
// 021, whose caps were raised in October 2026, and 050, whose cap is a
// variable projected under sample.largest_heaps_scanned.
func TestSchemaCapsAreProjectedWithTheirTotals(t *testing.T) {
	cases := []struct {
		file  string
		top   string // regexp whose first group is the TOP's literal
		cap   string // regexp whose first group is the projected cap
		total string // the count of what the list would hold without the cap
	}{
		{"040.compression.sql", `SELECT TOP \((\d+)\)\s+u\.\[table\]`,
			`(\d+)\s+AS \[listing_cap\]`, "AS [counts.uncompressed_indexes]"},
		{"045.columnstore.sql", `SELECT TOP \((\d+)\)\s+OBJECT_SCHEMA_NAME[^;]*?AS \[index_type\]`,
			`(\d+)\s+AS \[listing_cap\]`, "AS [counts.index_partitions]"},
		{"045.columnstore.sql", `SELECT TOP \((\d+)\)\s+OBJECT_SCHEMA_NAME[^;]*?AS \[trim_reason\]`,
			`(\d+)\s+AS \[listing_cap_trim_reasons\]`, "AS [counts.trim_reason_rows]"},
		{"021.missing-index-queries.sql", `SELECT TOP \((\d+)\)\s+q\.\[group_handle\]`,
			`(\d+)\s+AS \[listing_cap\]`, "AS [counts.rows_before_cap]"},
		{"050.heaps.sql", `DECLARE @top int = (\d+)`,
			`@top\s+AS \[sample\.largest_heaps_scanned\]()`, "AS [counts.eligible_partitions]"},
		{"030.index-operational.sql",
			`DECLARE @heaps_listing_cap int = (\d+);[\s\S]*INSERT INTO @heaps\s+SELECT TOP \(@heaps_listing_cap\)`,
			`@heaps_listing_cap\s+AS \[heaps_listing_cap\]()`, "AS [heaps_total]"},
		{"030.index-operational.sql",
			`DECLARE @heaps_listing_cap int = (\d+);[\s\S]*INSERT INTO @heaps\s+SELECT TOP \(@heaps_listing_cap\)`,
			`@heaps_listing_cap\s+AS \[heaps_listing_cap\]()`, "AS [heaps_listed]"},
	}
	for _, c := range cases {
		b, err := sqlauditor.Queries.ReadFile("queries/70.schema/" + c.file)
		if err != nil {
			t.Fatal(err)
		}
		code := collect.StripSQLComments(string(b))
		top := regexp.MustCompile(c.top).FindStringSubmatch(code)
		capm := regexp.MustCompile(c.cap).FindStringSubmatch(code)
		if top == nil || capm == nil {
			t.Errorf("%s: cannot find the TOP (%v) or the projected cap (%v)", c.file, top != nil, capm != nil)
			continue
		}
		if capm[1] != "" && capm[1] != top[1] {
			t.Errorf("%s: the list takes TOP (%s) and the root says %s", c.file, top[1], capm[1])
		}
		if !strings.Contains(code, c.total) {
			t.Errorf("%s: no %s, so a list cut at its cap reads as complete", c.file, c.total)
		}
	}
	// 021's outer TOP exists so that it never binds: the engine keeps at most
	// 600 missing-index groups, and each keeps at most listing_cap_per_suggestion
	// rows, so the product is the most the file can hold.
	b, err := sqlauditor.Queries.ReadFile("queries/70.schema/021.missing-index-queries.sql")
	if err != nil {
		t.Fatal(err)
	}
	code := collect.StripSQLComments(string(b))
	per := regexp.MustCompile(`(\d+)\s+AS \[listing_cap_per_suggestion\]`).FindStringSubmatch(code)
	rank := regexp.MustCompile(`\[rank_in_suggestion\] <= (\d+)`).FindStringSubmatch(code)
	outer := regexp.MustCompile(`(\d+)\s+AS \[listing_cap\]`).FindStringSubmatch(code)
	if per == nil || rank == nil || outer == nil {
		t.Fatalf("021: per-suggestion cap %v, rank filter %v, outer cap %v", per != nil, rank != nil, outer != nil)
	}
	if per[1] != rank[1] {
		t.Errorf("021: the rows are ranked to %s per suggestion and the root says %s", rank[1], per[1])
	}
	p, _ := strconv.Atoi(per[1])
	o, _ := strconv.Atoi(outer[1])
	if o < 600*p {
		t.Errorf("021: listing_cap %d is below 600 groups times %d, so it can drop whole suggestions", o, p)
	}
}

// The agent profile section of 042 names five parameters, and the names are
// matched as MSagent_parameters stores them: with the leading dash, measured
// on SQL Server 2025. A pivot on "SkipErrors" without it, or on a misspelling,
// compiles, runs, and reports NULL on every distributor, which reads as "no
// agent skips errors". Nothing at execution would say otherwise, so this test
// does. It also holds the line the header draws on the job step: the command
// is reduced to two tokens inside the read and never selected, because a
// replication agent's command line is where -PublisherPassword is written.
func TestReplicationAgentProfilesNameTheirParameters(t *testing.T) {
	b, err := sqlauditor.Queries.ReadFile("queries/90.availability/042.replication-distribution.sql")
	if err != nil {
		t.Fatal(err)
	}
	code := collect.StripSQLComments(string(b))
	for param, column := range map[string]string{
		"-SkipErrors":          "skip_errors",
		"-MaxCmdsInTran":       "max_cmds_in_tran",
		"-SubscriptionStreams": "subscription_streams",
		"-ReadBatchSize":       "read_batch_size",
		"-CommitBatchSize":     "commit_batch_size",
	} {
		pivot := regexp.MustCompile(`WHEN N'` + regexp.QuoteMeta(param) + `'\s+THEN x\.\[value\] END\), N''\)\s+AS \[` +
			column + `\]|WHEN N'` + regexp.QuoteMeta(param) + `'\s+THEN x\.\[value\] END\)\s+AS \[` + column + `\]`)
		if !pivot.MatchString(code) {
			t.Errorf("042 does not pivot %s into [%s] in agent_profiles", param, column)
		}
	}
	for _, token := range []string{"''-SkipErrors''", "''-MaxCmdsInTran''"} {
		if !strings.Contains(code, token) {
			t.Errorf("042 no longer looks for %s in the agent job steps", token)
		}
	}
	steps := regexp.MustCompile(`(?s)SELECT s\.job_id, s\.step_id,(.*?)FROM msdb\.dbo\.sysjobsteps`).FindStringSubmatch(code)
	if steps == nil {
		t.Fatal("042 no longer reads msdb.dbo.sysjobsteps in the shape this test knows")
	}
	if strings.Contains(strings.ToLower(steps[1]), "command") || strings.Contains(steps[1], "c.cmd") {
		t.Errorf("042 selects the job step command itself, not the two tokens:\n%s", steps[1])
	}
}
