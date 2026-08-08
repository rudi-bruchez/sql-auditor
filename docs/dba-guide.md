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
  - [Timeouts](#timeouts-15-s-to-connect-60-s-per-query)
  - [`.env` precedence](#env-overrides-exported-environment-variables)
- [`--include-session-text`](#--include-session-text)
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

The corpus is 14 files. The archive records the SHA-256 of the exact corpus that
was used, so a run can be tied to the questions it asked.

It collects; it does not judge. There are no thresholds and no recommendations
in the tool or in its output. It gathers facts and records what it could not
gather.

## What permissions it needs

The login must be able to connect. Beyond that there are three rights the
collector uses, and none of the three is required: it probes each one before it
starts and carries on without whatever it was refused, recording the omission.
This is what each one costs when it is missing, in the collector's own words —
the wording below is the same string the tool prints and writes into the archive.

| Capability | Grant | If it is missing |
| --- | --- | --- |
| Connect to the instance | `CONNECT SQL` | nothing can run |
| Read server and database metadata | `VIEW ANY DEFINITION` (server level) | instance configuration and database file layout not collected |
| Read performance counters | `VIEW SERVER STATE` | wait statistics, schedulers, memory and tempdb usage not collected |
| Read backup history from msdb | `SELECT` on `msdb.dbo.backupset` | backup history not collected — the report must not read this as 'no backups exist' |

A read-only login with all three, verified against SQL Server 2022:

```sql
CREATE LOGIN sqlauditor WITH PASSWORD = '...';
GRANT VIEW ANY DEFINITION TO sqlauditor;
GRANT VIEW SERVER STATE  TO sqlauditor;
USE msdb;
CREATE USER sqlauditor FOR LOGIN sqlauditor;
ALTER ROLE db_datareader ADD MEMBER sqlauditor;
```

Per-database collectors additionally need the login to be able to connect to the
database. A database the login cannot reach is skipped with the reason
`no access for this login`, and named in the manifest.

One caution about `VIEW ANY DEFINITION`. SQL Server does not raise an error when
it is missing. Metadata visibility filters catalog views row by row, so a query
against `sys.databases` still succeeds and simply returns fewer rows — only the
databases the login owns or is mapped to. Such a login can produce an archive
that lists no user databases at all while every query reports success. The
collector detects this case specifically and says so in the archive, but it is
worth granting the right so the picture is complete.

## Run `check` first

`check` connects, probes each permission, prints the query list and the exact set
of databases a collection would touch, then exits without collecting anything.
Run it before you run `collect`.

```
sql-auditor check
```

On an instance where everything is available:

```
Queries (14):
  10.system/010.properties.sql
  10.system/012.soft-numa.sql                SQL Server 13+
  10.system/013.memory-model.sql             SQL Server 13.0.4001+
  10.system/014.cpu-topology.sql             SQL Server 13.0.5026+
  10.system/050.tempdb.sql
  10.system/051.version-store.sql            SQL Server 13.0.5026+
  10.system/052.session-text.sql             --include-session-text (off)
  20.databases/010.all-databases.sql
  20.databases/011.all-databases-2014.sql    SQL Server 12+
  20.databases/012.all-databases-query-store.sql SQL Server 13+
  20.databases/020.properties.sql            per database
  20.databases/021.properties-2014.sql       per database, SQL Server 12+
  20.databases/022.query-store.sql           per database, SQL Server 13+
  20.databases/023.log-vlf.sql               per database, SQL Server 13.0.5026+

Output   : output

Permissions:
  ok      connect
  ok      view_any_definition
  ok      view_server_state
  ok      msdb_read

Server   : SQLPROD01  16.0.4265.3  Developer Edition (64-bit)

Databases that would be collected (1):
  - AppDb -> AppDb/
```

The annotations on the right are the conditions attached to a query: `per
database` means it runs once for each selected database, `SQL Server 13+` means
it is skipped on older instances, and the flag name means it is opt-in.

### A `denied` line is a warning, not a failure

On a login without `VIEW ANY DEFINITION` and `VIEW SERVER STATE`:

```
Permissions:
  ok      connect
  denied  view_any_definition — instance configuration and database file layout not collected
  denied  view_server_state — wait statistics, schedulers, memory and tempdb usage not collected
  ok      msdb_read

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

The exit codes from `check` are:

| Code | Meaning |
| --- | --- |
| `0` | usable, possibly degraded |
| `1` | the instance did not answer — nothing can be collected |
| `2` | the configuration is unusable: the query corpus fails its lint, or the output directory cannot be written |

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
      012.soft-numa.json
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
  SQLPROD01-2026-08-08.zip
```

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

Passwords and connection secrets are masked before being written.

That is metadata about the estate rather than the data held in it, but the
login names above are attributable to people, so treat this as internal
infrastructure documentation rather than public material.
```

That paragraph is generated from what the run actually did, not fixed at compile
time. Collect with `--include-session-text` and it gains a line disclosing the
captured statement text, and the closing paragraph changes to say the archive
should be treated as potentially containing personal data.

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

Partly, and it is worth being precise about how far it goes.

- **The source is public.** Every query is in the repository and can also be
  written out of the binary you were given, with `sql-auditor queries export`.
  Comparing the two tells you the binary is running the published questions.
- **Releases publish a SHA-256** of every asset, so you can confirm the file you
  downloaded is the file that was published.
- **Each released binary carries a build provenance attestation**, tying it to
  the source commit and the workflow run that built it.

What none of that gives you is a reproducible build. You cannot compile the
source yourself and get a byte-identical binary to compare against, so the
attestation is a statement by the build system about what it did, not something
you can independently recompute. If your standard is "I built it myself", build
from source and run your own binary: `go build ./cmd/sql-auditor`.

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

### Timeouts: 15 s to connect, 60 s per query

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
shipped corpus declares one: 60 seconds, except the per-database properties
collector at 300. So raising `SQL_QUERY_TIMEOUT_SEC` will not give a slow
collector longer to finish.

The collectors that will exceed 60 seconds first are the schema-heavy ones —
index fragmentation, largest objects, missing and unused indexes — which walk
every object in the database. A database with tens of thousands of objects can
pass that mark. A collector that times out is recorded as an error, the run
continues to the next one, and the process exits `2`; the archive is written
either way, missing that one file. A partial archive with timeouts in the error
list is the signal to give those collectors longer.

To do that, take a copy of the corpus, edit the `@timeout` line of the file
concerned, and run against the copy:

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

To make the exported value win, either remove the key from `.env` or override it
on the command line, which beats both:

```
sql-auditor collect --user svc_monitoring
```

#### The recognised keys

`SQL_SERVER`, `SQL_DATABASE`, `SQL_USER`, `SQL_PASSWORD`,
`SQL_INTEGRATED_SECURITY`, `SQL_ENCRYPT`, `SQL_TRUST_SERVER_CERTIFICATE`,
`SQL_CONNECT_TIMEOUT_SEC`, `SQL_QUERY_TIMEOUT_SEC`, `SQL_APPLICATION_NAME`,
`QUERIES_DIR`, `OUTPUT_DIR`, `DB_INCLUDE`, `DB_EXCLUDE`.

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

This is the one option that changes what kind of data the archive holds, and it
is off by default for that reason, not for performance. It costs one extra query.

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
carry a customer's details out of your building.

Turn it on when the question is why the tempdb version store is growing or why
it will not shrink, and you need to know which transaction is pinning it and
what that transaction is doing. Without the flag, `10.system/050.tempdb.sql`
still reports those same transactions, by session ID and age, with no statement
text and no names attached; that is usually enough to identify the culprit from
the server side. Leave the flag off for a routine inventory or configuration
review, where it adds nothing you need.

If you do turn it on, `MANIFEST.txt` says so. The disclosure section gains a line
naming the captured statement text, and the closing paragraph changes to:

```
Most of this is metadata about the estate rather than the data held in it,
but the captured statement text can carry values copied from application
tables, and the login names are attributable to people. Treat this archive
as potentially containing personal data and handle it on that basis.
```

Which form was used is a property of the archive, readable by whoever receives
it. Nobody has to take your word for which way the flag was set, and an archive
collected without it carries the narrower claim.

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

### TLS

The connection is encrypted by default (`SQL_ENCRYPT=true`), but the server
certificate is not validated (`SQL_TRUST_SERVER_CERTIFICATE=true`), which is the
only default that works against the self-signed certificate most instances ship
with. The tool prints a notice on every run to make sure this is not a silent
assumption:

```
note: the connection is encrypted but the server certificate is NOT validated
(SQL_TRUST_SERVER_CERTIFICATE=true)
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

`mcr.microsoft.com/mssql/server:2022-latest` is the tag pinned in `compose.yaml`
and the image CI runs its integration job against, so a failure you see locally
against this image is the one CI would see. Port 11433 on the host keeps it
clear of a local SQL Server on 1433.

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
full corpus run:

```sql
CREATE DATABASE AppDb;
```

When you are finished:

```
podman rm -f sqlauditor-test
```
