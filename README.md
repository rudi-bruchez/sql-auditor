# sql-auditor

A single-binary collector that reads diagnostic facts out of a SQL Server
instance and writes them to JSON, then packs the run into a zip for transport.
It collects; it does not judge. There are no thresholds, no scores and no
recommendations anywhere in this repository. Deciding what the numbers mean is
somebody else's job, done later, somewhere else. The collector's only
responsibility is to gather the facts accurately, say what it could not gather,
and leave a record of both.

It issues read-only `SELECT` statements against system catalog views and dynamic
management views. It does not read user or application tables, does not write to
your databases, and does not change any configuration.

## Install

The source is published, so the route that gives you the most assurance is to
build it yourself, having read it:

```
git clone https://github.com/rudi-bruchez/sql-auditor
cd sql-auditor
go build ./cmd/sql-auditor
```

`go install` works from the same source:

```
go install github.com/rudi-bruchez/sql-auditor/cmd/sql-auditor@latest
```

With no version tagged yet, `@latest` resolves to the tip of the default branch,
so it gives you whatever has been merged and not necessarily what this file
describes. Until the first release, `git clone` and `go build` are the way to
get a binary you can tie to a commit you have read.

What is not there yet is everything about a *downloaded* binary. Releases are
meant to carry a SHA-256 for every asset and a build provenance attestation
tying each binary to the commit and workflow that produced it; until a release
exists, a binary somebody handed you cannot be checked against anything except
its own query corpus — and no amount of tooling will ever give you a
byte-identical rebuild to compare against. What can and cannot be verified is
set out in full in
[docs/dba-guide.md](docs/dba-guide.md#can-i-verify-the-binary).

## Running it

Run `sql-auditor` with no argument on a terminal and it opens a four-step
wizard: connection, verification, what to collect, and the collection itself.
That is the default because the three things a first run gets wrong are all
invisible from a command line. Step 1 shows the address, the initial database,
the authentication mode and whether the server certificate is actually
validated — resolved from `.env` and the environment exactly as `collect` would
resolve them — and lets you correct the server and type a password. Step 2
probes the instance and lists every capability with the full consequence of each
refusal, never truncated, because that sentence is the only place the cost of a
missing `GRANT` is ever spelled out; `[g]` writes the T-SQL granting precisely
what was found missing, and the screen refuses to continue at all when the
coverage of a run cannot be established. Step 3 lists the opt-ins with what each
one discloses beside it, and the number of collectors resolved *for this
instance* rather than the size of the corpus, since the version gates close some
of them on every server. Step 4 is the collection, showing the collector, the
database, the elapsed time and the bytes written so far, and it ends on the path
of the archive alone on its line, indented, so that it can be selected with one
drag and pasted into a mail.

The keys are written at the bottom of every screen. On step 1 the quit key is
`ctrl-c`, not `[q]`, and it is the one screen where they differ:
every printable character there belongs to the field being edited, so an
instance named `QUALIF` and a password with a `q` in it have to stay typeable.
On the later screens `[q]` quits, `[b]` goes back, `[r]` runs the probe again,
and `ctrl-c` cancels whatever is being waited on — including a collection, which
still writes its manifest and its archive and says on the final screen that what
you are holding is partial.

The wizard advises on the harvest and never on the instance. "`VIEW SERVER
STATE` is missing, here is the T-SQL that grants it" is a statement about what
can be collected; "your Query Store will fill up in twelve days" is an opinion
about your server, and no layer of this program is allowed to hold one. The
sentence at the top of this file is not softened by the wizard existing: it
collects, it does not judge, and deciding what the numbers mean stays somebody
else's job. Nor does the wizard edit your configuration — `.env` remains the
place settings live, and the screens display its effect without ever rewriting
it — and it changes nothing on the server: `[g]` writes a file, and a DBA reads
it and runs it themselves.

## Commands

```
sql-auditor                          open the wizard (a terminal on both ends, no argument)
sql-auditor check                    verify connectivity, permissions and configuration,
                                     and list what a collection would run
sql-auditor collect                  collect, then archive
sql-auditor env init                 write the annotated .env template
sql-auditor queries export --to DIR  write the embedded queries to disk
sql-auditor version
```

`env init` writes `.env.example` — the annotated template listing every setting
this tool accepts — to `.env` in the current directory, or to `--to FILE`. It
refuses to write over an existing file unless `--force` is given, since that
file is where your server and password live. The template is embedded in the
binary, which is the point: the key set is closed, an unrecognised key stops the
run, and the person who received the executable on its own has no other copy of
the list.

`collect` shows its progress on **stderr**: on a terminal, one line rewritten in
place with the count, the percentage, the elapsed time and the collector
currently running, refreshed every second so that a slow collector can be told
apart from a hung one. Redirected, the same run writes one plain line per
finished unit instead, so `2> run.log` stays a file a person can read. A failed
unit leaves a permanent line either way. Nothing of this touches **stdout**,
which still carries only the summary and the archive path — `sql-auditor collect
| tail -1` reads the same thing it always did.

`check` and `collect` are the non-interactive path, and they are that in their
own right rather than as a leftover from before the wizard. They are what a
scheduled task, a remote shell, a CI job and a runbook use; they are what makes
a run reproducible, because a command line can be pasted into a ticket and a
sequence of keystrokes cannot; and `check` in particular is meant to be run
without a human present, since its whole output is a verdict something else can
read. An argument therefore wins over everything else: `sql-auditor collect`
does the same work whatever terminal it finds itself attached to, and no
invocation that asked for something ever gets a wizard instead.

The wizard steps aside in exactly three cases.

- `SQL_AUDITOR_NO_TUI` is set to any non-empty value. This is the escape hatch
  for a terminal that is real but that you do not want a full-screen program on
  — a nested session, a logging wrapper, a shell inside an editor. It is read
  from the **process environment**, and it is deliberately **not** a `.env` key:
  that set is closed, an unrecognised key there is a hard failure rather than a
  warning, and this is not a connection setting. Putting it in `.env` will stop
  the tool, not silence the wizard.
- Either stdin or stdout is not a terminal. Both ends are asked about
  separately, because a pipe on either one is enough. `sql-auditor > run.log`
  quietly starting a collection against a production instance would be the worst
  outcome this program could produce, so with no argument and no terminal it
  prints the usage on stderr and exits `2`, exactly as it did before the wizard
  existed.
- Raw mode cannot be taken. The terminal claims to be one and then refuses the
  mode a full-screen program needs; rather than degrade into a half-drawn
  screen, the wizard reports the failure and points at `sql-auditor collect`,
  which needs no terminal at all.

Options for `check` and `collect`:

| Flag | Meaning |
| --- | --- |
| `--server HOST[,PORT]` | overrides `SQL_SERVER` |
| `--user NAME` | overrides `SQL_USER` |
| `--env PATH` | `.env` file to read (default `.env`) |
| `--password-file FILE` | read `SQL_PASSWORD` from this file rather than from `.env`. One trailing line ending is ignored; an empty file is refused |
| `--password-stdin` | read `SQL_PASSWORD` from standard input, same rules |
| `--queries-dir DIR` | run a corpus from disk instead of the embedded one |
| `--output-dir DIR` | where to write results |
| `--keep` | keep an existing same-day run folder, suffixing this run |
| `--all` | turn on all seven options below at once — the six off for disclosure and the one off for cost. See the note under this table |
| `--grant-script FILE` | `check` only. Write the T-SQL that grants the permissions found missing, for the login the server reports, with the reason for each. Never executed. |
| `--include-session-text` | also collect the SQL text, and the login, host and program names, of the five longest-running snapshot transactions |
| `--include-object-definitions` | also collect the source of views, procedures, functions and triggers, one `.sql` file each, per database |
| `--include-deadlock-graphs` | also collect the deadlock reports `system_health` still holds, one `.xdl` file each |
| `--include-blocked-process-reports` | also collect the blocked process reports an Extended Events session captured, one `.xml` file each |
| `--estimate-compression` | also estimate page-compression savings on the largest uncompressed objects. Off for cost, not for disclosure: it samples real data into tempdb and is slow on large tables |
| `--query-store-detail` | also collect the full text and the execution plans of the heaviest Query Store queries, per database |
| `--query-store-plan-stats` | also look for the last profiled plan of each query the option above extracted. Does nothing on its own |
| `--query-store-days N` | how many days of Query Store history to read, counting back from now (default 7). Not combinable with the two bounds below |
| `--query-store-from T` | start of the window, `YYYY-MM-DDTHH:MM` or `YYYY-MM-DD`, in the **server's** local time |
| `--query-store-to T` | end of the window, same format and same clock (default: the moment of collection). Given on its own it implies a seven-day window ending at that bound |
| `--query-store-top N` | how many queries to extract per database, across the four rankings once deduplicated (default 50). Queries with a forced plan are added on top of this |
| `--query-store-databases P` | comma-separated `*`/`?` patterns narrowing which of the collected databases the Query Store extraction reads. It narrows the selection; it never widens it |

`--all` is the one option that is a convenience rather than a decision, and it
should be read as what it is: six of the seven collectors it turns on are off by
default because of what they put in the archive, not because of what they cost.
It asks for the widest archive this tool can produce. That is the right thing on
an instance you have a written mandate for and the wrong thing everywhere else,
and the tool will not ask you which it is. It changes nothing else: no
confirmation, no extra collectors beyond the seven, and `MANIFEST.txt` still
discloses them one by one, because what the archive contains is the fact that
matters and how briefly it was requested is not.

There is no `--password` flag, and there will not be one. A password on the
command line ends up in `ps` output, in shell history and in the process table
of every other user on the machine, and no amount of care at the call site takes
it back out.

`--password-file` and `--password-stdin` exist because that objection is about
the argument, not about scripting. A path is not a secret, and a pipe is read by
this process alone — neither appears in the process table, and a secret store
that prints to stdout can hand the password over without it ever touching disk:

```
vault read -field=password secret/sql-auditor | sql-auditor collect --password-stdin
```

Both read the value the same way. Exactly one trailing line ending is removed
and nothing else, so a password ending in a space is the password you wrote; an
empty source is refused rather than treated as no password, because a CI step
that wrote nothing would otherwise fall through to integrated authentication and
measure the wrong login. Giving both at once is refused too. The value obeys the
ordinary precedence — it beats `SQL_PASSWORD` in `.env`, which beats the
environment.

Otherwise, put it in `SQL_PASSWORD` in `.env`. The wizard's step 1 remains the
only place in this program where a password is *typed*, and what is typed there
stays in memory for the length of the run: it is masked on screen, it is never
written to disk, it never reaches `.env`, and it appears in no manifest and no
archive. A password from either option above is treated identically once read.

Both `check` and `collect` print `sql-auditor <version> (<build>)` on stderr
before they do anything else, so an archive, a terminal transcript and a bug
report can all be tied to the build that produced them.

`--include-session-text` is off by default and turning it on is a disclosure
decision, not a performance one: that statement text is the verbatim SQL of live
batches and can carry literals copied out of application tables. Read
[docs/dba-guide.md](docs/dba-guide.md#--include-session-text) before you use it.

`--query-store-detail` is the same kind of decision, and a wider one. It writes
the untruncated text of the heaviest queries in each database, their execution
plans as `.sqlplan` files, and their statistics per interval. A plan is not
merely a longer statement: it carries the parameter values the plan was compiled
for, the literal predicates, and the name of every object the query touches. The
cost to the instance is not the reason to hesitate — the Query Store is data
already on disk and the collector takes no lock — what ends up in the archive
is.

`--include-object-definitions` is a third decision of the same kind, and what it
discloses comes from somewhere else again. Session text is what was running; the
Query Store is what has run; a module body is code somebody on your side wrote,
and no execution needs to have touched it. It routinely names linked servers and
their addresses, and an `OPENQUERY` or an `EXECUTE AS` can hold a credential in
clear — most often in the procedure nobody has opened in years, which is
precisely the kind an audit goes looking for. Encrypted modules are listed but
their source is not, because the server does not return it.

`--include-deadlock-graphs` is the narrowest of the four. `10.system/060.system-health.sql`
already reports how many deadlocks the `system_health` ring buffer holds and when
they happened, on every run and without a flag; it stops there because a deadlock
report carries the verbatim SQL of both victims. This option crosses exactly that
line and no other — it writes each report as an `.xdl` file, which SSMS opens as
the deadlock diagram. Nothing on the instance is modified or cleared to read them.

`--include-blocked-process-reports` is the fifth, and the only option in this
tool that reads the server's file system. The reports live in an Extended Events
session's `.xel` files, which `sys.fn_xe_file_target_read_file` opens as the SQL
Server service account rather than as the login you connected with. A report
names the blocked session and the one blocking it, with the SQL of both — the
blocker included, which was doing nothing but holding a lock.

It only produces anything if somebody turned the capture on: `blocked process
threshold (s)` must be non-zero, and a session must subscribe to
`blocked_process_report` and write to a file. `sql-auditor check` tests all of
that and tells you which piece is missing, with a link to a script that sets it
up. `10.system/062.xe-sessions.sql` records the same facts in every archive,
without any flag, so an empty result can always be told apart from a capture
that was never running.

`--query-store-plan-stats` is a second, separate decision rather than a
widening of the first. Finding the last profiled plan means reading the plan
cache of the whole instance through `sys.dm_exec_query_stats`, where every other
per-database collector sees only the database it was pointed at; only the plans
belonging to the selected databases are kept, but the whole cache is read to
find them. Consenting to a deep read of one database is not consent to that, so
it is asked for separately. Both options, and the window the bounds describe,
are covered in
[docs/dba-guide.md](docs/dba-guide.md#--query-store-detail-and---query-store-plan-stats).

## Configuration

Settings are read from a `.env` file in the working directory. Copy
[`.env.example`](.env.example) to `.env` — or run `sql-auditor env init`, which
writes the same file from inside the binary — and fill in `SQL_SERVER`; every other
key in it is already set to the value the tool would use anyway. Precedence is
flag, then `.env`, then the process environment, then the default — note that
`.env` beats an exported environment variable, which is the reverse of the usual
twelve-factor ordering and is
[explained in the guide](docs/dba-guide.md#env-overrides-exported-environment-variables).

| Key | Default | Meaning |
| --- | --- | --- |
| `SQL_SERVER` | *(required)* | `HOST`, `HOST,PORT`, `HOST\INSTANCE`, optionally prefixed `tcp:` |
| `SQL_DATABASE` | `master` | initial database context |
| `SQL_USER` | *(empty)* | SQL login; empty means Windows integrated authentication |
| `SQL_PASSWORD` | *(empty)* | password for `SQL_USER` |
| `SQL_INTEGRATED_SECURITY` | `false` | force Windows authentication even when `SQL_USER` is set |
| `SQL_ENCRYPT` | `true` | encrypt the connection |
| `SQL_TRUST_SERVER_CERTIFICATE` | `true` | skip server certificate validation |
| `SQL_CONNECT_TIMEOUT_SEC` | `15` | seconds to establish the connection |
| `SQL_QUERY_TIMEOUT_SEC` | `60` | seconds per round trip made by the pipeline itself; a collector's own `@timeout` wins over it ([why](docs/dba-guide.md#timeouts-15-s-to-connect-and-why-raising-the-query-timeout-may-not-help)) |
| `SQL_APPLICATION_NAME` | `sql-auditor <version>` | application name shown in `sys.dm_exec_sessions`. The default carries the version, so a session can be tied to the corpus that produced it; a value you set is used exactly as written |
| `QUERIES_DIR` | *(empty)* | run a corpus from disk instead of the embedded one |
| `OUTPUT_DIR` | `output` | where run folders and archives are written |
| `DB_INCLUDE` | *(empty)* | comma-separated `*`/`?` patterns; empty means all user databases |
| `DB_EXCLUDE` | *(empty)* | comma-separated `*`/`?` patterns |
| `QUERY_STORE_DAYS` | `7` | days of Query Store history to read, counting back from now; refused together with the two bounds below |
| `QUERY_STORE_FROM` | *(empty)* | start of an absolute window, `YYYY-MM-DDTHH:MM` or `YYYY-MM-DD`, in the server's local time |
| `QUERY_STORE_TO` | *(empty)* | end of that window, same format and same clock; on its own it implies seven days ending there |
| `QUERY_STORE_TOP` | `50` | queries extracted per database, across the four rankings once deduplicated; forced plans are added on top |
| `QUERY_STORE_DB_INCLUDE` | *(empty)* | comma-separated `*`/`?` patterns narrowing which collected databases the extraction reads |

The set is closed. An unrecognised key is an error, not a warning, so a typo
cannot silently change what the collector does. `SQL_LOGIN` was renamed
`SQL_USER`; the old name is refused by name rather than ignored. That closure is
also why `SQL_AUDITOR_NO_TUI` is absent from this table: it is not a connection
setting, it is read from the process environment only, and writing it into
`.env` refuses the file.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | success — possibly degraded, if a permission was refused |
| `2` | partial failure, or a configuration the tool will not act on |
| `1` | fatal — the instance could not be reached, so nothing was collected |

A refused permission exits `0`. It reduces what is collected, and the omission
is recorded in the archive, but it is not a failure of the run.

## Supported versions

The query corpus is written to the SQL Server 2012 (11.x) grammar, and the
collectors that are always run use only columns available in 2012. Collectors
that need something newer carry a minimum-version directive and are skipped,
with the reason recorded, on instances below it: 2014 (12.x), 2016 (13.x),
2016 SP1 (13.0.4001), 2016 SP2 (13.0.5026) and 2019 (15.0).

The versions the collector has actually been executed against are SQL Server
2022 (16.0), SQL Server 2017 (14.0.1000) repeatedly, and SQL Server 2016 SP3
(13.0.6435) once. Nothing below 2016 has been run yet. The 2012 floor is a
static claim — every file has been parsed under the 2012 grammar and every
version-gated column checked against Microsoft's documentation — and it has not
yet been confirmed by a run.
[docs/verification-2012.md](docs/verification-2012.md) records exactly what has
and has not been verified, and is the checklist to fill in when the 2012 pass
happens.

## Before you run this against production

Read [docs/dba-guide.md](docs/dba-guide.md). It covers what the tool reads, the
permissions it needs and what each missing one costs, what ends up in the
archive, and the three behaviours that are not obvious from the outside.

## Licence

MIT. See [LICENSE](LICENSE).
