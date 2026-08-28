package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
)

func TestSessionsLifecycle(t *testing.T) {
	s := openTest(t, nil)
	ctx := context.Background()

	cur, err := s.CurrentSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Name != DefaultSessionName || !cur.Current || len(cur.ID) != 8 || cur.StartedAt.IsZero() {
		t.Fatalf("default session: %+v", cur)
	}
	again, _ := s.CurrentSession(ctx)
	if again.ID != cur.ID {
		t.Fatalf("CurrentSession created a second default: %+v", again)
	}

	now := time.Now()
	for i := 1; i <= 3; i++ {
		f := mkFlow(flow.ID(i), "h", "GET", "/", 200, now)
		f.Session = cur.ID
		s.Enqueue(done(f))
	}
	flush(t, s)

	sec, err := s.StartSession(ctx, "  second ")
	if err != nil {
		t.Fatal(err)
	}
	if sec.Name != "second" || !sec.Current || sec.ID == cur.ID {
		t.Fatalf("second: %+v", sec)
	}
	f := mkFlow(4, "h", "GET", "/", 200, now)
	f.Session = sec.ID
	s.Enqueue(done(f))
	flush(t, s)

	list, err := s.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("sessions: %+v", list)
	}
	byID := map[string]api.Session{}
	for _, ss := range list {
		byID[ss.ID] = ss
	}
	if d := byID[cur.ID]; d.Current || d.EndedAt.IsZero() || d.Flows != 3 {
		t.Fatalf("ended default: %+v", d)
	}
	if x := byID[sec.ID]; !x.Current || !x.EndedAt.IsZero() || x.Flows != 1 {
		t.Fatalf("current second: %+v", x)
	}
	if c, _ := s.CurrentSession(ctx); c.ID != sec.ID {
		t.Fatalf("CurrentSession = %+v", c)
	}

	// Empty name falls back to the default name.
	third, err := s.StartSession(ctx, "")
	if err != nil || third.Name != DefaultSessionName {
		t.Fatalf("StartSession(\"\") = %+v, %v", third, err)
	}

	// Deleting removes the session and its flows; blobs are pruned later.
	if err := s.DeleteSession(ctx, cur.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, cur.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
	for _, id := range []flow.ID{1, 2, 3} {
		if _, err := s.Get(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("flow %d survived DeleteSession: %v", id, err)
		}
	}
	if got, _ := s.Get(ctx, 4); got == nil || got.Session != sec.ID {
		t.Fatalf("flow 4 lost: %+v", got)
	}
	list, _ = s.Sessions(ctx)
	if len(list) != 2 {
		t.Fatalf("after delete: %+v", list)
	}

	// Deleting the current session: the next CurrentSession creates a default.
	if err := s.DeleteSession(ctx, third.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.CurrentSession(ctx)
	if err != nil || !fresh.Current || fresh.ID == third.ID || fresh.Name != DefaultSessionName {
		t.Fatalf("fresh current: %+v, %v", fresh, err)
	}
}

func TestSessionsSurviveReopen(t *testing.T) {
	s := openTest(t, nil)
	ctx := context.Background()
	ss, err := s.StartSession(ctx, "persisted")
	if err != nil {
		t.Fatal(err)
	}
	path := s.opts.Path
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenSQLite(SQLiteOptions{Path: path, Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	cur, err := s2.CurrentSession(ctx)
	if err != nil || cur.ID != ss.ID || cur.Name != "persisted" {
		t.Fatalf("after reopen: %+v, %v", cur, err)
	}
}
