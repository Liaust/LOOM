package knowledge

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	lexical "loom.local/loom/internal/search"
)

const (
	notesSearchFallbackEmbeddingsDisabled  = "embeddings_disabled"
	notesSearchFallbackNoActiveEmbeddings  = "no_active_embeddings"
	notesSearchFallbackRuntimeUnavailable  = "embedding_runtime_unavailable"
	notesSearchFallbackRuntimeTimeout      = "embedding_runtime_timeout"
	notesSearchFallbackSemanticUnavailable = "semantic_unavailable"
)

func (s *Service) resolveNotesSearchMode(ctx context.Context, input NotesSearchInput) (mode string, requestedMode string, semanticAvailable bool, fallbackReason string, settings EmbeddingSettings, err error) {
	requestedMode = normalizeNotesSearchMode(input.Mode)
	settings, err = s.store.GetEmbeddingSettings(ctx)
	if err != nil {
		return "", requestedMode, false, "", EmbeddingSettings{}, err
	}
	activeCount := 0
	if settings.Enabled {
		activeCount, err = s.store.CountActiveCurrentEmbeddings(ctx, settings)
		if err != nil {
			return "", requestedMode, false, "", EmbeddingSettings{}, err
		}
	}
	semanticAvailable = settings.Enabled && activeCount > 0 && s.embeddingRuntime != nil
	if requestedMode == "" {
		requestedMode = NotesSearchModeHybrid
	}
	mode = requestedMode
	switch requestedMode {
	case NotesSearchModeLexical:
		return NotesSearchModeLexical, requestedMode, semanticAvailable, "", settings, nil
	case NotesSearchModeSemantic:
		if !settings.Enabled {
			fallbackReason = notesSearchFallbackEmbeddingsDisabled
		} else if activeCount == 0 {
			fallbackReason = notesSearchFallbackNoActiveEmbeddings
		} else if s.embeddingRuntime == nil {
			fallbackReason = notesSearchFallbackRuntimeUnavailable
		}
		return NotesSearchModeSemantic, requestedMode, semanticAvailable, fallbackReason, settings, nil
	case NotesSearchModeHybrid:
		if !settings.Enabled {
			return NotesSearchModeLexical, requestedMode, semanticAvailable, notesSearchFallbackEmbeddingsDisabled, settings, nil
		}
		if activeCount == 0 {
			return NotesSearchModeLexical, requestedMode, semanticAvailable, notesSearchFallbackNoActiveEmbeddings, settings, nil
		}
		if s.embeddingRuntime == nil {
			return NotesSearchModeLexical, requestedMode, semanticAvailable, notesSearchFallbackRuntimeUnavailable, settings, nil
		}
		return NotesSearchModeHybrid, requestedMode, semanticAvailable, "", settings, nil
	default:
		return "", requestedMode, false, "", EmbeddingSettings{}, fmt.Errorf("%w: notes search mode %q is not supported", ErrInvalid, requestedMode)
	}
}

func (s *Service) searchSemanticNotes(ctx context.Context, input NotesSearchInput, settings EmbeddingSettings) ([]NotesSearchResult, error) {
	vector, err := s.notesSearchQueryVector(ctx, input, settings)
	if err != nil {
		return nil, err
	}
	return s.searchSemanticNotesVector(ctx, input, settings, vector)
}

func (s *Service) notesSearchQueryVector(ctx context.Context, input NotesSearchInput, settings EmbeddingSettings) (vector []float32, err error) {
	if input.queryEmbedding != nil {
		if input.queryEmbedding.loaded {
			return input.queryEmbedding.vector, input.queryEmbedding.err
		}
		defer func() {
			input.queryEmbedding.loaded, input.queryEmbedding.vector, input.queryEmbedding.err = true, vector, err
		}()
	}
	if s.embeddingRuntime == nil {
		return nil, fmt.Errorf("%w: embedding runtime is required for semantic notes search", ErrInvalid)
	}
	queryInput, err := BuildEmbeddingQueryInput(input.Query)
	if err != nil {
		return nil, err
	}
	response, err := s.embeddingRuntime.Embed(ctx, EmbeddingRuntimeRequest{
		Model:    settings.ModelKey,
		Inputs:   []string{queryInput},
		Truncate: false,
	})
	if err != nil {
		return nil, err
	}
	vector, err = AverageEmbeddingVectors(response.Embeddings)
	if err != nil {
		return nil, err
	}
	if len(vector) != settings.Dimensions {
		return nil, fmt.Errorf("%w: query embedding dimensions %d do not match configured dimensions %d", ErrInvalid, len(vector), settings.Dimensions)
	}
	return vector, nil
}

