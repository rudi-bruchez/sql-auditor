# Blocking

## The pre-consent directory probe deadlocks every command against the session it is meant to manage (executed)

The plan says in task 0.2 to create the directory-probe session “under the managed name”, and task 2.4 orders `describe, preflight, consent, then sweep or create`.  It does not limit the directory probe to creation modes.  The managed name is fixed, so `finish`, `stop`, and `status` necessarily encounter an already-defined session before they can sweep it.

I started a uniquely named `QvNadirObserveA` session, then issued a second `CREATE EVENT SESSION` with that same name.  SQL Server returned `Msg 25631: The event session ... already exists. Choose a unique name for the event session.`  This is the same collision the prescribed probe would receive.  Thus a running or stopped remnant prevents the preflight from reaching the sweep that is supposed to stop or drop it.  The plan and spec agree on the problematic sequence; the specification is wrong here too.

It should say that permissions are preflighted for every mode, but the directory DDL probe runs only on a path which is about to create a new capture, after `Describe` establishes that no session needs lifecycle handling.  `status`, `finish`, and `stop` must not probe a directory at all.  The task also needs an explicit probe name/ownership strategy if it is ever used while a fixed managed session can exist.

## Task 0.1 cannot be executed as written (executed as far as its artefact permits)

Task 0.1 says to create the session “exactly as the spec renders it” with a “small” `max_file_size`.  The rendered DDL contains unresolved `@database_id` and `@stem` placeholders; the plan supplies neither a scratch database nor a writable directory, and “small” contradicts the rendered default of 128 MB without supplying a value.  An implementer cannot produce the requested artefact verbatim without making all of those choices.

I therefore did not invent values for the plan task.  To test only its underlying server claim, I separately created and started a cleanup-bound session named with my `QvNadirObserve` prefix.  Its `sys.dm_xe_session_targets.target_data` was:

```xml
<EventFileTarget truncated="0"><Buffers logged="0" dropped="0"/><File name="/var/opt/mssql/data/qvnadir-observe-a_0_134343910972710000.xel"/></EventFileTarget>
```

So the claim is true on the supplied build, but it does not repair the task.  The plan should give a complete parameterized command, including an explicitly selected scratch database, a temporary writable directory, a concrete rollover size, traffic generator, wildcard query, and cleanup command—or explicitly delegate those choices to a prior task.  It must not call an unresolved template an executable artefact.

## Task 2.5 directs the new DDL permission into the collector-wide preflight (reading)

The plan says `collect/preflight.go` “grows the probe” and relies on `TestEveryProbedCapabilityCanBeGranted`.  In this tree, `collect.Capabilities()` is the one global list: `collect.Collect` calls it for ordinary read-only collection, the TUI renders it, and `collect/grants_test.go` iterates it to generate grants.  Adding `ALTER ANY EVENT SESSION` there makes `check`/`collect` ask for a server-altering permission they do not use, and the grant script offer it.  That contradicts the specification’s central separation of `observe` from `collect`.

It should name a distinct observe-only capability list (it may reuse the `Capability` and `RunPreflight` types) and a grant-script input/path that is only reached by `observe`.  The existing collector-wide list must remain read-only.

# Serious

## Rollover-loss detection is measured but never assigned an implementation task (reading)

The specification requires the first target filename to be read immediately after `START`, persisted, and compared with the final wildcard set.  Task 0.1 measures that behavior, but Slice 2’s `Describe` lists only the four catalog views and `sys.dm_xe_sessions`; it does not read `sys.dm_xe_session_targets`.  No later task says to capture the initial `target_data` filename, enumerate the final file set, compare them, or classify the result as rollover loss.  Slice 4 merely lists fields that `capture.json` must contain, so a test can pass with caller-supplied values while the actual lifecycle never performs the detection.

It should add a lifecycle task and live test: immediately after a successful `START`, read and persist the `event_file` target filename; at finish, derive/query the exact run wildcard, compare it, and set the partial/“unobservable” outcome.  It should explicitly test both a retained first file and an evicted one.

## The supplied verification commands target a different checkout (executed)

Every slice verification command begins by changing to `/home/rudi/Sources/Repos/sql-auditor-workspace/sql-auditor`, not the checkout being reviewed.  I ran the Slice 1 command verbatim.  Its build completed there, then it returned:

```
# ./collect/observe
stat /home/rudi/Sources/Repos/sql-auditor-workspace/sql-auditor/collect/observe: directory not found
FAIL    ./collect/observe [setup failed]
```

The plan therefore does not verify the tree it claims to modify and cannot reach its stated test count here.  It should run from the repository root (`go build ./... && go test ./collect/observe/...`) or define a repository-root variable once and use it consistently.

# Smaller

## The required test-count warning names the wrong file (reading/executed)

Slice 1 says `^TestObserve` already matches two tests of `collect/observer.go`.  The tests are actually in `collect/observer_test.go`; I ran `go test ./collect -run '^TestObserve' -v`, which ran `TestObserverCallbacksAreSafeOnTheZeroValue` and `TestObserverForwardsToTheWrappedImplementation` (two tests).  The count observation is right, but the file claim is false.

It should say `collect/observer_test.go`.

## The plan’s shared test prefix is unsafe for parallel measurements (reading)

The pre-code instructions require every object to use `ZzObserve` and finish by asserting only the system sessions remain.  A second reviewer or an unrelated test using the same prescribed prefix can create, drop, or count the other run’s objects; the final global session assertion also fails for unrelated sessions.  I used the distinct prefix `QvNadirObserve`; I created sessions `QvNadirObserveA`, `QvNadirObserveB`, and `QvNadirObserveProbe`, observed the successful probe leave one `.xel` file, removed that file and all prefixed capture files, and confirmed zero remaining sessions with that prefix.

It should require a per-run unique prefix, clean up only names and files bearing that prefix, and verify only that scoped set is empty.
