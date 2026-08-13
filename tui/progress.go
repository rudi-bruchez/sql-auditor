package tui

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// progressFileName is where the run's stderr commentary ends up, relative to
// OUTPUT_DIR. Dated rather than timestamped, and suffixed by freeName when
// taken, so a second run on the same day sits beside the first instead of on
// top of it.
func progressFileName(t time.Time) string {
	return "sql-auditor-" + t.Format("2006-01-02") + ".log"
}

// flushProgress writes the buffer collect.Run has been writing its commentary
// into to a file, and returns the path so the final screen can name it.
//
// This function is the price of taking the terminal. Everything collect writes
// to stderr — the run replacement warning, "connection lost; attempting one
// reconnect", the manifest's fallbacks — would normally land in front of the
// operator; under a full-screen wizard it would land in the middle of a frame,
// so the wizard hands collect a memory buffer instead. That buffer is the ONLY
// copy of those lines, which makes losing it unacceptable rather than untidy:
// lastResort exists so that a run always leaves a trace, including when no
// filesystem the process can reach will take the manifest, and in that case
// the entire manifest is in this buffer and nowhere else. io.Discard is
// forbidden on this path, and a buffer that is never flushed is io.Discard
// with extra steps.
//
// Hence the shape of the failure: if the destination refuses the write, the
// content goes to os.Stderr — after the terminal has been restored, so it is
// readable — and the error is returned rather than swallowed. Writing to the
// terminal is the worse outcome of the two, and it is still infinitely better
// than the alternative, which is that the one existing copy of the manifest
// dies with the process.
func flushProgress(buf *bytes.Buffer, outputDir string, now time.Time) (string, error) {
	// Nothing to say is the normal case: a clean run writes nothing to stderr,
	// and producing an empty log file next to the archive would only raise the
	// question of what went wrong.
	if buf == nil || buf.Len() == 0 {
		return "", nil
	}
	// An absolute path, for the same reason the grant script gets one: a
	// double-clicked binary can have any working directory at all, and a
	// relative path on the final screen is a path the operator cannot follow.
	dir, err := filepath.Abs(outputDir)
	if err != nil {
		return "", spill(buf, err)
	}
	path, err := freeName(dir, progressFileName(now))
	if err != nil {
		return "", spill(buf, err)
	}
	// 0o600: these lines name an instance, its databases and whatever the
	// server said when it refused something, on a machine the auditor does not
	// own.
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return "", spill(buf, err)
	}
	return path, nil
}

// spill is the last-resort of the last resort. It puts the buffer on stderr,
// where a redirection or a scrollback can still catch it, and hands the caller
// the original error so the exit path can report both.
func spill(buf *bytes.Buffer, cause error) error {
	fmt.Fprintf(os.Stderr, "could not write the run log (%v); it follows:\n", cause)
	// Ignoring this error is not an oversight: if stderr is gone too there is
	// nowhere left to report the failure of reporting.
	_, _ = os.Stderr.Write(buf.Bytes())
	if n := buf.Len(); n > 0 && buf.Bytes()[n-1] != '\n' {
		fmt.Fprintln(os.Stderr)
	}
	return cause
}
