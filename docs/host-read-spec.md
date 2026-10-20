# Reading the host on request

Status: proposed on 19 October 2026, not implemented; version 1.

## The gesture

A client has a symptom that the SQL connection only half explains: a `DBCC
CHECKDB` that fails on an availability group primary, backups that wait,
connections that drop. The auditor wants to see what happened on the machine
around it. Is a backup agent or an EDR taking VSS snapshots, and when? Is a
persistent shadow copy sitting on a data volume? Did the VM's backup extension
log a failure that night? What did the maintenance job write in its output
file? What do the System and Application logs say at that hour?

`sql-auditor host` reads those things on the server, in a few seconds, and packs
them into a separate archive that the client's DBA sends back. The DBA runs it
from a mail of instructions, on the server, from an elevated prompt, with the
same `.env` as `collect`. The auditor runs the same command when they have the
hand on the machine.

It is an instantaneous read. It installs nothing, starts nothing that outlives
it, downloads nothing, and writes nothing but its archive.

## Why not SQL LogScout

The DBA guide asks for SQL LogScout when a question needs the host. What the
private analysis actually reads from a LogScout output, counted on 19 October
2026 across the topics that cite it:

| LogScout output | Citations | Needs a time window |
|---|---|---|
| Perfmon counters | 24 | yes |
| System and Application event logs | 32 | no |
| PerfStats DMV snapshots (waits, blocking, schedulers) | 22 | yes |
| File system filter drivers (`fltmc`) | 10 | no |
| Power plan | 5 | no |

The part that needs no window is a handful of reads that Windows answers
directly. LogScout brings, for those, a download to secure, a run of up to an
hour, a Perfmon collector and often an Extended Events session left on the
server, and localized text files that the private reader had to learn to parse
in two languages. Only its data mattered (decision of 19 October 2026), so this
command reads that subset itself, plus what LogScout does not collect: VSS
state, VM extension logs, Agent job output files.

LogScout stays in the guide for what needs a window. That window is out of
scope here (see "Not covered").

## The decision this narrows

The guide's comparison table says of `sql-auditor`: "Where it runs: anywhere
that can reach the instance", "Windows rights: none", "What it writes on the
server: nothing". Those stay true of `collect` and `check`. `host`, like the
proposed `dumps` (`docs/memory-dumps-spec.md`), is a separate command:

- never run by `collect`, by the wizard, by any profile or `.env` setting;
- its archive is a separate file with a name `collect` never produces;
- it needs local administrator rights, and says so before reading anything.

## Where it runs

On Windows, on the machine that hosts the instance, from an elevated prompt.

- Any other build (Linux, macOS) refuses with one sentence naming the machine
  it must run on.
- Windows Server 2016 and later only. Go 1.21 and later do not run on Windows
  Server 2012 or 2012 R2 (Go 1.20 release notes), and the module is on Go 1.26.
  The DBA guide states the requirement, and that is all: no version check in
  the code, and no second binary built with an old Go for systems out of
  Microsoft support since October 2023.
- A process that is not elevated is refused before any read, with "run from an
  elevated prompt". The check is the token's elevation
  (`windows.GetCurrentProcessToken().IsElevated()`), not a membership test:
  a non-elevated member of Administrators cannot run `vssadmin`.
- When the instance answers, the machine check is the one of
  `docs/memory-dumps-spec.md`, "The machine check": `ComputerNamePhysicalNetBIOS`
  against `GetComputerNameEx(ComputerNamePhysicalNetBIOS)`. Whichever of the two
  commands is built first writes it; the second reuses it. There is no second
  check by file, since nothing here is listed by the instance with a size.
- On a mismatch, the instance-bound sources (job output files, the instance's
  own event source) are skipped, and the machine sources (event logs, VSS,
  filter drivers, power plan, VM extensions) are still read, as when the
  instance does not answer. The manifest names both machines and the run exits
  2. The typical case is a `.env` pointing at an availability group listener
  while the DBA runs `host` on the node they were asked about: the machine is
  the one the auditor wants, the instance is not. (Decided in this document.)

### When the instance does not answer

