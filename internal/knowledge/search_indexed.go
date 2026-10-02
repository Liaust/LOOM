package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// All state is local to one repeatable-read request. Candidates are not access
// grants: live admission is shared only after both lanes have retrieved them.
type notesIndexedSearch struct {
	lexical   map[notesLexicalVariant][]string
	semantic  map[notesLexicalVariant][]string
	exhausted map[notesLexicalVariant]bool
}

func notesIndexedBudget(input NotesSearchInput) int {
	if input.indexedBudget > 0 {
		return minInt(800, input.indexedBudget)
	}
	return minInt(800, max(128, notesSearchCandidateLimit(input.Limit)*2))
}

func (s *Service) prepareIndexedNotesSearch(ctx context.Context, input *NotesSearchInput, mode, fallback string, settings EmbeddingSettings) error {
	var ready bool
	if err := input.readTx.QueryRowContext(ctx, `SELECT to_regclass('search.search_notes_object_idx') IS NOT NULL`).Scan(&ready); err != nil {
		return err
	}
	if !ready {
		return nil // The previous release remains usable before migration 83.
	}
	if input.indexedExactScope == nil && notesIndexedExactScope(*input) {
		var args []any
		scope := notesIndexedScopeSQL(*input, &args)
		var count int
		query := `SELECT count(*) FROM (SELECT DISTINCT ko.knowledge_object_id
 FROM knowledge.knowledge_objects ko JOIN knowledge.notes_source_roots root USING(notes_source_root_id)
 JOIN search.search_documents sd ON sd.notes_object_id=ko.knowledge_object_id
 AND sd.source_kind IN ('knowledge_chunk','knowledge_object_metadata') AND sd.index_version='knowledge_bm25_v1'
 WHERE ` + scope + ` LIMIT 201) scoped`
		if err := input.readTx.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
			return err
		}
		exact := count <= 200
		input.indexedExactScope = &exact
	}
	state := &notesIndexedSearch{lexical: map[notesLexicalVariant][]string{}, semantic: map[notesLexicalVariant][]string{}, exhausted: map[notesLexicalVariant]bool{}}
	variants := notesLexicalVariants(*input)
	objects := map[string]bool{}
	documentObjects := map[string]string{}
	load := func(query notesSearchQuery, lane map[notesLexicalVariant][]string) error {
		rows, err := input.readTx.QueryContext(ctx, query.SQL, query.Args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for _, variant := range variants {
			lane[variant] = []string{}
		}
		for rows.Next() {
			var ordinal int
			var documentID, objectID string
			if err := rows.Scan(&ordinal, &documentID, &objectID); err != nil {
				return err
			}
			if ordinal < 0 || ordinal >= len(variants) {
				return fmt.Errorf("invalid indexed Notes partition %d", ordinal)
			}
			key := variants[ordinal]
			lane[key] = append(lane[key], documentID)
			objects[objectID] = true
			documentObjects[documentID] = objectID
			if len(lane[key]) >= notesIndexedBudget(*input) {
				state.exhausted[key] = true
			}
		}
		return rows.Err()
	}
	if mode != NotesSearchModeSemantic {
		query, err := buildIndexedNotesLexicalCandidates(*input, variants)
		if err != nil {
			return err
		}
		if err := load(query, state.lexical); err != nil {
			return err
		}
	}
	if mode != NotesSearchModeLexical && fallback == "" && input.queryEmbedding.err == nil {
		// Iterative scans let HNSW refill after scope/publication filtering. The
		// controls are transaction-local; exact scoped searches do not need ANN.
		if _, err := input.readTx.ExecContext(ctx, `SET LOCAL hnsw.iterative_scan = 'relaxed_order'; SET LOCAL hnsw.ef_search = 100`); err != nil {
			return err
		}
		query, err := buildIndexedNotesSemanticCandidates(*input, settings, input.queryEmbedding.vector, variants)
		if err != nil {
			return err
		}
		if err := load(query, state.semantic); err != nil {
			return err
		}
		if !notesIndexedExactScope(*input) {
			for _, variant := range variants {
				if variant.Lifecycle == SourceLifecycleFilterActive {
					// Post-filtered ANN is bounded even when every retrieved hit
					// is excluded. Never turn an empty pool into an exact count.
					state.exhausted[variant] = true
				}
			}
		}
	}
	ids := make([]string, 0, len(objects))
	for id := range objects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	raw, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	input.candidateObjectIDsJSON = string(raw)
	input.visibleObjectIDsJSON, err = s.loadNotesVisibleObjectIDs(ctx, *input)
	if err != nil {
		return err
	}
	input.indexed = state
	var admitted []string
	if err := json.Unmarshal([]byte(input.visibleObjectIDsJSON), &admitted); err != nil {
		return err
	}
	visible := map[string]bool{}
	for _, id := range admitted {
		visible[id] = true
	}
	primary := input.SourceLifecycle
	if primary == SourceLifecycleFilterAll {
		primary = SourceLifecycleFilterActive
	}
	key := notesLexicalVariant{primary, input.RequireCurrent, input.Limit}
	if notesIndexedBudget(*input) < 800 && notesIndexedNeedsRefill(state, key, documentObjects, visible, input.Limit) {
		input.indexedBudget = minInt(800, notesIndexedBudget(*input)*2)
		input.indexed = nil
		return s.prepareIndexedNotesSearch(ctx, input, mode, fallback, settings)
	}
	return nil
}

