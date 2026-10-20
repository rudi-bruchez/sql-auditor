# Reading the host on request

Status: proposed on 19 October 2026, not implemented; version 2, of 20 October
2026. Version 1 (`6ec7ec3`) was read by a panel of five readers (DeepSeek V4 Pro
and codex, each with a directive and a neutral prompt, and a Claude reader on the
neutral one; DeepSeek replaced agy, out of quota). What changed is summarized at
the end.

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

It is an instantaneous read. It installs nothing, downloads nothing, creates no
shadow copy, collector, session or task, and writes no file but its archive.
What Windows itself records about the command's activity is accepted and
stated in the guide: the WMI queries on `Win32_Shadow*` may start the VSS
service on demand, which stops itself after its idle timeout and leaves a
`7036` event in the System log; process creation is audited where auditing is
on; and if WMI is reached through PowerShell, its own logging records the
command.

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
state and SQL Writer's own log, VM extension logs, Agent job output files, the
failover cluster's configuration.

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
  (`TokenElevation` read through `syscall.OpenProcessToken` and
  `syscall.GetTokenInformation`), not a membership test: a
  non-elevated member of Administrators cannot enumerate filter drivers or read
  every log channel.
- When the instance answers, the machine check is the one of
  `docs/memory-dumps-spec.md`, "The machine check": `ComputerNamePhysicalNetBIOS`
  against `GetComputerNameEx(ComputerNamePhysicalNetBIOS)`. Whichever of the two
  commands is built first writes it; the second reuses it. There is no second
  check by file, since nothing here is listed by the instance with a size.
- On a mismatch, the job output files are skipped, the instance's event
  source is identified as when the instance does not answer (next section), and
  the machine sources (event logs, VSS, filter drivers, power plan, cluster, VM
  extensions) are still read. The manifest names both machines and the run
  exits 2. The typical case is a `.env` pointing at an availability group listener
  while the DBA runs `host` on the node they were asked about: the machine is
  the one the auditor wants, the instance is not. (Decided in this document.)

### When the instance does not answer

The command is most useful precisely when the instance misbehaves, so it does
not need the instance to read the host. If the connection fails, it reads every
host source, skips the job output files, records why in the manifest, and exits
2. The machine check cannot run then; the manifest says it did not, and the host
name it read is in the archive for the auditor to compare.

The instance's event source (`MSSQLSERVER` or `MSSQL$<name>`) is decided in
this order, and the manifest says which rule decided it:

1. The instance answered on the right machine: `SERVERPROPERTY('InstanceName')`,
   `NULL` for a default instance, which gives `MSSQLSERVER`, otherwise
   `MSSQL$<name>`.
2. Otherwise, the `.env` server string names an instance (`HOST\NAME`): the
   source is `MSSQL$NAME` if that service exists locally; if it does not, the
   source is undecided.
3. Otherwise (a bare host, a port, a listener): if exactly one SQL Server
   service exists locally, in any state, its name is the source; if there are
   several, or none, the source is undecided. The Windows Internal Database
   (`MSSQL$MICROSOFT##WID`, `##SSEE`) of WSUS, AD FS or the RD Connection
   Broker is not counted. (Decided while building.)

The services are listed through the service manager opened with
`SC_MANAGER_ENUMERATE_SERVICE` only (`OpenSCManagerW`, then
`EnumServicesStatusExW` over every state). When the source is undecided, the queries that need it (2 and 3b
below) are not run, and the manifest says why.

Those queries, and the job output files, belong to one instance. The machine
sources do not: query 1, the cluster channel and SQL Writer's log cover every
instance on the host, and an archive taken on a host with several instances
names the others' errors and databases. The manifest says so.

## What it reads

All sources are read for one window, `--since`, default 90 days before the local
date, the window the private reader and its topics were written against for
LogScout's event extract. Everything is written as JSON, one file per source,
with stable identifiers (numbers, GUIDs, provider names, event data values) next
to any localized text, so the reader never parses a translated label.

