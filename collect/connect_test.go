package collect

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The driver bounds the dial and nothing after it, so each door that opens a
// connection is tried against a server that accepts the socket and never
// answers. Each must give up near its own budget, not when the socket dies.
// The listener is never hung up before the assertion: hanging up is what used
// to bring these calls back, and it would hide the regression.

const silentConnectTimeout = time.Second

// within fails the test when f has not returned by limit. The goroutine is
// abandoned rather than waited for: a regression leaves it blocked until the
// test's cleanup closes the listener.
func within(t *testing.T, limit time.Duration, f func()) time.Duration {
	t.Helper()
	began := time.Now()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
		return time.Since(began)
	case <-time.After(limit):
		t.Fatalf("still connecting after %v to a server that never answers", limit)
		return 0
	}
}

func TestRunGivesUpOnAServerThatNeverAnswersTheLogin(t *testing.T) {
	t.Parallel()
	addr, _ := hangingInstance(t)
	out := filepath.Join(t.TempDir(), "output")
	var code int
	var err error
	took := within(t, 10*silentConnectTimeout, func() {
		code, err = Run(context.Background(), Options{
			Config: &Config{Server: addr, OutputDir: out, ConnectTimeout: silentConnectTimeout,
				QueryTimeout: 5 * time.Second},
			Corpus: noMemberCorpus(),
			Root:   "queries",
			Now:    time.Now(),
		})
	})
	if code != 1 || err == nil || !strings.Contains(err.Error(), "did not complete the login") {
		t.Errorf("code %d, err %v; want 1 and a login that never completed", code, err)
	}
	t.Logf("the first connection gave up after %v", took)
}

func TestVerifyServerGivesUpOnAServerThatNeverAnswersTheLogin(t *testing.T) {
	t.Parallel()
	addr, _ := hangingInstance(t)
	o := Options{
		Config: &Config{Server: addr, OutputDir: t.TempDir(), ConnectTimeout: silentConnectTimeout,
			QueryTimeout: 5 * time.Second},
		Corpus: checkCorpus, Root: "queries",
	}
	v, _ := VerifyLocal(o)
	within(t, 10*silentConnectTimeout, func() { _ = VerifyServer(context.Background(), o, &v) })
	if v.ConnErr == nil || !strings.Contains(v.ConnErr.Error(), "did not complete the login") {
		t.Errorf("ConnErr = %v; want a login that never completed", v.ConnErr)
	}
}

func TestTheWatchGivesUpOnAServerThatNeverAnswersTheLogin(t *testing.T) {
	t.Parallel()
	addr, _ := hangingInstance(t)
	cfg := &Config{Server: addr, AppName: "sql-auditor-test", ConnectTimeout: silentConnectTimeout}
	var w *blockingWatch
	var reason string
	within(t, 10*silentConnectTimeout, func() {
		var stop func()
		w, stop, reason = startBlockingWatch(context.Background(), cfg, nil, 55)
		stop()
	})
	if w != nil || !strings.Contains(reason, "could not be opened") {
		t.Errorf("watch %v, reason %q; want no watch and a connection that could not be opened", w, reason)
	}
}

// recycleConn hands the run's connection back between collectors and takes a
// fresh one, which opens a new session whenever the driver had marked the old
// one bad. The connection given back here comes from a fake driver, the pool
// from the silent server.
func TestRecycleConnGivesUpOnAServerThatNeverAnswersTheLogin(t *testing.T) {
	t.Parallel()
	addr, _ := hangingInstance(t)
	cfg := &Config{Server: addr, ConnectTimeout: silentConnectTimeout, QueryTimeout: 5 * time.Second}
	db, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	old, _ := openFake(t, nil)
	var rerr error
	within(t, 10*silentConnectTimeout, func() {
		_, _, rerr = recycleConn(context.Background(), db, old, cfg)
	})
	if rerr == nil || !strings.Contains(rerr.Error(), "did not complete the login") {
		t.Errorf("err = %v; want a login that never completed", rerr)
	}
}

// stallingDriver opens its first connection and then stalls every later open
// until released, ignoring the context, as go-mssqldb does after the dial. It
// is what the watch's identity connection meets when the server answers one
// login and hangs on the next.
type stallingDriver struct {
	first   *fakeDriver
	opened  atomic.Int32
	release chan struct{}
}

func (d *stallingDriver) Open(name string) (driver.Conn, error) {
	if d.opened.Add(1) > 1 {
		<-d.release
		return nil, errors.New("stallingDriver: released")
	}
	return d.first.Open(name)
}

// The watch opens a second connection to read who a waiter is, and a failure
// to open it costs the identity, not the watch and never the run. The poll
// connection answers (no waiter), the identity connection never opens: the
// watch must start without it, within the poll connection's own deadline.
func TestTheWatchGivesUpOnAnIdentityConnectionThatNeverOpens(t *testing.T) {
	t.Parallel()
	d := &stallingDriver{
		first:   &fakeDriver{answers: map[string]fakeAnswer{watchQuery: {}}},
		release: make(chan struct{}),
	}
	name := fmt.Sprintf("watch-stall-%d", fakeSeq.Add(1))
	sql.Register(name, d)
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	cfg := &Config{ConnectTimeout: silentConnectTimeout}
	var w *blockingWatch
	var stop func()
	var reason string
	within(t, 10*silentConnectTimeout, func() {
		w, stop, reason = startWatchOn(context.Background(), db, cfg, 55)
	})
	close(d.release)
	stop()
	if w == nil {
		t.Fatalf("no watch (%q); a lost identity connection must not cost the watch", reason)
	}
	if w.identify != nil {
		t.Error("the watch claims an identity connection that never opened")
	}
}
