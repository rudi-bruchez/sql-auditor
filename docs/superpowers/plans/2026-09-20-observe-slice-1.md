# `sql-auditor observe`, slice 1: implementation plan

> For agentic workers: required sub-skill, superpowers:subagent-driven-development (recommended) or superpowers:executing-plans, task by task. Steps use checkbox (`- [ ]`) syntax for tracking.

Goal: implement the default capture of `sql-auditor observe` as
`docs/observe-spec.md` specifies it, up to and including a written archive:
the timed run, `start` / `status` / `finish`, the histogram target, the
decoding package, resolution of hashes to statements, the consent prompt and
the exit codes.

Out of this slice, and deliberately: `--detail` with its ring buffer and event
file, `--session` borrowing, and `--agent-stop`. Each doubles a surface this
slice has to get right first, and each is a plan of its own. The code this
slice writes must not make them harder: the decoding package gets its ring
buffer decoder now, because its contract is shaped by having two shapes, and
the session builder takes its target as a value rather than assuming one.

Architecture: a new `observe` subcommand beside `collect` and `check`; a
session lifecycle owned by `collect/observe.go` (create, start, snapshot,
stop, drop, self-heal); a pure decoding package `collect/xevents`; a state
file binding server, database and session; resolution through the Query Store
then the plan cache; an archive written by the same writer `collect` uses.

Tech stack: Go (standard library, `testing`, `testing/fstest`), T-SQL, SQL
Server 2025 in the Podman container `sql2025` for every measurement step.

Spec: `docs/observe-spec.md` at commit `da6c3e9`. Read it before your task;
this plan argues from it, and where the two disagree, stop and say so.

## What the user decided, and what this plan owes the reviewers

`observe start --for <minutes>` is in scope, in the shape the user chose after
the trade was explained: the deadline is declared, `status` shows the time
left, any later invocation stops a session past its deadline, and a `finish`
that arrives late writes the archive with the window it really measured plus a
manifest line saying by how much the capture ran past its intent.

What it is not: a finish that happens with nobody present. The capture is a
histogram that keeps accumulating and a baseline subtracted at the end, so a
late finish cannot reconstruct the window that was asked for. A truly
self-acting finish needs a process alive at the deadline, which is what the
timed run already is; anything else is an orphan writing an archive nobody
watches. That was offered and declined for now.

## Global constraints

- This repository is public. No client identifier anywhere: code, SQL
  comments, tests, fixtures, docs, commit messages, file names. Use `SQL01`,
  `SQL01\PROD`, `SALESDB`, `192.0.2.0/24`, `example.com`.
- Commit messages are in English. The body is prose that explains why. No
  `Co-Authored-By`, no `Generated with`, no attribution trailer of any kind.
- Documentation you write uses no bold and no em dash or en dash.
- `gofmt -l .` must print nothing and `go test ./...` must pass before every
  commit.
- Every setup and its test run happen in ONE shell invocation. Shell state does
  not survive between tool calls.
- Every `go test -run` below names its expected count of top-level tests. Count
  the `--- PASS` / `--- FAIL` lines. A lower count means the filter is wrong,
  not that the task is done.
- Every task that adds an assertion has a break step: break the behaviour,
  confirm the named test fails, restore. Reporting "two of three broke as
  predicted" is the successful outcome of that step. Keep a copy of the file
  outside the repository to restore it; never `git checkout`, `git restore`,
  `git stash` or `git clean` a file holding uncommitted work.
- If your own measurement contradicts this plan or the spec, stop and say so in
  your report rather than making the code match the brief.
- SQL runs only against `sql2025` (`localhost,11533`). Its `sa` password stays
  in the container: `podman exec sql2025 printenv MSSQL_SA_PASSWORD`. Databases
  you create are named `ZzObserve<something>` and dropped before you finish.
  Extended Events sessions you create are named `ZzObserve<something>` too, and
  dropped the same way: this plan is the one place in the repository that
  creates them, so nobody else's cleanup will find yours.
- Never touch `LivreHarnais`, `PachaDataFormation`, `LabVerrous`,
  `LabVerrousClassique`, `LabCompilation`.

