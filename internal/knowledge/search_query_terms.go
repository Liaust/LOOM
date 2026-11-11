package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	lexical "loom.local/loom/internal/search"
)

type notesQueryTerm struct {
	Term      string `json:"term"`
	Key       string `json:"key"`
	Protected bool   `json:"protected"`
}

func notesQueryTerms(input NotesSearchInput) []notesQueryTerm {
	if input.nativeTerms != nil {
		return input.nativeTerms
	}
	terms := []notesQueryTerm{}
	positions := map[string]int{}
	add := func(text string, protected bool) {
		for _, term := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
		}) {
			key := lexical.LexicalTermKey(term)
			keep := protected || key != term || strings.ContainsFunc(term, func(r rune) bool { return !unicode.IsLetter(r) })
			if index, ok := positions[term]; ok {
				terms[index].Protected = terms[index].Protected || keep
				continue
			}
			positions[term] = len(terms)
			terms = append(terms, notesQueryTerm{term, key, keep})
		}
	}
	for _, token := range splitNotesSearchTokens(input.Query) {
		word := strings.Trim(token.Text, ".,;:!?()[]{}")
		identifier := strings.ContainsFunc(word, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsSpace(r) })
		add(token.Text, token.Quoted || identifier)
	}
	// Standalone literal queries are not discovery questions, even stop words.
	if len(terms) == 1 {
		terms[0].Protected = true
	}
	for _, phrase := range input.Phrases {
		add(phrase, true)
	}
	for _, tag := range input.Tags {
		add(tag, true)
	}
	return terms
}

func notesIndexedQueryArgs(input NotesSearchInput) []any {
	payload := input.preparedQuery
	if payload == "" {
		literals := []string{}
		literal := strings.ToLower(strings.Trim(strings.TrimSpace(input.Query), `"'`))
		if !strings.ContainsAny(literal, " \t\r\n") && strings.ContainsAny(literal, `/\.-`) {
			literals = append(literals, literal)
		}
		raw, _ := json.Marshal(map[string]any{"terms": notesQueryTerms(input), "literals": literals})
		payload = string(raw)
	}
	phrases, _ := notesSearchJSONList(input.Phrases)
	return []any{input.Query, payload, phrases}
}

func (s *Service) prepareNotesNativeQuery(ctx context.Context, input *NotesSearchInput) error {
	if input.preparedQuery != "" {
		return nil
	}
	input.nativeTerms = notesQueryTerms(*input)
	query := `WITH ` + notesIndexedQueryCTE(*input) + ` SELECT jsonb_build_object(
 'terms',$2::jsonb->'terms','literals',$2::jsonb->'literals',
 'discovery_terms',COALESCE((SELECT jsonb_agg(term ORDER BY term) FROM query_terms),'[]'::jsonb),
 'query',nq.query::text,'fallback_query',nq.fallback_query::text)::text,nq.query<>nq.fallback_query FROM native_query nq`
	return input.readTx.QueryRowContext(ctx, query, notesIndexedQueryArgs(*input)...).Scan(&input.preparedQuery, &input.nativeFiltered)
}

func notesIndexedQueryCTE(input NotesSearchInput) string {
	// The dictionary is consulted once per request, never for document rows. Its
	// stems are discarded: stored vectors and the actual tsquery both use simple.
	base := fmt.Sprintf(`all_query_terms AS MATERIALIZED (
 SELECT * FROM jsonb_to_recordset($2::jsonb->'terms') AS t(term text,key text,protected boolean)
 WHERE $1::text IS NOT NULL LIMIT %d),
 query_phrases AS MATERIALIZED (
 SELECT DISTINCT lower(trim(value)) phrase FROM jsonb_array_elements_text($3::jsonb) item(value) WHERE trim(value)<>'' LIMIT %d),
 `, len(notesQueryTerms(input)), len(input.Phrases))
	if input.preparedQuery != "" {
		return base + `query_terms AS MATERIALIZED (SELECT value term FROM jsonb_array_elements_text($2::jsonb->'discovery_terms')),
 native_query AS MATERIALIZED (SELECT ($2::jsonb->>'query')::tsquery query,($2::jsonb->>'fallback_query')::tsquery fallback_query)`
	}
	return base + `recognized_terms AS MATERIALIZED (
 SELECT *,protected OR COALESCE(cardinality(ts_lexize('pg_catalog.english_stem'::regdictionary,term)),1)<>0 keep
 FROM all_query_terms),
 query_terms AS MATERIALIZED (
 SELECT term FROM recognized_terms WHERE keep OR NOT EXISTS (SELECT 1 FROM recognized_terms WHERE keep)),
 term_queries AS MATERIALIZED (
 SELECT term,` + notesIndexedTermQuerySQL("term") + ` query FROM all_query_terms),
 native_query AS MATERIALIZED (
 SELECT COALESCE(string_agg('(' || query::text || ')',' | ' ORDER BY term)
 FILTER (WHERE numnode(query)>0 AND term IN (SELECT term FROM query_terms)),'')::tsquery query,
 COALESCE(string_agg('(' || query::text || ')',' | ' ORDER BY term) FILTER (WHERE numnode(query)>0),'')::tsquery fallback_query
 FROM term_queries)`
}

func notesIndexedTermQuerySQL(term string) string {
	plain := "plainto_tsquery('simple'," + term + ")"
	return "(CASE WHEN (SELECT count(*) FROM all_query_terms)=1 AND char_length(" + term + ")<3 AND strpos(" + term + ",'_')=0 AND numnode(" + plain + ")=1 THEN (" + plain + "::text || ':*')::tsquery ELSE " + plain + " END)"
}
