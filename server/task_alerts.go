package server

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

// Task-completion alerts — a lifecycle notification that fires when a task
// reaches a terminal state (done/failed/timeout). This is intentionally separate
// from the finding-delivery pipeline: that pipeline is keyed on finding_id
// (deliveries.finding_id BIGINT NOT NULL) and persists, retries, and digests
// each row, none of which fits a finding-free event. A task-completion alert is
// instead a single best-effort direct send to each enabled channel, driven by an
// exact watermark so a task is alerted at most once.
const (
	// settingNotifyTaskComplete gates task-completion alerts. Off by default: it is
	// an opt-in heads-up distinct from finding alerts (which ship whenever a
	// channel is configured), so turning notifications on must not silently start
	// emitting a message for every task that finishes.
	settingNotifyTaskComplete = "notify_task_complete"
	// schedStateTaskCompleteWatermark stores the completed_at of the newest task
	// already alerted (RFC3339Nano). TasksCompletedSince uses it as a strict lower
	// bound, so completions are picked up exactly once and in order.
	schedStateTaskCompleteWatermark = "notify_task_complete_watermark"
)

const (
	// taskAlertBatchLimit caps how many completed tasks one tick processes, so a
	// burst of completions (or a long window with the feature freshly enabled) is
	// spread over several ticks instead of one long blocking loop.
	taskAlertBatchLimit = 20
	// taskAlertSendTimeout bounds one channel send. A notice is best-effort and
	// must not hold up the notifier's main delivery loop.
	taskAlertSendTimeout = 15 * time.Second
)

// stepTaskAlerts emits a task-completion notice to every enabled channel for
// each task that reached a terminal state since the stored watermark. It is
// called once per notifier tick, after the finding pipeline has been serviced.
//
// Delivery is best-effort: there is no persistent delivery row, retry, or
// digest. The watermark is advanced to the newest completion processed even when
// some sends fail, so a flapping channel cannot wedge the loop into re-sending
// the same batch forever. A crash before the watermark is written re-sends the
// batch next tick (at-least-once), which is acceptable for a lifecycle heads-up.
func (n *Notifier) stepTaskAlerts(ctx context.Context, channels []*db.NotificationChannel, baseURL string) {
	if !n.pg.GetBool(settingNotifyTaskComplete, false) {
		return
	}

	// Resolve the watermark. On first enable there is none: initialize it to now
	// and return, so the feature does not retroactively alert for every task that
	// has ever finished.
	raw, err := n.pg.GetSchedState(schedStateTaskCompleteWatermark)
	if err != nil {
		log.Printf("[notify] failed to read task-complete watermark: %v", err)
		return
	}
	if raw == "" {
		if err := n.pg.SetSchedState(schedStateTaskCompleteWatermark, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			log.Printf("[notify] failed to initialize task-complete watermark: %v", err)
		}
		return
	}
	since, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		// A corrupt watermark must not pin the feature at a bad instant forever;
		// reset to now and pick up from the next completion.
		log.Printf("[notify] invalid task-complete watermark %q, resetting to now: %v", raw, err)
		_ = n.pg.SetSchedState(schedStateTaskCompleteWatermark, time.Now().UTC().Format(time.RFC3339Nano))
		return
	}

	tasks, err := n.pg.TasksCompletedSince(since, taskAlertBatchLimit)
	if err != nil {
		log.Printf("[notify] failed to query completed tasks: %v", err)
		return
	}
	if len(tasks) == 0 {
		return
	}

	// Resolve the enabled channels once; a notice fans out to all of them.
	type target struct {
		channel notify.Channel
		cfg     map[string]any
		kind    string
	}
	var targets []target
	for _, ch := range channels {
		if !ch.IsEnabled() {
			continue
		}
		channel, cfg, ok := n.adapt(ch)
		if !ok {
			continue
		}
		targets = append(targets, target{channel: channel, cfg: cfg, kind: ch.Kind})
	}

	newest := since
	for _, t := range tasks {
		if t.CompletedAt.After(newest) {
			newest = t.CompletedAt
		}
		if len(targets) == 0 {
			// No deliverable channel: still advance the watermark (below) so these
			// completions are not re-queried every tick.
			continue
		}
		msg := taskCompleteMessage(t, baseURL)
		for _, tg := range targets {
			sendCtx, cancel := context.WithTimeout(ctx, taskAlertSendTimeout)
			if _, err := tg.channel.Send(sendCtx, tg.cfg, msg); err != nil {
				log.Printf("[notify] task-complete alert failed task=%d channel=%s: %v", t.TaskID, tg.kind, err)
			}
			cancel()
		}
	}

	// Advance the watermark to the newest completion processed. The strict ">"
	// comparison in TasksCompletedSince means the next tick resumes right after
	// this instant.
	if newest.After(since) {
		if err := n.pg.SetSchedState(schedStateTaskCompleteWatermark, newest.UTC().Format(time.RFC3339Nano)); err != nil {
			log.Printf("[notify] failed to advance task-complete watermark: %v", err)
		}
	}
}

// taskCompleteMessage builds the notice for one completed task: a headline with
// the outcome and task name, body lines summarizing the findings, and a backlink
// to the task detail page when a public base URL is configured.
func taskCompleteMessage(t *db.CompletedTaskAlert, baseURL string) notify.Message {
	name := t.Name
	if name == "" {
		name = fmt.Sprintf("Task #%d", t.TaskID)
	}
	outcome := taskOutcomeWord(t.Status)
	lines := []string{fmt.Sprintf("Outcome: %s", outcome)}
	if t.Total == 0 {
		lines = append(lines, "No findings were recorded during this scan.")
	} else {
		lines = append(lines, fmt.Sprintf("%d findings recorded.", t.Total))
		var sev []string
		if t.Critical > 0 {
			sev = append(sev, fmt.Sprintf("Critical %d", t.Critical))
		}
		if t.High > 0 {
			sev = append(sev, fmt.Sprintf("High %d", t.High))
		}
		if t.Medium > 0 {
			sev = append(sev, fmt.Sprintf("Medium %d", t.Medium))
		}
		if t.Low > 0 {
			sev = append(sev, fmt.Sprintf("Low %d", t.Low))
		}
		if len(sev) > 0 {
			lines = append(lines, strings.Join(sev, " · "))
		}
	}
	notice := &notify.Notice{
		Title: fmt.Sprintf("Scan %s · %s", outcome, name),
		Lines: lines,
	}
	if baseURL != "" {
		// The task detail route reads the task id from the `id` query parameter
		// (web/src/app/(main)/function/tasks/detail/page.tsx).
		notice.URL = fmt.Sprintf("%s/function/tasks/detail?id=%d", baseURL, t.TaskID)
	}
	return notify.Message{Notice: notice}
}

// taskOutcomeWord renders a terminal task status as a human-readable outcome.
func taskOutcomeWord(status string) string {
	switch status {
	case "done":
		return "completed"
	case "failed":
		return "failed"
	case "timeout":
		return "timed out"
	default:
		return status
	}
}
