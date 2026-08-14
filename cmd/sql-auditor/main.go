// Command sql-auditor collects SQL Server diagnostic facts into JSON and
// packages them for transport. It collects; it does not judge.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	sqlauditor "github.com/rudi-bruchez/sql-auditor"
	"github.com/rudi-bruchez/sql-auditor/collect"
	"github.com/rudi-bruchez/sql-auditor/tui"
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
var version = "0.18.0"
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

// cliFlags is every option the command line accepts, held as values the flag
// set writes into directly.
//
// The set is created per command, because its name appears in the messages
// flag.ExitOnError prints, and it is parsed exactly once per invocation: run()
// parses it before the dispatch and hands the parsed struct to optionsFrom.
// The wizard needs the same resolution with no command line at all — it reads
// .env and the environment before the operator has typed anything — which is
// what buildOptions is for.
type cliFlags struct {
	fs *flag.FlagSet

	server, user, envFile, queriesDir, outputDir string
	to, grantScript                              string
	keep, force, all                             bool

	passwordFile  string
	passwordStdin bool

	sessionText, objectDefinitions              bool
	deadlockGraphs, blockedProcessReports       bool
	estimateCompression                         bool
	queryStoreDetail, queryStorePlanStats       bool
	queryStoreDays, queryStoreTop               int
	queryStoreFrom, queryStoreTo, queryStoreDBs string
}

