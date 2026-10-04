package collect

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// view_server_security_state exists because VIEW SERVER PERFORMANCE STATE
// opens the DMVs and not sys.dm_database_encryption_keys, so a login can pass
// the performance probe and still be refused the keys. A probe that read any
// other view would pass for exactly that login, and CI runs as sa, where
// every probe passes whatever it reads. So the probe is pinned to the view,
// and the view to the collectors that declare the capability: each of them
// must read it, or the probe vouches for something they do not need.
func TestTheSecurityStateProbeReadsTheEncryptionKeys(t *testing.T) {
	const view = "sys.dm_database_encryption_keys"
	var probe string
	for _, c := range Capabilities() {
		if c.Name == "view_server_security_state" {
			probe = c.SQL
		}
	}
	from := regexp.MustCompile(`(?i)\bFROM\s+([\w.\[\]]+)`).FindAllStringSubmatch(probe, -1)
	if len(from) != 1 || !strings.EqualFold(from[0][1], view) {
		t.Fatalf("the probe reads %v, want %s alone: %q", from, view, probe)
	}

	scripts, err := Discover(os.DirFS(".."), "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	declared := 0
	for _, s := range scripts {
		// Optional or required, a declaration is a claim that the file reads
		// the view, and the grant script asks for the permission on it.
		for _, p := range append(slices.Clone(s.Permissions), s.OptionalPermissions...) {
			if p != "view_server_security_state" {
				continue
			}
			declared++
			b, err := os.ReadFile(filepath.Join("..", "queries", s.Path))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.ToLower(StripSQLComments(string(b))), view) {
				t.Errorf("%s declares VIEW SERVER SECURITY STATE and does not read %s, which is what the probe tests", s.Path, view)
			}
		}
	}
	if declared == 0 {
		t.Error("no collector declares VIEW SERVER SECURITY STATE; the probe tests nothing anyone needs")
	}
}