func (s *Service) searchSemanticNotesVector(ctx context.Context, input NotesSearchInput, settings EmbeddingSettings, vector []float32) ([]NotesSearchResult, error) {
	query, err := buildSemanticNotesSearchQuery(input, settings, vector)
	if err != nil {
		return nil, err
	}
	rows, err := s.queryNotesSearch(ctx, input, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []NotesSearchResult{}
	rank := 0
	for rows.Next() {
		result, err := scanSemanticNotesSearchResult(rows)
		if err != nil {
			return nil, err
		}
		rank++
		result.SemanticRank = rank
		result.Citation = notesSearchCitation(result)
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func buildSemanticNotesSearchQuery(input NotesSearchInput, settings EmbeddingSettings, vector []float32) (notesSearchQuery, error) {
	if _, err := NormalizeSourceLifecycleFilter(input.SourceLifecycle); err != nil {
		return notesSearchQuery{}, err
	}
	if err := validateSourceCategory(input.SourceCategory); err != nil {
		return notesSearchQuery{}, err
	}
	if strings.TrimSpace(input.Query) == "" {
		return notesSearchQuery{}, fmt.Errorf("%w: notes search query is required", ErrInvalid)
	}
	if len(vector) == 0 {
		return notesSearchQuery{}, fmt.Errorf("%w: query embedding vector is required", ErrInvalid)
	}
	input, err := normalizeNotesSearchTemporalInput(input)
	if err != nil {
		return notesSearchQuery{}, err
	}
	candidateLimit := notesSearchCandidateLimit(input.Limit)
	args := []any{
		embeddingVectorLiteral(vector),
		firstNonEmpty(settings.RuntimeKey, EmbeddingRuntimeOllama),
		firstNonEmpty(settings.ModelKey, EmbeddingModelMXBAIEmbedLarge),
		firstPositiveInt(settings.Dimensions, DefaultEmbeddingDimensions),
	}
	sqlText := `
		WITH semantic_candidates AS (
			SELECT sd.search_document_id,
			       sd.source_kind,
			       ko.knowledge_object_id,
			       COALESCE(kc.knowledge_object_version_id, '') AS knowledge_object_version_id,
			       COALESCE(kov.source_hash, '') AS passage_source_hash,
			       (COALESCE(kc.metadata->>'text_source', '') = 'metadata_text'
			        OR COALESCE(sd.metadata->>'text_source', '') = 'metadata_text'
			        OR COALESCE(sd.metadata->'metadata_only' = 'true'::jsonb, false)) AS passage_metadata_only,
			       kc.knowledge_chunk_id,
			       root.notes_source_root_id,
			       root.root_kind,
			       ` + notesReadContextSQL("root", "ko") + ` AS source_context_root,
			       ko.source_node_key,
			       COALESCE(ko.project_id, '') AS project_id,
			       ko.relative_path,
			       ko.source_path,
			       COALESCE(NULLIF(ko.title, ''), NULLIF(sd.title, ''), ko.relative_path) AS title,
			       ko.file_class,
			       kc.chunk_index,
			       COALESCE(kc.structural_path, sd.summary, '') AS structural_path,
			       left(kc.chunk_text, 240) AS snippet,
			       (ce.embedding <=> $1::vector) AS semantic_distance,
			       sd.source_created_at,
			       sd.source_modified_at,
			       COALESCE(sd.recency_at, ko.recency_at) AS recency_at,
			       COALESCE(NULLIF(sd.recency_basis, ''), ko.recency_basis) AS recency_basis,
			       COALESCE(kov.observed_at, ko.last_seen_at) AS observed_at,
			       sd.indexed_at
			FROM knowledge.chunk_embeddings ce
			JOIN knowledge.knowledge_chunks kc
			  ON kc.knowledge_chunk_id = ce.knowledge_chunk_id
			 AND kc.knowledge_object_id = ce.knowledge_object_id
			 AND kc.chunk_hash = ce.chunk_hash
			 AND kc.chunker_version = ce.chunker_version
			JOIN knowledge.knowledge_objects ko
			  ON ko.knowledge_object_id = kc.knowledge_object_id
			 AND ko.knowledge_object_id = ce.knowledge_object_id
			LEFT JOIN knowledge.knowledge_object_versions kov
			  ON kov.knowledge_object_version_id = kc.knowledge_object_version_id
			 AND kov.knowledge_object_id = kc.knowledge_object_id
			JOIN knowledge.notes_source_roots root
			  ON root.notes_source_root_id = ko.notes_source_root_id
			JOIN search.search_documents sd
			  ON sd.source_kind = 'knowledge_chunk'
			 AND sd.source_id = kc.knowledge_chunk_id
			 AND sd.index_version = 'knowledge_bm25_v1'
			WHERE ce.active = true
			  AND ce.status = 'active'
			  AND ce.knowledge_chunk_id IS NOT NULL
			  AND ce.runtime_key = $2
			  AND ce.model_key = $3
			  AND ce.dimensions = $4
			  AND ko.deleted_at IS NULL
			  AND ` + visibleNotesCustodyObjectSQL("ko", true) + `
			  AND ` + notesLifecycleSelectionSQL("ko", input.SourceLifecycle) + `
			  AND (kc.knowledge_object_version_id IS NULL OR (kov.source_hash = ko.source_hash AND kov.source_revision = ko.source_revision))
			  AND kc.status IN ('created', 'indexed')
			  AND (
				ce.knowledge_object_version_id IS NULL
				OR kc.knowledge_object_version_id IS NULL
				OR ce.knowledge_object_version_id = kc.knowledge_object_version_id
			  )
			  AND ` + visibleNotesKnowledgeRelativePathSQL("ko.relative_path") + `
	`
	add := func(condition string, value any) {
		args = append(args, value)
		sqlText += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if value := strings.TrimSpace(input.ProjectID); value != "" {
		add("ko.project_id =", value)
	}
	if input.SourceCategory != "" {
		add("("+sourceCategorySQL("root.root_kind")+") =", input.SourceCategory)
	}
	if value := strings.TrimSpace(input.SourceNodeKey); value != "" {
		add("ko.source_node_key =", value)
	}
	if value := strings.TrimSpace(input.NotesSourceRootID); value != "" {
		add("ko.notes_source_root_id =", value)
	}
	if value := strings.TrimSpace(input.FileClass); value != "" {
		add("ko.file_class =", value)
	}
	if value := strings.TrimSpace(input.Path); value != "" {
		add("ko.relative_path ILIKE", "%"+value+"%")
	}
	for _, tag := range normalizeTags(input.Tags) {
		add("(sd.metadata->'tags') ?", tag)
	}
	for _, phrase := range input.Phrases {
		args = append(args, strings.ToLower(phrase))
		sqlText += fmt.Sprintf(" AND strpos(lower(COALESCE(ko.title,'') || ' ' || COALESCE(kc.structural_path,'') || ' ' || ko.relative_path || ' ' || kc.chunk_text), $%d) > 0", len(args))
	}
	if input.After != "" {
		after, _ := ParseAbsoluteTimestamp(input.After)
		add("COALESCE(sd.recency_at, ko.recency_at) >=", after)
	}
	if input.Before != "" {
		before, _ := ParseAbsoluteTimestamp(input.Before)
		add("COALESCE(sd.recency_at, ko.recency_at) <", before)
	}
	args = append(args, candidateLimit)
	sqlText += fmt.Sprintf(`
		)
		SELECT search_document_id,
		       source_kind,
		       knowledge_object_id,
		       knowledge_object_version_id,
		       knowledge_chunk_id,
		       notes_source_root_id,
		       root_kind,
		       source_node_key,
		       project_id,
		       relative_path,
		       source_path,
		       title,
		       file_class,
		       chunk_index,
		       structural_path,
		       snippet,
		       semantic_distance,
		       source_created_at,
		       source_modified_at,
		       recency_at,
		       recency_basis,
		       observed_at,
		       indexed_at
		       ,source_context_root
		       ,passage_source_hash, passage_metadata_only
		FROM semantic_candidates
		ORDER BY semantic_distance ASC, relative_path, search_document_id
		LIMIT $%d`, len(args))
	return notesSearchQuery{SQL: sqlText, Args: args}, nil
}

func scanSemanticNotesSearchResult(scanner notesSearchResultScanner) (NotesSearchResult, error) {
	var contextRoot []byte
	var passageSourceHash string
	var passageMetadataOnly bool
	var result NotesSearchResult
	var distance float64
	var sourceCreatedAt, sourceModifiedAt sql.NullTime
	if err := scanner.Scan(
		&result.SearchDocumentID,
		&result.SourceKind,
		&result.KnowledgeObjectID,
		&result.KnowledgeObjectVersionID,
		&result.KnowledgeChunkID,
		&result.NotesSourceRootID,
		&result.RootKind,
		&result.SourceNodeKey,
		&result.ProjectID,
		&result.RelativePath,
		&result.SourcePath,
		&result.Title,
		&result.FileClass,
		&result.ChunkIndex,
		&result.StructuralPath,
		&result.Snippet,
		&distance,
		&sourceCreatedAt,
		&sourceModifiedAt,
		&result.RecencyAt,
		&result.RecencyBasis,
		&result.ObservedAt,
		&result.IndexedAt,
		&contextRoot,
		&passageSourceHash, &passageMetadataOnly,
	); err != nil {
		return NotesSearchResult{}, err
	}
	result.SourceCreatedAt = nullTimePtr(sourceCreatedAt)
	result.SourceContext = sourceContextFromRootJSON(contextRoot, result.RelativePath)
	if err := decodeNotesSearchCustody(contextRoot, &result); err != nil {
		return NotesSearchResult{}, err
	}
	result.SourceModifiedAt = nullTimePtr(sourceModifiedAt)
	result.SemanticDistance = distance
	result.SemanticScore = lexical.VectorDistanceScore(distance)
	result.RankScore = result.SemanticScore
	result.FinalScore = result.SemanticScore
	result.MatchReasons = []string{"semantic"}
	result.PassageFollowup = notesSearchPassageFollowup(result, passageSourceHash, passageMetadataOnly)
	return result, nil
}

func fuseNotesSearchResults(lexicalResults []NotesSearchResult, semanticResults []NotesSearchResult, input NotesSearchInput, queryNow time.Time) []NotesSearchResult {
	lexicalByID, lexicalCandidates := notesSearchRelevanceRankedList(lexicalResults, NotesSearchModeLexical)
	semanticByID, semanticCandidates := notesSearchRelevanceRankedList(semanticResults, NotesSearchModeSemantic)
	combined := make([]NotesSearchResult, 0, len(lexicalByID)+len(semanticByID))
	seen := map[string]bool{}
	for _, source := range []map[string]NotesSearchResult{lexicalByID, semanticByID} {
		for id, result := range source {
			if !seen[id] {
				seen[id] = true
				combined = append(combined, result)
			}
		}
	}
	recencyByID, recencyCandidates := notesSearchRecencyRankedList(combined, queryNow)
	fused := lexical.ReciprocalRankFusion([]lexical.RankedList{
		{Name: NotesSearchModeLexical, Candidates: lexicalCandidates, Weight: lexical.DefaultRelevanceRankWeight},
		{Name: NotesSearchModeSemantic, Candidates: semanticCandidates, Weight: lexical.DefaultRelevanceRankWeight},
		{Name: "recency", Candidates: recencyCandidates, Weight: lexical.DefaultRecencyRankWeight},
	}, 0)
	results := make([]NotesSearchResult, 0, len(fused))
	for _, fusedResult := range fused {
		result, ok := lexicalByID[fusedResult.ID]
		if !ok {
			result = semanticByID[fusedResult.ID]
		}
		if rank, ok := fusedResult.Ranks[NotesSearchModeLexical]; ok {
			result.LexicalRank = rank
		}
		if semanticResult, ok := semanticByID[fusedResult.ID]; ok {
			result.SemanticRank = semanticResult.SemanticRank
			result.SemanticDistance = semanticResult.SemanticDistance
			result.SemanticScore = semanticResult.SemanticScore
			result.MatchReasons = appendNotesSearchReason(result.MatchReasons, "semantic")
			if result.Snippet == "" {
				result.Snippet = semanticResult.Snippet
			}
		}
		if recency, ok := recencyByID[fusedResult.ID]; ok {
			result.RecencyScore = recency.Score
			result.RecencyRank = recency.Rank
		}
		result.RankScore = fusedResult.Score
		result.FinalScore = fusedResult.Score
		result.Citation = notesSearchCitation(result)
		results = append(results, result)
	}
	results = sortNotesSearchResults(results, input.Sort)
	return groupNotesSearchResults(results, input.Limit)
}

func rerankNotesSearchResults(results []NotesSearchResult, input NotesSearchInput, queryNow time.Time, relevanceName string) []NotesSearchResult {
	resultsByID, relevanceCandidates := notesSearchRelevanceRankedList(results, relevanceName)
	recencyByID, recencyCandidates := notesSearchRecencyRankedList(results, queryNow)
	fused := lexical.ReciprocalRankFusion([]lexical.RankedList{
		{Name: relevanceName, Candidates: relevanceCandidates, Weight: lexical.DefaultRelevanceRankWeight},
		{Name: "recency", Candidates: recencyCandidates, Weight: lexical.DefaultRecencyRankWeight},
	}, 0)
	reranked := make([]NotesSearchResult, 0, len(fused))
	for _, fusedResult := range fused {
		result, ok := resultsByID[fusedResult.ID]
		if !ok {
			continue
		}
		if recency, ok := recencyByID[fusedResult.ID]; ok {
			result.RecencyScore = recency.Score
			result.RecencyRank = recency.Rank
		}
		result.FinalScore = fusedResult.Score
		result.Citation = notesSearchCitation(result)
		reranked = append(reranked, result)
	}
	return sortNotesSearchResults(reranked, input.Sort)
}

func notesSearchRelevanceRankedList(results []NotesSearchResult, name string) (map[string]NotesSearchResult, []lexical.RankedCandidate) {
	ranked := append([]NotesSearchResult(nil), results...)
	sort.SliceStable(ranked, func(i, j int) bool {
		leftScore := notesSearchRawRelevanceScore(ranked[i])
		rightScore := notesSearchRawRelevanceScore(ranked[j])
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		return ranked[i].SearchDocumentID < ranked[j].SearchDocumentID
	})
	byID := make(map[string]NotesSearchResult, len(ranked))
	candidates := make([]lexical.RankedCandidate, 0, len(ranked))
	for index, result := range ranked {
		rank := index + 1
		if index > 0 && notesSearchRawRelevanceScore(result) == notesSearchRawRelevanceScore(ranked[index-1]) {
			previous := byID[ranked[index-1].SearchDocumentID]
			if name == NotesSearchModeSemantic {
				rank = previous.SemanticRank
			} else {
				rank = previous.LexicalRank
			}
		}
		if name == NotesSearchModeSemantic {
			result.SemanticRank = rank
		} else {
			result.LexicalRank = rank
		}
		byID[result.SearchDocumentID] = result
		candidates = append(candidates, lexical.RankedCandidate{ID: result.SearchDocumentID, Rank: rank})
	}
	return byID, candidates
}

func notesSearchRawRelevanceScore(result NotesSearchResult) float64 {
	if result.RankScore != 0 {
		return result.RankScore
	}
	if result.SemanticScore != 0 {
		return result.SemanticScore
	}
	return result.FinalScore
}

func notesSearchRecencyRankedList(results []NotesSearchResult, queryNow time.Time) (map[string]lexical.RecencyResult, []lexical.RankedCandidate) {
	inputs := make([]lexical.RecencyCandidate, 0, len(results))
	for _, result := range results {
		inputs = append(inputs, lexical.RecencyCandidate{ID: result.SearchDocumentID, At: result.RecencyAt})
	}
	ranked := lexical.RankRecencyCandidates(queryNow, inputs, lexical.DefaultRecencyHalfLife)
	byID := make(map[string]lexical.RecencyResult, len(ranked))
	candidates := make([]lexical.RankedCandidate, 0, len(ranked))
	for _, result := range ranked {
		byID[result.ID] = result
		candidates = append(candidates, lexical.RankedCandidate{ID: result.ID, Rank: result.Rank})
	}
	return byID, candidates
}

func sortNotesSearchResults(results []NotesSearchResult, sortMode string) []NotesSearchResult {
	if sortMode != NotesSearchSortNewest && sortMode != NotesSearchSortOldest {
		return results
	}
	sorted := append([]NotesSearchResult(nil), results...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].RecencyAt.Equal(sorted[j].RecencyAt) {
			if sortMode == NotesSearchSortOldest {
				return sorted[i].RecencyAt.Before(sorted[j].RecencyAt)
			}
			return sorted[i].RecencyAt.After(sorted[j].RecencyAt)
		}
		if sorted[i].FinalScore != sorted[j].FinalScore {
			return sorted[i].FinalScore > sorted[j].FinalScore
		}
		if sorted[i].RelativePath != sorted[j].RelativePath {
			return sorted[i].RelativePath < sorted[j].RelativePath
		}
		return sorted[i].SearchDocumentID < sorted[j].SearchDocumentID
	})
	return sorted
}

func semanticSearchFallbackAllowed(err error) bool {
	return IsEmbeddingRuntimeUnavailable(err) || IsEmbeddingRuntimeTimeout(err)
}

func semanticSearchFallbackReason(err error) string {
	if IsEmbeddingRuntimeTimeout(err) {
		return notesSearchFallbackRuntimeTimeout
	}
	if IsEmbeddingRuntimeUnavailable(err) {
		return notesSearchFallbackRuntimeUnavailable
	}
	return notesSearchFallbackSemanticUnavailable
}

func appendNotesSearchReason(reasons []string, reason string) []string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return reasons
	}
	for _, existing := range reasons {
		if existing == reason {
			return reasons
		}
	}
	return append(reasons, reason)
}

