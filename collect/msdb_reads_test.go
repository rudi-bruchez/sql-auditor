package collect

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// A collector that reads msdb without declaring an msdb capability is not
// skipped by the preflight when the login cannot read msdb, so the read must
// be guarded or it takes the whole file down with it. 030.server-surface.sql
// counted Agent proxies with a bare subquery on msdb.dbo.sysproxies, and a
// login without msdb access lost every linked server, trigger, credential and
// audit to "The SELECT permission was denied on the object 'sysproxies'".
// Declaring a capability would not have helped there: no probe reads
// sysproxies, so the corpus' answer, as in 042.replication-distribution.sql,
// is a read inside BEGIN TRY that leaves the rest of the file standing.
//
// The rule checked is therefore narrow: in a file declaring none of the msdb
// capabilities, every reference to msdb in code sits between a BEGIN TRY and
// its END TRY. Comments are stripped first, since several files explain msdb
// in prose.
func TestUndeclaredMsdbReadsAreGuarded(t *testing.T) {
	scripts, err := Discover(os.DirFS(".."), "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	msdb := regexp.MustCompile(`(?i)\bmsdb\s*\.`)
	begin := regexp.MustCompile(`(?i)\bBEGIN\s+TRY\b`)
	end := regexp.MustCompile(`(?i)\bEND\s+TRY\b`)
	checked := 0
	for _, s := range scripts {
		if slices.ContainsFunc(s.Permissions, func(p string) bool {
			return slices.Contains(msdbCapabilities, p)
		}) {
			continue
		}
		b, err := os.ReadFile(filepath.Join("..", "queries", s.Path))
		if err != nil {
			t.Fatal(err)
		}
		sql := StripSQLComments(string(b))
		refs := msdb.FindAllStringIndex(sql, -1)
		if len(refs) == 0 {
			continue
		}
		checked++
		for _, r := range refs {
			depth := len(begin.FindAllStringIndex(sql[:r[0]], -1)) - len(end.FindAllStringIndex(sql[:r[0]], -1))
			if depth <= 0 {
				t.Errorf("%s reads msdb outside BEGIN TRY without declaring an msdb capability; "+
					"a login that cannot read msdb fails the whole file. Guard the read, or declare "+
					"the capability whose probe covers it: %q", s.Path, sql[r[0]:min(len(sql), r[0]+60)])
			}
		}
	}
	// The rule must have had something to check, or a change to Discover or
	// to the regex could leave it green over nothing.
	if checked == 0 {
		t.Error("no file reads msdb without declaring a capability; the rule checked nothing")
	}
}
