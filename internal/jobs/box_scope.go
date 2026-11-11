package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"loom.local/loom/internal/boxregistry"
)

// BoxOwnerSQL uses verified project membership before saved workspace ownership.
// Expressions are package-owned SQL, never caller input. An unassigned project
// must not fall back to metadata claiming a different owner.
func BoxOwnerSQL(projectID, scopeID, metadata string) string {
	owner := metadata + "->>'home_box_id'"
	if scopeID != "" {
		owner = fmt.Sprintf(`CASE WHEN EXISTS (SELECT 1 FROM projects.projects box_project WHERE box_project.project_scope_id = %s)
		THEN (SELECT box_project.metadata->>'home_box_id' FROM projects.projects box_project WHERE box_project.project_scope_id = %s)
		ELSE %s END`, scopeID, scopeID, owner)
	}
	if projectID != "" {
		owner = fmt.Sprintf(`CASE WHEN %s IS NOT NULL
		THEN (SELECT box_project.metadata->>'home_box_id' FROM projects.projects box_project WHERE box_project.project_id = %s)
		ELSE %s END`, projectID, projectID, owner)
	}
	return "(" + owner + ")"
}

// BoxPredicate is shared by inventory, aggregates and reference guards. Runtime
// callers without a selection, and explicit AllBoxes, include legacy/system rows.
func BoxPredicate(ctx context.Context, ownerSQL string, args *[]any) string {
	selection, bound := boxregistry.FromContext(ctx)
	if !bound || selection.AllBoxes {
		return "true"
	}
	if len(selection.BoxIDs) == 0 {
		return "false"
	}
	raw, _ := json.Marshal(selection.BoxIDs)
	*args = append(*args, string(raw))
	return fmt.Sprintf(`NULLIF(%s, '') IN (SELECT jsonb_array_elements_text($%d::jsonb))`, ownerSQL, len(*args))
}

// BoxSelectionDigest hashes the actual selection, not a supplied membership
// digest (which is identical for AllBoxes and an explicit set of all Boxes).
func BoxSelectionDigest(ctx context.Context) string {
	selection, bound := boxregistry.FromContext(ctx)
	if !bound {
		return ""
	}
	sort.Strings(selection.BoxIDs)
	raw, _ := json.Marshal(struct {
		All bool     `json:"all"`
		IDs []string `json:"ids"`
	}{selection.AllBoxes, selection.BoxIDs})
	return boxregistry.Digest("jobs-automation-selection", string(raw), "")
}

type boxQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type retainedBoxOwnerKey struct{}

// WithBoxOwner carries persisted execution ownership through routing into job
// creation. It is not a discovery selection or a UI default.
func WithBoxOwner(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, retainedBoxOwnerKey{}, owner)
}

// BindBoxMetadata snapshots verified project ownership, or the one explicitly
// selected workspace for a no-project create. It preserves unrelated metadata.
func BindBoxMetadata(ctx context.Context, db boxQueryer, projectID, scopeID string, raw json.RawMessage) (json.RawMessage, error) {
	metadata := map[string]json.RawMessage{}
	if len(raw) != 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &metadata); err != nil || metadata == nil {
			return nil, fmt.Errorf("metadata must be a JSON object")
		}
	}
	var supplied string
	if value, ok := metadata["home_box_id"]; ok {
		if err := json.Unmarshal(value, &supplied); err != nil || strings.TrimSpace(supplied) == "" {
			return nil, fmt.Errorf("metadata home_box_id must be a nonempty string")
		}
	}
	owner := ""
	project := false
	if projectID != "" || scopeID != "" {
		err := db.QueryRowContext(ctx, `SELECT COALESCE(metadata->>'home_box_id','') FROM projects.projects
			WHERE ($1::text <> '' AND project_id=$1) OR ($1::text = '' AND project_scope_id=$2)`, projectID, scopeID).Scan(&owner)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		project = err == nil
		if projectID != "" && !project {
			return nil, fmt.Errorf("project ownership is unavailable")
		}
	}
	selection, bound := boxregistry.FromContext(ctx)
	retainedOwner, retained := ctx.Value(retainedBoxOwnerKey{}).(string)
	if project {
		if supplied != "" && supplied != owner {
			return nil, fmt.Errorf("metadata home_box_id contradicts project ownership")
		}
		if bound && !selection.AllBoxes && (owner == "" || !selection.Contains(owner)) {
			return nil, fmt.Errorf("project is outside selected Box")
		}
		if retained && retainedOwner != owner {
			return nil, fmt.Errorf("execution Box contradicts project ownership")
		}
	} else if retained {
		owner = retainedOwner
		if supplied != "" && supplied != owner {
			return nil, fmt.Errorf("metadata home_box_id contradicts execution Box")
		}
		if bound && !selection.AllBoxes && (owner == "" || !selection.Contains(owner)) {
			return nil, fmt.Errorf("execution is outside selected Box")
		}
	} else if bound {
		if selection.AllBoxes || len(selection.BoxIDs) != 1 || selection.BoxIDs[0] == "" {
			return nil, fmt.Errorf("no-project creation requires exactly one selected Box")
		}
		owner = selection.BoxIDs[0]
		if supplied != "" && supplied != owner {
			return nil, fmt.Errorf("metadata home_box_id contradicts selected Box")
		}
	} else {
		owner = supplied
	}
	if owner != "" {
		metadata["home_box_id"], _ = json.Marshal(owner)
	}
	return json.Marshal(metadata)
}

func jobBoxPredicate(ctx context.Context, args *[]any) string {
	return BoxPredicate(ctx, BoxOwnerSQL("", "j.scope_id", "j.metadata"), args)
}

func bindJobBoxMetadata(ctx context.Context, db boxQueryer, scopeID, callID string, metadata json.RawMessage) (json.RawMessage, error) {
	if _, retained := ctx.Value(retainedBoxOwnerKey{}).(string); !retained && callID != "" {
		var owner string
		err := db.QueryRowContext(ctx, `SELECT COALESCE(metadata->>'home_box_id','') FROM routing.capability_calls WHERE capability_call_id=$1`, callID).Scan(&owner)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if owner != "" {
			ctx = WithBoxOwner(ctx, owner)
		}
	}
	return BindBoxMetadata(ctx, db, "", scopeID, metadata)
}
