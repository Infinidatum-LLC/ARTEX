package server

import (
	"log"
	"strconv"

	"github.com/Autumn-27/artex/db"
)

// fireScanSchedules claims every due recurring-scan schedule and creates one
// pentest task per claim. It runs on the Scheduler's tick (see Scheduler.Run),
// independently of the P3 agent triggers — a schedule fires even when no agent
// trigger is configured. Claiming is atomic in the database, so a restart or a
// second process never double-fires a schedule inside one interval.
func (sc *Scheduler) fireScanSchedules() {
	due, err := sc.pg.ClaimDueScanSchedules()
	if err != nil {
		log.Printf("[scan-scheduler] failed to claim due schedules: %v", err)
		return
	}
	for _, s := range due {
		var profileIDs []int64
		if s.LLMProfileID != nil {
			profileIDs = []int64{*s.LLMProfileID}
		}
		task, err := sc.s.m.CreateTaskWithOptions(s.Description, s.Goal, db.TaskCreateOptions{
			Name:           s.Name,
			LLMProfileIDs:  profileIDs,
			TimeoutSeconds: s.TimeoutSeconds,
		})
		if err != nil {
			// The claim already advanced last_run_at, so this schedule simply skips
			// this cycle rather than retrying in a tight loop.
			log.Printf("[scan-scheduler] schedule %d (%q) failed to create task: %v", s.ID, s.Name, err)
			continue
		}
		taskID, _ := strconv.ParseInt(task.ID, 10, 64)
		if taskID > 0 {
			if err := sc.pg.SetScanScheduleLastTask(s.ID, taskID); err != nil {
				log.Printf("[scan-scheduler] schedule %d: failed to record task id: %v", s.ID, err)
			}
		}
		log.Printf("[scan-scheduler] schedule %d (%q) created task %s", s.ID, s.Name, task.ID)
	}
}
