//go:build darwin

package sysproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// listing is real -listallnetworkservices output from macOS 26, with one
// disabled service added.
const listing = `An asterisk (*) denotes that a network service is disabled.
Chromatic - Player 01
*Thunderbolt Bridge
Wi-Fi
TunnelBear
`

// fakeSystem simulates networksetup (directly and via osascript) and records
// every invocation. It is safe for concurrent use.
type fakeSystem struct {
	t     *testing.T
	mu    sync.Mutex
	state map[string]*serviceState
	calls [][]string // networksetup argv; admin invocations get a leading "admin"

	denyDirect bool // direct -set* calls fail with a permission error
	failSet    func(args []string) error
	onSet      func(args []string)
}

func newFake(t *testing.T) *fakeSystem {
	t.Helper()
	return &fakeSystem{
		t: t,
		state: map[string]*serviceState{
			"Chromatic - Player 01": {Name: "Chromatic - Player 01"},
			"Thunderbolt Bridge":    {Name: "Thunderbolt Bridge", Bypass: []string{"*.local"}},
			"Wi-Fi": {
				Name:   "Wi-Fi",
				Web:    proxySetting{Enabled: true, Server: "proxy.corp.example", Port: 8080},
				Secure: proxySetting{Enabled: false, Server: "127.0.0.1", Port: 9090},
				Bypass: []string{"*.local", "169.254/16", "*.corp.example"},
			},
			"TunnelBear": {
				Name:   "TunnelBear",
				Web:    proxySetting{Server: "127.0.0.1", Port: 9090},
				Secure: proxySetting{Server: "127.0.0.1", Port: 9090},
				Bypass: []string{"*.local", "169.254/16"},
			},
		},
	}
}

func (f *fakeSystem) snapshotState() map[string]serviceState {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]serviceState, len(f.state))
	for k, v := range f.state {
		c := *v
		c.Bypass = append([]string(nil), v.Bypass...)
		out[k] = c
	}
	return out
}

func (f *fakeSystem) argv() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.calls...)
}

func (f *fakeSystem) Run(_ context.Context, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch name {
	case osascriptPath:
		if len(args) != 2 || args[0] != "-e" {
			f.t.Fatalf("unexpected osascript args %q", args)
		}
		bin, nsArgs := parseAdminScript(f.t, args[1])
		if bin != networksetupPath {
			f.t.Fatalf("admin script runs %q, want networksetup", bin)
		}
		f.calls = append(f.calls, append([]string{"admin"}, nsArgs...))
		return f.networksetup(nsArgs, true)
	case networksetupPath:
		f.calls = append(f.calls, args)
		return f.networksetup(args, false)
	}
	f.t.Fatalf("unexpected command %s %q", name, args)
	return "", nil
}

func (f *fakeSystem) networksetup(args []string, admin bool) (string, error) {
	op := args[0]
	if op == "-listallnetworkservices" {
		return listing, nil
	}
	if strings.HasPrefix(op, "-set") {
		if f.onSet != nil {
			f.onSet(args)
		}
		if f.denyDirect && !admin {
			return "", &cmdError{
				name: networksetupPath, args: args, code: 1,
				stderr: "networksetup: You are not authorized to perform this operation. This requires admin rights.\n",
			}
		}
		if f.failSet != nil {
			if err := f.failSet(args); err != nil {
				return "", err
			}
		}
	}
	svc, ok := f.state[args[1]]
	if !ok {
		const msg = "** Error: Unable to find item in network database.\n"
		return msg, &cmdError{name: networksetupPath, args: args, code: 8, stdout: msg}
	}
	switch op {
	case "-getwebproxy":
		return formatProxy(svc.Web), nil
	case "-getsecurewebproxy":
		return formatProxy(svc.Secure), nil
	case "-getproxybypassdomains":
		if len(svc.Bypass) == 0 {
			return fmt.Sprintf("There aren't any bypass domains set on %s.\n", svc.Name), nil
		}
		return strings.Join(svc.Bypass, "\n") + "\n", nil
	case "-setwebproxy", "-setsecurewebproxy":
		port, err := strconv.Atoi(args[3])
		if err != nil || args[2] == "" {
			return "", &cmdError{name: networksetupPath, args: args, code: 1, stdout: "** Error: The parameters were not valid.\n"}
		}
		p := proxySetting{Enabled: true, Server: args[2], Port: port}
		if op == "-setwebproxy" {
			svc.Web = p
		} else {
			svc.Secure = p
		}
	case "-setwebproxystate", "-setsecurewebproxystate":
		on := args[2] == "on"
		if op == "-setwebproxystate" {
			svc.Web.Enabled = on
		} else {
			svc.Secure.Enabled = on
		}
	case "-setproxybypassdomains":
		if len(args) == 3 && args[2] == noBypassMarker {
			svc.Bypass = nil
		} else {
			svc.Bypass = append([]string(nil), args[2:]...)
		}
	default:
		f.t.Fatalf("unexpected networksetup op %q", op)
	}
	return "", nil
}