### Event logs, System and Application

Four queries, each with its own cap, so that a chatty log or a busy source
cannot push out the events the analysis needs:

1. Every event of level Critical (1), Error (2) or Warning (3) in the window,
   both logs.
2. Every event of the instance's own source in the Application log, whatever
   its level. SQL Server writes much of what matters at the Information level:
   `833` (slow I/O), `19406` (AG health), `3197` and `3198` (I/O frozen and
   resumed for a snapshot), `18264` and `18265` (backups). The existing
   topics name these in their detection steps; a closed list would miss the
   next one a topic cites. Except `41050` to `41055`, the availability group's
   steps of reconnection to the local cluster service ("waiting for", "started",
   "is online"): on the lab they were more than half of this source's events
   over 90 days (see "Measured on the Windows lab"), enough to push the rest
   out of the cap. They are not dropped silently: a count per identifier, with
   the first and last time, goes in the same file, since a burst of them is the
   trace of a lost connection to the cluster. `41048` and `41049` (the cluster
   service or node became unavailable) stay in the query. (Decided by the
   auditor, 20 October 2026.)
3. Whatever its level, two closed lists, filtered on provider and identifier as
   the private reader does: (a) System, `1074`, `1076` (User32), `6005`,
   `6006`, `6008` (EventLog), `41` (Kernel-Power); (b) Application, the
   instance's source, `17162`, `17147`, `17148` (start and stop). These are
   kept apart from query 2 because `18265` alone writes one event per log
   backup per database, enough to fill any cap in days and push the restarts
   out.
4. On a cluster node, every event of
   `Microsoft-Windows-FailoverClustering/Operational` in the window, whatever
   its level: the history of failovers, lost nodes and resource failures,
   which a `cluster.log` of a few minutes never shows. Written to its own file,
   `025.events-cluster.json`.

- Per event: log, query (1, 2, 3 or 4), time in UTC, level as a number,
  provider name, event id, record id, the values of `EventData` (or
  `UserData`) as strings, and the rendered message cut at 2,000 characters. The
  values hold what the topics need (the file and database of an `833`) without
  parsing localized text, and survive a provider whose message file is gone,
  which renders no text. An event matched by two queries is written once, with
  both.
- What one event holds is bounded, so that the read does not squeeze the
  memory of the server it audits (the harm review measured 4.3 GB for 20,000
  events of 30 KB): each value, and the binary data in hexadecimal, cut at
  2,000 characters like the message, and at most 16 KiB of values per event,
  the values past that left out. `"cut": true` marks an event where the
  message, a value or the binary data was cut; `"data_dropped": N` counts the
  values left out at the end. Both are absent when nothing was. The count-only
  query 2c formats no message, and the events file is written one event at a
  time rather than built whole in memory.
- Caps: 20,000 events for query 1 per log, 20,000 for query 2, none for
  query 3, which is a handful per month, 20,000 for query 4. Newest kept; a
  truncation is recorded with the oldest time kept, so the reader knows counts
  before that time are incomplete.
- Per log or channel, its coverage: the time of its oldest record (the record
  number from `EvtGetLogInfo`, whose event is then read for its time), its
  maximum size, and its `Retention` and `AutoBackup` settings
  (`EvtGetChannelConfigProperty`). On a busy server the Application log wraps
  well before 90 days, and "no event" must not read the same as "log too
  short". With auto-backup on, older events sit in `Archive-*.evtx` files that
  the command does not read; the manifest says so rather than calling the log
  short.

Read through the Event Log API (`EvtQuery`, `EvtNext`, `EvtRender`,
`EvtFormatMessage` in `wevtapi.dll`). Not the classic API behind
`Get-EventLog`, whose entry types (`EventLogEntryType`: Error 1, Warning 2,
Information 4, audits 8 and 16) have no Critical and number levels differently
from the modern API (Critical 1, Error 2, Warning 3, Information 4, Verbose 5).
Event Log XPath has no `starts-with`, which is why query 2 names one exact
source.

