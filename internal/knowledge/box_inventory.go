package knowledge

import (
	"context"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projects"
)

func (s *Service) ListSourceBoxMembershipCandidates(ctx context.Context, input projects.BoxMembershipInventoryInput) (projects.BoxMembershipInventoryPage, error) {
	selection := notesBoxSelection(ctx)
	if selection == nil || !selection.AllBoxes || input.Limit < 1 || input.Limit > projects.MaxBoxMembershipBatch || (input.After != "" && ids.Validate(ids.NotesSourceRootPrefix, input.After) != nil) {
		return projects.BoxMembershipInventoryPage{}, invalid("explicit all-Box selection and a bounded source-root registry page are required")
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT notes_source_root_id FROM knowledge.notes_source_roots WHERE notes_source_root_id>$1 ORDER BY notes_source_root_id LIMIT $2`, input.After, input.Limit+1)
	if err != nil {
		return projects.BoxMembershipInventoryPage{}, err
	}
	defer rows.Close()
	page := projects.BoxMembershipInventoryPage{IDs: []string{}}
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
