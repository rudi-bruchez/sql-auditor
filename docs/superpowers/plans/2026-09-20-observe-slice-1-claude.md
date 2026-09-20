# Adversarial review: `observe`, slice 1

Reviewed: `docs/superpowers/plans/2026-09-20-observe-slice-1.md` against
`docs/observe-spec.md` and against the tree at `4ed20d8`.

This review did not only read the plan. Every claim that decides whether the
command produces a number was executed: against `sql2025`
(Microsoft SQL Server 2025 RTM-CU7, 17.0.4065) for the SQL side, and against
the repository for the Go side. The plan asked for that in its own words, "if
your own measurement contradicts this plan or the spec, stop and say so", and
four of the six blocking findings below only exist because the artefacts were
run rather than read.

Everything created on `sql2025` was dropped. Final state: no session or
database matching `ZzObserve%`, and `sys.server_event_sessions` holds only
`system_health`, `AlwaysOn_health` and `telemetry_xevents`. No file was left in
either working tree.

Severities: blocking means the slice cannot ship as written and the defect is
in the plan, not in the implementation of it. Serious means the code will be
written twice. Smaller means it costs a paragraph now and an argument later.

## Summary

The plan is careful where careful is cheap and confident where it is expensive.
Its structure is right: slice 0 exists because two facts decide everything, and
that instinct is correct. But slice 0 measures the wrong two facts. The
conversion it de-risks is not where the design fails, and the measurement it
prescribes cannot observe the failure that is actually there.

Measured on `sql2025`, the capture the spec designs returns one bucket
containing every event, with the value zero. Not a degraded result, not a
partial one: a single number, delivered with an exit code of 0 and a manifest
saying the capture ran. It is the failure mode the spec spends six paragraphs
being afraid of, reached through the one path neither document checked.

## Blocking, measured against `sql2025`

### B1. The capture counts nothing, because `query_hash` is zero on both chosen events

The spec picks two events, `rpc_completed` and `sql_batch_completed`, and
bucketizes the histogram on the `sqlserver.query_hash` action. Both halves of
the design were built. The action returns zero on both events.

Measured, ring buffer target so the raw action value is visible, on a user
table in a database created for the test:

| Event | `query_hash` action | Events |
| --- | --- | --- |
| `rpc_completed` | `0` | 80 |
| `sql_batch_completed` | `0` | 3 |
| `sql_statement_completed` | `4594718534688251656` | 12 |

`query_hash_signed` was collected alongside and returns zero on the same two
events. The statements were not exotic: `SELECT COUNT(*) FROM dbo.Orders WHERE
id = @p1`, sent as parameterised RPCs from the repository's own driver, and the
literal form sent as separate batches. The same statements carry a real hash
where the resolution step will look for it:

```
sys.dm_exec_query_stats.query_hash = 0x3FC3B829D6FF1F08 = 4594718534688251656
```

So the hash exists, the resolution target is populated, and the capture cannot
see it. What the archive gets is `statements.json` with nothing in it and
`unresolved.json` with one row, hash zero, count equal to the whole workload.

Three consequences worth separating:

- The deliverable is empty. `statements.json` is described in the spec as "the
  deliverable", and the command's whole reason to exist is one integer per
  statement shape.
- The failure is indistinguishable from success at every check the plan
  defines. The session builds, starts, counts, and returns a well-formed
  histogram. Exit code 0. Slice 5 step 1 says "check the count against what you
  drove: you know how many statements you sent", and the count will be right,
  because every event lands in the single bucket.
- The workaround costs the design decision the spec makes deliberately.
  `sql_statement_completed` and `sp_statement_completed` do carry the hash, and
  the spec rejects them by name: "Capturing `sql_statement_completed` and
  `sp_statement_completed` would see inside the batch and multiply the event
  volume by whatever the batch does, which is the trade this command declines
  by default." That paragraph now has to be reopened, because it is not a trade
  any more, it is the only path to a non-zero hash.

