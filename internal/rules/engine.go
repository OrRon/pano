package rules

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/config"
	"github.com/orron/pano/internal/flow"
)

// DefaultHoldTimeout is how long a breakpoint parks an exchange before it
// auto-continues when Options.HoldTimeout is zero.
const DefaultHoldTimeout = 120 * time.Second

// MaxBodyBytes bounds the body size the engine buffers to rewrite or hold an
// exchange. Larger bodies pass through untouched and the RuleHit says so.
const MaxBodyBytes = 8 << 20

var (
	// ErrNotFound is returned for unknown rule ids and held flow ids.
	ErrNotFound = errors.New("rules: not found")
	// ErrConflict is returned when adding a rule whose id already exists.
	ErrConflict = errors.New("rules: id already exists")
	// ErrInvalid is matched (errors.Is) by every validation failure.
	ErrInvalid = errors.New("rules: invalid")
)

// invalidError carries a descriptive validation message and matches ErrInvalid.
type invalidError struct{ msg string }

func (e *invalidError) Error() string        { return e.msg }
func (e *invalidError) Is(target error) bool { return target == ErrInvalid }

func invalidf(format string, args ...any) error {
	return &invalidError{msg: fmt.Sprintf(format, args...)}
}

// Options configure an Engine.
type Options struct {
	// PersistPath is the rules.json file written on every change and loaded
	// by New. Empty disables persistence.
	PersistPath string
	// HoldTimeout bounds how long a breakpoint parks an exchange before it
	// continues on its own. Zero means DefaultHoldTimeout.
	HoldTimeout time.Duration
	// Publish, if set, receives a flow.EvHeld event whenever a breakpoint
	// parks a flow (typically bus.Publish).
	Publish func(flow.Event)
	// Logger receives warnings about persistence and skipped rules. Nil means
	// slog.Default().
	Logger *slog.Logger
	// Now overrides the clock used for timestamps and expiry (tests).
	Now func() time.Time
}

// Engine holds the rule set and the breakpoint registry. It implements
// proxy.Hooks. Mutations take a mutex; the hot path is lock-free.
type Engine struct {
	persist     string
	holdTimeout time.Duration
	publish     func(flow.Event)
	log         *slog.Logger
	now         func() time.Time

	set     atomic.Pointer[ruleSet]
	version atomic.Uint64

	mu    sync.Mutex // guards rules and rebuilds of set
	rules map[string]*compiledRule

	saveMu sync.Mutex // serialises writes to persist

	heldMu sync.Mutex
	held   map[flow.ID]*heldEntry
}

// ruleSet is an immutable, sorted snapshot of the compiled rules.
type ruleSet struct {
	all  []*compiledRule
	req  []*compiledRule // rules with request-phase actions
	resp []*compiledRule // rules with response-phase actions
}

// New creates an engine, loading PersistPath when it exists. Rules whose
// expiry has passed are dropped at load; rules that no longer validate are
// skipped with a warning.
func New(opts Options) (*Engine, error) {
	e := &Engine{
		persist:     opts.PersistPath,
		holdTimeout: opts.HoldTimeout,
		publish:     opts.Publish,
		log:         opts.Logger,
		now:         opts.Now,
		rules:       make(map[string]*compiledRule),
		held:        make(map[flow.ID]*heldEntry),
	}
	if e.holdTimeout <= 0 {
		e.holdTimeout = DefaultHoldTimeout
	}
	if e.log == nil {
		e.log = slog.Default()
	}
	if e.now == nil {
		e.now = time.Now
	}
	if e.persist != "" {
		if err := e.load(); err != nil {
			return nil, err
		}
	}
	e.mu.Lock()
	e.rebuild()
	e.mu.Unlock()
	return e, nil
}

// Version is a counter that increases on every change to the rule set
// (including hit-limit disables and lazy expiry).
func (e *Engine) Version() uint64 { return e.version.Load() }

// List returns every rule, in evaluation order.
func (e *Engine) List() []api.Rule {
	rs := e.set.Load()
	out := make([]api.Rule, 0, len(rs.all))
	for _, cr := range rs.all {
		out = append(out, cr.view())
	}
	return out
}

// Get returns one rule.
func (e *Engine) Get(id string) (api.Rule, bool) {
	e.mu.Lock()
	cr, ok := e.rules[id]
	e.mu.Unlock()
	if !ok {
		return api.Rule{}, false
	}
	return cr.view(), true
}

