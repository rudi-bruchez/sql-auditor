# Collection gaps — specification

**Date:** September 2026, after an audit of two SQL Server 2016 SP1 instances.
**Status:** specification, except section 8, which is implemented.
**Revised:** 3 September 2026, after a five-reader external review. Every claim
below that says "measured" was measured on SQL Server 2022 (16.0.4265.3,
Developer, on Linux) unless another version is named. Four of the five
specified collectors carried a defect that would have cost a failed or
misleading collection run; what follows is what survived.

Nine gaps, each one found because an audit could not answer a question from the
archive and had to go back to the client. That is the bar for entry: a gap is
something the analysis actually needed, not something that would be nice to
have.

One of them is not a missing collector at all: the facts are collected and the
arithmetic that turns them into a finding is left to a reader who has no reason
to attempt it. That case is section 5.

Section 8 breaks the entry rule deliberately: two views that no instance
audited so far even has, collected because writing the file now costs nothing
and means the first modern instance arrives with the collector already in
place. Those two are written, not specified.

A last idea is recorded at the end **because it is already implemented**, and
the point of writing it down is to stop it being built a second time.

Sections 6 and 7 came out of a cross-check against a comprehensive audit
checklist assembled from sp_Blitz, Glenn Berry's diagnostic queries and this
practice's own topic corpus. What that cross-check mostly established is that
the corpus already covers those reference sets: buffer descriptors, memory
clerks, per-node page life expectancy, statistics properties, physical index
fragmentation, waiting tasks, scheduler counts, single-use cached plans, trace
flag status, the version store and the error log are all collected today. It
also arrived independently at the ring buffer of section 5, naming the same
four-hour window — which is agreement worth recording, because a gap two
sources find separately is not an opinion.

## What the review changed, in one place

Three lessons run through the revisions and are worth stating once rather than
seven times.

**A declared permission that does not cover the collector is worse than no
collector.** `@permissions` drives the skip gate: declare something the login
holds and the file *runs and fails*, landing in `Errors` instead of "Queries
not run". Two sections did exactly that. The rule now: the declared permission
is the one the read actually needs, measured, and where the real permission is
one this practice will not ask a client for, the collector tries anyway and
records the refusal rather than pretending it was never needed.

**The corpus floor includes Linux, and the first draft was written as though
every instance were Windows.** Two formulas printed false findings there —
one a negative number, one a constant 100%. Platform is now a first-class axis
beside version.

**The corpus runs on one pinned connection** (`db.SetMaxOpenConns(1)`,
`collect/runner.go`), so a `#temp` table outlives the batch that made it and
the second database of a run fails with error 2714. The corpus has hit this
twice. Nothing specified here creates a `#temp` table.

---

## 1. VLF count below SQL Server 2016 SP2

slug: vlf-count

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

### One file, not two, and no new directive

The first draft proposed a second file, `025.log-vlf-dbcc.sql`, gated by a new
`@max_version` directive so the two mechanisms could not both fire. The review
took that design apart, and the replacement is smaller in every dimension.

**`@max_version` is not needed.** The condition that matters is not "which
build is this" but "does the DMV exist here", and a collector can ask that
directly. `023.log-vlf.sql` loses its `@min_version` gate and answers the
question itself: read `sys.dm_db_log_info` when it is there, `DBCC LOGINFO`
when it is not, and say in the output which one produced the numbers. One
file, one shape, no overlap and no hole by construction rather than by
arithmetic on version strings.

That deletes a long list of work the first draft under-priced: a `MaxVersion`
field and parse case, a comparison that is *not* `VersionAtLeast` (an upper
bound cannot reuse prefix-matching semantics), an inverted skip reason, a
`scriptNote` branch, a rewrite of the "gated collectors" pass in
`docs/verification-2012.md` — which is written for a min-only world — and a
`tools/verify-corpus-grammar.ps1` that inspects only `@min_version` and would
parse a max-gated file under the 2012 grammar. It also removes a trap nobody
had noticed: Azure SQL Database and Managed Instance report ProductVersion
12.0.x indefinitely, so a file gated at "13.0.5025 and below" would be selected
there.

And it removes an ordering hazard that is real today: **an unknown directive is
silently ignored.** `parseScript` switches on directive names with no default
case, so a file carrying `@max_version` on a binary whose parser predates it
lints clean and runs on *every* version — writing the duplicate VLF count the
design existed to prevent, under a manifest saying nothing. See section 11.

### The permission, and why nothing is probed

`DBCC LOGINFO` is documented as requiring sysadmin. The first draft hedged that
"on some builds `db_owner` in the database is enough"; measured on 2022, it is
not — a `db_owner` of the database got the same `Msg 2571` as a bare login, and
so did `VIEW SERVER PERFORMANCE STATE`.

The first draft's answer was a `dbcc_loginfo` capability, probed and never
granted. The review costed it and the answer is no:

- It breaks an existing test. `TestEveryProbedCapabilityCanBeGranted` fails
  with "capability `dbcc_loginfo` is probed but nothing grants it" — and the
  rule that causes the failure is the draft's own ("`grants.go` must never emit
  a role addition for it").
- It makes `Coverage: COMPLETE` unreachable. `refreshCoverage` marks the
  archive incomplete for any probe that is not `ok`, and no grant script could
  ever fix it. Every client would be handed an archive that says it is
  incomplete, forever, for a capability nobody should grant.
- It is measured once, in `master`, for a permission that is per-database.

So: **nothing is probed and nothing is asked for.** The collector attempts the
`DBCC` path and records `Msg 2571` when refused, which is the same opportunistic
posture `docs/replication-spec.md` adopts for the publication catalog. On a
read-only audit login below 2016 SP2 the VLF count stays uncollected, and the
archive says why in the row rather than by omission. That is a smaller promise
than the first draft made, and it is one the tool can keep.

### The collector

`20.databases/023.log-vlf.sql`, `@scope: database`, `@resultsets: root:object,
vlf_per_file:array`, `@permissions: CONNECT, VIEW SERVER STATE`,
`@timeout: 60`. No `@min_version`.

The guard is the pattern `docs/replication-spec.md` specifies and measures:
`OBJECT_ID` decides, the read runs inside `sp_executesql` so that a missing
object raises an error the handler can catch, the rows land in a **table
variable**, and the result sets are emitted unconditionally from it.

