package rules

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/orron/pano/internal/flow"
	"github.com/orron/pano/internal/proxy"
)

// Request implements proxy.Hooks. It runs the request-phase actions of every
// matching rule in order and stops at the first mock or block.
func (e *Engine) Request(ctx context.Context, f *flow.Flow, r *http.Request) proxy.Decision {
	rs := e.set.Load()
	if len(rs.req) == 0 {
		return proxy.Decision{}
	}
	now := e.now()
	for _, cr := range rs.req {
		if !e.eligible(cr, now) || !cr.matches(phaseRequest, f, r, 0) || !e.fire(cr) {
			continue
		}
		if d, stop := e.applyRequest(ctx, cr, f, r); stop {
			return d
		}
	}
	return proxy.Decision{}
}

// Response implements proxy.Hooks for the response phase.
func (e *Engine) Response(ctx context.Context, f *flow.Flow, r *http.Request, resp *http.Response) proxy.Decision {
	rs := e.set.Load()
	if len(rs.resp) == 0 {
		return proxy.Decision{}
	}
	now := e.now()
	for _, cr := range rs.resp {
		if !e.eligible(cr, now) || !cr.matches(phaseResponse, f, r, resp.StatusCode) || !e.fire(cr) {
			continue
		}
		if d, stop := e.applyResponse(ctx, cr, f, r, resp); stop {
			return d
		}
	}
	return proxy.Decision{}
}

// eligible checks enabled state and expiry (expired rules are reaped lazily).
func (e *Engine) eligible(cr *compiledRule, now time.Time) bool {
	if !cr.enabled.Load() {
		return false
	}
	if !cr.expires.IsZero() && !now.Before(cr.expires) {
		e.reap(cr)
		return false
	}
	return true
}

// fire applies the probability gate and hit accounting. It returns false when
// the rule should be skipped this time.
func (e *Engine) fire(cr *compiledRule) bool {
	if p := cr.prob; p > 0 && p < 1 && rand.Float64() >= p {
		return false
	}
	n := cr.hits.Add(1)
	if cr.maxHits > 0 {
		if n > int64(cr.maxHits) {
			cr.hits.Add(-1)
			return false
		}
		if n == int64(cr.maxHits) {
			e.disable(cr)
		}
	}
	return true
}

func recordHit(f *flow.Flow, cr *compiledRule, ph phaseMask, action, note string) {
	f.Rules = append(f.Rules, flow.RuleHit{RuleID: cr.id, Name: cr.name, Phase: ph.String(), Action: action, Note: note})
}

func (e *Engine) applyRequest(ctx context.Context, cr *compiledRule, f *flow.Flow, r *http.Request) (proxy.Decision, bool) {
	const ph = phaseRequest
	side := requestSide(f, r)
	for _, a := range cr.reqActions {
		switch a.typ {
		case ActionDelay:
			recordHit(f, cr, ph, a.typ, e.delay(ctx, a))
		case ActionSetHeader:
			side.setHeader(a.headerName, a.spec.Value)
			recordHit(f, cr, ph, a.typ, a.headerName)
		case ActionRemoveHeader:
			side.delHeader(a.headerName)
			recordHit(f, cr, ph, a.typ, a.headerName)
		case ActionSetQuery:
			q := r.URL.Query()
			q.Set(a.spec.Name, a.spec.Value)
			r.URL.RawQuery = q.Encode()
			f.Query = r.URL.RawQuery
			recordHit(f, cr, ph, a.typ, a.spec.Name)
		case ActionRewriteBody:
			td := templateData{Host: f.Host, Path: r.URL.Path, Method: r.Method, header: r.Header}
			recordHit(f, cr, ph, a.typ, side.rewrite(a.transform(td)))
		case ActionMock:
			resp := a.mock(r) //nolint:bodyclose // handed to the proxy, which closes it
			recordHit(f, cr, ph, a.typ, strconv.Itoa(resp.StatusCode))
			return proxy.Decision{Mock: resp}, true
		case ActionMockEveryN:
			if a.tick() {
				resp := a.mock(r) //nolint:bodyclose // handed to the proxy, which closes it
				recordHit(f, cr, ph, a.typ, strconv.Itoa(resp.StatusCode))
				return proxy.Decision{Mock: resp}, true
			}
		case ActionBlock:
			d, note := a.block(cr.id, r)
			recordHit(f, cr, ph, a.typ, note)
			return d, true
		case ActionRedirect:
			recordHit(f, cr, ph, a.typ, a.redirect(f, r))
		case ActionBreakpoint:
			if d, stop := e.hold(ctx, cr, ph, f, r, nil); stop {
				return d, true
			}
		case ActionTag:
			recordHit(f, cr, ph, a.typ, addTags(f, a.spec.Tags))
		}
	}
	return proxy.Decision{}, false
}

