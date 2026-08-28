package rules

import (
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/orron/pano/internal/api"
)

// phaseMask is the set of phases a rule or action runs in.
type phaseMask uint8

const (
	phaseRequest phaseMask = 1 << iota
	phaseResponse
	phaseBoth = phaseRequest | phaseResponse
)

func (p phaseMask) String() string {
	switch p {
	case phaseRequest:
		return "request"
	case phaseResponse:
		return "response"
	case phaseBoth:
		return "both"
	}
	return ""
}

func parsePhase(s string) (phaseMask, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return 0, nil
	case "request", "req":
		return phaseRequest, nil
	case "response", "resp":
		return phaseResponse, nil
	case "both":
		return phaseBoth, nil
	}
	return 0, invalidf("rule: match.phase: unknown %q (want request|response|both)", s)
}

// Action types accepted in api.Action.Type.
const (
	ActionDelay        = "delay"
	ActionSetHeader    = "set_header"
	ActionRemoveHeader = "remove_header"
	ActionSetQuery     = "set_query"
	ActionRewriteBody  = "rewrite_body"
	ActionMock         = "mock"
	ActionMockEveryN   = "mock_every_n"
	ActionBlock        = "block"
	ActionRedirect     = "redirect"
	ActionThrottle     = "throttle"
	ActionBreakpoint   = "breakpoint"
	ActionTag          = "tag"
)

// actionTypes lists the accepted types in documentation order.
var actionTypes = []string{
	ActionDelay, ActionSetHeader, ActionRemoveHeader, ActionSetQuery, ActionRewriteBody,
	ActionMock, ActionMockEveryN, ActionBlock, ActionRedirect, ActionThrottle, ActionBreakpoint, ActionTag,
}

// actionKind says which side an action defaults to (0 = wherever the rule is
// evaluated) and which sides it may be pinned to with On.
type actionKind struct{ def, allowed phaseMask }

var actionKinds = map[string]actionKind{
	ActionDelay:        {0, phaseBoth},
	ActionSetHeader:    {0, phaseBoth},
	ActionRemoveHeader: {0, phaseBoth},
	ActionSetQuery:     {phaseRequest, phaseRequest},
	ActionRewriteBody:  {0, phaseBoth},
	ActionMock:         {phaseRequest, phaseBoth},
	ActionMockEveryN:   {phaseRequest, phaseBoth},
	ActionBlock:        {phaseRequest, phaseBoth},
	ActionRedirect:     {phaseRequest, phaseRequest},
	ActionThrottle:     {phaseResponse, phaseResponse},
	ActionBreakpoint:   {0, phaseBoth},
	ActionTag:          {0, phaseBoth},
}

// compiledRule is the hot-path form of a rule. Everything except the atomics
// is immutable after compileRule.
type compiledRule struct {
	id        string
	name      string
	priority  int
	createdAt time.Time
	expires   time.Time
	ttl       int
	maxHits   int
	prob      float64
	phase     phaseMask
	match     api.Match // normalised, for view()

	host        string
	hostHasPort bool
	path        pathMatcher
	methods     []string
	scheme      string
	headers     []headerMatcher
	status      func(int) bool

	actions     []*action
	reqActions  []*action
	respActions []*action

	enabled atomic.Bool
	hits    atomic.Int64
	reaped  atomic.Bool
}

// action is a compiled api.Action.
type action struct {
	spec       api.Action // normalised
	typ        string
	side       phaseMask // pinned side, 0 = flexible
	on         phaseMask // phases it actually runs in
	headerName string
	re         *regexp.Regexp
	tmpl       *template.Template
	upstream   *url.URL
	every      int64
	counter    atomic.Int64
	bucket     *bucket
}

// view renders the rule as the wire type.
func (cr *compiledRule) view() api.Rule {
	en := cr.enabled.Load()
	acts := make([]api.Action, len(cr.actions))
	for i, a := range cr.actions {
		acts[i] = cloneAction(a.spec)
	}
	return api.Rule{
		ID: cr.id, Name: cr.name, Enabled: &en, Priority: cr.priority,
		Match: cloneMatch(cr.match), Actions: acts, Probability: cr.prob,
		MaxHits: cr.maxHits, TTLSeconds: cr.ttl, Expires: cr.expires,
		Hits: cr.hits.Load(), CreatedAt: cr.createdAt,
	}
}

func cloneRule(r api.Rule) api.Rule {
	r.Match = cloneMatch(r.Match)
	acts := make([]api.Action, len(r.Actions))
	for i, a := range r.Actions {
		acts[i] = cloneAction(a)
	}
	r.Actions = acts
	if r.Enabled != nil {
		en := *r.Enabled
		r.Enabled = &en
	}
	return r
}