```sql
DECLARE @vlf TABLE (file_id int, vlf_size_mb decimal(18,3), vlf_active bit);
DECLARE @source varchar(20) = 'none', @err int = 0, @msg nvarchar(2048) = N'';

IF OBJECT_ID(N'sys.dm_db_log_info') IS NOT NULL
BEGIN
    BEGIN TRY
        INSERT INTO @vlf (file_id, vlf_size_mb, vlf_active)
        EXEC sys.sp_executesql
            N'SELECT li.file_id, li.vlf_size_mb, li.vlf_active
              FROM sys.dm_db_log_info(DB_ID()) AS li OPTION (RECOMPILE, MAXDOP 1)';
        SET @source = 'dm_db_log_info';
    END TRY
    BEGIN CATCH
        SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH
END
ELSE
BEGIN
    -- Eight columns first, seven on Msg 213. See "the column count is not
    -- knowable from here" below.
    BEGIN TRY
        INSERT INTO @dbcc8 EXEC ('DBCC LOGINFO WITH NO_INFOMSGS');
        SET @source = 'dbcc_loginfo_8';
    END TRY
    BEGIN CATCH
        SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH

    IF @err = 213
    BEGIN
        SELECT @err = 0, @msg = N'';
        BEGIN TRY
            INSERT INTO @dbcc7 EXEC ('DBCC LOGINFO WITH NO_INFOMSGS');
            SET @source = 'dbcc_loginfo_7';
        END TRY
        BEGIN CATCH
            SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
        END CATCH
    END

    INSERT INTO @vlf (file_id, vlf_size_mb, vlf_active)
    SELECT d.FileId, d.FileSize / 1048576.0, CASE WHEN d.Status = 2 THEN 1 ELSE 0 END
    FROM @dbcc8 AS d
    UNION ALL
    SELECT d.FileId, d.FileSize / 1048576.0, CASE WHEN d.Status = 2 THEN 1 ELSE 0 END
    FROM @dbcc7 AS d;
END

-- Then, unconditionally, the two declared result sets: the root object
-- carrying @source, @err and @msg beside the aggregates over @vlf, and the
-- vlf_per_file array. Both are emitted whatever happened above; that is the
-- whole point of staging. Note the wrapping: a comment line must never begin
-- with an @ word, or the header parser reads it as a directive.
```

The snippet in the first version of this section stopped at the `CATCH` and
emitted **no result set at all**, while its header declared two. That is not a
formatting slip — a reviewer ran it verbatim, as this document tells reviewers
to, and got zero result sets against a declared two. An artefact printed in a
specification is going to be run; it ends where the file ends.

### The column count is not knowable from here, so the file tries both

The first version asserted that `RecoveryUnitId` arrived in 2012, taking
`DBCC LOGINFO` from seven columns to eight. A reviewer contested it and placed
the column in 2016 SP2 instead. Neither can be settled: **Microsoft does not
document `DBCC LOGINFO` at all** — there is no page, which is why
`sys.dm_db_log_info` exists — and no 2012, 2014 or 2016 SP1 instance is
available here. The container is 2022 and returns eight.

If the reviewer is right, the consequence is severe and silent: the eight-column
declaration would mismatch on **every build where the DBCC branch runs**,
raise `Msg 213`, be caught, and record nothing — a collector dead on arrival on
exactly the versions it was written for, failing closed and saying so only in an
error number nobody reads.

So the file does not depend on the answer. It attempts the eight-column shape,
and on `Msg 213` — reproduced, it is precisely the shape-mismatch error — it
attempts the seven-column one. `@source` records which succeeded, which turns
the open question into a measurement the first real run answers for good.

Three further details, each one a review finding:

**`FileSize` is in bytes.** Measured: `DBCC LOGINFO` returns 253952 and 262144
for VLFs that `sys.dm_db_log_info` would report as 0.242 and 0.25 MB. Without
the division the archive is wrong by six orders of magnitude, and wrong in the
direction that looks like a catastrophic log.

**The shape is `023`'s, not a new one.** The first draft listed unprefixed
names and omitted one. `023` projects `[space.vlf_count]`,
`[space.vlf_active_count]`, `[space.vlf_inactive_count]`,
`[space.vlf_min_size_mb]`, `[space.vlf_avg_size_mb]`, `[space.vlf_max_size_mb]`,
`[space.vlf_under_1mb_count]` and `[space.log_file_count]`, plus a
`vlf_per_file` array. The `space.` prefix nests them in the JSON; an unprefixed
projection would have produced a different document while claiming to produce
the same one.

The `vlf_per_file` array names each log file, so `@vlf` stages
`logical_name` alongside `file_id` — from `sys.database_files`, which both
paths can join and which the first draft's staging table had no column for.

**Neither shape is nine columns.** The first draft's prose said nine, twice, in
the paragraph whose stated purpose was to stop the next reader getting it
wrong, while the T-SQL beside it declared eight. Anyone who "corrected" the
declaration to match the prose would produce exactly the `Msg 213` the
paragraph warns about — reproduced, both directions.

**The two mechanisms do not round alike, and the archive should not pretend
they do.** `DBCC LOGINFO` gives bytes, converted here to
`decimal(18,3)`; `sys.dm_db_log_info.vlf_size_mb` is a `float` and reports the
same VLF as 0.24 where the conversion gives 0.242. Per VLF the difference is
noise; across a log with tens of thousands of them the totals diverge
visibly, and an analysis comparing an archive from a 2014 instance with one
from a 2016 SP2 instance would see a difference that is arithmetic, not
fragmentation. `@source` is what lets the reader tell; it is not a defect to
fix but a fact to record.

**`@source` goes in the root object.** A reader must never have to infer which
mechanism produced a number, and an analysis that compares two archives must be
able to tell "no VLF count because the login was refused" from "no VLF count
because nothing ran". `@source`, `@err` and `@msg` say all three.

---

## 2. The operating system and the host

slug: os-and-host

### The gap

The archive says nothing about the operating system.
`10.system/020.host-services.sql` reads `sys.dm_server_services` and the
registry, which gives the service accounts and the startup parameters, not the
OS.

An audit needs the OS for three things it is routinely asked: whether the host
is still supported, whether a known storage or scheduler fix applies, and
whether the memory configuration makes sense against the physical machine. All
three were asked in September 2026 and the answer was "the collection does not
report it today".

### The axis is platform, not version

The first draft proposed splitting by version — `sys.dm_os_windows_info`
unconditionally, `sys.dm_os_host_info` behind a `min_version` sibling. Two
measurements broke that.

