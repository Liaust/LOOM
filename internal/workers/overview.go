package workers

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type OverviewItem struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Key         string     `json:"key"`
	Mode        string     `json:"mode"`
	Enabled     bool       `json:"enabled"`
	Paused      bool       `json:"paused"`
	Lifecycle   string     `json:"lifecycle"`
	Health      string     `json:"health"`
	CurrentRun  *string    `json:"current_run,omitempty"`
	NextRun     *time.Time `json:"next_run,omitempty"`
	LastSuccess *time.Time `json:"last_success,omitempty"`
	LastFailure *time.Time `json:"last_failure,omitempty"`
}

type Overview struct {
	ObservedAt time.Time      `json:"observed_at"`
	Total      int            `json:"total"`
	Counts     map[string]int `json:"counts"`
	Items      []OverviewItem `json:"items"`
	Complete   bool           `json:"complete"`
}

func (s Service) Overview(ctx context.Context) (Overview, error) {
	out := Overview{ObservedAt: time.Now().UTC(), Counts: map[string]int{"running": 0, "manual": 0, "scheduled": 0, "paused": 0, "disabled": 0}, Items: []OverviewItem{}}
	// The registry is small; only compact current instance metadata, never run history or configuration.
	rows, err := s.DB.QueryContext(ctx, `SELECT wi.worker_instance_id, wi.display_name,wi.worker_key,
 wi.enabled,wi.paused,wi.lifecycle_status,COALESCE(wh.health_status,'unknown'),wi.tick_policy_json,
 wi.current_run_id,wi.next_run_after,wi.last_success_at,wi.last_failure_at
 FROM workers.worker_instances wi LEFT JOIN workers.worker_health wh USING(worker_instance_id)
 JOIN nodes.nodes n ON n.node_id=wi.host_node_id WHERE n.node_key='main' ORDER BY wi.worker_key`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v OverviewItem
		var raw json.RawMessage
		var current sql.NullString
		var next, ok, failed sql.NullTime
		if err = rows.Scan(&v.ID, &v.Name, &v.Key, &v.Enabled, &v.Paused, &v.Lifecycle, &v.Health, &raw, &current, &next, &ok, &failed); err != nil {
			return out, err
		}
		v.CurrentRun = stringPtr(current)
		v.NextRun = timePtr(next)
		v.LastSuccess = timePtr(ok)
		v.LastFailure = timePtr(failed)
		v.Mode = "unknown"
		if p, e := ParseTickPolicy(raw); e == nil {
			v.Mode = p.Mode
		}
		bucket := "unknown"
		switch {
		case !v.Enabled:
			bucket = "disabled"
		case v.Paused:
			bucket = "paused"
		case v.CurrentRun != nil:
			bucket = "running"
		case v.Mode == TickModeManual:
			bucket = "manual"
		case v.Mode == TickModeInterval || v.Mode == TickModeDailyLocal:
			bucket = "scheduled"
		}
		out.Counts[bucket]++
		out.Total++
		if len(out.Items) < 100 {
			out.Items = append(out.Items, v)
		}
	}
	out.Complete = out.Total == len(out.Items)
	return out, rows.Err()
}
