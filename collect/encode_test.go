package collect

import (
	"math"
	"strings"
	"testing"
	"time"
)

func enc(t *testing.T, sets []NamedResultSet) string {
	t.Helper()
	b, _, err := Encode(sets)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return string(b)
}

func TestEncodeNestsDottedAliases(t *testing.T) {
	// A named set nests under its name; the dotted aliases nest inside that.
	got := enc(t, []NamedResultSet{{
		Spec: ResultSpec{Name: "tempdb", Shape: ShapeObject},
		Set: ResultSet{
			Columns: []string{"instance.server", "instance.edition", "uptime_days"},
			Types:   []string{"NVARCHAR", "NVARCHAR", "INT"},
			Rows:    [][]any{{"SRV01", "Developer Edition", int64(12)}},
		},
	}})
	want := `{"tempdb":{"instance":{"server":"SRV01","edition":"Developer Edition"},"uptime_days":12}}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestEncodeRootSetMergesAtTopLevel(t *testing.T) {
	// The set named "root" contributes its properties to the document root
	// rather than nesting under a "root" key. This is what keeps
	// 010.properties.json shaped like the FOR JSON output it replaces:
	// {"instance":{…},"system":{…},"waits":[…]} — not {"instance":{"instance":…}}.
	got := enc(t, []NamedResultSet{
		{
			Spec: ResultSpec{Name: RootSetName, Shape: ShapeObject},
			Set: ResultSet{
				Columns: []string{"instance.server", "system.cpus"},
				Types:   []string{"NVARCHAR", "INT"},
				Rows:    [][]any{{"SRV01", int64(8)}},
			},
		},
		{
			Spec: ResultSpec{Name: "waits", Shape: ShapeArray},
			Set: ResultSet{
				Columns: []string{"wait_type"},
				Types:   []string{"NVARCHAR"},
				Rows:    [][]any{{"PAGEIOLATCH_SH"}},
			},
		},
	})
	want := `{"instance":{"server":"SRV01"},"system":{"cpus":8},"waits":[{"wait_type":"PAGEIOLATCH_SH"}]}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestEncodeRootSetMustBeObject(t *testing.T) {
	_, _, err := Encode([]NamedResultSet{{
		Spec: ResultSpec{Name: RootSetName, Shape: ShapeArray},
		Set:  ResultSet{Columns: []string{"x"}, Types: []string{"INT"}},
	}})
	if err == nil {
		t.Fatal("expected an error: the root set cannot be an array")
	}
}

func TestEncodePreservesSelectOrder(t *testing.T) {
	// Alphabetical order would be a,b,z. SELECT order is z,a,b. This test
	// fails the moment a map[string]any is reintroduced on the output path.
	got := enc(t, []NamedResultSet{{
		Spec: ResultSpec{Name: "n", Shape: ShapeObject},
		Set:  ResultSet{Columns: []string{"z", "a", "b"}, Rows: [][]any{{1, 2, 3}}},
	}})
	want := `{"n":{"z":1,"a":2,"b":3}}`
	if got != want {
		t.Errorf("property order not preserved:\ngot  %s\nwant %s", got, want)
	}
}

func TestEncodeShapes(t *testing.T) {
	tests := []struct {
		name string
		set  NamedResultSet
		want string
	}{
		{"array with rows",
			NamedResultSet{ResultSpec{"waits", ShapeArray},
				ResultSet{Columns: []string{"t"}, Rows: [][]any{{"A"}, {"B"}}}},
			`{"waits":[{"t":"A"},{"t":"B"}]}`},
		{"array with no rows is an empty array",
			NamedResultSet{ResultSpec{"waits", ShapeArray},
				ResultSet{Columns: []string{"t"}}},
			`{"waits":[]}`},
		{"object with no rows is null, not an empty object",
			NamedResultSet{ResultSpec{"instance", ShapeObject},
				ResultSet{Columns: []string{"t"}}},
			`{"instance":null}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := enc(t, []NamedResultSet{tc.set}); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestEncodeErrors(t *testing.T) {
	tests := []struct {
		name    string
		set     NamedResultSet
		wantErr string
	}{
		{"object with two rows",
			NamedResultSet{ResultSpec{"i", ShapeObject},
				ResultSet{Columns: []string{"t"}, Rows: [][]any{{1}, {2}}}},
			"2 rows"},
		{"duplicate dotted path",
			NamedResultSet{ResultSpec{"i", ShapeObject},
				ResultSet{Columns: []string{"a.b", "a.b"}, Rows: [][]any{{1, 2}}}},
			"duplicate"},
		{"scalar and object claim the same path",
			NamedResultSet{ResultSpec{"i", ShapeObject},
				ResultSet{Columns: []string{"a", "a.b"}, Rows: [][]any{{1, 2}}}},
			"conflict"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Encode([]NamedResultSet{tc.set})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q should mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestEncodeValues(t *testing.T) {
	ts := time.Date(2026, 8, 8, 14, 30, 0, 0, time.UTC)
	got := enc(t, []NamedResultSet{{
		Spec: ResultSpec{Name: "v", Shape: ShapeObject},
		Set: ResultSet{
			Columns: []string{"null", "bool", "int", "float", "dt", "bin", "str"},
			Types:   []string{"INT", "BIT", "BIGINT", "FLOAT", "DATETIME", "VARBINARY", "NVARCHAR"},
			Rows:    [][]any{{nil, true, int64(42), 1.5, ts, []byte{0xDE, 0xAD}, "x"}},
		},
	}})
	want := `{"v":{"null":null,"bool":true,"int":42,"float":1.5,` +
		`"dt":"2026-08-08T14:30:00","bin":"0xDEAD","str":"x"}}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestEncodeTypesDrivenBySQLTypeName(t *testing.T) {
	// Four SQL types cannot be distinguished from the Go value the driver
	// returns. Deciding format from the dynamic type alone corrupts all four:
	// a DECIMAL arrives as []byte and would be emitted as a hex string.
	offset := time.FixedZone("CEST", 2*3600)
	tests := []struct {
		name, sqlType string
		value         any
		want          string
	}{
		{"datetimeoffset keeps its offset", "DATETIMEOFFSET",
			time.Date(2026, 8, 8, 14, 30, 0, 0, offset), `"2026-08-08T14:30:00+02:00"`},
		{"datetime has no offset", "DATETIME",
			time.Date(2026, 8, 8, 14, 30, 0, 0, offset), `"2026-08-08T14:30:00"`},
		{"time emits only the time part", "TIME",
			time.Date(2026, 8, 8, 14, 30, 5, 0, time.UTC), `"14:30:05"`},
		{"date emits only the date part", "DATE",
			time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC), `"2026-08-08"`},
		{"decimal from []byte becomes a number", "DECIMAL",
			[]byte("123.45"), `123.45`},
		{"decimal from string becomes a number", "DECIMAL",
			"123.45", `123.45`},
		{"money becomes a number", "MONEY",
			[]byte("10.50"), `10.5`},
		// Wire bytes as SQL Server actually sends them: the first three fields
		// are little-endian. Captured from a live server, cross-checked against
		// go-mssqldb's UniqueIdentifier.String.
		{"uniqueidentifier becomes a canonical lowercase GUID", "UNIQUEIDENTIFIER",
			[]byte{0xFF, 0x19, 0x96, 0x6F, 0x86, 0x8B, 0x11, 0xD0, 0xB4, 0x2D,
				0x00, 0xC0, 0x4F, 0xC9, 0x64, 0xFF},
			`"6f9619ff-8b86-d011-b42d-00c04fc964ff"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := enc(t, []NamedResultSet{{
				Spec: ResultSpec{Name: "v", Shape: ShapeObject},
				Set: ResultSet{
					Columns: []string{"c"}, Types: []string{tc.sqlType},
					Rows: [][]any{{tc.value}},
				},
			}})
			want := `{"v":{"c":` + tc.want + `}}`
			if got != want {
				t.Errorf("got  %s\nwant %s", got, want)
			}
		})
	}
}

func TestEncodeDecimalPrecisionCeiling(t *testing.T) {
	// Beyond what a float64 represents exactly, the value is carried as a
	// string rather than silently rounded.
	got := enc(t, []NamedResultSet{{
		Spec: ResultSpec{Name: "v", Shape: ShapeObject},
		Set: ResultSet{
			Columns: []string{"c"}, Types: []string{"DECIMAL"},
			Rows: [][]any{{[]byte("123456789012345678901234.5")}},
		},
	}})
	if !strings.Contains(got, `"123456789012345678901234.5"`) {
		t.Errorf("oversized decimal should be carried as a string, got %s", got)
	}
}

func TestEncodeNonFiniteFloatsBecomeNullWithWarning(t *testing.T) {
	// encoding/json errors on NaN. One pathological counter must not abort
	// a whole audit, but it must not vanish silently either.
	b, warns, err := Encode([]NamedResultSet{{
		Spec: ResultSpec{Name: "v", Shape: ShapeObject},
		Set:  ResultSet{Columns: []string{"pct"}, Rows: [][]any{{math.NaN()}}},
	}})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if string(b) != `{"v":{"pct":null}}` {
		t.Errorf("got %s", b)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "pct") {
		t.Errorf("expected a warning naming the column, got %v", warns)
	}
}

func TestEncodeDatetimeIsNotRFC3339(t *testing.T) {
	ts := time.Date(2026, 8, 8, 14, 30, 0, 0, time.UTC)
	got := enc(t, []NamedResultSet{{
		Spec: ResultSpec{Name: "v", Shape: ShapeObject},
		Set:  ResultSet{Columns: []string{"d"}, Rows: [][]any{{ts}}},
	}})
	const emitted = "2026-08-08T14:30:00"
	if !strings.Contains(got, emitted) {
		t.Fatalf("got %s, want it to contain %s", got, emitted)
	}
	if _, err := time.Parse(time.RFC3339, emitted); err == nil {
		t.Error("emitted datetime parsed as RFC 3339; it must be ISO 8601 local " +
			"without an offset, because the server's offset is unknown")
	}
	if _, err := time.Parse("2006-01-02T15:04:05", emitted); err != nil {
		t.Errorf("emitted datetime is not valid ISO 8601 local: %v", err)
	}
}
