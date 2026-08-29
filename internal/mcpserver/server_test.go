package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/orron/pano/internal/client"
)

// TestNewRegistersEverything guards against schema-tag mistakes that make
// AddTool panic at daemon start.
func TestNewRegistersEverything(t *testing.T) {
	s := New(client.New("/nonexistent.sock"), "test", nil)
	if s.MCP() == nil {
		t.Fatal("nil server")
	}
	// Connect an in-memory client and list tools to exercise schema generation.
	ct, st := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.MCP().Run(ctx, st) }()
	c := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := c.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 16 {
		t.Fatalf("got %d tools, want 16", len(tools.Tools))
	}
	for _, tl := range tools.Tools {
		if len(tl.Description) > 2000 {
			t.Errorf("tool %s description is %d bytes (>2000, Claude Code truncates)", tl.Name, len(tl.Description))
		}
	}
	if len(Instructions) > 2000 {
		t.Errorf("Instructions is %d bytes (>2000)", len(Instructions))
	}
	res, err := sess.ListResources(ctx, nil)
	if err != nil || len(res.Resources) < 3 {
		t.Fatalf("resources: %v %d", err, len(res.Resources))
	}
	pr, err := sess.ListPrompts(ctx, nil)
	if err != nil || len(pr.Prompts) != 2 {
		t.Fatalf("prompts: %v %d", err, len(pr.Prompts))
	}
	// A tool call against a dead daemon must return IsError, not a transport failure.
	out, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "pano_status", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !out.IsError {
		t.Fatal("expected IsError when daemon is down")
	}
	// The message must tell the agent that pano is off and how the *user*
	// turns it on; the server never starts the daemon itself.
	got := out.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(got, "pano is off") || !strings.Contains(got, "`pano on`") {
		t.Fatalf("offline message = %q, want 'pano is off' + `pano on`", got)
	}
	// Resources say the same thing instead of leaking the client's wording.
	if _, err := sess.ReadResource(ctx, &mcp.ReadResourceParams{URI: "pano://status"}); err == nil || !strings.Contains(err.Error(), "pano is off") {
		t.Fatalf("resource offline error = %v, want 'pano is off'", err)
	}
}
