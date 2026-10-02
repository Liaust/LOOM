package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projects"
	lexical "loom.local/loom/internal/search"
)

const (
	KnowledgeSearchSourceKind         = "knowledge_chunk"
	KnowledgeMetadataSearchSourceKind = "knowledge_object_metadata"
	KnowledgeSearchIndexKey           = "knowledge_bm25_v1"
	NotesSearchSortRelevance          = "relevance"
	NotesSearchSortNewest             = "newest"
	NotesSearchSortOldest             = "oldest"
)

type NotesSearchInput struct {
	RequireCurrent         bool                  `json:"require_current,omitempty"`
	SourceLifecycle        SourceLifecycleFilter `json:"source_lifecycle,omitempty"`
	queryEmbedding         *notesQueryEmbedding
	readTx                 *sql.Tx
	lexicalBatch           map[notesLexicalVariant][]NotesSearchResult
	semanticBatch          map[notesLexicalVariant][]NotesSearchResult
	visibleObjectIDsJSON   string
	indexed                *notesIndexedSearch
	indexedBudget          int
	indexedExactScope      *bool
	candidateObjectIDsJSON string
	SourceCategory         string   `json:"source_category,omitempty"`
	Query                  string   `json:"query"`
	Mode                   string   `json:"mode,omitempty"`
	Phrases                []string `json:"phrases,omitempty"`
	ProjectID              string   `json:"project_id,omitempty"`
	ProjectRef             string   `json:"project_ref,omitempty"`
	SourceNodeKey          string   `json:"source_node_key,omitempty"`
	NotesSourceRootID      string   `json:"notes_source_root_id,omitempty"`
	RootRef                string   `json:"root_ref,omitempty"`
	FileClass              string   `json:"file_class,omitempty"`
	Path                   string   `json:"path,omitempty"`
	Tags                   []string `json:"tags,omitempty"`
	After                  string   `json:"after,omitempty"`
	Before                 string   `json:"before,omitempty"`
	Sort                   string   `json:"sort,omitempty"`
	Limit                  int      `json:"limit,omitempty"`
}

type NotesSearchResultSet struct {
	CandidateRetrievalTruncated       bool                        `json:"candidate_retrieval_truncated,omitempty"`
	RefreshingMatchesOmitted          int                         `json:"refreshing_matches_omitted"`
	RefreshingMatchesOmittedTruncated bool                        `json:"refreshing_matches_omitted_truncated"`
	SourceLifecycle                   SourceLifecycleFilter       `json:"source_lifecycle"`
	LifecycleGroups                   []NotesSearchLifecycleGroup `json:"lifecycle_groups"`
	ArchivedMatchesOmitted            int                         `json:"archived_matches_omitted"`
	ArchivedMatchesOmittedTruncated   bool                        `json:"archived_matches_omitted_truncated"`
	Query                             string                      `json:"query"`
	Mode                              string                      `json:"mode,omitempty"`
	RequestedMode                     string                      `json:"requested_mode,omitempty"`
	FallbackReason                    string                      `json:"fallback_reason,omitempty"`
	SemanticAvailable                 bool                        `json:"semantic_available,omitempty"`
	ResultCount                       int                         `json:"result_count"`
	Results                           []NotesSearchResult         `json:"results"`
}

type NotesSearchResult struct {
	Freshness NotesSearchFreshness `json:"freshness"`
	SourceContext
	NotesCustodyContext
	PassageFollowup          *NotesPassageInput  `json:"passage_followup,omitempty"`
	SearchDocumentID         string              `json:"search_document_id"`
	SourceKind               string              `json:"source_kind,omitempty"`
	KnowledgeObjectID        string              `json:"knowledge_object_id"`
	KnowledgeObjectVersionID string              `json:"knowledge_object_version_id,omitempty"`
	KnowledgeChunkID         string              `json:"knowledge_chunk_id"`
	NotesSourceRootID        string              `json:"notes_source_root_id"`
	RootKind                 string              `json:"root_kind"`
	SourceNodeKey            string              `json:"source_node_key,omitempty"`
	ProjectID                string              `json:"project_id,omitempty"`
	RelativePath             string              `json:"relative_path"`
	SourcePath               string              `json:"source_path,omitempty"`
	Title                    string              `json:"title,omitempty"`
	FileClass                string              `json:"file_class"`
	TextSource               string              `json:"text_source,omitempty"`
	ExtractionStatus         string              `json:"extraction_status,omitempty"`
	MetadataOnly             bool                `json:"metadata_only,omitempty"`
	ChunkIndex               int                 `json:"chunk_index"`
	StructuralPath           string              `json:"structural_path,omitempty"`
	Snippet                  string              `json:"snippet,omitempty"`
	RankScore                float64             `json:"rank_score"`
	BM25Score                float64             `json:"bm25_score,omitempty"`
	FTSScore                 float64             `json:"fts_score,omitempty"`
	BoostScore               float64             `json:"boost_score,omitempty"`
	LexicalRank              int                 `json:"lexical_rank,omitempty"`
	SemanticRank             int                 `json:"semantic_rank,omitempty"`
	SemanticDistance         float64             `json:"semantic_distance,omitempty"`
	SemanticScore            float64             `json:"semantic_score,omitempty"`
	FinalScore               float64             `json:"final_score,omitempty"`
	MatchReasons             []string            `json:"match_reasons,omitempty"`
	SourceCreatedAt          *time.Time          `json:"source_created_at,omitempty"`
	SourceModifiedAt         *time.Time          `json:"source_modified_at,omitempty"`
	RecencyAt                time.Time           `json:"recency_at"`
	RecencyBasis             string              `json:"recency_basis"`
	RecencyScore             float64             `json:"recency_score"`
	RecencyRank              int                 `json:"recency_rank"`
	ObservedAt               time.Time           `json:"observed_at"`
	IndexedAt                time.Time           `json:"indexed_at"`
	Citation                 NotesSearchCitation `json:"citation"`
}

type NotesSearchCitation struct {
	Label     string `json:"label"`
	SourceRef string `json:"source_ref"`
}

// ValidPassageFollowup checks an optional exact tuple against its search hit.
// It does not establish current access; GetNotesPassage still owns admission.
func (result NotesSearchResult) ValidPassageFollowup() bool {
	input := result.PassageFollowup
	return input != nil && result.SourceKind == KnowledgeSearchSourceKind &&
		!result.MetadataOnly && result.TextSource != "metadata_text" &&
		(result.SourceLifecycle == SourceLifecycleActive || result.SourceLifecycle == SourceLifecycleArchived) &&
		input.SourceLifecycle == SourceLifecycleFilter(result.SourceLifecycle) &&
		input.KnowledgeObjectID == result.KnowledgeObjectID &&
		input.KnowledgeObjectVersionID == result.KnowledgeObjectVersionID &&
		input.KnowledgeChunkID == result.KnowledgeChunkID && ValidateNotesPassageInput(*input) == nil
}

func notesSearchPassageFollowup(result NotesSearchResult, sourceHash string, metadataOnly bool) *NotesPassageInput {
	if metadataOnly {
		return nil
	}
	result.PassageFollowup = &NotesPassageInput{KnowledgeObjectID: result.KnowledgeObjectID,
		KnowledgeObjectVersionID: result.KnowledgeObjectVersionID, KnowledgeChunkID: result.KnowledgeChunkID,
		SourceHash: sourceHash, SourceLifecycle: SourceLifecycleFilter(result.SourceLifecycle)}
	if !result.ValidPassageFollowup() {
		return nil
	}
	return result.PassageFollowup
}

type notesSearchQuery struct {
	SQL  string
	Args []any
}

type NotesSearchParsedQuery struct {
	Raw          string           `json:"raw"`
	DisplayQuery string           `json:"display_query"`
	Query        string           `json:"query"`
	Terms        []string         `json:"terms"`
	Phrases      []string         `json:"phrases,omitempty"`
	Input        NotesSearchInput `json:"input"`
}

