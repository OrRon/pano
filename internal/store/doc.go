// Package store keeps captured flows for the lifetime of the daemon, entirely
// in memory: a bounded ring of flow snapshots (the hot path), a byte-budgeted
// content-addressed blob cache for bodies, a per-flow WebSocket message log,
// the session registry, and the filter matching used by every list/search
// endpoint. Nothing is written to disk; every daemon start begins empty.
package store
