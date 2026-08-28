package rules

import (
	"context"
	"fmt"
	"testing"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
)

// nonMatchingEngine builds a 20-rule set none of which match api.other.com.
// withRegex swaps one rule in five for a regexp path rule.
func nonMatchingEngine(t testing.TB, withRegex bool) *Engine {
	t.Helper()
	e := newEngine(t, Options{})
	for i := range 20 {
		var m api.Match
		switch i % 5 {
		case 0:
			m = api.Match{Host: fmt.Sprintf("svc%d.example.com", i)}
		case 1:
			m = api.Match{Host: "*.example.com", Path: "/v1/"}
		case 2:
			m = api.Match{Path: fmt.Sprintf("/svc%d/", i), Method: []string{"POST"}}
			if withRegex {
				m.Path = fmt.Sprintf("/^\\/svc%d\\/.*$/", i)
			}
		case 3:
			m = api.Match{Header: map[string]string{"x-tenant": fmt.Sprintf("t%d*", i)}}
		case 4:
			m = api.Match{Host: "api.other.com", Path: "/v1/*/models", Scheme: "http"}
		}
		mustAdd(t, e, api.Rule{Match: m, Actions: []api.Action{{Type: "mock", Body: "x"}}})
		mustAdd(t, e, api.Rule{Match: api.Match{Host: fmt.Sprintf("r%d.example.com", i), Status: "5xx"}, Actions: []api.Action{{Type: "throttle", KBps: 1}}})
	}
	return e
}

func TestRequestNoMatchZeroAllocs(t *testing.T) {
	// regexp matching pools its scratch state; under the race detector
	// sync.Pool drops Puts at random, which shows up as allocations that do
	// not exist in a normal build (see raceEnabled).
	e := nonMatchingEngine(t, !raceEnabled)
	r := newReq(t, "GET", "https://api.other.com/v2/models", "", map[string]string{"X-Tenant": "zzz"})
	f := newFlow(r)
	resp := newResp(503, "", nil)
	ctx := context.Background()
	if allocs := testing.AllocsPerRun(1000, func() { e.Request(ctx, f, r) }); allocs != 0 {
		t.Errorf("Request: %v allocs/op, want 0", allocs)
	}
	if allocs := testing.AllocsPerRun(1000, func() { e.Response(ctx, f, r, resp) }); allocs != 0 {
		t.Errorf("Response: %v allocs/op, want 0", allocs)
	}
	if len(f.Rules) != 0 || f.State != flow.StateActive {
		t.Errorf("flow touched: %+v", f)
	}
}

func BenchmarkRequestNoMatch(b *testing.B) {
	e := nonMatchingEngine(b, true)
	r := newReq(b, "GET", "https://api.other.com/v2/models", "", map[string]string{"X-Tenant": "zzz"})
	f := newFlow(r)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		e.Request(ctx, f, r)
	}
}

func BenchmarkResponseNoMatch(b *testing.B) {
	e := nonMatchingEngine(b, true)
	r := newReq(b, "GET", "https://api.other.com/v2/models", "", nil)
	f := newFlow(r)
	resp := newResp(503, "", nil)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		e.Response(ctx, f, r, resp)
	}
}