Note also that neither of the two chosen events has a `query_hash` data column
on this build, which confirms the spec's claim on that point. The problem is
not that the column is missing. It is that the action offered in its place is
empty at the moment these two events fire.

This needs a decision before slice 1 is worth writing, and it is a spec
decision rather than a plan one. The three shapes it can take:

1. Bucketize on statement-level events and accept the volume and the change in
   what "one call" means. The spec's own sentence about not seeing inside a
   batch becomes false and the report's phrasing has to change with it.
2. Keep the two call-level events and bucketize on something they do carry.
   That gives a count per batch shape, not per query shape, and the resolution
   step against `query_hash` stops being possible.
3. Verify the behaviour on the version floor the spec claims, SQL Server 2012,
   before concluding. It is possible this is a regression rather than the
   design of the action. That is a measurement, and it belongs in slice 0.

Whichever is chosen, the reproduction above belongs in the spec beside it.

### B2. Stopping the session destroys the result, and the plan stops before reading

Slice 3 step 1 orders the timed run as: "create, start, wait, stop, read, drop,
write". Measured on the same session:

| Moment | Rows in `sys.dm_xe_sessions` | Rows in `sys.dm_xe_session_targets` |
| --- | --- | --- |
| running | 1 | 1 |
| after `STATE = STOP` | 0 | 0 |

A stopped session leaves `sys.dm_xe_sessions` entirely, so the join that reads
`target_data` returns no row at all. The in-memory target is torn down with the
running session. Reading after stopping does not return a truncated histogram
or an empty one: it returns nothing, and a caller that treats "no row" as "no
events" writes an archive saying the window saw no statement. Slice 4 step 4
asks for exactly that archive as a test case, so the wrong path has a passing
test waiting for it.

The spec is the source of the error and says the opposite of what happens:

> Stopping the session flushes the buffers, which is why `finish` always sees
> them and a mid-flight read may not.

The flush is real, but there is nowhere left to read it from. Both documents
need the order fixed to read before stopping, and the spec sentence needs
replacing rather than softening.

This also promotes `MAX_DISPATCH_LATENCY` from the cosmetic problem the plan
describes, risk 5, "the output has to say which", to a correctness constraint:
since the read must happen while the session runs, the tool has to wait out the
dispatch latency after the last event and before the read, or set the latency
low enough that the wait is tolerable. That is a number slice 2 step 1 has to
choose, not a sentence for `status`.

### B3. The slot value is `uint64`, the plan types it `int64`, and both obvious conversions are wrong

Slice 1 pins the decoder's interface before slice 0 has reported:

> `Histogram` carries `Buckets []Bucket` (`Value int64`, `Count int64`)

Measured, the XE type of the action is `uint64`:

```
<action name="query_hash" package="sqlserver"><type name="uint64" package="package0"/>
```

The histogram emits it as an unsigned decimal. A hash with the high bit set is
a decimal above 9223372036854775807, which `strconv.ParseInt` refuses and
`int64` cannot hold. Half of all hashes have the high bit set, so this is the
common case, not an edge.

The T-SQL side is worse, and this is the part that will ship. Both expressions
an implementer reaches for first were run:

| Expression, `v` the histogram's decimal | Result |
| --- | --- |
| `CONVERT(binary(8), CAST(v AS bigint))` | `Arithmetic overflow error converting expression to data type bigint` |
| `CONVERT(binary(8), CAST(v AS decimal(20,0)))` | `0x14000001FFFFFFFF` for `v = 18446744073709551615` |

The second one is the dangerous one. It does not fail. It converts the
decimal's internal storage representation rather than its numeric value, and
returns a well-formed `binary(8)` that resolves against nothing. The correct
answer for `v = 18446744073709551615` is `0xFFFFFFFFFFFFFFFF`.

The expression that was verified to work in both halves of the range:

```sql
CONVERT(binary(8), CAST(CASE WHEN v >= 9223372036854775808
                             THEN v - 18446744073709551616
                             ELSE v END AS bigint))
```

