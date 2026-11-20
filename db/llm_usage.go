package db

import (
	"sort"
	"strconv"
	"time"
)

// LLMUsage is one lightweight LLM-call metering row — the always-on usage ledger,
// distinct from llm_records (which stores full request/response bodies and is a
// gated debug feature). One row per completion call, written on both success and
// error, so token accounting is complete even for interrupted/failed runs. Carries
// only the dimensions needed to slice token spend (model / profile / task / agent),
// never any prompt or response content.
type LLMUsage struct {
	TaskID        string `json:"task_id"`        // task registry id (matches llm_records.task_id)
	ExplorationID int64  `json:"exploration_id"` // exploration id parsed from the session (0 = unknown/non-task)
	Worker        string `json:"worker"`         // agent lane: worker / planner / mainagent / goals
	Model         string `json:"model"`
	ProfileName   string `json:"profile_name"`
	LatencyMs     int    `json:"latency_ms"`
	InputTokens   int    `json:"input_tokens"`
	OutputTokens  int    `json:"output_tokens"`
	CacheRead     int    `json:"cache_read"`
	CacheWrite    int    `json:"cache_write"`
	Status        string `json:"status"` // ok | error
}

// EnsureLLMUsageTable creates the llm_usage metering table if it does not exist,
// by re-running the idempotent migration 0002_llm_usage.sql (see
// EnsureLLMRecordsTable for the locking rule).
func (d *DB) EnsureLLMUsageTable() error {
	return d.execEmbeddedMigration(llmUsageMigration)
}

// TaskLLMSpend is the token and turn total the in-memory task ledger would
// already have counted. One row is one turn. The ledger sees planner and worker
// completions, plus judge and compactor calls made on those contexts. Main-agent
// chat, goal decomposition, and side questions write llm_usage rows for the same
// task and never enter the ledger, so they are left out: counting them would
// tighten the turn cap after a restart. Cache read and write are not added:
// input_tokens already includes them. Every status counts, because a failed call
// is metered too.
func (d *DB) TaskLLMSpend(taskID string) (tokens, turns int64, err error) {
	if d == nil || taskID == "" {
		return 0, 0, nil
	}
	err = d.QueryRow(`SELECT COALESCE(SUM(input_tokens + output_tokens), 0), COUNT(*)
FROM llm_usage
WHERE task_id=$1 AND worker IN ('planner', 'worker', 'judge', 'compactor')`, taskID).Scan(&tokens, &turns)
	return tokens, turns, err
}

// InsertLLMUsage appends one metering row. Best-effort: callers log and continue on
// error (a lost metering row must never break the LLM call).
func (d *DB) InsertLLMUsage(u *LLMUsage) error {
	var expID any
	if u.ExplorationID > 0 {
		expID = u.ExplorationID
	}
	_, err := d.Exec(`
INSERT INTO llm_usage(task_id, exploration_id, worker, model, profile_name, latency_ms, input_tokens, output_tokens, cache_read, cache_write, status)
VALUES (NULLIF($1,''),$2,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),$6,$7,$8,$9,$10,$11)`,
		u.TaskID, expID, u.Worker, u.Model, u.ProfileName,
		u.LatencyMs, u.InputTokens, u.OutputTokens, u.CacheRead, u.CacheWrite, u.Status)
	return err
}