func notesIndexedNeedsRefill(state *notesIndexedSearch, key notesLexicalVariant, documentObjects map[string]string, visible map[string]bool, limit int) bool {
	if !state.exhausted[key] {
		return false
	}
	found := map[string]bool{}
	// Hybrid is one merged result set, not two independently full pages.
	for _, lane := range []map[notesLexicalVariant][]string{state.lexical, state.semantic} {
		for _, document := range lane[key] {
			if object := documentObjects[document]; visible[object] {
				found[object] = true
			}
		}
	}
	return len(found) < limit
}

func notesIndexedRequests(variants []notesLexicalVariant, budget int) string {
	values := make([]string, 0, len(variants))
	for i, variant := range variants {
		values = append(values, fmt.Sprintf("(%d,'%s',%t,%d)", i, variant.Lifecycle, variant.Current, budget))
	}
	return `(VALUES ` + strings.Join(values, ",") + `) AS request(ordinal,lifecycle,require_current,candidate_limit)`
}

func notesIndexedQueryCTE() string {
	// plainto_tsquery escapes identifier punctuation; OR matches the historical
	// any-term behaviour. Quotes are checked literally across fields separately.
	return `query_terms AS (SELECT DISTINCT lower(trim(value)) term FROM jsonb_array_elements_text($2::jsonb) item(value) WHERE trim(value)<>'' AND $1::text IS NOT NULL),
 query_phrases AS (SELECT DISTINCT lower(trim(value)) phrase FROM jsonb_array_elements_text($3::jsonb) item(value) WHERE trim(value)<>''),
 native_query AS (SELECT COALESCE(string_agg('(' || plainto_tsquery('simple',term)::text || ')',' | ')
 FILTER (WHERE numnode(plainto_tsquery('simple',term))>0),'')::tsquery query FROM query_terms)`
}

