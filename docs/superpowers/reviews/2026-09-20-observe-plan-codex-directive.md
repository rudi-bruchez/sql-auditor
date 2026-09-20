# Adversarial review: `observe` capture plan

Reviewed `docs/superpowers/plans/2026-09-20-observe-capture-slice-1.md` at `9ce552f` against `docs/observe-spec.md` and the tree at that commit.

I chose the prefix `KiteRookObserve`. I created no objects or files: task 0's DDL cannot be executed verbatim without inventing parameters, and the plan's fixed session name conflicts with its own reviewer-unique-prefix instruction. Consequently there was nothing to remove. A read-only catalog query found seven existing sessions, so I did not attempt the plan's unsafe assertion that only the three system sessions remain.

## Blocking

### B1. Task 0 cannot be performed as written, so its load-bearing measurement is not a measurement

**What the plan says.** Lines 46--58 say to create the session “exactly as the spec renders it”, with a small file size, force two rollovers, record the XML, and prove both detection outcomes. Lines 33--37 additionally require everything created to use `ZzObserve` and finish with only three system sessions. Task 0.2 then says only “Write the probe” and test an unwritable directory.

**What I did (reached by running and reading).** I did not fill in omitted parameters, per the review instruction. The rendered SQL has unbound `@database_id`, `@stem`, `@max_file_size_mb`, `@max_rollover_files`, `@max_memory_kb`, and `@dispatch_latency_s`; neither the plan nor the spec provides the test values, the scratch database, the service-writable directory, the deliberately unwritable directory, or a traffic generator. The requested fixed name `[sql-auditor observe]` also cannot have the required unique prefix. A read-only query on the supplied SQL Server returned SQL Server 2025 and seven catalog sessions, not only the three system sessions, so satisfying the final assertion would require deleting other reviewers' objects.

**What happened.** An implementer cannot execute task 0 verbatim, cannot reproduce its claimed rollover result, and cannot safely complete its cleanup assertion. This is especially consequential because task 0.1 is the stated gate for rollover-loss detection and task 0.2 is the stated gate for exit code 3.

**What it should say instead.** Supply a complete disposable SQL script and traffic command: concrete safe directory and intentionally failing directory, database id, tiny size and rollover values, XML query, wildcard query, and cleanup limited to a unique test prefix. Use a unique test session name rather than the production managed name, and assert only that objects with that prefix were removed. Do not say that only system sessions remain in a parallel-review environment.

### B2. Slices 3 and 4 are in the wrong order, and slice 3 omits required compensation

**What the plan says.** Slice 3 requires `start` to persist state, consent to record executed DDL in `_run.json`, and Ctrl-C to write a partial archive (lines 298--320). Slice 4, which comes later, first defines the state-file location, manifest, `_run.json`, `capture.json`, and archive implementation (lines 345--369). The spec requires a compensating action for every transition, a best-effort drop after any pre-durable-state failure, and an atomic state-file write only after `START` succeeds (spec lines 436--440).

**What I did (reached by reading).** I traced the stated file order. No archive or state-file task exists before the command task that must use both; the only teardown requirement in slice 2 is for sweep/cancellation on a fresh context. There is no task for `CREATE` succeeded/`START` failed, state-file write failed, counter read failed, or archive failure.

**What happened.** A task-by-task implementer cannot complete slice 3 without implementing future slice-4 code early, and can satisfy every listed slice-3 item while leaving a created session behind on an ordinary error. That violates the specification's safety guarantee. The plan is wrong here; the spec is clearer and should win.

**What it should say instead.** Move the minimal state/archive model before command wiring, and add explicit lifecycle transitions with compensating `STOP`/`DROP` on a fresh bounded context for every failure after `CREATE`. Require atomic state persistence after successful `START`, and state which failure still leaves which local/server residue.

## Serious

### S1. The slice-1 verification claim is false for the command it prints; all its numeric gates are hollow

**What the plan says.** The slice-1 command is `go test ./collect/observe/ -run '^TestObserveSession'`; it expects 12 tests and says `^TestObserve` would already match the two tests in `collect/observer.go` (lines 138--150).

