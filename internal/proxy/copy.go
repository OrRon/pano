package proxy

import (
	"crypto/tls"
	"io"
	"net/http"
)

type tlsState = tls.ConnectionState

func negotiated(cs tls.ConnectionState) string {
	switch cs.NegotiatedProtocol {
	case "h2":
		return "HTTP/2.0"
	case "http/1.1":
		return "HTTP/1.1"
	}
	return ""
}

// copyStream streams src to w while teeing into cap. When streamy is set
// every read is flushed immediately so SSE/LLM token streams arrive live.
func copyStream(w io.Writer, rc *http.ResponseController, src io.Reader, cap *capture, streamy bool) error {
	bp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bp)
	buf := *bp
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			_, _ = cap.Write(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if streamy {
				_ = rc.Flush()
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return nil
			}
			return rerr
		}
	}
}