Checked against `0xFFFFFFFFFFFFFFFF` at the top of the range and
`0x3FC3B829D6FF1F08` in the low half.

The lazier answer is better: decode to `uint64` in Go, write the eight bytes
big-endian, and pass a `binary(8)` parameter. No T-SQL arithmetic, no `CASE`,
nothing to get wrong twice. Either way the plan has to stop declaring
`Value int64` in slice 1 when slice 0 exists precisely to tell it what the type
is.

### B4. Task 0.1 cannot fail in the way that matters, and one of its steps cannot be performed

Task 0.1 is the plan's answer to its own risk 1, and it is the right instinct
aimed at the wrong target. Three defects, in order of cost:

It does not name the events. Step 2 says "create an Extended Events session
bucketizing on `sqlserver.query_hash` with `source_type = 1`". An implementer
who reaches for `sql_statement_completed`, which is what most published
examples of this pattern use, gets a real hash, writes a correct conversion
document, and reports the task done. The session the command will actually ship
returns zero, and task 0.1 never touched it. The task must pin the two events
the spec chose, by name, or it cannot observe B1.

Step 3 asks for something the histogram does not produce: "Include a negative
slot value: that is where signedness bites". The slot value is an unsigned
decimal and is never negative. The negative appears on the T-SQL side, in
`CONVERT(bigint, query_hash)`, which is the other end of the conversion. An
implementer following the instruction literally will search the XML for a minus
sign, find none, and either declare the case absent or quietly skip the
criterion the task is declared done by.

Step 4 samples in a way that avoids the failure. "Take three hashes from the
plan cache, convert, and match them against the histogram's slots by equality."
Three arbitrary hashes miss the high bit one time in eight, and the naive
`bigint` conversion works perfectly for every hash that does not have it. The
step has to construct a high-bit value rather than hope for one: run statements
in a loop until `query_hash >= 0x8000000000000000`, which is a dozen lines, or
test the conversion expression directly against the two range boundaries as
done above.

## Blocking, read from the repository

### B5. Both `go test` counts are already satisfied by tests that exist

The plan's verification discipline is one of its best features and it guards
only one direction: "A lower count means the filter is wrong, not that the task
is done." Nothing is said about a higher count, and two of the four filters
already match existing tests. Measured at `4ed20d8`:

| Plan step | Filter | Plan says | Already matches today |
| --- | --- | --- | --- |
| Slice 1, step 5 | `./collect/xevents/ -run 'Decode|Truncat'` | 6 | 0, new package |
| Slice 2, step 5 | `./collect/ -run Observe` | 5 | 2 |
| Slice 3, step 6 | `./cmd/sql-auditor/ -run Observe` | 8 | 0 |
| Slice 4, step 5 | `./collect/ -run 'Resolve|ObserveArchive'` | 6 | 19 |

The two that collide:

- `./collect/ -run Observe` matches `TestObserverCallbacksAreSafeOnTheZeroValue`
  and `TestObserverForwardsToTheWrappedImplementation` in
  `collect/observer_test.go`. An implementer who writes three tests instead of
  five counts five `--- PASS` lines and reports the step satisfied. The check
  designed to catch a wrong filter certifies a short one.
- `./collect/ -run 'Resolve|ObserveArchive'` matches nineteen existing tests
  across `config_test.go`, `collect_test.go` and `output_test.go`. The stated
  count of 6 is unreachable, so the step fails on a correct implementation and
  the implementer will resolve the contradiction by editing the number.

This is the same collision the plan itself flags in its note about
`collect/observer.go` and `collect/xevents` sitting in one tree under almost
the same name. It decided, correctly, that renaming belongs in its own commit.
It did not notice that the collision had already reached its own test filters.

Fix: anchor the filters, `-run '^TestObserve'` with a naming convention the
tasks state, and recount against the tree rather than from the name.

