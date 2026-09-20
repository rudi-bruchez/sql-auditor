# `sql-auditor observe`, capture slice 1: implementation plan

> For agentic workers: required sub-skill, superpowers:subagent-driven-development (recommended) or superpowers:executing-plans, task by task. Steps use checkbox (`- [ ]`) syntax for tracking.

The authority is `docs/observe-spec.md`. This plan replaces
`2026-09-20-observe-slice-1.md`, which planned the histogram design and is
marked superseded. It was itself rewritten after a panel of five readers ran its
first version against SQL Server 2025; "What the panel changed" is at the end,
and the reports are in `docs/superpowers/reviews/`.

If your own measurement contradicts this plan or the spec, stop and say so. Do
not make the code fit the brief. Across three panels now, every defect that would
have shipped was found by executing what a document said rather than by reading
it, and four of them were in briefs like this one.

## What this slice delivers

`observe --minutes N --database D`, `observe start`, `observe status`,
`observe finish` and `observe stop`, producing a capture file on the instance and
a small archive on the operator's machine that says where it is and what it
holds.

Out of scope, deliberately, each of them a surface that would double this one:
pulling the file back with `OPENROWSET`, borrowing an existing session, a SQL
Agent job as a deadline enforcer, and any analysis of the capture, which is
SQLFerret's.

## Working on the shared instance

The container `sql2025` is a SQL Server 2025 RTM-CU7 on port 11533 of the host.
The password comes only from `podman exec sql2025 printenv MSSQL_SA_PASSWORD`,
piped, never printed and never written to a file.

Choose a prefix of your own, four or five letters nobody else would pick, and use
it for every session, database, directory and file you create. Do not use
`ZzObserve`: a previous panel prescribed it to everyone, two readers picked it at
once, and one reader's cleanup dropped the other's sessions in the middle of a
measurement, producing a count that went 11, then 0, then 11.

Clean up only what carries your own prefix, and assert only that your prefix is
gone. Do not assert that the instance holds three sessions and nothing else: that
is a claim about other people's work, and it was already false at the start of
the last two panels.

One thing you cannot clean up, and it is a finding rather than an inconvenience:
a `.xel` file written by a session belongs to the SQL Server service account, and
nothing in this tool's permission set deletes a file. Removing one needs
`podman exec`, which you have and the shipped tool does not.

## Before you write any code

One thing in the spec is described rather than measured and decides code in slice
4. It is cheap and it comes first, for the reason the superseded plan's slice 0
existed: a measurement that arrives after the code it invalidates is paid for
twice.

### Task 0.1: can the running target name its current file?

The spec's rollover-loss detection rests on one sentence: that immediately after
the session starts, the running target's current file name can be read from
`target_data` and stored. If it cannot, rollover loss is undetectable and the
spec has to say so instead of promising it.

Run this as a whole, in one shell invocation, with `<P>` replaced by your prefix
throughout. The spec's rendered DDL is a template with named parameters and
cannot be executed as it stands; these are the values for the measurement, and
they are given here rather than left to you, because "small" is not a value and
an earlier version of this task said only that.

```sql
CREATE DATABASE <P>DB;
GO
USE <P>DB;
CREATE TABLE dbo.T(id int NOT NULL PRIMARY KEY, pad char(400) NOT NULL);
INSERT dbo.T SELECT TOP (2000) ROW_NUMBER() OVER (ORDER BY (SELECT 1)), 'x' FROM sys.all_columns;
GO
CREATE EVENT SESSION [<P> capture] ON SERVER
  ADD EVENT sqlserver.rpc_completed (
      SET collect_statement = 1
      ACTION (sqlserver.database_name, sqlserver.client_app_name)
      WHERE database_id = DB_ID(N'<P>DB')
        AND sqlserver.client_app_name <> N'sql-auditor'
        AND object_name <> N'sp_reset_connection'),
  ADD EVENT sqlserver.sql_batch_completed (
      SET collect_batch_text = 1
      ACTION (sqlserver.database_name, sqlserver.client_app_name)
      WHERE database_id = DB_ID(N'<P>DB')
        AND sqlserver.client_app_name <> N'sql-auditor')
  ADD TARGET package0.event_file (
      SET filename = N'/var/opt/mssql/log/<P>/sql-auditor-observe-20260920T120000-5.xel',
          max_file_size = 2,
          max_rollover_files = 2)
  WITH (MAX_MEMORY = 4096 KB,
        EVENT_RETENTION_MODE = ALLOW_SINGLE_EVENT_LOSS,
        MAX_DISPATCH_LATENCY = 3 SECONDS,
        STARTUP_STATE = OFF);
ALTER EVENT SESSION [<P> capture] ON SERVER STATE = START;
```

