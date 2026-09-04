package collect

import (
	"strings"
	"testing"
)

func rootSet(cols []string, vals []any) []NamedResultSet {
	return []NamedResultSet{{
		Spec: ResultSpec{Name: RootSetName, Shape: ShapeObject},
		Set:  ResultSet{Columns: cols, Rows: [][]any{vals}},
	}}
}

// The guard pattern the four blockable collectors use turns a lock timeout from
// "the document is lost" into "this area of the document is empty and says
// why". That is the right trade, and it has one cost that must not be paid
// silently: the pipeline used to see the failure as an error and now sees a
// successful collector. A run that lost half a database's schema would print
// "0 error(s)" and exit 0.
//
// So the root object's errors.* columns are read back out. The convention is
// the collector's, not the pipeline's — any collector adopting the pattern is
// reported the same way, and one that does not carry the columns reports
// nothing.
func TestPartialAreasAreReadBackOutOfTheRootObject(t *testing.T) {
	t.Run("a blocked area is named with its error number", func(t *testing.T) {
		got, _ := reportedErrors(rootSet(
			[]string{"database", "errors.counts", "errors.usage", "errors.missing"},
			[]any{"SALESDB", int64(0), int64(1222), int64(0)}))
		if len(got) != 1 {
			t.Fatalf("reportedErrors = %v, want exactly the usage area", got)
		}
		if got["usage"] != 1222 {
			t.Errorf("reportedErrors[usage] = %d, want 1222", got["usage"])
		}
	})

	t.Run("a clean collector reports nothing", func(t *testing.T) {
		got, _ := reportedErrors(rootSet(
			[]string{"errors.counts", "errors.usage"},
			[]any{int64(0), int64(0)}))
		if len(got) != 0 {
			t.Errorf("reportedErrors = %v, want none", got)
		}
	})

	t.Run("a collector without the convention reports nothing", func(t *testing.T) {
		got, _ := reportedErrors(rootSet([]string{"database", "rows"}, []any{"SALESDB", int64(7)}))
		if len(got) != 0 {
			t.Errorf("reportedErrors = %v, want none", got)
		}
	})

	t.Run("an empty root is not a panic", func(t *testing.T) {
		sets := []NamedResultSet{{
			Spec: ResultSpec{Name: RootSetName, Shape: ShapeObject},
			Set:  ResultSet{Columns: []string{"errors.counts"}},
		}}
		if got, _ := reportedErrors(sets); len(got) != 0 {
			t.Errorf("reportedErrors = %v, want none", got)
		}
	})
}

func TestPartialWarningNamesTheScriptTheTargetAndTheAreas(t *testing.T) {
	msg := partialWarning("70.schema/020.index-usage.sql", "SALESDB",
		map[string]int64{"usage": 1222, "counts": 1222}, 3)
	for _, want := range []string{"70.schema/020.index-usage.sql", "SALESDB", "counts", "usage", "1222"} {
		if !strings.Contains(msg, want) {
			t.Errorf("partialWarning should name %q:\n%s", want, msg)
		}
	}
	// Sorted, so two runs of the same degraded collection produce the same
	// manifest rather than a diff that is only map iteration order. Compared
	// on the area list alone: "usage" also occurs in the script's own name,
	// earlier, which made the first version of this assertion fail on correct
	// output.
	_, list, _ := strings.Cut(msg, "except ")
	if strings.Index(list, "counts") > strings.Index(list, "usage") {
		t.Errorf("areas should be named in a fixed order:\n%s", msg)
	}
}

// A collector that got nothing at all must not be described as one that
// collected everything but a few areas. The first wording of this warning did
// exactly that: 030.index-operational had all three of its areas refused and
// the manifest said "collected everything except contention, counts, heaps".
func TestPartialWarningSaysWhenNothingCameBack(t *testing.T) {
	all := partialWarning("70.schema/030.index-operational.sql", "SALESDB",
		map[string]int64{"counts": 1222, "heaps": 1222, "contention": 1222}, 3)
	if !strings.Contains(all, "every area of this collector was refused") {
		t.Errorf("a collector that read nothing should say so:\n%s", all)
	}
	some := partialWarning("70.schema/010.objects.sql", "SALESDB",
		map[string]int64{"tables": 1222}, 5)
	if !strings.Contains(some, "1 of its 5 areas") {
		t.Errorf("a partly-read collector should count the areas:\n%s", some)
	}
	if strings.Contains(some, "every area") {
		t.Errorf("a partly-read collector must not claim it read nothing:\n%s", some)
	}
}
