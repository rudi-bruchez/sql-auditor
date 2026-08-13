package collect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunWriterCreatesParentsAndCountsBytes(t *testing.T) {
	w := newRunWriter(t.TempDir(), 1<<20, func(string) {})
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
	w := newRunWriter(t.TempDir(), 10, func(string) {})
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

func TestRunWriterNoticesAPlan(t *testing.T) {
	var warnings []string
	w := newRunWriter(t.TempDir(), 1<<20, func(s string) { warnings = append(warnings, s) })
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
	_ = warnings
}
