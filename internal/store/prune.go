package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// pruneChunk bounds how many flows one retention transaction deletes so the
// writer is never blocked for long.
const pruneChunk = 2000

// PruneStats reports what one Prune pass removed.
type PruneStats struct {
	// Flows is the total number of flows deleted.
	Flows int64 `json:"flows"`
	// ByAge, ByCount and BySize break Flows down by the limit that caused it.
	ByAge   int64 `json:"by_age"`
	ByCount int64 `json:"by_count"`
	BySize  int64 `json:"by_size"`
	// Blobs is the number of unreferenced blobs deleted.
	Blobs int64 `json:"blobs"`
	// DBBytes is the main database file size after pruning.
	DBBytes int64 `json:"db_bytes"`
}

// Prune enforces retention: flows older than maxAge and flows beyond the
// newest maxFlows are deleted; while the database file exceeds maxDBBytes an
// extra 10% of the oldest flows go too (a few rounds at most). Unreferenced
// blobs are removed, free pages are returned with incremental_vacuum and the
// WAL is checkpointed. Zero limits are ignored.
func (s *SQLite) Prune(ctx context.Context, maxAge time.Duration, maxFlows int, maxDBBytes int64) (PruneStats, error) {
	var st PruneStats
	if maxAge > 0 {
		cutoff := time.Now().Add(-maxAge).UnixMicro()
		n, err := s.deleteChunked(ctx, "ts_start < ?", cutoff)
		if err != nil {
			return st, err
		}
		st.ByAge += n
	}
	if maxFlows > 0 {
		total, err := s.countFlows(ctx)
		if err != nil {
			return st, err
		}
		if excess := total - int64(maxFlows); excess > 0 {
			n, err := s.deleteOldest(ctx, excess)
			if err != nil {
				return st, err
			}
			st.ByCount += n
		}
	}
	if maxDBBytes > 0 {
		for round := 0; round < 8 && fileSize(s.opts.Path) > maxDBBytes; round++ {
			total, err := s.countFlows(ctx)
			if err != nil {
				return st, err
			}
			if total == 0 {
				break
			}
			n, err := s.deleteOldest(ctx, max(total/10, 1))
			if err != nil {
				return st, err
			}
			st.BySize += n
			blobs, err := s.deleteOrphanBlobs(ctx)
			if err != nil {
				return st, err
			}
			st.Blobs += blobs
			if err := s.vacuum(ctx); err != nil {
				return st, err
			}
			if n == 0 {
				break
			}
		}
	}
	st.Flows = st.ByAge + st.ByCount + st.BySize

	blobs, err := s.deleteOrphanBlobs(ctx)
	if err != nil {
		return st, err
	}
	st.Blobs += blobs
	if err := s.vacuum(ctx); err != nil {
		return st, err
	}
	st.DBBytes = fileSize(s.opts.Path)
	return st, nil
}

func (s *SQLite) deleteOrphanBlobs(ctx context.Context) (int64, error) {
	res, err := s.w.ExecContext(ctx, "DELETE FROM blobs WHERE refcount <= 0")
	if err != nil {
		return 0, fmt.Errorf("store: sqlite: prune blobs: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// StartPruner runs Prune every interval until ctx is done or the store is
// closed. Errors are logged.
func (s *SQLite) StartPruner(ctx context.Context, every, maxAge time.Duration, maxFlows int, maxDBBytes int64) {
	if every <= 0 {
		every = 10 * time.Minute
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.done:
				return
			case <-t.C:
			}
			st, err := s.Prune(ctx, maxAge, maxFlows, maxDBBytes)
			if err != nil {
				if ctx.Err() == nil {
					s.log.Warn("sqlite: prune failed", "err", err)
				}
				continue
			}
			if st.Flows > 0 || st.Blobs > 0 {
				s.log.Info("sqlite: pruned", "flows", st.Flows, "by_age", st.ByAge, "by_count", st.ByCount,
					"by_size", st.BySize, "blobs", st.Blobs, "db_bytes", st.DBBytes)
			}
		}
	}()
}

func (s *SQLite) countFlows(ctx context.Context) (int64, error) {
	var n int64
	if err := s.w.QueryRowContext(ctx, "SELECT COUNT(*) FROM flows").Scan(&n); err != nil {
		return 0, fmt.Errorf("store: sqlite: count flows: %w", err)
	}
	return n, nil
}

// deleteOldest removes the n lowest-id flows in chunks.
func (s *SQLite) deleteOldest(ctx context.Context, n int64) (int64, error) {
	var total int64
	for total < n {
		chunk := min(n-total, pruneChunk)
		deleted, err := s.deleteTx(ctx, "id IN (SELECT id FROM flows ORDER BY id LIMIT ?)", chunk)
		if err != nil {
			return total, err
		}
		total += deleted
		if deleted == 0 {
			break
		}
	}
	return total, nil
}

// deleteChunked removes flows matching cond in chunks until none remain.
func (s *SQLite) deleteChunked(ctx context.Context, cond string, args ...any) (int64, error) {
	var total int64
	for {
		chunkArgs := append(append([]any{}, args...), pruneChunk)
		deleted, err := s.deleteTx(ctx, "id IN (SELECT id FROM flows WHERE "+cond+" ORDER BY id LIMIT ?)", chunkArgs...)
		if err != nil {
			return total, err
		}
		total += deleted
		if deleted < pruneChunk {
			return total, nil
		}
	}
}

func (s *SQLite) deleteTx(ctx context.Context, cond string, args ...any) (int64, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: sqlite: prune: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	n, err := deleteFlowsWhere(ctx, tx, cond, args...)
	if err != nil {
		return 0, fmt.Errorf("store: sqlite: prune: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: sqlite: prune: %w", err)
	}
	return n, nil
}

// deleteFlowsWhere deletes flows matching cond together with their FTS rows
// and WebSocket messages. Blob refcounts are maintained by trigger.
func deleteFlowsWhere(ctx context.Context, tx *sql.Tx, cond string, args ...any) (int64, error) {
	// cond is always an internal constant fragment; values are bound.
	sub := "(SELECT id FROM flows WHERE " + cond + ")"
	if _, err := tx.ExecContext(ctx, "DELETE FROM flows_fts WHERE rowid IN "+sub, args...); err != nil { //nolint:gosec // see above
		return 0, fmt.Errorf("fts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM ws_messages WHERE flow_id IN "+sub, args...); err != nil { //nolint:gosec // see above
		return 0, fmt.Errorf("ws: %w", err)
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM flows WHERE id IN "+sub, args...) //nolint:gosec // see above
	if err != nil {
		return 0, fmt.Errorf("flows: %w", err)
	}
	return res.RowsAffected()
}

// vacuum returns free pages to the OS and checkpoints the WAL.
func (s *SQLite) vacuum(ctx context.Context) error {
	if _, err := s.w.ExecContext(ctx, "PRAGMA incremental_vacuum"); err != nil {
		return fmt.Errorf("store: sqlite: incremental_vacuum: %w", err)
	}
	if _, err := s.w.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		return fmt.Errorf("store: sqlite: checkpoint: %w", err)
	}
	return nil
}
