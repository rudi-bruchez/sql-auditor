# Bounding the duration of a collection

Status: proposed on 4 October 2026, not implemented. Written on 4 October 2026
from the decision taken that day to bound the collection as a whole, and before
the panel of five readers that every specification of this repository goes
through. The first step of the same proposal is already shipped: `check` and
the wizard's third screen print a ceiling (`4cd14c3`, `collect/duration.go`).

## The question

An operator starts a collection of the `space` profile with
`--measure-page-density` on an instance of four databases, in the evening, and
cannot tell whether it will last ten minutes or an hour and a half. Nor can
they tell it to stop by a given time.

Two archives of September 2026, both on client instances, show the shape of
the problem. The first spent 5 397 seconds collecting, of which 1 155 went to
units that returned something; the other 4 242 were two expiries of
`70.schema/055.page-density.sql` at 1 800 seconds and two of
`10.system/020.properties.sql` at 300. The second, on a single database, spent
1 923 seconds, 900 of them in two expiries. The README says it of both cost
options, in its table of options: "nothing bounds the run as a whole".

The decision of 4 October 2026: an option that bounds the duration of the
whole collection. Once the bound is passed, no new unit starts, and every unit
that did not start is recorded as an omission with its reason. The tool judges
nothing; only the collection is bounded.

This document settles how.

## What exists today

Nothing in the tree bounds a run. `grep -rn -i "max-duration\|MAX_DURATION"
--include=*.go` finds nothing, and `Run` (`collect/collect.go`) walks its
units with no clock of its own. What there is:

- Each unit has a limit, `@timeout` or `SQL_QUERY_TIMEOUT_SEC`, resolved by
  `unitTimeout` (`collect/duration.go`) and applied in `runUnit` as a
  `context.WithTimeout` on the unit's context. An expiry becomes an error
  through `outOfTime`, which names the limit: "still running when @timeout of
  30m0s expired".
- The unit's context is a `context.WithCancelCause` of the run's context. The
  blocking watch cancels it with a `*blockedError` as the cause, and
  `blockedOr` reads the cause back. This is the existing pattern for stopping
  one unit for a reason that is not the operator's.
- Two mechanisms skip a planned unit while the run is under way, both in the
  loop `for _, u := range units`:
  `heldBack` (a database where the blocking watch cancelled a collector),
  `droppedBefore` (a database found dropped, `collect/dropped.go`). Both append
  a `SkippedScript` with a target and a reason, and call `obs.UnitDone` with a
  `*UnitSkipped` so the gauge reaches its total.
- The operator's stop (`ctrl-c`, `SIGTERM`) cancels the run's context.
  `stopRequested` (`collect/cancel.go`) sets `run.cancelled` in `_run.json`,
  the loop breaks without recording the units left, and `settleRun` turns the
  exit code into 2.
- The byte budget (256 MiB, `maxRunBytes`, `collect/runfile.go`) is the
  mechanism the decision names as a model, and it is a model by analogy only.
  Its omissions are recorded inside a writer's `_index.json`, through
  `budgetReason` (`collect/querystore.go`); a plain unit the budget refuses
  fails, and is an error that sets exit 2. The closer model for a whole unit
  not started is `heldBack` and `droppedBefore`, and this document follows
  them.

The ceiling `check` prints today, taken from SQL Server 2025 in the lab on
4 October 2026, eight databases, with no option and then with the two cost
options:

```
Duration, a ceiling and not an estimate:
  8 databases; no costly collector on (--estimate-compression, --measure-page-density are off)
  all 289 units: at most 9h31m (34290 s) if every one runs to its @timeout
```

```
Duration, a ceiling and not an estimate:
  8 databases; costly collectors on: 70.schema/041.compression-savings.sql, 70.schema/055.page-density.sql;
    at most 6h00m (21600 s) if every one of their 12 units runs to its @timeout
  all 301 units: at most 15h31m (55890 s) if every one runs to its @timeout
```

A `collect` with no option on the same instance, the same day, took 108
seconds against that ceiling of 34 290. The `--debug` timeline put the first
unit at 2.6 seconds: 2.1 of them hashing and discovering the corpus, 0.2
connecting, preflight and listing the databases. The slowest unit took 9.7
seconds. The ceiling is the only figure that can be stated in advance, and it
is three hundred times the run. A bound is what turns an unknowable duration
into one the operator chooses.

## The option and its format

