# Bounding the duration of a collection

Status: proposed on 4 October 2026, not implemented. Written on 4 October 2026
from the decision taken that day to bound the collection as a whole, then
revised three times the same day, each time after a panel of five readers ran
the draft against the tree and the lab's SQL Server 2025; the third panel was
aimed at the rules the second revision had added. What they found, and what
became of each finding, is in "Review of 4 October 2026", "Second review of
4 October 2026" and "Third review of 4 October 2026" at the end. The first
step of the same proposal is already shipped: `check` and the wizard's third
screen print a ceiling (`4cd14c3`, `collect/duration.go`). So is a correction
the second review found on the way and that does not depend on the bound: the
wizard offers an archive only when `Run` returned no error (`3a8cc59`,
`archiveOf` in `tui/run.go`).

## The question

An operator starts a collection of the `space` profile with
`--measure-page-density` on an instance of four databases, in the evening, and
cannot tell whether it will last ten minutes or an hour and a half. Nor can
they tell it to stop by a given time.

Two archives of September 2026, both on client instances, show the shape of
the problem. The first spent 5 397 seconds collecting, of which 1 155 went to
units that returned something; the other 4 242 were two expiries of
`70.schema/055.page-density.sql` at 1 800 seconds and two of
`20.databases/020.properties.sql` at 300. The second, on a single database, spent
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
  30m0s expired". `outOfTime` asks the run's context first, so that a
  `ctrl-c` is never dressed as a timeout, and leaves alone an error that
  carries a SQL Server error number, so that a real failure landing in the
  same instant as the deadline keeps its own words
  (`TestOutOfTimeLeavesASQLErrorAloneEvenOnTheDeadline`).
- The unit's context is a `context.WithCancelCause` of the run's context. The
  blocking watch cancels it with a `*blockedError` as the cause, and
  `blockedOr` reads the cause back. This is the existing pattern for stopping
  one unit for a reason that is not the operator's.
- `runUnit` makes three server calls before the query: a session reset
  (`resetWithDeadline`, on the run's context, before the unit's context
  exists), the `USE` of a per-database unit (on the unit's context, and its
  error returns through `blockedOr` alone, never through `outOfTime`), and the
  query itself. After the rows are read, `leave` resets the session again on
  the run's context, to release the shared lock the session holds on the
  database.
- Two mechanisms skip a planned unit while the run is under way, both in the
  loop `for _, u := range units`:
  `heldBack` (a database where the blocking watch cancelled a collector),
  `droppedBefore` (a database found dropped, `collect/dropped.go`). Both append
  a `SkippedScript` with a target and a reason, and call `obs.UnitDone` with a
  `*UnitSkipped` so the gauge reaches its total.
- After a unit, the loop recycles the session (`recycleConn`) when the unit
  succeeded. When it failed, it pings (`connAlive`) and either recycles or
  reconnects once (`Connect`, then `resetWithDeadline`, then `sessionID`); a
  reconnect that fails ends the run with exit 1.
- The operator's stop (`ctrl-c`, `SIGTERM`) cancels the run's context.
  `stopRequested` (`collect/cancel.go`) sets `run.cancelled` in `_run.json`,
  the loop breaks without recording the units left, and `settleRun` turns the
  exit code into 2. Before the loop, `stoppedOr` applies the same rule to the
  steps between the connection and the first collector.
- The byte budget (1 GiB, `maxRunBytes`, `collect/runfile.go`) is the
  mechanism the decision names as a model, and it is a model by analogy only.
  Its omissions are recorded inside a writer's `_index.json`, through
  `budgetReason` (`collect/querystore.go`); a plain unit the budget refuses
  fails, and is an error that sets exit 2. The closer model for a whole unit
  not started is `heldBack` and `droppedBefore`, and this document follows
  them.

The steps of `Run` before the first unit, and the limit each one has on its
own, with the defaults (`SQL_CONNECT_TIMEOUT_SEC` 15, `SQL_QUERY_TIMEOUT_SEC`
60):

| Step | Its limit | On the defaults |
| --- | --- | --- |
| hashing and discovering the corpus | none, local | 2.1 s on the lab |
| `Connect` | twice `SQL_CONNECT_TIMEOUT_SEC` | 30 s |
| the four preflight probes | one `SQL_QUERY_TIMEOUT_SEC` for the four | 60 s |
| the instance probe | `SQL_QUERY_TIMEOUT_SEC` | 60 s |
| listing the databases | `SQL_QUERY_TIMEOUT_SEC` | 60 s |
| lock and run folder | none, local | |
| the session id | `SQL_QUERY_TIMEOUT_SEC` | 60 s |
| starting the blocking watch | `SQL_CONNECT_TIMEOUT_SEC` plus 2 s for its two connections, then a first poll with up to three retries (20.5 s by its own comment) | 37.5 s |

On the defaults that is about 307 seconds before the first unit on an instance
that answers every step at the last moment, and 2.6 seconds on the lab.

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
into one the operator chooses. (The lab has gained databases since; the same
`check` later that day counted ten. The figures above are kept as they were
read, with their date.)

## The option and its format

`--max-duration D` on the command line, `MAX_DURATION` in `.env` or in the
environment, with the usual precedence (flag, then `.env`, then the
environment). Read by `collect` and the wizard; `check` reads it to compare it
with the ceiling, and nothing else.

The name follows the existing pairs where the key is the flag in upper snake
case, `--output-dir`/`OUTPUT_DIR` and `--queries-dir`/`QUERIES_DIR`.
`flagNameFor` gains `case "MAX_DURATION": return "max-duration"`; without it
the provenance falls through to the key and reads `--MAX_DURATION`. `Resolve`
records the provenance in a `Config` field of its own, `MaxDurationFrom`, as
it does `ServerFrom` for `SQL_SERVER`: its `from` map is filled for every key,
but only `SQL_SERVER`'s reaches `Config` today. The bound itself is
`Config.MaxDuration`, zero when unset, and that is its only home: there is no
`Options.MaxDuration`. `Run` reads `o.Config.MaxDuration`; the command line
and the wizard both build their `Options` through `optionsFrom`, which hands
`Run` the `Config` that `Resolve` returned, and the wizard's `applyState`
copies that `Config` whole. A second field would need a copy from one to the
other on each path, and a path that missed it would print a bound in `check`
and on the wizard's third screen while `Run` ran unbounded. A test that wants
a two-second bound sets `cfg.MaxDuration` on the `Config` it passes, since the
floor lives in `Resolve` alone (below).

A `.env` key, and not only a flag as `--query-store-compare-at` is. That flag
refuses a key because a moment left in a file goes stale; a bound does not, it
is a property of the client's maintenance window and is set once per client.
The wizard writes `.env` only for `SQL_SERVER` and `SQL_USER`, when the
operator asks it to on its first screen (`UpdateDotEnv`, which leaves every
other line as it found it), and shows the Query Store window without editing
it; it will not edit `MAX_DURATION` either. Without a key the wizard could
never run bounded. A bound forgotten in a file is not silent: `check` prints
it, the manifest records it, and a run it cuts says so on its last line.

Where the setting is added, as a checklist the change is reviewed against:

- `knownKeys` (`collect/config.go`), and `Resolve`, which parses and checks
  the value and fills `Config.MaxDuration` and `Config.MaxDurationFrom`.
- `flagNameFor`, as above.
- The flag itself in `defineFlags` (`cmd/sql-auditor/main.go`), a string flag
  like `--output-dir`, so that `Resolve` parses and refuses the value in the
  same words whichever of the three sources it came from; and its line in the
  usage text.
- The `flags` map `optionsFrom` hands to `Resolve`, beside
  `"OUTPUT_DIR": c.outputDir`. Without that entry the flag is parsed and never
  reaches `Resolve`.
- `.env.example`, which is also what `env init` writes (`WriteEnvTemplate`,
  from the embedded copy), as a commented line, `# MAX_DURATION=2h`, with a
  comment saying what it does, as `QUERY_STORE_DAYS` is shipped. Not as an
  empty assignment such as `DB_INCLUDE=`: `checkKeys` refuses an unknown key
  whatever its value, and measured with 0.37.0, a `.env` holding
  `MAX_DURATION=` is refused (`unrecognised setting(s): MAX_DURATION`) while
  one holding `# MAX_DURATION=2h` passes `check`. An empty line would make
  every `.env` written by the new `env init` unreadable by every older binary
  for a setting it does not even use.
- The README's table of settings, its paragraph on the wizard's exit code
  (below), and the changelog.
- `docs/dba-guide.md`, "The recognised keys", which lists the closed set and
  says every other key stops the run.
- The paragraph of `MANIFEST.txt` that lists what the run settings in
  `_run.json` contain (`collect/manifest.go`), which gains the bound.

An empty value, `MAX_DURATION=` in a `.env` the new binary reads, means unset
at that level, as for the other keys, and the environment's value, if any,
then applies.

A `.env` that sets `MAX_DURATION` is refused as a whole by every binary older
than the one that adds the key, because `checkKeys` refuses a key it does not
know. Measured with the current binary, 0.37.0, and a `.env` holding
`MAX_DURATION=2h`: `unrecognised setting(s): MAX_DURATION`, exit 2, for
`check` as for any other command. That refusal is the right outcome: a binary
that ignored the key would run unbounded in a window the operator believes
bounded. The README and the changelog say from which version the key exists.
The environment has no such guard: `checkKeys` reads only `.env`, and the same
binary with `MAX_DURATION=2h` exported ran `check` to exit 0 without a word.
The README therefore recommends `.env` or the flag, and says that a bound
given through the environment to an older binary is ignored; the bound's line
in `check` (below) is what shows it was read.

