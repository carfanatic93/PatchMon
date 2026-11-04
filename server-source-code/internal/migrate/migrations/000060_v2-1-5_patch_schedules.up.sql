-- Recurring patch schedules: a cron expression plus a target (all hosts,
-- host groups or individual hosts). A dispatcher job creates patch runs for
-- every targeted host that has pending updates when a schedule becomes due.
CREATE TABLE IF NOT EXISTS patch_schedules (
    id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    cron_expr TEXT NOT NULL,
    timezone TEXT NOT NULL DEFAULT 'UTC',
    enabled BOOLEAN NOT NULL DEFAULT true,
    target_type TEXT NOT NULL DEFAULT 'all',
    target_ids TEXT[] NOT NULL DEFAULT '{}',
    next_run_at TIMESTAMP(3),
    last_run_at TIMESTAMP(3),
    last_run_result JSONB,
    created_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT patch_schedules_target_type_check CHECK (target_type IN ('all', 'host_group', 'host'))
);

CREATE INDEX IF NOT EXISTS idx_patch_schedules_due ON patch_schedules (next_run_at) WHERE enabled = true;
