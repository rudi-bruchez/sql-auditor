# `sql-auditor observe`, capture slice 1: implementation plan

> For agentic workers: required sub-skill, superpowers:subagent-driven-development (recommended) or superpowers:executing-plans, task by task. Steps use checkbox (`- [ ]`) syntax for tracking.

The authority is `docs/observe-spec.md` at commit `171ca87`, after its second
panel. This plan replaces `2026-09-20-observe-slice-1.md`, which planned the
histogram design and is marked superseded.

If your own measurement contradicts this plan or the spec, stop and say so. Do
not make the code fit the brief. Four of the defects the last two panels found
were in briefs like this one, and every one of them was found by executing what
the brief said rather than by reading it.

## What this slice delivers

`observe --minutes N --database D`, `observe start`, `observe status`,
`observe finish` and `observe stop`, producing a capture file on the instance
and a small archive on the operator's machine that says where it is and what it
holds.

Out of scope, deliberately, each of them a surface that would double this one:
pulling the file back with `OPENROWSET`, borrowing an existing session, a SQL
Agent job as a deadline enforcer, and any analysis of the capture, which is
SQLFerret's.

## Before you write any code

Two things in the spec are described rather than measured, and both are
load-bearing for code in slice 2. They are cheap and they come first, for the
reason slice 0 existed in the superseded plan: a measurement that arrives after
the code it invalidates is paid for twice.

The container `sql2025` is a SQL Server 2025 RTM-CU7 on port 11533 of the host.
The password comes only from `podman exec sql2025 printenv MSSQL_SA_PASSWORD`,
piped, never printed and never written to a file. Everything you create is named
with a `ZzObserve` prefix and dropped before you finish, and the last thing you
do is confirm `sys.server_event_sessions` holds only the three system sessions.

### Task 0.1: can the running target name its current file?

The spec's rollover-loss detection rests on one unmeasured sentence: that
immediately after the session starts, the running target's current file name can
be read from `target_data` and stored. If it cannot, rollover loss is
undetectable and the spec has to say so instead.

- [ ] Create the session exactly as the spec renders it, with a small
      `max_file_size` so rollover is reachable, and start it.
- [ ] Read `sys.dm_xe_session_targets.target_data` for the `event_file` target
      and find out whether it names the file currently being written, including
      the `_0_<ticks>` suffix. Record the exact XML shape.
- [ ] Drive enough traffic to roll over at least twice, so at least one file is
      deleted. Then read the wildcard set and check that the name recorded at
      the start is absent. That is the detection the spec specifies; prove it
      fires.
- [ ] Also check the case that must not produce a false positive: a run that
      does not roll over at all, where the recorded name must still be present.
- [ ] Write `docs/observe-capture-measurements.md` with both results, the build,
      and the XML verbatim.

If the target does not name its file, stop and say so. The spec has a fallback
sentence for that case and it changes the manifest, not the code.

### Task 0.2: does the directory probe work, and does it cost anything visible?

The spec's exit code 3 depends on a preflight that tests the capture directory by
creating a throwaway session there and dropping it. That is DDL before consent,
which is the one thing this command is careful about, so it has to be exactly as
small as claimed.

- [ ] Write the probe: create a session under the managed name with the intended
      target directory, start it, stop it, drop it. Measure how long it takes.
- [ ] Run it against a directory the service account cannot write to and record
      the exact message and where it arrives. The spec says `Msg 25602` at
      `START` and not at `CREATE`; confirm or contradict.
- [ ] Decide and write down whether the probe leaves a `.xel` file behind when it
      succeeds, and if so, that removing it is part of the probe.
- [ ] Append the result to `docs/observe-capture-measurements.md`.

- [ ] Commit both measurements before writing any Go.

## Slice 1: the session, as text

Everything here is a pure function. No connection, no server, no I/O. This is the
part the previous design got wrong by pinning an interface before measuring, so
the interface comes after task 0.

Files: create `collect/observe/session.go` and `collect/observe/session_test.go`.
A new package rather than `collect/observe.go`, because `collect/observer.go`
already defines the `Observer` interface that watches a run, and the two names
differ by one letter. Do not rename `Observer` in this slice; that is its own
commit and its own review.

### Task 1.1: render the DDL

- [ ] `Session` holds what the spec's parameter table lists: database id, capture
      directory, stem, max memory, dispatch latency, file size, rollover files,
      and the application name to exclude.