`DB_ID(N'<P>DB')` is written out because the predicate is evaluated at create
time and stores the number; that is the same literal the spec describes, reached
without a variable. `max_file_size = 2` is the floor: measured, a value of 1 is
refused with `Msg 25641, the parameter "max_file_size" passed is invalid`. The
session name is yours and not the managed `[sql-auditor observe]`, so that a
parallel reader cannot collide with it.

- [ ] Read `sys.dm_xe_session_targets.target_data` for the `event_file` target
      immediately after the start and record the XML verbatim. The question is
      whether it names the file currently being written, `_0_<ticks>` suffix
      included.
- [ ] Drive traffic until at least one file has been deleted by rollover. A loop
      of a thousand batches of a few kilobytes against `dbo.T` reaches 2 MB.
      Then read the wildcard set and confirm the name recorded at the start is
      absent. That is the detection the spec specifies; prove it fires.
- [ ] Prove it does not false-positive: a second session, `max_file_size = 128`
      and twenty batches, where the recorded name must still be present at the
      end.
- [ ] Write `docs/observe-capture-measurements.md` with both results, the build,
      and the XML verbatim. Commit it before writing any Go.

If the target does not name its file, stop and say so. The spec has a fallback
sentence for that case and it changes the manifest, not the code.

### There is no directory probe, and that is a decision

An earlier version of this plan had a second task here: preflight would test the
capture directory by creating a throwaway session there, starting it, stopping it
and dropping it, so that a bad path could be refused with exit code 3 before the
instance was touched. Four of the five readers attacked it and they were right.
Measured, with a colleague's session already holding the managed name, the four
statements run verbatim produce:

```
Msg 25631 ... The event session, "RbxProbe observe", already exists.
Msg 25705 ... The event session has already been started.
```

and then the `STOP` and the `DROP` succeed. The probe stopped and dropped a
running session it did not own, before consent and before any ownership check.
That is the worst thing this design could do to a production instance, and it was
in the plan as a safety feature.

It is not repairable by giving the probe its own name, because two more findings
survive that fix. A probe that succeeds leaves a `.xel` file the tool has no
permission to delete, which makes the manifest's "Nothing else on this instance
was modified" untestable. And a directory that does not exist is not refused:
measured, SQL Server creates it, two levels deep if asked, so a typo'd path
passes the probe and invents a directory on the client's instance.

The alternative is smaller and was measured. With no probe at all, an unwritable
directory fails at `STATE = START` with `Msg 25602`, the error is catchable in
`TRY`, the compensating `DROP` leaves no session, and no file exists because the
file could not be created. The probe bought one thing, the right to call the
refusal a 3 rather than a 1, and it cost a destructive action on a stranger's
session.

So: no probe. A bad capture directory is exit code 1, after consent, with the
compensating drop. The spec's exit code table and its "Where the file goes"
section are corrected to match in the same commit as this plan.

## Slice 1: the session, as text

Everything here is a pure function. No connection, no server, no I/O. This is the
part the superseded plan got wrong by pinning an interface before measuring, so
the interface comes after task 0.