The command is most useful precisely when the instance misbehaves, so it does
not need the instance to read the host. If the connection fails, it reads every
host source, skips the two that need SQL (job output files, the instance's own
event source name), records why in the manifest, and exits 2. The machine check
cannot run then; the manifest says it did not, and the host name it read is in
the archive for the auditor to compare.

## What it reads

All sources are read for one window, `--since`, default 30 days before the local
date. Everything is written as JSON, one file per source, with stable
identifiers (numbers, GUIDs, provider names) next to any localized text, so the
reader never parses a translated label.

### Event logs, System and Application

Three queries, each with its own cap, so that a chatty log cannot push out the
events the analysis needs:

1. Every event of level Critical, Error or Warning in the window, both logs.
2. Every event of the instance's own source in the Application log
   (`MSSQLSERVER` or `MSSQL$<name>`), whatever its level. SQL Server writes much
   of what matters at the Information level: `833` (slow I/O), `19406` (AG
   health), `3197` and `3198` (I/O frozen and resumed for a snapshot), `17162`,
   `17147`, `17148` (start and stop), `18264` (backup completed). A closed list
   of identifiers would miss the next one a topic cites. When the instance did
   not answer, or on a machine mismatch, the source is not known: every source
   named `MSSQLSERVER` or starting with `MSSQL$` is read, and the manifest says
   so.
3. Whatever its level, a closed list in the System log: `1074`, `1076`
   (User32), `6005`, `6006`, `6008` (EventLog), `41` (Kernel-Power).
4. On a cluster node, every event of `Microsoft-Windows-FailoverClustering/Operational`
   in the window, whatever its level: the history of failovers, lost nodes and
   resource failures, which a `cluster.log` of a few minutes never shows.
   Written to its own file, `025.events-cluster.json`.

- Per event: log, query (1, 2 or 3), time in UTC, level as a number, provider
  name, event id, record id, and the rendered message cut at 2,000 characters.
  An event matched by two queries is written once, with both.
- Caps: 20,000 events for query 1 per log, 20,000 for query 2, none for
  query 3, which is a handful per month, 20,000 for query 4. Newest kept; a truncation is recorded
  with the oldest time kept, so the reader knows counts before that time are
  incomplete.

Read through the Event Log API (`EvtQuery`, `EvtRender`, `EvtFormatMessage`), not
`Get-EventLog`, whose levels stop at five and miss Critical.

### VSS

- Shadow copies: `Win32_ShadowCopy` (id, set id, volume, device object, install
  date, provider id, persistent, client accessible, no auto release, state),
  with each volume mapped to its drive letters or mount points through
  `Win32_Volume`.
- Shadow storage: `Win32_ShadowStorage` (volume, diff area volume, used,
  allocated and maximum space).
- Providers: `Win32_ShadowProvider` (name, id, version). A third-party provider
  is what names the backup software.
- Writers: `vssadmin list writers`, the only source without a WMI class. Kept
  as raw text, plus what parses without depending on the language: writer name
  (between quotes), writer id and instance id (GUIDs), state number (between
  brackets). The last-error line is localized and kept as text.

### File system filter drivers

`FilterFindFirst` and `FilterFindNext` from `fltlib.dll`: name, altitude,
instance count, frame. The same data as `fltmc filters`, without its translated
header.

### Power plan

`PowerGetActiveScheme` from `powrprof.dll`: the GUID, which tells the four
standard plans apart in any language, plus the friendly name as text.

### Failover cluster

Only when the machine is a node of a Windows Server Failover Cluster (the
`root\MSCluster` WMI namespace exists and answers); otherwise recorded as
absent, not an error. Read through WMI, which writes nothing:

- `MSCluster_Cluster`: name, quorum type and resource, `SameSubnetDelay`,
  `SameSubnetThreshold`, `CrossSubnetDelay`, `CrossSubnetThreshold`,
  `RouteHistoryLength`, `QuorumArbitrationTimeMax`;
- `MSCluster_Node`: name, state, node weight;
- `MSCluster_Network`: name, role, address and mask, state;
- `MSCluster_ResourceGroup`: name, state, owner node, failover threshold and
  period;
