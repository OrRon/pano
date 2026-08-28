package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orron/pano/internal/api"
)

// DefaultSessionName is the name of the session created on first use.
const DefaultSessionName = "default"

const sessionCols = "s.id, s.name, s.started_at, s.ended_at, s.current, (SELECT COUNT(*) FROM flows f WHERE f.session = s.id)"

// Sessions lists all sessions newest-first with their flow counts.
func (s *SQLite) Sessions(ctx context.Context) ([]api.Session, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT "+sessionCols+" FROM sessions s ORDER BY s.started_at DESC, s.id")
	if err != nil {
		return nil, fmt.Errorf("store: sqlite: sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []api.Session
	for rows.Next() {
		ss, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store: sqlite: sessions: %w", err)
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}

// CurrentSession returns the current session, creating DefaultSessionName
// when none exists.
func (s *SQLite) CurrentSession(ctx context.Context) (api.Session, error) {
	row := s.r.QueryRowContext(ctx, "SELECT "+sessionCols+" FROM sessions s WHERE s.current = 1 LIMIT 1")
	ss, err := scanSession(row)
	if err == nil {
		return ss, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return api.Session{}, fmt.Errorf("store: sqlite: current session: %w", err)
	}
	return s.StartSession(ctx, DefaultSessionName)
}

// StartSession ends the current session (if any) and creates a new current
// one with a short random id. An empty name becomes DefaultSessionName.
func (s *SQLite) StartSession(ctx context.Context, name string) (api.Session, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultSessionName
	}
	now := time.Now()
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return api.Session{}, fmt.Errorf("store: sqlite: start session: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	if _, err := tx.ExecContext(ctx, "UPDATE sessions SET current = 0, ended_at = COALESCE(ended_at, ?) WHERE current = 1",
		now.UnixMicro()); err != nil {
		return api.Session{}, fmt.Errorf("store: sqlite: end session: %w", err)
	}
	var id string
	for attempt := 0; ; attempt++ {
		id = newSessionID()
		res, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO sessions(id, name, started_at, current) VALUES(?, ?, ?, 1)",
			id, name, now.UnixMicro())
		if err != nil {
			return api.Session{}, fmt.Errorf("store: sqlite: create session: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			break
		}
		if attempt >= 8 {
			return api.Session{}, errors.New("store: sqlite: could not allocate a session id")
		}
	}
	if err := tx.Commit(); err != nil {
		return api.Session{}, fmt.Errorf("store: sqlite: start session: %w", err)
	}
	return api.Session{ID: id, Name: name, StartedAt: time.UnixMicro(now.UnixMicro()).UTC(), Current: true}, nil
}

// DeleteSession removes a session and all of its flows (and their FTS rows
// and WebSocket messages). Blobs left unreferenced are removed by Prune.
// Deleting the current session leaves no current session; CurrentSession
// will create a fresh default one.
func (s *SQLite) DeleteSession(ctx context.Context, id string) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: sqlite: delete session: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	res, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("store: sqlite: delete session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := deleteFlowsWhere(ctx, tx, "session = ?", id); err != nil {
		return fmt.Errorf("store: sqlite: delete session flows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: sqlite: delete session: %w", err)
	}
	return nil
}

func scanSession(sc scanner) (api.Session, error) {
	var (
		ss      api.Session
		started int64
		ended   sql.NullInt64
		current int64
	)
	if err := sc.Scan(&ss.ID, &ss.Name, &started, &ended, &current, &ss.Flows); err != nil {
		return api.Session{}, err
	}
	ss.StartedAt = time.UnixMicro(started).UTC()
	if ended.Valid {
		ss.EndedAt = time.UnixMicro(ended.Int64).UTC()
	}
	ss.Current = current != 0
	return ss, nil
}

// newSessionID returns 8 hex characters of randomness.
func newSessionID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms; fall back to the
		// clock so we still return something unique enough.
		binary.BigEndian.PutUint32(b[:], uint32(time.Now().UnixNano()&0xffffffff)) //nolint:gosec // masked
	}
	return hex.EncodeToString(b[:])
}