`sys.dm_os_host_info` does not carry the same columns. It has `host_platform`,
`host_distribution`, `host_release`, `host_service_pack_level`, `host_sku`,
`os_language_version`, `host_architecture` — every one prefixed `host_`.
Selecting `windows_release` from it fails with `Msg 207`. Two files projecting
"the same" fields would have projected two different key sets.

And `sys.dm_os_windows_info` on Linux does not fail. It returns one row of
`windows_release = ''`, `windows_service_pack_level = ''`, `windows_sku = NULL`,
`os_language_version = 0`. Microsoft documents its behaviour on a non-Windows
host as "undefined"; in practice undefined means a well-formed row of nothing,
which sits in the archive looking like a measurement. The file's own stated bar
is that a wrong value is worse than none.

### The collector

`10.system/021.host-info.sql`, `@scope: instance`,
`@permissions: CONNECT, VIEW SERVER STATE`, one result set.

One file, guarded the same way as section 1: `sys.dm_os_host_info` when
`OBJECT_ID` says it is there (2017 and later), `sys.dm_os_windows_info`
otherwise. Below 2017 there is no ambiguity to resolve — SQL Server on Linux
began with 2017, so an instance without `dm_os_host_info` is on Windows by
construction. The projection is one shape with a `platform` column that is
`Windows` by deduction on the old path and `host_platform` verbatim on the new
one.

**`host_release` is a number and it is not the marketing name.** `10.0` covers
Windows Server 2016, 2019, 2022 and 2025 alike, and Windows 10 and 11 with it.
The collector projects the raw value and **must not** map it to a product name:
the mapping needs the build number, which neither view exposes, and a wrong
product name in an audit is worse than a version number the reader has to look
up. `host_sku` is likewise projected as its integer.

That limitation is worth stating rather than hiding, because it means this
collector answers the third question (memory against the machine) and only
half of the first: it says which Windows *family*, not which release, so
supportability still needs the build number from somewhere else.

---

## 3. Transport, authentication scheme and encryption in transit

slug: transport-and-encryption

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
`@permissions: CONNECT, VIEW SERVER STATE`. All four columns verified present
and readable under that grant.

Aggregate, never per session. One row per
`(net_transport, auth_scheme, encrypt_option, protocol_version)` with a count,
plus the number of distinct `client_net_address` values in each group. A
per-session dump would carry client addresses and hostnames into the archive
for no analytical gain.

The count is of **addresses, not hosts**. `sys.dm_exec_connections` has
`client_net_address`; host names live in `sys.dm_exec_sessions` and reaching
them means a join the first draft did not mention, for a number that is less
reliable — a host name is client-supplied.

The collector's own session is in the result and cannot be excluded honestly.
It is marked rather than filtered, but the marking is a group property, not a
session one: written as `CASE WHEN session_id = @@SPID ...` beside a `GROUP BY`
it does not compile at all (`Msg 8120`, reproduced), and written as
`MAX(CASE WHEN session_id = @@SPID THEN 1 ELSE 0 END)` it says "this tuple
contains the collector". The column is therefore named
`contains_collector_session`, which is what it means.

### What the sessions do, and what the server demands

`encrypt_option` says what the sessions happen to be doing. It does not say
what the server requires, and the two answer different questions: a run where
every session shows `TRUE` may be a server forcing encryption, or a set of
clients that all happened to ask for it while the next one will not.

`ForceEncryption` lives in the instance's own registry hive and is readable
through `sys.dm_server_registry`, which `10.system/020.host-services.sql`
already reads for the startup parameters. The setting belongs beside
`encrypt_option`, and the pair is the finding: forced and encrypted is a
configuration, unforced and encrypted is a coincidence.

**This half is Windows-only, and the file must say so in its output.** Measured:
`sys.dm_server_registry` exists on Linux and returns **zero rows** — no error,
nothing. On Linux the setting lives in `mssql-conf`, which no DMV exposes. An
empty registry read is indistinguishable from "encryption is not forced" to a
reader who does not know the platform, which is the answer-that-looks-like-an-
answer this document objects to everywhere else. The projection carries a
`force_encryption` value and a separate `registry_readable` flag, and the
platform from section 2 is what makes the pair legible.

The certificate the instance presents is in the same hive and is worth the same
trip, with the same caveat. Note that the value stored is a SHA-1 thumbprint,
not the certificate: no expiry, no issuer. A self-signed certificate is the
default and is not a finding by itself, but it is what the reader asks about
next.

What is **not** reachable is the SCHANNEL configuration: whether TLS 1.0 and
1.1 are disabled at the OS level lives outside SQL Server's hive, and
`sys.dm_server_registry` does not expose it. That stays a question for the
client.

---

## 4. Execution plans when the Query Store is off

slug: plans-without-query-store

### What already exists

`80.workload/021.query-store-detail.sql` extracts execution plans from the
Query Store. It is gated on `@requires_flag: query_store_detail` and capped at
`@qs_top` queries selected by a round robin over four metrics — total duration,
CPU, reads and executions — so the archive gets the plans that matter by any of
four definitions of "matter", not by one.

That file is why a recent audit had 138 plans in the archive.

### The gap

**When the Query Store is off, the archive contains no plan at all.** The first
draft said "nothing reads `sys.dm_exec_query_plan`", which is not quite true —
`80.workload/030.implicit-conversions.sql` does, to LIKE-search the XML — but
nothing *keeps* a plan. On such an instance the analysis has aggregate counters
and no way to see a plan shape, which is the difference between "this procedure
is expensive" and "this procedure is expensive because a CTE is expanded five
times".

### The collector

`80.workload/041.plan-cache-plans.sql`, **`@scope: instance`**,
`@requires_flag: plan_cache_plans`, `@writer: plan-cache-plans`.

Instance scope, not database scope, and this is a correction. The plan cache is
an instance-wide structure: `sys.dm_exec_query_stats` has no `database_id`, and
filtering by database means `CROSS APPLY sys.dm_exec_plan_attributes` over the
whole cache. A database-scoped file would scan and sort the entire cache once
per database — fifty times on a fifty-database instance — and write largely the
same plans into each database's directory.

**Deduplication is on `plan_handle`, not `query_plan_hash`.** The first draft
had this backwards. `sys.dm_exec_query_stats` holds one row per *statement*,
and every statement of a procedure shares one `plan_handle` while carrying its
own `query_plan_hash`. Deduplicating on the hash selects all ten statements of
a ten-statement procedure, and `sys.dm_exec_query_plan(plan_handle)` returns
the plan for the whole procedure — so the same XML would be written ten times.
Statement-level plans need
`sys.dm_exec_text_query_plan(plan_handle, start_offset, end_offset)`, which
returns text rather than typed XML; whether that is worth the shape change is
the one design question left open here.

