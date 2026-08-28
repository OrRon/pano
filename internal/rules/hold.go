package rules

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
	"github.com/orron/pano/internal/proxy"
)

// heldEntry is one exchange parked on a breakpoint.
type heldEntry struct {
	id     flow.ID
	phase  phaseMask
	ruleID string
	since  time.Time
	method string
	url    string
	status int
	resume chan api.ResumeRequest // buffered(1); Resume sends exactly once
}

// hold parks the exchange until Resume, client departure or HoldTimeout.
// stop is true when the exchange must be dropped.
func (e *Engine) hold(ctx context.Context, cr *compiledRule, ph phaseMask, f *flow.Flow, r *http.Request, resp *http.Response) (proxy.Decision, bool) {
	side := requestSide(f, r)
	if ph == phaseResponse {
		side = responseSide(f, resp)
	}
	raw, replay, readErr := slurp(*side.body)
	if readErr != nil {
		*side.body = replay
	} else {
		side.restore(raw)
	}

	ent := &heldEntry{
		id: f.ID, phase: ph, ruleID: cr.id, since: e.now(), method: r.Method, url: r.URL.String(),
		resume: make(chan api.ResumeRequest, 1),
	}
	if resp != nil {
		ent.status = resp.StatusCode
	}
	e.heldMu.Lock()
	e.held[ent.id] = ent
	e.heldMu.Unlock()

	f.State = flow.StateHeld
	if e.publish != nil {
		e.publish(flow.Event{Type: flow.EvHeld, TS: ent.since, Flow: f.Clone(), Phase: ph.String()})
	}

	timer := time.NewTimer(e.holdTimeout)
	defer timer.Stop()
	var req api.ResumeRequest
	outcome := "resume"
	select {
	case req = <-ent.resume:
	case <-ctx.Done():
		if e.unregister(ent) {
			outcome = "client gone"
		} else {
			req = <-ent.resume // Resume won the race; honour it
		}
	case <-timer.C:
		if e.unregister(ent) {
			outcome = "timeout"
		} else {
			req = <-ent.resume
		}
	}
	f.State = flow.StateActive

	switch outcome {
	case "client gone":
		recordHit(f, cr, ph, ActionBreakpoint, "dropped: client disconnected while held")
		return proxy.Decision{Block: "reset"}, true
	case "timeout":
		recordHit(f, cr, ph, ActionBreakpoint, "hold timeout")
		return proxy.Decision{}, false
	}
	if strings.EqualFold(req.Action, "drop") {
		recordHit(f, cr, ph, ActionBreakpoint, "dropped")
		return proxy.Decision{Block: "reset"}, true
	}
	recordHit(f, cr, ph, ActionBreakpoint, applyEdits(ph, f, r, resp, side, raw, readErr, req))
	return proxy.Decision{}, false
}

// unregister removes the entry and reports whether it was still registered
// (false means Resume already claimed it).
func (e *Engine) unregister(ent *heldEntry) bool {
	e.heldMu.Lock()
	defer e.heldMu.Unlock()
	if e.held[ent.id] != ent {
		return false
	}
	delete(e.held, ent.id)
	return true
}

// applyEdits applies a ResumeRequest to the parked exchange and returns a
// note listing what changed.
func applyEdits(ph phaseMask, f *flow.Flow, r *http.Request, resp *http.Response, side bodySide, raw []byte, readErr error, req api.ResumeRequest) string {
	var changes []string
	if ph == phaseRequest {
		if req.URL != "" {
			if u, err := url.Parse(req.URL); err == nil {
				if u.IsAbs() {
					r.URL = u
					r.Host = u.Host
					f.Scheme = u.Scheme
				} else {
					r.URL.Path, r.URL.RawPath, r.URL.RawQuery = u.Path, u.RawPath, u.RawQuery
				}
				f.Path, f.Query = r.URL.Path, r.URL.RawQuery
				changes = append(changes, "url")
			}
		}
		if req.Method != "" {
			r.Method = strings.ToUpper(req.Method)
			f.Method = r.Method
			changes = append(changes, "method")
		}
	} else if req.Status != 0 {
		resp.StatusCode = req.Status
		resp.Status = strconv.Itoa(req.Status) + " " + http.StatusText(req.Status)
		f.Status = req.Status
		changes = append(changes, "status")
	}
	for _, name := range req.RemoveHeaders {
		side.delHeader(name)
	}
	if len(req.RemoveHeaders) > 0 {
		changes = append(changes, "-"+strconv.Itoa(len(req.RemoveHeaders))+" headers")
	}
	for name, v := range req.SetHeaders {
		side.setHeader(name, v)
	}
	if len(req.SetHeaders) > 0 {
		changes = append(changes, "+"+strconv.Itoa(len(req.SetHeaders))+" headers")
	}
	if req.Body != nil || len(req.BodyPatch) > 0 {
		changes = append(changes, editBody(side, raw, readErr, req))
	}
	if len(changes) == 0 {
		return "resumed"
	}
	return "resumed with edits: " + strings.Join(changes, ", ")
}