Files: create `collect/observe/session.go` and `collect/observe/session_test.go`.
A new package rather than `collect/observe.go`, because `collect/observer.go`
already defines the `Observer` interface that watches a run and the two names
differ by one letter. Do not rename `Observer` in this slice; that is its own
commit and its own review.

### Task 1.1: render the DDL

- [ ] `Session` holds what the spec's parameter table lists: database id, capture
      directory, stem, max memory, dispatch latency, file size, rollover files,
      and the application name to exclude.
- [ ] The application name is a field and not a constant. `collect/config.go:148`
      has `DefaultAppName = "sql-auditor"`, but `config.go:493` is
      `AppName: get("SQL_APPLICATION_NAME", DefaultAppName)`, so an operator can
      change it, and `collect/watch.go:377` opens a second connection as
      `cfg.AppName + watchAppSuffix`. A predicate comparing against the hardcoded
      literal excludes the wrong thing in both cases. Take it from `cfg.AppName`.
- [ ] `Stem()` builds `sql-auditor-observe-<utc yyyyMMddTHHmmss>-<max minutes>.xel`
      from a clock passed in, never `time.Now()` read inside. The clock is a
      parameter so the test is not a race.
- [ ] `FilePath()` joins the capture directory and the stem with the server's
      separator and not the operator host's. The operator may be on Windows and
      the instance on Linux; `filepath.Join` on the client is the wrong function
      for a path the server will interpret. Decide the rule, write it in a
      comment, and test a deep directory.
- [ ] `CreateSQL()`, `StartSQL()`, `StopSQL()`, `DropSQL()` return the exact
      statements the spec renders, with the parameters substituted. The session
      name is bracketed, the name containing a space.
- [ ] The two spellings the spec establishes by measurement are not negotiable
      and each gets a test naming why: no comma between the last `ADD EVENT` and
      `ADD TARGET`, and `object_name` rather than `sqlserver.object_name`.

### Task 1.2: parse a stem back

The sweep reads the deadline out of the file name, from a machine that has lost
everything local. That parse is the whole self-healing story, and the input it
gets is not the string you think.

- [ ] `ParseStem(string) (startedAt time.Time, maxMinutes int, ok bool)` takes the
      last path component and nothing else. Measured, both places the server
      hands this back give a full absolute path:
      `sys.server_event_session_fields` returns
      `/var/opt/mssql/log/<dir>/sql-auditor-observe-20260920T152004-5.xel`, and
      `target_data` the same with the `_0_<ticks>` suffix. A `ParseStem` that
      tests whether its whole argument begins with `sql-auditor-observe-` returns
      false for every real input, the fingerprint never matches, and the sweep
      never fires. Nothing in an earlier version of this task caught that,
      because both forms it required tested were bare names.
- [ ] So: one test per real input, each the exact string the server returned in
      task 0.1, pasted rather than constructed.
- [ ] A name whose last component does not begin `sql-auditor-observe-` returns
      `ok` false. That is the ownership check and the test says so.
- [ ] Round trip: `ParseStem(Stem(t, n))` returns `t` truncated to the second,
      and `n`.

### Task 1.3: the fingerprint comparison

- [ ] A `Fingerprint` type holding what the spec's ordered list names: the stem
      prefix, the two event names with their text option, the action names, the
      target type. Not the predicate.
- [ ] `Matches(observed Fingerprint) (bool, []string)` returns the mismatches as
      text, because the spec requires the refusal to print what it found and what
      it expected.
- [ ] Test the four cases the spec cares about: exact match, a session missing an
      action, a session with a different target type, and a session whose stem
      belongs to somebody else.

### Verify slice 1

Run setup and tests in one shell invocation, from the repository root, because
state persists across nothing and a hardcoded absolute path tests somebody else's
checkout:

```
go build ./... && go test ./collect/observe/ -run '^TestObserveSession' -v 2>&1 | tail -40
```

