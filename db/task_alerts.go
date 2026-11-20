package db

import "time"

// CompletedTaskAlert is one task that reached a terminal state, carrying the
// finding counts accumulated over its run. It is the payload of a
// task-completion notification — a lifecycle alert that is deliberately separate
// from the finding-delivery pipeline (which is keyed on finding_id and cannot
// carry a finding-free event).
type CompletedTaskAlert struct {
	TaskID      int64
	Name        string
	Status      string // done / failed / timeout
	CompletedAt time.Time
	// Finding counts over the whole task, for the alert body.
	Total    int
	Critical int
	High     int
	Medium   int
	Low      int
}

// TasksCompletedSince returns tasks that entered a terminal state (done/failed/
// timeout) strictly after `since`, oldest completion first, up to `limit`. Each
// row carries its finding counts via a LEFT JOIN so tasks that finished with no
// findings are still reported. Soft-deleted tasks are excluded.
//
// The strict `completed_at > since` comparison is what makes the watcher's
// watermark exact: the caller advances the watermark to the newest completed_at
// it has processed, and the next call picks up only tasks that finished after
// that instant — no task is alerted twice, and none is skipped.
func (d *DB) TasksCompletedSince(since time.Time, limit int) ([]*CompletedTaskAlert, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.Query(`
SELECT t.id, COALESCE(t.name, ''), t.status, t.completed_at,
  COUNT(f.id),
  COUNT(f.id) FILTER (WHERE f.severity='critical'),
  COUNT(f.id) FILTER (WHERE f.severity='high'),
  COUNT(f.id) FILTER (WHERE f.severity='medium'),
  COUNT(f.id) FILTER (WHERE f.severity='low')
FROM tasks t
LEFT JOIN findings f ON f.task_id = t.id
WHERE t.deleted_at IS NULL
  AND t.status IN ('done','failed','timeout')
  AND t.completed_at IS NOT NULL
  AND t.completed_at > $1
GROUP BY t.id, t.name, t.status, t.completed_at
ORDER BY t.completed_at ASC
LIMIT $2`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*CompletedTaskAlert{}
	for rows.Next() {
		var a CompletedTaskAlert
		if err := rows.Scan(&a.TaskID, &a.Name, &a.Status, &a.CompletedAt,
			&a.Total, &a.Critical, &a.High, &a.Medium, &a.Low); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}
