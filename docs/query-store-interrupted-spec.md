# Interrupted executions: query timeouts and errors from the Query Store

Status: implemented on 19 September 2026
(`queries/80.workload/026.query-store-interrupted.sql`). Written the same day,
and revised after a panel of five independent readers ran it against a live
SQL Server 2025. What the panel changed is listed at the end.

## The question

"The application gets timeouts" is one of the most common complaints an audit
starts from. Microsoft's own troubleshooting page for it (*Troubleshoot query
timeout errors*, on Learn) starts by capturing `attention` events with Extended
Events, which only helps from the moment the session exists. An audit arrives
after the fact.

A timeout is decided by the client, not by the server. The client's command
timeout expires, it sends an attention, and the server stops the statement.
The server keeps no error for it: nothing in the error log, and no attention
event in the default trace or in `system_health`. `sys.dm_exec_query_stats`
does not count the interrupted execution at all (measured by a reviewer: three
regular runs then three timeouts left `execution_count` at 3).

The Query Store does record it. `sys.query_store_runtime_stats` keeps one row
per plan, interval and `execution_type`:

- 0, `Regular`: the statement finished;
- 3, `Aborted`: the client stopped it;
- 4, `Exception`: an error stopped it.

Nothing in the corpus ranks on that column today.
`80.workload/021.query-store-detail.sql` projects it, but only for the queries
it already selected, and only behind `--query-store-detail`.
`80.workload/020.query-store.sql`, `023` and `024` do not filter on it, so an
aborted execution counts in their totals like a finished one.

## What was measured

On SQL Server 2025 (17.0.4065.4), in scratch databases with one-minute
intervals, by the author and by the panel:

| What happened | Capture mode | `execution_type` | What the row holds |
| --- | --- | --- | --- |
| a CPU-bound statement stopped by `sqlcmd -t 2` | `ALL` | 3, `Aborted` | 3 executions, 2000 to 2003 ms; CPU equal to the duration |
| the same statement finishing normally | `ALL` | 0, `Regular` | a separate row for the same plan and interval |
| a statement stopped by a client timeout after a `WAITFOR DELAY '00:00:01'` in the same batch, `-t 3` | `ALL` | 3, `Aborted` | 2003 ms, not 3000 |
| a statement inside a procedure, stopped by the client timeout | `ALL` | 3, `Aborted` | on the inner statement only; no row for the outer `EXEC` |
| a client killed outright (connection reset, no attention) | `ALL` | 3, `Aborted` | duration up to the reset |
| a division by zero (Msg 8134) | `ALL` | 4, `Exception` | 2 executions |
| a `SELECT` that hit `SET LOCK_TIMEOUT 500` (Msg 1222) | `ALL` | 4, `Exception` | 1 execution, about 500 ms |
| a deadlock victim (Msg 1205) | `ALL` | 4, `Exception` | 1 execution |
| a statement whose session was ended by `KILL` | `ALL` | nothing | no runtime row for that execution |
| a timeout during compilation | `ALL` | nothing | no plan, so no runtime row |
| a blocked statement timing out five times, never run before | `AUTO` | nothing | the query was never captured |
| the same statement run 40 times normally, then timing out 3 times while blocked | `AUTO` | 3, `Aborted` for the 3 timeouts | only 11 regular executions recorded: those before capture are lost |
| a CPU-bound statement stopped at 2 s, first execution | `AUTO` | 3, `Aborted` | captured at once: 2 s of CPU passes the capture threshold |

### What that means for the reader

The duration of an aborted execution is close to the client's command timeout
only when the statement was the first thing the batch or the RPC did, and even
then a little under or over it: the client clock runs over the whole request,
compilation included, the Query Store times the statement's execution. A later
review measured 958 ms (2017) and 936 ms (2022) for the CI fixture's 1-second
timeout, 1000.4 to 1001.3 ms elsewhere, and 30025.6 ms for 30 seconds. Inside a procedure, the statement that gets cut shows the
timeout minus what ran before it, and that varies. A panel reader also saw
1923 ms for a 2-second timeout. So durations that cluster are consistent with a
timeout, and durations that scatter do not rule one out. The collector
projects minimum, average and maximum, and names nothing.

`Aborted` is any stop from the client side: a command timeout, a user pressing
Cancel, an application cancelling a task, a connection lost mid-statement.

