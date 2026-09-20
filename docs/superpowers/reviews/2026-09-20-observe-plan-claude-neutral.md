# Adversarial review: `2026-09-20-observe-capture-slice-1.md` at `9ce552f`

Reviewer: fresh Claude subagent, neutral prompt. Method: the plan's own artefacts
executed verbatim against the running `sql2025` container (SQL Server 2025 RTM-CU7,
17.0.4065.4), plus a throwaway copy of the tree at
`/tmp/.../claude-work/tree` for the Go experiments. The repository working tree was
not modified; `git status --porcelain -uall` is empty and `go test ./...` passes at
baseline.

Prefix used on the instance: `ZzNd2b`. Created and removed: database `ZzNd2bDB`,
event session `[ZzNd2b observe]` (several times), directories
`/var/opt/mssql/log/zznd2b`, `zznd2bprobe`, `zznd2bdeny`, `zznd2bnope`,
`zznd2bdeep`, and `/tmp/zznd2b.sql` inside the container. All removed;
`sys.server_event_sessions` is back to `AlwaysOn_health`, `system_health`,
`telemetry_xevents`, and no `ZzNd2b%` database remains.

Why not `ZzObserve`: see finding 12. The prefix the plan names was already in use
by another reader when I started.

## 1. Blocking

### 1.1 The task 0.2 probe destroys whatever holds the managed name, before consent

What the plan says. Task 0.2, first bullet: "Write the probe: create a session
under the managed name with the intended target directory, start it, stop it, drop
it." Task 2.4 wires it in: "Probe the capture directory with the throwaway session
from task 0.2", and fixes the order as "validate, connect, describe, preflight,
consent, then sweep or create". So the probe runs before consent and before the
sweep.

What I did. Rendered the probe as the four statements the plan lists, against a
managed name already held by a session I had created a moment earlier, and ran it.

What happened. The `CREATE` was refused:

```
Msg 25631, Level 16, State 1 ... The event session, "ZzNd2b observe", already exists.
```

and the three statements after it went on anyway. `DROP EVENT SESSION` succeeded.
The catalog afterwards held no `ZzNd2b%` session at all: the pre-existing session
was destroyed by the directory probe.

Two things follow, and the second bites even an implementer who checks every error.

The probe's last statement is an unconditional `DROP` of the managed name. Run
before consent and before the fingerprint has decided anything, that is the exact
action the spec forbids under "A fixed name is not proof of ownership": "a session
that does not match the managed fingerprint is never stopped, never altered, never
dropped, whatever its name". The plan states no guard, and an implementer
transcribing four statements has no reason to invent one.

Even with the `CREATE` error checked and the probe abandoned, the probe fails
whenever the managed name is taken, which is precisely the orphan case the sweep
exists for. Preflight then reports the capture directory as the problem and the run
exits 3 for a reason that has nothing to do with the directory. The plan's own
ordering guarantees this: describe happens first and sees the orphan, but nothing
tells preflight to skip the probe on what describe found.

What it should say. The probe must use a name of its own, not the managed one,
so that it cannot collide with and cannot drop a session the tool does not own.
Task 0.2 must say which name, and task 2.4 must say that the probe is skipped, not
failed, when describe found a session under the managed name.

### 1.2 The probe leaves a file on the client's disk that the tool cannot remove

What the plan says. Task 0.2, third bullet: "Decide and write down whether the probe
leaves a `.xel` file behind when it succeeds, and if so, that removing it is part of
the probe."

What I did. Ran the probe against a writable directory and listed the directory.
Ran it twice more at the same path.

What happened. The probe takes 41 ms and leaves a file:

```
-rw-rw----. 1 mssql mssql 20480 Sep 20 15:21 sql-auditor-observe-probe_0_134343912657810000.xel
```

Three probes at the same path left two files, the third having rolled the first
away because my probe set `max_rollover_files = 2`. With the table's default of 8
it would be eight.

So the answer to the "decide" is yes, and the branch the plan attaches to it cannot
be carried out. The tool has a SQL connection and the two rights in the spec's
permission table, `ALTER ANY EVENT SESSION` and `VIEW SERVER STATE`. Neither
deletes a file on the instance's disk, and the supported routes that do
(`xp_cmdshell`, the `xp_delete_*` family) are sysadmin surface the spec has spent
its whole argument refusing to ask for. "Removing it is part of the probe" is an
instruction with nothing behind it.

That is not cosmetic, because the manifest the spec drafts says:

> Nothing else on this instance was modified. The session named below was created
> and removed by this run.

