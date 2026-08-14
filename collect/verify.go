package collect

import (
	"context"
	"database/sql"
)

// VerifyResult is everything a caller needs to describe what a collection
// would do, without doing it. It exists because two callers now need those
// facts and only one of them prints: `check` writes a listing to stdout, and
// the wizard paints them into a screen it can repaint, scroll and re-probe.
// Keeping the gathering in Check would have forced the second caller to parse
// the first one's text.
//
// The contract is that a VerifyResult is ALWAYS usable, including when Verify
// returns an error. Every field a step could not reach keeps its zero value,
// and Probed is what tells the two apart: a caller that reads Checks without
// consulting Probed would report "no permissions denied" about an instance it
// never spoke to. The screens say "not checked" there; the listing prints
// nothing at all.
type VerifyResult struct {
	Server    ServerInfo
	ServerErr error
	// Probed is false when the server probe did not succeed — including when
	// the connection was never established. Everything derived from the
	// server's version therefore stays at zero: see Collectors.
	Probed bool

	Checks    []CapabilityCheck
	Selection Selection
	Folders   []DatabaseFolder
	// NoAccess is the databases the login may not read. Selection has no such
	// field: it carries Skipped, of which only the SkipNoAccess entries are a
	// permission problem — the others are the operator's own DB_EXCLUDE, which
	// no grant script should try to fix.
	NoAccess []string
	Blocking BlockingReadiness

	// Scripts is the corpus as discovered, in listing order. BuildGrantScript
	// needs it to know which permissions the collectors actually ask for, so
	// the wizard's [g] key cannot be served without it.
	Scripts []Script
	// Collectors is how many scripts would run at least once. It is zero when
	// Probed is false, and that is not the same as "nothing would run": the
	// plan depends on the server version and on what the preflight refused,
	// neither of which is known without a successful probe. A screen must
	// print "not checked" rather than "0 collectors".
	Collectors int
	// Skipped is the manifest's skip list as the run would write it: global
	// skips and per-database narrowings interleaved in plan order. Also empty
	// when Probed is false.
	Skipped []SkippedScript

	LintFailures   int
	OutputWritable bool

	// The failures, kept apart rather than folded into one error, because the
	// caller prices them differently: a corpus that cannot be read is exit 2
	// and prints nothing, an unusable address is exit 2 or 1 depending on the
	// address, an instance that does not answer is exit 1, and a database list
	// that could not be built is a warning the listing survives.
	CorpusErr     error
	OpenErr       error
	ConnErr       error
	CandidatesErr error
	SelectErr     error
}

// VerifyLocal gathers everything that can be known without a socket: the
// corpus, its lint verdicts, and whether the output directory will take an
// archive.
//
// It is separate from VerifyServer so that a caller can SAY something before
// spending minutes on the network. `check` prints the corpus listing and the
// Output line from this result alone, which is what it did before the two were
// ever gathered together: on a struggling instance every probe runs to
// SQL_QUERY_TIMEOUT_SEC, and an operator staring at an empty terminal cannot
// tell a working tool from a hung one. It also means `check > report.txt`
// killed halfway leaves the findings already established rather than an empty
// file.
//
// Discover comes first and outputWritable second, in that order and not the
// reverse: outputWritable creates the directory it tests, and a corpus that
// cannot be read is an early return. Probing the corpus first keeps
// `check --queries-dir C:\typo` a command that touches nothing on the
// operator's disk before refusing.
func VerifyLocal(o Options) (VerifyResult, error) {
	var v VerifyResult

	scripts, err := Discover(o.Corpus, o.Root)
	if err != nil {
		v.CorpusErr = corpusError(o.Config, err)
		return v, v.CorpusErr
	}
	v.Scripts = scripts
	for _, s := range scripts {
		if s.LintError != "" {
			v.LintFailures++
		}
	}

	// OutputWritable false is a finding the caller must be able to trust even
	// on a run that fails for an unrelated reason later, so it is gathered
	// unconditionally once the corpus is known to exist.
	v.OutputWritable = outputWritable(o.Config.OutputDir)
	return v, nil
}

