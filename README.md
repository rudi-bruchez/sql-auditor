# sql-auditor

A single-binary collector that reads diagnostic facts out of a SQL Server
instance, writes them to JSON, and packs the run into a zip for transport.

**It collects; it does not judge.** There are no thresholds, no scores and no
recommendations anywhere in this repository. Deciding what the numbers mean is
somebody else's job, done later, somewhere else. The collector's only
responsibility is to gather the facts accurately, say what it could not gather,
and leave a record of both.

What it does to your instance:

- it issues read-only `SELECT` statements against system catalog views and
  dynamic management views;
- it does **not** read user or application tables;
- it does **not** write to your databases;
- it does **not** change any configuration.

## In a hurry

```
git clone https://github.com/rudi-bruchez/sql-auditor
cd sql-auditor
go build ./cmd/sql-auditor
./sql-auditor            # no argument on a terminal: the wizard takes it from here
```

Or, staying on the command line:

```
sql-auditor env init     # write .env, then fill in SQL_SERVER
sql-auditor check        # what would be collected, and what permission is missing
sql-auditor collect      # collect, then archive
```

Before you point it at production, read [docs/dba-guide.md](docs/dba-guide.md).

## Install

### Download a release

[Releases](https://github.com/rudi-bruchez/sql-auditor/releases) carry an
archive per platform — `sql-auditor_<version>_linux_amd64.tar.gz` and
`sql-auditor_<version>_windows_amd64.zip` — each holding the binary, the licence
and this file. Alongside them sits `sql-auditor_<version>_checksums.txt`.

Check what you downloaded before you run it against an instance:

```
sha256sum -c --ignore-missing sql-auditor_<version>_checksums.txt
```

Every archive also carries a build provenance attestation, which says more than
a digest does: a checksum proves the file did not change in transit, the
attestation names the commit and the workflow run that built it.

```
gh attestation verify sql-auditor_<version>_linux_amd64.tar.gz \
   --repo rudi-bruchez/sql-auditor
```

### Build it yourself

The source is published, so the route that gives you the most assurance is to
build it yourself, having read it:

```
git clone https://github.com/rudi-bruchez/sql-auditor
cd sql-auditor
go build ./cmd/sql-auditor
```

### `go install`

```
go install github.com/rudi-bruchez/sql-auditor/cmd/sql-auditor@latest
```

`@latest` resolves to the highest published tag. Name one — `@v0.21.0` — to get
the version this file describes rather than whatever has been tagged since. A
build made this way carries no attestation: the module proxy hands you source,
and the binary is compiled on your machine.

### About a binary somebody handed you

A release archive can be checked two ways, both above: its digest against the
published checksum file, and its attestation against this repository. A binary
that arrived some other way — an email, a share, a USB key — can be checked
against neither. What it can still be made to do is say what it will ask:

```
sql-auditor queries export --to ./from-binary
```

Those files are the whole of what it collects, and they are plain SQL.

What no amount of tooling will give you is a byte-identical rebuild to compare
against. What can and cannot be verified is set out in full in
[docs/dba-guide.md](docs/dba-guide.md#can-i-verify-the-binary).

## Running it

### The wizard

Run `sql-auditor` with no argument on a terminal and it opens a four-step
wizard. That is the default because the three things a first run gets wrong are
all invisible from a command line.

**Step 1: connection.** Shows the address, the initial database, the
authentication mode and whether the server certificate is actually validated,
resolved from `.env` and the environment exactly as `collect` would resolve
them. It lets you correct the server and type a password.

**Step 2: verification.** Probes the instance and lists every capability with
the full consequence of each refusal, never truncated, because that sentence is
the only place the cost of a missing `GRANT` is ever spelled out.

- `[g]` writes the T-SQL granting precisely what was found missing;
- the screen refuses to continue at all when the coverage of a run cannot be
  established.

**Step 3: what to collect.** Lists the opt-ins with what each one discloses
beside it, and the number of collectors resolved *for this instance* rather than
the size of the corpus, since the version gates close some of them on every
server.

**Step 4: the collection.** Shows the collector, the database, the elapsed time
and the bytes written so far. It ends on the path of the archive alone on its
line, indented, so that it can be selected with one drag and pasted into a mail.

### Keys

The keys are written at the bottom of every screen.

| Key | What it does |
| --- | --- |
| `[q]` | quit (on the later screens only) |
| `[b]` | go back |
| `[r]` | run the probe again |
| `ctrl-c` | cancel whatever is being waited on |

On step 1 the quit key is `ctrl-c`, not `[q]`, and it is the one screen where
they differ: every printable character there belongs to the field being edited,
so an instance named `QUALIF` and a password with a `q` in it have to stay
typeable.

Cancelling a collection with `ctrl-c` still writes its manifest and its archive,
and the final screen says that what you are holding is partial.

### What the wizard will not do

- **It never advises on the instance.** "`VIEW SERVER STATE` is missing, here is
  the T-SQL that grants it" is a statement about what can be collected; "your
  Query Store will fill up in twelve days" is an opinion about your server, and
  no layer of this program is allowed to hold one. The sentence at the top of
  this file is not softened by the wizard existing: it collects, it does not
  judge, and deciding what the numbers mean stays somebody else's job.
- **It never edits your configuration.** `.env` remains the place settings live,
  and the screens display its effect without ever rewriting it.
- **It changes nothing on the server.** `[g]` writes a file, and a DBA reads it
  and runs it themselves.

## Commands

```
sql-auditor                          open the wizard (a terminal on both ends, no argument)
sql-auditor check                    verify connectivity, permissions and configuration,
                                     and list what a collection would run
sql-auditor collect                  collect, then archive
sql-auditor env init                 write the annotated .env template
sql-auditor queries export --to DIR  write the embedded queries to disk; refuses to replace files already there, --force to replace
sql-auditor version                  print the version and the build it came from
```

### `version`

Also spelt `--version`, `-version` and `-V`. Lowercase `-v` is **not** the
version: it is the reflex for "verbose" everywhere else, and this tool has a
`--debug` for that.

The same version line opens the help and every argument-less refusal, so
whatever the tool prints says which build printed it.

### No argument, and no terminal

The tool says what it looked for before it prints the help: whether there is a
`.env` in the current directory, whether `SQL_SERVER` is set. Then it names the
command to type next. It used to print ninety lines of options and not one word
about what was missing.

### `env init`

Writes `.env.example`, the annotated template listing every setting this tool
accepts, to `.env` in the current directory, or to `--to FILE`.

- It refuses to write over an existing file unless `--force` is given, since
  that file is where your server and password live.
- The template is embedded in the binary, which is the point: the key set is
  closed, an unrecognised key stops the run, and the person who received the
  executable on its own has no other copy of the list.

### Progress, on stderr

`collect` shows its progress on **stderr**:

- **on a terminal**, one line rewritten in place with the count, the percentage,
  the elapsed time and the collector currently running, refreshed every second
  so that a slow collector can be told apart from a hung one;
- **redirected**, one plain line per finished unit instead, so `2> run.log`
  stays a file a person can read;
- **a failed unit** leaves a permanent line either way.

Nothing of this touches **stdout**, which still carries only the summary and the
archive path. `sql-auditor collect | tail -1` reads the same thing it always
did.

### When you cannot tell what it is doing

`--debug` prints a timeline on **stderr**: one line before each step that can
take time, stamped with the time since the process started.

```
$ sql-auditor --debug check
debug   +0.0ms  start, sql-auditor 0.21.0 (2edb1455), windows/amd64, go1.26.7
debug   +0.0ms  working directory C:\audits\sql01
debug   +0.0ms  stdin tty=false, stdout tty=false, SQL_AUDITOR_NO_TUI="", args=1 → subcommand
debug   +0.0ms  .env read, 2 setting(s)
debug   +0.0ms  resolved: server="192.0.2.1" database="master" auth=windows connect-timeout=3s query-timeout=1m0s output-dir="output"
debug   +0.5ms  dispatching to check
debug   +0.5ms  reading and linting the corpus
debug  +19.2ms  connecting to 192.0.2.1 as windows, up to 3s to dial, then one probe per capability at 1m0s each
debug   +3.02s  the instance has been probed
debug   +3.02s  check finished, exit 1
```

It is the **gap between two stamps** that answers the question, which is why
every line is written before the thing it names rather than after: a run that
hangs prints no "finished" line, so the last line you see is the one that hung.

The counting from process start is deliberate too. If the first line itself
arrives seconds after you pressed enter, the time went into loading twelve
megabytes of executable past a virus scanner, and nothing inside the program
would ever have shown you that.

Three things about where it goes and how it is turned on:

- **stderr, always.** `check` writes its listing to stdout and `collect` writes
  the archive path there, because `sql-auditor collect | tail -1` is how a
  script picks it up. Under the wizard the lines are held back until the
  terminal is restored, so a stamped line cannot land in the middle of a frame.
- **`SQL_AUDITOR_DEBUG`** does the same thing from the process environment, under
  the same rule as `SQL_AUDITOR_NO_TUI`: any non-empty value turns it on,
  `false` and `0` included. It exists for the scheduled task that has an
  environment to set and no command line to change.
- **`sql-auditor --debug` on its own**, with no command, explains the
  argument-less run itself rather than being read as a command called
  `--debug`.

The timeline carries no password. The `.env` line reports how many settings were
read and never which; the resolved line names the server, the database, the
authentication kind and the timeouts, and stops there. It is meant to be pasted
into a ticket.

### The non-interactive path

`check` and `collect` are that path in their own right rather than as a leftover
from before the wizard.

- They are what a scheduled task, a remote shell, a CI job and a runbook use.
- They are what makes a run reproducible, because a command line can be pasted
  into a ticket and a sequence of keystrokes cannot.
- `check` in particular is meant to be run without a human present, since its
  whole output is a verdict something else can read.
- `ctrl-c` and `SIGTERM` behave the same here as in the wizard: the collection
  finishes what is in flight and still writes its manifest and its archive,
  marked as cancelled. A second `ctrl-c` abandons the run instead, so a
  collection that will not wind down can still be stopped.

An argument therefore wins over everything else: `sql-auditor collect` does the
same work whatever terminal it finds itself attached to, and no invocation that
asked for something ever gets a wizard instead.

### When the wizard steps aside

Exactly three cases.

- **`SQL_AUDITOR_NO_TUI` is set** to any non-empty value. This is the escape
  hatch for a terminal that is real but that you do not want a full-screen
  program on: a nested session, a logging wrapper, a shell inside an editor. It
  is read from the **process environment**, and it is deliberately **not** a
  `.env` key: that set is closed, an unrecognised key there is a hard failure
  rather than a warning, and this is not a connection setting. Putting it in
  `.env` will stop the tool, not silence the wizard.
- **Either stdin or stdout is not a terminal.** Both ends are asked about
  separately, because a pipe on either one is enough. `sql-auditor > run.log`
  quietly starting a collection against a production instance would be the worst
  outcome this program could produce, so with no argument and no terminal it
  prints the usage on stderr and exits `2`, exactly as it did before the wizard
  existed.
- **Raw mode cannot be taken.** The terminal claims to be one and then refuses
  the mode a full-screen program needs; rather than degrade into a half-drawn
  screen, the wizard reports the failure and points at `sql-auditor collect`,
  which needs no terminal at all.

## Options for `check` and `collect`

### Connection and output

| Flag | Meaning |
| --- | --- |
| `--server HOST[,PORT]` | overrides `SQL_SERVER` |
| `--user NAME` | overrides `SQL_USER` |
| `--env PATH` | `.env` file to read (default `.env`) |
| `--password-file FILE` | read `SQL_PASSWORD` from this file rather than from `.env`. One trailing line ending is ignored; an empty file is refused |
| `--password-stdin` | read `SQL_PASSWORD` from standard input, same rules |
| `--queries-dir DIR` | run a corpus from disk instead of the embedded one. Every file is checked first for what its statements DO, and one that changes the server is refused rather than run. That check is a syntactic guard against the accident and **not a sandbox**: it bounds what a mistake can do, it does not vouch for an author this project has never seen ([what is allowed](docs/dba-guide.md#what-a-corpus-from-a-directory-is-allowed-to-contain)) |
| `--output-dir DIR` | where to write results |
| `--keep` | keep an existing same-day run folder, suffixing this run |
| `--grant-script FILE` | `check` only. Write the T-SQL that grants the permissions found missing, for the login the server reports, with the reason for each. Never executed. |

### Collecting more than the default

| Flag | Meaning |
| --- | --- |
| `--all` | turn on all nine options below at once: the eight off for disclosure and the one off for cost. See the note under these tables |
| `--include-session-text` | also collect the SQL text, and the login, host and program names, of the five longest-running snapshot transactions |
| `--include-object-definitions` | also collect the source of views, procedures, functions and triggers, one `.sql` file each, per database |
| `--include-deadlock-graphs` | also collect the deadlock reports `system_health` still holds, one `.xdl` file each |
| `--include-blocked-process-reports` | also collect the blocked process reports an Extended Events session captured, one `.xml` file each |
| `--estimate-compression` | also estimate page-compression savings on the largest uncompressed objects. Off for cost, not for disclosure: it samples real data into tempdb and is slow on large tables |
| `--query-store-detail` | also collect the full text and the execution plans of the heaviest Query Store queries, per database |
| `--query-store-plan-stats` | also look for the last profiled plan of each query the option above extracted. Does nothing on its own |

### The Query Store window

| Flag | Meaning |
| --- | --- |
| `--query-store-days N` | how many days of Query Store history to read, counting back from now (default 7). Not combinable with the two bounds below |
| `--query-store-from T` | start of the window, `YYYY-MM-DDTHH:MM` or `YYYY-MM-DD`, in the **server's** local time |
| `--query-store-to T` | end of the window, same format and same clock (default: the moment of collection). Given on its own it implies a seven-day window ending at that bound |
| `--query-store-top N` | how many queries to extract per database, across the four rankings once deduplicated (default 50). Queries with a forced plan are added on top of this |
| `--query-store-databases P` | comma-separated `*`/`?` patterns narrowing which of the collected databases the Query Store extraction reads. It narrows the selection; it never widens it |

### `--all` asks for the widest archive this tool can produce

It is the one option that is a convenience rather than a decision, and it should
be read as what it is: eight of the nine collectors it turns on are off by
default because of what they put in the archive, not because of what they cost.

That is the right thing on an instance you have a written mandate for and the
wrong thing everywhere else, and the tool will not ask you which it is.

It changes nothing else: no confirmation, no extra collectors beyond the nine,
and `MANIFEST.txt` still discloses them one by one, because what the archive
contains is the fact that matters and how briefly it was requested is not.

## Passwords

**There is no `--password` flag, and there will not be one.** A password on the
command line ends up in `ps` output, in shell history and in the process table
of every other user on the machine, and no amount of care at the call site takes
it back out.

`--password-file` and `--password-stdin` exist because that objection is about
the argument, not about scripting. A path is not a secret, and a pipe is read by
this process alone. Neither appears in the process table, and a secret store
that prints to stdout can hand the password over without it ever touching disk:

```
vault read -field=password secret/sql-auditor | sql-auditor collect --password-stdin
```

Both read the value the same way:

- exactly one trailing line ending is removed and nothing else, so a password
  ending in a space is the password you wrote;
- an empty source is refused rather than treated as no password, because a CI
  step that wrote nothing would otherwise fall through to integrated
  authentication and measure the wrong login;
- giving both at once is refused too;
- the value obeys the ordinary precedence: it beats `SQL_PASSWORD` in `.env`,
  which beats the environment.

Otherwise, put it in `SQL_PASSWORD` in `.env`. The wizard's step 1 remains the
only place in this program where a password is *typed*, and what is typed there
stays in memory for the length of the run: it is masked on screen, it is never
written to disk, it never reaches `.env`, and it appears in no manifest and no
archive. A password from either option above is treated identically once read.

Both `check` and `collect` print `sql-auditor <version> (<build>)` on stderr
before they do anything else, so an archive, a terminal transcript and a bug
report can all be tied to the build that produced them.

## The options that widen the archive

Six collectors are off by default because of what they put in the archive. Each
is a decision to take deliberately, and each is covered at length in
[docs/dba-guide.md](docs/dba-guide.md).

### `--include-session-text`

Off by default, and turning it on is a disclosure decision, not a performance
one: that statement text is the verbatim SQL of live batches and can carry
literals copied out of application tables. Read
[the guide](docs/dba-guide.md#--include-session-text) before you use it.

### `--query-store-detail`

The same kind of decision, and a wider one. It writes:

- the untruncated text of the heaviest queries in each database;
- their execution plans as `.sqlplan` files;
- their statistics per interval.

A plan is not merely a longer statement: it carries the parameter values the
plan was compiled for, the literal predicates, and the name of every object the
query touches.

The cost to the instance is not the reason to hesitate. The Query Store is data
already on disk and the collector takes no lock. What ends up in the archive
is.

### `--include-object-definitions`

A third decision of the same kind, and what it discloses comes from somewhere
else again:

- session text is what was *running*;
- the Query Store is what *has run*;
- a module body is *code somebody on your side wrote*, and no execution needs to
  have touched it.

It routinely names linked servers and their addresses, and an `OPENQUERY` or an
`EXECUTE AS` can hold a credential in clear, most often in the procedure nobody
has opened in years, which is precisely the kind an audit goes looking for.

Encrypted modules are listed but their source is not, because the server does
not return it.

### `--include-deadlock-graphs`

The narrowest of them. `10.system/060.system-health.sql` already reports how
many deadlocks the `system_health` ring buffer holds and when they happened, on
every run and without a flag; it stops there because a deadlock report carries
the verbatim SQL of both victims.

This option crosses exactly that line and no other: it writes each report as an
`.xdl` file, which SSMS opens as the deadlock diagram. Nothing on the instance
is modified or cleared to read them.

### `--include-blocked-process-reports`

The only option in this tool that reads the server's file system. The reports
live in an Extended Events session's `.xel` files, which
`sys.fn_xe_file_target_read_file` opens as the SQL Server service account rather
than as the login you connected with. A report names the blocked session and the
one blocking it, with the SQL of both, the blocker included, which was doing
nothing but holding a lock.

It only produces anything if somebody turned the capture on:

- `blocked process threshold (s)` must be non-zero;
- a session must subscribe to `blocked_process_report` and write to a file.

`sql-auditor check` tests all of that and tells you which piece is missing, with
a link to a script that sets it up. `10.system/062.xe-sessions.sql` records the
same facts in every archive, without any flag, so an empty result can always be
told apart from a capture that was never running.

### `--query-store-plan-stats`

A second, separate decision rather than a widening of the first. Finding the
last profiled plan means reading the plan cache of the whole instance through
`sys.dm_exec_query_stats`, where every other per-database collector sees only
the database it was pointed at.

Only the plans belonging to the selected databases are kept, but the whole cache
is read to find them. Consenting to a deep read of one database is not consent
to that, so it is asked for separately.

Both options, and the window the bounds describe, are covered in
[the guide](docs/dba-guide.md#--query-store-detail-and---query-store-plan-stats).

## Configuration

Settings are read from a `.env` file in the working directory. Copy
[`.env.example`](.env.example) to `.env`, or run `sql-auditor env init`, which
writes the same file from inside the binary, and fill in `SQL_SERVER`. Every
other key in it is already set to the value the tool would use anyway.

Precedence is **flag, then `.env`, then the process environment, then the
default**. Note that `.env` beats an exported environment variable, which is the
reverse of the usual twelve-factor ordering and is
[explained in the guide](docs/dba-guide.md#env-overrides-exported-environment-variables).

### Connection

| Key | Default | Meaning |
| --- | --- | --- |
| `SQL_SERVER` | *(required)* | `HOST`, `HOST,PORT`, `HOST\INSTANCE`, optionally prefixed `tcp:` |
| `SQL_DATABASE` | `master` | initial database context |
| `SQL_USER` | *(empty)* | SQL login; empty means Windows integrated authentication |
| `SQL_PASSWORD` | *(empty)* | password for `SQL_USER` |
| `SQL_INTEGRATED_SECURITY` | `false` | force Windows authentication even when `SQL_USER` is set |
| `SQL_ENCRYPT` | `true` | encrypt the connection |
| `SQL_TRUST_SERVER_CERTIFICATE` | `false` | `true` accepts ANY certificate, which is what a machine-in-the-middle needs to read the login and its password. On an instance with a self-signed certificate the first run fails and explains the choice ([why](docs/dba-guide.md#tls)) |

### Timeouts and identification

| Key | Default | Meaning |
| --- | --- | --- |
| `SQL_CONNECT_TIMEOUT_SEC` | `15` | seconds to establish the connection |
| `SQL_QUERY_TIMEOUT_SEC` | `60` | seconds per round trip made by the pipeline itself; a collector's own `@timeout` wins over it ([why](docs/dba-guide.md#timeouts-15-s-to-connect-and-why-raising-the-query-timeout-may-not-help)) |
| `SQL_APPLICATION_NAME` | `sql-auditor <version>` | application name shown in `sys.dm_exec_sessions`. The default carries the version, so a session can be tied to the corpus that produced it; a value you set is used exactly as written |

### What to run, and where it goes

| Key | Default | Meaning |
| --- | --- | --- |
| `QUERIES_DIR` | *(empty)* | run a corpus from disk instead of the embedded one, under the same statement check as `--queries-dir` |
| `OUTPUT_DIR` | `output` | where run folders and archives are written |
| `DB_INCLUDE` | *(empty)* | comma-separated `*`/`?` patterns; empty means all user databases |
| `DB_EXCLUDE` | *(empty)* | comma-separated `*`/`?` patterns |

### Query Store

| Key | Default | Meaning |
| --- | --- | --- |
| `QUERY_STORE_DAYS` | `7` | days of Query Store history to read, counting back from now; refused together with the two bounds below |
| `QUERY_STORE_FROM` | *(empty)* | start of an absolute window, `YYYY-MM-DDTHH:MM` or `YYYY-MM-DD`, in the server's local time |
| `QUERY_STORE_TO` | *(empty)* | end of that window, same format and same clock; on its own it implies seven days ending there |
| `QUERY_STORE_TOP` | `50` | queries extracted per database, across the four rankings once deduplicated; forced plans are added on top |
| `QUERY_STORE_DB_INCLUDE` | *(empty)* | comma-separated `*`/`?` patterns narrowing which collected databases the extraction reads |

### The key set is closed

An unrecognised key is an error, not a warning, so a typo cannot silently change
what the collector does.

- `SQL_LOGIN` was renamed `SQL_USER`; the old name is refused by name rather
  than ignored.
- `SQL_AUDITOR_NO_TUI` and `SQL_AUDITOR_DEBUG` are absent from these tables on
  purpose: neither is a connection setting, both are read from the process
  environment only, and writing either into `.env` refuses the file.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | success, possibly degraded if a permission was refused |
| `2` | partial failure, or a configuration the tool will not act on |
| `1` | fatal: the instance could not be reached, so nothing was collected |

A refused permission exits `0`. It reduces what is collected, and the omission
is recorded in the archive, but it is not a failure of the run.

## Supported versions

The query corpus is written to the SQL Server 2012 (11.x) grammar, and the
collectors that are always run use only columns available in 2012.

Collectors that need something newer carry a minimum-version directive and are
skipped, with the reason recorded, on instances below it: 2014 (12.x), 2016
(13.x), 2016 SP1 (13.0.4001), 2016 SP2 (13.0.5026) and 2019 (15.0).

Where the collector has actually been executed:

| Version | Runs so far |
| --- | --- |
| SQL Server 2022 (16.0) | yes |
| SQL Server 2017 (14.0.1000) | repeatedly |
| SQL Server 2016 SP3 (13.0.6435) | once |
| anything below 2016 | none yet |

The 2012 floor is a **static claim**: every file has been parsed under the 2012
grammar and every version-gated column checked against Microsoft's
documentation. It has not yet been confirmed by a run.
[docs/verification-2012.md](docs/verification-2012.md) records exactly what has
and has not been verified, and is the checklist to fill in when the 2012 pass
happens.

## Before you run this against production

Read [docs/dba-guide.md](docs/dba-guide.md). It covers:

- what the tool reads;
- the permissions it needs, and what each missing one costs;
- what ends up in the archive;
- the three behaviours that are not obvious from the outside.

## Licence

MIT. See [LICENSE](LICENSE).
