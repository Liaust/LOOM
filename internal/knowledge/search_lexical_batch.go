package knowledge

import (
	"context"
	"fmt"
)

type notesLexicalVariant struct {
	Lifecycle SourceLifecycleFilter
	Current   bool
	Limit     int
}

func notesLexicalVariants(input NotesSearchInput) []notesLexicalVariant {
	var variants []notesLexicalVariant
	add := func(lifecycle SourceLifecycleFilter, limit int) {
		if input.RequireCurrent {
			variants = append(variants, notesLexicalVariant{lifecycle, false, 50})
		}
		variants = append(variants, notesLexicalVariant{lifecycle, input.RequireCurrent, limit})
	}
	if input.SourceLifecycle != SourceLifecycleFilterArchived {
		add(SourceLifecycleFilterActive, input.Limit)
	}
	limit := input.Limit
	if input.SourceLifecycle == SourceLifecycleFilterActive {
		limit = 50
	}
	add(SourceLifecycleFilterArchived, limit)
	return variants
}

// Share only the immutable snapshot work within this one request. Each partition
// retains its own BM25 corpus statistics, ranking and candidate bound.
func (s *Service) loadNotesLexicalBatch(ctx context.Context, input NotesSearchInput) (map[notesLexicalVariant][]NotesSearchResult, error) {
	variants := notesLexicalVariants(input)
	query, err := buildNotesSearchVariantsQuery(input, variants)
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
		result, err := scanNotesSearchResult(notesReadScannerFunc(func(dest ...any) error {
			return rows.Scan(append([]any{&ordinal}, dest...)...)
		}))
		if err != nil {
			return nil, err
		}
		if ordinal < 0 || ordinal >= len(variants) {
			return nil, fmt.Errorf("invalid lexical request partition")
		}
		key := variants[ordinal]
		result.LexicalRank = len(batch[key]) + 1
		result.Citation = notesSearchCitation(result)
		batch[key] = append(batch[key], result)
	}
	return batch, rows.Err()
}

func notesLexicalDocumentMatchesSQL() string {
	return `SELECT fd.search_document_id, fd.source_kind, fd.knowledge_object_id,
	 fd.knowledge_object_version_id, fd.knowledge_chunk_id, fd.relative_path,
	 fd.lexical_document_id, fd.document_length, fd.search_lifecycle, fd.publication_current,
	 EXISTS (SELECT 1 FROM query_terms qt WHERE strpos(lower(COALESCE(fd.title,'')), qt.term)>0) AS title_match,
	 EXISTS (SELECT 1 FROM query_terms qt WHERE strpos(lower(COALESCE(fd.structural_path,'')), qt.term)>0) AS heading_match,
	 EXISTS (SELECT 1 FROM query_terms qt WHERE strpos(lower(COALESCE(fd.relative_path,'') || ' ' || COALESCE(fd.source_path,'')), qt.term)>0) AS path_match,
	 (tag_match.search_document_id IS NOT NULL) AS tag_match,
	 (body_match.search_document_id IS NOT NULL) AS body_match,
	 EXISTS (SELECT 1 FROM query_phrases qp WHERE strpos(lower(COALESCE(fd.title,'') || ' ' || COALESCE(fd.structural_path,'') || ' ' || COALESCE(fd.relative_path,'') || ' ' || COALESCE(matched_source.body,'')),qp.phrase)>0) AS phrase_match,
	 NOT EXISTS (SELECT 1 FROM query_phrases qp WHERE strpos(lower(COALESCE(fd.title,'') || ' ' || COALESCE(fd.structural_path,'') || ' ' || COALESCE(fd.relative_path,'') || ' ' || COALESCE(matched_source.body,'')),qp.phrase)=0) AS phrases_match,
	 COALESCE(fulltext.fts_score, 0.0) AS fts_score
	 FROM raw_corpus fd
	 LEFT JOIN body_matches body_match ON body_match.search_document_id=fd.search_document_id
	 LEFT JOIN tag_matches tag_match ON tag_match.search_document_id=fd.search_document_id
	 LEFT JOIN fulltext_matches fulltext ON fulltext.search_document_id=fd.search_document_id
	 LEFT JOIN LATERAL (
	   SELECT body FROM search.search_documents phrase_source
	   WHERE phrase_source.search_document_id=fd.search_document_id
	     AND EXISTS (SELECT 1 FROM query_phrases)
	   OFFSET 0
	 ) matched_source ON true`
}

// Candidate sets contain no authority decisions. Corpus membership and ranking
// still use the live repeatable-read snapshot and exact publication filters.
func notesLexicalMatchSetsSQL() string {
	return `body_matches AS MATERIALIZED (
	 SELECT DISTINCT hit.search_document_id
	 FROM query_terms qt
	 CROSS JOIN LATERAL (
	   SELECT sd.search_document_id FROM search.search_documents sd
	   WHERE sd.source_kind IN ('knowledge_chunk','knowledge_object_metadata')
	     AND sd.index_version='knowledge_bm25_v1'
	     AND lower(sd.body) LIKE '%' || replace(replace(replace(qt.term, '!', '!!'), '%', '!%'), '_', '!_') || '%' ESCAPE '!'
	   OFFSET 0
	 ) hit
	), tag_matches AS MATERIALIZED (
	 SELECT DISTINCT hit.search_document_id
	 FROM query_terms qt
	 CROSS JOIN LATERAL (
	   SELECT sd.search_document_id FROM search.search_documents sd
	   WHERE sd.source_kind IN ('knowledge_chunk','knowledge_object_metadata')
	     AND sd.index_version='knowledge_bm25_v1'
	     AND (sd.metadata->'tags') ? qt.term
	   OFFSET 0
	 ) hit
	), fulltext_matches AS MATERIALIZED (
	 SELECT sd.search_document_id,
	   ts_rank_cd(sd.tsv,websearch_to_tsquery('simple',$1)) AS fts_score
	 FROM search.search_documents sd
	 WHERE sd.source_kind IN ('knowledge_chunk','knowledge_object_metadata')
	   AND sd.index_version='knowledge_bm25_v1'
	   AND sd.tsv @@ websearch_to_tsquery('simple',$1)
	)`
}
