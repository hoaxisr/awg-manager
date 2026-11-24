package realityserver

import (
	"net"
	"sync"
)

// limitListener caps concurrently open connections. Unlike
// x/net/netutil.LimitListener it hands out the *net.TCPConn itself (embedded):
// REALITY asserts CloseWrite on the raw conn and fails on an opaque wrapper.
// Accept blocks while the cap is reached.
type limitListener struct {
	net.Listener
	sem       chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newLimitListener(l net.Listener, n int) net.Listener {
	return &limitListener{Listener: l, sem: make(chan struct{}, n), done: make(chan struct{})}
}

// Close будит Accept, ждущий свободного слота: иначе на полном лимите
// горутина accept'а REALITY висела бы после остановки сервера.
func (l *limitListener) Close() error {
	l.closeOnce.Do(func() { close(l.done) })
	return l.Listener.Close()
}

func (l *limitListener) Accept() (net.Conn, error) {
	select {
	case l.sem <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	tc, ok := c.(*net.TCPConn)
	if !ok {
		<-l.sem
		return c, nil
	}
	return &limitedTCPConn{TCPConn: tc, release: sync.OnceFunc(func() { <-l.sem })}, nil
}

type limitedTCPConn struct {
	*net.TCPConn
	release func()
}

func (c *limitedTCPConn) Close() error {
	c.release()
	return c.TCPConn.Close()
}