### VSS

- Shadow copies: `Win32_ShadowCopy` (id, set id, volume, device object, install
  date, provider id, persistent, client accessible, no auto release, state),
  with each volume mapped to its drive letters or mount points through
  `Win32_Volume`.
- Shadow storage: `Win32_ShadowStorage` (volume, diff area volume, used,
  allocated and maximum space).
- Providers: `Win32_ShadowProvider` (name, id, version). A third-party
  provider is a hint of the backup software, not proof of it: the requester that
  starts a snapshot is not recorded here, and a transient shadow copy is gone by
  the time the command runs.
- SQL Writer's own log, which answers "when were which databases frozen and
  thawed", for every instance of the host: `SqlWriterLogger.txt` and its
  rollover `SqlWriterLogger1.txt` (every file named after the log's stem with
  digits, since the rollover name of a custom `TraceFile` is not measured), in
  `C:\Program Files\Microsoft SQL Server\90\Shared\` unless `TraceFile` in
  `SqlWriterConfig.ini`, in the same folder, names another path, which is then
  used (Learn, "SQL Server VSS Writer logging"). Copied whole, into
  `035.sqlwriter/`; their size is set by `TraceFileSizeMb`, 1 MB by default.
  The log does not name the requester. Absent on an older SQL Writer: recorded
  as absent.
- Not the writers' state. `vssadmin list writers` makes every writer answer an
  `OnIdentify`, which SQL Writer writes in that same log (Learn, same page), so
  running it would add to the very file the auditor reads and rotate its
  history out; it can also hang on the stuck writer under investigation. Writer
  failures reach the Application log under `VSS` and `SQLWRITER`, which query 1
  reads.

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

- `MSCluster_Cluster`: name, quorum type, `SameSubnetDelay`,
  `SameSubnetThreshold`, `CrossSubnetDelay`, `CrossSubnetThreshold`,
  `RouteHistoryLength`, `QuorumArbitrationTimeMax`; the quorum resource
  through the `MSCluster_ClusterToQuorumResource` association, since the class
  has no such property;
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

- The paths come from `msdb.dbo.sysjobsteps.output_file_name`, read by the
  command's own query with the job's name, id (`CONVERT(binary(16), job_id)`)
  and the step's id and name. `queries/50.agent/020.job-steps.sql` reads the
  column too, but not `job_id`. Agent stores the template as typed, tokens
  included.
- Agent tokens are expanded before matching. Tokens are case-sensitive, as
  Agent's are: only the uppercase forms are tokens, anything else is literal
  text. A wrapper applies its escape to the value as Agent does
  (`ESCAPE_SQUOTE` doubles `'`, `ESCAPE_DQUOTE` doubles `"`, `ESCAPE_RBRACKET`
  doubles `]`, `ESCAPE_NONE` leaves it), so a job named with an apostrophe
  matches the file Agent wrote:
  - `SQLLOGDIR` becomes the directory of `SERVERPROPERTY('ErrorLogFileName')`
    (the token exists from SQL Server 2014);
  - `JOBNAME` and `STEPNAME` (both SQL Server 2016 and later), `STEPID` and
    `JOBID` their values; `JOBID` is the binary form, `0x` and 32 hexadecimal digits,
    whose first three groups are byte-swapped from the GUID text (measured on
    SQL Server 2022 by a reader), hence the conversion above;
  - the tokens that vary per run (`DATE`, `TIME`, `STRTDT`, `STRTTM`) become
    wildcards;
  - any other token in the file name becomes a wildcard too, and is recorded
    with the step; any other token in the directory part (`SQLDIR` among them)
    makes the step recorded and not read, since a wildcard directory would
    search the disk.
