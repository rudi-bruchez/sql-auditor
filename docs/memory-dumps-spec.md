# Sending memory dumps on request

Status: proposed on 17 October 2026, not implemented; version 1. To be read by
the review panel before any code is written from it.

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

## The decision this reverses, and why it still holds

`queries/10.system/071.memory-dumps.sql` says the tool collects the metadata and
never the file, because a dump contains the process address space: page images,
statement text and parameter values from every database on the instance. That
remains true of `collect`, and this spec does not change it:

- `collect` never reads, copies or archives a dump file;
- `dumps` is a separate command, never run by `collect`, never by the wizard,
  never implied by any `.env` setting;
- its archive is a separate file, never merged into the collection archive.

What changes is that the client who decides to send dumps has a tool that picks
the right files and tells them what they contain, rather than a manual
procedure.

## What it reads

1. The instance, through the same connection settings as `collect` (`.env`,
   flags). The rows of `sys.dm_server_memory_dumps`: `filename`,
   `creation_time`, `size_in_bytes`. `VIEW SERVER STATE`, already required by
   `071.memory-dumps`. No new permission.
2. The local file system, for the files of each dump. For a row whose
   `filename` is `<dir>\SQLDumpNNNN.mdmp`, the siblings are
   `<dir>\SQLDumpNNNN.txt` and `<dir>\SQLDumpNNNN.log`. Also
   `<dir>\SQLDUMPER_ERRORLOG.log` once, when present.

It reads nothing else from the server: no other file of the `LOG` folder, no
errorlog, no registry.

## What it packs

| File | Default | Why |
|---|---|---|
| `SQLDumpNNNN.txt` | packed | exception, module list, short stack, input buffer; readable without symbols |
| `SQLDumpNNNN.log` | packed | errorlog snapshot at dump time, carries the build |
| `SQLDUMPER_ERRORLOG.log` | packed | what SQLDumper itself reported |
| `SQLDumpNNNN.mdmp` | only with `--with-minidump` | the process memory: a much wider disclosure, and needed only to read stacks the text file does not show |

The `.txt` is not harmless either: its input buffer is the statement text of
the session that triggered the dump. The manifest says so (below).

## Where it must run

`sql-auditor` runs anywhere that can reach the instance (`docs/dba-guide.md`,
"How it differs from sql-auditor"). `dumps` cannot: it reads files on the
server's disk. It must run on the machine that hosts the instance.

The path alone does not prove it: `C:\Program Files\Microsoft SQL Server\MSSQL16.MSSQLSERVER\MSSQL\Log\SQLDump0001.mdmp`
can exist on the workstation of
a DBA who runs a local instance of the same version, and packing it would send
the wrong server's memory. Before reading any file, `dumps` compares
`SERVERPROPERTY('ComputerNamePhysicalNetBIOS')` with the local computer name,
case-insensitively, on the first 15 characters (the NetBIOS length). On a
mismatch it packs nothing and says where it should be run.

On a failover cluster instance, `ComputerNamePhysicalNetBIOS` is the node that
currently owns the instance, so the command runs on that node.

## What it writes

One zip, beside where `collect` writes its archive, named
`<instance>-<yyyy-mm-dd>-dumps.zip` with the same instance-name sanitizing as
the collection archive. Inside:

- the packed files, under `dumps/`, with their original names;
- `MANIFEST.txt`, which lists each file with its size, creation time and
  SHA-256, says which files hold statement text (every `.txt`) and which hold
  process memory (every `.mdmp`), and states that the archive is to be sent only
  through the channel agreed with the auditor.

## Options

- `--since <yyyy-mm-dd>`: only dumps created on or after that date. Default:
  the last 90 days, because a dump older than that rarely explains a current
  problem and every file sent is a disclosure.
- `--with-minidump`: also pack the `.mdmp` files.
- `--list`: print what would be packed, with sizes, and pack nothing.

## Output and exit codes

On stdout, one line per dump (created at, base name, files found), then the path
of the archive as the last line, as `collect` does. Exit codes:

- 0: an archive was written;
- 1: nothing was packed (no dump in the window, wrong machine, files not
  readable), with the reason on stderr;
- 2: usage error.

A file listed by the view but missing or unreadable on disk is reported by name
and skipped; the others are still packed.

## Not covered

- SQL Server on Linux, whose dumps and core files follow other rules
  (`/var/opt/mssql/log`, `paldumper`).
- Reading or analyzing a dump: that is the auditor's side (the private
  repository, and `sqlautopsy`).
- Sending the archive: the client sends it through the channel agreed with the
  auditor.

## To verify before implementing

- Whether `sys.dm_server_memory_dumps` lists `.mdmp` files only, or `.txt` too.
- Whether a member of the local Administrators group can read the `MSSQL\Log`
  folder without elevation, and what the error looks like when it cannot.
- What `ComputerNamePhysicalNetBIOS` and the local computer name return on a
  machine whose name is longer than 15 characters.

## Documentation

A section of `docs/dba-guide.md`, "Memory dumps, on request", after the
SQL LogScout section: what a dump contains, why it is separate, where to run
the command, what to check in `MANIFEST.txt` before sending.
