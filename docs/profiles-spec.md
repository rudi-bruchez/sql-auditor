# Collection profiles: specification

Status: implemented, and shipped in `c3b0d50`: `--profile space`, the
`055.page-density.sql` collector, and the refusal of `--profile` outside
`check` and `collect`. Written 14 September 2026, and revised twice the
same day after two reviews by five independent readers; what each review changed
is recorded at the end.

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
already executes or already offers behind a flag, under the same manifest
paragraph. What changes is how much of the corpus ran, and the manifest says so.

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

A script declares a profile when its header names it. A member of the profile
is a declaring script that passed lint. Every count in this document that says
"members" means that, and "declaring scripts" is used where lint failures are
included.

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
operator to pass an option for a collector the profile excluded would send them
to an option that changes nothing.

`Options` gains `Profile string`. Both callers of `planScripts`, `Run` in
`collect/collect.go` and `VerifyServer` in `collect/verify.go`, pass it.

## Selecting the databases

`planScripts` decides which collectors run. It does not decide which databases
are targets, and one rule outside it does: the second pass of `SelectTargets`
(`collect/runner.go`) brings the distribution database back into a narrowed run
whenever a published database survived the first pass. Under a profile that
keeps no replication collector, that database would be listed in `MANIFEST.txt`
under "Databases covered", with "kept because: local distributor" and the
notice that its catalogs describe every publication on the instance, while no
collector runs against it. A reviewer reproduced exactly that with an overlay
test over the current `SelectTargets`, `planUnits` and manifest writer.

So the widening follows the plan. Everything `planScripts` needs is known before
the databases are selected. In `Run`, the preflight (`collect/collect.go:1422`),
the denied capabilities (1428) and the version probe (1433) all come before
`SelectTargets` (1486). In `VerifyServer`, the probes (`collect/verify.go:134`
and 136) come before it (149). Both callers therefore build the plan first and
select second:

```go
// WideningPurposes returns the @widened purposes of the planned scripts that
// will run, that is, those planScripts did not skip.
func WideningPurposes(plan []plannedScript) map[string]bool

func SelectTargets(c []DatabaseInfo, include, exclude string,
	widen map[string]bool) (Selection, error)
```

The second pass runs only when `widen["replication"]` is true. When the version
probe failed in `VerifyServer`, there is no plan; the purposes are then taken
from the members of the profile whose flag is on, every lint-clean script
without a profile, which is the most a selection without a version can know.

Without a profile, a full run on an instance where the replication collectors
run is unchanged. On an instance where they are gated off by version or refused
by permission, the distribution database is no longer brought back for
collectors that were never going to read it. That is a correction for full runs
as well, and `CHANGELOG.md` says so.

## The command line

`--profile NAME` on `check` and `collect`. There is no `.env` key: the setting
belongs to the run being prepared, not to the machine, and a key would make an
unattended run quietly narrower than the operator believed.

### Four refusals, all exit 2

| Refused | Message |
| --- | --- |
| a name outside `KnownProfiles` | `unknown profile "spaec"; expected one of space` |
| `--all` with `--profile` | `--all and --profile cannot be combined: --all asks for the widest archive this tool can produce, and a profile for a narrow one` |
| a profile with no member in the corpus | `profile space has no collector in this corpus; a corpus exported before profiles existed declares none` |
| an option whose flag has no member | `--query-store-detail has no collector in profile space, so it would collect nothing; drop the option or the profile` |

When the profile, or the flag, has declaring scripts and every one of them
failed lint, the message names the lint failures instead, because that is what
explains it and the old-corpus sentence would send the operator to the wrong
repair: `profile space has no usable collector in this corpus:
70.schema/055.page-density.sql failed lint (<the lint error>)`, and the same
form for a flag.

The third refusal exists for a corpus from `--queries-dir`. Any corpus exported
before this feature, including the one `docs/dba-guide.md` tells operators to
build in order to drop a costly collector, declares no profile. Without the
refusal, `--profile space` on it skips every collector, writes an archive and
exits 0.

```go
// CheckProfile refuses the profile and flag combinations that would produce a
// run which looks successful and collects nothing the operator asked for. It
// judges declarations and lint only. A member that the instance's version or
// the login's rights will gate off is not known here; it is skipped at planning
// and recorded in skipped_scripts, as a flagged collector too recent for the
// instance is today.
func CheckProfile(scripts []Script, profile string, flags map[string]bool) error
```

### Where the refusals happen

In `optionsFrom` (`cmd/sql-auditor/main.go`), the function both paths go
through: `run()` calls it for `check` and `collect`, and `buildOptionsWithDebug`
calls it for the tests and the wizard. `buildOptions` alone would not do, since
the real command line never calls it.

`optionsFrom` refuses `--all` with `--profile` before anything else, because it
needs no corpus. Only when `--profile` was given does it discover the corpus it
has just chosen (embedded, or `--queries-dir`) and call `CheckProfile`, and it
announces that step on the debug timeline first (`checking profile space
against the corpus`). A run without a profile does no extra work, and neither
does the wizard, which never receives one on its command line. A corpus that
cannot be read is not judged there: validation is skipped, and the existing
corpus error is reported by `Run` or `Check` exactly as today.

