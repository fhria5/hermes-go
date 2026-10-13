ALTER TABLE approvals ADD COLUMN canonical_args TEXT NOT NULL DEFAULT '';
ALTER TABLE approvals ADD COLUMN executed_at TEXT;
CREATE INDEX IF NOT EXISTS idx_approvals_task ON approvals(task_id);