func notesIndexedScopeSQL(input NotesSearchInput, args *[]any) string {
	clauses := []string{"ko.deleted_at IS NULL", visibleNotesKnowledgeRelativePathSQL("ko.relative_path")}
	add := func(condition string, value any) {
		*args = append(*args, value)
		clauses = append(clauses, fmt.Sprintf("%s $%d", condition, len(*args)))
	}
	for _, filter := range []struct{ column, value string }{
		{"ko.project_id =", input.ProjectID}, {"ko.notes_source_root_id =", input.NotesSourceRootID},
		{"ko.source_node_key =", input.SourceNodeKey}, {"ko.file_class =", input.FileClass},
		{"(" + sourceCategorySQL("root.root_kind") + ") =", input.SourceCategory},
	} {
		if value := strings.TrimSpace(filter.value); value != "" {
			add(filter.column, value)
		}
	}
	if input.Path != "" {
		add("ko.relative_path ILIKE", "%"+input.Path+"%")
	}
	for _, tag := range normalizeTags(input.Tags) {
		add("(sd.metadata->'tags') ?", tag)
	}
	if input.After != "" {
		after, _ := ParseAbsoluteTimestamp(input.After)
		add("COALESCE(sd.recency_at,ko.recency_at) >=", after)
	}
	if input.Before != "" {
		before, _ := ParseAbsoluteTimestamp(input.Before)
		add("COALESCE(sd.recency_at,ko.recency_at) <", before)
	}
	return strings.Join(clauses, " AND ")
}

func buildIndexedNotesLexicalCandidates(input NotesSearchInput, variants []notesLexicalVariant) (notesSearchQuery, error) {
	terms, err := notesSearchJSONList(notesSearchTerms(input))
	if err != nil {
		return notesSearchQuery{}, err
	}
	phrases, err := notesSearchJSONList(input.Phrases)
	if err != nil {
		return notesSearchQuery{}, err
	}
	args := []any{input.Query, terms, phrases}
	scope := notesIndexedScopeSQL(input, &args)
	where := `sd.source_kind IN ('knowledge_chunk','knowledge_object_metadata') AND sd.index_version='knowledge_bm25_v1'`
	query := `WITH ` + notesIndexedQueryCTE() + `,
 matched_ids AS MATERIALIZED (
 SELECT sd.search_document_id FROM search.search_documents sd,native_query nq WHERE ` + where + ` AND sd.tsv @@ nq.query
 UNION
 SELECT sd.search_document_id FROM query_terms qt CROSS JOIN LATERAL (
 SELECT search_document_id FROM search.search_documents sd WHERE ` + where + `
 AND lower(sd.body) LIKE ('%' || replace(replace(replace(qt.term,chr(92),chr(92)||chr(92)),'%',chr(92)||'%'),'_',chr(92)||'_') || '%') ESCAPE E'\\'
 ) sd
 UNION
 SELECT sd.search_document_id FROM query_terms qt CROSS JOIN LATERAL (
 SELECT search_document_id FROM search.search_documents sd WHERE ` + where + `
 AND sd.notes_fields LIKE ('%' || replace(replace(replace(qt.term,chr(92),chr(92)||chr(92)),'%',chr(92)||'%'),'_',chr(92)||'_') || '%') ESCAPE E'\\'
 ) sd
 UNION
 SELECT sd.search_document_id FROM query_terms qt CROSS JOIN LATERAL (
 SELECT search_document_id FROM search.search_documents sd WHERE ` + where + ` AND (sd.metadata->'tags') ? qt.term
 ) sd
 UNION
 SELECT lt.search_document_id FROM query_terms qt JOIN search.lexical_terms lt ON lt.term=qt.term
 ), eligible AS MATERIALIZED (
 SELECT sd.search_document_id,ko.knowledge_object_id,ko.relative_path,custody.source_lifecycle,
 ` + notesPublicationCurrentSQL("ko", "kov") + ` AS publication_current,
 (ts_rank_cd(sd.tsv,nq.query) +
 CASE WHEN EXISTS(SELECT 1 FROM query_terms qt WHERE strpos(lower(COALESCE(ko.title,sd.title,'')),qt.term)>0) THEN 2.5 ELSE 0 END +
 CASE WHEN EXISTS(SELECT 1 FROM query_terms qt WHERE strpos(lower(COALESCE(kc.structural_path,sd.summary,'')),qt.term)>0) THEN 1.4 ELSE 0 END +
 CASE WHEN EXISTS(SELECT 1 FROM query_terms qt WHERE strpos(lower(ko.relative_path || ' ' || ko.source_path),qt.term)>0) THEN 1.2 ELSE 0 END +
 CASE WHEN EXISTS(SELECT 1 FROM query_terms qt WHERE (sd.metadata->'tags') ? qt.term) THEN 1.8 ELSE 0 END)::double precision AS score
 FROM matched_ids hit JOIN search.search_documents sd USING(search_document_id)
 JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=sd.notes_object_id
 JOIN knowledge.notes_source_roots root ON root.notes_source_root_id=ko.notes_source_root_id
 JOIN knowledge.notes_object_custody custody ON custody.knowledge_object_id=ko.knowledge_object_id
 LEFT JOIN knowledge.knowledge_chunks kc ON sd.source_kind='knowledge_chunk' AND kc.knowledge_chunk_id=sd.source_id AND kc.knowledge_object_id=ko.knowledge_object_id
 LEFT JOIN knowledge.knowledge_object_versions kov ON kov.knowledge_object_version_id=kc.knowledge_object_version_id AND kov.knowledge_object_id=ko.knowledge_object_id
 CROSS JOIN native_query nq
 WHERE ` + where + ` AND ` + scope + ` AND ` + notesPublishedVersionSQL("ko", "kc", "kov", false, false) + `
 AND (sd.source_kind='knowledge_object_metadata' OR kc.knowledge_chunk_id IS NOT NULL)
 AND NOT EXISTS (SELECT 1 FROM query_phrases qp WHERE strpos(lower(COALESCE(ko.title,'') || ' ' || COALESCE(kc.structural_path,sd.summary,'') || ' ' || ko.relative_path || ' ' || sd.body),qp.phrase)=0)
 ) SELECT request.ordinal,result.search_document_id,result.knowledge_object_id
 FROM ` + notesIndexedRequests(variants, notesIndexedBudget(input)) + ` CROSS JOIN LATERAL (
 SELECT * FROM eligible WHERE source_lifecycle=request.lifecycle AND (NOT request.require_current OR publication_current)
 ORDER BY score DESC,relative_path,search_document_id LIMIT request.candidate_limit
 ) result ORDER BY request.ordinal,result.score DESC,result.relative_path,result.search_document_id`
	return notesSearchQuery{SQL: query, Args: args}, nil
}