func cloneMatch(m api.Match) api.Match {
	m.Method = slices.Clone(m.Method)
	m.Header = maps.Clone(m.Header)
	return m
}

func cloneAction(a api.Action) api.Action {
	a.JSONPatch = maps.Clone(a.JSONPatch)
	a.Headers = maps.Clone(a.Headers)
	a.Tags = slices.Clone(a.Tags)
	return a
}

// compileRule validates and compiles a rule. spec.ID must be set.
func compileRule(spec api.Rule) (*compiledRule, error) {
	if spec.ID == "" {
		return nil, invalidf("rule: id required")
	}
	if spec.Probability < 0 || spec.Probability > 1 {
		return nil, invalidf("rule: probability %g out of range [0,1]", spec.Probability)
	}
	if spec.MaxHits < 0 {
		return nil, invalidf("rule: max_hits must be >= 0")
	}
	if spec.TTLSeconds < 0 {
		return nil, invalidf("rule: ttl_s must be >= 0")
	}
	if len(spec.Actions) == 0 {
		return nil, invalidf("rule: at least one action is required")
	}
	cr := &compiledRule{
		id: spec.ID, name: spec.Name, priority: spec.Priority, createdAt: spec.CreatedAt,
		expires: spec.Expires, ttl: spec.TTLSeconds, maxHits: spec.MaxHits, prob: spec.Probability,
	}

	m := spec.Match
	m.Host = strings.ToLower(strings.TrimSpace(m.Host))
	if m.Host != "*" {
		cr.host = m.Host
		cr.hostHasPort = strings.Contains(m.Host, ":")
	}
	var err error
	m.Path = strings.TrimSpace(m.Path)
	if cr.path, err = compilePath(m.Path); err != nil {
		return nil, err
	}
	m.Method = nil
	for _, meth := range spec.Match.Method {
		if meth = strings.ToUpper(strings.TrimSpace(meth)); meth != "" {
			m.Method = append(m.Method, meth)
		}
	}
	cr.methods = m.Method
	m.Scheme = strings.ToLower(strings.TrimSpace(m.Scheme))
	if m.Scheme != "" && m.Scheme != "http" && m.Scheme != "https" {
		return nil, invalidf("rule: match.scheme: unknown %q (want http|https)", spec.Match.Scheme)
	}
	cr.scheme = m.Scheme
	if len(spec.Match.Header) > 0 {
		m.Header = make(map[string]string, len(spec.Match.Header))
		for name, pat := range spec.Match.Header {
			name = strings.TrimSpace(name)
			if name == "" {
				return nil, invalidf("rule: match.header: empty header name")
			}
			canon := http.CanonicalHeaderKey(name)
			m.Header[canon] = pat
			cr.headers = append(cr.headers, headerMatcher{name: canon, pattern: pat})
		}
		slices.SortFunc(cr.headers, func(a, b headerMatcher) int { return strings.Compare(a.name, b.name) })
	}
	m.Status = strings.TrimSpace(m.Status)
	if cr.status, err = statusMatcher(m.Status); err != nil {
		return nil, err
	}
	phase, err := parsePhase(m.Phase)
	if err != nil {
		return nil, err
	}

	var derived phaseMask
	cr.actions = make([]*action, len(spec.Actions))
	for i, a := range spec.Actions {
		act, err := compileAction(i, a)
		if err != nil {
			return nil, err
		}
		cr.actions[i] = act
		derived |= act.side
	}
	if phase == 0 {
		phase = derived
		if phase == 0 {
			phase = phaseRequest
			if m.Status != "" {
				phase = phaseResponse
			}
		}
	}
	if cr.status != nil && phase&phaseResponse == 0 {
		return nil, invalidf("rule: match.status %q needs phase response or both (got request)", m.Status)
	}
	for i, act := range cr.actions {
		on := act.side
		if on == 0 {
			on = phase
		}
		if on&phase == 0 {
			return nil, invalidf("rule: action[%d]: %s runs on the %s side but the rule phase is %s", i, act.typ, on, phase)
		}
		act.on = on & phase
		if act.on&phaseRequest != 0 {
			cr.reqActions = append(cr.reqActions, act)
		}
		if act.on&phaseResponse != 0 {
			cr.respActions = append(cr.respActions, act)
		}
	}
	m.Phase = phase.String()
	cr.phase = phase
	cr.match = m
	cr.enabled.Store(spec.Enabled == nil || *spec.Enabled)
	return cr, nil
}

