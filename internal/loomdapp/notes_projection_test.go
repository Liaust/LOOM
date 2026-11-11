package loomdapp

import (
	"testing"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesprojection"
	"loom.local/loom/internal/workers"
	workerruntimes "loom.local/loom/internal/workers/runtimes"
)

func TestNotesProjectionRegisteredSeparately(t *testing.T) {
	registry := workers.NewRegistry()
	service := knowledge.NewService(nil)
	reader := knowledge.NewStore(nil)
	if err := registerNotesProjection(nil, config.Config{}, service, reader, registry); err != nil {
		t.Fatal(err)
	}
	runtime, ok := registry.Get(notesprojection.WorkerKind)
	if !ok {
		t.Fatal("projection consumer missing")
	}
	projection, ok := runtime.(workerruntimes.NotesProjectionRuntime)
	if !ok || projection.Dirty == nil || projection.Projection == nil || projection.LoadCheckpoint == nil {
		t.Fatal("projection dependencies missing")
	}
	view, ok := projection.Projection.(*notesprojection.Service)
	if !ok || view.Sources != reader || projection.Dirty != service {
		t.Fatal("read-plan affinity changed the existing dirty-state owner")
	}
	indexer := workerruntimes.NewKnowledgeIndexerRuntime(service)
	if indexer.Projection != nil {
		t.Fatal("ordinary ingestion retained synchronous projection work")
	}
}