- `MSCluster_Resource`: name, type, state, owner group, `RestartAction`,
  `RestartThreshold`, `RestartPeriod`, `RetryPeriodOnFailure`,
  `PendingTimeout`, and all its private properties. For a resource of type
  `SQL Server Availability Group` these are `LeaseTimeout`,
  `HealthCheckTimeout` and `FailureConditionLevel`, the settings the lease
  questions turn on.

Measured on the lab on 19 October 2026 (Windows Server 2019, two nodes, one
availability group): every value above came back from WMI, the AG resource's
private properties included. A `cluster.log` generated over five minutes
(`Get-ClusterLog -TimeSpan 5`, 3.6 seconds, 800 KB, 3,100 lines, 2,300 of them
`DiagnosticVerbose`) has a header with the runtime state (views, quorum, failure
counts, last failover time) but none of these settings, and it overwrites
`C:\Windows\Cluster\Reports\Cluster.log` on the server. It is therefore not
read (see "Not covered").

### VM extension logs

Azure only in this version: the tree under `C:\WindowsAzure\Logs\Plugins\`.

- The listing of every file (path, size, last write time) goes in the archive.
- The files written in the window are copied, newest first, up to 50 MiB in
  total; the rest are listed as left out.
- No folder means not an Azure VM, or no extension: recorded as absent, not an
  error.

### Agent job output files

Only when the instance answered.

- The paths come from `msdb.dbo.sysjobsteps.output_file_name`, the column
  `queries/50.agent/020.job-steps.sql` already reads.
- Agent tokens are expanded before matching. `$(ESCAPE_SQUOTE(SQLLOGDIR))`
  becomes the directory of `SERVERPROPERTY('ErrorLogFileName')`,
  `$(ESCAPE_SQUOTE(JOBID))` and `STEPID` their values, and the tokens that vary
  per run (`DATE`, `TIME`, `STRTDT`, `STRTTM`) become wildcards. Ola
  Hallengren's jobs write `DatabaseIntegrityCheck_<jobid>_<stepid>_<date>_<time>.txt`
  in the log directory, which is the case this exists for.
- Per step, the five newest files written in the window, each cut to its last
  1 MiB.
- A path that matches nothing, or a token the command does not know, is
  recorded with the step, not guessed.

## What it does not read

The Security log, the registry beyond what the APIs above consult, the SQL
errorlog (`collect` already reads it, `10.system/040.error-log`), any database
file, and any other instance on the host.

## Options

The allowlist pattern of `dumps`: the connection options of `collect`, `--to`,
`--since`, `--debug`. Any other flag with `host` is refused before dispatch.

- `--to <dir>`: required, for the reason `dumps` gives: the working directory of
  an elevated prompt is `C:\Windows\System32`.
- `--since <yyyy-mm-dd>`: start of the window, in local time. Default 30 days
  before today.

## What it writes

One zip in `--to`, named `<instance>-<yyyy-mm-dd>-<hhmmss>.host.zip`, created
exclusively, with one top-level directory of the same name, as `dumps` does.
When the instance did not answer, `<instance>` is the local NetBIOS name.

```
<name>/
  MANIFEST.txt
  010.events-system.json
  020.events-application.json
  025.events-cluster.json
  030.vss.json
  040.filter-drivers.json
  050.power-plan.json
  060.vm-extensions.json
  060.vm-extensions/      copied files, original relative paths
  070.job-outputs.json
  070.job-outputs/        copied files, one folder per job step
  080.cluster.json