func buildIndexedNotesSemanticCandidates(input NotesSearchInput, settings EmbeddingSettings, vector []float32, variants []notesLexicalVariant) (notesSearchQuery, error) {
	args := []any{embeddingVectorLiteral(vector), firstNonEmpty(settings.RuntimeKey, EmbeddingRuntimeOllama), firstNonEmpty(settings.ModelKey, EmbeddingModelMXBAIEmbedLarge), firstPositiveInt(settings.Dimensions, DefaultEmbeddingDimensions)}
	scope := notesIndexedScopeSQL(input, &args)
	for _, phrase := range input.Phrases {
		args = append(args, strings.ToLower(phrase))
		scope += fmt.Sprintf(" AND strpos(lower(COALESCE(ko.title,'') || ' ' || COALESCE(kc.structural_path,'') || ' ' || ko.relative_path || ' ' || kc.chunk_text),$%d)>0", len(args))
	}
	// A direct distance ORDER BY/LIMIT permits HNSW. Explicitly small scopes and
	// archived groups use exact sorting so rare eligible sources cannot be lost
	// behind a global ANN budget. Admission is still applied to the selected IDs.
	selectSQL := `
 SELECT sd.search_document_id,ko.knowledge_object_id,(ce.embedding <=> $1::vector) AS distance
 FROM knowledge.chunk_embeddings ce
 JOIN knowledge.knowledge_chunks kc ON kc.knowledge_chunk_id=ce.knowledge_chunk_id AND kc.knowledge_object_id=ce.knowledge_object_id
 AND kc.chunk_hash=ce.chunk_hash AND kc.chunker_version=ce.chunker_version
 JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=ce.knowledge_object_id
 JOIN knowledge.notes_source_roots root ON root.notes_source_root_id=ko.notes_source_root_id
 JOIN knowledge.notes_object_custody custody ON custody.knowledge_object_id=ko.knowledge_object_id
 LEFT JOIN knowledge.knowledge_object_versions kov ON kov.knowledge_object_version_id=kc.knowledge_object_version_id AND kov.knowledge_object_id=ko.knowledge_object_id
 JOIN search.search_documents sd ON sd.source_kind='knowledge_chunk' AND sd.source_id=kc.knowledge_chunk_id AND sd.index_version='knowledge_bm25_v1'
 WHERE ce.active=true AND ce.status='active' AND ce.runtime_key=$2 AND ce.model_key=$3 AND ce.dimensions=$4
 AND kc.status IN ('created','indexed')
 AND (ce.knowledge_object_version_id IS NULL OR kc.knowledge_object_version_id IS NULL OR ce.knowledge_object_version_id=kc.knowledge_object_version_id)
 AND ` + scope + `
 AND ` + notesPublishedVersionSQL("ko", "kc", "kov", true, false) + ` AND ` + notesHybridVersionSQL(input.Mode) + `
	`
	parts := make([]string, 0, len(variants))
	for ordinal, variant := range variants {
		part := selectSQL
		order := "(ce.embedding <=> $1::vector) + 0.0,ko.relative_path,sd.search_document_id"
		if variant.Lifecycle == SourceLifecycleFilterActive && !notesIndexedExactScope(input) {
			part = strings.Replace(part, "FROM knowledge.chunk_embeddings ce", "FROM ann ce", 1)
			part = strings.Replace(part, "(ce.embedding <=> $1::vector) AS distance", "ce.distance AS distance", 1)
			order = "ce.distance,ko.relative_path,sd.search_document_id"
		} else if variant.Lifecycle == SourceLifecycleFilterArchived {
			part = strings.Replace(part, "JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=ce.knowledge_object_id", `JOIN archived_objects archived ON archived.knowledge_object_id=ce.knowledge_object_id
 JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=ce.knowledge_object_id`, 1)
		}
		part += fmt.Sprintf(" AND custody.source_lifecycle='%s'", variant.Lifecycle)
		if variant.Current {
			part += " AND " + notesPublicationCurrentSQL("ko", "kov")
		}
		part += fmt.Sprintf(" ORDER BY %s LIMIT %d", order, notesIndexedBudget(input))
		parts = append(parts, fmt.Sprintf("SELECT %d ordinal,result.* FROM (%s) result", ordinal, part))
	}
	query := fmt.Sprintf(`WITH ann AS MATERIALIZED (
 SELECT ce.*,(embedding <=> $1::vector) AS distance FROM knowledge.chunk_embeddings ce
 WHERE active=true AND status='active' AND runtime_key=$2 AND model_key=$3 AND dimensions=$4
 ORDER BY embedding <=> $1::vector LIMIT %d
 ), archived_objects AS MATERIALIZED (
 SELECT pointer.knowledge_object_id FROM knowledge.notes_current_custody pointer
 JOIN storage.workspace_lifecycle_events event ON event.workspace_lifecycle_event_id=pointer.workspace_lifecycle_event_id
 WHERE event.to_state='archived'
 ) SELECT ordinal,search_document_id,knowledge_object_id FROM (`, notesIndexedBudget(input)) + strings.Join(parts, " UNION ALL ") + ") candidates ORDER BY ordinal,distance,search_document_id"
	return notesSearchQuery{SQL: query, Args: args}, nil
}