func editBody(side bodySide, raw []byte, readErr error, req api.ResumeRequest) string {
	if readErr != nil {
		return "body not edited (" + readErr.Error() + ")"
	}
	enc := side.header.Get("Content-Encoding")
	plain, decoded, err := decodeBody(raw, enc)
	if err != nil && req.Body == nil {
		return "body not edited (" + err.Error() + ")"
	}
	if req.Body != nil {
		plain, decoded = []byte(*req.Body), enc != ""
	}
	if len(req.BodyPatch) > 0 {
		if plain, err = jsonPatch(plain, req.BodyPatch); err != nil {
			side.restore(raw)
			return "body not edited (" + err.Error() + ")"
		}
	}
	side.set(plain, decoded)
	return "body"
}

// Held lists the parked exchanges, oldest first.
func (e *Engine) Held() []api.Held {
	now := e.now()
	e.heldMu.Lock()
	out := make([]api.Held, 0, len(e.held))
	for _, ent := range e.held {
		out = append(out, api.Held{
			ID: ent.id, Short: ent.id.Short(), Phase: ent.phase.String(), Method: ent.method, URL: ent.url,
			Status: ent.status, Since: ent.since, Age: now.Sub(ent.since).Truncate(time.Millisecond).String(),
			RuleID: ent.ruleID,
		})
	}
	e.heldMu.Unlock()
	slices.SortFunc(out, func(a, b api.Held) int {
		if c := a.Since.Compare(b.Since); c != 0 {
			return c
		}
		return int(a.ID) - int(b.ID)
	})
	return out
}

// Resume releases a held exchange. Action "resume" (or empty) continues it
// with the given edits applied; "drop" resets the connection. Request-phase
// edits: URL, Method, SetHeaders, RemoveHeaders, Body, BodyPatch.
// Response-phase edits: Status, SetHeaders, RemoveHeaders, Body, BodyPatch.
func (e *Engine) Resume(id flow.ID, req api.ResumeRequest) error {
	req.Action = strings.ToLower(strings.TrimSpace(req.Action))
	switch req.Action {
	case "", "resume":
		req.Action = "resume"
	case "drop":
	default:
		return invalidf("resume: unknown action %q (want resume|drop)", req.Action)
	}
	if req.URL != "" {
		u, err := url.Parse(req.URL)
		if err != nil || (u.IsAbs() && u.Host == "") {
			return invalidf("resume: bad url %q", req.URL)
		}
	}
	if req.Status != 0 && (req.Status < 100 || req.Status > 599) {
		return invalidf("resume: status %d out of range (100-599)", req.Status)
	}
	e.heldMu.Lock()
	ent, ok := e.held[id]
	if ok {
		delete(e.held, id)
	}
	e.heldMu.Unlock()
	if !ok {
		return fmt.Errorf("%w: held flow %s", ErrNotFound, id.Short())
	}
	ent.resume <- req
	return nil
}

// releaseAll continues every held exchange unmodified.
func (e *Engine) releaseAll() {
	e.heldMu.Lock()
	ents := make([]*heldEntry, 0, len(e.held))
	for _, ent := range e.held {
		ents = append(ents, ent)
	}
	clear(e.held)
	e.heldMu.Unlock()
	for _, ent := range ents {
		ent.resume <- api.ResumeRequest{Action: "resume"}
	}
}
