# Adversarial review: `observe`, slice 1

Verdict: do not start Slice 1 as written. The early measurements are useful, but the plan has safety and truthfulness gaps that cannot be repaired inside the decoder. Resolve the blocking findings, then re-plan the affected slices.

## Blocking findings

### 1. The expiry sweep has no proof that the session is ours

Slice 2 says every entry point stops and drops any expired session under the fixed name. A fixed name makes an orphan discoverable; it does not establish ownership. A DBA can have created that name manually, reused it after a prior run, or be investigating an earlier failed run. With a missing state file, the proposed code would stop and drop that session merely because its `create_time` is old. That is an unauthorised destructive action on a production instance.

Add an ownership protocol before lifecycle code. Define a server-verifiable fingerprint of the managed session: target, events, actions, predicate, and options. The sweep must refuse, not alter, a same-named session that does not match it. Decide what proves ownership for a matching manually recreated session, document DBA recovery, and test absent, exact managed orphan, same-name different session, stopped managed session, and altered target.

### 2. A pure XML string cannot reveal every valid truncation

Slice 1 requires `DecodeHistogram(xml string)` to report `Truncated` when a fixture is cut between buckets while the XML still parses. That is not implementable from the proposed input. A syntactically complete XML document containing two buckets is indistinguishable from a transport-truncated document which happened to end after its second bucket and was made well-formed by the test. `encoding/xml`, streaming or eager, has no expected bucket count or source length against which to prove a missing tail.

Malformed XML can be detected and marked incomplete. A valid but shorter XML document needs an engine-supplied completeness marker, an independently read expected count, or must be reported as unknowable. Slice 0 must measure the actual histogram and ring-buffer XML on every supported version and identify which signal, if any, supplies that proof. Change the contract and tests to distinguish malformed input from verified engine truncation. Do not assert a property that no function of a string can establish.

### 3. The plan promises SQL Server 2012 support but measures only 2025

The specification sets SQL Server 2012 as the version floor and specifically requires the oldest-build XML shape, limits, and permissions to be measured. The plan instead makes `sql2025` the sole measurement environment and makes its fixtures the decoder contract. Attributes, histogram value formatting, ring-buffer loss counters, permission behaviour, and XE option support are load-bearing here. A 2025-only proof cannot justify a 2012-compatible command.

Either narrow the feature floor to the measured build or add a compatibility gate before Slice 1: preserve captures on 2012 plus the current build, test both, and record unsupported options or semantic differences. Do not present an untested oldest build as an acceptance criterion.

### 4. Resolution does not define identity or ambiguity

`query_hash` is not a unique statement identity. Multiple Query Store rows can share it through context, object, or genuine hash collision. The plan says one row per hash and Query Store first, but does not say whether multiple matching texts are emitted, selected arbitrarily, or made unresolved. An arbitrary choice turns a count into a confident attribution to the wrong statement.

The plan-cache fallback has a second documented problem: a SQL handle names a batch, not necessarily the statement. The repository's `missing-index-queries-spec.md` records that statement start and end offsets are needed to cut a batch to the actual statement. Slice 4 does not require offsets, a deterministic candidate policy, a database-context check, or tests with two statements in one batch and two candidates for one hash.

Define output for zero, one, and many candidates. A safe default is every distinct candidate with an ambiguity label, or unresolved with candidates counted, never arbitrary selection. Extract plan-cache text by offsets and label it literal-bearing. A broad manifest caveat is not a substitute for correct rows.

### 5. Creation has unhandled orphan paths

The intended order is create, start, snapshot, then write the state file. If the process dies or state-file writing fails after create or start, the next `finish` has neither a baseline nor reliable ownership evidence. If start fails after create, the DDL object also remains. Cancellation during create, stop, read, drop, or archive writing has the same issue. Reusing `collect/cancel.go` does not compensate a newly created server object.

Write a lifecycle state machine and a compensating action for every transition. Creation must be followed by a best-effort drop on any failure before durable state exists. Persist state atomically only after session and baseline are known. Define precedence when cleanup, archive writing, and the original error all fail. Add fault-injection tests at each boundary, not only three happy state cases.

### 6. Two operators can pass the check and race at create

