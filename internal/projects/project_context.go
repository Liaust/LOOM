package projects

import (
	"context"
	"fmt"

	"loom.local/loom/internal/ids"
)

// Stable keyset enumeration over registered sources only, including archived
// identities so their lifecycle can be reflected without reopening payloads.
func (s Service) ListProjectContextSources(ctx context.Context, owner, after string, limit int) ([]string, error) {
	if owner == "" || limit < 1 || limit > 20 || (after != "" && ids.Validate(ids.ProjectPrefix, after) != nil) {
		return nil, fmt.Errorf("invalid project context page")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT p.project_id
		FROM projects.projects p JOIN projects.project_repository_sources s ON s.project_id=p.project_id
		WHERE s.source_snapshot_json->'location'->>'owner_node'=$1 AND p.project_id>$2
		ORDER BY p.project_id LIMIT $3`, owner, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}
