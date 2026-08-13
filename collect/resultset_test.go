package collect

import "testing"

func sampleSet() ResultSet {
	return ResultSet{
		Columns: []string{"query_id", "text", "query_plan", "forced"},
		Types:   []string{"BIGINT", "NVARCHAR", "NVARCHAR", "BIT"},
		Rows: [][]any{
			{int64(1), "SELECT 1", "<ShowPlanXML/>", true},
			{int64(2), nil, nil, false},
			{int64(1), "SELECT 1", "<ShowPlanXML/>", true},
		},
	}
}

func TestColIndex(t *testing.T) {
	s := sampleSet()
	if got := colIndex(s, "query_plan"); got != 2 {
		t.Errorf("colIndex(query_plan) = %d, want 2", got)
	}
	if got := colIndex(s, "absent"); got != -1 {
		t.Errorf("colIndex(absent) = %d, want -1", got)
	}
}

func TestStringAtHandlesNullAndMissing(t *testing.T) {
	s := sampleSet()
	if v, ok := stringAt(s, 0, "text"); !ok || v != "SELECT 1" {
		t.Errorf("stringAt(0,text) = %q,%v want SELECT 1,true", v, ok)
	}
	if _, ok := stringAt(s, 1, "text"); ok {
		t.Error("stringAt on a NULL reported ok; a NULL must be distinguishable from an empty string")
	}
	if _, ok := stringAt(s, 0, "absent"); ok {
		t.Error("stringAt on a missing column reported ok")
	}
}

func TestRowsWhereKeepsColumnsAndTypes(t *testing.T) {
	s := rowsWhere(sampleSet(), "query_id", 1)
	if len(s.Rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(s.Rows))
	}
	if len(s.Columns) != 4 || len(s.Types) != 4 {
		t.Fatalf("columns/types lost: %d/%d, want 4/4", len(s.Columns), len(s.Types))
	}
}

func TestWithoutColumnsDropsValuesAndTypes(t *testing.T) {
	s := withoutColumns(sampleSet(), "text", "query_plan")
	want := []string{"query_id", "forced"}
	for i, w := range want {
		if s.Columns[i] != w {
			t.Fatalf("column %d = %q, want %q", i, s.Columns[i], w)
		}
	}
	if len(s.Types) != 2 {
		t.Fatalf("got %d types, want 2", len(s.Types))
	}
	for i, r := range s.Rows {
		if len(r) != 2 {
			t.Fatalf("row %d has %d values, want 2", i, len(r))
		}
	}
}

func TestWithoutColumnsLeavesTheSourceUntouched(t *testing.T) {
	src := sampleSet()
	_ = withoutColumns(src, "text")
	if len(src.Columns) != 4 {
		t.Fatalf("source mutated: %d columns left", len(src.Columns))
	}
}