The known name is not an atomic lock. Two `start` commands can both observe no session, pass consent and preflight, then race to `CREATE EVENT SESSION`. One gets already-exists. The proposed exit table has no classification for it and the fake-connection tests do not exercise it.

Treat already-exists at create as the normal collision refusal, re-read and describe the winner without stopping it. If re-read fails or the observed session does not match the managed fingerprint, refuse with that fact. Add a concurrent integration test or deterministic fake forcing the interleaving.

### 7. Permission and preflight claims are incomplete and contradictory

The specification requires preflight before creation and calls code 3 a refusal before touching the instance. Permission discovery necessarily contacts the instance, and the plan says the sweep runs before every entry point. Thus an invalid command or denied credential may have queried, or even dropped, a session before the supposed refusal. Ordering is undefined.

"Read access in the target database" is too vague for Query Store resolution. Name exact permission probes and scopes for Query Store, plan cache, XE DMVs, and DDL. Say which denial refuses and which becomes exit 2 after capture. Validate syntax, paths, and durations before connecting; then identify an existing session without mutating it, preflight, gain consent, and only then create or alter. Expiry cleanup needs explicit consent and ownership rules.

### 8. Baseline and `--no-baseline` semantics are underspecified

For a freshly created managed session, the histogram begins at zero. A baseline matters for a borrowed session, which this slice excludes, or recovery after state loss. Yet the plan makes state mandatory to finish and advertises `--no-baseline` without defining whether it is allowed only for a verified managed session, whether it reports from session creation, or how it avoids archiving a stranger's cumulative counts.

Specify separate paths. A new managed start records a zero baseline but still needs durable server, database, and intent binding. A borrowed session needs the full snapshot and create-time guard when that mode is implemented. A lost state file must never turn an arbitrary same-named session into an archive. Test restart, negative per-bucket delta, lost state, and `--no-baseline` against both managed and non-managed sessions.

### 9. The archive is unbounded despite carrying unbounded text

The capture is bounded, but Query Store and plan-cache resolution can return a large ad hoc batch or many candidates. Unlike existing archive writers, Slice 4 specifies no byte ceiling, per-text limit, candidate cap, omission record, or atomic write behaviour for `statements.json` and `unresolved.json`. This can exhaust memory or disk after the capture, then lose the only record when archive creation fails.

Reuse the existing run writer's budget and file permissions. State limits, record every omitted text with a reason, and test budget exhaustion. If a full archive cannot be written, preserve a manifest and raw non-sensitive histogram counts where possible instead of reducing evidence to an exit code.

### 10. Tests prove parsing details, not capture correctness

The five-minute check compares a histogram total with statements sent, but does not prove the two events are counted once, the database predicate excludes another database, the collector's SPID is excluded, or `source_type = 1` works for both event types. One test query can pass while an event is absent or the wrong session is counted.

Add an integration matrix with distinct counts for RPC and SQL batch calls, a second database, and work on the observe connection. Assert expected buckets after hash conversion, exclusion of the other database and observer, and manifest loss fields. Exercise a changed `create_time` between start and finish. This is more valuable than counting `--- PASS` lines.

## Important non-blocking corrections

- The unresolved-hash threshold is called stated but never specified. Make it a constant, put it in the manifest, and test below, equal to, and above it.
- `slots` is explicit and "sized for the instance", but has no value, sizing rule, supported range, or remediation. Use a conservative fixed default now, surface overflow as partial, and defer adaptive sizing until measured.
- `status` must distinguish a target not yet flushed from an empty capture and a target-data read failure. The plan notes latency but has no output contract.
- Require exact rendered SQL, including database id and observer SPID, in `_run.json`. An embedded template is not evidence of what ran.
- Do not make test-name counts a completion criterion. They are brittle and do not establish behaviour.

## Minimum re-plan order

1. Measure version support, XML completeness signals, hash representation, XE session definition, and permission probes before package APIs.
2. Specify ownership, lifecycle recovery, and exact preflight/mutation order.
3. Define resolution ambiguity, statement slicing, output limits, and manifest schema.
4. Implement the decoder only after its input can support completeness claims.
5. Run the integration matrix before claiming the default capture is correct.

