// Package store keeps captured flows: a bounded in-memory ring for the hot
// path, a content-addressed blob store for bodies, filter matching used by
// every list/search endpoint, and (in sqlite.go) write-behind persistence with
// full-text search.
package store