// Add creates a rule from req.Rule or from req.Preset and req.Params. Name
// and TTLS on the request override the rule's own. The returned rule is the
// normalised form with ID, CreatedAt, Enabled and Hits filled.
func (e *Engine) Add(req api.RuleAddRequest) (api.Rule, error) {
	var spec api.Rule
	switch {
	case req.Rule != nil && req.Preset != "":
		return api.Rule{}, invalidf("rule: specify either rule or preset, not both")
	case req.Rule != nil:
		spec = cloneRule(*req.Rule)
	case req.Preset != "":
		var err error
		if spec, err = expandPreset(req.Preset, req.Params); err != nil {
			return api.Rule{}, err
		}
	default:
		return api.Rule{}, invalidf("rule: a rule or a preset is required")
	}
	if req.Name != "" {
		spec.Name = req.Name
	}
	if req.TTLS > 0 {
		spec.TTLSeconds = req.TTLS
		spec.Expires = time.Time{}
	}
	now := e.now()
	spec.CreatedAt = now
	spec.Hits = 0
	if err := resolveExpiry(&spec, now); err != nil {
		return api.Rule{}, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if spec.ID == "" {
		spec.ID = e.newIDLocked()
	} else {
		if !validID(spec.ID) {
			return api.Rule{}, invalidf("rule: id %q: use 1-64 letters, digits, '_', '-' or '.'", spec.ID)
		}
		if _, dup := e.rules[spec.ID]; dup {
			return api.Rule{}, fmt.Errorf("%w: %s", ErrConflict, spec.ID)
		}
	}
	cr, err := compileRule(spec)
	if err != nil {
		return api.Rule{}, err
	}
	e.rules[cr.id] = cr
	e.rebuild()
	e.saveLogged()
	return cr.view(), nil
}

// Update patches a rule. A non-nil p.Rule replaces the match, actions and
// limits wholesale (ID, CreatedAt and Hits are kept); p.Name and p.Enabled
// take precedence over the fields inside p.Rule. Re-enabling a rule that was
// disabled by MaxHits resets its hit counter.
func (e *Engine) Update(id string, p api.RulePatch) (api.Rule, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	old, ok := e.rules[id]
	if !ok {
		return api.Rule{}, fmt.Errorf("%w: rule %s", ErrNotFound, id)
	}
	now := e.now()
	spec := old.view()
	if p.Rule != nil {
		next := cloneRule(*p.Rule)
		next.ID, next.CreatedAt, next.Hits = id, old.createdAt, old.hits.Load()
		if next.Name == "" {
			next.Name = spec.Name
		}
		if next.Enabled == nil {
			next.Enabled = spec.Enabled
		}
		if next.TTLSeconds == 0 && next.Expires.IsZero() {
			next.TTLSeconds, next.Expires = old.ttl, old.expires
		} else if err := resolveExpiry(&next, now); err != nil {
			return api.Rule{}, err
		}
		spec = next
	}
	if p.Name != "" {
		spec.Name = p.Name
	}
	if p.Enabled != nil {
		spec.Enabled = p.Enabled
	}
	cr, err := compileRule(spec)
	if err != nil {
		return api.Rule{}, err
	}
	hits := old.hits.Load()
	if cr.enabled.Load() && !old.enabled.Load() && cr.maxHits > 0 && hits >= int64(cr.maxHits) {
		hits = 0
	}
	cr.hits.Store(hits)
	e.rules[id] = cr
	e.rebuild()
	e.saveLogged()
	return cr.view(), nil
}

// Remove deletes a rule.
func (e *Engine) Remove(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.rules[id]; !ok {
		return fmt.Errorf("%w: rule %s", ErrNotFound, id)
	}
	delete(e.rules, id)
	e.rebuild()
	e.saveLogged()
	return nil
}

// RemoveAll deletes every rule and returns how many were removed.
func (e *Engine) RemoveAll() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := len(e.rules)
	clear(e.rules)
	e.rebuild()
	e.saveLogged()
	return n
}

// Close releases every held exchange (they continue unmodified) and persists
// the final hit counters.
func (e *Engine) Close() error {
	e.releaseAll()
	return e.save()
}

