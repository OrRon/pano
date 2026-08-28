package store

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/flate"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zlib"
	"github.com/klauspost/compress/zstd"
)

// DefaultDecodeLimit bounds DecodeBody output when limit is 0.
const DefaultDecodeLimit = 8 << 20

// ErrDecodeLimit is returned by DecodeBody when the decoded output exceeds
// the limit; the returned bytes are the truncated prefix.
var ErrDecodeLimit = errors.New("store: decoded body exceeds limit")

// ErrUnsupportedEncoding is returned for a Content-Encoding token DecodeBody
// does not know.
var ErrUnsupportedEncoding = errors.New("store: unsupported content encoding")

// DecodeBody removes a Content-Encoding from b. Supported tokens: gzip,
// x-gzip, deflate (zlib-wrapped or raw), br, zstd, identity. Comma-chained
// encodings ("gzip, br") are undone in reverse order of application. Output
// is bounded by limit bytes (0 = DefaultDecodeLimit); when exceeded the
// truncated prefix is returned together with ErrDecodeLimit. Identity and
// empty encodings return b unchanged.
func DecodeBody(encoding string, b []byte, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = DefaultDecodeLimit
	}
	var codings []string
	for _, tok := range strings.Split(encoding, ",") {
		tok = strings.ToLower(strings.TrimSpace(tok))
		if tok == "" || tok == "identity" {
			continue
		}
		codings = append(codings, tok)
	}
	if len(codings) == 0 {
		return b, nil
	}
	for i := len(codings) - 1; i >= 0; i-- {
		out, err := decodeOne(codings[i], b, limit)
		if err != nil {
			return out, err
		}
		b = out
	}
	return b, nil
}

func decodeOne(coding string, b []byte, limit int64) ([]byte, error) {
	var (
		r   io.Reader
		err error
	)
	switch coding {
	case "gzip", "x-gzip":
		r, err = gzip.NewReader(bytes.NewReader(b))
	case "deflate":
		// RFC 7230 says zlib-wrapped; some servers send raw DEFLATE.
		if zr, zerr := zlib.NewReader(bytes.NewReader(b)); zerr == nil {
			r = zr
		} else {
			r = flate.NewReader(bytes.NewReader(b))
		}
	case "br":
		r = brotli.NewReader(bytes.NewReader(b))
	case "zstd":
		var zr *zstd.Decoder
		zr, err = zstd.NewReader(bytes.NewReader(b), zstd.WithDecoderConcurrency(1))
		if zr != nil {
			defer zr.Close()
			r = zr
		}
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedEncoding, coding)
	}
	if err != nil {
		return nil, fmt.Errorf("store: decode %s: %w", coding, err)
	}
	return readBounded(r, limit, coding)
}

// readBounded reads up to limit bytes; one extra byte detects overflow.
func readBounded(r io.Reader, limit int64, coding string) ([]byte, error) {
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, limit+1))
	if err != nil && n < limit {
		return buf.Bytes(), fmt.Errorf("store: decode %s: %w", coding, err)
	}
	if n > limit {
		return buf.Bytes()[:limit], ErrDecodeLimit
	}
	return buf.Bytes(), nil
}