The value is a Go duration, parsed by `time.ParseDuration`: `2h`, `90m`,
`1h30m`, `1.5h`, `5400s`. Every other duration setting of the tool is a whole
number of seconds (`SQL_QUERY_TIMEOUT_SEC`, `SQL_CONNECT_TIMEOUT_SEC`) or of
days (`QUERY_STORE_DAYS`), and the key carries the unit in its name. A
collection window is counted in hours, and asking for `7200` where the
operator thinks "two hours" invites the factor-of-sixty mistake. A bare number
is refused, which `time.ParseDuration` already does ("missing unit in
duration").

The value must be a whole number of seconds. `time.ParseDuration` accepts
`1m0.5s` and `1m500ms`, and the bound is recorded and printed in whole
seconds (`formatCeiling` truncates, `config.max_duration_sec` is an integer);
a half second applied but not recorded would make the record differ from the
deadline, and the criterion that a cut run's `duration_sec` is at least the
bound could fail on a correct run. So a value with a fraction of a second is
refused, in the same message as the floor. `1.5h` is 5 400 whole seconds and
is accepted; `+2h` is `2h`.

The smallest accepted value is one minute. A value below that, zero, or
negative, or with a fraction of a second, is a configuration error, exit 2,
with a message in the form `secOf` uses:
`MAX_DURATION: invalid value "30s", want a whole number of seconds of at least 1m, such as 90m or 2h`.
The floor is enforced in `Resolve`, and only there: `Run` takes any positive
`Config.MaxDuration`, so that a test can bound a run by two seconds. `Run`
does not refuse a fraction either, so a test that passes one gets a record
truncated to whole seconds (a one-millisecond bound is recorded as `"0"`).
Only a test can do that, since `Resolve` stands between every operator and
`Run`; tests that read the recorded bound use whole seconds.

The floor is a guard against a slip of unit, `30s` typed for `30m`, and
nothing more. It does not make the bound longer than the steps before the
first unit: those take 2.6 seconds on the lab and can take about five minutes
on the defaults, depending on the instance and not on anything `Resolve` can
see. The bound covers those steps (next section), so a bound shorter than
them gives a run that records why nothing was collected, rather than one that
overruns in silence. There is no upper limit: a bound above the ceiling is
harmless and `check` says so.

Unset means no bound, which is today's behaviour.

`--all` does not set a bound and is not affected by one. `--all` turns on the
eleven opt-ins and "changes nothing else", as its help says; a bound is not an
opt-in and does not enter `KnownFlags` or `ValueFlags`. A profile is not
affected either: the bound applies to whatever plan the profile and the
options produce.

## The bound's context

`Run` makes one context for the bound, right after it takes `started`:

```go
bound, cancelBound := context.WithDeadlineCause(ctx,
	started.Add(o.Config.MaxDuration), errMaxDurationReached)
defer cancelBound()
```

or `ctx` itself, with a no-op cancel, when there is no bound. There is no
other clock: every question this document asks about the bound is
`context.Cause(bound) == errMaxDurationReached`, written once as a helper,
`boundReached`. The cause, and not `bound.Err()`, because a `ctrl-c` cancels
`bound` too, with the cause `context.Canceled`.

The cause is a constant and the deadline an instant, so neither carries the
duration the operator set, which every message of this feature names. The
helpers below that build a message take it as an argument, `limit
time.Duration` (`o.Config.MaxDuration`), and every sentence comes from one
function, `maxDurationText(limit)`, which returns `the collection reached its
maximum duration of 2h00m (7200 s)` with the duration written by
`formatCeiling`. The other sentences are built on it.

The run's context `ctx` is never replaced by `bound`. It stays the context of
`stopRequested`, `recordUnitFailure`, the parent test of `outOfTime` and
`leave`. Passing `bound` where `ctx` is expected would be the defect
`recordUnitFailure` documents at length, in a new form: at the bound,
`bound.Err()` is set, and every one of those calls would file the bound as an
operator's stop.

What runs under `bound`:

- The server steps before the loop: `Connect`, the preflight, the instance
  probe, listing the databases, and reading the session id.
- In each unit, the unit's context, now derived from `bound` rather than from
  `ctx`, and with it the first session reset, which moves after the unit's
  context is made and runs on it. The watch is armed later, so the move
  changes nothing for the watch.
- After each unit, the catalog check that tells a dropped database from a
  failure (`databaseExists`, `collect/dropped.go`, reached when a unit's
  error carries 911), the ping, the recycle and the one reconnect, with each
  of the reconnect's three calls: `Connect`, the reset, and the reading of
  the new session's id (`spid, _ = sessionID(...)`, whose error the loop
  ignores, so that on the run's context it would be a query of up to
  `SQL_QUERY_TIMEOUT_SEC` after the bound with nothing to report it).

What does not:

- `leave`, the reset after a unit's rows are read. It releases the database
  the session is in, it runs on the run's context as today, and on a
  connection the driver closed after a cancellation it fails at once.
- The blocking watch, whose parent stays the run's context. Its start is the
  one server step before the loop that the bound does not cut (below), and a
  poll it has in flight when the bound passes runs to its end.
- Hashing the corpus, the run folder, the files of a unit that finished
  reading, the manifest and the zip, which are local.

## When the bound is checked

### Before the first unit

The steps before the first unit fall on two sides of `prepareRunFolder`, which
is where the previous run of the same server and day is renamed aside as
`.superseded-HHMMSS`. `Connect`, the preflight, the instance probe and the
listing come before it; reading the session id and starting the watch come
after it, in today's order (`collect/collect.go`: `prepareRunFolder`, then
`planUnits`, then `sessionID`, then `startBlockingWatch`). The bound is
treated differently on each side, because what a cut leaves on disk differs.

Before the run folder. `stoppedOr`, the path of every server step before the
run folder that can fail, asks one function, `preambleStop(ctx, bound
context.Context, err error) (byOperator, byBound bool)`, in this order: a
dead run context is the operator's stop, as today; otherwise an error that
carries a SQL Server error number is the step's own failure and keeps its
words and its exit code, as `maxDurationOr` does in the loop (step 3 below);
otherwise, when `boundReached`, it is the bound's. A login refused with 18456
as the bound passes is then reported as a refused login, exit 1, rather than
as a bound the operator would raise to no effect (measured on the lab:
`Connect` with a wrong password returns an error from which
`sqlErrorNumber`, through the `cannot reach the instance: %w` wrapping, reads
18456). For the bound's case the run sets `run.max_duration_reached`, drops
the error (it describes the cut, as a `ctrl-c`'s does), records a warning,
and finishes with exit 2, returning the same sentence as its error:
`the collection reached its maximum duration of 1m00s (60 s) before the first collector: nothing was collected`.
Without this, a `Connect` cut by the bound comes back as exit 1 with
`cannot reach the instance: context deadline exceeded` (measured on the lab:
`Connect` on an expired context returns that error in 29 µs), about an
instance that was answering.
The preflight does not return an error: probes cut by the bound come back as
`error` statuses, `PreflightExitCode` returns 1, and the run reaches
`stoppedOr` with an error of its own that carries no number, so the bound's
path applies. And the run asks `boundReached` once more just
before `lockRun`, after the listing, and takes the same path when it has
passed, so that a bound reached during the last server step before the run
folder, or during the local steps after it, does not go on to set the
previous run aside.

Such a run ends before the run folder exists, so its manifest goes where
every failed run's goes, and the previous run of the day is not touched.

The check before `lockRun` is the one rule of this section a test cannot
reach from outside `Run`: a bound short enough to pass before it passes before
`Connect` (a one-millisecond bound has expired before the corpus is hashed),
and the run then leaves through `stoppedOr` and never comes near `lockRun`,
with or without the check. So `Run` gains a test seam, a package variable
`pauseHook func(point string, bound context.Context)`, nil outside tests,
called at two points: `"before the run folder"`, immediately before the check
before `lockRun`, and `"before the session id"`, immediately before
the session id is read, after `prepareRunFolder` and `planUnits`. A test sets
it to wait on `bound.Done()` at one of the two points, which places the
bound exactly there (criteria 5 and 16). It is the first seam of its kind in `collect`; the alternative was
to leave the check with no criterion.

After the run folder. A bound that passes between `lockRun` and the first
unit is not a step failing: the run folder exists and the previous run has
been set aside, so the run goes on into the loop, where the check before each
unit skips every unit with the bound's reason. The run writes its manifest and
its archive in the run folder, exits 2 through `settleRun` (below), and keeps
the previous run aside as `.superseded-HHMMSS` with the warning that says so,
as any partial rerun does. Of the two server steps on this side, reading the
session id runs under `bound`; then, before the blocking watch starts, `Run`
asks the bound once more. Once it has passed, the watch is not started, and
`blocking_watch.reason` says `not started: the collection reached its maximum
duration before the first collector`. A failure to read the session id whose
cause is the bound is treated the same way, so that a run cut there does not
carry the warning that the watch is off for an unreadable session id.

Nor does it carry the rest of today's warning. When the watch is off, `Run`
records `the blocking watch is off, <reason>: nothing will cancel a collector
that other sessions are waiting on` and prints `note: the blocking watch is
off, <reason>` on stderr. When the reason is the bound, no collector will
start, and both sentences would describe a risk the run no longer runs. So a
watch not started for the bound adds no warning and no stderr note:
`blocking_watch.reason` and the bound's own records (the flag, the skips, the
note after the loop) say what happened.

### Before each unit

The loop asks whether the bound has passed before starting a unit, after
`heldBack` and `droppedBefore`: a unit on a database already found dropped,
or held back by the watch, keeps that reason, which is more specific and is
the one `scopeLost` matches on. Every unit that reaches the check after the
bound has passed is skipped with the bound's reason, without touching the
connection.

The three questions become one function, `skipBefore(cancelledOn, droppedOn,
bound, limit, target) (reason string, byBound, skip bool)`, so that their
order is tested without a server. `byBound` says which question answered:
the loop needs it to set `UnitSkipped.MaxDuration`,
`run.max_duration_reached` and the count of the note after the loop, and
asking `boundReached` again after `skipBefore` returns would answer "the
bound" for a unit held back or on a dropped database once the bound has
passed, counting it in the note while `MANIFEST.txt` files it under its own
reason. The reason is `maxDurationText(limit)` followed by ` before this
collector started`.

### During a unit

A unit still running when the bound passes is stopped. The alternative,
letting it run to its own `@timeout`, means a bound of 23:00 can be overshot
by 1 800 seconds whenever `055.page-density` or `041.compression-savings`
starts at 22:59, and the reason the option exists is the window promised to a
client. A bound that holds only when the slow unit does not happen to be
running is not a bound.

The stop reuses the watch's pattern: the unit's context inherits the bound's
deadline and its cause. The run's context is not touched, for the reason
`recordUnitFailure` gives: a dead run context means the operator stopped the
run, and the bound must not be filed as a `ctrl-c`.

How a stopped unit's error is told apart. One helper,
`maxDurationOr(parent, call context.Context, limit time.Duration, err error)
error`, decides, in this order:

1. The run's context is dead: `err` unchanged. The stop is
   `recordUnitFailure`'s to file, and it will drop the error.
2. The cause of `call` is not `errMaxDurationReached`: `err` unchanged.
   `call` is the context the failing server call ran on, the innermost one,
   and not the unit's context. This covers a unit cancelled by the watch first
   (the first cause wins: measured, a unit context cancelled with a
   `*blockedError` keeps that cause after the bound passes, and so does a
   query context derived from it, while `bound` itself then carries
   `errMaxDurationReached`), and a call whose own deadline expired first.
3. The error carries a SQL Server error number: `err` unchanged, as
   `outOfTime` already does for its own deadline.
4. Otherwise, a `*maxDurationError` that names the bound and wraps the
   driver's error.

Why the innermost context. The query runs on `qctx`, the `USE` on `uctx`, the
first reset on a deadline context of its own, each a child of the unit's
context with its own limit. When that limit expires first, the driver waits
for the server to confirm the cancellation, up to five seconds and sometimes
ten, and the bound can pass during that wait. Measured with contexts built as
`Run` will build them (a bound at 150 ms, the unit's context under it, a query
context of 50 ms under that, read after the bound): `Cause(qctx)` is
`context deadline exceeded`, the query's own, while `Cause(unitCtx)` is
`errMaxDurationReached`, which the bound propagated after the fact. Asking the
unit's context would file a real `@timeout` expiry as a bound cut. Asking
`call` gives the right answer in the three orders: when the bound passes
first, `Cause(qctx)` is the bound's cause and `qctx.Err()` is
`DeadlineExceeded`; when the watch cancels first, both carry the
`*blockedError`.