// TokenByModel aggregates a task's LLM token usage grouped by model, most-used
// first, from the always-on llm_usage ledger. taskID is the task registry id.
// Accurate even with per-agent model bindings, pool rotation/failover, and
// interrupted runs, since every call (success or error) is metered.
func (d *DB) TokenByModel(taskID string) ([]ModelTokenStat, error) {
	// Group by (model, profile) so each subgroup can be priced with its own
	// profile's rates, then merge up to one row per model. A model used through
	// two profiles with different prices is costed correctly and still shown once.
	rows, err := d.Query(`
SELECT COALESCE(NULLIF(model,''),'(unknown)') AS model, COALESCE(profile_name,'') AS profile_name,
       COUNT(*) AS calls,
       COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
       COALESCE(SUM(cache_read),0), COALESCE(SUM(cache_write),0)
FROM llm_usage
WHERE COALESCE(task_id,'') = $1
GROUP BY model, profile_name`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	prices, err := d.profilePriceMap()
	if err != nil {
		return nil, err
	}
	type acc struct {
		stat   ModelTokenStat
		cost   float64
		priced bool
	}
	byModel := map[string]*acc{}
	order := []string{}
	for rows.Next() {
		var model, profile string
		var calls, in, out, cr, cw int
		if err := rows.Scan(&model, &profile, &calls, &in, &out, &cr, &cw); err != nil {
			return nil, err
		}
		a := byModel[model]
		if a == nil {
			a = &acc{stat: ModelTokenStat{Model: model}}
			byModel[model] = a
			order = append(order, model)
		}
		a.stat.Calls += calls
		a.stat.InputTokens += in
		a.stat.OutputTokens += out
		a.stat.CacheReadTokens += cr
		a.stat.CacheWriteTokens += cw
		if pr, ok := prices[profile]; ok && !pr.zero() {
			a.cost += pr.cost(in, out, cr, cw)
			a.priced = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]ModelTokenStat, 0, len(order))
	for _, model := range order {
		a := byModel[model]
		if a.priced {
			c := a.cost
			a.stat.CostEur = &c
		}
		result = append(result, a.stat)
	}
	// Most-used model first (tokens), stable by name — matches the old ordering.
	sort.Slice(result, func(i, j int) bool {
		li := result[i].InputTokens + result[i].OutputTokens
		lj := result[j].InputTokens + result[j].OutputTokens
		if li != lj {
			return li > lj
		}
		return result[i].Model < result[j].Model
	})
	return result, nil
}

// ProfileUsage aggregates the whole ledger's token spend for one LLM profile
// (global, all tasks). Powers the dashboard's per-profile token card (new source).
type ProfileUsage struct {
	ProfileName      string `json:"profile_name"`
	Calls            int    `json:"calls"`
	Tasks            int    `json:"tasks"`
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
	// CostEur is set only when the profile carries at least one price; nil = not priced.
	CostEur *float64 `json:"cost_eur,omitempty"`
}

// UsageByProfile returns global token spend grouped by profile name, most-used
// first. profile_name may be empty for calls made on env/non-persisted configs.
func (d *DB) UsageByProfile() ([]ProfileUsage, error) {
	rows, err := d.Query(`
SELECT COALESCE(profile_name,'') AS profile_name, COUNT(*) AS calls,
       COUNT(DISTINCT task_id) AS tasks,
       COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
       COALESCE(SUM(cache_read),0), COALESCE(SUM(cache_write),0)
FROM llm_usage
GROUP BY profile_name
ORDER BY SUM(input_tokens) + SUM(output_tokens) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProfileUsage{}
	for rows.Next() {
		var p ProfileUsage
		if err := rows.Scan(&p.ProfileName, &p.Calls, &p.Tasks,
			&p.InputTokens, &p.OutputTokens, &p.CacheReadTokens, &p.CacheWriteTokens); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	archived, err := d.archivedTaskAggregates()
	if err != nil {
		return nil, err
	}
	byName := make(map[string]ProfileUsage, len(out))
	for _, current := range out {
		byName[current.ProfileName] = current
	}
	for _, aggregate := range archived {
		for _, cold := range aggregate.TokenProfiles {
			current := byName[cold.ProfileName]
			current.ProfileName = cold.ProfileName
			current.Calls += cold.Calls
			current.Tasks += cold.Tasks
			current.InputTokens += cold.InputTokens
			current.OutputTokens += cold.OutputTokens
			current.CacheReadTokens += cold.CacheReadTokens
			current.CacheWriteTokens += cold.CacheWriteTokens
			byName[cold.ProfileName] = current
		}
	}
	out = out[:0]
	for _, current := range byName {
		out = append(out, current)
	}
	if err := applyCostEur(d, out); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		left := out[i].InputTokens + out[i].OutputTokens
		right := out[j].InputTokens + out[j].OutputTokens
		if left != right {
			return left > right
		}
		return out[i].ProfileName < out[j].ProfileName
	})
	return out, nil
}

// ProfileDayUsage is one (profile, UTC calendar day) token bucket for the daily
// chart. Unlike the activity-based chart, ts is the real call time, so this is
// actual per-day consumption rather than tokens bucketed by task creation date.
type ProfileDayUsage struct {
	ProfileName     string `json:"profile_name"`
	Date            string `json:"date"` // YYYY-MM-DD (UTC)
	InputTokens     int    `json:"input_tokens"`
	OutputTokens    int    `json:"output_tokens"`
	CacheReadTokens int    `json:"cache_read_tokens"`
	// CostEur is the day's spend for this profile, set only when the profile
	// carries at least one price; nil = not priced. It is an approximation of the
	// authoritative per-profile total (UsageByProfile): the daily series does not
	// track cache_write separately, so cache-write tokens are charged at the input
	// price rather than their own. For a trend chart this is close enough; the
	// headline total still comes from UsageByProfile.
	CostEur *float64 `json:"cost_eur,omitempty"`
}

// UsageDaily returns per-(profile, day) token buckets for the past `days` days
// (default 365 when days<=0), so the dashboard can slice by profile + range.
func (d *DB) UsageDaily(days int) ([]ProfileDayUsage, error) {
	if days <= 0 {
		days = 365
	}
	rows, err := d.Query(`
SELECT COALESCE(profile_name,'') AS profile_name,
       to_char(ts AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS day,
       COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(cache_read),0)
FROM llm_usage
WHERE ts >= now() - ($1 * interval '1 day')
GROUP BY profile_name, day
ORDER BY day`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProfileDayUsage{}
	for rows.Next() {
		var p ProfileDayUsage
		if err := rows.Scan(&p.ProfileName, &p.Date, &p.InputTokens, &p.OutputTokens, &p.CacheReadTokens); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	archived, err := d.archivedTaskAggregates()
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
	byKey := make(map[string]ProfileDayUsage, len(out))
	for _, current := range out {
		byKey[current.ProfileName+"\x00"+current.Date] = current
	}
	for _, aggregate := range archived {
		for _, cold := range aggregate.TokenDaily {
			if cold.Date < cutoff {
				continue
			}
			key := cold.ProfileName + "\x00" + cold.Date
			current := byKey[key]
			current.ProfileName = cold.ProfileName
			current.Date = cold.Date
			current.InputTokens += cold.InputTokens
			current.OutputTokens += cold.OutputTokens
			current.CacheReadTokens += cold.CacheReadTokens
			byKey[key] = current
		}
	}
	out = out[:0]
	for _, current := range byKey {
		out = append(out, current)
	}
	// Price each day bucket. The daily series has no cache_write column, so pass 0
	// for it (see ProfileDayUsage.CostEur): cache-write tokens are then billed at
	// the input price. The authoritative total stays UsageByProfile.
	prices, err := d.profilePriceMap()
	if err != nil {
		return nil, err
	}
	for i := range out {
		pr, ok := prices[out[i].ProfileName]
		if !ok || pr.zero() {
			continue
		}
		c := pr.cost(out[i].InputTokens, out[i].OutputTokens, out[i].CacheReadTokens, 0)
		out[i].CostEur = &c
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].ProfileName < out[j].ProfileName
	})
	return out, nil
}

// JudgeDayUsage is one UTC-day token bucket for the intercept fallback judge,
// for the config page's recent-spend sparkline.
type JudgeDayUsage struct {
	Date         string `json:"date"` // YYYY-MM-DD (UTC)
	Calls        int    `json:"calls"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

// JudgeUsage is the cumulative token spend of the intercept fallback judge
// (worker='judge' rows in the always-on ledger), plus a recent daily series.
type JudgeUsage struct {
	Calls            int             `json:"calls"`
	InputTokens      int             `json:"input_tokens"`
	OutputTokens     int             `json:"output_tokens"`
	CacheReadTokens  int             `json:"cache_read_tokens"`
	CacheWriteTokens int             `json:"cache_write_tokens"`
	Daily            []JudgeDayUsage `json:"daily"`
}

// JudgeUsageStats returns the fallback judge's all-time token totals and a
// per-day series over the past `days` days (default 30). Sourced from the
// always-on llm_usage ledger, so it is accurate across pool rotation and
// interrupted/failed judge calls. days only bounds the daily series; totals are
// all-time.
func (d *DB) JudgeUsageStats(days int) (JudgeUsage, error) {
	if days <= 0 {
		days = 30
	}
	var u JudgeUsage
	err := d.QueryRow(`
SELECT COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
       COALESCE(SUM(cache_read),0), COALESCE(SUM(cache_write),0)
FROM llm_usage
WHERE worker = 'judge'`).Scan(&u.Calls, &u.InputTokens, &u.OutputTokens,
		&u.CacheReadTokens, &u.CacheWriteTokens)
	if err != nil {
		return JudgeUsage{}, err
	}
	rows, err := d.Query(`
SELECT to_char(ts AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS day,
       COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0)
FROM llm_usage
WHERE worker = 'judge' AND ts >= now() - ($1 * interval '1 day')
GROUP BY day
ORDER BY day`, days)
	if err != nil {
		return JudgeUsage{}, err
	}
	defer rows.Close()
	u.Daily = []JudgeDayUsage{}
	for rows.Next() {
		var day JudgeDayUsage
		if err := rows.Scan(&day.Date, &day.Calls, &day.InputTokens, &day.OutputTokens); err != nil {
			return JudgeUsage{}, err
		}
		u.Daily = append(u.Daily, day)
	}
	if err := rows.Err(); err != nil {
		return JudgeUsage{}, err
	}
	return u, nil
}

// ParseExpID turns the exploration-id segment parsed from a session string into an
// int64 (0 when empty/non-numeric, e.g. chat sessions keyed by conversation id).
func ParseExpID(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// profilePrices is one profile's per-1M-token prices (EUR), shared by every cost
// computation so the billing rule lives in exactly one place.
type profilePrices struct{ in, outp, cr, cw float64 }

// zero reports whether no price is set, so an un-priced profile yields a nil cost
// rather than a misleading 0.00.
func (p profilePrices) zero() bool { return p.in == 0 && p.outp == 0 && p.cr == 0 && p.cw == 0 }

// cost prices one bucket of tokens. InputTokens already includes both cached
// subsets: Norma normalizes it to input + cache read + cache write (OpenAI
// prompt_tokens semantics). The fresh share is therefore input minus cache reads
// minus cache writes; charging either subset at full input price as well bills it
// twice. Clamped at zero for gateways that report the buckets disjointly.
func (p profilePrices) cost(input, output, cacheRead, cacheWrite int) float64 {
	fresh := input - cacheRead - cacheWrite
	if fresh < 0 {
		fresh = 0
	}
	return float64(fresh)/1e6*p.in + float64(output)/1e6*p.outp +
		float64(cacheRead)/1e6*p.cr + float64(cacheWrite)/1e6*p.cw
}

// profilePriceMap loads the per-profile prices keyed by profile name. llm_usage
// records the name, not the id; the first matching profile wins, and duplicate
// names are a configuration error the operator sees as a weird number, not a crash.
func (d *DB) profilePriceMap() (map[string]profilePrices, error) {
	rows, err := d.Query(`SELECT name, COALESCE(price_input_per_m,0), COALESCE(price_output_per_m,0), COALESCE(price_cache_read_per_m,0), COALESCE(price_cache_write_per_m,0) FROM llm_profiles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byName := map[string]profilePrices{}
	for rows.Next() {
		var n string
		var pr profilePrices
		if err := rows.Scan(&n, &pr.in, &pr.outp, &pr.cr, &pr.cw); err != nil {
			return nil, err
		}
		if _, seen := byName[n]; !seen {
			byName[n] = pr
		}
	}
	return byName, rows.Err()
}

// applyCostEur fills CostEur on each profile bucket from the stored per-profile
// prices (EUR per 1M tokens). Profiles without any price keep CostEur nil.
func applyCostEur(d *DB, out []ProfileUsage) error {
	byName, err := d.profilePriceMap()
	if err != nil {
		return err
	}
	for i := range out {
		pr, ok := byName[out[i].ProfileName]
		if !ok || pr.zero() {
			continue
		}
		c := pr.cost(out[i].InputTokens, out[i].OutputTokens, out[i].CacheReadTokens, out[i].CacheWriteTokens)
		out[i].CostEur = &c
	}
	return nil
}
