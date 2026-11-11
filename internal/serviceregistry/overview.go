package serviceregistry

import (
	"context"
	"database/sql"
	"time"
)

type OverviewItem struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	ProjectID  *string    `json:"project_id,omitempty"`
	ScopeID    string     `json:"scope_id"`
	Registry   string     `json:"registry"`
	Health     string     `json:"health"`
	Process    string     `json:"process"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
}
type Overview struct {
	ObservedAt time.Time      `json:"observed_at"`
	Total      int            `json:"total"`
	Counts     map[string]int `json:"counts"`
	Items      []OverviewItem `json:"items"`
	Complete   bool           `json:"complete"`
}

func ReadOverview(ctx context.Context, db *sql.DB) (Overview, error) {
	out := Overview{ObservedAt: time.Now().UTC(), Counts: map[string]int{}, Items: []OverviewItem{}}
	rows, err := db.QueryContext(ctx, `SELECT p.provider_id,p.display_name,p.scope_id,project.project_id,p.status,
 COALESCE(h.health_status,'unknown'),COALESCE(h.details_json->>'process_state','unknown'),h.last_checked_at
 FROM capabilities.providers p JOIN nodes.nodes n ON n.node_id=p.node_id
 LEFT JOIN capabilities.provider_health h ON h.provider_id=p.provider_id
 LEFT JOIN projects.projects project ON project.project_scope_id=p.scope_id
 WHERE n.node_key='main' AND p.provider_type='service' ORDER BY p.display_name,p.provider_id`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v OverviewItem
		if err = rows.Scan(&v.ID, &v.Name, &v.ScopeID, &v.ProjectID, &v.Registry, &v.Health, &v.Process, &v.ObservedAt); err != nil {
			return out, err
		}
		out.Total++
		out.Counts[v.Health]++
		if len(out.Items) < 100 {
			out.Items = append(out.Items, v)
		}
	}
	out.Complete = out.Total == len(out.Items)
	return out, rows.Err()
}
