package collect

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// errAttemptAbandoned is what a dial returns once connWithin has given up on
// the attempt it belongs to. database/sql retries a bad connection, and a
// retry must not open a socket nobody is left to close.
var errAttemptAbandoned = errors.New("the connection attempt was abandoned")

type attemptKey struct{}

// attempt records the sockets dialed on behalf of one connWithin call, so that
// connWithin can close them when it gives up. go-mssqldb reads the pre-login
// answer with no deadline and without watching the context; closing the socket
// under it is the one thing that makes that read return.
type attempt struct {
	mu        sync.Mutex
	conns     []net.Conn
	abandoned bool
}

// add records c, or reports false when the attempt was already abandoned.
func (a *attempt) add(c net.Conn) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.abandoned {
		return false
	}
	a.conns = append(a.conns, c)
	return true
}

// abandon closes every socket the attempt dialed and refuses the ones it would
// dial later. A socket that had already completed its login is closed too,
// which is what connWithin wants: it returns no connection once it has given
// up, so nothing would ever use that one.
func (a *attempt) abandon() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.abandoned = true
	for _, c := range a.conns {
		c.Close()
	}
	a.conns = nil
}

// attemptDialer is the driver's own dialer, a net.Dialer with its default
// keep-alive of 30 s, plus the bookkeeping above. A dial whose context carries
// no attempt, a pool refill database/sql makes on its own, is a plain dial.
// The driver also dials the SQL Browser over UDP through it; that socket is
// recorded like the other, and closing it twice is harmless.
type attemptDialer struct{}

func (attemptDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d := net.Dialer{KeepAlive: 30 * time.Second}
	c, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if a, ok := ctx.Value(attemptKey{}).(*attempt); ok && !a.add(c) {
		c.Close()
		return nil, errAttemptAbandoned
	}
	return c, nil
}
