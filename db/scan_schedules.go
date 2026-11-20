package db

import (
	"database/sql"
	"time"
)

// ScanSchedule is one recurring-scan definition: every interval_sec, the server
// scheduler auto-creates a pentest task from (description, goal, llm_profile_id,
// timeout_seconds). Fires are claimed atomically so a restart or a second process
// does not double-fire a schedule inside one interval (see ClaimDueScanSchedules).
type ScanSchedule struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	Enabled        bool       `json:"enabled"`
	IntervalSec    int        `json:"interval_sec"`
	Description    string     `json:"description"`
	Goal           string     `json:"goal"`
	LLMProfileID   *int64     `json:"llm_profile_id,omitempty"`
	TimeoutSeconds int        `json:"timeout_seconds"`
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	LastTaskID     *int64     `json:"last_task_id,omitempty"`
	RunCount       int        `json:"run_count"`
	CreatedAt      time.Time  `json:"created_at"`
}

const scanScheduleCols = `id, name, enabled, interval_sec, description, goal, llm_profile_id, timeout_seconds, last_run_at, last_task_id, run_count, created_at`

func scanScanSchedule(sc interface{ Scan(...any) error }) (*ScanSchedule, error) {
	var s ScanSchedule
	var profileID, lastTask sql.NullInt64
	var lastRun sql.NullTime
	if err := sc.Scan(&s.ID, &s.Name, &s.Enabled, &s.IntervalSec, &s.Description, &s.Goal,
		&profileID, &s.TimeoutSeconds, &lastRun, &lastTask, &s.RunCount, &s.CreatedAt); err != nil {
		return nil, err
	}
	if profileID.Valid {
		s.LLMProfileID = &profileID.Int64
	}
	if lastTask.Valid {
		s.LastTaskID = &lastTask.Int64
	}
	if lastRun.Valid {
		s.LastRunAt = &lastRun.Time
	}
	return &s, nil
}

// ListScanSchedules returns all schedules, newest first.
func (d *DB) ListScanSchedules() ([]*ScanSchedule, error) {
	return d.queryScanSchedules(`SELECT ` + scanScheduleCols + ` FROM scan_schedules ORDER BY id DESC`)
}

func (d *DB) queryScanSchedules(q string, args ...any) ([]*ScanSchedule, error) {
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ScanSchedule{}
	for rows.Next() {
		s, err := scanScanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CreateScanSchedule inserts a schedule and returns it.
func (d *DB) CreateScanSchedule(s *ScanSchedule) (*ScanSchedule, error) {
	row := d.QueryRow(`
INSERT INTO scan_schedules(name, enabled, interval_sec, description, goal, llm_profile_id, timeout_seconds)
VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING `+scanScheduleCols,
		s.Name, s.Enabled, s.IntervalSec, s.Description, s.Goal, s.LLMProfileID, s.TimeoutSeconds)
	return scanScanSchedule(row)
}

// UpdateScanSchedule updates a schedule's editable fields (not run bookkeeping).
func (d *DB) UpdateScanSchedule(s *ScanSchedule) error {
	_, err := d.Exec(`UPDATE scan_schedules SET name=$2, enabled=$3, interval_sec=$4, description=$5, goal=$6, llm_profile_id=$7, timeout_seconds=$8 WHERE id=$1`,
		s.ID, s.Name, s.Enabled, s.IntervalSec, s.Description, s.Goal, s.LLMProfileID, s.TimeoutSeconds)
	return err
}

// DeleteScanSchedule removes a schedule.
func (d *DB) DeleteScanSchedule(id int64) error {
	_, err := d.Exec(`DELETE FROM scan_schedules WHERE id=$1`, id)
	return err
}

// ClaimDueScanSchedules atomically bumps last_run_at + run_count for every enabled
// schedule whose interval has elapsed and returns the claimed rows. Doing the
// due-check and the bump in one UPDATE ... RETURNING means two ticks (or two
// processes) can never both claim the same schedule for the same interval — the
// caller then creates one task per returned row. A task-creation failure after a
// claim simply loses that one cycle (last_run_at already advanced), which is safer
// than a tight retry loop.
func (d *DB) ClaimDueScanSchedules() ([]*ScanSchedule, error) {
	return d.queryScanSchedules(`
UPDATE scan_schedules
SET last_run_at = now(), run_count = run_count + 1
WHERE enabled AND interval_sec > 0
  AND (last_run_at IS NULL OR last_run_at <= now() - make_interval(secs => interval_sec))
RETURNING ` + scanScheduleCols)
}

// SetScanScheduleLastTask records the task id a fire created.
func (d *DB) SetScanScheduleLastTask(id, taskID int64) error {
	_, err := d.Exec(`UPDATE scan_schedules SET last_task_id=$2 WHERE id=$1`, id, taskID)
	return err
}