A refusal from `optionsFrom` writes no file and never opens a connection. It is
not silent: `run()` prints the version banner before it calls `optionsFrom`, so
that every invocation, a refused one included, can be tied to a build, and
`--debug` prints its timeline as requested. The refusal message follows them.

`VerifyLocal` and `Run` repeat `CheckProfile`, as a guard for a caller that
builds `Options` itself. `VerifyResult` gains `ProfileErr error`; `Check`
returns 2 with it before printing the listing, next to the existing `CorpusErr`
branch. `Run` calls it immediately after `Discover`; a refusal there goes
through `finishWith`, so it leaves a `failed-run-*` record whose profile block
names what was asked. The wizard cannot reach either guard: it offers no
refused combination.

## Permissions under a profile

The preflight probes every capability the corpus knows, whatever the profile. A
login prepared for a space run is refused rights no member uses, such as
`log_shipping`, `agent_alerts` and `error_log`. Left as they are, those refusals
would make `MANIFEST.txt` print "INCOMPLETE ... Parts of this instance were not
read", mark them `MISSING` in the grant script header, and annotate them in
`check`, three different statements of one fact.

One rule replaces all three. A right the profile does not need, and that the
login was refused, gets its own status:

```go
// ProfileChecks returns checks with the status of every capability that is
// "denied", that no member of the profile declares in @permissions, and that
// is neither "connect" nor "view_any_definition", set to "not_needed". Every
// other status, "error" included, is left as it is. With an empty profile it
// returns checks unchanged.
func ProfileChecks(checks []CapabilityCheck, scripts []Script, profile string) []CapabilityCheck
```

Three limits, each found by reviewers:

- `denied` only. `error` means the connection failed during the preflight, and
  `RunPreflight` then marks every later probe `error` as well; the three
  capabilities `space` does not need are the last three probed, under one
  shared deadline. Rewriting `error` would hide a dropped connection: `Check`
  returns `PreflightExitCode` even when the server probe failed, so it would
  exit 0.
- Not `connect`, which every run needs.
- Not `view_any_definition`, which the database discovery needs whatever the
  collectors declare: `CandidateDatabases` reads through it, and
  `refreshCoverage` explains a short database list by its refusal. A future
  profile of instance-level collectors must not certify a database list the
  login could not see.

Where it is applied:

- In `Run`, to `m.Preflight` as soon as the preflight returns, before
  `DeniedCapabilities` and before coverage is computed.
- `VerifyServer` keeps returning the raw checks. `Check` applies `ProfileChecks`
  to them before it prints the listing and before it builds the grant script.
  The wizard applies it with `s.Profile` wherever it reads a status, so a
  verification repeated after the profile changed never loses a raw status.

Every reader of `CapabilityCheck.Status` in the repository:

| Reader | Under a profile |
| --- | --- |
| `DeniedCapabilities` (`collect/preflight.go`) | unchanged; `not_needed` is not `ok`, and no member declares the capability, so no member is skipped for it |
| `PreflightExitCode` (`collect/preflight.go`) | unchanged; it reads `error`, which is never rewritten, so a dropped connection still exits 1 |
| `refreshCoverage` (`collect/manifest.go`) | `not_needed` counts as `ok`: coverage is `complete` when every capability a member needs was granted; its notes about `view_any_definition` are unaffected, since that capability is never rewritten |
| `writeCoverage` (`collect/manifest.go`) | the COMPLETE and INCOMPLETE texts are unchanged; a paragraph follows, `Not needed by profile space`, listing the labels, saying they were refused and that no collector of this profile reads what they allow |
| `checkStatus` (`collect/manifest.go`) | called for `view_any_definition` only, which is never rewritten |
| the `check` listing (`collect/collect.go`) | `not needed  <name>`, without the impact text |
| `anyDenied` (`collect/collect.go`) | reads the checks after `ProfileChecks` |
| `BuildGrantScript` (`collect/grants.go`) | builds sections from `denied` only, so a `not_needed` capability gets none; the header marks it `not needed`; `errored` is unchanged |
| wizard screen 2 (`tui/render.go`) | status `not needed`, no impact text, counted with `ok` in the `N / M` figure |
| `deniedPermissions` (`tui/render.go`) | reads the checks after `ProfileChecks`, so the final screen counts only refusals the run was affected by, as the manifest does |

`_run.json` therefore carries `"status": "not_needed"` in `preflight` under a
profile, and never without one.

### The grant script

`GrantScriptInput` gains `Profile string`. With a profile, the header says `for
profile space`, and its instruction for checking the result becomes `sql-auditor
check --profile space`: without the option, the unneeded rights would print
`denied` again. Both callers, `writeGrantScript` in `collect/collect.go` and the
`[g]` key in the wizard, pass the members as `Scripts` and the checks after
`ProfileChecks`, so `collectorsFor` names members only. The section for
databases the login cannot enter is written only when at least one script in
`Scripts` is database-scoped; a future profile made of instance collectors must
not ask for per-database access. `space` has database-scoped members, so the
section stays for it.

