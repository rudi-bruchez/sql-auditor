# Adversarial review: superseded histogram plan

## Verdict

Do not implement this plan. This is not a request for further changes to the
document: its status block correctly says it was superseded before code was
written. The remaining body is historical evidence, not an executable
implementation plan.

## Evidence

The plan's essential mechanism is a histogram bucketized on the
`sqlserver.query_hash` action for `rpc_completed` and
`sql_batch_completed`. The current specification records the decisive
measurement on SQL Server 2025 RTM-CU7:

- both selected call-level events produced a query-hash action value of zero;
- all captured calls therefore collapse into one bucket;
- the total count remains correct while the deliverable contains no statement
  attribution.

That is a silent-success failure: creation, decoding, exit code, overflow
handling, and archive writing can all pass while the feature's output is
useless. No correction to the conversion, slots, XML decoder, Query Store
resolution, or test count can make this plan's selected capture signal carry
the missing identity.

## Consequences for every slice

- Slice 0 cannot establish a hash conversion for the events the command would
  ship. A successful experiment on statement-completed events would test a
  different design.
- Slice 1's histogram types, fixtures, overflow semantics, and truncation
  contract are not part of the replacement architecture.
- Slice 2's session DDL bakes in the broken target and action.
- Slices 3 and 4 turn the unusable bucket into a deceptive archive and exit
  status.
- Slice 5 could demonstrate that events were counted, but not that the command
  attributes calls to statements, which is the command's purpose.

The ownership, race, cancellation, preflight ordering, and archive-safety
concerns found during review remain relevant requirements. The supersession
notice says they were folded into the current specification; they must be
reviewed against the replacement plan, not retrofitted into this one.

## Scope boundary

The replacement is documented in `docs/observe-spec.md` and has its own plan,
`docs/superpowers/plans/2026-09-20-observe-capture-slice-1.md`. It uses a
capture file rather than the retired histogram design. This review intentionally
does not assess that new plan: the requested path is the superseded one, and
treating it as current would review the wrong implementation.

## Required action

Keep this plan marked superseded and execute none of its checkboxes. Review the
capture-file plan separately before implementation.

