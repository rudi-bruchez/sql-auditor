package collect

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// GrantScript turns the preflight result into a T-SQL script that grants
// exactly what was denied, and nothing else.
//
// Why this exists. The collector needs permissions the person running it
// usually does not hold and cannot grant themselves. Until now the answer was
// a paragraph in a README, which means every operator retypes it from memory,
// and the usual outcome of that is db_datareader or sysadmin "to get it
// working". A generated script removes the guesswork in the safe direction:
// it names the login the server actually sees, it asks for the narrowest
// permission that the documentation says covers the reads we make, and it says
// in the file what each line buys and what it costs.
//
// It is deliberately a file to read, not a statement to run. The collector is
// connected with a login that by definition cannot execute any of this, so it
// could not apply the script even if it wanted to. What it produces goes to a
// DBA who will read it, and it is written for that reader.
//
// The permission model is version-dependent and the script branches on it.
// SQL Server 2022 introduced narrower alternatives to VIEW SERVER STATE, and
// on those instances asking for the wide one would be asking for more than we
// need.

// Where a section's statements have to run. Getting this wrong is the classic
// way a permission script fails: a GRANT on an msdb object issued in master
// either errors or, worse, grants on a same-named object elsewhere.
const (
	sectionInMaster = iota
	sectionInMsdb
	// sectionOwnsContext is for a section that switches database itself,
	// several times — the per-database block. It also emits its own GO.
	sectionOwnsContext
)

// grantSection is one block of the generated script: a heading, the reason,
// and the statements.
type grantSection struct {
	title     string
	why       []string // prose lines, already wrapped
	statement []string
	caveat    []string // security consequences the DBA must weigh, if any
	marker    int      // one of the section* constants above
}

// GrantScriptInput is everything BuildGrantScript needs. It is a struct rather
// than a parameter list because the caller assembles it from three different
// sources and a positional call would be unreadable at the call site.
type GrantScriptInput struct {
	// Login is the login as the SERVER names it, from SUSER_SNAME(), not the
	// string in .env. The two differ more often than one would like: a Windows
	// login arrives as DOMAIN\user whatever the case written in the config, a
	// login can be mapped through a group, and a GRANT naming the config string
	// would either fail or, worse, create permissions on a principal nobody is
	// using. If it is empty the script cannot be written at all.
	Login string
	// Instance and Version are stamped into the header so the file cannot be
	// run against the wrong server by mistake, and Version also selects the
	// permission vocabulary.
	Instance, Version, Edition string
	Checks                     []CapabilityCheck
	// Scripts is the corpus. Each section lists the collectors that declared
	// the capability it grants, so the reader sees what the line buys in terms
	// of actual output rather than in terms of a permission name. Deriving it
	// from the corpus rather than hard-coding it means the list cannot drift
	// when a collector is added.
	Scripts []Script
	// NoAccessDatabases are the databases the run would skip because the login
	// cannot connect to them.
	//
	// This belongs here because it is the failure the permission probes cannot
	// see. All six probes can come back "ok" while two thirds of the collectors
	// are skipped, since the per-database ones run inside each database and
	// need to get in. A script that fixed only what the probes measured would
	// leave the operator with six green lines and a third of an audit.
	NoAccessDatabases []string
	// Tool is the version string of the binary that produced the file.
	Tool string
}

// majorVersion extracts the leading integer of a ProductVersion like
// "14.0.1000.169". It returns 0 when the version is unknown or unparseable,
// and every version-gated decision below treats 0 as "assume the old, wide
// permission", because asking for a permission the instance does not have
// produces an error the DBA cannot act on.
func majorVersion(v string) int {
	head, _, _ := strings.Cut(v, ".")
	n, err := strconv.Atoi(strings.TrimSpace(head))
	if err != nil {
		return 0
	}
	return n
}

// quoteIdent renders a SQL Server identifier as a delimited name. Doubling the
// closing bracket is what makes it safe: a login is an arbitrary string chosen
// elsewhere, and without this a name containing "]" would end the identifier
// early and turn the rest into statement text.
func quoteIdent(s string) string {
	return "[" + strings.ReplaceAll(s, "]", "]]") + "]"
}