func notesIndexedExactScope(input NotesSearchInput) bool {
	if input.indexedExactScope != nil {
		return *input.indexedExactScope
	}
	return input.ProjectID != "" || input.NotesSourceRootID != "" || input.Path != "" || input.FileClass != "" || input.SourceCategory != "" || input.SourceNodeKey != "" || len(input.Tags) > 0 || len(input.Phrases) > 0 || input.After != "" || input.Before != ""
}

type notesIndexedWanted struct {
	Ordinal int    `json:"ordinal"`
	ID      string `json:"id"`
	Limit   int    `json:"candidate_limit"`
}

func notesIndexedWantedJSON(input NotesSearchInput, variants []notesLexicalVariant, semantic bool) (string, error) {
	lane := input.indexed.lexical
	if semantic {
		lane = input.indexed.semantic
	}
	if len(variants) == 0 {
		variants = []notesLexicalVariant{{input.SourceLifecycle, input.RequireCurrent, input.Limit}}
	}
	wanted := []notesIndexedWanted{}
	for ordinal, variant := range variants {
		for _, id := range lane[variant] {
			wanted = append(wanted, notesIndexedWanted{ordinal, id, notesSearchCandidateLimit(variant.Limit)})
		}
	}
	raw, err := json.Marshal(wanted)
	return string(raw), err
}

