package collect

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
var msdbKnownUnguarded = map[string]string{}

var (
	msdbRef  = regexp.MustCompile(`(?i)(?:\bmsdb|\[msdb\])\s*\.\s*(?:\[?dbo\]?)?\s*\.\s*\[?(\w+)\]?`)
	msdbBare = regexp.MustCompile(`(?i)(\bmsdb|\[msdb\])\s*\.`)
	tryBegin = regexp.MustCompile(`(?i)\bBEGIN\s+TRY\b`)
	tryEnd   = regexp.MustCompile(`(?i)\bEND\s+TRY\b`)
)

// msdbCoverage is what each probe vouches for, read from the probes themselves
// so that a probe narrowed or widened moves the rule with it.
func msdbCoverage(t *testing.T) map[string]map[string]bool {
	t.Helper()
	covers := map[string]map[string]bool{}
	for _, c := range Capabilities() {
		for _, m := range msdbRef.FindAllStringSubmatch(c.SQL, -1) {
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
	return covers
}

// msdbVouchers are the capabilities whose probes may vouch for a file's msdb
// reads: the ones it requires, and only those. A capability in
// @optional_permissions is never a gate, so the file runs when its probe was
// refused, and an unguarded read the probe "covers" is then refused with the
// whole file.
func msdbVouchers(s Script) []string {
	return s.Permissions
}

// msdbUnguarded returns the msdb reads of sql that no capability of s vouches
// for and that sit outside BEGIN TRY, as "object: excerpt", and how many reads
// were checked at all. sql has had its comments stripped.
func msdbUnguarded(s Script, sql string, covers map[string]map[string]bool) (out []string, checked int) {
	for _, r := range msdbRef.FindAllStringSubmatchIndex(sql, -1) {
		object := strings.ToLower(sql[r[2]:r[3]])
		covered := false
		for _, p := range msdbVouchers(s) {
			if covers[p][object] {
				covered = true
			}
		}
		if covered {
			continue
		}
		checked++
		depth := len(tryBegin.FindAllStringIndex(sql[:r[0]], -1)) - len(tryEnd.FindAllStringIndex(sql[:r[0]], -1))
		if depth <= 0 {
			out = append(out, object+": "+sql[r[0]:min(len(sql), r[0]+60)])
		}
	}
	return out, checked
}

func TestUndeclaredMsdbReadsAreGuarded(t *testing.T) {
	scripts, err := Discover(os.DirFS(".."), "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	covers := msdbCoverage(t)

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
		if n, m := len(msdbBare.FindAllStringIndex(sql, -1)), len(msdbRef.FindAllStringIndex(sql, -1)); n != m {
			t.Errorf("%s: %d msdb references, %d of them parsed as msdb.<schema>.<object>", s.Path, n, m)
		}
		unguarded, n := msdbUnguarded(s, sql, covers)
		checked += n
		for _, u := range unguarded {
			object, excerpt, _ := strings.Cut(u, ": ")
			if key := s.Path + " " + object; msdbKnownUnguarded[key] != "" {
				seenKnown[key] = true
				continue
			}
			t.Errorf("%s reads msdb.dbo.%s outside BEGIN TRY, and no capability it requires vouches for "+
				"that object; a login the preflight lets through can still be refused it and lose "+
				"the whole file. Guard the read, or require the capability whose probe covers it: %q",
				s.Path, object, excerpt)
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

// An optional MSDB READ vouches for nothing. The same unguarded read of
// backupset is allowed when the capability is required, since a refusal then
// skips the file before it runs, and refused when it is optional, since the
// file then runs for the login the probe refused.
func TestAnOptionalMsdbCapabilityVouchesForNothing(t *testing.T) {
	covers := msdbCoverage(t)
	const body = `
-- @timeout: 30
SELECT COUNT(*) AS [n] FROM msdb.dbo.backupset;
`
	required := parseScript("60.backup/099.t.sql", "-- @permissions: CONNECT, MSDB READ"+body)
	optional := parseScript("60.backup/099.t.sql",
		"-- @permissions: CONNECT\n-- @optional_permissions: MSDB READ"+body)
	// The fixture is a header and one statement, not a collector, so only
	// what the rule reads is checked: the two directives parsed as intended.
	if !slices.Contains(required.Permissions, "msdb_read") ||
		!slices.Contains(optional.OptionalPermissions, "msdb_read") ||
		slices.Contains(optional.Permissions, "msdb_read") {
		t.Fatalf("fixtures parsed wrong: required %v, optional %v / %v",
			required.Permissions, optional.Permissions, optional.OptionalPermissions)
	}
	if got, _ := msdbUnguarded(required, StripSQLComments(required.SQL), covers); len(got) != 0 {
		t.Errorf("a required MSDB READ vouches for backupset, got %v", got)
	}
	if got, _ := msdbUnguarded(optional, StripSQLComments(optional.SQL), covers); len(got) != 1 {
		t.Errorf("an optional MSDB READ must not vouch for an unguarded backupset read, got %v", got)
	}
}
