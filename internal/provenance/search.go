package provenance

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"loom.local/loom/internal/ids"
)

const (
	SearchSchemaVersion               = "loom.provenance.search.v1"
	DefaultSearchLimit                = 8
	MaximumSearchLimit                = 25
	MaximumSearchResultsPerCollection = 8
	MaximumSearchQueryBytes           = 512
	MaximumSearchCursorBytes          = 1024
	MaximumSearchMatchExplanation     = 280
	MaximumSearchCompactResult        = 800
	MaximumSearchCompactResponse      = 6000
	MaximumSearchSourceReferences     = 2
	MaximumSearchFilterBytes          = 256
	MaximumSearchTerms                = 16
	MaximumSearchTermRunes            = 64
	searchExactProjectionLimit        = 16
	searchInitialSummaryRunes         = 180
	searchInitialExplanationRunes     = 220
	searchCursorSchemaVersion         = "loom.provenance.search_cursor.v1"
	searchOrdering                    = "score_desc_collection_asc_recorded_at_desc_id_asc"
)

type SearchCollection string

const (
	SearchCollectionAcceptedRecords   SearchCollection = "accepted_records"
	SearchCollectionPendingCandidates SearchCollection = "pending_candidates"
	SearchCollectionUnresolvedCases   SearchCollection = "unresolved_cases"
	SearchCollectionRepositoryState   SearchCollection = "repo_state"
	SearchCollectionProjectState      SearchCollection = "project_state"
)

type SearchRequest struct {
	Query          string             `json:"query"`
	Project        string             `json:"project,omitempty"`
	Repository     string             `json:"repository,omitempty"`
	Collections    []SearchCollection `json:"collections,omitempty"`
	IncludePending bool               `json:"include_pending,omitempty"`
	Limit          int                `json:"limit,omitempty"`
	Cursor         string             `json:"cursor,omitempty"`
}

type SearchText struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

type SearchSourceSummary struct {
	Kind               string       `json:"kind"`
	SourceReferenceIDs []SemanticID `json:"source_reference_ids"`
	Truncated          bool         `json:"truncated"`
}

type SearchFreshness struct {
	RecordedAt  time.Time  `json:"recorded_at"`
	ObservedAt  *time.Time `json:"observed_at,omitempty"`
	ValidFrom   *time.Time `json:"valid_from,omitempty"`
	ValidUntil  *time.Time `json:"valid_until,omitempty"`
	Currentness string     `json:"currentness"`
}

type SearchExactGet struct {
	Resource string `json:"resource"`
	ID       string `json:"id"`
	Path     string `json:"path"`
}

type SearchCompactMatch struct {
	Summary          SearchText          `json:"summary"`
	MatchExplanation SearchText          `json:"match_explanation"`
	MatchedFields    []string            `json:"matched_fields"`
	MatchedTerms     []string            `json:"matched_terms"`
	Source           SearchSourceSummary `json:"source"`
	Freshness        SearchFreshness     `json:"freshness"`
	AssertionPosture string              `json:"assertion_posture"`
	ProjectIDs       []string            `json:"project_ids"`
	RepositoryIDs    []string            `json:"repository_ids"`
	CompactTruncated bool                `json:"compact_truncated"`
}

type AcceptedRecordSearchResult struct {
	RecordID SemanticID         `json:"record_id"`
	Match    SearchCompactMatch `json:"match"`
	ExactGet SearchExactGet     `json:"exact_get"`
}

type PendingCandidateSearchResult struct {
	CandidateID SemanticID         `json:"candidate_id"`
	Match       SearchCompactMatch `json:"match"`
	ExactGet    SearchExactGet     `json:"exact_get"`
}

type UnresolvedCaseSearchResult struct {
	CaseID   SemanticID         `json:"case_id"`
	Match    SearchCompactMatch `json:"match"`
	ExactGet SearchExactGet     `json:"exact_get"`
}

type RepositoryStateSearchResult struct {
	RepositoryID string             `json:"repository_id"`
	Match        SearchCompactMatch `json:"match"`
	ExactGet     SearchExactGet     `json:"exact_get"`
}

type ProjectStateSearchResult struct {
	ProjectID  string             `json:"project_id"`
	SnapshotID SemanticID         `json:"snapshot_id"`
	Match      SearchCompactMatch `json:"match"`
	ExactGet   SearchExactGet     `json:"exact_get"`
}

type SearchCollectionPage[T any] struct {
	Items     []T  `json:"items"`
	Truncated bool `json:"truncated"`
}

