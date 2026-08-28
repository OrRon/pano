package view

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// ErrDecodeLimit is returned (wrapped) by Decode when the decoded output
// would exceed the requested limit. The output produced up to the limit is
// returned alongside the error.
var ErrDecodeLimit = errors.New("decoded body exceeds limit")

// Decode removes a Content-Encoding from b. Supported codings are gzip,
// x-gzip, deflate (zlib-wrapped or raw), br and zstd; comma-chained values
// such as "gzip, br" are applied in reverse order. Output is bounded by limit
// bytes (0 means DefaultDecodeLimit). The input is returned unchanged when
// encoding is "" or "identity".
//
// On a truncated or corrupt stream Decode returns whatever it managed to
// decode together with a non-nil error, so callers can still show partial
// content from bodies that were cut at capture time.
func Decode(encoding string, b []byte, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = DefaultDecodeLimit
	}
	codings := splitEncodings(encoding)
	if len(codings) == 0 {
		return b, nil
	}
	out := b
	for i := len(codings) - 1; i >= 0; i-- {
		var err error
		out, err = decodeOne(codings[i], out, limit)
		if err != nil {
			return out, fmt.Errorf("%s: %w", codings[i], err)
		}
	}
	return out, nil
}

// splitEncodings parses a Content-Encoding header value into its codings,
// dropping "identity" and blanks.
func splitEncodings(encoding string) []string {
	var out []string
	for _, p := range strings.Split(encoding, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || p == "identity" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func decodeOne(coding string, b []byte, limit int64) ([]byte, error) {
	switch coding {
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		return readLimited(zr, limit)
	case "deflate":
		// HTTP "deflate" is zlib-wrapped per RFC 9110, but raw DEFLATE
		// streams are common in the wild; try both.
		if zr, err := zlib.NewReader(bytes.NewReader(b)); err == nil {
			out, rerr := readLimited(zr, limit)
			if rerr == nil || len(out) > 0 {
				return out, rerr
			}
		}
		return readLimited(flate.NewReader(bytes.NewReader(b)), limit)
	case "br":
		return readLimited(brotli.NewReader(bytes.NewReader(b)), limit)
	case "zstd":
		// Bound the window allocation as well as the output; limit is
		// always positive here and far below the uint64 range.
		maxMem := uint64(DefaultDecodeLimit)
		if limit > 0 && limit < 1<<40 {
			maxMem = uint64(limit) + 1
		}
		zr, err := zstd.NewReader(bytes.NewReader(b),
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderMaxMemory(maxMem),
		)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		return readLimited(zr, limit)
	default:
		return nil, fmt.Errorf("unsupported content-encoding %q", coding)
	}
}

// readLimited reads r into memory, stopping at limit bytes. Partial output is
// returned with the error when the stream is corrupt or too large.
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(&io.LimitedReader{R: r, N: limit + 1})
	if int64(buf.Len()) > limit {
		buf.Truncate(int(limit))
		return buf.Bytes(), ErrDecodeLimit
	}
	return buf.Bytes(), err
}
