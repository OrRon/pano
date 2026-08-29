package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orron/pano/internal/config"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.3.0", "v0.2.9", true},
		{"0.3.0", "0.3.0", false},
		{"v0.2.0", "v0.3.0", false},
		{"v1.0.0", "v0.99.99", true},
		{"v0.3.0", "v0.3.0-beta.1", true},
		{"v0.3.0-beta.2", "v0.3.0-beta.1", true},
		{"v0.3.0-beta.1", "v0.3.0", false},
		{"v0.3.0-rc.1", "v0.3.0-beta.9", true},
		{"v0.3.0", "v0.3.1-0.20260830120000-abcdef123456", false}, // go pseudo-version past the tag
		{"v0.3.0", "v0.0.0-20260830120000-abcdef123456", true},    // untagged module
		{"v0.3.0+build.7", "v0.3.0", false},
		{"nope", "v0.1.0", false},
		{"v0.3.0", "dev", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestIsDev(t *testing.T) {
	for _, v := range []string{"", "dev", "(devel)", "v0.3.0-4-gabc1234", "v0.3.0-4-gabc1234-dirty", "v0.3.0-dirty"} {
		if !IsDev(v) {
			t.Errorf("IsDev(%q) = false", v)
		}
	}
	for _, v := range []string{"v0.3.0", "0.3.0", "v0.3.0-beta.1", "v0.0.0-20260830120000-abcdef123456"} {
		if IsDev(v) {
			t.Errorf("IsDev(%q) = true", v)
		}
	}
}

func TestHint(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		exe  string
		env  map[string]string
		want string
	}{
		{"/opt/homebrew/Caskroom/pano/0.2.0/pano", nil, "brew upgrade pano"},
		{"/usr/local/Cellar/pano/0.2.0/bin/pano", nil, "brew upgrade pano"},
		{"/home/dev/go/bin/pano", map[string]string{"HOME": "/home/dev"}, "go install github.com/orron/pano/cmd/pano@latest"},
		{"/srv/gopath/bin/pano", map[string]string{"GOPATH": "/srv/gopath"}, "go install github.com/orron/pano/cmd/pano@latest"},
		{"/srv/bin/pano", map[string]string{"GOBIN": "/srv/bin"}, "go install github.com/orron/pano/cmd/pano@latest"},
		{"/usr/local/bin/pano", map[string]string{"HOME": "/home/dev"}, "https://github.com/OrRon/pano/releases/latest"},
	}
	for _, c := range cases {
		if got := Hint(c.exe, env(c.env)); got != c.want {
			t.Errorf("Hint(%q) = %q, want %q", c.exe, got, c.want)
		}
	}
}

func TestDisabled(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	none := config.Paths{}
	if r := Disabled("v0.3.0", env(nil), none); r != "" {
		t.Fatalf("enabled build reported %q", r)
	}
	if r := Disabled("dev", env(nil), none); r == "" {
		t.Fatal("dev build should be disabled")
	}
	for _, k := range []string{EnvDisable, EnvDoNotTrack, "CI"} {
		if r := Disabled("v0.3.0", env(map[string]string{k: "1"}), none); r == "" {
			t.Fatalf("%s=1 should disable", k)
		}
	}
	if r := Disabled("v0.3.0", env(map[string]string{EnvDisable: "0"}), none); r != "" {
		t.Fatalf("%s=0 should not disable: %q", EnvDisable, r)
	}
	dir := t.TempDir()
	paths := config.Paths{Dir: dir}
	if r := Disabled("v0.3.0", env(nil), paths); r != "" {
		t.Fatalf("missing config should keep the default: %q", r)
	}
	if err := os.WriteFile(paths.ConfigFile(), []byte("[updates]\ncheck = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := Disabled("v0.3.0", env(nil), paths); r == "" {
		t.Fatal("config check = false should disable")
	}
	old := Default
	Default = "off"
	defer func() { Default = old }()
	if r := Disabled("v0.3.0", env(nil), none); r != "compiled out" {
		t.Fatalf("compiled out: %q", r)
	}
}

func TestCheckCachesAndBypassesProxy(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "pano/0.") || !strings.HasSuffix(ua, " (update check)") {
			t.Errorf("user agent %q", ua)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v0.3.0", "html_url": "https://github.com/OrRon/pano/releases/tag/v0.3.0"})
	}))
	defer srv.Close()
	// A proxy that would be pano: nothing may reach it.
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")

	state := filepath.Join(t.TempDir(), "update-check.json")
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	env := func(k string) string { return map[string]string{"HOME": "/home/dev"}[k] }
	opts := Options{Current: "v0.2.0", StatePath: state, Endpoint: srv.URL, Exe: "/opt/homebrew/Caskroom/pano/0.2.0/pano", Getenv: env, Now: func() time.Time { return now }}

	info, err := Check(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Available || info.Latest != "0.3.0" || info.Current != "0.2.0" || info.Hint != "brew upgrade pano" || info.URL == "" {
		t.Fatalf("info %+v", info)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits %d", hits.Load())
	}
	// Within the interval: served from the cache.
	opts.Now = func() time.Time { return now.Add(23 * time.Hour) }
	if _, err := Check(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("cache miss: hits %d", hits.Load())
	}
	// Force ignores the cache.
	opts.Force = true
	if _, err := Check(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	opts.Force = false
	// After the interval: asks again.
	opts.Now = func() time.Time { return now.Add(48 * time.Hour) } // the forced check refreshed the cache at +23h
	if _, err := Check(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 3 {
		t.Fatalf("hits %d", hits.Load())
	}
	// Same version installed: nothing to say.
	opts.Current = "v0.3.0"
	info, err = Check(context.Background(), opts)
	if err != nil || info.Available {
		t.Fatalf("up to date: %+v %v", info, err)
	}
	if fi, err := os.Stat(state); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file: %v %v", fi, err)
	}
}

func TestCheckerSwallowsErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	c := Start(context.Background(), Options{Current: "v0.2.0", Endpoint: srv.URL, Exe: "/x/pano", Getenv: func(string) string { return "" }})
	if info := c.Wait(5 * time.Second); info != nil {
		t.Fatalf("expected nil on 500, got %+v", info)
	}
	var nilc *Checker
	if nilc.Result() != nil || nilc.Wait(time.Millisecond) != nil {
		t.Fatal("nil checker must be safe")
	}
}
