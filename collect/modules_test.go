package collect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// moduleSets builds what 70.schema/080.modules.sql returns for one database:
// one ordinary view, one encrypted procedure, one module past the per-database
// cap, one above the per-module byte cap, and one the catalog has nothing for.
// The four absent definitions are the point of the fixture — they must not be
// reported as the same thing.
func moduleSets() []NamedResultSet {
	root := ResultSet{
		Columns: []string{"database", "modules_total", "caps.modules", "caps.module_bytes"},
		Types:   []string{"NVARCHAR", "INT", "INT", "INT"},
		Rows:    [][]any{{"Sales", int64(2402), int64(maxModules), int64(maxModuleBytes)}},
	}
	big := strings.Repeat("x", 32)
	modules := ResultSet{
		Columns: []string{"schema", "name", "type", "definition", "definition_bytes",
			"is_encrypted", "module.rank", "module.count"},
		Types: []string{"NVARCHAR", "NVARCHAR", "NVARCHAR", "NVARCHAR", "BIGINT",
			"INT", "BIGINT", "BIGINT"},
		Rows: [][]any{
			{"dbo", "v_orders", "VIEW", "CREATE VIEW dbo.v_orders AS SELECT 1 AS a", int64(41), int64(0), int64(1), int64(2402)},
			// Encrypted: the server returns no definition and never could.
			{"dbo", "p_secret", "SQL_STORED_PROCEDURE", nil, nil, int64(1), int64(2), int64(2402)},
			// Above the byte cap: the SQL nulled it, the size still arrives.
			{"dbo", "p_generated", "SQL_STORED_PROCEDURE", nil, int64(maxModuleBytes + 1), int64(0), int64(3), int64(2402)},
			// Past the per-database cap.
			{"dbo", "p_ancient", "SQL_STORED_PROCEDURE", nil, int64(120), int64(0), int64(maxModules + 1), int64(2402)},
			// Nothing wrong with it and nothing there either.
			{"dbo", "p_empty", "SQL_STORED_PROCEDURE", nil, nil, int64(0), int64(5), int64(2402)},
			// Two names that sanitise to one filename: both illegal characters
			// become an underscore.
			{"dbo", "rpt/daily", "VIEW", "CREATE VIEW a AS SELECT 1" + big, int64(57), int64(0), int64(6), int64(2402)},
			{"dbo", "rpt|daily", "VIEW", "CREATE VIEW b AS SELECT 2" + big, int64(57), int64(0), int64(7), int64(2402)},
		},
	}
	return []NamedResultSet{
		{Spec: ResultSpec{Name: "root", Shape: ShapeObject}, Set: root},
		{Spec: ResultSpec{Name: "modules", Shape: ShapeArray}, Set: modules},
	}
}

func runModuleWriter(t *testing.T, sets []NamedResultSet, budget int) (root, rel string, res WriteResult, warnings []string) {
	t.Helper()
	root = t.TempDir()
	req := WriteRequest{
		Out:    newRunWriter(root, budget),
		Script: Script{Path: "70.schema/080.modules.sql", Dir: "70.schema", Base: "080.modules"},
		Unit:   DatabaseFolder{Name: "Sales", Folder: "Sales"},
		Sets:   sets,
		State:  NewQueryStoreState(),
		Warn:   func(s string) { warnings = append(warnings, s) },
	}
	w := writerFor("object-definitions")
	if w == nil {
		t.Fatal("writerFor(object-definitions) = nil")
	}
	res, err := w(req)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	return root, res.Rel, res, warnings
}

