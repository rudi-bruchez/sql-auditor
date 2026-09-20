# Does the corpus hold on an instance with a binary collation?

Measured on 20 September 2026, against four instances of the same image built
for the purpose, plus two more to confirm one finding:

| Instance | Server collation | Build |
| --- | --- | --- |
| binary | `Latin1_General_BIN2` | 16.0.4265.3 |
| binary, second build | `Latin1_General_BIN2` | 15.0.4480.2 |
| case sensitive | `Latin1_General_CS_AS` | 16.0.4265.3 |
| default | `SQL_Latin1_General_CP1_CI_AS` | 16.0.4265.3 |

Each one was given the same fixture: a database with a table of 5,000 rows, a
nonclustered index with an included column, a view, a procedure and the Query
Store on. The binary instance also got a second database in
`SQL_Latin1_General_CP1_CI_AS`, so that a database collation differing from
`tempdb`'s was in the run. The whole corpus was then collected from each, and
the archives compared field by field, looking for a value present on one side
and null, empty or absent on the other.

The short answer: the corpus's own SQL holds. One thing does not hold, and it
is not the corpus.

## The finding: dynamic SQL does not run at all on a BIN2 instance

On an instance whose server collation is `Latin1_General_BIN2`, every call of
`sys.sp_executesql` from T-SQL fails:

```
Msg 17750, Level 16, State 1, Procedure sys.sp_executesql, Line 1
Could not load the DLL (server internal), or one of the DLLs it references.
Reason: 126(The specified module could not be found.).
```

That is for `EXEC sys.sp_executesql N'SELECT ''ok'' AS r'`, with no dynamic
content of any kind. It reproduced on three containers and two builds, 2019
and 2022, and did not happen on `Latin1_General_CS_AS` or on the default
collation of the same image. Two properties make it worse than an error:

- `TRY ... CATCH` does not catch it. The batch goes on, `ERROR_NUMBER()` is
  never set, and a collector that wraps the call in its own `TRY` reports
  `error_number 0`;
- `INSERT INTO @t EXEC sys.sp_executesql ...` inserts zero rows and returns
  no error, so the staging table stays empty and the collector emits its row
  with every field null.

The result is the failure this project dislikes most: a clean, complete
looking, entirely empty answer.

Eleven collectors read through `sp_executesql`, which is how this corpus
defers a view that may not exist on the build in front of it:

```
10.system/021.host-info.sql          70.schema/020.index-usage.sql
10.system/043.cpu-neighbours.sql     70.schema/091.statistics-density.sql
10.system/044.default-trace.sql      90.availability/041.replication-publisher.sql
10.system/045.default-trace-detail.sql   042.replication-distribution.sql
20.databases/023.log-vlf.sql             043.replication-subscriber.sql
                                         044.replication-counters.sql
```

Measured on the same fixture, binary against default:

| Collector | On the binary instance | On the default one |
| --- | --- | --- |
| `021.host-info` | every field null, `source` still `dm_os_host_info`, `error_number` 0 | `Linux`, `Ubuntu`, `22.04`, `X64` |
| `044.default-trace` | `traces` 0, `events` 0 | `traces` 1, `events` 72 |
| `091.statistics-density` | `statistics` 0 | `statistics` 2 |
| `023.log-vlf` | `vlf_per_file` empty | populated |

The whole run reported `0 error(s)` on both.

What is not affected: the parameters the tool itself binds. The driver sends a
parameterized statement as an RPC rather than as `EXEC sys.sp_executesql`, and
`80.workload/025.query-store-compare.sql`, which takes three parameters, ran
and produced its root on the binary instance.

### What to do about it

Not a corpus change: rewriting eleven guards to avoid dynamic SQL would trade
a known and rare platform defect for a compile-time failure on every old
build, which is what the guards exist to prevent.

The honest fix is a probe. One `EXEC sys.sp_executesql N'SELECT 1'` at
preflight says whether dynamic SQL runs on this instance; where it does not,
the coverage block says so and names the collectors whose results will be
empty, exactly as it already does for a denied permission. A degraded run is a
success; a degraded run that reads as a complete one is not. That needs a spec
and a panel before code, and is recorded as its own task.

## What does hold

Sorting changes, contents do not. Under `BIN2`, `ORDER BY name` puts
`INFORMATION_SCHEMA` where a case-insensitive collation does not: the arrays
of `40.security/020.database-principals.sql` carry exactly the same members in
a different order. Nothing is lost, and an analysis that compares two archives
from differently collated instances has to compare sets rather than lines.

No collation conflict anywhere. A full run against the binary instance, which
held a database in `SQL_Latin1_General_CP1_CI_AS` beside a `tempdb` in
`Latin1_General_BIN2`, produced no `Cannot resolve the collation conflict`
error. The corpus declares its table variables inside the database it reads,
and the comparisons it makes are between columns of the same source.

Case sensitivity costs nothing, and that was checked rather than assumed. The
corpus compares 23 mixed-case literals against name columns: performance
counter names, configuration names. Each was tested on the case-sensitive
instance against the real values, once with a binary comparison and once with
a case-insensitive one, and the two counts were equal for all 23. The corpus
spells them exactly as the engine does.

The only other differences between the case-sensitive archive and the default
one were in the ring buffers, `10.system/041.connectivity` and
`043.cpu-neighbours`, which hold different events on two instances that have
not lived the same life. They are not collation.

## Method, for whoever repeats this

The comparison is the part worth keeping: two archives of the same fixture,
walked field by field, reporting only the paths where one side is null, empty
or absent and the other is not. A diff of the raw JSON is useless here, since
dates, ids and generated names differ on every line; the null-against-value
projection is what turns 459 raw differences into the five that mean
something.
