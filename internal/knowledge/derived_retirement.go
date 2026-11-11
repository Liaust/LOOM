package knowledge

import (
	"context"
	"fmt"
)

type RetainedFileOutputsInput struct {
	After string `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type RetainedFileOutput struct {
	FileID    string `json:"file_id"`
	VersionID string `json:"version_id"`
	Artifacts int64  `json:"artifacts"`
	TextBytes int64  `json:"text_bytes"`
	Chunks    int64  `json:"chunks"`
	Vectors   int64  `json:"vectors"`
}

type RetainedFileOutputsResult struct {
	Outputs   []RetainedFileOutput `json:"outputs"`
	NextAfter string               `json:"next_after,omitempty"`
	Notice    string               `json:"notice"`
}

// An inventory is not deletion authority. Historical citations are not all
// foreign keys: project files and external consumers can still reference them.
func (s *Service) ListRetainedFileOutputs(ctx context.Context, input RetainedFileOutputsInput) (RetainedFileOutputsResult, error) {
	if input.Limit == 0 {
		input.Limit = 100
	}
	if input.Limit < 1 || input.Limit > 200 {
		return RetainedFileOutputsResult{}, fmt.Errorf("%w: limit must be 1..200", ErrInvalid)
	}
	rows, err := s.store.db.QueryContext(ctx, `WITH candidates AS (
	 SELECT v.knowledge_object_version_id,v.knowledge_object_id,COALESCE(o.canonical_file_id,o.knowledge_object_id) file_id
	 FROM knowledge.knowledge_object_versions v JOIN knowledge.knowledge_objects o USING(knowledge_object_id)
	 WHERE v.knowledge_object_version_id>$1
	 AND v.knowledge_object_version_id IS DISTINCT FROM o.lexical_version_id
	 AND v.knowledge_object_version_id IS DISTINCT FROM o.semantic_version_id
	 AND NOT EXISTS(SELECT 1 FROM knowledge.pipeline_runs r WHERE r.knowledge_object_version_id=v.knowledge_object_version_id
	 AND r.status IN ('processing','queued','waiting_coordinator','waiting_heavy','waiting_quiet_window'))
	 ORDER BY v.knowledge_object_version_id LIMIT $2)
	 SELECT c.file_id,c.knowledge_object_version_id,
	 (SELECT count(*) FROM knowledge.derived_artifacts a WHERE a.knowledge_object_version_id=c.knowledge_object_version_id),
	 (SELECT COALESCE(sum(octet_length(a.text_content)),0) FROM knowledge.derived_artifacts a WHERE a.knowledge_object_version_id=c.knowledge_object_version_id),
	 (SELECT count(*) FROM knowledge.knowledge_chunks k WHERE k.knowledge_object_version_id=c.knowledge_object_version_id),
	 (SELECT count(*) FROM knowledge.chunk_embeddings e WHERE e.knowledge_object_version_id=c.knowledge_object_version_id)
	 FROM candidates c ORDER BY c.knowledge_object_version_id`, input.After, input.Limit+1)
	if err != nil {
		return RetainedFileOutputsResult{}, err
	}
	defer rows.Close()
	out := RetainedFileOutputsResult{Outputs: []RetainedFileOutput{}, Notice: "Read-only inventory. Current and in-flight publications are excluded. Review historical citations, Provenance and recovery references before any separate reclamation; nothing is deleted."}
	for rows.Next() {
		var item RetainedFileOutput
		if err = rows.Scan(&item.FileID, &item.VersionID, &item.Artifacts, &item.TextBytes, &item.Chunks, &item.Vectors); err != nil {
			return out, err
		}
		if len(out.Outputs) == input.Limit {
			out.NextAfter = out.Outputs[len(out.Outputs)-1].VersionID
			break
		}
		out.Outputs = append(out.Outputs, item)
	}
	return out, rows.Err()
}
