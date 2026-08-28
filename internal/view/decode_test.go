package view

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

func encode(t *testing.T, coding string, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	switch coding {
	case "gzip", "x-gzip":
		w := gzip.NewWriter(&buf)
		mustWriteClose(t, w, b)
	case "deflate":
		w := zlib.NewWriter(&buf)
		mustWriteClose(t, w, b)
	case "raw-deflate":
		w, err := flate.NewWriter(&buf, flate.DefaultCompression)
		if err != nil {
			t.Fatal(err)
		}
		mustWriteClose(t, w, b)
	case "br":
		w := brotli.NewWriter(&buf)
		mustWriteClose(t, w, b)
	case "zstd":
		w, err := zstd.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}
		mustWriteClose(t, w, b)
	default:
		t.Fatalf("unknown coding %q", coding)
	}
	return buf.Bytes()
}

type writeCloser interface {
	Write([]byte) (int, error)
	Close() error
}

func mustWriteClose(t *testing.T, w writeCloser, b []byte) {
	t.Helper()
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeRoundTrips(t *testing.T) {
	payload := []byte(strings.Repeat(anthropicBody, 20))
	for _, coding := range []string{"gzip", "x-gzip", "deflate", "br", "zstd"} {
		t.Run(coding, func(t *testing.T) {
			enc := encode(t, coding, payload)
			out, err := Decode(coding, enc, 0)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out, payload) {
				t.Errorf("round trip mismatch (%d vs %d bytes)", len(out), len(payload))
			}
			// Case and whitespace in the header value are tolerated.
			out, err = Decode(" "+strings.ToUpper(coding)+" ", enc, 0)
			if err != nil || !bytes.Equal(out, payload) {
				t.Errorf("upper-case coding: err=%v", err)
			}
		})
	}
	t.Run("raw deflate", func(t *testing.T) {
		out, err := Decode("deflate", encode(t, "raw-deflate", payload), 0)
		if err != nil || !bytes.Equal(out, payload) {
			t.Errorf("raw deflate: err=%v", err)
		}
	})
	t.Run("chained", func(t *testing.T) {
		enc := encode(t, "gzip", encode(t, "br", payload))
		out, err := Decode("br, gzip", enc, 0)
		if err != nil || !bytes.Equal(out, payload) {
			t.Errorf("chained: err=%v", err)
		}
	})
	t.Run("identity", func(t *testing.T) {
		for _, enc := range []string{"", "identity", " identity , "} {
			out, err := Decode(enc, payload, 0)
			if err != nil || !bytes.Equal(out, payload) {
				t.Errorf("%q: err=%v", enc, err)
			}
		}
	})
}

func TestDecodeErrors(t *testing.T) {
	payload := []byte(strings.Repeat("0123456789", 1000))

	if _, err := Decode("compress", payload, 0); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("want unsupported error, got %v", err)
	}
	for _, coding := range []string{"gzip", "br", "zstd", "deflate"} {
		if _, err := Decode(coding, []byte("definitely not compressed"), 0); err == nil {
			t.Errorf("%s: want error for garbage input", coding)
		}
	}

	// Output limit.
	enc := encode(t, "gzip", payload)
	out, err := Decode("gzip", enc, 100)
	if !errors.Is(err, ErrDecodeLimit) {
		t.Fatalf("want ErrDecodeLimit, got %v", err)
	}
	if len(out) != 100 || !bytes.Equal(out, payload[:100]) {
		t.Errorf("partial output = %d bytes", len(out))
	}

	// Truncated stream yields partial output and an error.
	out, err = Decode("gzip", enc[:len(enc)/2], 0)
	if err == nil {
		t.Fatal("want error for truncated stream")
	}
	if len(out) == 0 || !bytes.HasPrefix(payload, out) {
		t.Errorf("partial output should be a prefix of payload (%d bytes)", len(out))
	}

	// Chained: the error names the failing coding.
	_, err = Decode("br, gzip", []byte("garbage"), 0)
	if err == nil || !strings.HasPrefix(err.Error(), "gzip:") {
		t.Errorf("want gzip error first, got %v", err)
	}
}
