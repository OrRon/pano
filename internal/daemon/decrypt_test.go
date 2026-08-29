package daemon

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/config"
)

// TestDecryptRoundTrip covers client → control → daemon: partial updates,
// normalisation, the @rejected alias, persistence to config.toml and the
// audit line with its source.
func TestDecryptRoundTrip(t *testing.T) {
	c, paths := startDaemon(t)
	ctx := context.Background()

	d, err := c.Decrypt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d.Mode != "all" || len(d.Only) != 0 || len(d.Never) != len(config.DefaultNever) {
		t.Fatalf("default policy: %+v", d)
	}

	d, err = c.ChangeDecrypt(ctx, api.DecryptChange{Mode: "only", AddOnly: []string{" API.Anthropic.com:443 ", "localhost"}, Source: "mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Mode != "only" || strings.Join(d.Only, ",") != "api.anthropic.com,localhost" {
		t.Fatalf("after change: %+v", d)
	}
	// Idempotent add, then remove by un-normalised spelling.
	d, err = c.ChangeDecrypt(ctx, api.DecryptChange{AddOnly: []string{"localhost"}, RemoveOnly: []string{"API.anthropic.com"}, Source: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(d.Only, ",") != "localhost" {
		t.Fatalf("after remove: %+v", d)
	}
	if _, err := c.ChangeDecrypt(ctx, api.DecryptChange{RemoveNever: []string{"nope.example"}}); err == nil {
		t.Fatal("removing an absent host should fail")
	}
	if _, err := c.ChangeDecrypt(ctx, api.DecryptChange{AddNever: []string{"https://bad/url"}}); err == nil {
		t.Fatal("URL-shaped entry should be rejected")
	}
	if _, err := c.ChangeDecrypt(ctx, api.DecryptChange{Mode: "maybe"}); err == nil {
		t.Fatal("bad mode should be rejected")
	}
	// @rejected with nothing rejected is a no-op, not an error.
	if _, err := c.ChangeDecrypt(ctx, api.DecryptChange{AddNever: []string{api.RejectedAlias}}); err != nil {
		t.Fatal(err)
	}

	// Persisted and visible in status (full lists, not counts).
	cfg, err := config.Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Decrypt.Mode != "only" || strings.Join(cfg.Decrypt.Only, ",") != "localhost" || len(cfg.Decrypt.Never) != len(config.DefaultNever) {
		t.Fatalf("config not persisted: %+v", cfg.Decrypt)
	}
	st, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Decrypt.Mode != "only" || len(st.Decrypt.Never) != len(config.DefaultNever) {
		t.Fatalf("status: %+v", st.Decrypt)
	}

	audit, err := os.ReadFile(paths.AuditLog())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "decrypt source=mcp mode=only +only=") || !strings.Contains(string(audit), "decrypt source=cli") {
		t.Fatalf("audit log:\n%s", audit)
	}
}
