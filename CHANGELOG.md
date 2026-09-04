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

### Fixed

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
