package knowledge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Engine-owned Top-K is materialized before LOOM's exact current-publication
// and access checks. Scores are BM25, never recomputed from the candidate pool.
func buildBM25NotesLexicalCandidates(input NotesSearchInput, variants []notesLexicalVariant) (notesSearchQuery, error) {
	args := notesIndexedQueryArgs(input)
	terms, err := notesBM25QueryText(input)
	if err != nil {
		return notesSearchQuery{}, err
	}
	args = append(args, terms)
	// These fields often repeat the same filename or heading. Use the engine's
	// best-field score so that repetition is not independent relevance evidence.
	filters := []string{`d.id @@@ paradedb.disjunction_max(ARRAY[
 paradedb.match('body',$4::text),paradedb.match('title',$4::text),
 paradedb.match('heading',$4::text),paradedb.match('path',$4::text)],0)`}
	if input.boxSelection != nil && !input.boxSelection.AllBoxes {
		filters = append(filters, `EXISTS (SELECT 1 FROM knowledge.notes_source_roots box_root WHERE box_root.notes_source_root_id=d.root_id AND `+notesSelectedRootSQL(input.boxSelection, "box_root", &args)+`)`)
	}
	add := func(clause string, value any) {
		args = append(args, value)
		filters = append(filters, fmt.Sprintf(clause, len(args)))
	}
	for _, f := range []struct{ field, value string }{
		{"project_id", input.ProjectID}, {"root_id", input.NotesSourceRootID}, {"node_key", input.SourceNodeKey},
		{"file_class", input.FileClass},
	} {
		if f.value != "" {
			add("d."+f.field+"=$%d::text", f.value)
		}
	}
	if input.SourceCategory != "" {
		add("("+sourceCategorySQL("d.root_kind")+")=$%d::text", input.SourceCategory)
	}
	if input.Path != "" {
		add("d.path ILIKE $%d::text", "%"+input.Path+"%")
	}
	for _, tag := range normalizeTags(input.Tags) {
		add("d.tags ? $%d::text", tag)
	}
	if input.After != "" {
		value, _ := ParseAbsoluteTimestamp(input.After)
		add("d.recency_at >= $%d::timestamptz", value)
	}
	if input.Before != "" {
		value, _ := ParseAbsoluteTimestamp(input.Before)
		add("d.recency_at < $%d::timestamptz", value)
	}
	parts := []string{}
	for ordinal, variant := range variants {
		if variant.Lifecycle != SourceLifecycleFilterActive && variant.Lifecycle != SourceLifecycleFilterArchived {
			return notesSearchQuery{}, fmt.Errorf("invalid BM25 lifecycle")
		}
		where := strings.Join(filters, " AND ") + fmt.Sprintf(" AND d.lifecycle='%s'", variant.Lifecycle)
		if variant.Current {
			where += " AND d.is_current"
		}
		parts = append(parts, fmt.Sprintf(`SELECT %d ordinal,%t require_current,hit.* FROM (
 SELECT d.id AS search_document_id,d.object_id AS knowledge_object_id,pdb.score(d.id)::double precision AS score
 FROM knowledge.notes_bm25_documents d WHERE %s
 ORDER BY pdb.score(d.id) DESC,d.id_sort LIMIT %d) hit`, ordinal, variant.Current, where, notesIndexedBudget(input)))
	}
	query := `WITH ` + notesIndexedQueryCTE(input) + `, candidate_pool AS MATERIALIZED (` + strings.Join(parts, " UNION ALL ") + `),
 requests AS (SELECT * FROM ` + notesIndexedRequests(variants, notesIndexedBudget(input)) + `),
 pool_status AS MATERIALIZED (
 SELECT request.ordinal,count(pool.search_document_id)>=request.candidate_limit truncated
 FROM requests request LEFT JOIN candidate_pool pool USING(ordinal) GROUP BY request.ordinal,request.candidate_limit
 ), eligible AS MATERIALIZED (
 SELECT pool.*,ko.relative_path,row_number() OVER(PARTITION BY pool.ordinal,ko.knowledge_object_id ORDER BY pool.score DESC,pool.search_document_id) object_position
 FROM candidate_pool pool JOIN search.search_documents sd ON sd.search_document_id=pool.search_document_id AND sd.notes_object_id=pool.knowledge_object_id
 JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=pool.knowledge_object_id
 LEFT JOIN knowledge.knowledge_chunks kc ON sd.source_kind='knowledge_chunk' AND kc.knowledge_chunk_id=sd.source_id AND kc.knowledge_object_id=ko.knowledge_object_id
 LEFT JOIN knowledge.knowledge_object_versions kov ON kov.knowledge_object_version_id=kc.knowledge_object_version_id AND kov.knowledge_object_id=ko.knowledge_object_id
 WHERE ` + notesObservedSourcePresentSQL("ko") + ` AND ` + notesPublishedVersionSQL("ko", "kc", "kov", false, false) + `
 AND (sd.source_kind='knowledge_object_metadata' OR kc.status IN ('created','indexed'))
 AND (NOT pool.require_current OR ` + notesPublicationCurrentSQL("ko", "kov") + `)
 AND NOT EXISTS (SELECT 1 FROM query_phrases qp WHERE strpos(lower(sd.notes_fields || ' ' || sd.body),qp.phrase)=0)
 AND NOT EXISTS (SELECT 1 FROM all_query_terms qt WHERE strpos(qt.term,'_')>0
 AND NOT COALESCE((sd.metadata->'tags') ? qt.term,false) AND strpos(lower(sd.notes_fields || ' ' || sd.body),qt.term)=0)
 ) SELECT status.ordinal,COALESCE(result.search_document_id,''),COALESCE(result.knowledge_object_id,''),
 status.truncated,COALESCE(result.score,0),false
 FROM pool_status status LEFT JOIN eligible result ON result.ordinal=status.ordinal AND result.object_position<=` + fmt.Sprint(notesIndexedObjectLimit(input)) + `
 ORDER BY status.ordinal,result.score DESC,result.relative_path,result.search_document_id`
	return notesSearchQuery{SQL: query, Args: args}, nil
}

func notesBM25QueryText(input NotesSearchInput) (string, error) {
	var prepared struct {
		Terms []string `json:"discovery_terms"`
	}
	if input.preparedQuery != "" {
		if err := json.Unmarshal([]byte(input.preparedQuery), &prepared); err != nil {
			return "", err
		}
	} else {
		for _, term := range notesQueryTerms(input) {
			prepared.Terms = append(prepared.Terms, term.Term)
		}
	}
	return strings.Join(prepared.Terms, " "), nil
}

// Exact punctuation, short prefixes and protected phrase semantics retain their
// indexed literal route. This is query routing, not a silent engine-error retry.
func notesBM25LiteralQuery(input NotesSearchInput) bool {
	if len(input.Phrases) > 0 || len(input.Tags) > 0 {
		return true
	}
	terms := notesQueryTerms(input)
	if len(terms) == 1 && len([]rune(terms[0].Term)) < 3 {
		return true
	}
	for _, term := range terms {
		if term.Key != term.Term || strings.Contains(term.Term, "_") {
			return true
		}
	}
	return !strings.ContainsAny(strings.TrimSpace(input.Query), " \t\r\n") && strings.ContainsAny(input.Query, `/\.-`)
}