Three things the file must say about itself:

- **The cache is not history.** A plan absent from it was evicted or has not run
  since the last restart. `021` can say a query was not among the top N; this
  file cannot distinguish that from "never ran".
- **Cached plans carry no runtime statistics.** They are compiled plans, the
  same limitation as the Query Store plans, and the report must not read an
  operator cost as a measurement.
- **`sys.dm_exec_query_plan` returns NULL** rather than raising, for a plan too
  large or containing an XML-invalid construct. A NULL is written as a NULL and
  counted, never silently dropped.

### What this costs in Go, which the first draft did not say

`@requires_flag` and `@writer` are closed vocabularies (`KnownFlags`,
`KnownWriters` in `collect/queryset.go`); a file naming an unknown one fails
discovery lint. The flag needs a `BoolVar` and help text in
`cmd/sql-auditor/main.go`. The writer needs an implementation — the dispatch is
a switch in `collect.go`, and `021`'s writer is a substantial piece of code that
plans, deduplicates and writes directories. None of that is a `.sql` file.

There is also a disclosure question the first draft skipped, and it is a
defect rather than a documentation gap. Resolving text from the cache means
`sys.dm_exec_sql_text`, and `readsSessionText` matches that string on **any**
script whose flag is not `include_session_text`. So this collector would make
the manifest warn, on every run, that the archive holds "the SQL text of
statements running during collection" and that the file should have declared
`--include-session-text` — a warning that is both wrong about the provenance
and wrong about the flag.

Cached text may carry literal parameter values where Query Store text is
parameterised, and `observe-spec.md` treats those as materially different
disclosures. So this collector owes three things, none of them a `.sql` file: a
`CollectedKinds` entry of its own, a MANIFEST.txt paragraph describing
cache-resolved text, and a change to `readsSessionText` so that
`plan_cache_plans` is a recognised reader rather than an accident.

### What this deliberately does not do

**It does not compile a plan for a procedure that has none.** Obtaining an
estimated plan means `SET SHOWPLAN_XML ON` and executing the batch, which
requires parameter values the collector cannot invent, compiles on the
production instance, and would break the read-only promise. "Every plan we can
get" means "every plan already materialised in the Query Store or the cache".

**It does not collect all procedures.** A database of eight hundred procedures
whose plans run from 0.5 to 2 MB would produce a multi-gigabyte archive. The
cap is what makes the collector usable.

---

## 5. Is SQL Server alone on this machine?

slug: co-located-processes

### The gap, and what already answers half of it

An application server sharing a host with its database is a finding an audit
should make on its own, and today it is made by accident. In September 2026 it
surfaced only because the client mentioned it in passing.

**Part of the answer is already in the archive and nobody was reading it.**
`10.system/010.properties.sql` collects `total_physical_ram_mb`,
`available_physical_ram_mb`, `sql_ram_in_use_mb` and `sql_locked_pages_mb`.

### The arithmetic, and the two ways the first draft got it wrong

The subtraction the first draft proposed —
`total - available - sql_ram_in_use` — **goes negative**. Measured on an idle
instance with nothing else running: −3041 MB. Two independent causes. The three
counters are not sampled atomically, so small negatives are ordinary. And
`physical_memory_in_use_kb` is the process working set, which excludes locked
pages, so on an instance using Lock Pages in Memory the residue is overstated
by exactly the locked allocation — which `010` already collects and the first
draft did not use.

So the projection is

```
residue            = total - available - (sql_ram_in_use + sql_locked_pages)
other_processes_mb = CASE WHEN residue < 0 THEN 0 ELSE residue END
```

`MAX(0, x)` is not how the clamp is written, and the first version wrote it
that way: `MAX` in T-SQL is an aggregate taking one argument — measured,
`Msg 174` — and the scalar `GREATEST` arrived only in 2022, above the floor.
The clamp is a `CASE`.

The clamp does not rescue Linux, and the first version's word for it —
"underestimate" — was too kind. Measured on the container: total 12756,
available 11708, SQL in use 4178, residue **−3130**. The OS page cache counts
as available, so the residue is not merely low, it is negative on an idle
machine, and the clamp turns it into a flat zero on every Linux host. So the
projection carries `platform` beside it and the value is reported as
unavailable rather than as zero where the platform is not Windows. A derived
value that can be negative is not a finding, and a zero that means "we cannot
compute this here" is worse than no column.

`014.cpu-topology.sql` is the precedent for deriving at all: it projects the
hardware-versus-soft-NUMA answer rather than leaving it to be inferred. A fact
that is meaningless without an operation on its neighbours does not get to stay
implicit.

### What is genuinely missing: the CPU half

Memory says something else is resident. It does not say something else is
*running*. `RING_BUFFER_SCHEDULER_MONITOR` records carry a `<SystemHealth>`
element with `<ProcessUtilization>` — the CPU percentage used by the SQL Server
process — and `<SystemIdle>`. What is left over is everybody else.

**That formula is Windows-only, and unguarded it prints a false finding.**
Measured: on Linux every scheduler-monitor record carries
`ProcessUtilization = 0` and `SystemIdle = 0`, on all 256 of them, so
`100 - 0 - 0` reports that other processes are using the entire CPU, all the
time, on every Linux instance. The record shape exists, so the collector would
not error — it would lie.

`10.system/043.cpu-neighbours.sql` therefore projects the residue **only where
the host platform is Windows**, records `platform` beside it, and clamps at
zero (scheduling jitter can push the sum past 100). Elsewhere it reports the
records and their window without the derived percentage.

The guard is on the platform, never on the values, and the first version of
this section got that wrong in a way worth keeping as a warning. It said to
compute "only where both operands are not zero". On a Windows host running at
100% CPU — SQL Server at 35%, something else at 65% — `SystemIdle` is exactly
zero. The value-based guard would suppress the residue precisely on the
saturated machine where the neighbour is the finding. A guard that fails on the
interesting case is worse than no guard, because it looks like a measurement
that came back empty.

It inherits two constraints from `041.connectivity.sql`, both learned the hard
way there:

- **The buffer wraps at 256 records.** Confirmed on the test instance, which
  reached exactly 256. Measured span on a real instance: 4 hours 15 minutes.
  The window must be reported beside the numbers, because a short window *is*
  the finding when someone reads a quiet afternoon as a quiet server.
