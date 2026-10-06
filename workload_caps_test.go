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
	applied   []capForm // the exact forms that apply the cap, each counted
	projected []string  // root fields that must carry @variable as is
}

// capForm is one exact form in which a file applies a cap, and how many times
// the file holds it. Until 043 every cap was a TOP (@variable) and the count
// of that one string was the whole check; 043 pins with TOP (@cap + 1), as
// 027 does, and reads its chunk as a range of ranks, which a count of
// TOP (@chunk) cannot see: the only count it passes is 0, which asserts
// nothing.
type capForm struct {
	text  string
	count int
}

// top is the form every cap of this table used before 043.
func top(variable string, n int) []capForm {
	return []capForm{{"TOP (" + variable + ")", n}}
}

var workloadCaps = []workloadCap{
	{"020.query-store.sql", "@listing_cap", 200, top("@listing_cap", 2), []string{"listing_cap"}},
	{"023.query-store-most-executed.sql", "@listing_cap", 200, top("@listing_cap", 2), []string{"listing_cap"}},
	{"024.query-store-rowcount.sql", "@listing_cap", 200, top("@listing_cap", 1), []string{"listing_cap"}},
	{"026.query-store-interrupted.sql", "@listing_cap", 200, top("@listing_cap", 2), []string{"listing_cap"}},
	{"028.query-store-resources.sql", "@listing_cap", 200, top("@listing_cap", 1), []string{"listing_cap"}},
	{"060.spills.sql", "@listing_cap", 200, top("@listing_cap", 1), []string{"listing_cap"}},
	{"042.parallel-cost-distribution.sql", "@examined", 1000, top("@examined", 1), []string{"examined.cap"}},
	{"030.implicit-conversions.sql", "@examined", 2500, top("@examined", 1), []string{"bounds.examined_cap"}},
	{"030.implicit-conversions.sql", "@candidate_cap", 1000, top("@candidate_cap", 1), []string{"bounds.candidate_cap"}},
	{"053.plan-warnings.sql", "@examined", 1000, top("@examined", 1), []string{"bounds.examined_cap"}},
	{"053.plan-warnings.sql", "@candidate_cap", 500, top("@candidate_cap", 1), []string{"bounds.candidate_cap"}},
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
		for _, f := range c.applied {
			if n := strings.Count(code, f.text); n != f.count {
				t.Errorf("%s: %s applied %d times, want %d", c.file, f.text, n, f.count)
			}
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