func ParseNotesSearchInput(raw string) NotesSearchInput {
	return ParseNotesSearchQuery(raw).Input
}

func ParseNotesSearchQuery(raw string) NotesSearchParsedQuery {
	tokens := splitNotesSearchTokens(raw)
	input := NotesSearchInput{Limit: 10}
	queryParts := []string{}
	phrases := []string{}
	for _, token := range tokens {
		key, value, isFilter := notesSearchTokenFilter(token.Text)
		if isFilter {
			switch key {
			case "lifecycle", "source_lifecycle":
				input.SourceLifecycle = SourceLifecycleFilter(value)
			case "project":
				input.ProjectRef = value
			case "category", "source_category":
				input.SourceCategory = value
			case "node":
				input.SourceNodeKey = value
			case "root":
				input.RootRef = value
			case "class", "fileclass", "file_class":
				input.FileClass = value
			case "path":
				input.Path = value
			case "tag":
				input.Tags = append(input.Tags, strings.TrimPrefix(value, "#"))
			case "after":
				input.After = value
			case "before":
				input.Before = value
			case "sort":
				input.Sort = value
			}
			continue
		}
		queryParts = append(queryParts, token.Text)
		if token.Quoted {
			phrases = append(phrases, token.Text)
		}
	}
	input.Query = strings.TrimSpace(strings.Join(queryParts, " "))
	input.Phrases = normalizePhrases(append(input.Phrases, phrases...))
	input.Tags = normalizeTags(input.Tags)
	terms := lexical.TokenizeLexical(strings.Join(append([]string{input.Query}, input.Phrases...), " "))
	return NotesSearchParsedQuery{
		Raw:          raw,
		DisplayQuery: notesSearchDisplayQuery(input),
		Query:        input.Query,
		Terms:        lexical.UniqueTerms(terms),
		Phrases:      input.Phrases,
		Input:        input,
	}
}

type notesSearchToken struct {
	Text   string
	Quoted bool
}

func splitNotesSearchTokens(raw string) []notesSearchToken {
	tokens := []notesSearchToken{}
	var builder strings.Builder
	quoted := false
	tokenQuoted := false
	escaped := false
	flush := func() {
		text := strings.TrimSpace(builder.String())
		if text != "" {
			tokens = append(tokens, notesSearchToken{Text: text, Quoted: tokenQuoted})
		}
		builder.Reset()
		tokenQuoted = false
	}
	for _, r := range raw {
		if escaped {
			builder.WriteRune(r)
			escaped = false
			continue
		}
		if quoted && r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			if quoted {
				quoted = false
				tokenQuoted = true
				flush()
				continue
			}
			quoted = true
			tokenQuoted = true
			continue
		}
		if !quoted && (r == ' ' || r == '\n' || r == '\t' || r == '\r') {
			flush()
			continue
		}
		builder.WriteRune(r)
	}
	flush()
	return tokens
}

func notesSearchTokenFilter(token string) (string, string, bool) {
	key, value, ok := strings.Cut(token, ":")
	if !ok {
		return "", "", false
	}
	key = strings.ToLower(strings.TrimSpace(key))
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", false
	}
	switch key {
	case "project", "node", "root", "class", "fileclass", "file_class", "path", "tag", "after", "before", "sort", "category", "source_category", "lifecycle", "source_lifecycle":
		return key, value, true
	default:
		return "", "", false
	}
}

func normalizePhrases(phrases []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, phrase := range phrases {
		phrase = strings.TrimSpace(phrase)
		if phrase == "" {
			continue
		}
		key := strings.ToLower(phrase)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, phrase)
	}
	return out
}