- **The `ms_ticks` arithmetic is done in seconds.** The columns themselves are
  `bigint`; what overflows is `DATEADD`'s increment argument, which is an `int`
  and gives up after 24 days of uptime — exactly the population worth auditing.
  The file's comment should blame `DATEADD`, not the column type.

### The third signal: who connects from the machine itself

Covered by collector 3 above. `net_transport = 'Shared memory'` means the
session originates on the server — **on Windows**. Shared Memory does not exist
on Linux, where every local session arrives over TCP loopback, so the test is
`net_transport = 'Shared memory' OR client_net_address IN ('127.0.0.1', '::1',
'<local>')`.

`program_name` from `sys.dm_exec_sessions` would say *which* application is
co-resident rather than only that one is. It is client-supplied and therefore
not evidence: empty for a default `SqlClient` connection, and it can say
anything. It corroborates, it does not prove.

**It goes in its own file, `10.system/046.local-sessions.sql`, not into
`042`.** Section 3 defines `042` as an aggregate over
`sys.dm_exec_connections` and explicitly refuses the join to
`sys.dm_exec_sessions`; adding `program_name` to it would change the grouping
key and contradict that section two pages later. A reviewer found the two
sections describing the same file incompatibly, which is what happens when a
collector is specified in two places.

The disclosure question is real but smaller than the first version implied.
`052.session-text.sql`'s invariant governs *statement text*, and the manifest's
disclosure is driven by `readsSessionText`, which matches `dm_exec_sql_text`
only. A client-supplied `program_name` does not flip it. One line in the file's
header saying the column is client-supplied and aggregated is the whole
reconciliation.

That addition changes what `042` carries, so it changes what the manifest
discloses. `052.session-text.sql` states an invariant about session-derived
text; adding a client-supplied string to an ungated file has to be reconciled
with it rather than slipped in.

### The honest limit, which belongs in the file

**Nothing inside SQL Server can enumerate the processes of its host.** There is
no read-only DMV for it, and the paths that exist — `xp_cmdshell`, a CLR
assembly, WMI through a linked server — all require permissions this collector
refuses to ask for and would break the read-only promise.

So the finding this makes possible is *"SQL Server is not alone on this
machine, here is how much memory and how much CPU it is not getting, and here
is what connects locally"*. It is not a process list. An audit that says
"18.5 GB is used by other processes" is reporting a measurement; one that names
the application is repeating what somebody said.

`sys.dm_server_services`, already collected by `020.host-services.sql`, closes
the last corner: it reports the SQL-family services on the host, so a Reporting
Services or Analysis Services instance sharing the machine is visible without
any of the above.

---

## 6. The default trace — who changed what, and when

slug: default-trace

### The gap

The default trace runs on every instance audited so far. It is on by default,
and `default trace enabled` was 1 on both instances of the September 2026
audit. Nothing in the corpus reads it.

It is the only free record of what happened to the instance: `sp_configure`
changes, database creation and deletion, file autogrowth and autoshrink events,
DDL on objects, changes to server role membership, login failures, and
full-text and backup errors. It retains five rolling 20 MB files, which on a
quiet instance is weeks and on a busy one is days.

The audit that raised this found a `sp_configure` with a pending reconfigure —
someone had changed a setting and never run `RECONFIGURE`. The report could say
what the state was, and had to write "it is better to look at what happened
before settling it", which is an instruction to the client to go and find out.
**The trace had the answer, with a timestamp.**

### The permission is the whole problem

`sys.traces` and `sys.fn_trace_gettable` require **ALTER TRACE**, not
`VIEW SERVER STATE`. Measured with a login holding exactly CONNECT and
`VIEW SERVER STATE`: `Msg 8189, You do not have permission to run 'SYS.TRACES'`,
and the same for `FN_TRACE_GETTABLE` even when the path is passed as a literal,
bypassing `sys.traces` entirely. Granting `ALTER TRACE` alone makes both work.

`ALTER TRACE` allows creating, modifying and stopping traces. It is the
Profiler permission, and it is exactly the class section 1 refuses to ask a
client for. The first draft declared `VIEW SERVER STATE` and predicted that a
failure would surface "at read time … reported as a skip"; neither is true. The
declared permission is granted, so the skip gate never fires, and the file
lands in `Errors` on every ordinary audit run.

The posture is the one section 1 now takes: **ask for nothing, try, and record
the refusal.** The collector attempts the read and writes `collected = 0` with
`Msg 8189` when it cannot. Where a DBA runs the tool themselves, or the client
grants it deliberately, the archive gets the trace; otherwise it gets a row
saying why not. `docs/dba-guide.md` describes what is thinner without it and
does not ask for it.

### The collector

Two files, because one cannot be both gated and ungated.

`10.system/044.default-trace.sql`, `@scope: instance`, no flag: the aggregate.
One row per `(EventClass, ObjectType)` with a count and the first and last
timestamp, plus the span in the root object. No text.

For the autogrow classes — 92 and 93 — the aggregate also carries `Duration`
and `IntegerData`, summed and at their maximum. A count of growth events
without their duration throws away the finding: log autogrowth cannot use
instant file initialisation, so it is unbuffered, and a single 40-second growth
stalls every writer on the database. "The log grew 180 times" is a curiosity;
"the log grew 180 times and the slowest took 41 seconds" is the report.

`10.system/045.default-trace-detail.sql`, `@requires_flag: default_trace`: the
retained rows for the event classes that carry a decision. `@requires_flag`
gates the whole file (`skipReason`), so the first draft's "the aggregate half
runs without it" was not buildable as one file.

**That flag is not free, and this section owes what section 4 pays.**
`KnownFlags` in `collect/queryset.go` is a closed map; `default_trace` is not
in it, so the file fails discovery lint and the binary refuses to start until
the entry exists, along with a `BoolVar` and help text in
`cmd/sql-auditor/main.go` for `--include-default-trace`. Section 4 states this
cost for `plan_cache_plans`; section 6 omitted it entirely, which is the kind
of asymmetry that makes an estimate wrong by a day.

**Both files read `sys.traces` through dynamic SQL into a table variable, and
that is not decoration.** Measured: under a login without `ALTER TRACE`, a
plain `SELECT id FROM sys.traces` inside a `TRY` emits the column metadata as
an **empty result set** before raising `Msg 8189`, so the `CATCH` fires, the
handler's own set follows, and the unit returns one result set more than it
declared. `sys.traces` is a table-valued function and the engine sends its
shape before it evaluates the permission. Staging it the way every other guard
here works suppresses the phantom set.

