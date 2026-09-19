# Interrupted executions: query timeouts and errors from the Query Store

Status: draft, not implemented. Written on 19 September 2026.

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
counts the execution without saying it was stopped.

The Query Store does say it. `sys.query_store_runtime_stats` keeps one row per
plan, interval and `execution_type`:

- 0, `Regular`: the statement finished;
- 3, `Aborted`: the client stopped it;
- 4, `Exception`: an error stopped it.

Nothing in the corpus ranks on that column today.
`80.workload/021.query-store-detail.sql` projects it, but only for the queries
it already selected, and only behind `--query-store-detail`.
`80.workload/020.query-store.sql`, `023` and `024` do not filter on it, so an
aborted execution counts in their totals like a finished one.

## What was measured

On SQL Server 2025 (17.0.4065.4), in a scratch database with the Query Store
in `READ_WRITE`, one-minute intervals and capture mode `ALL`:

| What happened | `execution_type` | What the row holds |
| --- | --- | --- |
| a statement stopped by `sqlcmd -t 2` (client timeout, "Timeout expired") | 3, `Aborted` | 3 executions, average 2001 ms, maximum 2002 ms |
| the same statement finishing normally | 0, `Regular` | a separate row for the same plan and interval |
| a division by zero (Msg 8134) | 4, `Exception` | 2 executions |
| a `SELECT` that hit `SET LOCK_TIMEOUT 500` (Msg 1222) | 4, `Exception` | 1 execution, average 500 ms |
| a statement whose session was ended by `KILL` | nothing | no runtime row at all for that execution |

Three consequences for the design:

1. The duration of the aborted executions is the client's command timeout, to
   the millisecond. A query whose aborted executions all last about 30,000 ms
   was stopped by a 30-second timeout, which is the .NET `SqlCommand` default.
   Minimum and maximum are projected so the reader can see the durations
   cluster; the collector does not name the timeout.
2. `Aborted` is any client-side stop, not only a timeout: a user pressing
   Cancel, an application cancelling a task. Clustered durations say timeout;
   scattered ones say something else. The collector reports, it does not
   classify.
3. `Exception` does not carry the error number. A lock timeout and a
   division by zero are the same row. The duration helps (a lock timeout lasts
   `LOCK_TIMEOUT`), and deadlock victims can be matched against
   `10.system/061.deadlock-graphs.sql`, but the collector does not guess.

And one limit to state: an execution ended by `KILL` leaves nothing in the
runtime statistics, so a DBA who kills runaway queries by hand makes them
invisible here.

A side note, measured while setting this up and recorded because it will
happen again: after a load test drove a store into `READ_ONLY` with
`readonly_reason` 262144 (the in-memory limit), every Query Store on the
instance, including one created afterwards, went back to `READ_ONLY` within
seconds of being set `READ_WRITE`, until the instance was restarted.
`state.readonly_reason` is already projected by the readers, and this one
projects it too.

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
data covers is projected instead.

### The root

A `LEFT JOIN` from `sys.databases`, as in 021, so a database whose store was
never enabled still gets a row:

- `database`, `collected_at`;
- `state.actual`, `state.desired`, `state.readonly_reason`,
  `state.capture_mode`;
- `window.oldest_interval`, `window.newest_interval`, `window.intervals`;
- `executions.regular`, `executions.aborted`, `executions.exception`: the
  totals over the store, all queries;
- `queries.with_aborted`, `queries.with_exception`: how many queries have at
  least one such execution;
- `listing_cap`: 50.

The totals give the proportion; the two listings give the queries.

### The listings

`aborted`: the 50 queries with the most `Aborted` executions, ties broken on
`query_id`. `exceptions`: the same on `Exception`. A query can be in both.

Each row:

- `query_id`, `object` (schema.name, null for ad hoc);
- for its interrupted type (`aborted.*` in the first listing, `exception.*` in
  the second): `executions`, `avg_duration_ms` (weighted by executions),
  `min_duration_ms`, `max_duration_ms`, `avg_cpu_ms`, `first_execution`,
  `last_execution`;
- `regular.executions`, `regular.avg_duration_ms`, `regular.max_duration_ms`:
  how the same query behaves when it finishes. A query that finishes in 20 ms
  and is aborted at 30 s is waiting on something; one that finishes in 29 s
  when it finishes is simply too slow for its timeout;
- in `aborted`, `exception.executions`, and in `exceptions`,
  `aborted.executions`, so the other count is visible without a join;
- `text`: the first 500 characters.

Durations are converted from microseconds to milliseconds, as everywhere else.
`first_execution` and `last_execution` are the minimum and maximum of the
runtime rows' `first_execution_time` and `last_execution_time` for that type,
so they date the interruptions, not the query.

### No verdict

Nothing is labelled a timeout. The collector never infers the client's timeout
value, never calls a query "blocked", and never names an error. Clustered
durations, the regular behaviour beside them, and the deadlock graphs are the
reader's material.

## What is not in scope

- Attention events, which need an Extended Events session this tool does not
  create.
- The error number of an `Exception`, which the Query Store does not keep.
- Instances without a Query Store (before 2016, or with it off): the root says
  the store is off, and there is nothing else to read.

## Tests

- The corpus inventory gains one entry (`testdata/corpus.txt`, regenerated),
  which also runs the contract lint on the header.
- CI: the file is produced for `ci_probe` on 2017 and 2022, and its root has
  the three execution totals.
- On the lab: the scenario above gives one query in `aborted` with a minimum
  and maximum near 2000 ms and its regular executions beside them, and two in
  `exceptions`, one of them with an average near 500 ms.