- [ ] `Stem()` builds `sql-auditor-observe-<utc yyyyMMddTHHmmss>-<max minutes>.xel`
      from a clock passed in, never `time.Now()` read inside. The clock is a
      parameter so the test is not a race.
- [ ] `CreateSQL()`, `StartSQL()`, `StopSQL()`, `DropSQL()` return the exact
      statements the spec renders. The session name is bracketed, the name
      containing a space.
- [ ] The two spellings the spec establishes by measurement are not negotiable
      and each gets a test naming why: no comma between the last `ADD EVENT` and
      `ADD TARGET`, and `object_name` rather than `sqlserver.object_name`.

### Task 1.2: parse a stem back

The sweep reads the deadline out of the file name, from a machine that has lost
everything local. That parse is the whole self-healing story.

- [ ] `ParseStem(string) (startedAt time.Time, maxMinutes int, ok bool)`.
- [ ] It accepts the name with the `_0_<ticks>.xel` suffix SQL Server appends,
      since that is the form read back from the target, and the form without it,
      since that is the form in the session definition. Both, tested.
- [ ] A stem that does not begin `sql-auditor-observe-` returns `ok` false. That
      is the ownership check and the test says so.
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

Run setup and tests in one shell invocation. State persists across nothing.

```
cd /home/rudi/Sources/Repos/sql-auditor-workspace/sql-auditor && \
  go build ./... && go test ./collect/observe/ -run '^TestObserveSession' -v 2>&1 | tail -40
```

- [ ] Expect 12 tests. A lower count means your `-run` filter is wrong, not that
      the task is done. Do not use `^TestObserve` as a filter: measured against
      this tree, it already matches two tests of `collect/observer.go` and would
      certify a short implementation as complete.
- [ ] Break the code on purpose and watch the tests fall: put the comma back
      before `ADD TARGET`, and change `object_name` to `sqlserver.object_name`.
      Report which tests failed and how many. Two failures out of twelve is this
      step succeeding, not failing. Then restore.

## Slice 2: the lifecycle, against a real instance

This is where the panel found almost everything, so read
`docs/observe-spec.md` sections "A fixed name is not proof of ownership", "Two
operators can still collide", "Ordering, which decides whether exit code 3 tells
the truth" and "Preflight, and why it departs from the collector's rule" before
writing a line.

Files: `collect/observe/lifecycle.go`, `collect/observe/lifecycle_test.go`,
`collect/observe/live_test.go`.

### Task 2.1: read the instance without altering it

- [ ] `Describe(ctx, db) (*Observed, error)` reads any session under the managed
      name from `sys.server_event_sessions`,
      `sys.server_event_session_events`, `sys.server_event_session_actions` and
      `sys.server_event_session_fields`, plus `sys.dm_xe_sessions` for
      `create_time` and the counters.
- [ ] It alters nothing. That is the ordering rule and there is a test that the
      function issues no DDL, by running it against a fake connection that fails
      any statement that is not a `SELECT`.
- [ ] Three states, all of them distinguished and named: absent, defined and
      running, defined and stopped. The third is what a service restart leaves
      and it has no `create_time`.

### Task 2.2: the sweep

- [ ] A session matching the fingerprint and past the deadline in its own stem is
      stopped and dropped. The deadline comes from the stem, never from the
      sweeping process's flags.
- [ ] A session matching the fingerprint but stopped is dropped whatever its age,
      there being no `create_time` to compute from.
- [ ] A session not matching the fingerprint is never touched, whatever its name
      or age.
- [ ] A session matching and within its deadline is left alone and reported.
- [ ] The teardown runs on a context derived from `context.Background()` with its
      own timeout, never on the caller's. Test it with a cancelled context: the
      drop must still happen. This is the finding the superseded plan got wrong
      by pointing at `collect/cancel.go`, which is 65 lines that classify an
      error and touch no server.

### Task 2.3: the collision

- [ ] `CREATE` returning already-exists is an ordinary refusal: re-read, describe
      the winner, stop nothing. Measured on 2025 it is `Msg 25631` and it is
      catchable, so the driver surfaces it as an ordinary query error.
- [ ] If the re-read fails, or what it finds does not match the fingerprint, the
      refusal says that instead of guessing.

### Task 2.4: preflight