func defineFlags(cmd string) *cliFlags {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.Usage = usage
	c := &cliFlags{fs: fs}
	fs.StringVar(&c.server, "server", "", "SQL Server instance (overrides SQL_SERVER)")
	fs.StringVar(&c.user, "user", "", "SQL login (overrides SQL_USER)")
	fs.StringVar(&c.envFile, "env", ".env", "path to the .env file")
	// Neither of these is --password, and the difference is the whole point:
	// a password given as an argument is in the process table for every other
	// user on the machine and in the shell history afterwards, and nothing at
	// the call site takes it back out. A path is not a secret, and a pipe is
	// read by this process alone.
	fs.StringVar(&c.passwordFile, "password-file", "",
		"read SQL_PASSWORD from this file instead of .env (one trailing line ending is ignored)")
	fs.BoolVar(&c.passwordStdin, "password-stdin", false,
		"read SQL_PASSWORD from standard input instead of .env")
	fs.StringVar(&c.queriesDir, "queries-dir", "", "run queries from this directory instead of the embedded corpus")
	fs.StringVar(&c.outputDir, "output-dir", "", "where to write results")
	fs.BoolVar(&c.keep, "keep", false, "keep an existing same-day run folder, suffixing this run")
	// A union of the seven opt-ins below, and deliberately not a mode: it
	// turns them on and changes nothing else. Six of them are disclosure
	// decisions and one is a cost decision, so what this asks for is the
	// widest archive the tool can produce — which is the right thing on an
	// instance you have a mandate for, and the wrong thing everywhere else.
	// MANIFEST.txt is unchanged by it: the archive keeps recording the seven
	// individually, because what was collected is the fact worth keeping and
	// how few words it took to ask is not.
	fs.BoolVar(&c.all, "all", false,
		"turn on every optional collector at once, including the ones off by default "+
			"for disclosure and the one off for cost")
	fs.StringVar(&c.to, "to", "",
		"destination: a directory for 'queries export', a file for 'env init' (default .env)")
	fs.BoolVar(&c.force, "force", false, "'env init' only: replace an existing file")
	// Writes a file, never a permission. The collector connects with the
	// login being measured, which by construction cannot grant anything —
	// so the output is a script for a DBA to read and run, and the tool
	// stays a reader of the instance in every mode.
	fs.StringVar(&c.grantScript, "grant-script", "",
		"write the T-SQL that grants the missing permissions to this file (check only)")
	// Off by default, and it has to stay that way: this is the only option
	// that puts the verbatim SQL of live user batches into the archive,
	// along with the login, host and program names behind them. That text
	// can carry literals copied out of application tables. Turning it on
	// also changes what MANIFEST.txt discloses, so the archive says so.
	fs.BoolVar(&c.sessionText, "include-session-text", false,
		"also collect the SQL text of sessions running during collection, "+
			"with their login, host and program names — this may contain application data")
	// Off by default, and for privacy rather than cost: this exports the
	// client's own code. A view or a procedure names linked servers, can
	// embed values as literals, and now and then holds a credential in
	// clear inside an OPENQUERY. Turning it on changes what MANIFEST.txt
	// discloses, so the archive says so.
	fs.BoolVar(&c.objectDefinitions, "include-object-definitions", false,
		"also collect the source of views, procedures, functions and triggers — "+
			"this is code written on this server and may contain credentials")
	// Off by default, and the narrowest of the disclosure options: a
	// deadlock graph carries the verbatim SQL of both victims.
	// 060.system-health.sql collects the count and the timestamps by
	// default and stops there, which is the line this option crosses.
	fs.BoolVar(&c.deadlockGraphs, "include-deadlock-graphs", false,
		"also collect the deadlock reports system_health still holds, one .xdl "+
			"file each — these carry the SQL of both victims")
	// Off by default. It is the only collector that reads the server's file
	// system — through sys.fn_xe_file_target_read_file, as the SQL Server
	// service account — and a report names the blocking session's SQL as
	// well as the blocked one's.
	fs.BoolVar(&c.blockedProcessReports, "include-blocked-process-reports", false,
		"also collect the blocked process reports captured by an Extended Events "+
			"session, one .xml file each — these name both sessions and carry their SQL")
	// Off by default for cost, not for privacy. The estimate samples real
	// data into tempdb, and the objects worth asking about are the large
	// ones — which is precisely when it hurts.
	fs.BoolVar(&c.estimateCompression, "estimate-compression", false,
		"also estimate page-compression savings on the largest uncompressed objects — "+
			"this samples data into tempdb and is slow on large tables")
	// Off by default, and it has to stay that way: this is the option that
	// puts the full text of production queries and their execution plans
	// into the archive. A plan carries the compiled parameter values and
	// the literal predicates. Turning it on changes what MANIFEST.txt
	// discloses, so the archive says so.
	fs.BoolVar(&c.queryStoreDetail, "query-store-detail", false,
		"also collect the full text and execution plans of the heaviest Query Store "+
			"queries — this may contain application data")
	// A second option rather than a widening of the first: finding the
	// profiled plan reads the plan cache of the whole instance, where every
	// other per-database collector sees only the database it was pointed
	// at. It does nothing without --query-store-detail, which is what
	// produces the list of queries to look for.
	fs.BoolVar(&c.queryStorePlanStats, "query-store-plan-stats", false,
		"also look for the last profiled plan of each extracted query — this reads "+
			"the plan cache of the whole instance, and needs LAST_QUERY_PLAN_STATS "+
			"or trace flag 2451 to return anything")
	fs.IntVar(&c.queryStoreDays, "query-store-days", 0,
		"how many days of Query Store history to read, counting back from now "+
			"(default 7); cannot be combined with --query-store-from/--query-store-to")
	// The bounds exist for the question a sliding window cannot answer: the
	// client saw a slowdown for one hour, eighteen days ago. Widening to
	// eighteen days does not help — the hour disappears into the average.
	fs.StringVar(&c.queryStoreFrom, "query-store-from", "",
		"start of the window, YYYY-MM-DDTHH:MM, in the SERVER's local time")
	fs.StringVar(&c.queryStoreTo, "query-store-to", "",
		"end of the window, YYYY-MM-DDTHH:MM, in the SERVER's local time "+
			"(default: the moment of collection); given on its own it implies "+
			"a seven-day window ending at that bound")
	fs.IntVar(&c.queryStoreTop, "query-store-top", 0,
		"how many queries to extract per database, across all four rankings "+
			"once deduplicated (default 50); queries with a forced plan are added "+
			"on top of this")
	fs.StringVar(&c.queryStoreDBs, "query-store-databases", "",
		"comma-separated wildcards narrowing which of the collected databases the "+
			"Query Store extraction reads")
	return c
}

// buildOptions turns a command line, a .env file and the process environment
// into the collect.Options that check, collect and the wizard all run on.
//
// It exists because the wizard needs a complete, resolved configuration and had
// no way to obtain one: every line of this resolution used to sit inside run(),
// after the command dispatch and below a dozen local variables. Nothing here is
// new behaviour — the flag set, the precedence and the refusals are the ones
// that were there before, moved.
//
// The returned int is the exit code to use when the error is non-nil; it is
// always 2, since everything that can fail here is a configuration the operator
// wrote and can correct. The error is returned rather than printed so the caller
// decides where it goes: a subcommand puts it on stderr, and the wizard cannot,
// because it is about to take the screen.
func buildOptions(cmd string, args []string, env func(string) string, stdin io.Reader) (collect.Options, int, error) {
	c := defineFlags(cmd)
	_ = c.fs.Parse(args)
	return optionsFrom(c, env, stdin)
}