## The decision this plan makes that the spec does not

**Where `observe`'s SQL lives.** Not in `queries/`. That tree is the corpus:
its statement lint refuses anything but a read, `check` lists its files as the
questions the tool asks, and `MANIFEST.txt` tells a security officer that "the
collector issues only read-only SELECT statements". A `CREATE EVENT SESSION`
in there would make that sentence false and would fail the lint that keeps it
true.

`observe`'s statements live in `collect/observesql/`, embedded with
`//go:embed`, one file per statement, with the same header comments the corpus
uses and none of its directives. They are not part of the corpus inventory,
they do not appear in `check`, and `testdata/corpus.txt` does not change. The
DDL among them is what the consent prompt prints, which is a second reason for
it to be a file rather than a Go string with `%s` in it.

Reviewers: this is the decision most likely to be wrong. The alternative is a
second corpus with its own directive set and its own lint, which is more
machinery than one command justifies today and less than it will justify when
`observe` grows.

---

### Slice 0: measure the two things that decide everything

No shipping code. Two facts are load-bearing for every later task, and both
are cheap to get wrong in a way that looks like something else.

#### Task 0.1: the hash conversion

The histogram returns its slot value as a decimal. `query_hash` in
`sys.query_store_query` and `sys.dm_exec_query_stats` is `binary(8)`. The
conversion is signed against unsigned and byte order at once. Get it wrong and
every hash resolves to nothing, which looks exactly like a cold plan cache.

- [ ] Step 1: on `sql2025`, create `ZzObserveHash`, run a statement you can
      recognise, and read its `query_hash` from `sys.dm_exec_query_stats` as
      `binary(8)` and as `CONVERT(bigint, query_hash)`.
- [ ] Step 2: create an Extended Events session bucketizing on
      `sqlserver.query_hash` with `source_type = 1`, run the same statement,
      read `target_data`, and take the slot value.
- [ ] Step 3: write down, in `docs/observe-hash-conversion.md`, the exact
      expression that turns one into the other, in both directions, with the
      measured values beside it. Include a negative slot value: that is where
      signedness bites, and a statement whose hash has the high bit set is
      what produces one.
- [ ] Step 4: prove the inverse. Take three hashes from the plan cache,
      convert, and match them against the histogram's slots by equality, not
      by eye.

Done when the document names the expression and shows a match on at least one
hash with the high bit set.

#### Task 0.2: the fixtures

- [ ] Step 1: capture a real histogram `target_data` on `ZzObserveHash` with
      at least three distinct statements and an overflow (set `slots = 2` to
      force it).
- [ ] Step 2: capture a real ring buffer `target_data` from the same session
      definition with a ring buffer target instead.
- [ ] Step 3: scrub both: the ring buffer carries statement text, and the rule
      in `CLAUDE.md` applies to fixtures exactly as it applies to code. Replace
      any database or object name with `SALESDB` and `dbo.Orders`.
- [ ] Step 4: commit them under `collect/xevents/testdata/`, with a short
      `README.md` saying which build produced them and what was scrubbed. A
      fixture whose provenance is unknown is a fixture nobody dares change.

---

### Slice 1: `collect/xevents`, the decoder

Files:
- Create: `collect/xevents/xevents.go`, `collect/xevents/xevents_test.go`
- Use: the fixtures from task 0.2

Interfaces:
- `func DecodeHistogram(xml string) (Histogram, error)` where `Histogram`
  carries `Buckets []Bucket` (`Value int64`, `Count int64`), `Slots int`,
  `NotFiltered int64`, `Truncated bool`.
- `func DecodeRingBuffer(xml string) (RingBuffer, error)` where `RingBuffer`
  carries `Events []Event`, `DroppedCount int64`, `DroppedBuffers int64`,
  `Truncated bool`.
- No function in this package opens a file or a connection.

- [ ] Step 1: write the failing tests against the fixtures: bucket count and
      values, the overflow attribute, both loss counters, and an unknown field
      type coming back as its string form labelled unconverted.
