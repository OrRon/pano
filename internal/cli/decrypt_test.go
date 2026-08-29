package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/orron/pano/internal/api"
)

// TestRenderDecryptPrintsEveryEntry pins the rule that lists are printed in
// full — every host, no counts, no ellipsis — and wrap instead of truncating.
func TestRenderDecryptPrintsEveryEntry(t *testing.T) {
	a := &App{color: false}
	never := []string{
		"*.apple.com", "*.icloud.com", "*.icloud-content.com", "*.mzstatic.com", "*.cdn-apple.com",
		"*.push.apple.com", "*.apple-cloudkit.com", "*.ls.apple.com", "*.crashlytics.com",
		"a-very-long-hostname-to-force-wrapping.example", "another-long-hostname.example",
	}
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	d := api.Decrypt{
		Mode: "only", Only: []string{"api.anthropic.com", "localhost"}, Never: never,
		Rejected: []api.RejectedHost{{Host: "mmg.whatsapp.net", Count: 14, Last: now.Add(-2 * time.Minute)}},
	}
	out := a.renderDecrypt(d, "  ", now)
	for _, want := range append([]string{"only", "api.anthropic.com", "localhost", "rejected", "mmg.whatsapp.net", "×14", "2m ago", "never add --rejected"}, never...) {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "…") {
		t.Fatalf("lists must not be elided:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if len(l) > 130 {
			t.Fatalf("line not wrapped (%d chars): %s", len(l), l)
		}
	}
	if !strings.Contains(a.renderDecrypt(api.Decrypt{Mode: "all"}, "", now), "—") {
		t.Fatal("empty lists render as —")
	}
}

func TestDecryptCommandTree(t *testing.T) {
	a := &App{color: false}
	cmd := a.cmdDecrypt()
	names := map[string]bool{}
	for _, c := range cmd.Commands() {
		names[c.Name()] = true
	}
	for _, want := range []string{"all", "only", "off", "never"} {
		if !names[want] {
			t.Fatalf("missing subcommand %s", want)
		}
	}
	seen := map[string]int{}
	hasRejected := map[string]bool{}
	for _, c := range cmd.Commands() {
		seen[c.Name()]++
		for _, sc := range c.Commands() {
			if sc.Name() == "add" {
				hasRejected[c.Name()] = sc.Flags().Lookup("rejected") != nil
			}
		}
	}
	for n, k := range seen {
		if k != 1 {
			t.Fatalf("subcommand %q registered %d times", n, k)
		}
	}
	if !hasRejected["never"] || hasRejected["only"] {
		t.Fatalf("--rejected belongs to `never add` only: %v", hasRejected)
	}
}
