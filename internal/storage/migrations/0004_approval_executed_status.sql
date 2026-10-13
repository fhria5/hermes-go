-- Tambah status 'executing'/'executed' ke CHECK constraint (SQLite: rebuild tabel).
CREATE TABLE approvals_new (
  id TEXT PRIMARY KEY,
  task_id INTEGER REFERENCES tasks(id),
  session_id TEXT NOT NULL,
  tool TEXT NOT NULL,
  args_hash TEXT NOT NULL,
  args_preview TEXT NOT NULL,
  canonical_args TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK(status IN ('pending','approved','rejected','expired','executed')),
  requested_at TEXT NOT NULL,
  decided_at TEXT,
  decided_by INTEGER,
  expires_at TEXT NOT NULL,
  executed_at TEXT
);
INSERT INTO approvals_new(id, task_id, session_id, tool, args_hash, args_preview, canonical_args, status, requested_at, decided_at, decided_by, expires_at)
  SELECT id, task_id, session_id, tool, args_hash, args_preview, COALESCE(canonical_args,''), status, requested_at, decided_at, decided_by, expires_at FROM approvals;
DROP TABLE approvals;
ALTER TABLE approvals_new RENAME TO approvals;
CREATE INDEX IF NOT EXISTS idx_approvals_status ON approvals(status, expires_at);
CREATE INDEX IF NOT EXISTS idx_approvals_task ON approvals(task_id);