**The event-class list was wrong and is corrected.** Measured against
`sys.trace_events`:

| Class | What it actually is |
| --- | --- |
| 20 | Audit Login Failed — the dormant-account argument needs this one |
| 22 | ErrorLog — **and this is where `sp_configure` changes arrive** |
| 46, 47 | Object:Created, Object:Deleted |
| 92, 93 | Data / Log File Auto Grow |
| 94, 95 | Data / Log File Auto Shrink |
| 104 | Audit Addlogin — login creation, not role membership |
| 105 | Audit Login GDR — grant/deny/revoke |
| 108 | Audit Add Login to Server Role — the one the first draft wanted |
| 115 | Backup/Restore |
| 116 | Audit DBCC |
| 164 | Object:Altered |

There is **no `sp_configure` event class**. The first draft's "152
`sp_configure`" names `Audit Change Database Owner`; a collector built from
that list would have labelled database-owner changes as configuration changes,
in an archive a client acts on. The real record is a class-22 row whose text
reads "Configuration option 'show advanced options' changed from 0 to 1. Run
the RECONFIGURE statement to install." — read live on the test instance.

**The window is the finding as often as the content is.** A trace whose oldest
record is four hours old, on a server up for eighty days, says the instance
generates events fast enough to roll 100 MB in an afternoon. The span goes in
the root object beside the counts, the same discipline `041.connectivity.sql`
applies to its ring buffers.

Three cautions belong in the files:

- **The path may not exist.** With `default trace enabled = 0`,
  `sys.traces WHERE is_default = 1` returns no rows, and
  `fn_trace_gettable(NULL, DEFAULT)` raises `Msg 19050` rather than returning
  nothing. The path is tested before it is used.
- **`DEFAULT` reads the rollover set** — verified, 264 rows across files — but
  `sys.traces.path` names the *current* file, so the set read is the one from
  that file forward. The span reported is the span actually read, not the span
  the five files hold.
- **Absence proves nothing.** The files rolled. The report must not read an
  empty autogrow count as "the files never grew".

---

## 7. Enterprise features persisted in a database

slug: persisted-sku-features

### The gap, corrected

`sys.dm_db_persisted_sku_features` reports the Enterprise-only features
physically present in a database: partitioning, data compression, online index
rebuild artefacts, change data capture, transparent encryption,
memory-optimized tables. One row per feature, per database, and it costs
nothing.

Nothing in the corpus reads it. But the first draft's motivating claim is out
of date, and it matters because it would have produced a false finding.

**"A backup taken on Enterprise restores onto Standard only if this view is
empty" has not been true since SQL Server 2016 SP1.** That release moved
compression, partitioning, change data capture and In-Memory OLTP into
Standard. The view still lists them — measured: a table created with
`DATA_COMPRESSION = PAGE` puts a `Compression` row there on a Developer
instance — but the database restores onto Standard and the feature works.
Written as the first draft had it, an audit would have reported a defect
against healthy Standard instances. The instances that motivated the section
were 2016 SP1.

What the view still answers, and what the collector is for:

- **Which Enterprise-era features are physically present**, which is a
  migration and licensing conversation rather than a defect.
- **The genuinely edition-bound ones** — transparent data encryption before
  2019, and the features that remain Enterprise-only — where the restore
  question is still real.

The finding is therefore "this database carries these features; here is which
of them constrain the target edition", and the edition boundary belongs to the
analysis layer with the target version in hand, not to the collector.

### The collector

`20.databases/026.persisted-sku-features.sql`, `@scope: database`,
`@permissions: CONNECT, VIEW SERVER STATE`, `@resultsets: root:object,
features:array`.

**The permission is `VIEW SERVER STATE`, not `VIEW ANY DEFINITION`.** Measured:
a login holding exactly CONNECT and `VIEW ANY DEFINITION` gets `Msg 262,
VIEW DATABASE PERFORMANCE STATE permission denied`, then `Msg 297`. This is a
dynamic management view, not a catalog view; `VIEW ANY DEFINITION` governs
metadata visibility and does not imply it. `VIEW SERVER STATE` carries
`VIEW DATABASE STATE` into every database, which `grants.go` already notes, and
the read then succeeds.

**Two result sets, not one.** The first draft asked for "one result set, one
row per feature, plus a count in the root object", which the encoder cannot
express: a `root:object` set must return at most one row, and an array set
cannot merge a value into the root. The count that distinguishes "nothing
persisted" from "the collector did not run" lives in the root object, and the
features are the array.

The view is empty on a database that never carried such a feature, so **an empty
result is the answer and not a failure.** That has to be said in the file,
because every other array in the corpus is empty only when something went
wrong.

---

## 8. Two views for builds we do not audit yet — implemented

Everything above is a specification. These two are **written and in the
corpus**: they cost two files, they are gated on the build, and writing them
now means the first 2022 or 2025 instance this practice audits arrives with the
collector already there instead of producing an archive that has to be
explained.

The version gate is not a formality. Both views are absent on every instance
audited so far, and a collector that referenced them ungated would fail the
whole script on a batch-level error. With `@min_version` the file is skipped
and the skip is recorded, so the archive says "not applicable on this build"
rather than being silently short of a file.

Both files were run or parsed during the review: `073` executes verbatim on
2022 under a `VIEW SERVER STATE` login, `074`'s view is absent there
(`Msg 208`) which is what makes its gate load-bearing, and both pass the corpus
lint and `go test ./...`.

### `10.system/073.accelerators.sql` — `@min_version: 16`

`sys.dm_server_accelerator_status`, SQL Server 2022 and later. Intel QuickAssist
offload for backup compression.

The finding is not whether the feature exists. Three outcomes look identical
from outside — never enabled, enabled and running in hardware, or enabled and
quietly running in **software** because the card is absent, the driver failed to
load, or the edition does not allow it. Only the third is a defect, and it is
invisible everywhere else: the backups keep working, a little slower, on the CPU
that was supposed to have been freed.

`mode_reason_desc` is what separates them and it is projected verbatim. Its
values name the cause — `SOFTWARE_MODE_ACCELERATOR_HARDWARE_NOT_FOUND` is a
broken deployment, `SOFTWARE_MODE_NON_ENTERPRISE_SKU` is a licensing decision,
`NONE_HARDWARE_OFFLOAD_NOT_ENABLED` is the untouched default. The review turned
up a fourth this document had not seen,
`NONE_HARDWARE_OFFLOAD_LINUX_NOT_SUPPORTED`, which is the argument for
projecting verbatim making itself: collapsing these to a verdict would have
thrown away a value nobody knew to enumerate.

