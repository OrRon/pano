package proxy

import (
	"fmt"
	"testing"
)

func TestDeviceName(t *testing.T) {
	cases := map[string]string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1": "iPhone · iOS 17.5",
		"Mozilla/5.0 (iPad; CPU OS 16_2 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148 Safari/604.1":                                           "iPad · iOS 16.2",
		"Mozilla/5.0 (Linux; Android 14; Pixel 8 Build/AP1A.240405.002) AppleWebKit/537.36 Chrome/124.0 Mobile Safari/537.36":                     "Pixel 8 · Android 14",
		"Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 Chrome/124.0 Mobile Safari/537.36":                                                 "Android 10",
		"MyApp/1.0 CFNetwork/1494.0.7 Darwin/23.4.0":                                                                                              "iOS device",
		"okhttp/4.12.0": "Android",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 Safari/605.1.15": "Mac",
		"curl/8.4.0": "",
		"":           "",
	}
	for ua, want := range cases {
		if got := DeviceName(ua); got != want {
			t.Errorf("DeviceName(%q) = %q, want %q", ua, got, want)
		}
	}
}

func TestDeviceTable(t *testing.T) {
	s := New(Options{Addr: "127.0.0.1:0"})
	s.noteRequest("127.0.0.1:5555", "curl", true) // loopback: never tracked
	if got := s.Devices(); len(got) != 0 {
		t.Fatalf("loopback tracked: %+v", got)
	}
	s.noteRequest("192.168.1.40:5000", "", true)
	s.noteRequest("192.168.1.40:5001", "MyApp/1.0 CFNetwork/1494.0.7 Darwin/23.4.0", false)
	s.noteRequest("192.168.1.40:5002", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) Mobile/15E148 Safari/604.1", false)
	s.noteHandshake("192.168.1.40:5003", false)
	s.noteHandshake("192.168.1.40:5004", true)
	d, ok := s.Device("192.168.1.40:1")
	if !ok {
		t.Fatal("device missing")
	}
	if d.Requests != 1 || d.Rejected != 1 || d.Decrypted != 1 || !d.ProxyOK() || !d.TLSOK() {
		t.Fatalf("device: %+v", d)
	}
	if d.Name != "iPhone · iOS 17.5" {
		t.Fatalf("generic name should be upgraded, got %q", d.Name)
	}
	// Bounded: the oldest entry goes when the table is full.
	for i := 0; i < maxDevices+5; i++ {
		s.noteRequest(fmt.Sprintf("10.0.%d.%d:1", i/256, i%256), "", true)
	}
	if n := len(s.Devices()); n != maxDevices {
		t.Fatalf("devices = %d, want %d", n, maxDevices)
	}
	if _, ok := s.Device("192.168.1.40:1"); ok {
		t.Fatal("oldest device should have been evicted")
	}
}

func TestIsMagic(t *testing.T) {
	for _, h := range []string{"pano.internal", "PANO.internal:443", "pano.internal:80"} {
		if !isMagic(h) {
			t.Errorf("%q should be magic", h)
		}
	}
	for _, h := range []string{"pano.internal.example.com", "example.com:443", ""} {
		if isMagic(h) {
			t.Errorf("%q should not be magic", h)
		}
	}
}