### B6. `collect/cancel.go` does not hold the discipline the plan attributes to it

Slice 3 step 1: "Ctrl-C stops, drops, and writes a partial archive whose
manifest says it was interrupted. `collect/cancel.go` already has this
discipline; use it rather than writing a second one." The spec says the same.

`collect/cancel.go` is 63 lines holding two functions, `stopRequested` and
`recordUnitFailure`. Neither handles a signal, writes an archive, or touches
the server. What they do is decide that a dead context means the operator
stopped the run, so a cancellation is not filed as a network fault. The signal
handling is `interruptible` in `cmd/sql-auditor/main.go`, and all it does is
cancel the context.

That is the whole of the discipline, and it is sufficient for `collect` for a
reason that does not carry over: on Ctrl-C, `collect` has nothing to undo on
the server. It creates no object. Its residue is local files. `observe` has an
Extended Events session on a client's production instance, and pointing an
implementer at `cancel.go` hands them a pattern that is silent about the two
things that now matter.

- The teardown cannot run on the cancelled context. Once `interruptible`
  cancels, every `QueryContext` on that context fails immediately with
  `context.Canceled`, including the read of `target_data` and the
  `DROP EVENT SESSION`. The teardown needs a fresh context with its own short
  timeout, derived from `context.Background()`. Nothing in the plan says so,
  and following the plan's instruction to reuse the existing discipline
  produces a drop that fails at the first statement, leaving exactly the orphan
  the spec is most nervous about.
- The second signal kills, by design. `interruptible` calls `signal.Stop`
  before cancelling, so a second Ctrl-C takes the default disposition and the
  process dies. The comment explains why that is right for `collect`: an
  operator whose run will not wind down can still get out. For `observe` the
  same keystroke leaves a session running on the instance. That may still be
  the right trade, but it is a different trade and the plan makes it by
  inheritance rather than by decision. At minimum the first Ctrl-C message has
  to say what a second one will leave behind, and the session name has to be on
  screen before the wait starts, not only in the archive.

## Serious

### S1. The `observe` manifest does not belong in `collect/manifest.go`

Slice 4 lists "Modify: `collect/manifest.go` (an `observe` manifest, not the
collector's)". The parenthesis is right and the file is wrong.

`collect/manifest.go` is 1271 lines around one `Manifest` struct and one
`Human()` that runs a fixed sequence of writers, among them
`writeReadOnlyClaim`, which emits:

> It creates no permanent object: nothing that belongs to this server or its
> databases is created, altered or deleted, and no data of yours is written
> anywhere by this tool.

`observe` needs a manifest that says the opposite of that paragraph. Putting
both in one type means a branch inside `writeReadOnlyClaim`, and that is the
shape the spec rejects by name as its reason for making `observe` a separate
command at all:

> An opt-in step inside `collect` creates two states of the manifest, and every
> approver then has to ask which one they are signing.

The spec avoided two states of one manifest at the command surface. Slice 4
reintroduces them one layer down, where nobody is signing anything and a
mis-set flag is a code path rather than a visible choice. A separate file and a
separate type, sharing only `Zip` and the JSON writer, costs a little
duplication and makes it structurally impossible for the collector's promise to
be printed over a run that created a session.

The reuse the plan is reaching for is real and it is `collect/archive.go`:
`Zip` takes a folder and a destination and knows nothing about collection. That
one carries over unchanged.

### S2. `_run.json` is promised by the manifest the spec drafts, and appears nowhere in the plan

The spec's output block lists four files, and the manifest text it drafts makes
a specific promise about one of them:

> The exact statements that created and dropped it are in `_run.json`.

That promise is the consent argument in written form: it is what lets an
operator paste the DDL into a ticket after the fact, and what makes the printed
prompt verifiable against what ran. Slice 4 covers `statements.json`,
`unresolved.json` and the manifest. `_run.json` is not named in any step of any
slice, and slice 4 step 4 tests that "an archive from a fixture capture has the
files" without saying which files.

