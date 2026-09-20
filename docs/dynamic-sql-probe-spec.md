# Saying so when deferred reads cannot run

Status: WITHDRAWN on 20 September 2026, the day it was written, before any
code came out of it. The panel that reviewed it broke its premise, and the
document is kept because what it proposed is the obvious thing to propose
again, and because the reasons not to are measurements rather than opinions.

Why it is withdrawn, in the order the reasons arrived:

- The failure is not a property of the collation. A reviewer restarted a BIN2
  container and `sp_executesql` worked, collation unchanged, and the author
  reproduced that. The state is "the default collation was changed on this
  running instance", which a container built with `MSSQL_COLLATION` is in on
  its first boot and never again, and which a real instance leaves by the
  restart that rebuilding master performs. A probe on every run of every
  instance, to name a state essentially unreachable in production, is a cost
  paid by everyone for nobody.
- The list of collectors it protects was wrong. It came from a grep that read
  a comment: `70.schema/020.index-usage.sql` says it deliberately does not
  defer its reads, and the number is ten, not eleven.
- The vocabulary does not have room for it. `Capability` is one half of a
  permission vocabulary whose other half is `@permissions`, and two tests
  enforce that: a capability with no grant fails
  `TestEveryProbedCapabilityCanBeGranted`, and one with no permission spelling
  fails `TestCapabilityNamesMatchNormalisedPermissions`. The grant script
  would have told a DBA to grant something that is not a permission, and the
  wizard would have counted it among "Permissions". Making the name legal in
  that vocabulary is worse: `skipReason` matches a script's declared
  permissions against the denied set, so the ten collectors would then have
  been skipped, which is the opposite of what this document argued for.
- The coverage renderer would have lied. Its denial wording is "refused for
  this login", hardcoded, and nothing here was refused for any login.
- Two of its claims about the collectors were false: `023.log-vlf` loses half
  its root, and `043.cpu-neighbours` keeps a full root with a wrong value in
  it.

Those last two were real defects and are fixed, without a probe: a deferred
read that comes back with nothing now leaves a null rather than a zero or a
deduction. `docs/verification-binary-collation.md` carries the measurements.

What follows is the withdrawn design, unchanged.

## The original document

Written from the measurements in `docs/verification-binary-collation.md`.

## The question

Eleven collectors defer a read through `sys.sp_executesql`. It is the corpus's
answer to a view that may not exist on the build in front of it: a missing
object is a compile-time error, which a `TRY` at the outer level cannot catch,
and a runtime one inside `sp_executesql`, which it can.

On an instance whose server collation is `Latin1_General_BIN2`, every one of
those calls fails before it runs anything:

```
Msg 17750, Level 16, State 1, Procedure sys.sp_executesql, Line 1
Could not load the DLL (server internal), or one of the DLLs it references.
Reason: 126(The specified module could not be found.).
```

Measured on 2019 (15.0.4480.2) and 2022 (16.0.4265.3), on three containers,
and absent on a case-sensitive and a default collation of the same image. Two
properties make it worse than an error: `TRY ... CATCH` does not catch it, so
the collector's own error handling sets nothing, and `INSERT INTO @t EXEC`
inserts no row and raises nothing, so the staging table stays empty.

The result is an archive that says `0 error(s)` while `021.host-info` is all
null, `044.default-trace` reports no trace and no event where there was one
trace and 72 events, and `091.statistics-density` reports no statistic where
there were two. A complete looking, entirely empty answer, which is the
failure `CoverageBlock` exists to prevent for a denied permission and does not
yet prevent here.

## What is added

### 1. A capability probe

`Capabilities()` gains one entry, beside the permission probes:

```go
{Name: "dynamic_sql", Label: "Run a deferred read (sp_executesql)",
 SQL:    "EXEC sys.sp_executesql N'SELECT 1'",
 Impact: "collectors that defer a read cannot run it, and return empty results rather than failing"}
```

