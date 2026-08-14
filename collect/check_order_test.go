package collect

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// hangingInstance is a TCP listener that accepts and then says nothing, plus
// the hangUp that lets a caller end the silence. It is not a fake SQL Server
// and does not try to be: the driver writes its prelogin packet and waits for a
// reply that never comes, which models faithfully enough the instance this test
// is about — one that is up, accepts the socket, and takes minutes to answer.
// "invalid.invalid" cannot serve here precisely because it fails instantly.
//
// hangUp exists because the driver's handshake read does not watch the context:
// once the socket is open, only the far end going away brings Check back.
func hangingInstance(t *testing.T) (addr string, hangUp func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	closed := false
	hangUp = func() {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return
		}
		closed = true
		ln.Close()
		for _, c := range conns {
			c.Close()
		}
	}
	t.Cleanup(hangUp)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			if closed {
				c.Close()
			} else {
				conns = append(conns, c)
			}
			mu.Unlock()
		}
	}()
	return ln.Addr().String(), hangUp
}

// Check must print the corpus listing and the Output line BEFORE it opens a
// socket. Every probe of the second half runs to SQL_QUERY_TIMEOUT_SEC on an
// instance that is struggling, so an operator who sees nothing cannot tell a
// working tool from a hung one — and `check > report.txt` killed halfway would
// leave an empty file instead of the findings already established about the
// corpus and the output directory.
//
// The assertion is the ordering, not the content: the listing has to be
// readable while Check is still blocked on the connection.
func TestCheckPrintsTheCorpusBeforeItTouchesTheInstance(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "output")
	cfg := checkConfig(dir)
	var hangUp func()
	cfg.Server, hangUp = hangingInstance(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = w

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			os.Stdout = saved
			w.Close()
		}()
		Check(ctx, Options{Config: cfg, Corpus: checkCorpus, Root: "queries"})
	}()

	// The read is bounded and happens off this goroutine. A Check that prints
	// nothing until it has finished probing would block it forever, and a suite
	// that dies on the package timeout names no test — this way the regression
	// fails in five seconds with a sentence describing it.
	want := checkListing + "\nOutput   : " + dir + "\n"
	listing := make(chan string, 1)
	go func() {
		buf := make([]byte, len(want))
		n, _ := io.ReadFull(r, buf)
		listing <- string(buf[:n])
	}()

	var got string
	timedOut := false
	select {
	case got = <-listing:
	case <-time.After(5 * time.Second):
		timedOut = true
	}
	// Check is still inside the connection attempt. If this second select
	// fires, the listing only became readable because Check had finished — the
	// same regression, seen on an instance that refused faster than this test
	// could look.
	returnedEarly := false
	select {
	case <-done:
		returnedEarly = true
	default:
	}

	// Let go of the instance before reporting anything: the failure paths below
	// all end the test, and a Check still blocked on a socket nobody will ever
	// answer would hold os.Stdout for every test that follows. Hanging up is
	// what releases it — the driver's handshake read does not watch the
	// context — and the cancel is there for the probes that come after it.
	cancel()
	hangUp()
	drained := make(chan struct{})
	go func() { io.Copy(io.Discard, r); close(drained) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Error("Check did not return after the instance hung up")
	}
	<-drained
	r.Close()

	switch {
	case timedOut:
		t.Fatal("nothing reached stdout while Check was connecting; the listing now waits on the instance")
	case returnedEarly:
		t.Error("Check had already returned when the listing arrived; it probed the instance before printing anything")
	case got != want:
		t.Errorf("stdout mismatch\n got: %q\nwant: %q", got, want)
	}
}