// quoteLiteral renders a string literal, doubling embedded quotes for the same
// reason.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// collectorsFor lists the query paths that declared a capability, sorted.
//
// A collector behind @requires_flag is named with its flag. Without that the
// list overstates what the grant buys: someone could grant the permission,
// re-run, and still not see the collector, because it is off by default and
// the permission was never what was stopping it.
func collectorsFor(scripts []Script, capability string) []string {
	var out []string
	for _, s := range scripts {
		if s.LintError != "" {
			continue
		}
		for _, p := range s.Permissions {
			if p != capability {
				continue
			}
			line := s.Path
			if s.RequiresFlag != "" {
				line += "   (only with --" + strings.ReplaceAll(s.RequiresFlag, "_", "-") + ")"
			}
			out = append(out, line)
			break
		}
	}
	sort.Strings(out)
	return out
}

// BuildGrantScript returns the script and whether it contains any statement.
//
// The second return value matters: when nothing was denied the file is still
// written, and it still says so in words. A zero-byte file, or no file at all,
// is indistinguishable from a tool that failed to write one.
func BuildGrantScript(in GrantScriptInput) (string, bool) {
	major := majorVersion(in.Version)
	login := quoteIdent(in.Login)

	denied := map[string]bool{}
	for _, c := range in.Checks {
		if c.Status == "denied" {
			denied[c.Name] = true
		}
	}
	// A capability that errored is not a permission problem. The instance went
	// away mid-preflight, and granting anything on the strength of that reading
	// would be guesswork. Those are listed in the header as unknown instead.
	errored := map[string]bool{}
	for _, c := range in.Checks {
		if c.Status == "error" {
			errored[c.Name] = true
		}
	}

	var sections []grantSection
	needsMsdbUser := denied["msdb_read"] || denied["agent_jobs"]

	// --- server-level state -------------------------------------------------

	// error_log rides on the same permission as the DMVs below 2022, so the two
	// are decided together rather than emitted twice.
	wantState := denied["view_server_state"]
	wantErrorLog := denied["error_log"]

	if wantState || (wantErrorLog && major < 16) {
		s := grantSection{title: "Read performance counters"}
		if major >= 16 {
			s.statement = []string{fmt.Sprintf("GRANT VIEW SERVER PERFORMANCE STATE TO %s;", login)}
			s.why = append(s.why,
				"This instance is SQL Server 2022 or later, where VIEW SERVER PERFORMANCE",
				"STATE covers the dynamic management views without also opening the",
				"security-related ones. VIEW SERVER STATE would work and would grant more.")
		} else {
			s.statement = []string{fmt.Sprintf("GRANT VIEW SERVER STATE TO %s;", login)}
			s.why = append(s.why,
				"Before SQL Server 2022 this is the only permission that covers the",
				"dynamic management views; the narrower VIEW SERVER PERFORMANCE STATE",
				"does not exist on this version.")
		}
		s.why = append(s.why,
			"It also carries VIEW DATABASE STATE in every database, which is what lets",
			"the per-database collectors read index usage and file statistics.")
		if wantErrorLog && major < 16 {
			s.why = append(s.why,
				"On this version it is also what sys.sp_readerrorlog requires, so the",
				"error log needs no separate grant.")
		}
		s.why = append(s.why, "", "Collectors that need it:")
		s.why = append(s.why, indentList(collectorsFor(in.Scripts, "view_server_state"))...)
		sections = append(sections, s)
	}

	if wantErrorLog && major >= 16 {
		sections = append(sections, grantSection{
			title: "Read the SQL Server error log",
			why: append([]string{
				"From SQL Server 2022 the error log has its own permission, so it can be",
				"granted without VIEW SERVER STATE. VIEW SERVER PERFORMANCE STATE also",
				"covers it; this line is redundant if that was granted above, and",
				"harmless.",
				"",
				"Collectors that need it:",
			}, indentList(collectorsFor(in.Scripts, "error_log"))...),
			statement: []string{fmt.Sprintf("GRANT VIEW ANY ERROR LOG TO %s;", login)},
		})
	}

	// --- metadata -----------------------------------------------------------

	if denied["view_any_definition"] {
		sections = append(sections, grantSection{
			title: "Read server and database metadata",
			why: append([]string{
				"Without this the catalog views do not raise an error, they return fewer",
				"rows: SQL Server filters metadata by visibility. A collection made",
				"without it therefore looks successful and is quietly incomplete, which",
				"is the failure mode this whole script exists to prevent.",
				"",
				"It carries VIEW ANY DATABASE, so the login can also see the database",
				"list. It does NOT grant access to any data in any table.",
				"",
				"Collectors that need it:",
			}, indentList(collectorsFor(in.Scripts, "view_any_definition"))...),
			statement: []string{fmt.Sprintf("GRANT VIEW ANY DEFINITION TO %s;", login)},
		})
	}

	// --- msdb ---------------------------------------------------------------

	if needsMsdbUser {
		var stmts []string
		// The user is created only if absent, so the script can be run twice.
		stmts = append(stmts,
			"IF NOT EXISTS (SELECT 1 FROM sys.database_principals WHERE name = "+quoteLiteral(in.Login)+")",
			"    CREATE USER "+login+" FOR LOGIN "+login+";")
		why := []string{
			"Backup history and the Agent job inventory live in msdb, and permissions",
			"there are database-scoped: the login needs a user in msdb before anything",
			"can be granted to it. This creates no user anywhere else.",
		}
		sections = append(sections, grantSection{title: "A user in msdb", why: why, statement: stmts, marker: sectionInMsdb})
	}

	if denied["msdb_read"] {
		sections = append(sections, grantSection{
			title:  "Read backup history",
			marker: sectionInMsdb,
			why: append([]string{
				"SELECT on one table, not db_datareader. msdb also holds Database Mail",
				"contents, job step commands and operator addresses, none of which the",
				"collector reads.",
				"",
				"Without it the report cannot say when the databases were last backed up,",
				"and must not read the silence as 'no backups exist'.",
				"",
				"Collectors that need it:",
			}, indentList(collectorsFor(in.Scripts, "msdb_read"))...),
			statement: []string{fmt.Sprintf("GRANT SELECT ON OBJECT::dbo.backupset TO %s;", login)},
		})
	}

	if denied["log_shipping"] {
		sections = append(sections, grantSection{
			title:  "Read whether this instance ships transaction logs",
			marker: sectionInMsdb,
			why: append([]string{
				"SELECT on the six log shipping tables. Neither MSDB READ nor",
				"SQLAgentReaderRole reaches them, so without this the archive cannot",
				"tell log shipping that is absent from log shipping it may not look",
				"at — and those are opposite findings.",
				"",
				"Collectors that need it:",
			}, indentList(collectorsFor(in.Scripts, "log_shipping"))...),
			// OBJECT::dbo.x and the PROBED login, exactly like the backup
			// history section above. A first version of this wrote three-part
			// names and a hardcoded "sqlauditor": GRANT does not accept a
			// database-qualified name at all, and the hardcoded principal would
			// have granted the rights to whatever unrelated login happens to
			// carry that name on the client's instance — silently, since the
			// script runs green either way. The login here is the one the
			// server itself reported through SUSER_SNAME.
			statement: []string{
				fmt.Sprintf("GRANT SELECT ON OBJECT::dbo.log_shipping_primary_databases TO %s;", login),
				fmt.Sprintf("GRANT SELECT ON OBJECT::dbo.log_shipping_secondary_databases TO %s;", login),
				fmt.Sprintf("GRANT SELECT ON OBJECT::dbo.log_shipping_secondary TO %s;", login),
				fmt.Sprintf("GRANT SELECT ON OBJECT::dbo.log_shipping_monitor_primary TO %s;", login),
				fmt.Sprintf("GRANT SELECT ON OBJECT::dbo.log_shipping_monitor_secondary TO %s;", login),
				fmt.Sprintf("GRANT SELECT ON OBJECT::dbo.log_shipping_monitor_error_detail TO %s;", login),
			},
		})
	}

	if denied["agent_job_steps"] {
		sections = append(sections, grantSection{
			title:  "Read what the Agent jobs actually run",
			marker: sectionInMsdb,
			why: append([]string{
				"SELECT on one table. SQLAgentReaderRole shows the job inventory but",
				"not the steps, so without this the report can say a maintenance job",
				"exists and ran, and nothing about what it did — which index options",
				"it passed, which databases it targeted, whether it rebuilds or",
				"reorganises.",
				"",
				"Collectors that need it:",
			}, indentList(collectorsFor(in.Scripts, "agent_job_steps"))...),
			caveat: []string{
				"Job step commands are the one place in msdb where a connection string",
				"or a password is routinely written in clear by whoever wrote the job.",
				"The collector projects the first 200 characters of T-SQL steps only,",
				"and for CmdExec, PowerShell or SSIS steps — where an external",
				"credential actually appears — nothing but the subsystem and the",
				"length. The grant itself, however, lets the login read every command",
				"in full. Decide on the login, not on the collector.",
			},
			statement: []string{fmt.Sprintf("GRANT SELECT ON OBJECT::dbo.sysjobsteps TO %s;", login)},
		})
	}

	if denied["agent_jobs"] {
		sections = append(sections, grantSection{
			title:  "Read the Agent job inventory and its history",
			marker: sectionInMsdb,
			why: append([]string{
				"sp_help_jobhistory cannot be reached by granting EXECUTE on it: the",
				"documentation states such a grant may be reverted by an upgrade, and",
				"membership of one of the Agent roles is the supported route. Reader is",
				"the narrowest of the three that can see jobs it does not own.",
				"",
				"Collectors that need it:",
			}, indentList(collectorsFor(in.Scripts, "agent_jobs"))...),
			caveat: []string{
				"SQLAgentReaderRole implies SQLAgentUserRole, and a member of",
				"SQLAgentUserRole can create and run jobs that it owns, using any proxy",
				"already granted to that role. On an instance with permissive proxies",
				"that is a real privilege, and it is the one line of this script worth",
				"a deliberate decision. If you would rather not, leave it out: the run",
				"proceeds without the Agent collector and says so in its manifest.",
			},
			statement: []string{
				fmt.Sprintf("ALTER ROLE SQLAgentReaderRole ADD MEMBER %s;", login),
				fmt.Sprintf("GRANT SELECT ON OBJECT::dbo.syscategories TO %s;", login),
			},
		})
	}

	// --- per-database access ------------------------------------------------

	if len(in.NoAccessDatabases) > 0 {
		dbs := append([]string(nil), in.NoAccessDatabases...)
		// Case-insensitive, because most instances are, and a byte sort puts
		// every capitalised name before every lower-case one — which reads as
		// no order at all to whoever is checking the list against theirs. The
		// tiebreak on the raw name keeps it deterministic on the case-sensitive
		// instances where two databases can differ only by case.
		sort.Slice(dbs, func(i, j int) bool {
			li, lj := strings.ToLower(dbs[i]), strings.ToLower(dbs[j])
			if li != lj {
				return li < lj
			}
			return dbs[i] < dbs[j]
		})
		var stmts []string
		for _, db := range dbs {
			stmts = append(stmts,
				"USE "+quoteIdent(db)+";",
				"IF NOT EXISTS (SELECT 1 FROM sys.database_principals WHERE name = "+quoteLiteral(in.Login)+")",
				"    CREATE USER "+login+" FOR LOGIN "+login+";",
				"GO",
				"")
		}
		// The trailing blank and GO are emitted per database above, so drop the
		// section's own terminator rather than printing two.
		stmts = stmts[:len(stmts)-2]
		sections = append(sections, grantSection{
			title: "Connect to each database that would otherwise be skipped",
			why: []string{
				"These databases were listed by the run as skipped, with the reason",
				"\"no access for this login\". They are not a permission the probe can",
				"measure: every probe above can be green while the per-database",
				"collectors are all skipped, because those run inside each database and",
				"the login cannot get in.",
				"",
				"A user with no role membership is enough. It carries CONNECT and",
				"nothing else; the reads themselves are already covered by the",
				"server-level grants above. In particular this is NOT db_datareader,",
				"which would hand over every table in the database.",
				"",
				"One exception, and it is opt-in: --estimate-compression runs",
				"sp_estimate_data_compression_savings, which samples real rows into",
				"tempdb and so needs SELECT on the data. Without it that one collector",
				"fails with error 229 and the run reports it. Nothing else here reads a",
				"user table.",
				"",
				"On SQL Server 2014 and later, one server-level grant replaces the whole",
				"block, at the price of also covering databases created later:",
				"",
				"  GRANT CONNECT ANY DATABASE TO " + login + ";",
				"",
				"Databases concerned:",
			},
			statement: stmts,
			marker:    sectionOwnsContext,
		})
		for _, db := range dbs {
			sections[len(sections)-1].why = append(sections[len(sections)-1].why, "  "+db)
		}
	}

	var b strings.Builder
	writeGrantHeader(&b, in, major, denied, errored)

	if len(sections) == 0 {
		b.WriteString("/*  Nothing to grant.\n\n")
		b.WriteString("    Every capability the collector probes came back usable, so this file\n")
		b.WriteString("    contains no statement. It is written anyway: a missing file and a file\n")
		b.WriteString("    saying \"nothing to do\" are not the same message.\n*/\n")
		return b.String(), false
	}

	current := sectionInMaster
	for _, s := range sections {
		// The context switch is emitted once, where it changes. A section that
		// manages its own context resets what we believe the current one is,
		// because after it the script is sitting in whichever database it
		// visited last.
		if s.marker != current && s.marker != sectionOwnsContext {
			if s.marker == sectionInMsdb {
				b.WriteString("USE msdb;\nGO\n\n")
			} else {
				b.WriteString("USE master;\nGO\n\n")
			}
			current = s.marker
		}
		if s.marker == sectionOwnsContext {
			current = sectionOwnsContext
		}
		b.WriteString("-- " + strings.Repeat("-", 72) + "\n")
		b.WriteString("-- " + s.title + "\n--\n")
		for _, line := range s.why {
			b.WriteString(strings.TrimRight("-- "+line, " ") + "\n")
		}
		if len(s.caveat) > 0 {
			b.WriteString("--\n-- WEIGH THIS ONE:\n")
			for _, line := range s.caveat {
				b.WriteString(strings.TrimRight("-- "+line, " ") + "\n")
			}
		}
		b.WriteString("\n")
		for _, st := range s.statement {
			b.WriteString(st + "\n")
		}
		if s.marker != sectionOwnsContext {
			b.WriteString("GO\n")
		}
		b.WriteString("\n")
	}

	writeGrantFooter(&b, in)
	return b.String(), true
}

