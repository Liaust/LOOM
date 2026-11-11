package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// All state is local to one repeatable-read request. Candidates are not access
// grants: live admission is shared only after both lanes have retrieved them.
type notesIndexedSearch struct {
	bm25              bool
	lexical           map[notesLexicalVariant][]string
	semantic          map[notesLexicalVariant][]string
	exhausted         map[notesLexicalVariant]bool
	documentObjects   map[string]string
	visible           map[string]bool
	lexicalScores     map[string]float64
	lexicalFallback   map[string]bool
	semanticCoverage  map[string]notesSemanticCoverage
	semanticDistances map[string]float64
	filePartitions    map[notesLexicalVariant]notesSearchPartition
}

const notesIndexedObjectCandidateLimit = 4

func annotateIndexedNotesLexicalResult(input NotesSearchInput, result *NotesSearchResult) {
	if input.indexed != nil {
		result.LexicalMethod = "postgres_fts"
		if input.indexed.bm25 {
			result.LexicalMethod = "pg_search_bm25"
			result.BM25Score = input.indexed.lexicalScores[result.SearchDocumentID]
			result.FTSScore = 0
		}
		result.lexicalFallback = input.indexed.lexicalFallback[result.SearchDocumentID]
		if input.indexed.semanticCoverage != nil {
			result.SemanticCoverage = input.indexed.semanticCoverage[result.SearchDocumentID]
			if result.SemanticCoverage == "" {
				result.SemanticCoverage = notesSemanticCoverageUnknown
			}
		}
	}
}

// Object IDs identify immutable source/version/root bindings in this request's
// repeatable-read snapshot. Negative admissions are reusable here too; nothing
// from this cache survives a transaction or skips a later request's fences.
type notesIndexedAdmission struct {
	tx      *sql.Tx
	checked map[string]bool
}