## What the archive says

### `_run.json`

```go
type ProfileBlock struct {
	// Name is the profile requested, empty when none was.
	Name string `json:"name"`
	// Members is how many members the profile has in the corpus, and Corpus
	// how many lint-clean scripts the corpus holds. Both are 0 when the run
	// ended before the corpus was read. What actually ran is in results.
	Members int `json:"members"`
	Corpus  int `json:"corpus"`
}
```

serialised as `"profile": {"name": "space", "members": 22, "corpus": 85}`.
`Name` is set from `Options` when the manifest is created, before the first
thing that can fail, so every `failed-run-*` record says what was asked.
`Members` and `Corpus` are set after `Discover`.

The block is written on every run, with `"name": ""` when there is no profile,
for the reason the transport block is: an absent key and a key saying "none"
must not be told apart by guesswork.

`skipped_scripts` keeps one entry per collector left out by the profile, with
`ProfileSkipReason` as the reason. The analysis layer reads that list to tell
"not collected because of the profile" from "not collected because a permission
was refused", and it must stay complete.

### `MANIFEST.txt`

One header line, printed unconditionally, after `Contents`:

```
Profile      : none, the whole corpus
Profile      : space, 22 of the 85 collectors in the corpus belong to it
Profile      : space, requested; the corpus was not read
```

The count is membership, not execution: `041.compression-savings` and
`055.page-density` are members behind flags, so a space run with neither flag
executes at most 19, and "Queries run" says how many did.

In "Queries not run", the entries whose reason equals
`ProfileSkipReason(m.Profile.Name)` are collapsed into one line placed first:

```
Queries not run (65):
  - 63 collectors outside profile space, each listed in _run.json
  - 70.schema/041.compression-savings.sql
      not collected by default; pass --estimate-compression to include it
  - 70.schema/055.page-density.sql
      not collected by default; pass --measure-page-density to include it
```

The count in the heading stays the count of entries in `skipped_scripts`. The
document a security officer reads has to stay readable; 63 identical lines
bury the skips that carry information.

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
suffix is appended after it. The callers are `Run` in `collect/collect.go`,
`runFolderFor` in `tui/state.go`, and the tests in `collect/output_test.go`,
`collect/collect_test.go` and `tui/state_test.go`.

A full run and a space run on the same day no longer collide, so neither
replaces the other.

## `check`

```
Profile: space, 22 of 85 collectors
Queries (22):
  10.system/010.properties.sql               ...
  !! 70.schema/099.custom.sql                <the lint error>
  ...
Not in profile space: 63 collectors. Run check without --profile to list them.
Lint failures outside profile space (1):
  !! 80.workload/099.other.sql               <the lint error>
```

The `Queries` block lists every script that declares the profile: its members,
and a declaring script that failed lint with its `!!` line. Its heading counts
those lines, which is why it can exceed the member count on the line above.
Lint failures of scripts that do not declare the profile get a block of their
own, printed only when there are some.

## The wizard

Screen 3, "What to collect", gains a first row above the options:

```
  Profile        [p]  none, the whole corpus
                      space: what makes the databases on this instance larger ...
```

- `State` gains `Profile string`. `[p]` cycles through `""` and the keys of
  `KnownProfiles` that have at least one member in `s.Verify.Scripts`, in sorted
  order, then back to `""`. A profile with no member in the corpus is not
  offered: selecting it could only make `[enter]` do nothing without saying why.
- `[p]` is handled in `pressEvent.apply` in `tui/run.go`, beside `[g]`, and not
  in the pure `keyOptions`, because it has to look at the output directory.
  After changing the profile it recomputes `s.Collision = collisionFor(s,
  e.opts)`; when the banner changes, `Keep` returns to `false`, since an answer
  given for the previous folder name does not apply to the new one. It also
  clears `GrantPath` and `GrantError`, which describe a script written for the
  previous profile. Without the collision probe, a same-day space archive would
  be replaced by pressing `[enter]` with no banner, because the collision was
  probed for the full-run name during verification.
- The option rows shown are the flags of `flagOrder` that have at least one
  member in the profile, or all of them without a profile. Changing the profile
  turns off every flag whose row it hides and clamps `FlagIndex`. The wizard
  therefore never starts a combination `CheckProfile` refuses.
- The wizard keeps the raw checks `VerifyServer` returns, including after `[b]`
  and `[r]` verify again with a profile selected, and applies
  `ProfileChecks(s.Verify.Checks, s.Verify.Scripts, s.Profile)` wherever it
  reads a status: screen 2, `[g]`, the count, and `deniedPermissions` on the
  final screen.
- `[g]` is accepted on screen 3 as well as on screen 2, and uses `s.Profile`:
  the members as scripts, the checks after `ProfileChecks`. On screen 2, before
  a profile is chosen, it writes the full script as today; screen 3 says
  `[g] write the T-SQL for this profile`.
