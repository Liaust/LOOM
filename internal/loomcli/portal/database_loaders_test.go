package portal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/response"
)

func TestDatabaseIndependentReadsBoundConcurrencyAndPreservePartialOrder(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{}, 11)
	var mu sync.Mutex
	active, maximum, total := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		active++
		total++
		if active > maximum {
			maximum = active
		}
		mu.Unlock()
		defer func() { mu.Lock(); active--; mu.Unlock() }()
		if r.Method != http.MethodGet {
			t.Errorf("parallelized mutation %s", r.Method)
		}
		started <- struct{}{}
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/objects" || r.URL.Path == "/v1/indexes/queue" {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"ok":false,"error":{"code":"source.failed","domain":"runtime","summary":"owned source unavailable"}}`)
			return
		}
		var data any = []any{}
		switch r.URL.Path {
		case "/v1/sync/status":
			data = map[string]any{"summary": map[string]any{"open_conflict_count": 5}}
		case "/v1/sync/batches":
			data = []any{map[string]any{"sync_batch_id": "owned_batch", "status": "accepted"}}
		case "/v1/workers":
			data = []any{map[string]any{"worker_key": "owned_worker"}}
		}
		if err := json.NewEncoder(w).Encode(response.Success("corr_database", data)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client, err := localclient.NewHTTP(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resultCh := make(chan ScreenLoadResult, 1)
	go func() { resultCh <- LoadScreen(ctx, client, "corr_database", ScreenDatabase, Snapshot{}) }()
	for i := 0; i < databaseReadConcurrency; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			close(gate)
			t.Fatal("independent screen reads did not overlap")
		}
	}
	close(gate)
	var result ScreenLoadResult
	select {
	case result = <-resultCh:
	case <-ctx.Done():
		t.Fatal("screen read did not finish")
	}
	mu.Lock()
	observedMax, observedTotal := maximum, total
	mu.Unlock()
	if observedMax != 4 || observedTotal != 11 {
		t.Fatalf("concurrency=%d requests=%d", observedMax, observedTotal)
	}
	if result.State.Status != ScreenLoadPartial {
		t.Fatalf("state=%+v", result.State)
	}
	var sources []string
	for _, partial := range result.PartialErrors {
		sources = append(sources, partial.Source)
	}
	if !reflect.DeepEqual(sources, []string{"database_objects", "database_index_queue"}) {
		t.Fatalf("partial order=%v", sources)
	}
	data := result.State.Data.Database
	if len(data.SyncBatches) != 1 || data.SyncBatches[0].SyncBatchID != "owned_batch" || len(data.Workers) != 1 || data.Workers[0].WorkerKey != "owned_worker" {
		t.Fatalf("successful panels were lost: %+v", data)
	}
}

func TestDatabaseIndependentReadsReceiveDeadlineAndSkipCancelledQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	builder := newScreenLoadBuilder(ScreenDatabase, Snapshot{})
	reads := make([]databasePanelRead, 8)
	var mu sync.Mutex
	called := 0
	for i := range reads {
		reads[i] = databasePanelRead{source: fmt.Sprintf("source_%d", i), load: func(ctx context.Context) func() {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > databaseReadTimeout {
				t.Error("read has no bounded deadline")
			}
			mu.Lock()
			called++
			mu.Unlock()
			cancel()
			<-ctx.Done()
			return func() { builder.addPartial("read_cancelled", ctx.Err()) }
		}}
	}
	collectDatabasePanels(ctx, &builder, reads)
	if called < 1 || called > databaseReadConcurrency || len(builder.partials) != len(reads) {
		t.Fatalf("called=%d partials=%v", called, builder.partials)
	}
	// A cancelled parent must not issue another set of client reads.
	called = 0
	collectDatabasePanels(ctx, &builder, reads)
	if called != 0 {
		t.Fatalf("cancelled parent started %d reads", called)
	}
}