type SearchResponse struct {
	SchemaVersion     string                                             `json:"schema_version"`
	Query             string                                             `json:"query"`
	Ordering          string                                             `json:"ordering"`
	AcceptedRecords   SearchCollectionPage[AcceptedRecordSearchResult]   `json:"accepted_records"`
	PendingCandidates SearchCollectionPage[PendingCandidateSearchResult] `json:"pending_candidates"`
	UnresolvedCases   SearchCollectionPage[UnresolvedCaseSearchResult]   `json:"unresolved_cases"`
	RepositoryState   SearchCollectionPage[RepositoryStateSearchResult]  `json:"repo_state"`
	ProjectState      SearchCollectionPage[ProjectStateSearchResult]     `json:"project_state"`
	Returned          int                                                `json:"returned"`
	NextCursor        string                                             `json:"next_cursor,omitempty"`
	Truncated         bool                                               `json:"truncated"`
}

// SearchTransport extends the existing exact foundation surface with one
// provenance-only lexical/filter operation. Exact retrieval continues through
// FoundationTransport's lifecycle gets and ProjectionTransport's repository get.
type SearchTransport interface {
	Search(context.Context, SearchRequest) (SearchResponse, error)
}

type normalizedSearchRequest struct {
	Query          string
	Terms          []string
	Project        string
	Repository     string
	Collections    []SearchCollection
	IncludePending bool
	Limit          int
	Cursor         searchQueryCursor
	Digest         string
}

type encodedSearchCursor struct {
	SchemaVersion      string    `json:"schema_version"`
	RequestDigest      string    `json:"request_digest"`
	Score              int       `json:"score"`
	CollectionPriority int       `json:"collection_priority"`
	SortTime           time.Time `json:"sort_time"`
	ID                 string    `json:"id"`
}

type materializedSearchResult struct {
	row        searchQueryRow
	record     *AcceptedRecordSearchResult
	candidate  *PendingCandidateSearchResult
	caseItem   *UnresolvedCaseSearchResult
	repository *RepositoryStateSearchResult
	project    *ProjectStateSearchResult
}

func (api *FoundationAPI) Search(ctx context.Context, request SearchRequest) (SearchResponse, error) {
	if api == nil || api.store == nil || api.service == nil {
		return SearchResponse{}, errors.New("provenance search runtime is not ready")
	}
	request.Project = strings.TrimSpace(request.Project)
	if request.Project != "" && ids.Validate(ids.ProjectPrefix, request.Project) != nil {
		// Bind names to the current projected identity before binding the cursor.
		// Exact IDs still work for semantic anchors without a project projection.
		validation := request
		validation.Cursor = ""
		if _, err := normalizeSearchRequest(validation); err != nil {
			return SearchResponse{}, &FoundationValidationError{Err: err}
		}
		project, err := api.store.resolveSearchProject(ctx, request.Project)
		if err != nil {
			return SearchResponse{}, err
		}
		request.Project = project
	}
	normalized, err := normalizeSearchRequest(request)
	if err != nil {
		return SearchResponse{}, &FoundationValidationError{Err: err}
	}

	rows := make([]searchQueryRow, 0, len(normalized.Collections)*MaximumSearchResultsPerCollection)
	queryTruncated := map[SearchCollection]bool{}
	for _, collection := range normalized.Collections {
		var page []searchQueryRow
		var truncated bool
		switch collection {
		case SearchCollectionAcceptedRecords:
			page, truncated, err = api.store.searchAcceptedRecords(ctx, normalized.Terms, normalized.Project, normalized.Repository, normalized.Cursor, MaximumSearchResultsPerCollection)
		case SearchCollectionUnresolvedCases:
			page, truncated, err = api.store.searchUnresolvedCases(ctx, normalized.Terms, normalized.Project, normalized.Repository, normalized.Cursor, MaximumSearchResultsPerCollection)
		case SearchCollectionPendingCandidates:
			page, truncated, err = api.store.searchPendingCandidates(ctx, normalized.Terms, normalized.Project, normalized.Repository, normalized.Cursor, MaximumSearchResultsPerCollection)
		case SearchCollectionRepositoryState:
			page, truncated, err = api.store.searchRepositoryState(ctx, normalized.Terms, normalized.Project, normalized.Repository, normalized.Cursor, MaximumSearchResultsPerCollection)
		case SearchCollectionProjectState:
			page, truncated, err = api.store.searchProjectState(ctx, normalized.Terms, normalized.Project, normalized.Repository, normalized.Cursor, MaximumSearchResultsPerCollection)
		}
		if err != nil {
			return SearchResponse{}, err
		}
		rows = append(rows, page...)
		queryTruncated[collection] = truncated
	}

	sort.Slice(rows, func(i, j int) bool { return searchRowLess(rows[i], rows[j]) })
	selected, moreByCollection := selectBoundedSearchRows(rows, normalized.Limit)
	for collection, truncated := range queryTruncated {
		moreByCollection[collection] = moreByCollection[collection] || truncated
	}

	materialized := make([]materializedSearchResult, 0, len(selected))
	for _, row := range selected {
		item, err := api.materializeSearchResult(ctx, normalized.Terms, row)
		if err != nil {
			return SearchResponse{}, err
		}
		materialized = append(materialized, item)
	}

	for {
		response := buildSearchResponse(normalized.Query, materialized, moreByCollection)
		hasMore := false
		for _, value := range moreByCollection {
			hasMore = hasMore || value
		}
		if hasMore && len(materialized) > 0 {
			response.NextCursor, err = encodeSearchCursor(normalized.Digest, materialized[len(materialized)-1].row)
			if err != nil {
				return SearchResponse{}, err
			}
		}
		response.Truncated = hasMore
		if searchResponseRuneCount(response) <= MaximumSearchCompactResponse {
			return response, nil
		}
		if len(materialized) == 0 {
			return SearchResponse{}, errors.New("bounded provenance search metadata exceeds the compact response limit")
		}
		removed := materialized[len(materialized)-1]
		materialized = materialized[:len(materialized)-1]
		moreByCollection[removed.row.Collection] = true
	}
}

