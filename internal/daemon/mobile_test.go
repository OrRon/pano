package daemon

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/orron/pano/internal/api"
)

func TestMobileListener(t *testing.T) {
	c, _ := startDaemon(t)
	ctx := context.Background()
	st, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mobile.Enabled || len(st.Mobile.Devices) != 0 {
		t.Fatalf("mobile should start off: %+v", st.Mobile)
	}
	if _, err := c.SetMobile(ctx, api.MobileRequest{Enabled: true, IP: "::1"}); err == nil {
		t.Fatal("IPv6 override should be rejected")
	}
	// Loopback stands in for the LAN address in tests, so it needs its own
	// port (a real LAN listener shares the proxy port on a different IP).
	port := freePort(t)
	m, err := c.SetMobile(ctx, api.MobileRequest{Enabled: true, IP: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Enabled || m.IP != "127.0.0.1" || m.Port != port || m.URL != "http://"+m.Addr || m.MagicURL != "http://pano.internal" {
		t.Fatalf("mobile: %+v", m)
	}
	resp, err := http.Get(m.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "Put this device behind pano") {
		t.Fatalf("setup page: %d %.100s", resp.StatusCode, b)
	}
	if _, err := http.Get(m.URL + "/_pano/pano-ca.mobileconfig"); err != nil {
		t.Fatal(err)
	}
	// Idempotent.
	if m2, err := c.SetMobile(ctx, api.MobileRequest{Enabled: true, IP: "127.0.0.1", Port: port}); err != nil || m2.Addr != m.Addr {
		t.Fatalf("re-enable: %v %+v", err, m2)
	}
	st, _ = c.Status(ctx)
	if !st.Mobile.Enabled {
		t.Fatal("status should show mobile on")
	}
	addr := m.Addr
	if m, err = c.SetMobile(ctx, api.MobileRequest{Enabled: false}); err != nil || m.Enabled || m.LastAddr != addr {
		t.Fatalf("off: %v %+v (want last_addr %s)", err, m, addr)
	}
	if st, _ := c.Status(ctx); st.Mobile.LastAddr != addr {
		t.Fatalf("status after off: %+v", st.Mobile)
	}
	if conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port))); err == nil {
		conn.Close()
		t.Fatal("listener should be closed")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
