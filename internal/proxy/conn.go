package proxy

import (
	"bufio"
	"io"
	"net"
	"sync"
)

// mitmConn wraps the hijacked client connection and remembers the CONNECT
// target so certificate selection works without SNI.
type mitmConn struct {
	net.Conn
	target string
	r      io.Reader // pre-buffered bytes from the hijacked bufio.Reader
}

func newMITMConn(c net.Conn, br *bufio.Reader, target string) *mitmConn {
	m := &mitmConn{Conn: c, target: target, r: c}
	if br != nil && br.Buffered() > 0 {
		m.r = io.MultiReader(io.LimitReader(br, int64(br.Buffered())), c)
	}
	return m
}

func (m *mitmConn) Read(p []byte) (int, error) { return m.r.Read(p) }

// ConnectTarget implements ca.TargetConn.
func (m *mitmConn) ConnectTarget() string { return m.target }

// oneConnListener hands a single connection to http.Server.Serve and then
// reports closed.
type oneConnListener struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
}

func newOneConnListener(c net.Conn) *oneConnListener {
	return &oneConnListener{conn: c, done: make(chan struct{})}
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() { c = l.conn })
	if c != nil {
		return c, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return nil
}

func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }
