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
// today (the general collectors, and the Query Store plan extraction to
// come) and probably a third later, and a showplan check wired into the
// paths one remembers today is a check that silently misses the next one
// added tomorrow. Routing all writes through here means the presence of an
// execution plan in an archive can be read off the bytes actually written,
// rather than guessed from which collector produced them.
type runWriter struct {
	root        string
	budget      int
	spent       int
	sawShowplan bool
	warn        func(string)
}

// newRunWriter returns a runWriter rooted at root, refusing to write more
// than budget bytes of collector payloads in total. warn is called for
// conditions worth surfacing to the operator without failing the write.
func newRunWriter(root string, budget int, warn func(string)) *runWriter {
	return &runWriter{root: root, budget: budget, warn: warn}
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

// overBudget reports whether the writer has spent its full budget.
func (w *runWriter) overBudget() bool {
	return w.spent >= w.budget
}
