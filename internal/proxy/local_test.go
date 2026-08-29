package proxy_test

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/orron/pano/internal/proxy"
)

// localSite is a stand-in for the mobile setup site.
func localSite() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "local %s %s abs=%v", r.Method, r.URL.Path, r.URL.IsAbs())
	})
}

func body(t *testing.T, get func() (*http.Response, error)) string {
	t.Helper()
	resp, err := get()
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return fmt.Sprintf("%d %s", resp.StatusCode, b)
}

// A client that already proxies through pano must reach pano's own site at
// its own address (the setup page polling its status) and at pano.internal
// over http and https — never a forwarded request or a 403.
func TestLocalSiteReachableThroughProxy(t *testing.T) {
	e := start(t, func(o *proxy.Options) { o.Local = localSite() })

	if got := body(t, func() (*http.Response, error) { return e.h1cli.Get("http://" + e.addr + "/_pano/setup.json") }); got != "200 local GET /_pano/setup.json abs=true" {
		t.Fatalf("self absolute: %q", got)
	}
	if got := body(t, func() (*http.Response, error) { return e.h1cli.Get("http://pano.internal/_pano/setup.json") }); got != "200 local GET /_pano/setup.json abs=true" {
		t.Fatalf("magic http: %q", got)
	}
	if got := body(t, func() (*http.Response, error) { return e.h1cli.Get("https://pano.internal/_pano/ok") }); !strings.HasPrefix(got, "200 local GET /_pano/ok") {
		t.Fatalf("magic https: %q", got)
	}
	// Direct (non-proxy) request on the port.
	if got := body(t, func() (*http.Response, error) { return http.Get("http://" + e.addr + "/") }); got != "200 local GET / abs=false" {
		t.Fatalf("direct: %q", got)
	}
	// Nothing about our own site becomes a flow.
	select {
	case id := <-e.sink.done:
		t.Fatalf("unexpected flow %v", id)
	default:
	}
}

// pano.internal is terminated even when decryption is off, and a handshake
// against it is counted for the client but recorded as no flow.
func TestMagicHostIgnoresDecryptMode(t *testing.T) {
	e := start(t, func(o *proxy.Options) {
		o.Local = localSite()
		o.Decrypt = proxy.DecryptPolicy{Mode: proxy.DecryptOff}
	})
	if got := body(t, func() (*http.Response, error) { return e.h1cli.Get("https://pano.internal/_pano/ok") }); !strings.HasPrefix(got, "200 local") {
		t.Fatalf("magic https with decrypt off: %q", got)
	}
	// A client without the CA fails the handshake — counted, not a flow.
	pu, _ := url.Parse("http://" + e.addr)
	untrusting := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}}
	if _, err := untrusting.Get("https://pano.internal/_pano/ok"); err == nil {
		t.Fatal("expected certificate error")
	}
	select {
	case id := <-e.sink.done:
		t.Fatalf("unexpected flow %v for pano.internal", id)
	default:
	}
}

// AddListener serves the same proxy on another address and that address is
// "self" for the loop guard and the local site.
func TestAddListener(t *testing.T) {
	e := start(t, func(o *proxy.Options) { o.Local = localSite() })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e.srv.AddListener(ln)
	extra := ln.Addr().String()
	if got := body(t, func() (*http.Response, error) { return http.Get("http://" + extra + "/") }); got != "200 local GET / abs=false" {
		t.Fatalf("direct on extra: %q", got)
	}
	pu, _ := url.Parse("http://" + extra)
	viaExtra := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	if got := body(t, func() (*http.Response, error) { return viaExtra.Get("http://" + extra + "/x") }); got != "200 local GET /x abs=true" {
		t.Fatalf("self via extra: %q", got)
	}
	if err := e.srv.RemoveListener(ln); err != nil {
		t.Fatal(err)
	}
	if c, err := net.Dial("tcp", extra); err == nil {
		c.Close()
		t.Fatal("extra listener should be closed")
	}
}