One property worth knowing when reading the output: **the view is never empty on
a supported build.** A row for QAT is present from 2022 onwards whether or not
the hardware exists and whether or not the driver is installed. So an empty
array is a collection failure, not an answer, and the count in the root object
is what makes the two distinguishable.

The gate is the bare major `16`: the view arrived with 2022 RTM, so unlike
`023.log-vlf.sql` there is no cumulative-update floor to respect.

### `10.system/074.memory-health.sql` — `@min_version: 17`

`sys.dm_os_memory_health_history`, SQL Server 2025 and later.

Every other memory reading in the archive is an instant. `015.buffer-pool.sql`
and `010.properties.sql` say what memory looked like at the moment of
collection, and a collection is one moment. An instance that spends five minutes
an hour unable to satisfy allocations, and is comfortable the rest of the time,
reads as comfortable — which is the reading that sends an audit looking
somewhere else entirely. This view is the engine keeping that history itself: a
snapshot every fifteen seconds with its own severity verdict, the memory it
could still hand out, and the memory it could reclaim by shrinking caches.

**The window is 256 snapshots, which is one hour and four minutes, and a restart
resets it.** So it is excellent evidence of a problem and no evidence at all of
its absence. The span goes in the root object beside the counts.

Two projection decisions are worth recording because both are about what *not*
to collect.

`top_memory_clerks` is a JSON document of up to 4000 characters on every one of
the 256 rows. Projecting all of them would put a megabyte of near-identical JSON
into the archive to answer a question that has one interesting instant. So the
series carries the numbers without the JSON, and the clerks are expanded once,
for the snapshot with the highest severity — ties broken by the least allocation
potential, then by the most recent.

Three columns are documented as "identified for informational purposes only, not
supported, future compatibility is not guaranteed": `out_of_memory_event_count`,
`memgrant_timeout_count` and `memgrant_waiter_count`. They are not read. A column
Microsoft declines to stand behind has no place in a document a client acts on,
and `80.workload/010.wait-stats.sql` already carries the supported reading of
memory grant pressure.

### What both files taught about the corpus lint

Two things the test suite caught that are worth knowing before writing the next
collector.

A root-object column prefix may not collide with the name of a result set: the
first draft projected `[accelerators.reported]` beside an `accelerators` array,
and the encoder refuses that rather than writing a document with two meanings
for one key. The prefix became `offload`.

And a directive name written in the prose of a header is parsed as a directive.
The mechanism is narrower than this document first described it: the parser
matches any header comment line beginning `-- @word` and, for a name it knows,
takes **the rest of that same line** as the value. It does not read the next
line. Explaining in a comment which permission a directive declares therefore
breaks the header only if the sentence starts with the directive name. The
lesson stands — do not open a header line with `@`— but the next author should
test the right thing.

### And what they broke

Both files declare versions the corpus's own grammar checker did not know.
`tools/verify-corpus-grammar.ps1` mapped `@min_version` to parsers 11 through
15 and threw on anything else, so from the moment `073` landed the run died on
it — in alphabetical order, which left every file after `072` unchecked, and
the exception looked like a broken tool rather than an unverified corpus. Fixed
separately: 16 and 17 are mapped, an unmapped version is now a `NOT CHECKED`
line rather than an abort, and the committed artefact — which had described 38
files and a three-week-old corpus tree — describes all 58.

---

## 9. Procedure execution frequency — already collected, do not build again

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

---

## 10. The other ring buffers

slug: other-ring-buffers

`10.system/041.connectivity.sql` already emits one row per
`ring_buffer_type` — count, oldest, newest, span. So the archive already says
which buffers exist and how fast each one turns over. The only question left is
which ones are worth **decoding**, and the rule is:

> Decode a buffer when its record shape has been stable across the supported
> range and it answers a question no other collector answers. Everything else
> is covered by the summary row, which costs nothing and lets an analyst ask
> for more when a number looks wrong.

The first version of this rule said "when Microsoft documents its record
shape", and a reviewer pointed out that the rule then forbids everything it
goes on to propose: Microsoft documents `sys.dm_os_ring_buffers` as
*"identified for informational purposes only, not supported, future
compatibility is not guaranteed"*, and publishes the record schema of **no**
buffer — not `RESOURCE_MONITOR`, not `SECURITY_ERROR`, not the ones refused
below. A criterion that excludes its own conclusions is not a criterion.

So the honest rule is the one above, and it comes with an obligation: what is
decoded here is unsupported, its shape is asserted from observation, and every
projection must survive an element that is missing or renamed — an XPath that
returns NULL rather than a query that fails. The files say so in their headers.

**File numbers.** These take `047` and `048` in `10.system`. The first version
gave the first of them `045`, which section 6 had already taken — two files in
one document claiming one number, in a corpus where nothing checks for that.

Fourteen types exist on a bare 2022 instance. Two are worth decoding, one is
worth an aggregate, and the rest are not.

### `10.system/047.resource-pressure.sql` — `RING_BUFFER_RESOURCE_MONITOR`

The strongest of them. Its records carry a `<Notification>` —
`RESOURCE_MEMPHYSICAL_LOW`, `RESOURCE_MEMPHYSICAL_HIGH`,
`RESOURCE_MEMVIRTUAL_LOW` — with `<IndicatorsProcess>`, `<IndicatorsSystem>`
and `<IndicatorsPool>`, and the node, which is `<NodeId>` and not `<Node>` as
the first version wrote it. An XPath on the wrong name returns NULL rather than
failing, so this is the class of error that ships and produces a column of
nulls nobody questions.

This is the historical record of memory pressure, and every other memory
reading in the archive is an instant. Section 8 makes exactly that argument for
`074.memory-health.sql`, but that view is 2025 and later; this buffer covers
the whole supported range. One record on a healthy idle instance, which is
itself the reading: an instance stacking `LOW` notifications is under pressure,
and nothing else in the archive says so.

Aggregate by notification and node, with counts and the first and last
timestamp, plus the buffer's span. `@scope: instance`,
`@permissions: CONNECT, VIEW SERVER STATE`.

### `10.system/048.security-errors.sql` — `RING_BUFFER_SECURITY_ERROR`

Records carry `<SPID>`, `<APIName>`, `<CallingAPIName>`, `<ErrorCode>`,
`<SQLErrorCode>` and `<SQLErrorState>` — the security API talking: SSPI
negotiation failures, Kerberos problems, impersonation refused.

