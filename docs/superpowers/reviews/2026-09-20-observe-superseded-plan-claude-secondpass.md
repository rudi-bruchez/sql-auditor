# Adversarial review: `observe`, slice 1

Reviewed: `docs/superpowers/plans/2026-09-20-observe-slice-1.md` against
`docs/observe-spec.md` and against the tree at `4ed20d8`.

This is the second pass. It was run because a single reading of a plan is a
sample, not a verdict, and it found five defects the first reading did not,
one of which corrects the first reading. The file carries both passes, because
the union is the deliverable and an earlier finding does not stop being true
when a later one arrives. Provenance is marked where it matters.

Measurements were taken against `sql2025` (Microsoft SQL Server 2025 RTM-CU7,
17.0.4065) and against the repository. Nothing here is inferred from how
Extended Events are supposed to behave. Everything created on the instance was
dropped: `sys.server_event_sessions` holds only `system_health`,
`AlwaysOn_health` and `telemetry_xevents`.

Severities: blocking means the slice cannot ship as written and the defect is
in the plan rather than in an implementation of it. Serious means the code gets
written twice. Smaller means it costs a paragraph now and an argument later.

## Correction to the first pass

The first pass asserted that the histogram's `truncated` attribute is its
overflow counter, and recommended renaming the decoder field to avoid a
collision with document truncation. The collision is real. The premise was
wrong, and wrong in the direction that matters: there is no overflow counter at
all. N1 below replaces that finding and is worse than it.

This is the class of error the repository's own instructions warn about, a line
written to fix a reviewer's finding getting less scrutiny than the original.
It was caught by running the case rather than by rereading the sentence.

## Summary

The plan is careful where careful is cheap and confident where it is expensive.
Its instinct is right: slice 0 exists because two facts decide everything. But
slice 0 measures the wrong two facts, and its prescribed measurement cannot
observe the failure that is actually there.

Measured, the capture the spec designs returns one bucket containing every
event, with the value zero. And where it does return distinct buckets, it
silently merges statements past its slot count and reports a real hash for the
merged bucket, so resolution succeeds and attributes six statements' executions
to one of them. Both failures arrive with exit code 0 and a manifest saying the
capture ran. They are the answer-that-looks-like-an-answer the spec spends six
paragraphs being afraid of, reached through the two paths neither document
checked.

## Blocking, new in the second pass

### N1. Slot overflow is invisible, and the folded bucket resolves to a real statement

The spec instructs:

> Past that the target does not drop events: it folds different statements into
> one bucket, which corrupts the single number this command exists to produce.
> Set it explicitly, sized for the instance, and put the target's overflow
> attributes in the manifest beside the drop count.

The first half is correct. The second half asks for attributes that do not
exist. One workload of nine distinct statement shapes, run twice against the
same session definition with only `slots` changed:

| `slots` requested | distinct slots returned | total events counted | `truncated` |
| --- | --- | --- | --- |
| 256 | 9 | 9 | `0` |
| 2 | 2 | 9 | `0` |

Three facts follow, and the third is the expensive one.

Nothing was dropped. Both runs counted nine events, which confirms the spec's
first sentence.

Seven of nine shapes disappeared and the target said nothing. `truncated`
stayed at `0` through a 78 percent loss of granularity. The histogram target on
this build exposes exactly two attributes, `truncated` and `buckets`, and
neither moves when folding happens. There is no overflow attribute to put in
the manifest, so the spec's instruction cannot be carried out and slice 1's
interface, which declares `Slots int` and `NotFiltered int64`, is shaped around
a document that does not arrive. Slice 1 step 4 asks the implementer to break
"the overflow attribute" as the proof that the decoder reads it. There is
nothing there to break.

The surviving bucket carries a real hash. With two slots the values returned
were `376380086247690154` and `13574406096104372066`, both genuine
`query_hash` values of statements in the workload. So resolution will find
them, name the two statements, and silently credit them with the executions of
the seven that were folded in. The output is not an unresolvable bucket a
reader might question. It is a confident, wrong attribution in the one column
the report exists to print, and there is no counter anywhere in the archive
that contradicts it.

