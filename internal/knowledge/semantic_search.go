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
	hasEmbeddings := false
	if settings.Enabled {
		hasEmbeddings, err = s.store.HasActiveCurrentEmbeddings(ctx, settings)
		if err != nil {
			return "", requestedMode, false, "", EmbeddingSettings{}, err
		}
	}
	semanticAvailable = settings.Enabled && hasEmbeddings && s.embeddingRuntime != nil
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
		} else if !hasEmbeddings {
			fallbackReason = notesSearchFallbackNoActiveEmbeddings
		} else if s.embeddingRuntime == nil {
			fallbackReason = notesSearchFallbackRuntimeUnavailable
		}
		return NotesSearchModeSemantic, requestedMode, semanticAvailable, fallbackReason, settings, nil
	case NotesSearchModeHybrid:
		if !settings.Enabled {
			return NotesSearchModeLexical, requestedMode, semanticAvailable, notesSearchFallbackEmbeddingsDisabled, settings, nil
		}
		if !hasEmbeddings {
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
	if input.semanticBatch != nil {
		if results, ok := input.semanticBatch[notesLexicalVariant{input.SourceLifecycle, input.RequireCurrent, input.Limit}]; ok {
			return append([]NotesSearchResult{}, results...), nil
		}
	}
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
	return buildSemanticNotesVariantsQuery(input, settings, vector, nil)
}

