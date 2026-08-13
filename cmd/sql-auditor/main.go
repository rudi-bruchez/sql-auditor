// Command sql-auditor collects SQL Server diagnostic facts into JSON and
// packages them for transport. It collects; it does not judge.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	sqlauditor "github.com/rudi-bruchez/sql-auditor"
	"github.com/rudi-bruchez/sql-auditor/collect"
)

// version is the source of truth between releases. The release workflow
// overrides both of these with -ldflags, stamping the tag and the commit it
// built from, and it refuses to publish a binary whose "version" output
// disagrees with the tag.
//
// Between releases the number still has to mean something: an archive records
// the tool that produced it, and "dev" told a reader nothing about which
// collectors were in the corpus. So the number lives here and moves with the
// corpus, while buildStamp fills in the revision from the build itself.
var version = "0.12.0"
var commit = ""

// buildStamp returns what to print after the version. When -ldflags supplied a
// commit, that wins. Otherwise Go embeds the VCS revision at build time for any
// build made inside a checkout, which covers the binaries handed round between
// releases — and it marks a dirty tree, because a binary built from
// uncommitted work is not the commit it names.
func buildStamp() string {
	if commit != "" {
		return commit
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "unknown"
	}
	if len(rev) > 8 {
		rev = rev[:8]
	}
	if dirty {
		return rev + ", modified"
	}
	return rev
}

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) < 2 {
		usage()
		return 2
	}
	cmd, args := os.Args[1], os.Args[2:]
	// "queries export" is the one command with a subcommand, and flag.Parse
	// stops at the first non-flag argument — so parsing os.Args[2:] whole
	// leaves --to unset and the export refuses a destination the user did
	// supply. Take the subcommand off the front before parsing.
	sub := ""
	if cmd == "queries" && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.Usage = usage
	var (
		server     = fs.String("server", "", "SQL Server instance (overrides SQL_SERVER)")
		user       = fs.String("user", "", "SQL login (overrides SQL_USER)")
		envFile    = fs.String("env", ".env", "path to the .env file")
		queriesDir = fs.String("queries-dir", "", "run queries from this directory instead of the embedded corpus")
		outputDir  = fs.String("output-dir", "", "where to write results")
		keep       = fs.Bool("keep", false, "keep an existing same-day run folder, suffixing this run")
		to         = fs.String("to", "", "destination directory for 'queries export'")
		// Writes a file, never a permission. The collector connects with the
		// login being measured, which by construction cannot grant anything —
		// so the output is a script for a DBA to read and run, and the tool
		// stays a reader of the instance in every mode.
		grantScript = fs.String("grant-script", "",
			"write the T-SQL that grants the missing permissions to this file (check only)")
		// Off by default, and it has to stay that way: this is the only option
		// that puts the verbatim SQL of live user batches into the archive,
		// along with the login, host and program names behind them. That text
		// can carry literals copied out of application tables. Turning it on
		// also changes what MANIFEST.txt discloses, so the archive says so.
		sessionText = fs.Bool("include-session-text", false,
			"also collect the SQL text of sessions running during collection, "+
				"with their login, host and program names — this may contain application data")
		// Off by default for cost, not for privacy. The estimate samples real
		// data into tempdb, and the objects worth asking about are the large
		// ones — which is precisely when it hurts.
		estimateCompression = fs.Bool("estimate-compression", false,
			"also estimate page-compression savings on the largest uncompressed objects — "+
				"this samples data into tempdb and is slow on large tables")
		// Off by default, and it has to stay that way: this is the option that
		// puts the full text of production queries and their execution plans
		// into the archive. A plan carries the compiled parameter values and
		// the literal predicates. Turning it on changes what MANIFEST.txt
		// discloses, so the archive says so.
		queryStoreDetail = fs.Bool("query-store-detail", false,
			"also collect the full text and execution plans of the heaviest Query Store "+
				"queries — this may contain application data")
		// A second option rather than a widening of the first: finding the
		// profiled plan reads the plan cache of the whole instance, where every
		// other per-database collector sees only the database it was pointed
		// at. It does nothing without --query-store-detail, which is what
		// produces the list of queries to look for.
		queryStorePlanStats = fs.Bool("query-store-plan-stats", false,
			"also look for the last profiled plan of each extracted query — this reads "+
				"the plan cache of the whole instance, and needs LAST_QUERY_PLAN_STATS "+
				"or trace flag 2451 to return anything")
		queryStoreDays = fs.Int("query-store-days", 0,
			"how many days of Query Store history to read, counting back from now "+
				"(default 7); cannot be combined with --query-store-from/--query-store-to")
		// The bounds exist for the question a sliding window cannot answer: the
		// client saw a slowdown for one hour, eighteen days ago. Widening to
		// eighteen days does not help — the hour disappears into the average.
		queryStoreFrom = fs.String("query-store-from", "",
			"start of the window, YYYY-MM-DDTHH:MM, in the SERVER's local time")
		queryStoreTo = fs.String("query-store-to", "",
			"end of the window, YYYY-MM-DDTHH:MM, in the SERVER's local time "+
				"(default: the moment of collection); given on its own it implies "+
				"a seven-day window ending at that bound")
		queryStoreTop = fs.Int("query-store-top", 0,
			"how many queries to extract per database, across all four rankings "+
				"once deduplicated (default 50); queries with a forced plan are added "+
				"on top of this")
		queryStoreDBs = fs.String("query-store-databases", "",
			"comma-separated wildcards narrowing which of the collected databases the "+
				"Query Store extraction reads")
	)
	_ = fs.Parse(args)

	if cmd == "version" {
		fmt.Printf("sql-auditor %s (%s)\n", version, buildStamp())
		return 0
	}
	if cmd == "queries" {
		if sub != "export" {
			fmt.Fprintln(os.Stderr, "the only subcommand is: sql-auditor queries export --to DIR")
			return 2
		}
		if *to == "" {
			fmt.Fprintln(os.Stderr, "queries export requires --to DIR")
			return 2
		}
		if err := collect.ExportQueries(sqlauditor.Queries, "queries", *to); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("queries written to %s\n", *to)
		return 0
	}
	if cmd != "collect" && cmd != "check" {
		// Say what was wrong before printing the help. A wall of usage text
		// with nothing pointing at the mistake leaves the reader to spot it,
		// and the mistake that actually happens is "--check" for "check":
		// every other argument here is a flag, so writing the command as one
		// is the natural error, and the help alone does not correct it.
		switch {
		case cmd == "":
			fmt.Fprintln(os.Stderr, "no command given.")
		case strings.HasPrefix(cmd, "-"):
			fmt.Fprintf(os.Stderr,
				"%q is not a command: check, collect, queries and version are written without dashes. Did you mean %q?\n\n",
				cmd, strings.TrimLeft(cmd, "-"))
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q.\n\n", cmd)
		}
		usage()
		return 2
	}

	// Which build produced an archive is the first question asked of one that
	// disagrees with another, so say it before anything can go wrong — ahead of
	// reading .env, so a refused configuration is still attributable to a
	// build. On stderr, so redirecting a check's listing to a file leaves this
	// on the terminal where the operator is looking.
	fmt.Fprintf(os.Stderr, "sql-auditor %s (%s)\n\n", version, buildStamp())

	dotenv := map[string]string{}
	if f, err := os.Open(*envFile); err == nil {
		defer f.Close()
		if parsed, perr := collect.ParseDotEnv(f); perr == nil {
			dotenv = parsed
		} else {
			fmt.Fprintf(os.Stderr, "%s: %v\n", *envFile, perr)
			return 2
		}
	}
	flags := map[string]string{}
	for k, v := range map[string]string{
		"SQL_SERVER": *server, "SQL_USER": *user,
		"QUERIES_DIR": *queriesDir, "OUTPUT_DIR": *outputDir,
	} {
		if v != "" {
			flags[k] = v
		}
	}
	// Which flags the operator actually typed, from the flag set itself rather
	// than from their values. The two integers default to 0 above, not to 7 and
	// 50 — Resolve owns the defaults, and a flag carrying its own would beat a
	// .env value the operator set — and asking the flag set preserves that
	// while giving a typed value the same answer whatever it is. Testing
	// "> 0" instead dropped --query-store-days -3 on the floor: the run took
	// the default of 7 and the manifest recorded 7 as though it had been
	// chosen, while the same -3 in a .env was refused.
	typed := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { typed[f.Name] = true })
	if typed["query-store-days"] {
		flags["QUERY_STORE_DAYS"] = strconv.Itoa(*queryStoreDays)
	}
	if *queryStoreFrom != "" {
		flags["QUERY_STORE_FROM"] = *queryStoreFrom
	}
	if *queryStoreTo != "" {
		flags["QUERY_STORE_TO"] = *queryStoreTo
	}
	if typed["query-store-top"] {
		flags["QUERY_STORE_TOP"] = strconv.Itoa(*queryStoreTop)
	}
	if *queryStoreDBs != "" {
		flags["QUERY_STORE_DB_INCLUDE"] = *queryStoreDBs
	}
	cfg, err := collect.Resolve(flags, dotenv, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if cfg.TrustCert && cfg.Encrypt {
		fmt.Fprintln(os.Stderr, "note: the connection is encrypted but the server certificate is NOT validated "+
			"(SQL_TRUST_SERVER_CERTIFICATE=true)")
	}

	opts := collect.Options{
		Config: cfg, Corpus: sqlauditor.Queries, Root: "queries",
		Now: time.Now(), Keep: *keep, Version: version, Commit: buildStamp(),
		GrantScript: *grantScript,
		Flags: map[string]bool{
			collect.FlagIncludeSessionText:  *sessionText,
			collect.FlagEstimateCompression: *estimateCompression,
			collect.FlagQueryStoreDetail:    *queryStoreDetail,
			collect.FlagQueryStorePlanStats: *queryStorePlanStats,
		},
	}
	if cfg.QueriesDir != "" {
		opts.Corpus = os.DirFS(cfg.QueriesDir)
		opts.Root = "."
	}

	ctx := context.Background()
	var code int
	switch cmd {
	case "collect":
		code, err = collect.Run(ctx, opts)
	case "check":
		code, err = collect.Check(ctx, opts)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	return code
}

func usage() {
	fmt.Fprint(os.Stderr, `sql-auditor — SQL Server diagnostic collector

  sql-auditor check                    verify connectivity, permissions and configuration,
                                       and list what a collection would run
  sql-auditor collect                  collect, then archive
  sql-auditor queries export --to DIR  write the embedded queries to disk
  sql-auditor version

Options (check, collect):
  --server HOST[,PORT]        overrides SQL_SERVER
  --user NAME                 overrides SQL_USER
  --env PATH                  .env file to read (default .env)
  --queries-dir DIR           run a corpus from disk instead of the embedded one
  --output-dir DIR            where to write results
  --keep                      keep an existing same-day run folder
  --grant-script FILE         check only. After probing permissions, write the
                              T-SQL that grants exactly the ones found missing,
                              for the login the server reports, with the reason
                              for each. The tool never runs it: the login being
                              measured cannot grant anything. Give the file to
                              a DBA.
  --include-session-text      also collect the SQL text of running sessions and
                              the login, host and program names behind them.
                              Off by default: that text can contain application
                              data. MANIFEST.txt discloses it when it is on.
  --estimate-compression      also estimate page-compression savings on the largest
                              uncompressed objects. Off by default for cost: it
                              samples data into tempdb and is slow on big tables.
  --query-store-detail        also collect the full text and the execution plans of
                              the heaviest Query Store queries. Off by default: a
                              plan carries the compiled parameter values and the
                              literal predicates. MANIFEST.txt discloses it when
                              it is on.
  --query-store-plan-stats    also look for the last profiled plan of each extracted
                              query. A separate decision from the one above: this
                              lookup reads the plan cache of the whole instance.
                              Needs LAST_QUERY_PLAN_STATS or trace flag 2451, and
                              does nothing without --query-store-detail.
  --query-store-days N        how many days of history to read, counting back from
                              now (default 7). Not combinable with the bounds below.
  --query-store-from T        start of the window, YYYY-MM-DDTHH:MM, in the SERVER's
                              local time. For the incident an average hides: one
                              hour, eighteen days ago.
  --query-store-to T          end of the window, same format and same clock
                              (default: the moment of collection). On its own it
                              implies a seven-day window ending at that bound.
  --query-store-top N         how many queries per database, across the four
                              rankings once deduplicated (default 50). Queries with
                              a forced plan are added on top of this.
  --query-store-databases P   comma-separated wildcards narrowing which of the
                              collected databases the extraction reads. It narrows
                              the selection; it never widens it.

Exit codes: 0 success, 2 partial failure or bad configuration, 1 fatal.
`)
}