func (api *FoundationAPI) materializeSearchResult(ctx context.Context, terms []string, row searchQueryRow) (materializedSearchResult, error) {
	result := materializedSearchResult{row: row}
	switch row.Collection {
	case SearchCollectionProjectState:
		if row.ProjectContext == nil || row.ProjectSnapshotID == "" {
			return materializedSearchResult{}, fmt.Errorf("project state %s is missing its captured projection", row.ID)
		}
		project := *row.ProjectContext
		match := newSearchCompactMatch(terms, projectSearchFields(project), row.SearchableSummary,
			"project_state_projection", []SemanticID{}, projectSearchSourceTruncated(project),
			"source_declared", projectSearchCurrentness(project), row.SortTime, row.ObservedAt,
			nil, nil, []string{project.ProjectID}, []string{})
		item := ProjectStateSearchResult{
			ProjectID: project.ProjectID, SnapshotID: row.ProjectSnapshotID, Match: match,
			ExactGet: SearchExactGet{Resource: "project", ID: project.ProjectID,
				Path: "/v1/provenance/projects/" + project.ProjectID + "?snapshot_id=" + string(row.ProjectSnapshotID)},
		}
		if err := fitSearchCompactResult(&item.Match, func() any { return item }); err != nil {
			return materializedSearchResult{}, fmt.Errorf("compact project state %s: %w", row.ID, err)
		}
		result.project = &item
	case SearchCollectionRepositoryState:
		if row.RepositoryCard == nil {
			return materializedSearchResult{}, fmt.Errorf("repository state %s is missing its current projection", row.ID)
		}
		card := *row.RepositoryCard
		fields := repositorySearchFields(card)
		match := newSearchCompactMatch(terms, fields, row.SearchableSummary,
			"repository_state_projection", []SemanticID{}, false,
			"deterministic_rebuildable_projection", repositorySearchCurrentness(card),
			row.SortTime, card.Freshness.ObservedAt, nil, nil,
			[]string{card.OwningProject.ProjectID}, []string{card.RepositoryID})
		item := RepositoryStateSearchResult{
			RepositoryID: card.RepositoryID, Match: match,
			ExactGet: SearchExactGet{Resource: "repo", ID: card.RepositoryID, Path: "/v1/provenance/repos/" + card.RepositoryID},
		}
		if err := fitSearchCompactResult(&item.Match, func() any { return item }); err != nil {
			return materializedSearchResult{}, fmt.Errorf("compact repository state %s: %w", row.ID, err)
		}
		result.repository = &item
	case SearchCollectionAcceptedRecords:
		id := SemanticID(row.ID)
		projection, found, err := api.service.GetRecordLifecycle(ctx, id, searchExactProjectionLimit)
		if err != nil {
			return materializedSearchResult{}, err
		}
		if !found {
			return materializedSearchResult{}, fmt.Errorf("accepted record %s disappeared during bounded search", row.ID)
		}
		anchors := projection.Record.Anchors
		fields := []searchField{
			{name: "claim", value: projection.Record.Claim},
			{name: "record_context", value: projection.Record.RecordContext},
			{name: "record_kind", value: projection.Record.RecordKind},
			{name: "domain", value: projection.Record.Domain},
			{name: "anchors", value: searchableAnchors(anchors)},
		}
		match := newSearchCompactMatch(terms, fields, projection.Record.Claim,
			"accepted_record", projection.SourceReferenceIDs, projection.Truncated,
			projection.Record.AssertionPosture, projection.Record.Temporal.Interpretation,
			projection.Record.CreatedAt, projection.Record.Temporal.ObservedAt,
			projection.Record.Temporal.ValidFrom, projection.Record.Temporal.ValidUntil,
			anchorProjects(anchors), anchorRepositories(anchors))
		item := AcceptedRecordSearchResult{
			RecordID: id, Match: match,
			ExactGet: SearchExactGet{Resource: "record", ID: row.ID, Path: "/v1/provenance/records/" + row.ID},
		}
		if err := fitSearchCompactResult(&item.Match, func() any { return item }); err != nil {
			return materializedSearchResult{}, fmt.Errorf("compact accepted record %s: %w", row.ID, err)
		}
		result.record = &item
	case SearchCollectionPendingCandidates:
		id := SemanticID(row.ID)
		projection, found, err := api.service.GetCandidateLifecycle(ctx, id, searchExactProjectionLimit)
		if err != nil {
			return materializedSearchResult{}, err
		}
		if !found || projection.EffectiveState != "pending" {
			return materializedSearchResult{}, fmt.Errorf("pending candidate %s changed during bounded search", row.ID)
		}
		var registration CandidateRegistration
		if err := json.Unmarshal(projection.Candidate.Submitted, &registration); err != nil {
			return materializedSearchResult{}, fmt.Errorf("decode candidate %s anchors: %w", row.ID, err)
		}
		fields := []searchField{
			{name: "claim", value: projection.Candidate.Claim},
			{name: "record_context", value: projection.Candidate.RecordContext},
			{name: "record_kind", value: projection.Candidate.RecordKind},
			{name: "domain", value: projection.Candidate.Domain},
			{name: "anchors", value: searchableAnchors(registration.Anchors)},
		}
		match := newSearchCompactMatch(terms, fields, projection.Candidate.Claim,
			"pending_candidate", projection.SourceReferenceIDs, projection.Truncated,
			"unaccepted_candidate", "pending_unaccepted", projection.Candidate.RegisteredAt,
			ptrTime(projection.Candidate.RegisteredAt), nil, nil,
			anchorProjects(registration.Anchors), anchorRepositories(registration.Anchors))
		item := PendingCandidateSearchResult{
			CandidateID: id, Match: match,
			ExactGet: SearchExactGet{Resource: "candidate", ID: row.ID, Path: "/v1/provenance/candidates/" + row.ID},
		}
		if err := fitSearchCompactResult(&item.Match, func() any { return item }); err != nil {
			return materializedSearchResult{}, fmt.Errorf("compact pending candidate %s: %w", row.ID, err)
		}
		result.candidate = &item
	case SearchCollectionUnresolvedCases:
		id := SemanticID(row.ID)
		projection, found, err := api.service.GetResolutionCaseLifecycle(ctx, id, searchExactProjectionLimit)
		if err != nil {
			return materializedSearchResult{}, err
		}
		if !found || isClosedStatus(projection.EffectiveState) {
			return materializedSearchResult{}, fmt.Errorf("unresolved case %s changed during bounded search", row.ID)
		}
		projects, repositories, err := api.caseSearchAnchors(ctx, projection.Members)
		if err != nil {
			return materializedSearchResult{}, err
		}
		sourceIDs := caseEvidenceSourceIDs(projection.Events)
		fields := []searchField{
			{name: "issue", value: projection.Case.Issue},
			{name: "domain", value: projection.Case.Domain},
			{name: "status", value: projection.EffectiveState},
		}
		match := newSearchCompactMatch(terms, fields, projection.Case.Issue,
			"resolution_case", sourceIDs, projection.Truncated,
			"unresolved", projection.EffectiveState, projection.Case.CreatedAt,
			ptrTime(projection.Case.CreatedAt), nil, nil, projects, repositories)
		item := UnresolvedCaseSearchResult{
			CaseID: id, Match: match,
			ExactGet: SearchExactGet{Resource: "case", ID: row.ID, Path: "/v1/provenance/cases/" + row.ID},
		}
		if err := fitSearchCompactResult(&item.Match, func() any { return item }); err != nil {
			return materializedSearchResult{}, fmt.Errorf("compact unresolved case %s: %w", row.ID, err)
		}
		result.caseItem = &item
	default:
		return materializedSearchResult{}, fmt.Errorf("unsupported provenance search collection %q", row.Collection)
	}
	return result, nil
}