It is applied on each return of `runUnit` that comes from a server call made
under the unit's context, with that call's context: the first reset (which
`runUnit` then writes as `dctx := deadline(unitCtx, cfg)` and
`ResetSession(dctx, ...)` rather than through `resetWithDeadline`, so that it
holds the context it must ask), the `USE` with `uctx` (whose error today
reaches only `blockedOr`, so a cut `USE` would be filed as a bare "context
deadline exceeded", as one reader measured on a prototype), and, inside
`outOfTime`, the query and `ReadResultSets` with `qctx`. In `outOfTime` it comes after the parent test
and the SQL error number test, and before the `@timeout` message, which would
otherwise name a limit that did not expire: the query context
(`context.WithTimeout(unitCtx, timeout)`) inherits the bound's deadline when
it is earlier, and `qctx.Err()` is then `DeadlineExceeded`. `blockedOr` stays
outermost, as today. It is not applied to the writing of files, which takes no
context: a disk error after the bound is a disk error.

`*maxDurationError` is a type, as `*blockedError` is, because the loop has to
recognise it (`errors.As`) to set `run.max_duration_reached` for a unit
stopped rather than skipped. The loop does not decide what it does next from
the error, though. It asks `boundReached`, which is true whatever the error
became, so a unit stopped at the bound with a coincident SQL error is recorded
with its SQL error and still followed by the bound's skips.

What a stopped unit costs:

- Its work is lost. It is recorded as an error, `ErrorEntry`, with its
  `duration_ms`, and a message that names the bound and keeps the driver's
  words, as `outOfTime` does: `stopped when the collection reached its
  maximum duration of 2h00m (7200 s): context deadline exceeded`, the
  duration written by `formatCeiling` as everywhere else in this feature. The
  driver's words are whatever it returned: on a statement the server took
  longer than five seconds to cancel, one reader measured `Invalid TDS stream:
  did not get cancellation confirmation from the server (current response:
  context deadline exceeded)`. An error and not a skip, because it started:
  the time it consumed is the most useful fact in the record, and only
  `ErrorEntry` carries a duration.
- It leaves no partial file. `runUnit` reads every result set into memory
  (`ReadResultSets`) before it writes anything, and a writer receives
  `WriteRequest` with no connection and no context. The bound can land in the
  first reset, the `USE`, `QueryContext` or `ReadResultSets`, all before the
  first byte is written. A unit that finished reading just before the bound
  writes its files to the end; that part is local disk, not server work.
- The blocking watch's record stays true. A unit stopped at the bound may be
  holding locks others wait on while the driver waits for the cancellation,
  and the watch can reach its five seconds in that interval. It then calls
  the unit's cancel with a `*blockedError`, which changes no cause (the
  bound's came first) but sets `fired`. In `runUnit`'s deferred switch today,
  `fired` without a `*blockedError` leads to the warning that "the collector
  had already read its rows, and the waiter was released when the session
  left the database", which is false for a unit stopped mid-query. The next
  case, the worst wait seen, is false too: `fired` is set only when a wait
  has reached the watch's limit, and its sentence ends "under the blocking
  watch's 5s limit" (one reader built the case on a copy of the tree and got
  `session 70 had been waiting on this collector for 5.3 s (...), under the
  blocking watch's 5s limit`). So the switch gains a case of its own, after
  the `*blockedError` case and before `fired`: an error that is a
  `*maxDurationError` with `fired` set writes `<script> on <target>: <worst
  wait>; the collector was being stopped at the collection's maximum duration
  when the blocking watch reached its 5s limit`, and the unit is not counted
  in `cancelled_units`. A `*maxDurationError` without `fired` falls through
  to the existing cases unchanged. A unit stopped at the bound whose error
  kept a coincident SQL error number (step 3) is not a `*maxDurationError`,
  and still gets the `fired` sentence; that coincidence is left as it is.

### After a unit

After each unit, the loop asks, in this order: the operator's stop (as
today), then `boundReached`. When the bound has passed, it neither pings, nor
recycles, nor reconnects, after a success as after a failure: it goes to the
next unit, which the check before each unit skips, and then to the manifest.
Nothing after the loop uses the collection connection.

A unit that fails on its own a moment before the bound still goes through the
catalog check for a dropped database, the ping and possibly the reconnect, and
those now run under `bound`. A failure of one of them asks the stop, then the
bound, before it ends the run with exit 1, and before it prints anything: a
reconnect cut by the bound is not a lost instance, and the loop goes on to the
skips. Without this, a failure just before the bound costs up to twice
`SQL_CONNECT_TIMEOUT_SEC` and two `SQL_QUERY_TIMEOUT_SEC` past it, or an exit
1 that the bound caused.

The order matters for the ping in particular. Today, when `connAlive` returns
false, the loop prints `connection lost; attempting one reconnect` before it
tries anything. A ping made on an expired context fails at once (one reader
measured 1.3 ms, and the connection was closed afterwards), so a bound landing
during the ping would put that sentence on stderr about a connection that was
fine. The loop asks the stop and then `boundReached` when `connAlive` returns
false, and prints the sentence only when neither explains the failure.

The catalog check runs under `bound` rather than the run's context, where it
runs today. A 911 that coincides with the bound is left as it is by
`maxDurationOr` (step 3), so the check is reachable after the bound; on the
run's context it would be a new query for up to `SQL_QUERY_TIMEOUT_SEC`.
Under `bound` it fails at once, `databaseDropped` reads a check that could not
be made as "not shown dropped", and the unit is recorded with its 911 error.
That is the price: a database really dropped in the same instant as the bound
is filed as an error rather than a drop, in a run that exits 2 for the bound
in any case.

## What the bound holds

The bound counts from the instant `Run` takes `started`, the instant
`run.duration_sec` in `_run.json` is measured from. It includes hashing the
corpus, connecting, the preflight, the probe and listing the databases. It
does not include what the wizard or `check` did before `Run` was called.

The reason is that the operator has one number to compare against: the bound
and `duration_sec` share their origin. Counting from the connection would
exclude a dial that can take twice `SQL_CONNECT_TIMEOUT_SEC`, and a
maintenance window counts from when the job started, not from when the server
answered.

The promise, which the README states in these terms: no collector starts after
the bound, and the statement running when it passes is cancelled. It does not
say that the server work ends within a few seconds of the bound, as the
previous draft did: two of the items below, `leave` and the start of the
watch, can each run for tens of seconds past it. What the run can still spend
after the bound, in full:

- The cancellation of the statement in flight. The driver (go-mssqldb
  1.10.0) sends an attention and waits for the server to confirm it, up to
  five seconds (`cancelDrainTimeout`), and once more up to five seconds when
  the confirmation is not in the response it was reading. One reader measured,
  on a prototype, a unit stopped at a 4-second bound returning after 5.373 s,
  and at a 15-second bound after 20.019 s, on an `INSERT` of 80 million rows
  into a table of a lab database.
- The server's own rollback of a cancelled statement. When the driver gives up
  waiting, the server may still be undoing what the statement wrote: in the
  same measurement, 197 381 rows of the cancelled insert were still visible
  after `runUnit` had returned. The tool cannot bound that. The collectors
  write nothing to user tables, but some write to tempdb, and
  `041.compression-savings` samples data there through
  `sp_estimate_data_compression_savings`, which is the statement whose
  cancellation is most likely to cost a rollback.
- `leave`, after a unit that had read its rows when the bound passed:
  milliseconds as a rule, at most `SQL_QUERY_TIMEOUT_SEC`.
- The start of the blocking watch, if the bound passes during it: 388 ms on
  the lab, 37.5 seconds at most on the defaults.
- A poll of the blocking watch in flight when the bound passes. The watch
  polls only while a unit is armed, and after the bound no unit is, so there
  is at most one; but its retries wait on the run's context (20.5 seconds by
  the watch's own comment, plus a read of the session's identity), and `Run`
  waits for the watch's goroutine when it returns, after the manifest and the
  zip. It is a read of the DMVs on the watch's own connection, and it delays
  `Run`'s return, not the archive.
