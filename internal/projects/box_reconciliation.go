package projects

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"loom.local/loom/internal/boxregistry"
)

const MaxBoxMembershipBatch = 100

type BoxMembershipReconcileInput struct {
	ProjectIDs    []string          `json:"project_ids"`
	Apply         bool              `json:"apply"`
	CatalogDigest string            `json:"catalog_digest,omitempty"`
	Evidence      map[string]string `json:"evidence,omitempty"`
}

type BoxMembershipChange struct {
	ProjectID      string `json:"project_id"`
	HomeBoxID      string `json:"home_box_id,omitempty"`
	EvidenceDigest string `json:"evidence_digest"`
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
}

type BoxMembershipReconcileResult struct {
	CatalogDigest string                `json:"catalog_digest"`
	Changes       []BoxMembershipChange `json:"changes"`
}

func BoxCatalogDigest(catalog boxregistry.Catalog) string {
	catalog.Boxes = append([]boxregistry.Box(nil), catalog.Boxes...)
	sort.Slice(catalog.Boxes, func(i, j int) bool { return catalog.Boxes[i].ID < catalog.Boxes[j].ID })
	raw, _ := json.Marshal(catalog)
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}

// ReconcileBoxMembership is an explicit, bounded metadata-only operation. It
// never reapplies declarations, walks payloads, or schedules processing. Apply
// requires the catalog and per-project evidence from the preceding dry run.
func (s Service) ReconcileBoxMembership(ctx context.Context, input BoxMembershipReconcileInput) (BoxMembershipReconcileResult, error) {
	if s.boxes == nil || len(input.ProjectIDs) == 0 || len(input.ProjectIDs) > MaxBoxMembershipBatch {
		return BoxMembershipReconcileResult{}, fmt.Errorf("a validated catalog and 1-%d exact project IDs are required", MaxBoxMembershipBatch)
	}
	result := BoxMembershipReconcileResult{CatalogDigest: BoxCatalogDigest(*s.boxes), Changes: []BoxMembershipChange{}}
	if input.Apply && input.CatalogDigest != result.CatalogDigest {
		return result, fmt.Errorf("Box catalog changed; dry-run again")
	}
	if s.DB == nil {
		return result, fmt.Errorf("project database is not configured")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: !input.Apply, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	seen := map[string]bool{}
	ids := append([]string(nil), input.ProjectIDs...)
	sort.Strings(ids)
	for _, id := range ids {
		if seen[id] || id == "" {
			return result, fmt.Errorf("unique exact project IDs are required")
		}
		seen[id] = true
		if input.Apply {
			if err := acquireProjectRepositoryAdvisoryLocksTx(ctx, tx, "project", []string{"id:" + id}); err != nil {
				return result, err
			}
		}
		query := projectSelectSQL() + ` WHERE p.project_id=$1`
		if input.Apply {
			query += ` FOR UPDATE OF p`
		}
		project, err := scanProject(tx.QueryRowContext(ctx, query, id))
		if err != nil {
			return result, err
		}
		registration, err := getContractRegistrationByProjectTx(ctx, tx, id)
		if err != nil && err != sql.ErrNoRows {
			return result, err
		}
		change := BoxMembershipChange{ProjectID: id, HomeBoxID: HomeBoxID(project.Metadata)}
		raw, _ := json.Marshal([]any{project.ProjectID, project.Status, project.Metadata, project.ArchiveState, registration.ProjectContractRegistrationID, registration.ProjectRoot, registration.ContractHash, registration.RegistrationRevision, registration.RegistrationStatus, result.CatalogDigest})
		change.EvidenceDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
		path := registration.ProjectRoot
		if change.HomeBoxID != "" {
			change.Status = "unchanged"
		} else if err == sql.ErrNoRows {
			change.Status, change.Reason = "unassigned", "no registered source evidence"
		} else {
			if project.Status == "archived" || projectPhysicalArchiveStateRawPresent(project.ArchiveState) {
				state, ok := ParseProjectPhysicalArchiveState(project.ArchiveState)
				var proven bool
				if ok && ValidateProjectPhysicalArchiveState(state) == nil && state.ProjectID == id && state.Phase == ProjectArchivePhaseComplete && state.Restore == nil {
					err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM storage.workspace_archive_operations op
 WHERE op.workspace_archive_operation_id=$1 AND op.operation_kind='archive' AND op.terminal_status='complete'
 AND op.phase='archive_complete' AND op.source_absolute_path=$2 AND op.destination_absolute_path=$3)
 AND EXISTS (SELECT 1 FROM projects.project_repository_source_history h WHERE h.project_id=$4 AND h.project_root=$2)`, state.OperationID, state.ActivePath, state.ArchivePath, id).Scan(&proven)
					if err != nil {
						return result, err
					}
				}
				if proven {
					path = state.ActivePath
				} else {
					path = ""
					change.Reason = "archived origin is not proven by prior source and completed custody evidence"
				}
			}
			box, placementErr := s.boxes.ForPath(path)
			if placementErr == nil && box.Node != "" {
				var ownerMatches bool
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM nodes.nodes WHERE node_id=$1 AND node_key=$2)`, project.HomeNodeID, box.Node).Scan(&ownerMatches); err != nil {
					return result, err
				}
				if !ownerMatches {
					placementErr = fmt.Errorf("registered owner node differs from Box owner")
				}
			}
			if placementErr != nil || (registration.RegistrationStatus != ProjectRegistrationStatusRegistered && registration.RegistrationStatus != ProjectRegistrationStatusArchived) {
				change.Status = "unassigned"
				if change.Reason == "" {
					change.Reason = "registered location is external, unavailable, or not accepted"
				}
			} else {
				change.HomeBoxID, change.Status = box.ID, "would_assign"
			}
		}
		if input.Apply && change.Status == "would_assign" {
			if input.Evidence[id] != change.EvidenceDigest {
				return result, fmt.Errorf("project %s evidence changed; dry-run again", id)
			}
			if selection, ok := boxregistry.FromContext(ctx); ok && !selection.AllBoxes && !selection.Contains(change.HomeBoxID) {
				return result, fmt.Errorf("project membership is outside selected Box")
			}
			_, err = tx.ExecContext(ctx, `UPDATE projects.projects SET metadata=metadata||jsonb_build_object('home_box_id',$2::text,'box_membership_revision',$3::text),updated_at=now() WHERE project_id=$1`, id, change.HomeBoxID, change.EvidenceDigest)
			if err != nil {
				return result, err
			}
			change.Status = "assigned"
		}
		result.Changes = append(result.Changes, change)
	}
	return result, tx.Commit()
}
