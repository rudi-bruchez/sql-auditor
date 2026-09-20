# Adversarial review: `observe-spec.md`

Reviewed at `a6692b8`, on the supplied SQL Server 2025 build.  I created
`ZzObserveReview`, `ZzObservePerm`, `ZzObserveBulkLogin`,
`ZzObserveReviewLogin`, `ZzObserveRollOur`, `ZzObserveTextOur`, and
`ZzObserveCancelOur` during the probes.  I removed every one, plus the
matching `ZzObserve*.xel` files I created.  I left pre-existing `ZzObserve*`
objects alone.

## Blocking

### B1. There is no session artefact to implement or to execute verbatim

**What it says.** The specification repeatedly promises “the exact DDL” in
the consent prompt and `_run.json`, calls the fingerprint exact, and says that
`filename`, `max_file_size`, `max_rollover_files`, and `MAX_MEMORY` are all
set explicitly.  But it gives no `CREATE EVENT SESSION` statement and no
values for `MAX_MEMORY`, `max_file_size`, or `max_rollover_files`.  `--file`
is described as a directory although the target requires a file name.  The
predicate and the exact action/target/fingerprint representation are likewise
not specified.

**What I did.** This is a read finding, and it prevented the requested
verbatim execution: there is no artefact to run.  I built only the stated
pieces as a probe (the two events, their stated text options, actions, file
target, and stated session options).  The first syntactically plausible
spelling with a comma before `ADD TARGET` returned `Msg 102, Incorrect syntax
near 'TARGET'`; the working spelling places `ADD TARGET` after the event list
without that comma.  That is precisely an implementer choice the document
leaves open.

**What it should say instead.** Include one complete, parameterized DDL
template, with concrete defaults and validation bounds, including the file
stem derived from `--file`, database-id and observer-SPID predicates, actions,
all target options, and all `WITH` values.  Define the canonical catalog rows
that constitute its fingerprint.  Put the rendered template in the document
and make it the artefact measured on each supported version.

### B2. `start --for` cannot do what its contract says

**What it says.** `observe start --for <minutes>` “stops by itself at a
declared deadline.”  A few paragraphs later the specification correctly says
there is no server-side timer and that only a later invocation of `observe`
stops an expired session.  Its recovery rule derives the deadline from
`create_time + --max-minutes`, not from `--for`.

**What I did.** This is a read finding from the lifecycle text itself.  With
the default maximum, `start --for 5` followed by loss of the local process has
no actor which can stop it at five minutes; the specified cross-machine sweep
will instead regard it as in date until 60 minutes after `create_time`.  Thus
the stated deadline, `status`'s time-left value, and the durable recovery rule
cannot all be true.

**What it should say instead.** Either remove `--for` from this slice and say
that every start is bounded only by the maximum on a future invocation, or add
a durable server-side/externally scheduled enforcer before promising automatic
stop.  If `--for` remains as an intent recorded locally, say explicitly that
it is not an enforceable deadline after process loss and never call it
“stops by itself.”

## Serious

### S1. The SQL Server 2012 support claim is asserted and then disclaimed

**What it says.** “Version floor: SQL Server 2012” says one DDL covers that
floor.  The open questions then say the whole session, including those DDL
options and `fn_xe_file_target_read_file`, has never been run on 2012.

**What I did.** This is a read finding.  The supplied SQL Server 2025 probe
did verify the current-build claims: the XE metadata lists `statement` after
`collect_statement=1` and `batch_text` after `collect_batch_text=1`; captured
call-level events had `query_hash = 0`; and after `STATE = STOP` both
`sys.dm_xe_sessions` and `sys.dm_xe_session_targets` returned zero matching
rows.  None of that establishes the advertised 2012 floor.

**What it should say instead.** State “verified on SQL Server 2025; SQL Server
2012 support is not yet promised” until the complete published DDL and the
permission/file-count probes run on 2012.  Then promote the measured result to
the version floor.

### S2. Rollover-loss reporting is not an implementable proof yet

**What it says.** The tool will record files present “beside the number it
expected” and state when the earliest remaining file is not the file the
session started with, while also saying it reads the capture only to count
events.

**What I did.** This is a read finding.  No configured rollover limit or
initial concrete file name is specified (B1), and no lifecycle step records
the initial running-target file identity before workload starts.  A final file
count alone cannot distinguish exactly filling the retained-file limit from
having rolled over and deleted an earlier file.  I attempted the requested
rollover pressure probe with the stated file-target shape; without the missing
document values it could not be a verbatim test of the command.

**What it should say instead.** Choose the target values, record the first
running target file identity immediately after start, preserve it in the
durable state/run record, and define loss as its absence from the final
wildcard set.  Specify what outcome is reported if that initial identity is
unavailable.  Measure this procedure with an actual rollover before calling
the loss indication reliable.

## Smaller

### M1. The permission sentence is stronger than its evidence needs to be

**What it says.** The two server permissions are “the whole list.”

**What I did.** This was measured on the supplied current build.  A temporary
login holding only `VIEW SERVER STATE` and `ALTER ANY EVENT SESSION` returned
`xel_count = 6` from `sys.fn_xe_file_target_read_file`, then successfully
created, started, stopped, and dropped a temporary file-target session.  This
supports the current-build claim.  It does not support the same unqualified
claim for the stated 2012 floor, nor does it cover writing into an arbitrary
operator-supplied directory.

**What it should say instead.** Say these are the complete SQL Server 2025
permissions measured for the published DDL; directory write access is a
separate service-account preflight, and older-version support remains pending
the compatibility measurement.