func formatProxy(p proxySetting) string {
	enabled := "No"
	if p.Enabled {
		enabled = "Yes"
	}
	return fmt.Sprintf("Enabled: %s\nServer: %s\nPort: %d\nAuthenticated Proxy Enabled: 0\n", enabled, p.Server, p.Port)
}

// parseAdminScript reverses adminScript: it unwraps the AppleScript literal
// and splits the single-quoted /bin/sh words.
func parseAdminScript(t *testing.T, script string) (string, []string) {
	t.Helper()
	const prefix, suffix = `do shell script "`, `" with administrator privileges`
	if !strings.HasPrefix(script, prefix) || !strings.HasSuffix(script, suffix) {
		t.Fatalf("malformed admin script %q", script)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(script, prefix), suffix)
	var sb strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] == '\\' && i+1 < len(body) {
			i++
		}
		sb.WriteByte(body[i])
	}
	cmd := sb.String()

	var words []string
	for i := 0; i < len(cmd); {
		if cmd[i] == ' ' {
			i++
			continue
		}
		if cmd[i] != '\'' {
			t.Fatalf("unquoted word at %d in %q", i, cmd)
		}
		i++
		var w strings.Builder
		for {
			if i >= len(cmd) {
				t.Fatalf("unterminated quote in %q", cmd)
			}
			if cmd[i] != '\'' {
				w.WriteByte(cmd[i])
				i++
				continue
			}
			if strings.HasPrefix(cmd[i:], `'\''`) {
				w.WriteByte('\'')
				i += 4
				continue
			}
			i++
			break
		}
		words = append(words, w.String())
	}
	if len(words) == 0 {
		t.Fatalf("empty admin command %q", cmd)
	}
	return words[0], words[1:]
}

func newTestManager(t *testing.T, f *fakeSystem) (*manager, string) {
	t.Helper()
	statePath := filepath.Join(t.TempDir(), "sub", "sysproxy.json")
	m := newManager(statePath, nil, f)
	m.binary = networksetupPath // exists on every macOS host
	m.now = func() time.Time { return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC) }
	return m, statePath
}

func readState(t *testing.T, path string) snapshot {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var s snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("parse state: %v", err)
	}
	return s
}

func TestParseServices(t *testing.T) {
	got := parseServices(listing)
	want := []string{"Chromatic - Player 01", "Wi-Fi", "TunnelBear"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseServices = %q, want %q", got, want)
	}
	if got := parseServices(""); len(got) != 0 {
		t.Fatalf("parseServices(empty) = %q", got)
	}
}