- The collector count is recomputed from the plan whenever the profile or a
  flag changes, and `canStart` uses it instead of `Verify.Collectors`, so the
  start gate and the figure on screen agree:

  ```go
  // PlannedCollectors is VerifyResult.Collectors for another profile or
  // another set of flags: planScripts over v.Scripts, with the denied
  // capabilities of ProfileChecks(v.Checks, v.Scripts, profile) and the
  // version of v.Server. It is zero when v.Probed is false. Like
  // countCollectors, it counts scripts that would run at least once and
  // does not look at which databases were selected.
  func PlannedCollectors(v VerifyResult, profile string, flags map[string]bool) int
  ```

- With a profile, the sentence reads `Profile space: N collectors will run on
  this instance, computed from the resolved plan.` and the sentence introducing
  the options describes the rows actually shown.
- `applyState` copies `Profile` into `Options`.

The wizard has no command line, so this row is the only way to choose a profile
there.

## The `space` profile

### Members

21 collectors: 20 that exist today and one added by this specification. Two of
them are behind flags.

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
| `50.agent/020.job-steps.sql` | the step text, where a `DBCC SHRINKFILE` in a T-SQL job shows |
| `50.agent/040.maintenance-plans.sql` | shrink tasks in maintenance plans |
| `60.backup/010.history.sql` | how far back msdb history goes, log backup cadence |
| `70.schema/010.objects.sql` | table sizes, data and indexes apart, creation and modification dates |
| `70.schema/020.index-usage.sql` | the size and usage counters of every index, uncapped |
| `70.schema/040.compression.sql` | what is compressed today |
| `70.schema/041.compression-savings.sql` | estimated savings, only with `--estimate-compression` |
| `70.schema/050.heaps.sql` | page fullness and forwarded records on the 50 largest heaps |
| `70.schema/055.page-density.sql` | new: page fullness on the 50 largest rowstore index partitions, indexed views included, only with `--measure-page-density` |
| `70.schema/060.columns.sql` | declared types, LOB columns |
| `70.schema/070.index-columns.sql` | keys and included columns, fill factor, disabled indexes |

`50.agent/020.job-steps.sql` carries `@discloses: job_step_text`. It stays in
the profile, and the manifest discloses it exactly as it does for a full run.

The wizard offers both flags under the profile. The documentation says that a
space question is answered better with `--measure-page-density`, and says what
it costs, so the operator decides per instance.

### Left out, and why

| Collector | Why |
| --- | --- |
| `70.schema/030.index-operational.sql` | the write profile the space question needs is `user_updates` in `020.index-usage` |
| `70.schema/090.statistics.sql`, `091.statistics-density.sql` | statistics occupy negligible space |
| `80.workload/*` | about time spent, not space occupied |
| `10.system/020.host-services.sql` | instant file initialisation decides how fast a file grows, not how large it is |
| `10.system/040.error-log.sql` | discloses the error log for one marginal question, the size of the log file itself |
| `20.databases/011.all-databases-2014.sql`, `012.all-databases-query-store.sql`, `021.properties-2014.sql` | additive version extensions of members, carrying delayed durability, incremental statistics and the requested Query Store state; none bears on size, and `022.query-store` carries the storage figures |

### What the profile cannot say

Why one table compresses by three quarters and another by a third. The share of
NULLs and the actual width of the values are properties of the data, and the
metadata collectors do not read data. The estimate comes from
`041.compression-savings`, which does, and which stays behind its flag for that
reason.

Whether an index unused since the last restart is used once a year. The uptime
is collected so the analysis can say how long the window was; it cannot say what
happened outside it.

Whether a shrink is scheduled when the command sits after the first 200
characters of a T-SQL job step: `50.agent/020.job-steps.sql` keeps only that
much of the step text.

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

### Why it is behind a flag

`SAMPLED` does not read 1 % of the pages, and what it reads is decided
allocation unit by allocation unit. Measured on SQL Server 2025, with the
database taken offline and back online before each run, and
`sys.dm_io_virtual_file_stats` read before and after:

| Target | In-row pages | LOB pages | Pages read | Read |
| --- | ---: | ---: | ---: | --- |
| clustered index, contiguous | 30,000 | 0 | 2,608 | 8.7 % |
| clustered index, fragmented at 99 % | 39,985 | 0 | 4,672 | 11.7 % |
| heap | 30,100 | 0 | 2,520 | 8.4 % |
| clustered index, LOB stored off-row | 16,730 | 50,001 | 5,560 to 5,568 | 8.3 % of all its pages |
| clustered index under 10,000 in-row pages, LOB off-row | 6,713 | 20,009 | 8,432 to 8,496 | every in-row page, and about 8.7 % of the LOB |
| the two 30,000-page targets in `DETAILED` (control) | | | | about 100 % |

The rule the table gives, and the one to state wherever the cost is described:
an allocation unit of 10,000 pages or more brings 8 to 12 % of its pages into
the buffer pool, because each sample reads a whole extent; a smaller one is read
in full; and the LOB and row-overflow pages of an index are units of their own.
Ranking partitions by in-row pages decides what is measured, not what it costs.