- Local work: the files of a unit that finished reading, the manifest, and the
  zip (0.05 seconds on the lab's 108-second run).

So "the bound limits server work" in the first draft was too strong. The
README says what is above, in fewer words: the bound stops the collection and
cancels the query running at the bound; the server can take a few seconds to
confirm the cancellation and longer to undo what that query had written; the
session's reset, the blocking watch's start and its last poll are not bounded
and can each take up to their own limits past it; the manifest and the
archive are written after it.

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
with a cost option on, bounded or not; nothing reads that order as
meaningful. A run with no cost option keeps today's order exactly. No
dependency is broken: the only ordering the corpus relies on is
`021.query-store-detail` before `022` (`queryStoreArgs`), both ungated by cost,
and no collector reads what `041` or `055` produce. One reader applied the
move as a stable partition at the end of `planUnits` on a copy of the tree:
the whole suite stayed green.

The rest of the corpus keeps its path order. A finer ordering, cheapest first
or by value to the audit, would be a judgment this document has no measure
for.

## How a bounded run is recorded

In `_run.json`:

- `run.max_duration_reached: true`, `omitempty`, for the same reason
  `run.cancelled` is: every manifest written before it, and every run the bound
  did not reach, stays byte-identical in that field. It is set when the bound
  cut something: a unit skipped for it (`skipBefore` answered `byBound`), a
  unit stopped by it (a `*maxDurationError`), or a step before the run folder
  (`stoppedOr` through `preambleStop`, or the check before `lockRun`). It is
  not set merely because the bound passed: a bound that passes during the last
  unit's `leave`, the manifest or the zip cut nothing, and the run is
  complete. Nor is it set when the operator's stop lands first in the
  unit's own classification: a `ctrl-c` pressed while the driver waits for
  the cancellation of a unit the bound has just cut (the moment an operator
  watching a gauge frozen at the bound is likeliest to press it) makes
  `maxDurationOr` return the error unchanged at step 1, `recordUnitFailure`
  drops it, and the loop breaks with `run.cancelled` set and no bound skips.
  The run is recorded as stopped by the operator, which it also was; the
  command line exits 2 either way, and the wizard exits 0 as for any stop.
- `config.max_duration_sec`, the bound in seconds as a string, `"7200"`, in
  the unit of `duration_sec` beside it. The key is absent when no bound was
  set, so that its absence means the same thing in this version's manifests
  and in older ones.
- One `skipped_scripts` entry per unit not started, with its target and one
  reason shared by all of them:
  `the collection reached its maximum duration of 2h00m (7200 s) before this collector started`.
  It is one constant string per run, built by one function, because
  `MANIFEST.txt` groups on it.
- The unit stopped at the bound, if any, in `errors`, as described above.
- For a cut before the run folder, a warning carrying the sentence the run
  returns, `the collection reached its maximum duration of 1m00s (60 s) before
  the first collector: nothing was collected`. `stoppedOr` drops the step's
  error, as it does for a stop, so without the warning nothing in that
  manifest but the flag would say what happened.

In `MANIFEST.txt`:

- The duration line says it, since a reader of `MANIFEST.txt` alone has no
  other way to learn the run was cut:
  `Duration     : 7204 s, stopped at the maximum duration of 2h00m (7200 s)`.
  The line is printed today only when `duration_sec` is positive
  (`collect/manifest.go`); when `max_duration_reached` is set it is printed
  whatever the duration, so that a run cut in its first second, which only a
  test can produce, still says so.
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

- On the command line, a note on stderr once the loop is over, counted from
  the units actually skipped for the bound (`byBound`), so that it agrees with
  the grouped entry of `MANIFEST.txt` (a unit kept back by `heldBack` or
  `droppedBefore` is not counted). Its second half depends on what the bound
  cut, since a bound that passes between units, or after the run folder and
  before the first unit, stops nothing. One function,
  `maxDurationNote(limit, notStarted int, stopped bool)`, writes it:

  | Not started | A unit stopped | The note |
  | --- | --- | --- |
  | 198 | yes | `note: the collection reached its maximum duration of 2h00m (7200 s); 198 collectors were not started, and the one running then was stopped` |
  | 198 | no | `note: the collection reached its maximum duration of 2h00m (7200 s); 198 collectors were not started` |
  | 1 | no | `note: the collection reached its maximum duration of 2h00m (7200 s); 1 collector was not started` |
  | 0 | yes | `note: the collection reached its maximum duration of 2h00m (7200 s); the collector running then was stopped` |

  It is printed only when `max_duration_reached` is set, so the case of
  nothing skipped and nothing stopped does not arise. A cut before the run
  folder never reaches the loop and prints no note; the command line prints
  the error `Run` returns, which is the bound's sentence.
  The loop takes milliseconds once the bound has passed, since it no longer
  touches the server. The summary line scripts parse gains a token, as it
  does for a stop:
  `102 result(s), 210 skipped, 1 error(s), max duration reached`
  (illustrative, for the 301 units of the lab's run with both cost options:
  12 skipped by the plan, 198 by the bound, one stopped).
- The observer is told each skip through `UnitDone` with a `*UnitSkipped`,
  as `heldBack` does, so the gauge reaches its total. But neither screen may
  print it per unit, as both do for the watch's skips today. The command
  line's gauge (`cmd/sql-auditor/progress.go`) prints every `*UnitSkipped` as
  a permanent `-- <unit>: skipped: <reason>` line, tty or not; one reader fed
  it 198 skips with the bound's reason and got 198 lines before the note. The
  wizard adds a note per skip to a list it keeps at its last six
  (`maxNotes`), so the bound's skips would push the stopped unit's error off
  the screen. So `UnitSkipped` gains a field, `MaxDuration bool`, set only
  for the bound's skips; the command line's gauge counts such a skip without
  printing it, and the wizard counts it in its skipped total without a note.
  The one line that says it is the note after the loop on the command line,
  and the last screen in the wizard.
- The wizard's summary line counts what was collected. Today `summaryLine`
  prints `s.DoneUnits` as "N collected", and `unitDoneEvent.apply` raises
  `DoneUnits` for every `UnitDone`, skips and errors included, which is right
  for the gauge and wrong for that word. Fed 289 planned units, 90 successes,
  one error and 198 skips with the bound's reason, today's final screen reads
  `289 collected, 198 skipped, 1 error, 0 permissions denied` (measured on the
  tree of `f723563`). The over-count exists today for failed units and the
  watch's skips, a handful at most; the bound makes it the whole tail of the plan, on the
  screen the operator reads before mailing the archive. So `State` gains
  `CollectedUnits`, raised by a `UnitDone` with no error, and `summaryLine`
  prints it; the gauge keeps `DoneUnits` against `Units`. The same count
  decides the wizard's exit (next section).
- `Observer.Finished` carries the bound as well as the cancellation,
  `Finished(cancelled, maxDurationReached bool)`, from the same manifest
  fields; the wizard's `observer.Finished` puts both into its
  `finishedEvent`, and `State` gains `MaxDurationReached`. The wizard's last
  screen fits the bound into `renderDone`'s switch, which since `3a8cc59`
  tests `ZipPath == ""` before `Cancelled`, with the bound before the stop in
  each branch, since the bound came first when both are set:

  | Archive written | Bound | Stopped | First line |
  | --- | --- | --- | --- |
  | no | yes | either | `Collection stopped at its maximum duration. No archive was written by this run.` |
  | no | no | yes | `Collection stopped. No archive was written by this run.` (today) |
  | no | no | no | `No archive was written by this run.` (today) |
  | yes | yes | either | `Collection stopped at its maximum duration. This archive is partial:` |
  | yes | no | yes | `Collection stopped. This archive is partial:` (today) |
  | yes | no | no | `Send this file to whoever requested the audit:` (today) |

  The lines under it are unchanged: the archive's path and size, or, with no
  archive, the earlier run's file at the same name when there is one
  (`PreviousZip`). The previous draft's `Collection reached its maximum
  duration before anything was collected.` is gone: "no archive" is not
  "nothing collected". A bounded run that collected a hundred units and then
  failed in `Zip` or in the manifest write returns an error and shows no
  archive, while its run folder holds the hundred results. Where the cut
  happened is said by `Run`'s error, which `collectDoneEvent.apply` already
  adds to the notes under the summary: the bound's sentence ending "nothing
  was collected" for a cut before the run folder, the zip's error otherwise.

## Exit code and the previous run of the day

A run the bound cut exits 2, the README's code for a partial run, on the
command line. `settleRun` already says why 0 is wrong here, in its own words:
0 "told every scheduler, runbook and CI job that stopped a collection at its
time limit that the collection had succeeded". A bound reached is that case
with the tool holding the clock. A run whose bound passed after its last unit
exits as it would have without one.

Where `exit` becomes 2. A unit stopped at the bound is a failed unit, and
`recordUnitFailure` already returns 2 for it. But a run whose bound passed
between two units, or during a reset or a ping, fails no unit: every unit left
is skipped, `runUnit` is never called again, `exit` stays 0, and
`settleRun(0, false)` returns 0 and allows the previous run to be deleted
(one reader checked it by reproducing `settleRun`). So `settleRun` takes the
bound beside the cancellation: it is called as `settleRun(exit,
m.Run.Cancelled || m.Run.MaxDurationReached)`, and its second parameter is
renamed for what it now means, a run cut short, by the operator or by the
bound. The rule inside does not change: a cut run that would exit 0 exits 2.
A cut before the run folder exits 2 through `stoppedOr`, above; a cut after
it goes through the loop and `settleRun`.

In the wizard, a bounded run that is cut, produced an archive and collected
at least one unit exits 0, as a stopped one does. The README gives the reason
for a stop: "an operator who stops the collection from the wizard has read
the screen that calls the archive partial, and the wizard exits 0". The bound
is the same decision taken in advance: the wizard shows it on its third screen
before the collection starts, and its last screen calls the archive partial.
That reason needs something to send. A bound reached before the run folder
produces no archive, and a bound reached after the run folder but before the
first unit returned anything (while the session id is read, before the watch
starts, or during the first unit) produces an archive of skips and at most
one error. With the one-minute floor and a preamble of up to five minutes on
the defaults, that second case is what a too-short bound actually produces.
Both exit 2 in the wizard, as on the command line: a scheduler wrapping the
wizard must not record success for a run with nothing to send.

How the wizard knows. Today `collectDoneEvent.exitStatus()` takes no
argument and sees only `Run`'s code, its error and whether the wizard's own
context was cancelled; the run's verdict reaches the wizard as a separate
`finishedEvent`, which only updates `State`. Every event of the collection is
sent from the one goroutine that calls `Run`, on one channel: `UnitDone` and
`Finished` from inside `Run` through `observer.send`, a plain blocking send,
and `collectDoneEvent` after `Run` returns through `runner.send`, which
selects on the channel and on `r.done`. That `select` can drop the
`collectDoneEvent`, but only once `r.done` is closed, when the wizard is
quitting and the loop no longer reads; it cannot reorder it. So every
`unitDoneEvent` and the `finishedEvent` are applied before the
`collectDoneEvent`. The design uses that order: `finishedEvent` carries
`maxDurationReached` into `State`, `unitDoneEvent` raises `CollectedUnits`,
the `coded` interface becomes `exitStatus(s State) int` (`panicEvent`'s
ignores the state and still returns 2), and the loop passes the state as it
stands before the event is applied. `collectDoneEvent.exitStatus`
then decides in this order:

1. `s.MaxDurationReached` is set: 0 when `Run` returned no error and
   `s.CollectedUnits` is above zero, and otherwise `Run`'s code, which is 2
   for every run the bound cut.
2. The wizard's own context was cancelled: 0, as today.
3. Otherwise `Run`'s code, as today.

The bound is asked before the cancellation because the bound came first. A
`ctrl-c` pressed after the bound has cut the run before the run folder, while
`Run` writes its failed-run manifest, cancels the wizard's context too; with
the cancellation first, that run would exit 0 with no archive (two readers
drove exactly these two events through `loop` with the previous draft's rule
and got 0). With the bound first it exits 2, as the command line does. A
`ctrl-c` after a bound that cut a run with an archive and something collected
exits 0 under either order. A stop that lands before the bound is asked
(step 1 of `maxDurationOr`, in "How a bounded run is recorded") leaves
`MaxDurationReached` unset and is a stop.

`Run` returning no error is the test for "an archive was produced", and not
the presence of a `.zip` at the run's name. That rule is already on main:
`3a8cc59` made `archiveOf` (`tui/run.go`) offer the archive only when `Run`
returned no error, and name a file older than the run found at the run's name
as an earlier run's, with its time (`PreviousZip`). The bound relies on that
rule and adds nothing to it.

The README's paragraph on the wizard's exit code names the bound beside the
stop, and says that a bound that cut the run before anything was collected
exits 2.

The previous run of the same server and day is kept, by two locks:

- `settleRun` allows the previous run to be deleted only after exit 0, so a
  bounded run that was cut never reaches `previousRunLost`.
- If it did, `skipLoses` would answer "a loss" for the bound's reason, which
  falls under "any other reason" in its table. The table in its comment gains
  a row, "reached the maximum duration: yes, though such a run already exits 2
  and keeps prev as partial", the same wording as the blocking watch's row.
  Like that row, it is a line of the comment and not a case in the code: the
  value is the fallback's `true`. The table test asserts `true` for the
  bound's reason by name, so that a case added above the fallback that
  happened to match it would fail.

`settingsLost` does not compare the bound. A bound that was not reached
changed nothing in what was collected, and one that was reached makes the run
exit 2, so naming it would only repeat what the exit code already decided.

The rerun of the day follows. An operator who reruns after a bounded run
finds the bounded archive set aside as `.superseded-HHMMSS`. A rerun deletes
it only when it exits 0 and `previousRunLost` finds nothing it lost: the same
server name, address and `SQL_DATABASE`, a profile and options at least as
wide, the same value settings, every database the bounded run read, and every
collector the bounded run ran to a result run again or skipped for a reason
that loses nothing. The units the bounded run skipped for the bound produced
nothing, so they cost nothing in that comparison; the unit it stopped is an
error, and a unit run only to an error holds nothing either. A complete rerun
of the same plan therefore deletes it; a narrower one keeps it and says why. A
rerun that is bounded again and cut is partial again and keeps it, so two
bounded runs in one day leave two archives on disk, each partial at a
different point.

## What check and the wizard announce

When a bound is set and the plan could be priced, `check` adds two lines under
its ceiling. The first states the rule, the second compares figures and makes
no forecast:

```
Duration, a ceiling and not an estimate:
  8 databases; costly collectors on: 70.schema/041.compression-savings.sql, 70.schema/055.page-density.sql;
    at most 6h00m (21600 s) if every one of their 12 units runs to its @timeout
  all 301 units: at most 15h31m (55890 s) if every one runs to its @timeout
  bounded by MAX_DURATION=2h (from .env): no collector starts after 2h00m (7200 s), and the one running then is stopped;
    the costly collectors run last; the 289 units before them: at most 9h31m (34290 s), above the bound
```

(Illustrative: the first four lines are the real output above, the last two
do not exist yet.)

The cases of the second line:

- The whole ceiling is at or under the bound: `the ceiling of the units is
  under the bound`.
- A costly collector is on, and the whole ceiling is above the bound: the line
  above, which gives the ceiling of the units before the costly ones and says
  whether it is above or under the bound.
- No costly collector is on, and the ceiling is above the bound: `the ceiling
  is above the bound; if it is reached, the collectors last in the plan are
  the ones not run`.

The first draft named the costly collectors as "the first to go" whenever
their ceiling alone exceeded the bound. That is true of the order and
misleading about the outcome: measured on the lab with `--profile space` and
both cost options, the costly units' ceiling was 28 800 s of a whole 50 160 s,
so the units before them could take 21 360 s, three times a two-hour bound,
and a bound reached there cuts ordinary collectors too. Giving the ceiling of
the units before the costly ones lets the operator see it. Even when that
ceiling is under the bound, the line does not promise that only costly
collectors will be cut, because the bound also counts the steps before the
first unit and the resets between units, which no ceiling includes.

The figures are the ceilings `PlannedDuration` already computes; the second
is `Ceiling` minus `CostlyCeiling`, and its unit count `Units` minus
`CostlyUnits`. `check` does not walk the plan with cumulative timeouts to name
the unit where a worst-case run would stop: on the lab that would announce a
stop around the fortieth unit for a run that collects all 289 in 108 seconds,
and a forecast three hundred times too pessimistic misleads more than it
informs.

The provenance, `(from .env)`, `(from --max-duration)` or `(from the
environment)`, is printed because the precedence of this tool is the reverse
of most, a `.env` beating an exported variable, and a bound nobody remembers
setting is the one that will be argued about.

The wizard's third screen shows the same lines under the same ceiling,
displayed and never edited, as the Query Store window is: `.env` stays the
place where settings live. The last screen's wording is above.

## What is not in scope

- A bound given as a time of day, `--until 23:00`. The decision is a
  duration, and a time of day raises the question of whose clock, which the
  Query Store window already had to answer for the server's.
- Bounding the post-loop phases (the manifest and the zip), the blocking
  watch's start, and a poll the watch has in flight when the bound passes.
- Bounding the server's rollback of a cancelled statement, which no client
  can.
- A finer ordering of the corpus than "costly last".
- Saying in `MANIFEST.txt` that the operator stopped a run. It does not say so
  today: `run.cancelled` is in `_run.json` and on the screens, and nowhere in
  `Human()`. The bound's line above would make the bound the only kind of stop
  `MANIFEST.txt` reports; see the open questions.

## Tests

One criterion per decision. Each names what the test must show, and where a
live instance is needed it uses the lab's SQL Server 2025 as the existing live
tests do, with a corpus built in the test (a `fstest.MapFS` of two or three
scripts, as `collect/unit_live_test.go` builds its probes; its preamble took
0.48 s on the lab). A live test that creates a database names it `ZzMaxDur…`
and drops it.

A criterion that cannot fail is a defect of this document, not a detail, and
the third panel found three. So each criterion written or changed by the
third revision ends with "Fails when", the change to the future code that
makes it fail; the review of the implementation makes that change and watches
the test fall before it believes the test.

1. Format and floor. `Resolve` with `MAX_DURATION` set to `90m`, `2h`,
   `1h30m`, `1.5h` and `5400s` gives the same 5 400 seconds for the third,
   fourth and fifth; `30s`, `0`, `-5m`, `1m0.5s`, `7200` and `two hours` are
   each refused with an error naming the key. The flag beats `.env`, which
   beats the environment, as for every other key, and `MaxDurationFrom` names
   `--max-duration`, `.env` or `the environment` accordingly. Unset gives no
   bound. A `.env` holding `MAX_DURATION` passes `checkKeys`. And through the
   command line's own path, not `Resolve` alone: `buildOptions("collect",
   ...)` with `--max-duration 2h`, and again with `MAX_DURATION=2h` in a
   `.env` and no flag, returns an `Options` whose `Config.MaxDuration` is two
   hours; removing the entry from the `flags` map of `optionsFrom` must make
   the first case fail. The embedded `.env.example` holds the key only on a
   commented line, so that `ParseDotEnv` of it yields no `MAX_DURATION`.
2. `--all` and profiles. `buildOptions` with `--all` and no bound gives a
   `Config.MaxDuration` of zero, and neither `KnownFlags` nor `ValueFlags` has
   an entry naming the bound; a run with `--all` and no bound records no
   `max_duration_sec`. Adding the bound to `KnownFlags` must make this test
   fail. `TestAllTurnsOnEveryOptIn` is not that guard: one reader added
   `"max_duration": "--max-duration"` to `KnownFlags` and to the options'
   `Flags` map and it still passed, and only the wizard's
   `TestTheWizardOffersEveryOptInTheCommandLineHas` failed.
3. The order of the skips. `skipBefore`, as a table, with a two-hour
   `limit`: a target held back keeps the watch's reason and a dropped one
   `skipDroppedDuringRun`, with the bound passed or not, and both answer
   `byBound` false; any other target is skipped exactly when `bound` carries
   `errMaxDurationReached`, with `byBound` true and the reason exactly `the
   collection reached its maximum duration of 2h00m (7200 s) before this
   collector started`, and not when `bound` was cancelled by a `ctrl-c`.
   Fails when the bound's question is asked before `heldBack` or
   `droppedBefore` (the held-back row with the bound passed gets the bound's
   reason), when `byBound` is computed as `boundReached` rather than from the
   question that answered (the same row answers true), or when the reason is
   built from anything but `limit` (the string differs).
4. The classification. `maxDurationOr`, as a table, with contexts built in the
   test as `Run` and `runUnit` build them (`bound`, the unit's
   `WithCancelCause` child, a call context with its own timeout under it), a
   two-hour `limit` and no server. Rows that leave the error unchanged: the
   run's context cancelled after the bound has passed (built in that order:
   the bound passes first, so that `Cause(call)` is the bound's, then the run's
   context is cancelled; a row built as a plain `ctrl-c` with the bound an hour
   away cannot tell the order of the tests, since step 2 already returns its
   error unchanged, as one reader measured); a unit context cancelled by a
   `*blockedError` before the bound passed; a call context whose own timeout
   expired first; the straddling case, where the call context's own timeout
   expires and then the bound passes before the error is classified (the
   driver's wait for the cancellation); and a SQL Server error number
   coinciding with the bound. The row that changes it: a bare context error
   after the bound becomes a `*maxDurationError` whose message is exactly
   `stopped when the collection reached its maximum duration of 2h00m (7200
   s): context deadline exceeded`. Fails when the bound's test is moved before
   the parent test (the first row is relabelled), before the SQL error number
   test (the last unchanged row is relabelled), or asks the unit's context
   instead of the call's (the straddling row is relabelled).
5. A bound passed before the run folder, live, in three parts.
   a. `preambleStop`, as a table without a server: the run's context
   cancelled after the bound passed gives `byOperator`; the bound passed and
   a bare context error give `byBound`; the bound passed and an error wrapping
   an `mssql.Error` numbered 18456 inside `cannot reach the instance: %w`
   give neither; the bound not passed gives neither. Fails when the bound is
   asked before the stop (first row) or before the SQL error number (third
   row).
   b. `Run` with a `Config` whose `MaxDuration` is one millisecond, which has
   expired before `Connect`: `run.max_duration_reached` is true,
   `run.cancelled` is absent, the exit code is 2 and not 1, the returned error
   and a warning in `_run.json` are exactly the bound's sentence ending
   "before the first collector: nothing was collected", the returned error
   does not begin with `cannot reach the instance`, and `MANIFEST.txt` has a
   duration line naming the bound although the run took less than a second.
   Fails when `stoppedOr` does not ask `preambleStop` (measured: `Connect` on
   an expired context returns `cannot reach the instance: context deadline
   exceeded`, and the run exits 1), or when the duration line keeps today's
   test on a positive `duration_sec`.
   c. `Run` with a bound of three seconds and `pauseHook` set to wait on
   `bound.Done()` at `"before the run folder"`, in an output directory where
   the test has planted a folder and a `.zip` at the run's name (from
   `RunFolderFor`, with the server name the test reads on its own connection
   and the `Now` it passes): the hook was called, the exit code is 2,
   `run.max_duration_reached` is true, no run folder was prepared, nothing
   named `.superseded-*` exists, and the planted folder and `.zip` are still
   at their names. If the preamble outlasts three seconds the hook is never
   called and the test fails saying so, rather than passing on the
   `stoppedOr` path. Fails when the check before `lockRun` is missing, or
   placed before the hook's point (the run goes on to `prepareRunFolder` and
   sets the planted run aside).
6. A unit stopped at the bound, live. A first instance-scope script with
   `@timeout` 1800 and a `WAITFOR DELAY '00:01:00'`, followed by a second
   script, under a bound of two seconds: the first unit's error names the
   maximum duration and not `@timeout`; `run.cancelled` is absent; no file
   for that unit exists in the run folder; the second unit is skipped with the
   bound's reason; with the command line's gauge as the observer, wrapped by
   a recorder of `Finished`, stderr carries the first unit's `!!` line and
   exactly the note `note: the collection reached its maximum duration of
   0m02s (2 s); 1 collector was not started, and the one running then was
   stopped` (`formatCeiling` writes two seconds as `0m02s (2 s)`), no `-- `
   line for the second unit, and nothing saying `connection lost`; the
   recorder saw `Finished(false, true)`; the exit code is 2; `duration_sec`
   is at least 2; the first unit returned within twelve seconds of the start
   (the bound and two waits of the driver). Fails when the cause check in
   `outOfTime` is broken (the message names `@timeout`), when `finish` passes
   `false` for the bound to `Finished` (the recorder), or when the second
   unit's skip is not marked `MaxDuration` (a `-- ` line).
7. The first reset, live. `runUnit` called directly with a `bound` whose
   deadline has already passed returns a `*maxDurationError`, from the first
   reset, and writes no file.
8. The order. For a plan with both cost options on, every unit whose script's
   `RequiresFlag` is in `CostFlags` comes after every unit whose script's is
   not, and the order within each group is the order before the move. The test
   derives the costly set from `CostFlags`, not from a list of paths.
   `PlannedDuration` and `Run` see the same order. For a plan with no cost
   option, the order is the one `planUnits` gives today.
9. The record. `max_duration_reached` is true when one unit was skipped or
   stopped for the bound, or the bound cut a step before the run folder, and
   absent otherwise, including when the bound passes after the last unit;
   `config.max_duration_sec` is present exactly when a bound was set.
   `MANIFEST.txt` prints the duration line and one grouped entry. The manifest
   of a run with no bound has neither key and today's duration line; for a run
   with no cost option, the order of `results` is today's.
10. The exit code. A run the bound cut exits 2 on the command line, including
    one cut only between units (the case where `exit` was 0); its summary line
    ends with `max duration reached`. `TestSettleRun` gains the case of a run
    cut by the bound with no failed unit. The live case with no failed unit is
    criterion 16. The wizard's rule is criterion 14. Fails when `settleRun`
    is called with the cancellation alone (the new case returns 0).
11. The previous run. `skipLoses` returns true for the bound's reason,
    asserted by name in its table test. `settingsLost` names nothing for two
    runs differing only by the bound. That a same-day rerun cut by the bound
    keeps the run it replaced is shown live by criterion 16.
12. What check prints. For each of the three comparisons, a `check` against a
    fixture `VerifyResult` prints the bound's lines with the provenance;
    without a bound the Duration block is byte-identical to today's.
13. The screens, without a server. The command line's `progress`, fed 198
    `UnitDone` calls carrying a `*UnitSkipped` with `MaxDuration` set, prints
    none of them, tty or not, and its count reaches the total; one carrying a
    watch's skip still prints its `-- ` line. `maxDurationNote`, as a table
    over the four rows of "How a bounded run is recorded", gives those exact
    strings. The wizard, fed through `unitDoneEvent.apply` the events of the
    measured case (289 planned, 90 successes, one error, 198 bound skips) and
    rendered with `renderDone`, shows the summary line `90 collected, 198
    skipped, 1 error, 0 permissions denied` and none of the 198 skips among
    its notes. Fails when the stopped clause of the note is unconditional
    (second row), when `summaryLine` keeps printing `DoneUnits` (it reads
    `289 collected`), or when the wizard's skip adds a note.
14. The wizard's exit and last screen, without a server. The events are
    produced by the wizard's own `observer{ch}` methods, `UnitDone` and
    `Finished`, in the order `Run` calls them, then a `collectDoneEvent`, all
    driven through `loop`; none is built by hand, since a hand-built
    `finishedEvent` passes with an `observer.Finished` that drops the bound
    (one reader planted that slip and the hand-built version stayed green
    while the observer-built one failed). Rows and exit codes:

    | Units done | `Finished` | `collectDoneEvent` | Exit |
    | --- | --- | --- | --- |
    | one success | `(false, true)` | code 2, no error | 0 |
    | none | `(false, true)` | code 2, the bound's "nothing was collected" error | 2 |
    | none | `(false, true)` | the same, with the wizard's context cancelled | 2 |
    | three bound skips | `(false, true)` | code 2, no error | 2 |
    | none | `(true, false)` | code 2, a stop's error, context cancelled | 0 |

    And `Render` of the final state of the first and second rows shows the
    first lines of "How a bounded run is recorded" for an archive and for
    none. Fails when `observer.Finished` drops its second argument (first row
    exits 2), when the cancellation is asked before the bound (third row exits
    0), when `CollectedUnits` is not consulted (fourth row exits 0), or when
    the bound's case is missing from either branch of `renderDone`'s switch.
15. The watch's record after a bound stop. `runUnit`'s deferred switch, given
    a `*maxDurationError` with `fired` set and a worst wait of 5.3 s, writes
    exactly the warning `... ; the collector was being stopped at the
    collection's maximum duration when the blocking watch reached its 5s
    limit`, and neither "had already read its rows" nor "under the blocking
    watch's", and does not count the unit in `cancelled_units`. Fails when
    the case falls to `fired` or to the worst wait's case, which is how the
    previous draft's version of this criterion passed while writing a
    sentence that contradicted itself.
16. A bound passed after the run folder, live. A same-day rerun without
    `--keep`: the test runs a first unbounded collection of a two-script
    corpus, then a second with a bound of three seconds and `pauseHook` set
    to wait on `bound.Done()` at `"before the session id"`, with the command
    line's gauge as the observer. The second run exits 2;
    `run.max_duration_reached` is true; `results` is empty and both units are
    in `skipped_scripts` with the bound's reason; `blocking_watch.enabled` is
    false and its reason is `not started: the collection reached its maximum
    duration before the first collector`; no warning contains "nothing will
    cancel"; stderr has no `note: the blocking watch is off`, no `-- ` line,
    and exactly the note `...; 2 collectors were not started`, with no clause
    about a unit stopped; its archive exists; the first run is kept as
    `.superseded-*`, with the warning that says so. Fails when `settleRun` is
    not given the bound (exit 0, and the first run is deleted), when the
    note's stopped clause is unconditional, when the watch's warning is left
    as today, or when neither the reading of the session id nor the check
    before the watch asks the bound (the watch is started). Either one alone
    keeps the watch off in this test, since the session id is read after the
    bound has passed; the check before the watch is there for a bound that
    passes between a successful read and the watch's start, which no test
    places.

The `USE` path has no live test of its own: a `USE` that waits until a bound
passes is not cheap to produce (measured: a `USE` of a database whose
`SET SINGLE_USER` is pending fails at once, in 0.01 s, with "is in
transition", rather than waiting). Criterion 4 tests the helper, and the
review of the change checks that the `USE`'s error goes through it.

## Open questions

- Is one minute the right floor, now that the bound covers the steps before
  the first unit and the floor only guards against a slip of unit? A floor of
  ten minutes would also catch `5m` typed for `5h`, and refuse nothing a real
  window needs.
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
- Should the wizard exit 2 for a run the bound cut, as the first draft said,
  on the ground that the operator did not stop it at that moment? This draft
  follows the README's reason for a stop instead, for a run that produced an
  archive and collected something, and exits 2 for one that did not.
- The wizard's "N collected" over-count exists without the bound, for the
  watch's skips and for every failed unit. Should `CollectedUnits` land on its
  own, before this feature, as `archiveOf` did?
- Should the blocking watch's start run under the bound too? It would need its
  connections and first poll on one context and its lifetime on another.

## Review of 4 October 2026

Five readers ran the first draft (`6998f70`) against the tree of the day and
the lab's SQL Server 2025: agy with the neutral prompt, codex with the
directive and the neutral prompts, DeepSeek V4 Pro with the directive prompt
(in the seat of agy's directive run, which the service refused with a 503),
and a Claude subagent with the neutral prompt. Claude built a minimal prototype of the design on a copy of the tree
and measured it; the others read, ran the suite and the existing live tests,
and ran `check`. The union of what they found, and what became of each:

1. A run whose bound passes between two units exits 0. No unit fails, `exit`
   stays 0, and `settleRun(0, false)` returns 0 and allows the previous run to
   be deleted (agy, by reproducing `settleRun`). Taken: "Exit code and the
   previous run of the day", `settleRun` takes the bound beside the
   cancellation.
2. The steps before the first unit are not cut, and the first draft's figure
   for them was missing. Codex (both prompts) showed that a slow preamble can
   pass the bound and that a step failing after it exits 1 through
   `stoppedOr`; DeepSeek computed 315 s on the defaults. The arithmetic is
   corrected here: the four preflight probes share one
   `SQL_QUERY_TIMEOUT_SEC` rather than taking one each, and the session id
   and the watch's start were missing; about 307 s in all. Taken: "The
   bound's context" and "Before the first unit" put those steps under the
   bound, except the watch's start, which is listed with its 37.5 s in "What
   the bound holds". The floor is revised in "The option and its format".
3. The first session reset of a unit runs before the unit's context exists,
   on the run's context, so the bound cannot cut it (codex, both prompts).
   Taken: the reset moves under the unit's context.
4. A bound that lands during the `USE` is filed as a bare "context deadline
   exceeded", because that error returns through `blockedOr` alone (Claude,
   measured on the prototype; codex neutral, by reading). Taken:
   `maxDurationOr` is applied on that return too.
5. The order of the tests in `outOfTime`: the bound's cause must come after
   the run's context, or a `ctrl-c` during a unit the bound also cut is filed
   as the bound (DeepSeek), and after the SQL error number, or a real failure
   coinciding with the bound is relabelled, which
   `TestOutOfTimeLeavesASQLErrorAloneEvenOnTheDeadline` guards today for the
   unit's own deadline (codex directive). Taken: the four-step order of
   `maxDurationOr`, and criterion 4 tests both inversions.
6. The analogy with `blockedOr` is loose: `blockedOr` sits outside
   `outOfTime` and does not consult the run's context (DeepSeek). Taken: the
   text now places `maxDurationOr` exactly and keeps `blockedOr` outermost.
7. The reconnect after a stopped unit. The driver closes the connection, so
   `connAlive` fails and the loop reconnects unless told not to (agy, codex
   directive); and a unit that fails on its own just before the bound still
   goes through the ping, the reconnect and its reset, past the bound, or to
   exit 1 if they are cut (Claude, codex directive). Taken: "After a unit".
8. The driver waits up to five seconds for the server to confirm a
   cancellation, and the server can go on rolling back after it gives up:
   20.019 s for a 15-second bound, 197 381 rows of the cancelled insert still
   visible, and a stored message reading "Invalid TDS stream" rather than
   "context deadline exceeded" (Claude, measured). The driver's source shows a
   second wait of five seconds in one path. Taken: "What the bound holds"
   replaces "the bound limits server work", and test 6 allows twelve seconds.
9. A `.env` holding `MAX_DURATION` is refused as a whole by every binary that
   does not know the key (Claude, measured). Confirmed by the author on
   0.37.0, which also showed the converse: an exported `MAX_DURATION` is
   ignored without a word, exit 0. Taken: "The option and its format", which
   keeps the refusal as the right outcome, recommends `.env` or the flag, and
   lists the four places the key is added.
10. Fractional durations: `time.ParseDuration` accepts `1m0.5s`, which passes
    a one-minute floor while `formatCeiling` and `max_duration_sec` record
    60 s, and a cut run's `duration_sec` could then be under the recorded
    bound (codex, both prompts; Claude, as a minor point). Taken: a value must
    be whole seconds.
11. `check`'s line naming the costly collectors as "the first to go" is
    misleading when the units before them can already exceed the bound
    (codex neutral, measured with `--profile space` and both options: 9h00m
    costly of 15h39m). Re-measured by the author later the same day, ten
    databases: 28 800 s costly of 50 160 s. Taken: the second line gives the
    ceiling of the units before the costly ones and makes no forecast.
12. `flagNameFor` needs the entry's value spelt out, or the provenance reads
    `--MAX_DURATION` (DeepSeek). Taken, with the `Config` field that carries
    the provenance, which `Resolve` does not expose today for any key but
    `SQL_SERVER`.
13. The wizard's exit code. The first draft had a cut wizard run exit 2, which
    contradicts the README's reason for a stop (Claude), and a `ctrl-c` after
    the bound made the wizard exit 0 where the command line exits 2
    (DeepSeek). Taken: the wizard exits 0 for a run the bound cut, as for a
    stop, which removes the second case; kept as an open question.
14. The wizard does write `.env`, for `SQL_SERVER` and `SQL_USER` (Claude).
    Taken: the sentence is corrected; the conclusion, that it never edits
    `MAX_DURATION`, stands.
15. Criterion 8 of the first draft required a byte-identical manifest for
    every unbounded run, while the order decision changes `results` for any
    run with a cost option (Claude). Taken: criteria 8 and 9 now restrict the
    identical order to runs with no cost option.
16. Tests 3 and 6 relied on an injected clock that the context deadline does
    not share, so the bound would have had two clocks (Claude). Taken: one
    clock, the bound's context; `boundReached` is the only question; the
    tests use a short `Options.MaxDuration` and tables over contexts.
17. The floor must be enforced in `Resolve` only, or no live test can use a
    two-second bound (Claude). Taken.
18. The stderr note's count was computed before the skips were recorded and
    would count units held back for other reasons (Claude). Taken: the note is
    printed after the loop, from the bound's skips.
19. The claim that a complete rerun deletes the bounded archive is too broad:
    deletion needs exit 0 and nothing lost in `previousRunLost` (codex
    directive). Taken, with the target comparison by address and database
    that landed on main since the first draft.
20. The `skipLoses` row is a line of the comment's table, and the draft did
    not say what the function returns (DeepSeek). Taken: the value is the
    fallback's `true`, asserted by name.
21. The reorder breaks no dependency and no test: 021 before 022 is preserved,
    nothing reads what 041 or 055 produce, and the suite stays green with the
    move applied (agy, codex, DeepSeek; Claude by running it). No change.
22. No partial file: a writer receives materialised sets with no connection
    and no context (codex, DeepSeek, Claude). No change.
23. `context.Cause` carries the bound's cause through the unit's context and
    the query's (Claude, live; agy and codex by reading). Confirmed by the
    author with a test program, which also showed that a unit context
    cancelled by the watch first keeps the watch's cause while the bound's
    context carries its own: hence two questions on two contexts in this
    draft. No change beyond that.
24. The lab has changed since the first draft's figures (Claude counted 11
    databases, the author 10 later the same day). Set aside: the figures are
    kept with their date, and a sentence says the lab has grown.
25. The byte budget the first draft quoted was 256 MiB; main raised it to
    1 GiB (`d68ef34`) the same day. Taken (found by the author re-reading
    main, not by a reader).
26. Codex neutral ran the whole live suite, which creates and drops
    `ZzWatchLive` and `ZzDroppedDuringRun`, outside the prefix it had been
    given; its `check` afterwards listed neither. Set aside: not a finding on
    the design. Noted for the next panel's prompt.

Not taken from the readers' "not a problem" sections, but worth stating: the
`max_duration_reached` field as the first draft defined it ("set when at least
one unit was stopped or skipped") needed a way for the loop to know a unit
was stopped by the bound, which the draft did not give; and a definition by
"the bound passed before the loop ended" would have made a complete run whose
bound passed during its last `leave` exit 2. Both are settled by the
`*maxDurationError` type and the definition in "How a bounded run is
recorded".

## Second review of 4 October 2026

Five readers ran the second draft (`722a951`, read on `8d7b4ab`) against the
tree of the day and the lab's SQL Server 2025: agy and codex each with the
directive and the neutral prompts, and a Claude subagent with the neutral
prompt. No seat had to be replaced. Claude worked on a copy of the tree and
measured; agy measured the cancellation's rollback and the context semantics;
codex ran the suite, two live tests that create nothing, and `check` with the
literal key. agy's neutral reading found no defect, which is itself a reason
to read its log rather than its verdict. The union of what they found, and
what became of each:

1. The command line's gauge prints one permanent line per unit the bound
   skips: fed 198 `UnitDone` calls with the bound's reason, `progress` printed
   198 `-- ... skipped: ...` lines, tty or not, before the one-line note the
   draft promised (Claude, measured). The author found the same weight in the
   wizard, which keeps six notes and would show six identical skips in place
   of the stopped unit's error. Taken: "How a bounded run is recorded",
   `UnitSkipped.MaxDuration`, counted silently on both screens; criteria 6 and
   13.
2. `maxDurationOr` asked the unit's context, and a call whose own deadline
   expired first, with the bound passing while the driver waits for the
   cancellation, would be filed as stopped by the bound (Claude, measured with
   a Go program; codex directive for the `USE` and the reset; agy directive
   for the query). Re-measured by the author in five orders: own timeout then
   bound, bound then watch, watch then bound, own timeout then watch, `ctrl-c`
   after the bound; the innermost context gives the right cause in each.
   Taken: "During a unit", step 2 asks the call's context, the first reset is
   written so that `runUnit` holds that context, and criterion 4 has the
   straddling row.
3. Criterion 2's evidence showed nothing: with the bound added to
   `KnownFlags` and to the options' `Flags`, `TestAllTurnsOnEveryOptIn` still
   passed, and only the wizard's `TestTheWizardOffersEveryOptInTheCommandLineHas`
   failed (Claude, measured). Taken: criterion 2 asserts the absence directly
   and must fail when the bound enters `KnownFlags`.
4. Two homes for the bound: `Config.MaxDuration` in "The option and its
   format", `o.MaxDuration` in `Run` and in criterion 5, and no step copying
   one into the other; `check` could print a bound `collect` ignored (Claude,
   codex directive, codex neutral). Taken: `Config.MaxDuration` is the only
   home; criterion 1 tests the command line's path end to end.
5. A cut "before the first collector" can come after `prepareRunFolder`,
   which runs before the session id and the watch, so the previous run may
   already be set aside and a run folder exist (codex, both prompts, by
   reading `Run`; confirmed by the author). Taken: "Before the first unit"
   now treats the two sides of the run folder apart, asks the bound before
   `lockRun`, and sends a later cut through the loop; criterion 5 plants a
   previous run and checks it is untouched.
6. The wizard exits 0 for a bound reached before anything was collected,
   with no archive, on a reason that holds only for a partial archive (codex,
   both prompts). Taken: "Exit code and the previous run of the day", 0 only
   when `Run` returned no error.
7. `collectDoneEvent.exitStatus` has no way to learn what `Finished`
   reported (codex neutral, agy directive). Taken: the design relies on the
   order of the two events on one channel and passes the state to
   `exitStatus`; criterion 14 drives the two events through the loop.
8. Criterion 5 asked for a manifest that says the bound was reached before
   the first collector, which nothing wrote, and `MANIFEST.txt` prints its
   duration line only for a positive `duration_sec`, so a run bounded at one
   millisecond would say nothing there (Claude). Taken: the cut before the
   run folder records a warning, and the duration line is printed whenever
   the bound was reached.
9. A ping cut by the bound prints `connection lost; attempting one
   reconnect` before the bound is asked, about a connection that was fine
   (Claude, by reading, with a ping on an expired context measured to fail in
   1.3 ms). Taken: "After a unit".
10. The watch firing during the driver's wait after a bound stop leaves the
    deferred switch on its `fired` case, whose warning says the collector had
    already read its rows (Claude). Taken: "During a unit", criterion 15.
11. `.env.example` must carry the key on a commented line, or every `.env`
    the new `env init` writes is refused by every older binary (Claude).
    Measured by the author on 0.37.0: `MAX_DURATION=` refused,
    `# MAX_DURATION=2h` passes `check`. Taken: "The option and its format",
    criterion 1.
12. `databaseExists` runs on the run's context, after a 911 that
    `maxDurationOr` deliberately leaves alone, so a new catalog query can
    start after the bound and take `SQL_QUERY_TIMEOUT_SEC` (Claude, agy
    directive, codex, both prompts). Taken: the check runs under `bound`, and
    "After a unit" states the price, a drop coinciding with the bound filed as
    an error.
13. A poll of the blocking watch in flight at the bound runs on the run's
    context with its retries, up to 20.5 s plus an identity read, before
    `Run` returns (codex directive). Taken: listed in "What the bound holds"
    and in "What is not in scope".
14. The places where the key is added were counted short: the flag in
    `defineFlags` and its usage line, the `flags` map of `optionsFrom` (Claude),
    `docs/dba-guide.md`'s closed list and the run settings paragraph of
    `MANIFEST.txt` (codex neutral). Taken: a checklist without a count.
15. The motivating archive names `10.system/020.properties.sql`, which does
    not exist; the collector with `@timeout` 300 is
    `20.databases/020.properties.sql` (Claude). Taken.
16. `context.WithDeadlineCause` returns a context and a cancel function, so
    the draft's one-line assignment does not compile (codex neutral, from
    `go doc`). Taken: "The bound's context".
17. A fractional bound passed to `Run` by a test is applied but recorded
    truncated (agy directive). Taken as a sentence: `Run` validates nothing,
    and tests that read the record use whole seconds.
18. The parent test in `maxDurationOr` is redundant, since
    `recordUnitFailure` asks `stopRequested` first and drops the error (agy
    directive). Set aside: true of the loop's filing, but the helper is tested
    on its own and called inside `outOfTime`, whose message must not be built
    for a stop either; the step costs one comparison.
19. A run whose remaining units are all held back or on a dropped database
    when the bound passes exits as it would without a bound (agy directive).
    Set aside: that is the definition of `max_duration_reached`, the bound cut
    nothing those units would have run.
20. Confirmations, no change: the server's rollback outlives the driver's
    return (agy directive, 584 rows of a cancelled insert visible 198 ms
    after it); the cause propagates from the bound to a query context, and a
    `ctrl-c` after the bound still reads as a stop (agy neutral, Claude); the
    `.env` refusal and the silent environment on 0.37.0 (all five);
    `settleRun`'s new argument, `1.5h` as whole seconds, the order of the SQL
    error test, and the driver's two five-second waits in its source (codex);
    the reorder's dependencies (agy); the summary line's arithmetic and
    `WAITFOR` passing `statementlint` (Claude).