- The case this exists for is Ola Hallengren's maintenance solution. Its
  current version names the file
  `$(ESCAPE_SQUOTE(JOBNAME))_$(ESCAPE_SQUOTE(STEPID))_$(ESCAPE_SQUOTE(DATE))_$(ESCAPE_SQUOTE(TIME)).txt`
  (`MaintenanceSolution.sql`, since January 2019), in its output directory or
  the log directory; older versions used `JOBID`, `STRTDT` and `STRTTM`. All of
  them expand under the rules above.
- Only local paths on a fixed drive are read: the command reads this
  machine, not a share an elevated account happens to reach. A UNC path, a
  mapped or removable drive, or a symbolic link is recorded with the step and
  not read. A volume mount point or a junction on a local disk, common for log
  directories on a failover cluster instance, can only lead to a local volume
  and is read.
- The directory is judged as typed before it is listed, since listing a share
  already sends the account's credentials to it: its drive letter must be a
  local fixed drive (`GetDriveType`), each of its components is read from its
  parent's listing (`FindFirstFile`, which does not traverse it) and refused
  if it is a symbolic link, and its volume (`GetVolumePathName`) must be a
  local fixed drive. A junction's own target is not walked. The same applies
  to SQL Writer's directory and to the extension logs' folder.
- A link in the directory is not followed, whatever it points to: a matching
  one is recorded with the step, so a planted link cannot export a private
  file under the log's name.
- Each file is opened once, without following it if it is a link
  (`FILE_FLAG_OPEN_REPARSE_POINT`), and judged by the handle it is copied
  from: a link or a directory is refused, and the handle's final path
  (`GetFinalPathNameByHandle`, then `GetVolumePathName` and `GetDriveType`)
  must be on a local fixed drive. Nothing swapped in between a check and an
  open can be read. SQL Writer's log and the extension logs are opened the
  same way.
- Per step, the five newest files written in the window, each cut to its last
  1 MiB.
- A path that matches nothing is recorded with the step, not guessed.
- When `msdb.dbo.sysjobsteps` is refused to the login (`SQLAgentReaderRole`
  reads `sysjobs_view` but not this table, see the header of `020.job-steps`),
  the source is "refused", as `collect` records a refused permission, and does
  not change the exit code. The same holds for a login that may not enter
  `msdb` at all (error 916).

## What it does not read

The Security log, the registry beyond what the APIs above consult, the SQL
errorlog (`collect` already reads it, `10.system/040.error-log`), any database
file, and the instance-bound sources (queries 2 and 3b, job output files) of
any other instance on the host.

## Options

The allowlist pattern of `dumps`: the connection options of `collect`, `--to`,
`--since`, `--debug`. Any other flag with `host` is refused before dispatch.

- `--to <dir>`: required, for the reason `dumps` gives: the working directory of
  an elevated prompt is `C:\Windows\System32`.
- `--since <yyyy-mm-dd>`: start of the window, in local time. Default 90 days
  before today.

## What it writes

One zip in `--to`, named `<instance>-<yyyy-mm-dd>-<hhmmss>.host.zip`, created
exclusively, with one top-level directory of the same name, as `dumps` does.
It is created with a protected DACL, nothing inherited from `--to`, giving
full access to `SYSTEM`, `BUILTIN\Administrators` and the user who ran the
command, and to no one else: under a directory such as `C:\audit` the
inherited ACL gives `BUILTIN\Users` read access, measured with `icacls`.
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
  035.sqlwriter/          SQL Writer's log files
  070.job-outputs/        copied files, one folder per job step
  080.cluster.json
