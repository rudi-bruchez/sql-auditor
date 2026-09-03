# Collection gaps — specification

**Date:** September 2026, after an audit of two SQL Server 2016 SP1 instances.
**Status:** specification. Nothing here is implemented yet.

Five gaps, each one found because an audit could not answer a question from the
archive and had to go back to the client. That is the bar for entry: a gap is
something the analysis actually needed, not something that would be nice to
have.

One of the five is not a missing collector at all: the facts are collected and
the arithmetic that turns them into a finding is left to a reader who has no
reason to attempt it. That case is section 5, and it is the cheapest of the
five to fix.

A sixth idea is recorded at the end **because it is already implemented**, and
the point of writing it down is to stop it being built a second time.

---

## 1. VLF count below SQL Server 2016 SP2

### The gap

`20.databases/023.log-vlf.sql` reads `sys.dm_db_log_info`, which arrived in
2016 SP2, so the file carries `@min_version: 13.0.5026`. Below that build the
archive has no VLF count at all — and the instances that predate SP2 are
precisely the ones whose logs have been growing by percentage increments for
years.

The audit that raised this had two instances at 13.0.4457 and 13.0.4451. The
report had to write "the VLF count could not be collected, and on a 61 GB log
grown in 10% increments there is a good chance it is badly fragmented". That
sentence is a guess wearing an audit's clothes. Run by hand afterwards, the
real answer was 46, which is fine. **The guess was wrong, and it was printed.**

### The mechanism

`DBCC LOGINFO` predates everything the corpus supports and returns one row per
VLF. Counting the rows is the whole collector.

Two constraints shape the design.

**It needs sysadmin, and we will not ask for it.** `DBCC LOGINFO` is documented
as requiring membership in the sysadmin fixed server role, and on some builds
`db_owner` in the database is enough. Either way it is well above the read-only
posture of this collector, and `collect/grants.go` must **never** emit a role
addition for it. The rule stands: a permission we cannot ask for is a
capability we probe and skip.

The existing preflight machinery does this already, and nothing new is needed
beyond one entry. `Capabilities()` in `collect/preflight.go` gains:

```go
{Name: "dbcc_loginfo", Label: "Count virtual log files (DBCC LOGINFO)",
    SQL:    "DBCC LOGINFO WITH NO_INFOMSGS",
    Impact: "VLF count not collected on instances below 2016 SP2"},
```

The probe runs in whatever database the connection lands in — `master`, whose
log is small — and a login without the permission gets an error, so the
ordinary raising path applies and `NeedsRows` stays false. `NormalisePermission`
gains the `DBCC LOGINFO` spelling so `@permissions` and the capability name stay
one vocabulary, which `preflight_test.go` already enforces in both directions.

**It must not run where the DMV works.** Running both on a modern instance
would write two VLF counts into the archive from two different mechanisms, and
a reader would have to know which to believe. This calls for a directive the
corpus does not have yet:

```
-- @max_version: 13.0.5025
```

Symmetric to `@min_version` in every respect: parsed in `collect/queryset.go`
into `Script.MaxVersion []int`, checked in `collect.go` beside the existing
`VersionAtLeast` call, and reported in the manifest note as
`SQL Server <version> and below`. The skip reason is the same shape as the
existing one, so nothing downstream changes.

Two files then cover every supported build with no overlap and no hole:
`023.log-vlf.sql` from 13.0.5026 up, `025.log-vlf-dbcc.sql` below it.

### The collector

`20.databases/025.log-vlf-dbcc.sql`, `@scope: database`, one result set.

```sql
CREATE TABLE #loginfo (
    RecoveryUnitId int NULL, FileId int, FileSize bigint, StartOffset bigint,
    FSeqNo bigint, Status int, Parity int, CreateLSN nvarchar(48));

INSERT INTO #loginfo EXEC ('DBCC LOGINFO WITH NO_INFOMSGS');
```

`RecoveryUnitId` is the trap. It was added in SQL Server 2012 and the column
list of `DBCC LOGINFO` differs on either side of that boundary, so an `INSERT
... EXEC` against a fixed table shape fails with error 213 on the wrong version.
The corpus floor is 2012, so the nine-column shape is the one to declare — but
the file must say so, because the next person to read it will not know why a
column nobody projects is in the table definition.

Projected: `vlf_count` (the row count), `vlf_active_count` (`Status = 2`),
`vlf_min_size_mb`, `vlf_avg_size_mb`, `vlf_max_size_mb`, `vlf_under_1mb_count`,
and `log_file_count` (distinct `FileId`). The same names as
`023.log-vlf.sql` projects, so an analysis reads one shape whatever produced it.

The per-VLF rows are not projected, for the same reason as in the existing
file: a badly grown log holds tens of thousands of them and the aggregate says
everything.

---

## 2. The Windows version and the host

### The gap

The archive says nothing about the operating system. `10.system/020.host-services.sql`
reads `sys.dm_server_services` and the registry, which gives the service
accounts and the startup parameters, not the OS.

