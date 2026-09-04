# Working in this repository

## This repository is public. No client identifier enters it.

`sql-auditor` is published at github.com/rudi-bruchez/sql-auditor. Nothing that
identifies a client of this consultancy belongs in it: no instance or host name,
no database name, no login or service account, no IP address, no application
table name.

That applies everywhere, and the last two are the ones people forget:

- source, SQL comments, test fixtures;
- `docs/`, `README.md`;
- **commit messages**;
- file names and directory names.

The collections themselves, and everything derived from them, live outside this
repository: in `sql-auditor-private` and in the `audits/` directory beside it,
neither of which is public.

### Why it needs saying

The leak path is never negligence. You write a test, or a comment explaining a
bug, from the collection you happen to have open, and the server name travels
with it. On 27 August 2026 a fix to `063.blocked-process-reports.sql` was
committed with `Found on <instance>` in both the comment and the message.
Looking for other occurrences turned up eleven files that already carried
client names, including a table in `docs/verification-2012.md` that named three
real production instances as the servers the corpus had been run against.

None of them was needed for what the test or the document was demonstrating.

### What to use instead

Follow what the repository already does with `AUDIT_RO`, `example.com` and
`192.0.2.1`, all of them obviously invented:

| For | Use |
| --- | --- |
| an instance | `SQL01`, `SQL01\PROD` |
| a database | `SALESDB` |
| an IP address | the `192.0.2.0/24` documentation range |
| an email address | `example.com` |

An audit finding is told without a proper noun. "Found on a client instance in
August 2026" says everything the reader needs.

### Before committing

```
git grep -niE "<the client names you have been working with>"
```

Search for fragments too, not just whole names. The rename of August 2026 missed
a `QUERY_STORE_DB_INCLUDE` glob in a test, which held the first five letters of
a client database rather than its full name, because the search was for the full
name. A failing test caught it a minute later, which is luck rather than method.

## Tests

`go test ./...` must pass before a commit. Test fixtures are part of the public
surface of this repository, so the rule above applies to them in full.

### Adding or removing a collector

The corpus inventory lives in `testdata/corpus.txt`. Regenerate it rather than
editing it, and run the rest of the checks in the same breath:

```
pwsh -File tools/refresh-corpus.ps1
```

The regeneration alone is `go test . -run TestEmbeddedCorpusIsValid -update`.

**Never run either in CI**, and `ci.yml` does not. The file is a guard: a
collector must not enter or leave the corpus without someone saying so, and a
pipeline that regenerated it before checking it would assert nothing. What a
reviewer reads is the diff on `testdata/corpus.txt`.

The test reports with `Errorf`, so an inventory mismatch no longer aborts the
run and the directive and contract lint report in the same pass. A new collector
fails once, with everything wrong with it listed together.

### Do not write down the size of a growing collection

The target is narrow: a test that hardcodes how many things there are, where the
things go on being added. It is not about a fixture asserting its own values — a
plan id of 104 or a ring count of 412 is the fixture, and belongs where it is.

This section exists because three tests hardcoded a size, and each cost a round
trip every time the collection grew — 62 to 75 collectors paid that toll eight
times. Such a count is also the weakest available assertion: it says "got 74,
want 75", names nothing, and is blind to a rename.

There is always a better form, and which one depends on what is being counted:

- **Derive it from an independent source of truth.** `TestAllTurnsOnEveryOptIn`
  compares `Options.Flags` against `collect.KnownFlags` — two sets decided in
  different places, so comparing them is a real test and costs no maintenance.
  The verification screen's total comes from its own fixture the same way.
- **Make it a golden file** when there is no second source, as the corpus
  inventory does. Regenerated with `-update`, reviewed as a diff.

A hardcoded number is a golden test written in the worst available format.

## Commit messages are written in English

Whatever language the conversation is in, the message that lands in the log is
English. This repository is public: its history is read by people who have no
reason to speak French, and a message they cannot read is a message that does
not explain the change.

That is a rule about the log, not about the work. Discussion, review and
planning happen in whatever language suits; only the commit message is fixed.
The same goes for everything else already in the public surface — source
comments, `docs/`, `README.md` — which is English throughout.
