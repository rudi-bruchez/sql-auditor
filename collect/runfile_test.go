package collect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunWriterCreatesParentsAndCountsBytes(t *testing.T) {
	w := newRunWriter(t.TempDir(), 1<<20)
	n, err := w.write("80.workload/Sales/021.query-store-detail/query_1.sql", []byte("SELECT 1"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 8 {
		t.Errorf("wrote %d bytes, want 8", n)
	}
	if w.spent != 8 {
		t.Errorf("spent = %d, want 8", w.spent)
	}
}

func TestRunWriterStopsAtTheBudget(t *testing.T) {
	w := newRunWriter(t.TempDir(), 10)
	if _, err := w.write("a.bin", make([]byte, 8)); err != nil {
		t.Fatal(err)
	}
	if w.overBudget() {
		t.Fatal("over budget after 8 of 10 bytes")
	}
	if _, err := w.write("b.bin", make([]byte, 8)); err == nil {
		t.Fatal("wrote past the budget")
	}
	if _, err := os.Stat(filepath.Join(w.root, "b.bin")); err == nil {
		t.Error("a refused write still left a file behind")
	}
}

// The files that describe a run are written after the run has produced what
// they describe, which is exactly when the budget is most likely to be gone. If
// the budget could refuse them, a truncated archive would carry no account of
// its own truncation.
func TestRunWriterWritesTheDescriptionPastTheBudget(t *testing.T) {
	w := newRunWriter(t.TempDir(), 10)
	if _, err := w.write("a.bin", make([]byte, 10)); err != nil {
		t.Fatal(err)
	}
	if !w.overBudget() {
		t.Fatal("not over budget after spending all 10 bytes")
	}
	if _, err := w.write("refused.bin", []byte("x")); err == nil {
		t.Fatal("an ordinary write got past an exhausted budget")
	}

	n, err := w.writeUnbudgeted("_index.json", []byte(`{"omissions":[]}`))
	if err != nil {
		t.Fatalf("the index was refused by the budget it exists to report: %v", err)
	}
	if n != 16 {
		t.Errorf("wrote %d bytes, want 16", n)
	}
	if _, err := os.Stat(filepath.Join(w.root, "_index.json")); err != nil {
		t.Errorf("_index.json is not on disk: %v", err)
	}
	// Still accounted for: the manifest's byte total has to be what was really
	// written, budget or no budget.
	if w.spent != 26 {
		t.Errorf("spent = %d, want 26", w.spent)
	}
}

// Suspending the budget must not suspend the inspection. A plan written this
// way is still a plan the archive has to admit to holding.
func TestRunWriterNoticesAPlanWrittenPastTheBudget(t *testing.T) {
	w := newRunWriter(t.TempDir(), 1)
	plan := []byte(`<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan"/>`)
	if _, err := w.writeUnbudgeted("plan.sqlplan", plan); err != nil {
		t.Fatal(err)
	}
	if !w.sawShowplan {
		t.Error("an execution plan went to disk without the writer noticing")
	}
}

func TestRunWriterNoticesAPlan(t *testing.T) {
	w := newRunWriter(t.TempDir(), 1<<20)
	if _, err := w.write("plain.json", []byte(`{"counts":{"plans":42}}`)); err != nil {
		t.Fatal(err)
	}
	if w.sawShowplan {
		t.Fatal("plan metadata was mistaken for a plan")
	}
	payload := []byte(`<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan"/>`)
	if _, err := w.write("query_1.plan_2.sqlplan", payload); err != nil {
		t.Fatal(err)
	}
	if !w.sawShowplan {
		t.Error("a plan written straight to disk was not noticed")
	}
}

// A query that reads plans names the Showplan namespace in its text, and the
// default run keeps 500 characters of Query Store text. That text, encoded as
// 020 and 023 encode it, must not count as a plan; a plan encoded the same
// way, as the value of a field, must. Both go through the real encoder, since
// the escaping it applies is what the check reads.
func TestContainsShowplanTellsAPlanFromTextThatNamesOne(t *testing.T) {
	encode := func(col, val string) []byte {
		b, _, err := Encode([]NamedResultSet{{
			Spec: ResultSpec{Name: "top_queries", Shape: ShapeArray},
			Set:  ResultSet{Columns: []string{col}, Types: []string{"NVARCHAR"}, Rows: [][]any{{val}}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, text := range []string{
		`WITH XMLNAMESPACES (DEFAULT 'http://schemas.microsoft.com/sqlserver/2004/07/showplan') ` +
			`SELECT qp.query_plan.value('(//StmtSimple/@StatementText)[1]', 'nvarchar(4000)') FROM sys.dm_exec_query_plan(@h) AS qp`,
		`WITH XMLNAMESPACES ('http://schemas.microsoft.com/sqlserver/2004/07/showplan' AS p) ` +
			`SELECT n.value('@PhysicalOp', 'sysname') FROM @plan.nodes('/p:ShowPlanXML//p:RelOp') AS x(n)`,
	} {
		if containsShowplan(encode("text", text)) {
			t.Errorf("query text that names the namespace was taken for a plan: %s", text)
		}
		if containsShowplan([]byte(text)) {
			t.Errorf("the raw text was taken for a plan: %s", text)
		}
	}
	plan := `<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan" Version="1.6" Build="17.0.4065.4"><BatchSequence/></ShowPlanXML>`
	if !containsShowplan(encode("query_plan", plan)) {
		t.Error("a plan in a field, through the ordinary encoder, was not detected")
	}
	if !containsShowplan([]byte(plan)) {
		t.Error("a plan written as a .sqlplan file was not detected")
	}
}
