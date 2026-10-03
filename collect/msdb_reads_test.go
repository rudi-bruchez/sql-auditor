package collect

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A collector runs when the preflight probes of the capabilities it declares
// pass, and a probe answers for the msdb objects it reads, not for msdb as a
// whole. A read of any other msdb object can still be refused on a login the
// preflight let through, and an unguarded refusal takes the whole file down
// with it. 030.server-surface.sql counted Agent proxies with a bare subquery
// on msdb.dbo.sysproxies, and a login without msdb access lost every linked
// server, trigger, credential and audit to "The SELECT permission was denied
// on the object 'sysproxies'". Declaring MSDB READ would not have helped
// there: its probe reads backupset, so a login granted exactly what the grant
// script asks for passes it and is still refused sysproxies.
//
// The rule checked is therefore per object: every msdb object a file names in
// code is either vouched for by a capability the file declares, or sits
// between a BEGIN TRY and its END TRY. Comments are stripped first, since
// several files explain msdb in prose.
//
// A probe vouches for the objects it reads, and for the objects a default
// msdb grants to the same principal, since the login that passes the probe
// reaches them by the same route. Read from msdb's sys.database_permissions
// on SQL Server 2025: public holds SELECT on backupset, backupmediafamily,
// restorehistory and suspect_pages, so a login that reads backupset reads the
// other three; SQLAgentUserRole, which SQLAgentReaderRole implies, holds
// SELECT on sysjobs_view and syscategories and EXECUTE on sp_help_jobhistory.
// sysproxies is granted to TargetServersRole alone, which nothing the
// preflight probes or the grant script offers reaches.
var msdbSameRoute = map[string][]string{
	"msdb_read":  {"backupmediafamily", "restorehistory", "suspect_pages"},
	"agent_jobs": {"syscategories", "sp_help_jobhistory"},
}

// msdbKnownUnguarded lists reads the rule refuses and the corpus still makes,
// each with what it costs. The test fails when an entry stops being needed, so
// the list cannot outlive the fix.
var msdbKnownUnguarded = map[string]string{
	// A login built from the grant script (SQLAgentReaderRole and SELECT on
	// sysjobsteps) passes both probes and is refused the proxy join.
	"50.agent/020.job-steps.sql sysproxies": "loses every job step",
}

func TestUndeclaredMsdbReadsAreGuarded(t *testing.T) {
	scripts, err := Discover(os.DirFS(".."), "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	ref := regexp.MustCompile(`(?i)(?:\bmsdb|\[msdb\])\s*\.\s*(?:\[?dbo\]?)?\s*\.\s*\[?(\w+)\]?`)
	bare := regexp.MustCompile(`(?i)(\bmsdb|\[msdb\])\s*\.`)

	// What each probe vouches for, read from the probes themselves so that a
	// probe narrowed or widened moves the rule with it.
	covers := map[string]map[string]bool{}
	for _, c := range Capabilities() {
		for _, m := range ref.FindAllStringSubmatch(c.SQL, -1) {
			if covers[c.Name] == nil {
				covers[c.Name] = map[string]bool{}
			}
			covers[c.Name][strings.ToLower(m[1])] = true
		}
	}
	for c, objects := range msdbSameRoute {
		if covers[c] == nil {
			t.Fatalf("msdbSameRoute names %s, which no probe reads in msdb", c)
		}
		for _, o := range objects {
			covers[c][o] = true
		}
	}
	if !covers["msdb_read"]["backupset"] || covers["msdb_read"]["sysproxies"] {
		t.Fatalf("probe coverage read wrong: msdb_read covers %v", covers["msdb_read"])
	}

	begin := regexp.MustCompile(`(?i)\bBEGIN\s+TRY\b`)
	end := regexp.MustCompile(`(?i)\bEND\s+TRY\b`)
	checked := 0
	seenKnown := map[string]bool{}
	for _, s := range scripts {
		b, err := os.ReadFile(filepath.Join("..", "queries", s.Path))
		if err != nil {
			t.Fatal(err)
		}
		sql := StripSQLComments(string(b))
		// Every msdb reference must be one the object pattern understands,
		// or an unusual spelling would slip past the per-object rule.
		if n, m := len(bare.FindAllStringIndex(sql, -1)), len(ref.FindAllStringIndex(sql, -1)); n != m {
			t.Errorf("%s: %d msdb references, %d of them parsed as msdb.<schema>.<object>", s.Path, n, m)
		}
		for _, r := range ref.FindAllStringSubmatchIndex(sql, -1) {
			object := strings.ToLower(sql[r[2]:r[3]])
			covered := false
			for _, p := range s.Permissions {
				if covers[p][object] {
					covered = true
				}
			}
			if covered {
				continue
			}
			checked++
			depth := len(begin.FindAllStringIndex(sql[:r[0]], -1)) - len(end.FindAllStringIndex(sql[:r[0]], -1))
			if key := s.Path + " " + object; depth <= 0 && msdbKnownUnguarded[key] != "" {
				seenKnown[key] = true
			} else if depth <= 0 {
				t.Errorf("%s reads msdb.dbo.%s outside BEGIN TRY, and no capability it declares vouches for "+
					"that object; a login the preflight lets through can still be refused it and lose "+
					"the whole file. Guard the read, or declare the capability whose probe covers it: %q",
					s.Path, object, sql[r[0]:min(len(sql), r[0]+60)])
			}
		}
	}
	for key := range msdbKnownUnguarded {
		if !seenKnown[key] {
			t.Errorf("msdbKnownUnguarded lists %q, which is no longer an unguarded read; remove the entry", key)
		}
	}
	// The rule must have had something to check, or a change to Discover or
	// to the regex could leave it green over nothing.
	if checked == 0 {
		t.Error("no msdb read falls outside a declared probe; the rule checked nothing")
	}
}
