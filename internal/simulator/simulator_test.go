package simulator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// listJSON is real `simctl list devices booted -j` output (one booted
// iPhone, one empty runtime), plus an unavailable and a shutdown device.
const listJSON = `{
  "devices" : {
    "com.apple.CoreSimulator.SimRuntime.iOS-26-4" : [
      {"udid" : "94517A5C-8E3A-4BB0-B76B-F364EC01CDFD", "isAvailable" : true,
       "state" : "Booted", "name" : "iPhone 17"},
      {"udid" : "AAAA", "isAvailable" : false, "state" : "Booted", "name" : "Broken"},
      {"udid" : "BBBB", "isAvailable" : true, "state" : "Shutdown", "name" : "iPad Pro"}
    ],
    "com.apple.CoreSimulator.SimRuntime.iOS-26-0" : [ ]
  }
}`

type fakeXcrun struct {
	t     *testing.T
	mu    sync.Mutex
	calls [][]string
	fail  map[string]error // keyed by simctl subcommand
}

func (f *fakeXcrun) Run(_ context.Context, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// name is m.binary, overridden in tests; only the argv matters.
	if len(args) == 0 || args[0] != "simctl" {
		f.t.Fatalf("unexpected command %s %q", name, args)
	}
	f.calls = append(f.calls, args)
	if err := f.fail[args[1]]; err != nil {
		return "", err
	}
	if args[1] == "list" {
		return listJSON, nil
	}
	return "", nil
}

func (f *fakeXcrun) argv() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.calls...)
}

func newTestManager(t *testing.T) (*Manager, *fakeXcrun) {
	t.Helper()
	dir := t.TempDir()
	cert := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(cert, []byte("-----BEGIN CERTIFICATE-----\nfake\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeXcrun{t: t, fail: map[string]error{}}
	m := New(cert, filepath.Join(dir, "sub", "simulators.json"))
	m.binary = os.Args[0] // exists, so Supported() is true
	m.run = f
	return m, f
}

func TestBootedParsesAndFilters(t *testing.T) {
	m, _ := newTestManager(t)
	devs, err := m.Booted(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Device{{UDID: "94517A5C-8E3A-4BB0-B76B-F364EC01CDFD", Name: "iPhone 17", Runtime: "iOS 26.4"}}
	if !reflect.DeepEqual(devs, want) {
		t.Fatalf("Booted = %+v, want %+v", devs, want)
	}
	if got := devs[0].Label(); got != "iPhone 17 (iOS 26.4)" {
		t.Fatalf("Label = %q", got)
	}
}

func TestSuggestInstallDismissCycle(t *testing.T) {
	m, f := newTestManager(t)
	ctx := context.Background()

	devs := m.Suggest(ctx)
	if len(devs) != 1 {
		t.Fatalf("fresh Suggest = %+v", devs)
	}

	// Install records the CA fingerprint: no more suggestions.
	if err := m.Install(ctx, devs[0]); err != nil {
		t.Fatal(err)
	}
	want := []string{"simctl", "keychain", devs[0].UDID, "add-root-cert", m.certPath}
	found := false
	for _, c := range f.argv() {
		if reflect.DeepEqual(c, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no add-root-cert call: %q", f.argv())
	}
	if got := m.Suggest(ctx); len(got) != 0 {
		t.Fatalf("Suggest after install = %+v", got)
	}

	// A rotated CA suggests again; a dismissal silences it for good.
	if err := os.WriteFile(m.certPath, []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}
	devs = m.Suggest(ctx)
	if len(devs) != 1 {
		t.Fatalf("Suggest after rotation = %+v", devs)
	}
	if err := m.Dismiss(devs); err != nil {
		t.Fatal(err)
	}
	if got := m.Suggest(ctx); len(got) != 0 {
		t.Fatalf("Suggest after dismiss = %+v", got)
	}
}

func TestSuggestIsBestEffort(t *testing.T) {
	m, f := newTestManager(t)
	f.fail["list"] = errors.New("simctl: Unable to locate a developer directory")
	if got := m.Suggest(context.Background()); got != nil {
		t.Fatalf("Suggest with failing simctl = %+v", got)
	}
	// Missing CA: nothing to install, so nothing to suggest.
	m2, _ := newTestManager(t)
	if err := os.Remove(m2.certPath); err != nil {
		t.Fatal(err)
	}
	if got := m2.Suggest(context.Background()); got != nil {
		t.Fatalf("Suggest without CA = %+v", got)
	}
}

func TestRebootShutsDownThenBoots(t *testing.T) {
	m, f := newTestManager(t)
	d := Device{UDID: "94517A5C", Name: "iPhone 17"}
	if err := m.Reboot(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	calls := f.argv()
	if len(calls) != 2 || calls[0][1] != "shutdown" || calls[1][1] != "boot" {
		t.Fatalf("Reboot calls = %q", calls)
	}
	f.fail["boot"] = errors.New("boom")
	if err := m.Reboot(context.Background(), d); err == nil || !strings.Contains(err.Error(), "boot") {
		t.Fatalf("boot failure not surfaced: %v", err)
	}
}

func TestParseRuntime(t *testing.T) {
	for in, want := range map[string]string{
		"com.apple.CoreSimulator.SimRuntime.iOS-26-4":     "iOS 26.4",
		"com.apple.CoreSimulator.SimRuntime.watchOS-11-0": "watchOS 11.0",
		"com.apple.CoreSimulator.SimRuntime.iOS-18":       "iOS 18",
		"weird":    "",
		"iOS-26-4": "",
	} {
		if got := parseRuntime(in); got != want {
			t.Fatalf("parseRuntime(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStateSurvivesGarbage(t *testing.T) {
	m, _ := newTestManager(t)
	if err := os.MkdirAll(filepath.Dir(m.statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := m.Suggest(context.Background()); len(got) != 1 {
		t.Fatalf("Suggest over corrupt state = %+v", got)
	}
	if err := m.Install(context.Background(), Device{UDID: "u", Name: "n"}); err != nil {
		t.Fatal(err)
	}
}
