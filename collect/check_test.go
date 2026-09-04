package collect

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// Check is the one user-facing command of this package with no test at all,
// and the next change rewrites most of it. These are characterisation tests:
// they assert what Check prints TODAY, byte for byte, so that an extraction
// which quietly swaps two blocks of output cannot pass. They are not a
// statement that the current wording is right — only that changing it has to
// be deliberate.
//
// Nothing here needs a server. Every path below stops before, or at, the first
// socket: the corpus is a fstest.MapFS, and the address is "invalid.invalid" —
// a TLD RFC 2606 reserves so that it never resolves — with a one-second
// connect timeout so the failure is immediate rather than merely eventual.

// checkCorpus is a three-script corpus chosen for the three shapes of the
// listing: an instance script with no annotation, a per-database one, and a
// file that fails the lint. All three lines have different formatting, and the
// lint one also drives the exit code.
var checkCorpus = fstest.MapFS{
	"queries/10.system/010.properties.sql": {Data: []byte(
		"-- @scope: instance\n-- @resultsets: a:object\n-- @timeout: 60\n" +
			"SET NOCOUNT ON;\nSET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;\nSET LOCK_TIMEOUT 10000;\n" +
			"SELECT 1 AS x OPTION (RECOMPILE, MAXDOP 1);")},
	"queries/20.databases/020.files.sql": {Data: []byte(
		"-- @scope: database\n-- @resultsets: a:object\n-- @timeout: 60\n" +
			"SET NOCOUNT ON;\nSET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;\nSET LOCK_TIMEOUT 10000;\n" +
			"SELECT 1 AS x OPTION (RECOMPILE, MAXDOP 1);")},
	"queries/20.databases/030.broken.sql": {Data: []byte(
		"-- @scope: database\nSELECT 1;")},
}

// checkListing is the query listing every reachable path prints first. It is
// spelled out rather than built, because a helper that reproduced the %-42s
// padding would reproduce a formatting mistake as faithfully as the code does.
const checkListing = "Queries (3):\n" +
	"  10.system/010.properties.sql               \n" +
	"  20.databases/020.files.sql                 per database\n" +
	"  !! 20.databases/030.broken.sql                missing the @resultsets directive; " +
	"declare each result set as name:object or name:array\n"

// captureStdout runs f with os.Stdout replaced by a pipe and returns what was
// written to it. Check prints its listing with fmt.Printf, i.e. to the process
// stdout and not to any injectable writer, so intercepting the file handle is
// the only way to read it. The pipe is drained by a goroutine: a listing longer
// than the pipe buffer would otherwise block Check itself.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		r.Close()
		done <- string(b)
	}()
	// Restored through a defer, and in an inner function so the restoration
	// happens before the read below: closing the write end is what ends the
	// read, and a panic inside f that left os.Stdout on an undrained pipe
	// would hang the rest of the package rather than fail one test.
	func() {
		defer func() {
			os.Stdout = saved
			w.Close()
		}()
		f()
	}()
	return <-done
}

// checkConfig is the offline configuration: an address that cannot resolve and
// a connect timeout of one second, so the run reaches the first network call
// and comes straight back.
func checkConfig(outputDir string) *Config {
	return &Config{
		Server:         "invalid.invalid",
		OutputDir:      outputDir,
		ConnectTimeout: time.Second,
		QueryTimeout:   5 * time.Second,
	}
}

// A corpus that cannot be read stops Check before it has printed anything.
// That silence is part of the contract: the CLI prints the returned error, and
// a half-written "Queries (0):" above it would suggest an empty corpus was
// found rather than none.
func TestCheckPrintsNothingWhenTheCorpusCannotBeRead(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-corpus")
	var code int
	var err error
	out := captureStdout(t, func() {
		code, err = Check(context.Background(), Options{
			Config: &Config{Server: "invalid.invalid", QueriesDir: missing,
				OutputDir: t.TempDir(), ConnectTimeout: time.Second},
			Corpus: os.DirFS(missing),
			Root:   ".",
		})
	})
	if out != "" {
		t.Errorf("stdout = %q, want nothing printed before the corpus failure", out)
	}
	if code != 2 {
		t.Errorf("exit code = %d, want 2 (bad configuration)", code)
	}
	if err == nil || !strings.Contains(err.Error(), "query corpus") {
		t.Errorf("error = %v, want one naming the query corpus", err)
	}
}

// An output directory that is really a file is reported inline, on the Output
// line, and does not stop the rest of the listing. The "!! not writable"
// marker is the whole point of that line.
func TestCheckMarksAnUnwritableOutputDirectoryOnTheOutputLine(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var code int
	out := captureStdout(t, func() {
		code, _ = Check(context.Background(), Options{
			Config: checkConfig(file), Corpus: checkCorpus, Root: "queries",
		})
	})
	want := checkListing + "\nOutput   : " + file + "  !! not writable\n"
	if out != want {
		t.Errorf("stdout mismatch\n got: %q\nwant: %q", out, want)
	}
	// The unreachable instance outranks the unwritable directory: Check
	// returns 1 here, and the "cannot reach the instance" line it prints for
	// it goes to stderr, not to the listing above.
	if code != 1 {
		t.Errorf("exit code = %d, want 1 (the instance could not be reached)", code)
	}
}

// The nominal path, up to the first network call. Everything Check can say
// without a server is printed; the connection failure then ends it, and
// nothing else reaches stdout.
func TestCheckPrintsTheListingBeforeFailingToReachTheInstance(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "output")
	var code int
	var err error
	started := time.Now()
	out := captureStdout(t, func() {
		code, err = Check(context.Background(), Options{
			Config: checkConfig(dir), Corpus: checkCorpus, Root: "queries",
		})
	})
	want := checkListing + "\nOutput   : " + dir + "\n"
	if out != want {
		t.Errorf("stdout mismatch\n got: %q\nwant: %q", out, want)
	}
	if code != 1 || err == nil {
		t.Errorf("Check = (%d, %v), want (1, an error) for an instance that cannot be reached", code, err)
	}
	// A guard against a future version that waits on the network: this test
	// must stay a unit test, and .invalid never resolves.
	if d := time.Since(started); d > 30*time.Second {
		t.Errorf("Check took %v; it should fail on name resolution, not on a connect timeout", d)
	}
}

// The Query Store window conflict is printed between the listing and the
// Output line, and its second line depends on the flags: with a Query Store
// flag on, a collection would refuse; with none on, it would warn and carry
// on. Both wordings are asserted because they are the reason the block exists.
func TestCheckPricesTheQueryStoreWindowConflictAgainstTheFlags(t *testing.T) {
	for _, tc := range []struct {
		name   string
		flags  map[string]bool
		second string
	}{
		{"with a Query Store option on", map[string]bool{FlagQueryStoreDetail: true},
			"     with the options above, a collection would stop on this.\n"},
		{"with none on", nil,
			"     no Query Store option is on, so a collection would warn and continue.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "output")
			cfg := checkConfig(dir)
			cfg.QueryStoreWindowConflict = "QUERY_STORE_DAYS was set alongside QUERY_STORE_FROM"
			out := captureStdout(t, func() {
				Check(context.Background(), Options{
					Config: cfg, Corpus: checkCorpus, Root: "queries", Flags: tc.flags,
				})
			})
			want := checkListing +
				"\n  !! " + cfg.QueryStoreWindowConflict + "\n" + tc.second +
				"\nOutput   : " + dir + "\n"
			if out != want {
				t.Errorf("stdout mismatch\n got: %q\nwant: %q", out, want)
			}
		})
	}
}
