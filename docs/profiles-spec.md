# Collection profiles: specification

Status: draft, not implemented. Written 14 September 2026.

## What it is for

`collect` answers every question the corpus knows how to ask, in one run. Some
engagements ask only one question. The first one to arrive was: which databases
on this instance can be made smaller, by how much, and in what order, without
buying disk. It came from a client instance in September 2026 whose data volume
had raised an alert, where the answer turned out to be one nonclustered index
nobody used and a compression pass, and where the rebuilds already under way
would have made the alert worse.

Everything that answer needed was already in the corpus, with two exceptions
covered below. What was missing is a way to run only that part: a shorter run, a
smaller archive, fewer permissions to request, and a manifest a client's
security officer can approve because it describes less.

A profile is that. `--profile space` runs the collectors that answer the space
question and nothing else, and the archive says which profile produced it.

## What a profile is, and what it is not

A profile is a named, closed subset of the corpus. It is declared by the
collectors themselves, in their header, the way `@requires_flag` is.

It only ever removes collectors from a run. It never adds one: a collector
behind `@requires_flag` still needs its flag, whether or not it belongs to the
profile. Profiles and flags are independent decisions, and a run is the
intersection of the two.

It is not a new command. `observe` is a separate command because it breaks the
read-only promise `MANIFEST.txt` makes (see `docs/observe-spec.md`). A profile
cannot break that promise: every collector it keeps is one the default run
already executes, under the same manifest paragraph. What changes is how much of
the corpus ran, and the manifest says so.

It is not a filter by path. `--only 70.schema/*` was considered and rejected: an
operator running the tool alone would have to know the layout of the corpus,
and the archive would record a pattern rather than an intent, so the analysis
layer could not tell a space archive from a run somebody narrowed by hand.

## The directive

```
-- @profiles:    space
```

A comma-separated list of profile names, in the header, parsed like
`@discloses`. The vocabulary is closed:

```go
// Profile describes one named subset of the corpus. Description is what
// check, MANIFEST.txt and the wizard print beside the name.
type Profile struct {
	Description string
}

var KnownProfiles = map[string]Profile{
	"space": {Description: "what makes the databases on this instance larger " +
		"than they need to be: index usage and size, compression, page fullness, " +
		"files, logs and tempdb"},
}
```

`Script` gains `Profiles []string`, and `profiles` joins `knownDirectives`.

Two lint errors, for the reason every closed vocabulary here is closed, since a
misspelt name would silently leave the collector out of every profiled run:

- `@profiles: unknown value "spaec"; expected one of space`
- `@profiles: no profile named`, for a directive with an empty value.

A lint error anywhere in the corpus keeps its present meaning whatever the
profile. A corpus from `--queries-dir` that fails lint outside the profile is
still a broken corpus, and the run still reports it.

## Selecting the plan

`skipReason` and `planScripts` take the profile, and the profile gate is
evaluated first:

```go
func skipReason(s Script, profile string, denied map[string]bool,
	serverVersion []int, enabled map[string]bool) (string, bool) {
	if profile != "" && !slices.Contains(s.Profiles, profile) {
		return ProfileSkipReason(profile), true
	}
	// ... the flag, version and permission gates, unchanged and in the same order
}

// ProfileSkipReason is the one place the sentence is built, so MANIFEST.txt
// can recognise these skips by equality rather than by searching the text.
func ProfileSkipReason(profile string) string {
	return "not in profile " + profile
}

func planScripts(scripts []Script, profile string, denied map[string]bool,
	serverVersion []int, enabled map[string]bool) []plannedScript
```

First, because the comment above `skipReason` already orders the reasons with
the operator's own choice ahead of the server's version and the login's rights,
and choosing a profile is the operator's choice. A collector outside the profile
that is also gated on a flag reports the profile, not the flag: telling the
operator to pass `--estimate-compression` for a collector the profile excluded
would send them to an option that changes nothing.

`Options` gains `Profile string`. Both callers of `planScripts`,
`collect/collect.go` in `Run` and `collect/verify.go` in `VerifyServer`, pass
it. `VerifyResult.Collectors` is therefore the count under the profile, and so
is the wizard's "Always collected" figure.