The only signal available is indirect, and the plan has to be told to compute
it: if the number of distinct slots returned equals the target's capacity, the
capture is at or past its limit and folding must be assumed. That comparison
needs the effective capacity, which is N2.

### N2. The slot count the tool asks for is not the slot count it gets

`slots` is rounded up to a power of two, and the two values are readable from
two different places that disagree. Measured, one session created with
`slots = 300`:

| Source | Value |
| --- | --- |
| `sys.server_event_session_fields` for the target | `300` |
| `target_data`, `<HistogramTarget buckets=...>` | `512` |

The catalog keeps what was asked for. The target reports what it holds. Slice 4
step 3 says the manifest carries "the session options as created", which is
ambiguous between the two and will be resolved by whichever query the
implementer writes first. The natural one is the catalog, because that is where
`queries/10.system/062.xe-sessions.sql` already reads session configuration
from, and it is the wrong one: it understates capacity by 70 percent here, and
it makes the N1 comparison impossible, since a capture returning 300 distinct
slots out of a real 512 is not at its limit and one returning 512 is.

Record the effective `buckets` from `target_data`. Record the requested value
too if it is wanted, but the manifest must not print one number for both.

### N3. The dispatch the plan waves at is a closed list and a hard-coded pair, and the second one already documents the bug `observe start` would reintroduce

Slice 3 lists "Modify: `cmd/sql-auditor/main.go` (dispatch, help)" and names
nothing inside it. Two specific places have to change and one of them is a
trap the repository has already sprung once.

`isCommand` is a closed switch:

```go
case "check", "collect", "env", "queries", "version":
```

An unregistered `observe` reaches it and is answered with a "did you mean"
suggestion. That much is merely a line to add.

The subcommand split is the trap, at `cmd/sql-auditor/main.go:750`:

```go
if (cmd == "queries" || cmd == "env") && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
    sub, args = args[0], args[1:]
}
```

with the comment above it explaining why it exists:

> "queries export" is the one command with a subcommand, and flag.Parse stops
> at the first non-flag argument, so parsing os.Args[2:] whole leaves --to
> unset and the export refuses a destination the user did supply.

`observe start --for 2` is that shape exactly. Parsed without being added to
this condition, `flag.Parse` stops at `start`, `--for` is never seen, and the
command runs with the deadline unset. The plan's slice 3 step 3 is entirely
about `--for` behaving correctly, and the flag will not reach the code at all
until this line names `observe`. A step that tests the deadline logic against a
value the parser discarded tests the default.

The plan's own global constraint says to put the real names of the tree into
the task. This is the line those names were for.

### N4. `ALTER ANY EVENT SESSION` exists nowhere in the repository, and no slice adds it

The spec is explicit about who approves and what they grant:

> The consent prompt prints the permissions next to the DDL. Preflight checks
> them before the session is created, the way `collect/preflight.go` already
> does for the collector, and `collect/grants.go` grows the corresponding
> `GRANT`. Without that, "the tool refuses rather than degrades" is a promise
> with nothing behind it.

Searched across every `.go` and `.sql` file in the tree: the string
`ALTER ANY EVENT SESSION` does not appear. `collect/grants.go` is 28 kilobytes
of rights, each with the reason it is narrower or wider than the obvious one,
and none of them is this one.

`collect/grants.go` is named in no slice of the plan. Neither is
`collect/preflight.go`. Slice 3 step 1 says "print the DDL and the permissions"
and stops there, which produces a prompt that lists a right the tool cannot
help anyone obtain. The path a locked-down instance actually takes is
`sql-auditor check --grant-script grants.sql`, handed to whoever can run it,
and a script that omits `ALTER ANY EVENT SESSION` sends the auditor back for a
second round trip with the one right the command needs.

Add both files to a slice and say which. The grant is server-scoped, which
`grants.go` already has a shape for.

### N5. The state file has no location, so slice 5 step 3 cannot be performed

The spec places it "next to the archive directory". The plan's slice 3 step 2
says what it binds, server, database and session name, and never where it
lives. The archive directory is `OUTPUT_DIR`, a configuration key, not the
working directory.