// rebuild publishes a new sorted snapshot. Caller holds e.mu.
func (e *Engine) rebuild() {
	rs := &ruleSet{all: make([]*compiledRule, 0, len(e.rules))}
	for _, cr := range e.rules {
		rs.all = append(rs.all, cr)
	}
	slices.SortFunc(rs.all, func(a, b *compiledRule) int {
		if a.priority != b.priority {
			return b.priority - a.priority
		}
		if c := a.createdAt.Compare(b.createdAt); c != 0 {
			return c
		}
		return strings.Compare(a.id, b.id)
	})
	for _, cr := range rs.all {
		if len(cr.reqActions) > 0 {
			rs.req = append(rs.req, cr)
		}
		if len(cr.respActions) > 0 {
			rs.resp = append(rs.resp, cr)
		}
	}
	e.set.Store(rs)
	e.version.Add(1)
}

// disable is called from the hot path when a rule reaches MaxHits.
func (e *Engine) disable(cr *compiledRule) {
	if !cr.enabled.CompareAndSwap(true, false) {
		return
	}
	e.version.Add(1)
	if e.persist != "" {
		go e.saveLogged()
	}
}

// reap removes an expired rule; called from the hot path at most once per rule.
func (e *Engine) reap(cr *compiledRule) {
	if !cr.reaped.CompareAndSwap(false, true) {
		return
	}
	go func() {
		e.mu.Lock()
		if e.rules[cr.id] == cr {
			delete(e.rules, cr.id)
			e.rebuild()
		}
		e.mu.Unlock()
		e.saveLogged()
	}()
}

// persistence

func (e *Engine) load() error {
	b, err := os.ReadFile(e.persist)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("rules: read %s: %w", e.persist, err)
	}
	var specs []api.Rule
	if len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &specs); err != nil {
			return fmt.Errorf("rules: parse %s: %w", e.persist, err)
		}
	}
	now := e.now()
	for _, spec := range specs {
		if spec.ID == "" {
			e.log.Warn("rules: skipping persisted rule without id", "name", spec.Name)
			continue
		}
		if !spec.Expires.IsZero() && !spec.Expires.After(now) {
			continue
		}
		cr, err := compileRule(spec)
		if err != nil {
			e.log.Warn("rules: skipping persisted rule", "id", spec.ID, "err", err)
			continue
		}
		cr.hits.Store(spec.Hits)
		e.rules[cr.id] = cr
	}
	return nil
}

// save writes the current snapshot to PersistPath.
func (e *Engine) save() error {
	if e.persist == "" {
		return nil
	}
	e.saveMu.Lock()
	defer e.saveMu.Unlock()
	rs := e.set.Load()
	specs := make([]api.Rule, 0, len(rs.all))
	for _, cr := range rs.all {
		specs = append(specs, cr.view())
	}
	b, err := json.MarshalIndent(specs, "", "  ")
	if err != nil {
		return fmt.Errorf("rules: encode: %w", err)
	}
	if err := config.WriteAtomic(e.persist, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("rules: persist: %w", err)
	}
	return nil
}

func (e *Engine) saveLogged() {
	if err := e.save(); err != nil {
		e.log.Warn("rules: persist failed", "path", e.persist, "err", err)
	}
}

// ids

const idAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// randomID returns a short id such as "r_7k3q2".
func randomID() string {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("rules: crypto/rand: " + err.Error())
	}
	out := make([]byte, 0, 2+len(b))
	out = append(out, 'r', '_')
	for _, c := range b {
		out = append(out, idAlphabet[c&31])
	}
	return string(out)
}

func (e *Engine) newIDLocked() string {
	for {
		id := randomID()
		if _, taken := e.rules[id]; !taken {
			return id
		}
	}
}

func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-', c == '.':
		default:
			return false
		}
	}
	return true
}

// resolveExpiry turns TTLSeconds into Expires and rejects past expiries.
func resolveExpiry(spec *api.Rule, now time.Time) error {
	if spec.TTLSeconds < 0 {
		return invalidf("rule: ttl_s must be >= 0")
	}
	if spec.TTLSeconds > 0 {
		spec.Expires = now.Add(time.Duration(spec.TTLSeconds) * time.Second)
		return nil
	}
	if !spec.Expires.IsZero() && !spec.Expires.After(now) {
		return invalidf("rule: expires %s is in the past", spec.Expires.Format(time.RFC3339))
	}
	return nil
}
