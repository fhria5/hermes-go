CREATE TABLE IF NOT EXISTS approvals (
  id TEXT PRIMARY KEY,
  task_id INTEGER REFERENCES tasks(id),
  session_id TEXT NOT NULL,
  tool TEXT NOT NULL,
  args_hash TEXT NOT NULL,
  args_preview TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('pending','approved','rejected','expired')),
  requested_at TEXT NOT NULL,
  decided_at TEXT,
  decided_by INTEGER,
  expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_approvals_status ON approvals(status, expires_at);

CREATE TABLE IF NOT EXISTS schedules (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  name TEXT NOT NULL UNIQUE,
  prompt TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('once','interval')),
  run_at TEXT,
  interval_seconds INTEGER,
  tz TEXT NOT NULL DEFAULT 'Asia/Jakarta',
  enabled INTEGER NOT NULL DEFAULT 1,
  next_run_at TEXT,
  last_run_at TEXT,
  last_result TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS schedule_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  schedule_id INTEGER REFERENCES schedules(id),
  task_id INTEGER,
  status TEXT,
  started_at TEXT NOT NULL,
  finished_at TEXT
);