`--max-duration D` on the command line, `MAX_DURATION` in `.env` or in the
environment, with the usual precedence (flag, then `.env`, then the
environment). Read by `collect` and the wizard; `check` reads it to compare it
with the ceiling, and nothing else.

The name follows the existing pairs where the key is the flag in upper snake
case, `--output-dir`/`OUTPUT_DIR` and `--queries-dir`/`QUERIES_DIR`.
`flagNameFor` gains the entry, so a provenance line names the flag.

A `.env` key, and not only a flag as `--query-store-compare-at` is. That flag
refuses a key because a moment left in a file goes stale; a bound does not, it
is a property of the client's maintenance window and is set once per client.
The wizard displays settings from `.env` and never edits them, as it does the
Query Store window, so without a key the wizard could never run bounded. A
bound forgotten in a file is not silent: `check` prints it, the manifest
records it, and a run it cuts exits 2.

The value is a Go duration, parsed by `time.ParseDuration`: `2h`, `90m`,
`1h30m`, `5400s`. Every other duration setting of the tool is a whole number
of seconds (`SQL_QUERY_TIMEOUT_SEC`, `SQL_CONNECT_TIMEOUT_SEC`) or of days
(`QUERY_STORE_DAYS`), and the key carries the unit in its name. A collection
window is counted in hours, and asking for `7200` where the operator thinks
"two hours" invites the factor-of-sixty mistake. A bare number is refused,
which `time.ParseDuration` already does ("missing unit in duration").

The smallest accepted value is one minute. Below that, the preamble alone (2.6
seconds on the lab, up to `SQL_CONNECT_TIMEOUT_SEC` plus one
`SQL_QUERY_TIMEOUT_SEC` per preflight probe on a struggling instance) can
consume the bound, and the archive holds nothing but the record of every unit
skipped. A value below one minute, zero, or negative is a configuration error,
exit 2, with a message in the form `secOf` uses:
`MAX_DURATION: invalid value "30s", want a duration of at least 1m, such as 90m or 2h`.
There is no upper limit: a bound above the ceiling is harmless and `check`
says so.

Unset means no bound, which is today's behaviour.

`--all` does not set a bound and is not affected by one. `--all` turns on the
eleven opt-ins and "changes nothing else", as its help says; a bound is not an
opt-in and does not enter `KnownFlags` or `ValueFlags`. A profile is not
affected either: the bound applies to whatever plan the profile and the
options produce.

## When the bound is checked

At two moments.

Before each unit. The loop asks whether the bound has passed before starting
a unit, after `heldBack` and `droppedBefore`: a unit on a database already
found dropped, or held back by the watch, keeps that reason, which is more
specific and is the one `scopeLost` matches on. Every unit that reaches the
check after the bound has passed is skipped with the bound's reason.

During a unit. A unit still running when the bound passes is stopped. The
alternative, letting it run to its own `@timeout`, means a bound of 23:00 can
be overshot by 1 800 seconds whenever `055.page-density` or
`041.compression-savings` starts at 22:59, and the reason the option exists is
the window promised to a client. A bound that holds only when the slow unit
does not happen to be running is not a bound.

The stop reuses the watch's pattern. The unit's context gets a deadline at
the bound with its own cause, a sentinel `errMaxDurationReached`
(`context.WithDeadlineCause`). The run's context is not touched, for the
reason `recordUnitFailure` gives at length: a dead run context means the
operator stopped the run, and the bound must not be filed as a `ctrl-c`.

What a stopped unit costs:

- Its work is lost. It is recorded as an error, `ErrorEntry`, with its
  `duration_ms`, and a message that names the bound and keeps the driver's
  words, as `outOfTime` does: `stopped when the collection reached its
  maximum duration of 2h00m (7200 s): context deadline exceeded`, the
  duration written by `formatCeiling` as everywhere else in this feature. An error and not a
  skip, because it started: the time it consumed is the most useful fact in
  the record, and only `ErrorEntry` carries a duration.
- It leaves no partial file. `runUnit` reads every result set into memory
  (`ReadResultSets`) before it writes anything, and a writer receives
  `WriteRequest` with no connection and no context. The deadline can only land
  in the `USE`, in `QueryContext` or in `ReadResultSets`, all before the first
  byte is written. A unit that finished reading just before the bound writes
  its files to the end; that part is local disk, not server work.
