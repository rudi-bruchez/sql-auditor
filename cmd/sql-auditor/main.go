// Command sql-auditor collects SQL Server diagnostic facts into JSON and
// packages them for transport. It collects; it does not judge.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	sqlauditor "github.com/rudi-bruchez/sql-auditor"
	"github.com/rudi-bruchez/sql-auditor/collect"
)

var version = "dev"
var commit = "unknown"

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
				"(default: the moment of collection)")
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
		fmt.Printf("sql-auditor %s (%s)\n", version, commit)
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
		usage()
		return 2
	}

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
	// The two integers default to 0 above, not to 7 and 50: Resolve owns the
	// defaults, and a flag carrying its own would beat a .env value the operator
	// set. They reach the map only when the operator actually typed one, exactly
	// as --server does.
	if *queryStoreDays > 0 {
		flags["QUERY_STORE_DAYS"] = strconv.Itoa(*queryStoreDays)
	}
	if *queryStoreFrom != "" {
		flags["QUERY_STORE_FROM"] = *queryStoreFrom
	}
	if *queryStoreTo != "" {
		flags["QUERY_STORE_TO"] = *queryStoreTo
	}
	if *queryStoreTop > 0 {
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
		Now: time.Now(), Keep: *keep, Version: version, Commit: commit,
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
                              (default: the moment of collection).
  --query-store-top N         how many queries per database, across the four
                              rankings once deduplicated (default 50). Queries with
                              a forced plan are added on top of this.
  --query-store-databases P   comma-separated wildcards narrowing which of the
                              collected databases the extraction reads. It narrows
                              the selection; it never widens it.

Exit codes: 0 success, 2 partial failure or bad configuration, 1 fatal.
`)
}
