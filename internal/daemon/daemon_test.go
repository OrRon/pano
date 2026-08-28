package daemon

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/client"
	"github.com/orron/pano/internal/config"
	"github.com/orron/pano/internal/flow"
)

// startDaemon runs a daemon on random ports in a temp home and returns a client.
func startDaemon(t *testing.T) (*client.Client, config.Paths) {
	t.Helper()
	dir := t.TempDir()
	// Keep the socket path short (macOS limit).
	paths := config.Paths{Dir: dir}
	cfg := config.Default()
	cfg.Proxy.Port = 0
	cfg.Proxy.MCPPort = 0
	cfg.Capture.Persist = true
	cfg.SystemProxy.RestoreOnExit = false
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, Options{Paths: paths, Config: cfg, Version: "test"}) }()
	c := client.New(paths.Socket())
	deadline := time.Now().Add(10 * time.Second)
	for !c.Ping(ctx) {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("daemon did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	return c, paths
}

func proxiedClient(t *testing.T, c *client.Client, paths config.Paths) *http.Client {
	t.Helper()
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pem, err := c.CAPEM(context.Background())
	if err != nil || len(pem) == 0 {
		t.Fatalf("ca pem: %v", err)
	}
	_ = paths
	pool := x509Pool(t, pem)
	pu, _ := url.Parse("http://" + st.ProxyAddr)
	return &http.Client{Transport: &http.Transport{
		Proxy:              http.ProxyURL(pu),
		TLSClientConfig:    &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		DisableCompression: true,
	}, Timeout: 10 * time.Second}
}

func TestDaemonEndToEnd(t *testing.T) {
	c, paths := startDaemon(t)
	ctx := context.Background()

	// Origin with a self-signed cert: pano must not trust it... so use plain HTTP
	// for the upstream and HTTPS-through-proxy is exercised in the proxy package.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, `{"path":%q,"echo":%s,"token":"sk-ant-abcdefghijklmnopqrstuvwxyz1234"}`, r.URL.Path, orNull(body))
	}))
	defer origin.Close()

	hc := proxiedClient(t, c, paths)
	req, _ := http.NewRequest("POST", origin.URL+"/v1/thing", strings.NewReader(`{"a":1,"password":"pw"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-token-value")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// List.
	var list api.FlowList
	for i := 0; i < 50; i++ {
		list, err = c.Flows(ctx, api.FlowFilter{})
		if err == nil && len(list.Flows) == 1 && list.Flows[0].State == flow.StateDone {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || len(list.Flows) != 1 {
		t.Fatalf("flows: %v %+v", err, list)
	}
	row := list.Flows[0]
	if row.Method != "POST" || row.Status != 200 || row.Path != "/v1/thing" {
		t.Fatalf("row %+v", row)
	}

	// Detail with redaction.
	det, err := c.Flow(ctx, row.ID, api.FlowQuery{View: api.ViewSummary})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(det.Text, "secret-token-value") || strings.Contains(det.Text, "sk-ant-abcdefghijklmnopqrstuvwxyz1234") {
		t.Fatalf("secrets leaked:\n%s", det.Text)
	}
	if !strings.Contains(det.Text, "== response ==") {
		t.Fatalf("missing response section:\n%s", det.Text)
	}
	// Reveal.
	det2, err := c.Flow(ctx, row.ID, api.FlowQuery{View: api.ViewPretty, RevealSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(det2.Text, "sk-ant-abcdefghijklmnopqrstuvwxyz1234") {
		t.Fatalf("reveal did not reveal:\n%s", det2.Text)
	}
	// Path select.
	det3, err := c.Flow(ctx, row.ID, api.FlowQuery{View: api.ViewRaw, Part: "response", Path: "path"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(det3.Text, "/v1/thing") {
		t.Fatalf("path select:\n%s", det3.Text)
	}

	// Body endpoint.
	b, err := c.Body(ctx, row.ID, "request", true)
	if err != nil || string(b) != `{"a":1,"password":"pw"}` {
		t.Fatalf("body: %v %q", err, b)
	}

	// Rules: mock + hit.
	rule, err := c.AddRule(ctx, api.RuleAddRequest{Rule: &api.Rule{
		Match:   api.Match{Path: "/mocked"},
		Actions: []api.Action{{Type: "mock", Status: 418, Body: `{"mock":true}`}},
	}, Name: "teapot"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err = hc.Get(origin.URL + "/mocked")
	if err != nil {
		t.Fatal(err)
	}
	mb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 418 || !strings.Contains(string(mb), "mock") {
		t.Fatalf("mock: %d %s", resp.StatusCode, mb)
	}
	rules, _ := c.Rules(ctx)
	if len(rules) != 1 || rules[0].Hits != 1 {
		t.Fatalf("rules: %+v", rules)
	}
	if err := c.RemoveRule(ctx, rule.ID); err != nil {
		t.Fatal(err)
	}

	// Replay with a patch; the new flow must be tagged.
	rr, err := c.Replay(ctx, row.ID, api.ReplayRequest{BodyPatch: map[string]any{"a": "2"}, SetHeaders: map[string]string{"X-Replay": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if rr.Status != 200 || rr.NewID == 0 {
		t.Fatalf("replay: %+v", rr)
	}
	nf, err := c.FlowRaw(ctx, rr.NewID)
	if err != nil || !nf.Replay || nf.ReplayOf != row.ID {
		t.Fatalf("replay flow: %v %+v", err, nf)
	}
	if nf.ReqHeaders.Get("X-Pano-Replay-Of") != "" {
		t.Fatal("internal header leaked into captured headers")
	}

	// Diff.
	dr, err := c.Diff(ctx, api.DiffRequest{A: row.ID, B: rr.NewID, Part: "request"})
	if err != nil || dr.Changes == 0 || !strings.Contains(dr.Text, "X-Replay") {
		t.Fatalf("diff: %v %+v", err, dr)
	}

	// Tail: since=now must wait and return the next flow.
	go func() {
		time.Sleep(200 * time.Millisecond)
		r, err := hc.Get(origin.URL + "/tailed")
		if err == nil {
			r.Body.Close()
		}
	}()
	tr, err := c.Tail(ctx, api.TailRequest{Since: "now", WaitMS: 5000})
	if err != nil || len(tr.Flows) != 1 || tr.Flows[0].Path != "/tailed" {
		t.Fatalf("tail: %v %+v", err, tr)
	}

	// HAR export.
	harPath := filepath.Join(paths.Dir, "out.har")
	hr, err := c.HAR(ctx, api.HARRequest{Action: "export", Path: harPath})
	if err != nil || hr.Count < 3 {
		t.Fatalf("har: %v %+v", err, hr)
	}

	// Status sanity.
	st, err := c.Status(ctx)
	if err != nil || !st.Capturing || st.Flows < 3 {
		t.Fatalf("status: %v %+v", err, st)
	}

	// Sessions + search over persisted store.
	sess, err := c.StartSession(ctx, "second")
	if err != nil || sess.Name != "second" {
		t.Fatalf("session: %v %+v", err, sess)
	}
	time.Sleep(200 * time.Millisecond) // let the writer flush
	fl, err := c.Flows(ctx, api.FlowFilter{Q: "tailed"})
	if err != nil || fl.Total == 0 {
		t.Fatalf("fts search: %v %+v", err, fl)
	}
}

func orNull(b []byte) string {
	if len(b) == 0 {
		return "null"
	}
	return string(b)
}