- `outOfTime` must ask the cause first. The query context
  (`context.WithTimeout(unitCtx, timeout)`) inherits the bound's deadline when
  it is earlier, and `qctx.Err()` is then `DeadlineExceeded`, so `outOfTime`
  as written would file the stop as "still running when @timeout of 30m0s
  expired", a limit that did not expire. `context.Cause(qctx) ==
  errMaxDurationReached` is tested before it, as `blockedOr` tests for
  `*blockedError`.
- The driver closes a connection whose query it had to cancel. The loop does
  not reconnect after a stop at the bound: it goes straight to recording the
  units left, which need no connection, and then to the manifest.

## What the duration counts

From the instant `Run` takes `started`, the instant `run.duration_sec` in
`_run.json` is measured from. It includes hashing the corpus, connecting, the
preflight, the probe and listing the databases. It does not include what the
wizard or `check` did before `Run` was called, and it does not include writing
the manifest and the archive after the loop.

The reason is that the operator has one number to compare against: the bound
and `duration_sec` share their origin, so `duration_sec` above the bound by
more than the post-loop phases is a defect anyone can see. Counting from the
connection would exclude a dial that can take `SQL_CONNECT_TIMEOUT_SEC`, and a
maintenance window counts from when the job started, not from when the server
answered.

The bound limits server work, not the process. After it passes, the run can
still spend: the reset of a stopped unit's session (bounded by
`SQL_QUERY_TIMEOUT_SEC`, and quick on a connection the driver has closed),
the writing of files a unit had finished reading, the manifest, and the zip
(0.05 seconds on the lab's 108-second run). The README says so in those
words.

The preamble itself is not cut. Each of its steps has its own limit, and a
run whose preamble passed the bound skips every unit at the first check.

## The order of the units

Today the order is fixed by `Discover`, which sorts the scripts by path
(`collect/queryset.go`), and by `planUnits` (`collect/observer.go`), which
unfolds each script over its databases: script after script, each over every
database. The test the proposal points to, `check_order_test.go`, is about the
order in which `check` prints, not the order of the units.

With a bound the order decides what is lost, since what runs last is what is
skipped. In path order the two cost collectors sit in `70.schema`, before the
whole of `80.workload` (Query Store, plan cache, waits) and `90.availability`.
A bounded run with `--measure-page-density` spends its window on page density
and skips the Query Store, which is the core of a performance audit and the
part an operator least expects to lose to an option about index fullness.

Decision: `planUnits` moves the units of every collector gated by a
`CostFlags` opt-in after all the others, keeping the order within each group.
The move is made in `planUnits` because `PlannedDuration` and `Run` both call
it, so the order `check` describes is the order the run walks.

It applies to every run, bounded or not. One order means a bound changes only
where a run stops, never what it ran before that point, and two runs of the
same plan read the same sequence in their `_run.json`. The move also helps
without a bound: a `ctrl-c` at the operator's own time limit keeps everything
but the costly part, and a blocking-watch cancellation of `055` on a database
holds back nothing after it.

The cost is the order of `results` in `_run.json`, which changes for a run
with a cost option on; nothing reads that order as meaningful. No dependency
is broken: the only ordering the corpus relies on is
`021.query-store-detail` before `022` (`queryStoreArgs`), both ungated by cost,
and no collector reads what `041` or `055` produce.

The rest of the corpus keeps its path order. A finer ordering, cheapest first
or by value to the audit, would be a judgment this document has no measure
for.

## How a bounded run is recorded

In `_run.json`:

- `run.max_duration_reached: true`, `omitempty`, for the same reason
  `run.cancelled` is: every manifest written before it, and every run the bound
  did not reach, stays byte-identical. It is set when at least one unit was
  stopped or skipped for the bound, and only then. A bound that passes during
  the manifest and the zip reached nothing.
- `config.max_duration_sec`, the bound in seconds as a string, `"7200"`, in
  the unit of `duration_sec` beside it. The key is absent when no bound was
  set, so that its absence means the same thing in this version's manifests
  and in older ones.
- One `skipped_scripts` entry per unit not started, with its target and one
  reason shared by all of them:
  `the collection reached its maximum duration of 2h00m (7200 s) before this collector started`.
  It is one constant string per run, built by one function, because
  `MANIFEST.txt` groups on it and `skipLoses` will match on it.
- The unit stopped at the bound, if any, in `errors`, as described above.

In `MANIFEST.txt`:

- The duration line says it, since a reader of `MANIFEST.txt` alone has no
  other way to learn the run was cut:
  `Duration     : 7204 s, stopped at the maximum duration of 2h00m (7200 s)`.
- "Queries not run" groups the bound's skips into one entry, as it groups the
  profile's skips and a dropped database's, because a bound that fires early
  can skip two hundred units for one reason:

```
Queries not run (210):
  - 198 collectors not started, each listed in _run.json
      the collection reached its maximum duration of 2h00m (7200 s) before this collector started
