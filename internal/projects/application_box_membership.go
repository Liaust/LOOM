package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/ids"
)

// This inventory fits within the native application browse project-selection
// bound. Overflow is unavailable, never a silently truncated selected catalog.
const MaxApplicationProjectBoxes = 4096

var ErrApplicationProjectBoxesUnavailable = errors.New("application.browse.project_membership_unavailable")

// ApplicationProjectBoxes reads current membership metadata, not source files,
// application grants or credentials. Nil IDs requests the complete selected
// inventory; a non-nil slice requests exactly those immutable project IDs.
func (s Service) ApplicationProjectBoxes(ctx context.Context, requestedIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(requestedIDs) > MaxApplicationProjectBoxes {
		return nil, fmt.Errorf("%w: exact project selection exceeds its bound", ErrApplicationProjectBoxesUnavailable)
	}
	seen := map[string]bool{}
	for _, id := range requestedIDs {
		if ids.Validate(ids.ProjectPrefix, id) != nil || seen[id] {
			return nil, fmt.Errorf("%w: invalid exact project selection", ErrApplicationProjectBoxesUnavailable)
		}
		seen[id] = true
	}
	if requestedIDs != nil && len(requestedIDs) == 0 {
		return out, nil
	}
	selection, scoped := boxregistry.FromContext(ctx)
	if scoped && !selection.AllBoxes && len(selection.BoxIDs) == 0 {
		return out, nil
	}
	if s.DB == nil {
		return nil, ErrApplicationProjectBoxesUnavailable
	}
	args := []any{}
	predicate := projectBoxSelectionSQL(ctx, "p", &args)
	if scoped {
		predicate += ` AND NULLIF(p.metadata->>'home_box_id','') IS NOT NULL`
		if s.boxes != nil {
			available := []string{}
			for _, box := range s.boxes.Boxes {
				if box.Available && box.ID != "" {
					available = append(available, box.ID)
				}
			}
			raw, _ := json.Marshal(available)
			args = append(args, string(raw))
			predicate += fmt.Sprintf(` AND p.metadata->>'home_box_id' IN (SELECT jsonb_array_elements_text($%d::jsonb))`, len(args))
		}
	}
	if requestedIDs != nil {
		raw, _ := json.Marshal(requestedIDs)
		args = append(args, string(raw))
		predicate += fmt.Sprintf(` AND p.project_id IN (SELECT jsonb_array_elements_text($%d::jsonb))`, len(args))
	}
	args = append(args, MaxApplicationProjectBoxes+1)
	rows, err := s.DB.QueryContext(ctx, `SELECT p.project_id,COALESCE(p.metadata->>'home_box_id','')
 FROM projects.projects p WHERE `+predicate+fmt.Sprintf(` ORDER BY p.project_id LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrApplicationProjectBoxesUnavailable, err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if count > MaxApplicationProjectBoxes {
			return nil, fmt.Errorf("%w: selected inventory exceeds its bound", ErrApplicationProjectBoxesUnavailable)
		}
		var projectID, boxID string
		if err := rows.Scan(&projectID, &boxID); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrApplicationProjectBoxesUnavailable, err)
		}
		if ids.Validate(ids.ProjectPrefix, projectID) != nil {
			return nil, ErrApplicationProjectBoxesUnavailable
		}
		if boxID != "" && !boxregistry.IsBoxRef("box:"+boxID) {
			boxID = ""
		}
		if requestedIDs != nil && !seen[projectID] {
			return nil, ErrApplicationProjectBoxesUnavailable
		}
		if scoped {
			if boxID == "" || !selection.AllBoxes && !selection.Contains(boxID) {
				continue
			}
			if s.boxes != nil {
				if _, err := s.boxes.Get(boxID); err != nil {
					continue
				}
			}
		}
		out[projectID] = boxID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrApplicationProjectBoxesUnavailable, err)
	}
	return out, nil
}
