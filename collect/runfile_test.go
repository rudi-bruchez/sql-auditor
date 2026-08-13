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