Slice 5 step 3 reads: "kill the tool mid-capture, run `observe status` from a
clean working directory, and confirm the session is found, reported and
stopped when past its deadline." The step's whole point is that the state file
is gone. Whether it is gone depends on where it was written: under a configured
`OUTPUT_DIR` it survives a clean working directory and the test proves nothing,
and the branch it was written to exercise, recovery by session name alone, is
never entered. The step passes either way and means something different in each
case.

Fix the location in slice 3, then say in slice 5 step 3 what has to be removed
to reach the branch under test.

Related and unfixed by the plan: the archive name. The spec asks for
`observe-<server>-<date>-<time>.zip`. The collector's `RunFolderName` in
`collect/output.go` produces `<server>-<date>` with day granularity, and
`collect.go` supersedes an existing run of the same name by renaming it aside.
Two captures in one day are the normal case for `observe` and would collide
under the collector's scheme. The spec's added time component is the right
answer; no slice says which function produces it, and reusing `RunFolderName`
would silently supersede the morning's capture.

## Blocking, carried from the first pass

### B1. The capture counts nothing, because `query_hash` is zero on both chosen events

The spec picks `rpc_completed` and `sql_batch_completed` and bucketizes on the
`sqlserver.query_hash` action. Measured with a ring buffer target so the raw
action value is visible:

| Event | `query_hash` action | Events |
| --- | --- | --- |
| `rpc_completed` | `0` | 80 |
| `sql_batch_completed` | `0` | 3 |
| `sql_statement_completed` | `4594718534688251656` | 12 |

`query_hash_signed` was collected alongside and is also zero on the same two
events. The statements were ordinary: `SELECT COUNT(*) FROM dbo.Orders WHERE
id = @p1` sent as parameterised RPCs from the repository's own driver, and the
literal form sent as separate batches. The same statements carry a real hash
where resolution will look for it, `sys.dm_exec_query_stats.query_hash =
0x3FC3B829D6FF1F08 = 4594718534688251656`.

So the hash exists, the resolution target is populated, and the capture cannot
see it. `statements.json` comes out empty and `unresolved.json` holds one row:
hash zero, count equal to the whole workload.

Neither of the two chosen events has a `query_hash` data column on this build,
which confirms the spec's claim on that point. The problem is that the action
offered in its place is empty at the moment these two events fire.

The workaround costs a decision the spec makes deliberately.
`sql_statement_completed` and `sp_statement_completed` do carry the hash, and
the spec rejects them by name: "Capturing `sql_statement_completed` and
`sp_statement_completed` would see inside the batch and multiply the event
volume by whatever the batch does, which is the trade this command declines by
default." That is no longer a trade. It is the only path to a non-zero hash.

Three shapes the decision can take, and it is a spec decision rather than a
plan one:

1. Bucketize on statement-level events and accept the volume, and the change in
   what "one call" means. The spec's sentence about not seeing inside a batch
   becomes false and the report's phrasing changes with it.
2. Keep the two call-level events and bucketize on something they carry. That
   gives a count per batch shape, and resolution against `query_hash` stops
   being possible.
3. Verify on the version floor the spec claims, SQL Server 2012, before
   concluding. This may be a regression rather than the design of the action.
   That is a measurement and it belongs in slice 0.

### B2. Stopping the session destroys the result, and the plan stops before reading

Slice 3 step 1 orders the timed run "create, start, wait, stop, read, drop,
write". Measured on one session:

| Moment | Rows in `sys.dm_xe_sessions` | Rows in `sys.dm_xe_session_targets` |
| --- | --- | --- |
| running | 1 | 1 |
| after `STATE = STOP` | 0 | 0 |

A stopped session leaves `sys.dm_xe_sessions` entirely, so the join that reads
`target_data` returns no row. The in-memory target is torn down with the
running session. Reading after stopping does not return a short histogram: it
returns nothing, and a caller treating "no row" as "no events" writes an
archive saying the window saw no statement. Slice 4 step 4 asks for exactly
that archive as a test case, so the wrong path has a passing test waiting for
it.

