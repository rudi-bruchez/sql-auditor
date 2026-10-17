# Sending memory dumps on request

Status: proposed on 17 October 2026, not implemented; version 2, of 17 October
2026. Version 1 (`20e5bc0`) was read the same day by a panel of five readers
(agy and codex, each with a directive and a neutral prompt, and a Claude
reader on the neutral one), without a SQL Server lab. Version 2 answers them;
what they found and what became of each finding is in "Review of 17 October
2026" at the end.

## The gesture

When `071.memory-dumps` shows that an instance has written dumps, the auditor
wants to read them: the exception, the faulting stack, the third-party modules
loaded in the process and the input buffer say what the instance met, which the
dates alone cannot. Today the client has to find the files, choose which ones to
send, and zip them by hand, with no guidance on what they contain.

`sql-auditor dumps` packs the dump files of the instance into a separate archive
that the client reviews and sends knowingly. It is to dumps what the
SQL LogScout section of the DBA guide is to host data: a separate request, a
separate archive, decided separately.

## The decision this narrows

`queries/10.system/071.memory-dumps.sql` says the tool collects the metadata and
never the file, "and this tool does not transport it", because a dump contains
the process address space: page images, statement text and parameter values
from every database on the instance. With this command that sentence becomes
false for the tool as a whole and stays true of `collect`:

- `collect` never reads, copies or archives a dump file;
- `dumps` is a separate command, never run by `collect`, never by the wizard,
  never implied by any `.env` setting or profile;
- its archive is a separate file with a name `collect` never produces.

The comment of `071`, and the guide where it says the same thing, are updated
with the command (see "Documentation").

## Where it runs

`sql-auditor` runs anywhere that can reach the instance. `dumps` reads files on
the server's disk, so it packs only when run on the machine that hosts the
instance, on Windows. On any other build (Linux, macOS) it refuses to pack and
says why; `--list` still works there.

It refuses an instance whose `sys.dm_os_host_info.host_platform` is not
`Windows`: dumps of SQL Server on Linux follow other rules
(`/var/opt/mssql/log`, `paldumper`) and are not covered.

### The machine check

Before reading any file, `dumps` compares
`SERVERPROPERTY('ComputerNamePhysicalNetBIOS')` with the local NetBIOS name,
read with `windows.GetComputerNameEx(windows.ComputerNamePhysicalNetBIOS)` from
`golang.org/x/sys/windows` (already a dependency in `go.mod`), for exact
equality, case-insensitively. Not `os.Hostname`, which on Windows returns the
physical DNS host name (`$GOROOT/src/os/sys_windows.go`), and not a prefix
comparison, which accepts two machines whose names share their first fifteen
characters.

NetBIOS names are unique within a domain but not across domains, so equal names
do not prove it is the same machine. The second check is the file itself: for
each row, the `.mdmp` at the row's path must exist and its size must equal the
row's `size_in_bytes`. A row that fails it is not packed and is reported. When
no row passes, nothing is packed.

On a mismatch of names, nothing is read, and the message names the machine the
command must run on. On a failover cluster instance that is the node that
currently owns the instance (`ComputerNamePhysicalNetBIOS` follows it), and the
dump folder is normally on the shared disk that moved with it.

### The Windows account

The command reads `MSSQL\Log`, which setup normally grants to the service
SID and to Administrators. Run it from an elevated prompt; an access-denied
error on a file says so ("run from an elevated prompt"). To verify on a lab:
whether a non-elevated member of Administrators is refused, and the exact error.

The SQL connection uses the same settings as `collect` (`.env`, flags). Windows
authentication as the administrator who runs it works when that login holds the
permission below; otherwise a SQL login in `.env`.

## What it reads

