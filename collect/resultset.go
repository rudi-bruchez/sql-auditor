package collect

import "strings"

// Small read-only helpers over a materialised ResultSet. The Query Store
// writers need to reach into rows by column name, and doing that inline three
// times over would put the same off-by-one in three places.
//
// Every accessor distinguishes "absent or NULL" from "present and empty": a
// query with no plan and a query with an empty plan are different findings, and
// collapsing them would be the kind of silence the rest of the tool refuses.

func setByName(sets []NamedResultSet, name string) (ResultSet, bool) {
	for _, s := range sets {
		if s.Spec.Name == name {
			return s.Set, true
		}
	}
	return ResultSet{}, false
}

func colIndex(s ResultSet, col string) int {
	for i, c := range s.Columns {
		if strings.EqualFold(c, col) {
			return i
		}
	}
	return -1
}

func valueAt(s ResultSet, row int, col string) (any, bool) {
	i := colIndex(s, col)
	if i < 0 || row < 0 || row >= len(s.Rows) || i >= len(s.Rows[row]) {
		return nil, false
	}
	v := s.Rows[row][i]
	if v == nil {
		return nil, false
	}
	return v, true
}

func stringAt(s ResultSet, row int, col string) (string, bool) {
	v, ok := valueAt(s, row, col)
	if !ok {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, true
	case []byte:
		return string(t), true
	}
	return "", false
}

func int64At(s ResultSet, row int, col string) (int64, bool) {
	v, ok := valueAt(s, row, col)
	if !ok {
		return 0, false
	}
	return asInt64(v)
}

// asInt64 is the widening int64At applies, split out so a caller that already
// holds the cell — rowsWhere, walking one known column over every row — can
// reuse it without resolving the column name again.
func asInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int32:
		return int64(t), true
	case int:
		return int64(t), true
	}
	return 0, false
}

func boolAt(s ResultSet, row int, col string) bool {
	v, ok := valueAt(s, row, col)
	if !ok {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case int64:
		return t != 0
	}
	return false
}

// rowsWhere keeps the rows whose col equals want, and every column and type.
// The result is still a valid ResultSet, so it can be handed straight to
// Encode.
//
// The column is resolved once and the index is then used directly, rather than
// going back through int64At per row: the name walk is case-insensitive over
// every column, and this runs over the whole extraction of every database. The
// two guards int64At applies are kept — a row shorter than the header, and a
// NULL cell — because a truncated row must be skipped here as it is there.
func rowsWhere(s ResultSet, col string, want int64) ResultSet {
	out := ResultSet{Columns: s.Columns, Types: s.Types}
	i := colIndex(s, col)
	if i < 0 {
		return out
	}
	for _, row := range s.Rows {
		if i >= len(row) || row[i] == nil {
			continue
		}
		if v, ok := asInt64(row[i]); ok && v == want {
			out.Rows = append(out.Rows, row)
		}
	}
	return out
}

// withoutColumns returns a copy with the named columns removed. It exists for
// one reason: the query text and the plan XML are written to their own files,
// and repeating megabytes of XML inside the JSON beside them would double the
// archive for nothing.
func withoutColumns(s ResultSet, cols ...string) ResultSet {
	drop := map[int]bool{}
	for _, c := range cols {
		if i := colIndex(s, c); i >= 0 {
			drop[i] = true
		}
	}
	out := ResultSet{}
	for i, c := range s.Columns {
		if !drop[i] {
			out.Columns = append(out.Columns, c)
			if i < len(s.Types) {
				out.Types = append(out.Types, s.Types[i])
			}
		}
	}
	for _, row := range s.Rows {
		kept := make([]any, 0, len(out.Columns))
		for i, v := range row {
			if !drop[i] {
				kept = append(kept, v)
			}
		}
		out.Rows = append(out.Rows, kept)
	}
	return out
}