The spec is the source and says the opposite of what happens:

> Stopping the session flushes the buffers, which is why `finish` always sees
> them and a mid-flight read may not.

The flush is real and there is nowhere left to read it from. Fix the order in
both documents, and replace that sentence rather than softening it.

This also promotes `MAX_DISPATCH_LATENCY` from the cosmetic problem the plan
describes, risk 5, to a correctness constraint: since the read must happen
while the session runs, the tool has to wait out the dispatch latency after the
last event and before the read, or set the latency low enough that the wait is
tolerable. That is a number slice 2 step 1 has to choose.

### B3. The slot value is `uint64`, the plan types it `int64`, and both obvious conversions are wrong

Slice 1 pins the decoder before slice 0 has reported: "`Histogram` carries
`Buckets []Bucket` (`Value int64`, `Count int64`)". The XE type is `uint64`:

```
<action name="query_hash" package="sqlserver"><type name="uint64" package="package0"/>
```

The histogram emits it as an unsigned decimal, so a hash with the high bit set
exceeds `int64` and `strconv.ParseInt` refuses it. This is the common case, not
an edge. In the nine-shape workload of N1, five of the nine hashes were above
`2^63`, among them `13574406096104372066` and `16432268320504878462`.

The T-SQL side is worse. Both expressions an implementer reaches for first were
run:

| Expression, `v` the histogram's decimal | Result |
| --- | --- |
| `CONVERT(binary(8), CAST(v AS bigint))` | `Arithmetic overflow error converting expression to data type bigint` |
| `CONVERT(binary(8), CAST(v AS decimal(20,0)))` | `0x14000001FFFFFFFF` for `v = 18446744073709551615` |

The second is the dangerous one. It does not fail. It converts the decimal's
internal storage representation rather than its numeric value and returns a
well-formed `binary(8)` that resolves against nothing. The correct answer for
that input is `0xFFFFFFFFFFFFFFFF`.

Verified to work across both halves of the range:

```sql
CONVERT(binary(8), CAST(CASE WHEN v >= 9223372036854775808
                             THEN v - 18446744073709551616
                             ELSE v END AS bigint))
```

Checked against `0xFFFFFFFFFFFFFFFF` at the top and `0x3FC3B829D6FF1F08` in the
low half. The lazier answer is better: decode to `uint64` in Go, write the
eight bytes big-endian, pass a `binary(8)` parameter. No T-SQL arithmetic,
nothing to get wrong twice.

Either way the plan must stop declaring `Value int64` in slice 1 when slice 0
exists to tell it what the type is.

### B4. Task 0.1 cannot fail in the way that matters, and one of its steps cannot be performed

Task 0.1 is the plan's answer to its own risk 1, aimed at the wrong target.

It does not name the events. Step 2 says "create an Extended Events session
bucketizing on `sqlserver.query_hash` with `source_type = 1`". An implementer
who reaches for `sql_statement_completed`, which is what most published
examples of this pattern use, gets a real hash, writes a correct conversion
document, and reports the task done, having never touched the session the
command will ship. The task must pin the two events by name or it cannot
observe B1.

Step 3 asks for something the histogram does not produce: "Include a negative
slot value: that is where signedness bites". The slot value is an unsigned
decimal and is never negative. The negative appears on the T-SQL side, in
`CONVERT(bigint, query_hash)`. An implementer following the instruction
literally searches the XML for a minus sign, finds none, and skips the
criterion the task is declared done by.

Step 4 samples in a way that avoids the failure: "Take three hashes from the
plan cache, convert, and match them against the histogram's slots by equality."
Three arbitrary hashes miss the high bit one time in eight, and the naive
`bigint` conversion is perfect for every hash that lacks it. The step has to
construct a high-bit value rather than hope for one.

### B5. Both `go test` counts are already satisfied by tests that exist

The plan's verification discipline guards one direction only: "A lower count
means the filter is wrong, not that the task is done." Nothing is said about a
higher count, and two of the four filters already match. Measured at `4ed20d8`:

