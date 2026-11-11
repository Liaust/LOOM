package projects

import (
	"context"
	"fmt"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/ids"
)

type BoxMembershipInventoryInput struct {
	After string `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type BoxMembershipInventoryPage struct {
	IDs       []string `json:"ids"`
	NextAfter string   `json:"next_after,omitempty"`
}

// ListBoxMembershipCandidates enumerates registry IDs, including unassigned
// historical entries. It never inspects source files or content objects.
func (s Service) ListBoxMembershipCandidates(ctx context.Context, input BoxMembershipInventoryInput) (BoxMembershipInventoryPage, error) {
	selection, ok := boxregistry.FromContext(ctx)
	if !ok || !selection.AllBoxes || input.Limit < 1 || input.Limit > MaxBoxMembershipBatch || (input.After != "" && ids.Validate(ids.ProjectPrefix, input.After) != nil) {
		return BoxMembershipInventoryPage{}, fmt.Errorf("explicit all-Box selection and a bounded registry page are required")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT project_id FROM projects.projects WHERE project_id>$1 ORDER BY project_id LIMIT $2`, input.After, input.Limit+1)
	if err != nil {
		return BoxMembershipInventoryPage{}, err
	}
	defer rows.Close()
	page := BoxMembershipInventoryPage{IDs: []string{}}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return page, err
		}
		page.IDs = append(page.IDs, id)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.IDs) > input.Limit {
		page.IDs = page.IDs[:input.Limit]
		page.NextAfter = page.IDs[len(page.IDs)-1]
	}
	return page, nil
}
