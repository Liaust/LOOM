package knowledge

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	lexical "loom.local/loom/internal/search"
)

// These are ranking inputs, not passages or access grants. Both lanes have
// already passed publication and shared live admission in this same snapshot.
func buildNotesFileCandidatesQuery(input NotesSearchInput) (notesSearchQuery, error) {
	variants := notesLexicalVariants(input)
	lexicalIDs, err := notesIndexedWantedJSON(input, variants, false)
	if err != nil {
		return notesSearchQuery{}, err
	}
	semanticIDs, err := notesIndexedWantedJSON(input, variants, true)
	if err != nil {
		return notesSearchQuery{}, err
	}
	return notesSearchQuery{SQL: `WITH wanted AS MATERIALIZED (
 SELECT id FROM jsonb_to_recordset($1::jsonb) AS w(id text)
 UNION SELECT id FROM jsonb_to_recordset($2::jsonb) AS w(id text))
 SELECT sd.search_document_id,sd.source_kind,ko.knowledge_object_id,
 COALESCE(ko.canonical_file_id,ko.knowledge_object_id),ko.relative_path,
 COALESCE(sd.recency_at,ko.recency_at),COALESCE(sd.metadata->>'text_source',''),
 (COALESCE(sd.metadata->'metadata_only'='true'::jsonb,false)
 OR COALESCE(sd.metadata->>'text_source','')='metadata_text'
 OR COALESCE(kc.metadata->>'text_source','')='metadata_text')
 FROM wanted w JOIN search.search_documents sd ON sd.search_document_id=w.id
 JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=sd.notes_object_id
 LEFT JOIN knowledge.knowledge_chunks kc ON sd.source_kind='knowledge_chunk'
 AND kc.knowledge_chunk_id=sd.source_id AND kc.knowledge_object_id=ko.knowledge_object_id`, Args: []any{lexicalIDs, semanticIDs}}, nil
}