- [ ] No count is given here and none should be added. `CLAUDE.md` in this
      repository forbids writing down the size of a growing collection, calling a
      hardcoded number "a golden test written in the worst available format", and
      an earlier version of this plan required four of them. What is checked is
      behaviour: every bullet above has a named test and you list them.
- [ ] Break the code on purpose and watch the tests fall: put the comma back
      before `ADD TARGET`, change `object_name` to `sqlserver.object_name`, and
      feed `ParseStem` a full path. Report which tests failed and how many. Three
      failures is this step succeeding, not failing. Then restore.
- [ ] A note on filters, corrected from an earlier version which got it wrong in
      a way worth knowing: `go test ./collect/observe/ -run '^TestObserve'` is
      safe, because `-run` never crosses a package boundary and the two
      `TestObserver...` tests live in package `collect`, in
      `collect/observer_test.go`. The collision is real only for a command that
      tests `./collect/`. Anchor your filters anyway; the reason is legibility,
      not safety.

## Slice 2: the durable state, before anything needs it

This slice exists because an earlier version of the plan had the commands in
slice 3 persisting a state file that slice 4 defined. A task-by-task implementer
cannot do that, and one who tries writes it twice.

Files: `collect/observe/state.go`, `collect/observe/state_test.go`.

- [ ] The state file is `observe.state.json`, written in the output directory
      beside the run folders. The output directory is
      `collect/config.go:501`, `OutputDir: get("OUTPUT_DIR", "output")`, which is
      relative by default: a clean working directory therefore does remove it in
      the default configuration and does not when `OUTPUT_DIR` is absolute. Say
      which in a comment, because slice 5 depends on knowing.
- [ ] It binds server, database, session name, the capture file path, the stem,
      and the first file name read from `target_data` after the start.
- [ ] Written atomically, and only after `START` has succeeded. Before that there
      is nothing durable to point at and a half-written state file is worse than
      none.
- [ ] `finish` deletes it on success. A `finish` that cannot find it reconstructs
      the window from the stem on the server and says so, rather than refusing.

## Slice 3: the lifecycle, against a real instance

This is where two panels found almost everything, so read `docs/observe-spec.md`
sections "A fixed name is not proof of ownership", "Two operators can still
collide", "Ordering, which decides whether exit code 3 tells the truth" and
"Preflight, and why it departs from the collector's rule" before writing a line.

Files: `collect/observe/lifecycle.go`, `collect/observe/lifecycle_test.go`,
`collect/observe/live_test.go`.

### Task 3.1: read the instance without altering it

- [ ] `Describe(ctx, db) (*Observed, error)` reads any session under the managed
      name from `sys.server_event_sessions`,
      `sys.server_event_session_events`, `sys.server_event_session_actions`,
      `sys.server_event_session_fields`, `sys.dm_xe_sessions` for the counters,
      and `sys.dm_xe_session_targets` for the running target's file name. That
      last one is not decoration: it is where the rollover baseline comes from,
      and an earlier version of this plan measured that read in task 0 and then
      never asked any code to perform it.
- [ ] It alters nothing. That is the ordering rule and there is a test that the
      function issues no DDL, by running it against a fake connection that fails
      any statement that is not a `SELECT`.
- [ ] Three states, all of them distinguished and named: absent, defined and
      running, defined and stopped. The third is what a service restart leaves
      and it has no `sys.dm_xe_sessions` row at all.

### Task 3.2: the sweep

- [ ] The deadline comes from the stem and from nowhere else. Not from the
      sweeping process's flags, and not from `create_time`. Measured, a
      `STOP` and `START` cycle moves `create_time` while the stem does not, so a
      session a DBA restarted by hand would have its deadline slide forward
      indefinitely. Both the spec and an earlier version of this plan carried the
      two clocks without saying which one governed.
- [ ] A session matching the fingerprint and past the deadline in its own stem is
      stopped and dropped.
- [ ] A session matching the fingerprint but stopped is dropped whatever its age,
      there being no running-session row to read anything from.