## The command line

`--profile NAME` on `check` and `collect`. There is no `.env` key: the setting
belongs to the run being prepared, not to the machine, and a key would make an
unattended run quietly narrower than the operator believed.

Three refusals, all exit 2, all before the instance is touched:

| Refused | Message | Where |
| --- | --- | --- |
| a name outside `KnownProfiles` | `unknown profile "spaec"; expected one of space` | `CheckProfile` |
| `--all` with `--profile` | `--all and --profile cannot be combined: --all asks for the widest archive this tool can produce, and a profile for a narrow one` | `buildOptions` in `cmd/sql-auditor/main.go` |
| a flag with no collector in the profile | `--query-store-detail has no collector in profile space, so it would collect nothing; drop the option or the profile` | `CheckProfile` |

```go
// CheckProfile refuses the profile and flag combinations that would produce a
// run which looks successful and collects nothing the operator asked for.
// scripts is the corpus as discovered; a script that failed lint does not
// count as a member.
func CheckProfile(scripts []Script, profile string, flags map[string]bool) error
```

The third refusal exists because the failure it prevents is silent. Without it,
`collect --profile space --query-store-detail` finishes with exit 0, and the
operator believes the archive holds plans it does not hold.

`--all` is refused in `buildOptions` because it needs no corpus to be judged.
`CheckProfile` needs the corpus, so it runs where the corpus is first known:
in `Run`, immediately after `Discover` and before `Open`, and in `VerifyLocal`,
before the `Queries` listing is printed. A malformed command line has always
been refused before anything else happens, and this keeps that true.

## What the archive says

### `_run.json`

`Manifest` gains a block:

```go
type ProfileBlock struct {
	// Name is empty when no profile was requested: the whole corpus ran.
	Name string `json:"name"`
	// Collectors is how many scripts of the corpus belong to the profile, and
	// Corpus how many the corpus holds, lint failures excluded from both. The
	// pair says how much of the corpus the archive stands for.
	Collectors int `json:"collectors"`
	Corpus     int `json:"corpus"`
}
```

serialised as `"profile": {"name": "space", "collectors": 21, "corpus": 84}`. It
is written on every run, with `"name": ""` when there is no profile, for the
reason the transport block is: an absent key and a key saying "none" must not be
told apart by guesswork.

`skipped_scripts` keeps one entry per collector left out by the profile, with
`ProfileSkipReason` as the reason. The analysis layer reads that list to tell
"not collected because of the profile" from "not collected because a permission
was refused", and it must stay complete.

### `MANIFEST.txt`

One header line, printed unconditionally, after `Contents`:

```
Profile      : none, the whole corpus ran
Profile      : space, 21 of 84 collectors (what makes the databases on this instance larger than they need to be: ...)
```

And in "Queries not run", the entries whose reason equals
`ProfileSkipReason(m.Profile.Name)` are collapsed into one line placed first:

```
Queries not run (64):
  - 63 collectors outside profile space, each listed in _run.json
  - 70.schema/041.compression-savings.sql
      not collected by default; pass --estimate-compression to include it
```

The count in the heading stays the count of entries in `skipped_scripts`. The
document a security officer reads has to stay readable; 63 identical lines
bury the two skips that carry information.

This changes the text of every manifest, profiled or not.
`collect/manifest_test.go` and `collect/collect_test.go` assert on `Human()`
and move with it.

### The run folder

```go
func RunFolderName(server, profile string, t time.Time) string
func RunFolderFor(outputDir, server, profile string, now time.Time, keep bool) string
```

With a profile the name is `<server>-<date>-<profile>`, for example
`SQL01_PROD-2026-09-14-space`; without one it is unchanged. The `--keep` time
suffix is appended after it. The callers are `collect/collect.go` (twice),
`tui/state.go`, and the tests in `collect/output_test.go`,
`collect/collect_test.go` and `tui/state_test.go`.

A consequence worth having: a full run and a space run on the same day no
longer collide, so neither replaces the other.

