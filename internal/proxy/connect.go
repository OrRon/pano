package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/http2"

	"github.com/orron/pano/internal/flow"
)

// handleConnect terminates a CONNECT tunnel: either splices it opaquely
// (never list, mode only/off) or wraps it in TLS with a minted certificate and serves HTTP on the
// decrypted stream.
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if _, _, err := net.SplitHostPort(target); err != nil {
		target = net.JoinHostPort(target, "443")
	}
	magic := isMagic(target)
	if !magic && s.isSelf(target) {
		http.Error(w, "pano: refusing to proxy to itself", http.StatusForbidden)
		return
	}
	select {
	case s.sem <- struct{}{}:
	default:
		http.Error(w, "pano: too many connections", http.StatusServiceUnavailable)
		return
	}
	defer func() { <-s.sem }()
	s.active.Add(1)
	defer s.active.Add(-1)

	host, _, _ := net.SplitHostPort(target)
	client := clientAddr(r.Context())

	rc := http.NewResponseController(w)
	conn, brw, err := rc.Hijack()
	if err != nil {
		http.Error(w, "pano: hijack unsupported: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	raw := newMITMConn(conn, brw.Reader, target)

	if decrypt, reason := s.policy.Load().Decide(host); !decrypt && !magic {
		s.splice(raw, target, client, reason)
		return
	}
	if s.opts.TLS == nil {
		if magic {
			return // nothing to terminate the tunnel with
		}
		s.splice(raw, target, client, ReasonOff)
		return
	}

	// pano.internal is always terminated, whatever the decrypt mode: it is
	// pano's own site, and reaching it over https is how a phone proves it
	// trusts the certificate.
	tlsConn := tls.Server(raw, s.opts.TLS)
	hctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = tlsConn.HandshakeContext(hctx)
	cancel()
	s.noteHandshake(client, err == nil)
	if err != nil {
		if magic {
			return // the setup page reports this itself; no flow for our own host
		}
		s.recordHandshakeFailure(target, client, err)
		return
	}
	defer tlsConn.Close()

	switch tlsConn.ConnectionState().NegotiatedProtocol {
	case "h2":
		if !s.opts.DisableH2 {
			ctx := withClient(withTunnel(context.Background(), target), client)
			s.h2srv.ServeConn(tlsConn, &http2.ServeConnOpts{
				Context:    ctx,
				BaseConfig: s.h2,
				Handler:    s.h2.Handler,
			})
			return
		}
		fallthrough
	default:
		ln := newOneConnListener(tlsConn)
		_ = s.mitm.Serve(ln)
	}
}

// splice copies bytes both ways without decryption and records a tunnel flow.
func (s *Server) splice(client net.Conn, target, clientAddr, reason string) {
	f := &flow.Flow{
		ID: s.ids.Next(), Session: s.opts.Session(), Kind: flow.KindTunnel, Client: clientAddr,
		Scheme: "https", State: flow.StateActive, T: flow.Timing{Start: time.Now()},
		Tags: []string{reason},
	}
	f.Host, f.Port = splitHostPort(target, 443)
	s.emitStarted(f)

	up, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		f.Error = "dial: " + err.Error()
		s.finish(f, flow.StateFailed)
		return
	}
	defer up.Close()
	f.T.Connected = time.Now()

	var upBytes, downBytes int64
	done := make(chan struct{}, 2)
	go func() {
		n, _ := copyBuf(up, client)
		upBytes = n
		closeWrite(up)
		done <- struct{}{}
	}()
	go func() {
		n, _ := copyBuf(client, up)
		downBytes = n
		closeWrite(client)
		done <- struct{}{}
	}()
	<-done
	<-done
	f.ReqBody.Size, f.RespBody.Size = upBytes, downBytes
	s.finish(f, flow.StateDone)
}

func (s *Server) recordHandshakeFailure(target, client string, err error) {
	f := &flow.Flow{
		ID: s.ids.Next(), Session: s.opts.Session(), Kind: flow.KindTunnel, Client: client,
		Scheme: "https", State: flow.StateFailed, T: flow.Timing{Start: time.Now()},
	}
	f.Host, f.Port = splitHostPort(target, 443)
	msg := err.Error()
	rejected := true
	switch {
	case strings.Contains(msg, "unknown certificate authority") || strings.Contains(msg, "unknown_ca"),
		strings.Contains(msg, "bad certificate"), strings.Contains(msg, "certificate unknown"):
		f.Error = "client rejected pano certificate (CA not trusted, or the app pins certificates — run `pano decrypt never add " + f.Host + "`)"
	case errors.Is(err, io.EOF):
		f.Error = "client closed connection during TLS handshake (likely certificate rejected / pinning — run `pano decrypt never add " + f.Host + "`)"
	default:
		rejected = false
		f.Error = "tls handshake: " + msg
	}
	if rejected {
		s.rejected.add(f.Host, f.Error)
	}
	s.emitStarted(f)
	s.finish(f, flow.StateFailed)
}

func copyBuf(dst io.Writer, src io.Reader) (int64, error) {
	bp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bp)
	return io.CopyBuffer(dst, src, *bp)
}

func closeWrite(c net.Conn) {
	type cw interface{ CloseWrite() error }
	if x, ok := c.(cw); ok {
		_ = x.CloseWrite()
		return
	}
	if m, ok := c.(*mitmConn); ok {
		if x, ok := m.Conn.(cw); ok {
			_ = x.CloseWrite()
			return
		}
	}
	_ = c.Close()
}

func splitHostPort(hp string, def int) (string, int) {
	h, p, err := net.SplitHostPort(hp)
	if err != nil {
		return hp, def
	}
	port := def
	if n, err := parsePort(p); err == nil {
		port = n
	}
	return h, port
}

func parsePort(p string) (int, error) {
	n := 0
	for _, c := range p {
		if c < '0' || c > '9' {
			return 0, errors.New("bad port")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
