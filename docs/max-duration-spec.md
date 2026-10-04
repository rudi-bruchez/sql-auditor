# Bounding the duration of a collection

Status: proposed on 4 October 2026, not implemented. Written on 4 October 2026
from the decision taken that day to bound the collection as a whole, then
revised twice the same day, each time after a panel of five readers ran the
draft against the tree and the lab's SQL Server 2025. What they found, and
what became of each finding, is in "Review of 4 October 2026" and "Second
review of 4 October 2026" at the end. The first step of
the same proposal is already shipped: `check` and the wizard's third screen
print a ceiling (`4cd14c3`, `collect/duration.go`).

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
  error carries 911), the ping, the recycle and the one reconnect.

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
run folder that can fail, asks the bound after the stop: a dead run context
is still the operator's stop, and otherwise, when `boundReached`, the run
sets `run.max_duration_reached`, drops the error (it describes the cut, as a
`ctrl-c`'s does), records a warning, and finishes with exit 2, returning the
same sentence as its error:
`the collection reached its maximum duration of 1m00s (60 s) before the first collector: nothing was collected`.
Without this, a `Connect` or a probe cut by the bound would come back as exit
1, "the instance could not be reached", about an instance that was answering.
The preflight does not return an error: probes cut by the bound come back as
`error` statuses, `PreflightExitCode` returns 1, and the run reaches
`stoppedOr` the same way. And the run asks `boundReached` once more just
before `lockRun`, after the listing, and takes the same path when it has
passed, so that a bound reached during the last server step before the run
folder, or during the local steps after it, does not go on to set the
previous run aside.

Such a run ends before the run folder exists, so its manifest goes where
every failed run's goes, and the previous run of the day is not touched.

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

### Before each unit

The loop asks whether the bound has passed before starting a unit, after
`heldBack` and `droppedBefore`: a unit on a database already found dropped,
or held back by the watch, keeps that reason, which is more specific and is
the one `scopeLost` matches on. Every unit that reaches the check after the
bound has passed is skipped with the bound's reason, without touching the
connection.

The three questions become one function, `skipBefore(cancelledOn, droppedOn,
bound, target) (string, bool)`, so that their order is tested without a
server.

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
`maxDurationOr(parent, call context.Context, err error) error`, decides, in
this order:

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
  left the database", which is false for a unit stopped mid-query. The case
  `fired` is taken only when the error is not a `*maxDurationError`; a unit
  stopped at the bound falls to the next case, the one that records the
  worst wait seen under the watch's limit, and is not counted in
  `cancelled_units`.

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
the bound, and the server work the run asked for ends within a few seconds of
it. What the run can still spend after the bound, in full:

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
manifest and the archive are written after it.

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
  cut something: a unit skipped for it, a unit stopped by it (a
  `*maxDurationError`), or a step before the run folder (`stoppedOr`, or the
  check before `lockRun`). It is
  not set merely because the bound passed: a bound that passes during the last
  unit's `leave`, the manifest or the zip cut nothing, and the run is
  complete.
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
  the units actually skipped for the bound, so that it agrees with the grouped
  entry of `MANIFEST.txt` (a unit kept back by `heldBack` or `droppedBefore`
  is not counted):
  `note: the collection reached its maximum duration of 2h00m (7200 s); 198 collectors were not started, and the one running then was stopped`.
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
- `Observer.Finished` carries the bound as well as the cancellation,
  `Finished(cancelled, maxDurationReached bool)`, from the same manifest
  fields, and the wizard's last screen reads `Collection stopped at its
  maximum duration. This archive is partial:` in place of the line it shows
  for a stop, when the run produced an archive (next section), and
  `Collection reached its maximum duration before anything was collected.`
  when it did not, with the "no archive was produced" line under it as today.

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

In the wizard, a bounded run that is cut and produced an archive exits 0, as a
stopped one does. The README gives the reason for a stop: "an operator who
stops the collection from the wizard has read the screen that calls the
archive partial, and the wizard exits 0". The bound is the same decision taken
in advance: the wizard shows it on its third screen before the collection
starts, and its last screen calls the archive partial. That reason needs an
archive. A bound reached before the run folder produces none, and its
wizard exits 2, as the command line does: a scheduler wrapping the wizard
must not record success for a run with nothing to send.

How the wizard knows. Today `collectDoneEvent.exitStatus()` takes no
argument and sees only `Run`'s code, its error and whether the wizard's own
context was cancelled; the run's verdict reaches the wizard as a separate
`finishedEvent`, which only updates `State`. The two events are sent on the
same channel by the same goroutine (`Finished` is called inside `Run`, from
`finish`, and `collectDoneEvent` after `Run` returns, both through blocking
sends), so the `finishedEvent` is always applied first. The design uses that
order: `finishedEvent` carries `maxDurationReached` into `State`, the `coded`
interface becomes `exitStatus(s State) int`, and the loop passes the state as
it stands before the event is applied. `collectDoneEvent.exitStatus` then
returns 0 when its own context was cancelled, as today, or when
`s.MaxDurationReached` is set and `Run` returned no error; otherwise its code.

`Run` returning no error is the test for "an archive was produced", and not
the presence of a `.zip` at the run's name. `Run` returns a nil error only on
its last line, after `Zip`; every path that ends without an archive returns
one (`stoppedOr`, a failed reconnect, a failed manifest, a failed zip). The
`.zip` on disk is not evidence: the wizard derives the archive's path before
`Run` and stats it afterwards, and on a same-day rerun without `--keep` the
previous run's archive sits at exactly that name until `prepareRunFolder`
moves it. So the wizard reads the archive's path and size only when `Run`
returned no error. This also corrects today's behaviour for every failure
before the run folder (an unreachable instance, a refused preflight), which
leaves the previous run's archive at that name and shows it on the last
screen under "Send this file", as if this run had produced it.

This also settles the case of a `ctrl-c` pressed after the bound has cut the
run: the wizard exits 0, as it does today for any stop, while the command
line exits 2. The README's paragraph on the wizard's exit code names the bound
beside the stop, and says that a bound reached before anything was collected
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
3. The order of the skips. `skipBefore`, as a table: a target held back keeps
   the watch's reason and a dropped one `skipDroppedDuringRun`, with the bound
   passed or not; any other target is skipped with the bound's reason exactly
   when `bound` carries `errMaxDurationReached`, and not when it was cancelled
   by a `ctrl-c`.
4. The classification. `maxDurationOr`, as a table, with contexts built in the
   test as `Run` and `runUnit` build them (`bound`, the unit's
   `WithCancelCause` child, a call context with its own timeout under it) and
   no server: a dead run context, a unit context cancelled by a
   `*blockedError` before the bound passed, a call context whose own timeout
   expired first, and a SQL Server error number coinciding with the bound each
   leave the error unchanged; a bare context error after the bound becomes a
   `*maxDurationError` whose message names the bound and keeps the original
   words. One row is the straddling case: the call context's own timeout
   expires, then the bound passes before the error is classified (the
   driver's wait for the cancellation), and the error must come back
   unchanged. Moving the bound's test before the parent test, or before the
   SQL error number test, or asking the unit's context instead of the call's,
   must make this test fail.
5. A bound passed before the run folder, live. `Run` with a `Config` whose
   `MaxDuration` is one millisecond, in an output directory where the test has
   planted a folder and a `.zip` at the run's name (from `RunFolderFor`, with
   the server name the test reads on its own connection and the `Now` it
   passes): `run.max_duration_reached`
   is true, `run.cancelled` is absent, the exit code is 2, the returned error
   and a warning in `_run.json` are the bound's sentence ending "before the
   first collector: nothing was collected", the error is not "the instance
   could not be reached", `MANIFEST.txt` has a duration line naming the bound
   although the run took less than a second, no run folder was prepared, and
   the planted folder and `.zip` are still at their names.
6. A unit stopped at the bound, live. A first instance-scope script with
   `@timeout` 1800 and a `WAITFOR DELAY '00:01:00'`, followed by a second
   script, under a bound of two seconds: the first unit's error names the
   maximum duration and not `@timeout`; `run.cancelled` is absent; no file
   for that unit exists in the run folder; the second unit is skipped with the
   bound's reason; with the command line's gauge as the observer, stderr
   carries the first unit's `!!` line and the bound's note, no `-- ` line for
   the second unit, and nothing saying `connection lost`; the exit code is
   2; `duration_sec` is at least 2; the first unit returned within twelve
   seconds of the start (the bound and two waits of the driver). Breaking the
   cause check in `outOfTime` must make this test fail on the message.
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
    cut by the bound with no failed unit. In the wizard, `exitStatus` is 0 for
    a run that reported the bound and produced an archive, as for a stopped
    one, and 2 for one cut before the run folder (criterion 14).
11. The previous run. A same-day rerun cut by the bound keeps the run it
    replaced, and the warning says so. `skipLoses` returns true for the
    bound's reason, asserted by name in its table test. `settingsLost` names
    nothing for two runs differing only by the bound.
12. What check prints. For each of the three comparisons, a `check` against a
    fixture `VerifyResult` prints the bound's lines with the provenance;
    without a bound the Duration block is byte-identical to today's.
13. The screens, without a server. The command line's `progress`, fed 198
    `UnitDone` calls carrying a `*UnitSkipped` with `MaxDuration` set, prints
    none of them, tty or not, and its count reaches the total; one carrying a
    watch's skip still prints its `-- ` line. The wizard's `unitDoneEvent`
    for a bound's skip raises `SkippedCount` and adds no note.
14. The wizard's exit and last screen, without a server. With a
    `finishedEvent` reporting the bound applied first: a `collectDoneEvent`
    with code 2 and no error exits 0; one with code 2 and the bound's
    "nothing was collected" error exits 2. The test drives the two events
    through the loop in the order `Run` sends them, so that a `finishedEvent`
    whose field never reaches `exitStatus` fails it. The `collect` goroutine
    reads the archive's path only when `Run` returned no error: with a `.zip`
    planted at the run's name and `Run` failing before the run folder, the
    last screen says "no archive was produced".
15. The watch's record after a bound stop. `runUnit`'s deferred switch, given
    a `*maxDurationError` with `fired` set, writes no "had already read its
    rows" warning and does not count the unit in `cancelled_units`.

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
  archive, and exits 2 for one that did not.
- The wizard's reading of the archive path only when `Run` returned no error
  corrects a defect that exists without the bound (the previous run's archive
  shown after a failure before the run folder). Should it land on its own,
  before this feature, with its own test?
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