func TestModuleWriterLaysOutOneDirectoryPerDatabase(t *testing.T) {
	root, rel, res, _ := runModuleWriter(t, moduleSets(), maxRunBytes)
	if rel != "70.schema/Sales/080.modules" {
		t.Fatalf("rel = %q, want 70.schema/Sales/080.modules", rel)
	}
	dir := filepath.Join(root, filepath.FromSlash(rel))
	for _, name := range []string{"_index.json", "dbo.v_orders.sql"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	for _, name := range []string{"dbo.p_secret.sql", "dbo.p_generated.sql", "dbo.p_ancient.sql", "dbo.p_empty.sql"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s was written for a module with no definition", name)
		}
	}
	// Three definitions came back: the view and the two colliding names.
	if res.DefinitionFiles != 3 {
		t.Errorf("DefinitionFiles = %d, want 3", res.DefinitionFiles)
	}
	// The counter that drives the Query Store disclosure must stay untouched,
	// or an archive of view source would announce execution plans it has none
	// of.
	if res.PlanFiles != 0 || res.TextFiles != 0 {
		t.Errorf("PlanFiles = %d, TextFiles = %d, want 0 and 0: those latch a different disclosure",
			res.PlanFiles, res.TextFiles)
	}
}

func TestModuleWriterWritesTheDefinitionVerbatim(t *testing.T) {
	root, rel, _, _ := runModuleWriter(t, moduleSets(), maxRunBytes)
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), "dbo.v_orders.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "CREATE VIEW dbo.v_orders AS SELECT 1 AS a" {
		t.Errorf("dbo.v_orders.sql = %q, want the definition unchanged", string(b))
	}
}

type moduleIndexFile struct {
	Counts struct {
		Total   int64 `json:"total"`
		Listed  int   `json:"listed"`
		Written int   `json:"written"`
	} `json:"counts"`
	Modules []struct {
		Name string `json:"name"`
		File string `json:"file"`
	} `json:"modules"`
	Omissions []struct {
		Name   string `json:"name"`
		Reason string `json:"reason"`
		Bytes  int64  `json:"bytes"`
	} `json:"omissions"`
}

func readModuleIndex(t *testing.T, root, rel string) moduleIndexFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel), "_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx moduleIndexFile
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	return idx
}

// The heart of this writer. Four different things make a definition absent, and
// an archive that cannot tell them apart states something false about the
// server: an encrypted module reported as oversized invents a measurement, and
// one dropped by the cap reported as "the catalog holds no definition" denies
// the existence of code that is sitting right there.
func TestModuleWriterTellsTheFourAbsencesApart(t *testing.T) {
	root, rel, _, warnings := runModuleWriter(t, moduleSets(), maxRunBytes)
	idx := readModuleIndex(t, root, rel)

	reasons := map[string]string{}
	for _, o := range idx.Omissions {
		reasons[o.Name] = o.Reason
	}
	if len(idx.Omissions) != 4 {
		t.Fatalf("omissions = %+v, want exactly 4", idx.Omissions)
	}
	for name, want := range map[string]string{
		"p_secret":    "encrypted",
		"p_generated": "per-module cap",
		"p_ancient":   "per-database cap",
		"p_empty":     "holds no definition",
	} {
		got, ok := reasons[name]
		if !ok {
			t.Errorf("%s: no omission recorded", name)
			continue
		}
		if !strings.Contains(got, want) {
			t.Errorf("%s: reason %q does not say %q", name, got, want)
		}
	}
	// The three that are absent for a stated reason must not borrow each
	// other's wording.
	if strings.Contains(reasons["p_secret"], "cap") {
		t.Error("an encrypted module was reported as capped")
	}
	if strings.Contains(reasons["p_ancient"], "holds no definition") {
		t.Error("a module dropped by the cap was reported as one the catalog does not hold")
	}
	if strings.Contains(reasons["p_generated"], "holds no definition") {
		t.Error("an oversized module was reported as one the catalog does not hold")
	}
	// The size survives the omission for the two absences that have one, and
	// is absent for the two that do not: nobody measured an encrypted body.
	if idx.Omissions[0].Name == "p_secret" && idx.Omissions[0].Bytes != 0 {
		t.Errorf("p_secret carries %d bytes; an encrypted module has no measured size", idx.Omissions[0].Bytes)
	}
	if len(warnings) != 4 {
		t.Errorf("got %d warnings, want one per omission", len(warnings))
	}
}

