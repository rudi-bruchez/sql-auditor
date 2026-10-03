package tui

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// The assistant's first screen opens a connection of its own, and the driver
// bounds the dial and nothing after it. Against a server that accepts the
// socket and never answers, that connection must give up within the budget
// collect.Connect sets, twice SQL_CONNECT_TIMEOUT_SEC, and hand the operator
// back to screen 1. The listener stays silent until the test ends: hanging up
// is what used to bring the call back, and it would hide the regression.
func TestTheAssistantGivesUpOnAServerThatNeverAnswersTheLogin(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()

	r := &runner{opts: baseOptions(), events: make(chan event, 1), done: make(chan struct{})}
	defer close(r.done)
	go r.connect(context.Background(), State{Step: StepConnecting, Server: ln.Addr().String(), User: "auditor"})

	limit := 10 * r.opts.Config.ConnectTimeout
	select {
	case e := <-r.events:
		failed, ok := e.(connectFailedEvent)
		if !ok {
			t.Fatalf("event = %T, want a refusal", e)
		}
		if !strings.Contains(failed.err.Error(), "did not complete the login") {
			t.Errorf("err = %v; want a login that never completed", failed.err)
		}
	case <-time.After(limit):
		t.Fatalf("still connecting after %v to a server that never answers", limit)
	}
}