- [ ] Step 2: write the decoders. Eager, not lazy.
- [ ] Step 3: the truncation test, which is the one that matters. Cut a
      fixture in the middle of a bucket and assert `Truncated` is true rather
      than a short list returned cleanly. Cut it between two buckets, where the
      XML still parses, and assert the same. The second case is the one a
      parser gets wrong.
- [ ] Step 4: break step. Make `DecodeHistogram` ignore the overflow
      attribute; confirm the named test fails; restore.
- [ ] Step 5: `go test ./collect/xevents/ -run 'Decode|Truncat'` names 6 tests.

Note for the reviewer of this plan: the spec asks for a rename, because
`collect/observer.go` and `collect/xevents` sit in one tree with almost the
same name. This slice does not do it. `Observer` is the interface watching a
`collect` run and is unrelated; renaming it touches every caller and belongs in
a commit of its own, before this package lands or after, not inside it.

---

### Slice 2: the session, and the statements that drive it

Files:
- Create: `collect/observesql/*.sql`, `collect/observe.go`,
  `collect/observe_test.go`

The statements, one file each: create the session (the DDL the prompt prints),
start it, read its state from `sys.dm_xe_sessions` and
`sys.dm_xe_session_targets`, read `target_data`, stop it, drop it, and the two
reads of `Batch Requests/sec` the cost estimate needs.

- [ ] Step 1: the DDL, built as text and never as a concatenation of user
      input. The database filter is `sqlserver.database_id = <resolved id>`,
      resolved before the DDL is composed, and the session excludes `@@SPID`,
      which means reading it first. `MAX_MEMORY`, `EVENT_RETENTION_MODE =
      ALLOW_SINGLE_EVENT_LOSS`, `STARTUP_STATE = OFF`, `MAX_DISPATCH_LATENCY`
      and the histogram's `slots` and `source_type = 1` are all explicit. A
      default inherited here is a number that differs between builds.
- [ ] Step 2: `sessionState`, reading the fixed name: does it exist, is it
      running, when was it created, how many events has it counted. The count
      is the sum of the histogram's buckets and the code says so where it is
      computed, because `status` must not report it as a live count.
- [ ] Step 3: the deadline, derived rather than stored:
      `create_time + max-minutes`. A function taking the state and a clock, so
      the test does not sleep.
- [ ] Step 4: the self-healing sweep. Any entry point calls it first: a
      session under the known name past its deadline is stopped and dropped
      before anything else happens, and the fact is reported. Within its
      deadline, `start` refuses and says who is running what since when.
- [ ] Step 5: tests with a fake clock and a fake connection: expired stops,
      live refuses, absent proceeds. `go test ./collect/ -run Observe` names 5
      tests.
- [ ] Step 6: break step. Make the sweep compare against the wrong side of the
      deadline; confirm the expiry test fails; restore.
- [ ] Step 7: against `sql2025`, once, by hand: create the session through the
      real statements, confirm it appears in `sys.dm_xe_sessions` with the
      options as written, stop and drop it, and confirm it is gone. Record the
      output in the task report.

---

### Slice 3: the commands

Files:
- Create: `cmd/sql-auditor/observe.go`
- Modify: `cmd/sql-auditor/main.go` (dispatch, help)

- [ ] Step 1: `observe --minutes N`: estimate the cost from two reads of the
      cumulative counter, print the DDL and the permissions, wait for
      confirmation unless `--yes`, create, start, wait, stop, read, drop,
      write. Ctrl-C stops, drops, and writes a partial archive whose manifest
      says it was interrupted. `collect/cancel.go` already has this discipline;
      use it rather than writing a second one.
- [ ] Step 2: `observe start [--for N]`, `observe status`, `observe finish`.
      The state file binds server, database and session name, and `finish`
      refuses when it is lost, with `--no-baseline` as the documented way to
      get the raw counters and a manifest note saying they are cumulative.
- [ ] Step 3: `--for` in the shape the user chose. The intent is stored in the
      state file; `status` prints the time left, or by how much it is over;
      `finish` after the deadline writes the real window and a manifest line
      naming the overrun. The archive never claims the requested window.