- [ ] A session not matching the fingerprint is never touched, whatever its name
      or age. The live test for this is the one that matters most in the whole
      plan: create a session under the managed name that is not ours, sweep, and
      assert it is still there and still running.
- [ ] A session matching and within its deadline is left alone and reported.
- [ ] The teardown runs on a context derived from `context.Background()` with its
      own timeout, never on the caller's. Test it with a cancelled context: the
      drop must still happen. This is the finding the superseded plan got wrong
      by pointing at `collect/cancel.go`, which is 65 lines that classify an
      error and touch no server.

### Task 3.3: creation, and a compensating action for every step

- [ ] `CREATE` returning already-exists is an ordinary refusal: re-read, describe
      the winner, stop nothing. Measured on 2025 it is `Msg 25631` and it is
      catchable, so the driver surfaces it as an ordinary query error.
- [ ] If the re-read fails, or what it finds does not match the fingerprint, the
      refusal says that instead of guessing.
- [ ] `CREATE` succeeded and `START` failed is the bad-directory case and every
      other target failure: the compensating `DROP` runs on a fresh context, and
      the run exits 1 saying the directory was refused by the server. Measured,
      `Msg 25602` is catchable and the drop leaves nothing.
- [ ] `START` succeeded and the state file could not be written: stop, drop,
      exit 1. There is a running session with nothing pointing at it otherwise.
- [ ] One test per transition, with the failure injected. Four listed here, and
      if you find a fifth, add it and say so.

### Task 3.4: preflight and ordering

- [ ] Probe `ALTER ANY EVENT SESSION` with `HAS_PERMS_BY_NAME`, and put the
      comment the spec asks for: this departs from `collect/preflight.go`'s rule
      that a probe reads a real object, because the only faithful probe is the
      DDL that must not run before consent.
- [ ] Probe `VIEW SERVER STATE` the way `Capabilities()` already does.
- [ ] Order: validate, connect, describe, preflight, consent, then sweep or
      create. There is a test that asserts the order by recording the calls a
      fake connection receives.
- [ ] `status`, `stop` and `finish` do not prompt for consent and do not create
      anything. They describe and they sweep. The sweep can issue `STOP` and
      `DROP`, which the spec authorises and requires them to print; that is the
      one documented exception to "no DDL before consent" and it is narrow
      because it acts only on a session the fingerprint proved is ours. An
      earlier version of this plan gave one order for all five subcommands, which
      would have had `observe status` printing a `CREATE EVENT SESSION` for
      approval.

### Task 3.5: the grant, in its own vocabulary

Measured on this tree: `ALTER ANY EVENT SESSION` appears in no `.go` and no
`.sql` file. The auditor's path on a locked-down instance is
`sql-auditor check --grant-script grants.sql`, handed to whoever can run it, so a
script that omits the one right `observe` needs buys a second round trip.

- [ ] Do NOT add it to `collect.Capabilities()`. That function returns the one
      global list: `collect.Run` passes it to preflight for an ordinary read-only
      collection, the TUI renders it, and `CapabilityCheck`'s own doc comment
      says the result is read in `MANIFEST.txt` by a security officer. Adding an
      observe-only DDL right there would make every `check` and every `collect`
      on every client probe a server-altering permission and print its denial in
      the manifest of a read-only collection. That is the spec's central
      separation failing from the inside, and an earlier version of this plan
      asked for exactly it.
- [ ] Give `observe` its own capability list, reusing the `Capability` type and
      `RunPreflight`, and its own grant-script path.
- [ ] Add a test that ordinary `collect` neither probes nor offers the alteration
      right, and that `observe` does. That is the assertion the separation needs,
      and it is not the one the two existing global tests make.
- [ ] `go test ./collect/` must be green afterwards, not only
      `go test ./collect/ -run Grant`: `TestEveryProbedCapabilityCanBeGranted` is
      in `collect/grants_test.go` and `TestCapabilityNamesMatchNormalisedPermissions`
      is in `collect/preflight_test.go`, which no filter containing "Grant"
      reaches. An earlier version of this plan named both tests and gave a command
      that runs one.

