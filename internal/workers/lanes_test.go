package workers

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSupervisorLanesKeepControlIndependentAndJoinShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan string, len(supervisorLanes))
	done := make(chan struct{})
	go func() {
		runSupervisorLanes(ctx, func(ctx context.Context, lane string) { started <- lane; <-ctx.Done() })
		close(done)
	}()
	seen := map[string]bool{}
	for range supervisorLanes {
		select {
		case lane := <-started:
			if seen[lane] {
				t.Fatalf("duplicate lane %s", lane)
			}
			seen[lane] = true
		case <-time.After(time.Second):
			t.Fatal("a blocked maintenance lane prevented another lane from starting")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not join every lane")
	}
}

func TestSupervisorLaneSelectionAndWakeParity(t *testing.T) {
	if workerLane(KindKnowledgeRoots) != "background" || workerLane(KindKnowledgeIndexer) != "knowledge" || queueWakeKind(KindKnowledgeRoots) {
		t.Fatal("periodic root repair shares the serial publication or content-wake lane")
	}
	seen := map[string]bool{}
	w := NewQueueWakes()
	for lane, kinds := range supervisorLaneKinds {
		for _, kind := range kinds {
			if seen[kind] || workerLane(kind) != lane || !strings.Contains(workerLaneSQL(), "'"+kind+"'") {
				t.Fatalf("ambiguous SQL/wake ownership for %s", kind)
			}
			seen[kind] = true
			w.SignalLocal(kind)
			select {
			case <-w.channel(lane):
			default:
				t.Fatalf("lost %s hint", kind)
			}
			if !w.take(lane)[kind] {
				t.Fatal("lost exact kind")
			}
		}
	}
	if workerLane(KindMainBackup) != "background" || workerLane(KindCloudSnapshotUpload) != "background" || workerLane("future_kind") != "background" {
		t.Fatal("maintenance order or unknown-kind fallback changed")
	}
	w.catchUp()
	for _, lane := range supervisorLanes {
		select {
		case <-w.channel(lane):
		default:
			t.Fatalf("catch-up omitted %s", lane)
		}
		if !w.take(lane)["*"] {
			t.Fatalf("catch-up marker missing in %s", lane)
		}
	}
}
