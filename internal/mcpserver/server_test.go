package mcpserver

import (
	"context"
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
	if len(tools.Tools) != 15 {
		t.Fatalf("got %d tools, want 15", len(tools.Tools))
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
}