Found by the author while checking finding 6, and not by a reader: the
wizard derives the archive's path before `Run` and stats it afterwards, so
after any failure before the run folder on a same-day rerun without
`--keep`, its last screen shows the previous run's archive, which is still at
that name, as the file to send, under "This archive is partial" if the
failure was a stop. Concluded by reading `tui/run.go` and `tui/render.go`,
not reproduced. Taken: the archive is read only when `Run` returned no error;
an open question asks whether that lands on its own.

The readers left nothing behind: the real repository's `git status` is clean,
agy's rollback test was written in its worktree only, and the live tests
codex ran create nothing.

The rules this revision is least sure of, for whoever reads it next:

- The wizard's exit rule depends on `finishedEvent` being applied before
  `collectDoneEvent`. It holds because both are blocking sends from one
  goroutine on one channel; a change to either send (a `select` that can drop
  an event, a second goroutine) would break it without a compile error.
  Criterion 14 drives the events through the loop for that reason.
- The check before `lockRun` narrows the window in which a pre-loop cut
  sets the previous run aside, without closing it: a bound that passes
  between that check and the loop goes through the loop and keeps the
  previous run as superseded, which is the partial-rerun behaviour and is
  not tested live, since no test can place a bound in that interval.
- `databaseExists` under `bound` files a real drop coinciding with the bound
  as an error.
