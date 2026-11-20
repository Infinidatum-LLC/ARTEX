-- Recurring scan schedules: each row auto-creates a pentest task from a stored
-- description/goal at a fixed interval. The server's scheduler claims due rows
-- atomically (bumping last_run_at + run_count in one UPDATE ... RETURNING) so a
-- restart or a second process never double-fires a schedule within one interval.
CREATE TABLE IF NOT EXISTS scan_schedules (
    id              BIGSERIAL PRIMARY KEY,
    name            TEXT NOT NULL,
    enabled         BOOLEAN NOT NULL DEFAULT true,
    interval_sec    INTEGER NOT NULL,            -- recurrence period; must be > 0 to fire
    description     TEXT NOT NULL,               -- task description created on each fire
    goal            TEXT NOT NULL DEFAULT '',    -- task goal created on each fire
    llm_profile_id  BIGINT,                      -- optional LLM profile for the created task
    timeout_seconds INTEGER NOT NULL DEFAULT 0,  -- per-task wall-clock budget (0 = none)
    last_run_at     TIMESTAMPTZ,                 -- when the schedule last fired (NULL = never)
    last_task_id    BIGINT,                      -- task id created by the most recent fire
    run_count       INTEGER NOT NULL DEFAULT 0,  -- total fires
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The scheduler scans for enabled + due rows every tick; this index keeps that
-- claim query cheap as the schedule list grows.
CREATE INDEX IF NOT EXISTS scan_schedules_due_idx
    ON scan_schedules (enabled, last_run_at);
