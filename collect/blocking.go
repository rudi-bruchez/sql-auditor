package collect

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// BlockingHowTo is where an operator is sent when the instance is waiting on
// locks and nothing is capturing why. It is a script to read and run, never one
// this tool runs: creating a session and changing sp_configure are writes, and
// this tool does not write to a server.
const BlockingHowTo = "https://github.com/rudi-bruchez/tsql-scripts/blob/main/extended-events/on-prem/blocked-processes-create.sql"

// BlockingReadiness is what `check` needs to answer one question: if this
// instance is losing time to locks, will a collection be able to say why?
//
// Three facts decide it, and all three are reported rather than reduced to a
// verdict. A session can exist and the threshold be 0, in which case it has
// never received an event; the threshold can be set and no session subscribe,
// in which case the event fires into nothing; and both can be right while the
// instance has no lock waits at all, in which case there is nothing to look for.
type BlockingReadiness struct {
	// Probed is false when the query could not run — a denied VIEW SERVER
	// STATE, a timeout. Every other field is then meaningless, and the caller
	// must say "not checked" rather than "nothing found".
	Probed bool
	Err    error

	LockWaitMS    int64
	LockWaitTasks int64
	MaxLockWaitMS int64
	TotalWaitMS   int64

	// ThresholdSeconds is 'blocked process threshold (s)'. At 0 — the default —
	// the blocked_process_report event cannot fire, whatever subscribes to it.
	ThresholdSeconds int
	// Sessions subscribing to blocked_process_report, and how many of those are
	// running. A session that is defined and stopped captures nothing, and is a
	// different problem from one that does not exist.
	SessionsCapturing int
	SessionsRunning   int
	// WritesToFile counts the capturing sessions that have an event_file
	// target. Only those can be read back by a collection: a ring-buffer-only
	// capture is visible in SSMS and cannot be exported.
	WritesToFile int
}

// LockShare is the share of all recorded wait time spent waiting on locks, 0
// when nothing has been recorded. It is a proportion rather than a verdict: the
// same 200 hours means one thing on an instance up for a week and another on
// one up for two years, and only the reader knows which.
func (r BlockingReadiness) LockShare() float64 {
	if r.TotalWaitMS <= 0 {
		return 0
	}
	return float64(r.LockWaitMS) * 100 / float64(r.TotalWaitMS)
}

// HasLockWaits is the trigger for saying anything at all, and it is deliberately
// the loosest test that is still true: any recorded lock wait.
//
// A share threshold was the obvious alternative and is worse. Picking one —
// 5%, 10% — would be this tool declaring how much blocking is too much, which
// is exactly the judgement it does not make. The loose test costs a few lines
// of output on an instance that does not need them; a tight one costs a return
// visit to a client. The figures are printed beside it so a reader can decide
// the share is noise, which they can do and this code cannot.
func (r BlockingReadiness) HasLockWaits() bool { return r.Probed && r.LockWaitMS > 0 }

// CanCapture reports whether a collection could export blocked process reports
// as things stand: something has to be subscribed, running, writing to a file,
// AND the threshold has to be non-zero. Miss any one and the archive gets an
// empty directory.
func (r BlockingReadiness) CanCapture() bool {
	return r.Probed && r.ThresholdSeconds > 0 && r.SessionsRunning > 0 && r.WritesToFile > 0
}

const blockingProbeSQL = `
SET NOCOUNT ON;
SELECT
    SUM(CASE WHEN w.wait_type LIKE 'LCK[_]M[_]%' THEN w.wait_time_ms ELSE 0 END),
    SUM(CASE WHEN w.wait_type LIKE 'LCK[_]M[_]%' THEN w.waiting_tasks_count ELSE 0 END),
    MAX(CASE WHEN w.wait_type LIKE 'LCK[_]M[_]%' THEN w.max_wait_time_ms ELSE 0 END),
    SUM(w.wait_time_ms),
    (SELECT CAST(c.value_in_use AS int) FROM sys.configurations AS c
      WHERE c.name = 'blocked process threshold (s)'),
    (SELECT COUNT(DISTINCT e.event_session_id)
       FROM sys.server_event_session_events AS e
      WHERE e.name = 'blocked_process_report'),
    (SELECT COUNT(DISTINCT s.event_session_id)
       FROM sys.server_event_sessions AS s
       JOIN sys.server_event_session_events AS e ON e.event_session_id = s.event_session_id
       JOIN sys.dm_xe_sessions AS r ON r.name = s.name
      WHERE e.name = 'blocked_process_report'),
    (SELECT COUNT(DISTINCT s.event_session_id)
       FROM sys.server_event_sessions AS s
       JOIN sys.server_event_session_events AS e ON e.event_session_id = s.event_session_id
       JOIN sys.server_event_session_targets AS t ON t.event_session_id = s.event_session_id
      WHERE e.name = 'blocked_process_report' AND t.name = 'event_file')
FROM sys.dm_os_wait_stats AS w
OPTION (RECOMPILE, MAXDOP 1);`

// ProbeBlocking runs the readiness probe. Like the preflight probes it never
// returns an error to the caller: a probe that could not run is an answer, and
// `check` must not fail because one advisory section could not be filled in.
func ProbeBlocking(ctx context.Context, c *sql.Conn) BlockingReadiness {
	var r BlockingReadiness
	// Its own deadline, short. This is the least important thing check does and
	// must never be what makes it hang.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	row := c.QueryRowContext(ctx, blockingProbeSQL)
	if err := row.Scan(&r.LockWaitMS, &r.LockWaitTasks, &r.MaxLockWaitMS, &r.TotalWaitMS,
		&r.ThresholdSeconds, &r.SessionsCapturing, &r.SessionsRunning, &r.WritesToFile); err != nil {
		r.Err = err
		return r
	}
	r.Probed = true
	return r
}

