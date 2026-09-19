# Performance counters: the raw content of `sys.dm_os_performance_counters`

Status: draft, not implemented. Written on 19 September 2026, and revised
the same day after a panel of five independent readers ran it against SQL
Server 2025, 2022 and 2017. What the panel changed is listed at the end.

## The question

A DBA who opens Performance Monitor on a server reads the same counters
`sys.dm_os_performance_counters` exposes: page splits, full scans, forwarded
records, lock waits, latch waits, Query Store CPU, SQL errors, columnstore
activity, resource pool I/O. An archive that answers "what did PerfMon say"
has to carry them.

The corpus reads that view in six collectors, each for a named purpose and
each with its interpretation written beside it:

| Collector | Counters read |
| --- | --- |
| `10.system/010.properties.sql` | memory manager totals and grants, buffer cache hit ratio, batch requests, compilations, recompilations, `Transactions/sec` and `Log Flushes/sec` for `_Total`, and from `General Statistics` `User Connections`, `Logins/sec`, `Logouts/sec`, `Connection Reset/sec` |
| `10.system/015.buffer-pool.sql` | database cache, stolen and free memory, page life expectancy (minimum over NUMA nodes) |
| `10.system/050.tempdb.sql` | version store and tempdb counters of `Transactions` and `General Statistics` |
| `10.system/075.deprecated-features.sql` | `Deprecated Features`, above zero only |
| `80.workload/011.batch-response-times.sql` | `Batch Resp Statistics`, pivoted into a histogram |
| `90.availability/044.replication-counters.sql` | the replication objects |

Some facts the other counters carry reach the archive through other views:
`80.workload/010.wait-stats.sql` reads `sys.dm_os_wait_stats`, and
`70.schema/030.index-operational.sql` reads per-index lock, latch and
forwarded-record figures from `sys.dm_db_index_operational_stats`. What no
file carries is the counters themselves: `Access Methods` (`Full Scans/sec`,
`Page Splits/sec`, `Worktables Created/sec`), `Locks` per lock type (waits,
timeouts, deadlocks), `Latches`, `SQL Errors`, `Plan Cache` per cache type,
`Query Store`, `Columnstore`, `Memory Broker Clerks`, `Resource Pool Stats`
and `Workload Group Stats`, and the per-database `Databases` counters.

## What was measured

On SQL Server 2025 (17.0.4065.4, Linux container, 15 database instances in the
view, `mssqlsystemresource` and the Linux model copies included):

- 3,669 rows, 56 objects;
- `cntr_type`: 2,290 rows of 65792, 1,035 of 272696576, 47 of 272696320,
  71 of 537003264, 78 of 1073874176, 148 of 1073939712; 2017 and 2022 show
  the same six types and no other;
- per database, about 87 rows over `Databases`, `Columnstore`,
  `Catalog Metadata`, `Broker Activation`, `Query Store` and `Advanced
  Analytics` (a reviewer measured 87 rows and 14 KB of JSON for one
  `CREATE DATABASE`). A database in an availability group adds the
  `Database Replica` counters, about 24 more (not measured);
- the rest does not scale with databases, but it is not fixed either.
  `SQLPAL` objects (Linux 2025 only, absent from the 2017 and 2022 Linux
  images) were 1,177 rows and 193 KB of a 571 KB document, and
  `SQLPAL:Scheduler` grows with the scheduler count;
- sizes of the drafted collector's output: 571 KB on 2025 (3,590 rows), 1,283
  rows on 2017, 1,549 on 2022.

The names are `nchar` and padded. The object names carry a prefix:
`SQLServer:` on a default instance, `MSSQL$<name>:` on a named one, `SQLPAL:`
for the Linux host layer, and a product name with a year and no colon for the
in-memory OLTP objects (`SQL Server 2017 XTP Cursors` on 2017, `SQL Server
2022 XTP Cursors` on 2022 and on 2025). `(object, counter, instance)` is not
a unique key: on 2025 Linux `SQLPAL:Host Memory` `Total Memory (bytes)`
appears 17 times with an empty instance.

