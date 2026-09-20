# Adversarial review: `docs/observe-spec.md`

## Blocking

### B1. The session cannot be built or tested as specified

**Document says.** The session has explicit `MAX_MEMORY`, `MAX_DISPATCH_LATENCY`, `event_file.filename`, `max_file_size`, and `max_rollover_files` options; the consent prompt and `_run.json` show the exact DDL. It also fixes the managed session name.

**I did.** I read the complete specification, searched it for `CREATE EVENT SESSION`, `ADD EVENT`, `event_file`, and each option, then ran the documented CLI invocation verbatim. The invocation returned `flag provided but not defined: -minutes` and usage listing only the existing commands. There is no rendered `CREATE EVENT SESSION` statement in the spec, and none of the required option values, file stem, or collision/uniqueness rule is supplied. I did not create a substitute session: doing so would choose precisely the missing parameters the review instruction prohibits completing.

**What happened.** There is no executable session artefact to run. An implementer must make consequential, unreviewed choices before a session can be created; in particular, a fixed file stem can mix this capture with retained files from a prior run, while a per-run stem changes the ownership fingerprint and retrieval pattern.

**It should say instead.** Include one complete, parameterized DDL template and a table of fixed defaults/ranges: bracketed session identifier, generated file stem, filename pattern used for counting, `MAX_MEMORY`, `MAX_DISPATCH_LATENCY`, `max_file_size`, and `max_rollover_files`. Specify which values become part of the exact managed fingerprint. This finding is reached by running the supplied command and reading the artefact.

### B2. The stated self-healing deadline is not recoverable

**Document says.** A `start --for <minutes>` deadline is “declared and stored”; later invocations on any machine derive it from `sys.dm_xe_sessions.create_time + --max-minutes`, so loss of local state is survivable. `--max-minutes` is a caller-set cap with a default of 60.

**I did.** I traced the deadline paragraphs and command surface without supplying a missing run value. The only server-side facts named for recovery are the session name and `create_time`. Neither records the selected `--for` duration nor the selected `--max-minutes`.

**What happened.** A later process cannot distinguish a run capped at 5 minutes from one capped at 60 minutes (or another allowed cap). Using its own default/configuration changes the deadline after the fact. Therefore “any machine” cannot correctly decide whether to sweep, and the “declared and stored” claim contradicts the subsequent choice not to store it server-side.

**It should say instead.** Either make the maximum lifetime a single immutable built-in value and say so, or put the intended expiry in a server-verifiable part of the managed session and include it in the exact fingerprint. If it remains only local state, remove the cross-machine/self-healing claim and require a human `stop`. This finding is reached by reading the specified state model.

## Serious

### S1. The claimed reuse of `062.xe-sessions.sql` cannot verify ownership

**Document says.** The managed fingerprint comprises events, actions, predicate, target, and target options, “read back from `sys.server_event_sessions` and its companions the way `queries/10.system/062.xe-sessions.sql` already reads them.”

**I did.** I read that query end to end and queried the live XE metadata. The live metadata exposes the required concepts: `database_id`, `database_name`, `client_app_name`, and `session_id` exist as actions/predicate sources; the two named events exist.

**What happened.** `062.xe-sessions.sql` returns session options, event names, targets, and target fields only. It neither joins `sys.server_event_session_actions` nor projects an event predicate. Reusing it as claimed makes two different sessions with the same events/target but different database or observer-SPID filtering indistinguishable. That is unsafe ownership verification, not a harmless reporting omission.

**It should say instead.** State that the command needs dedicated fingerprint queries, enumerate each catalog view and normalized field, and define exact comparison semantics (including predicate representation and target-field ordering). Keep `062` as inventory reuse only. This finding is reached by reading the existing corpus and checking the live catalog vocabulary.

### S2. Rollover-loss detection has no observable definition

**Document says.** The tool detects rollover loss by recording the number of files present “beside the number it expected” and reports when the earliest file is not the file with which the session started.

**I did.** I traced the target and retrieval sections, without fabricating the omitted file target. No starting filename is persisted, no moment for observing it is specified, and no definition says how an expected count is calculated.

**What happened.** An end-of-run count equal to the rollover limit is ambiguous: it can mean a capture naturally created that many files or one that created more and had its beginning deleted. The configured target uses generated rollover names, so the starting file cannot be inferred from the configured stem alone.

**It should say instead.** Define a concrete protocol: capture and persist the first generated file identity before accepting workload, enumerate the exact wildcard after stop, and classify loss from absence of that identity. If that first identity cannot be obtained reliably, say rollover is unobservable and avoid a false partial/success classification. This finding is reached by reading the target design.

## Smaller

### M1. The promised disk-size estimate lacks the quantity that turns calls into bytes

**Document says.** The consent prompt states expected capture size from measured Batch Requests/sec times the window.

**I did.** I compared the stated calculation with the capture contents. Live metadata confirms both events collect variable-length text (`statement` and `batch_text`) plus optional/context data; text size is not a property of Batch Requests/sec.

**What happened.** Calls per second times minutes yields an expected call count, not bytes. One workload can have the same rate as another while carrying orders of magnitude more statement text. The document also does not define a measured bytes-per-event value.

**It should say instead.** Either show an event-count/rate estimate only, or require a measured conservative bytes-per-event estimate, identify its source/window, and label the resulting disk estimate as such. This finding is reached by reading the calculation and running the XE metadata query.

No SQL Server objects, databases, logins, or files were created. Consequently none required removal; all live-server checks above were read-only.