Add it explicitly, with the constraint that decides its content: the DDL it
records must be the text that was executed, captured at execution, not
recomposed afterwards from the same builder. A recomposition proves the builder
is deterministic, which nobody doubted, and not that the prompt told the truth.

### S3. The wire attributes are not the ones the plan names, and one name means the opposite

Slice 1 declares `Histogram` with `Slots int`, `NotFiltered int64`,
`Truncated bool`, and slice 1 step 4 breaks "the overflow attribute". Measured,
the histogram target returns:

```
<HistogramTarget truncated="0" buckets="256"><Slot count="9"><value>0</value></Slot></HistogramTarget>
```

- `slots` on the wire is `buckets`.
- There is no `NotFiltered` attribute on this build. If it is expected from an
  older one, that is a version claim and belongs in the fixture README with the
  build beside it.
- `truncated` is the overflow counter, the events that found no free slot. It
  is not document truncation.

That last point is the expensive one. The plan's `Truncated bool` means "this
document was cut", and the wire carries an attribute spelled `truncated`
meaning "events were folded for lack of slots". An implementer decoding a
document with `truncated="0"` will map it to the field of the same name, and
the manifest will then report a clean document for a capture that overflowed,
or a truncated document for one that did not. Two different facts, one spelling,
in the same struct. Rename the Go field, and say in the doc comment which one
the wire word means.

The ring buffer measured as:

```
truncated="0" processingTime="0" totalEventsProcessed="85"
eventCount="85" droppedCount="0" memoryUsed="26949"
```

`droppedBuffers` was absent, on a capture that dropped nothing. The spec
requires both counters and is right to, since they mean different things, but a
decoder that requires the attribute to be present will fail on the ordinary
case. Task 0.2 should force a drop rather than capture a clean run, so the
fixture shows whether the attribute appears only when non-zero. A fixture that
never exercised loss cannot test the loss counters, and the plan's task 0.2 as
written captures a healthy session.

### S4. The sweep makes `status` a DDL command, and falsifies the sentence beside exit code 3

Slice 2 step 4: "Any entry point calls it first: a session under the known name
past its deadline is stopped and dropped before anything else happens." Two
things follow that the plan does not address.

`observe status` becomes a command that issues `ALTER EVENT SESSION ... STATE =
STOP` and `DROP EVENT SESSION` with no prompt, in a tool whose stated premise
is that DDL requires printed consent. The spec does authorise the sweep, and
the reasoning is good, but `status` reads like a look and now is not one. It
has to print what it dropped, and the help text has to say it can.

Exit code 3 is documented as "refused before touching the instance", and the
plan carries the table into slice 3 step 5 unchanged. A run whose sweep dropped
an orphan and then refused for a missing permission exits 3 while having
altered the instance. Either the sentence changes or the sweep moves behind
preflight.

Which raises the ordering the plan never states: the sweep needs
`ALTER ANY EVENT SESSION`, and preflight is what turns "the tool refuses rather
than degrades" into something real. If the sweep runs first, an operator
without that right meets a raw SQL error instead of the designed refusal. Order
preflight, then sweep, then prompt, and say so in the step.

### S5. Slice 1 has no baseline to subtract, and carries the machinery for one

With `--session` out of scope, stated in the plan's own opening, `observe`
always creates its own session, so the histogram always starts at zero. The
snapshot, the subtraction, the `create_time` comparison at both ends, the
negative-delta backstop and `--no-baseline` all exist in the spec for the
borrowed session, where the counter may have been running for a week.

Slice 3 step 2 carries all of it. That is code written for a mode this slice
does not have, tested against a condition it cannot reach, and it is the kind
of machinery that gets a passing test and no exercise. Keep the state file,
which earns its place binding server, database and session name so a `finish`
against the wrong connection string refuses. Drop the baseline arithmetic until
`--session` arrives, and keep `--no-baseline` out of the flag set rather than
documenting a flag whose opposite does not yet exist.