```

`MANIFEST.txt`: tool version, host, instance, run time, window, elevation, the
result of the machine check, how the instance's event source was identified,
and per source its status (read, absent, refused, failed with the error,
skipped because the instance did not answer) and what it left out. It lists
every copied file with its source path and size.

It also says, before the archive is sent, what it may contain that the client
should review: computer, cluster and volume names, addresses, paths, event
messages, and above all the copied text files. Job output files are whatever
the step printed, and a CmdExec or PowerShell step can print a connection
string or a password; Azure extension logs and SQL Writer's log name databases
and accounts. The repository already withholds the text of non-T-SQL job
commands for that reason (`020.job-steps.sql`); this command copies their
output, and says so.

The archive is removed, and the run exits 2, on an error writing the zip or an
interrupt.

## Output and exit codes

On stdout, one line per source with its count, then the archive path as the
last line, as `collect` does. The archive path is printed only when an archive
was written, so a script tells the two cases of code 2 apart by that line.

The codes follow README's "Exit codes", where 0 is a usable result, possibly
degraded:

- 0: an archive was written; every source was read, legitimately absent, or
  refused by a permission.
- 2: partial or refused. An archive was written but a source failed, the
  instance did not answer, or the machine check failed; or no archive was
  written: a configuration the tool will not act on (wrong platform, not
  elevated, usage error, an unreadable `.env` or `SQL_SERVER`, which `host`
  refuses as `collect` does rather than reading the machine without knowing
  the instance), as README already defines 2; or an error writing the zip, or an
  interrupt, after which the archive is removed.
- 1 is not used. README gives 1 to "the instance could not be reached, so
  nothing was collected", and `host` always collects the machine. README's
  "Exit codes" and the DBA guide's "The three exit codes" both gain that
  exception for `host`, and the `dumps` spec, which uses 1 for
  an unreachable instance because it then has nothing to read, keeps it.

## The reading side

The private analysis reads the archive. Its reader is designed separately, in
the private repository. The `host` files are raw: one record per event, English
keys. The LogScout products the topics cite are derived: groups by source,
identifier and level (`groupes`, with `occurrences`, first and last), the
machine and instance restarts (`arrets_demarrages`, `demarrages_instance`), the
period covered (`periode`), filters (`filtres`), the plan's `type`. The
constraint from here is that every field a topic cites today can be rebuilt by
that reader from a `host` archive:

- the groups and restarts from queries 1 and 3, the period from each log's
  coverage and truncation time;
- the filters from `040.filter-drivers.json`;
- the plan's `type` from the scheme GUID, which tells the four standard plans
  apart without the translated name.

Three differences the reader must handle, decided there and not here: whether
the evidence keeps the `logscout:` genre or gains one of its own (a finding
should not credit LogScout for data `host` collected); Critical events, which
LogScout's export folded into another type, and which change group counts; and
the shorter coverage of a wrapped log. Query 2 serves the topics' detection
steps (`833`, `19406`), which today ask for a hand-run `Get-WinEvent`; no
existing product reads it.

## Not covered

- A time window: Perfmon counters and DMV snapshots over an hour. SQL LogScout
  remains the answer, until a later version decides otherwise.
- Launching, downloading or reading SQL LogScout.
- The cluster log: `Get-ClusterLog` writes files on the server, and what the
  analysis needs from it is read from WMI and the Operational channel instead.
  SQL LogScout remains for a detailed trace.
- Anything that creates state: shadow copies, Perfmon collectors, event
  sessions, scheduled tasks; and `vssadmin list writers`, which writes to SQL
  Writer's log (see "VSS").
- `msdb` of another availability group replica: that is `collect` run against
  that replica.
- Hypervisor extensions other than Azure, Linux hosts, the wizard, profiles.

## Constraints on the build

- No PowerShell launched with `-EncodedCommand`. That is what EDRs flag on a
  production server, and the client's EDR is sometimes the subject of the
  audit.
- No new module. `golang.org/x/sys` is only an indirect dependency, and the
  repository keeps it so on purpose (`tui/screen/terminal_windows.go`: the
  tool's public argument is one module beyond the SQL driver). The Windows calls
  go through the standard `syscall` package. Every DLL that is not one Go
  itself loads from the system directory (`wevtapi.dll`, `fltlib.dll`,
  `powrprof.dll`, `advapi32.dll` for the service manager) is loaded by its
  absolute path in the system directory, from `GetSystemDirectoryW` in
  `kernel32.dll`: the process is elevated, and a bare name would let a DLL left
  beside the executable be loaded instead (the `syscall.LoadDLL` documentation
  warns of it). Event Log handles are used from one OS thread
  (`runtime.LockOSThread`), as the API expects.
- WMI serves `Win32_ShadowCopy`, `Win32_ShadowStorage`, `Win32_ShadowProvider`,
  `Win32_Volume`, and the `MSCluster_*` classes in `root\MSCluster`, all
  through `Get-CimInstance` in a visible
  `powershell.exe -NoProfile -NonInteractive -Command`, the executable named by
  its absolute path under the system directory, the script text a constant of
  the source: a COM binding would be a new module. (Decided in the plan.)
- `dumps` is not built yet. `host` does not wait for it: the pieces the two
  share (the flag allowlist, the machine check, the timestamped exclusive zip
  removed on interrupt) are written once, in a place `dumps` will reuse, and the
  plan says where. Both accept `--since yyyy-mm-dd`, with the meaning each
  spec gives it; the sentence of `docs/memory-dumps-spec.md` saying every other
  command refuses `--since` is amended in the same change. (Decided in this
  document.)
- Windows-only code behind build tags; the token expansion, the path boundary
  rules, the instance source choice and the JSON shapes are plain functions
  tested on the Linux CI.
- The rules of `CLAUDE.md`: no client identifier in fixtures, comments or
  messages.

## Measured on the Windows lab, 20 October 2026

Windows Server 2019 (17763), SQL Server 2025 CU9 (17.0.5015.4), a two-node
availability group with node majority quorum, and a domain controller that is
not a cluster node. Everything created for the measures (a persistent shadow
copy, a throwaway database holding Ola Hallengren's solution, its jobs and
their output files) was removed afterwards.

| Question | Measured |
|---|---|
| WMI starts VSS | `Get-CimInstance Win32_ShadowCopy` with VSS stopped: VSS and the software shadow copy provider start, two `7036` events in the System log. |
| SQL Writer's log while held open | `SqlWriterLogger.txt` opens for reading with `FileShare.Read`; no need for a copy of the volume. |
| What SQL Writer logs of a VSS backup | `diskshadow` backup of `C:`: `OnPrepareBackup` with the count and the names of the databases per instance, `OnPrepareSnapshot`, `OnFreeze`, `OnThaw`, `OnBackupComplete`, each with its local time; the trace header gives the UTC offset. No requester is named. The file's own `OnIdentify` lines come from each enumeration of writers, the snapshot's included. |
| SQL Writer's configuration | `SqlWriterConfig.ini` holds `[Logging]` with `EnableTracing`, `TraceLevel`, `TraceFile`, `TraceFileSizeMb`, `ForceFlush`; the service rewrites it at start. |
| Freeze events in the Application log | Same backup: per database, `3197`, `3198`, `3014` and `18264`, source `MSSQLSERVER`, level 4. The `EventData` of `18264` holds the insertion strings (database, dates, pages, LSNs, the virtual device) followed by the event's binary data; the reader keeps the strings, and the binary as hexadecimal. |
| Query 2's volume | 9,626 events of `MSSQLSERVER` over 90 days on a lab that is off most of the time, of which about 4,650 are `41050` to `41055` (reconnection steps to the cluster service, about 780 each). They are now counted instead of kept (query 2); the cap still binds on a busy replica, and the truncation record tells the reader so. |
| Event log sizes | System and Application 20 MB, circular; FailoverClustering Operational 100 MB, circular, 1,824 records, all of level 4 in the window. |
| A persistent shadow copy | `vssadmin create shadow /for=C:`: `Persistent`, `ClientAccessible`, `NoAutoRelease` and `NoWriters` true, `State` 12, `OriginatingMachine` and `ServiceMachine` the host's DNS name; `Win32_ShadowStorage` gives the volume, the diff area, used, allocated and maximum space; `Win32_Volume.DriveType` 3 for a fixed disk. |
| Ola Hallengren's output files | Current version installed: `output_file_name` is `$(ESCAPE_SQUOTE(SQLLOGDIR))\$(ESCAPE_SQUOTE(JOBNAME))_$(ESCAPE_SQUOTE(STEPID))_$(ESCAPE_SQUOTE(DATE))_$(ESCAPE_SQUOTE(TIME)).txt`, and the job wrote `DatabaseIntegrityCheck - SYSTEM_DATABASES_1_20261019_185206.txt` in the errorlog's directory. A job renamed with an apostrophe wrote `...de l''instance_1_....txt`: `ESCAPE_SQUOTE` doubles it in the file name, as the rules above say. |
| `JOBID` | `4BCE4909-FFDB-42F7-BC01-D28BDEF4C9F3` is `0x0949CE4BDBFFF742BC01D28BDEF4C9F3` from `CONVERT(binary(16), job_id)`: the first three groups byte-swapped. |
| A host that is not a cluster node | `root\MSCluster` answers `InvalidNamespace`, and the Operational channel does not exist: both recorded as absent. |
| Quorum without a witness | `MSCluster_ClusterToQuorumResource` returns nothing under node majority (`QuorumType` `Node Majority`): recorded as "no quorum resource", not as a failure. |
| Duration | Through PowerShell, 11 to 16 seconds per event query over 90 days, 0.6 seconds for the cluster classes; the Go reads through the API are to be timed in the build. |
| Acceptance of the built command, 20 October 2026 | `sql-auditor host --to` on SQL01, elevated, integrated authentication: exit 0 in 7 seconds through the system APIs, against 11 to 16 seconds per query through PowerShell. Every JSON file parses; `080.cluster` gives the AG resource with `LeaseTimeout` 20000, `HealthCheckTimeout` 30000, `FailureConditionLevel` 3 and a null quorum; `030.vss` the four Microsoft providers; query 2 kept 5,456 events and counted the 5,469 events `41050` to `41055`, their sum with 3b being the 10,960 events `Get-WinEvent` returns for the same XPath, so the `@SystemTime>=` bound works on the real engine. |
| Job outputs, end to end | A temporary job named `verif_host Check de l'instance` with Ola's current template wrote `verif_host Check de l''instance_1_20261019_220322.txt`; the command read the step through `sysjobs_view`, matched the file and copied it into `070.job-outputs/verif_host Check de l'instance_1/`. Job and file removed afterwards. |
| An instance that does not answer, and a host without SQL Server | On SQL01 with `SQL_SERVER=NOPE01`: exit 2 in 10 seconds, archive written, job outputs `skipped`, event source `MSSQLSERVER` by the single service rule. On DC01: exit 2 in 4 seconds; the cluster channel, the cluster, SQL Writer and the extension logs absent with their reasons; event source undecided. TCP 1433 of SQL01 answers from DC01: the instance did not answer because a WinRM session cannot pass its credentials on (`ANONYMOUS LOGON`), not because the port is closed. |
| `SqlWriterConfig.ini` encoding | Its first bytes are `3B 20 43 6F` (`; Co`): single-byte text, no BOM. |