## The counter types

`cntr_type` is a Windows performance counter type. The six seen:

| `cntr_type` | Windows name | What Windows means by it |
| --- | --- | --- |
| 65792 | `PERF_COUNTER_LARGE_RAWCOUNT` | a value shown as is |
| 272696576 | `PERF_COUNTER_BULK_COUNT` | a 64-bit count, turned into a rate from two samples |
| 272696320 | `PERF_COUNTER_COUNTER` | the same on 32 bits, which wraps |
| 537003264 | `PERF_LARGE_RAW_FRACTION` | a numerator, divided by its base |
| 1073874176 | `PERF_AVERAGE_BULK` | a total, divided by its base's count |
| 1073939712 | `PERF_LARGE_RAW_BASE` | the denominator of one of the two above |

The declared type does not say how to read the value, and the collector does
not pretend it does. Measured on 2025:

- 65792 holds levels (`Memory Grants Pending`, `User Connections`), but also
  counts since start: all 68 `Batch Resp Statistics` rows (which 011 reads as
  such), `Databases` `Log Growths` and `Log Shrinks`, the `Broker Statistics`
  totals, the `Columnstore` totals. And rates the engine has already
  computed: `Workload Group Stats` `Requests completed/sec` read 1, 1, 1
  during a load where `Batch Requests/sec` (272696576) grew by 850 between
  reads. The `Wait Statistics` rows are 65792 too, with instance names that
  say "per second", "Cumulative" and "Average".
- 537003264 is not cumulative: the buffer cache hit ratio numerator read 168,
  20 and 454 on three reads, so the ratio it gives is of recent activity.
- Pairing a fraction or an average with its base cannot be done by name or by
  order. Most bases are the counter's name plus ` Base`, but the suffix is also
  `base`, `BASE` and `BS`; `FileTable` names `Avg time delete FileTable item`
  against `Time delete FileTable item BASE`; `Avg Dist From EOL/LP Request`
  goes with `Log Pool Requests Base`; `Tiered Buffer cache hit ratio` has no
  base. In `(object, counter, instance)` order, 21 of 154 fraction and average
  rows are followed by their base.

So `type_name` is projected as what the engine declares, and the reading of a
counter is the reader's, counter by counter.

## What is added

One collector, `queries/10.system/078.performance-counters.sql`:

```
-- @scope:       instance
-- @resultsets:  root:object, counters:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
```

No flag and no parameter. The body follows the corpus contract: `SET NOCOUNT
ON`, `READ UNCOMMITTED`, `LOCK_TIMEOUT 10000`, `OPTION (RECOMPILE, MAXDOP 1)`
on each statement. `VIEW SERVER STATE` was checked sufficient on 2025, where
it implies `VIEW SERVER PERFORMANCE STATE`.

### One read

The view is read once, into a table variable, and both result sets are built
from it. Two reads disagree whenever a database appears between them: a
`CREATE DATABASE` adds about 87 rows, and so does any access that opens an
`AUTO_CLOSE` database, whose rows vanish when it closes. A reviewer produced
a root of 1,804 rows beside an array of 1,891 that way. With one read the
root describes the array it sits beside.

### The root

- `collected_at`, `instance_start`, both on the server's local clock as
  `sys.dm_os_sys_info` gives the start;
- `uptime_s`: `(ms_ticks - sqlserver_start_time_ms_ticks) / 1000` from
  `sys.dm_os_sys_info`, which no clock change or UTC offset can skew;
- `counts.rows` and `counts.objects`, from the table variable.

`uptime_s` is the window of the instance-wide counters that accumulate. It is
not the window of a per-database row: those start when the database starts,
and reset on `OFFLINE`/`ONLINE`, on a restore or an attach, and when an
`AUTO_CLOSE` database reopens. A reviewer measured `Transactions/sec` for one
database at 3,083 before an `OFFLINE`/`ONLINE` and 11 after. The collector
does not date a database's start and says so; a per-database count divided
by `uptime_s` is not a rate.