func notesSearchDisplayQuery(input NotesSearchInput) string {
	parts := []string{}
	if input.Query != "" {
		parts = append(parts, input.Query)
	}
	for _, phrase := range input.Phrases {
		if phrase != "" && !strings.Contains(input.Query, phrase) {
			parts = append(parts, `"`+phrase+`"`)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func (s *Service) SearchNotes(ctx context.Context, input NotesSearchInput) (NotesSearchResultSet, error) {
	if err := ValidateNotesSearchInput(input); err != nil {
		return NotesSearchResultSet{}, err
	}
	if s == nil || s.store.db == nil {
		return NotesSearchResultSet{}, fmt.Errorf("knowledge store is not configured")
	}
	resolved, err := s.resolveNotesSearchInput(ctx, input)
	if err != nil {
		return NotesSearchResultSet{}, err
	}
	mode, requestedMode, semanticAvailable, fallbackReason, settings, err := s.resolveNotesSearchMode(ctx, resolved)
	if err != nil {
		return NotesSearchResultSet{}, err
	}
	return s.searchNotesLifecycles(ctx, resolved, mode, requestedMode, semanticAvailable, fallbackReason, settings)
}

// ValidateNotesSearchInput validates the request fields that do not require
// database-backed reference resolution. HTTP, CLI-backed service calls, and
// direct service callers therefore share the same query/date/sort contract.
func ValidateNotesSearchInput(input NotesSearchInput) error {
	if _, err := NormalizeSourceLifecycleFilter(input.SourceLifecycle); err != nil {
		return err
	}
	if err := validateSourceCategory(input.SourceCategory); err != nil {
		return err
	}
	if strings.TrimSpace(input.Query) == "" {
		return fmt.Errorf("%w: notes search query is required", ErrInvalid)
	}
	mode := normalizeNotesSearchMode(input.Mode)
	if mode != "" && !IsNotesSearchMode(mode) {
		return fmt.Errorf("%w: notes search mode %q is not supported", ErrInvalid, mode)
	}
	_, err := normalizeNotesSearchTemporalInput(input)
	return err
}

func (s *Service) searchLexicalNotes(ctx context.Context, input NotesSearchInput) ([]NotesSearchResult, error) {
	if input.lexicalBatch != nil {
		results, ok := input.lexicalBatch[notesLexicalVariant{input.SourceLifecycle, input.RequireCurrent, input.Limit}]
		if !ok {
			return nil, fmt.Errorf("missing lexical request partition")
		}
		return append([]NotesSearchResult(nil), results...), nil
	}
	query, err := buildNotesSearchQuery(input)
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
		result, err := scanNotesSearchResult(rows)
		if err != nil {
			return nil, err
		}
		rank++
		result.LexicalRank = rank
		result.Citation = notesSearchCitation(result)
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func (s *Service) resolveNotesSearchInput(ctx context.Context, input NotesSearchInput) (NotesSearchInput, error) {
	var lifecycleErr error
	input.SourceLifecycle, lifecycleErr = NormalizeSourceLifecycleFilter(input.SourceLifecycle)
	if lifecycleErr != nil {
		return NotesSearchInput{}, lifecycleErr
	}
	input.Query = strings.TrimSpace(input.Query)
	if input.Query == "" {
		return NotesSearchInput{}, fmt.Errorf("%w: notes search query is required", ErrInvalid)
	}
	input.Mode = normalizeNotesSearchMode(input.Mode)
	if input.Mode != "" && !IsNotesSearchMode(input.Mode) {
		return NotesSearchInput{}, fmt.Errorf("%w: notes search mode %q is not supported", ErrInvalid, input.Mode)
	}
	if input.Limit <= 0 || input.Limit > 50 {
		input.Limit = 10
	}
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.ProjectRef = strings.TrimSpace(input.ProjectRef)
	if input.ProjectID == "" && input.ProjectRef != "" {
		project, err := projects.NewService(s.store.db).ResolveProjectRef(ctx, input.ProjectRef)
		if err != nil {
			return NotesSearchInput{}, fmt.Errorf("resolve project filter: %w", err)
		}
		input.ProjectID = project.ProjectID
	}
	input.NotesSourceRootID = strings.TrimSpace(input.NotesSourceRootID)
	input.RootRef = strings.TrimSpace(input.RootRef)
	if input.NotesSourceRootID == "" && input.RootRef != "" {
		root, err := s.store.ResolveSourceRootRef(ctx, input.RootRef)
		if err != nil {
			return NotesSearchInput{}, fmt.Errorf("resolve notes source root filter: %w", err)
		}
		input.NotesSourceRootID = root.NotesSourceRootID
	}
	input.SourceNodeKey = strings.TrimSpace(input.SourceNodeKey)
	input.FileClass = strings.TrimSpace(input.FileClass)
	input.Path = strings.TrimSpace(input.Path)
	input.Tags = normalizeTags(input.Tags)
	input.Phrases = normalizePhrases(input.Phrases)
	return normalizeNotesSearchTemporalInput(input)
}

func normalizeNotesSearchTemporalInput(input NotesSearchInput) (NotesSearchInput, error) {
	for _, token := range splitNotesSearchTokens(input.Query) {
		if token.Quoted {
			input.Phrases = append(input.Phrases, token.Text)
		}
	}
	input.Phrases = normalizePhrases(input.Phrases)
	input.Sort = strings.ToLower(strings.TrimSpace(input.Sort))
	if input.Sort == "" {
		input.Sort = NotesSearchSortRelevance
	}
	switch input.Sort {
	case NotesSearchSortRelevance, NotesSearchSortNewest, NotesSearchSortOldest:
	default:
		return NotesSearchInput{}, fmt.Errorf("%w: notes search sort %q is not supported", ErrInvalid, input.Sort)
	}
	var after, before time.Time
	var err error
	if raw := strings.TrimSpace(input.After); raw != "" {
		after, err = ParseAbsoluteTimestamp(raw)
		if err != nil {
			return NotesSearchInput{}, fmt.Errorf("%w: invalid notes search after timestamp: %v", ErrInvalid, err)
		}
		input.After = after.Format(time.RFC3339Nano)
	} else {
		input.After = ""
	}
	if raw := strings.TrimSpace(input.Before); raw != "" {
		before, err = ParseAbsoluteTimestamp(raw)
		if err != nil {
			return NotesSearchInput{}, fmt.Errorf("%w: invalid notes search before timestamp: %v", ErrInvalid, err)
		}
		input.Before = before.Format(time.RFC3339Nano)
	} else {
		input.Before = ""
	}
	if !after.IsZero() && !before.IsZero() && !after.Before(before) {
		return NotesSearchInput{}, fmt.Errorf("%w: notes search after timestamp must be earlier than before timestamp", ErrInvalid)
	}
	return input, nil
}

func buildNotesSearchQuery(input NotesSearchInput) (notesSearchQuery, error) {
	return buildNotesSearchVariantsQuery(input, nil)
}

func buildNotesSearchVariantsQuery(input NotesSearchInput, variants []notesLexicalVariant) (notesSearchQuery, error) {
	if input.indexed != nil {
		return buildIndexedNotesLexicalHydration(input, variants)
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
	if input.Limit <= 0 || input.Limit > 50 {
		input.Limit = 10
	}
	input, err := normalizeNotesSearchTemporalInput(input)
	if err != nil {
		return notesSearchQuery{}, err
	}
	termsJSON, err := notesSearchJSONList(notesSearchTerms(input))
	if err != nil {
		return notesSearchQuery{}, err
	}
	phrasesJSON, err := notesSearchJSONList(input.Phrases)
	if err != nil {
		return notesSearchQuery{}, err
	}
	candidateLimit := notesSearchCandidateLimit(input.Limit)
	args := []any{strings.TrimSpace(input.Query), termsJSON, phrasesJSON}
	objectsInput := input
	if len(variants) > 0 {
		objectsInput.SourceLifecycle = SourceLifecycleFilterAll
	}
	objectsSQL := notesSearchObjectsSQL(objectsInput, &args)
	publishedSQL := notesPublishedVersionSQL("ko", "kc", "kov", false, false)
	if input.RequireCurrent && len(variants) == 0 {
		publishedSQL += " AND COALESCE(kov.source_revision,ko.source_revision)=ko.latest_source_revision AND COALESCE(kov.source_hash,ko.source_hash)=ko.latest_source_hash"
	}
	// Keep corpus intermediates narrow. Object evidence is evaluated once, term
	// postings once per term, and full response context only for selected rows.
	sqlText := `
		WITH visible_objects AS MATERIALIZED (` + objectsSQL + `),
		query_terms AS (
			SELECT DISTINCT lower(trim(value)) AS term
			FROM jsonb_array_elements_text($2::jsonb) AS item(value)
			WHERE trim(value) <> ''
		),
		query_phrases AS (
			SELECT DISTINCT lower(trim(value)) AS phrase
			FROM jsonb_array_elements_text($3::jsonb) AS item(value)
			WHERE trim(value) <> ''
		),
		chunk_refs AS MATERIALIZED (
			SELECT chunk.knowledge_chunk_id, chunk.knowledge_object_id, chunk.knowledge_object_version_id,
			       chunk.chunk_index, chunk.structural_path
			FROM knowledge.knowledge_chunks chunk
			JOIN visible_objects owner ON owner.knowledge_object_id=chunk.knowledge_object_id
		),
		lexical_lengths AS MATERIALIZED (
			SELECT search_document_id, document_length FROM search.lexical_documents
		),
		raw_corpus AS (
			SELECT sd.search_document_id,
			       ld.search_document_id AS lexical_document_id, ld.document_length,
			       ko.search_lifecycle,
			       (COALESCE(kov.source_revision,ko.source_revision)=ko.latest_source_revision
			        AND COALESCE(kov.source_hash,ko.source_hash)=ko.latest_source_hash) AS publication_current,
			       sd.source_kind,
			       sd.source_created_at,
			       sd.source_modified_at,
			       COALESCE(sd.recency_at, ko.recency_at) AS recency_at,
			       COALESCE(NULLIF(sd.recency_basis, ''), ko.recency_basis) AS recency_basis,
			       COALESCE(kov.observed_at, ko.last_seen_at) AS observed_at,
			       sd.indexed_at,
			       ko.knowledge_object_id,
			       COALESCE(kc.knowledge_object_version_id, '') AS knowledge_object_version_id,
			       COALESCE(kov.source_hash, '') AS passage_source_hash,
			       COALESCE(kc.knowledge_chunk_id, '') AS knowledge_chunk_id,
			       ko.notes_source_root_id,
			       ko.root_kind,
			       ko.source_node_key,
			       COALESCE(ko.project_id, '') AS project_id,
			       ko.relative_path,
			       ko.source_path,
			       COALESCE(NULLIF(ko.title, ''), NULLIF(sd.title, ''), ko.relative_path) AS title,
			       ko.file_class,
			       COALESCE(kc.chunk_index, 0) AS chunk_index,
			       COALESCE(kc.structural_path, sd.summary, '') AS structural_path
			FROM search.search_documents sd
			LEFT JOIN lexical_lengths ld ON ld.search_document_id=sd.search_document_id
			LEFT JOIN chunk_refs kc
			  ON sd.source_kind = 'knowledge_chunk'
				 AND kc.knowledge_chunk_id = sd.source_id
			LEFT JOIN knowledge.knowledge_object_versions kov
			  ON kov.knowledge_object_version_id = kc.knowledge_object_version_id
			 AND kov.knowledge_object_id = kc.knowledge_object_id
			JOIN visible_objects ko
			  ON ko.knowledge_object_id = CASE sd.source_kind
			       WHEN 'knowledge_chunk' THEN kc.knowledge_object_id
			       WHEN 'knowledge_object_metadata' THEN sd.source_id END
			WHERE sd.source_kind IN ('knowledge_chunk', 'knowledge_object_metadata')
			  AND sd.index_version = 'knowledge_bm25_v1'
			  AND ` + publishedSQL + `
	`
	add := func(condition string, value any) {
		args = append(args, value)
		sqlText += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	for _, tag := range normalizeTags(input.Tags) {
		add("(sd.metadata->'tags') ?", tag)
	}
	if input.After != "" {
		after, _ := ParseAbsoluteTimestamp(input.After)
		add("COALESCE(sd.recency_at, ko.recency_at) >=", after)
	}
	if input.Before != "" {
		before, _ := ParseAbsoluteTimestamp(input.Before)
		add("COALESCE(sd.recency_at, ko.recency_at) <", before)
	}
	limitSQL := "request.candidate_limit"
	if len(variants) == 0 {
		args = append(args, candidateLimit)
		limitSQL = fmt.Sprintf("$%d", len(args))
	}
	sqlText += `
		),
		` + notesLexicalMatchSetsSQL() + `,
		search_corpus AS MATERIALIZED (` + notesLexicalDocumentMatchesSQL() + `),
		matching_terms AS MATERIALIZED (
			SELECT lt.search_document_id, lt.term, lt.term_frequency, lt.field_key
			FROM query_terms qt
			JOIN search.lexical_terms lt ON lt.term=qt.term
		)
	`
	if len(variants) > 0 {
		values := make([]string, 0, len(variants))
		for i, variant := range variants {
			if variant.Lifecycle != SourceLifecycleFilterActive && variant.Lifecycle != SourceLifecycleFilterArchived {
				return notesSearchQuery{}, fmt.Errorf("invalid lexical partition lifecycle")
			}
			values = append(values, fmt.Sprintf("(%d, '%s', %t, %d)", i, variant.Lifecycle, variant.Current, notesSearchCandidateLimit(variant.Limit)))
		}
		sqlText += `SELECT request.ordinal, result.* FROM (VALUES ` + strings.Join(values, ",") + `)
		 AS request(ordinal,lifecycle,require_current,candidate_limit)
		 CROSS JOIN LATERAL (
		 WITH filtered_docs AS MATERIALIZED (
			SELECT * FROM search_corpus
			WHERE search_lifecycle=request.lifecycle AND (NOT request.require_current OR publication_current)
		 ),`
	} else {
		sqlText += `, filtered_docs AS (SELECT * FROM search_corpus),`
	}
	sqlText += `
		corpus_stats AS MATERIALIZED (
			SELECT COUNT(fd.lexical_document_id)::double precision AS document_count,
			       COALESCE((AVG(GREATEST(fd.document_length, 1)) FILTER (WHERE fd.lexical_document_id IS NOT NULL))::double precision, 1.0) AS average_document_length
			FROM filtered_docs fd
		),
		doc_term_scores AS (
			SELECT fd.search_document_id,
			       lt.term,
			       MAX(fd.lexical_document_id) AS lexical_document_id, MAX(fd.document_length) AS document_length,
			       SUM(lt.term_frequency::double precision *
			           CASE lt.field_key
			             WHEN 'title' THEN 3.0
			             WHEN 'tag' THEN 2.5
			             WHEN 'heading' THEN 2.0
			             WHEN 'path' THEN 1.6
			             ELSE 1.0
			           END) AS weighted_frequency
			FROM filtered_docs fd
			JOIN matching_terms lt
			  ON lt.search_document_id = fd.search_document_id
			GROUP BY fd.search_document_id, lt.term
		),
		term_stats AS (
			SELECT term,
			       COUNT(*)::double precision AS document_frequency
			FROM doc_term_scores
			GROUP BY term
		),
		bm25_scores AS MATERIALIZED (
			SELECT dts.search_document_id,
			       SUM(
			           LN(1 + ((cs.document_count - ts.document_frequency + 0.5) / (ts.document_frequency + 0.5))) *
			           (
			             (dts.weighted_frequency * (1.2 + 1.0)) /
			             (
			               dts.weighted_frequency +
			               1.2 * (
			                 1.0 - 0.75 +
			                 0.75 * (GREATEST(dts.document_length, 1)::double precision / cs.average_document_length)
			               )
			             )
			           )
			       ) AS bm25_score
			FROM doc_term_scores dts
			JOIN term_stats ts
			  ON ts.term = dts.term
			CROSS JOIN corpus_stats cs
			WHERE cs.document_count > 0
			  AND cs.average_document_length > 0
			  AND dts.lexical_document_id IS NOT NULL
			GROUP BY dts.search_document_id
		),
		matched_docs AS (
			SELECT * FROM filtered_docs
			WHERE phrases_match AND (fts_score > 0 OR title_match OR heading_match OR path_match
			 OR tag_match OR body_match OR phrase_match
			 OR search_document_id IN (SELECT search_document_id FROM doc_term_scores))
		),
		scored AS (
			SELECT fd.*,
			       COALESCE(bm25.bm25_score, 0.0) AS bm25_score,
			       (
			         CASE WHEN fd.title_match THEN 2.5 ELSE 0.0 END +
			         CASE WHEN fd.heading_match THEN 1.4 ELSE 0.0 END +
			         CASE WHEN fd.path_match THEN 1.2 ELSE 0.0 END +
			         CASE WHEN fd.tag_match THEN 1.8 ELSE 0.0 END +
			         CASE WHEN fd.phrase_match THEN 3.0 ELSE 0.0 END +
			         CASE WHEN fd.source_kind = 'knowledge_chunk' THEN 0.4 ELSE 0.0 END +
			         CASE WHEN fd.source_kind = 'knowledge_object_metadata' AND (fd.title_match OR fd.path_match) THEN 0.5 ELSE 0.0 END
			       ) AS boost_score
			FROM matched_docs fd
			LEFT JOIN bm25_scores bm25
			  ON bm25.search_document_id = fd.search_document_id
		),
		ranked AS MATERIALIZED (
			SELECT s.*, (s.bm25_score + s.fts_score + s.boost_score) AS final_score
			FROM scored s
			WHERE (s.bm25_score > 0 OR s.fts_score > 0 OR s.title_match OR s.heading_match
			 OR s.path_match OR s.tag_match OR s.body_match OR s.phrase_match) AND s.phrases_match
			ORDER BY final_score DESC,s.bm25_score DESC,s.fts_score DESC,s.relative_path,s.search_document_id
			LIMIT ` + limitSQL + `
		)
		SELECT s.search_document_id,
		       s.source_kind,
		       s.knowledge_object_id,
		       s.knowledge_object_version_id,
		       s.knowledge_chunk_id,
		       detail_object.notes_source_root_id,
		       detail_root.root_kind,
		       detail_object.source_node_key,
		       COALESCE(detail_object.project_id,''),
		       s.relative_path,
		       detail_object.source_path,
		       COALESCE(NULLIF(detail_object.title,''),NULLIF(detail_document.title,''),detail_object.relative_path),
		       detail_object.file_class,
		       COALESCE(detail_document.metadata->>'text_source',''),
		       COALESCE(detail_document.metadata->>'extraction_status',''),
		       COALESCE((detail_document.metadata->>'metadata_only')::boolean,false),
		       COALESCE(detail_chunk.chunk_index,0),
		       COALESCE(detail_chunk.structural_path,detail_document.summary,''),
		       (SELECT CASE
		         WHEN s.fts_score > 0
		         THEN ts_headline('simple', snippet_source.body, websearch_to_tsquery('simple', $1),
		                          'MaxWords=32, MinWords=8, ShortWord=3')
		         ELSE left(snippet_source.body, 240)
		       END FROM search.search_documents snippet_source
		       WHERE snippet_source.search_document_id=s.search_document_id) AS snippet,
		       (s.bm25_score + s.fts_score + s.boost_score) AS rank_score,
		       s.bm25_score,
		       s.fts_score,
		       s.boost_score,
		       (s.bm25_score + s.fts_score + s.boost_score) AS final_score,
		       detail_document.source_created_at,
		       detail_document.source_modified_at,
		       COALESCE(detail_document.recency_at,detail_object.recency_at),
		       COALESCE(NULLIF(detail_document.recency_basis,''),detail_object.recency_basis),
		       COALESCE(detail_version.observed_at,detail_object.last_seen_at),
		       detail_document.indexed_at,
		       s.title_match,
		       s.heading_match,
		       s.path_match,
		       s.tag_match,
		       s.body_match,
		       s.phrase_match
		       ,(SELECT ` + notesSearchReadContextSQL("context_root", "context_object", "context_version", false) + `
		         FROM knowledge.knowledge_objects context_object
		         JOIN knowledge.notes_source_roots context_root
		           ON context_root.notes_source_root_id=context_object.notes_source_root_id
		         LEFT JOIN knowledge.knowledge_object_versions context_version
		           ON context_version.knowledge_object_version_id=s.knowledge_object_version_id
		          AND context_version.knowledge_object_id=context_object.knowledge_object_id
		         WHERE context_object.knowledge_object_id=s.knowledge_object_id) AS source_context_root
		       ,COALESCE(detail_version.source_hash,''),
		       (SELECT COALESCE(detail_chunk.metadata->>'text_source','')='metadata_text'
		          OR COALESCE(detail.metadata->>'text_source','')='metadata_text'
		          OR COALESCE(detail.metadata->'metadata_only'='true'::jsonb,false)
		        FROM search.search_documents detail
		        LEFT JOIN knowledge.knowledge_chunks detail_chunk ON detail_chunk.knowledge_chunk_id=s.knowledge_chunk_id
		        WHERE detail.search_document_id=s.search_document_id)
		FROM ranked s
		JOIN knowledge.knowledge_objects detail_object ON detail_object.knowledge_object_id=s.knowledge_object_id
		JOIN knowledge.notes_source_roots detail_root ON detail_root.notes_source_root_id=detail_object.notes_source_root_id
		JOIN search.search_documents detail_document ON detail_document.search_document_id=s.search_document_id
		LEFT JOIN knowledge.knowledge_chunks detail_chunk ON detail_chunk.knowledge_chunk_id=s.knowledge_chunk_id
		LEFT JOIN knowledge.knowledge_object_versions detail_version ON detail_version.knowledge_object_version_id=s.knowledge_object_version_id
		 AND detail_version.knowledge_object_id=s.knowledge_object_id
	`
	sqlText += `
		ORDER BY s.final_score DESC, s.bm25_score DESC, s.fts_score DESC,
		         s.relative_path, s.search_document_id`
	if len(variants) > 0 {
		sqlText += `) result ORDER BY request.ordinal, result.final_score DESC,
		 result.bm25_score DESC,result.fts_score DESC,result.relative_path,result.search_document_id`
	}
	return notesSearchQuery{SQL: sqlText, Args: args}, nil
}

// Evaluate live object evidence once per request, before expanding it to chunks.
// This is a statement-local result, never a cached permission or freshness grant.
func notesSearchObjectsSQL(input NotesSearchInput, args *[]any) string {
	visibility := visibleNotesCustodyObjectSQL("ko", true)
	if input.visibleObjectIDsJSON != "" {
		*args = append(*args, input.visibleObjectIDsJSON)
		visibility = fmt.Sprintf("ko.knowledge_object_id IN (SELECT jsonb_array_elements_text($%d::jsonb))", len(*args))
	}
	query := `SELECT ko.knowledge_object_id, ko.notes_source_root_id, ko.source_node_key,
	 ko.project_id, ko.relative_path, ko.source_path, ko.title, ko.file_class,
	 ko.recency_at, ko.recency_basis, ko.last_seen_at, ko.lexical_version_id,
	 ko.semantic_version_id, ko.semantic_runtime_key, ko.semantic_model_key, ko.semantic_dimensions,
	 ko.source_revision, ko.source_hash, root.root_kind,
	 (SELECT custody.source_lifecycle FROM knowledge.notes_object_custody custody
	  WHERE custody.knowledge_object_id=ko.knowledge_object_id) AS search_lifecycle,
	 ` + notesLatestSourceSQL("ko", "source_revision") + ` AS latest_source_revision,
	 ` + notesLatestSourceSQL("ko", "source_hash") + ` AS latest_source_hash
	 FROM knowledge.knowledge_objects ko
	 JOIN knowledge.notes_source_roots root ON root.notes_source_root_id=ko.notes_source_root_id
	 WHERE ` + visibility + `
	 AND ` + notesLifecycleSelectionSQL("ko", input.SourceLifecycle) + `
	 AND ` + visibleNotesKnowledgeRelativePathSQL("ko.relative_path")
	add := func(condition, value string) {
		if value != "" {
			*args = append(*args, value)
			query += fmt.Sprintf(" AND %s $%d", condition, len(*args))
		}
	}
	add("ko.project_id =", strings.TrimSpace(input.ProjectID))
	add("("+sourceCategorySQL("root.root_kind")+") =", input.SourceCategory)
	add("ko.source_node_key =", strings.TrimSpace(input.SourceNodeKey))
	add("ko.notes_source_root_id =", strings.TrimSpace(input.NotesSourceRootID))
	add("ko.file_class =", strings.TrimSpace(input.FileClass))
	if input.candidateObjectIDsJSON != "" {
		*args = append(*args, input.candidateObjectIDsJSON)
		query += fmt.Sprintf(" AND ko.knowledge_object_id IN (SELECT jsonb_array_elements_text($%d::jsonb))", len(*args))
	}
	if value := strings.TrimSpace(input.Path); value != "" {
		add("ko.relative_path ILIKE", "%"+value+"%")
	}
	return query
}

func notesSearchTerms(input NotesSearchInput) []string {
	text := strings.Join(append([]string{input.Query, strings.Join(input.Phrases, " "), strings.Join(input.Tags, " ")}, input.Path), " ")
	// Identifier components must not become unrelated words such as the A in NOT_A_SECRET.
	terms := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
	for i, term := range terms {
		terms[i] = lexical.LexicalTermKey(term)
	}
	return lexical.UniqueTerms(terms)
}

func notesSearchCandidateLimit(limit int) int {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	candidateLimit := limit * 8
	if candidateLimit < limit {
		candidateLimit = limit
	}
	if candidateLimit > 400 {
		candidateLimit = 400
	}
	return candidateLimit
}

func normalizeNotesSearchMode(mode string) string {
	return strings.ToLower(strings.TrimSpace(mode))
}

func notesSearchJSONList(values []string) (string, error) {
	cleaned := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			cleaned = append(cleaned, value)
		}
	}
	raw, err := json.Marshal(cleaned)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

type notesSearchResultScanner interface {
	Scan(dest ...any) error
}

func scanNotesSearchResult(scanner notesSearchResultScanner) (NotesSearchResult, error) {
	var result NotesSearchResult
	var flags notesSearchMatchFlags
	var sourceCreatedAt, sourceModifiedAt sql.NullTime
	var contextRoot []byte
	var passageSourceHash string
	var passageMetadataOnly bool
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
		&result.TextSource,
		&result.ExtractionStatus,
		&result.MetadataOnly,
		&result.ChunkIndex,
		&result.StructuralPath,
		&result.Snippet,
		&result.RankScore,
		&result.BM25Score,
		&result.FTSScore,
		&result.BoostScore,
		&result.FinalScore,
		&sourceCreatedAt,
		&sourceModifiedAt,
		&result.RecencyAt,
		&result.RecencyBasis,
		&result.ObservedAt,
		&result.IndexedAt,
		&flags.Title,
		&flags.Heading,
		&flags.Path,
		&flags.Tag,
		&flags.Body,
		&flags.Phrase,
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
	result.MatchReasons = notesSearchMatchReasons(flags)
	result.PassageFollowup = notesSearchPassageFollowup(result, passageSourceHash, passageMetadataOnly)
	return result, nil
}

func notesSearchCitation(result NotesSearchResult) NotesSearchCitation {
	label := strings.TrimSpace(result.RelativePath)
	if result.StructuralPath != "" {
		label += " > " + result.StructuralPath
	}
	sourceRef := result.KnowledgeObjectID
	if result.KnowledgeChunkID != "" {
		sourceRef += "#" + result.KnowledgeChunkID
	}
	return NotesSearchCitation{
		Label:     label,
		SourceRef: sourceRef,
	}
}

type notesSearchMatchFlags struct {
	Title   bool
	Heading bool
	Path    bool
	Tag     bool
	Body    bool
	Phrase  bool
}

func notesSearchMatchReasons(flags notesSearchMatchFlags) []string {
	reasons := []string{}
	if flags.Title {
		reasons = append(reasons, "title")
	}
	if flags.Heading {
		reasons = append(reasons, "heading")
	}
	if flags.Path {
		reasons = append(reasons, "path")
	}
	if flags.Tag {
		reasons = append(reasons, "tag")
	}
	if flags.Body {
		reasons = append(reasons, "body")
	}
	if flags.Phrase {
		reasons = append(reasons, "phrase")
	}
	return reasons
}

func groupNotesSearchResults(results []NotesSearchResult, limit int) []NotesSearchResult {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	grouped := make([]NotesSearchResult, 0, minInt(limit, len(results)))
	seen := map[string]bool{}
	for _, result := range results {
		key := result.KnowledgeObjectID
		if key == "" {
			key = result.SearchDocumentID
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		grouped = append(grouped, result)
		if len(grouped) >= limit {
			break
		}
	}
	return grouped
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func (s Store) ResolveSourceRootRef(ctx context.Context, ref string) (SourceRoot, error) {
	if s.db == nil {
		return SourceRoot{}, fmt.Errorf("knowledge store is not configured")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return SourceRoot{}, fmt.Errorf("%w: source root ref is required", ErrInvalid)
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+sourceRootColumns()+`
		FROM knowledge.notes_source_roots
		WHERE notes_source_root_id = $1
		   OR backend_root_key = $1
		   OR source_path = $1
		ORDER BY
			CASE
				WHEN notes_source_root_id = $1 THEN 0
				WHEN backend_root_key = $1 THEN 1
				ELSE 2
			END,
			updated_at DESC
		LIMIT 1`, ref)
	return scanSourceRoot(row)
}

func (s *Service) replaceKnowledgeSearchDocumentsTx(ctx context.Context, tx *sql.Tx, object KnowledgeObject, version KnowledgeObjectVersion, chunks []KnowledgeChunk) ([]KnowledgeChunk, int, error) {
	if err := deleteKnowledgeMetadataSearchDocumentTx(ctx, tx, object.KnowledgeObjectID); err != nil {
		return nil, 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM search.search_documents
		WHERE source_kind = $1
		  AND index_version = $2
		  AND source_version_id = $3
	`, KnowledgeSearchSourceKind, KnowledgeSearchIndexKey, version.KnowledgeObjectVersionID); err != nil {
		return nil, 0, err
	}
	indexed := make([]KnowledgeChunk, 0, len(chunks))
	for _, chunk := range chunks {
		if strings.TrimSpace(chunk.ChunkText) == "" {
			indexed = append(indexed, chunk)
			continue
		}
		if _, err := insertKnowledgeSearchDocumentTx(ctx, tx, object, version, chunk); err != nil {
			return nil, 0, err
		}
		updated, err := markKnowledgeChunkIndexedTx(ctx, tx, chunk.KnowledgeChunkID)
		if err != nil {
			return nil, 0, err
		}
		indexed = append(indexed, updated)
	}
	return indexed, len(indexed), nil
}

func insertKnowledgeSearchDocumentTx(ctx context.Context, tx *sql.Tx, object KnowledgeObject, version KnowledgeObjectVersion, chunk KnowledgeChunk) (string, error) {
	searchDocumentID := ids.NewSearchDocumentID()
	title := firstNonEmpty(object.Title, object.RelativePath)
	metadata := knowledgeSearchDocumentMetadata(object, version, chunk)
	err := tx.QueryRowContext(ctx, `
		INSERT INTO search.search_documents (
			search_document_id, source_kind, source_id, source_version_id,
			object_id, object_version_id, document_chunk_id, source_node_id,
			scope_id, title, summary, body, body_hash, tsv,
			language_config, data_classification, trust_level, freshness_state,
			source_updated_at, source_created_at, source_modified_at, recency_at,
			recency_basis, indexed_at, index_version, metadata
		)
		VALUES ($1,$2,$3,$4,null,null,null,$5,null,
		        nullif($6::text, ''), nullif($7::text, ''), $8::text, $9::text,
		        setweight(to_tsvector('simple', COALESCE($6::text, '')), 'A') ||
		        setweight(to_tsvector('simple', COALESCE($7::text, '')), 'B') ||
		        setweight(to_tsvector('simple', $8::text), 'C'),
		        'simple', 'internal', 'trusted_system', 'fresh', $10, $11, $12,
		        $13, $14, now(), $15, $16)
		ON CONFLICT (source_kind, source_id, index_version)
		DO UPDATE SET
			source_version_id = EXCLUDED.source_version_id,
			source_node_id = EXCLUDED.source_node_id,
			title = EXCLUDED.title,
			summary = EXCLUDED.summary,
			body = EXCLUDED.body,
			body_hash = EXCLUDED.body_hash,
			tsv = EXCLUDED.tsv,
			language_config = EXCLUDED.language_config,
			data_classification = EXCLUDED.data_classification,
			trust_level = EXCLUDED.trust_level,
			freshness_state = 'fresh',
			source_updated_at = EXCLUDED.source_updated_at,
			source_created_at = EXCLUDED.source_created_at,
			source_modified_at = EXCLUDED.source_modified_at,
			recency_at = EXCLUDED.recency_at,
			recency_basis = EXCLUDED.recency_basis,
			indexed_at = now(),
			metadata = EXCLUDED.metadata
		RETURNING search_document_id
	`, searchDocumentID,
		KnowledgeSearchSourceKind,
		chunk.KnowledgeChunkID,
		version.KnowledgeObjectVersionID,
		nullableString(object.SourceNodeID),
		title,
		chunk.StructuralPath,
		chunk.ChunkText,
		chunk.ChunkHash,
		nullableTime(version.SourceModifiedAt),
		nullableTime(version.SourceCreatedAt),
		nullableTime(version.SourceModifiedAt),
		version.RecencyAt,
		version.RecencyBasis,
		KnowledgeSearchIndexKey,
		metadata,
	).Scan(&searchDocumentID)
	if err != nil {
		return "", err
	}
	_, err = lexical.UpsertLexicalDocumentTx(ctx, tx, knowledgeChunkLexicalDocumentInput(searchDocumentID, object, version, chunk))
	if err != nil {
		return "", err
	}
	return searchDocumentID, nil
}

func replaceKnowledgeMetadataSearchDocumentForObjectTx(ctx context.Context, tx *sql.Tx, object KnowledgeObject) error {
	if !shouldIndexKnowledgeObjectMetadata(object) {
		return deleteKnowledgeMetadataSearchDocumentTx(ctx, tx, object.KnowledgeObjectID)
	}
	_, err := upsertKnowledgeMetadataSearchDocumentTx(ctx, tx, object)
	return err
}

func deleteKnowledgeMetadataSearchDocumentTx(ctx context.Context, tx *sql.Tx, objectID string) error {
	_, err := tx.ExecContext(ctx, `
		DELETE FROM search.search_documents
		WHERE source_kind = $1
		  AND source_id = $2
		  AND index_version = $3
	`, KnowledgeMetadataSearchSourceKind, objectID, KnowledgeSearchIndexKey)
	return err
}

func upsertKnowledgeMetadataSearchDocumentTx(ctx context.Context, tx *sql.Tx, object KnowledgeObject) (string, error) {
	searchDocumentID := ids.NewSearchDocumentID()
	title := firstNonEmpty(object.Title, object.RelativePath)
	body := knowledgeMetadataSearchBody(object)
	bodyHash := textHashURI(body)
	metadata := knowledgeMetadataSearchDocumentMetadata(object)
	err := tx.QueryRowContext(ctx, `
		INSERT INTO search.search_documents (
			search_document_id, source_kind, source_id, source_version_id,
			object_id, object_version_id, document_chunk_id, source_node_id,
			scope_id, title, summary, body, body_hash, tsv,
			language_config, data_classification, trust_level, freshness_state,
			source_updated_at, source_created_at, source_modified_at, recency_at,
			recency_basis, indexed_at, index_version, metadata
		)
		VALUES ($1,$2,$3,nullif($4::text, ''),null,null,null,$5,null,
		        nullif($6::text, ''), nullif($7::text, ''), $8::text, $9::text,
		        setweight(to_tsvector('simple', COALESCE($6::text, '')), 'A') ||
		        setweight(to_tsvector('simple', COALESCE($7::text, '')), 'B') ||
		        setweight(to_tsvector('simple', $8::text), 'C'),
		        'simple', 'internal', 'trusted_system', 'fresh', $10, $11, $12,
		        $13, $14, now(), $15, $16)
		ON CONFLICT (source_kind, source_id, index_version)
		DO UPDATE SET
			source_version_id = EXCLUDED.source_version_id,
			source_node_id = EXCLUDED.source_node_id,
			title = EXCLUDED.title,
			summary = EXCLUDED.summary,
			body = EXCLUDED.body,
			body_hash = EXCLUDED.body_hash,
			tsv = EXCLUDED.tsv,
			language_config = EXCLUDED.language_config,
			data_classification = EXCLUDED.data_classification,
			trust_level = EXCLUDED.trust_level,
			freshness_state = 'fresh',
			source_updated_at = EXCLUDED.source_updated_at,
			source_created_at = EXCLUDED.source_created_at,
			source_modified_at = EXCLUDED.source_modified_at,
			recency_at = EXCLUDED.recency_at,
			recency_basis = EXCLUDED.recency_basis,
			indexed_at = now(),
			metadata = EXCLUDED.metadata
		RETURNING search_document_id
	`, searchDocumentID,
		KnowledgeMetadataSearchSourceKind,
		object.KnowledgeObjectID,
		object.SourceRevision,
		nullableString(object.SourceNodeID),
		title,
		object.RelativePath,
		body,
		bodyHash,
		nullableTime(object.SourceModifiedAt),
		nullableTime(object.SourceCreatedAt),
		nullableTime(object.SourceModifiedAt),
		object.RecencyAt,
		object.RecencyBasis,
		KnowledgeSearchIndexKey,
		metadata,
	).Scan(&searchDocumentID)
	if err != nil {
		return "", err
	}
	_, err = lexical.UpsertLexicalDocumentTx(ctx, tx, knowledgeMetadataLexicalDocumentInput(searchDocumentID, object))
	if err != nil {
		return "", err
	}
	return searchDocumentID, nil
}

func markKnowledgeChunkIndexedTx(ctx context.Context, tx *sql.Tx, chunkID string) (KnowledgeChunk, error) {
	row := tx.QueryRowContext(ctx, `UPDATE knowledge.knowledge_chunks
		SET status = $2,
		    indexed_at = now()
		WHERE knowledge_chunk_id = $1
		RETURNING `+knowledgeChunkColumns(), chunkID, ChunkStatusIndexed)
	return scanKnowledgeChunk(row)
}

func knowledgeSearchDocumentMetadata(object KnowledgeObject, version KnowledgeObjectVersion, chunk KnowledgeChunk) json.RawMessage {
	chunkMeta := jsonObject(chunk.Metadata)
	payload, err := json.Marshal(map[string]any{
		"schema_version":              "knowledge.search_document.v0.8",
		"source_kind":                 KnowledgeSearchSourceKind,
		"knowledge_object_id":         object.KnowledgeObjectID,
		"knowledge_object_version_id": version.KnowledgeObjectVersionID,
		"knowledge_chunk_id":          chunk.KnowledgeChunkID,
		"notes_source_root_id":        object.NotesSourceRootID,
		"source_node_key":             object.SourceNodeKey,
		"project_id":                  stringValue(object.ProjectID),
		"relative_path":               object.RelativePath,
		"source_path":                 object.SourcePath,
		"file_class":                  object.FileClass,
		"chunk_index":                 chunk.ChunkIndex,
		"structural_path":             chunk.StructuralPath,
		"text_source":                 stringMapValue(chunkMeta, "text_source"),
		"extractor_key":               stringMapValue(chunkMeta, "extractor_key"),
		"extractor_version":           stringMapValue(chunkMeta, "extractor_version"),
		"extraction_status":           stringMapValue(chunkMeta, "extraction_status"),
		"section_index":               intMapValue(chunkMeta, "section_index"),
		"artifact_ids":                chunkMeta["artifact_ids"],
		"source_locators":             chunkMeta["source_locators"],
		"source_kinds":                chunkMeta["source_kinds"],
		"pipeline_key":                KnowledgeSearchIndexKey,
		"pipeline_version":            KnowledgeFileExtractionPipelineVersion,
		"source_created_at":           version.SourceCreatedAt,
		"source_modified_at":          version.SourceModifiedAt,
		"recency_at":                  version.RecencyAt,
		"recency_basis":               version.RecencyBasis,
		"absolute_time":               jsonObject(version.AbsoluteTimeMetadata),
		"tags":                        tagsFromVersionMetadata(version.Metadata),
	})
	if err != nil {
		return emptyJSONObject
	}
	return payload
}

func knowledgeChunkLexicalDocumentInput(searchDocumentID string, object KnowledgeObject, version KnowledgeObjectVersion, chunk KnowledgeChunk) lexical.LexicalDocumentInput {
	chunkMeta := jsonObject(chunk.Metadata)
	return lexical.LexicalDocumentInput{
		SearchDocumentID: searchDocumentID,
		IndexVersion:     KnowledgeSearchIndexKey,
		SourceKind:       KnowledgeSearchSourceKind,
		SourceID:         chunk.KnowledgeChunkID,
		SourceVersionID:  version.KnowledgeObjectVersionID,
		ObjectID:         object.KnowledgeObjectID,
		Fields: []lexical.LexicalFieldInput{
			{Key: lexical.LexicalFieldTitle, Text: firstNonEmpty(object.Title, object.RelativePath)},
			{Key: lexical.LexicalFieldHeading, Text: chunk.StructuralPath},
			{Key: lexical.LexicalFieldPath, Text: object.RelativePath + " " + object.SourcePath},
			{Key: lexical.LexicalFieldTag, Text: strings.Join(tagsFromVersionMetadata(version.Metadata), " ")},
			{Key: lexical.LexicalFieldBody, Text: chunk.ChunkText},
		},
		Metadata: knowledgeLexicalDocumentMetadataWithExtraction(object, KnowledgeSearchSourceKind, false, chunkMeta),
	}
}

func knowledgeMetadataLexicalDocumentInput(searchDocumentID string, object KnowledgeObject) lexical.LexicalDocumentInput {
	return lexical.LexicalDocumentInput{
		SearchDocumentID: searchDocumentID,
		IndexVersion:     KnowledgeSearchIndexKey,
		SourceKind:       KnowledgeMetadataSearchSourceKind,
		SourceID:         object.KnowledgeObjectID,
		SourceVersionID:  object.SourceRevision,
		ObjectID:         object.KnowledgeObjectID,
		Fields: []lexical.LexicalFieldInput{
			{Key: lexical.LexicalFieldTitle, Text: firstNonEmpty(object.Title, object.RelativePath)},
			{Key: lexical.LexicalFieldPath, Text: object.RelativePath + " " + object.SourcePath},
			{Key: lexical.LexicalFieldBody, Text: knowledgeMetadataSearchBody(object)},
		},
		Metadata: knowledgeLexicalDocumentMetadata(object, KnowledgeMetadataSearchSourceKind, true),
	}
}

func knowledgeMetadataSearchDocumentMetadata(object KnowledgeObject) json.RawMessage {
	extractionMeta := objectExtractionMetadata(object.Metadata)
	payload, err := json.Marshal(map[string]any{
		"schema_version":         "knowledge.search_document.v0.8.4",
		"source_kind":            KnowledgeMetadataSearchSourceKind,
		"knowledge_object_id":    object.KnowledgeObjectID,
		"notes_source_root_id":   object.NotesSourceRootID,
		"source_node_key":        object.SourceNodeKey,
		"project_id":             stringValue(object.ProjectID),
		"relative_path":          object.RelativePath,
		"source_path":            object.SourcePath,
		"file_class":             object.FileClass,
		"mime_type":              object.MimeType,
		"metadata_only":          true,
		"body_is_metadata":       true,
		"text_source":            TextSourceMetadataText,
		"extractor_key":          stringMapValue(extractionMeta, "extractor_key"),
		"extractor_version":      stringMapValue(extractionMeta, "extractor_version"),
		"extraction_status":      stringMapValue(extractionMeta, "status"),
		"pipeline_key":           KnowledgeSearchIndexKey,
		"knowledge_pipeline_key": object.PipelineKey,
		"source_created_at":      object.SourceCreatedAt,
		"source_modified_at":     object.SourceModifiedAt,
		"recency_at":             object.RecencyAt,
		"recency_basis":          object.RecencyBasis,
		"absolute_time":          jsonObject(object.AbsoluteTimeMetadata),
	})
	if err != nil {
		return emptyJSONObject
	}
	return payload
}

func knowledgeLexicalDocumentMetadata(object KnowledgeObject, sourceKind string, metadataOnly bool) json.RawMessage {
	return knowledgeLexicalDocumentMetadataWithExtraction(object, sourceKind, metadataOnly, nil)
}

func knowledgeLexicalDocumentMetadataWithExtraction(object KnowledgeObject, sourceKind string, metadataOnly bool, extraction map[string]any) json.RawMessage {
	if extraction == nil {
		extraction = objectExtractionMetadata(object.Metadata)
	}
	payload, err := json.Marshal(map[string]any{
		"schema_version":       "knowledge.lexical_document.v0.8.4",
		"source_kind":          sourceKind,
		"knowledge_object_id":  object.KnowledgeObjectID,
		"notes_source_root_id": object.NotesSourceRootID,
		"relative_path":        object.RelativePath,
		"file_class":           object.FileClass,
		"metadata_only":        metadataOnly,
		"text_source":          firstNonEmpty(stringMapValue(extraction, "text_source"), TextSourceMetadataText),
		"extractor_key":        stringMapValue(extraction, "extractor_key"),
		"extractor_version":    stringMapValue(extraction, "extractor_version"),
		"extraction_status":    firstNonEmpty(stringMapValue(extraction, "extraction_status"), stringMapValue(extraction, "status")),
	})
	if err != nil {
		return emptyJSONObject
	}
	return payload
}

func knowledgeMetadataSearchBody(object KnowledgeObject) string {
	parts := nonEmptyStrings(
		firstNonEmpty(object.Title, object.RelativePath),
		object.RelativePath,
		object.SourcePath,
		object.FileClass,
		object.MimeType,
		object.SourceNodeKey,
		stringValue(object.ProjectID),
	)
	if extraction := objectExtractionMetadata(object.Metadata); len(extraction) > 0 {
		parts = append(parts, searchableMetadataText(extraction))
	}
	body := strings.Join(nonEmptyStrings(parts...), "\n")
	if strings.TrimSpace(body) == "" {
		return object.KnowledgeObjectID
	}
	return body
}

func searchableMetadataText(value any) string {
	lines := []string{}
	appendSearchableMetadataText("", value, &lines, 0)
	return strings.Join(nonEmptyStrings(lines...), "\n")
}

func appendSearchableMetadataText(prefix string, value any, lines *[]string, depth int) {
	if depth > 4 || value == nil {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			appendSearchableMetadataText(path, typed[key], lines, depth+1)
		}
	case []any:
		for index, item := range typed {
			appendSearchableMetadataText(fmt.Sprintf("%s.%d", prefix, index), item, lines, depth+1)
		}
	case string:
		if text := strings.TrimSpace(typed); text != "" {
			*lines = append(*lines, strings.TrimSpace(prefix+" "+text))
		}
	case bool, float64, int, int64:
		*lines = append(*lines, strings.TrimSpace(fmt.Sprintf("%s %v", prefix, typed)))
	}
}

func shouldIndexKnowledgeObjectMetadata(object KnowledgeObject) bool {
	if object.DeletedAt != nil || object.ProcessingState == ProcessingStateDeleted {
		return false
	}
	if object.ProcessingState == ProcessingStateMetadataOnly && extractionStatusIndexesMetadataOnly(objectExtractionStatus(object.Metadata)) {
		return true
	}
	switch strings.TrimSpace(object.FileClass) {
	case "", "markdown", "text":
		return false
	default:
		return true
	}
}

func objectExtractionStatus(raw json.RawMessage) string {
	return stringMapValue(objectExtractionMetadata(raw), "status")
}

func extractionStatusIndexesMetadataOnly(status string) bool {
	switch strings.TrimSpace(status) {
	case ExtractionStatusMetadataOnly,
		ExtractionStatusTooLarge,
		ExtractionStatusSourceUnavailable,
		ExtractionStatusUnsupportedBodyExtraction,
		ExtractionStatusPasswordRequired,
		ExtractionStatusNoEmbeddedText,
		ExtractionStatusOCRDeferred:
		return true
	default:
		return false
	}
}

func objectExtractionMetadata(raw json.RawMessage) map[string]any {
	metadata := jsonObject(raw)
	extraction, _ := metadata["extraction"].(map[string]any)
	return extraction
}

func stringMapValue(input map[string]any, key string) string {
	if input == nil {
		return ""
	}
	value, _ := input[key].(string)
	return strings.TrimSpace(value)
}

func intMapValue(input map[string]any, key string) int {
	if input == nil {
		return 0
	}
	switch value := input[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func textHashURI(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("sha256:%x", sum[:])
}

func nonEmptyStrings(values ...string) []string {
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func tagsFromVersionMetadata(raw json.RawMessage) []string {
	metadata := jsonObject(raw)
	if metadata == nil {
		return nil
	}
	frontmatter, _ := metadata["frontmatter"].(map[string]any)
	if frontmatter == nil {
		return nil
	}
	tags := []string{}
	for _, key := range []string{"tags", "tag"} {
		tags = append(tags, tagsFromValue(frontmatter[key])...)
	}
	return normalizeTags(tags)
}

func tagsFromValue(value any) []string {
	switch typed := value.(type) {
	case string:
		return splitTagString(typed)
	case []any:
		tags := []string{}
		for _, item := range typed {
			tags = append(tags, tagsFromValue(item)...)
		}
		return tags
	case []string:
		tags := []string{}
		for _, item := range typed {
			tags = append(tags, splitTagString(item)...)
		}
		return tags
	default:
		return nil
	}
}

func splitTagString(value string) []string {
	tags := []string{}
	for _, item := range strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t'
	}) {
		item = strings.Trim(strings.TrimSpace(item), "#")
		if item != "" {
			tags = append(tags, item)
		}
	}
	return tags
}

func normalizeTags(values []string) []string {
	seen := map[string]struct{}{}
	tags := []string{}
	for _, value := range values {
		value = strings.ToLower(strings.Trim(strings.TrimSpace(value), "#"))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		tags = append(tags, value)
	}
	return tags
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
