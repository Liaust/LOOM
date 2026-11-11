package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Groups address contiguous ranges in Results; passages are serialized once.
type NotesSearchLifecycleGroup struct {
	SourceLifecycle SourceLifecycle `json:"source_lifecycle"`
	Offset          int             `json:"offset"`
	ResultCount     int             `json:"result_count"`
}

// The embedding worker never receives the SQL transaction. Its result is read
// only after done closes, and every request joins it even on an early SQL error.
type notesQueryEmbedding struct {
	loaded bool
	vector []float32
	err    error
	done   chan struct{}
}

func (q *notesQueryEmbedding) wait() {
	if q != nil && q.done != nil {
		<-q.done
	}
}

func (s *Service) startNotesQueryEmbedding(ctx context.Context, query string, settings EmbeddingSettings) (*notesQueryEmbedding, func()) {
	ctx, cancel := context.WithCancel(ctx)
	q := &notesQueryEmbedding{done: make(chan struct{})}
	go func() {
		defer close(q.done)
		q.vector, q.err = s.notesSearchQueryVector(ctx, NotesSearchInput{Query: query}, settings)
		q.loaded = true
	}()
	return q, func() {
		cancel()
		q.wait()
	}
}

type notesSearchPartition struct {
	results          []NotesSearchResult
	matches          int
	truncated        bool
	omitted          int
	omittedTruncated bool
	fallback         string
}

