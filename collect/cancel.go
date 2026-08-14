package collect

import "context"

// recordUnitFailure decides what a failed unit means and writes that decision
// into the manifest. It asks the CONTEXT rather than the error, and it asks it
// before anything is written down.
//
// The distinction is the whole function. On cancellation every error in flight
// says "context canceled": the unit's, then connAlive's ping — which runs on
// the same dead context and so can only fail — then the reconnect's. The run
// used to follow that chain to finishWith(..., 1, "reconnect failed: context
// canceled"), and exit 1 is documented as "the instance could not be reached".
// An operator's Ctrl-C was filed as a network fault, sending whoever read the
// manifest to investigate a server that was answering.
//
// A cancelled context is therefore authoritative over whatever error came
// back, and the error is dropped rather than recorded: it describes the
// stopping, which the manifest states directly, and keeping it would leave the
// record asserting a failure that never happened. Deciding after the
// ErrorEntry had been appended would produce a manifest saying exit_code 0,
// cancelled true, and carrying an error reading "context canceled" — three
// claims that contradict each other in the one document an audit is meant to
// be able to trust.
//
// A live context means the unit failed on its own merits, and the error is
// recorded verbatim.
//
// The returned exit code is 0 on cancellation, and the caller deliberately
// does not lower an exit code already raised by something else: a lint error
// found before the loop is a real partial failure and stopping the run early
// does not undo it.
func recordUnitFailure(ctx context.Context, m *Manifest, script, target string, err error) (int, bool) {
	if ctx.Err() != nil {
		m.Run.Cancelled = true
		return 0, true
	}
	m.Errors = append(m.Errors, ErrorEntry{
		Script: script, Target: target, Message: err.Error(), SQLError: sqlErrorNumber(err),
	})
	return 2, false
}
