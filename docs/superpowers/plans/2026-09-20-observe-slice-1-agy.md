# Deep Adversarial Implementation Plan Review: `observe`, Slice 1

This document is an adversarial review of `2026-09-20-observe-slice-1.md`. It identifies architectural flaws, logical contradictions, edge cases, and failure modes in the proposed implementation plan.

## Executive Summary
The plan is structured thoughtfully with a strong emphasis on testing (Slice 0's hash conversion is an excellent de-risking step). However, several critical flaws will cause the tool to either lock out the user, fail to parse data, leak unrelated batch code, or miscalculate metrics. These must be addressed before implementation.

## Critical Architectural Flaws

### 1. Orphaned Sessions & The "Within-Deadline" Trap
**The Flaw**: In Slice 2, the self-healing sweep dictates: *"A session under the known name past its deadline is stopped and dropped... Within its deadline, start refuses."* 
**The Exploit**: If a user runs `observe start --for 60`, and the `sql-auditor` process is killed (e.g., OOM, network drop, hard VM reset), the state file may be lost or the process is no longer running. If the user immediately tries to run `observe start` again to restart the capture, the tool will see the existing session is *within* its deadline and **refuse to start**.
**The Impact**: The user is completely locked out of using `observe` for up to 60 minutes. There is no `observe stop`, `observe drop`, or `--force` command to override this state. 
**Remediation**: `observe start` must accept a `--force` flag to explicitly kill and replace a running session, OR `observe stop` must be implemented to allow manual teardown of orphaned sessions without relying on the deadline sweep.

### 2. The XML Parsing & Truncation Fallacy
**The Flaw**: In Slice 1, Step 3, the plan requires eager decoding (`xml.Unmarshal`) and testing truncation by cutting a fixture "in the middle of a bucket", expecting the decoder to return a `Truncated: true` flag and the partial buckets it *did* parse.
**The Exploit**: Standard Go `encoding/xml` is strict. If you feed it XML cut in the middle of a tag, `xml.Unmarshal` will return a `SyntaxError` and a completely empty/incomplete struct. It will not cleanly give you the first $N$ buckets and a truncation flag.
**The Impact**: If the `target_data` is genuinely truncated by SQL Server (due to size limits), the eager decoder will crash or return an error, losing *all* captured data instead of recovering the partial data.
**Remediation**: To recover partial trees from malformed/cut XML, you cannot use eager `xml.Unmarshal`. You must use a token-streaming `xml.Decoder` loop that processes valid `Bucket` tokens until it hits an `EOF` or `SyntaxError`, at which point it flags `Truncated = true` and returns the accumulated buckets.

### 3. Plan Cache Resolution & Statement Offsets Leaking Code
**The Flaw**: In Slice 4, Step 1, plan cache resolution is done using `sys.dm_exec_query_stats` to map a `query_hash` to a text.
**The Exploit**: `sys.dm_exec_query_stats.sql_handle` maps to the *entire batch* of SQL submitted (via `sys.dm_exec_sql_text`), not the individual statement. A batch might be a 500-line stored procedure or script. If multiple statements in that batch are captured, resolving by `sql_handle` alone will return the full 500-line text for *every* unique `query_hash`.
**The Impact**: The archive will massively duplicate text and potentially leak surrounding code (application data, comments) from the batch that wasn't actually part of the measured query.
**Remediation**: The resolution query must use `statement_start_offset` and `statement_end_offset` from `sys.dm_exec_query_stats` in conjunction with `SUBSTRING` on the resolved text to extract *only* the specific statement matching the `query_hash`.

### 4. The "Baseline" Contradiction
**The Flaw**: The plan states: *"The capture is a histogram that keeps accumulating and a baseline subtracted at the end..."* and *"finish refuses when [the state file] is lost, with --no-baseline as the documented way to get the raw counters"*.
**The Exploit**: This conflates how DMVs work with how Extended Events work. An XE Histogram target counts events *since the session was started*. If `observe` creates, starts, and drops the session per run, the histogram **starts at zero**. There is no "baseline" to subtract for the XE histogram. 
If the "baseline" refers to the `Batch Requests/sec` counter used for the cost estimate (which *is* cumulative since server restart), then subtracting a baseline is correct. However, if the SQL Server restarts between `start` and `finish`, the finish counter will be lower than the start counter, resulting in a negative value.
**The Impact**: If the code attempts to subtract a baseline from the XE histogram, it will yield invalid data. If it subtracts from `Batch Requests/sec` without handling server restarts, it will crash or report negative cost.
**Remediation**: Clarify that the XE histogram requires *no* baseline. Add logic to handle negative diffs for `Batch Requests/sec` (e.g., detecting a server restart via `tempdb` creation date or simply discarding the cost estimate if `finish < start`).

## Secondary Risks & Edge Cases

1. **Histogram Overflow (`slots`)**: The XE histogram has a maximum slot limit (often 256 or 1024). For highly unparameterized workloads (e.g., ORMs generating `SELECT * WHERE id = 1`, `id = 2`), the histogram will instantly fill, and all subsequent queries will be dumped into a single overflow bucket. The plan mentions logging the overflow attribute, but `sql-auditor` should explicitly warn the user if the overflow bucket contains a significant percentage of total events, as the capture's granularity is compromised.
2. **`MAX_DISPATCH_LATENCY` Lag**: Because the default latency is often 30 seconds, `observe status` may report 0 events for the first 30 seconds of a healthy capture. The CLI must communicate this clearly (e.g., *"0 events (buffers flush every 30s)"*) so users don't assume the capture is broken and forcefully kill it.
3. **Permission Pre-Flight**: Since the `observe` DDL lives in `collect/observesql/` and not the main corpus, the standard `check` command will not verify if the user has `ALTER ANY EVENT SESSION`. `observe` should run a quick `HAS_PERMS_BY_NAME` pre-flight check before attempting to create the session to fail fast and cleanly.

## Tactical Code Review Notes
* **Slice 0 (Hash Conversion)**: Spot on. The conversion of `binary(8)` to `bigint` is fraught with endianness and sign-extension pitfalls. Proving this first is the smartest decision in the plan.
* **Slice 2 (Clock Mocking)**: Deriving the deadline from `create_time + max-minutes` is robust, provided `create_time` is read from the server's UTC time (`sys.dm_xe_sessions.create_time`), not the client's clock, to avoid clock skew issues.
