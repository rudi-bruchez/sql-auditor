package sqlauditor_test

import (
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"

	sqlauditor "github.com/rudi-bruchez/sql-auditor"
)

// The caps of 80.workload, and the fields that say a cap was reached.
//
// Each capped collector there declares its cap once, in a variable, and both
// the TOP that applies it and the root field that reports it read that
// variable. The pairing is what makes the field true: a TOP written back as a
// literal would go on cutting at the old number while the root reported the
// new one, and no collection on a small cache would notice, because a cap
// that is not reached cuts nothing. Until 4 October 2026 the listings carried
// a literal TOP (50) beside a literal 50 AS [listing_cap], and two of them
// carried no field at all.
//
// The values are written here on purpose. They were raised on measurements
// (docs/caps-inventory.md), and a change to one is a decision that should
// show in this file's diff as well as in the collector's.
type workloadCap struct {
	file      string
	variable  string
	value     int
	tops      int      // how many TOP (@variable) the file must apply
	projected []string // root fields that must carry @variable as is
}

var workloadCaps = []workloadCap{
	{"020.query-store.sql", "@listing_cap", 200, 2, []string{"listing_cap"}},
	{"023.query-store-most-executed.sql", "@listing_cap", 200, 2, []string{"listing_cap"}},
	{"024.query-store-rowcount.sql", "@listing_cap", 200, 1, []string{"listing_cap"}},
}

var (
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	lineComment  = regexp.MustCompile(`--[^\n]*`)
	literalTop   = regexp.MustCompile(`(?i)\bTOP\s*\(\s*(\d+)\s*\)`)
)

func workloadSQL(t *testing.T, name string) (raw, code string) {
	t.Helper()
	b, err := fs.ReadFile(sqlauditor.Queries, "queries/80.workload/"+name)
	if err != nil {
		t.Fatal(err)
	}
	raw = string(b)
	code = lineComment.ReplaceAllString(blockComment.ReplaceAllString(raw, ""), "")
	return raw, code
}

func TestWorkloadCapsAreDeclaredAppliedAndReported(t *testing.T) {
	for _, c := range workloadCaps {
		_, code := workloadSQL(t, c.file)
		decl := regexp.MustCompile(`(?i)DECLARE\s+` + regexp.QuoteMeta(c.variable) + `\s+int\s*=\s*(\d+)\s*;`)
		m := decl.FindAllStringSubmatch(code, -1)
		if len(m) != 1 {
			t.Errorf("%s: %d declarations of %s, want 1", c.file, len(m), c.variable)
			continue
		}
		if v, _ := strconv.Atoi(m[0][1]); v != c.value {
			t.Errorf("%s: %s is %d, want %d", c.file, c.variable, v, c.value)
		}
		if n := strings.Count(code, "TOP ("+c.variable+")"); n != c.tops {
			t.Errorf("%s: TOP (%s) applied %d times, want %d", c.file, c.variable, n, c.tops)
		}
		for _, lit := range literalTop.FindAllStringSubmatch(code, -1) {
			if n, _ := strconv.Atoi(lit[1]); n > 1 {
				t.Errorf("%s: literal %s outside a comment; a cap goes through its variable so the reported value cannot drift from the applied one", c.file, lit[0])
			}
		}
		for _, field := range c.projected {
			proj := regexp.MustCompile(regexp.QuoteMeta(c.variable) + `\s+AS\s+\[` + regexp.QuoteMeta(field) + `\]`)
			if !proj.MatchString(code) {
				t.Errorf("%s: root field %s does not project %s", c.file, field, c.variable)
			}
		}
	}
}

// 024 ranks by call count "like 023, so the two files rank the same
// population and can be read side by side". Raised together on 4 October
// 2026; this keeps them together.
func TestMostExecutedAndRowcountRankTheSameNumber(t *testing.T) {
	val := func(name string) string {
		_, code := workloadSQL(t, name)
		m := regexp.MustCompile(`DECLARE\s+@listing_cap\s+int\s*=\s*(\d+)\s*;`).FindStringSubmatch(code)
		if m == nil {
			t.Fatalf("%s: no @listing_cap", name)
		}
		return m[1]
	}
	if a, b := val("023.query-store-most-executed.sql"), val("024.query-store-rowcount.sql"); a != b {
		t.Errorf("023 ranks %s and 024 ranks %s; they must rank the same population", a, b)
	}
}