A file the run created and did not remove makes that sentence false, and slice 4
says the manifest is "the spec's draft, verbatim ... Every sentence of it is a
claim the code has to make true, so there is one test per claim". There is no way
to make that claim true with a probe that writes.

What it should say. Either the probe writes to a path the operator is told about
and the manifest names the leftover, or the directory is probed some other way and
task 0.2 says which. The permission table must grow, or the manifest sentence must
change. The plan currently promises both and can keep neither.

### 1.3 A capture directory that does not exist is not refused, it is created

What the plan says. Task 0.2, second bullet, tests one direction only: "Run it
against a directory the service account cannot write to". The spec's exit code 3
covers "a capture directory the preflight probe could not write to".

What I did. Ran the same probe against `/var/opt/mssql/log/zznd2bnope`, a path I
had never created, and then against `/var/opt/mssql/log/zznd2bdeep/a/b`, two levels
deep.

What happened. No error, either time. The probe reported success, and afterwards:

```
drwxr-xr-x. 1 mssql mssql 100 Sep 20 15:22 /var/opt/mssql/log/zznd2bnope
-rw-rw----. 1 mssql mssql 20480 Sep 20 15:22 .../sql-auditor-observe-probe_0_134343913281550000.xel

/var/opt/mssql/log/zznd2bdeep
/var/opt/mssql/log/zznd2bdeep/a
/var/opt/mssql/log/zznd2bdeep/a/b
/var/opt/mssql/log/zznd2bdeep/a/b/sql-auditor-observe-probe_0_134343913550370000.xel
```

SQL Server created the directories, nested ones included.

So a typed `--file-dir /var/opt/mssq/log` passes preflight, passes consent, and the
capture lands in a directory the tool invented on the client's instance, where
nobody will look for it. And the creation happens before consent, which makes it a
second permanent object the command creates without being authorised to, on top of
1.2.

The unwritable direction does behave as the spec says, and I confirmed it: `Msg
25602` arrives at `START`, not at `CREATE`, carrying the OS error:

```
Msg 25602, Level 17, State 1 ... The operating system returned error 5:
'Access is denied.' while creating the file
'/var/opt/mssql/log/zznd2bdeny/sql-auditor-observe-probe_0_134343913275670000.xel'.
```

Task 0.2 measures that one and concludes the probe works. The other direction was
never asked for and is the one that gets a run silently misplaced.

What it should say. Task 0.2 needs a third case: a directory that does not exist.
And a decision, written down, on whether `observe` refuses a `--file-dir` that is
not already there, which is the only behaviour that makes exit code 3 mean what the
spec's table says.

## 2. Serious

### 2.1 The slice 3 guard tests do not test what the plan says they test

What the plan says, verify slice 3:

> `go test ./cmd/sql-auditor/ -run 'TestTheDiagnosisNeverPointsAtACommandThatWouldFail|TestAnArgumentLessRunSaysWhatIsMissing' -v`
> must still pass, 2 tests. They live in `cmd/sql-auditor/usage_test.go` and they
> are what a new command breaks: the first checks that the "did you mean"
> suggestion never offers a word the dispatch would refuse, which is exactly what
> happens if `isCommand` and the `switch` disagree.

What I did. Read both tests, then ran the experiment. In the scratch copy I added
`"observe"` to `isCommand` at `cmd/sql-auditor/main.go:635` and changed nothing
else: no case in the dispatch `switch cmd` at line 912. That is precisely the
`isCommand` and `switch` disagreement the plan says these tests catch.

What happened. Both tests pass. So does the whole package:

```
=== RUN   TestAnArgumentLessRunSaysWhatIsMissing
--- PASS
=== RUN   TestTheDiagnosisNeverPointsAtACommandThatWouldFail
--- PASS
ok  github.com/rudi-bruchez/sql-auditor/cmd/sql-auditor   2.991s
```

And the built binary:

```
$ ./sa observe
unknown command "observe".
... (the whole help)
exit=2
```

Reading the tests says why. Both call `nothingToDo(exists, env)`.
`TestAnArgumentLessRunSaysWhatIsMissing` checks the phrases on the screen an
argument-less run prints. `TestTheDiagnosisNeverPointsAtACommandThatWouldFail`
checks that when nothing is configured, that screen does not say
`sql-auditor check`. Neither reads `isCommand`, neither reads the dispatch, and
neither has anything to do with the "did you mean" suggestion.