**What I did (reached by running).** I ran every printed verification command verbatim. Each `./collect/observe` command failed with `directory not found`; the command test reported `warning: no tests to run` and passed; the two named usage tests passed. Separately, `go test ./collect/ -run '^TestObserve' -v` ran exactly the two `Observer` tests. They are in the parent `collect` package, which `./collect/observe` never tests. `go test ./collect/ -run 'Grant' -v` currently runs 20 passing tests, and the two specifically named capability tests exist and pass.

**What happened.** The warning about `^TestObserve` is true only for a different package command, so the plan's claim that its printed filter could certify `observer.go` is false. More generally, the required 12/14/4/9/11 counts are hard-coded growing-test counts, prohibited by `CLAUDE.md`, and a count does not prove the named behaviours were exercised.

**What it should say instead.** Fix the package-path explanation, remove all hard-coded test totals, and run explicitly named behavioural tests (including the negative mutation cases). Keep the two existing usage tests as named regressions. A newly created package naturally has zero matching tests before implementation; that is not a measured count to preserve.

### S2. Task 2.5 would make the read-only collector demand an alteration permission

**What the plan says.** It tells the implementer to add the `ALTER ANY EVENT SESSION` probe to `collect/preflight.go` and its grant to `collect/grants.go`, so the two global capability tests pass (lines 227--233).

**What I did (reached by reading and running).** A repository search found no existing `ALTER ANY EVENT SESSION` string. The supplied server reports that the review login has that permission. I traced `Capabilities()`: ordinary `collect.Run` passes that complete global slice to `RunPreflight`, the TUI renders the same complete slice, and `TestEveryProbedCapabilityCanBeGranted` iterates that slice. The two named tests pass today because they only enforce the existing collector vocabulary.

**What happened.** Following task 2.5 literally adds an observe-only server DDL right to normal `collect` preflight, its UI, and `check --grant-script`. That blurs the specification's central separation: a read-only collection now asks a DBA to grant a permission whose purpose is creating/altering XE sessions. The named tests would certify that coupling, not that observe has a correctly scoped grant path.

**What it should say instead.** Define an observe-specific capability/grant path (it may reuse capability and grant helpers) and state exactly how its grant script is requested. Do not append it to the slice consumed by normal `collect`. Add tests proving ordinary `collect` neither probes nor offers the alteration right, while observe does.

### S3. The file-path/stem contract is incomplete, so recovery can silently use the wrong target name

**What the plan says.** `Session` has both a capture directory and a stem; `Stem()` returns only `sql-auditor-observe-...xel`; `CreateSQL()` emits the spec's `filename = @stem`; and `ParseStem` accepts only the bare name or its generated suffix (lines 95--120).

**What I did (reached by reading).** The spec says `--file-dir` accepts a directory and explicitly distinguishes it from a file, while its displayed DDL has only `@stem`. The plan provides no join rule, server-path separator rule, basename extraction rule, maximum path policy, or test for a deep operator directory. The supplied server's error-log property is an absolute *file* path, as the spec says, not a directory.

**What happened.** A literal implementation can ignore `CaptureDirectory` and create in SQL Server's default location; a different implementer can concatenate it incorrectly. When the server reports an absolute generated filename, `ParseStem` as specified has no defined input transformation. The ownership, deadline, and rollover proof all depend on that parse, so this is not cosmetic.

**What it should say instead.** Define one server-side target filename contract: how the configured directory and generated leaf name are combined without using the operator host's path semantics; how the leaf is recovered from the catalog and target XML; accepted separators; and the maximum accepted full path with a refusal before `CREATE`. Include round trips for a deep directory and a returned absolute generated filename.

## Smaller

### M1. The application-name test demonstrates the known false exclusion but cannot detect it

**What the plan says.** Slice 5 drives a second client named `sql-auditor` and requires its calls not to appear (lines 386--390), while its own risk list says an unrelated application can have that name (lines 423--425).

**What I did (reached by reading).** I compared that task to the specification, which intentionally filters every `client_app_name = sql-auditor` connection.

**What happened.** The proposed test passes precisely when an unrelated application using that conventional name is silently excluded. It proves the mechanism, not that the excluded client is the tool. This is an accepted limitation in the spec, not a contradiction, but the plan presents it as an end-to-end safety check.

**What it should say instead.** Label the test as proof of the limitation and put the limitation in command help/consent. If the product must distinguish the tool, the specification—not this test—needs a different identity mechanism.