// compileAction validates one action and normalises its spec.
func compileAction(i int, a api.Action) (*action, error) {
	typ := strings.ToLower(strings.TrimSpace(a.Type))
	if typ == "hold" {
		typ = ActionBreakpoint
	}
	kind, ok := actionKinds[typ]
	if !ok {
		return nil, invalidf("rule: action[%d]: unknown type %q (want %s)", i, a.Type, strings.Join(actionTypes, "|"))
	}
	a.Type = typ
	side := kind.def
	a.On = strings.ToLower(strings.TrimSpace(a.On))
	switch a.On {
	case "":
	case "request":
		side = phaseRequest
	case "response":
		side = phaseResponse
	default:
		return nil, invalidf("rule: action[%d]: on: unknown %q (want request|response)", i, a.On)
	}
	if a.On != "" && side&kind.allowed == 0 {
		return nil, invalidf("rule: action[%d]: %s cannot run on the %s side", i, typ, a.On)
	}
	act := &action{typ: typ, side: side}

	switch typ {
	case ActionDelay:
		if a.MS < 0 || a.JitterMS < 0 {
			return nil, invalidf("rule: action[%d]: delay: ms and jitter_ms must be >= 0", i)
		}
		if a.MS == 0 && a.JitterMS == 0 {
			return nil, invalidf("rule: action[%d]: delay: ms or jitter_ms required", i)
		}
	case ActionSetHeader, ActionRemoveHeader:
		a.Name = strings.TrimSpace(a.Name)
		if a.Name == "" {
			return nil, invalidf("rule: action[%d]: %s: name required", i, typ)
		}
		act.headerName = http.CanonicalHeaderKey(a.Name)
	case ActionSetQuery:
		if a.Name == "" {
			return nil, invalidf("rule: action[%d]: set_query: name required", i)
		}
	case ActionRewriteBody:
		n := 0
		if len(a.JSONPatch) > 0 {
			n++
		}
		if a.Regex != "" {
			n++
		}
		if a.Template != "" {
			n++
		}
		if n != 1 {
			return nil, invalidf("rule: action[%d]: rewrite_body: exactly one of json_patch, regex or template is required", i)
		}
		if a.Regex != "" {
			re, err := regexp.Compile(a.Regex)
			if err != nil {
				return nil, invalidf("rule: action[%d]: rewrite_body: bad regex: %v", i, err)
			}
			act.re = re
		}
		if a.Template != "" {
			t, err := template.New("body").Parse(a.Template)
			if err != nil {
				return nil, invalidf("rule: action[%d]: rewrite_body: bad template: %v", i, err)
			}
			act.tmpl = t
		}
	case ActionMock, ActionMockEveryN:
		if err := checkStatus(i, typ, a.Status); err != nil {
			return nil, err
		}
		if typ == ActionMockEveryN {
			n, err := strconv.Atoi(strings.TrimSpace(a.Value))
			if err != nil || n < 1 {
				return nil, invalidf("rule: action[%d]: mock_every_n: value must be a positive integer (fire on every nth hit)", i)
			}
			act.every = int64(n)
			a.Value = strconv.Itoa(n)
		}
	case ActionBlock:
		a.Mode = strings.ToLower(strings.TrimSpace(a.Mode))
		switch a.Mode {
		case "":
			a.Mode = "status"
		case "status", "reset", "timeout":
		default:
			return nil, invalidf("rule: action[%d]: block: unknown mode %q (want reset|timeout|status)", i, a.Mode)
		}
		if a.MS < 0 {
			return nil, invalidf("rule: action[%d]: block: ms must be >= 0", i)
		}
		if err := checkStatus(i, typ, a.Status); err != nil {
			return nil, err
		}
	case ActionRedirect:
		u, err := url.Parse(strings.TrimSpace(a.Upstream))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, invalidf("rule: action[%d]: redirect: upstream must be an http(s) URL with a host, got %q", i, a.Upstream)
		}
		act.upstream = u
	case ActionThrottle:
		if a.KBps < 1 {
			return nil, invalidf("rule: action[%d]: throttle: kbps must be >= 1", i)
		}
		act.bucket = newBucket(float64(a.KBps)*1024, throttleChunk)
	case ActionBreakpoint:
	case ActionTag:
		var tags []string
		for _, t := range a.Tags {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
		if len(tags) == 0 {
			return nil, invalidf("rule: action[%d]: tag: tags required", i)
		}
		a.Tags = tags
	}
	act.spec = a
	return act, nil
}

func checkStatus(i int, typ string, s int) error {
	if s != 0 && (s < 100 || s > 599) {
		return invalidf("rule: action[%d]: %s: status %d out of range (100-599)", i, typ, s)
	}
	return nil
}