The rule predicts the whole file. On a database holding one clustered index of
16,753 in-row and 50,009 LOB pages, a table split into three 521-page
partitions, a 1,529-page table and a 1,521-page indexed view on it, it predicts
about 10,220 pages; the file, run verbatim from a cold cache, read 10,192 and
10,224. Two reviewers found parts of the rule independently, one the full read
below 10,000 pages and the other the LOB sampling.

On the 820 GB client database this profile was designed from, the 50 largest
partitions would bring at least 70 to 95 GB into the buffer pool per database,
more where they carry LOB, evicting what the workload had there. `SET
LOCK_TIMEOUT` does not bound that.

So it is an opt-in for cost, like `--estimate-compression`:

```go
const FlagMeasurePageDensity = "measure_page_density"

// in KnownFlags:
"measure_page_density": "--measure-page-density",
```

The flag name is spelt so that `collectorsFor`, which derives the option from
the flag by replacing `_` with `-`, prints the real option. `--all` turns it on
with the others, `TestAllTurnsOnEveryOptIn` covers it through `KnownFlags`,
`flagOrder` in `tui/state.go` gains it after `FlagEstimateCompression`, and the
option text reads `measure page density: reads 8 to 12 % of every large index
partition into the buffer pool, LOB included, and all of a small one`.

`@timeout` is 1800 seconds, like `041.compression-savings`. At 300, a large
database cancels the batch, and a cancelled batch loses the whole document:
`TRY/CATCH` does not catch a client cancel, so the summary row goes with the
detail rows, after the buffer pool has already been evicted.

### The unit is the partition

`sys.dm_db_partition_stats` has one row per partition, and a rebuild can be run
per partition, so the collector ranks and caps partitions and says so: the root
fields are `sample.largest_partitions_scanned` and `counts.eligible_partitions`,
and `counts.indexes_covered` says how many distinct indexes the list holds. One
heavily partitioned index can take every slot; the root makes that visible
instead of calling 50 partitions 50 indexes.

### The file

To be committed verbatim. It was executed on the SQL Server 2025 container
exactly as below, except for the `@requires_flag` and `@profiles` lines, which
the current parser rejects as an unknown flag and an unknown directive until
this specification is implemented. Through the tool, on a database holding a
clustered index split into three partitions, a table carrying off-row LOB, a
plain table and an indexed view on it, it passed lint and returned
`eligible_partitions 6`, `indexes_covered 4`, the indexed view among the rows,
and a `lob_reserved_mb` of 23.4 for the LOB table.

Indexed views are included (`o.type IN ('U', 'V')`): the clustered index of a
view occupies space and is rebuilt like a table's, and the first two versions
left it out without saying so.

The file paid for one rule while this revision was verified: a header comment
line must not begin with an `@` word. A line reading `-- @timeout IS 1800, LIKE
041.` was parsed as a second `@timeout` directive and the collector failed lint;
sqlcmd, which ignores comments, could not show it.