// VerifyServer fills in everything that needs the instance — the preflight,
// the server probe, the blocking readiness, the database selection and the
// resolved plan — into a result VerifyLocal has already populated.
//
// The returned error is the first FATAL failure — the address, the
// connection — and is nil for the two degraded outcomes, which are reported on
// the result instead: a database list that could not be read still leaves a
// usable permissions report, and refusing to return the whole thing over it
// would hide the answer to the question the operator asked.
//
// One connection for everything, as in Run and in the version of Check this
// was extracted from: the pool allows exactly one, so anything taking the
// *sql.DB while this Conn is held would wait for a connection only this
// function can give back.
func VerifyServer(ctx context.Context, o Options, v *VerifyResult) error {
	db, err := Open(o.Config)
	if err != nil {
		v.OpenErr = err
		return err
	}
	defer db.Close()

	conn, err := db.Conn(ctx)
	if err != nil {
		v.ConnErr = err
		return err
	}
	defer conn.Close()

	v.Checks = runPreflightWithDeadline(ctx, conn, o.Config)

	si, perr := probeWithDeadline(ctx, conn, o.Config)
	v.Server, v.ServerErr, v.Probed = si, perr, perr == nil

	v.Blocking = blockingWithDeadline(ctx, conn, o.Config)

	// The database list is the blast radius, and it is gathered even when it
	// comes back empty: an empty list is itself the finding when VIEW ANY
	// DEFINITION is missing.
	cands, cerr := candidatesWithDeadline(ctx, conn, o.Config)
	switch {
	case cerr != nil:
		v.CandidatesErr = cerr
	default:
		sel, serr := SelectTargets(cands, o.Config.DBInclude, o.Config.DBExclude)
		if serr != nil {
			v.SelectErr = serr
			break
		}
		v.Selection = sel
		v.Folders = ResolveDatabaseFolders(sel.Included)
		for _, s := range sel.Skipped {
			if s.Reason == SkipNoAccess {
				v.NoAccess = append(v.NoAccess, s.Name)
			}
		}
	}

	// The plan is the one part that cannot be built from a failed probe:
	// planScripts gates scripts on the server's version, so without one every
	// version-gated collector would be reported as running. Leaving Collectors
	// at zero and letting the caller read Probed is the honest shape.
	if v.Probed {
		denied := DeniedCapabilities(v.Checks)
		// connect is not a per-script gate. Getting here means it answered.
		delete(denied, "connect")
		plan := planScripts(v.Scripts, denied, ParseVersion(si.Version), o.Flags)
		v.Collectors = countCollectors(plan)
		_, v.Skipped, _ = planUnits(plan, v.Folders, o.Config)
	}
	return nil
}

// countCollectors is how many scripts would run at least once, which is not
// len(plan) and not a number of units either. A gated script runs zero times;
// a script that fails lint is an error rather than a collector and Run records
// it as one; a per-database script pointed at twelve databases is still one
// collector. The wizard's "Always collected: N collectors" says how much of
// the corpus applies to this instance, and multiplying by the databases would
// answer a question nobody asked.
func countCollectors(plan []plannedScript) int {
	n := 0
	for _, p := range plan {
		if p.Skip == "" && p.Script.LintError == "" {
			n++
		}
	}
	return n
}

// blockingWithDeadline is the fourth of the bounded read-only calls this
// package makes outside runUnit, and it was the one missing a bound. It sits
// beside the other three for the same reason they sit together: both Check and
// Verify make these calls, and a deadline present in one copy and absent from
// the other is invisible until an instance hangs.
//
// ProbeBlocking has its own fifteen-second ceiling, deliberately short because
// this is the least important thing check does. That is not a substitute for
// this one: it is a fixed number that ignores SQL_QUERY_TIMEOUT_SEC, so an
// operator who lowered the timeout to five seconds because the instance is
// struggling would still wait fifteen here. The shorter of the two wins, which
// is the intent in both directions.
func blockingWithDeadline(ctx context.Context, c *sql.Conn, cfg *Config) BlockingReadiness {
	dctx, cancel := deadline(ctx, cfg)
	defer cancel()
	return ProbeBlocking(dctx, c)
}
