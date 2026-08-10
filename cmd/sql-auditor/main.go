// Command sql-auditor collects SQL Server diagnostic facts into JSON and
// packages them for transport. It collects; it does not judge.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
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
		Flags: map[string]bool{
			collect.FlagIncludeSessionText:  *sessionText,
			collect.FlagEstimateCompression: *estimateCompression,
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
  --include-session-text      also collect the SQL text of running sessions and
                              the login, host and program names behind them.
                              Off by default: that text can contain application
                              data. MANIFEST.txt discloses it when it is on.
  --estimate-compression      also estimate page-compression savings on the largest
                              uncompressed objects. Off by default for cost: it
                              samples data into tempdb and is slow on big tables.

Exit codes: 0 success, 2 partial failure or bad configuration, 1 fatal.
`)
}