func TestParseProxy(t *testing.T) {
	cases := []struct {
		in   string
		want proxySetting
	}{
		{
			"Enabled: No\nServer: 127.0.0.1\nPort: 9090\nAuthenticated Proxy Enabled: 0\n",
			proxySetting{Enabled: false, Server: "127.0.0.1", Port: 9090},
		},
		{
			"Enabled: Yes\nServer: proxy.corp.example\nPort: 8080\nAuthenticated Proxy Enabled: 0\n",
			proxySetting{Enabled: true, Server: "proxy.corp.example", Port: 8080},
		},
		{"Enabled: No\nServer: \nPort: 0\nAuthenticated Proxy Enabled: 0\n", proxySetting{}},
	}
	for _, c := range cases {
		got, err := parseProxy(c.in)
		if err != nil {
			t.Fatalf("parseProxy(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("parseProxy(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "** Error: Unable to find item in network database.\n", "Enabled: Yes\nServer: x\nPort: abc\n"} {
		if _, err := parseProxy(bad); err == nil {
			t.Fatalf("parseProxy(%q) succeeded, want error", bad)
		}
	}
}

func TestParseBypass(t *testing.T) {
	if got := parseBypass("There aren't any bypass domains set on Chromatic - Player 01.\n"); len(got) != 0 {
		t.Fatalf("parseBypass(none) = %q", got)
	}
	got := parseBypass("*.local\n169.254/16\n")
	if want := []string{"*.local", "169.254/16"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parseBypass = %q, want %q", got, want)
	}
}

func TestEnableSnapshotsBeforeChanging(t *testing.T) {
	f := newFake(t)
	m, statePath := newTestManager(t, f)
	before := f.snapshotState()

	stateSeen := true
	f.onSet = func([]string) {
		if _, err := os.Stat(statePath); err != nil {
			stateSeen = false
		}
	}
	if err := m.Enable(context.Background(), "127.0.0.1", 9091, []string{"api.internal"}); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !stateSeen {
		t.Fatal("a -set* command ran before the snapshot was written")
	}

	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("state file mode = %o, want 0600", perm)
	}
	snap := readState(t, statePath)
	if snap.Version != 1 || snap.Pano != (endpoint{"127.0.0.1", 9091}) || snap.UsedAdmin {
		t.Fatalf("unexpected snapshot header %+v", snap)
	}
	if snap.SetAt != m.now() {
		t.Fatalf("set_at = %v, want %v", snap.SetAt, m.now())
	}
	var names []string
	for _, svc := range snap.Services {
		names = append(names, svc.Name)
		if !reflect.DeepEqual(svc, before[svc.Name]) {
			t.Fatalf("snapshot for %s = %+v, want pre-change state %+v", svc.Name, svc, before[svc.Name])
		}
	}
	if want := []string{"Chromatic - Player 01", "Wi-Fi", "TunnelBear"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("snapshotted services %q, want enabled services %q", names, want)
	}

	// The first three calls are the listing and the Wi-Fi-less first
	// service's queries; no -set* may appear before the last -get*.
	lastGet, firstSet := -1, -1
	for i, c := range f.argv() {
		if strings.HasPrefix(c[0], "-get") || c[0] == "-listallnetworkservices" {
			lastGet = i
		} else if firstSet == -1 {
			firstSet = i
		}
	}
	if firstSet < lastGet {
		t.Fatalf("set command at %d before last query at %d: %q", firstSet, lastGet, f.argv())
	}

	// Every enabled service now points at pano with a merged bypass list;
	// the disabled service is untouched.
	after := f.snapshotState()
	for _, name := range names {
		svc := after[name]
		want := proxySetting{Enabled: true, Server: "127.0.0.1", Port: 9091}
		if svc.Web != want || svc.Secure != want {
			t.Fatalf("%s after Enable = %+v", name, svc)
		}
	}
	if got, want := after["Wi-Fi"].Bypass, []string{"*.local", "169.254/16", "*.corp.example", "localhost", "127.0.0.1", "api.internal"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Wi-Fi bypass = %q, want %q", got, want)
	}
	if got, want := after["Chromatic - Player 01"].Bypass, []string{"localhost", "127.0.0.1", "*.local", "169.254/16", "api.internal"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Chromatic bypass = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(after["Thunderbolt Bridge"], before["Thunderbolt Bridge"]) {
		t.Fatalf("disabled service was modified: %+v", after["Thunderbolt Bridge"])
	}
}

func TestEnableKeepsExistingSnapshot(t *testing.T) {
	f := newFake(t)
	m, statePath := newTestManager(t, f)
	ctx := context.Background()
	if err := m.Enable(ctx, "127.0.0.1", 9091, nil); err != nil {
		t.Fatal(err)
	}
	first := readState(t, statePath)
	if err := m.Enable(ctx, "127.0.0.1", 9092, nil); err != nil {
		t.Fatal(err)
	}
	second := readState(t, statePath)
	if !reflect.DeepEqual(first.Services, second.Services) {
		t.Fatalf("re-Enable overwrote the pre-pano snapshot: %+v", second.Services)
	}
	if second.Pano.Port != 9092 {
		t.Fatalf("pano endpoint not updated: %+v", second.Pano)
	}
	if err := m.Disable(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.snapshotState()["Wi-Fi"].Web; got != (proxySetting{Enabled: true, Server: "proxy.corp.example", Port: 8080}) {
		t.Fatalf("Wi-Fi web proxy after Disable = %+v", got)
	}
}

func TestEnableRejectsBadInput(t *testing.T) {
	m, _ := newTestManager(t, newFake(t))
	for _, c := range []struct {
		host string
		port int
	}{{"", 9091}, {"127.0.0.1", 0}, {"127.0.0.1", 70000}} {
		if err := m.Enable(context.Background(), c.host, c.port, nil); err == nil {
			t.Fatalf("Enable(%q, %d) succeeded", c.host, c.port)
		}
	}
	m.binary = filepath.Join(t.TempDir(), "missing")
	if m.Supported() {
		t.Fatal("Supported() with missing binary")
	}
	if err := m.Enable(context.Background(), "127.0.0.1", 9091, nil); err == nil {
		t.Fatal("Enable succeeded without networksetup")
	}
}

func TestDisableRestoresExactPriorState(t *testing.T) {
	f := newFake(t)
	m, statePath := newTestManager(t, f)
	ctx := context.Background()
	before := f.snapshotState()

	if err := m.Enable(ctx, "127.0.0.1", 9091, []string{"api.internal"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
	if err := m.Disable(ctx); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state file still present after Disable (err=%v)", err)
	}

	after := f.snapshotState()
	for name, want := range before {
		got := after[name]
		if got.Web.Enabled != want.Web.Enabled || got.Secure.Enabled != want.Secure.Enabled {
			t.Fatalf("%s enabled state: got web=%v secure=%v, want web=%v secure=%v",
				name, got.Web.Enabled, got.Secure.Enabled, want.Web.Enabled, want.Secure.Enabled)
		}
		if want.Web.Enabled && got.Web != want.Web {
			t.Fatalf("%s web proxy = %+v, want %+v", name, got.Web, want.Web)
		}
		if !reflect.DeepEqual(got.Bypass, want.Bypass) {
			t.Fatalf("%s bypass = %q, want %q", name, got.Bypass, want.Bypass)
		}
	}

	// Wi-Fi had a foreign proxy enabled: it must be re-pointed and turned on,
	// the secure proxy turned off, and the old bypass list written back.
	var wifi [][]string
	for _, c := range f.argv() {
		if c[1] == "Wi-Fi" {
			wifi = append(wifi, c)
		}
	}
	want := [][]string{
		{"-setwebproxy", "Wi-Fi", "proxy.corp.example", "8080"},
		{"-setwebproxystate", "Wi-Fi", "on"},
		{"-setsecurewebproxystate", "Wi-Fi", "off"},
		{"-setproxybypassdomains", "Wi-Fi", "*.local", "169.254/16", "*.corp.example"},
	}
	if !reflect.DeepEqual(wifi, want) {
		t.Fatalf("Wi-Fi restore commands:\n got %q\nwant %q", wifi, want)
	}
	// A service with no bypass list is cleared with the Empty marker.
	found := false
	for _, c := range f.argv() {
		if c[0] == "-setproxybypassdomains" && c[1] == "Chromatic - Player 01" {
			found = true
			if !reflect.DeepEqual(c[2:], []string{"Empty"}) {
				t.Fatalf("empty bypass restore args = %q", c)
			}
		}
	}
	if !found {
		t.Fatal("no bypass restore for Chromatic - Player 01")
	}
}

func TestDisableWithoutStateIsNoop(t *testing.T) {
	f := newFake(t)
	m, _ := newTestManager(t, f)
	if err := m.Disable(context.Background()); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if calls := f.argv(); len(calls) != 0 {
		t.Fatalf("Disable without state ran commands: %q", calls)
	}
}

func TestDisableSkipsVanishedServiceAndKeepsStateOnFailure(t *testing.T) {
	f := newFake(t)
	m, statePath := newTestManager(t, f)
	ctx := context.Background()
	if err := m.Enable(ctx, "127.0.0.1", 9091, nil); err != nil {
		t.Fatal(err)
	}

	// TunnelBear went away: not an error. Wi-Fi fails: reported, state kept.
	f.mu.Lock()
	delete(f.state, "TunnelBear")
	f.mu.Unlock()
	f.failSet = func(args []string) error {
		if args[1] == "Wi-Fi" {
			return &cmdError{name: networksetupPath, args: args, code: 1, stdout: "** Error: The parameters were not valid.\n"}
		}
		return nil
	}
	err := m.Disable(ctx)
	if err == nil || !strings.Contains(err.Error(), "Wi-Fi") {
		t.Fatalf("Disable error = %v, want Wi-Fi failure", err)
	}
	if strings.Contains(err.Error(), "TunnelBear") {
		t.Fatalf("vanished service reported as error: %v", err)
	}
	if _, statErr := os.Stat(statePath); statErr != nil {
		t.Fatalf("state file removed despite failure: %v", statErr)
	}
	if got := f.snapshotState()["Chromatic - Player 01"].Web.Enabled; got {
		t.Fatal("other services were not restored")
	}

	// Once the failure clears, the retry finishes and removes the state.
	f.failSet = nil
	restored, err := m.RestoreStale(ctx)
	if err != nil || !restored {
		t.Fatalf("RestoreStale = %v, %v", restored, err)
	}
	if _, statErr := os.Stat(statePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("state file still present: %v", statErr)
	}
}

func TestRestoreStale(t *testing.T) {
	f := newFake(t)
	m, statePath := newTestManager(t, f)
	ctx := context.Background()

	restored, err := m.RestoreStale(ctx)
	if err != nil || restored {
		t.Fatalf("RestoreStale without state = %v, %v", restored, err)
	}

	// A snapshot left behind by a crashed daemon.
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := `{
  "version": 1,
  "set_at": "2026-08-28T11:00:00Z",
  "pano": {"host": "127.0.0.1", "port": 9091},
  "services": [
    {"name": "Wi-Fi", "web": {"enabled": false, "server": "", "port": 0},
     "secure": {"enabled": true, "server": "old.example", "port": 3128}, "bypass": []}
  ]
}`
	if err := os.WriteFile(statePath, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.state["Wi-Fi"].Web = proxySetting{Enabled: true, Server: "127.0.0.1", Port: 9091}
	f.state["Wi-Fi"].Secure = proxySetting{Enabled: true, Server: "127.0.0.1", Port: 9091}
	f.mu.Unlock()

	restored, err = m.RestoreStale(ctx)
	if err != nil || !restored {
		t.Fatalf("RestoreStale = %v, %v", restored, err)
	}
	wifi := f.snapshotState()["Wi-Fi"]
	if wifi.Web.Enabled || wifi.Secure != (proxySetting{Enabled: true, Server: "old.example", Port: 3128}) || len(wifi.Bypass) != 0 {
		t.Fatalf("Wi-Fi after RestoreStale = %+v", wifi)
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state file not removed: %v", err)
	}
}

func TestReadSnapshotRejectsGarbage(t *testing.T) {
	m, statePath := newTestManager(t, newFake(t))
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RestoreStale(context.Background()); err == nil {
		t.Fatal("corrupt state accepted")
	}
	if err := os.WriteFile(statePath, []byte(`{"version": 99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Disable(context.Background()); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("future version accepted: %v", err)
	}
}

func TestPrivilegeFallback(t *testing.T) {
	f := newFake(t)
	f.denyDirect = true
	m, statePath := newTestManager(t, f)
	ctx := context.Background()

	if err := m.Enable(ctx, "127.0.0.1", 9091, nil); err != nil {
		t.Fatalf("Enable with admin fallback: %v", err)
	}
	if !readState(t, statePath).UsedAdmin {
		t.Fatal("snapshot does not record used_admin")
	}
	calls := f.argv()
	// The first set command is tried directly, refused, then retried as
	// admin; every later set command goes straight to admin.
	var direct, admin int
	for i, c := range calls {
		switch {
		case c[0] == "admin":
			admin++
		case strings.HasPrefix(c[0], "-set"):
			direct++
			if i+1 >= len(calls) || calls[i+1][0] != "admin" || !reflect.DeepEqual(calls[i+1][1:], c) {
				t.Fatalf("direct set at %d not retried as admin: %q", i, calls)
			}
		}
	}
	if direct != 1 || admin != 9 {
		t.Fatalf("direct=%d admin=%d set calls, want 1 and 9", direct, admin)
	}
	if got := f.snapshotState()["Wi-Fi"].Web; got != (proxySetting{Enabled: true, Server: "127.0.0.1", Port: 9091}) {
		t.Fatalf("Wi-Fi not set through admin path: %+v", got)
	}

	// Restore uses the admin path from the start.
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
	if err := m.Disable(ctx); err != nil {
		t.Fatalf("Disable via admin: %v", err)
	}
	for _, c := range f.argv() {
		if c[0] != "admin" {
			t.Fatalf("restore issued a non-admin command: %q", c)
		}
	}
	if got := f.snapshotState()["Wi-Fi"].Web; got != (proxySetting{Enabled: true, Server: "proxy.corp.example", Port: 8080}) {
		t.Fatalf("Wi-Fi not restored: %+v", got)
	}
}

func TestNonPermissionFailureIsNotEscalated(t *testing.T) {
	f := newFake(t)
	m, _ := newTestManager(t, f)
	f.failSet = func(args []string) error {
		if args[0] == "-setsecurewebproxy" && args[1] == "TunnelBear" {
			return &cmdError{name: networksetupPath, args: args, code: 1, stdout: "** Error: The parameters were not valid.\n"}
		}
		return nil
	}
	err := m.Enable(context.Background(), "127.0.0.1", 9091, nil)
	if err == nil || !strings.Contains(err.Error(), "TunnelBear") || !strings.Contains(err.Error(), "parameters were not valid") {
		t.Fatalf("Enable error = %v", err)
	}
	for _, c := range f.argv() {
		if c[0] == "admin" {
			t.Fatalf("non-permission failure escalated: %q", c)
		}
	}
	// The other services were still configured.
	if got := f.snapshotState()["Wi-Fi"].Secure; !got.pointsAt("127.0.0.1", 9091) {
		t.Fatalf("Wi-Fi not configured after unrelated failure: %+v", got)
	}
}

func TestIsPermissionError(t *testing.T) {
	yes := []string{
		"You are not authorized to perform this operation.",
		"Operation not permitted",
		"Permission denied",
		"This command requires admin privileges",
		"** Error: authorization failed",
	}
	for _, s := range yes {
		if !isPermissionError(&cmdError{code: 1, stderr: s}) {
			t.Errorf("%q not detected as permission error", s)
		}
	}
	no := []error{
		nil,
		errors.New("permission"),
		&cmdError{code: 0, stderr: "permission"},
		&cmdError{code: 1, stdout: "** Error: The parameters were not valid."},
	}
	for _, err := range no {
		if isPermissionError(err) {
			t.Errorf("%v wrongly detected as permission error", err)
		}
	}
}

func TestAdminScriptQuoting(t *testing.T) {
	args := []string{"-setproxybypassdomains", `Bob's "Wi-Fi" \ 01`, "*.local", "$HOME", "`id`"}
	script := adminScript(networksetupPath, args)
	bin, got := parseAdminScript(t, script)
	if bin != networksetupPath || !reflect.DeepEqual(got, args) {
		t.Fatalf("round trip = %q %q, want %q", bin, got, args)
	}
	want := `do shell script "'/usr/sbin/networksetup' '-setproxybypassdomains' 'Bob'\\''s \"Wi-Fi\" \\ 01' '*.local' '$HOME' '` + "`id`" + `'" with administrator privileges`
	if script != want {
		t.Fatalf("adminScript =\n%s\nwant\n%s", script, want)
	}
}

func TestStatus(t *testing.T) {
	f := newFake(t)
	m, statePath := newTestManager(t, f)
	ctx := context.Background()

	st, err := m.Status(ctx, "127.0.0.1", 9091)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Supported || st.Enabled || st.SetByPano || len(st.Services) != 0 || st.Detail == "" {
		t.Fatalf("initial status = %+v", st)
	}
	// A foreign proxy at the same address counts as enabled but not ours.
	st, err = m.Status(ctx, "proxy.corp.example", 8080)
	if err != nil || !st.Enabled || st.SetByPano || !reflect.DeepEqual(st.Services, []string{"Wi-Fi"}) {
		t.Fatalf("foreign proxy status = %+v, %v", st, err)
	}

	if err := m.Enable(ctx, "127.0.0.1", 9091, nil); err != nil {
		t.Fatal(err)
	}
	st, err = m.Status(ctx, "127.0.0.1", 9091)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || !st.SetByPano {
		t.Fatalf("status after Enable = %+v", st)
	}
	if want := []string{"Chromatic - Player 01", "Wi-Fi", "TunnelBear"}; !reflect.DeepEqual(st.Services, want) {
		t.Fatalf("services = %q, want %q", st.Services, want)
	}
	if want := "3 services (Chromatic - Player 01, Wi-Fi, TunnelBear) → 127.0.0.1:9091"; st.Detail != want {
		t.Fatalf("detail = %q, want %q", st.Detail, want)
	}

	// Somebody else turned the proxies off underneath us: stale state.
	for _, svc := range f.state {
		svc.Web.Enabled, svc.Secure.Enabled = false, false
	}
	st, err = m.Status(ctx, "127.0.0.1", 9091)
	if err != nil || st.Enabled || !st.SetByPano || !strings.Contains(st.Detail, "stale") {
		t.Fatalf("stale status = %+v, %v", st, err)
	}

	// A cancelled context reports from the state file only.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
	st, err = m.Status(cancelled, "127.0.0.1", 9091)
	if err != nil || !st.SetByPano || len(st.Services) != 3 || len(f.argv()) != 0 {
		t.Fatalf("status with cancelled ctx = %+v, %v (calls %q)", st, err, f.argv())
	}

	if err := os.WriteFile(statePath, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Status(ctx, "127.0.0.1", 9091); err == nil {
		t.Fatal("corrupt state not reported by Status")
	}
}

func TestStatusSingleServiceDetail(t *testing.T) {
	f := newFake(t)
	m, _ := newTestManager(t, f)
	st, err := m.Status(context.Background(), "proxy.corp.example", 8080)
	if err != nil {
		t.Fatal(err)
	}
	if want := "1 service (Wi-Fi) → proxy.corp.example:8080"; st.Detail != want {
		t.Fatalf("detail = %q, want %q", st.Detail, want)
	}
}

func TestExecRunner(t *testing.T) {
	ctx := context.Background()
	out, err := (execRunner{}).Run(ctx, "/bin/sh", "-c", "echo hello; echo oops >&2; exit 3")
	if out != "hello\n" {
		t.Fatalf("stdout = %q", out)
	}
	var ce *cmdError
	if !errors.As(err, &ce) || ce.code != 3 || ce.stderr != "oops\n" || ce.stdout != "hello\n" {
		t.Fatalf("err = %#v", err)
	}
	if msg := ce.Error(); !strings.Contains(msg, "sh -c") || !strings.Contains(msg, "exit status 3") || !strings.Contains(msg, "oops") {
		t.Fatalf("Error() = %q", msg)
	}
	if _, err := (execRunner{}).Run(ctx, filepath.Join(t.TempDir(), "nope")); err == nil || errors.As(err, &ce) {
		t.Fatalf("missing binary error = %v", err)
	}
	if out, err := (execRunner{}).Run(ctx, "/bin/echo", "ok"); err != nil || out != "ok\n" {
		t.Fatalf("echo = %q, %v", out, err)
	}
}

func TestNewReturnsRealManager(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "state.json"), nil)
	if !m.Supported() {
		t.Skip("networksetup not present")
	}
	// Reading state is the only thing that is safe to exercise for real.
	if restored, err := m.RestoreStale(context.Background()); err != nil || restored {
		t.Fatalf("RestoreStale on fresh manager = %v, %v", restored, err)
	}
}