```sql
-- @scope:       database
-- @resultsets:  root:object, indexes:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER STATE
-- @timeout:     1800
-- @requires_flag: measure_page_density
-- @profiles:    space
--
-- How full the leaf pages of the largest rowstore index partitions are.
--
-- Why this collector exists: whether rebuilding an index gives space back
-- depends on how full its pages are, and nothing else in the corpus says.
-- 20.databases/020.properties reads sys.dm_db_index_physical_stats in LIMITED
-- mode, where avg_page_space_used_in_percent is NULL, and keeps only indexes
-- above 10 % logical fragmentation. Logical fragmentation is page ORDER, not
-- page FULLNESS: a table can be perfectly ordered and half empty, and it is
-- then the one a rebuild shrinks.
--
-- IT IS BEHIND A FLAG FOR COST, LIKE 041.compression-savings. SAMPLED is not
-- the 1 % read its name suggests. Measured on SQL Server 2025, from a cold
-- buffer pool, allocation unit by allocation unit: one with 10,000 pages or
-- more brings in 8 to 12 % of its pages, because each sample reads a whole
-- extent; one below 10,000 pages is read in full. The LOB and row-overflow
-- pages of the index count as units of their own and are read the same way.
-- On a large database that is tens of gigabytes pulled into the buffer pool,
-- evicting what the workload had there, and SET LOCK_TIMEOUT does not bound it.
--
-- THE TIMEOUT IS 1800 SECONDS, LIKE 041. A batch cancelled on timeout loses the whole
-- document: TRY/CATCH does not catch a client cancel, so the summary row goes
-- with the detail rows, after the buffer pool was already evicted.
--
-- THE UNIT IS THE PARTITION, NOT THE INDEX. sys.dm_db_partition_stats has one
-- row per partition, and a rebuild can be run per partition, so the cap of 50
-- is 50 partitions. One heavily partitioned index can take every slot; root
-- says how many distinct indexes the list covers so a reader sees it.
--
-- CHOSEN AND ORDERED BY IN-ROW PAGES, NOT BY RESERVED PAGES. reserved_page_count
-- counts LOB and row-overflow pages too. Measured on SQL Server 2025: a table
-- holding one 30 MB varbinary(max) row reserved 29.3 MB and had ONE in-row leaf
-- page, so ranking by reserved size spent a scan on an index with nothing a
-- rebuild could repack. The ranking decides what is measured, not what it
-- costs: the LOB pages of a chosen partition are read too, so lob_reserved_mb
-- is projected beside the other two sizes and the cost of each row is visible.
--
-- Indexed views are included. The clustered index of a view occupies space
-- and is rebuilt like a table's.
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
DECLARE @eligible int = NULL, @indexes_covered int = NULL;
DECLARE @err int = 0, @msg nvarchar(2048) = N'';
DECLARE @density TABLE (
    [table]           nvarchar(300) NOT NULL,
    index_name        sysname       NULL,
    index_id          int           NOT NULL,
    index_type        nvarchar(60)  NOT NULL,
    partition_number  int           NOT NULL,
    fill_factor       tinyint       NOT NULL,
    reserved_mb       decimal(18,1) NOT NULL,
    in_row_reserved_mb decimal(18,1) NOT NULL,
    lob_reserved_mb   decimal(18,1) NOT NULL,
    page_count        bigint        NULL,
    page_fullness_pct decimal(5,2)  NULL,
    fragmentation_pct decimal(5,2)  NULL,
    record_count      bigint        NULL
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
    WHERE o.type IN ('U', 'V') AND o.is_ms_shipped = 0
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
           CAST(c.lob_reserved_pages * 8 / 1024.0 AS decimal(18,1)),
           ips.page_count,
           CAST(ips.avg_page_space_used_in_percent AS decimal(5,2)),
           CAST(ips.avg_fragmentation_in_percent AS decimal(5,2)),
           ips.record_count
    FROM (
        SELECT TOP (@top)
               ps.object_id, ps.index_id, ps.partition_number,
               i.name                 AS index_name,
               i.type_desc            AS index_type,
               i.fill_factor,
               ps.reserved_page_count        AS reserved_pages,
               ps.in_row_reserved_page_count AS in_row_reserved_pages,
               ps.lob_reserved_page_count    AS lob_reserved_pages
        FROM sys.dm_db_partition_stats AS ps
        JOIN sys.indexes AS i ON i.object_id = ps.object_id AND i.index_id = ps.index_id
        JOIN sys.objects AS o ON o.object_id = ps.object_id
        WHERE o.type IN ('U', 'V') AND o.is_ms_shipped = 0
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

SELECT @indexes_covered = COUNT(*)
FROM (SELECT DISTINCT [table], index_id FROM @density) AS d;

SELECT DB_NAME()                                   AS [database],
       CONVERT(varchar(23), SYSDATETIME(), 126)    AS [collected_at],
       @top                                        AS [sample.largest_partitions_scanned],
       'SAMPLED'                                   AS [sample.mode],
       @eligible                                   AS [counts.eligible_partitions],
       @indexes_covered                            AS [counts.indexes_covered],
       CASE WHEN @err = 0 THEN 1 ELSE 0 END        AS [collected.indexes],
       @err                                        AS [errors.indexes],
       NULLIF(@msg, N'')                           AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

SELECT [table], index_name, index_id, index_type, partition_number, fill_factor,
       reserved_mb, in_row_reserved_mb, lob_reserved_mb, page_count, page_fullness_pct, fragmentation_pct, record_count
FROM @density
ORDER BY in_row_reserved_mb DESC, [table], index_id, partition_number
OPTION (RECOMPILE, MAXDOP 1);
```

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
The fragment defines a set; neither file depends on its order.

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

### Measured

Membership, on the 203-table database, before and after:

| | `tables` in `010.objects` | the LOB table listed | its columns in `060.columns` |
| --- | --- | --- | --- |
| today | 200 | no | none |
| with the change | 201 | yes | `id`, `body` |

`tables_covered` in `060.columns` read 201, matching `010.objects`. Three
reviewers reproduced this table independently.

Cost, the whole `010.objects` file through sqlcmd, wall clock, two runs each:

| Catalog | Today | With the change |
| --- | ---: | ---: |
| 261 tables | 349 to 610 ms | 414 to 440 ms |
| 5,001 tables | 1,593 to 1,750 ms | 572 to 593 ms |

The change is faster on a large catalog, because today's statement computes the
per-table aggregates for every table before its `TOP`. Materialising the
fragment into a table variable first measured the same as the `IN` form, so the
simpler form stays.

## What does not change

- The read-only paragraph of `MANIFEST.txt`, and every disclosure rule.
- `--queries-dir`: a corpus from disk may declare `@profiles`, under the same
  closed vocabulary.
- `DB_INCLUDE` and `DB_EXCLUDE`: a profile narrows the collectors, and only the
  replication widening follows it.
- The exit codes, apart from the four refusals above, which use the existing 2.

## Tests

In `collect`:

- `TestSkipReasonForProfile`: a script outside the profile is skipped with
  `ProfileSkipReason`; a member falls through to the next gate; an empty
  profile changes nothing; a script outside the profile that is also gated on
  an unset flag and a denied permission reports the profile.
