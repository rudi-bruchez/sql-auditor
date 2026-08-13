package collect

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// maxRunBytes bounds the collector payloads written into a run folder, not
// the run folder itself: MANIFEST.txt and _run.json are written outside this
// budget, because an archive that cannot describe itself is worse than a
// truncated one. The name and the value (256 MiB) date from when this budget
// covered Query Store plan extraction alone; it now covers every collector
// in the run, which is far less data than 256 MiB in practice. The number
// stayed generous on purpose rather than being retuned down, so a reader
// should not infer from its size that it was sized for plans specifically.
const maxRunBytes = 256 << 20

// showplanNS is the XML namespace SQL Server stamps onto every execution
// plan it emits, in any of the shapes a plan reaches disk (a standalone
// .sqlplan file, an XML column inside a Query Store JSON blob, and so on).
// Testing for its presence in raw bytes is cheaper and harder to get wrong
// than trying to enumerate those shapes.
var showplanNS = []byte("http://schemas.microsoft.com/sqlserver/2004/07/showplan")

// containsShowplan reports whether payload contains an execution plan, by
// looking for the showplan XML namespace anywhere in the bytes. A false
// positive over-discloses — it can only make the archive claim to contain a
// plan that isn't really one — which is the acceptable side of this error,
// so no more precise a check is attempted.
func containsShowplan(payload []byte) bool {
	return bytes.Contains(payload, showplanNS)
}

// runWriter is the single choke point through which every collector payload
// is written into a run folder. It exists because there are two write paths
// today (the general collectors, and the Query Store plan extraction) and
// probably a third later, and a showplan check wired into the paths one
// remembers today is a check that silently misses the next one added
// tomorrow. Routing all writes through here means the presence of an
// execution plan in an archive can be read off the bytes actually written,
// rather than guessed from which collector produced them.
//
// One writer serves the whole run, but the plan flag is read per unit: the
// warning it feeds has to name the script and the database that wrote the
// plan, which a run-level total cannot do. See takeShowplan.
type runWriter struct {
	root   string
	budget int
	spent  int
	// sawShowplan records that a plan reached disk. It is NOT a run-level
	// total: takeShowplan reads and clears it after every unit, so what it
	// holds is "a plan was written since the last unit was accounted for".
	// The run-level fact lives in the manifest, which is set-only.
	sawShowplan bool
}

// takeShowplan reports whether a plan reached disk since the last call, and
// clears the flag. The run writer is shared by every unit, so a flag left set
// would make the next collector answer for this one's plan — and the warning
// it drives exists precisely to name the script responsible.
//
// Read-and-reset is safe only because the fact it feeds is latched elsewhere
// and never cleared: discloseWrites sets Manifest.Collected.QueryStoreDetail
// and nothing in the codebase ever sets it back to false. The first unit's
// plan is therefore recorded in the manifest before the flag is consumed, and
// a later unit that writes nothing cannot retract it. Anything that gains the
// power to clear a Collected field breaks this and must not.
func (w *runWriter) takeShowplan() bool {
	saw := w.sawShowplan
	w.sawShowplan = false
	return saw
}

// newRunWriter returns a runWriter rooted at root, refusing to write more
// than budget bytes of collector payloads in total.
//
// It takes no warning channel. One was carried here for a while and no method
// ever called it: the writers reach the manifest through WriteRequest.Warn,
// which is the closure that knows which script and which database is writing.
// A field with no reader reads as a working feature, so it is gone rather than
// kept for a caller that never came.
func newRunWriter(root string, budget int) *runWriter {
	return &runWriter{root: root, budget: budget}
}

// write saves payload at rel, relative to the writer's root, using slashes
// regardless of host OS (converted with filepath.FromSlash so the same rel
// path works on Windows). It refuses the write outright if it would push
// total spend over the budget — nothing is written in that case, not even a
// partial file — otherwise it creates any missing parent directories, writes
// the file, updates spent, and records whether the payload contained an
// execution plan.
func (w *runWriter) write(rel string, payload []byte) (int, error) {
	if w.spent+len(payload) > w.budget {
		return 0, fmt.Errorf("runWriter: writing %s would exceed the %d byte budget (%d already spent)", rel, w.budget, w.spent)
	}
	return w.put(rel, payload)
}

// put is the choke point itself: the bytes reach disk here and nowhere else,
// and the Showplan inspection and the running total are applied here for both
// callers. It is one method rather than a body copied into each because the
// duplicate could not be enforced by the comment that asked for it — and the
// anonymisation hook a later version needs is exactly this body, which must not
// have to be added twice.
func (w *runWriter) put(rel string, payload []byte) (int, error) {
	full := filepath.Join(w.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return 0, fmt.Errorf("runWriter: creating directory for %s: %w", rel, err)
	}
	if err := os.WriteFile(full, payload, 0o644); err != nil {
		return 0, fmt.Errorf("runWriter: writing %s: %w", rel, err)
	}

	w.spent += len(payload)
	if containsShowplan(payload) {
		w.sawShowplan = true
	}
	return len(payload), nil
}

// writeUnbudgeted saves payload the way write does — same root, same slash
// conversion, same directory creation, same Showplan inspection, same running
// byte total — but neither consults nor is refused by the budget.
//
// It exists for the files that describe what a run contains: MANIFEST.txt,
// _run.json, and a collector directory's _index.json. The reasoning above
// applies unchanged to all three. An archive that cannot describe itself is
// worse than a truncated one, and a budget that dies one file before the index
// would leave query texts and execution plans on disk with nothing saying they
// are there — exactly the undescribed directory the index exists to prevent.
// Its bytes still count towards spent, so the total the manifest reports stays
// the total actually written; it just cannot be turned away.
//
// This is not a second write path, and that is now true by construction rather
// than by assertion: it calls the same put as write does, with the budget test
// left out. The Showplan inspection is the reason the choke point is single,
// and a copy of the body could have lost it here without anything failing —
// putting a plan on disk that the archive never admits to holding.
func (w *runWriter) writeUnbudgeted(rel string, payload []byte) (int, error) {
	return w.put(rel, payload)
}

// overBudget reports whether the writer has spent its full budget.
func (w *runWriter) overBudget() bool {
	return w.spent >= w.budget
}
