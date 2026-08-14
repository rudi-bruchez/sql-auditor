# Running sql-auditor against your server

You have been handed a binary and asked to run it against an instance you are
responsible for. This document answers, in the order you are likely to ask them,
the questions you need answered before you do.

- [What it does, and what it does not do](#what-it-does-and-what-it-does-not-do)
- [What permissions it needs](#what-permissions-it-needs)
- [Run `check` first](#run-check-first)
- [Then run `collect`](#then-run-collect)
- [What is in the archive](#what-is-in-the-archive)
- [Can I verify the binary?](#can-i-verify-the-binary)
- [Three things that are not obvious from the outside](#three-things-that-are-not-obvious-from-the-outside)
  - [`READ UNCOMMITTED`](#the-collector-runs-under-read-uncommitted)
  - [Timeouts](#timeouts-15-s-to-connect-and-why-raising-the-query-timeout-may-not-help)
  - [`.env` precedence](#env-overrides-exported-environment-variables)
- [`--include-session-text`](#--include-session-text)
- [`--query-store-detail` and `--query-store-plan-stats`](#--query-store-detail-and---query-store-plan-stats)
- [`--include-object-definitions`](#--include-object-definitions)
- [`--include-deadlock-graphs`](#--include-deadlock-graphs)
- [`--include-blocked-process-reports`](#--include-blocked-process-reports)
- [Authentication](#authentication)
- [Reproducing a run locally](#reproducing-a-run-locally)

## What it does, and what it does not do

It connects to one instance, runs a fixed set of `SELECT` statements against
system catalog views and dynamic management views, writes each result to a JSON
file, and packs the result into a zip.

It does not:

- read any user or application table;
- run `INSERT`, `UPDATE`, `DELETE`, or any DDL;
- change any server or database setting, trace flag or configuration option;
- install anything on the server;
- take a lock that your workload has to wait behind.

The queries it runs are not hidden. They are compiled into the binary, and you
can write them out and read them before you run anything:

```
sql-auditor queries export --to ./queries-to-review
```

The corpus is 55 files. The archive records the SHA-256 of the exact corpus that
was used, so a run can be tied to the questions it asked.

Seven of those files are opt-in and produce nothing unless you ask for them:

| File | Option |
| --- | --- |
| `10.system/052.session-text.sql` | `--include-session-text` |
| `10.system/061.deadlock-graphs.sql` | `--include-deadlock-graphs` |
| `10.system/063.blocked-process-reports.sql` | `--include-blocked-process-reports` |
| `70.schema/041.compression-savings.sql` | `--estimate-compression` |
| `70.schema/080.modules.sql` | `--include-object-definitions` |
| `80.workload/021.query-store-detail.sql` | `--query-store-detail` |
| `80.workload/022.query-store-profiled.sql` | `--query-store-plan-stats` |

Six of the seven change what kind of data ends up in the archive and have
sections of their own below; `--estimate-compression` is opt-in for cost rather
than for disclosure.

It collects; it does not judge. There are no thresholds and no recommendations
in the tool or in its output. It gathers facts and records what it could not
gather.

The tool is MIT licensed — see [LICENSE](../LICENSE). Running it, modifying it
and passing it on are all permitted, and nothing here asks you to accept terms.
The source is published at `github.com/rudi-bruchez/sql-auditor`, so reading it
and building your own binary from it are both open to you. What that does and
does not prove about the binary you were handed is set out in
[Can I verify the binary?](#can-i-verify-the-binary).

## What permissions it needs

**The short answer: let the tool write the script.**

```
sql-auditor check --grant-script grants.sql
```

`check` probes each permission, then writes the T-SQL that grants exactly the
ones that came back refused — for the login the server reports, not the one in
your `.env`, which is not always the same principal. Each statement in the file
carries the reason for it, the collectors it unlocks, and, where there is one,
the security consequence to weigh. Nothing is executed: the login being
measured cannot grant anything by definition. Read the file, then hand it to
someone who can run it.

The rest of this section is what that script contains, for the reader who wants
to know before running anything.

The login must be able to connect. Beyond that there are six rights the
collector uses, and none of them is required: it probes each one before it
starts and carries on without whatever it was refused, recording the omission.
This is what each one costs when it is missing, in the collector's own words —
the wording below is the same string the tool prints and writes into the archive.

| Capability | Grant | If it is missing |
| --- | --- | --- |
| Connect to the instance | `CONNECT SQL` | nothing can run |
| Read server and database metadata | `VIEW ANY DEFINITION` (server level) | instance configuration and database file layout not collected |
| Read performance counters | `VIEW SERVER STATE`, or `VIEW SERVER PERFORMANCE STATE` on SQL Server 2022 and later | wait statistics, schedulers, memory and tempdb usage not collected |
| Read backup history | `SELECT` on `msdb.dbo.backupset` | backup history not collected — the report must not read this as 'no backups exist' |
| Read the Agent job inventory | `SQLAgentReaderRole` in msdb | Agent jobs not collected — the report must not read this as 'no jobs' or 'no failing jobs' |
| Read the Agent job steps | `SELECT` on `msdb.dbo.sysjobsteps` | job steps not collected — the report can say a job exists but not what it runs |
| Read the log shipping tables | `SELECT` on the six `msdb.dbo.log_shipping_*` tables | log shipping configuration and lag not collected — the report must not read this as 'no log shipping' |
| Read the SQL Server error log | covered by `VIEW SERVER STATE` before 2022; `VIEW ANY ERROR LOG` from 2022 | the error log is not collected — the report must not read this as 'no errors were logged' |

Three of those deserve a second look before you grant them.

`SQLAgentReaderRole` implies `SQLAgentUserRole`, whose members can create and
run jobs they own, through any proxy already granted to that role. On an
instance with permissive proxies that is a real privilege. Leaving it out is a
supported outcome: the Agent collector is skipped and the archive says so.

The job **steps** are a separate right because `SQLAgentReaderRole` does not
carry them: it shows the job inventory and refuses `msdb.dbo.sysjobsteps`. That
is why the two are probed separately — an instance can report every job and
nothing about what any of them runs, and a manifest claiming the Agent is
covered when only half of it is would be worse than one admitting the gap. The
generated script grants `SELECT` on that one table and nothing else. Weigh it:
job step commands are the one place in msdb where a connection string or a
password is routinely written in clear by whoever wrote the job. The collector
projects the first 200 characters of T-SQL steps only, and for CmdExec,
PowerShell or SSIS steps nothing but the subsystem and the length — but the
grant itself lets the login read every command in full, so the decision is
about the login rather than about the collector.

On SQL Server 2022 and later the generated script asks for `VIEW SERVER
PERFORMANCE STATE` rather than `VIEW SERVER STATE`. It covers the dynamic
management views the collector reads without also opening the security-related
ones, and it is the narrower of the two.

One caution about `VIEW ANY DEFINITION`. SQL Server does not raise an error when
it is missing. Metadata visibility filters catalog views row by row, so a query
against `sys.databases` still succeeds and simply returns fewer rows — only the
databases the login owns or is mapped to. Such a login can produce an archive
that lists no user databases at all while every query reports success. The
collector detects this case specifically and says so in the archive, but it is
worth granting the right so the picture is complete.

### Step 1: the instance-scope rights

Creating the login is the one part the tool cannot write for you, since it has
no business choosing a password:

```sql
CREATE LOGIN sqlauditor WITH PASSWORD = '...';
```

From there, run `check` with that login and let it write the rest:

```
sql-auditor check --user sqlauditor --grant-script grants.sql
```

The generated file grants `VIEW ANY DEFINITION` and `VIEW SERVER STATE` at the
server, creates a user in msdb, and grants `SELECT` on `msdb.dbo.backupset` and
on `msdb.dbo.sysjobsteps` plus `SQLAgentReaderRole` — and nothing else. In
particular it does **not** put
the login in `db_datareader` on msdb, which an earlier version of this guide
suggested: that role also hands over Database Mail contents, job step commands
and operator addresses, none of which the collector reads.

**Step 1 alone is not the whole recipe, and stopping here is the mistake to
avoid.** A login with exactly these rights passes every permission probe —
green `ok` lines, no warning — and then collects roughly two thirds of what it
should, because every per-database collector is skipped. Measured on SQL Server
2022, against the corpus of the day: 9 results instead of 13. The corpus has
grown since, and so has the gap; the ratio is what the number is there for.

The generated script covers this too, which is why it is written at the end of
`check` rather than straight after the probes: it needs the list of databases
the run would skip. Those get a section of their own.

### Step 2: access to each database

Per-database collectors run inside each database and need the login to be able
to connect to it. Without that, the database is skipped with the reason
`no access for this login` and the archive is missing the file layout, backup
history, index and fragmentation data for it. The skip is recorded in
`MANIFEST.txt` under "Databases skipped", so the omission is visible — but only
if you read that far.

The generated script writes this section for you, one block per database the
run reported as skipped. What it emits is a user and nothing else:

```sql
USE AppDb;
CREATE USER sqlauditor FOR LOGIN sqlauditor;
```

No role membership. A user with none carries `CONNECT` and that is all the
per-database collectors need: the metadata they read is already covered by
`VIEW ANY DEFINITION` at the server, and the dynamic management views by
`VIEW SERVER STATE`, which implies `VIEW DATABASE STATE` everywhere.

There is one exception, and it is opt-in. `--estimate-compression` runs
`sp_estimate_data_compression_savings`, which samples the actual rows of a
table into tempdb and therefore needs `SELECT` on the data. Without it that
collector fails with error 229 and the run says so. If you want compression
estimates, that flag is the only reason to grant read access to user tables —
and it is worth granting narrowly, on the tables you care about, rather than
through `db_datareader`.

Or, to cover the whole instance including databases created later, one
server-level grant does it (SQL Server 2014 and later):

```sql
GRANT CONNECT ANY DATABASE TO sqlauditor;
```

`CONNECT ANY DATABASE` grants nothing in any database beyond the ability to
connect. Combined with the `VIEW ANY DEFINITION` from step 1, that is exactly
what the collector needs and no more: metadata everywhere, table contents
nowhere. You do not also need `VIEW ANY DATABASE` — `VIEW ANY DEFINITION`
already implies it.

On SQL Server 2012, which has no `CONNECT ANY DATABASE`, the per-database user
is the only route.

### Confirming you got it right

Run `check` and look at the database list, not only at the permission lines.
Green probes above a `Databases that would be collected (0)` line mean step 2
is missing. Re-running with --grant-script writes the fix.

## Run `check` first

`check` connects, probes each permission, prints the query list and the exact set
of databases a collection would touch, then exits without collecting anything.
Run it before you run `collect`.

```
sql-auditor check
```

On an instance where everything is available:

```
sql-auditor 0.11.0 (a1b2c3d4)

note: the connection is encrypted but the server certificate is NOT validated (SQL_TRUST_SERVER_CERTIFICATE=true)
Queries (38):
  10.system/010.properties.sql
  10.system/013.memory-model.sql             SQL Server 13.0.4001+
  10.system/014.cpu-topology.sql             SQL Server 13.0.5026+
  10.system/015.buffer-pool.sql
  10.system/020.host-services.sql            SQL Server 13.0.4001+
  10.system/030.file-io.sql
  10.system/040.error-log.sql
  10.system/041.connectivity.sql
  10.system/050.tempdb.sql
  10.system/051.version-store.sql            SQL Server 13.0.5026+
  10.system/052.session-text.sql             --include-session-text (off)
  10.system/060.system-health.sql
  20.databases/010.all-databases.sql
  20.databases/011.all-databases-2014.sql    SQL Server 12+
  20.databases/012.all-databases-query-store.sql SQL Server 13+
  20.databases/020.properties.sql            per database
  20.databases/021.properties-2014.sql       per database, SQL Server 12+
  20.databases/022.query-store.sql           per database, SQL Server 13+
  20.databases/023.log-vlf.sql               per database, SQL Server 13.0.5026+
  20.databases/024.log-stats.sql             per database, SQL Server 13.0.5026+
  40.security/010.principals.sql
  50.agent/010.jobs.sql
  50.agent/020.job-steps.sql
  60.backup/010.history.sql
  60.backup/020.restore-history.sql
  70.schema/010.objects.sql                  per database
  70.schema/020.index-usage.sql              per database
  70.schema/030.index-operational.sql        per database
  70.schema/040.compression.sql              per database
  70.schema/041.compression-savings.sql      per database, --estimate-compression (off)
  70.schema/050.heaps.sql                    per database
  80.workload/010.wait-stats.sql
  80.workload/020.query-store.sql            per database, SQL Server 13.0+
  80.workload/021.query-store-detail.sql     per database, SQL Server 13+, --query-store-detail (off), one directory per database: query text, plans and per-interval statistics
  80.workload/022.query-store-profiled.sql   per database, SQL Server 15.0+, --query-store-plan-stats (off), the last profiled plan, when the instance still holds one
  80.workload/023.query-store-most-executed.sql per database, SQL Server 13+
  80.workload/030.implicit-conversions.sql
  80.workload/040.plan-cache.sql

Output   : output

Permissions:
  ok      connect
  ok      view_any_definition
  ok      view_server_state
  ok      msdb_read
  ok      agent_jobs
  ok      agent_job_steps
  ok      error_log

Server   : SQLPROD01  16.0.4265.3  Developer Edition (64-bit)
Login    : sqlauditor

Databases that would be collected (1):
  - AppDb -> AppDb/
```

The first line goes to stderr, before anything else happens, so a transcript
always says which build produced it.

The annotations on the right are the conditions attached to a query: `per
database` means it runs once for each selected database, `SQL Server 13+` means
it is skipped on older instances, the flag name and its state mean it is opt-in,
and the sentence after those on the two Query Store lines says that the
collector writes a directory rather than a single JSON document.

### A `denied` line is a warning, not a failure

On a login without `VIEW ANY DEFINITION` and `VIEW SERVER STATE`:

```
sql-auditor 0.11.0 (a1b2c3d4)

note: the connection is encrypted but the server certificate is NOT validated (SQL_TRUST_SERVER_CERTIFICATE=true)
Queries (38):
  ... (unchanged - the query list does not depend on permissions)

Output   : output

Permissions:
  ok      connect
  denied  view_any_definition — instance configuration and database file layout not collected
  denied  view_server_state — wait statistics, schedulers, memory and tempdb usage not collected
  ok      msdb_read
  ok      agent_jobs
  denied  agent_job_steps — job steps not collected — the report can say a job exists but not what it runs
  ok      error_log

Server   : SQLPROD01  16.0.4265.3  Developer Edition (64-bit)
Login    : sqlauditor

Databases that would be collected (0):
  (none)
```

**`check` exits `0` here.** That is deliberate. A denied permission degrades the
run; it does not break it. If a missing `VIEW ANY DEFINITION` exited non-zero,
the reasonable conclusion would be that the tool is broken, and the run would
stop — which costs more than the missing data does. So read the exit code and
the `denied` lines as answering two different questions: the exit code says
whether the tool can proceed, and the `denied` lines say how much of the picture
you will get.

`check` and `collect` use the same three codes:

| Code | Meaning |
| --- | --- |
| `0` | usable, possibly degraded |
| `1` | the instance did not answer — nothing can be collected |
| `2` | the configuration is unusable, or the run was partial: a `SQL_SERVER` that cannot be parsed, a `--queries-dir` that cannot be read, a query corpus that fails its lint, an output directory that cannot be written, or, for `collect`, a collector that failed |

A mistyped address is `2`, not `1`. `HOST\` with no instance name, or a bare
`::1,1433` missing its brackets, is refused before a socket is opened, so
nothing has been asked of any server and `1` would send you to check a machine
that was never contacted.

Whenever either command exits non-zero it prints the reason on stderr first. If
you get a bare `1` with nothing above it, that is a bug — please report it.

There is a real difference between `denied` and `error` in that list.
`denied` means the server was reached, understood the query, and refused it —
a permission problem. `error` means no answer came back at all: a dropped
socket, a failed login, an unreachable host. The tool never reports one as the
other, so a `denied` line always means a `GRANT` is the fix and an `error` line
never does.

## Then run `collect`

```
sql-auditor collect
```

It writes into `OUTPUT_DIR` (default `output`), one folder per run, named for
the server and the date, with a zip of the same name beside it:

```
output/
  SQLPROD01-2026-08-08/
    MANIFEST.txt
    _run.json
    10.system/
      010.properties.json
      013.memory-model.json
      014.cpu-topology.json
      050.tempdb.json
      051.version-store.json
    20.databases/
      010.all-databases.json
      011.all-databases-2014.json
      012.all-databases-query-store.json
      AppDb/
        020.properties.json
        021.properties-2014.json
        022.query-store.json
        023.log-vlf.json
    70.schema/
      AppDb/
        010.objects.json
        020.index-usage.json
    80.workload/
      010.wait-stats.json
      AppDb/
        020.query-store.json
        023.query-store-most-executed.json
  SQLPROD01-2026-08-08.zip
```

One file per collector, one folder per database for the collectors that run
inside one. The Query Store extraction is the single exception and writes a
directory instead of a document; that shape is described in
[its own section](#--query-store-detail-and---query-store-plan-stats).

The last line of output is the path to the zip. That file is the deliverable;
everything else is the same content unpacked.

Running `collect` twice on the same day replaces the earlier run — both the
folder and the zip. It warns on stderr before it does. Pass `--keep` to write
this run alongside the previous one instead; the new run gets a time suffix.

Runs are quick. On a small instance the whole collection finishes in about a
second; on a large estate the per-database collectors dominate, so budget by
database count.

## What is in the archive

`MANIFEST.txt` sits at the top of the archive and is written for whoever has to
approve the transfer. It states which server this is, when it was read, how much
was read, what could not be read and why, and what kind of data is inside. This
is its central paragraph, verbatim from a real run:

```
What this archive contains
--------------------------
Server and database metadata, and performance counters: configuration
settings, database options, file layout and sizes, wait statistics, index
and backup metadata.

The collector issues only read-only SELECT statements against system
catalog views and dynamic management views. It runs no INSERT, UPDATE,
DELETE or DDL, and it does not read any user or application table.

What is in here that names things:
  - this server's name, version, edition and file paths
  - database, schema and object names
  - the Windows or SQL login names of database owners

The password of the login used for this run is recorded nowhere in this
archive. The run settings in _run.json are the query and output directories,
the database name filters, and whether session text was collected; any setting
whose name marks it as a password, token or other secret is replaced with
"(redacted)" before that block is written.

That is metadata about the estate rather than the data held in it, but the
login names above are attributable to people, so treat this as internal
infrastructure documentation rather than public material.
```

That paragraph is generated from what the run actually did, not fixed at compile
time. Each of the six options that widen what the archive holds adds its own
line to it:

- `--include-session-text` discloses the captured statement text and the login,
  host and program names behind it;
- `--query-store-detail` discloses the full text of the heaviest Query Store
  queries, their execution plans in XML and their statistics per interval, and
  says in the same breath that a plan carries the compiled parameter values, the
  literal predicates and the name of every object the query touches;
- `--query-store-plan-stats` discloses that the last profiled plan was looked
  up as well, and that finding it meant reading the plan cache of the whole
  instance rather than only the databases listed above;
- `--include-object-definitions` discloses the source of the views, procedures,
  functions and triggers, and says that this is code written on your side which
  can name linked servers and carry a credential in clear;
- `--include-deadlock-graphs` discloses the deadlock reports, and says that each
  one carries the SQL of both victims;
- `--include-blocked-process-reports` discloses the blocked process reports, says
  that each names both sessions and carries their SQL, and states that they were
  read off the server's own file system without any file being modified.

Any one of the six also changes the closing paragraph, from "metadata about
the estate" to a statement that the archive should be treated as potentially
containing personal data. Which form was used is a property of the archive:
whoever receives it can read which options were on without taking anyone's word
for it.

`_run.json` beside it holds the same facts in machine-readable form, plus the
full list of output files, per-query timings and byte counts, and the SHA-256 of
the query corpus.

**The archive identifies the server.** Its name is in the zip filename, in
`MANIFEST.txt` and in `_run.json`, along with its version, edition, file paths
and the names of every database collected. Nothing is anonymised or hashed, and
there is no option to do so — the facts would be useless without the names
attached. Where that archive goes and how it gets there is your decision, made
under your organisation's rules. The tool does not transmit it anywhere; it
writes a file and stops.

## Can I verify the binary?

Partly, and it is worth being precise about which bucket each thing falls into.

**What you can do today.** Two things:

- **Read the source and build your own.** It is published at
  `github.com/rudi-bruchez/sql-auditor`, and nothing else here comes close to
  compiling from source you have read:

  ```
  git clone https://github.com/rudi-bruchez/sql-auditor
  cd sql-auditor
  go build ./cmd/sql-auditor
  ```

  A binary you built yourself answers the question completely. Everything below
  is about the case where you were handed one instead.

- **Check which questions the binary will ask.** Write the queries out of the
  binary you were handed and read them:

  ```
  sql-auditor queries export --to ./from-binary
  ```

  Whatever that binary collects, it collects with those files, and they are
  plain SQL. `MANIFEST.txt` also records the SHA-256 of the corpus each run
  used, so an archive can be tied to the questions behind it.

  Note what this does *not* establish. It tells you what the collector asks. It
  does not prove that a binary somebody handed you contains nothing else.

**What does not exist yet.** No release has been published, so there is no
releases page, no published SHA-256 to check a download against, and no build
provenance attestation. When the first version is released it will carry a
digest for every asset and an attestation tying each binary to the commit and
workflow that produced it. Until then, a binary you were handed cannot be
checked against anything except its own query corpus — so if that is not enough
assurance for your situation, build it yourself from the source above.

**And what none of it will ever give you** is a reproducible build. The source
being public does not mean you can compile it and get a byte-identical binary
to compare against, so the attestation, when it exists, will be a statement by
the build system about what it did rather than something you can independently
recompute.

## Three things that are not obvious from the outside

### The collector runs under `READ UNCOMMITTED`

Every query in the corpus begins with:

```sql
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
```

The collector will not block your production workload, and will not be blocked
by it. That is the point: a diagnostic tool that takes shared locks on catalog
views can end up queued behind a long transaction, or contribute to the
contention it was brought in to measure.

The cost of `READ UNCOMMITTED` is that a read can see uncommitted changes, or
miss rows that move during a scan. For what this tool reads — catalog metadata
and DMV counters, not business tables — that trade is worth making. Object
counts and file sizes are moving targets anyway, and a wait-statistics snapshot
is a sample of a live counter regardless of isolation level. Nobody is
reconciling these numbers to the penny. There is no case in this corpus where a
dirty read produces a wrong answer that a clean read would have got right; the
worst outcome is a value fractionally staler than the instant it was requested.

### Timeouts: 15 s to connect, and why raising the query timeout may not help

`SQL_CONNECT_TIMEOUT_SEC` defaults to 15 seconds and covers establishing the
connection, and nothing after it. Raise it if the instance is behind a slow link
or a failover cluster that takes its time answering.

`SQL_QUERY_TIMEOUT_SEC` defaults to 60 seconds. It bounds every round trip the
pipeline makes on its own account: the permission probes, the server
identification query, listing the databases, each `USE`, the session reset before
every collector, and the liveness ping after a failure. Nothing here should take
anywhere near a minute, so if this timeout is what expires, the instance is in
trouble rather than merely busy.

There is one thing it does **not** bound, and it is the one you probably came
here for. Each collector declares its own timeout in the query file, and a
declared timeout wins over `SQL_QUERY_TIMEOUT_SEC` outright. Every file in the
shipped corpus declares one, across five tiers:

| `@timeout` | Files |
| --- | --- |
| 30 s | 2 |
| 60 s | 19 |
| 120 s | 11 |
| 300 s | 5 |
| 1800 s | 1 |

So raising `SQL_QUERY_TIMEOUT_SEC` will not give a slow collector longer to
finish.

Two collectors are the candidates to run out of time first, for different
reasons.

`20.databases/020.properties.sql` has 300 seconds and is the schema-heavy one:
index fragmentation, largest objects, and missing and unused indexes are all
result sets inside that single file, and all of them walk every object in the
database. It runs once per database, so its 300 seconds is per database rather
than for the run. A database with tens of thousands of objects, or a heavily
fragmented one, can pass even that mark.

`70.schema/041.compression-savings.sql` has the corpus's longest timeout at 1800
seconds and is the only collector that samples real data —
`sp_estimate_data_compression_savings` copies rows into tempdb to measure them.
It runs only under `--estimate-compression`, and half an hour per database is
what it was given because the objects worth asking about are the large ones.
That is a collector to run deliberately, not one to leave on.

A collector that times out is recorded as an error, the run continues to the
next one, and the process exits `2`; the archive is written either way, missing
that one file. A partial archive with timeouts in the error list is the signal
to give that file longer.

To do that, take a copy of the corpus, edit its `@timeout` line, and run against
the copy:

```
sql-auditor queries export --to ./my-queries
# edit the "-- @timeout:" line in the file that timed out
sql-auditor collect --queries-dir ./my-queries
```

`MANIFEST.txt` records that the corpus came from a directory rather than from
the binary, along with its SHA-256, and withdraws its claim that the queries are
the published ones. A modified run cannot be mistaken for a standard one.

Both timeout settings must be positive whole numbers of seconds. A value the
tool cannot parse is an error rather than a silent fallback to the default.

```
SQL_CONNECT_TIMEOUT_SEC=30
SQL_QUERY_TIMEOUT_SEC=120
```

### `.env` overrides exported environment variables

Settings live in a `.env` file in the working directory. The repository ships
[`.env.example`](../.env.example) as a starting point: copy it to `.env` and
fill in `SQL_SERVER`. Every other key in it is already set to the value the tool
would use anyway, so copying it verbatim changes nothing else.

Working from the binary alone, `sql-auditor env init` writes the same template —
it is embedded in the executable — to `.env` in the current directory, or to
`--to FILE`. It refuses to write over a file that already exists unless you pass
`--force`.

Precedence is: **command-line flag, then `.env`, then the process environment,
then the built-in default.**

`.env` beating the exported environment is the reverse of what most tooling
does, and it is deliberate — it matches the PowerShell collector that this tool
replaces, and the configurations written for it. It is called out here because
it is the kind of surprise that costs an hour.

A worked example. Suppose your shell has:

```
export SQL_USER=svc_monitoring
```

and your `.env` contains:

```
SQL_SERVER=SQLPROD01,1433
SQL_USER=sqlauditor
SQL_PASSWORD=...
```

The run connects as **`sqlauditor`**, not `svc_monitoring`. The exported value
loses. You can confirm which login was used after the fact: `MANIFEST.txt`
records it.

```
Authenticated: sql:sqlauditor
```

Setting the key to an empty value in `.env` does **not** pin it to empty — an
empty value is treated as absent, so `SQL_USER=` in `.env` lets the exported
`svc_monitoring` through. To make the exported value win, either delete the line
or leave it empty; to override both, use the command line, which beats
everything:

```
sql-auditor collect --user svc_monitoring
```

#### The recognised keys

`SQL_SERVER`, `SQL_DATABASE`, `SQL_USER`, `SQL_PASSWORD`,
`SQL_INTEGRATED_SECURITY`, `SQL_ENCRYPT`, `SQL_TRUST_SERVER_CERTIFICATE`,
`SQL_CONNECT_TIMEOUT_SEC`, `SQL_QUERY_TIMEOUT_SEC`, `SQL_APPLICATION_NAME`,
`QUERIES_DIR`, `OUTPUT_DIR`, `DB_INCLUDE`, `DB_EXCLUDE`, `QUERY_STORE_DAYS`,
`QUERY_STORE_FROM`, `QUERY_STORE_TO`, `QUERY_STORE_TOP`,
`QUERY_STORE_DB_INCLUDE`.

Defaults are listed in the [README](../README.md#configuration). The set is
closed: any other key in `.env` stops the run.

```
$ sql-auditor check
unrecognised setting(s): SQL_PASWORD
```

#### `SQL_LOGIN` is now `SQL_USER`

If you are copying a configuration from the PowerShell predecessor, rename this
key. The tool refuses the old name rather than ignoring it:

```
$ sql-auditor check
SQL_LOGIN is no longer recognised; rename it to SQL_USER
```

The hard failure is the point. Ignoring `SQL_LOGIN` would leave `SQL_USER`
empty, which means Windows integrated authentication — so the run would either
fail with a confusing login error or, worse, succeed as the wrong identity.

#### What the `.env` parser accepts

One `KEY=VALUE` per line. Blank lines and lines starting with `#` are ignored. A
leading `export ` is accepted and stripped. Values may be wrapped in single or
double quotes, and anything after the closing quote is discarded. An unquoted
value can carry a trailing comment, which must be preceded by a space: `KEY=value # comment`.

It does not support escaped quotes inside a quoted value, values spanning more
than one line, or line continuations. A password containing an awkward character
is the usual reason to hit one of these — quote the whole value.

## `--include-session-text`

This is the first of the options that change what kind of data the archive
holds — the other five are `--query-store-detail`, `--query-store-plan-stats`,
`--include-object-definitions`, `--include-deadlock-graphs` and
`--include-blocked-process-reports`, below — and it is off by default for that
reason, not for performance. It costs one extra query.

With it on, one more collector runs: `10.system/052.session-text.sql`. It reads
the five longest-running snapshot transactions open against the instance at the
moment of collection, and for each one it records

- the verbatim SQL text of the batch that session is executing, and
- the session's login, host and program names.

The SQL text is the problem. `sys.dm_exec_sql_text` returns a live batch exactly
as it was sent, so a statement built by string concatenation arrives with its
literals intact — a customer email address in a `WHERE` clause, a name, an
account number, an entire `VALUES` list. Nothing filters it, because nothing
could: the collector cannot tell which literal in somebody else's SQL is
sensitive. The login and host names are attributable to individuals in their own
right.

The scope is narrow — five rows, and only sessions holding a snapshot
transaction open — but narrow is not the same as safe. One row is enough to
carry a customer's details out of your building. A session that opened its
transaction and then went idle has no batch running, so its text comes back
empty; that is luck, not a safeguard.

Turn it on when the question is why the tempdb version store is growing or why
it will not shrink, and you need to know which transaction is pinning it and
what that transaction is doing. Without the flag, `10.system/050.tempdb.sql`
still reports those same transactions, by session ID and age, with no statement
text and no names attached; that is usually enough to identify the culprit from
the server side. Leave the flag off for a routine inventory or configuration
review, where it adds nothing you need.

If you do turn it on, `MANIFEST.txt` says so. The disclosure section gains a line
naming the captured statement text, and the closing paragraph changes to the one
any of the three widening options produces:

```
Most of this is metadata about the estate rather than the data held in it,
but the captured statement text can carry values copied from application
tables, and the login names are attributable to people. Treat this archive
as potentially containing personal data and handle it on that basis.
```

Which form was used is a property of the archive, readable by whoever receives
it. Nobody has to take your word for which way the flag was set, and an archive
collected without it carries the narrower claim.

## `--query-store-detail` and `--query-store-plan-stats`

These are the other two options that change what kind of data the archive holds,
and they are the widest ones. Everything the collector reads without them is
metadata and counters. With them, the archive carries the untruncated SQL of
your heaviest production queries and the execution plans behind them.

They are two options rather than one because they are two decisions. The rest of
this section says what each one gathers, what a plan actually contains, how the
window is chosen, and where the collector records what it left out.

### What `--query-store-detail` collects

It adds `80.workload/021.query-store-detail.sql`, which runs once per database
on SQL Server 2016 and later, and reads the Query Store of that database over a
window you choose. For each database it retains, by default, fifty queries — the
`--query-store-top` cap — selected by a round robin over four rankings computed
across the window:

- total duration,
- total CPU time,
- total logical reads,
- execution count.

The round robin takes every metric's first place, then every metric's second
place, and so on, so a query that leads logical reads and sits eightieth on
everything else is retained on the first pass. Execution count is in the list
for the query that is invisible on the other three: four million calls at
0.3 ms rank nowhere on duration, CPU or reads, and a row-by-row loop is exactly
that shape. Queries with a forced plan are added on top of the cap, whether or
not they ran in the window, because someone took a decision about them — and
because a plan that can no longer be forced stops being applied without anything
raising an error.

No ranking is a verdict. Nothing is labelled a regression, no threshold is
applied, and the four ranks a query holds are written out raw beside it: rank 3
on CPU and rank 812 on logical reads is a fact about a query, and it is not the
collector's business to say what it means.

Instead of one JSON document, this collector writes a directory per database:

```
80.workload/
  AppDb/
    021.query-store-detail/
      _index.json
      query_1183.sql
      query_1183.stats.json
      query_1183.plan_2044.sqlplan
      query_1183.plan_2101.sqlplan
      query_4472.sql
      ...
```

`_index.json` is the table of contents and is always written, even when the
directory is otherwise empty: it carries the Query Store's state, the window
that was asked for beside the window that was actually available, the four ranks
of each query, and every omission. "The Query Store is off in this database" and
"the collector never ran here" are different facts, and an empty directory
states neither.

`--query-store-plan-stats` adds a second directory of the same shape,
`022.query-store-profiled/`, holding files named `.actual.sqlplan`.

### What a plan contains, and why it is a wider disclosure than session text

`query_<id>.sql` is the statement exactly as the Query Store recorded it, with
no truncation. That alone is more than `--include-session-text` gathers, which
is five live sessions; this is the top fifty queries of every database
collected, drawn from however much history the store holds.

The plan files are wider still. A Showplan is not a longer copy of the
statement. It carries:

- **the parameter values the plan was compiled for** — the literal argument that
  was passed the first time the plan was cached, recorded in the plan itself;
- **the literal predicates of the statement** — the constants in every seek,
  scan and filter, exactly as the application sent them;
- **the name of every object touched** — database, schema, table, view, index
  and column names, along with the statistics the optimiser consulted.

So an application that builds its SQL by concatenation puts a customer's email
address, an account number or a national identifier into a `WHERE` clause, and
that value reaches the archive twice: once in the statement text and once
compiled into the plan. Nothing filters it, because nothing could — the
collector cannot tell which literal in somebody else's SQL is sensitive.

This is why `80.workload/020.query-store.sql` and
`80.workload/023.query-store-most-executed.sql` need no flag and run on every
collection: they truncate the statement to 500 characters and collect no plan at
all. Enough to identify a query, not enough to reconstruct a payload. The moment
that trade stops being made, a flag appears.

Turn it on when the question is which queries are heavy and why, and you have
somewhere appropriate to put the answer. Leave it off for an inventory or a
configuration review, where the summary collectors already say which queries
dominate.

`MANIFEST.txt` says which of the two options were on, in the section a security
officer reads, so the archive discloses its own contents without anybody having
to remember how it was collected.

### Why `--query-store-plan-stats` is a second decision

The Query Store keeps the *estimated* plan: the shape the optimiser chose, with
the row counts it expected. `sys.dm_exec_query_plan_stats` returns the last plan
the engine still holds with the row counts it actually got, which is the
difference between "the optimiser expected 12 rows" and "it got 4 million" —
often the whole diagnosis.

The reason it has its own flag is scope, not volume. Finding that plan means
reading `sys.dm_exec_query_stats`, which is the plan cache of the **whole
instance** — every database on the server, including the ones you excluded from
the collection and the ones this login was never pointed at. The database filter
restricts what is **kept**, not what is **read**. Only plans belonging to the
databases being collected are written out, but the cache is scanned entire to
find them, and an operator who consented to a deep read of one database has not
thereby consented to that. So it is asked for separately, and
`--query-store-plan-stats` does nothing at all without `--query-store-detail`,
which is what produces the list of queries to look for.

**Getting nothing back is an ordinary outcome, not a fault.** Four conditions
must all hold:

- the instance is SQL Server 2019 or later — below that the collector is skipped
  with the reason recorded;
- `LAST_QUERY_PLAN_STATS` is on for the database, which is **off by default** and
  is a decision someone has to have taken, or trace flag 2451 is set on the
  instance, which turns it on globally;
- the plan is still resident in the cache, which a restart, memory pressure or a
  recompile ends at any moment;
- the cached plan matches a Query Store plan by `query_plan_hash`.

Any one of them being false gives you `matched_plans: 0`. The index reports
`last_query_plan_stats` beside that zero precisely so the commonest two
explanations can be told apart: a database with the feature switched off and a
plan cache holding nothing look identical otherwise. Read that field for what it
is — the value of the database scoped configuration and nothing more, read from
`sys.database_scoped_configurations`. An instance running trace flag 2451 shows
it off there while plans arrive anyway. Off with plans beside it means the trace
flag is doing the work; off with no plans means either explanation.

The match is made on `query_plan_hash`, which is an MD5, so it is declared and
never asserted: each row records `"match": "plan_hash"` and a `candidates`
count of how many cached plans shared that hash. A `candidates` of 4 is not a
red flag by itself — a bare recompile adds another — but a reader told nothing
would see a certainty. Prepared statements come back from the DMF with a NULL
database id and are dropped, which is one of the reasons the profiled directory
usually holds fewer plans than the detail directory.

**A profiled plan showing a single operator is not a broken collection.** For a
query simple enough — which is to say, most of an OLTP workload — the DMF
returns a Showplan reduced to its root node. Nothing in the file distinguishes
it from any other profiled plan, because it *is* one. If you open a
one-operator `.actual.sqlplan` and conclude the extraction is faulty, that is
the documentation's failure and not the file's.

### Choosing the window

Two forms, and they are mutually exclusive — asking for both stops the run
rather than silently picking one. The refusal is decided on `QUERY_STORE_DAYS`
being *present*, not on its value, so a `.env` carrying `QUERY_STORE_DAYS=7`
makes every run that passes a bound fail with `QUERY_STORE_DAYS cannot be
combined with QUERY_STORE_FROM or QUERY_STORE_TO`. Delete or comment the line;
absent already means seven days.

**Sliding**, which is the default: `--query-store-days N`, seven days counting
back from the moment of collection. This is the right form for "what does this
instance normally do".

**Absolute**: `--query-store-from` and `--query-store-to`, written as
`YYYY-MM-DDTHH:MM` or `YYYY-MM-DD`, and read in the **server's** local time —
not your laptop's, not UTC. The operator types what the client said. Giving
`--query-store-to` on its own implies a seven-day window ending at that bound;
giving `--query-store-from` on its own runs from there to the moment of
collection.

The absolute form exists for the question the sliding one cannot answer. A
client reports that everything was unusable between 14:00 and 15:00 on the 26th,
eighteen days ago. Widening the sliding window to eighteen days does not find
that hour — it dissolves it, because an hour of trouble inside 432 hours of
normal operation moves an average by nothing at all. You have to ask for the
hour:

```
sql-auditor collect --query-store-detail \
  --query-store-from 2026-07-26T14:00 --query-store-to 2026-07-26T15:00
```

Three things to know about the answer.

**It is rounded to the Query Store's interval length.** The store aggregates into
buckets — 60 minutes by default — and the collector keeps every bucket that
*overlaps* the request rather than every bucket contained in it, so a one-hour
question is answered by one or two hourly buckets. Demanding containment would
silently drop the bucket straddling the start, which is usually the one you
wanted. `_index.json` reports `window.interval_minutes` so you know the
precision of what you got.

**The window you asked for and the window you got are both recorded.** A store
that retains two days cannot answer a question about the 26th, and it does not
say so on its own: it returns what it has, which looks exactly like a quiet hour.
So `_index.json` carries `window.requested_from`/`requested_to` beside
`window.effective_from`/`effective_to`, clamped to what the store still holds,
and `window.intervals` — the number of buckets that actually intersected the
request. Zero there, on a healthy store, means the window fell outside the
retention. That is the number to read before concluding anything about a quiet
hour.

**A bound in the future is refused**, and so is a window whose start is not
before its end, both before anything is collected. A bound in the future is
almost always a bound typed in the wrong timezone, and the Query Store has
nothing to say about it either way.

`--query-store-databases` narrows which of the collected databases the
extraction reads, with the same `*`/`?` wildcards as `DB_INCLUDE`. It only
narrows: a database excluded from the run cannot be brought back by naming it
here. Each database it removes is recorded by name in the manifest, so a
selection can be reconstructed after the fact.

### The caps, and where an omission is recorded

Two caps apply, and neither ever truncates: a truncation nobody is told about
reads as "everything is here".

**8 MiB per plan.** A plan larger than that is not sent and not written; the
index records an omission naming the query, the plan and the plan's actual size,
and a warning with the same content goes into `MANIFEST.txt`. The size is
measured with `DATALENGTH`, which counts bytes, and the plan XML is `nvarchar`
— two bytes per character — so the effective ceiling is about four million
characters of XML. That is a very large plan; a query with a few hundred
operators is nowhere near it.

**256 MiB for the whole run.** When the run reaches it, further files are
refused with the omission recorded as `the run reached the 256 MiB extraction
cap`. `_index.json` itself is written outside that budget, so the record of what
was left out cannot be the thing that gets left out.

Everything else the collector could not gather is recorded the same way: a query
whose text the Query Store no longer holds, a plan the store has no XML for, a
query with no profiled plan in the cache. Each is a line in the index with a
reason, and a warning in `MANIFEST.txt` for a reader who never opens the JSON.

### Both Query Store states are read

Every state except `OFF` is read: `READ_WRITE`, `READ_ONLY`, `ERROR` and
`READ_CAPTURE_SECONDARY` alike. A store that stopped recording still holds the
history from before it stopped, and that history exists nowhere else on the
instance — not in the plan cache, which a restart empties, and not in any DMV,
which knows only about now. A store found `READ_ONLY` is often exactly the
interesting case: it filled up and stopped, and what it stopped on is what you
came for. The state, the desired state and `readonly_reason` are reported raw in
the index, so a reader learns not only that it stopped but why.

A database whose Query Store is `OFF` still gets its `_index.json`, saying so.

### It needs no permission the rest of the corpus does not already ask for

`VIEW SERVER STATE` at the server implies `VIEW DATABASE STATE` in every
database, which is what the Query Store catalog views require, and
`--grant-script` already emits it — including the narrower `VIEW SERVER
PERFORMANCE STATE` on SQL Server 2022 and later. Nothing about these two options
adds a `GRANT` to that script. If `check` came back clean, the extraction has
what it needs.

### What it costs the instance

Little. The Query Store is a set of catalog views over data already on disk, and
the collector reads them under `READ UNCOMMITTED` like every other file in the
corpus, so it takes no lock your workload has to wait behind. The detail
collector declares a 300-second timeout per database and the profiled lookup
120 seconds; the work is the aggregation over the window, which grows with the
number of intervals the window spans rather than with the size of the database.

The one thing that is not free is the archive. Fifty queries per database, each
with its text, its statistics and one file per plan, is a larger archive than a
metadata-only run — and the plans are what makes it a document to handle
carefully rather than a big one.

## `--include-object-definitions`

The fourth option that changes what the archive holds, and the only one whose
content nobody's query ever produced: this is source code written on your side.

With it on, one more collector runs per database:
`70.schema/080.modules.sql`. It reads `sys.sql_modules` and writes one `.sql`
file per view, stored procedure, function and trigger, under
`70.schema/<database>/080.modules/`, with an `_index.json` listing every module
— including the ones whose source is not there.

Why it is off by default. A module body is the one artifact in this archive that
was authored rather than measured. In practice it carries:

- the names and addresses of linked servers, in every `OPENQUERY` and
  four-part name;
- values embedded as literals — thresholds, account numbers, email addresses in
  a `WHERE` clause;
- occasionally a credential in clear, in an `OPENROWSET` connection string or
  behind an `EXECUTE AS`. This is not hypothetical, and it is likeliest in the
  procedure nobody has opened in a decade.

Nothing filters any of it, for the same reason nothing filters session text: the
collector cannot tell which literal in somebody else's code is sensitive.

**Four things can leave a definition out, and the index says which.** They are
kept apart deliberately, because reading one as another would make the archive
state something false about your server:

| In `_index.json` | What happened |
| --- | --- |
| `encrypted (WITH ENCRYPTION)` | the server returns no definition and never could |
| `beyond the per-database cap` | more than 2000 modules; the most recently modified were kept |
| `above the per-module byte cap` | the definition exceeds 1 MiB |
| `the catalog holds no definition` | nothing was there to read |

Every module is listed either way, with its type, its size and its dates. A
module the archive cannot show you is still a module the archive tells you
exists.

**What it costs the instance.** Almost nothing: `sys.sql_modules` is a catalog
view and the read is metadata only. The cost is in the archive — a database of
2000 procedures is a few tens of megabytes of text before compression, and it is
a document to handle carefully rather than a large one.

## `--include-deadlock-graphs`

The narrowest of the options that change what the archive holds, and the one
with the clearest line: `10.system/060.system-health.sql` already reports, on
every run and without any flag, how many deadlocks the `system_health` ring
buffer holds and when each happened. It stops exactly there, because the report
itself carries the verbatim SQL of both victims. This option collects the
reports.

With it on, one more collector runs: `10.system/061.deadlock-graphs.sql`. It
writes one `.xdl` file per deadlock under `10.system/061.deadlock-graphs/`, plus
an `_index.json`. SSMS opens an `.xdl` as the deadlock diagram rather than as
XML; the bytes are the same either way.

**Why the graph is worth having.** Wait statistics say sessions waited on locks.
The counts say how many deadlocks and when. Neither says which two statements,
on which resource, in which order, or which one the engine chose as victim — and
that is the difference between an audit that reports "lock contention" and one
that names the pattern. It is in the graph and nowhere else within reach.

**What it reads, and what it does not touch.** The `system_health` session has
run by default since SQL Server 2008 and writes to a memory ring of bounded
size. Reading it costs one query. Nothing is modified, cleared or consumed:
collecting a graph does not remove it from the ring.

**What the ring does not owe you.** It is a window, not an archive. Old events
are overwritten and a restart empties it entirely, so what you get is whatever
it still held at the moment of collection. `_index.json` carries the earliest
and latest timestamps for exactly that reason — "412 deadlocks" means one thing
over two days and another over twenty minutes, and "no deadlocks" can mean a
healthy instance or one restarted an hour ago.

**Three things can leave a graph out, and the index says which:**

| In `_index.json` | What happened |
| --- | --- |
| `beyond the cap of 100 graphs` | more than 100 deadlocks in the ring; the most recent were kept |
| `above the … byte cap` | that one report exceeds 1 MiB |
| `the ring buffer holds no graph for this event` | the deadlock was recorded and its report was not |

Every deadlock is listed either way, with its timestamp. A cluster of forty in
one minute is visible even when only the newest hundred carry a file.

## Authentication

**SQL authentication** works everywhere. Set `SQL_USER` and `SQL_PASSWORD` in
`.env`.

**Windows integrated authentication** works on Windows. Leave `SQL_USER` empty,
or set `SQL_INTEGRATED_SECURITY=true`, and the run uses the account the process
is running as. Both of these are first-class: they are what the tool is built
around and what it is tested with.

**Kerberos on Linux is best-effort.** It is not a third supported mode of equal
standing. The driver can use a Kerberos ticket, but it needs a working `krb5`
configuration on the machine — a correct `/etc/krb5.conf`, a resolvable realm, a
valid ticket in the cache (`kinit`, then `klist` to confirm), and the SQL Server
SPN registered correctly. When any of that is wrong, the failure surfaces as a
generic login error that says nothing about Kerberos. If you are running from
Linux and do not already have a working Kerberos setup for other tools, use SQL
authentication. It will take less of your afternoon.

Note that `MANIFEST.txt` reports the authentication mode as either `sql:<login>`
or `windows`. A Kerberos run is recorded as `windows`, since from the
configuration's point of view it is the same integrated path.

#### Supplying the password without a `.env`

`--password-file FILE` and `--password-stdin` read `SQL_PASSWORD` from somewhere
other than the `.env`, for a scheduled run or a pipeline that has no business
leaving a credential on disk. They are mutually exclusive, and both read the
value the same way: exactly one trailing `\n` or `\r\n` is removed, and nothing
else is. A trailing space, a `#`, a quote — all part of the password. This is
deliberately stricter than the `.env` parser, which strips a trailing comment
from an unquoted value: here there is no line to comment, so there is nothing to
strip and no ambiguity to resolve.

An empty file, or an empty standard input, stops the run. It is not read as "no
password": with `SQL_USER` set and `SQL_PASSWORD` empty the tool would try
integrated authentication and measure whichever identity the scheduler runs as,
which is the failure the closed key set exists to prevent elsewhere.

There is no `--password`. See the README for why the argument form is the one
thing these two options are not.

### TLS

The connection is encrypted by default (`SQL_ENCRYPT=true`), but the server
certificate is not validated (`SQL_TRUST_SERVER_CERTIFICATE=true`), which is the
only default that works against the self-signed certificate most instances ship
with. The tool prints a notice on every run to make sure this is not a silent
assumption:

```
note: the connection is encrypted but the server certificate is NOT validated (SQL_TRUST_SERVER_CERTIFICATE=true)
```

If your instance has a certificate your machine trusts, set
`SQL_TRUST_SERVER_CERTIFICATE=false` and the notice goes away.

## Reproducing a run locally

To try the tool against a throwaway instance rather than yours, run SQL Server in
a container. The repository has a `compose.yaml`, but `podman compose` needs a
provider that is not installed on every machine, so this one-liner is the
reliable form:

```
podman run -d --name sqlauditor-test \
  -e ACCEPT_EULA=Y -e MSSQL_SA_PASSWORD='Str0ng!Passw0rd' \
  -p 11433:1433 \
  mcr.microsoft.com/mssql/server:2022-latest
```

Substitute `docker run` for `podman run` if that is what you have; the arguments
are identical.

`mcr.microsoft.com/mssql/server:2022-latest` is the tag pinned in the
repository's `compose.yaml`. Port 11433 on the host keeps it clear of a local
SQL Server on 1433. The collector has also been run repeatedly against SQL
Server 2017 (14.0.1000) and once against 2016 SP3 (13.0.6435); nothing older
has been exercised yet.

Then point a `.env` at it:

```
SQL_SERVER=localhost,11433
SQL_USER=sa
SQL_PASSWORD=Str0ng!Passw0rd
```

```
sql-auditor check
sql-auditor collect
```

A fresh container has no user databases, so the per-database collectors have
nothing to run against and `check` reports zero databases. Create one to see the
full corpus run — the image ships `sqlcmd`, so no client is needed on the host:

```
podman exec sqlauditor-test /opt/mssql-tools18/bin/sqlcmd -S localhost -U sa -P 'Str0ng!Passw0rd' -C -Q 'CREATE DATABASE AppDb;'
```

When you are finished:

```
podman rm -f sqlauditor-test
```