## `check`

The listing prints the profile before the queries and lists the members only:

```
Profile: space, 21 of 84 collectors
Queries (21):
  10.system/010.properties.sql               ...
  ...
Not in profile space: 63 collectors. Run check without --profile to list them.
```

Lint failures outside the profile are still listed, with the `!!` prefix, for
the reason given under "The directive".

A capability probed as denied that no member of the profile declares in
`@permissions` is annotated `(not needed by profile space)`. Its status is
unchanged, so `PreflightExitCode`, which does not read `denied`, is unaffected.

### The grant script

`GrantScriptInput` gains `Profile string`. When it is set,
`BuildGrantScript` restricts `in.Scripts` to the members of the profile and
removes from `denied` every capability none of them declares, before any
section is built. Those capabilities are named in the header under one line:
`Denied, and not needed by profile space: <names>`. Without a profile nothing
changes, including for capabilities no collector declares.

Both callers pass it: `writeGrantScript` in `collect/collect.go` and the `[g]`
key in `tui/grants.go`. A grant script for a space run asks for what a space run
reads, which is the argument that gets a cautious client to run it.

## The wizard

Screen 3, "What to collect", gains a first row above the options:

```
  Profile        [p]  none, the whole corpus
                      space: what makes the databases on this instance larger ...
```

- `State` gains `Profile string`; `[p]` cycles through `""` and every key of
  `KnownProfiles` in sorted order, then back to `""`.
- The option rows shown are the flags of `flagOrder` that have at least one
  member in the profile, or all of them when there is no profile. `FlagIndex`
  is clamped to the rows shown.
- Changing the profile turns off every flag whose row it hides. A combination
  `CheckProfile` refuses can therefore never be started from the wizard.
- The collector count is recomputed from the plan whenever the profile or a
  flag changes, through an exported function over what the verification step
  already holds:

  ```go
  // PlannedCollectors is VerifyResult.Collectors for another profile or
  // another set of flags. It is zero when v.Probed is false.
  func PlannedCollectors(v VerifyResult, profile string, flags map[string]bool) int
  ```

- With a profile, the sentence reads `Profile space: N collectors on this
  instance, computed from the resolved plan.` and the sentence introducing the
  options describes the rows actually shown.
- `applyState` copies `Profile` into `Options`; the collision probe in
  `tui/state.go` and the `[g]` key pass it on.

The wizard has no command line, so this row is the only way to choose a profile
there.

## The `space` profile

### Members

21 collectors: 20 that exist today and one added by this specification.

| Collector | What the space question reads in it |
| --- | --- |
| `10.system/010.properties.sql` | edition, which decides whether a rebuild can be online; uptime, which bounds what "never used" can mean |
| `10.system/030.file-io.sql` | free space on the volumes, size of every file |
| `10.system/050.tempdb.sql` | tempdb size and what occupies it, which a rebuild with `SORT_IN_TEMPDB` spends |
| `10.system/051.version-store.sql` | the version store, which grows tempdb |
| `20.databases/010.all-databases.sql` | recovery model, `log_reuse_wait`, last log backup, `auto_shrink`, data and log size |
| `20.databases/020.properties.sql` | allocated and used space per file, autogrowth, largest objects |
| `20.databases/022.query-store.sql` | current and maximum Query Store storage |
| `20.databases/023.log-vlf.sql` | VLF layout |
| `20.databases/024.log-stats.sql` | what holds the log back from truncating, and how much of it is active |
| `50.agent/010.jobs.sql` | the jobs a scheduled shrink runs in |
| `50.agent/020.job-steps.sql` | the step text, the only place a `DBCC SHRINKFILE` in a T-SQL job shows |
| `50.agent/040.maintenance-plans.sql` | shrink tasks in maintenance plans |
| `60.backup/010.history.sql` | how far back msdb history goes, log backup cadence |
| `70.schema/010.objects.sql` | table sizes, data and indexes apart, creation and modification dates |
| `70.schema/020.index-usage.sql` | the size and usage counters of every index, uncapped |
| `70.schema/040.compression.sql` | what is compressed today |
| `70.schema/041.compression-savings.sql` | estimated savings, still only with `--estimate-compression` |
| `70.schema/050.heaps.sql` | page fullness and forwarded records on the 50 largest heaps |
| `70.schema/055.page-density.sql` | new: page fullness on the 50 largest rowstore indexes |
| `70.schema/060.columns.sql` | declared types, LOB columns |
| `70.schema/070.index-columns.sql` | keys and included columns, fill factor, disabled indexes |