An audit needs the OS build for three things it is routinely asked: whether the
host is still supported, whether a known storage or scheduler fix applies, and
whether the memory configuration makes sense against the physical machine. All
three were asked in September 2026 and the answer was "the collection does not
report it today".

### The collector

`10.system/021.windows-info.sql`, `@scope: instance`,
`@permissions: CONNECT, VIEW SERVER STATE`, one result set.

`sys.dm_os_windows_info` gives `windows_release`, `windows_service_pack_level`,
`windows_sku` and `os_language_version`. `sys.dm_os_host_info` gives the same
plus `host_platform` and `host_distribution`, and exists from 2017 — so the
file reads `dm_os_windows_info` unconditionally and `dm_os_host_info` behind a
`min_version` sibling, or projects NULLs. Prefer two files over a branch: the
corpus already splits on version by filename (`011.all-databases-2014.sql`,
`021.always-on-2016.sql`) and that convention is worth keeping.

**`windows_release` is a number, and it is not the marketing name.** `10.0`
covers Windows Server 2016, 2019, 2022 and 2025 alike, and Windows 10 and 11
with it. The collector projects the raw value and **must not** map it to a
product name: the mapping needs the build number, which this DMV does not give,
and a wrong product name in an audit is worse than a version number the reader
has to look up. `windows_sku` is likewise projected as its integer.

---

## 3. Transport, authentication scheme and encryption in transit

### The gap

`10.system/041.connectivity.sql` reads the connectivity **ring buffer** —
connections that failed or were reset. Nothing reads `sys.dm_exec_connections`,
so the archive cannot say how sessions actually connect.

Three findings depend on it and none could be made:

- **Encryption in transit.** `encrypt_option` is the only place the instance
  says whether the TDS session is encrypted. An audit that recommends enabling
  encryption without knowing the current state is guessing.
- **Authentication scheme.** `auth_scheme` distinguishes KERBEROS, NTLM and
  SQL. NTLM where Kerberos was assumed means an SPN is missing, which is a real
  finding with a real fix and is invisible everywhere else.
- **Transport.** `net_transport` separates TCP from Shared Memory and Named
  Pipes. Sessions arriving over Shared Memory are running **on the server
  itself**, which changes what an application-latency finding can mean.

### The collector

`10.system/042.connection-security.sql`, `@scope: instance`,
`@permissions: CONNECT, VIEW SERVER STATE`.

Aggregate, never per session. One row per
`(net_transport, auth_scheme, encrypt_option, protocol_version)` with a count,
plus the number of distinct client hosts in each group. A per-session dump
would carry `client_net_address` and hostnames into the archive for no
analytical gain, and the aggregate answers all three questions above.

The collector's own session is in the result and cannot be excluded honestly —
the auditor's connection is a connection. It is marked rather than filtered:
one boolean column `is_collector_session`, set from `@@SPID`.

---

## 4. Execution plans when the Query Store is off

### What already exists

`80.workload/021.query-store-detail.sql` extracts execution plans from the
Query Store. It is gated on `@requires_flag: query_store_detail` and capped at
`@qs_top` queries selected by a round robin over four metrics — total duration,
CPU, reads and executions — so the archive gets the plans that matter by any of
four definitions of "matter", not by one.

That file is why a recent audit had 138 plans in the archive.

### The gap

**When the Query Store is off, the archive contains no plan at all.** Nothing
reads `sys.dm_exec_query_plan`. On such an instance the analysis has aggregate
counters and no way to see a plan shape, which is the difference between "this
procedure is expensive" and "this procedure is expensive because a CTE is
expanded five times".

### The collector

`80.workload/041.plan-cache-plans.sql`, `@scope: database`,
`@requires_flag: plan_cache_plans`, `@writer: plan-cache-plans`, following
`021` in every structural respect so that an analysis reads one shape.

Selection is the same round robin over four metrics from
`sys.dm_exec_query_stats`, capped by the same kind of parameter. Deduplication
is on `query_plan_hash`: the cache holds one entry per plan handle and a
procedure called with different `SET` options has several, all identical.

Three things the file must say about itself, because each one is a way to read
it wrongly:

- **The cache is not history.** A plan absent from it was evicted or has not run
  since the last restart. `021` can say a query was not among the top N; this
  file cannot distinguish that from "never ran".
- **Cached plans carry no runtime statistics.** They are compiled plans, the
  same limitation as the Query Store plans, and the report must not read an
  operator cost as a measurement.
- **`sys.dm_exec_query_plan` returns NULL** rather than raising, for a plan too
  large or containing an XML-invalid construct. A NULL is written as a NULL and
  counted, never silently dropped.

### What this deliberately does not do

**It does not compile a plan for a procedure that has none.** Obtaining an
estimated plan for an arbitrary procedure means `SET SHOWPLAN_XML ON` and
executing the batch, which requires parameter values the collector cannot
invent, compiles on the production instance, and would break the read-only
promise the corpus makes everywhere else. "Every plan we can get" therefore
means "every plan already materialised in the Query Store or the cache", and
that set is the bound.

