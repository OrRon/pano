package proxy

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/orron/pano/internal/flow"
)

// handleWebSocket dials the origin, forwards the upgrade, and splices frames
// both ways, optionally parsing them for capture.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request, scheme, hostport string) {
	client := clientAddr(r.Context())
	f := &flow.Flow{
		ID: s.ids.Next(), Session: s.opts.Session(), Kind: flow.KindWebSocket, Client: client,
		Proto: r.Proto, Scheme: scheme, Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
		ReqHeaders: r.Header.Clone(), State: flow.StateActive, T: flow.Timing{Start: time.Now()},
	}
	f.Host, f.Port = splitHostPort(hostport, defaultPort(scheme))
	s.emitStarted(f)

	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.URL.Scheme = scheme
	// Dialing needs an explicit port; the Host header usually has none.
	if _, _, err := net.SplitHostPort(hostport); err != nil {
		hostport = net.JoinHostPort(hostport, strconv.Itoa(defaultPort(scheme)))
	}
	out.URL.Host = hostport
	if out.Host == "" {
		out.Host = hostport
	}
	stripHop(out.Header, true)
	if s.hooks != nil {
		if d := s.hooks.Request(r.Context(), f, out); d.Mock != nil || d.Block != "" {
			f.Error = "websocket blocked by rule"
			http.Error(w, "pano: blocked", http.StatusForbidden)
			s.finish(f, flow.StateFailed)
			return
		}
	}

	var up net.Conn
	var err error
	if scheme == "https" {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if s.opts.UpstreamTLS != nil {
			cfg = s.opts.UpstreamTLS.Clone()
		}
		cfg.ServerName = f.Host
		cfg.NextProtos = []string{"http/1.1"}
		up, err = tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", out.URL.Host, cfg)
	} else {
		up, err = net.DialTimeout("tcp", out.URL.Host, 10*time.Second)
	}
	if err != nil {
		f.Error = upstreamError(err)
		http.Error(w, "pano: "+f.Error, http.StatusBadGateway)
		s.finish(f, flow.StateFailed)
		return
	}
	defer up.Close()
	f.T.Connected = time.Now()

	if err := out.Write(up); err != nil {
		f.Error = "upstream write: " + err.Error()
		http.Error(w, "pano: "+f.Error, http.StatusBadGateway)
		s.finish(f, flow.StateFailed)
		return
	}
	f.T.WroteReq = time.Now()
	upr := bufio.NewReader(up)
	resp, err := http.ReadResponse(upr, out)
	if err != nil {
		f.Error = "upstream response: " + err.Error()
		http.Error(w, "pano: "+f.Error, http.StatusBadGateway)
		s.finish(f, flow.StateFailed)
		return
	}
	f.T.FirstByte = time.Now()
	f.Status = resp.StatusCode
	f.RespHeaders = resp.Header.Clone()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		// Not upgraded: proxy the response normally.
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			w.Header()[k] = vs
		}
		stripHop(w.Header(), false)
		w.WriteHeader(resp.StatusCode)
		rc := http.NewResponseController(w)
		cap := newCapture(s.opts.MaxBody, s.budget)
		_ = copyStream(w, rc, resp.Body, cap, false)
		s.storeBody(&f.RespBody, cap)
		s.finish(f, flow.StateDone)
		return
	}

	rc := http.NewResponseController(w)
	clientConn, brw, err := rc.Hijack()
	if err != nil {
		f.Error = "hijack: " + err.Error()
		s.finish(f, flow.StateFailed)
		return
	}
	defer clientConn.Close()
	if err := resp.Write(brw); err == nil {
		_ = brw.Flush()
	}
	f.T.HeadersSent = time.Now()
	s.emitUpdated(f)

	var clientReader io.Reader = clientConn
	if brw.Reader.Buffered() > 0 {
		clientReader = io.MultiReader(io.LimitReader(brw.Reader, int64(brw.Reader.Buffered())), clientConn)
	}
	var upReader io.Reader = up
	if upr.Buffered() > 0 {
		upReader = io.MultiReader(io.LimitReader(upr, int64(upr.Buffered())), up)
	}

	done := make(chan struct{}, 2)
	var c2s, s2c int64
	go func() {
		c2s = s.spliceWS(up, clientReader, f, "c2s")
		closeWrite(up)
		done <- struct{}{}
	}()
	go func() {
		s2c = s.spliceWS(clientConn, upReader, f, "s2c")
		closeWrite(clientConn)
		done <- struct{}{}
	}()
	<-done
	<-done
	f.ReqBody.Size, f.RespBody.Size = c2s, s2c
	s.finish(f, flow.StateDone)
}

