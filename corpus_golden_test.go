package sqlauditor_test

import (
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/rudi-bruchez/sql-auditor/collect"
)

// The corpus inventory is a golden file rather than a number, and that is a
// change of kind and not of style.
//
// A count can only ever report "got 74, want 75". It names nothing, so every
// addition costs a round trip to find out which file the run was missing — and
// it is blind to a rename, which leaves the total untouched while changing what
// the archive holds. The list names the file in the failure message, and a
// rename shows up as one path gone and one arrived.
//
// Regenerate after adding or removing a collector:
//
//	go test . -run TestEmbeddedCorpusIsValid -update
//
// Regenerating is deliberate and never automatic. The point of the file is that
// a collector cannot enter or leave the corpus without someone saying so.
var updateCorpus = flag.Bool("update", false,
	"rewrite testdata/corpus.txt from the embedded corpus")

const corpusGolden = "testdata/corpus.txt"

func checkCorpusInventory(t *testing.T, scripts []collect.Script) {
	t.Helper()

	got := make([]string, 0, len(scripts))
	for _, s := range scripts {
		got = append(got, inventoryLine(s))
	}
	sort.Strings(got)

	if *updateCorpus {
		path := filepath.FromSlash(corpusGolden)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(strings.Join(got, "\n")+"\n"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", corpusGolden, err)
		}
		t.Logf("wrote %s with %d collectors", corpusGolden, len(got))
		return
	}

	want, err := readCorpusGolden()
	if err != nil {
		t.Fatalf("%v\nregenerate with: go test . -run TestEmbeddedCorpusIsValid -update", err)
	}

	// Errorf and not Fatalf, and that is the second half of the fix. An
	// inventory mismatch used to abort the whole test, so the lint checks below
	// never ran and every collector added cost one round trip to learn the
	// count and another to learn what was wrong with the file. The inventory
	// and the lint are independent facts; one run should report both.
	wantProfiles := map[string]string{}
	for _, l := range want {
		p, prof := splitInventoryLine(l)
		wantProfiles[p] = prof
	}
	gotProfiles := map[string]string{}
	for _, l := range got {
		p, prof := splitInventoryLine(l)
		gotProfiles[p] = prof
	}
	for p, prof := range gotProfiles {
		w, ok := wantProfiles[p]
		switch {
		case !ok:
			t.Errorf("%s is in the corpus and not in %s: a new collector, or a rename "+
				"whose other half is below", p, corpusGolden)
		case w != prof:
			t.Errorf("%s: profiles %q in the corpus, %q in %s", p, prof, w, corpusGolden)
		}
	}
	for p := range wantProfiles {
		if _, ok := gotProfiles[p]; !ok {
			t.Errorf("%s is in %s and not in the corpus: a collector was removed, renamed, "+
				"or is no longer embedded", p, corpusGolden)
		}
	}
}

// inventoryLine is the path, followed by the profiles the collector declares
// when it declares any. Membership is in the golden file so that a collector
// cannot enter or leave a profile without the diff saying so.
func inventoryLine(s collect.Script) string {
	if len(s.Profiles) == 0 {
		return s.Path
	}
	p := append([]string(nil), s.Profiles...)
	sort.Strings(p)
	return s.Path + " @profiles: " + strings.Join(p, ", ")
}

// splitInventoryLine undoes inventoryLine.
func splitInventoryLine(line string) (path, profiles string) {
	path, profiles, _ = strings.Cut(line, " @profiles: ")
	return path, profiles
}

// readCorpusGolden tolerates CRLF: the working tree of this repository is CRLF
// under core.autocrlf, so the golden file arrives with whatever endings git
// checked out and the inventory must not depend on which.
func readCorpusGolden() ([]string, error) {
	b, err := os.ReadFile(filepath.FromSlash(corpusGolden))
	if err != nil {
		return nil, err
	}
	var want []string
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			want = append(want, line)
		}
	}
	return want, nil
}