- `TestDiscoverParsesProfiles`, and two cases in `TestDiscoverLintErrors`: an
  unknown value, and an empty one.
- `TestCheckProfile`: an unknown name; a corpus with no declaring script; a
  profile whose declaring scripts all failed lint, whose message names the lint
  error; a flag with no member; a flag whose only member failed lint; a flag
  with a member.
- `TestWideningPurposes`, and `TestSelectTargetsWidensOnlyForAPurpose`: the
  distribution database is kept with `widen["replication"]` and not without it,
  with the publisher selected in both cases; and a plan whose replication
  collectors are all skipped yields no purpose.
- `TestProfileChecks`: a `denied` capability no member declares becomes
  `not_needed`; one a member declares stays `denied`; `error` is never
  rewritten; `ok` is untouched; `connect` and `view_any_definition` are never
  rewritten; an empty profile returns the checks unchanged.
- `TestCheckExitsOneWhenAnUnneededProbeGotNoAnswer`: under a profile, an
  `error` on a capability no member declares still gives exit 1.
- `TestVerifyServerReturnsRawChecks`.
- `TestRefreshCoverageTreatsNotNeededAsOk` and
  `TestManifestHumanNotNeededParagraph`.
- `TestBuildGrantScriptUnderProfile`: a `not_needed` capability gets no section
  and is marked in the header; the header's check command carries
  `--profile space`; without a profile the same input produces the script it
  produces today; the no-access section is absent when no script is
  database-scoped.
- `TestCheckRefusesProfileBeforeListing`: `Check` returns 2 with `ProfileErr`
  and prints no `Queries` line.
- `TestRunFolderNameCarriesProfile`, and the existing `RunFolderFor` tests with
  a profile, including the `--keep` suffix after it.
- `TestManifestHumanProfileLine`, for the three forms of the header line, and
  `TestManifestHumanGroupsProfileSkips`, which also checks that a skip for
  another reason is still listed on its own.
- `TestPlannedCollectorsFollowsProfile`.

In `cmd/sql-auditor`, through `buildOptions`, which reaches `optionsFrom` like
`run()` does: `--profile` is parsed into `Options.Profile`; `--all` with
`--profile` returns exit 2 with its message; an unknown profile and an option
with no member return exit 2; a `--queries-dir` corpus with no `@profiles` line
returns exit 2 under `--profile space`; and without `--profile` the corpus is
not discovered in `optionsFrom`.

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

In `tui`: `[p]` cycles only the profiles with a member; `[p]` recomputes the
collision, and a same-day folder for the new name brings the banner up; `[p]`
clears `GrantPath` and `GrantError`; changing the profile turns off the flags it
hides and clamps `FlagIndex`; `[p]`, `[b]`, `[r]` leaves the raw checks raw;
`[g]` on screen 3 writes the script for the profile; screen 2 renders
`not needed`; `deniedPermissions` ignores unneeded refusals; `canStart` and the
count follow `PlannedCollectors`.

Verification against a real instance, before the release: `check --profile
space` and `collect --profile space --measure-page-density` against the local
SQL Server 2025 container, reading the listing, `MANIFEST.txt`, `_run.json` and
the grant script of a login missing an unneeded right, then leaving the
container as it was found.

## Documentation

- `README.md`: `--profile` and `--measure-page-density` in the table of options
  for `check` and `collect`; `--all` turns on ten options; a section "Collecting
  for one question" saying what a profile removes, what it does not, how it
  combines with the flags, and which flag a space question wants.
- `docs/dba-guide.md`: the same section written for the DBA who approves the
  run, and what `not needed` means in `check` and in `MANIFEST.txt`;
  `70.schema/055.page-density.sql` in the table of opt-in files, whose heading
  becomes ten; and a correction in "What the default run costs a large
  instance": the heap scan brings in 8 to 12 % of the pages of each of the 50
  largest heaps that has 10,000 pages or more, and all of a smaller one, LOB
  pages included, not about 1 %; a 500 GB heap costs tens of gigabytes of reads,
  not 5 GB. The measurements above are the source.
- `CHANGELOG.md`: an Unreleased entry for the profile, the collector, the
  `not_needed` status, the table listing change, the corrected cost, and the
  replication widening, which now follows the plan in full runs too.

## What this does not do

- Analyse anything. The archive holds what the space question needs; deciding
  what to disable, compress or rebuild, and in which order, is the analysis
  layer's job, outside this repository.
- Estimate ROW compression. `041.compression-savings` estimates PAGE on the 20
  largest uncompressed objects, and its header explains why ROW is a follow-up
  on chosen candidates rather than a second sweep.
- Offer any profile other than `space`.

## The review of 14 September 2026

Five readers reviewed the first version: two runs each of agy and of codex, one
with a prompt naming the load-bearing claims and one without, and a fresh Claude
subagent. Each finding was checked before it changed anything here.

What changed, and who established it:

