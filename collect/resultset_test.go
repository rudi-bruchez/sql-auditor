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

func TestSetByNameFindsByName(t *testing.T) {
	sets := []NamedResultSet{
		{Spec: ResultSpec{Name: "queries"}, Set: sampleSet()},
	}
	s, ok := setByName(sets, "queries")
	if !ok {
		t.Fatal("setByName(queries) not found")
	}
	if len(s.Columns) != 4 {
		t.Errorf("got %d columns, want 4", len(s.Columns))
	}
	if _, ok := setByName(sets, "absent"); ok {
		t.Error("setByName(absent) reported found")
	}
}

func TestBoolAt(t *testing.T) {
	s := sampleSet()
	if v := boolAt(s, 0, "forced"); !v {
		t.Errorf("boolAt(0,forced) = %v, want true", v)
	}
	if v := boolAt(s, 1, "forced"); v {
		t.Errorf("boolAt(1,forced) = %v, want false", v)
	}
	// A NULL BIT and an explicit 0 are indistinguishable through this
	// signature by design; both must report false rather than panicking or
	// returning an "ok" flag the brief's signature doesn't have.
	if v := boolAt(s, 1, "text"); v {
		t.Errorf("boolAt on a NULL column = %v, want false", v)
	}
	if v := boolAt(s, 0, "absent"); v {
		t.Errorf("boolAt on a missing column = %v, want false", v)
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

// rowsWhere resolves the column once and then reads the cell directly, so the
// guards int64At applied per row have to be applied here instead: a row shorter
// than the header, a NULL cell, and a value of another type are all "not a
// match", never a panic and never a kept row.
func TestRowsWhereGuardsTheRowsTheHelpersGuarded(t *testing.T) {
	s := sampleSet()
	s.Rows = append(s.Rows,
		[]any{},         // short row: no query_id at all
		[]any{nil},      // NULL query_id
		[]any{"1"},      // a string where a BIGINT was declared
		[]any{int32(1)}, // a narrower integer, which int64At widened
	)
	got := rowsWhere(s, "query_id", 1)
	if len(got.Rows) != 3 {
		t.Fatalf("got %d rows, want the two int64 matches and the int32 one: %v", len(got.Rows), got.Rows)
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
