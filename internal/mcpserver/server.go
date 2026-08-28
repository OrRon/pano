package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/orron/pano/internal/client"
)

// Instructions is sent to clients at initialize (kept under 2 KB: Claude Code
// truncates longer instructions).
const Instructions = `pano is a local HTTPS proxy that records this Mac's traffic, including LLM API calls, and lets you shape it.
Workflow: pano_status → pano_flows (filters; ONE line per flow) → pano_flow with view=summary or path=… → only then view=raw with an explicit max_bytes.
For api.anthropic.com / api.openai.com flows use pano_flow_explain instead of reading bodies: it reassembles streams into the final message, tool calls and token usage.
To test an app under bad conditions use pano_rule_add presets (slow_network, fail_rate, offline_host, timeout, rate_limit, hold) with ttl_s so they expire; remove rules when done.
pano_tail polls for new flows with a cursor — loop it while a user reproduces something.
Secrets (API keys, cookies, bearer tokens) are redacted by default; pass reveal_secrets=true only when the user needs the actual value.
pano_system_proxy CHANGES macOS SYSTEM SETTINGS — only call it when the user explicitly asks. Installing the CA is terminal-only (pano ca install).
pano only runs while the user has it on: if a tool answers "pano is off", ask the user to run pano on in a terminal, then retry — you cannot start it yourself.
Flow ids are short strings like "f8k2q"; every result ends with a next: hint.`

// Server wraps the MCP server with its dependencies.
type Server struct {
	c   *client.Client
	log *slog.Logger
	mcp *mcp.Server
}

// New builds the MCP server. version is pano's version string.
func New(c *client.Client, version string, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(nilWriter{}, nil))
	}
	s := &Server{c: c, log: logger}
	s.mcp = mcp.NewServer(&mcp.Implementation{Name: "pano", Title: "pano — all-seeing HTTPS proxy", Version: version}, &mcp.ServerOptions{
		Instructions: Instructions,
		Logger:       logger,
	})
	s.registerTools()
	s.registerResources()
	s.registerPrompts()
	return s
}

// MCP returns the underlying server.
func (s *Server) MCP() *mcp.Server { return s.mcp }

// ServeStdio runs over stdin/stdout until ctx is done.
func (s *Server) ServeStdio(ctx context.Context) error {
	return s.mcp.Run(ctx, &mcp.StdioTransport{})
}

// HTTPHandler returns a Streamable HTTP handler (stateless) for mounting on
// the daemon's loopback port.
func (s *Server) HTTPHandler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, &mcp.StreamableHTTPOptions{
		Stateless: true,
	})
}

type nilWriter struct{}

func (nilWriter) Write(p []byte) (int, error) { return len(p), nil }

// --- result helpers ---

func text(parts ...string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Join(parts, "\n")}}}
}

func withNext(body, next string) *mcp.CallToolResult {
	if next == "" {
		return text(body)
	}
	return text(strings.TrimRight(body, "\n"), "next: "+next)
}

// offMsg is what every tool and resource says while the daemon is not
// running. The MCP server deliberately never starts the daemon: pano is only
// on between `pano on`/`pano start` and `pano off`/`pano stop`.
const offMsg = "pano is off: the daemon is not running. Ask the user to run `pano on` (or `pano start`) in a terminal, then retry."

// describe turns a client error into the text an agent sees.
func describe(err error) string {
	if errors.Is(err, client.ErrNotRunning) {
		return offMsg
	}
	return err.Error()
}

func errResult(err error, hint string) (*mcp.CallToolResult, any, error) {
	msg := describe(err)
	if hint != "" && !errors.Is(err, client.ErrNotRunning) {
		msg += "\nnext: " + hint
	}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}, nil, nil
}

func ok(r *mcp.CallToolResult) (*mcp.CallToolResult, any, error) { return r, nil, nil }

func boolp(b bool) *bool { return &b }

func readOnly(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, OpenWorldHint: boolp(false)}
}

func mutating(title string, idempotent bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, DestructiveHint: boolp(false), IdempotentHint: idempotent, OpenWorldHint: boolp(false)}
}

func meta(kv ...any) mcp.Meta {
	m := mcp.Meta{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[fmt.Sprint(kv[i])] = kv[i+1]
	}
	return m
}