func (e *Engine) applyResponse(ctx context.Context, cr *compiledRule, f *flow.Flow, r *http.Request, resp *http.Response) (proxy.Decision, bool) {
	const ph = phaseResponse
	side := responseSide(f, resp)
	for _, a := range cr.respActions {
		switch a.typ {
		case ActionDelay:
			recordHit(f, cr, ph, a.typ, e.delay(ctx, a))
		case ActionSetHeader:
			side.setHeader(a.headerName, a.spec.Value)
			recordHit(f, cr, ph, a.typ, a.headerName)
		case ActionRemoveHeader:
			side.delHeader(a.headerName)
			recordHit(f, cr, ph, a.typ, a.headerName)
		case ActionRewriteBody:
			td := templateData{Host: f.Host, Path: r.URL.Path, Method: r.Method, Status: resp.StatusCode, header: resp.Header}
			recordHit(f, cr, ph, a.typ, side.rewrite(a.transform(td)))
		case ActionMock:
			m := a.mock(r) //nolint:bodyclose // handed to the proxy, which closes it
			recordHit(f, cr, ph, a.typ, strconv.Itoa(m.StatusCode))
			return proxy.Decision{Mock: m}, true
		case ActionMockEveryN:
			if a.tick() {
				m := a.mock(r) //nolint:bodyclose // handed to the proxy, which closes it
				recordHit(f, cr, ph, a.typ, strconv.Itoa(m.StatusCode))
				return proxy.Decision{Mock: m}, true
			}
		case ActionBlock:
			d, note := a.block(cr.id, r)
			recordHit(f, cr, ph, a.typ, note)
			return d, true
		case ActionThrottle:
			resp.Body = newThrottledReader(ctx, resp.Body, a.bucket)
			recordHit(f, cr, ph, a.typ, strconv.Itoa(a.spec.KBps)+" KB/s")
		case ActionBreakpoint:
			if d, stop := e.hold(ctx, cr, ph, f, r, resp); stop {
				return d, true
			}
		case ActionTag:
			recordHit(f, cr, ph, a.typ, addTags(f, a.spec.Tags))
		}
	}
	return proxy.Decision{}, false
}

// delay sleeps for ms plus random jitter, returning early when ctx ends.
func (e *Engine) delay(ctx context.Context, a *action) string {
	d := time.Duration(a.spec.MS) * time.Millisecond
	if j := a.spec.JitterMS; j > 0 {
		d += time.Duration(rand.IntN(j+1)) * time.Millisecond
	}
	if d <= 0 {
		return "0ms"
	}
	start := time.Now()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return fmt.Sprintf("%dms", d/time.Millisecond)
	case <-ctx.Done():
		return fmt.Sprintf("cancelled after %dms", time.Since(start)/time.Millisecond)
	}
}

func (a *action) mock(r *http.Request) *http.Response {
	return buildMock(a.spec.Status, a.spec.Headers, a.spec.Body, r)
}

// tick advances the mock_every_n counter and reports whether this hit fires.
func (a *action) tick() bool { return a.counter.Add(1)%a.every == 0 }

func (a *action) block(ruleID string, r *http.Request) (proxy.Decision, string) {
	switch a.spec.Mode {
	case "reset":
		return proxy.Decision{Block: "reset"}, "reset"
	case "timeout":
		ms := a.spec.MS
		if ms == 0 {
			ms = 30000
		}
		return proxy.Decision{Block: "timeout", Deadline: time.Duration(ms) * time.Millisecond}, fmt.Sprintf("timeout %dms", ms)
	default:
		status := a.spec.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		body := a.spec.Body
		if body == "" {
			body = `{"error":"blocked by pano rule ` + ruleID + `"}`
		}
		return proxy.Decision{Mock: buildMock(status, a.spec.Headers, body, r)}, "status " + strconv.Itoa(status) //nolint:bodyclose // handed to the proxy, which closes it
	}
}

// redirect points the request at the upstream. The proxy re-derives the flow
// host and port from r.URL.Host after the request hook.
func (a *action) redirect(f *flow.Flow, r *http.Request) string {
	u := a.upstream
	from := r.URL.Host
	r.URL.Scheme = u.Scheme
	r.URL.Host = u.Host
	if p := strings.TrimSuffix(u.Path, "/"); p != "" {
		r.URL.Path = p + r.URL.Path
		r.URL.RawPath = ""
		f.Path = r.URL.Path
	}
	if !a.spec.PreserveHost {
		r.Host = u.Host
	}
	f.Scheme = u.Scheme
	return from + " -> " + u.Scheme + "://" + u.Host
}

func addTags(f *flow.Flow, tags []string) string {
	for _, t := range tags {
		if !slices.Contains(f.Tags, t) {
			f.Tags = append(f.Tags, t)
		}
	}
	return strings.Join(tags, ",")
}