### Verify slice 3

```
go test ./collect/observe/ -run '^TestObserveLifecycle' -v 2>&1 | tail -40 && \
SQL_AUDITOR_LIVE_SERVER=localhost,11533 SQL_AUDITOR_LIVE_USER=sa \
SQL_AUDITOR_LIVE_PASSWORD="$(podman exec sql2025 printenv MSSQL_SA_PASSWORD)" \
go test ./collect/observe/ -run '^TestLiveObserve' -v 2>&1 | tail -30
```

- [ ] The live guard is the one `collect/watch_live_test.go` already uses, read
      from that file rather than remembered: three variables,
      `SQL_AUDITOR_LIVE_SERVER` in `host,port` form, `SQL_AUDITOR_LIVE_USER` and
      `SQL_AUDITOR_LIVE_PASSWORD`, and its single test is
      `TestLiveWatchRecordsAWait`. Follow that naming.
- [ ] The live tests are the ones a fake cannot replace. At minimum: a same-named
      session that is not ours survives a sweep and is still running afterwards,
      a stopped remnant is dropped, a `CREATE` against an unwritable directory
      leaves nothing behind, and a drop succeeds on a cancelled caller context.
- [ ] Break the code on purpose: make the sweep read the deadline from a
      parameter instead of the stem. The test that catches it is the one where
      the sweeping process passes a smaller maximum than the session declared. If
      nothing fails, your test is not testing that, and say so.

## Slice 4: the commands

Files: `cmd/sql-auditor/observe.go`, `cmd/sql-auditor/observe_test.go`, and edits
to `cmd/sql-auditor/main.go`.

### Task 4.1: wire the command

- [ ] `isCommand` in `cmd/sql-auditor/main.go:635` gains `observe`. It is the
      single list the "did you mean" suggestion reads.
- [ ] The dispatch `switch cmd` at `main.go:912` gains its case, beside `collect`
      and `check`.
- [ ] Those two are separate edits and nothing in the existing suite notices if
      you make only the first. Measured: adding `observe` to `isCommand` alone
      leaves the whole `cmd/sql-auditor` package green while the built binary
      answers `sql-auditor observe` with `unknown command "observe".` and exit 2.
      An earlier version of this plan asserted that
      `TestTheDiagnosisNeverPointsAtACommandThatWouldFail` guards this; it does
      not. Read it: it calls `nothingToDo()` and checks a string on the
      argument-less screen. So write the guard that does not exist, a test that
      every word `isCommand` accepts reaches a dispatch case.
- [ ] The subcommand split at `cmd/sql-auditor/main.go:750` gains `observe`:

      ```go
      if (cmd == "queries" || cmd == "env") && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
      ```

      The comment above it says why it exists: `flag.Parse` stops at the first
      non-flag argument, so a flag written after a subcommand is never seen.
      `observe start --for 2` is that shape exactly. Without `observe` in this
      condition the deadline arrives as the zero value and every test of the
      deadline logic passes while testing the default.
- [ ] A test that `observe start --for 2` reaches the code with 2. Write it
      before the parsing change and watch it fail.
- [ ] Flags: `--minutes`, `--for`, `--database`, `--file-dir`, `--max-minutes`
      defaulting to 60, `--yes`. Use `flag.IntVar`, which already refuses a
      fractional value; do not write a parser for that.

### Task 4.2: consent

- [ ] The prompt prints the rendered DDL with every parameter substituted, the
      two permissions, the capture path, and the expected call count with the
      size labelled an estimate.
- [ ] It also says, in one line, that the capture directory will be created by
      SQL Server if it does not exist. Measured: a directory that is absent is
      not refused, it is created, two levels deep if the path asks for it. Since
      there is no preflight probe any more, the prompt is the only place a typo
      can be caught, and it is caught by a human reading the path.