func (api *FoundationAPI) caseSearchAnchors(ctx context.Context, members []CaseMember) ([]string, []string, error) {
	var projects, repositories []string
	for _, member := range members {
		var anchors *StructuralAnchors
		switch {
		case member.RecordID != nil:
			record, found, err := api.store.GetRecord(ctx, *member.RecordID)
			if err != nil {
				return nil, nil, err
			}
			if found {
				anchors = record.Anchors
			}
		case member.CandidateID != nil:
			candidate, found, err := api.store.GetCandidate(ctx, *member.CandidateID)
			if err != nil {
				return nil, nil, err
			}
			if found {
				var registration CandidateRegistration
				if err := json.Unmarshal(candidate.Submitted, &registration); err != nil {
					return nil, nil, err
				}
				anchors = registration.Anchors
			}
		}
		projects = appendUniqueBounded(projects, anchorProjects(anchors), 4)
		repositories = appendUniqueBounded(repositories, anchorRepositories(anchors), 4)
	}
	return projects, repositories, nil
}

func normalizeSearchRequest(request SearchRequest) (normalizedSearchRequest, error) {
	query := strings.TrimSpace(request.Query)
	if query == "" {
		return normalizedSearchRequest{}, errors.New("provenance search query is required")
	}
	if len(query) > MaximumSearchQueryBytes {
		return normalizedSearchRequest{}, fmt.Errorf("provenance search query must be at most %d bytes", MaximumSearchQueryBytes)
	}
	project, repository := strings.TrimSpace(request.Project), strings.TrimSpace(request.Repository)
	if len(project) > MaximumSearchFilterBytes || len(repository) > MaximumSearchFilterBytes {
		return normalizedSearchRequest{}, fmt.Errorf("project and repository filters must be at most %d bytes", MaximumSearchFilterBytes)
	}
	terms := normalizeSearchTerms(query)
	if len(terms) == 0 {
		return normalizedSearchRequest{}, errors.New("provenance search query has no searchable terms")
	}
	limit := request.Limit
	if limit == 0 {
		limit = DefaultSearchLimit
	}
	if limit < 1 || limit > MaximumSearchLimit {
		return normalizedSearchRequest{}, fmt.Errorf("provenance search limit must be between 1 and %d", MaximumSearchLimit)
	}

	selected := map[SearchCollection]bool{}
	includePending := request.IncludePending
	if len(request.Collections) == 0 {
		selected[SearchCollectionAcceptedRecords] = true
		selected[SearchCollectionUnresolvedCases] = true
		selected[SearchCollectionRepositoryState] = true
		selected[SearchCollectionProjectState] = true
		if includePending {
			selected[SearchCollectionPendingCandidates] = true
		}
	} else {
		for _, collection := range request.Collections {
			switch collection {
			case SearchCollectionAcceptedRecords, SearchCollectionUnresolvedCases, SearchCollectionRepositoryState, SearchCollectionProjectState:
				selected[collection] = true
			case SearchCollectionPendingCandidates:
				selected[collection] = true
				includePending = true
			default:
				return normalizedSearchRequest{}, fmt.Errorf("unsupported provenance search collection %q", collection)
			}
		}
	}
	if request.IncludePending {
		selected[SearchCollectionPendingCandidates] = true
	}
	collections := make([]SearchCollection, 0, 5)
	// New collections append without renumbering existing cursor kinds.
	for _, collection := range []SearchCollection{SearchCollectionAcceptedRecords, SearchCollectionUnresolvedCases, SearchCollectionPendingCandidates, SearchCollectionRepositoryState, SearchCollectionProjectState} {
		if selected[collection] {
			collections = append(collections, collection)
		}
	}
	normalized := normalizedSearchRequest{
		Query: query, Terms: terms, Project: project, Repository: repository,
		Collections: collections, IncludePending: includePending, Limit: limit,
	}
	normalized.Digest = searchRequestDigest(normalized)
	if cursor := strings.TrimSpace(request.Cursor); cursor != "" {
		decoded, err := decodeSearchCursor(cursor, normalized.Digest, normalized.Collections)
		if err != nil && len(request.Collections) == 0 {
			// Continue old default queries with their original collection set,
			// rather than silently mixing new results into an existing page walk.
			legacy := normalized
			legacy.Collections = append([]SearchCollection(nil), collections[:len(collections)-1]...)
			legacy.Digest = searchRequestDigest(legacy)
			if oldCursor, oldErr := decodeSearchCursor(cursor, legacy.Digest, legacy.Collections); oldErr == nil {
				legacy.Cursor = oldCursor
				return legacy, nil
			}
		}
		if err != nil {
			return normalizedSearchRequest{}, err
		}
		normalized.Cursor = decoded
	}
	return normalized, nil
}

