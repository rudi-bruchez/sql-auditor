# SQL Server 2012 verification checklist

The project claims a SQL Server 2012 (11.x) floor. This file exists so that
claim traces to a recorded artifact instead of to somebody's memory of having
checked once.

**Status: the 2012 pass has not been run.** The sections below are written in two
parts — what has been verified and by what method, and what has not. The value
columns in [The pass](#the-pass) are deliberately empty. Fill them in when
someone runs it, and commit the result.

## What has been verified

### Static parse under the 2012 grammar

This half is reproducible, and you should reproduce it rather than believe the
paragraph. `tools/verify-corpus-grammar.ps1` parses every file in the corpus
with `Microsoft.SqlServer.TransactSql.ScriptDom`, under the T-SQL grammar
matching that file's own declared floor — `TSql110Parser`, the SQL Server 2012
grammar, for the four ungated collectors — and checks each file's declared
`@resultsets` count against the number of result-returning top-level `SELECT`
statements in the parse tree.

```
pwsh -File tools/verify-corpus-grammar.ps1
```

It exits 0 when everything passes and 1 otherwise. The output of the last run is
committed as [`verification-2012-parse.txt`](verification-2012-parse.txt),
recording the date, the ScriptDom build used, and the git tree object of
`queries/` — which is what tells you the artifact still describes the corpus in
front of you:

```
git rev-parse HEAD:queries
```

If that hash differs from the `Corpus tree` line in the artifact, the corpus has
changed since the parse and the artifact is stale; re-run the script. (The stamp
is the corpus tree rather than a commit id on purpose: the artifact is always
committed one commit after the run that produced it, so a commit stamp would
name the commit before the one containing the file, and amending to fix it would
change the id again.)

As recorded, all 14 files parse with zero errors and every result-set count
matches — including the four that carry the 2012 claim:

> **The corpus has grown since this pass.** It is 28 files today. The 14 below
> are the ones this run covered, and the number is left as recorded rather than
> updated, because the record describes a run that happened. The corpus tree
> hash above is what tells you the artifact is stale; the collectors added since
> have not been through this parse.

| Query | `@resultsets` declared | Counted from the parse tree |
| --- | --- | --- |
| `10.system/010.properties.sql` | 6 | 6 |
| `10.system/050.tempdb.sql` | 11 | 11 |
| `20.databases/010.all-databases.sql` | 1 | 1 |
| `20.databases/020.properties.sql` | 7 | 7 |

The predecessor's originals fail the same parse with "XML expected, JSON found",
which is independent confirmation that the rework was necessary rather than
cosmetic. That comparison is not reproducible from this repository — the
originals are not in it — so take it as background rather than as evidence.

### Version applicability of every column

This half is **not** reproducible from the repository, and it is the weaker of
the two. Every column referenced by a collector was checked by hand against its
Microsoft Learn page for the version in which it appeared. There is no script
and no recorded output; the evidence is the `@min_version` directives themselves
and the review history. Read it as a careful manual pass, not as a check you can
re-run.

Columns that postdate 2012 were moved into separate files carrying a
`@min_version` directive, so the ungated collectors reference only what 2012
provides. The gated files and their floors:

| File | `@min_version` | Version |
| --- | --- | --- |
| `10.system/012.soft-numa.sql` | `13` | SQL Server 2016 |
| `10.system/013.memory-model.sql` | `13.0.4001` | SQL Server 2016 SP1 |
| `10.system/014.cpu-topology.sql` | `13.0.5026` | SQL Server 2016 SP2 |
| `10.system/051.version-store.sql` | `13.0.5026` | SQL Server 2016 SP2 |
| `20.databases/011.all-databases-2014.sql` | `12` | SQL Server 2014 |
| `20.databases/012.all-databases-query-store.sql` | `13` | SQL Server 2016 |
| `20.databases/021.properties-2014.sql` | `12` | SQL Server 2014 |
| `20.databases/022.query-store.sql` | `13` | SQL Server 2016 |
| `20.databases/023.log-vlf.sql` | `13.0.5026` | SQL Server 2016 SP2 |

The gate compares dotted versions rather than the major component alone,
because a major-only gate would let 2016 RTM attempt columns that arrived in
2016 SP2 and fail with `Invalid column name`.

One known limitation of the numeric gate: `sql_memory_model_desc` has disjoint
applicability — 2012 SP4 and 2016 SP1 and later, but no 12.x — which no single
numeric floor can express. It sits behind the 2016 SP1 gate, so a 2012 SP4
instance will not collect a column it does in fact have.

## What has not been verified

**No collection has ever been executed against SQL Server 2012, or against any
version other than SQL Server 2022 (16.0).** Everything above is static
analysis: a parse and a documentation check. Neither can detect

- a DMV that exists on 2012 but returns a different shape or type than expected;
- a `SERVERPROPERTY` name that is valid on 2022 and returns `NULL` on 2012
  (`SERVERPROPERTY` never errors on an unknown name, so a wrong or newer name is
  silently `NULL` forever);
- a permission that behaves differently on 2012;
- a runtime failure in a code path the parser accepts;
- an encoding difference in how the driver reports a 2012 column's type.

Until the table below is filled in, "supports SQL Server 2012" should be read as
"written for 2012 and statically checked against it", not "run on it".

## The pass

Run this against a real SQL Server 2012 instance and record the results.

### Record

| Field | Value |
| --- | --- |
| Date | |
| Operator | |
| `SELECT @@VERSION` (full output) | |
| `SERVERPROPERTY('ProductVersion')` | |
| `SERVERPROPERTY('Edition')` | |
| sql-auditor commit tested | |
| sql-auditor version reported by `sql-auditor version` | |
| Instance address used | |
| Login used, and its granted rights | |

### Commands run

Record the exact command lines and their exit codes.

| Command | Exit code | Notes |
| --- | --- | --- |
| `sql-auditor version` | | |
| `sql-auditor check` | | |
| `sql-auditor collect` | | |

### The four ungated collectors

These four carry no `@min_version` and therefore must run on 2012. Each is the
subject of the claim. Record, for each: did it complete, how many result sets
came back, and whether the output JSON is well-formed and populated.

| Query | Ran without error | Result sets (expect) | Result sets (actual) | Output non-empty | Notes |
| --- | --- | --- | --- | --- | --- |
| `10.system/010.properties.sql` | | 6 | | | |
| `10.system/050.tempdb.sql` | | 11 | | | |
| `20.databases/010.all-databases.sql` | | 1 | | | |
| `20.databases/020.properties.sql` | | 7 | | | |

### The gated collectors

On a 2012 instance all nine should be skipped, each with a reason recorded in
`MANIFEST.txt` under "Queries not run" of the form `needs SQL Server 12 or
later; this instance reports 11.0.7001.0` — the gate on the left, the
instance's own `ProductVersion` on the right. Confirm none of them ran and none
produced an error.

| Check | Result |
| --- | --- |
| All nine version-gated collectors appear under "Queries not run" | |
| None of them appears under "Errors" | |
| `10.system/052.session-text.sql` skipped as flag-gated (run without `--include-session-text`) | |

### Cross-checks

| Check | Result |
| --- | --- |
| `MANIFEST.txt` Coverage section reads COMPLETE (with a fully granted login) | |
| Server name, version and edition in `MANIFEST.txt` match `@@VERSION` | |
| Every JSON file parses as valid JSON | |
| `_run.json` lists no errors | |
| `collect` exit code is `0` | |
| The zip opens and contains `MANIFEST.txt` and `_run.json` | |
| `050.tempdb.sql` issues `USE tempdb` and the session is returned to the default database afterwards (subsequent per-database collectors target the right database) | |

### Anything that behaved differently from 2022

Record here, even if it did not fail.

*(empty)*

### Verdict

| Field | Value |
| --- | --- |
| Does the 2012 claim hold as written | |
| Changes required | |
| Signed off by | |
