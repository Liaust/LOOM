package knowledge

import (
	"fmt"
	"strings"
)

// Native matching/ranking precedes the bounded publication and admission work.
// GIN is not Top-K pruning: broad queries can still rank many matching vectors.
func buildIndexedNotesLexicalCandidates(input NotesSearchInput, variants []notesLexicalVariant) (notesSearchQuery, error) {
	args := notesIndexedQueryArgs(input)
	scope := notesIndexedScopeSQL(input, &args)
	where := `sd.source_kind IN ('knowledge_chunk','knowledge_object_metadata') AND sd.index_version='knowledge_bm25_v1'`
	matchSource, scopeCTE, fallbackScope := "search.search_documents", "", ""
	if notesIndexedHasScope(input) {
		matchSource = "scoped_documents"
		// Preserve the scope relation without hiding its existing native indexes.
		scopeCTE = `scoped_documents AS NOT MATERIALIZED (
 SELECT sd.search_document_id,sd.source_kind,sd.index_version,sd.notes_object_id,sd.tsv,sd.notes_fields,sd.metadata,sd.recency_at
 FROM search.search_documents sd
 JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=sd.notes_object_id
 JOIN knowledge.notes_source_roots root ON root.notes_source_root_id=ko.notes_source_root_id
 WHERE ` + where + ` AND ` + scope + `),
 `
		fallbackScope = ` JOIN scoped_documents scoped USING(search_document_id)`
	}
	budget := fmt.Sprint(notesIndexedBudget(input))
	fallbackOrdinals := []string{}
	for ordinal, variant := range variants {
		if input.indexedUnfiltered[variant] {
			fallbackOrdinals = append(fallbackOrdinals, fmt.Sprint(ordinal))
		}
	}
	fallbackEnabled := "FALSE"
	if len(fallbackOrdinals) > 0 {
		fallbackEnabled = "request.ordinal IN (" + strings.Join(fallbackOrdinals, ",") + ")"
	}
	pattern := `('%' || replace(replace(replace(qt.term,'!','!!'),'%','!%'),'_','!_') || '%') ESCAPE '!'`
	objectLimit := fmt.Sprintf("CASE WHEN request.lifecycle='archived' THEN request.candidate_limit ELSE %d END", notesIndexedObjectLimit(input))
	if input.fileView {
		objectLimit = fmt.Sprint(notesIndexedObjectCandidateLimit)
	}
	query := `WITH ` + notesIndexedQueryCTE(input) + `,
 ` + scopeCTE + `primary_ids AS MATERIALIZED (
 SELECT sd.search_document_id FROM ` + matchSource + ` sd CROSS JOIN native_query nq WHERE ` + where + ` AND sd.tsv @@ nq.query
 UNION SELECT sd.search_document_id FROM ` + matchSource + ` sd WHERE ` + where + ` AND (sd.metadata->'tags') ?| ARRAY(SELECT term FROM query_terms)
 UNION SELECT sd.search_document_id FROM all_query_terms qt
 JOIN search.lexical_terms lt ON qt.key<>qt.term AND lt.term=qt.key
 JOIN ` + matchSource + ` sd USING(search_document_id) WHERE ` + where + `
 ), literal_terms AS MATERIALIZED (
 SELECT term FROM (
 SELECT term FROM all_query_terms WHERE (SELECT count(*) FROM all_query_terms)=1
 UNION SELECT phrase FROM query_phrases
 UNION SELECT value FROM jsonb_array_elements_text($2::jsonb->'literals')
 ) qt WHERE qt.term ~ '[[:alnum:]]{3}'
 AND (EXISTS (SELECT 1 FROM query_phrases) OR COALESCE(jsonb_array_length($2::jsonb->'literals'),0)>0
 OR (SELECT count(*) FROM (SELECT 1 FROM primary_ids LIMIT ` + budget + `) bounded)<` + budget + `)
 ), literal_fallback AS MATERIALIZED (
 SELECT DISTINCT hit.search_document_id FROM literal_terms qt CROSS JOIN LATERAL (
 SELECT sd.search_document_id FROM search.search_documents sd` + fallbackScope + ` CROSS JOIN native_query nq
 WHERE ` + where + ` AND (sd.notes_fields LIKE ` + pattern + ` OR lower(sd.body) LIKE ` + pattern + `)
 ORDER BY ts_rank_cd(sd.tsv,nq.query,32) DESC,sd.metadata->>'relative_path',sd.search_document_id
 LIMIT ` + budget + `
 ) hit
 ), primary_scores AS MATERIALIZED (
 SELECT sd.search_document_id,ko.knowledge_object_id,ko.relative_path,custody.source_lifecycle,
 ts_rank_cd(sd.tsv,nq.query,32)::double precision score,false unfiltered
 FROM (SELECT search_document_id FROM primary_ids UNION SELECT search_document_id FROM literal_fallback) hit
 JOIN ` + matchSource + ` sd USING(search_document_id)
 JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=sd.notes_object_id
 JOIN knowledge.notes_source_roots root ON root.notes_source_root_id=ko.notes_source_root_id
 JOIN knowledge.notes_object_custody custody ON custody.knowledge_object_id=ko.knowledge_object_id
 CROSS JOIN native_query nq WHERE ` + scope + `
 AND NOT EXISTS (SELECT 1 FROM query_phrases qp WHERE NOT (
 sd.tsv @@ phraseto_tsquery('simple',qp.phrase) OR
 sd.notes_fields LIKE ('%' || replace(replace(replace(qp.phrase,'!','!!'),'%','!%'),'_','!_') || '%') ESCAPE '!'
 OR EXISTS (SELECT 1 FROM search.search_documents phrase_document
 WHERE phrase_document.search_document_id=sd.search_document_id
 AND strpos(lower(phrase_document.body),qp.phrase)>0)
 OR EXISTS (SELECT 1 FROM all_query_terms qt WHERE qt.key<>qt.term)))
 ), requests AS (SELECT * FROM ` + notesIndexedRequests(variants, notesIndexedBudget(input)) + `),
 unfiltered_fallback AS MATERIALIZED (
 SELECT request.ordinal,hit.* FROM requests request CROSS JOIN native_query nq CROSS JOIN LATERAL (
 SELECT sd.search_document_id,ko.knowledge_object_id,ko.relative_path,custody.source_lifecycle,
 ts_rank_cd(sd.tsv,nq.fallback_query,32)::double precision score,true unfiltered
 FROM ` + matchSource + ` sd
 JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=sd.notes_object_id
 JOIN knowledge.notes_source_roots root ON root.notes_source_root_id=ko.notes_source_root_id
 JOIN knowledge.notes_object_custody custody ON custody.knowledge_object_id=ko.knowledge_object_id
 WHERE ` + where + ` AND ` + scope + ` AND custody.source_lifecycle=request.lifecycle
 AND nq.query<>nq.fallback_query AND sd.tsv @@ nq.fallback_query AND NOT sd.tsv @@ nq.query
 AND NOT EXISTS (SELECT 1 FROM primary_ids primary_hit WHERE primary_hit.search_document_id=sd.search_document_id)
 AND ` + fallbackEnabled + `
 ORDER BY score DESC,ko.relative_path,sd.search_document_id LIMIT request.candidate_limit
 ) hit
 ), indexed_scores AS MATERIALIZED (
 SELECT request.ordinal,score.* FROM requests request JOIN primary_scores score ON score.source_lifecycle=request.lifecycle
 UNION ALL SELECT * FROM unfiltered_fallback
 ), diverse AS MATERIALIZED (
 SELECT indexed_scores.*,row_number() OVER (PARTITION BY ordinal,knowledge_object_id ORDER BY unfiltered,score DESC,relative_path,search_document_id) object_position
 FROM indexed_scores
 ), candidate_pool AS MATERIALIZED (
 SELECT request.ordinal,request.require_current,request.candidate_limit,result.* FROM requests request CROSS JOIN LATERAL (
 SELECT search_document_id,knowledge_object_id,relative_path,score,unfiltered FROM diverse
 WHERE diverse.ordinal=request.ordinal AND object_position <= ` + objectLimit + `
 ORDER BY unfiltered,score DESC,relative_path,search_document_id LIMIT request.candidate_limit
 ) result
 ), pool_status AS MATERIALIZED (
 SELECT request.ordinal,((SELECT count(*) FROM literal_fallback)>=request.candidate_limit
 OR (SELECT count(*) FROM unfiltered_fallback uf WHERE uf.ordinal=request.ordinal)>=request.candidate_limit
 OR count(pool.search_document_id)>=request.candidate_limit
 OR EXISTS (SELECT 1 FROM diverse WHERE diverse.ordinal=request.ordinal AND object_position>` + objectLimit + `)) truncated
 FROM requests request LEFT JOIN candidate_pool pool USING(ordinal) GROUP BY request.ordinal,request.candidate_limit,request.lifecycle
 ), eligible AS MATERIALIZED (
 SELECT pool.* FROM candidate_pool pool JOIN search.search_documents sd USING(search_document_id)
 JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=pool.knowledge_object_id
 LEFT JOIN knowledge.knowledge_chunks kc ON sd.source_kind='knowledge_chunk' AND kc.knowledge_chunk_id=sd.source_id AND kc.knowledge_object_id=ko.knowledge_object_id
 LEFT JOIN knowledge.knowledge_object_versions kov ON kov.knowledge_object_version_id=kc.knowledge_object_version_id AND kov.knowledge_object_id=ko.knowledge_object_id
 WHERE ` + notesObservedSourcePresentSQL("ko") + ` AND ` + notesPublishedVersionSQL("ko", "kc", "kov", false, false) + `
 AND (sd.source_kind='knowledge_object_metadata' OR kc.knowledge_chunk_id IS NOT NULL)
 AND (NOT pool.require_current OR ` + notesPublicationCurrentSQL("ko", "kov") + `)
 AND NOT EXISTS (SELECT 1 FROM query_phrases qp WHERE strpos(lower(sd.notes_fields || ' ' || sd.body),qp.phrase)=0)
 AND NOT EXISTS (SELECT 1 FROM all_query_terms qt WHERE strpos(qt.term,'_')>0
 AND NOT COALESCE((sd.metadata->'tags') ? qt.term,false) AND strpos(lower(sd.notes_fields || ' ' || sd.body),qt.term)=0)
 ) SELECT status.ordinal,COALESCE(result.search_document_id,''),COALESCE(result.knowledge_object_id,''),status.truncated,COALESCE(result.score,0),COALESCE(result.unfiltered,false)
 FROM pool_status status LEFT JOIN eligible result USING(ordinal)
 ORDER BY status.ordinal,result.unfiltered,result.score DESC,result.relative_path,result.search_document_id`
	return notesSearchQuery{SQL: query, Args: args}, nil
}