| Plan step | Filter | Plan says | Matches today |
| --- | --- | --- | --- |
| Slice 1, step 5 | `./collect/xevents/ -run 'Decode|Truncat'` | 6 | 0, new package |
| Slice 2, step 5 | `./collect/ -run Observe` | 5 | 2 |
| Slice 3, step 6 | `./cmd/sql-auditor/ -run Observe` | 8 | 0 |
| Slice 4, step 5 | `./collect/ -run 'Resolve|ObserveArchive'` | 6 | 19 |

`./collect/ -run Observe` matches `TestObserverCallbacksAreSafeOnTheZeroValue`
and `TestObserverForwardsToTheWrappedImplementation`. An implementer who writes
three tests instead of five counts five `--- PASS` lines and reports the step
satisfied. The check designed to catch a wrong filter certifies a short one.

`./collect/ -run 'Resolve|ObserveArchive'` matches nineteen existing tests
across `config_test.go`, `collect_test.go` and `output_test.go`. The stated
count of 6 is unreachable, so the step fails on a correct implementation and
the implementer resolves the contradiction by editing the number.

This is the same collision the plan flags between `collect/observer.go` and
`collect/xevents`. It decided, correctly, that renaming belongs in its own
commit. It did not notice the collision had already reached its own filters.
Anchor them, `-run '^TestObserve'` with a naming convention the tasks state,
and recount against the tree.

### B6. `collect/cancel.go` does not hold the discipline the plan attributes to it

Slice 3 step 1: "Ctrl-C stops, drops, and writes a partial archive whose
manifest says it was interrupted. `collect/cancel.go` already has this
discipline; use it rather than writing a second one." The spec says the same.

`collect/cancel.go` is 63 lines holding `stopRequested` and
`recordUnitFailure`. Neither handles a signal, writes an archive, or touches
the server. They decide that a dead context means the operator stopped the run,
so a cancellation is not filed as a network fault. The signal handling is
`interruptible` in `cmd/sql-auditor/main.go` and all it does is cancel the
context.

That is sufficient for `collect` for a reason that does not carry over: on
Ctrl-C, `collect` has nothing to undo on the server, because it creates no
object. Its residue is local files. `observe` has a session on a client's
production instance, and pointing an implementer at `cancel.go` hands them a
pattern silent about the two things that now matter.

The teardown cannot run on the cancelled context. Once `interruptible` cancels,
every `QueryContext` on that context fails immediately with `context.Canceled`,
including the read of `target_data` and the `DROP EVENT SESSION`. The teardown
needs a fresh context with its own short timeout from `context.Background()`.
Following the plan's instruction to reuse the existing discipline produces a
drop that fails at its first statement, leaving exactly the orphan the spec is
most nervous about.

The second signal kills, by design. `interruptible` calls `signal.Stop` before
cancelling, so a second Ctrl-C takes the default disposition and the process
dies. The comment explains why that is right for `collect`: an operator whose
run will not wind down can still get out. For `observe` the same keystroke
leaves a session running on the instance. That may still be the right trade,
but it is a different one, and the plan makes it by inheritance rather than by
decision. At minimum the first Ctrl-C message has to say what a second one will
leave behind, and the session name has to be on screen before the wait starts.

## Serious

### S1. The `observe` manifest does not belong in `collect/manifest.go`

Slice 4 lists "Modify: `collect/manifest.go` (an `observe` manifest, not the
collector's)". The parenthesis is right and the file is wrong.

`collect/manifest.go` is 1271 lines around one `Manifest` struct and one
`Human()` running a fixed sequence of writers, among them `writeReadOnlyClaim`,
which emits:

> It creates no permanent object: nothing that belongs to this server or its
> databases is created, altered or deleted, and no data of yours is written
> anywhere by this tool.

`observe` needs a manifest that says the opposite. Both in one type means a
branch inside `writeReadOnlyClaim`, and that is the shape the spec rejects by
name as its reason for making `observe` a separate command:

> An opt-in step inside `collect` creates two states of the manifest, and every
> approver then has to ask which one they are signing.