- [ ] Probe `ALTER ANY EVENT SESSION` with `HAS_PERMS_BY_NAME`, and put the
      comment the spec asks for: this departs from `collect/preflight.go`'s rule
      that a probe reads a real object, because the only faithful probe is the
      DDL that must not run before consent.
- [ ] Probe `VIEW SERVER STATE` the way `Capabilities()` already does.
- [ ] Probe the capture directory with the throwaway session from task 0.2.
- [ ] Order: validate, connect, describe, preflight, consent, then sweep or
      create. There is a test that asserts the order by recording the calls a
      fake connection receives.

### Verify slice 2

```
cd /home/rudi/Sources/Repos/sql-auditor-workspace/sql-auditor && \
  go test ./collect/observe/ -run '^TestObserveLifecycle' -v 2>&1 | tail -40 && \
  SQL_AUDITOR_LIVE_SERVER=localhost,11533 SQL_AUDITOR_LIVE_USER=sa \
  SQL_AUDITOR_LIVE_PASSWORD="$(podman exec sql2025 printenv MSSQL_SA_PASSWORD)" \
  go test ./collect/observe/ -run '^TestLiveObserve' -v 2>&1 | tail -30
```

- [ ] Expect 14 tests without a server and 4 with one. The live guard is the one
      `collect/watch_live_test.go` already uses, read from that file rather than
      remembered: three variables, `SQL_AUDITOR_LIVE_SERVER` in
      `host,port` form, `SQL_AUDITOR_LIVE_USER` and `SQL_AUDITOR_LIVE_PASSWORD`,
      and its single test is `TestLiveWatchRecordsAWait`. Follow that naming, so
      the live tests here are `TestLiveObserve...` and the filter above finds
      them and nothing else.
- [ ] The live tests are the ones that matter and they are the ones a fake cannot
      replace. At minimum: a same-named session that is not ours survives a
      sweep, a stopped remnant is dropped, and a drop succeeds on a cancelled
      caller context.
- [ ] Break the code on purpose: make the sweep read the deadline from a
      parameter instead of the stem. The test that catches it is the one where
      the sweeping process passes a smaller maximum than the session declared.
      If nothing fails, your test is not testing that, and say so.

## Slice 3: the commands

Files: `cmd/sql-auditor/observe.go`, `cmd/sql-auditor/observe_test.go`, and edits
to `cmd/sql-auditor/main.go`.

### Task 3.1: wire the command

- [ ] `isCommand` in `cmd/sql-auditor/main.go` gains `observe`. It is the single
      list the "did you mean" suggestion reads, and a command missing from it is
      suggested and then refused.
- [ ] The dispatch `switch cmd` gains its case, beside `collect` and `check`.
- [ ] Subcommands: bare, `start`, `status`, `finish`, `stop`. An unknown one is a
      refusal naming the five.
- [ ] Flags: `--minutes`, `--for`, `--database`, `--file-dir`, `--max-minutes`
      defaulting to 60, `--yes`. Minutes are integers; a fractional value is
      refused rather than truncated.

### Task 3.2: consent

- [ ] The prompt prints the rendered DDL with every parameter substituted, the
      two permissions, the capture path, and the expected call count with the
      size labelled an estimate.
- [ ] `--yes` skips the prompt and nothing else.
- [ ] What is recorded in `_run.json` is the statement as executed, captured at
      execution. Not rebuilt afterwards from the same builder: a rebuild proves
      the builder is deterministic, which nobody doubted, and not that the
      prompt told the truth. There is a test that mutates the statement between
      render and execute and asserts the two differ in the record.

### Task 3.3: cancellation

- [ ] Ctrl-C stops, drops, and writes a partial archive saying it was
      interrupted. The teardown uses the fresh context from task 2.2.
- [ ] The first Ctrl-C message names what a second one will leave behind: a
      running session and a growing file. `interruptibleOn` in
      `cmd/sql-auditor/main.go` is where the existing wording lives and the
      observe path needs its own, because `collect`'s message promises an
      archive of what was collected and says nothing about a server object.
- [ ] The session name and the capture path are on screen before the wait starts,
      not only in the archive.

### Verify slice 3

```
cd /home/rudi/Sources/Repos/sql-auditor-workspace/sql-auditor && \
  go test ./cmd/sql-auditor/ -run '^TestObserveCommand' -v 2>&1 | tail -30
```

- [ ] Expect 9 tests. Measured against this tree, `^TestObserveCommand` matches
      nothing today, so every test counted is one you wrote.