// indentList renders the collector paths as comment lines, or says plainly
// that none declared the capability. An empty list here means the corpus and
// the preflight have drifted apart, and printing nothing would hide that.
func indentList(paths []string) []string {
	if len(paths) == 0 {
		return []string{"  (no collector in this corpus declares it — the preflight and the",
			"  corpus disagree, which is worth reporting as a bug)"}
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, "  "+p)
	}
	return out
}

func writeGrantHeader(b *strings.Builder, in GrantScriptInput, major int, denied, errored map[string]bool) {
	fmt.Fprintf(b, `/*  Permissions for the sql-auditor collector login
    ============================================================================

    Generated by sql-auditor %s from a permission probe run against:

        instance   %s
        version    %s  (%s)
        login      %s

    WHO RUNS THIS. Someone holding CONTROL SERVER or sysadmin. The collector
    login cannot: it has just been measured, and what it lacks is precisely
    what this script grants.

    WHAT IT DOES NOT DO. It creates no login and sets no password. It grants no
    permission on any user database and no access to application data. It
    changes no server configuration. Every statement below is a read permission
    on metadata, and each one says which collectors it unlocks.

    HOW TO CHECK IT WORKED. Re-run the probe with the same login:

        sql-auditor check

    Every line should come back "ok". Nothing else needs to change.

    HOW TO UNDO IT. Replace GRANT with REVOKE, ALTER ROLE ... ADD MEMBER with
    DROP MEMBER, and DROP USER in msdb. The order does not matter.

`, orUnknown(in.Tool), orUnknown(in.Instance), orUnknown(in.Version), orUnknown(in.Edition), in.Login)

	b.WriteString("    WHAT THE PROBE FOUND\n\n")
	for _, c := range in.Checks {
		mark := "granted "
		switch c.Status {
		case "denied":
			mark = "MISSING "
		case "error":
			mark = "unknown "
		}
		fmt.Fprintf(b, "        %s %-22s %s\n", mark, c.Name, c.Label)
	}
	if len(errored) > 0 {
		b.WriteString(`
    One or more probes did not get an answer at all: the instance became
    unreachable during the check. Those are marked "unknown" above and this
    script grants nothing for them, because a dropped connection is not a
    refusal. Re-run the check before deciding.
`)
	}
	if major == 0 {
		b.WriteString(`
    The product version could not be read, so this script assumes the older
    and wider permission model. On SQL Server 2022 or later a narrower grant
    exists; re-generate once the version is known.
`)
	}
	b.WriteString("*/\n\n")
}

func writeGrantFooter(b *strings.Builder, in GrantScriptInput) {
	fmt.Fprintf(b, `-- %s
-- One last thing worth saying out loud: this is the least privilege that lets
-- the collector do its job, and it is still a read of every piece of metadata
-- on the instance — schema, configuration, job names, backup history. It is
-- not access to your data, and it is not nothing either. The archive the
-- collector produces lists exactly what it read.
`, strings.Repeat("-", 72))
}