**`ErrorCode` is a hexadecimal string, not a number.** A live record carries
`0x139F`, and `CAST('0x139F' AS int)` fails with `Msg 245` — measured. It is
projected as `varchar(32)` verbatim; a reader who wants the decimal can convert
through `varbinary`, and the archive keeps the form the engine wrote, which is
also the form every search engine will match.

It is not the same thing as a failed login, and that is why it is worth having.
`040.error-log.sql` counts 18456s, which says an authentication attempt failed;
this buffer says whether the cause was a wrong password or a broken SPN. Read
beside the `auth_scheme` distribution from section 3 — NTLM where Kerberos was
expected is the symptom, these error codes are the cause.

Aggregate by `APIName` and `ErrorCode` with counts and the window. No per-SPID
dump: the records carry a session id and no user name, and the aggregate
answers the question.

### `RING_BUFFER_EXCEPTION` — an aggregate, folded into `041`

459 records on an instance doing nothing. Its `<Exception>` node carries
`<Task>`, `<Error>`, `<Severity>`, `<State>`, `<UserDefined>` and `<Origin>`,
followed by a `<Stack>` — and **no `<SPID>`**, which the first version claimed
by confusing it with the security buffer above. It survives a cycle of the
error log. An aggregate by error number and severity, with counts and the
window, is cheap and would say "this instance throws three thousand 8134s an
hour", which the error log's filtered read can miss. A dump would be noise, and
the `<Stack>` is never projected.

### The refusals, and why

`RING_BUFFER_SCHEDULER` (5257 records), `RING_BUFFER_HOBT_SCHEMAMGR` (791),
`RING_BUFFER_QE_MEM_BUFF_POOL_RESERVE`, both `RING_BUFFER_MEMORY_BROKER*`,
`RING_BUFFER_SECURITY_CACHE` and `RING_BUFFER_CLRHOSTTASK` are undocumented
internals, high in volume, and their shape changes between builds. An audit
tool that publishes undocumented internals invites a client question nobody can
answer.

`RING_BUFFER_XE_LOG` and `RING_BUFFER_XE_BUFFER_STATE` are Extended Events
plumbing. They matter when diagnosing why an XE session drops events, which is
`observe`'s territory, not `collect`'s.

### Two inherited traps

Both were solved in `041.connectivity.sql` and both must be reused rather than
rediscovered: the `ms_ticks` arithmetic is done in seconds, because `DATEADD`'s
increment is an `int` and gives up after 24 days of uptime; and the buffer's
window is reported beside its numbers, because a short window *is* the finding
when someone reads a quiet afternoon as a quiet server.

---

## 10 bis. One gap the review named that this document does not close

slug: review-unclosed-gap

**The missing-index DMVs.** `sys.dm_db_missing_index_group_stats` and its
siblings are absent from the corpus and from this specification, and a reviewer
was right to notice: aggregating them is a fixture of every performance audit,
and the tool collects index *usage* and *operational* statistics without ever
saying which index the optimiser wished for.

It is not added here, and the reason is the one the query-plan discipline
already states elsewhere in this practice: the missing-index DMVs are a
suggestion engine, not a measurement. Their `improvement_measure` is a
heuristic, they propose overlapping indexes, they ignore write cost, and their
DDL is famously not to be pasted. Collecting them is easy; collecting them
without inviting a report that pastes them is the design question, and it wants
its own section rather than a line here.

The honest position: it is a real gap, it is deliberate, and it is the next one
to write.

## 11. Two things the review found in the repository — both now fixed

Neither belonged to this specification's work. Both are recorded because this
document argued from them, and both changed under it while it was being
reviewed.

**An unknown directive was silently ignored.** `parseScript` switched on
directive names with no default case, so `-- @minversion:` or any other
misspelling produced no lint error and no effect — the file shipped ungated
with nothing anywhere saying so. It is now a lint error. Writing the test first
turned up three header lines in the shipped corpus that opened with an `@` word
in prose and had been parsed as directives all along; all three are reworded,
and the rule for the next author is that a header line never begins with an
`@` word, even in prose.

Section 1 still declines to introduce `@max_version` — the self-guarding file
is smaller for other reasons — but the argument that a new directive is
dangerous to ship no longer holds, and that is worth knowing before the next
one is proposed.

**The manifest's promise was inexact and is reworded.** It used to say the
collector "runs no INSERT, UPDATE, DELETE or DDL", which
`040.error-log.sql` and `041.compression-savings.sql` had already contradicted
by capturing `sp_readerrorlog` and `sp_estimate_data_compression_savings`
through `INSERT ... EXEC` — and which section 1's redesigned `023` would have
contradicted a third time. A reviewer found it by reading this document beside
the replication specification, which quotes the same promise as a binding
constraint while building its guard pattern on an INSERT.

The sentence now promises what the reader actually needs: only SELECT against
catalog and dynamic management views, no user or application table, the few
command-only diagnostics captured into scratch storage in tempdb, and **no
permanent object created, altered or deleted**. That is true of the corpus,
true of everything specified here, and it keeps the line that makes `observe` a
separate command — `CREATE EVENT SESSION` creates a permanent object.

---

## 12. The foreign key graph

slug: foreign-key-graph

**Status: not collected.** `70.schema/010.objects.sql` reads `sys.foreign_keys`,
but only to count the constraints the optimizer no longer trusts — those with
`is_not_trusted = 1` or `is_disabled = 1`. The graph itself, which table
references which and through which columns, is nowhere in an archive.

Two analyses need it and neither can be done today.

A missing foreign key is a data-model finding in its own right, and the first
one an auditor reaches for on a schema built by an application that enforces
its own integrity. Without the graph, the finding cannot be made from an
archive at all; it has to be asked for by hand, every time.

And a purge routine that rediscovers the reference graph on every pass is a
recurring, expensive pattern. Reading one from `INFORMATION_SCHEMA` per table
per call costs more than the deletions it exists to order. Recognising that
shape from an archive needs the graph the routine is rebuilding.

What a collector would project: the constraint, its parent and referenced
table, the ordered column pairs, whether it is trusted, whether it cascades,
and whether an index covers the referencing columns — that last one is what
turns the graph into an indexing finding as well.

Scope is `database`, and the cost is a catalog read: `sys.foreign_keys` joined
to `sys.foreign_key_columns`, bounded by the same 200-table cap the rest of
`70.schema` uses so one archive does not carry ten thousand rows nobody reads.