func normalizeSearchTerms(query string) []string {
	stopWords := map[string]struct{}{
		"a": {}, "accepted": {}, "an": {}, "and": {}, "applies": {}, "are": {}, "at": {},
		"be": {}, "current": {}, "did": {}, "do": {}, "does": {}, "enabled": {}, "for": {},
		"from": {}, "how": {}, "i": {}, "in": {}, "is": {}, "it": {}, "may": {}, "must": {},
		"now": {}, "of": {}, "on": {}, "open": {}, "or": {}, "our": {}, "search": {},
		"should": {}, "show": {}, "the": {}, "to": {}, "us": {}, "what": {}, "where": {},
		"which": {}, "with": {}, "why": {},
	}
	var terms []string
	seen := map[string]struct{}{}
	var token []rune
	flush := func() {
		if len(token) == 0 {
			return
		}
		rawValue := strings.ToLower(string(token))
		token = token[:0]
		if _, stop := stopWords[rawValue]; stop || utf8.RuneCountInString(rawValue) > MaximumSearchTermRunes {
			return
		}
		value := stemSearchTerm(rawValue)
		if _, duplicate := seen[value]; duplicate || len(terms) >= MaximumSearchTerms {
			return
		}
		seen[value] = struct{}{}
		terms = append(terms, value)
	}
	for _, character := range query {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			token = append(token, unicode.ToLower(character))
		} else {
			flush()
		}
	}
	flush()
	return terms
}