```

  (Illustrative: the tool does not produce this yet.)

On the screens:

- On the command line, a note on stderr when the bound fires, before the
  skips are recorded:
  `note: the collection reached its maximum duration of 2h00m (7200 s); 198 collectors were not started`.
  The summary line scripts parse gains a token, as it does for a stop:
  `102 result(s), 210 skipped, 1 error(s), max duration reached`
  (illustrative, for the 301 units of the lab's run with both cost options:
  12 skipped by the plan, 198 by the bound, one stopped).
- The observer is told each skip through `UnitDone` with a `*UnitSkipped`,
  as `heldBack` does, so the gauge reaches its total. `Observer.Finished`
  carries the bound as well as the cancellation, and the wizard's last screen
  reads `Collection stopped at its maximum duration. This archive is partial:`
  in place of the line it shows for a stop.

## Exit code and the previous run of the day

A run the bound reached exits 2, the README's code for a partial run.
`settleRun` already says why 0 is wrong here, in its own words: 0 "told every
scheduler, runbook and CI job that stopped a collection at its time limit that
the collection had succeeded". A bound reached is that case with the tool
holding the clock. A run whose bound passed after its last unit exits as it
would have without one.

In the wizard, the exit code is the one `Run` returns: the wizard exits 0 only
when its own context was cancelled (`tui/run.go`, `ctxCancelled`), and the
bound does not cancel that context. A bounded wizard run that is cut exits 2.

The previous run of the same server and day is kept, by two locks:

- `settleRun` allows the previous run to be deleted only after exit 0, so a
  bounded run that was cut never reaches `previousRunLost`.
- If it did, `skipLoses` would answer "a loss" for the bound's reason, which
  falls under "any other reason" in its table. The table gains an explicit row
  rather than relying on that fallback: "reached the maximum duration: yes,
  though such a run already exits 2 and keeps prev as partial", the same
  wording as the blocking watch's row.

`settingsLost` does not compare the bound. A bound that was not reached
changed nothing in what was collected, and one that was reached makes the run
exit 2, so naming it would only repeat what the exit code already decided.

The rerun of the day follows. An operator who reruns after a bounded run
finds the bounded archive set aside as `.superseded-HHMMSS`. A complete rerun
covers it (the units the bounded run skipped produced nothing, so their skip
loses nothing in the comparison) and deletes it. A rerun that is bounded again
is partial again and keeps it, so two bounded runs in one day leave two
archives on disk, each partial at a different point.

## What check and the wizard announce

When a bound is set and the plan could be priced, `check` adds a line under
its ceiling, worded from the comparison:

```
Duration, a ceiling and not an estimate:
  8 databases; costly collectors on: 70.schema/041.compression-savings.sql, 70.schema/055.page-density.sql;
    at most 6h00m (21600 s) if every one of their 12 units runs to its @timeout
  all 301 units: at most 15h31m (55890 s) if every one runs to its @timeout
  bounded by MAX_DURATION=2h (from .env): no collector starts after 2h00m (7200 s), and the one running then is stopped;
    the costly collectors run last, so they are the first to go if the bound is reached
