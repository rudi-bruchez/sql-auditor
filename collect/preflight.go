package collect

import (
	"context"
	"database/sql"
	"errors"

	mssql "github.com/microsoft/go-mssqldb"
)

// CapabilityCheck is the outcome of one capability probe. Status is one of:
//
//	"ok"     the read succeeded; whatever needs this capability will run
//	"denied" the server answered with an error; the capability is unavailable
//	"error"  no answer came back at all — the instance is unreachable or the
//	         connection died mid-preflight, which is not the same as a refusal
//
// Impact is empty when Status is "ok" and otherwise carries the capability's
// stated consequence, so a caller printing the checks never has to look the
// consequence up.
//
// Label is the capability written for someone who has never read this code.
// MANIFEST.txt is read by a security officer, and "view_any_definition" tells
// that reader nothing; Name stays in _run.json, where the identifier is what
// an analysis layer matches on.
type CapabilityCheck struct {
	Name   string `json:"name"`
	Label  string `json:"label,omitempty"`
	Status string `json:"status"`
	Impact string `json:"impact,omitempty"`
}

// Capability is one thing the collector needs to be able to do, plus the
// query that establishes whether it can and the consequence if it cannot.
//
// Name is the same vocabulary NormalisePermission produces for the
// @permissions directive, so a denied capability can be matched against the
// scripts that declared it. The two must not drift. Label is the same thing
// said in English, for the manifest.
type Capability struct {
	Name, Label, SQL, Impact string
	// NeedsRows marks a probe whose denial is silent. SQL Server's metadata
	// visibility rules filter catalog views row by row rather than raising:
	// a login without VIEW ANY DEFINITION does not get an error from
	// sys.master_files, it gets zero rows. For such a probe the query must be
	// one whose object is never legitimately empty, and an empty result is
	// the denial. Probes that raise leave this false, because there zero rows
	// is a real answer.
	NeedsRows bool
}

// Capabilities returns the preflight probes. Permissions are tested by
// attempting a cheap representative read rather than deduced from
// HAS_PERMS_BY_NAME: a permission can arrive through role membership, be
// denied at a lower scope, or be shadowed by a database setting, and every
// such case produces a preflight that disagrees with what the run actually
// does. A SELECT cannot disagree with reality.
//
// Each probe reads one row from the cheapest object that the real collectors
// depend on, so the probe fails in exactly the cases they would.
//
// Two shapes of denial exist and both are checked. VIEW SERVER STATE and a
// closed msdb raise an error. VIEW ANY DEFINITION does not: metadata
// visibility silently drops the rows, so its probe reads sys.master_files —
// which carries a row per file of every system database on any instance, and
// so is empty only when the login cannot see it. sys.configurations, the
// obvious candidate, is readable in full without VIEW ANY DEFINITION and was
// measured returning all 97 rows to a login holding an explicit DENY.
func Capabilities() []Capability {
	return []Capability{
		{Name: "connect", Label: "Connect to the instance",
			SQL:    "SELECT 1",
			Impact: "nothing can run"},
		{Name: "view_any_definition", Label: "Read server and database metadata (VIEW ANY DEFINITION)",
			SQL:       "SELECT TOP 1 database_id FROM sys.master_files",
			Impact:    "instance configuration and database file layout not collected",
			NeedsRows: true},
		{Name: "view_server_state", Label: "Read performance counters (VIEW SERVER STATE)",
			SQL:    "SELECT TOP 1 wait_type FROM sys.dm_os_wait_stats",
			Impact: "wait statistics, schedulers, memory and tempdb usage not collected"},
		{Name: "msdb_read", Label: "Read backup history from msdb",
			SQL:    "SELECT TOP 1 database_name FROM msdb.dbo.backupset",
			Impact: "backup history not collected — the report must not read this as 'no backups exist'"},
	}
}