```

`MANIFEST.txt`: tool version, host, instance, run time, window, elevation, the
result of the machine check, and per source its status (read, absent, failed
with the error, skipped because the instance did not answer) and what it left
out. It also says what the archive may contain that the client may want to
review: computer and volume names, paths, the text of job outputs (database
names, error messages), event messages.

The archive is removed, and the run exits 2, on an error writing the zip or an
interrupt.

## Output and exit codes

On stdout, one line per source with its count, then the archive path as the
last line, as `collect` does.

- 0: every source read or legitimately absent.
- 2: an archive was written but a source failed, or the instance did not
  answer; or nothing was read (wrong platform, not elevated, wrong machine,
  usage error).
- 1 is not used: a host that answers is always worth an archive.

## The reading side

The private analysis reads the archive. Its reader is designed separately, in
the private repository. One constraint from here: the topics that cite the
event logs, filter drivers and power plan from LogScout today must resolve from
a `host` archive without being rewritten. Checked against them on 19 October
2026: they read groups by source, identifier and level over Error and Warning,
the start and stop identifiers of the machine and of the instance, and
Information events of the instance's source (`833`, `19406`). Queries 1 to 3
cover all of these. What a `host` archive lacks is the Information level of
every other source, which no topic reads.

## Not covered

- A time window: Perfmon counters and DMV snapshots over an hour. SQL LogScout
  remains the answer, until a later version decides otherwise.
- Launching, downloading or reading SQL LogScout.
- The cluster log: `Get-ClusterLog` writes files on the server, and what the
  analysis needs from it is read from WMI and the Operational channel instead.
  SQL LogScout remains for a detailed trace.
- Anything that creates state: shadow copies, Perfmon collectors, event
  sessions, scheduled tasks.
- `msdb` of another availability group replica: that is `collect` run against
  that replica.
- Hypervisor extensions other than Azure, Linux hosts, the wizard, profiles.

## Constraints on the build

- No PowerShell launched with `-EncodedCommand`. That is what EDRs flag on a
  production server, and the client's EDR is sometimes the subject of the
  audit. Prefer direct API calls through `golang.org/x/sys/windows`, already a
  dependency; WMI is the exception for the three `Win32_Shadow*` classes. How
  WMI is reached (a new dependency, or a visible `powershell.exe -Command` with
  `Get-CimInstance`) is the builder's call, stated in the plan.
- `dumps` is not built yet. `host` does not wait for it: the pieces the two
  share (the flag allowlist, the machine check, the timestamped exclusive zip
  removed on interrupt) are written once, in a place `dumps` will reuse, and the
  plan says where. Both accept `--since yyyy-mm-dd`, with the meaning each
  spec gives it; the sentence of `docs/memory-dumps-spec.md` saying every other
  command refuses `--since` is amended in the same change. (Decided in this
  document.)
- Windows-only code behind build tags; the parsing of `vssadmin` text, the token
  expansion and the JSON shapes are plain functions tested on the Linux CI.
- The rules of `CLAUDE.md`: no client identifier in fixtures, comments or
  messages.

## To verify on the Windows lab before implementing

The lab (Windows Server 2019, SQL Server 2025, an availability group) answers
most of these; the French Windows 11 VM answers the language ones.

- Whether `3197`, `3198` and `18264` reach the Application log, with which
  source, during a VSS backup of a database, and how many events query 2
  returns over 30 days on an instance that logs every backup.
- What `Win32_ShadowCopy` says of a persistent, client-accessible shadow
  (`vssadmin create shadow`, then deleted).
- `vssadmin list writers` on the French VM: which parts parse without the
  language.
- The `output_file_name` of a stock Ola Hallengren installation, and the token
  values Agent writes in the file name (format of `JOBID`).
- The elevation check from a non-elevated administrator.
- How long the whole read takes on a VM with 30 days of logs.
- `root\MSCluster` on Windows Server 2016, and what a host that is not a
  cluster node answers when the namespace is queried.

## Documentation

- `docs/dba-guide.md`: a section "Reading the host, on request", before the
  SQL LogScout section: what is read, why it needs an elevated prompt, what the
  archive may contain, what to check in `MANIFEST.txt`. The SQL LogScout
  section says that `host` covers the event logs, filters and power plan, and
  that LogScout is asked for only for a time window. The comparison table
  gains the exception of `host`.
- `README.md`, "Commands": the new command.
- The client mail template (private repository) gains the `host` command.

## Decisions

Taken by the auditor, 19 October 2026:

- The client's DBA runs it from a mail of instructions; the auditor runs it
  too when they have the machine.
- Only LogScout's data matters: the command reads the subset itself and does
  not launch LogScout.
- An instantaneous read is enough for the first version.
- Windows Server 2012 R2 and older are not supported, and the documentation says so.
- On a cluster node, the cluster configuration from WMI and the
  FailoverClustering Operational channel; no `cluster.log`.

Taken in this document, open to change: the command name, the 30-day default,
the caps (20,000 events per log, 50 MiB of extension logs, five job output files
of 1 MiB per step), reading the host when the instance does not answer, exit
code 2 in that case, Azure only for extensions.
