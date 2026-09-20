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

The short answer: the corpus's own SQL holds. One thing does not, and it is
neither the corpus nor, as the first version of this document claimed, the
collation.

## The finding, and the correction a reviewer made to it

On a container built with `MSSQL_COLLATION=Latin1_General_BIN2`, every call of
`sys.sp_executesql` from T-SQL fails:

```
Msg 17750, Level 16, State 1, Procedure sys.sp_executesql, Line 1
Could not load the DLL (server internal), or one of the DLLs it references.
Reason: 126(The specified module could not be found.).
```

That is for `EXEC sys.sp_executesql N'SELECT ''ok'' AS r'`, with no dynamic
content of any kind. It reproduced on three containers and two builds, 2019
and 2022, and did not happen on `Latin1_General_CS_AS` or on the default
collation of the same image.

RESTARTING THE INSTANCE CLEARS IT, and that is the correction. The first
version of this document called the failure a property of the collation. A
reviewer restarted a BIN2 container and watched `sp_executesql` start working,
with the server collation unchanged; the author reproduced it on a container
of his own, failing on first boot and answering `ok` after `podman restart`.
The engine's log says what the state is: the instance reports `Attempting to
change default collation`, then `The default collation was successfully
changed`, and 17750 appears in that same server lifetime.

So the trigger is a default collation changed on a running instance, not a
collation. A container built with `MSSQL_COLLATION` is in that state exactly
once, on its first boot, and never again. On a real instance the same change
is made by rebuilding master, which restarts the service, so the window is
narrow and this is a container artefact far more than a production case. It is
recorded because it produced two real defects in the corpus, below, and
because "measured on three containers" was not the same as "measured", which
is the lesson worth keeping. Two properties make it worse than an error:

- `TRY ... CATCH` does not catch it. The batch goes on, `ERROR_NUMBER()` is
  never set, and a collector that wraps the call in its own `TRY` reports
  `error_number 0`;
- `INSERT INTO @t EXEC sys.sp_executesql ...` inserts zero rows and returns
  no error, so the staging table stays empty and the collector emits its row
  with every field null.

The result is the failure this project dislikes most: a clean, complete
looking, entirely empty answer.

Ten collectors read through `sp_executesql`, which is how this corpus defers a
view that may not exist on the build in front of it:

```
10.system/021.host-info.sql              90.availability/041.replication-publisher.sql
10.system/043.cpu-neighbours.sql             042.replication-distribution.sql
10.system/044.default-trace.sql              043.replication-subscriber.sql
10.system/045.default-trace-detail.sql       044.replication-counters.sql
20.databases/023.log-vlf.sql
70.schema/091.statistics-density.sql
```

Ten and not eleven: the first version of this list came from `grep -l`, and
`70.schema/020.index-usage.sql` matched on a comment saying it deliberately
does NOT defer its reads that way. It contains no `EXEC` at all, and a
reviewer confirmed its output is identical on both instances. A grep that
reads comments is how a list of affected files gains a file that is fine.

Measured on the same fixture, in that state against a healthy instance:

| Collector | In the broken state | On a healthy instance |
| --- | --- | --- |
| `021.host-info` | every field null, `source` still `dm_os_host_info`, `error_number` 0 | `Linux`, `Ubuntu`, `22.04`, `X64` |
| `044.default-trace` | `traces` 0, `events` 0 | `traces` 1, `events` 72 |
| `091.statistics-density` | `statistics` 0 | `statistics` 2 |
| `023.log-vlf` | `vlf_per_file` empty, and the root said `vlf_count` 0 | populated |
| `043.cpu-neighbours` | the root said `platform` Windows on a Linux host, `residue_computed` 1 | correct |

The whole run reported `0 error(s)` on both.

The last two rows are the reason any of this is worth recording, and a
reviewer found them. They are not "a collector returned less": they are a
collector returning something false, and neither needs a rare instance to do
it. Any silent failure of a deferred read produces them.

`023.log-vlf` counted an empty staging table and wrote `vlf_count` 0 and
`log_file_count` 0 into its root. A log always has at least two virtual log
files, so zero is not a possible measurement; it now leaves those fields null
and its `source` at `none`, which the file already has a word for.

`043.cpu-neighbours` was worse. Its platform falls back to `Windows`, which is
a sound deduction where `sys.dm_os_host_info` does not exist, because below
SQL Server 2017 there was no other platform. Where the view exists and the
read merely came back empty, the deduction is false, and the file then set
`residue_computed` to 1 and published a memory residue computed from it: a
number presented as measured, on a premise that was wrong. The fallback now
applies only where the view is absent, and the platform is null otherwise,
which takes the residue with it.

Both fixes were checked in both directions: unchanged output on a healthy
instance, and `source` `none` with null counts, and a null platform with no
residue, on an instance in the broken state.

## What did not need fixing

The eight other deferred readers lose an array and keep an honest root, or
lose nothing a reader could mistake for a measurement. And no probe was added
to the preflight: `docs/dynamic-sql-probe-spec.md` records why that design was
withdrawn once the trigger turned out to be what it is.

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