func stemSearchTerm(value string) string {
	runes := []rune(value)
	switch {
	case len(runes) > 5 && strings.HasSuffix(value, "ing"):
		return string(runes[:len(runes)-3])
	case len(runes) > 4 && strings.HasSuffix(value, "ed"):
		return string(runes[:len(runes)-2])
	case len(runes) > 4 && strings.HasSuffix(value, "s"):
		return string(runes[:len(runes)-1])
	default:
		return value
	}
}

func searchRequestDigest(request normalizedSearchRequest) string {
	payload, _ := json.Marshal(struct {
		Terms          []string           `json:"terms"`
		Project        string             `json:"project"`
		Repository     string             `json:"repository"`
		Collections    []SearchCollection `json:"collections"`
		IncludePending bool               `json:"include_pending"`
	}{request.Terms, request.Project, request.Repository, request.Collections, request.IncludePending})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func encodeSearchCursor(requestDigest string, row searchQueryRow) (string, error) {
	payload, err := json.Marshal(encodedSearchCursor{
		SchemaVersion: searchCursorSchemaVersion, RequestDigest: requestDigest,
		Score: row.Score, CollectionPriority: row.CollectionPriority,
		SortTime: row.SortTime.UTC(), ID: row.ID,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeSearchCursor(value, requestDigest string, collections []SearchCollection) (searchQueryCursor, error) {
	if len(value) > MaximumSearchCursorBytes {
		return searchQueryCursor{}, fmt.Errorf("provenance search cursor must be at most %d bytes", MaximumSearchCursorBytes)
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return searchQueryCursor{}, errors.New("provenance search cursor is invalid")
	}
	var cursor encodedSearchCursor
	if err := decodeStrictJSON(payload, &cursor); err != nil {
		return searchQueryCursor{}, errors.New("provenance search cursor is invalid")
	}
	collection, supported := searchCollectionForPriority(cursor.CollectionPriority)
	if cursor.SchemaVersion != searchCursorSchemaVersion || cursor.RequestDigest != requestDigest ||
		cursor.Score < 1 || !supported || !searchCollectionSelected(collection, collections) ||
		cursor.SortTime.IsZero() || validateSearchCursorID(collection, cursor.ID) != nil {
		return searchQueryCursor{}, errors.New("provenance search cursor does not match this request")
	}
	return searchQueryCursor{
		Enabled: true, Score: cursor.Score, CollectionPriority: cursor.CollectionPriority,
		SortTime: cursor.SortTime.UTC(), ID: cursor.ID,
	}, nil
}

func searchCollectionForPriority(priority int) (SearchCollection, bool) {
	switch priority {
	case searchAcceptedRecordPriority:
		return SearchCollectionAcceptedRecords, true
	case searchUnresolvedCasePriority:
		return SearchCollectionUnresolvedCases, true
	case searchPendingCandidatePriority:
		return SearchCollectionPendingCandidates, true
	case searchRepositoryStatePriority:
		return SearchCollectionRepositoryState, true
	case searchProjectStatePriority:
		return SearchCollectionProjectState, true
	default:
		return "", false
	}
}

func searchCollectionSelected(collection SearchCollection, selected []SearchCollection) bool {
	for _, value := range selected {
		if value == collection {
			return true
		}
	}
	return false
}

func validateSearchCursorID(collection SearchCollection, id string) error {
	if collection == SearchCollectionRepositoryState {
		return ids.Validate("repo", id)
	}
	if collection == SearchCollectionProjectState {
		return ids.Validate("project", id)
	}
	_, err := ParseSemanticID(id)
	return err
}

func searchRowLess(left, right searchQueryRow) bool {
	if left.Score != right.Score {
		return left.Score > right.Score
	}
	if left.CollectionPriority != right.CollectionPriority {
		return left.CollectionPriority < right.CollectionPriority
	}
	if !left.SortTime.Equal(right.SortTime) {
		return left.SortTime.After(right.SortTime)
	}
	return left.ID < right.ID
}

func selectBoundedSearchRows(rows []searchQueryRow, limit int) ([]searchQueryRow, map[SearchCollection]bool) {
	selected := make([]searchQueryRow, 0, limit)
	more := map[SearchCollection]bool{}
	perCollection := map[SearchCollection]int{}
	firstUnselected := len(rows)
	for index, row := range rows {
		if len(selected) >= limit || perCollection[row.Collection] >= MaximumSearchResultsPerCollection {
			firstUnselected = index
			break
		}
		selected = append(selected, row)
		perCollection[row.Collection]++
	}
	for _, row := range rows[firstUnselected:] {
		more[row.Collection] = true
	}
	return selected, more
}

func buildSearchResponse(query string, items []materializedSearchResult, more map[SearchCollection]bool) SearchResponse {
	response := SearchResponse{
		SchemaVersion: SearchSchemaVersion, Query: query, Ordering: searchOrdering,
		AcceptedRecords:   SearchCollectionPage[AcceptedRecordSearchResult]{Items: []AcceptedRecordSearchResult{}, Truncated: more[SearchCollectionAcceptedRecords]},
		PendingCandidates: SearchCollectionPage[PendingCandidateSearchResult]{Items: []PendingCandidateSearchResult{}, Truncated: more[SearchCollectionPendingCandidates]},
		UnresolvedCases:   SearchCollectionPage[UnresolvedCaseSearchResult]{Items: []UnresolvedCaseSearchResult{}, Truncated: more[SearchCollectionUnresolvedCases]},
		RepositoryState:   SearchCollectionPage[RepositoryStateSearchResult]{Items: []RepositoryStateSearchResult{}, Truncated: more[SearchCollectionRepositoryState]},
		ProjectState:      SearchCollectionPage[ProjectStateSearchResult]{Items: []ProjectStateSearchResult{}, Truncated: more[SearchCollectionProjectState]},
		Returned:          len(items),
	}
	for _, item := range items {
		switch {
		case item.record != nil:
			response.AcceptedRecords.Items = append(response.AcceptedRecords.Items, *item.record)
		case item.candidate != nil:
			response.PendingCandidates.Items = append(response.PendingCandidates.Items, *item.candidate)
		case item.caseItem != nil:
			response.UnresolvedCases.Items = append(response.UnresolvedCases.Items, *item.caseItem)
		case item.repository != nil:
			response.RepositoryState.Items = append(response.RepositoryState.Items, *item.repository)
		case item.project != nil:
			response.ProjectState.Items = append(response.ProjectState.Items, *item.project)
		}
	}
	return response
}

func repositorySearchFields(card RepositoryCard) []searchField {
	return []searchField{
		{name: "repository_id", value: card.RepositoryID},
		{name: "name", value: card.Name},
		{name: "aliases", value: strings.Join(card.Aliases, " ")},
		{name: "owning_project_context", value: strings.Join([]string{card.OwningProject.ProjectID, card.OwningProject.Name, card.OwningProject.Slug}, " ")},
		{name: "role", value: card.Role},
		{name: "purpose", value: card.Purpose},
		{name: "topics", value: strings.Join(card.Topics, " ")},
		{name: "current_state", value: card.CurrentState},
		{name: "active_focus", value: strings.Join(card.ActiveFocus, " ")},
		{name: "recent_outcomes", value: strings.Join(card.RecentOutcomes, " ")},
		{name: "next_priorities", value: strings.Join(card.NextPriorities, " ")},
		{name: "blockers", value: strings.Join(card.Blockers, " ")},
		{name: "tracking_status", value: string(card.TrackingStatus)},
	}
}

func repositorySearchCurrentness(card RepositoryCard) string {
	if posture := strings.TrimSpace(card.Freshness.Posture); posture != "" {
		return posture
	}
	if card.TrackingStatus != "" {
		return string(card.TrackingStatus)
	}
	return "projected"
}

type searchField struct {
	name  string
	value string
}

func newSearchCompactMatch(terms []string, fields []searchField, summary, sourceKind string, sourceIDs []SemanticID, sourceTruncated bool, assertionPosture, currentness string, recordedAt time.Time, observedAt, validFrom, validUntil *time.Time, projects, repositories []string) SearchCompactMatch {
	matchedTerms, matchedFields := explainSearchMatch(terms, fields)
	explanation := "Matched " + strings.Join(matchedFields, ", ") + " on: " + strings.Join(matchedTerms, ", ") + "."
	boundedSourceIDs := append([]SemanticID(nil), sourceIDs...)
	if len(boundedSourceIDs) > MaximumSearchSourceReferences {
		boundedSourceIDs = boundedSourceIDs[:MaximumSearchSourceReferences]
		sourceTruncated = true
	}
	return SearchCompactMatch{
		Summary:          boundedSearchText(summary, searchInitialSummaryRunes),
		MatchExplanation: boundedSearchText(explanation, searchInitialExplanationRunes),
		MatchedFields:    append([]string(nil), matchedFields...), MatchedTerms: append([]string(nil), matchedTerms...),
		Source:           SearchSourceSummary{Kind: sourceKind, SourceReferenceIDs: boundedSourceIDs, Truncated: sourceTruncated},
		Freshness:        SearchFreshness{RecordedAt: recordedAt.UTC(), ObservedAt: utcTimePointer(observedAt), ValidFrom: utcTimePointer(validFrom), ValidUntil: utcTimePointer(validUntil), Currentness: currentness},
		AssertionPosture: assertionPosture,
		ProjectIDs:       appendUniqueBounded(nil, projects, 4), RepositoryIDs: appendUniqueBounded(nil, repositories, 4),
	}
}

func explainSearchMatch(terms []string, fields []searchField) ([]string, []string) {
	var matchedTerms, matchedFields []string
	for _, field := range fields {
		value := strings.ToLower(field.value)
		fieldMatched := false
		for _, term := range terms {
			if strings.Contains(value, term) {
				matchedTerms = appendUniqueBounded(matchedTerms, []string{term}, 8)
				fieldMatched = true
			}
		}
		if fieldMatched {
			matchedFields = append(matchedFields, field.name)
		}
	}
	return matchedTerms, matchedFields
}

func fitSearchCompactResult(match *SearchCompactMatch, value func() any) error {
	for {
		encodedRunes := jsonRuneCount(value())
		if encodedRunes <= MaximumSearchCompactResult {
			return nil
		}
		match.CompactTruncated = true
		switch {
		case utf8.RuneCountInString(match.Summary.Text) > 80:
			match.Summary = boundedSearchText(match.Summary.Text, utf8.RuneCountInString(match.Summary.Text)-16)
			match.Summary.Truncated = true
		case utf8.RuneCountInString(match.MatchExplanation.Text) > 96:
			match.MatchExplanation = boundedSearchText(match.MatchExplanation.Text, utf8.RuneCountInString(match.MatchExplanation.Text)-16)
			match.MatchExplanation.Truncated = true
		case len(match.Source.SourceReferenceIDs) > 0:
			match.Source.SourceReferenceIDs = match.Source.SourceReferenceIDs[:len(match.Source.SourceReferenceIDs)-1]
			match.Source.Truncated = true
		case len(match.MatchedTerms) > 0:
			match.MatchedTerms = match.MatchedTerms[:len(match.MatchedTerms)-1]
		case len(match.MatchedFields) > 0:
			match.MatchedFields = match.MatchedFields[:len(match.MatchedFields)-1]
		case len(match.ProjectIDs) > 0:
			match.ProjectIDs = match.ProjectIDs[:len(match.ProjectIDs)-1]
		case len(match.RepositoryIDs) > 0:
			match.RepositoryIDs = match.RepositoryIDs[:len(match.RepositoryIDs)-1]
		case utf8.RuneCountInString(match.Summary.Text) > 0:
			match.Summary = boundedSearchText(match.Summary.Text, utf8.RuneCountInString(match.Summary.Text)-1)
			match.Summary.Truncated = true
		case utf8.RuneCountInString(match.MatchExplanation.Text) > 0:
			match.MatchExplanation = boundedSearchText(match.MatchExplanation.Text, utf8.RuneCountInString(match.MatchExplanation.Text)-1)
			match.MatchExplanation.Truncated = true
		default:
			return fmt.Errorf("typed search result requires %d code points after bounded truncation; maximum is %d", encodedRunes, MaximumSearchCompactResult)
		}
	}
}

func boundedSearchText(value string, maximum int) SearchText {
	runes := []rune(value)
	if len(runes) <= maximum {
		return SearchText{Text: value}
	}
	if maximum < 2 {
		return SearchText{Text: string(runes[:maximum]), Truncated: true}
	}
	return SearchText{Text: strings.TrimSpace(string(runes[:maximum-1])) + "…", Truncated: true}
}

func searchResponseRuneCount(response SearchResponse) int { return jsonRuneCount(response) }

func jsonRuneCount(value any) int {
	payload, err := json.Marshal(value)
	if err != nil {
		return MaximumSearchCompactResponse + MaximumSearchCompactResult + 1
	}
	return utf8.RuneCount(payload)
}

func searchableAnchors(anchors *StructuralAnchors) string {
	if anchors == nil {
		return ""
	}
	return strings.Join(append(append(append([]string{}, anchors.Entities...), anchors.Topics...), anchors.Projects...), " ")
}

func anchorProjects(anchors *StructuralAnchors) []string {
	if anchors == nil {
		return nil
	}
	return append([]string(nil), anchors.Projects...)
}

func anchorRepositories(anchors *StructuralAnchors) []string {
	if anchors == nil {
		return nil
	}
	var result []string
	for _, entity := range anchors.Entities {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(entity)), "repo_") {
			result = append(result, entity)
		}
	}
	return result
}

func appendUniqueBounded(destination, values []string, limit int) []string {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		found := false
		for _, existing := range destination {
			if existing == value {
				found = true
				break
			}
		}
		if !found {
			destination = append(destination, value)
		}
		if len(destination) >= limit {
			break
		}
	}
	return destination
}

func caseEvidenceSourceIDs(events []CaseEvent) []SemanticID {
	var result []SemanticID
	for _, event := range events {
		for _, id := range event.EvidenceSourceIDs {
			found := false
			for _, existing := range result {
				if existing == id {
					found = true
					break
				}
			}
			if !found {
				result = append(result, id)
			}
		}
	}
	return result
}

func ptrTime(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func utcTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := value.UTC()
	return &normalized
}

var _ SearchTransport = (*FoundationAPI)(nil)