// spliceWS copies src→dst verbatim; when capture is on, frames are parsed
// from the copied bytes and emitted as WSMessages.
func (s *Server) spliceWS(dst io.Writer, src io.Reader, f *flow.Flow, dir string) int64 {
	if !s.opts.CaptureWS || !s.capturing.Load() || s.sink == nil {
		n, _ := copyBuf(dst, src)
		return n
	}
	pr := &frameParser{s: s, f: f, dir: dir, limit: s.opts.MaxBody}
	n, _ := copyBuf(dst, io.TeeReader(src, pr))
	return n
}

// frameParser is an incremental RFC 6455 frame parser fed via Write.
type frameParser struct {
	s     *Server
	f     *flow.Flow
	dir   string
	limit int64
	buf   []byte
	seq   int
	// message reassembly
	msgOp   int
	msg     []byte
	msgLen  int64
	msgTrun bool
}

func (p *frameParser) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		consumed, ok := p.parseOne()
		if !ok {
			break
		}
		p.buf = p.buf[consumed:]
	}
	return len(b), nil
}

// parseOne parses a single frame from buf if complete.
func (p *frameParser) parseOne() (int, bool) {
	b := p.buf
	if len(b) < 2 {
		return 0, false
	}
	fin := b[0]&0x80 != 0
	op := int(b[0] & 0x0f)
	masked := b[1]&0x80 != 0
	plen := int64(b[1] & 0x7f)
	off := 2
	switch plen {
	case 126:
		if len(b) < 4 {
			return 0, false
		}
		plen = int64(binary.BigEndian.Uint16(b[2:4]))
		off = 4
	case 127:
		if len(b) < 10 {
			return 0, false
		}
		plen = int64(binary.BigEndian.Uint64(b[2:10])) //nolint:gosec // bounded below
		off = 10
	}
	if plen < 0 || plen > 1<<31 {
		// Corrupt or hostile; stop parsing this direction.
		p.buf = nil
		return 0, false
	}
	var mask [4]byte
	if masked {
		if len(b) < off+4 {
			return 0, false
		}
		copy(mask[:], b[off:off+4])
		off += 4
	}
	if int64(len(b)) < int64(off)+plen {
		// Frame incomplete. If it's huge, avoid buffering it all: consume the
		// header and payload lazily by dropping capture for this message.
		if plen > p.limit {
			p.msgTrun = true
		}
		return 0, false
	}
	payload := b[off : off+int(plen)]
	if masked {
		unmasked := make([]byte, len(payload))
		for i := range payload {
			unmasked[i] = payload[i] ^ mask[i%4]
		}
		payload = unmasked
	}
	total := off + int(plen)

	switch op {
	case 0x8, 0x9, 0xA: // control frames: close/ping/pong
		p.emit(op, payload, int64(len(payload)), false, masked)
	case 0x0: // continuation
		p.append(payload)
		if fin {
			p.emit(p.msgOp, p.msg, p.msgLen, p.msgTrun, masked)
			p.msg, p.msgLen, p.msgTrun = nil, 0, false
		}
	default: // text/binary
		p.msgOp = op
		p.msg, p.msgLen, p.msgTrun = nil, 0, false
		p.append(payload)
		if fin {
			p.emit(op, p.msg, p.msgLen, p.msgTrun, masked)
			p.msg, p.msgLen, p.msgTrun = nil, 0, false
		}
	}
	return total, true
}

func (p *frameParser) append(payload []byte) {
	p.msgLen += int64(len(payload))
	room := p.limit - int64(len(p.msg))
	if room <= 0 {
		p.msgTrun = true
		return
	}
	if int64(len(payload)) > room {
		payload = payload[:room]
		p.msgTrun = true
	}
	p.msg = append(p.msg, payload...)
}

func (p *frameParser) emit(op int, payload []byte, size int64, truncated, masked bool) {
	p.seq++
	m := &flow.WSMessage{
		FlowID: p.f.ID, Seq: p.seq, TS: time.Now(), Dir: p.dir, Opcode: op, Len: size, Masked: masked,
	}
	if len(payload) > 0 {
		m.Payload = append([]byte(nil), payload...)
	}
	_ = truncated
	p.s.sink.WS(m)
}