**It does not collect all procedures.** A database of eight hundred procedures
whose plans run from 0.5 to 2 MB would produce a multi-gigabyte archive to
carry mostly plans nobody reads. The cap is what makes the collector usable,
and it stays.

---

## 5. Is SQL Server alone on this machine?

### The gap, and what already answers half of it

An application server sharing a host with its database is a finding an audit
should make on its own, and today it is made by accident. In September 2026 it
surfaced only because the client mentioned it in passing.

**Part of the answer is already in the archive and nobody was reading it.**
`10.system/010.properties.sql` collects `total_physical_ram_mb`,
`available_physical_ram_mb` and `sql_ram_in_use_mb`. The subtraction is the
finding:

```
other_processes_mb = total_physical_ram - available_physical_ram - sql_ram_in_use
```

On the instance in question that came to 18.5 GB of RAM held by something other
than the database engine, on a machine of 64 GB. Nothing in the report said so,
because the three numbers sat in three fields and the arithmetic was left to a
reader who had no reason to attempt it.

**The first change is therefore not a collector.** `010.properties.sql` should
project the derived value beside its operands, the way `014.cpu-topology.sql`
projects the hardware-versus-soft-NUMA answer rather than leaving it to be
inferred. A fact that is meaningless without an operation on its neighbours does
not get to stay implicit.

### What is genuinely missing: the CPU half

Memory says something else is resident. It does not say something else is
*running*. The ring buffer does.

`RING_BUFFER_SCHEDULER_MONITOR` records carry a `<SystemHealth>` element with
`<ProcessUtilization>` — the CPU percentage used by the SQL Server process —
and `<SystemIdle>`. What is left over is everybody else:

```
other_processes_pct = 100 - ProcessUtilization - SystemIdle
```

That is the standard reading and it is a time series, one record per minute,
which is better than any instantaneous figure: it shows whether the neighbour is
constant or spikes at the hour a batch runs.

`10.system/041.connectivity.sql` already reads `sys.dm_os_ring_buffers` and
already reports the span of every buffer, but it projects only the connectivity
records. The scheduler-monitor records need their own collector,
`10.system/043.cpu-neighbours.sql`, and it inherits two constraints from its
sibling, both learned the hard way there:

- **The buffer wraps at 256 records.** Measured on a real instance: a span of
  4 hours 15 minutes. The window must be reported beside the numbers, because a
  short window *is* the finding when someone reads a quiet afternoon as a quiet
  server.
- **The ms_ticks arithmetic is done in seconds**, not milliseconds, or it
  overflows a 32-bit int after 24 days of uptime — which is exactly the
  population worth auditing.

### The third signal: who connects from the machine itself

Covered by collector 3 above. `net_transport = 'Shared memory'` means the
session originates on the server, and a local TCP connection means the same
thing. Add `program_name` from `sys.dm_exec_sessions`, aggregated with a count,
and the archive says *which* application is co-resident rather than only that
one is.

`program_name` is client-supplied and therefore not evidence: it is empty for a
default `SqlClient` connection and can say anything. It corroborates, it does
not prove.

### The honest limit, which belongs in the file

**Nothing inside SQL Server can enumerate the processes of its host.** There is
no read-only DMV for it, and the paths that exist — `xp_cmdshell`, a CLR
assembly, WMI through a linked server — all require permissions this collector
refuses to ask for and would break the read-only promise.

So the finding this makes possible is *"SQL Server is not alone on this machine,
here is how much memory and how much CPU it is not getting, and here is what
connects locally"*. It is not a process list, and the collector must not be
written as though it were producing one. That phrasing matters: an audit that
says "18.5 GB is used by other processes" is reporting a measurement, while one
that names the application is repeating what somebody said.

`sys.dm_server_services`, already collected by `020.host-services.sql`, closes
the last corner: it reports the SQL-family services on the host, so a Reporting
Services or Analysis Services instance sharing the machine is visible without
any of the above.

## 6. Procedure execution frequency — already collected, do not build again

Recorded here because it was proposed as new twice, from two directions, and
the second proposal is the reason this section exists.

**From the plan cache:** `80.workload/050.procedure-stats.sql` already reports,
for every stored procedure of the database still in the cache, the execution
count, elapsed time, CPU, logical and physical reads and logical writes — each
one as a total **and** per execution — with `cached_time` beside them, without
which the counters are not comparable between procedures.

**From the Query Store:** `80.workload/023.query-store-most-executed.sql`
already carries a `by_object` result set, which is the same frequency
aggregated per procedure over the whole retained window.

The two are not redundant and the difference is worth knowing when reading
them: the cache resets on restart, on recompilation and on memory pressure,
while the Query Store keeps its window across all three. A procedure heavy in
the Query Store and absent from the cache was evicted; the reverse means it
started running recently.

What is genuinely missing is not a collector. It is that nothing joins them: an
analysis wanting "the procedures ranked by frequency" reads two files with two
different windows and reconciles them by hand. That belongs in the analysis
step, not here.