func firstPositiveInt(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func (s Store) CountActiveCurrentEmbeddings(ctx context.Context, settings EmbeddingSettings) (int, error) {
	if s.db == nil {
		return 0, fmt.Errorf("knowledge store is not configured")
	}
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)::int
		FROM knowledge.chunk_embeddings ce
		JOIN knowledge.knowledge_chunks kc
		  ON kc.knowledge_chunk_id = ce.knowledge_chunk_id
		 AND kc.knowledge_object_id = ce.knowledge_object_id
		 AND kc.chunk_hash = ce.chunk_hash
		 AND kc.chunker_version = ce.chunker_version
		JOIN knowledge.knowledge_objects ko
		  ON ko.knowledge_object_id = kc.knowledge_object_id
		 AND ko.knowledge_object_id = ce.knowledge_object_id
		WHERE ce.active = true
		  AND ce.status = 'active'
		  AND ce.knowledge_chunk_id IS NOT NULL
		  AND ce.runtime_key = $1
		  AND ce.model_key = $2
		  AND ce.dimensions = $3
		  AND ko.deleted_at IS NULL
		  AND (
			ce.knowledge_object_version_id IS NULL
			OR kc.knowledge_object_version_id IS NULL
			OR ce.knowledge_object_version_id = kc.knowledge_object_version_id
		  )
	`, firstNonEmpty(settings.RuntimeKey, EmbeddingRuntimeOllama), firstNonEmpty(settings.ModelKey, EmbeddingModelMXBAIEmbedLarge), firstPositiveInt(settings.Dimensions, DefaultEmbeddingDimensions)).Scan(&count)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return count, err
}