`50.agent/020.job-steps.sql` carries `@discloses: job_step_text`. It stays in
the profile, and the manifest discloses it exactly as it does for a full run.

### Left out, and why

| Collector | Why |
| --- | --- |
| `70.schema/030.index-operational.sql` | the write profile the space question needs is `user_updates` in `020.index-usage` |
| `70.schema/090.statistics.sql`, `091.statistics-density.sql` | statistics occupy negligible space |
| `80.workload/*` | about time spent, not space occupied |
| `10.system/020.host-services.sql` | instant file initialisation decides how fast a file grows, not how large it is |
| `10.system/040.error-log.sql` | discloses the error log for one marginal question, the size of the log file itself |
| `20.databases/011.all-databases-2014.sql`, `012.all-databases-query-store.sql`, `021.properties-2014.sql` | version extensions of members, carrying delayed durability, incremental statistics and the requested Query Store state, none of which bears on size; `022.query-store` carries the storage figures |

### What the profile cannot say

Why one table compresses by three quarters and another by a third. The share of
NULLs and the actual width of the values are properties of the data, and a
profile made of metadata collectors does not read data. The estimate itself
comes from `041.compression-savings`, which does, and which stays behind its
flag for that reason.

Whether an index unused since the last restart is used once a year. The uptime
is collected so the analysis can say how long the window was; it cannot say what
happened outside it.

## New collector: `70.schema/055.page-density.sql`

### Why it is needed

Whether rebuilding an index returns space depends on how full its leaf pages
are. `20.databases/020.properties.sql` reads
`sys.dm_db_index_physical_stats` in `LIMITED` mode, where
`avg_page_space_used_in_percent` is NULL, and keeps only indexes with more than
10 % logical fragmentation. Logical fragmentation measures page order, not page
fullness. On the client instance this profile was designed from, the largest
single compression candidate was a table fragmented at about 1 %, which that
filter never lists; and a table can be perfectly ordered and half empty, which
is exactly the table a rebuild shrinks.

It runs in the default corpus as well as in the profile. The index maintenance
questions of a full audit need the same measure, and the cost is of the same
kind and bound as the heap scan every default run already pays.

### The file

To be committed verbatim. It was executed on the SQL Server 2025 container
exactly as below except for the `@profiles` line, which the current parser
rejects as an unknown directive until this specification is implemented.