Still not verified, and why:

- Windows Server 2016: no such machine in the lab. The APIs and classes named
  are documented for it; the first client run on 2016 is the test.
- A non-elevated administrator: WinRM sessions of an administrator are always
  elevated, so the refusal message is tested in the build, by hand, from a
  console.
- Azure VM extension logs: the lab is not on Azure; the folder layout comes
  from Microsoft's documentation, and the first Azure client run is the test.
- A job name with accented letters: the lab measure was spoiled by the
  transport of the T-SQL, not by Agent; to redo from a console.

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
- `41050` to `41055` are counted, not kept, in the instance's events.
- Windows Server 2012 R2 and older are not supported, and the documentation says so.
- On a cluster node, the cluster configuration from WMI and the
  FailoverClustering Operational channel; no `cluster.log`.

Taken in this document, open to change: the command name, the 90-day default,
the caps (20,000 events for query 1 per log and for queries 2 and 4, none for
query 3, 50 MiB of extension logs, five job output files of 1 MiB per step),
reading the host when the
instance does not answer, exit code 2 in that case, the instance source chosen
from the local services, no `vssadmin list writers`, local fixed drives only for
job outputs, Azure only for extensions.

## Review of 20 October 2026

Five readers on version 1. Every finding was taken individually; the expensive
ones were checked against their source before being applied.

