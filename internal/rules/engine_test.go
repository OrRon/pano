package rules

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/orron/pano/internal/api"
)

func TestCRUDAndVersion(t *testing.T) {
	e := newEngine(t, Options{})
	v0 := e.Version()
	r1 := mustAdd(t, e, api.Rule{Name: "one", Match: api.Match{Host: "a.test"}, Actions: []api.Action{{Type: "delay", MS: 1}}})
	if e.Version() != v0+1 {
		t.Errorf("version %d want %d", e.Version(), v0+1)
	}
	if !regexp.MustCompile(`^r_[0-9a-z]{5}$`).MatchString(r1.ID) {
		t.Errorf("id %q", r1.ID)
	}
	if r1.Enabled == nil || !*r1.Enabled || r1.CreatedAt.IsZero() || r1.Hits != 0 || r1.Match.Phase != "request" {
		t.Errorf("rule %+v", r1)
	}
	r2 := mustAdd(t, e, api.Rule{ID: "custom-1", Name: "two", Priority: 5, Actions: []api.Action{{Type: "delay", MS: 1}}})
	if r2.ID != "custom-1" {
		t.Errorf("id %q", r2.ID)
	}
	if _, err := e.Add(api.RuleAddRequest{Rule: &api.Rule{ID: "custom-1", Actions: []api.Action{{Type: "delay", MS: 1}}}}); !errors.Is(err, ErrConflict) {
		t.Errorf("dup: %v", err)
	}
	if _, err := e.Add(api.RuleAddRequest{Rule: &api.Rule{ID: "bad id!", Actions: []api.Action{{Type: "delay", MS: 1}}}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad id: %v", err)
	}
	if _, err := e.Add(api.RuleAddRequest{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty add: %v", err)
	}
	list := e.List()
	if len(list) != 2 || list[0].ID != "custom-1" || list[1].ID != r1.ID {
		t.Errorf("order: %+v", list)
	}
	if got, ok := e.Get(r1.ID); !ok || got.Name != "one" {
		t.Errorf("get %+v %v", got, ok)
	}
	if _, ok := e.Get("nope"); ok {
		t.Error("get missing")
	}

	v := e.Version()
	off := false
	up, err := e.Update(r1.ID, api.RulePatch{Name: "uno", Enabled: &off})
	if err != nil || up.Name != "uno" || *up.Enabled || e.Version() != v+1 {
		t.Errorf("update %+v %v version %d", up, err, e.Version())
	}
	up, err = e.Update(r1.ID, api.RulePatch{Rule: &api.Rule{Match: api.Match{Host: "b.test"}, Actions: []api.Action{{Type: "tag", Tags: []string{"x"}}}}})
	if err != nil || up.Match.Host != "b.test" || up.Name != "uno" || *up.Enabled || up.ID != r1.ID || !up.CreatedAt.Equal(r1.CreatedAt) {
		t.Errorf("replace %+v %v", up, err)
	}
	if _, err := e.Update(r1.ID, api.RulePatch{Rule: &api.Rule{Actions: []api.Action{{Type: "nope"}}}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad update: %v", err)
	}
	if _, err := e.Update("missing", api.RulePatch{Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing: %v", err)
	}
	if err := e.Remove("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove missing: %v", err)
	}
	v = e.Version()
	if err := e.Remove(r1.ID); err != nil || e.Version() != v+1 {
		t.Errorf("remove: %v", err)
	}
	if n := e.RemoveAll(); n != 1 || len(e.List()) != 0 {
		t.Errorf("remove all %d", n)
	}
}

func TestAddNameAndTTLOverride(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := newEngine(t, Options{Now: func() time.Time { return now }})
	r, err := e.Add(api.RuleAddRequest{Rule: &api.Rule{Name: "x", TTLSeconds: 5, Actions: []api.Action{{Type: "delay", MS: 1}}}, Name: "y", TTLS: 60})
	if err != nil || r.Name != "y" || r.TTLSeconds != 60 || !r.Expires.Equal(now.Add(time.Minute)) {
		t.Errorf("%+v %v", r, err)
	}
	if _, err := e.Add(api.RuleAddRequest{Rule: &api.Rule{Expires: now.Add(-time.Second), Actions: []api.Action{{Type: "delay", MS: 1}}}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("past expiry: %v", err)
	}
}

func TestProbability(t *testing.T) {
	e := newEngine(t, Options{})
	mustAdd(t, e, api.Rule{Probability: 0.5, Actions: []api.Action{{Type: "tag", Tags: []string{"p"}}}})
	r := newReq(t, "GET", "https://api.test/", "", nil)
	hits := 0
	for range 400 {
		f := newFlow(r)
		e.Request(context.Background(), f, r)
		if len(f.Tags) == 1 {
			hits++
		}
	}
	if hits < 140 || hits > 260 {
		t.Errorf("hits %d out of 400 with p=0.5", hits)
	}
	if got := e.List()[0].Hits; got != int64(hits) {
		t.Errorf("counter %d want %d", got, hits)
	}
}

func TestMaxHits(t *testing.T) {
	e := newEngine(t, Options{})
	rule := mustAdd(t, e, api.Rule{MaxHits: 2, Actions: []api.Action{{Type: "tag", Tags: []string{"m"}}}})
	r := newReq(t, "GET", "https://api.test/", "", nil)
	v := e.Version()
	tagged := 0
	for range 5 {
		f := newFlow(r)
		e.Request(context.Background(), f, r)
		tagged += len(f.Tags)
	}
	got, _ := e.Get(rule.ID)
	if tagged != 2 || got.Hits != 2 || *got.Enabled {
		t.Errorf("tagged %d hits %d enabled %v", tagged, got.Hits, *got.Enabled)
	}
	if e.Version() != v+1 {
		t.Errorf("version not bumped on disable")
	}
	on := true
	if up, err := e.Update(rule.ID, api.RulePatch{Enabled: &on}); err != nil || up.Hits != 0 || !*up.Enabled {
		t.Errorf("re-enable %+v %v", up, err)
	}
	f := newFlow(r)
	e.Request(context.Background(), f, r)
	if len(f.Tags) != 1 {
		t.Error("rule did not fire after re-enable")
	}
}

func TestTTLExpiry(t *testing.T) {
	now := time.Now()
	e := newEngine(t, Options{Now: func() time.Time { return now }})
	rule := mustAdd(t, e, api.Rule{TTLSeconds: 10, Actions: []api.Action{{Type: "tag", Tags: []string{"t"}}}})
	r := newReq(t, "GET", "https://api.test/", "", nil)
	f := newFlow(r)
	e.Request(context.Background(), f, r)
	if len(f.Tags) != 1 {
		t.Fatal("rule should fire before expiry")
	}
	now = now.Add(11 * time.Second)
	f = newFlow(r)
	e.Request(context.Background(), f, r)
	if len(f.Tags) != 0 {
		t.Fatal("expired rule fired")
	}
	waitFor(t, "lazy removal", func() bool { _, ok := e.Get(rule.ID); return !ok })
	if len(e.List()) != 0 {
		t.Error("expired rule still listed")
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	now := time.Now()
	e := newEngine(t, Options{PersistPath: path, Now: func() time.Time { return now }})
	a := mustAdd(t, e, api.Rule{
		Name: "a", Priority: 2, Match: api.Match{Host: "*.test", Path: "/v1/", Method: []string{"post"}, Header: map[string]string{"x-k": "v*"}},
		Actions: []api.Action{{Type: "rewrite_body", JSONPatch: map[string]any{"a.b": 1.5}}, {Type: "mock", Status: 201, Headers: map[string]string{"X": "1"}, Body: "{}"}},
	})
	mustAdd(t, e, api.Rule{Name: "expired", TTLSeconds: 1, Actions: []api.Action{{Type: "delay", MS: 1}}})
	r := newReq(t, "POST", "https://api.test/v1/x", "{}", map[string]string{"X-K": "v1"})
	e.Request(context.Background(), newFlow(r), r)
	if err := e.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted []api.Rule
	if err := json.Unmarshal(raw, &persisted); err != nil || len(persisted) != 2 {
		t.Fatalf("persisted %d rules: %v\n%s", len(persisted), err, raw)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}

	now = now.Add(5 * time.Second)
	e2 := newEngine(t, Options{PersistPath: path, Now: func() time.Time { return now }})
	list := e2.List()
	if len(list) != 1 {
		t.Fatalf("loaded %d rules, want 1 (expired dropped)", len(list))
	}
	got := list[0]
	if got.ID != a.ID || got.Name != "a" || got.Hits != 1 || got.Priority != 2 || got.Match.Path != "/v1/" || got.Match.Method[0] != "POST" || got.Match.Header["X-K"] != "v*" {
		t.Errorf("loaded %+v", got)
	}
	if len(got.Actions) != 2 || got.Actions[1].Status != 201 || got.Actions[1].Headers["X"] != "1" || got.Actions[0].JSONPatch["a.b"] != 1.5 {
		t.Errorf("actions %+v", got.Actions)
	}
	if !got.CreatedAt.Equal(a.CreatedAt) {
		t.Errorf("created %v want %v", got.CreatedAt, a.CreatedAt)
	}
	if d := e2.Request(context.Background(), newFlow(r), r); d.Mock == nil || d.Mock.StatusCode != 201 {
		t.Errorf("loaded rule inactive: %+v", d)
	}

	// Corrupt file is an error; missing file is fine; invalid rule is skipped.
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{PersistPath: path}); err == nil {
		t.Error("want parse error")
	}
	if err := os.WriteFile(path, []byte(`[{"id":"x","actions":[{"type":"bogus"}]},{"id":"y","actions":[{"type":"delay","ms":1}]}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	e3 := newEngine(t, Options{PersistPath: path})
	if l := e3.List(); len(l) != 1 || l[0].ID != "y" {
		t.Errorf("loaded %+v", l)
	}
	e4 := newEngine(t, Options{PersistPath: filepath.Join(t.TempDir(), "missing.json")})
	if len(e4.List()) != 0 {
		t.Error("missing file")
	}
}

func TestPersistOnMaxHits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	e := newEngine(t, Options{PersistPath: path})
	rule := mustAdd(t, e, api.Rule{MaxHits: 1, Actions: []api.Action{{Type: "tag", Tags: []string{"m"}}}})
	r := newReq(t, "GET", "https://api.test/", "", nil)
	e.Request(context.Background(), newFlow(r), r)
	waitFor(t, "persisted disable", func() bool {
		raw, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		var rs []api.Rule
		return json.Unmarshal(raw, &rs) == nil && len(rs) == 1 && rs[0].ID == rule.ID && rs[0].Enabled != nil && !*rs[0].Enabled && rs[0].Hits == 1
	})
}

func TestPriorityAndOrder(t *testing.T) {
	e := newEngine(t, Options{})
	mustAdd(t, e, api.Rule{Name: "low", Priority: 1, Actions: []api.Action{{Type: "tag", Tags: []string{"low"}}}})
	mustAdd(t, e, api.Rule{Name: "high", Priority: 10, Actions: []api.Action{{Type: "tag", Tags: []string{"high"}}}})
	mustAdd(t, e, api.Rule{Name: "high-mock", Priority: 10, Actions: []api.Action{{Type: "mock", Body: "x"}}})
	r := newReq(t, "GET", "https://api.test/", "", nil)
	f := newFlow(r)
	d := e.Request(context.Background(), f, r)
	if d.Mock == nil || len(f.Tags) != 1 || f.Tags[0] != "high" {
		t.Errorf("tags %v mock %v", f.Tags, d.Mock != nil)
	}
	if l := e.List(); l[0].Name != "high" || l[1].Name != "high-mock" || l[2].Name != "low" {
		t.Errorf("order %v", l)
	}
}

func TestConcurrentChangesAndEvaluation(t *testing.T) {
	e := newEngine(t, Options{PersistPath: filepath.Join(t.TempDir(), "rules.json")})
	r := newReq(t, "GET", "https://api.test/", "", nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			id := mustAdd(t, e, api.Rule{Actions: []api.Action{{Type: "tag", Tags: []string{"c"}}}}).ID
			if i%2 == 0 {
				_ = e.Remove(id)
			}
		}
	}()
	for range 2000 {
		e.Request(context.Background(), newFlow(r), r)
		e.List()
	}
	<-done
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
}