The one piece worth keeping from that paragraph is the `create_time`
comparison, for a different reason than subtraction: it is how `finish` notices
that the session it is finishing is not the session it started.

## Smaller

Truncation, and why slice 1 step 3 tests a case that cannot occur. The step
calls itself "the one that matters" and describes two cuts: mid-bucket, and
"between two buckets, where the XML still parses". The second case was
constructed and run against Go's decoder:

| Document | `xml.Unmarshal` | Token loop |
| --- | --- | --- |
| intact | no error, 3 slots | 3 slots |
| cut mid-bucket | syntax error, unexpected EOF | 1 slot, cut detected |
| cut between buckets | syntax error, unexpected EOF | 1 slot, cut detected |

There is no cut point of a histogram document that parses cleanly into a short
list, because any cut leaves the root element unclosed. The premise of the
step's second case is wrong for this shape. What the step should measure
instead is what a real `target_data` truncation looks like, which is not known
and is open question 2 of the spec: SQL Server may return a byte-cut document
or a well-formed one with a marker. Testing against an invented cut proves the
parser handles an invented cut.

Related, and this is where the agy review is right and worth acting on: eager
`xml.Unmarshal` returns an error on any cut, so the caller discards everything.
Recovering the buckets that did arrive needs a token loop. The plan's "eager,
not lazy" instruction in step 2 and its expectation of partial recovery in step
3 are not compatible, and the resolution is that eager is about not keeping the
XML alive, not about which API parses it.

Fixture scrubbing, task 0.2 step 3. The instruction is right and its target is
slightly off: the histogram carries no text at all, so nothing in it needs
scrubbing. The ring buffer carries text only if `sqlserver.sql_text` is among
its actions, which the spec's session definition does not include. A useful
ring buffer fixture has to add that action deliberately, and that is the moment
the scrubbing rule applies. Say which of the two fixtures is the one with text
in it.

`filtering_event_name` is confirmed unnecessary with `source_type = 1`. The
session was created without it and the DDL was accepted. The spec is right on
this point and slice 2 step 1 does not need to list it among the explicit
options.

## What survived the review

The decision the plan nominates as most likely wrong is the one that held up.
`collect/observesql/` with `//go:embed` was checked against the mechanism that
would have broken: the corpus embed is `//go:embed queries` and nothing in the
repository, in CI or in the tests walks the tree for `*.sql`. So
`testdata/corpus.txt` genuinely does not change, `check` genuinely does not list
them, and the statement lint genuinely does not see them. The plan's reasoning
about the manifest sentence is also correct: a `CREATE EVENT SESSION` in
`queries/` would make it false.

Deferring the `Observer` rename out of this slice is right, and its reasoning
survives contact with the tree. The collision it names is real, and it reaches
further than the plan noticed, which is B5.

`Zip` in `collect/archive.go` is generic and reusable as the plan assumes.

## Overlap with the agy review

Both reviews were done independently. Where they meet:

| Finding | agy | This review |
| --- | --- | --- |
| Truncation needs a token loop, not `xml.Unmarshal` | found | confirmed by measurement, and extended: the step's second test case cannot be built |
| `MAX_DISPATCH_LATENCY` visible in `status` | found | promoted to a correctness constraint by B2 |
| Preflight must check `ALTER ANY EVENT SESSION` | found | extended: ordering against the sweep, and exit code 3 |
| Overflow share should warn, not only be recorded | found | extended by S3, the attribute is spelled `truncated` |
| Baseline does not apply to a session `observe` created | found, partly | sharpened in S5: it is dead weight because `--session` is out of scope |
| No way out of a live session within its deadline | found | agreed, and `observe stop` is the smaller of the two remedies |
| Plan cache resolution must use the statement offsets | found | agreed, and it is the second place the archive can leak text |

Nothing agy reports was contradicted. The six blocking findings above are not
in its report, and its statement-offset finding is not in this one, which is the
argument for running both.