func TestModuleWriterCountsWhatIsOnDisk(t *testing.T) {
	root, rel, _, _ := runModuleWriter(t, moduleSets(), maxRunBytes)
	idx := readModuleIndex(t, root, rel)
	if idx.Counts.Total != 2402 {
		t.Errorf("counts.total = %d, want the database's own total 2402", idx.Counts.Total)
	}
	if idx.Counts.Listed != 7 {
		t.Errorf("counts.listed = %d, want the 7 rows the result set carried", idx.Counts.Listed)
	}
	if idx.Counts.Written != 3 {
		t.Errorf("counts.written = %d, want the 3 files on disk", idx.Counts.Written)
	}
	// Every module is listed, whether or not its body was written: a module
	// missing from the index is a module the archive never admits exists.
	if len(idx.Modules) != 7 {
		t.Errorf("modules = %d entries, want all 7 listed", len(idx.Modules))
	}
	for _, m := range idx.Modules {
		if m.Name == "p_ancient" && m.File != "" {
			t.Errorf("p_ancient names file %q it does not have", m.File)
		}
	}
}

// Two object names that sanitise to one filename must not have one body land
// under the other's name — an omission is recoverable, a file whose contents
// belong to a different object is not.
func TestModuleWriterKeepsCollidingNamesApart(t *testing.T) {
	root, rel, _, _ := runModuleWriter(t, moduleSets(), maxRunBytes)
	dir := filepath.Join(root, filepath.FromSlash(rel))
	first, err := os.ReadFile(filepath.Join(dir, "dbo.rpt_daily.sql"))
	if err != nil {
		t.Fatalf("dbo.rpt_daily.sql: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(dir, "dbo.rpt_daily~2.sql"))
	if err != nil {
		t.Fatalf("the second colliding name was not written under its own file: %v", err)
	}
	if string(first) == string(second) {
		t.Error("both colliding modules hold the same body")
	}
	idx := readModuleIndex(t, root, rel)
	for _, m := range idx.Modules {
		if m.Name == "rpt|daily" && m.File != "dbo.rpt_daily~2.sql" {
			t.Errorf("the index names %q, not the file actually written", m.File)
		}
	}
}

// An exhausted run budget is a recorded omission, never a silent absence.
func TestModuleWriterRecordsWhatTheBudgetRefused(t *testing.T) {
	root, rel, res, _ := runModuleWriter(t, moduleSets(), 20)
	idx := readModuleIndex(t, root, rel)
	if res.DefinitionFiles != 0 {
		t.Errorf("DefinitionFiles = %d with a 20 byte budget, want 0", res.DefinitionFiles)
	}
	var budgeted int
	for _, o := range idx.Omissions {
		if strings.Contains(o.Reason, "cap on what the whole collection may write") {
			budgeted++
		}
	}
	if budgeted != 3 {
		t.Errorf("%d budget omissions, want the 3 definitions that could not be written", budgeted)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel), "_index.json")); err != nil {
		t.Errorf("the index was refused by the budget it exists to report: %v", err)
	}
}

// Both caps are written twice — as a Go constant and as a literal in the SQL —
// and only one of the two decides anything. A Go constant raised above a stale
// SQL literal would make the module past the SQL's cap arrive with a NULL
// definition and be reported here as one the catalog does not hold, which is a
// false fact about the server.
func TestModuleCapsAreTheSameNumbersInTheCorpus(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "queries", "70.schema", "080.modules.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	for _, c := range []struct {
		what    string
		pattern string
		want    int
	}{
		{"the per-database module cap", `\w+\.module_rank <= (\d+)`, maxModules},
		{"the per-module byte cap", `DATALENGTH\(m\.definition\) <= (\d+)`, maxModuleBytes},
	} {
		m := regexp.MustCompile(c.pattern).FindAllStringSubmatch(sql, -1)
		if len(m) != 1 {
			t.Errorf("080.modules.sql has %d guards for %s, want exactly 1", len(m), c.what)
			continue
		}
		got, err := strconv.Atoi(m[0][1])
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("080.modules.sql applies %d for %s, Go applies %d — the two are one rule and have drifted",
				got, c.what, c.want)
		}
	}
}