It needs no `NeedsRows`. Measured through the driver rather than assumed: the
same statement that a `TRY` cannot catch inside a batch comes back to
go-mssqldb as an ordinary query error, `mssql: Could not load the DLL (server
internal) ... (17750)`, so the existing probe runner records it as `denied`
with no new machinery.

The probe is the cheapest statement that exercises exactly the mechanism the
eleven collectors use, which is the rule every other probe in that list
follows.

### 2. The collectors that depend on it, from the corpus rather than a directive

A script's body is already parsed and linted. `Script` gains `UsesDynamicSQL`,
set when the body contains `sp_executesql`, and no new directive is invented:
a directive would be a second place to state a fact the body already states,
and the two would drift. The lint has the body in hand and the test that
guards the corpus inventory will carry the flag, so a twelfth collector that
starts deferring a read is covered the day it is written.

### 3. What the run says when the probe is denied

The eleven are not skipped. Measured, the deferred read is a part of the
collector and not the whole of it: `091.statistics-density` loses its array
and keeps its root, `023.log-vlf` the same, `044.default-trace` the same, and
`70.schema/020.index-usage.sql` keeps the index usage its name is about and
loses only the missing-index suggestions. Skipping them would throw away what
still works to avoid reporting what does not.

So the run continues, and says three things:

- `coverage.status` becomes `incomplete` and `coverage.denied_capabilities`
  gains `dynamic_sql`, which an analysis layer already reads;
- `coverage.notes` gains one sentence naming the mechanism and the
  consequence, in the register the existing notes use: that the deferred reads
  of these collectors returned nothing, that this is not the same as an empty
  instance, and that their roots are still theirs;
- `MANIFEST.txt` lists the affected collectors by path under the existing
  "not run" section, with the reason. A reader holding an archive with a
  null `021.host-info` is entitled to find out why in the file that comes with
  it rather than in this document.

### 4. The profile case

`ProfileChecks` rewrites a denied capability to `not_needed` when no collector
of the profile declares it, and a capability that is not a permission is
declared by nobody. It gains the one line that makes it true here: the
capability is needed when any collector of the profile has `UsesDynamicSQL`.
The `space` profile contains two of the eleven, `020.index-usage` and
`091.statistics-density`, so under that profile the probe stays a denial
rather than becoming a no-op.

## What is not in scope

- Rewriting the eleven guards. They exist because a view that is absent is a
  compile-time error on the builds this corpus still supports; replacing them
  would trade a rare platform defect for a certain failure on every old
  instance.
- Making the collectors themselves notice. They cannot: `TRY` does not catch
  it and `@@ROWCOUNT` of zero is a legitimate answer for most of them.
- Any judgement about the collation. The corpus holds on a binary collation,
  which `docs/verification-binary-collation.md` measured; what fails is one
  platform mechanism, and the tool's job here is to say so.

## Tests

Without a server:

- the probe is in `Capabilities()` and its SQL is the literal above;
- a denied `dynamic_sql` produces the coverage status, the denied capability,
  the note, and the manifest listing naming exactly the collectors whose
  bodies use `sp_executesql`, from a fixture corpus where some do and some do
  not;
- `ProfileChecks` keeps it denied for a profile containing such a collector,
  and rewrites it to `not_needed` for a profile containing none;
- the flag itself: a script whose body contains `sp_executesql` in any casing
  carries `UsesDynamicSQL`, one that mentions it only in a comment does not
  (the comment case is the one the naive test gets wrong, and
  `10.system/021.host-info.sql` has it in prose as well as in code);
- a run where the probe is `ok` says none of it: no note, no listing, and the
  coverage status unchanged, so the archive of a healthy instance is
  byte-identical to today's.

Against a server:

- CI asserts the probe is `ok` on 2017 and 2022, which is the case that must
  not regress: a probe that reported a denial everywhere would be worse than
  no probe;
- by hand, against a container built with `MSSQL_COLLATION=Latin1_General_BIN2`,
  which takes one `podman run` and reproduces in under a minute: the probe is
  denied, the manifest lists the eleven, and the collectors still produce
  their roots. The recipe is in `docs/verification-binary-collation.md`.