- [ ] Step 4: `--max-minutes`, default 60, refusing rather than obeying beyond
      it, on every mode.
- [ ] Step 5: the exit codes, 0, 1, 2 and 3, from the table in the spec, each
      with a test. 2 is the one that will be wrong: it means the capture ran
      and something is missing, which includes overflow, truncation, drops and
      an unresolved share above the stated threshold.
- [ ] Step 6: `go test ./cmd/sql-auditor/ -run Observe` names 8 tests.

---

### Slice 4: resolution and the archive

Files:
- Create: `collect/observeresolve.go`, `collect/observearchive.go`
- Modify: `collect/manifest.go` (an `observe` manifest, not the collector's)

- [ ] Step 1: resolution, Query Store first in the target database, then the
      plan cache, each row labelled with where its text came from. The manifest
      says when the Query Store was off, and says that plan-cache resolution is
      ambiguous across databases because `sys.dm_exec_query_stats` is
      instance-wide.
- [ ] Step 2: `statements.json`, sorted by execution count, no first-seen or
      last-seen timestamps, and `unresolved.json` beside it.
- [ ] Step 3: the manifest: the window, the session options as created, the
      drop count, the overflow attributes, the truncation flag, whether the
      capture was interrupted or ran past its deadline, and the disclosure
      paragraph. The disclosure is not the collector's: the capture carries no
      application data, but the archive carries resolved text, and plan-cache
      text can embed literals. Say exactly that.
- [ ] Step 4: a test that an archive from a fixture capture has the files, the
      counts and the labels; and one that an empty capture produces an archive
      rather than nothing, with a manifest that says the window saw no
      statement.
- [ ] Step 5: `go test ./collect/ -run 'Resolve|ObserveArchive'` names 6 tests.

---

### Slice 5: the end-to-end run

- [ ] Step 1: against `sql2025`, a real five-minute capture on a database you
      create, with a workload you drive from a second connection. Check the
      count against what you drove: you know how many statements you sent.
- [ ] Step 2: the same with `start --for 2`, finished late on purpose, and
      confirm the manifest names the overrun and the window is the real one.
- [ ] Step 3: kill the tool mid-capture, run `observe status` from a clean
      working directory, and confirm the session is found, reported and
      stopped when past its deadline.
- [ ] Step 4: drop everything you created and prove it: no session under the
      name, no `ZzObserve` database.
- [ ] Step 5: write the results into `docs/observe-spec.md` as a "what was
      measured" section, in the register the other specs use.

---

## The risky points, named for the adversarial review

Not a summary. These are the claims this plan rests on, in the order that
would hurt most if they are wrong.

1. **The hash conversion** (task 0.1). If it is wrong, every capture reports
   every statement as unresolved and the failure is indistinguishable from a
   cold cache. It is measured first for that reason.
2. **`source_type = 1`.** Bucketizing on an action rather than a column is
   what makes one histogram cover two events. The column path builds, runs,
   and covers half the workload.
3. **`slots`.** Past the slot count the target folds distinct statements into
   one bucket rather than dropping them, which corrupts the single number this
   command exists to produce. The overflow attributes must reach the manifest.
4. **The deadline has no server-side timer.** It is derived from
   `create_time` and enforced at the next visit. The plan inherits the spec's
   honesty here and must not quietly promise more.
5. **`MAX_DISPATCH_LATENCY`.** `status` can report zero events for the first
   half-minute of a healthy capture. Whatever the implementation chooses, the
   output has to say which.
6. **`target_data` truncation.** A cut document can parse into a shorter list
   that looks complete. Slice 1 step 3 is the test that matters most in this
   plan.
7. **Where the SQL lives.** The decision above, and the one most likely to be
   revisited.
8. **The state file binds three things**, not the session name alone. A
   `finish` against the wrong connection string must not subtract a stranger's
   baseline.
9. **One `observe` per instance**, from the fixed name, with two branches that
   are the same check.
10. **The archive's disclosure paragraph** is not the collector's. The capture
    carries no application data; the resolved text can.