- `UnitSkipped.MaxDuration` makes the bound's skips silent on both screens;
  an operator watching the gauge sees it jump to its total, and the note
  after the loop is the only line that explains the jump.
- Reading the archive only when `Run` returned no error changes what the
  wizard shows after a failed zip: today the stat may find a partial `.zip`
  and show it; after the change it says no archive was produced.

## Third review of 4 October 2026

Five readers ran the third draft (`92a1a83`, merged as `bc9c74d`) with the
prompts aimed at the rules the second revision had added: agy and codex each
with the directive and the neutral prompts, and a Claude subagent with the
neutral prompt. No seat had to be replaced. Claude read on `f723563`, one
commit past the draft, implemented the wizard's half and the context helpers
on a copy of the tree, planted faults and ran the criteria as written; codex
drove the wizard's events through the real `loop` in temporary copies; agy
wrote small tests of the loop and of the rendering. agy's directive reading
found no defect beyond one premise, which is again a reason to read its log
rather than its verdict. The union of what they found, and what became of
each:

1. Criterion 14 does not guard the hop it was written for. With
   `observer.Finished` planted to drop the bound, or `finish` passing `false`,
   a test building the `finishedEvent` by hand passes, and the same scenario
   with the event produced by `observer{ch}.Finished(false, true)` fails
   (Claude, measured; the rest of the suite stayed green with both slips).
   Taken: criterion 14 produces every event through the wizard's observer,
   and criterion 6 records what a real bounded `Run` passes to `Finished`.
