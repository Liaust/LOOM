package knowledge

import (
	"context"
	"database/sql"

	"loom.local/loom/internal/projectcontracts"
)

func (s *Service) ProjectAdmissionStatus(ctx context.Context, projectID string) ([]projectcontracts.KnowledgeAdmissionStatus, error) {
	rows, err := s.store.db.QueryContext(ctx, `WITH roots AS MATERIALIZED (
	 SELECT r.*,`+declarationEnrollmentMembershipSQL("r")+` AS authority_current
	 FROM knowledge.notes_source_roots r WHERE r.project_id=$1 AND r.status<>'deleted'
	) SELECT r.notes_source_root_id,r.backend_root_key,r.status,r.authority_current,
	 count(o.knowledge_object_id),count(o.knowledge_object_id) FILTER (WHERE
	 o.metadata#>'{source_root,knowledge_source}' IS DISTINCT FROM r.metadata#>'{registration_metadata,knowledge_source}'),
	 max(o.updated_at)
	 FROM roots r LEFT JOIN knowledge.knowledge_objects o ON o.notes_source_root_id=r.notes_source_root_id AND o.deleted_at IS NULL
	 GROUP BY r.notes_source_root_id,r.backend_root_key,r.status,r.authority_current ORDER BY r.backend_root_key`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []projectcontracts.KnowledgeAdmissionStatus{}
	for rows.Next() {
		var item projectcontracts.KnowledgeAdmissionStatus
		var rootStatus string
		var authority bool
		var refreshed sql.NullTime
		if err = rows.Scan(&item.RootID, &item.RootKey, &rootStatus, &authority, &item.AdmittedObjects, &item.PendingBindings, &refreshed); err != nil {
			return nil, err
		}
		item.State, item.Reason = "ready", "current_admission_binding"
		switch {
		case rootStatus != SourceRootStatusActive || !authority:
			item.State, item.Reason = "waiting_for_owner", "source_authority_not_current"
		case item.PendingBindings > 0:
			item.State, item.Reason = "pending", "admission_metadata_pending"
		case item.AdmittedObjects == 0:
			item.State, item.Reason = "empty", "no_admitted_files"
		}
		if refreshed.Valid {
			item.LastObjectRefresh = &refreshed.Time
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
