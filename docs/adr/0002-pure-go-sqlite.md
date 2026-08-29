# ADR 0002: Pure-Go SQLite (modernc.org/sqlite) with write-behind ingestion

Status: superseded by [ADR 0011](0011-in-memory-only.md) (2026-08-30) — pano no longer persists anything; this ADR is kept for the record.

## Context

pano must persist flows across daemon restarts, answer full-text queries
("what did my app send that mentioned `rate limit`?") and bound disk usage,
while never slowing the proxy down. Requirements that shaped the choice:

- Single static binary, `CGO_ENABLED=0`, cross-compiled for darwin and
  linux from one runner (`.goreleaser.yaml`). `mattn/go-sqlite3` needs cgo
  and a C toolchain per target.
- Full-text search and JSON helpers. `modernc.org/sqlite` v1.57 is a
  cgo-free transpilation of SQLite that ships FTS5 and JSON1 and is verified
  on darwin/arm64.
- Ingestion is bursty (a page load is hundreds of flows in a second) and a
  slow disk must not add latency to proxied requests.

Alternatives considered: BoltDB/Badger (no SQL, no FTS), Postgres or another
daemon (nothing to install is a hard requirement), plain JSONL files (no
search, no retention without rewriting).

## Decision

- Use `modernc.org/sqlite` via `database/sql`, WAL mode, one writer
  connection with `_txlock=immediate` and a small read-only pool.
- Ingest **write-behind**: the store subscribes to the event bus, queues
  items on a bounded channel (8192) and a single writer goroutine commits
  batches of ≤ 256 items / 50 ms in one transaction. `Enqueue` never blocks;
  a full queue drops the item and increments a counter that `pano status`
  reports.
- Bodies are content-addressed blobs (sha256) with trigger-maintained
  reference counts; decoded text is cached in `blob_text`; a contentless FTS5
  table indexes host, path, headers and ≤ 256 KiB of decoded text per body.
- Reads serve from the in-memory ring first and fall back to SQLite for
  `q=`, other sessions, or misses.
- Retention (`max_age`, `max_flows`, `max_db_bytes`) runs every minute in
  chunked deletes followed by `incremental_vacuum` and a WAL checkpoint.
- Schema changes are embedded SQL migrations tracked in `schema_version`.

## Consequences

- `go install github.com/orron/pano/cmd/pano@latest` works on a machine
  without a C compiler; releases are reproducible with `-trimpath`.
- modernc SQLite is slower than the C library for heavy write loads; the
  drop policy makes that a capture-completeness problem rather than a proxy
  latency problem, and the counter makes it visible.
- Because reads observe committed batches, a flow may be visible in
  `pano flows` (ring) a few tens of milliseconds before `--q` can find it.
- The blob refcount triggers and contentless FTS make deletes cheap but mean
  the FTS index cannot be rebuilt from itself; a rebuild re-reads
  `blob_text`.
