package flow

import (
	"testing"
)

func TestShortRoundTrip(t *testing.T) {
	for _, id := range []ID{0, 1, 2, 9, 10, 31, 32, 33, 1000, 123456789, 1 << 40, 1<<63 + 5} {
		s := id.Short()
		got, ok := ParseShort(s)
		if !ok || got != id {
			t.Fatalf("id %d → %q → %d ok=%v", id, s, got, ok)
		}
	}
	if ID(1).Short() != "1" || ID(31).Short() != "z" || ID(32).Short() != "10" {
		t.Fatalf("unexpected encodings: %s %s %s", ID(1).Short(), ID(31).Short(), ID(32).Short())
	}
	if _, ok := ParseShort("u"); ok {
		t.Fatal("u is not in the alphabet")
	}
	if got, _ := ParseShort("I"); got != 1 {
		t.Fatal("confusable I should parse as 1")
	}
	if _, ok := ParseShort(""); ok {
		t.Fatal("empty must fail")
	}
}

func TestFlowURL(t *testing.T) {
	f := &Flow{Scheme: "https", Host: "api.example.com", Port: 443, Path: "/v1/x", Query: "a=1"}
	if f.URL() != "https://api.example.com/v1/x?a=1" {
		t.Fatal(f.URL())
	}
	f.Port = 8443
	if f.URL() != "https://api.example.com:8443/v1/x?a=1" {
		t.Fatal(f.URL())
	}
}