Applied:

- `vssadmin list writers` writes to SQL Writer's log (Learn, "SQL Server VSS
  Writer logging", checked): dropped, and that log is read instead, which also
  gives the history the providers could not (codex directive and neutral,
  Claude).
- Ola Hallengren's files are named by `JOBNAME`, not `JOBID`, since 2019
  (`MaintenanceSolution.sql`, three readers); `JOBID` is the byte-swapped
  binary form (Claude, measured on SQL Server 2022); `STEPNAME`, `SQLDIR` and
  unknown tokens handled; `SQLLOGDIR` from 2014.
- Query 2's cap could push the instance's restarts out (Claude): they move to
  the uncapped closed list.
- The fallback to every `MSSQL*` source read other instances' events, against
  "What it does not read" (codex, Claude): the source is now chosen from the
  local services, or the queries are not run. Event Log XPath has no
  `starts-with` (Claude).
- Exit codes contradicted README and themselves, and a refused permission on
  `sysjobsteps` would have given 2 (all readers): rewritten against README,
  with the stdout line as the marker of an archive.
- The window: 90 days, the window of the private reader; per-log coverage
  recorded, since a wrapped log otherwise reads as "no event" (codex, Claude).
- `EventData` values kept beside the rendered message (Claude).
- The reading-side claim was false as written: the `host` files are raw and the
  topics cite derived LogScout products (DeepSeek, codex, Claude); restated as
  "every cited field can be rebuilt", with the differences left to the private
  spec.
