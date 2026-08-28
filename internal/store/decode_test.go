package store

import (
	"bytes"
	"errors"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/flate"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zlib"
	"github.com/klauspost/compress/zstd"
)

func encode(t *testing.T, coding string, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	switch coding {
	case "gzip":
		w := gzip.NewWriter(&buf)
		_, _ = w.Write(b)
		_ = w.Close()
	case "zlib":
		w := zlib.NewWriter(&buf)
		_, _ = w.Write(b)
		_ = w.Close()
	case "raw-deflate":
		w, _ := flate.NewWriter(&buf, flate.DefaultCompression)
		_, _ = w.Write(b)
		_ = w.Close()
	case "br":
		w := brotli.NewWriter(&buf)
		_, _ = w.Write(b)
		_ = w.Close()
	case "zstd":
		w, _ := zstd.NewWriter(&buf)
		_, _ = w.Write(b)
		_ = w.Close()
	default:
		t.Fatalf("unknown coding %s", coding)
	}
	return buf.Bytes()
}

func TestDecodeBody(t *testing.T) {
	plain := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 200)
	cases := []struct {
		name, header string
		data         []byte
	}{
		{"gzip", "gzip", encode(t, "gzip", plain)},
		{"x-gzip", "x-gzip", encode(t, "gzip", plain)},
		{"GZIP upper", "GZIP", encode(t, "gzip", plain)},
		{"deflate zlib", "deflate", encode(t, "zlib", plain)},
		{"deflate raw", "deflate", encode(t, "raw-deflate", plain)},
		{"br", "br", encode(t, "br", plain)},
		{"zstd", "zstd", encode(t, "zstd", plain)},
		{"identity", "identity", plain},
		{"empty", "", plain},
		{"chained gzip then br", "gzip, br", encode(t, "br", encode(t, "gzip", plain))},
		{"chained with identity", "identity, zstd , gzip", encode(t, "gzip", encode(t, "zstd", plain))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := DecodeBody(c.header, c.data, 0)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out, plain) {
				t.Fatalf("mismatch: %d bytes", len(out))
			}
		})
	}
}

func TestDecodeBodyErrors(t *testing.T) {
	plain := bytes.Repeat([]byte("abcdefgh"), 1000)
	out, err := DecodeBody("gzip", encode(t, "gzip", plain), 100)
	if !errors.Is(err, ErrDecodeLimit) {
		t.Fatalf("limit: err = %v", err)
	}
	if len(out) != 100 || !bytes.Equal(out, plain[:100]) {
		t.Fatalf("limit: got %d bytes", len(out))
	}
	// Exactly at the limit is not an error.
	if out, err := DecodeBody("gzip", encode(t, "gzip", plain), int64(len(plain))); err != nil || len(out) != len(plain) {
		t.Fatalf("exact limit: %d, %v", len(out), err)
	}
	if _, err := DecodeBody("sdch", plain, 0); !errors.Is(err, ErrUnsupportedEncoding) {
		t.Fatalf("unsupported: %v", err)
	}
	if _, err := DecodeBody("gzip", []byte("not gzip at all"), 0); err == nil {
		t.Fatal("corrupt gzip accepted")
	}
	if _, err := DecodeBody("zstd", []byte{0x28, 0xb5, 0x2f, 0xfd, 0xff, 0xff}, 0); err == nil {
		t.Fatal("corrupt zstd accepted")
	}
	if out, err := DecodeBody("", nil, 0); err != nil || out != nil {
		t.Fatalf("nil passthrough: %v %v", out, err)
	}
}