```sql
-- @scope:       database
-- @resultsets:  root:object, indexes:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     300
-- @profiles:    space
--
-- How full the leaf pages of the largest rowstore indexes are.
--
-- Why this collector exists: whether rebuilding an index gives space back
-- depends on how full its pages are, and nothing else in the corpus says.
-- 20.databases/020.properties reads sys.dm_db_index_physical_stats in LIMITED
-- mode, where avg_page_space_used_in_percent is NULL, and keeps only indexes
-- above 10 % logical fragmentation. Logical fragmentation is page ORDER, not
-- page FULLNESS: a table can be perfectly ordered and half empty, and it is
-- then the one a rebuild shrinks.
--
-- THE COST IS BOUNDED THE WAY 050.heaps BOUNDS IT. SAMPLED reads a sample of
-- the pages rather than metadata, so the scan is applied only to the 50
-- largest rowstore indexes, chosen from metadata first. The cap is projected
-- in root so a reader never mistakes the list for the whole.
--
-- CHOSEN AND ORDERED BY IN-ROW PAGES, NOT BY RESERVED PAGES. reserved_page_count
-- counts LOB and row-overflow pages too. Measured on SQL Server 2025: a table
-- holding one 30 MB varbinary(max) row reserved 29.3 MB and had ONE in-row leaf
-- page, so ranking by reserved size spent a scan on an index with nothing a
-- rebuild could repack. Both sizes are projected, because the total is what
-- the disk sees and the in-row part is what this measurement is about.
--
-- NO JUDGEMENT IS APPLIED, and no reclaimable size is computed. What a rebuild
-- would free depends on the fill factor it is run with, which is the
-- analysis layer's choice; this file reports the fullness, the fill factor
-- the index carries and the page count, which are the three inputs.
--
-- Leaf level of in-row data only. Upper levels are a rounding error on a large
-- index, and LOB and row-overflow pages are not repacked by a rebuild the same
-- way, so mixing them in would describe no real operation.
--
-- SQL Server 2012 is the floor. Every column read here is documented before
-- it; the file has been executed on SQL Server 2025 only.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @top int = 50;
DECLARE @eligible int = NULL;
DECLARE @err int = 0, @msg nvarchar(2048) = N'';
DECLARE @density TABLE (
    [table]            nvarchar(300) NOT NULL,
    index_name         sysname       NULL,
    index_id           int           NOT NULL,
    index_type         nvarchar(60)  NOT NULL,
    partition_number   int           NOT NULL,
    fill_factor        tinyint       NOT NULL,
    reserved_mb        decimal(18,1) NOT NULL,
    in_row_reserved_mb decimal(18,1) NOT NULL,
    page_count         bigint        NULL,
    page_fullness_pct  decimal(5,2)  NULL,
    fragmentation_pct  decimal(5,2)  NULL,
    record_count       bigint        NULL
);

/* Read inside TRY/CATCH into a table variable, and emitted unconditionally
   below, for the reason 70.schema/020.index-usage gives: this names user
   objects, READ UNCOMMITTED does not release metadata locks, and a blocked read
   must cost this list rather than the whole document. */
BEGIN TRY
    SELECT @eligible = COUNT(*)
    FROM sys.dm_db_partition_stats AS ps
    JOIN sys.indexes AS i ON i.object_id = ps.object_id AND i.index_id = ps.index_id
    JOIN sys.objects AS o ON o.object_id = ps.object_id
    WHERE o.type = 'U' AND o.is_ms_shipped = 0
      AND i.type IN (1, 2) AND i.is_disabled = 0 AND i.is_hypothetical = 0
      AND ps.in_row_used_page_count > 128
    OPTION (RECOMPILE, MAXDOP 1);

    INSERT INTO @density
    SELECT OBJECT_SCHEMA_NAME(c.object_id) + N'.' + OBJECT_NAME(c.object_id),
           c.index_name,
           c.index_id,
           c.index_type,
           c.partition_number,
           c.fill_factor,
           CAST(c.reserved_pages * 8 / 1024.0 AS decimal(18,1)),
           CAST(c.in_row_reserved_pages * 8 / 1024.0 AS decimal(18,1)),
           ips.page_count,
           CAST(ips.avg_page_space_used_in_percent AS decimal(5,2)),
           CAST(ips.avg_fragmentation_in_percent AS decimal(5,2)),
           ips.record_count
    FROM (
        SELECT TOP (@top)
               ps.object_id, ps.index_id, ps.partition_number,
               i.name                        AS index_name,
               i.type_desc                   AS index_type,
               i.fill_factor,
               ps.reserved_page_count        AS reserved_pages,
               ps.in_row_reserved_page_count AS in_row_reserved_pages
        FROM sys.dm_db_partition_stats AS ps
        JOIN sys.indexes AS i ON i.object_id = ps.object_id AND i.index_id = ps.index_id
        JOIN sys.objects AS o ON o.object_id = ps.object_id
        WHERE o.type = 'U' AND o.is_ms_shipped = 0
          AND i.type IN (1, 2) AND i.is_disabled = 0 AND i.is_hypothetical = 0
          AND ps.in_row_used_page_count > 128
        ORDER BY ps.in_row_reserved_page_count DESC, ps.object_id, ps.index_id, ps.partition_number
    ) AS c
    CROSS APPLY sys.dm_db_index_physical_stats(DB_ID(), c.object_id, c.index_id, c.partition_number, 'SAMPLED') AS ips
    WHERE ips.index_level = 0
      AND ips.alloc_unit_type_desc = N'IN_ROW_DATA'
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT DB_NAME()                                   AS [database],
       CONVERT(varchar(23), SYSDATETIME(), 126)    AS [collected_at],
       @top                                        AS [sample.largest_indexes_scanned],
       'SAMPLED'                                   AS [sample.mode],
       @eligible                                   AS [counts.eligible_indexes],
       CASE WHEN @err = 0 THEN 1 ELSE 0 END        AS [collected.indexes],
       @err                                        AS [errors.indexes],
       NULLIF(@msg, N'')                           AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

SELECT [table], index_name, index_id, index_type, partition_number, fill_factor,
       reserved_mb, in_row_reserved_mb, page_count, page_fullness_pct, fragmentation_pct, record_count
FROM @density
ORDER BY in_row_reserved_mb DESC, [table], index_id, partition_number
OPTION (RECOMPILE, MAXDOP 1);
```