- The disclosure understated what copied files hold (codex neutral, DeepSeek
  neutral); job output paths limited to local fixed drives (codex neutral).
- `x/sys/windows` wraps none of the event, filter and power APIs (DeepSeek,
  Claude, codex), and WMI serves more than three classes (DeepSeek neutral,
  Claude); `MSCluster_Cluster` has no quorum resource property (codex
  directive); the `Get-EventLog` rationale confused two numberings (codex
  directive); providers do not name the backup software (codex neutral); the VSS
  service may start on demand (Claude).

A fresh adversarial reader then read only the lines version 2 had added, and
found that they reopened what they closed: "no other instance's events" was
false of query 1 and of SQL Writer's log, which is host-wide (Learn); the
instance source rule picked another instance when the `.env` named a missing
one, and left `InstanceName` `NULL` unmapped; `svc/mgr` connects with full
access; wrappers were removed instead of applied; the reparse rule refused
mount points; `EvtGetLogInfo` gives a record number, not a time; auto-backup
archives were ignored; the caps in "Decisions" did not match the body. All
fixed above.

Rejected:

- `STRTDT` and `STRTTM` "are not Agent tokens" (DeepSeek directive): Learn's
  token table lists both, checked.
- `vssadmin list writers` "is read-only and leaves no state" (DeepSeek
  directive): contradicted by Learn, above.
- The `MSCluster_*` documentation "returns 404" (DeepSeek neutral): codex
  fetched both class pages under `previous-versions/windows/desktop/cluswmi/`;
  the lab measurement stands, Server 2016 stays to verify.