// readPassword resolves --password-file and --password-stdin into the value
// SQL_PASSWORD would have carried. An empty string with a nil error means
// neither option was given, which is the ordinary case.
//
// Exactly one trailing line ending is removed and nothing else. Trimming
// whitespace generally would be the friendlier-looking choice and the wrong
// one: a password can end in a space, and one that works in .env — where the
// operator quotes it — must not become a different password here. The single
// \r\n or \n is the one thing an editor or a `>` redirection adds without
// being asked.
//
// An empty source is an error rather than an empty password. A CI step that
// wrote nothing to the file, or a pipeline whose left-hand side failed, would
// otherwise fall through to Windows integrated authentication and quietly
// measure a different login than the one the run is about.
func readPassword(c *cliFlags, stdin io.Reader) (string, error) {
	if c.passwordFile != "" && c.passwordStdin {
		return "", fmt.Errorf("--password-file and --password-stdin cannot both be given")
	}
	var raw []byte
	switch {
	case c.passwordFile != "":
		b, err := os.ReadFile(c.passwordFile)
		if err != nil {
			return "", fmt.Errorf("--password-file: %w", err)
		}
		raw = b
	case c.passwordStdin:
		b, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("--password-stdin: %w", err)
		}
		raw = b
	default:
		return "", nil
	}
	s := strings.TrimSuffix(string(raw), "\n")
	s = strings.TrimSuffix(s, "\r")
	if s == "" {
		source := "--password-file " + c.passwordFile
		if c.passwordStdin {
			source = "--password-stdin"
		}
		return "", fmt.Errorf("%s: empty; a password read from nothing is not a password", source)
	}
	return s, nil
}