2. The wizard's last screen counts the bound's skips as collected:
   `summaryLine` prints `DoneUnits`, which every `UnitDone` raises (Claude,
   measured: `289/289` and `289 collected, 198 skipped, 1 error`). Re-measured
   by the author on `f723563` with the same events: `289 collected, 198
   skipped, 1 error, 0 permissions denied`. Taken: `State.CollectedUnits`,
   criterion 13, and an open question on landing it alone.
3. The case the second revision chose for a unit stopped at the bound whose
   watch fired writes `..., under the blocking watch's 5s limit` about a
   wait of 5.3 s, since `fired` means a wait reached the limit (Claude,
   measured on a copy). Taken: a case and a sentence of its own, criterion 15
   asserting it exactly.
4. The tree moved under the draft: `3a8cc59` shipped the archive rule,
   with `archiveOf`, `PreviousZip`, and the wording `No archive was written
   by this run.` (Claude). Taken: the passages presenting it as to do, and
   the open question, are removed; the bound's wordings are placed in
   `renderDone`'s switch as it now stands.
5. Criterion 4's "dead run context" row catches the inversion it names only
   when it is built as the bound first, then the stop; built as a plain
   `ctrl-c`, step 2 already answers (Claude, measured on a mutated helper).
   Taken: criterion 4 specifies the order of that row.
6. Criterion 5 could not fail without the check before `lockRun`: a
   one-millisecond bound has expired before `Connect`, the run leaves through
   `stoppedOr`, and the planted run survives either way; and its negative
   assertion named a sentence the tool does not print (Claude, measured
   `Connect` on an expired context in 64 µs). Re-measured by the author:
   `cannot reach the instance: context deadline exceeded` in 29 µs. Taken:
   criterion 5 is split in three, and the check before `lockRun` is reached
   through a test seam, `pauseHook`.
7. A `ctrl-c` pressed after the bound cut a run before its run folder made
   the wizard exit 0 with no archive, since the previous draft asked the
   wizard's cancelled context first (codex, both prompts, each by driving
   `finishedEvent` and `collectDoneEvent` through `loop` in a copy). Taken:
   `exitStatus` asks the bound first; criterion 14's third row.
8. `Collection reached its maximum duration before anything was collected.`
   is shown for any bounded run without an archive, including one that
   collected units and then failed in `Zip` (agy neutral, by rendering;
   codex directive and Claude, by reading). Taken: the sentence is gone; the
   screen says no archive was written, and `Run`'s error, already among the
   notes, says where.
9. The wizard exits 0 for an archive that holds nothing, when the bound
   passes between `lockRun` and the first result (agy neutral, by driving the
   loop; Claude, by reading). Taken: 0 needs `CollectedUnits` above zero;
   criterion 14's fourth row, and criterion 16 produces such a run live.
10. `skipBefore` did not say which of its three questions answered, and an
    implementation asking `boundReached` again would count a held-back skip
    as the bound's (Claude). Taken: a `byBound` result, criterion 3.
11. The stderr note said `the one running then was stopped` when nothing was
    running (Claude, codex neutral). Taken: `maxDurationNote` and its four
    rows, criteria 6, 13 and 16.
12. A run whose watch is not started for the bound still carried `nothing
    will cancel a collector that other sessions are waiting on` and its
    stderr note (Claude). Taken: no warning and no note in that case;
    criterion 16.
13. `stoppedOr` gave the bound precedence over an error with a SQL Server
    number, unlike the loop (Claude). Taken: `preambleStop`; the author
    measured that a refused login keeps 18456 through the `cannot reach the
    instance: %w` wrapping; criterion 5a.
14. A `ctrl-c` during the driver's wait after the bound cut a unit is filed
    as the operator's stop, with no bound flag (Claude). Taken as a sentence
    in "How a bounded run is recorded"; no change of behaviour.
15. `maxDurationOr` and `skipBefore` had no way to name the duration their
    messages print: the cause is a constant and the deadline an instant
    (codex neutral; two expired contexts of different durations compared
    equal in deadline, cause and error). Taken: a `limit` argument and
    `maxDurationText`; criteria 3 and 4 assert exact strings.
16. The session id read after a reconnect was not named among the calls
    under the bound, and its error is ignored (codex directive). Taken.
17. The README promise that server work ends "within a few seconds" of the
    bound contradicted the section's own list, where `leave` and the watch's
    start can each run for tens of seconds (codex directive). Taken: the
    promise is that no collector starts and the running statement is
    cancelled.
18. The wizard predicts the archive's path before `Run`, and under `--keep`
    `Run` can pick another name if a competing archive appears in between,
    so a successful run could offer another run's file (codex neutral,
    measured by creating the competing archive between the two calls). Set
    aside: it exists without the bound and needs a second process writing at
    the same name within the preamble; the fix is `Run` reporting the path it
    wrote, a change of its own, noted for after this feature.
19. The second revision's claim that both events go through blocking sends
    is false: `collectDoneEvent` goes through `runner.send`, which selects on
    `r.done` (agy directive). Taken: "How the wizard knows" says why the
    order still holds (the `select` can drop the event once the wizard is
    quitting, and cannot reorder it).
20. Confirmations, no change: asking the innermost context keeps a call's own
    earlier deadline after the bound passes (agy, both prompts; codex
    directive; Claude, all by running); every call moved under the bound
    fails at once on an expired context (Claude, measured: `databaseExists`
    1.19 ms, `sessionID` 0.90 ms, ping 0.91 ms, reset 0.88 ms, `USE` 1.00
    ms); the order of `finishedEvent` and `collectDoneEvent` (all five); the
    check before `lockRun` sits before `prepareRunFolder` (all five);
    `PreflightExitCode` returns 1 on any `error` status, and `formatCeiling`
    writes `1m00s (60 s)` and `2h00m (7200 s)` (Claude).
21. Codex directive interrupted the full live suite, whose first cases create
    `ZzDroppedDuringRun`, outside the prefix it had been given, and a `check`
    afterwards still listed it. Set aside, not a finding on the design. When
    the author looked at the end of this revision it was gone; the
    `ZzPanel8…` databases present then belong to another session and were
    not touched.

The rules this revision is least sure of, for whoever reads it next. Most of
them are corrections written in answer to a finding, which is where the worst
defect of the previous rounds came from:

- `pauseHook` is a package variable in production code that only tests set,
  the first of its kind in `collect`. Criterion 5c proves the check before
  `lockRun` relative to the hook's point, not relative to the listing; a hook
  called at the wrong place would make the criterion prove the wrong thing.
- The wizard's exit for a bounded run now depends on a count the wizard
  keeps from the observer's events, and a stop with nothing collected still
  exits 0 while a bound with nothing collected exits 2. The asymmetry is
  deliberate (the operator who stops has read the screen; the bound has no
  one watching), but it is new.
- Asking the bound before the wizard's cancelled context changes the exit of
  a run that was both cut by the bound and stopped. With an archive and
  something collected, nothing changes; without, it now exits 2.
- `preambleStop` exempts an error with a SQL Server number, but the
  preflight's failure reaches it as a sentence of the run's own with no
  number, so a probe refused with a number in the instant of the bound is
  still filed as the bound. The loop and `stoppedOr` now agree for every
  step but that one.
- A unit stopped at the bound with a coincident SQL error number is not a
  `*maxDurationError`: it gets the old `fired` sentence when the watch fired,
  the note does not count it as stopped, and when it was the last unit, with
  nothing skipped, `max_duration_reached` stays unset. The run exits 2 for the
  error in any case.
- A watch not started for the bound adds no warning; the manifest says why
  only through `blocking_watch.reason` and the bound's own records.
- The rules carried over from the second revision and still not tested live:
  `databaseExists` under `bound` files a real drop coinciding with the bound
  as an error; a bound that passes between the session id read and the
  watch's start goes through the check before the watch, which no test
  places.
