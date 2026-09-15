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

// This cache belongs to one sequential search request, never the service.
type notesQueryEmbedding struct {
	loaded bool
	vector []float32
	err    error
}

type notesSearchPartition struct {
	results   []NotesSearchResult
	matches   int
	truncated bool
	fallback  string
}

func (s *Service) searchNotesLifecycles(ctx context.Context, input NotesSearchInput, mode, requested string, semanticAvailable bool, fallback string, settings EmbeddingSettings) (NotesSearchResultSet, error) {
	input.queryEmbedding = &notesQueryEmbedding{}
	if mode != NotesSearchModeLexical && fallback == "" {
		if _, err := s.notesSearchQueryVector(ctx, input, settings); err != nil && (mode != NotesSearchModeHybrid || !semanticSearchFallbackAllowed(err)) {
			return NotesSearchResultSet{}, err
		}
	}
	tx, err := s.store.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return NotesSearchResultSet{}, err
	}
	defer tx.Rollback()
	input.readTx = tx
	now := s.currentTime()
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
	archived, err = s.searchNotesPartition(ctx, archiveInput, mode, settings, fallback, now)
	if err != nil {
		return NotesSearchResultSet{}, err
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

func notesLifecycleGroupLimits(active, archived, limit int) (int, int) {
	a := minInt(active, (limit+1)/2)
	b := minInt(archived, limit-a)
	a = minInt(active, limit-b)
	return a, b
}

func (s *Service) searchNotesPartition(ctx context.Context, input NotesSearchInput, mode string, settings EmbeddingSettings, fallback string, now time.Time) (notesSearchPartition, error) {
	part := notesSearchPartition{results: []NotesSearchResult{}, fallback: fallback}
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
