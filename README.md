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

What is not there yet is everything about a *downloaded* binary. Releases are
meant to carry a SHA-256 for every asset and a build provenance attestation
tying each binary to the commit and workflow that produced it; until a release
exists, a binary somebody handed you cannot be checked against anything except
its own query corpus — and no amount of tooling will ever give you a
byte-identical rebuild to compare against. What can and cannot be verified is
set out in full in
[docs/dba-guide.md](docs/dba-guide.md#can-i-verify-the-binary).

## Commands

```
sql-auditor check                    verify connectivity, permissions and configuration,
                                     and list what a collection would run
sql-auditor collect                  collect, then archive
sql-auditor queries export --to DIR  write the embedded queries to disk
sql-auditor version
```

Options for `check` and `collect`:

| Flag | Meaning |
| --- | --- |
| `--server HOST[,PORT]` | overrides `SQL_SERVER` |
| `--user NAME` | overrides `SQL_USER` |
| `--env PATH` | `.env` file to read (default `.env`) |
| `--queries-dir DIR` | run a corpus from disk instead of the embedded one |
| `--output-dir DIR` | where to write results |
| `--keep` | keep an existing same-day run folder, suffixing this run |
| `--grant-script FILE` | `check` only. Write the T-SQL that grants the permissions found missing, for the login the server reports, with the reason for each. Never executed. |
| `--include-session-text` | also collect the SQL text, and the login, host and program names, of the five longest-running snapshot transactions |
| `--estimate-compression` | also estimate page-compression savings on the largest uncompressed objects. Off for cost, not for disclosure: it samples real data into tempdb and is slow on large tables |
| `--query-store-detail` | also collect the full text and the execution plans of the heaviest Query Store queries, per database |
| `--query-store-plan-stats` | also look for the last profiled plan of each query the option above extracted. Does nothing on its own |
| `--query-store-days N` | how many days of Query Store history to read, counting back from now (default 7). Not combinable with the two bounds below |
| `--query-store-from T` | start of the window, `YYYY-MM-DDTHH:MM` or `YYYY-MM-DD`, in the **server's** local time |
| `--query-store-to T` | end of the window, same format and same clock (default: the moment of collection). Given on its own it implies a seven-day window ending at that bound |
| `--query-store-top N` | how many queries to extract per database, across the four rankings once deduplicated (default 50). Queries with a forced plan are added on top of this |
| `--query-store-databases P` | comma-separated `*`/`?` patterns narrowing which of the collected databases the Query Store extraction reads. It narrows the selection; it never widens it |

There is no `--password` flag. A password on the command line ends up in `ps`
output and in shell history; put it in `.env` instead.

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
[`.env.example`](.env.example) to `.env` and fill in `SQL_SERVER`; every other
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
| `SQL_APPLICATION_NAME` | `sql-auditor` | application name shown in `sys.dm_exec_sessions` |
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
`SQL_USER`; the old name is refused by name rather than ignored.

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