1. From the instance, the rows of `sys.dm_server_memory_dumps`: `filename`,
   `creation_time`, `size_in_bytes`. The permission is the one `check` already
   probes for that view: `VIEW SERVER STATE`, or `VIEW SERVER PERFORMANCE STATE`
   on SQL Server 2022 and later (Learn, `sys.dm_server_memory_dumps`, and the
   DBA guide's permission table). The command runs that same probe first. The
   header of `071` names only `VIEW SERVER STATE`; that is a separate fix.
2. From the local disk, the files of each row. The view lists the `.mdmp` file
   only (Learn). Its siblings are the same path with the extension replaced by
   `.txt` and by `.log`, whatever the name: `SQLDumpNNNN.mdmp`, a manual
   `SQLDmprNNNN.mdmp`, or a trace flag 2616 `SQLDmpr...dmp`. A sibling that
   does not exist is reported as absent, not as an error.

Paths are parsed as Windows paths by string operations on `\` and `/`, never
with `path/filepath`, so that the parsing runs and is tested on the Linux CI;
only the reading of the files is Windows-only.

It reads nothing else: no other file of the `LOG` folder, not
`SQLDUMPER_ERRORLOG.log` (it covers every dump ever written, so it would ignore
`--since`), no errorlog, no registry.

## What it packs

| File | Default | Contains |
|---|---|---|
| `.txt` | packed | exception, module list, short stack, formatted session structures, and the input buffer: the statement text of the session that triggered the dump |
| `.log` | packed | the errorlog at dump time, with the build, and logins and client addresses |
| `.mdmp` | only with `--with-memory-dump` | process memory: a mini, filtered or full dump, which the extension does not tell apart |

Packing the `.txt` by default, although it carries statement text, is
deliberate: the command is itself the opt-in, run by someone who has been asked
for these files, and the text file is what makes a dump readable at all. The
manifest and the last lines of stdout say which files carry statement text.

### The memory dump

`--with-memory-dump` (not "minidump": the view lists mini, all-thread and full
dumps under the same extension, and a full dump is the size of the process
memory, up to hundreds of GB, per Learn, "Use Sqldumper.exe").

- It packs only `.mdmp` files of at most `--max-dump-mb` megabytes, default
  200, the ceiling the DBA guide already gives for SQL LogScout. A larger one is
  listed with its size and skipped.
- Before writing, it checks that the destination volume has free space for the
  selected files plus 10 %, and refuses otherwise.

## Options

All three are accepted by `dumps` only. Every other command refuses them, and
`dumps` refuses the options of `collect` (`--all`, `--profile`, `--keep`,
`--include-*`, `--max-duration`), by the same mechanism `main.go` already uses
for `--profile` and `--grant-script` on the shared flag set. `isCommand`, the
usage text and the "did you mean" message gain `dumps`.

- `--to <dir>`: where to write the archive. Required, no default: the working
  directory of an elevated prompt is `C:\Windows\System32`, and `OUTPUT_DIR`
  may be a shared folder chosen for metadata. A file in `<dir>` inherits its
  ACL; the guide's "Where you leave it matters" applies with more force.
- `--since <yyyy-mm-dd>`: only dumps whose `creation_time`, a `datetimeoffset`
  in the server's offset, falls on or after that date in that same offset. The
  filter is applied in T-SQL (`CONVERT(date, creation_time) >= @since`), so the
  client's local time zone plays no part. Default: 90 days before the server's
  current date. Every file sent is a disclosure, and a dump older than that
  rarely explains a current problem; `--list` shows the older ones.
- `--with-memory-dump` and `--max-dump-mb <n>`: above.
- `--list`: print every row of the view, marked in or out of the window, with
  the files found beside it and their sizes when run on the host. Packs nothing,
  writes nothing, needs no `--to`, and runs from any machine (the file columns
  then say "not on this machine").

## What it writes

One zip in `--to`, named
`<instance>-<yyyy-mm-dd>-<hhmmss>.dumps.zip`, where `<instance>` is the
`SafeFolderName` of the server name `collect` uses. The double extension is a
shape `RunFolderName` never produces, whatever the profile. The time in the name
means a second run the same day (typically after adding `--with-memory-dump`)
never meets the first; the file is created exclusively (`O_EXCL`), as
`collect.Zip` does, and removed if the run fails or is interrupted. The
interrupt message names the dumps archive, not a collection.

Inside, one top-level directory of the same name without `.zip`, as
`collect.Zip` keeps, so the archive cannot explode into the recipient's working
directory:

- `dumps/`, the packed files with their original names. Two rows from different
  folders with the same file name get a `-2`, `-3` suffix, and the manifest
  records each file's source path;
- `MANIFEST.txt`: tool version, instance, host, run time, the options used, and
  for each file its source path, size, the row's `creation_time` and its
  SHA-256, computed while the file is copied into the zip in one pass; which
  files carry statement text (`.txt`), logins and addresses (`.log`), process
  memory (`.mdmp`); and every row or file skipped, with the reason.

## Output and exit codes

On stdout, one line per dump (created at, name, files packed), then the archive
path as the last line, as `collect` does. With `--list`, the listing only.

The codes follow the guide's "The three exit codes":

- 0: a usable result. An archive holding every file selected, or `--list`
  printed, or no dump in the window (said on stderr, no archive written).
- 1: the instance did not answer.
- 2: unusable or partial. Wrong machine, wrong platform, the permission missing,
  no space, a usage error; or an archive written with files missing or skipped
  (the archive is kept, its manifest and stderr name what is missing).

## Not covered

- SQL Server on Linux, and Azure SQL.
- Reading or analyzing a dump: that is the auditor's side.
- Sending the archive: the client sends it through the channel agreed with the
  auditor.

## To verify on a Windows lab before implementing

- The ACL of `MSSQL\Log` and the error a non-elevated administrator gets.
- Whether `ComputerNamePhysicalNetBIOS` equals
  `GetComputerNameEx(ComputerNamePhysicalNetBIOS)` on a machine whose DNS name
  is longer than 15 characters, and on an FCI node.
- What `sys.dm_server_memory_dumps` returns for a manual `SQLDmpr` dump and
  under trace flag 2616.

## Documentation

- A section of `docs/dba-guide.md`, "Memory dumps, on request", after the
  SQL LogScout section: what each file contains, why it is separate, where and
  how to run the command, what to check in `MANIFEST.txt` before sending.
- The same guide, where it says the tool never transports a dump, and its
  comparison table ("Where it runs: anywhere", "Windows rights: none"): both
  gain the exception of `dumps`.
- The header of `071.memory-dumps.sql`: "this tool does not transport it"
  becomes "`collect` does not transport it", with a pointer to `dumps`.

## Review of 17 October 2026

Five readers on version 1. All findings, merged where several readers made the
same one, and what version 2 does with each.

| Finding | Readers | Version 2 |
|---|---|---|
| The 15-character prefix comparison accepts two machines sharing a prefix and can refuse the right one; `os.Hostname` returns the DNS name | all five | exact comparison with `GetComputerNameEx(ComputerNamePhysicalNetBIOS)`, plus the size check of each `.mdmp` |
| `--with-minidump` also packs full dumps, without any size ceiling or space check | Claude, codex ×2, agy ×2 | renamed `--with-memory-dump`; 200 MB ceiling, free-space check |
| `filepath` does not split `\` on the Linux CI | Claude, agy neutral (both ran it) | string parsing, Windows-only reading |
| The shared flag set would accept the new options on every command, and collect's on `dumps` | Claude, agy ×2 (ran it) | refused both ways, `isCommand` and usage updated |
| Exit codes contradict the guide; `--list` always exits 1; a partial archive exits 0 | all five | aligned on the guide's three codes |
| Same-day rerun undefined; `collect.Zip` is exclusive | all five | time in the name, `O_EXCL`, removal on failure |
| The archive name can collide with `collect --profile dumps` | Claude, agy directive | `.dumps.zip`, a shape `RunFolderName` cannot produce |
| No top-level directory in the zip; duplicate names across folders | agy ×2 | top-level directory, suffixes, source path in the manifest |
| `SQLDUMPER_ERRORLOG.log` ignores `--since` | Claude, codex neutral | no longer packed |
| `--since` has no time zone | agy ×2 | filtered in T-SQL in the server's offset |
| `VIEW SERVER PERFORMANCE STATE` on 2022 and later | codex ×2, agy ×2 | the existing version-aware probe |
| `OUTPUT_DIR` and the elevated prompt's working directory are wrong places for a dump | Claude, codex neutral | `--to` required |
| `SQLDmpr` and trace flag 2616 names | Claude, codex directive, agy directive | sibling rule on the stem, whatever the extension |
| The `.log` carries logins and client addresses | Claude | in the manifest's classification |
| Manifest lacks skipped files, options, tool metadata | agy neutral | added |
| Nothing ties the file on disk to the row | Claude | size check |
| The guide's statements and `071`'s comment become false | Claude | listed under "Documentation" |
| Ctrl-C prints the collect message and may leave a partial zip | agy neutral | stated |
| Hashing a large file twice | agy directive, codex directive | one pass while copying |
| Non-elevated administrators cannot read `MSSQL\Log` | agy neutral (from Learn) | elevated prompt required, to verify on a lab |
| Linux instances | Claude | refused on `host_platform` |

Not adopted:

- Packing the `.txt` by default contradicts the guide's treatment of statement
  text (`--include-session-text` off by default): codex ×2, agy directive. The
  guide's rule is about what a default collection carries; here the command is
  itself the deliberate request, and without the text file a dump is unreadable
  to the auditor. Kept, with the classification in the manifest and on stdout.
- agy directive's fourth verified finding argues against a rule that `071`
  should strip the folder from `filename`. Version 1 contains no such rule; the
  finding rests on a misreading and is set aside.
- The FCI case where the crash dump sits on the previous owner's local disk
  (agy directive): the dump folder of an FCI is on its shared disk by default; a
  file not found is reported, which is the right outcome.
