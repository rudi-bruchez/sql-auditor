package collect

import (
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"testing"
	"time"
)

// A connection attempt connWithin gives up on must not outlive it. Against a
// server that accepts the socket and never answers, connWithin returns at its
// deadline; the socket it dialed must then be closed from the client side, so
// that the driver's read returns and the attempt's goroutines exit. Before,
// both lived as long as the server kept the socket open, which in the
// assistant meant one leak per attempt.
//
// Not parallel: the goroutine count is only meaningful with no other test
// starting or ending goroutines meanwhile.
func TestConnWithinClosesTheSocketOfAnAbandonedLogin(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()

	// The dial timeout is ConnectTimeout: a minute, so that nothing but the
	// abandon can end the attempt within this test.
	db, err := Open(&Config{Server: ln.Addr().String(), User: "AUDIT_RO", Password: "x",
		AppName: "sql-auditor-test", ConnectTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	baseline := runtime.NumGoroutine()

	const deadline = 300 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	began := time.Now()
	c, err := connWithin(ctx, db)
	took := time.Since(began)
	if c != nil {
		c.Close()
		t.Fatal("the silent server gave a connection")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline", err)
	}
	if took > deadline+time.Second {
		t.Errorf("connWithin returned after %v, deadline %v", took, deadline)
	}

	var server net.Conn
	select {
	case server = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("the driver never dialed the listener")
	}
	defer server.Close()
	// The pre-login packet arrives first; what follows must be the end of the
	// stream, not silence. io.Copy returns nil on EOF and the timeout otherwise.
	server.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.Copy(io.Discard, server); err != nil {
		t.Fatalf("the client side of the abandoned socket was not closed: %v", err)
	}

	limit := time.Now().Add(2 * time.Second)
	n := runtime.NumGoroutine()
	for n > baseline && time.Now().Before(limit) {
		time.Sleep(10 * time.Millisecond)
		n = runtime.NumGoroutine()
	}
	if n > baseline {
		t.Errorf("%d goroutines two seconds after the abandon, %d before the attempt", n, baseline)
	}
	t.Logf("returned after %v; goroutines %d, baseline %d", took, n, baseline)
}
