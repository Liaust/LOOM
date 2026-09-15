package knowledge

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

type notesArchiveQueryEmbedding struct {
	calls int
	fail  bool
}

func (r *notesArchiveQueryEmbedding) Health(context.Context) EmbeddingRuntimeHealth {
	return EmbeddingRuntimeHealth{Available: true}
}
func (r *notesArchiveQueryEmbedding) Embed(_ context.Context, request EmbeddingRuntimeRequest) (EmbeddingRuntimeResponse, error) {
	r.calls++
	if len(request.Inputs) != 1 || !strings.HasPrefix(request.Inputs[0], EmbeddingQueryPromptPrefix) {
		panic("archive search attempted source embedding")
	}
	if r.fail {
		return EmbeddingRuntimeResponse{}, &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorUnavailable}
	}
	vector := make([]float32, DefaultEmbeddingDimensions)
	for i := range vector {
		vector[i] = 1
	}
	return EmbeddingRuntimeResponse{Embeddings: [][]float32{vector}, Dimensions: len(vector)}, nil
}

func seedNotesArchiveSearchEmbeddings(t *testing.T, f *notesArchivePostgresFixture) *notesArchiveQueryEmbedding {
	t.Helper()
	setPipelinePolicyForTest(t, f.s.store.db, PipelinePolicy{EmbeddingsEnabled: true})
	settings, err := f.s.store.GetEmbeddingSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.s.store.db.Query(`SELECT ` + knowledgeChunkColumns() + ` FROM knowledge.knowledge_chunks ORDER BY knowledge_chunk_id`)
	if err != nil {
		t.Fatal(err)
	}
	var chunks []KnowledgeChunk
	for rows.Next() {
		chunk, err := scanKnowledgeChunk(rows)
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, chunk := range chunks {
		seedActiveChunkEmbeddingForTest(t, f.s.store.db, chunk, settings, 1, chunk.ChunkHash)
	}
	if _, err := f.s.store.db.Exec(`UPDATE knowledge.chunk_embeddings SET embedding=array_fill(1::real,ARRAY[dimensions])::vector`); err != nil {
		t.Fatal(err)
	}
	runtime := &notesArchiveQueryEmbedding{}
	f.s.embeddingRuntime = runtime
	f.before = f.processingSnapshot(t)
	return runtime
}

func TestNotesArchiveSearchActiveArchivedRestorePostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	runtime := seedNotesArchiveSearchEmbeddings(t, f)
	f.s.store.db.SetMaxOpenConns(1)
	f.archive(t)
	if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{NotesSearchModeLexical, NotesSearchModeSemantic, NotesSearchModeHybrid} {
		t.Run(mode, func(t *testing.T) {
			for _, filter := range []SourceLifecycleFilter{"", SourceLifecycleFilterActive, SourceLifecycleFilterArchived, SourceLifecycleFilterAll} {
				beforeCalls := runtime.calls
				out, err := f.s.SearchNotes(t.Context(), NotesSearchInput{Query: "cobalt observatory", Mode: mode, SourceLifecycle: filter, Limit: 10})
				if err != nil {
					t.Fatal(err)
				}
				want, omitted := 3, 2
				if filter == SourceLifecycleFilterArchived {
					want, omitted = 2, 0
				}
				if filter == SourceLifecycleFilterAll {
					want, omitted = 5, 0
				}
				if out.ResultCount != want || len(out.Results) != want || out.ArchivedMatchesOmitted != omitted || out.ArchivedMatchesOmittedTruncated {
					t.Fatalf("%s result/count: %+v", filter, out)
				}
				if mode != NotesSearchModeLexical && runtime.calls-beforeCalls != 1 {
					t.Fatalf("query embedding calls: %d", runtime.calls-beforeCalls)
				}
				if mode == NotesSearchModeLexical && runtime.calls != beforeCalls {
					t.Fatal("lexical search invoked model")
				}
				next := 0
				for _, group := range out.LifecycleGroups {
					if group.Offset != next {
						t.Fatal("groups have gap/overlap")
					}
					for _, item := range out.Results[group.Offset : group.Offset+group.ResultCount] {
						if item.SourceLifecycle != group.SourceLifecycle || item.OriginalPath != item.SourcePath || item.Citation.SourceRef == "" {
							t.Fatalf("unbound result context: %+v", item)
						}
						archived := strings.HasPrefix(item.RelativePath, "topic-one/")
						if archived != (item.SourceLifecycle == SourceLifecycleArchived) {
							t.Fatal("cross-lifecycle contamination")
						}
						if archived && (item.ArchiveOperationID != f.plan.OperationID || item.CanonicalPath == item.SourcePath || item.ArchivedAt == nil) {
							t.Fatal("archive custody missing")
						}
					}
					next += group.ResultCount
				}
				if next != len(out.Results) {
					t.Fatal("ungrouped results")
				}
			}
		})
	}
	for _, limit := range []int{1, 2, 3, 5} {
		out, err := f.s.SearchNotes(t.Context(), NotesSearchInput{Query: "cobalt", Mode: NotesSearchModeLexical, SourceLifecycle: SourceLifecycleFilterAll, Limit: limit})
		if err != nil || out.ResultCount != limit {
			t.Fatalf("all result bound: %+v %v", out, err)
		}
	}
	// Explicit all partitions retain exactly the same independent rankings.
	active, err := f.s.SearchNotes(t.Context(), NotesSearchInput{Query: "cobalt", Mode: NotesSearchModeLexical, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	all, err := f.s.SearchNotes(t.Context(), NotesSearchInput{Query: "cobalt", Mode: NotesSearchModeLexical, SourceLifecycle: SourceLifecycleFilterAll, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for i := range active.Results {
		if active.Results[i].KnowledgeObjectID != all.Results[i].KnowledgeObjectID || active.Results[i].BM25Score != all.Results[i].BM25Score || active.Results[i].LexicalRank != all.Results[i].LexicalRank {
			t.Fatal("all mixed archive into active ranking")
		}
	}
	if _, err := f.s.store.db.Exec(`UPDATE files.file_metadata SET index_policy='private_no_index' WHERE source_path LIKE '%/topic-one/source.md'`); err != nil {
		t.Fatal(err)
	}
	private, err := f.s.SearchNotes(t.Context(), NotesSearchInput{Query: "cobalt", Mode: NotesSearchModeLexical})
	if err != nil || private.ArchivedMatchesOmitted != 1 {
		t.Fatalf("private omitted count: %+v %v", private, err)
	}
	if _, err := f.s.store.db.Exec(`UPDATE files.file_metadata SET index_policy='text_later' WHERE source_path LIKE '%/topic-one/source.md'`); err != nil {
		t.Fatal(err)
	}
	runtime.fail = true
	beforeCalls := runtime.calls
	fallback, err := f.s.SearchNotes(t.Context(), NotesSearchInput{Query: "cobalt", Mode: NotesSearchModeHybrid, SourceLifecycle: SourceLifecycleFilterAll})
	if err != nil || fallback.ResultCount != 5 || fallback.FallbackReason == "" || runtime.calls-beforeCalls != 1 {
		t.Fatalf("shared fallback: %+v %v calls=%d", fallback, err, runtime.calls-beforeCalls)
	}
	runtime.fail = false
	restore := f.restore(t)
	if _, err := f.p.ProjectOperation(t.Context(), restore.OperationID); err != nil {
		t.Fatal(err)
	}
	final, err := f.s.SearchNotes(t.Context(), NotesSearchInput{Query: "cobalt", Mode: NotesSearchModeHybrid})
	if err != nil || final.ResultCount != 5 || final.ArchivedMatchesOmitted != 0 {
		t.Fatalf("restored search: %+v %v", final, err)
	}
	for _, item := range final.Results {
		if item.SourceLifecycle != SourceLifecycleActive {
			t.Fatal("restored archive label retained")
		}
	}
	if f.processingSnapshot(t) != f.before {
		t.Fatal("search/archive changed source processing")
	}
}

func TestNotesArchiveSearchProjectInactiveRestorePostgres(t *testing.T) {
	for _, material := range []bool{false, true} {
		testNotesArchiveProjectCompletion(t, material, func(f *notesArchivePostgresFixture, object KnowledgeObject, lifecycle SourceLifecycle) {
			for _, filter := range []SourceLifecycleFilter{"", SourceLifecycleFilterArchived, SourceLifecycleFilterAll} {
				result, err := f.s.SearchNotes(t.Context(), NotesSearchInput{Query: "Project decision", ProjectID: *object.ProjectID, Mode: NotesSearchModeLexical, SourceLifecycle: filter})
				if err != nil {
					t.Fatal(err)
				}
				included, err := SourceLifecycleIncluded(filter, lifecycle)
				if err != nil {
					t.Fatal(err)
				}
				want := 0
				if included {
					want = 1
				}
				if result.ResultCount != want {
					t.Fatalf("project lifecycle %s/filter %s: %+v", lifecycle, filter, result)
				}
				if included {
					item := result.Results[0]
					if item.KnowledgeObjectID != object.KnowledgeObjectID || item.SourceLifecycle != lifecycle {
						t.Fatal("project identity/lifecycle changed")
					}
					if material && item.Declaration != "docs" {
						t.Fatalf("archived declaration lost: %+v", item.SourceContext)
					}
				}
			}
		})
	}
}

func TestNotesArchiveSearchSnapshotDuringRestorePostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	f.archive(t)
	if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err != nil {
		t.Fatal(err)
	}
	tx, err := f.s.store.db.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	input := NotesSearchInput{Query: "cobalt", Mode: NotesSearchModeLexical, SourceLifecycle: SourceLifecycleFilterActive, Limit: 10, readTx: tx}
	active, err := f.s.searchLexicalNotes(t.Context(), input)
	if err != nil || len(active) != 3 {
		t.Fatalf("snapshot active: %d %v", len(active), err)
	}
	restore := f.restore(t)
	if _, err := f.p.ProjectOperation(t.Context(), restore.OperationID); err != nil {
		t.Fatal(err)
	}
	input.SourceLifecycle = SourceLifecycleFilterArchived
	archived, err := f.s.searchLexicalNotes(t.Context(), input)
	if err != nil || len(archived) != 2 {
		t.Fatalf("snapshot archived: %d %v", len(archived), err)
	}
	seen := map[string]bool{}
	for _, group := range [][]NotesSearchResult{active, archived} {
		for _, item := range group {
			if seen[item.KnowledgeObjectID] {
				t.Fatal("concurrent restore duplicated lifecycle result")
			}
			seen[item.KnowledgeObjectID] = true
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	fresh, err := f.s.SearchNotes(t.Context(), NotesSearchInput{Query: "cobalt", Mode: NotesSearchModeLexical})
	if err != nil || fresh.ResultCount != 5 || fresh.ArchivedMatchesOmitted != 0 {
		t.Fatalf("fresh snapshot: %+v %v", fresh, err)
	}
}
