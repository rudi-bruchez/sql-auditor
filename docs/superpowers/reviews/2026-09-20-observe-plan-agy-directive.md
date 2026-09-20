# 1. Blocking

## The Preflight Directory Probe Leaves Undeletable Garbage
**What the plan says:** Task 0.2 asks to run the directory probe (creating, starting, stopping, and dropping a throwaway session) and to "Decide and write down whether the probe leaves a `.xel` file behind when it succeeds, and if so, that removing it is part of the probe."
**What I did (running):** I chose the prefix `ZzObserve_`, created event sessions `[ZzObserve_test]` (for Task 0.1) and `[ZzObserve_probe]` (for Task 0.2), and dropped them after my measurements. For Task 0.2, I executed the exact sequence against `sql2025` and then listed the directory using `ls -la '/var/opt/mssql/log/sql-auditor-observe-probe*.xel'`.
**What happened:** The probe successfully creates and drops the session, but leaves a 16KB `.xel` file behind on the disk. SQL Server provides no unprivileged T-SQL command to delete files. Removing it requires enabling `xp_cmdshell` or `sp_OACreate`, which demands `sysadmin` rights and defeats the spec's entire argument about minimal consent. The instruction to remove the file is impossible to perform under the granted permissions.
**What it should say instead:** The plan must acknowledge that the directory probe inherently leaves a small `.xel` file behind every time it runs and cannot be cleaned up. If this is unacceptable, the directory probe must be abandoned entirely, relying solely on `START` failing with `Msg 25602`.

## Preflight Probe Breaks Capability Tests and Architecture
**What the plan says:** Task 2.4 requires running the directory probe during preflight. Task 2.5 says "collect/preflight.go grows the probe from task 2.4, so the capability and the grant are the same vocabulary. Check TestEveryProbedCapabilityCanBeGranted... still pass".
**What I did (reading):** I read `collect/preflight.go` and `collect/grants_test.go`.
**What happened:** `Capabilities()` returns a static list of `SQL` strings taking no arguments, whereas the directory probe requires the dynamic `--file-dir` path provided by the user. Furthermore, `TestEveryProbedCapabilityCanBeGranted` loops over every capability and asserts that `BuildGrantScript` produces a valid T-SQL `GRANT` statement for it. An OS-level directory write permission has no T-SQL grant. Adding the probe to `Capabilities()` breaks the test, and modifying the test to exempt it violates the rule that "a step certifies work that was not done."
**What it should say instead:** The directory probe cannot be integrated into `Capabilities()`. It must be implemented as a standalone function in `collect/observe/lifecycle.go` that runs during preflight, bypassing the collector's capability/grant machinery.

# 2. Serious

## `observe status` Consent Ordering
**What the plan says:** Slice 2 establishes the strict command ordering: "validate, connect, describe, preflight, consent, then sweep or create."
**What I did (reading):** I checked the spec for `observe status`, which says it "sweeps like every other entry point".
**What happened:** If the implementation strictly follows the plan's order, `observe status` will prompt the operator for consent with the `CREATE EVENT SESSION` DDL before it is allowed to perform its sweep, even though `status` never creates a new session.
**What it should say instead:** The plan must explicitly branch the ordering: `status` skips consent and preflight, proceeding directly to describe and sweep, while `start` and timed runs follow the full sequence.

## Exclusion by Application Name Contradicts the Spec and Collides
**What the plan says:** Task 1.1 instructs that `Session` holds "the application name to exclude". But the spec's rendered DDL hardcodes `AND sqlserver.client_app_name <> N'sql-auditor'`. Task 4 identifies the risk of an application legitimately named `sql-auditor` being silently excluded, and says "does the plan's test catch it."
**What I did (reading):** I analyzed the Slice 5 test instructions.
**What happened:** The Slice 5 test only verifies that a client named `sql-auditor` is successfully ignored; it does not test or prevent the catastrophic failure of colliding with the user's actual application workload name. Furthermore, parameterizing the application name in `Session` contradicts the spec's hardcoded DDL.
**What it should say instead:** The plan should instruct the implementer to change the spec's hardcoded `N'sql-auditor'` to a parameterised, collision-free name (e.g., `sql-auditor-observe-<uuid>`), set that string as the tool's `Application Name` in the connection string, and pass it to the DDL. The test in Slice 5 should verify the exclusion against this dynamic name.

# 3. Smaller

## `ParseStem` Ambiguity on Full Paths
**What the plan says:** Task 1.2 specifies `ParseStem(string)` to parse the deadline out of the file stem read from the target.
**What I did (running):** I ran Task 0.1 and read the XML output from `sys.dm_xe_session_targets.target_data`.
**What happened:** `target_data` returns the absolute path on the server (e.g., `/var/opt/mssql/log/sql-auditor-observe-20260920T120000-60_0_134343910867040000.xel`). If the user specifies a directory path that happens to contain `-` or `_0_`, a naive string split on the full path will panic or return the wrong deadline.
**What it should say instead:** Instruct the implementer that `ParseStem` must call `filepath.Base` (or equivalent string manipulation) to isolate the filename from the absolute path before attempting to parse the stem.

## `interruptibleOn` Signature Change
**What the plan says:** Task 3.3 says "interruptibleOn in cmd/sql-auditor/main.go is where the existing wording lives and the observe path needs its own".
**What I did (reading):** I read `cmd/sql-auditor/main.go`.
**What happened:** `interruptibleOn` currently hardcodes the message. The plan doesn't specify how to provide a custom message, risking an implementer hardcoding a dirty command check inside `main.go`.
**What it should say instead:** Instruct the implementer to modify the signature of `interruptibleOn` to accept the cancellation message as a parameter, so `main.go` remains generic and doesn't hardcode command-specific text.