func (a *notesIndexedAdmission) admit(ids []string, lookup func([]string) ([]string, error)) ([]string, error) {
	missing := []string{}
	for _, id := range ids {
		if _, checked := a.checked[id]; !checked {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		admitted, err := lookup(missing)
		if err != nil {
			return nil, err
		}
		for _, id := range missing {
			a.checked[id] = false
		}
		for _, id := range admitted {
			a.checked[id] = true
		}
	}
	visible := []string{}
	for _, id := range ids {
		if a.checked[id] {
			visible = append(visible, id)
		}
	}
	return visible, nil
}

func (s *Service) admitIndexedNotesObjects(ctx context.Context, input *NotesSearchInput, ids []string) ([]string, error) {
	if input.readTx == nil {
		return nil, fmt.Errorf("indexed Notes admission requires a request snapshot")
	}
	if input.indexedAdmission == nil || input.indexedAdmission.tx != input.readTx {
		input.indexedAdmission = &notesIndexedAdmission{tx: input.readTx, checked: map[string]bool{}}
	}
	return input.indexedAdmission.admit(ids, func(missing []string) ([]string, error) {
		raw, err := json.Marshal(missing)
		if err != nil {
			return nil, err
		}
		delta := *input
		delta.candidateObjectIDsJSON = string(raw)
		visible, err := s.loadNotesVisibleObjectIDs(ctx, delta)
		if err != nil {
			return nil, err
		}
		var admitted []string
		err = json.Unmarshal([]byte(visible), &admitted)
		return admitted, err
	})
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
	if err := s.prepareNotesNativeQuery(ctx, input); err != nil {
		return err
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
	state := &notesIndexedSearch{lexical: map[notesLexicalVariant][]string{}, semantic: map[notesLexicalVariant][]string{}, exhausted: map[notesLexicalVariant]bool{}, lexicalScores: map[string]float64{}, lexicalFallback: map[string]bool{}, semanticDistances: map[string]float64{}}
	state.bm25 = s.notesSearchBackend == NotesSearchBackendBM25 && !notesBM25LiteralQuery(*input)
	variants := notesLexicalVariants(*input)
	objects := map[string]bool{}
	documentObjects := map[string]string{}
	load := func(query notesSearchQuery, lane map[notesLexicalVariant][]string, lexical bool) error {
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
			var truncated bool
			var score float64
			var unfiltered bool
			dest := []any{&ordinal, &documentID, &objectID, &truncated, &score}
			if lexical {
				dest = append(dest, &unfiltered)
			}
			if err := rows.Scan(dest...); err != nil {
				return err
			}
			if ordinal < 0 || ordinal >= len(variants) {
				return fmt.Errorf("invalid indexed Notes partition %d", ordinal)
			}
			key := variants[ordinal]
			state.exhausted[key] = state.exhausted[key] || truncated
			if documentID == "" {
				continue // A capped pool can have no published/current survivors.
			}
			if lexical {
				state.lexicalScores[documentID] = score
				state.lexicalFallback[documentID] = unfiltered
			} else {
				state.semanticDistances[documentID] = score
			}
			lane[key] = append(lane[key], documentID)
			objects[objectID] = true
			documentObjects[documentID] = objectID
		}
		return rows.Err()
	}
	if mode != NotesSearchModeSemantic {
		build := buildIndexedNotesLexicalCandidates
		if state.bm25 {
			build = buildBM25NotesLexicalCandidates
		}
		query, err := build(*input, variants)
		if err != nil {
			return err
		}
		if err := load(query, state.lexical, true); err != nil {
			return err
		}
	}
	if mode != NotesSearchModeLexical && fallback == "" {
		if _, err := s.notesSearchQueryVector(ctx, *input, settings); err != nil && (mode != NotesSearchModeHybrid || !semanticSearchFallbackAllowed(err)) {
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
		if err := load(query, state.semantic, false); err != nil {
			return err
		}
		if notesIndexedUsesANN(*input) {
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
	admitted, err := s.admitIndexedNotesObjects(ctx, input, ids)
	if err != nil {
		return err
	}
	raw, err = json.Marshal(admitted)
	if err != nil {
		return err
	}
	input.visibleObjectIDsJSON = string(raw)
	input.indexed = state
	visible := map[string]bool{}
	for _, id := range admitted {
		visible[id] = true
	}
	state.documentObjects, state.visible = documentObjects, visible
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
	// Do not run the unfiltered question merely to fill an internal pool. First
	// exhaust ordinary bounded refill, then consider actual admitted file count.
	if mode != NotesSearchModeSemantic && !state.bm25 && input.nativeFiltered && input.indexedUnfiltered == nil {
		input.indexedUnfiltered = notesIndexedUnfilteredVariants(*input, state, variants)
		if len(input.indexedUnfiltered) > 0 {
			input.indexed = nil
			return s.prepareIndexedNotesSearch(ctx, input, mode, fallback, settings)
		}
	}
	if mode == NotesSearchModeHybrid && fallback == "" && input.queryEmbedding != nil && input.queryEmbedding.err == nil {
		state.semanticCoverage, err = s.loadNotesSemanticCoverage(ctx, *input, settings)
		if err != nil {
			return err
		}
	}
	return nil
}

func notesIndexedUnfilteredVariants(input NotesSearchInput, state *notesIndexedSearch, variants []notesLexicalVariant) map[notesLexicalVariant]bool {
	needed := map[notesLexicalVariant]bool{}
	for _, key := range variants {
		// Diagnostic archived/current-omission partitions must not activate a
		// broader interpretation that the requested result partition did not need.
		if key.Current != input.RequireCurrent || (input.SourceLifecycle != SourceLifecycleFilterAll && key.Lifecycle != input.SourceLifecycle) {
			continue
		}
		found := map[string]bool{}
		for _, document := range state.lexical[key] {
			if object := state.documentObjects[document]; state.visible[object] {
				found[object] = true
			}
		}
		wanted := key.Limit
		if input.fileView && input.discoveryLimit > 0 {
			wanted = input.discoveryLimit
		}
		if len(found) < wanted {
			needed[key] = true
		}
	}
	return needed
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

func notesIndexedScopeSQL(input NotesSearchInput, args *[]any) string {
	clauses := []string{"ko.deleted_at IS NULL", visibleNotesKnowledgeRelativePathSQL("ko.relative_path")}
	clauses = append(clauses, notesSelectedRootSQL(input.boxSelection, "root", args))
	if input.fileView && input.SourceLifecycle == SourceLifecycleFilterArchived {
		// Restrict the candidate owner before expanding custody/publication.
		// An archived-only file query needs no active-corpus diagnostics.
		clauses = append(clauses, `ko.knowledge_object_id IN (
	 SELECT pointer.knowledge_object_id FROM knowledge.notes_current_custody pointer
	 JOIN storage.workspace_lifecycle_events event USING(workspace_lifecycle_event_id)
	 WHERE event.to_state='archived')`)
	}
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

func buildIndexedNotesSemanticCandidates(input NotesSearchInput, settings EmbeddingSettings, vector []float32, variants []notesLexicalVariant) (notesSearchQuery, error) {
	args := []any{embeddingVectorLiteral(vector), firstNonEmpty(settings.RuntimeKey, EmbeddingRuntimeOllama), firstNonEmpty(settings.ModelKey, EmbeddingModelMXBAIEmbedLarge), firstPositiveInt(settings.Dimensions, DefaultEmbeddingDimensions)}
	scope := notesIndexedScopeSQL(input, &args)
	annMembership := ""
	if input.boxSelection != nil && !input.boxSelection.AllBoxes {
		// Keep this as a point-correlated filter, not a materialized object/vector
		// join or a filter over a global Top-K. HNSW retains its direct ORDER BY.
		annMembership = ` AND EXISTS (SELECT 1 FROM knowledge.knowledge_objects ann_object
 JOIN knowledge.notes_source_roots ann_root ON ann_root.notes_source_root_id=ann_object.notes_source_root_id
 WHERE ann_object.knowledge_object_id=ce.knowledge_object_id AND ` + notesSelectedRootSQL(input.boxSelection, "ann_root", &args) + ` OFFSET 0)`
	}
	literalScope := ""
	for _, phrase := range input.Phrases {
		args = append(args, strings.ToLower(phrase))
		literalScope += fmt.Sprintf(" AND strpos(lower(COALESCE(ko.title,'') || ' ' || COALESCE(kc.structural_path,'') || ' ' || ko.relative_path || ' ' || kc.chunk_text),$%d)>0", len(args))
	}
	// A direct distance ORDER BY/LIMIT permits HNSW. Explicitly small scopes and
	// archived groups use exact sorting so rare eligible sources cannot be lost
	// behind a global ANN budget. Admission is still applied to the selected IDs.
	selectSQL := `
 SELECT sd.search_document_id,ko.knowledge_object_id,ko.relative_path,kc.knowledge_chunk_id,(ce.embedding <=> $1::vector) AS distance
 FROM knowledge.chunk_embeddings ce
 JOIN knowledge.knowledge_chunks kc ON kc.knowledge_chunk_id=ce.knowledge_chunk_id AND kc.knowledge_object_id=ce.knowledge_object_id
 AND kc.chunk_hash=ce.chunk_hash AND kc.chunker_version=ce.chunker_version
 JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=ce.knowledge_object_id
 JOIN knowledge.notes_source_roots root ON root.notes_source_root_id=ko.notes_source_root_id
 JOIN knowledge.notes_object_custody custody ON custody.knowledge_object_id=ko.knowledge_object_id
 JOIN search.search_documents sd ON sd.source_kind='knowledge_chunk' AND sd.source_id=kc.knowledge_chunk_id AND sd.index_version='knowledge_bm25_v1'
 WHERE ce.active=true AND ce.status='active' AND ce.runtime_key=$2 AND ce.model_key=$3 AND ce.dimensions=$4
 AND kc.status IN ('created','indexed')
 AND (ce.knowledge_object_version_id IS NULL OR kc.knowledge_object_version_id IS NULL OR ce.knowledge_object_version_id=kc.knowledge_object_version_id)
	 AND ` + scope + `
	`
	parts := make([]string, 0, len(variants))
	for ordinal, variant := range variants {
		part := selectSQL
		order := "(ce.embedding <=> $1::vector) + 0.0,ko.relative_path,sd.search_document_id"
		ann := variant.Lifecycle == SourceLifecycleFilterActive && notesIndexedUsesANN(input)
		if ann {
			part = strings.Replace(part, "FROM knowledge.chunk_embeddings ce", "FROM ann ce", 1)
			part = strings.Replace(part, "(ce.embedding <=> $1::vector) AS distance", "ce.distance AS distance", 1)
			// Keep the custody view keyed to each bounded ANN candidate. Flattening
			// this join can rescan the whole object view for every candidate.
			part = strings.Replace(part, "JOIN knowledge.notes_object_custody custody ON custody.knowledge_object_id=ko.knowledge_object_id", `JOIN LATERAL (
 SELECT c.* FROM knowledge.notes_object_custody c
 WHERE c.knowledge_object_id=ko.knowledge_object_id OFFSET 0
 ) custody ON true`, 1)
			order = "ce.distance,ko.relative_path,sd.search_document_id"
		} else if variant.Lifecycle == SourceLifecycleFilterArchived {
			part = strings.Replace(part, "JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=ce.knowledge_object_id", `JOIN archived_objects archived ON archived.knowledge_object_id=ce.knowledge_object_id
 JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=ce.knowledge_object_id`, 1)
		}
		part += fmt.Sprintf(" AND custody.source_lifecycle='%s'", variant.Lifecycle)
		if input.fileView && !ann {
			// File discovery needs different files even in a small exact scope;
			// diversify cheap distances before the scoped passage cap.
			part = fmt.Sprintf(`SELECT search_document_id,knowledge_object_id,relative_path,knowledge_chunk_id,distance FROM (
 SELECT pool.*,row_number() OVER (PARTITION BY knowledge_object_id ORDER BY distance,relative_path,search_document_id) object_position
 FROM (%s) pool) diverse WHERE object_position<=%d ORDER BY distance,relative_path,search_document_id LIMIT %d`, part, notesIndexedObjectCandidateLimit, notesIndexedBudget(input))
		} else {
			part += fmt.Sprintf(" ORDER BY %s LIMIT %d", order, notesIndexedBudget(input))
		}
		if ann {
			// Diversify the bounded ANN pool, then let ordinary refill enlarge it
			// when one source dominates. Never replace HNSW with a corpus sort.
			part = fmt.Sprintf(`SELECT search_document_id,knowledge_object_id,relative_path,knowledge_chunk_id,distance FROM (
 SELECT pool.*,row_number() OVER (PARTITION BY knowledge_object_id ORDER BY distance,relative_path,search_document_id) object_position
 FROM (%s) pool) diverse WHERE object_position<=%d ORDER BY distance,relative_path,search_document_id`, part, notesIndexedObjectCandidateLimit)
		}
		parts = append(parts, fmt.Sprintf("SELECT %d ordinal,%t require_current,result.* FROM (%s) result", ordinal, variant.Current, part))
	}
	query := fmt.Sprintf(`WITH ann AS MATERIALIZED (
 SELECT ce.*,(embedding <=> $1::vector) AS distance FROM knowledge.chunk_embeddings ce
 WHERE active=true AND status='active' AND runtime_key=$2 AND model_key=$3 AND dimensions=$4
 `+annMembership+`
 ORDER BY embedding <=> $1::vector LIMIT %d
 ), archived_objects AS MATERIALIZED (
 SELECT pointer.knowledge_object_id FROM knowledge.notes_current_custody pointer
 JOIN storage.workspace_lifecycle_events event ON event.workspace_lifecycle_event_id=pointer.workspace_lifecycle_event_id
 WHERE event.to_state='archived'
 ), candidate_pool AS MATERIALIZED (`, notesIndexedBudget(input)) + strings.Join(parts, " UNION ALL ") + `),
 requests AS (SELECT * FROM ` + notesIndexedRequests(variants, notesIndexedBudget(input)) + `),
 pool_status AS MATERIALIZED (
 SELECT request.ordinal,count(pool.search_document_id)>=request.candidate_limit truncated
 FROM requests request LEFT JOIN candidate_pool pool USING(ordinal) GROUP BY request.ordinal,request.candidate_limit
 ), eligible AS MATERIALIZED (
 SELECT pool.* FROM candidate_pool pool
 JOIN knowledge.knowledge_chunks kc ON kc.knowledge_chunk_id=pool.knowledge_chunk_id AND kc.knowledge_object_id=pool.knowledge_object_id
 JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=pool.knowledge_object_id
 JOIN knowledge.chunk_embeddings ce ON ce.knowledge_chunk_id=kc.knowledge_chunk_id AND ce.knowledge_object_id=ko.knowledge_object_id
 AND ce.active=true AND ce.status='active' AND ce.runtime_key=$2 AND ce.model_key=$3 AND ce.dimensions=$4
 LEFT JOIN knowledge.knowledge_object_versions kov ON kov.knowledge_object_version_id=kc.knowledge_object_version_id AND kov.knowledge_object_id=ko.knowledge_object_id
 WHERE ` + notesObservedSourcePresentSQL("ko") + ` AND ` + notesPublishedVersionSQL("ko", "kc", "kov", true, false) + ` AND ` + notesHybridVersionSQL(input.Mode) + `
 AND (NOT pool.require_current OR ` + notesPublicationCurrentSQL("ko", "kov") + `)` + literalScope + `
 ) SELECT status.ordinal,COALESCE(result.search_document_id,''),COALESCE(result.knowledge_object_id,''),status.truncated,COALESCE(result.distance,0)::double precision
 FROM pool_status status LEFT JOIN eligible result USING(ordinal) ORDER BY status.ordinal,result.distance,result.search_document_id`
	return notesSearchQuery{SQL: query, Args: args}, nil
}

func notesIndexedObjectLimit(input NotesSearchInput) int {
	if input.fileView {
		return notesIndexedObjectCandidateLimit
	}
	if notesIndexedExactScope(input) {
		return notesIndexedBudget(input)
	}
	return notesIndexedObjectCandidateLimit
}

func notesIndexedExactScope(input NotesSearchInput) bool {
	if input.indexedExactScope != nil {
		return *input.indexedExactScope
	}
	// A default or explicitly selected Box can hold the whole working corpus.
	// Do not count it on every request or infer that it is an exact/small scope.
	return notesIndexedHasContentScope(input) || len(input.Phrases) > 0
}

func notesIndexedHasScope(input NotesSearchInput) bool {
	return (input.boxSelection != nil && !input.boxSelection.AllBoxes) || notesIndexedHasContentScope(input)
}

func notesIndexedHasContentScope(input NotesSearchInput) bool {
	return input.ProjectID != "" || input.NotesSourceRootID != "" || input.Path != "" || input.FileClass != "" || input.SourceCategory != "" || input.SourceNodeKey != "" || len(input.Tags) > 0 || input.After != "" || input.Before != ""
}

func notesIndexedUsesANN(input NotesSearchInput) bool {
	return !notesIndexedExactScope(input) && !notesIndexedHasContentScope(input)
}

type notesIndexedWanted struct {
	Ordinal  int     `json:"ordinal"`
	ID       string  `json:"id"`
	Limit    int     `json:"candidate_limit"`
	Score    float64 `json:"score"`
	Fallback bool    `json:"fallback"`
}

func notesIndexedWantedJSON(input NotesSearchInput, variants []notesLexicalVariant, semantic bool) (string, error) {
	if len(variants) == 0 {
		variants = []notesLexicalVariant{{input.SourceLifecycle, input.RequireCurrent, input.Limit}}
	}
	wanted := []notesIndexedWanted{}
	for ordinal, variant := range variants {
		limit := notesSearchCandidateLimit(variant.Limit)
		for _, id := range notesIndexedLaneIDs(input.indexed, variant, semantic) {
			if input.indexed.filePartitions != nil && !notesSelectedFileNeedsLane(input.indexed, variant, id, semantic) {
				continue
			}
			wanted = append(wanted, notesIndexedWanted{ordinal, id, limit, input.indexed.lexicalScores[id], input.indexed.lexicalFallback[id]})
		}
	}
	raw, err := json.Marshal(wanted)
	return string(raw), err
}

func notesIndexedLaneIDs(state *notesIndexedSearch, variant notesLexicalVariant, semantic bool) []string {
	lane := state.lexical
	if semantic {
		lane = state.semantic
	}
	ids := []string{}
	for _, id := range lane[variant] {
		if state.visible != nil && !state.visible[state.documentObjects[id]] {
			continue
		}
		ids = append(ids, id)
		if len(ids) == notesSearchCandidateLimit(variant.Limit) {
			break
		}
	}
	return ids
}

func buildIndexedNotesLexicalHydration(input NotesSearchInput, variants []notesLexicalVariant) (notesSearchQuery, error) {
	wanted, err := notesIndexedWantedJSON(input, variants, false)
	if err != nil {
		return notesSearchQuery{}, err
	}
	args := append(notesIndexedQueryArgs(input), wanted)
	objectsSQL, err := notesIndexedHydrationObjectsSQL(input, &args)
	if err != nil {
		return notesSearchQuery{}, err
	}
	excerpt := notesIndexedExcerptSQL("sd.body")
	excerptCTE, excerptJoin := "", ""
	if input.indexed != nil && input.indexed.bm25 {
		terms, err := notesBM25QueryText(input)
		if err != nil {
			return notesSearchQuery{}, err
		}
		args = append(args, terms)
		// Highlight only the already selected, admitted IDs. The engine pushes
		// their exact key set into its scan; no candidate-pool highlighting.
		excerptCTE = fmt.Sprintf(`bm25_excerpts AS MATERIALIZED (
 SELECT d.id,paradedb.snippet(d.body,start_tag=>'',end_tag=>'',max_num_chars=>700) excerpt
 FROM knowledge.notes_bm25_documents d
 WHERE d.id=ANY(ARRAY(SELECT DISTINCT id FROM wanted))
 AND d.id @@@ paradedb.match('body',$%d::text)
 LIMIT (SELECT count(DISTINCT id) FROM wanted)),`, len(args))
		excerptJoin = " LEFT JOIN bm25_excerpts bx ON bx.id=sd.search_document_id "
		excerpt = "COALESCE(NULLIF(bx.excerpt,'')," + excerpt + ")"
	}
	query := `WITH ` + notesIndexedQueryCTE(input) + `,
 wanted AS (SELECT * FROM jsonb_to_recordset($4::jsonb) AS w(ordinal int,id text,candidate_limit int,score double precision,fallback boolean)),
 ` + excerptCTE + `visible_objects AS MATERIALIZED (` + objectsSQL + `),
 matching AS MATERIALIZED (
 SELECT sd.search_document_id,ko.knowledge_object_id,
 EXISTS(SELECT 1 FROM query_terms qt WHERE strpos(lower(COALESCE(ko.title,sd.title,'')),qt.term)>0) title_match,
 EXISTS(SELECT 1 FROM query_terms qt WHERE strpos(lower(COALESCE(kc.structural_path,sd.summary,'')),qt.term)>0) heading_match,
 EXISTS(SELECT 1 FROM query_terms qt WHERE strpos(lower(ko.relative_path || ' ' || ko.source_path),qt.term)>0) path_match,
 EXISTS(SELECT 1 FROM query_terms qt WHERE (sd.metadata->'tags') ? qt.term) tag_match,
 EXISTS(SELECT 1 FROM query_terms qt WHERE strpos(lower(sd.body),qt.term)>0) body_match,
 EXISTS(SELECT 1 FROM query_phrases qp WHERE strpos(lower(COALESCE(ko.title,'') || ' ' || COALESCE(kc.structural_path,sd.summary,'') || ' ' || ko.relative_path || ' ' || sd.body),qp.phrase)>0) phrase_match
 FROM (SELECT DISTINCT id FROM wanted) w JOIN search.search_documents sd ON sd.search_document_id=w.id
 JOIN visible_objects ko ON ko.knowledge_object_id=sd.notes_object_id
 LEFT JOIN knowledge.knowledge_chunks kc ON sd.source_kind='knowledge_chunk' AND kc.knowledge_chunk_id=sd.source_id
 CROSS JOIN native_query nq
 ), ranked AS (
 SELECT w.ordinal,w.candidate_limit,s.*,w.score AS final_score,
 row_number() OVER (PARTITION BY w.ordinal ORDER BY w.fallback,w.score DESC,ko.relative_path,s.search_document_id) position
 FROM wanted w JOIN matching s ON s.search_document_id=w.id JOIN visible_objects ko USING(knowledge_object_id)
 ) SELECT `
	if len(variants) > 0 {
		query += "ranked.ordinal,"
	}
	query += `sd.search_document_id,sd.source_kind,ko.knowledge_object_id,COALESCE(kc.knowledge_object_version_id,''),COALESCE(kc.knowledge_chunk_id,''),
 root.notes_source_root_id,root.root_kind,ko.source_node_key,COALESCE(ko.project_id,''),ko.relative_path,ko.source_path,
 COALESCE(NULLIF(ko.title,''),NULLIF(sd.title,''),ko.relative_path),ko.file_class,
 COALESCE(sd.metadata->>'text_source',''),COALESCE(sd.metadata->>'extraction_status',''),COALESCE((sd.metadata->>'metadata_only')::boolean,false),
 COALESCE(kc.chunk_index,0),COALESCE(kc.structural_path,sd.summary,''),
 ` + excerpt + `,
 ranked.final_score,0::double precision,ranked.final_score,0::double precision,ranked.final_score,
 sd.source_created_at,sd.source_modified_at,COALESCE(sd.recency_at,ko.recency_at),COALESCE(NULLIF(sd.recency_basis,''),ko.recency_basis),
 COALESCE(kov.observed_at,ko.last_seen_at),sd.indexed_at,
 ranked.title_match,ranked.heading_match,ranked.path_match,ranked.tag_match,ranked.body_match,ranked.phrase_match,
 ` + notesSearchReadContextSQL("root", "ko", "kov", false) + `,COALESCE(kov.source_hash,''),
 (COALESCE(kc.metadata->>'text_source','')='metadata_text' OR COALESCE(sd.metadata->>'text_source','')='metadata_text' OR COALESCE(sd.metadata->'metadata_only'='true'::jsonb,false))
 FROM ranked JOIN search.search_documents sd USING(search_document_id)
 ` + excerptJoin + `
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
	args := append(notesIndexedQueryArgs(input), embeddingVectorLiteral(vector), wanted)
	objectsSQL, err := notesIndexedHydrationObjectsSQL(input, &args)
	if err != nil {
		return notesSearchQuery{}, err
	}
	query := `WITH ` + notesIndexedQueryCTE(input) + `,
 wanted AS (SELECT * FROM jsonb_to_recordset($5::jsonb) AS w(ordinal int,id text,candidate_limit int)),
 visible_objects AS MATERIALIZED (` + objectsSQL + `), ranked AS (
 SELECT w.ordinal,w.candidate_limit,sd.search_document_id,ko.knowledge_object_id,kc.knowledge_chunk_id,ce.embedding <=> $4::vector distance,
 row_number() OVER (PARTITION BY w.ordinal ORDER BY ce.embedding <=> $4::vector,ko.relative_path,sd.search_document_id) position
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
 kc.chunk_index,COALESCE(kc.structural_path,sd.summary,''),` + notesIndexedExcerptSQL("kc.chunk_text") + `,ranked.distance,
 sd.source_created_at,sd.source_modified_at,COALESCE(sd.recency_at,ko.recency_at),COALESCE(NULLIF(sd.recency_basis,''),ko.recency_basis),
 COALESCE(kov.observed_at,ko.last_seen_at),sd.indexed_at,
 ` + notesSearchReadContextSQL("root", "ko", "kov", true) + `,COALESCE(kov.source_hash,''),
 (COALESCE(kc.metadata->>'text_source','')='metadata_text' OR COALESCE(sd.metadata->>'text_source','')='metadata_text' OR COALESCE(sd.metadata->'metadata_only'='true'::jsonb,false))
 FROM ranked JOIN search.search_documents sd USING(search_document_id)
 JOIN knowledge.knowledge_chunks kc USING(knowledge_chunk_id)
 JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=ranked.knowledge_object_id
 JOIN knowledge.notes_source_roots root ON root.notes_source_root_id=ko.notes_source_root_id
 LEFT JOIN knowledge.knowledge_object_versions kov ON kov.knowledge_object_version_id=kc.knowledge_object_version_id AND kov.knowledge_object_id=ko.knowledge_object_id
 CROSS JOIN native_query nq WHERE ranked.position<=ranked.candidate_limit ORDER BY ranked.ordinal,ranked.position`
	return notesSearchQuery{SQL: query, Args: args}, nil
}

func notesIndexedExcerptSQL(body string) string {
	// Body parsing is restricted to hydrated, admitted passages. Field-only or
	// semantic-only hits still expose real surrounding source text, not a blank
	// headline. Both lanes reuse the prepared discovery query, never its grammar
	// fallback, and keep the existing compact file ceiling.
	return `CASE WHEN to_tsvector('simple',` + body + `) @@ nq.query
 THEN left(ts_headline('simple',` + body + `,nq.query,'MaxFragments=1, MaxWords=56, MinWords=24, ShortWord=0'),700)
 ELSE left(` + body + `,700) END`
}
