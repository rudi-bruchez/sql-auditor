# Adversarial harm review, 12 September 2026

What this review asked, in this order:

1. What can this software do to a production system, a database, or the machine
   it runs on?
2. What vulnerabilities does the code carry?

The first matters more and is harder. Scanners answer the second, and their
output is input to the review rather than its findings.

**Every finding below has been fixed.** The fixes are named with the line they
landed on, and each one has a test that fails if it comes back. This document
is published because the holes are closed; it would have been irresponsible
while they were open, since three of them were working bypasses of a guard in a
tool other people point at production instances.

## What this document does and does not claim

It does not say this tool is safe. No document can say that, and a document
that tried would be the same failure this review exists to find: the papers
reassuring a reader more than the code justifies.

What it says is narrower and checkable. On the default path — the corpus
compiled into the binary — nothing found here endangers an instance. On the
documented `--queries-dir` path, five ways to harm a server got past the
statement lint, all five are now refused, and the table that refuses them is
part of the test suite. The lint has now been walked past by two separate
reviews, six weeks apart, and the honest conclusion is not that it is finished
but that it is the part of this program most worth attacking next.

## The threat model

Someone downloads a release, runs `sql-auditor env init`, edits the connection
string, and points it at production having read `README.md` and
`docs/dba-guide.md` but not the source. They will not read every configuration
key.

For findings 1 to 3, one further step, which the guard's own comment names as
the reason it exists: they ran `sql-auditor queries export`, dropped one of
their own maintenance scripts into that directory, and pointed
`--queries-dir` at it.

## 1. CATASTROPHIC — `sp_msforeachdb` carried any statement past the lint

`statementLint` refuses a destructive statement inside a literal handed to
`EXEC(...)` or `sp_executesql`, because `executedLiterals` recognises those two
vehicles and recurses into what they run. It did not recognise `sp_msforeachdb`,
`sp_msforeachtable` or `sp_MSforeach_worker`, which execute their literal
argument against every database — or every table — on the instance. The payload
was blanked as an ordinary string by `BlankSQLStrings`, never descended into,
and no rule in the file ever saw it.

Verified by running the real `statementLint`:

```
accepted  EXEC sp_msforeachdb 'DROP TABLE dbo.Orders'
accepted  EXEC sp_msforeachdb 'ALTER DATABASE [?] SET OFFLINE WITH ROLLBACK IMMEDIATE'
accepted  EXEC sp_msforeachtable 'TRUNCATE TABLE ?'
accepted  EXEC sp_MSforeach_worker 'DROP TABLE dbo.Orders'
refused   EXEC sp_executesql N'DROP TABLE dbo.Orders'
refused   EXEC('DROP TABLE dbo.Orders')
```

The second line takes every database on the instance offline and rolls back
every in-flight transaction. That the identical payload was refused through
`EXEC(...)` is what made this a gap rather than a stated limit.

**Fixed** at `collect/statementlint.go:97`: the three names are in
`forbiddenProcedure`. Refusing them outright is smaller and safer than teaching
`executedLiterals` a third vehicle — they are undocumented, and no collector
that reads has a reason to call one.

## 2. SEVERE — `DBCC SQLPERF(…, CLEAR)` erased the instance's wait statistics

`SQLPERF` is on the read-only DBCC allowlist because `DBCC SQLPERF(LOGSPACE)`
is a genuine read, and that allowlist is on the command word by design. It is
the one allowlisted command that also takes a destructive argument:
`DBCC SQLPERF('sys.dm_os_wait_stats', CLEAR)` resets the instance's accumulated
wait statistics, and the latch-stats form does the same. Accepted in all three
spellings tried, including `N''` and `WITH NO_INFOMSGS`.

Wait statistics are cumulative since the last restart and there is no undo.
They are the baseline a performance audit argues from, and the client's own
monitoring may be trending them. A tool whose purpose is to collect that
evidence would have been destroying it on the run meant to gather it, and
nothing in the archive would have said so.

