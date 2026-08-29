package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func waitGone(t *testing.T, ping func() bool, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ping() == !want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("daemon gone=%v not reached", want)
}

// An owning UI whose connection drops turns pano off (ADR 0009).
func TestOwningUIDropTurnsOff(t *testing.T) {
	c, _ := startDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	gone, err := c.Attach(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	st, err := c.Status(context.Background())
	if err != nil || st.Lifecycle.Mode != "app" || st.Lifecycle.UIs != 1 {
		t.Fatalf("status after attach: %+v err=%v", st.Lifecycle, err)
	}
	cancel()
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("attach stream did not close")
	}
	waitGone(t, func() bool { return c.Ping(context.Background()) }, true)
}

// A plain attachment (pano ui on a running daemon) never stops anything;
// disowning an owner turns app mode into background mode.
func TestAttachWithoutOwnAndDisown(t *testing.T) {
	c, _ := startDaemon(t)
	bg := context.Background()
	ctx, cancel := context.WithCancel(bg)
	if _, err := c.Attach(ctx, false); err != nil {
		t.Fatal(err)
	}
	if st, _ := c.Status(bg); st.Lifecycle.Mode != "background" || st.Lifecycle.UIs != 1 {
		t.Fatalf("lifecycle = %+v", st.Lifecycle)
	}
	cancel()
	time.Sleep(200 * time.Millisecond)
	if !c.Ping(bg) {
		t.Fatal("non-owning ui must not stop the daemon")
	}

	ctx, cancel = context.WithCancel(bg)
	defer cancel()
	if _, err := c.Attach(ctx, true); err != nil {
		t.Fatal(err)
	}
	if st, _ := c.Status(bg); st.Lifecycle.Mode != "app" {
		t.Fatalf("lifecycle = %+v", st.Lifecycle)
	}
	l, err := c.Disown(bg)
	if err != nil || l.Mode != "background" || l.UIs != 1 {
		t.Fatalf("disown = %+v err=%v", l, err)
	}
	cancel()
	time.Sleep(200 * time.Millisecond)
	if !c.Ping(bg) {
		t.Fatal("disowned daemon must keep running when the ui leaves")
	}
}

// Off from a UI stops the daemon.
func TestOffStopsDaemon(t *testing.T) {
	c, _ := startDaemon(t)
	if err := c.Off(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitGone(t, func() bool { return c.Ping(context.Background()) }, true)
}

// Stopping must not wait for in-flight requests: browsers keep streaming
// and long-poll connections open for minutes.
func TestStopDoesNotWaitForInflightRequests(t *testing.T) {
	c, _ := startDaemon(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	defer origin.Close()
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	proxyURL, _ := url.Parse("http://" + st.ProxyAddr)
	hc := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	go func() { _, _ = hc.Get(origin.URL + "/slow") }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if st, _ := c.Status(context.Background()); st.ActiveConns > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("request never became active")
		}
		time.Sleep(20 * time.Millisecond)
	}
	start := time.Now()
	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitGone(t, func() bool { return c.Ping(context.Background()) }, true)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("shutdown took %v with a request in flight; want under 3s", took)
	}
}
