package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/projects"
)

type SourceBoxMembershipInput struct {
	SourceRootIDs []string          `json:"source_root_ids"`
	Apply         bool              `json:"apply"`
	CatalogDigest string            `json:"catalog_digest,omitempty"`
	Evidence      map[string]string `json:"evidence,omitempty"`
}

type SourceBoxMembershipChange struct {
	SourceRootID   string `json:"source_root_id"`
	BoxID          string `json:"box_id,omitempty"`
	EvidenceDigest string `json:"evidence_digest"`
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
}

type SourceBoxMembershipResult struct {
	CatalogDigest string                      `json:"catalog_digest"`
	Changes       []SourceBoxMembershipChange `json:"changes"`
}

// ReconcileSourceBoxMembership repairs only exact roots requested by the
// operator. Do not put it on the admission timer: this is a one-shot metadata
// repair, with no object writes, audit refresh, config changes, or queue work.
func (s *Service) ReconcileSourceBoxMembership(ctx context.Context, catalog boxregistry.Catalog, input SourceBoxMembershipInput) (SourceBoxMembershipResult, error) {
	result := SourceBoxMembershipResult{CatalogDigest: projects.BoxCatalogDigest(catalog), Changes: []SourceBoxMembershipChange{}}
	if len(input.SourceRootIDs) == 0 || len(input.SourceRootIDs) > projects.MaxBoxMembershipBatch {
		return result, invalid("1-%d exact source root IDs required", projects.MaxBoxMembershipBatch)
	}
	if input.Apply && input.CatalogDigest != result.CatalogDigest {
		return result, fmt.Errorf("%w: Box catalog changed; dry-run again", ErrConflict)
	}
	if s == nil || s.store.db == nil {
		return result, fmt.Errorf("knowledge store is not configured")
	}
	tx, err := s.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: !input.Apply, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	seen := map[string]bool{}
	ids := append([]string(nil), input.SourceRootIDs...)
	sort.Strings(ids)
	for _, id := range ids {
		if id == "" || seen[id] {
			return result, invalid("unique exact source root IDs required")
		}
		seen[id] = true
		query := `SELECT ` + sourceRootColumns() + ` FROM knowledge.notes_source_roots WHERE notes_source_root_id=$1`
		if input.Apply {
			query += ` FOR UPDATE`
		}
		root, err := scanSourceRoot(tx.QueryRowContext(ctx, query, id))
		if err != nil {
			return result, err
		}
		boxID, revision, proof, err := sourceBoxMembershipEvidence(ctx, tx, root, catalog)
		if err != nil {
			return result, err
		}
		change := SourceBoxMembershipChange{SourceRootID: id, BoxID: boxID}
		raw, _ := json.Marshal([]any{root, boxID, revision, proof, result.CatalogDigest})
		change.EvidenceDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
		old := stringMapValue(jsonObject(root.Metadata), "box_id")
		switch {
		case boxID == "":
			change.Status, change.Reason = "unassigned", "no exact registration/project ownership evidence"
		case old != "" && old != boxID:
			change.Status, change.Reason = "conflict", "source ownership differs; explicit reassociation required"
		case old == boxID:
			change.Status = "unchanged"
		default:
			change.Status = "would_assign"
		}
		if input.Apply && change.Status == "would_assign" {
			if input.Evidence[id] != change.EvidenceDigest {
				return result, fmt.Errorf("%w: source %s evidence changed; dry-run again", ErrConflict, id)
			}
			if selection := notesBoxSelection(ctx); selection != nil && !selection.AllBoxes && !selection.Contains(boxID) {
				return result, fmt.Errorf("%w: source outside selected Box", ErrConflict)
			}
			_, err = tx.ExecContext(ctx, `UPDATE knowledge.notes_source_roots SET metadata=metadata||jsonb_build_object('box_id',$2::text,'box_membership_revision',$3::text),updated_at=now() WHERE notes_source_root_id=$1`, id, boxID, revision)
			if err != nil {
				return result, err
			}
			change.Status = "assigned"
		}
		result.Changes = append(result.Changes, change)
	}
	return result, tx.Commit()
}

func sourceBoxMembershipEvidence(ctx context.Context, tx *sql.Tx, root SourceRoot, catalog boxregistry.Catalog) (string, string, any, error) {
	if root.ProjectID != nil {
		var home, revision string
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(p.metadata->>'home_box_id',''),COALESCE(p.metadata->>'box_membership_revision','')
 FROM projects.projects p JOIN projects.project_watched_root_registrations wr ON wr.project_id=p.project_id
 WHERE p.project_id=$1 AND wr.project_watched_root_registration_id=$2
 AND wr.node_id IS NOT DISTINCT FROM $3 AND wr.backend_root_key=$4 AND wr.root_relative_path=$5`, root.ProjectID, root.ProjectWatchedRootRegistrationID, root.NodeID, root.BackendRootKey, root.RootRelativePath).Scan(&home, &revision)
		if err == sql.ErrNoRows {
			return "", "", nil, nil
		}
		if err != nil {
			return "", "", nil, err
		}
		if home == "" {
			return "", "", nil, nil
		}
		if _, err := catalog.Lookup(home); err != nil {
			return "", "", nil, nil
		}
		return home, revision, []any{root.ProjectID, root.ProjectWatchedRootRegistrationID, home, revision}, nil
	}
	var boxID, boxPath, nodeKey, backend, relative string
	err := tx.QueryRowContext(ctx, `SELECT box_id,box_root_path,owner_node_key,backend_root_key,root_relative_path FROM box.watch_root_registrations
 WHERE box_watch_root_registration_id=$1 AND node_id IS NOT DISTINCT FROM $2`, root.BoxWatchRootRegistrationID, root.NodeID).Scan(&boxID, &boxPath, &nodeKey, &backend, &relative)
	if err == sql.ErrNoRows {
		return "", "", nil, nil
	}
	if err != nil {
		return "", "", nil, err
	}
	box, err := catalog.Lookup(boxID)
	if err != nil || (box.Node != "" && box.Node != nodeKey) || box.RootPath != boxPath || root.NodeKey != nodeKey || root.BackendRootKey != backend || root.RootRelativePath != relative || root.SourcePath != joinSourcePath(boxPath, relative) {
		return "", "", nil, nil
	}
	return boxID, "", []string{boxID, boxPath, nodeKey, backend, relative}, nil
}