// RunPreflight probes each capability in order and reports what the login can
// actually do.
//
// It takes a *sql.Conn, not a *sql.DB, for the reason Probe does: the pool
// allows exactly one connection, so a caller holding a Conn that also handed
// the DB to this function would deadlock until its context expired.
//
// It never returns an error. A failed probe is the answer, not a failure of
// the preflight — the whole point is to report gaps rather than abort on them.
func RunPreflight(ctx context.Context, c *sql.Conn, caps []Capability) []CapabilityCheck {
	out := make([]CapabilityCheck, 0, len(caps))
	unreachable := false
	for _, p := range caps {
		// Once the instance has proven unreachable, the remaining probes can
		// only repeat that verdict, each after a full connect timeout. Report
		// them without asking.
		if unreachable {
			out = append(out, CapabilityCheck{Name: p.Name, Label: p.Label, Status: "error", Impact: p.Impact})
			continue
		}
		check := CapabilityCheck{Name: p.Name, Label: p.Label, Status: "ok"}
		rows, err := probeCapability(ctx, c, p.SQL)
		if err == nil && p.NeedsRows && rows == 0 {
			// The server answered, and the answer was "you may see nothing".
			// That is a refusal wearing the shape of an empty table.
			check.Status, check.Impact = "denied", p.Impact
		} else if err != nil {
			check.Impact = p.Impact
			// A refusal comes back as a SQL Server error: the server was
			// reached, understood the query and declined it. Anything else —
			// a dropped socket, a cancelled context, a login failure — means
			// no answer arrived, and reporting that as "denied" would send a
			// DBA hunting for a GRANT that was never the problem.
			var serverErr mssql.Error
			if errors.As(err, &serverErr) && p.Name != "connect" {
				check.Status = "denied"
			} else {
				check.Status = "error"
				unreachable = true
			}
		}
		out = append(out, check)
	}
	return out
}

// probeCapability runs one probe to completion and reports how many rows came
// back. The rows are drained rather than merely opened: some errors are only
// delivered while reading the result set, and a probe that stopped at
// QueryContext would call those a success.
func probeCapability(ctx context.Context, c *sql.Conn, query string) (int, error) {
	rows, err := c.QueryContext(ctx, query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	return n, rows.Err()
}

// DeniedCapabilities is the set of capability names that did not come back
// "ok", for matching against the @permissions a script declares. A capability
// that errored is as unavailable as one that was denied, so both belong here.
//
// "connect" appearing in this set is not an instruction to skip the scripts
// that declare CONNECT. It means the instance is unreachable and nothing runs
// at all — PreflightExitCode returns 1 and the run is abandoned before any
// script is considered. A caller that treated it as an ordinary skip would
// quietly emit an output file describing a server it never reached.
func DeniedCapabilities(checks []CapabilityCheck) map[string]bool {
	out := map[string]bool{}
	for _, c := range checks {
		if c.Status != "ok" {
			out[c.Name] = true
		}
	}
	return out
}

// PreflightExitCode maps preflight state to a process exit code:
//
//	0  usable, possibly degraded
//	1  the instance is unreachable — nothing can be collected
//	2  the configuration is unusable: the query corpus fails lint, or the
//	   output path cannot be written
//
// A denied permission returns 0. It degrades the run, and a degraded run is a
// warning: if a missing VIEW ANY DEFINITION exited non-zero, a DBA would
// conclude the tool is broken and stop, which costs more than the missing data.
//
// An unreachable instance outranks a bad configuration, because fixing the
// configuration would not make the run possible.
//
// ANY check with status "error" returns 1, not just "connect". A server can go
// away after the first probe answers: connect stays "ok", every later probe
// fails, and keying on connect alone would exit 0 — "usable, possibly
// degraded" — for an instance that is no longer there. This cannot swallow a
// legitimate denial, because RunPreflight assigns "error" only to a failure
// that is not an mssql.Error, meaning a transport failure and never a refusal.
func PreflightExitCode(checks []CapabilityCheck, lintFailures int, outputWritable bool) int {
	for _, c := range checks {
		if c.Status == "error" {
			return 1
		}
	}
	if lintFailures > 0 || !outputWritable {
		return 2
	}
	return 0
}