1. `055.page-density` moved behind `--measure-page-density`. The first version
   put it in the default run on the strength of the documented 1 %; a reviewer
   measured about 8.6 % of the pages brought into the buffer pool, and a second
   measurement, above, found 8.4 to 11.7 %.
2. Its cap and counts were renamed to partitions, with `indexes_covered` added.
   Four readers found it, three by running the file on partitioned tables.
3. The replication widening follows the profile ("Selecting the databases"),
   from an overlay test that showed the distribution database listed as covered
   with nothing run against it.
4. The refusal of `--all` moved from `buildOptions` to `optionsFrom`, which the
   real command line calls; `buildOptions` is reached only by tests and the
   wizard.
5. `Check` handles the profile refusal through `ProfileErr`; the first version
   put the refusal in `VerifyLocal` without saying how `Check` would stop.
6. A profile with no member in the corpus is refused; before, an old exported
   corpus produced an empty archive with exit 0, which was measured.
7. The `not_needed` status replaces a `check`-only annotation, after readers
   showed that coverage would read INCOMPLETE and the grant script header would
   contradict itself for rights the profile never uses.
8. The profile block counts members, and `MANIFEST.txt` says membership is not
   execution; the first version gave "collectors" two meanings, 21 members
   against at most 20, now 19, planned.
9. The wizard recomputes the collision on `[p]` and accepts `[g]` on screen 3;
   three readers showed that a same-day space archive could be replaced without
   a banner, and two that `[g]` came before the profile could be chosen.
10. Smaller corrections: `CheckProfile` states that it does not see version and
    permission gates; a refusal caused by lint names the lint error; `check`
    gives outside-profile lint failures their own block; the no-access section
    of the grant script needs a database-scoped script; the 200-character limit
    of job step text is stated; `RunFolderFor` has one caller in
    `collect/collect.go`, not two; a refusal in `Run` leaves a failed-run record,
    which the document now says.

What was rejected, and why:

- "The `IN (union)` form makes `010.objects` 269 times slower." Not reproduced:
  the measurements above show it as fast on 261 tables and three times faster on
  5,001.
- "`UNION` destroys the size ordering." The fragment defines a set, used by
  `IN` in one file and as a join in the other; no output depends on its order.
- "Disabled indexes are excluded although they are rebuild candidates." A
  disabled nonclustered index has no pages to measure, and a disabled clustered
  index makes the table unreadable, which is not a space question.
- "`--keep` does not protect the first run of the day." That is the present
  behaviour of `RunFolderFor`, and the profile suffix does not change it.

## The second review, the same day

The revision was reviewed again by the same five readers, whose prompts said that
the changes were the least trustworthy part of the document. Nothing forced a
change of design. Eleven corrections followed, and every one that could be
measured was measured before it was written.

1. `ProfileChecks` rewrites `denied` only, and never `view_any_definition`. Four
   readers showed that rewriting `error` hides a dropped connection and lets
   `check` exit 0; two that the database discovery depends on that capability.
2. `VerifyServer` returns raw checks, and `Check`, the wizard and
   `deniedPermissions` apply the rule where they read. A verification repeated
   in the wizard after `[p]` would otherwise have lost the raw statuses, and the
   final screen would have counted refusals the manifest did not.
3. The cost is stated per allocation unit, LOB included, with a full read below
   10,000 pages. Two readers measured the two halves; a whole-file run confirmed
   the rule's prediction.
4. `@timeout` is 1800; at 300 the design database loses the whole document.
5. The widening follows the plan. The version and the denied capabilities are
   known before the selection, which the first revision said they were not; the
   order was checked in `Run` and in `VerifyServer`.
6. The refusal text no longer claims silence: the banner is printed first.
7. The corpus is discovered in `optionsFrom` only when a profile is given, with
   a line on the debug timeline.
8. Declaring scripts and members are distinguished, and a profile or a flag
   whose declaring scripts all failed lint is refused with the lint failure.
9. The wizard offers only profiles with a member, clears the grant result on
   `[p]`, and starts on `PlannedCollectors`.
10. Indexed views are measured, which a reader found left out.
11. The grant script tells the DBA to check again with `--profile`.

What was rejected, and why:

- "`collectorsFor` does not derive the option from the flag." It does, in
  `collect/grants.go`.
- "A custom replication collector without `@widened` loses the widening."
  `planUnits` never offered it the widened database.
- "Discovering the corpus in `optionsFrom` noticeably delays the wizard." The
  embedded corpus is read from memory, and the discovery now happens only when
  `--profile` is given, which the wizard never receives.

Verifying these corrections found one more defect, in a correction: the header
line described under "The file".

## Open questions

1. The SQL Server 2012 floor of `055.page-density` rests on the documentation
   of the columns it reads. No 2012 instance is available locally.
2. The per-allocation-unit rule was measured on one Linux container, on extents
   written by fresh and by random inserts. A production file on Windows may read
   differently. The flag makes the cost the operator's decision; it does not
   bound it.
3. `countCollectors`, and so `PlannedCollectors`, counts a database-scoped script
   as a collector even when no database is selected. That predates profiles and
   is left as it is; the wizard's figure and `canStart` inherit it.
