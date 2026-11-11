package knowledge

import "context"

type notesSemanticCoverage string

const (
	notesSemanticCoverageAvailable   notesSemanticCoverage = "available"
	notesSemanticCoverageUnavailable notesSemanticCoverage = "unavailable"
	notesSemanticCoverageUnknown     notesSemanticCoverage = "unknown"
)

// Inspect only the admitted passages that lexical hydration will consume. A
// vector on another chunk/version is not coverage, nor is absence from ANN proof
// of unavailability. All rows belong to the same repeatable-read request.
func buildNotesSemanticCoverageQuery(input NotesSearchInput, settings EmbeddingSettings) (notesSearchQuery, error) {
	wanted, err := notesIndexedWantedJSON(input, notesLexicalVariants(input), false)
	if err != nil {
		return notesSearchQuery{}, err
	}
	query := `WITH wanted AS MATERIALIZED (
 SELECT DISTINCT id FROM jsonb_to_recordset($1::jsonb) AS w(id text))
 SELECT w.id,CASE
 WHEN sd.source_kind<>'knowledge_chunk' OR kc.knowledge_object_version_id IS NULL
 OR kov.knowledge_object_version_id IS NULL OR kc.status NOT IN ('created','indexed')
 OR COALESCE(sd.metadata->'metadata_only'='true'::jsonb,false)
 OR COALESCE(sd.metadata->>'text_source','')='metadata_text'
 OR COALESCE(kc.metadata->>'text_source','')='metadata_text'
 OR NOT ` + notesPublishedVersionSQL("ko", "kc", "kov", false, false) + ` THEN 'unknown'
 WHEN EXISTS (
 SELECT 1 FROM knowledge.chunk_embeddings ce
 WHERE ce.knowledge_chunk_id=kc.knowledge_chunk_id AND ce.knowledge_object_id=ko.knowledge_object_id
 AND ce.chunk_hash=kc.chunk_hash AND ce.chunker_version=kc.chunker_version
 AND (ce.knowledge_object_version_id IS NULL OR ce.knowledge_object_version_id=kc.knowledge_object_version_id)
 AND ce.active=true AND ce.status='active' AND ce.runtime_key=$2 AND ce.model_key=$3 AND ce.dimensions=$4
 AND ` + notesPublishedVersionSQL("ko", "kc", "kov", true, false) + `
 AND ` + notesHybridVersionSQL(NotesSearchModeHybrid) + `) THEN 'available'
 ELSE 'unavailable' END
 FROM wanted w JOIN search.search_documents sd ON sd.search_document_id=w.id
 JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=sd.notes_object_id
 LEFT JOIN knowledge.knowledge_chunks kc ON sd.source_kind='knowledge_chunk'
 AND kc.knowledge_chunk_id=sd.source_id AND kc.knowledge_object_id=ko.knowledge_object_id
 LEFT JOIN knowledge.knowledge_object_versions kov ON kov.knowledge_object_version_id=kc.knowledge_object_version_id
 AND kov.knowledge_object_id=ko.knowledge_object_id`
	return notesSearchQuery{SQL: query, Args: []any{wanted,
		firstNonEmpty(settings.RuntimeKey, EmbeddingRuntimeOllama),
		firstNonEmpty(settings.ModelKey, EmbeddingModelMXBAIEmbedLarge),
		firstPositiveInt(settings.Dimensions, DefaultEmbeddingDimensions)}}, nil
}

func (s *Service) loadNotesSemanticCoverage(ctx context.Context, input NotesSearchInput, settings EmbeddingSettings) (map[string]notesSemanticCoverage, error) {
	query, err := buildNotesSemanticCoverageQuery(input, settings)
	if err != nil {
		return nil, err
	}
	rows, err := input.readTx.QueryContext(ctx, query.SQL, query.Args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	coverage := map[string]notesSemanticCoverage{}
	for rows.Next() {
		var id string
		var state notesSemanticCoverage
		if err := rows.Scan(&id, &state); err != nil {
			return nil, err
		}
		coverage[id] = state
	}
	return coverage, rows.Err()
}
