# ADR 0011: Captures live in memory only; every start is fresh

**Status:** accepted (2026-08-30). Supersedes [ADR 0002](0002-pure-go-sqlite.md).

## Context

Until now the daemon wrote every flow, body, WebSocket message and session
to `~/.pano/pano.db` through a write-behind SQLite writer with an FTS5
index and a retention pruner (ADR 0002). It bought history across restarts
at a real cost in behaviour nobody asked for:

- A database that grew to gigabytes in `~/.pano`, pruned on a schedule the
  user never saw, with vacuum and WAL-checkpoint pauses.
- Two answers to the same question: lists came from the ring unless the
  filter "needed the database", so `q=` and `session=` could return flows
  that `pano tail` never showed, with different search semantics (FTS5
  terms vs substrings).
- A drop counter, "store fell behind" warnings, orphaned `active` flows
  marked failed on the next open, migrations, a 1 000-line writer and its
  tests — all to keep a debugging tool's scrollback.
- Stale traffic from last week showing up next to today's, which for a
  proxy you switch on to look at *this* problem is noise, not history.

pano is turned on to inspect what is happening now. What matters is that
the last N exchanges are complete, searchable and fast to reach.

## Decision

- **Everything captured is held in memory and dies with the daemon.** The
  ring (`[capture] ring_size`, default 10 000 flows) is the only flow
  store; bodies sit in a 256 MiB content-addressed LRU; WebSocket messages
  in a per-flow log (newest 1 000 per flow, 64 MiB overall) released when
  the flow leaves the ring; sessions in a registry that starts with
  `default` current.
- **Every `pano on` starts empty.** IDs start from 1, there is no history to
  load, nothing to migrate and nothing to prune.
- **One read path.** Lists, searches, single-flow lookups, replay, HAR
  export and `tail` all scan the ring with the same `store.Matcher`. `q` is
  a case-insensitive substring match over URL, headers, error and up to
  256 KiB of decoded text per textual body, fetched lazily from the blob
  cache.
- **No flow store on disk, ever.** Rules, the CA, config and the audit log
  stay on disk as before; captures do not. `capture.persist` and
  `[retention]` are accepted and ignored with a warning; a leftover
  `pano.db` (+ `-wal`, `-shm`) is deleted when the daemon starts so the old
  database does not sit in `~/.pano` forever.
- `modernc.org/sqlite` and the migrations are gone from the module.

## Consequences

- Turn pano off and the captures are gone. `pano export har` is the way to
  keep anything; the FAQ says so.
- Memory is the budget: ring size × flow size plus 256 MiB of bodies plus
  64 MiB of WebSocket payloads, all configurable or fixed constants in
  `internal/daemon`. A flow whose body was evicted from the LRU still lists
  and still has headers; its body is reported as unavailable.
- `q` searches are a linear scan that may decode bodies; at 10 000 flows
  this is milliseconds, which is fine for a local tool and simpler than an
  index. If it ever is not, cache decoded text per blob — do not bring back
  a database.
- `api.Status` lost `persist` and `dropped`; `/v1/stats` reports
  `mem_*`, `blobs`, `blob_bytes`, `ws_*` and `sessions` instead of `db_*`.
- Binary is smaller and builds faster without the transpiled SQLite.
