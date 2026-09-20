# Review of observe-spec.md

## 1. Blocking

### The @@SPID exclusion creates a fatal blind spot and fails across connections
**Reached by:** Running (and lifecycle analysis).
**What the document says:** "The session also excludes `observe`'s own session id, which means reading `@@SPID` before the `CREATE`, so the id appears in the DDL the consent prompt shows."
**What I did:** I simulated the lifecycle of the command across `start`, the consent prompt, and `stop`, and verified the behavior of SQL Server connection pooling with `@@SPID`.
**What happened:** A CLI tool like `observe` connects, gets its SPID, prompts for consent, executes `CREATE EVENT SESSION`, and exits. Because SQL Server aggressively reuses SPIDs, that exact SPID will be assigned to a new application connection while the session is running. The session, hardcoded to ignore that SPID in its `WHERE` clause, will silently drop that application's events. Furthermore, the `stop` and `finish` commands run on entirely new connections with different SPIDs, meaning the tool's own teardown traffic *will* be captured anyway.
**What it should say instead:** The session must filter on `sqlserver.client_app_name <> N'sql-auditor'` (assuming the tool sets `Application Name=sql-auditor` in its connection string). This reliably excludes the tool's own traffic across all invocations and never excludes innocent application connections.

### The sweep logic deadlocks on a stopped session
**Reached by:** Running.
**What the document says:** "`STARTUP_STATE = OFF` means restarting SQL Server does not bring the session back... Any later `observe` ... reads when it started, and stops it if it has outlived its maximum."
**What I did:** I built the exact session described, started it, and then stopped it (simulating the state after a service restart where `STARTUP_STATE = OFF`). I then queried `sys.dm_xe_sessions` and `sys.server_event_sessions`.
**What happened:** When the session is stopped, it is removed from `sys.dm_xe_sessions` entirely, meaning `create_time` (the start time) is no longer available. However, the session definition remains in `sys.server_event_sessions`. When a later `observe` sweep runs, it finds the session but cannot "read when it started". The document provides no logic for this state, meaning the sweep will fail to determine if it has "outlived its maximum" and will likely refuse to clean it up, permanently locking out the tool from reusing the name.
**What it should say instead:** The sweep logic must explicitly state that if a session matching the tool's fingerprint exists in `sys.server_event_sessions` but is not actively running in `sys.dm_xe_sessions`, it is an abandoned remnant (from a crash or restart) and must be dropped unconditionally.

## 2. Serious

### The pattern for reading the capture file is omitted
**Reached by:** Running.
**What the document says:** It warns that appending a wildcard to the configured name (`observe.xel*`) matches nothing due to rollover naming. It then says "The event count in the file is read afterwards, with `SELECT COUNT(*) FROM sys.fn_xe_file_target_read_file(...)`".
**What I did:** I ran the session, observed the filenames written to disk, and attempted to read the file using `sys.fn_xe_file_target_read_file`.
**What happened:** The document correctly identifies the naming trap (SQL Server stripping the configured `.xel` extension and appending `_0_<ticks>.xel`), but it fails to specify the *correct* pattern to pass to the function. An implementer is left to guess how to construct the working pattern.
**What it should say instead:** The document should explicitly provide the correct pattern construction: strip the `.xel` extension from the configured path and append `*.xel` (e.g., `/var/opt/mssql/log/observe*.xel`), and pass that to the function.

## 3. Smaller

### Omitted parameters for the file target
**Reached by:** Reading.
**What the document says:** "`MAX_MEMORY` set explicitly" and "`package0.event_file`, with `filename`, `max_file_size` and `max_rollover_files` all set explicitly."
**What I did:** I read the specification to build the exact DDL.
**What happened:** The document is silent on what the actual values for `MAX_MEMORY`, `max_file_size`, and `max_rollover_files` should be. Without these values, the DDL is incomplete and an implementer is forced to invent them, which leads to the very drift the document is trying to prevent.
**What it should say instead:** The document must provide the concrete values (e.g., `MAX_MEMORY = 4096 KB`) or define the exact arithmetic to calculate them from the measured batch rate.

---
*Note: During this review, I created an event session `ZzObserve` (and `ZzObserve2`), a login `ZzTestLogin`, and `.xel` files on disk prefixed with `ZzObserve`. All of these artifacts have been successfully dropped and removed from the SQL Server instance.*
