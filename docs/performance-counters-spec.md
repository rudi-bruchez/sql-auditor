# Performance counters: the whole of `sys.dm_os_performance_counters`

Status: draft, not implemented. Written on 19 September 2026.

## The question

A DBA who opens Performance Monitor on a server reads the same counters
`sys.dm_os_performance_counters` exposes: page splits, full scans, forwarded
records, lock waits, user connections, logins, latch waits, Query Store CPU,
SQL errors. An archive that answers "what did PerfMon say" has to carry them.

The corpus already reads that view in six collectors, each for a named
purpose and each with its interpretation written beside it:

| Collector | Counters read |
| --- | --- |
| `10.system/010.properties.sql` | memory manager totals and grants, buffer cache hit ratio, batch requests, compilations, recompilations, transactions, log flushes |
| `10.system/015.buffer-pool.sql` | database cache, stolen and free memory, page life expectancy (minimum over NUMA nodes) |
| `10.system/050.tempdb.sql` | version store and tempdb counters of `Transactions` and `General Statistics` |
| `10.system/075.deprecated-features.sql` | `Deprecated Features`, above zero only |
| `80.workload/011.batch-response-times.sql` | `Batch Resp Statistics`, pivoted into a histogram |
| `90.availability/044.replication-counters.sql` | the replication objects |

Everything else in the view reaches no archive. Among what is lost:
`Access Methods` (`Full Scans/sec`, `Page Splits/sec`, `Forwarded Records/sec`,
`Worktables Created/sec`), `Locks` per lock type (waits, timeouts, deadlocks),
`Wait Statistics`, `Latches`, `General Statistics` (`User Connections`,
`Logins/sec`, `Logouts/sec`, `Processes blocked`), `SQL Errors`,
`Plan Cache`, `Query Store`, `Columnstore`, `Memory Broker Clerks`,
`Resource Pool Stats` and `Workload Group Stats`, and the per-database
`Databases` counters beyond the one `_Total` row 010 reads.

## What was measured

On SQL Server 2025 (17.0.4065.4, Linux container, 11 user and system
databases plus `mssqlsystemresource` and the Linux model copies, which the
view lists as instances):

- 3,669 rows, 57 objects;
- `cntr_type`: 2,290 rows of 65792, 1,035 of 272696576, 47 of 272696320,
  71 of 537003264, 78 of 1073874176, 148 of 1073939712;
- the largest objects: `Databases` 960, `SQLPAL:Scheduler` 840 (Linux only,
  one set per scheduler), `Deprecated Features` 257, `Columnstore` 180;
- rows whose `instance_name` is a database: about 88 per database, over
  `Databases`, `Columnstore`, `Catalog Metadata`, `Broker Activation`,
  `Query Store` and `Advanced Analytics`, plus `LogPool FreePool`;
- the text of the rows plus about 80 bytes of JSON per row: about 500 KB.

So the size is roughly 400 KB fixed and 13 KB per database: 13 MB at a
thousand databases. The per-database collectors of the corpus already write
more than that on such an instance.

The object names carry a prefix: `SQLServer:` on a default instance,
`MSSQL$<name>:` on a named one, `SQLPAL:` for the Linux host layer, and a
product name with no colon for the in-memory OLTP objects (`SQL Server 2022
XTP Cursors` on a 2025 instance). `object_name`, `counter_name` and
`instance_name` are `nchar` and padded.

## The counter types

`cntr_type` is a Windows performance counter type, and it decides how the
value is read. The six seen:

| `cntr_type` | Windows name | What the value is |
| --- | --- | --- |
| 65792 | `PERF_COUNTER_LARGE_RAWCOUNT` | a level at the moment of collection |
| 272696576 | `PERF_COUNTER_BULK_COUNT` | a count since the instance started; the "/sec" in its name is what PerfMon computes from two samples |
| 272696320 | `PERF_COUNTER_COUNTER` | the same, on 32 bits |
| 537003264 | `PERF_LARGE_RAW_FRACTION` | a numerator; divided by its base it is a ratio |
| 1073874176 | `PERF_AVERAGE_BULK` | a cumulative total; divided by its base it is an average |
| 1073939712 | `PERF_LARGE_RAW_BASE` | the denominator of one of the two above |

Pairing a fraction or an average with its base cannot be done by name in SQL.
Most bases are the counter's name plus ` Base`, but the suffix is also
`base`, `BASE` and `BS`; `FileTable` names `Avg time delete FileTable item`
against `Time delete FileTable item BASE`; `Avg Dist From EOL/LP Request` is
paired with `Log Pool Requests Base`; and `Tiered Buffer cache hit ratio` has
no base at all. The collector does not pair them.

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
on both statements.

### The root

- `collected_at`, `instance_start` and `uptime_s` (seconds from the start to
  the collection), which a reader needs to turn a count since start into a
  rate;
- `counts.rows` and `counts.objects`: what the array holds.

### The array

One row per counter row of the view, except `Deprecated Features`, which 075
owns and filters: its 257 rows are mostly zeros, and shipping them twice would
be the noise 075 was written to remove.

- `object`, `counter`, `instance`: the three names, `RTRIM`med. `instance` is
  NULL when the view has an empty string there. The prefix of `object` is
  kept: stripping it would lose the named instance and the `SQLPAL` origin,
  and a reader matches on the part after the colon either way;
- `value`: `cntr_value`;
- `type`: `cntr_type`, and `type_name`: the Windows name from the table above,
  NULL for a type not in it.

Ordered by `object`, `counter`, `instance`. Zeros are kept: a level at zero
is a reading (`Memory Grants Pending`), and a count at zero since the start
says the event never happened, which is also a finding.

### No verdict, and no computation

The collector computes no rate, no ratio and no average. A rate needs a second
sample and this is one snapshot; a ratio needs the pairing the names do not
allow. `uptime_s` in the root is there so a reader can compute an average
rate since start and say that it is one.

## What is not in scope

- A second sample and the true rates it gives. That is sampling over time,
  which `sql-auditor observe` (`docs/observe-spec.md`) is designed for; a
  collector that waited seconds between two reads would lengthen every run.
- Rewriting the six collectors that read counters today. They keep their
  selection and their interpretation; the new file is the raw layer beside
  them, and some of its rows repeat what they project.

## Tests

- The corpus inventory gains one entry (`testdata/corpus.txt`, regenerated);
  `TestEmbeddedCorpusIsValid` lints the header and the body.
- CI asserts, on 2017 and 2022, that the array is not empty, that it holds a
  `Buffer Manager` `Page life expectancy` row with `type_name`
  `PERF_COUNTER_LARGE_RAWCOUNT`, that no row's `object` contains
  `Deprecated Features`, and that `counts.rows` equals the length of the
  array.
- On the lab: every `cntr_type` present has a `type_name`, and the size is
  measured against the estimate above.