- [ ] `--yes` skips the prompt and nothing else.
- [ ] What is recorded in `_run.json` is the statement as executed, captured at
      execution. Not rebuilt afterwards from the same builder: a rebuild proves
      the builder is deterministic, which nobody doubted, and not that the prompt
      told the truth. There is a test that mutates the statement between render
      and execute and asserts the two differ in the record.

### Task 4.3: cancellation

- [ ] Ctrl-C stops, drops, and writes a partial archive saying it was
      interrupted. The teardown uses the fresh context from task 3.2.
- [ ] The first Ctrl-C message names what a second one will leave behind: a
      running session and a growing file. `interruptibleOn` in
      `cmd/sql-auditor/main.go:1214` hardcodes its message today, so give it the
      message as a parameter rather than branching on the command inside it.
- [ ] The session name and the capture path are on screen before the wait starts,
      not only in the archive.

### Verify slice 4

```
go test ./cmd/sql-auditor/ -run '^TestObserveCommand' -v 2>&1 | tail -30 && \
go test ./cmd/sql-auditor/ 2>&1 | tail -5
```

- [ ] The whole package, not only your filter. The two edits in task 4.1 are the
      kind a filtered run misses.

## Slice 5: the archive

Files: `collect/observe/archive.go`, `collect/observe/archive_test.go`.

- [ ] A separate manifest type in this package. Do not add a branch to
      `collect/manifest.go`: it is built around one `Manifest` whose `Human()`
      prints the read-only claim, and `observe` needs a manifest that says the
      opposite. The spec's argument is that the approver's choice must not become
      a flag inside a function.
- [ ] `Zip` from `collect/archive.go` is reused unchanged. Its signature is
      `Zip(runFolder, destZip string) error` and it knows nothing about
      collection.
- [ ] The manifest text is the spec's draft, verbatim. Every sentence of it is a
      claim the code has to make true, so there is one test per claim. The
      sentence that is hardest to keep true is "Nothing else on this instance was
      modified": with the directory probe gone there is nothing else, and a test
      asserts the run creates no file other than the capture.
- [ ] `capture.json`: the path, the file set, the event count, the counters read
      before the stop, the recorded first file name and whether it survived.
- [ ] On the recovery path, where the state file is gone, the first file name is
      not available and rollover loss is therefore unknowable. Write
      `"rollover": "unobservable"` and not a false negative. An earlier version of
      this plan made the field mandatory and the recovery path unable to fill it.
- [ ] `_run.json`: the executed statements, the window, the exit classification.
- [ ] The archive is named with a time and not only a date, and it does not reuse
      `RunFolderName` from `collect/output.go`. That function formats
      `2006-01-02`, day granularity, and `collect/collect.go` handles a name
      collision by renaming the previous run aside as `.superseded-<time>`. Two
      captures in one day are the normal case for `observe`, so reusing it would
      quietly supersede the morning's capture. `FailedRunFolderName` in the same
      file is the precedent for a time-granular name.
- [ ] The window a late `finish` reports is the one it really measured, with the
      overrun named. Test a `finish` arriving after the deadline.

## Slice 6: end to end, on a real instance

- [ ] `observe --minutes 1 --database <P>DB` against `sql2025`, driving a known
      number of calls from a Go client with a known application name, and a
      second client named `sql-auditor` whose calls must not appear.
- [ ] Check the count against what you drove, and check it twice: once for the
      total, once for the absence of `sp_reset_connection` and of the excluded
      application. A total that matches while an exclusion silently failed is the
      failure mode this design has been bitten by twice.
- [ ] That second client proves the mechanism and not the safety. It passes
      precisely when an unrelated application called `sql-auditor` would be
      silently excluded from its own audit, which is a known limitation of the
      spec rather than a bug in the code. Say so in the test's name, and check
      the command help says it too.