// optionsFrom is the resolution itself, on a command line that has already been
// parsed. run() parses one before the dispatch — a malformed command line has
// always been refused before anything else is said, including before the
// "unknown command" message — and parsing it a second time here would only
// produce the same values.
func optionsFrom(c *cliFlags, env func(string) string, stdin io.Reader) (collect.Options, int, error) {
	// Before the .env is opened, because a password source the operator named
	// and this program cannot read is their mistake to correct, and saying so
	// first keeps the two refusals from arriving in an order that depends on
	// which file happens to be missing.
	password, err := readPassword(c, stdin)
	if err != nil {
		return collect.Options{}, 2, err
	}
	dotenv := map[string]string{}
	if f, err := os.Open(c.envFile); err == nil {
		defer f.Close()
		if parsed, perr := collect.ParseDotEnv(f); perr == nil {
			dotenv = parsed
		} else {
			return collect.Options{}, 2, fmt.Errorf("%s: %w", c.envFile, perr)
		}
	}
	flags := map[string]string{}
	for k, v := range map[string]string{
		"SQL_SERVER": c.server, "SQL_USER": c.user,
		"QUERIES_DIR": c.queriesDir, "OUTPUT_DIR": c.outputDir,
		// A password from --password-file or --password-stdin enters here
		// and nowhere else, so it obeys the same precedence as every other
		// flag — over .env, over the environment — with no rule of its own.
		"SQL_PASSWORD": password,
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
	c.fs.Visit(func(f *flag.Flag) { typed[f.Name] = true })
	if typed["query-store-days"] {
		flags["QUERY_STORE_DAYS"] = strconv.Itoa(c.queryStoreDays)
	}
	if c.queryStoreFrom != "" {
		flags["QUERY_STORE_FROM"] = c.queryStoreFrom
	}
	if c.queryStoreTo != "" {
		flags["QUERY_STORE_TO"] = c.queryStoreTo
	}
	if typed["query-store-top"] {
		flags["QUERY_STORE_TOP"] = strconv.Itoa(c.queryStoreTop)
	}
	if c.queryStoreDBs != "" {
		flags["QUERY_STORE_DB_INCLUDE"] = c.queryStoreDBs
	}
	cfg, err := collect.Resolve(flags, dotenv, env)
	if err != nil {
		return collect.Options{}, 2, err
	}
	// A DBA watching sys.dm_exec_sessions while this runs sees program_name and
	// nothing else, and "sql-auditor" on its own does not say which corpus is
	// on the wire — which is the question asked when two runs disagree. So the
	// default carries the version.
	//
	// A name the operator chose is left exactly as they wrote it: it was picked
	// to be matched by an Extended Events filter or a monitoring rule, and
	// appending to it would break the match. Stamping it here rather than in
	// Resolve keeps the version in the one file the release workflow rewrites.
	if cfg.AppName == collect.DefaultAppName {
		cfg.AppName = collect.DefaultAppName + " " + version
	}

	opts := collect.Options{
		Config: cfg, Corpus: sqlauditor.Queries, Root: "queries",
		Now: time.Now(), Keep: c.keep, Version: version, Commit: buildStamp(),
		GrantScript: c.grantScript,
		Flags: map[string]bool{
			collect.FlagIncludeSessionText:    c.all || c.sessionText,
			collect.FlagEstimateCompression:   c.all || c.estimateCompression,
			collect.FlagQueryStoreDetail:      c.all || c.queryStoreDetail,
			collect.FlagQueryStorePlanStats:   c.all || c.queryStorePlanStats,
			collect.FlagObjectDefinitions:     c.all || c.objectDefinitions,
			collect.FlagDeadlockGraphs:        c.all || c.deadlockGraphs,
			collect.FlagBlockedProcessReports: c.all || c.blockedProcessReports,
		},
	}
	if cfg.QueriesDir != "" {
		opts.Corpus = os.DirFS(cfg.QueriesDir)
		opts.Root = "."
	}
	return opts, 0, nil
}

// Mode is what an invocation of this program turns out to be.
type Mode int

const (
	// ModeSubcommand is every invocation that carries an argument. It is first
	// because it must be: a command line says what it wants, and a wizard is
	// never a better answer than the thing that was asked for.
	ModeSubcommand Mode = iota
	// ModeTUI is the wizard, and it is reached only when nothing was asked for
	// and both ends of the terminal are real.
	ModeTUI
	// ModeUsage is the help on stderr and exit 2 — the behaviour this program
	// had for every argument-less invocation before the wizard existed.
	ModeUsage
)

// mode decides between the three, in the order the spec fixes.
//
// The TTY predicate is injected because no test in this repository can allocate
// a pseudo-terminal portably, and a switch whose only interesting outcome is
// untestable is a switch that will be got wrong. With it as a parameter, all
// four combinations are a table.
//
// The order matters more than any single rule. An argument wins over
// everything, so a scripted `sql-auditor collect` behaves the same whatever the
// terminal is. Then SQL_AUDITOR_NO_TUI, an escape hatch that must work even on
// a perfectly good terminal. Then the terminal itself: `sql-auditor > run.log`
// that quietly started collecting on a production instance would be the worst
// possible outcome of this whole batch.
//
// SQL_AUDITOR_NO_TUI is read from the environment and is deliberately NOT a
// .env key. That set is closed — an unrecognised key there is a hard failure,
// because a typo that silently changes behaviour is worse than a refusal — and
// this is not a connection setting.
func mode(isTTY func(*os.File) bool, stdin, stdout *os.File, env func(string) string, args []string) Mode {
	if len(args) > 0 {
		return ModeSubcommand
	}
	if env("SQL_AUDITOR_NO_TUI") != "" {
		return ModeUsage
	}
	if !isTTY(stdin) || !isTTY(stdout) {
		return ModeUsage
	}
	return ModeTUI
}

// isTerminal is the production predicate. Both ends are asked about
// separately: a pipe on either one is enough to make a full-screen wizard the
// wrong answer.
func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

func run() int {
	switch mode(isTerminal, os.Stdin, os.Stdout, os.Getenv, os.Args[1:]) {
	case ModeUsage:
		usage()
		return 2
	case ModeTUI:
		// The wizard resolves its configuration exactly as `collect` would,
		// from .env and the environment, and then lets the operator correct the
		// server and supply a password on screen 1. A refusal here is a .env
		// the wizard cannot show, so it is reported the way a subcommand would
		// report it — before the terminal is taken.
		o, code, err := buildOptions("collect", nil, os.Getenv, os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return code
		}
		return tui.Run(o, os.Stdin, os.Stdout)
	}

	cmd, args := os.Args[1], os.Args[2:]
	// "queries export" is the one command with a subcommand, and flag.Parse
	// stops at the first non-flag argument — so parsing os.Args[2:] whole
	// leaves --to unset and the export refuses a destination the user did
	// supply. Take the subcommand off the front before parsing.
	sub := ""
	if (cmd == "queries" || cmd == "env") && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	// Parsed before the dispatch, and deliberately: a malformed command line has
	// always been refused before anything else is said, including before the
	// "unknown command" message, and moving that refusal after the dispatch
	// would change which of the two an operator sees.
	c := defineFlags(cmd)
	_ = c.fs.Parse(args)

	if cmd == "version" {
		fmt.Printf("sql-auditor %s (%s)\n", version, buildStamp())
		return 0
	}
	if cmd == "queries" {
		if sub != "export" {
			fmt.Fprintln(os.Stderr, "the only subcommand is: sql-auditor queries export --to DIR")
			return 2
		}
		if c.to == "" {
			fmt.Fprintln(os.Stderr, "queries export requires --to DIR")
			return 2
		}
		if err := collect.ExportQueries(sqlauditor.Queries, "queries", c.to); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("queries written to %s\n", c.to)
		return 0
	}
	if cmd == "env" {
		if sub != "init" {
			fmt.Fprintln(os.Stderr, "the only subcommand is: sql-auditor env init [--to FILE] [--force]")
			return 2
		}
		// Defaulted here rather than on the flag, so that the help can say
		// ".env" without --to appearing to have been given when it was not.
		dest := c.to
		if dest == "" {
			dest = ".env"
		}
		if err := collect.WriteEnvTemplate(sqlauditor.EnvExample, dest, c.force); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		fmt.Printf("%s written — fill in SQL_SERVER, then run: sql-auditor check\n", dest)
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

	opts, code, err := optionsFrom(c, os.Getenv, os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return code
	}
	if opts.Config.TrustCert && opts.Config.Encrypt {
		fmt.Fprintln(os.Stderr, "note: the connection is encrypted but the server certificate is NOT validated "+
			"(SQL_TRUST_SERVER_CERTIFICATE=true)")
	}

	ctx := context.Background()
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
  sql-auditor env init                 write the annotated .env template, so the
                                       settings this tool accepts can be read on
                                       a machine that has only the executable.
                                       --to FILE to write elsewhere (default
                                       .env), --force to replace an existing one
  sql-auditor queries export --to DIR  write the embedded queries to disk
  sql-auditor version

Options (check, collect):
  --server HOST[,PORT]        overrides SQL_SERVER
  --user NAME                 overrides SQL_USER
  --env PATH                  .env file to read (default .env)
  --password-file FILE        read SQL_PASSWORD from this file rather than from
                              .env. Exactly one trailing line ending is ignored;
                              everything else, including a trailing space, is
                              part of the password. An empty file is refused.
  --password-stdin            read SQL_PASSWORD from standard input, same rules.
                              For a secret store that prints to a pipe. There is
                              no --password: an argument is in the process table
                              and in the shell history, and a path is not.
  --queries-dir DIR           run a corpus from disk instead of the embedded one
  --output-dir DIR            where to write results
  --keep                      keep an existing same-day run folder
  --all                       turn on all seven options below at once: the six
                              that are off for disclosure and the one that is
                              off for cost. The widest archive this tool can
                              produce. It changes nothing else, and MANIFEST.txt
                              still records the seven individually.
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
  --include-object-definitions
                              also collect the source of views, procedures,
                              functions and triggers, one file each. Off by
                              default: this is code written on this server and
                              can hold credentials inside an OPENQUERY.
                              MANIFEST.txt discloses it when it is on.
  --include-deadlock-graphs   also collect the deadlock reports system_health
                              still holds, one .xdl file each. Off by default:
                              a report carries the SQL of both victims.
                              MANIFEST.txt discloses it when it is on.
  --include-blocked-process-reports
                              also collect the blocked process reports an
                              Extended Events session captured, one .xml each.
                              Off by default: a report carries the SQL of the
                              blocked session and of the one blocking it, and
                              reading it touches the server's file system.
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