### Cost

`SAMPLED` reads about 1 % of the pages of an index, and switches to `DETAILED`
below 10,000 pages, where a full read is cheap. On the client database of about
820 GB this profile was designed from, the 50 largest indexes would mean reads
in the order of 8 GB, per database. That figure is an estimate: the file has
only been run on a small test database, and the first collection on a large
instance measures it. Like the heap scan, the cost is buffer-pool eviction,
which neither `SET LOCK_TIMEOUT` nor `@timeout` bounds.

## Change to `70.schema/010.objects.sql` and `70.schema/060.columns.sql`

### The defect

Both files keep the 200 tables with the most rows. A table of documents or
images with few rows and a large LOB footprint falls outside that list on any
database with more than 200 tables holding more rows than it has, and it is
exactly the table the space question is about. Measured on SQL Server 2025, on
a test database of 203 tables: a table holding one 30 MB `varbinary(max)` row
was absent from both documents.

### The change

The selection becomes the union of the 200 tables with the most rows and the 50
tables with the most reserved pages, all allocation units included. Both files
carry the same fragment, verbatim, for the reason the comment in
`010.objects.sql` gives for the ORDER BY today: two statements that choose
different tables produce an archive listing a table whose columns are missing.

In `060.columns.sql`, the body of both `sized` CTEs becomes:

```sql
    SELECT by_rows.object_id
    FROM (SELECT TOP (200) t2.object_id
          FROM sys.tables AS t2
          CROSS APPLY (SELECT SUM(p.row_count) AS row_count
                       FROM sys.dm_db_partition_stats AS p
                       WHERE p.object_id = t2.object_id AND p.index_id IN (0, 1)) AS r
          WHERE t2.is_ms_shipped = 0
          ORDER BY r.row_count DESC, t2.object_id) AS by_rows
    UNION
    SELECT by_size.object_id
    FROM (SELECT TOP (50) t2.object_id
          FROM sys.tables AS t2
          CROSS APPLY (SELECT SUM(p.reserved_page_count) AS reserved_pages
                       FROM sys.dm_db_partition_stats AS p
                       WHERE p.object_id = t2.object_id) AS r
          WHERE t2.is_ms_shipped = 0
          ORDER BY r.reserved_pages DESC, t2.object_id) AS by_size
```

In `010.objects.sql`, the `INSERT INTO @tables` loses its `TOP (200)`, and its
`WHERE t.is_ms_shipped = 0` gains `AND t.object_id IN (<the same fragment>)`.
Its `ORDER BY` stays; it no longer decides membership.

Both root objects gain `50 AS [listing_cap_by_size]` beside `listing_cap`, so a
reader can see that the list holds up to 250 tables and why.

Measured on SQL Server 2025, on the same test database, before and after:

| | `tables` in `010.objects` | the LOB table listed | its columns in `060.columns` |
| --- | --- | --- | --- |
| today | 200 | no | none |
| with the change | 201 | yes | `id`, `body` |