The spec avoided two states of one manifest at the command surface. Slice 4
reintroduces them one layer down, where nobody is signing anything and a
mis-set flag is a code path rather than a visible choice. A separate file and
type, sharing only `Zip` and the JSON writer, costs a little duplication and
makes it structurally impossible for the collector's promise to be printed over
a run that created a session.

The reuse the plan is reaching for is real and it is `collect/archive.go`:
`Zip` takes a folder and a destination and knows nothing about collection.

### S2. `_run.json` is promised by the manifest the spec drafts, and appears nowhere in the plan

The spec's output block lists four files and the manifest it drafts makes a
specific promise about one: "The exact statements that created and dropped it
are in `_run.json`."

That promise is the consent argument in written form. It is what lets an
operator paste the DDL into a ticket afterwards, and what makes the printed
prompt verifiable against what ran. Slice 4 covers `statements.json`,
`unresolved.json` and the manifest. `_run.json` is named in no step of any
slice, and slice 4 step 4 tests that "an archive from a fixture capture has the
files" without saying which.

Add it, with the constraint that decides its content: the DDL recorded must be
the text that was executed, captured at execution, not recomposed afterwards
from the same builder. A recomposition proves the builder is deterministic,
which nobody doubted, not that the prompt told the truth.

### S3. The wire attribute names are not the plan's

Slice 1 declares `Histogram` with `Slots int`, `NotFiltered int64`,
`Truncated bool`. Measured, the histogram target returns:

```
<HistogramTarget truncated="0" buckets="256"><Slot count="9"><value>0</value></Slot></HistogramTarget>
```

`slots` on the wire is `buckets`. There is no `NotFiltered` attribute on this
build; if it is expected from an older one, that is a version claim and belongs
in the fixture README with the build beside it. And `truncated` does not mean
what the plan's field of the same name means, which N1 now settles: it is not
document truncation and it is not the overflow counter either, since it stayed
at zero through a 78 percent fold. Whatever it counts, a decoder must not map
it to a field named `Truncated` and let the manifest speak from it.

The ring buffer measured as:

```
truncated="0" processingTime="0" totalEventsProcessed="85"
eventCount="85" droppedCount="0" memoryUsed="26949"
```

`droppedBuffers` was absent on a capture that dropped nothing. The spec
requires both counters and is right to, since they mean different things, but a
decoder that requires the attribute to be present will fail on the ordinary
case. Task 0.2 should force a drop rather than capture a healthy session, so
the fixture shows whether the attribute appears only when non-zero. A fixture
that never exercised loss cannot test the loss counters.

### S4. The sweep makes `status` a DDL command, and falsifies the sentence beside exit code 3

Slice 2 step 4: "Any entry point calls it first: a session under the known name
past its deadline is stopped and dropped before anything else happens."

`observe status` becomes a command issuing `ALTER EVENT SESSION ... STATE =
STOP` and `DROP EVENT SESSION` with no prompt, in a tool whose stated premise is
that DDL requires printed consent. The spec authorises the sweep and the
reasoning is good, but `status` reads like a look and is not one. It has to
print what it dropped and the help has to say it can.

Exit code 3 is documented as "refused before touching the instance" and the
plan carries the table into slice 3 step 5 unchanged. A run whose sweep dropped
an orphan and then refused for a missing permission exits 3 having altered the
instance. Either the sentence changes or the sweep moves behind preflight.

Which raises an ordering the plan never states: the sweep needs
`ALTER ANY EVENT SESSION`, and preflight is what makes "the tool refuses rather
than degrades" real. If the sweep runs first, an operator without that right
meets a raw SQL error instead of the designed refusal. Order preflight, then
sweep, then prompt, and say so in the step. See also N4: the right does not
exist anywhere in the tree yet.

### S5. Slice 1 has no baseline to subtract, and carries the machinery for one

With `--session` out of scope, stated in the plan's own opening, `observe`
always creates its own session, so the histogram always starts at zero. The
snapshot, the subtraction, the `create_time` comparison at both ends, the
negative-delta backstop and `--no-baseline` all exist in the spec for the
borrowed session, where the counter may have been running for a week.