```

(Illustrative: the first four lines are the real output above, the last two
do not exist yet.)

The three cases of the last line:

- The whole ceiling is under the bound: `the ceiling is under the bound, so
  the units alone cannot reach it`.
- The costly ceiling alone exceeds the bound: the line above, naming the
  costly collectors as first to go.
- Otherwise: `the ceiling is above the bound; if it is reached, the collectors
  last in the plan are the ones not run`.

The figures are the ceilings `PlannedDuration` already computes. `check` does
not walk the plan with cumulative timeouts to name the unit where a
worst-case run would stop: on the lab that would announce a stop around the
fortieth unit for a run that collects all 289 in 108 seconds, and a forecast
three hundred times too pessimistic misleads more than it informs.

The provenance, `(from .env)`, `(from --max-duration)` or `(from the
environment)`, is printed because the precedence of this tool is the reverse
of most, a `.env` beating an exported variable, and a bound nobody remembers
setting is the one that will be argued about.

The wizard's third screen shows the same line under the same ceiling,
displayed and never edited, as the Query Store window is: `.env` stays the
place where settings live. The last screen's wording is above.

## What is not in scope

- A bound given as a time of day, `--until 23:00`. The decision is a
  duration, and a time of day raises the question of whose clock, which the
  Query Store window already had to answer for the server's.
- Bounding the preamble or the post-loop phases.
- A finer ordering of the corpus than "costly last".
- Saying in `MANIFEST.txt` that the operator stopped a run. It does not say so
  today: `run.cancelled` is in `_run.json` and on the screens, and nowhere in
  `Human()`. The bound's line above would make the bound the only kind of stop
  `MANIFEST.txt` reports; see the open questions.

## Tests

One criterion per decision. Each names what the test must show, and where a
live instance is needed it uses the lab's SQL Server 2025 as the existing live
tests do.

1. Format and floor. `Resolve` with `MAX_DURATION` set to `90m`, `2h`,
   `1h30m` and `5400s` gives the same 5 400 seconds for the third and fourth;
   `30s`, `0`, `-5m`, `7200` and `two hours` are each refused with an error
   naming the key. The flag beats `.env`, which beats the environment, as for
   every other key. Unset gives no bound.
2. `--all` and profiles. A run with `--all` and no bound records no
   `max_duration_sec`; `TestAllTurnsOnEveryOptIn` still passes unchanged,
   which shows the bound did not enter `KnownFlags`.
3. Checked before each unit. With a bound already past when the loop starts
   (an injected clock, not a sleep), every unit is in `skipped_scripts` with
   the bound's reason, no unit reaches `runUnit`, and the observer received
   one `UnitDone` per planned unit.
4. Precedence of the other skips. A database marked dropped, then the bound
   passing: its remaining units carry `skipDroppedDuringRun`, the others the
   bound's reason.
5. A unit stopped at the bound, live. A `Script` built in the test, as
   `collect/unit_live_test.go` builds its probes, with `@timeout` 1800 and a
   `WAITFOR DELAY '00:01:00'`, under a bound that passes two seconds in: the error message names the maximum duration and not
   `@timeout`; `run.cancelled` is absent; no file for that unit exists in the
   run folder; the units after it are skipped with the bound's reason; the run
   took under ten seconds. Breaking the cause check in `outOfTime` must make
   this test fail on the message.
6. What the duration counts. `duration_sec` of a run whose bound fired is at
   least the bound and exceeds it by no more than the post-loop phases; a test
   with an injected clock shows the deadline is `started` plus the bound.
7. The order. For a plan with both cost options on, every unit whose script's
   `RequiresFlag` is in `CostFlags` comes after every unit whose script's is
   not, and the order within each group is the order before the move. The test
   derives the costly set from `CostFlags`, not from a list of paths.
   `PlannedDuration` and `Run` see the same order.
8. The record. `max_duration_reached` is true when one unit was skipped or
   stopped for the bound, and absent otherwise, including when the bound
   passes after the last unit; `config.max_duration_sec` is present exactly
   when a bound was set. `MANIFEST.txt` prints the duration line and one
   grouped entry. A manifest of a run with no bound is byte-identical to the
   one the current tree writes.
9. The exit code. A run the bound reached exits 2 on the command line and in
   the wizard; its summary line ends with `max duration reached`.
10. The previous run. A same-day rerun cut by the bound keeps the run it
    replaced, and the warning says so. `skipLoses` returns true for the
    bound's reason, asserted by name in its table test. `settingsLost` names nothing for two runs differing
    only by the bound.
11. What check prints. For each of the three comparisons, a `check` against a
    fixture `VerifyResult` prints the bound's lines with the provenance;
    without a bound the Duration block is byte-identical to today's.

## Open questions

- Is one minute the right floor, or should the floor follow the
  configuration, for instance the connection timeout plus one query timeout,
  so that it cannot be consumed by the preamble alone?
- Should a unit stopped at the bound be an error, which counts in "N
  error(s)" for something the operator asked for, or a skip with a reason
  that says it had started, at the price of losing its `duration_ms`?
- Should moving the costly collectors last apply only when a bound is set,
  leaving every unbounded run's `_run.json` in path order as it is today?
- Should the bound's line in `MANIFEST.txt` come with a matching line for an
  operator's stop, which `MANIFEST.txt` does not report today, so that the
  bound is not the only interruption a reader of that file can see?
- A bounded run skips per unit, and a bound that fires early writes hundreds
  of `skipped_scripts` entries. Is that the right weight for `_run.json`, or
  should the bound record the units it skipped as one entry with a count?
- Should `check` warn when the bound is under the preamble measured on its own
  connection, rather than only comparing it with the ceiling?
