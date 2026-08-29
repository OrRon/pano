package mcpserver

import (
	"strings"
	"testing"
	"time"

	"github.com/orron/pano/internal/api"
)

func TestFormatMobile(t *testing.T) {
	off := FormatMobile(api.Mobile{})
	if !strings.Contains(off, "mobile: off") || !strings.Contains(off, "pano mobile") {
		t.Fatalf("off: %q", off)
	}
	on := FormatMobile(api.Mobile{
		Enabled: true, Addr: "192.168.1.23:9091", Interface: "en0", Network: "Home", URL: "http://192.168.1.23:9091", MagicURL: "http://pano.internal",
		Devices: []api.Device{
			{IP: "192.168.1.40", Name: "iPhone · iOS 17.5", Proxy: true, TLS: true, Requests: 12, LastSeen: time.Now()},
			{IP: "192.168.1.41", Proxy: true, Rejected: 3, Requests: 2, LastSeen: time.Now()},
		},
	})
	for _, want := range []string{"mobile: on at 192.168.1.23:9091 (en0 · Home)", "192.168.1.40 iPhone · iOS 17.5 — proxy ✓, https ✓, 12 requests", "192.168.1.41 192.168.1.41 — proxy ✓, https ✕ (certificate refused ×3", "pano_flows client=<ip>"} {
		if !strings.Contains(on, want) {
			t.Errorf("missing %q in\n%s", want, on)
		}
	}
	if !strings.Contains(FormatStatus(api.Status{Mobile: api.Mobile{}}), "mobile: off") {
		t.Fatal("status should include the mobile block")
	}
}