- [ ] `go test ./cmd/sql-auditor/ -run 'TestTheDiagnosisNeverPointsAtACommandThatWouldFail|TestAnArgumentLessRunSaysWhatIsMissing' -v`
      must still pass, 2 tests. They live in `cmd/sql-auditor/usage_test.go` and
      they are what a new command breaks: the first checks that the "did you
      mean" suggestion never offers a word the dispatch would refuse, which is
      exactly what happens if `isCommand` and the `switch` disagree. There is no
      test called `TestUsage`; this plan said there was, which is what reading
      the tree rather than remembering it is for.

## Slice 4: the archive

Files: `collect/observe/archive.go`, `collect/observe/archive_test.go`.

- [ ] A separate manifest type in this package. Do not add a branch to
      `collect/manifest.go`: it is built around one `Manifest` whose `Human()`
      prints the read-only claim, and `observe` needs a manifest that says the
      opposite. The spec's argument is that the approver's choice must not become
      a flag inside a function.
- [ ] `Zip` from `collect/archive.go` is reused unchanged. It takes a folder and
      a destination and knows nothing about collection.
- [ ] The manifest text is the spec's draft, verbatim, including the paragraph
      saying the capture holds the operator's own statement text and stays on
      their disk. Every sentence of it is a claim the code has to make true, so
      there is one test per claim.
- [ ] `capture.json`: the path, the file set, the event count, the counters read
      before the stop, the recorded first file name and whether it survived.
- [ ] `_run.json`: the executed statements, the window, the exit classification.
- [ ] The window a late `finish` reports is the one it really measured, with the
      overrun named. Test a `finish` arriving after the deadline.

### Verify slice 4

```
cd /home/rudi/Sources/Repos/sql-auditor-workspace/sql-auditor && \
  go test ./collect/observe/ -run '^TestObserveArchive' -v 2>&1 | tail -30
```

- [ ] Expect 11 tests. Do not use `-run 'Archive'` unanchored: `collect` already
      has `archive_test.go` and the count would be meaningless.

## Slice 5: end to end, on a real instance

- [ ] `observe --minutes 1 --database <a scratch database>` against `sql2025`,
      driving a known number of calls from a Go client with a known application
      name, and a second client named `sql-auditor` whose calls must not appear.
- [ ] Check the count against what you drove, and check it twice: once for the
      total, once for the absence of `sp_reset_connection` and of the excluded
      application. A total that matches while an exclusion silently failed is the
      failure mode this whole design has been bitten by twice.
- [ ] `start`, then `status` while it runs, then `finish`. Then `start`, kill the
      process, and check that the next invocation reports the orphan and stops it
      only when its own stem says it is past its deadline.
- [ ] Hand the resulting `.xel` to SQLFerret and confirm it ingests it. That is
      the whole point of the file and it has never been tried end to end. If it
      refuses the file, that is a finding about the target options, not about
      SQLFerret.
- [ ] `go test ./...` passes, and `git status --porcelain -uall` is clean in the
      real repository, not only in a worktree.

## The risky points, named for whoever reviews this

In the order of what they cost if they are wrong.

1. The stem carries the deadline, the ownership proof and the run's identity.
   One string doing three jobs is a design smell, and the alternative, a state
   the server holds properly, does not exist in Extended Events. If the stem
   turns out to be alterable or truncated anywhere, all three fail together.
2. The rollover-loss detection is measured in task 0.1 or it is a paragraph. It
   is the one place where the archive can claim a complete capture of a window
   whose beginning is gone.
3. The preflight directory probe is DDL before consent. It is small and it is
   still the one place the command touches the instance before anybody said yes.
4. Excluding on the application name assumes the tool sets it and that nothing
   else does. An application that happens to be called `sql-auditor` is excluded
   from its own audit, silently.
5. `HAS_PERMS_BY_NAME` was measured to agree with reality on one build, which is
   not the property `collect/preflight.go`'s rule protects.
6. The version floor is 2012 in intent and 2025 in evidence. Nothing in this plan
   runs on 2012, and the spec now says the command refuses a build it was not
   measured on. That refusal is a decision somebody should look at.
7. The event count comes from a scan of the capture file. On a large capture that
   is real work, and nothing here bounds it.
8. SQLFerret ingesting the file is asserted from its README and tried only in
   slice 5, which is late for a handoff that justifies the whole design.