func buildSemanticNotesVariantsQuery(input NotesSearchInput, settings EmbeddingSettings, vector []float32, variants []notesLexicalVariant) (notesSearchQuery, error) {
	if input.indexed != nil {
		return buildIndexedNotesSemanticHydration(input, settings, vector, variants)
	}
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
	objectsInput := input
	if len(variants) > 0 {
		objectsInput.SourceLifecycle = SourceLifecycleFilterAll
	}
	objectsSQL := notesSearchObjectsSQL(objectsInput, &args)
	sqlText := `
		WITH visible_objects AS MATERIALIZED (` + objectsSQL + `),
		semantic_candidates AS MATERIALIZED (
			SELECT sd.search_document_id,
			       ko.knowledge_object_id,
			       kc.knowledge_chunk_id,
			       ko.relative_path,
			       (ce.embedding <=> $1::vector) AS semantic_distance,
			       ko.search_lifecycle,
			       (COALESCE(kov.source_revision,ko.source_revision)=ko.latest_source_revision
			        AND COALESCE(kov.source_hash,ko.source_hash)=ko.latest_source_hash) AS publication_current
			FROM knowledge.chunk_embeddings ce
			JOIN knowledge.knowledge_chunks kc
			  ON kc.knowledge_chunk_id = ce.knowledge_chunk_id
			 AND kc.knowledge_object_id = ce.knowledge_object_id
			 AND kc.chunk_hash = ce.chunk_hash
			 AND kc.chunker_version = ce.chunker_version
			JOIN visible_objects ko
			  ON ko.knowledge_object_id = kc.knowledge_object_id
			 AND ko.knowledge_object_id = ce.knowledge_object_id
			LEFT JOIN knowledge.knowledge_object_versions kov
			  ON kov.knowledge_object_version_id = kc.knowledge_object_version_id
			 AND kov.knowledge_object_id = kc.knowledge_object_id
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
			  AND ` + notesPublishedVersionSQL("ko", "kc", "kov", true, false) + `
			  AND ` + notesHybridVersionSQL(input.Mode) + `
			  AND kc.status IN ('created', 'indexed')
			  AND (
				ce.knowledge_object_version_id IS NULL
				OR kc.knowledge_object_version_id IS NULL
				OR ce.knowledge_object_version_id = kc.knowledge_object_version_id
			  )
	`
	add := func(condition string, value any) {
		args = append(args, value)
		sqlText += fmt.Sprintf(" AND %s $%d", condition, len(args))
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
	sqlText += `)`
	filterSQL, limitSQL := "TRUE", "request.candidate_limit"
	if len(variants) > 0 {
		values := make([]string, 0, len(variants))
		for i, variant := range variants {
			if variant.Lifecycle != SourceLifecycleFilterActive && variant.Lifecycle != SourceLifecycleFilterArchived {
				return notesSearchQuery{}, fmt.Errorf("invalid semantic partition lifecycle")
			}
			values = append(values, fmt.Sprintf("(%d,'%s',%t,%d)", i, variant.Lifecycle, variant.Current, notesSearchCandidateLimit(variant.Limit)))
		}
		sqlText += ` SELECT request.ordinal,result.* FROM (VALUES ` + strings.Join(values, ",") + `)
		 AS request(ordinal,lifecycle,require_current,candidate_limit) CROSS JOIN LATERAL (`
		filterSQL = "search_lifecycle=request.lifecycle AND (NOT request.require_current OR publication_current)"
	} else {
		if input.RequireCurrent {
			filterSQL = "publication_current"
		}
		args = append(args, candidateLimit)
		limitSQL = fmt.Sprintf("$%d", len(args))
	}
	if len(variants) == 0 {
		sqlText += `,`
	} else {
		sqlText += `WITH`
	}
	sqlText += ` ranked AS MATERIALIZED (
	 SELECT * FROM semantic_candidates WHERE ` + filterSQL + `
	 ORDER BY semantic_distance ASC,relative_path,search_document_id LIMIT ` + limitSQL + `
	)
	SELECT sd.search_document_id, sd.source_kind, ko.knowledge_object_id,
	 COALESCE(kc.knowledge_object_version_id,''), kc.knowledge_chunk_id,
	 root.notes_source_root_id, root.root_kind, ko.source_node_key, COALESCE(ko.project_id,''),
	 ko.relative_path, ko.source_path,
	 COALESCE(NULLIF(ko.title,''),NULLIF(sd.title,''),ko.relative_path), ko.file_class,
	 kc.chunk_index, COALESCE(kc.structural_path,sd.summary,''), left(kc.chunk_text,240),
	 ranked.semantic_distance, sd.source_created_at, sd.source_modified_at,
	 COALESCE(sd.recency_at,ko.recency_at), COALESCE(NULLIF(sd.recency_basis,''),ko.recency_basis),
	 COALESCE(kov.observed_at,ko.last_seen_at), sd.indexed_at,
	 ` + notesSearchReadContextSQL("root", "ko", "kov", true) + ` AS source_context_root,
	 COALESCE(kov.source_hash,'') AS passage_source_hash,
	 (COALESCE(kc.metadata->>'text_source','')='metadata_text'
	  OR COALESCE(sd.metadata->>'text_source','')='metadata_text'
	  OR COALESCE(sd.metadata->'metadata_only'='true'::jsonb,false))
	FROM ranked
	JOIN search.search_documents sd ON sd.search_document_id=ranked.search_document_id
	JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=ranked.knowledge_object_id
	JOIN knowledge.knowledge_chunks kc ON kc.knowledge_chunk_id=ranked.knowledge_chunk_id
	JOIN knowledge.notes_source_roots root ON root.notes_source_root_id=ko.notes_source_root_id
	LEFT JOIN knowledge.knowledge_object_versions kov
	 ON kov.knowledge_object_version_id=kc.knowledge_object_version_id AND kov.knowledge_object_id=ko.knowledge_object_id
	ORDER BY ranked.semantic_distance,ranked.relative_path,ranked.search_document_id`
	if len(variants) > 0 {
		sqlText += `) result ORDER BY request.ordinal,result.semantic_distance,result.relative_path,result.search_document_id`
	}
	return notesSearchQuery{SQL: sqlText, Args: args}, nil
}

func (s *Service) loadNotesSemanticBatch(ctx context.Context, input NotesSearchInput, settings EmbeddingSettings, vector []float32) (map[notesLexicalVariant][]NotesSearchResult, error) {
	variants := notesLexicalVariants(input)
	query, err := buildSemanticNotesVariantsQuery(input, settings, vector, variants)
	if err != nil {
		return nil, err
	}
	rows, err := s.queryNotesSearch(ctx, input, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	batch := make(map[notesLexicalVariant][]NotesSearchResult, len(variants))
	for _, variant := range variants {
		batch[variant] = []NotesSearchResult{}
	}
	for rows.Next() {
		var ordinal int
		result, err := scanSemanticNotesSearchResult(notesReadScannerFunc(func(dest ...any) error {
			return rows.Scan(append([]any{&ordinal}, dest...)...)
		}))
		if err != nil {
			return nil, err
		}
		if ordinal < 0 || ordinal >= len(variants) {
			return nil, fmt.Errorf("invalid semantic request partition")
		}
		key := variants[ordinal]
		result.SemanticRank = len(batch[key]) + 1
		result.Citation = notesSearchCitation(result)
		batch[key] = append(batch[key], result)
	}
	return batch, rows.Err()
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

func (s Store) HasActiveCurrentEmbeddings(ctx context.Context, settings EmbeddingSettings) (bool, error) {
	if s.db == nil {
		return false, fmt.Errorf("knowledge store is not configured")
	}
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1
		FROM knowledge.chunk_embeddings ce
		JOIN knowledge.knowledge_chunks kc
		  ON kc.knowledge_chunk_id = ce.knowledge_chunk_id
		 AND kc.knowledge_object_id = ce.knowledge_object_id
		 AND kc.chunk_hash = ce.chunk_hash
		 AND kc.chunker_version = ce.chunker_version
		JOIN knowledge.knowledge_objects ko
		  ON ko.knowledge_object_id = kc.knowledge_object_id
		 AND ko.knowledge_object_id = ce.knowledge_object_id
		LEFT JOIN knowledge.knowledge_object_versions kov ON kov.knowledge_object_version_id=kc.knowledge_object_version_id
		WHERE ce.active = true
		  AND ce.status = 'active'
		  AND ce.knowledge_chunk_id IS NOT NULL
		  AND ce.runtime_key = $1
		  AND ce.model_key = $2
		  AND ce.dimensions = $3
		  AND ko.deleted_at IS NULL
		  AND `+notesPublishedVersionSQL("ko", "kc", "kov", true, false)+`
		  AND (
			ce.knowledge_object_version_id IS NULL
			OR kc.knowledge_object_version_id IS NULL
			OR ce.knowledge_object_version_id = kc.knowledge_object_version_id
		  )
		)
	`, firstNonEmpty(settings.RuntimeKey, EmbeddingRuntimeOllama), firstNonEmpty(settings.ModelKey, EmbeddingModelMXBAIEmbedLarge), firstPositiveInt(settings.Dimensions, DefaultEmbeddingDimensions)).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return exists, err
}
