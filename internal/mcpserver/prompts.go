package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerPrompts() {
	s.mcp.AddPrompt(&mcp.Prompt{
		Name:        "debug_failing_request",
		Description: "Root-cause a failing request captured by pano and propose a fix.",
		Arguments:   []*mcp.PromptArgument{{Name: "id", Description: "flow id of the failing request (from pano_flows)", Required: true}},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		id := req.Params.Arguments["id"]
		msg := fmt.Sprintf(`Debug the failing request with pano flow id %s.
1. Call pano_flow id=%s view=summary (both parts). If the host is an LLM API, call pano_flow_explain id=%s instead.
2. Find a similar request that succeeded: pano_flows host=<same host> path=<same path> status=2xx limit=5. If one exists, call pano_flow_diff a=<good id> b=%s part=both.
3. If the response body matters, use pano_flow with path= to fetch only the relevant field (e.g. error.message).
4. Explain the root cause in two sentences, then propose a concrete fix (code or config). If a retry would help, offer to pano_flow_replay with the corrected headers/body.`, id, id, id, id)
		return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: msg}}}}, nil
	})

	s.mcp.AddPrompt(&mcp.Prompt{
		Name:        "simulate_conditions",
		Description: "Reproduce a network scenario (slow, flaky, offline, rate-limited) against a host while the user exercises their app.",
		Arguments: []*mcp.PromptArgument{
			{Name: "host", Description: "host glob to affect, e.g. api.openai.com", Required: true},
			{Name: "scenario", Description: "slow | flaky | offline | timeout | rate_limited | hold", Required: true},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		host, scenario := req.Params.Arguments["host"], req.Params.Arguments["scenario"]
		msg := fmt.Sprintf(`Simulate the "%s" scenario for %s using pano rules.
1. Call pano_status to confirm capture is on and note whether the system proxy is enabled (if not, tell the user to run the app with `+"`pano run -- <cmd>`"+` or `+"`pano on`"+`).
2. Map the scenario to a preset: slow→slow_network{ms:3000,jitter_ms:1000}, flaky→fail_rate{rate:0.3,status:503}, offline→offline_host, timeout→timeout{after_ms:30000}, rate_limited→rate_limit{every_n:3,status:429,retry_after:2}, hold→hold{on:"request"}. Add it with pano_rule_add preset=… params={host:"%s",…} ttl_s=600 name="sim:%s".
3. Tell the user to exercise the app now, and call pano_tail since=now host=%s wait_ms=20000 in a loop to observe how it behaves (retries? backoff? errors surfaced?).
4. Summarise findings and any resilience bugs, then remove the rule with pano_rule_remove id=<id>.`, scenario, host, host, scenario, host)
		return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: msg}}}}, nil
	})
}