**Fixed** at `collect/statementlint.go:108` and checked in `scanStatements`
before the command-word allowlist: `DBCC SQLPERF` with `CLEAR` anywhere in its
argument list is refused, and `DBCC SQLPERF(LOGSPACE)` still passes. `CLEAR`
sits outside the string literal, so it survives `BlankSQLStrings` and is
visible to the rule.

## 3. SEVERE — five writing statements carried no keyword any rule looked for

The rules key on statement keywords and on a named list of procedures. These
matched neither, and all of them change the server:

| Statement | What it does to a production instance |
|---|---|
| `EXEC sp_updatestats` | rewrites statistics for every table in the database; hours on a large one, takes locks, and changes plan choices for the whole workload |
| `EXEC sp_recompile 'dbo.T'` | takes a schema-modification lock on a live object and invalidates every plan referencing it |
| `EXEC sp_cycle_errorlog` | rotates the error log; with the default archive count the oldest log, which is diagnostic evidence, is discarded |
| `EXEC sp_trace_setstatus 2, 0` | stops another party's running trace |
| `CHECKPOINT` | forces a checkpoint, writing dirty pages |

`sp_updatestats` opens a great many maintenance scripts.

**Fixed**: the four procedures at `collect/statementlint.go:97`, `CHECKPOINT`
in `forbiddenOutright` at `collect/statementlint.go:63`. Word boundaries keep
`CHECKPOINT` off the corpus's own identifiers — `log_checkpoint_lsn` and
`since_last_checkpoint_mb` have no word boundary before `checkpoint`, and the
test asserts that a statement selecting them is still accepted.

The general shape of the problem is not fixed and is not claimed to be: a
blocklist of what is forbidden will always trail an allowlist of what is
permitted. `collect/statementlint.go` says so itself. What replaced trust in
the patterns is the table in the next section.

## The adversarial table

`collect/statementlint_adversarial_test.go` holds every statement above plus
the ones earlier reviews found, as a table that must stay refused, and eight
shapes the corpus actually needs, as a table that must stay accepted. A lint
that refuses everything protects nothing, so both halves matter.

This exists because the fix each time was a better regular expression, and each
time the expressions looked complete before the review. The protection that
outlives the next set of patterns is the table, not the patterns.

## 4. MODERATE — CI ran actions at mutable tags while the release pinned by SHA

`.github/workflows/ci.yml` used `actions/checkout@v7` and
`actions/setup-go@v7`. A tag can be silently repointed by its owner, which is
the shape of the `trivy-action` and `kics-github-action` compromises; attacker
code would then run on every push, in a job with the repository checked out.

What made it a finding rather than a nit: `release.yml` pins every action to a
full 40-character SHA. The decision had been taken deliberately for the
workflow that publishes binaries and not carried to the one that runs on every
push, and an inconsistent control is the kind that gets assumed to be in force.

**Fixed**: all four uses pinned to the same SHAs `release.yml` already
carries, in its style, with the version in a trailing comment.

Found by `semgrep --config auto`
(`yaml.github-actions.security.github-actions-mutable-action-tag`), verified by
reading both workflows.

## 5. MINOR — the console hold could spin forever

`holdConsole` waited for a newline with a hand-rolled loop over `Read`.
`io.Reader` is permitted to return `(0, nil)`, and that loop had no bound on it:
it would burn a core, invisibly, in a window the operator cannot close. On the
path this function serves — a double-clicked binary on a client's server — that
is the worst place for an invisible busy loop.

This was not reproduced: reading a Windows console handle blocks rather than
returning zero, and no trigger was constructed. It is recorded because the fix
is smaller than the argument.

**Fixed** at `cmd/sql-auditor/main.go:862`: `bufio.Reader.ReadString`, whose
`fill` gives up after a hundred consecutive empty reads and returns
`io.ErrNoProgress`. The bound was already in the standard library.