Slice 3 step 2 carries all of it: code written for a mode this slice does not
have, tested against a condition it cannot reach. Keep the state file, which
earns its place binding server, database and session name so a `finish` against
the wrong connection string refuses. Drop the baseline arithmetic until
`--session` arrives, and keep `--no-baseline` out of the flag set rather than
documenting a flag whose opposite does not yet exist.

The one piece worth keeping from that paragraph is the `create_time`
comparison, for a different reason than subtraction: it is how `finish` notices
that the session it is finishing is not the session it started.

## Smaller

Slice 1 step 3 tests a case that cannot occur. The step calls itself "the one
that matters" and describes two cuts, mid-bucket and "between two buckets,
where the XML still parses". Both were constructed and run against Go's
decoder:

| Document | `xml.Unmarshal` | Token loop |
| --- | --- | --- |
| intact | no error, 3 slots | 3 slots |
| cut mid-bucket | syntax error, unexpected EOF | 1 slot, cut detected |
| cut between buckets | syntax error, unexpected EOF | 1 slot, cut detected |

There is no cut point of a histogram document that parses cleanly into a short
list, because any cut leaves the root element unclosed. The premise of the
step's second case is wrong for this shape. What it should measure instead is
what a real `target_data` truncation looks like, which is not known and is open
question 2 of the spec: SQL Server may return a byte-cut document or a
well-formed one with a marker. Testing against an invented cut proves the
parser handles an invented cut.

Related: eager `xml.Unmarshal` returns an error on any cut, so a caller
checking `err` discards everything including the buckets that did arrive.
Recovering them needs a token loop. The plan's "eager, not lazy" in step 2 and
its expectation of partial recovery in step 3 are not compatible, and the
resolution is that eager is about not keeping the XML alive, not about which
API parses it.

The fixed session name is never written down. The spec fixes it as
`sql-auditor observe`; the plan says only "reading the fixed name". It contains
a space, so every statement that names it needs bracket quoting, which was
confirmed to work: `CREATE EVENT SESSION [sql-auditor observe] ON SERVER`
succeeds and the session is found in `sys.dm_xe_sessions` under the unquoted
string. Put the literal in the task, since the sweep, the refusal message and
the orphan a DBA has to recognise all depend on it being the same everywhere.

Fixture scrubbing, task 0.2 step 3. The instruction is right and its target
slightly off: the histogram carries no text, so nothing in it needs scrubbing.
The ring buffer carries text only if `sqlserver.sql_text` is among its actions,
which the spec's session definition does not include. A useful ring buffer
fixture has to add that action deliberately, and that is the moment the rule
applies. Say which of the two fixtures is the one with text in it.

`filtering_event_name` is confirmed unnecessary with `source_type = 1`. A
session was created without it and the DDL was accepted. Slice 2 step 1 does
not need it among the explicit options.

## What survived the review

The decision the plan nominates as most likely wrong is the one that held up.
`collect/observesql/` with `//go:embed` was checked against the mechanism that
would have broken it: the corpus embed is `//go:embed queries`, and nothing in
the repository, in CI or in the tests walks the tree for `*.sql`. So
`testdata/corpus.txt` genuinely does not change, `check` genuinely does not list
them, and the statement lint genuinely does not see them. The reasoning about
the manifest sentence is also correct: a `CREATE EVENT SESSION` in `queries/`
would make it false.

Deferring the `Observer` rename out of this slice is right, and its reasoning
survives contact with the tree. The collision it names is real and reaches
further than the plan noticed, which is B5.

`Zip` in `collect/archive.go` is generic and reusable as the plan assumes.

## Where this leaves the plan

Slice 0 has to be rewritten before it is worth running, because it is the only
part that can catch B1 and it currently cannot. N1 and N2 belong in it too:
both are measurements, both are cheap, and both change an interface that
slice 1 currently declares from assumption.

Slices 1 through 4 are built on a capture that returns one bucket. They are not
wrong in their shape, and most of what they say survives whichever of B1's
three resolutions is chosen. But none of them should be started until that
choice is made, because it decides what a bucket means.
