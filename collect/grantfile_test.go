package collect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// check --grant-script used to truncate whatever its path named, so a script
// that had been reviewed and signed off was replaced by the next run. Measured
// on 0.23.0: a one-line file became the 2 274-byte generated script, exit 0.
func TestWriteNewFileRefusesAnExistingFileUnlessForced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reviewed.sql")
	if err := os.WriteFile(path, []byte("signed off by the DBA\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := writeNewFile(path, []byte("GRANT ...\n"), false)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("writeNewFile over an existing file = %v, want an already-exists refusal", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "signed off by the DBA\n" {
		t.Errorf("the existing file was changed by a refused write: %q", b)
	}

	if err := writeNewFile(path, []byte("GRANT ...\n"), true); err != nil {
		t.Fatalf("writeNewFile with force: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "GRANT ...\n" {
		t.Errorf("force did not replace the file: %q", b)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("a replaced grant script keeps mode %v, want 0600", fi.Mode().Perm())
	}
}