func (s *Service) selectIndexedNotesFiles(ctx context.Context, input NotesSearchInput, mode string, now time.Time) error {
	query, err := buildNotesFileCandidatesQuery(input)
	if err != nil {
		return err
	}
	rows, err := s.queryNotesSearch(ctx, input, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	metadata := map[string]NotesSearchResult{}
	for rows.Next() {
		var r NotesSearchResult
		if err := rows.Scan(&r.SearchDocumentID, &r.SourceKind, &r.KnowledgeObjectID, &r.FileID,
			&r.RelativePath, &r.RecencyAt, &r.TextSource, &r.MetadataOnly); err != nil {
			return err
		}
		metadata[r.SearchDocumentID] = r
	}
	if err := rows.Err(); err != nil {
		return err
	}
	input.indexed.filePartitions = selectNotesFileCandidates(input, mode, now, metadata)
	return nil
}

func selectNotesFileCandidates(input NotesSearchInput, mode string, now time.Time, metadata map[string]NotesSearchResult) map[notesLexicalVariant]notesSearchPartition {
	partitions := map[notesLexicalVariant]notesSearchPartition{}
	for _, key := range notesLexicalVariants(input) {
		lexicalResults := notesFileCandidateLane(input, key, false, metadata)
		semanticResults := notesFileCandidateLane(input, key, true, metadata)
		part := notesSearchPartition{}
		part.matches, part.truncated = notesPartitionMatchCount(lexicalResults, semanticResults, notesSearchCandidateLimit(key.Limit))
		part.truncated = part.truncated || input.indexed.exhausted[key]
		selection := input
		selection.Limit = key.Limit
		if input.discoveryLimit > 0 {
			// Keep a small refill margin, without shrinking either retrieval lane
			// or recomputing RRF/recency on only the winning files.
			selection.Limit = minInt(key.Limit, input.discoveryLimit+4)
		}
		switch {
		case mode == NotesSearchModeHybrid && len(semanticResults) > 0:
			part.results = fuseNotesSearchResults(lexicalResults, semanticResults, selection, now)
		case mode == NotesSearchModeSemantic:
			part.results = groupNotesSearchResultsByFile(rerankNotesSearchResults(semanticResults, selection, now, mode), selection.Limit, true)
		default:
			part.results = groupNotesSearchResultsByFile(rerankNotesSearchResults(lexicalResults, selection, now, NotesSearchModeLexical), selection.Limit, true)
		}
		partitions[key] = part
	}
	return partitions
}

func notesFileCandidateLane(input NotesSearchInput, key notesLexicalVariant, semantic bool, metadata map[string]NotesSearchResult) []NotesSearchResult {
	results := []NotesSearchResult{}
	for _, id := range notesIndexedLaneIDs(input.indexed, key, semantic) {
		r, ok := metadata[id]
		if !ok {
			continue
		}
		if semantic {
			r.SemanticDistance = input.indexed.semanticDistances[id]
			r.SemanticScore = lexical.VectorDistanceScore(r.SemanticDistance)
			r.RankScore, r.FinalScore = r.SemanticScore, r.SemanticScore
			r.SemanticCoverage = notesSemanticCoverageAvailable
		} else {
			r.RankScore, r.FinalScore = input.indexed.lexicalScores[id], input.indexed.lexicalScores[id]
			annotateIndexedNotesLexicalResult(input, &r)
		}
		results = append(results, r)
	}
	// Preserve the hydration lane's deterministic ranks, including equal-score
	// ties whose retrieval order need not include the path tie-breaker.
	sort.SliceStable(results, func(i, j int) bool {
		a, b := results[i], results[j]
		if semantic && a.SemanticDistance != b.SemanticDistance {
			return a.SemanticDistance < b.SemanticDistance
		}
		if !semantic {
			if a.lexicalFallback != b.lexicalFallback {
				return !a.lexicalFallback
			}
			if a.RankScore != b.RankScore {
				return a.RankScore > b.RankScore
			}
		}
		if a.RelativePath != b.RelativePath {
			return a.RelativePath < b.RelativePath
		}
		return a.SearchDocumentID < b.SearchDocumentID
	})
	for i := range results {
		if semantic {
			results[i].SemanticRank = i + 1
		} else {
			results[i].LexicalRank = i + 1
		}
	}
	return results
}

func notesFileHydratesLane(input NotesSearchInput, semantic bool) bool {
	if input.indexed == nil || input.indexed.filePartitions == nil {
		return true
	}
	for _, part := range input.indexed.filePartitions {
		for _, r := range part.results {
			if semantic == (r.LexicalRank == 0) {
				return true
			}
		}
	}
	return false
}

func notesIndexedHydrationObjectsSQL(input NotesSearchInput, args *[]any) (string, error) {
	visible := input.visibleObjectIDsJSON
	if input.indexed.filePartitions != nil {
		ids, seen := []string{}, map[string]bool{}
		for _, part := range input.indexed.filePartitions {
			for _, r := range part.results {
				if !seen[r.KnowledgeObjectID] && input.indexed.visible[r.KnowledgeObjectID] {
					ids = append(ids, r.KnowledgeObjectID)
					seen[r.KnowledgeObjectID] = true
				}
			}
		}
		sort.Strings(ids)
		raw, err := json.Marshal(ids)
		if err != nil {
			return "", err
		}
		visible = string(raw)
	}
	return notesSearchObjectsSQL(NotesSearchInput{SourceLifecycle: SourceLifecycleFilterAll, visibleObjectIDsJSON: visible}, args), nil
}

// A shared hit uses lexical hydration, as fusion did previously. Its semantic
// distance/rank and coverage come from the original candidate lane, not a
// second snippet generation or reranking of the reduced hydration set.
func notesSelectedFileNeedsLane(state *notesIndexedSearch, key notesLexicalVariant, id string, semantic bool) bool {
	for _, selected := range state.filePartitions[key].results {
		if selected.SearchDocumentID == id {
			return semantic == (selected.LexicalRank == 0)
		}
	}
	return false
}

func selectedNotesFilePartition(input NotesSearchInput, fallback string) notesSearchPartition {
	key := notesLexicalVariant{input.SourceLifecycle, input.RequireCurrent, input.Limit}
	part := input.indexed.filePartitions[key]
	part.fallback = fallback
	if part.fallback == "" && input.queryEmbedding != nil && input.queryEmbedding.err != nil {
		part.fallback = semanticSearchFallbackReason(input.queryEmbedding.err)
	}
	hydrated := map[string]NotesSearchResult{}
	for _, lane := range []map[notesLexicalVariant][]NotesSearchResult{input.lexicalBatch, input.semanticBatch} {
		for _, r := range lane[key] {
			hydrated[r.SearchDocumentID] = r
		}
	}
	results := []NotesSearchResult{}
	for _, selected := range part.results {
		r, ok := hydrated[selected.SearchDocumentID]
		if !ok {
			part.truncated = true
			continue
		}
		r.FileID = selected.FileID
		r.RankScore, r.FinalScore = selected.RankScore, selected.FinalScore
		r.LexicalRank, r.SemanticRank = selected.LexicalRank, selected.SemanticRank
		r.SemanticScore, r.SemanticDistance = selected.SemanticScore, selected.SemanticDistance
		r.SemanticCoverage = selected.SemanticCoverage
		r.RecencyScore, r.RecencyRank = selected.RecencyScore, selected.RecencyRank
		if r.SemanticRank > 0 {
			r.MatchReasons = appendNotesSearchReason(r.MatchReasons, "semantic")
		}
		results = append(results, r)
	}
	part.results = results
	return part
}
