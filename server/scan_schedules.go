package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// ---------- recurring scan schedules ----------

// minScanIntervalSec is the floor for a schedule's interval. The scheduler ticks
// every few seconds, so a very small interval would spawn tasks faster than they
// finish; 60s is low enough for testing yet safe against runaway task creation.
const minScanIntervalSec = 60

type scanScheduleReq struct {
	Name           string `json:"name"`
	Enabled        bool   `json:"enabled"`
	IntervalSec    int    `json:"interval_sec"`
	Description    string `json:"description"`
	Goal           string `json:"goal"`
	LLMProfileID   *int64 `json:"llm_profile_id"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// validateScanSchedule enforces the shared create/update rules and normalizes the
// request in place (trimming, clamping timeout). It returns a user-facing message
// on failure, empty on success.
func validateScanSchedule(req *scanScheduleReq) string {
	req.Name = strings.TrimSpace(req.Name)
	req.Description = strings.TrimSpace(req.Description)
	req.Goal = strings.TrimSpace(req.Goal)
	if req.Name == "" {
		return "schedule name is required"
	}
	if req.Description == "" {
		return "task description is required"
	}
	if req.IntervalSec < minScanIntervalSec {
		return "interval must be at least " + strconv.Itoa(minScanIntervalSec) + " seconds"
	}
	if req.TimeoutSeconds < 0 {
		req.TimeoutSeconds = 0
	}
	return ""
}

func (s *Server) pgListScanSchedules(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	list, err := pg.ListScanSchedules()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"schedules": list})
}

func (s *Server) pgCreateScanSchedule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req scanScheduleReq
	if err := decode(w, r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if msg := validateScanSchedule(&req); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	sched, err := pg.CreateScanSchedule(&db.ScanSchedule{
		Name: req.Name, Enabled: req.Enabled, IntervalSec: req.IntervalSec,
		Description: req.Description, Goal: req.Goal,
		LLMProfileID: req.LLMProfileID, TimeoutSeconds: req.TimeoutSeconds,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, sched)
}

func (s *Server) pgUpdateScanSchedule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "bad schedule id")
		return
	}
	var req scanScheduleReq
	if err := decode(w, r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if msg := validateScanSchedule(&req); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if err := pg.UpdateScanSchedule(&db.ScanSchedule{
		ID: id, Name: req.Name, Enabled: req.Enabled, IntervalSec: req.IntervalSec,
		Description: req.Description, Goal: req.Goal,
		LLMProfileID: req.LLMProfileID, TimeoutSeconds: req.TimeoutSeconds,
	}); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) pgDeleteScanSchedule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "bad schedule id")
		return
	}
	if err := pg.DeleteScanSchedule(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": id})
}

// pgRunScanSchedule fires a schedule immediately, creating a task now without
// waiting for its interval. It does not touch the schedule's last_run_at, so the
// next automatic fire still lands on the regular cadence.
func (s *Server) pgRunScanSchedule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "bad schedule id")
		return
	}
	list, err := pg.ListScanSchedules()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var sched *db.ScanSchedule
	for _, sc := range list {
		if sc.ID == id {
			sched = sc
			break
		}
	}
	if sched == nil {
		writeErr(w, 404, "schedule not found")
		return
	}
	var profileIDs []int64
	if sched.LLMProfileID != nil {
		profileIDs = []int64{*sched.LLMProfileID}
	}
	task, err := s.m.CreateTaskWithOptions(sched.Description, sched.Goal, db.TaskCreateOptions{
		Name: sched.Name, LLMProfileIDs: profileIDs, TimeoutSeconds: sched.TimeoutSeconds,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"task_id": task.ID})
}
