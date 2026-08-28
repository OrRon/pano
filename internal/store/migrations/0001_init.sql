-- Schema version 1.
--
-- NOTE: PRAGMA auto_vacuum = INCREMENTAL is applied by OpenSQLite on the raw
-- connection *before* this file runs; the pragma only takes effect on an empty
-- database and is ignored inside a transaction, so it cannot live here.
--
-- Timestamps are unix microseconds. Headers, tags, rule hits and timing are
-- JSON text. Bodies live in content-addressed blobs (sha256 hex) with a
-- trigger-maintained reference count so retention can drop orphans.

CREATE TABLE sessions(
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  started_at INTEGER NOT NULL,
  ended_at   INTEGER,
  current    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE flows(
  id            INTEGER PRIMARY KEY,
  session       TEXT NOT NULL,
  kind          TEXT NOT NULL,
  state         TEXT NOT NULL,
  ts_start      INTEGER NOT NULL,
  ts_end        INTEGER,
  ttfb_us       INTEGER,
  total_us      INTEGER,
  client        TEXT,
  proto         TEXT,
  up_proto      TEXT,
  scheme        TEXT,
  host          TEXT NOT NULL,
  port          INTEGER,
  method        TEXT,
  path          TEXT,
  query         TEXT,
  status        INTEGER,
  req_headers   TEXT,
  resp_headers  TEXT,
  trailers      TEXT,
  req_blob      TEXT,
  req_size      INTEGER,
  req_captured  INTEGER,
  req_trunc     INTEGER,
  req_enc       TEXT,
  req_mime      TEXT,
  resp_blob     TEXT,
  resp_size     INTEGER,
  resp_captured INTEGER,
  resp_trunc    INTEGER,
  resp_enc      TEXT,
  resp_mime     TEXT,
  error         TEXT,
  tags          TEXT,
  rules         TEXT,
  replay        INTEGER,
  replay_of     INTEGER,
  timing        TEXT
);

CREATE INDEX flows_ts        ON flows(ts_start);
CREATE INDEX flows_host_ts   ON flows(host, ts_start);
CREATE INDEX flows_session   ON flows(session, ts_start);
CREATE INDEX flows_state     ON flows(state) WHERE state IN ('active', 'held');
-- Blob back-references: lets a late-arriving blob seed its refcount and keeps
-- orphan detection cheap.
CREATE INDEX flows_req_blob  ON flows(req_blob)  WHERE req_blob  IS NOT NULL;
CREATE INDEX flows_resp_blob ON flows(resp_blob) WHERE resp_blob IS NOT NULL;

CREATE TABLE blobs(
  hash       TEXT PRIMARY KEY,
  size       INTEGER NOT NULL,
  data       BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  refcount   INTEGER NOT NULL DEFAULT 0
);

-- Decoded (content-encoding removed) UTF-8 text of textual blobs.
CREATE TABLE blob_text(
  hash TEXT PRIMARY KEY REFERENCES blobs(hash) ON DELETE CASCADE,
  text TEXT NOT NULL
);

CREATE TABLE ws_messages(
  flow_id INTEGER NOT NULL,
  seq     INTEGER NOT NULL,
  ts      INTEGER NOT NULL,
  dir     TEXT NOT NULL,
  opcode  INTEGER NOT NULL,
  len     INTEGER NOT NULL,
  payload BLOB,
  masked  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(flow_id, seq)
);

-- Contentless FTS index keyed by flow id. contentless_delete=1 lets rows be
-- removed with a plain DELETE by rowid, without re-supplying the indexed text.
CREATE VIRTUAL TABLE flows_fts USING fts5(
  host, path, req_headers, resp_headers, req_text, resp_text,
  content='', contentless_delete=1, tokenize='unicode61'
);

-- Reference counting for blobs. A flow whose request and response share a
-- blob counts twice, symmetrically on insert, update and delete.
CREATE TRIGGER flows_ai AFTER INSERT ON flows BEGIN
  UPDATE blobs SET refcount = refcount + 1 WHERE hash = NEW.req_blob;
  UPDATE blobs SET refcount = refcount + 1 WHERE hash = NEW.resp_blob;
END;

CREATE TRIGGER flows_au AFTER UPDATE OF req_blob, resp_blob ON flows BEGIN
  UPDATE blobs SET refcount = refcount - 1 WHERE hash = OLD.req_blob  AND OLD.req_blob  IS NOT NEW.req_blob;
  UPDATE blobs SET refcount = refcount + 1 WHERE hash = NEW.req_blob  AND OLD.req_blob  IS NOT NEW.req_blob;
  UPDATE blobs SET refcount = refcount - 1 WHERE hash = OLD.resp_blob AND OLD.resp_blob IS NOT NEW.resp_blob;
  UPDATE blobs SET refcount = refcount + 1 WHERE hash = NEW.resp_blob AND OLD.resp_blob IS NOT NEW.resp_blob;
END;

CREATE TRIGGER flows_ad AFTER DELETE ON flows BEGIN
  UPDATE blobs SET refcount = refcount - 1 WHERE hash = OLD.req_blob;
  UPDATE blobs SET refcount = refcount - 1 WHERE hash = OLD.resp_blob;
END;
