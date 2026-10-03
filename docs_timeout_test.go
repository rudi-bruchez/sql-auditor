package sqlauditor_test

import (
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	sqlauditor "github.com/rudi-bruchez/sql-auditor"
	"github.com/rudi-bruchez/sql-auditor/collect"
)

// The DBA guide tells a reader how many collectors sit at each @timeout, which
// is the number they need when a run stops on a deadline. Written by hand, it
// went stale with every collector added: on 3 October 2026 two branches each
// recounted it on their own base and the merge gave a table that was wrong on
// both sides. The corpus is the source of truth, so the table is checked
// against it rather than trusted.
func TestTheGuideCountsTheTimeoutTiersOfTheCorpus(t *testing.T) {
	scripts, err := collect.Discover(sqlauditor.Queries, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	want := map[int]int{}
	for _, s := range scripts {
		want[s.TimeoutSec]++
	}

	b, err := os.ReadFile("docs/dba-guide.md")
	if err != nil {
		t.Fatal(err)
	}
	_, table, found := strings.Cut(string(b), "| `@timeout` | Files |\n| --- | --- |\n")
	if !found {
		t.Fatal("docs/dba-guide.md has no @timeout table")
	}
	table, _, _ = strings.Cut(table, "\n\n")
	row := regexp.MustCompile(`^\| (\d+) s \| (\d+) \|$`)
	got := map[int]int{}
	for _, line := range strings.Split(table, "\n") {
		m := row.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("unexpected row in the @timeout table: %q", line)
		}
		var sec, n int
		fmt.Sscan(m[1], &sec)
		fmt.Sscan(m[2], &n)
		got[sec] = n
	}
	if !maps.Equal(got, want) {
		var lines []string
		for _, sec := range slices.Sorted(maps.Keys(want)) {
			lines = append(lines, fmt.Sprintf("| %d s | %d |", sec, want[sec]))
		}
		t.Errorf("the @timeout table in docs/dba-guide.md disagrees with the corpus; it should read:\n%s", strings.Join(lines, "\n"))
	}
}