## What this software gets right

Stated because it calibrates the rest, and because two of these are the
findings a harm review usually comes back with.

- **The destructive default is gone, and the fix is the right one.**
  `prepareRunFolder` renames the previous run aside instead of deleting it, and
  `discardSuperseded` runs at exactly one place — `collect/collect.go:1699`,
  after `Zip` has returned successfully. A rerun that dies anywhere earlier
  leaves the operator with the earlier archive. The comment records that this
  used to be `RemoveAll` before the first query ran.
- **No timeout can be disabled.** `secOf` refuses a value of zero or less, and
  every script query goes through `context.WithTimeout`.
- **The grant script's comment-injection hole is closed, and completely.** A
  SQL Server identifier may contain a newline, and the generated script tells
  the reader to run it as sysadmin. `commentSafe`, `quoteIdent` and
  `quoteLiteral` (`collect/grants.go:113-149`) neutralise it, the attack is
  documented at the call site, and all four comment-writing sites plus the
  header pass through it — checked individually.
- **The manifest withdraws its own attestation when it cannot vouch.**
  `collect/manifest.go:635` emits a different paragraph for a corpus supplied
  with `--queries-dir`, says in terms that the lint "is not a sandbox", and
  records the corpus SHA-256 for the reader to check. Most tools would print
  the reassuring paragraph unconditionally.
- **The comment-stripping the lint requires is load-bearing and correct.** Two
  shipped collectors discuss `DBCC SQLPERF(… CLEAR)` and `DBCC TRACEON` in
  their header comments, explaining what those commands do and why the
  collector does not use them. Comments are stripped before the lint runs, so a
  file explaining what it does not do is not refused for saying so.
- **The permission script asks for the narrower right where one exists**,
  preferring `VIEW SERVER PERFORMANCE STATE` on SQL Server 2022 and later and
  explaining what the wider one would have added.
- `govulncheck`: no reachable vulnerability, in a tree with eighteen in
  required modules.

## What was rejected, and why

A report padded with scanner output a maintainer has to disprove is worse than
no report, so every hit was opened.

- **`gosec` G703 ×3 (HIGH, path traversal via taint analysis)** — the
  `--password-file`, `--env` and `env init --to` paths. A command-line tool
  opening the path its operator named is its job, and there is no
  lower-privileged source for the taint.
- **`gosec` G304 ×9 (MEDIUM, file inclusion via variable)** — the same class:
  the `.env` path, the corpus directory, the archive walk, the lock file. All
  operator-supplied or internally constructed.
- **`gosec` G104 ×8 (LOW, unhandled errors)** — `Close` and `RemoveAll` on
  best-effort cleanup paths, several already carrying a comment explaining why
  the error is discarded.
- **`gosec` G103 ×3 (LOW, `unsafe`)** — the `kernel32` console calls, whose
  choice over `golang.org/x/sys` is documented at
  `tui/screen/terminal_windows.go:10`.

## What this review did not reach

Said plainly, because a review that lists only what it found invites the reader
to assume it looked everywhere.

External readers were not launched. The enumeration this kind of review
requires before pointing third-party coding agents at a machine found, beside
the repository, a directory of real client collections and several completed
audits — and these agents are measured wandering outside the tree they are
launched in. That is a client-confidentiality decision for the maintainer
rather than for the review, so it was put to them instead of taken. The union
of findings here is therefore one reader's, not four's.

Not examined: the archive writer beyond its scanner hits, two simultaneous runs
against one output directory, and what the Extended Events and Query Store
collectors cost on a busy instance.

## The warning this earns

Shorter than the document, and the part worth carrying elsewhere:

> `--queries-dir` runs the SQL you give it. The statement lint refuses the
> obvious ways to change a server, and it has been walked past twice by people
> trying; it stops an accident, not an author. A corpus you have not read is a
> corpus you are running against production, as the login you configured.
