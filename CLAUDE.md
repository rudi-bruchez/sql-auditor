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

## Commit messages are written in English

Whatever language the conversation is in, the message that lands in the log is
English. This repository is public: its history is read by people who have no
reason to speak French, and a message they cannot read is a message that does
not explain the change.

That is a rule about the log, not about the work. Discussion, review and
planning happen in whatever language suits; only the commit message is fixed.
The same goes for everything else already in the public surface — source
comments, `docs/`, `README.md` — which is English throughout.