### The array

One row per row of the view, except `Deprecated Features`. 075 owns those:
it keeps the ones above zero, and the 250 or so at zero are the noise it was
written to remove. The array is therefore not the whole view, and the root
does not claim it is.

- `object`, `counter`, `instance`: the three names, `RTRIM`med. `instance` is
  NULL when the view has an empty string there. The prefix of `object` is
  kept whole: stripping it would lose the named instance and the `SQLPAL`
  origin, and no single rule strips it (the XTP objects have no colon, and a
  year that differs by version). A reader matches objects the way the corpus
  already does, with `LIKE '%Buffer Manager%'`;
- `value`: `cntr_value`;
- `type`: `cntr_type`, and `type_name`: the Windows name from the table above,
  NULL for a type not in it.

Ordered by `object`, `counter`, `instance`, which is not unique (see above).
Zeros are kept: a level at zero is a reading, and a count at zero is one too,
within whatever window that counter has.

### No verdict, and no computation

The collector computes no rate, no ratio and no average. A rate needs a second
sample and this is one snapshot; a ratio needs a pairing the names do not
give; and the declared type would not say which rows to compute for.

## What is not in scope

- A second sample and the true rates it gives. That is sampling over time,
  which `sql-auditor observe` (`docs/observe-spec.md`) is designed for; a
  collector that waited seconds between two reads would lengthen every run.
- Dating each database's own start.
- Rewriting the six collectors that read counters today. They keep their
  selection and their interpretation; some of their values repeat here.

## Tests

- The corpus inventory gains one entry (`testdata/corpus.txt`, regenerated);
  `TestEmbeddedCorpusIsValid` lints the header and the body.
- CI asserts, on 2017 and 2022:
  - rows from several objects no other collector reads: `Access Methods`
    `Page Splits/sec`, `Locks`, `SQL Errors`, `Plan Cache`, and a `Databases`
    row whose instance is `ci_probe`;
  - the `Buffer Manager` `Page life expectancy` row, `type_name`
    `PERF_COUNTER_LARGE_RAWCOUNT`;
  - no row whose `object` contains `Deprecated Features`, and no NULL
    `type_name`;
  - `counts.rows` equal to the length of the array. With one read this is
    what the file promises, and it is the only check here that a mistaken
    second read could break.
  A collector narrowed to a few rows fails the first line.
- On the lab: the size against the figures above.

## What the panel changed

Five readers (agy and codex, each with a directive and a neutral prompt, and
a Claude subagent) ran the first draft against SQL Server 2025, 2022 and
2017.

- The type table claimed 65792 was a level. It holds counts since start and
  precomputed rates too (Claude subagent, verified by the author on the
  `Batch Resp Statistics` rows). The table now says what Windows means, and
  the spec says the engine does not keep to it.
- `uptime_s` was offered as the window of every accumulating counter; the
  per-database rows have their own, which resets (Claude subagent, and agy
  directive, whose own measurement did not show the reset).
- Root and array were two reads, so they could disagree (codex both seats,
  agy neutral, Claude subagent); now one read.
- The inventory said `General Statistics` logins and connections were lost,
  and that 010 read one `_Total` row; 010 reads them, and two (agy, codex).
  The facts other views carry are now named.
- The object count was 56, not 57 (codex both seats, Claude subagent);
  `LogPool FreePool` does not scale with databases (agy directive);
  the fixed part is not fixed on Linux; the key is not unique; matching after
  the colon fails for the XTP objects (codex directive, Claude subagent).
- `uptime_s` from ticks rather than clock times: a reader built it from a UTC
  collection time against the local start and was off by the UTC offset (agy
  neutral). The draft used local time on both sides, which a daylight-saving
  change would still skew.
- CI would have passed a collector narrowed to one row (codex directive).
- Rejected: including the `Deprecated Features` zeros so the array is the
  whole view (codex neutral). 075 removed them on purpose; the spec now says
  the array is not the whole view instead.
