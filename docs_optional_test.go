package sqlauditor_test

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	sqlauditor "github.com/rudi-bruchez/sql-auditor"
	"github.com/rudi-bruchez/sql-auditor/collect"
)

// The DBA guide lists every collector that declares an optional permission,
// with what it loses without it, because that is the table a DBA reads when
// deciding which grant to refuse. Written by hand it would fall behind the
// first collector converted after it, the way the @timeout table did, so its
// pairs are checked against the corpus. The loss column stays prose.
func TestTheGuideListsEveryOptionalPermission(t *testing.T) {
	scripts, err := collect.Discover(sqlauditor.Queries, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	var want []string
	for _, s := range scripts {
		for _, p := range s.OptionalPermissions {
			want = append(want, s.Path+" "+p)
		}
	}
	slices.Sort(want)

	b, err := os.ReadFile("docs/dba-guide.md")
	if err != nil {
		t.Fatal(err)
	}
	_, table, found := strings.Cut(string(b), "| Collector | Optional | Lost without it |\n| --- | --- | --- |\n")
	if !found {
		t.Fatal("docs/dba-guide.md has no table of optional permissions")
	}
	table, _, _ = strings.Cut(table, "\n\n")
	row := regexp.MustCompile("^\\| `([^`]+)` \\| `([^`]+)` \\| .+ \\|$")
	var got []string
	for _, line := range strings.Split(table, "\n") {
		m := row.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("unexpected row in the optional permissions table: %q", line)
		}
		key, ok := collect.NormalisePermission(m[2])
		if !ok {
			t.Fatalf("the table names %q, which is not a permission", m[2])
		}
		got = append(got, strings.TrimPrefix(m[1], "queries/")+" "+key)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("the optional permissions table in docs/dba-guide.md disagrees with the corpus:\n got  %v\n want %v", got, want)
	}
	if len(want) == 0 {
		t.Error("no collector declares an optional permission; the table describes nothing")
	}
}