`tables_covered` in `060.columns` read 201, matching `010.objects`.

## What does not change

- The read-only paragraph of `MANIFEST.txt`, and every disclosure rule.
- `--queries-dir`: a corpus from disk may declare `@profiles`, under the same
  closed vocabulary.
- `DB_INCLUDE` and `DB_EXCLUDE`: a profile narrows the collectors, never the
  databases.
- The exit codes of `collect`, apart from the three refusals above, which use
  the existing 2.

## Tests

In `collect`:

- `TestSkipReasonForProfile`: a script outside the profile is skipped with
  `ProfileSkipReason`; a member falls through to the next gate; an empty
  profile changes nothing; a script outside the profile that is also gated on
  an unset flag and a denied permission reports the profile.
- `TestDiscoverParsesProfiles`, and two cases in `TestDiscoverLintErrors`: an
  unknown value, and an empty one.
- `TestCheckProfile`: an unknown name; a flag with no member; a flag with a
  member; a lint-failed member does not count as one.
- `TestRunFolderNameCarriesProfile`, and the existing `RunFolderFor` tests with
  a profile, including the `--keep` suffix after it.
- `TestManifestHumanProfileLine`, for both forms of the header line, and
  `TestManifestHumanGroupsProfileSkips`, which also checks that a skip for
  another reason is still listed on its own.
- `TestBuildGrantScriptRestrictedToProfile`: a capability denied and declared
  by no member produces no section and is named in the header; without a
  profile, the same input produces the script it produces today.
- `TestPlannedCollectorsFollowsProfile`.

In `cmd/sql-auditor`: `--profile` is parsed into `Options.Profile`, and `--all`
with `--profile` returns exit 2 with the message above.

In the root package:

- `TestEveryKnownProfileHasACollector`: every key of `KnownProfiles` is declared
  by at least one embedded collector. The two sets are decided in different
  places, which is what makes the comparison a test.
- `testdata/corpus.txt` records membership. A line is the path, followed, when
  the collector declares profiles, by one space and `@profiles: <names>`:
  `70.schema/055.page-density.sql @profiles: space`. `checkCorpusInventory`
  compares paths as today and then membership, with its own message:
  `70.schema/050.heaps.sql: profiles "space" in the corpus, "" in testdata/corpus.txt`.
  A collector cannot enter or leave a profile without the diff saying so.

In `tui`: `[p]` cycles the profiles; changing the profile turns off the flags
it hides and clamps `FlagIndex`; the count follows `PlannedCollectors`.

Verification against a real instance, before the release: `check --profile
space` and `collect --profile space` against the local SQL Server 2025
container, reading the listing, `MANIFEST.txt`, `_run.json` and the grant
script, then leaving the container as it was found.

## Documentation

- `README.md`: `--profile` in the table of options for `check` and `collect`,
  and a section "Collecting for one question" saying what a profile removes,
  what it does not, and how it combines with the flags.
- `docs/dba-guide.md`: the same section, written for the DBA who approves the
  run; a row for `70.schema/055.page-density.sql` in "What the default run costs
  a large instance"; the collector counts it states.
- `CHANGELOG.md`: an Unreleased entry for the profile, the collector and the
  table listing change.

## What this does not do

- Analyse anything. The archive holds what the space question needs; deciding
  what to disable, compress or rebuild, and in which order, is the analysis
  layer's job, outside this repository.
- Estimate ROW compression. `041.compression-savings` estimates PAGE on the 20
  largest uncompressed objects, and its header explains why ROW is a follow-up
  on chosen candidates rather than a second sweep.
- Offer any profile other than `space`.

## Open questions

1. The cost of `055.page-density` on a large database is estimated, not
   measured. If the first large collection shows it is unacceptable in the
   default run, the options are a lower `@top` or making it a profile member
   that the default corpus does not run, which would need a notion of
   profile-only collectors this document deliberately does not introduce.
2. The SQL Server 2012 floor of `055.page-density` rests on the documentation
   of the columns it reads. No 2012 instance is available locally.