`Exception` carries no error number. A lock timeout, a deadlock victim, a
division by zero and, on 2025, a query refused by the `ABORT_QUERY_EXECUTION`
hint are the same row. A lock timeout lasts about `LOCK_TIMEOUT`, and deadlock
victims can be matched against `10.system/061.deadlock-graphs.sql`, but the
collector does not guess.

CPU beside duration is the most useful reading aid, and the first draft read
the wrong column. It said a query that finishes in 20 ms and is aborted at 30 s
"is waiting on something". The lab's own aborted query refutes that: 14.6 ms
when it finishes, 2001 ms when aborted, and 2001 ms of CPU for those 2001 ms. It
was a parameter-sensitive, CPU-bound query, not a wait. CPU close to the
duration means the statement was working when it was stopped; CPU far below
means it was waiting. The collector projects both and draws no line.

### What the listing cannot see

- Under the default capture mode, `AUTO`, a query is captured on execution
  count or CPU, never on duration. A rarely run statement that times out
  because it is blocked uses no CPU and is never captured: five timeouts left
  nothing. A frequent one is captured once it passes the threshold, and its
  later timeouts are recorded; the executions before capture are not. Under
  `AUTO` the listing is a lower bound, biased toward frequent and CPU-heavy
  queries, and an empty one does not mean there were no timeouts.
  `state.capture_mode` is projected in the root for that reason.
- A timeout during compilation leaves no runtime row: there is no plan yet.
- An execution ended by `KILL` leaves no runtime row. `system_health` records
  `process_killed`, which is outside this collector.
- A blocked timeout has a second witness. `system_health` records `wait_info`
  for a lock wait over 30 seconds (15 for latch and I/O waits), with the wait
  type, the duration and the statement text: a later review saw two blocked
  `SELECT`s timed out at 30 and 35 seconds there, `LCK_M_S` at 30025 and
  35028 ms. A statement cut at the usual 30-second timeout passes that
  threshold only just, and only when it was the first thing its request did.
  `10.system/060.system-health.sql` counts these events; their text stays on
  the instance.

A side note, recorded because it cost an hour: after a load test drove a store
into `READ_ONLY` with `readonly_reason` 262144, every Query Store on the
instance, including one created afterwards, went back to `READ_ONLY` within
seconds of being set `READ_WRITE`, until the instance was restarted.
Microsoft documents 262144 as a temporary state that clears once the in-memory
items are persisted, so this may be specific to the build or the container.
The root projects `state.actual` and `state.readonly_reason`, which is what a
reader needs to notice it.

## What is added

One collector, `queries/80.workload/026.query-store-interrupted.sql`:

```
-- @scope:       database
-- @resultsets:  root:object, aborted:array, exceptions:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     120
-- @min_version: 13
-- @discloses:   query_text
```

No flag and no parameter, like `023.query-store-most-executed.sql`, for the
same two reasons: the text is cut at 500 characters, and it reads the whole
retained history. A query that timed out every night for a month is the
finding, and a sliding window would only hide the older nights. The window the
data covers is projected instead. The body follows the corpus contract like
every Query Store reader: `SET NOCOUNT ON`, `READ UNCOMMITTED`,
`LOCK_TIMEOUT 10000`, and `OPTION (RECOMPILE, MAXDOP 1)` on each of the three
statements.

Before SQL Server 2016 the runner skips the file on `@min_version`, and the
skip is recorded like any other; there is no root then.

### The root

A `LEFT JOIN` from `sys.databases`, as in 021, so a database whose store was
never enabled still gets a row:

- `database`, `collected_at`;
- `state.actual`, `state.desired`, `state.readonly_reason`,
  `state.capture_mode`;
- `window.oldest_interval`, `window.newest_interval`, `window.intervals`;
- `executions.regular`, `executions.aborted`, `executions.exception`: the
  totals over the store, all queries. Zero when the store holds none, NULL
  only when there is no store to read (`state.actual` NULL): a literal `SUM`
  gives NULL on an empty store, which a reviewer showed let a check "the root
  has the three totals" pass without anything being counted;
- `queries.with_aborted`, `queries.with_exception`;
- `listing_cap`: 50.

### The listings

`aborted`: the 50 queries with the most `Aborted` executions, ties broken on
`query_id`. `exceptions`: the same on `Exception`. A query can be in both.

Each row:

- `query_id`, `object`, labelled as in 023: `(ad hoc)` when there is no
  object, `(dropped object, object_id N)` when the id no longer resolves, and
  `schema.name` otherwise. The whole history is read, so dropped objects are
  expected, and a NULL would confuse them with ad hoc batches;
- for its interrupted type (`aborted.*` in the first listing, `exception.*` in
  the second): `executions`, `plans` (distinct plans with such executions),
  `avg_duration_ms` (weighted by executions), `min_duration_ms`,
  `max_duration_ms`, `avg_cpu_ms`, `first_execution`, `last_execution`;
- `regular.executions`, `regular.plans`, `regular.avg_duration_ms`,
  `regular.max_duration_ms`, `regular.avg_cpu_ms`: the same query's finished
  executions, which may have run under other plans; the two `plans` counts
  say so;
- in `aborted`, `exception.executions`, and in `exceptions`,
  `aborted.executions`, so the other count is visible without a join;
- `text`: the first 500 characters.

Durations and CPU are converted from microseconds to milliseconds.
`first_execution` and `last_execution` are the minimum and maximum of the
runtime rows' `first_execution_time` and `last_execution_time` for that type,
which Microsoft documents as execution end times, so they date the
interruptions, not the query.

Aggregation across plans and intervals: counts summed, averages weighted by
executions, the minimum of `min_duration` and the maximum of `max_duration`.

### No verdict

Nothing is labelled a timeout. The collector never infers the client's timeout
value, never calls a query blocked, and never names an error. Durations, CPU,
the regular behaviour beside them, the capture mode and the deadlock graphs
are the reader's material.

## What is not in scope

- Attention events, which need an Extended Events session this tool does not
  create.
- The error number of an `Exception`, which the Query Store does not keep.
- `process_killed` from `system_health`.

## Tests

- The corpus inventory gains one entry (`testdata/corpus.txt`, regenerated);
  `TestEmbeddedCorpusIsValid` runs the directive lint on the header and the
  contract and statement lints on the body.
- CI proves the classification, not only that the file runs. The CI job
  switches the Query Store on for `ci_probe` with capture mode `ALL`, runs one
  CPU-bound statement under a one-second client timeout and one division by
  zero, and asserts that the timed-out statement is in `aborted` and not in
  `exceptions`, and the division the other way round. Without that fixture
  the database is empty and every total is zero, whatever the collector
  filters on. The first version asserted only that both root counts were at
  least one, which a later review showed passes with the two types swapped:
  counts of one each are symmetric.
- On the lab: one query in `aborted` with its durations near 2000 ms, its CPU
  beside them and its regular executions; queries in `exceptions`, one of them
  with an average near 500 ms.

## What the panel changed

Five readers (agy and codex, each with a directive and a neutral prompt, and a
Claude subagent) ran the first draft against SQL Server 2025.

- The capture-mode limit: the first draft measured only under `ALL`, and
  under the default `AUTO` a blocking timeout on a rarely run query is never
  recorded (Claude subagent; extended by the author to a frequent query, which
  is recorded once captured).
- The duration equals the timeout only for the first statement of a request
  (Claude subagent), and was measured at 1923 ms for 2 s (codex): "to the
  millisecond" and "scattered means something else" were withdrawn.
- The "waiting on something" heuristic was refuted by the lab's own query and
  replaced by CPU beside duration.
- `sys.dm_exec_query_stats` does not count interrupted executions at all.
- Compilation timeouts and connection loss were added to the table; the root
  totals are zero rather than NULL on an empty store; CI now plants a timeout
  and an exception; objects are labelled as in 023; the plan counts show when
  regular and interrupted executions ran under different plans; the 262144
  note says what Microsoft documents.
- Confirmed by agy: a timeout during a blocked wait (under `ALL`) and a
  go-mssqldb context cancellation while rows are streamed are both `Aborted`;
  a deadlock victim's duration includes its lock wait.
- Cost: two readers noted that the root totals scan every runtime row, with no
  index leading on `execution_type`. That is the scan 023 and 024 already make
  under the same 120-second timeout, and 025, which makes several passes of
  the same view, took 757 ms on a store of 100,892 runtime rows. Not measured
  again for this file.
- Rejected: that recent executions are invisible until the store flushes. The
  author measured an exception visible at once with `flush_interval_seconds`
  900, the catalog views reading the in-memory data too.