func buildIndexedNotesLexicalHydration(input NotesSearchInput, variants []notesLexicalVariant) (notesSearchQuery, error) {
	wanted, err := notesIndexedWantedJSON(input, variants, false)
	if err != nil {
		return notesSearchQuery{}, err
	}
	terms, _ := notesSearchJSONList(notesSearchTerms(input))
	phrases, _ := notesSearchJSONList(input.Phrases)
	args := []any{input.Query, terms, phrases, wanted}
	objectsSQL := notesSearchObjectsSQL(NotesSearchInput{SourceLifecycle: SourceLifecycleFilterAll, visibleObjectIDsJSON: input.visibleObjectIDsJSON}, &args)
	query := `WITH ` + notesIndexedQueryCTE() + `,
 wanted AS (SELECT * FROM jsonb_to_recordset($4::jsonb) AS w(ordinal int,id text,candidate_limit int)),
 visible_objects AS MATERIALIZED (` + objectsSQL + `),
 matching AS MATERIALIZED (
 SELECT sd.search_document_id,ko.knowledge_object_id,
 EXISTS(SELECT 1 FROM query_terms qt WHERE strpos(lower(COALESCE(ko.title,sd.title,'')),qt.term)>0) title_match,
 EXISTS(SELECT 1 FROM query_terms qt WHERE strpos(lower(COALESCE(kc.structural_path,sd.summary,'')),qt.term)>0) heading_match,
 EXISTS(SELECT 1 FROM query_terms qt WHERE strpos(lower(ko.relative_path || ' ' || ko.source_path),qt.term)>0) path_match,
 EXISTS(SELECT 1 FROM query_terms qt WHERE (sd.metadata->'tags') ? qt.term) tag_match,
 EXISTS(SELECT 1 FROM query_terms qt WHERE strpos(lower(sd.body),qt.term)>0) body_match,
 EXISTS(SELECT 1 FROM query_phrases qp WHERE strpos(lower(COALESCE(ko.title,'') || ' ' || COALESCE(kc.structural_path,sd.summary,'') || ' ' || ko.relative_path || ' ' || sd.body),qp.phrase)>0) phrase_match,
 ts_rank_cd(sd.tsv,nq.query)::double precision fts_score
 FROM (SELECT DISTINCT id FROM wanted) w JOIN search.search_documents sd ON sd.search_document_id=w.id
 JOIN visible_objects ko ON ko.knowledge_object_id=sd.notes_object_id
 LEFT JOIN knowledge.knowledge_chunks kc ON sd.source_kind='knowledge_chunk' AND kc.knowledge_chunk_id=sd.source_id
 CROSS JOIN native_query nq
 ), scored AS (
 SELECT matching.*, (CASE WHEN title_match THEN 2.5 ELSE 0 END + CASE WHEN heading_match THEN 1.4 ELSE 0 END +
 CASE WHEN path_match THEN 1.2 ELSE 0 END + CASE WHEN tag_match THEN 1.8 ELSE 0 END + CASE WHEN phrase_match THEN 3.0 ELSE 0 END)::double precision boost_score
 FROM matching
 ), ranked AS (
 SELECT w.ordinal,w.candidate_limit,s.*,(s.fts_score+s.boost_score) AS final_score,
 row_number() OVER (PARTITION BY w.ordinal ORDER BY s.fts_score+s.boost_score DESC,ko.relative_path,s.search_document_id) position
 FROM wanted w JOIN scored s ON s.search_document_id=w.id JOIN visible_objects ko USING(knowledge_object_id)
 ) SELECT `
	if len(variants) > 0 {
		query += "ranked.ordinal,"
	}
	query += `sd.search_document_id,sd.source_kind,ko.knowledge_object_id,COALESCE(kc.knowledge_object_version_id,''),COALESCE(kc.knowledge_chunk_id,''),
 root.notes_source_root_id,root.root_kind,ko.source_node_key,COALESCE(ko.project_id,''),ko.relative_path,ko.source_path,
 COALESCE(NULLIF(ko.title,''),NULLIF(sd.title,''),ko.relative_path),ko.file_class,
 COALESCE(sd.metadata->>'text_source',''),COALESCE(sd.metadata->>'extraction_status',''),COALESCE((sd.metadata->>'metadata_only')::boolean,false),
 COALESCE(kc.chunk_index,0),COALESCE(kc.structural_path,sd.summary,''),
 CASE WHEN ranked.fts_score>0 THEN ts_headline('simple',sd.body,nq.query,'MaxWords=32, MinWords=8, ShortWord=3') ELSE left(sd.body,240) END,
 ranked.final_score,0::double precision,ranked.fts_score,ranked.boost_score,ranked.final_score,
 sd.source_created_at,sd.source_modified_at,COALESCE(sd.recency_at,ko.recency_at),COALESCE(NULLIF(sd.recency_basis,''),ko.recency_basis),
 COALESCE(kov.observed_at,ko.last_seen_at),sd.indexed_at,
 ranked.title_match,ranked.heading_match,ranked.path_match,ranked.tag_match,ranked.body_match,ranked.phrase_match,
 ` + notesSearchReadContextSQL("root", "ko", "kov", false) + `,COALESCE(kov.source_hash,''),
 (COALESCE(kc.metadata->>'text_source','')='metadata_text' OR COALESCE(sd.metadata->>'text_source','')='metadata_text' OR COALESCE(sd.metadata->'metadata_only'='true'::jsonb,false))
 FROM ranked JOIN search.search_documents sd USING(search_document_id)
 JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=ranked.knowledge_object_id
 JOIN knowledge.notes_source_roots root ON root.notes_source_root_id=ko.notes_source_root_id
 LEFT JOIN knowledge.knowledge_chunks kc ON sd.source_kind='knowledge_chunk' AND kc.knowledge_chunk_id=sd.source_id AND kc.knowledge_object_id=ko.knowledge_object_id
 LEFT JOIN knowledge.knowledge_object_versions kov ON kov.knowledge_object_version_id=kc.knowledge_object_version_id AND kov.knowledge_object_id=ko.knowledge_object_id
 CROSS JOIN native_query nq WHERE ranked.position<=ranked.candidate_limit
 ORDER BY ranked.ordinal,ranked.position`
	return notesSearchQuery{SQL: query, Args: args}, nil
}

