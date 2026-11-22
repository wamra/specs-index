CREATE TABLE IF NOT EXISTS meta (
  key TEXT PRIMARY KEY,
  value TEXT
);

CREATE TABLE IF NOT EXISTS sources (
  id INTEGER PRIMARY KEY,
  path TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS documents (
  id INTEGER PRIMARY KEY,
  path TEXT NOT NULL UNIQUE,
  type TEXT NOT NULL,
  version TEXT,
  sha256 TEXT,
  mtime INTEGER,
  size INTEGER,
  indexed_at TEXT,
  status TEXT,
  fail_reason TEXT
);

CREATE TABLE IF NOT EXISTS entities (
  id INTEGER PRIMARY KEY,
  doc_id INTEGER NOT NULL,
  key TEXT NOT NULL UNIQUE,
  type TEXT NOT NULL,
  title TEXT,
  body TEXT,
  status TEXT,
  priority TEXT,
  tags TEXT,
  start_line INTEGER,
  end_line INTEGER,
  body_sha256 TEXT
);
CREATE INDEX IF NOT EXISTS entities_doc ON entities(doc_id);

CREATE TABLE IF NOT EXISTS steps (
  id INTEGER PRIMARY KEY,
  entity_id INTEGER NOT NULL,
  seq INTEGER NOT NULL,
  keyword TEXT,
  text TEXT,
  line INTEGER
);
CREATE INDEX IF NOT EXISTS steps_entity ON steps(entity_id);

CREATE TABLE IF NOT EXISTS edges (
  id INTEGER PRIMARY KEY,
  src_key TEXT NOT NULL,
  dst_key TEXT NOT NULL,
  kind TEXT NOT NULL,
  doc_id INTEGER,
  line INTEGER,
  explicit INTEGER NOT NULL DEFAULT 1,
  confidence REAL,
  origin TEXT
);
CREATE INDEX IF NOT EXISTS edges_src ON edges(src_key, kind);
CREATE INDEX IF NOT EXISTS edges_dst ON edges(dst_key, kind);
CREATE INDEX IF NOT EXISTS edges_doc ON edges(doc_id);

CREATE TABLE IF NOT EXISTS chunks (
  id INTEGER PRIMARY KEY,
  entity_id INTEGER NOT NULL,
  text TEXT NOT NULL,
  text_sha256 TEXT,
  embed_fingerprint TEXT
);
CREATE INDEX IF NOT EXISTS chunks_entity ON chunks(entity_id);

CREATE TABLE IF NOT EXISTS findings (
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL,
  key TEXT NOT NULL,
  doc_id INTEGER,
  line INTEGER,
  detail TEXT
);

-- rowid = entities.id
CREATE VIRTUAL TABLE IF NOT EXISTS chunk_fts USING fts5(
  title, body, steps, tags,
  tokenize = 'unicode61'
);

CREATE VIRTUAL TABLE IF NOT EXISTS id_fts USING fts5(
  key, path,
  tokenize = 'trigram'
);

-- rowid = chunks.id; run the 'rebuild' command (flat, cosine) after loading vectors
CREATE VIRTUAL TABLE IF NOT EXISTS chunk_vec USING vec1(vector, type, status);

INSERT OR IGNORE INTO meta(key, value) VALUES ('schema_version', '1');
INSERT OR IGNORE INTO meta(key, value) VALUES ('contract_version', '1');