- [ ] `start`, then `status` while it runs, then `finish`. Then `start`, kill the
      process, delete `observe.state.json` explicitly, and confirm the next
      invocation reports the orphan, reconstructs the window from the stem, and
      stops the session only when that stem says it is past its deadline.
      Deleting the file is the step, not running from another directory: whether
      a clean working directory removes it depends on `OUTPUT_DIR` being relative
      or absolute, so a test that relies on that passes without entering the
      branch.
- [ ] Two captures on the same day, checked for two archives rather than one
      superseding the other.
- [ ] Hand the resulting `.xel` to SQLFerret and confirm it ingests it. That is
      the whole point of the file and it has never been tried end to end. If it
      refuses the file, that is a finding about the target options, not about
      SQLFerret.
- [ ] `go test ./...` passes, and `git status --porcelain -uall` is clean in the
      real repository, not only in a worktree.

## The risky points, named for whoever reviews this

In the order of what they cost if they are wrong.

1. The stem carries the deadline, the ownership proof and the run identity. One
   string doing three jobs is a design smell, and the alternative, a durable
   state the server holds properly, does not exist in Extended Events. If the
   stem is alterable, truncated, or returned in a form the parse does not expect,
   all three fail together and silently. The panel already found one instance of
   that last case.
2. The sweep destroys things. It is now the only DDL that runs before consent,
   and the argument that it is safe rests entirely on the fingerprint. A
   fingerprint that matches too loosely drops a stranger's session; one that
   matches too tightly never fires. There is no middle setting to tune later.
3. Rollover loss is detectable only when nothing went wrong, since the baseline
   lives in the state file the recovery path has lost.
4. Excluding on the application name assumes the tool sets it and nothing else
   does. An application called `sql-auditor` is excluded from its own audit.
5. `HAS_PERMS_BY_NAME` was measured to agree with reality on one build, which is
   not the property `collect/preflight.go`'s rule protects.
6. The version floor is 2012 in intent and 2025 in evidence, and nothing in this
   plan runs on 2012.
7. The event count comes from a scan of the capture file, and nothing here bounds
   that work.
8. SQLFerret ingesting the file is asserted from its README and tried only in
   slice 6, which is late for the handoff that justifies the whole design.

## What the panel changed

Five readers ran the first version of this plan against SQL Server 2025. The
overlap was small and each found something no other did.

- The directory probe was a destructive action disguised as a safety check. Run
  verbatim against a session holding the managed name, it stopped and dropped
  that session, before consent and before any ownership test. The whole task is
  deleted and the measured alternative is cheaper.
- A probe that succeeds leaves a file nothing in the tool's permission set can
  delete, which makes a manifest sentence untestable.
- A capture directory that does not exist is created, not refused.
- Adding the new permission to `collect.Capabilities()` would have made every
  read-only collection on every client probe a server-altering right and print
  its denial in a security officer's manifest.
- The two guard tests this plan claimed would catch an unregistered command do
  not read `isCommand` or the dispatch at all. Adding the command to one and not
  the other leaves the package green and the binary broken.
- `ParseStem`'s ownership check was written for a bare name and every real input
  is an absolute path, so the fingerprint would never have matched and the sweep
  would never have fired.
- The deadline had two clocks. `create_time` moves on a `STOP` and `START` cycle
  while the stem does not.
- The hardcoded test counts are forbidden by this repository's own `CLAUDE.md`,
  and the warning attached to them reasoned about a package the command never
  tests.
- The verification commands ran in a hardcoded absolute path, so a reviewer in a
  worktree tested somebody else's checkout.
- Task 0 could not be executed: the spec's DDL is a template with named
  parameters and the task supplied no values, no scratch database, no directory
  and no traffic.
- `max_file_size = 1` is refused; the floor is 2.
- The command ordering was written once for five subcommands, which would have
  had `observe status` asking approval for a `CREATE EVENT SESSION`.
- The state file was declared named and never named, and the slices needed it one
  slice before it was defined.
- The shared `ZzObserve` prefix caused one reader's cleanup to drop another's
  sessions mid-measurement, for the second panel running.
