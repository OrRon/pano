package proxy

import (
	"errors"
	"testing"
	"time"
)

func TestDecideMatrix(t *testing.T) {
	only := []string{"api.anthropic.com", "*.example.com"}
	never := []string{"*.apple.com", "whatsapp.net"}
	tests := []struct {
		mode   DecryptMode
		host   string
		want   bool
		reason string
	}{
		{DecryptAll, "example.org", true, ""},
		{DecryptAll, "gateway.apple.com", false, ReasonNever},
		{DecryptAll, "mmg.whatsapp.net", false, ReasonNever},
		{DecryptAll, "whatsapp.net", false, ReasonNever},
		{DecryptAll, "notwhatsapp.net", true, ""},
		{DecryptOnly, "api.anthropic.com", true, ""},
		{DecryptOnly, "API.Anthropic.COM", true, ""},
		{DecryptOnly, "sub.api.anthropic.com", true, ""},
		{DecryptOnly, "foo.example.com", true, ""},
		{DecryptOnly, "example.com", false, ReasonUnlisted},
		{DecryptOnly, "example.org", false, ReasonUnlisted},
		{DecryptOnly, "gateway.apple.com", false, ReasonNever},
		{DecryptOff, "api.anthropic.com", false, ReasonOff},
		{DecryptOff, "gateway.apple.com", false, ReasonNever},
		{"", "example.org", true, ""}, // zero mode behaves as all
	}
	for _, tc := range tests {
		p := DecryptPolicy{Mode: tc.mode, Only: only, Never: never}
		got, reason := p.Decide(tc.host)
		if got != tc.want || reason != tc.reason {
			t.Errorf("mode=%q host=%q: got (%v,%q) want (%v,%q)", tc.mode, tc.host, got, reason, tc.want, tc.reason)
		}
	}
}

func TestNeverWinsInOnlyMode(t *testing.T) {
	p := DecryptPolicy{Mode: DecryptOnly, Only: []string{"pinned.example"}, Never: []string{"pinned.example"}}
	if ok, reason := p.Decide("pinned.example"); ok || reason != ReasonNever {
		t.Fatalf("never must win: %v %q", ok, reason)
	}
}

func TestHostMatch(t *testing.T) {
	tests := []struct {
		pattern, host string
		want          bool
	}{
		{"example.com", "example.com", true},
		{"example.com", "a.b.example.com", true},
		{"example.com", "notexample.com", false},
		{"example.com", "example.com.evil", false},
		{"*.example.com", "a.example.com", true},
		{"*.example.com", "example.com", false},
		{"api?.example.com", "api1.example.com", true},
		{"127.0.0.1", "127.0.0.1", true},
		{"EXAMPLE.com", "example.COM", true},
	}
	for _, tc := range tests {
		if got := HostMatch(tc.pattern, tc.host); got != tc.want {
			t.Errorf("HostMatch(%q,%q)=%v want %v", tc.pattern, tc.host, got, tc.want)
		}
	}
}

func TestNormalizeHost(t *testing.T) {
	good := []struct{ in, want string }{
		{"  Example.COM ", "example.com"},
		{"example.com.", "example.com"},
		{"example.com:443", "example.com"},
		{"[::1]:8443", "::1"},
		{"*.Apple.com", "*.apple.com"},
		{"mmg.whatsapp.net", "mmg.whatsapp.net"},
	}
	for _, tc := range good {
		got, err := NormalizeHost(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("NormalizeHost(%q)=%q,%v want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "   ", "https://example.com", "example.com/path", "two words"} {
		if _, err := NormalizeHost(bad); !errors.Is(err, ErrBadPattern) {
			t.Errorf("NormalizeHost(%q) should fail with ErrBadPattern, got %v", bad, err)
		}
	}
}

func TestParseDecryptMode(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want DecryptMode
	}{{"all", DecryptAll}, {" Only ", DecryptOnly}, {"OFF", DecryptOff}} {
		if got, err := ParseDecryptMode(tc.in); err != nil || got != tc.want {
			t.Errorf("ParseDecryptMode(%q)=%q,%v", tc.in, got, err)
		}
	}
	if _, err := ParseDecryptMode("maybe"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRejectedRing(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	r := newRejectedRing()
	r.now = func() time.Time { return now }
	r.limit = 2

	r.add("a.example", "rejected")
	r.add("a.example", "rejected")
	now = now.Add(30 * time.Second)
	r.add("b.example", "closed")
	got := r.list()
	if len(got) != 2 || got[0].Host != "a.example" || got[0].Count != 2 || got[1].Host != "b.example" {
		t.Fatalf("list: %+v", got)
	}

	// Over the limit: the entry with the oldest Last (a) is evicted; ties in
	// count sort newest first.
	now = now.Add(30 * time.Second)
	r.add("c.example", "rejected")
	got = r.list()
	if len(got) != 2 || got[0].Host != "c.example" || got[1].Host != "b.example" {
		t.Fatalf("evict: %+v", got)
	}

	// forget drops hosts now covered by never.
	r.forget([]string{"example"})
	if got := r.list(); len(got) != 0 {
		t.Fatalf("forget: %+v", got)
	}

	// Entries age out of the window.
	r.add("d.example", "rejected")
	now = now.Add(rejectedWindow + time.Second)
	if got := r.list(); len(got) != 0 {
		t.Fatalf("prune: %+v", got)
	}
	r.add("", "ignored")
	if got := r.list(); len(got) != 0 {
		t.Fatalf("empty host recorded: %+v", got)
	}
}

func TestSetDecryptForgetsCoveredRejections(t *testing.T) {
	s := New(Options{Addr: "127.0.0.1:0"})
	s.rejected.add("mmg.whatsapp.net", "rejected")
	s.rejected.add("other.example", "rejected")
	s.SetDecrypt(DecryptPolicy{Mode: DecryptAll, Never: []string{"whatsapp.net"}})
	got := s.Rejected()
	if len(got) != 1 || got[0].Host != "other.example" {
		t.Fatalf("rejected after SetDecrypt: %+v", got)
	}
	if p := s.Decrypt(); p.Mode != DecryptAll || len(p.Never) != 1 {
		t.Fatalf("policy: %+v", p)
	}
}