func buildIndexedNotesSemanticHydration(input NotesSearchInput, settings EmbeddingSettings, vector []float32, variants []notesLexicalVariant) (notesSearchQuery, error) {
	wanted, err := notesIndexedWantedJSON(input, variants, true)
	if err != nil {
		return notesSearchQuery{}, err
	}
	args := []any{embeddingVectorLiteral(vector), wanted}
	objectsSQL := notesSearchObjectsSQL(NotesSearchInput{SourceLifecycle: SourceLifecycleFilterAll, visibleObjectIDsJSON: input.visibleObjectIDsJSON}, &args)
	query := `WITH wanted AS (SELECT * FROM jsonb_to_recordset($2::jsonb) AS w(ordinal int,id text,candidate_limit int)),
 visible_objects AS MATERIALIZED (` + objectsSQL + `), ranked AS (
 SELECT w.ordinal,w.candidate_limit,sd.search_document_id,ko.knowledge_object_id,kc.knowledge_chunk_id,ce.embedding <=> $1::vector distance,
 row_number() OVER (PARTITION BY w.ordinal ORDER BY ce.embedding <=> $1::vector,ko.relative_path,sd.search_document_id) position
 FROM wanted w JOIN search.search_documents sd ON sd.search_document_id=w.id
 JOIN knowledge.knowledge_chunks kc ON kc.knowledge_chunk_id=sd.source_id AND sd.source_kind='knowledge_chunk'
 JOIN visible_objects ko ON ko.knowledge_object_id=kc.knowledge_object_id
 JOIN knowledge.chunk_embeddings ce ON ce.knowledge_chunk_id=kc.knowledge_chunk_id AND ce.active=true
 AND ce.status='active' AND ce.runtime_key=`
	args = append(args, firstNonEmpty(settings.RuntimeKey, EmbeddingRuntimeOllama))
	query += fmt.Sprintf("$%d", len(args))
	args = append(args, firstNonEmpty(settings.ModelKey, EmbeddingModelMXBAIEmbedLarge))
	query += fmt.Sprintf(" AND ce.model_key=$%d", len(args))
	args = append(args, firstPositiveInt(settings.Dimensions, DefaultEmbeddingDimensions))
	query += fmt.Sprintf(" AND ce.dimensions=$%d", len(args)) + `) SELECT `
	if len(variants) > 0 {
		query += "ranked.ordinal,"
	}
	query += `sd.search_document_id,sd.source_kind,ko.knowledge_object_id,COALESCE(kc.knowledge_object_version_id,''),kc.knowledge_chunk_id,
 root.notes_source_root_id,root.root_kind,ko.source_node_key,COALESCE(ko.project_id,''),ko.relative_path,ko.source_path,
 COALESCE(NULLIF(ko.title,''),NULLIF(sd.title,''),ko.relative_path),ko.file_class,
 kc.chunk_index,COALESCE(kc.structural_path,sd.summary,''),left(kc.chunk_text,240),ranked.distance,
 sd.source_created_at,sd.source_modified_at,COALESCE(sd.recency_at,ko.recency_at),COALESCE(NULLIF(sd.recency_basis,''),ko.recency_basis),
 COALESCE(kov.observed_at,ko.last_seen_at),sd.indexed_at,
 ` + notesSearchReadContextSQL("root", "ko", "kov", true) + `,COALESCE(kov.source_hash,''),
 (COALESCE(kc.metadata->>'text_source','')='metadata_text' OR COALESCE(sd.metadata->>'text_source','')='metadata_text' OR COALESCE(sd.metadata->'metadata_only'='true'::jsonb,false))
 FROM ranked JOIN search.search_documents sd USING(search_document_id)
 JOIN knowledge.knowledge_chunks kc USING(knowledge_chunk_id)
 JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=ranked.knowledge_object_id
 JOIN knowledge.notes_source_roots root ON root.notes_source_root_id=ko.notes_source_root_id
 LEFT JOIN knowledge.knowledge_object_versions kov ON kov.knowledge_object_version_id=kc.knowledge_object_version_id AND kov.knowledge_object_id=ko.knowledge_object_id
 WHERE ranked.position<=ranked.candidate_limit ORDER BY ranked.ordinal,ranked.position`
	return notesSearchQuery{SQL: query, Args: args}, nil
}