// BlockingNotice is what check prints, as lines, or nil when there is nothing
// worth saying. Separated from the printing so it can be tested against every
// combination of the three facts, which is where the reasoning lives.
//
// It is advice, and advice belongs in `check` rather than in the archive. The
// archive states what the instance is; check helps an operator prepare a run.
// Nothing in this function ever reaches MANIFEST.txt.
func BlockingNotice(r BlockingReadiness) []string {
	if !r.Probed {
		if r.Err == nil {
			return nil
		}
		return []string{fmt.Sprintf("could not check whether blocking is being captured: %v", r.Err)}
	}
	if !r.HasLockWaits() {
		// Nothing has waited on a lock since the counters were last cleared. A
		// missing capture is then not worth a paragraph, and saying so anyway
		// would train the reader to skip this section.
		return nil
	}

	hours := float64(r.LockWaitMS) / 3600000
	head := fmt.Sprintf("Lock waits: %.1f h across %d waits, %.1f%% of all recorded wait time; longest single wait %.1f s.",
		hours, r.LockWaitTasks, r.LockShare(), float64(r.MaxLockWaitMS)/1000)
	// Said once, here, because every number above is cumulative since the
	// counters were last cleared and a reader who does not know that will read a
	// two-year total as a description of today.
	head += " These totals accumulate since the instance started or the counters were last cleared."

	if r.CanCapture() {
		return []string{
			head,
			fmt.Sprintf("Blocked process reports ARE being captured: %d session(s) subscribed, %d running, threshold %d s.",
				r.SessionsCapturing, r.SessionsRunning, r.ThresholdSeconds),
			"Run collect with --include-blocked-process-reports to export them as .xml files.",
		}
	}

	out := []string{head, "Nothing on this instance can currently produce a blocked process report:"}
	for _, f := range blockingFailures(r) {
		out = append(out, "  - "+f+".")
	}
	out = append(out,
		"To capture them, a DBA can create the session and set the threshold with this script:",
		"  "+BlockingHowTo,
		"This tool will not make either change: both are writes to the server.")
	return out
}

// BlockingCaptureLines describes the blocked process capture for the wizard's
// verification screen: the first line is the status word — "ready", "not
// ready", "not checked" — and the rest are the facts behind it, one sentence
// per line, unwrapped. The renderer aligns the first and folds the others.
//
// It deliberately does not call BlockingNotice, which returns nil as soon as
// HasLockWaits() is false. That is a considered decision there and the right
// one: `check` prints advice, and an instance with no lock waits does not need
// a paragraph about capturing them. It is ruinous here. The wizard shows this
// block on every instance, and on one restarted last night — wait counters back
// to zero — the block would come out empty while the capture is in fact absent.
// The screen would then be silent about the very thing it was asked to report.
//
// Nothing here is a judgement about the instance: the sentences state which of
// the four preconditions is missing, and the URL points at a script a DBA runs
// themselves. This tool does not write to a server, and creating an Extended
// Events session and changing sp_configure are both writes.
func BlockingCaptureLines(r BlockingReadiness) []string {
	if !r.Probed {
		// "not checked", never "not ready" and never 0: an absent measurement
		// is not a measurement of absence, and a DBA who reads "not ready" here
		// goes and configures something that may already be configured.
		if r.Err != nil {
			return []string{"not checked", fmt.Sprintf("the probe could not run: %v", r.Err)}
		}
		return []string{"not checked", "the probe did not run, so nothing is known about this capture"}
	}
	if r.CanCapture() {
		return []string{
			"ready",
			fmt.Sprintf("%d session(s) subscribe to blocked_process_report, %d running, %d writing to a file, threshold %d s",
				r.SessionsCapturing, r.SessionsRunning, r.WritesToFile, r.ThresholdSeconds),
		}
	}

	out := append([]string{"not ready"}, blockingFailures(r)...)
	return append(out, "to set the capture up:", BlockingHowTo)
}

// blockingFailures names every precondition of the capture that is missing,
// one sentence per line, with NO trailing punctuation and no bullet: the two
// callers dress them differently, and only the dressing differs.
//
// It exists because BlockingNotice and BlockingCaptureLines carried the same
// three-branch switch, the same four sentences to a full stop, and the same
// ThresholdSeconds test in two places. The spec's one demand about this pair is
// that the two formulations "must not drift apart", and sitting next to each
// other in the file is not a mechanism — a correction applied to one copy would
// not reach the other. Now there is one copy.
func blockingFailures(r BlockingReadiness) []string {
	var out []string
	// The three session failures are mutually exclusive and get three distinct
	// sentences, because they cost three different actions: create a session,
	// start it, or give it an event_file target.
	switch {
	case r.SessionsCapturing == 0:
		out = append(out, "no Extended Events session subscribes to blocked_process_report")
	case r.SessionsRunning == 0:
		out = append(out, fmt.Sprintf("%d session(s) subscribe to blocked_process_report, but none is running", r.SessionsCapturing))
	case r.WritesToFile == 0:
		// This one looks healthy in SSMS: the events are captured and visible
		// live, they are simply nowhere on disk, so no collection can bring
		// them back.
		out = append(out, "the capturing session writes to a ring buffer only, with no event_file target, "+
			"so its events cannot be exported (they are visible live in SSMS and nowhere else)")
	}
	// Compounding rather than exclusive: every session can be right and the
	// threshold still be 0, in which case the event has never fired once. An
	// operator told about only one of the two comes back a second time.
	if r.ThresholdSeconds == 0 {
		out = append(out, "'blocked process threshold (s)' is 0, the default, so the event never fires at all")
	}
	return out
}
