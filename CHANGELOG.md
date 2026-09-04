# Changelog

Notable changes to sql-auditor. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html) with the caveat that
it is pre-1.0: the minor version moves for features and for behaviour changes
alike. The command surface is still settling, and 1.0 would promise a stability
this tool is not yet in a position to promise.

This file starts at 0.21.0, the first *published* release. Everything before it
is in the git history, which is the honest record of it; cutting that history
into per-release entries after the fact would mean inventing boundaries the
repository never had.

The version is stamped into every binary and recorded in the `MANIFEST.txt` of
every archive, so a collection can always name the build that produced it. The
release workflow refuses a tag that disagrees with either this file or
`cmd/sql-auditor/main.go`.

## [Unreleased]

The corpus goes from 62 collectors to 71. Every one of them closes a gap in
[docs/collection-gaps-spec.md](docs/collection-gaps-spec.md), and the bar for
entry there is that an audit needed the answer, could not find it in an
archive, and had to go back to the client for it.

### Added

- **The host and its operating system** (`10.system/021.host-info.sql`). The
  archive said nothing about the machine, so three questions an audit is
  routinely asked — is the host still supported, does a known fix apply, does
  the memory configuration make sense against the hardware — had no answer in
  it. One file reads whichever of the two views this build has, and reports the
  raw release number rather than mapping it to a product name it cannot know.
- **Transport, authentication and encryption in transit**
  (`10.system/042.connection-security.sql`), aggregated and never per session.
  It carries what the sessions do and, separately, what the server demands:
  forced and encrypted is a configuration, unforced and encrypted is a
  coincidence, and only the pair is a finding.
- **What else is running on this machine** (`10.system/043.cpu-neighbours.sql`
  and `046.local-sessions.sql`): the CPU and memory a neighbour is taking, and
  the sessions that originate on the server itself. Nothing inside SQL Server
  can enumerate a host's processes, so this is how much it is not getting and
  who connects locally — not a process list.
- **The default trace, in aggregate** (`10.system/044.default-trace.sql`). It is
  the only free record of what was done to an instance, with timestamps, and
  nothing read it. Autogrow events carry their duration, because "the log grew
  180 times" is a curiosity and "the slowest took 41 seconds" is the report.
- **Two more ring buffers** decoded (`10.system/047.resource-pressure.sql` and
  `048.security-errors.sql`), plus the exception buffer folded into
  `041.connectivity.sql` as a fourth result set. Resource pressure is the only
  history of memory pressure that covers the whole supported range; the security
  buffer says whether a failed login was a wrong password or a broken SPN.
- **Enterprise-era features persisted in a database**
  (`20.databases/026.persisted-sku-features.sql`), with the edition boundary
  left to the analysis step — since SQL Server 2016 SP1 the presence of these
  features is a licensing conversation and not a defect.
- **Column distribution** (`70.schema/091.statistics-density.sql`), so an index
  key order can be argued from the archive. It estimates the leading column's
  density from the histogram and says so in the column names: the density vector
  itself needs one `DBCC SHOW_STATISTICS` call per statistic, built from
  variables, which the corpus's read-only statement lint refuses by design.
- **`schema_option` on replication articles**, raw and with six bits decoded.
  Without it nothing separated an index that came from replication from one made
  by hand — and with nonclustered index copying off, a reinitialisation drops
  every index on the subscriber. That answer had to be got by mail.

### Changed

- **`20.databases/023.log-vlf.sql` no longer carries a version floor.** The
  condition was never which build this is but whether `sys.dm_db_log_info`
  exists, so the file asks that directly and falls back to `DBCC LOGINFO`,
  naming the mechanism that answered and recording a refusal rather than
  pretending the question was never asked. The old 13.0.5026 gate denied a VLF
  count to every instance below SQL Server 2016 SP2 — which is exactly the
  population whose logs have been growing by percentage increments for years.

### Fixed

- **`90.availability/043.replication-subscriber.sql` reported nothing on a
  subscriber.** It gated on `sys.databases.is_subscribed`, which reads 0 on a
  push subscriber whose database carries the apply procedures the snapshot
  generated, so the collector returned `applies: 0` and every count at zero and
  the topology had to be rebuilt from the publisher's archive. Recognition now
  goes through `MSreplication_subscriptions`, then through those procedures, and
  `applies_source` names the test that answered.

- **Links in the packaged `README.md`** are rewritten to absolute URLs at build
  time, pinned to the tag being released. `docs/` and `.env.example` are not in
  the archive, so from an unpacked copy those links led nowhere — and the reader
  they failed was the DBA sent to the guide before authorising a run. The
  release now stops if any relative link survives packaging.

## [0.21.0] - 2026-09-04

The first release with binaries. Everything below already worked from a
`go build`; what changes is that it can now be downloaded, checked against a
published SHA-256, and tied to the commit and workflow that produced it.

### Added

- **Published archives** for linux/amd64 and windows/amd64, each carrying the
  binary, `LICENSE` and `README.md`, alongside a checksum file and a build
  provenance attestation. Until now a binary somebody handed you could be
  checked against nothing but its own query corpus.

### What the collector is at this version

- **`check`** — connectivity, permissions and configuration, and the full list
  of what a collection would run and which databases it would touch, printed
  before anything is collected. `--grant-script FILE` writes the T-SQL granting
  exactly the permissions found missing, for the login the server reports, with
  a reason for each; the tool never runs it.
- **`collect`** — 62 read-only queries against catalog and dynamic management
  views, written to JSON and packed into a zip with a `MANIFEST.txt` that
  records what ran, what did not, and why.
- **`queries export`** — writes the embedded corpus to disk, so what the
  collector will ask can be read before a run is authorised. `--queries-dir`
  runs a corpus from disk in its place.
- **`env init`** — writes the annotated `.env` template, so the settings this
  tool accepts can be read on a machine that has only the executable.
- **The wizard** — an argument-less run on a terminal opens a four-step
  wizard covering the three things a first run gets wrong.
- **Disclosure is a flag, and the manifest says so.** Session text, object
  definitions, deadlock graphs, blocked process reports and Query Store plans
  are each off by default because each can carry application data or
  credentials; `MANIFEST.txt` records every one of them individually, whether
  it was on or off.
- **The collector takes no locks.** Every query runs under
  `READ UNCOMMITTED` with a `LOCK_TIMEOUT`, and no user or application table is
  read.

### Known limits

- The supported floor is SQL Server 2012. CI exercises 2017 and 2022, the
  oldest and newest images Microsoft publishes; 2012 is verified by hand, and
  what that covers is set out in [docs/verification-2012.md](docs/verification-2012.md).
- The build is not reproducible. The attestation is a statement by the build
  system about what it did, not something you can recompute by compiling.
- Only the linux/amd64 archive is smoke-tested on the runner. The Windows
  build is covered by compiling and by the test suite, not by execution.