func (s *Service) searchNotesLifecycles(ctx context.Context, input NotesSearchInput, mode, requested string, semanticAvailable bool, fallback string, settings EmbeddingSettings) (NotesSearchResultSet, error) {
	input.queryEmbedding = &notesQueryEmbedding{}
	if mode != NotesSearchModeLexical && fallback == "" {
		var finish func()
		input.queryEmbedding, finish = s.startNotesQueryEmbedding(ctx, input.Query, settings)
		defer finish()
	}
	tx, err := s.store.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return NotesSearchResultSet{}, err
	}
	defer tx.Rollback()
	input.readTx = tx
	input.Mode = mode
	if err := s.prepareIndexedNotesSearch(ctx, &input, mode, fallback, settings); err != nil {
		return NotesSearchResultSet{}, err
	}
	// Also join for the pre-index compatibility reader, which has no candidate
	// phase at which to await the vector.
	if mode != NotesSearchModeLexical && fallback == "" {
		if _, err := s.notesSearchQueryVector(ctx, input, settings); err != nil && (mode != NotesSearchModeHybrid || !semanticSearchFallbackAllowed(err)) {
			return NotesSearchResultSet{}, err
		}
	}
	now := s.currentTime()
	if input.fileView && input.indexed != nil {
		if err := s.selectIndexedNotesFiles(ctx, input, mode, now); err != nil {
			return NotesSearchResultSet{}, err
		}
	}
	if input.indexed == nil && mode == NotesSearchModeHybrid && fallback == "" && input.queryEmbedding.err == nil {
		input.visibleObjectIDsJSON, err = s.loadNotesVisibleObjectIDs(ctx, input)
		if err != nil {
			return NotesSearchResultSet{}, err
		}
	}
	if mode != NotesSearchModeLexical && fallback == "" && input.queryEmbedding.err == nil && notesFileHydratesLane(input, true) {
		semanticInput := input
		semanticInput.Mode = mode
		input.semanticBatch, err = s.loadNotesSemanticBatch(ctx, semanticInput, settings, input.queryEmbedding.vector)
		if err != nil {
			return NotesSearchResultSet{}, err
		}
	}
	if mode != NotesSearchModeSemantic && notesFileHydratesLane(input, false) {
		input.lexicalBatch, err = s.loadNotesLexicalBatch(ctx, input)
		if err != nil {
			return NotesSearchResultSet{}, err
		}
	}
	active, archived := notesSearchPartition{}, notesSearchPartition{}
	if input.SourceLifecycle != SourceLifecycleFilterArchived {
		activeInput := input
		activeInput.SourceLifecycle = SourceLifecycleFilterActive
		var err error
		active, err = s.searchNotesPartition(ctx, activeInput, mode, settings, fallback, now)
		if err != nil {
			return NotesSearchResultSet{}, err
		}
	}
	archiveInput := input
	archiveInput.SourceLifecycle = SourceLifecycleFilterArchived
	if input.SourceLifecycle == SourceLifecycleFilterActive {
		archiveInput.Limit = 50
	}
	if !input.fileView || input.SourceLifecycle != SourceLifecycleFilterActive {
		archived, err = s.searchNotesPartition(ctx, archiveInput, mode, settings, fallback, now)
		if err != nil {
			return NotesSearchResultSet{}, err
		}
	}
	out := NotesSearchResultSet{Query: notesSearchDisplayQuery(input), Mode: mode, RequestedMode: requested,
		SemanticAvailable: semanticAvailable, FallbackReason: firstNonEmpty(fallback, active.fallback, archived.fallback),
		SourceLifecycle: input.SourceLifecycle, Results: []NotesSearchResult{}, LifecycleGroups: []NotesSearchLifecycleGroup{}}
	appendGroup := func(lifecycle SourceLifecycle, results []NotesSearchResult) {
		out.LifecycleGroups = append(out.LifecycleGroups, NotesSearchLifecycleGroup{SourceLifecycle: lifecycle, Offset: len(out.Results), ResultCount: len(results)})
		out.Results = append(out.Results, results...)
	}
	switch input.SourceLifecycle {
	case SourceLifecycleFilterAll:
		a, b := notesLifecycleGroupLimits(len(active.results), len(archived.results), input.Limit)
		appendGroup(SourceLifecycleActive, active.results[:a])
		appendGroup(SourceLifecycleArchived, archived.results[:b])
	case SourceLifecycleFilterArchived:
		appendGroup(SourceLifecycleArchived, archived.results)
	default:
		appendGroup(SourceLifecycleActive, active.results)
		out.ArchivedMatchesOmitted, out.ArchivedMatchesOmittedTruncated = archived.matches, archived.truncated
	}
	out.ResultCount = len(out.Results)
	out.Coverage = notesSearchReturnedCoverage(out.Results)
	out.CandidateRetrievalTruncated = active.truncated || archived.truncated
	if input.SourceLifecycle != SourceLifecycleFilterArchived {
		out.RefreshingMatchesOmitted += active.omitted
		out.RefreshingMatchesOmittedTruncated = active.omittedTruncated
	}
	if input.SourceLifecycle != SourceLifecycleFilterActive {
		out.RefreshingMatchesOmitted += archived.omitted
		out.RefreshingMatchesOmittedTruncated = out.RefreshingMatchesOmittedTruncated || archived.omittedTruncated
	}
	if input.fileView {
		if err := s.loadNotesFileIdentities(ctx, input, out.Results); err != nil {
			return NotesSearchResultSet{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return NotesSearchResultSet{}, err
	}
	return out, nil
}

func (s *Service) queryNotesSearch(ctx context.Context, input NotesSearchInput, query notesSearchQuery) (*sql.Rows, error) {
	if input.readTx != nil {
		return input.readTx.QueryContext(ctx, query.SQL, query.Args...)
	}
	return s.store.db.QueryContext(ctx, query.SQL, query.Args...)
}

// Admission is shared only by lexical and semantic queries in this one
// repeatable-read transaction. Never retain it across requests or transactions.
func (s *Service) loadNotesVisibleObjectIDs(ctx context.Context, input NotesSearchInput) (string, error) {
	if input.readTx == nil {
		return "", fmt.Errorf("shared Notes admission requires a request snapshot")
	}
	input.SourceLifecycle = SourceLifecycleFilterAll
	input.visibleObjectIDsJSON = ""
	input.indexedNoCustody = false
	if input.candidateObjectIDsJSON != "" {
		if err := input.readTx.QueryRowContext(ctx, `SELECT NOT EXISTS (
 SELECT 1 FROM jsonb_array_elements_text($1::jsonb) requested(id)
 JOIN knowledge.notes_custody_transitions history ON history.knowledge_object_id=requested.id)`,
			input.candidateObjectIDsJSON).Scan(&input.indexedNoCustody); err != nil {
			return "", err
		}
	}
	var args []any
	query := "SELECT knowledge_object_id FROM (" + notesSearchObjectsSQL(input, &args) + ") visible_objects"
	rows, err := input.readTx.QueryContext(ctx, query, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(ids)
	return string(raw), err
}

func notesLifecycleGroupLimits(active, archived, limit int) (int, int) {
	a := minInt(active, (limit+1)/2)
	b := minInt(archived, limit-a)
	a = minInt(active, limit-b)
	return a, b
}

func (s *Service) searchNotesPartition(ctx context.Context, input NotesSearchInput, mode string, settings EmbeddingSettings, fallback string, now time.Time) (notesSearchPartition, error) {
	part := notesSearchPartition{results: []NotesSearchResult{}, fallback: fallback}
	if input.RequireCurrent && !input.fileView {
		all := input
		all.RequireCurrent = false
		all.Limit = 50
		unrestricted, err := s.searchNotesPartition(ctx, all, mode, settings, fallback, now)
		if err != nil {
			return part, err
		}
		for _, r := range unrestricted.results {
			if !r.Freshness.Current {
				part.omitted++
			}
		}
		part.omittedTruncated = unrestricted.truncated || len(unrestricted.results) == 50
	}
	input.Mode = mode
	if input.fileView && input.indexed != nil && input.indexed.filePartitions != nil {
		return selectedNotesFilePartition(input, fallback), nil
	}
	var lexicalResults, semanticResults []NotesSearchResult
	var err error
	if mode != NotesSearchModeSemantic {
		lexicalResults, err = s.searchLexicalNotes(ctx, input)
		if err != nil {
			return part, err
		}
	}
	if mode == NotesSearchModeHybrid || (mode == NotesSearchModeSemantic && fallback == "") {
		semanticResults, err = s.searchSemanticNotes(ctx, input, settings)
		if err != nil {
			if mode != NotesSearchModeHybrid || (!semanticSearchFallbackAllowed(err) && fallback == "") {
				return part, err
			}
			part.fallback = firstNonEmpty(fallback, semanticSearchFallbackReason(err))
		}
	}
	part.matches, part.truncated = notesPartitionMatchCount(lexicalResults, semanticResults, notesSearchCandidateLimit(input.Limit))
	if input.indexed != nil {
		key := notesLexicalVariant{input.SourceLifecycle, input.RequireCurrent, input.Limit}
		part.truncated = part.truncated || input.indexed.exhausted[key]
	}
	switch mode {
	case NotesSearchModeSemantic:
		part.results = groupNotesSearchResults(rerankNotesSearchResults(semanticResults, input, now, NotesSearchModeSemantic), input.Limit)
	case NotesSearchModeHybrid:
		if len(semanticResults) != 0 {
			part.results = fuseNotesSearchResults(lexicalResults, semanticResults, input, now)
			break
		}
		fallthrough
	default:
		part.results = groupNotesSearchResults(rerankNotesSearchResults(lexicalResults, input, now, NotesSearchModeLexical), input.Limit)
	}
	return part, nil
}

func notesPartitionMatchCount(lexical, semantic []NotesSearchResult, candidateLimit int) (int, bool) {
	seen := map[string]bool{}
	for _, items := range [][]NotesSearchResult{lexical, semantic} {
		for _, item := range items {
			key := item.KnowledgeObjectID
			if key == "" {
				key = item.SearchDocumentID
			}
			seen[key] = true
		}
	}
	return minInt(len(seen), 500), len(seen) > 500 || len(lexical) >= candidateLimit || len(semantic) >= candidateLimit
}

func decodeNotesSearchCustody(raw []byte, result *NotesSearchResult) error {
	var additional struct {
		Freshness NotesSearchFreshness `json:"freshness"`
	}
	if err := json.Unmarshal(raw, &additional); err != nil {
		return err
	}
	result.Freshness = additional.Freshness
	custody, err := decodeNotesReadCustody(raw, result.SourcePath)
	if err != nil {
		return err
	}
	result.NotesCustodyContext = custody
	return nil
}

func decodeNotesReadCustody(raw []byte, sourcePath string) (NotesCustodyContext, error) {
	var context struct {
		Custody NotesCustodyContext `json:"custody"`
	}
	if err := json.Unmarshal(raw, &context); err != nil {
		return NotesCustodyContext{}, err
	}
	if (context.Custody.SourceLifecycle != SourceLifecycleActive && context.Custody.SourceLifecycle != SourceLifecycleArchived) || context.Custody.OriginalPath != sourcePath {
		return NotesCustodyContext{}, fmt.Errorf("invalid Notes read custody context")
	}
	if context.Custody.SourceLifecycle == SourceLifecycleArchived && (context.Custody.CanonicalPath == "" || context.Custody.ArchiveOperationID == "" || context.Custody.WorkspaceLifecycleEventID == "" || context.Custody.ArchivedAt == nil) {
		return NotesCustodyContext{}, fmt.Errorf("incomplete archived Notes read context")
	}
	return context.Custody, nil
}