This line comes from the correction commit `9ce552f`, which fixed the name of the
test ("There is no test called `TestUsage`; this plan said there was, which is what
reading the tree rather than remembering it is for") and did not check what the
test with the right name actually does. The plan's own lesson, applied halfway.

What it should say. No test in this tree pairs `isCommand` with the dispatch. Slice 3
must write one, a table over the words `isCommand` accepts asserting that each
reaches a case rather than the "unknown command" branch, and the two named tests
should be described as what they are, guards on the argument-less screen.

### 2.2 Task 2.5's verification command runs one of the two tests it names, and the file list is two short

What the plan says, task 2.5: "Check `TestEveryProbedCapabilityCanBeGranted` and
`TestCapabilityNamesMatchNormalisedPermissions` still pass", then "Run the existing
grant tests before and after: `go test ./collect/ -run 'Grant' -v`."

What I did. Ran `go test ./collect/ -run 'Grant' -v` at baseline: 20 tests, green.
`TestCapabilityNamesMatchNormalisedPermissions` is not one of them. It lives in
`collect/preflight_test.go` and its name contains no "Grant", so the filter cannot
reach it.

Then I did what task 2.5 asks: added the capability to `Capabilities()` in
`collect/preflight.go`, with `HAS_PERMS_BY_NAME(NULL, NULL, 'ALTER ANY EVENT
SESSION')` as the probe.

What happened. The plan's command reports one failure:

```
--- FAIL: TestEveryProbedCapabilityCanBeGranted
    grants_test.go:302: capability "alter_any_event_session" is probed but nothing grants it
```

`go test ./collect/` reports two:

```
--- FAIL: TestCapabilityNamesMatchNormalisedPermissions
    preflight_test.go:118: capability "alter_any_event_session" has no @permissions
    spelling that normalises back to it
```

Clearing the second needs an entry in `permissionKeys`, which is production code in
`collect/queryset.go:647`, and a case in `nameToPermission`, which is the test's own
inverse in `collect/preflight_test.go:124`. Task 2.5 names `collect/grants.go` and
`collect/preflight.go` and nothing else, so an implementer who runs the command the
plan gives sees one failure, fixes one thing, and reports the task green.

What it should say. `go test ./collect/`, the whole package, and four files:
`collect/preflight.go`, `collect/grants.go`, `collect/queryset.go`,
`collect/preflight_test.go`.

### 2.3 An observe-only capability in `Capabilities()` lands in every `collect` manifest

Reached by reading, from the code task 2.5 points at.

`Capabilities()` is the single list `collect`'s preflight probes, and
`CapabilityCheck`'s own doc comment says where the result goes: "Label is the
capability written for someone who has never read this code. MANIFEST.txt is read
by a security officer". So adding `alter_any_event_session` there makes every
`sql-auditor check` and every `sql-auditor collect` on every client instance probe
a right the collector does not need, and puts a denial for it into the manifest of a
read-only collection.

That is the spec's own argument arriving from the other side. "Why it is a separate
command" spends four paragraphs on the manifest not being allowed to carry an
exception, and the plan then routes an `observe` permission into the manifest
`collect` prints. It is also the concrete question the plan leaves unanswered: task
2.5 says "the capability and the grant are the same vocabulary" and nothing about
which command's preflight runs the probe.

The grant side has a smaller version of the same problem. Every server-scoped
section in `collect/grants.go` ends with "Collectors that need it:" followed by
`indentList(collectorsFor(in.Scripts, name))`. No corpus script declares
`alter_any_event_session`, so a section copied "in the shape it already has for
server-scoped rights" prints that heading with nothing under it.

What it should say. Whether the probe is scoped to `observe` and how, or, if it is
shared, what the `collect` manifest says about a capability `collect` does not use.

### 2.4 Task 2.2 names two different clocks for the deadline, and both exist

What the plan says. Task 2.2, bullet 1: "A session matching the fingerprint and past
the deadline in its own stem is stopped and dropped. The deadline comes from the
stem, never from the sweeping process's flags." Bullet 2: "A session matching the
fingerprint but stopped is dropped whatever its age, there being no `create_time` to
compute from."

Bullet 2's justification only holds if the age comes from `create_time`. Bullet 1
says it comes from the stem, and the stem carries a timestamp, so a stopped
session's age is perfectly computable. The two bullets cannot both be the rule.

The spec carries the same pair: line 282 says the start time is
`sys.dm_xe_sessions.create_time`, "which is the time of `STATE = START` and not of
`CREATE`"; lines 554 to 560 say the deadline travels in the stem.

What I did. Created a session whose stem said `...-20260920T152004-5.xel`, left it
defined and unstarted, then started it and read `create_time`, then stopped and
started it again.

What happened.

```
defined but never started: dm_xe_sessions rows = 0
after start:  create_time 2026-09-20 15:25:23.733
after a stop/start cycle: create_time 2026-09-20 15:25:23.767
```

Five minutes and nineteen seconds between what the stem declares and what
`create_time` reports, and a `STOP`/`START` cycle moves `create_time` again while
the stem never changes. So the two clocks are real and they diverge, and a session
somebody restarts by hand gets its deadline pushed forward indefinitely if the
sweep computes from `create_time`. In the other direction, a session created at
14:00 and started at 14:50 is swept fifty minutes early if the sweep computes from
the stem and the implementer built the stem at create time.

This matters more than it looks because the spec's "What the second panel changed"
records "There were two deadlines and no section said which it meant" as a defect
the panel fixed. The maximum moved into the stem; the start time did not.

What it should say. One sentence in task 2.2 naming which timestamp the deadline is
computed from, and a bullet in task 1.1 saying whether `Stem()` is called with the
clock reading of `CREATE` or of `START`. Then bullet 2 needs its real reason, which
is the spec's: a stopped session is a remnant and is dropped whatever its age.

### 2.5 `ParseStem`'s ownership check is applied to a value that is a full path

What the plan says. Task 1.2: "A stem that does not begin `sql-auditor-observe-`
returns `ok` false. That is the ownership check and the test says so." The two forms
it requires tested are the name with `_0_<ticks>.xel` and the name without.

What I did. Created the session as the spec renders it and read back the two places
the plan takes the stem from.

What happened. `sys.server_event_session_fields`, which the fingerprint reads:

```
event_file  target  filename  /var/opt/mssql/log/zznd2b/sql-auditor-observe-20260920T152004-5.xel
```

and the running target's `target_data`:

```xml
<EventFileTarget truncated="0"><Buffers logged="0" dropped="0"/>
<File name="/var/opt/mssql/log/zznd2b/sql-auditor-observe-20260920T152004-5_0_134343911496720000.xel"/>
</EventFileTarget>
```

Both are full paths. Neither begins `sql-auditor-observe-`. A `ParseStem` written to
the letter of task 1.2 and fed either value returns `ok` false, the fingerprint never
matches, and the sweep never fires. The failure is silent and it is exactly the one
the sweep exists to prevent.

The two forms task 1.2 lists are both bare names, so neither test catches it. The
plan says who reads the value and never says who takes the base name of it.

What it should say. A third accepted form in task 1.2, the configured full path as
`sys.server_event_session_fields` returns it, with its own test, or an explicit
sentence that the caller takes the base name and the fingerprint test uses the value
as read from the catalog.

### 2.6 The rollover-loss field has no value on the recovery path slice 5 exercises

Reached by reading the two slices against each other.

Slice 4 requires `capture.json` to carry "the recorded first file name and whether
it survived". The recording happens at start, into the state file and `_run.json`.

Slice 5 requires deleting the state file to prove the recovery branch is entered:
"Delete the state file explicitly, say so in the step, and confirm the branch under
test is the one that recovers from the session name and its stem alone."

A `finish` on that branch has no recorded first file name and cannot obtain one:
`target_data` at finish time names the current file, not the first. So the field
slice 4 makes mandatory cannot be filled, and the archive either omits it or fills
it with the wrong file, which is the "complete capture of a window whose beginning
is gone" the plan lists as risk 2.

The spec's fallback sentence covers a different case: "If the name cannot be read at
that moment, the run says rollover is unobservable" is about the read at start
failing, not about the recording being lost afterwards.

What it should say. A bullet in slice 4 saying what `capture.json` and the manifest
say when the run was reconstructed from the stem, and a check in slice 5 that the
archive of the recovered run says rollover is unobservable rather than claiming a
clean capture.

### 2.7 The application-name exclusion is a constant compared exactly, and this repository already connects under another name

What the plan says. Task 1.1: `Session` holds "the application name to exclude". The
spec's rendered DDL carries the literal `sqlserver.client_app_name <> N'sql-auditor'`.

What I found in the tree.

- `collect/config.go:148`: `const DefaultAppName = "sql-auditor"`, which matches.
- `collect/config.go:493`: `AppName: get("SQL_APPLICATION_NAME", DefaultAppName)`.
  The operator overrides it, and `config.go:75` records that
  `SQL_APPLICATION_NAME=sql-auditor` written out explicitly is a legitimate thing to
  write, so the code already distinguishes the two.
- `collect/watch.go:377`: `wcfg.AppName = cfg.AppName + watchAppSuffix`, where
  `watchAppSuffix = " (blocking watch)"`. The tool already opens a second connection
  under a name that is not exactly `sql-auditor`.

So two holes. If the literal is the constant rather than `cfg.AppName`, an operator
who set `SQL_APPLICATION_NAME` gets their own traffic captured and somebody else's
excluded. And any second connection `observe` opens the way `watch` does is not
excluded by an exact `<>`, because the name is a prefix match away, not an equality.

The spec names the first of these as risk 4 in the plan's own list. Nothing in the
plan says where the value comes from, and "the application name to exclude" as a
field is where an implementer stops thinking about it.

What it should say. Task 1.1 must say the value is `cfg.AppName` read at render
time, and the consent prompt must show the literal it will use. If `observe` opens
more than one connection, the exclusion is a prefix or every connection uses the
same name, and the plan has to pick.

## 3. Smaller

### 3.1 Both "do not use this filter" warnings reason about a scope the commands do not have

What the plan says. Slice 1: "Do not use `^TestObserve` as a filter: measured
against this tree, it already matches two tests of `collect/observer.go` and would
certify a short implementation as complete." Slice 4: "Do not use `-run 'Archive'`
unanchored: `collect` already has `archive_test.go` and the count would be
meaningless."

What I did. Put three `TestObserveSession*` functions into `collect/observe` in the
scratch copy and ran both filters with the package path the plan gives.

What happened.

```
go test ./collect/observe/ -run '^TestObserveSession'  -> 3
go test ./collect/observe/ -run '^TestObserve'         -> 3
go test ./collect/        -run '^TestObserve'          -> TestObserverCallbacksAreSafeOnTheZeroValue,
                                                          TestObserverForwardsToTheWrappedImplementation
go test ./collect/        -run 'Archive'               -> 4
```

The two tests the first warning names are in package `collect`, and every verify
command in the plan scopes to `./collect/observe/`, a different package. `-run` never
crosses that boundary. The hazard is real for `./collect/` and cannot occur for the
commands the plan actually gives. Same for the archive warning.

Harmless in effect, but it is two paragraphs of false reasoning sitting in the part
of the document the implementer is told to trust, and one of them carries the word
"measured".

### 3.2 The test counts are not derivable and `-v` counts subtests

Slice 1 expects 12, slice 2 expects 14 and 4, slice 3 expects 9, slice 4 expects 11,
each with "A lower count means your `-run` filter is wrong, not that the task is
done".

Counting the assertions slice 1's three tasks actually name gives at most eleven:
one for `Stem`, four for the four SQL builders, two for the two spellings, four for
`ParseStem`, four for `Matches`, minus whatever an implementer merges. Nothing in
the plan adds up to twelve, so the only way to reconcile is to split or merge tests
until the number comes out, and the sentence quoted above forbids reporting the
honest result.

`go test -v` also prints one `=== RUN` line per subtest. A table-driven test with
`t.Run`, which is this repository's style in several files, makes any count
ambiguous unless the plan says whether it means top-level functions or run lines.

This is the repository's own rule turned on the plan. `CLAUDE.md`: "A hardcoded
number is a golden test written in the worst available format ... it says 'got 74,
want 75', names nothing, and is blind to a rename."

What it should say. The list of assertions each task owes, and one sentence saying a
table under one `t.Run` counts once.

### 3.3 `max_file_size` has a floor task 0.1 does not give

Task 0.1 says "with a small `max_file_size` so rollover is reachable" and leaves the
value to the implementer. The obvious smallest value is refused:

```
max_file_size = 1
Msg 25641, Level 16, State 1 ... For target, "package0.event_file", the parameter
"max_file_size" passed is invalid. Target parameter at index 1 is invalid
```

2 and 3 both create. So the floor is 2 MB on 17.0.4065.4. One number in the task
saves the implementer a round trip, and it belongs in the measurements document
anyway since open question 2 is about this target's options.

### 3.4 A failed `START` makes the probe's `STOP` raise as well

In the unwritable-directory run, `Msg 25602` at `START` was followed by a second
error at the next statement:

```
Msg 25704, Level 16, State 1 ... The event session has already been stopped.
```

An implementer whose probe is create, start, stop, drop with every error checked
will treat 25704 as a failure of its own and report the wrong one. Task 0.2 should
say the stop is expected to raise after a failed start, and which message the
refusal quotes.

### 3.5 `OUTPUT_DIR` is relative by default, so the correction's premise is false in the default case

Slice 5 says: "'run it from a clean working directory' does not make it gone: the
file is under `OUTPUT_DIR`, which a clean working directory does not touch."

`collect/config.go:501` reads `OutputDir: get("OUTPUT_DIR", "output")`, a relative
path. With the default, a clean working directory gets a fresh `./output` and the
state file is gone, which is the opposite of what the line says. The sentence is
true only when the operator has set `OUTPUT_DIR` to an absolute path.

The instruction that follows, delete the state file explicitly, is right either way,
so the cost is small: an implementer running with the default will find the plan
contradicting the machine and will not know which to believe. Worth noting that this
line is from the correction commit `9ce552f`, which is the least-reviewed line in
the document by the plan's own argument.

### 3.6 The `ZzObserve` prefix is shared, and the closing check was already false before I started

The plan says "Everything you create is named with a `ZzObserve` prefix and dropped
before you finish, and the last thing you do is confirm `sys.server_event_sessions`
holds only the three system sessions."

The first thing I ran, before creating anything, returned five:

```
AlwaysOn_health
system_health
telemetry_xevents
ZzObserve_sql_auditor_observe
ZzObserve_test
```

The last two belong to another reader following the same instruction. A prefix the
document fixes for everybody is not a prefix, it is a shared namespace, and the
closing check is an assertion about the whole instance rather than about the work
the performer did. I used `ZzNd2b` and left the other reader's objects alone; they
were gone by the time I finished, cleaned up by whoever made them.

What it should say. A prefix chosen per run, and a closing check scoped to that
prefix rather than to the instance.

## What measured clean, and is worth recording as such

The plan lists the rollover-loss detection as risk 2, "measured in task 0.1 or it is
a paragraph". It measures clean in both directions, so the spec's fallback sentence
is not needed.

Immediately after `STATE = START`, with no events yet flushed, the running target
names its current file, full path, `_0_<ticks>` suffix included:

```xml
<EventFileTarget truncated="0"><Buffers logged="0" dropped="0"/>
<File name="/var/opt/mssql/log/zznd2b/sql-auditor-observe-20260920T151849-5_0_134343911496720000.xel"/>
</EventFileTarget>
```

Rollover case, `max_file_size = 2`, `max_rollover_files = 2`, 1500 batches of about
7 KB of text each: two files left, neither of them the recorded one, and the
wildcard read over `<stem>*.xel` returned 187 events with the recorded name absent
from its `file_name` column. Detection fires.

No-rollover case, `max_file_size = 128`, `max_rollover_files = 8`, 20 batches: the
recorded name is still the only file in the set, 21 events. No false positive.

Also confirmed against the instance:

- the spec's rendered DDL compiles verbatim on 17.0.4065.4, with no comma before
  `ADD TARGET` and the bare `object_name`;
- the bare `database_id` in the predicate resolves to
  `<global name="database_id" package="sqlserver">` in `predicate_xml`, so the spec's
  prose and its rendered DDL agree;
- `Msg 25602` arrives at `START` and not at `CREATE`, as the spec says;
- already-exists at `CREATE` is `Msg 25631` and is an ordinary catchable error, as
  task 2.3 says;
- a session defined and never started has no row in `sys.dm_xe_sessions`, which is
  the third state task 2.1 requires.

And against the tree:

- `ALTER ANY EVENT SESSION` appears in no `.go` and no `.sql` file;
- `collect/cancel.go` is 65 lines;
- `cmd/sql-auditor/main.go:750` is exactly the condition the plan quotes;
- `^TestObserveCommand` matches nothing in `cmd/sql-auditor` today;
- `collect/watch_live_test.go` uses the three variables named, in `host,port` form,
  and its single test is `TestLiveWatchRecordsAWait`;
- `RunFolderName` formats `2006-01-02` and `FailedRunFolderName` is the
  time-granular precedent beside it;
- `Zip(runFolder, destZip string) error` takes a folder and a destination and knows
  nothing about collection.

## One question pushed further than the plan asks

The plan stops at "does the running target name its current file". It does. The
question behind it, which decides whether the detection is worth its code, is
whether the name can still be had when it matters, and there the answer is no: the
recovery path in slice 5 deletes the only two places the name is stored. That is
finding 2.6. A detection that works in the case where nothing went wrong, and is
unavailable in the case where the process died, buys less than the plan's risk list
credits it with.
