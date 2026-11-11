package knowledge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type notesCancellableQueryRuntime struct {
	started chan struct{}
	release chan struct{}
	stopped chan struct{}
	failure error
	calls   int
}

func (r *notesCancellableQueryRuntime) Health(context.Context) EmbeddingRuntimeHealth {
	return EmbeddingRuntimeHealth{Available: true}
}

func (r *notesCancellableQueryRuntime) Embed(ctx context.Context, _ EmbeddingRuntimeRequest) (EmbeddingRuntimeResponse, error) {
	r.calls++
	close(r.started)
	defer close(r.stopped)
	select {
	case <-ctx.Done():
		return EmbeddingRuntimeResponse{}, ctx.Err()
	case <-r.release:
		return EmbeddingRuntimeResponse{Embeddings: [][]float32{{1, 2, 3}}}, r.failure
	}
}

func TestNotesQueryEmbeddingOverlapsAndJoinsOnce(t *testing.T) {
	for _, failure := range []error{nil, &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorUnavailable}, errors.New("invalid response")} {
		runtime := &notesCancellableQueryRuntime{started: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{}), failure: failure}
		s := &Service{embeddingRuntime: runtime}
		settings := EmbeddingSettings{Dimensions: 3}
		query, finish := s.startNotesQueryEmbedding(t.Context(), "thermal question", settings)
		t.Cleanup(finish)
		select {
		case <-runtime.started:
		case <-time.After(time.Second):
			t.Fatal("embedding did not start independently of the SQL phase")
		}
		select {
		case <-query.done:
			t.Fatal("embedding unexpectedly completed before release")
		default:
		}
		close(runtime.release)
		for range 3 {
			vector, err := s.notesSearchQueryVector(t.Context(), NotesSearchInput{queryEmbedding: query}, settings)
			if err != failure || (failure == nil && len(vector) != 3) {
				t.Fatalf("joined result changed: %v %v", vector, err)
			}
		}
		finish()
		if runtime.calls != 1 {
			t.Fatal("refill or partitions repeated query embedding")
		}
	}
}

func TestNotesQueryEmbeddingEarlyReturnCancelsAndJoins(t *testing.T) {
	runtime := &notesCancellableQueryRuntime{started: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{})}
	s := &Service{embeddingRuntime: runtime}
	query, finish := s.startNotesQueryEmbedding(t.Context(), "thermal question", EmbeddingSettings{Dimensions: 3})
	finish()
	select {
	case <-runtime.stopped:
	default:
		t.Fatal("request returned before cancelled embedding stopped")
	}
	if !errors.Is(query.err, context.Canceled) || !query.loaded || runtime.calls != 1 {
		t.Fatalf("early exit did not cancel the sole embedding operation: %+v", query)
	}
}

func TestNotesArchiveSearchLifecycleQueryContracts(t *testing.T) {
	stop := notesProjectArchiveStopSQL("r", "o", "archive_id")
	// Deactivation preserves declaration ownership and knowledge-source metadata;
	// only the audit fields identify the stop. Privacy and declaration gates stay.
	for _, required := range []string{
		"stopped_writer.metadata @> jsonb_build_object",
		"stopped_writer.last_applied_by_actor_id=stopped_archive.actor_id",
		"stopped_writer.updated_at=stopped_writer.last_applied_at",
		"r.metadata->'registration_metadata'=stopped_writer.metadata",
	} {
		if !strings.Contains(stop, required) {
			t.Fatalf("archive stop guard missing: %s", required)
		}
	}
	for _, filter := range []SourceLifecycleFilter{"", "active", "archived", "all"} {
		input := NotesSearchInput{Query: "cobalt", SourceLifecycle: filter, Limit: 2}
		lexical, err := buildNotesSearchQuery(input)
		if err != nil {
			t.Fatal(err)
		}
		semantic, err := buildSemanticNotesSearchQuery(input, EmbeddingSettings{}, []float32{1})
		if err != nil {
			t.Fatal(err)
		}
		for _, query := range []notesSearchQuery{lexical, semantic} {
			predicate := notesLifecycleSelectionSQL("ko", filter)
			if !strings.Contains(query.SQL, "AND "+predicate) || !strings.Contains(query.SQL, "notes_custody_projection_receipts") || !strings.Contains(query.SQL, "visibility_file.index_policy") {
				t.Fatal("lifecycle/current-evidence predicate missing")
			}
			if strings.Index(query.SQL, "AND "+predicate) > strings.LastIndex(query.SQL, "LIMIT $") {
				t.Fatal("lifecycle applied after candidate limit")
			}
		}
	}
	for _, filter := range []SourceLifecycleFilter{"latest", "ACTIVE", " archived", "archived' OR true --"} {
		input := NotesSearchInput{Query: "cobalt", SourceLifecycle: filter}
		if ValidateNotesSearchInput(input) == nil {
			t.Fatal("invalid lifecycle accepted")
		}
		if _, err := buildNotesSearchQuery(input); err == nil {
			t.Fatal("invalid lexical lifecycle accepted")
		}
		if _, err := buildSemanticNotesSearchQuery(input, EmbeddingSettings{}, []float32{1}); err == nil {
			t.Fatal("invalid semantic lifecycle accepted")
		}
	}
	parsed := ParseNotesSearchInput("cobalt lifecycle:archived")
	if parsed.Query != "cobalt" || parsed.SourceLifecycle != SourceLifecycleFilterArchived {
		t.Fatalf("parsed lifecycle: %+v", parsed)
	}
}

func TestNotesArchiveSearchGroupBoundsAndOmittedTruth(t *testing.T) {
	for limit := 1; limit <= 50; limit++ {
		for active := 0; active <= 55; active++ {
			for archived := 0; archived <= 55; archived++ {
				a, b := notesLifecycleGroupLimits(active, archived, limit)
				if a > active || b > archived || a+b != minInt(active+archived, limit) || a < 0 || b < 0 {
					t.Fatalf("group budget: %d/%d/%d -> %d/%d", active, archived, limit, a, b)
				}
				if active >= limit && archived >= limit && (a != (limit+1)/2 || b != limit/2) {
					t.Fatal("groups do not share budget")
				}
			}
		}
	}
	rows := []NotesSearchResult{{KnowledgeObjectID: "one"}, {KnowledgeObjectID: "one"}, {KnowledgeObjectID: "two"}}
	if n, truncated := notesPartitionMatchCount(rows, rows, 4); n != 2 || truncated {
		t.Fatalf("duplicate count: %d/%t", n, truncated)
	}
	if n, truncated := notesPartitionMatchCount(rows, nil, 3); n != 2 || !truncated {
		t.Fatal("candidate exhaustion represented as exact")
	}
	rows = nil
	for i := 0; i < 501; i++ {
		rows = append(rows, NotesSearchResult{KnowledgeObjectID: fmt.Sprint(i)})
	}
	if n, truncated := notesPartitionMatchCount(rows, nil, 1000); n != 500 || !truncated {
		t.Fatal("count cap not explicit")
	}
}
