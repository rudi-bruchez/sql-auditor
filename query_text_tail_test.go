package sqlauditor_test

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	sqlauditor "github.com/rudi-bruchez/sql-auditor"
)

// Every default Query Store listing keeps the first 500 characters of a
// statement in text and, since October 2026, what follows them in text_tail,
// at most its last 500, with the whole length in text_length. The tail was
// added because a listing of 028 held extraction queries whose FROM clause
// was past character 500 and never reached the archive.
//
// The collectors are those that declare @discloses: query_text, read from the
// corpus rather than listed here, so a new one is held to the same three
// fields. The live test of 028 checks what the expressions return.
var (
	discloseQueryText = regexp.MustCompile(`(?m)^-- @discloses:.*\bquery_text\b`)
	textHead          = regexp.MustCompile(`AS \[text\]`)
	textTriple        = regexp.MustCompile(`LEFT\(qt\.query_sql_text, 500\)\s+AS \[text\],\s+` +
		`(MAX\()?CASE WHEN LEN\(qt\.query_sql_text\) > 500\s+` +
		`THEN RIGHT\(LEFT\(qt\.query_sql_text, LEN\(qt\.query_sql_text\)\),\s+` +
		`CASE WHEN LEN\(qt\.query_sql_text\) > 1000 THEN 500\s+` +
		`ELSE LEN\(qt\.query_sql_text\) - 500 END\)\s+` +
		`END\)?\s+AS \[text_tail\],\s+` +
		`(MAX\()?LEN\(qt\.query_sql_text\)\)?\s+AS \[text_length\]`)
	textUnbounded = regexp.MustCompile(`qt\.query_sql_text\s+AS`)
)

func TestQueryStoreListingsKeepTheTailOfALongText(t *testing.T) {
	var held []string
	err := fs.WalkDir(sqlauditor.Queries, "queries", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".sql") {
			return err
		}
		b, err := fs.ReadFile(sqlauditor.Queries, path)
		if err != nil {
			return err
		}
		if !discloseQueryText.Match(b) {
			return nil
		}
		held = append(held, path)
		code := collectorCode(t, path)
		heads := len(textHead.FindAllString(code, -1))
		triples := len(textTriple.FindAllString(code, -1))
		if heads == 0 {
			t.Errorf("%s declares @discloses: query_text and projects no [text]", path)
		}
		if triples != heads {
			t.Errorf("%s projects [text] %d times and [text], [text_tail], [text_length] in the form of 020 %d times",
				path, heads, triples)
		}
		if textUnbounded.MatchString(code) {
			t.Errorf("%s projects query_sql_text whole", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(held) == 0 {
		t.Fatal("no collector declares @discloses: query_text")
	}
	t.Logf("held: %v", held)
}
