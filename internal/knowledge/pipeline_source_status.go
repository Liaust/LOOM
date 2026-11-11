package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
)

type pipelineSourceEvidence struct {
	Exists, Deleted, ClaimEligible, RootActive, ExclusionAllowed     bool
	RevisionCurrent, Visible, Present, BodyOwner, Writable, Readable bool
	RootMetadata, ObjectMetadata                                     json.RawMessage
	RootKind, RelativePath                                           string
}

// The same predicates used by claims are evaluated for a bounded set of run
// identities. This is an observation only: it never advances or claims work.
func (s *Service) pipelineSourceStatuses(ctx context.Context, ids []string) (map[string]*PipelineSourceStatus, error) {
	result := map[string]*PipelineSourceStatus{}
	if len(ids) == 0 {
		return result, nil
	}
	if len(ids) > pipelineProgressLimit {
		return result, fmt.Errorf("pipeline source status exceeds bounded window")
	}
	rows, err := s.store.db.QueryContext(ctx, `WITH selected AS MATERIALIZED (
	 SELECT knowledge_pipeline_run_id,knowledge_object_id,source_hash,source_revision
	 FROM knowledge.pipeline_runs WHERE knowledge_pipeline_run_id=ANY($1) LIMIT 100
	 ) SELECT r.knowledge_pipeline_run_id,
	 o.knowledge_object_id IS NOT NULL, o.deleted_at IS NOT NULL,
	 COALESCE(`+pipelineClaimSourceSQL("r", "o")+` AND `+notesCustodyWriteAllowedSQL("o")+`,false),
	 COALESCE(root.status='active',false),
	 COALESCE(root.status='active' AND `+declarationEnrollmentMembershipSQL("root")+`
	 AND `+notesCustodyReadCaughtUpSQL("o")+` AND `+notesCustodyWriteAllowedSQL("o")+`,false),
	 COALESCE(r.source_revision=o.source_revision AND r.source_hash=o.source_hash
	 AND o.source_revision=`+notesLatestSourceSQL("o", "source_revision")+`
	 AND o.source_hash=`+notesLatestSourceSQL("o", "source_hash")+`,false),
	 COALESCE(`+visibleNotesKnowledgeObjectSQL("o")+`,false),
	 COALESCE(`+notesObservedSourcePresentSQL("o")+`,false),
	 COALESCE(`+notesCanonicalBodyOwnerSQL("o")+`,false),
	 COALESCE(`+notesCustodyWriteAllowedSQL("o")+`,false),
	 COALESCE(`+visibleNotesCustodyObjectSQL("o", true)+` AND `+visibleNotesKnowledgeRelativePathSQL("o.relative_path")+`,false),
	 COALESCE(root.metadata,'{}'::jsonb),COALESCE(o.metadata,'{}'::jsonb),COALESCE(root.root_kind,''),COALESCE(o.relative_path,'')
	 FROM selected r
	 LEFT JOIN knowledge.knowledge_objects o ON o.knowledge_object_id=r.knowledge_object_id
	 LEFT JOIN knowledge.notes_source_roots root ON root.notes_source_root_id=o.notes_source_root_id
	 `, ids)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var evidence pipelineSourceEvidence
		if err := rows.Scan(&id, &evidence.Exists, &evidence.Deleted, &evidence.ClaimEligible, &evidence.RootActive, &evidence.ExclusionAllowed,
			&evidence.RevisionCurrent, &evidence.Visible, &evidence.Present, &evidence.BodyOwner, &evidence.Writable,
			&evidence.Readable, &evidence.RootMetadata, &evidence.ObjectMetadata, &evidence.RootKind, &evidence.RelativePath); err != nil {
			return result, err
		}
		result[id] = pipelineSourceStatus(evidence)
		result[id].ObservedAt = s.currentTime()
	}
	return result, rows.Err()
}

func pipelineSourceStatus(e pipelineSourceEvidence) *PipelineSourceStatus {
	eligible := e.ClaimEligible
	readable := e.Exists && !e.Deleted && e.Readable && notesPassageSourceAllowed(SourceRoot{RootKind: e.RootKind, Metadata: e.RootMetadata}, e.RelativePath, e.ObjectMetadata)
	reason := "source_admission_evidence_not_current"
	switch {
	case eligible:
		reason = "eligible"
	case !e.Exists:
		reason = "source_object_unavailable"
	case e.Deleted:
		reason = "source_withdrawn"
	case !e.Writable:
		reason = "custody_or_project_paused"
	case !e.RootActive:
		reason = "source_root_inactive"
	case e.ExclusionAllowed && projectionDeclaredPathExcluded(e.RootMetadata, e.RelativePath):
		reason = "excluded_by_current_declaration"
		readable = false
	case !e.Present:
		reason = "source_presence_unavailable"
	case !e.RevisionCurrent:
		reason = "source_revision_changed"
	case !e.Visible:
		reason = "source_admission_evidence_not_current"
	case !e.BodyOwner:
		reason = "body_owned_by_another_source"
	}
	out := &PipelineSourceStatus{ClaimSourceEligible: &eligible, Readable: &readable, Reason: reason}
	if readable {
		out.RelativePath = e.RelativePath
	}
	return out
}
