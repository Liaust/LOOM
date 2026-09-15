package localclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesprojection"
	"loom.local/loom/internal/workers"
)

func TestNotesSearchFollowupClientPassThrough(t *testing.T) {
	for _, lifecycle := range []knowledge.SourceLifecycleFilter{knowledge.SourceLifecycleFilterActive, knowledge.SourceLifecycleFilterArchived} {
		input := knowledge.NotesPassageInput{KnowledgeObjectID: ids.NewKnowledgeObjectID(), KnowledgeObjectVersionID: ids.NewKnowledgeObjectVersionID(), KnowledgeChunkID: ids.NewKnowledgeChunkID(), SourceHash: "sha256:" + strings.Repeat("a", 64), SourceLifecycle: lifecycle}
		for _, withTuple := range []bool{false, true} {
			calls := 0
			client := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					if req.Method != http.MethodPost || req.URL.Path != "/v1/knowledge/notes/search" {
						t.Fatalf("unexpected lookup: %s", req.URL)
					}
					r := knowledge.NotesSearchResult{KnowledgeObjectID: input.KnowledgeObjectID, KnowledgeObjectVersionID: input.KnowledgeObjectVersionID, KnowledgeChunkID: input.KnowledgeChunkID}
					if withTuple {
						r.PassageFollowup = &input
					}
					body, _ := json.Marshal(knowledge.NotesSearchResultSet{ResultCount: 1, Results: []knowledge.NotesSearchResult{r}})
					return okEnvelope(string(body)), nil
				}
				q := req.URL.Query()
				if calls != 2 || req.Method != http.MethodGet || req.URL.Path != "/v1/knowledge/notes/passages/"+input.KnowledgeChunkID || len(q) != 4 || q.Get("object_id") != input.KnowledgeObjectID || q.Get("version_id") != input.KnowledgeObjectVersionID || q.Get("source_hash") != input.SourceHash || q.Get("source_lifecycle") != string(lifecycle) {
					t.Fatalf("exact follow-up changed: %s", req.URL)
				}
				body, _ := json.Marshal(knowledge.NotesPassage{NotesPassageInput: input, Historical: true, Text: "retained"})
				return okEnvelope(string(body)), nil
			})}}
			got, err := client.SearchKnowledgeNotes(t.Context(), "followup", knowledge.NotesSearchInput{Query: "cobalt", SourceLifecycle: lifecycle})
			if err != nil || len(got.Data.Results) != 1 || calls != 1 {
				t.Fatalf("search lookup: %+v %v calls=%d", got, err, calls)
			}
			p := got.Data.Results[0].PassageFollowup
			if !withTuple {
				if p != nil {
					t.Fatal("legacy response fabricated tuple")
				}
				continue
			}
			if p == nil || *p != input {
				t.Fatal("search lost typed tuple")
			}
			passage, err := client.GetKnowledgeNotesPassage(t.Context(), "followup", *p)
			if err != nil || passage.Data.NotesPassageInput != input || !passage.Data.Historical || calls != 2 {
				t.Fatalf("passage: %+v %v", passage, err)
			}
		}
	}
}

func TestNotesArchiveLifecycleClientBindings(t *testing.T) {
	for _, lifecycle := range []knowledge.SourceLifecycleFilter{"", knowledge.SourceLifecycleFilterActive, knowledge.SourceLifecycleFilterArchived, knowledge.SourceLifecycleFilterAll} {
		calls := 0
		client := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			got := req.URL.Query().Get("source_lifecycle")
			if req.Method == http.MethodPost {
				var input knowledge.NotesSearchInput
				if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
					t.Fatal(err)
				}
				got = string(input.SourceLifecycle)
			}
			if got != string(lifecycle) {
				t.Fatalf("lifecycle lost: %s %q", req.URL, got)
			}
			data := "{}"
			if strings.HasSuffix(req.URL.Path, "/roots") || strings.HasSuffix(req.URL.Path, "/objects") {
				data = "[]"
			}
			return okEnvelope(data), nil
		})}}
		if _, err := client.ListKnowledgeNotesRoots(t.Context(), "corr", knowledge.SourceRootFilter{SourceLifecycle: lifecycle}); err != nil {
			t.Fatal(err)
		}
		if _, err := client.ListKnowledgeNotesObjects(t.Context(), "corr", knowledge.KnowledgeObjectFilter{SourceLifecycle: lifecycle}); err != nil {
			t.Fatal(err)
		}
		if _, err := client.GetKnowledgeNotesOverview(t.Context(), "corr", knowledge.NotesOverviewInput{SourceLifecycle: lifecycle}); err != nil {
			t.Fatal(err)
		}
		if _, err := client.GetKnowledgeNotesObjectWithLifecycle(t.Context(), "corr", "exact", lifecycle); err != nil {
			t.Fatal(err)
		}
		if _, err := client.SearchKnowledgeNotes(t.Context(), "corr", knowledge.NotesSearchInput{Query: "cobalt", SourceLifecycle: lifecycle}); err != nil {
			t.Fatal(err)
		}
		citation := knowledge.NotesPassageInput{KnowledgeObjectID: ids.NewKnowledgeObjectID(), KnowledgeObjectVersionID: ids.NewKnowledgeObjectVersionID(), KnowledgeChunkID: ids.NewKnowledgeChunkID(), SourceHash: "sha256:" + strings.Repeat("a", 64), SourceLifecycle: lifecycle}
		if _, err := client.GetKnowledgeNotesPassage(t.Context(), "corr", citation); err != nil {
			t.Fatal(err)
		}
		if calls != 6 {
			t.Fatalf("requests: %d", calls)
		}
		if _, err := client.GetKnowledgeNotesObjectWithLifecycle(t.Context(), "corr", "exact", "invalid"); err == nil || calls != 6 {
			t.Fatal("invalid filter reached transport")
		}
	}
}

func TestNotesPassageClientExactBindings(t *testing.T) {
	input := knowledge.NotesPassageInput{KnowledgeObjectID: ids.NewKnowledgeObjectID(), KnowledgeObjectVersionID: ids.NewKnowledgeObjectVersionID(), KnowledgeChunkID: ids.NewKnowledgeChunkID(), SourceHash: "sha256:" + strings.Repeat("a", 64)}
	calls := 0
	client := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		q := req.URL.Query()
		if req.Method != "GET" || req.URL.Path != "/v1/knowledge/notes/passages/"+input.KnowledgeChunkID || len(q) != 3 || q.Get("object_id") != input.KnowledgeObjectID || q.Get("version_id") != input.KnowledgeObjectVersionID || q.Get("source_hash") != input.SourceHash {
			t.Fatalf("binding lost: %s", req.URL)
		}
		return okEnvelope(`{"schema_version":"loom.notes.passage.v1","text":"old text","historical":true}`), nil
	})}}
	got, err := client.GetKnowledgeNotesPassage(t.Context(), "corr_passage", input)
	if err != nil || got.Data.Text != "old text" || !got.Data.Historical {
		t.Fatalf("%#v %v", got, err)
	}
	input.SourceHash = ""
	if _, err := client.GetKnowledgeNotesPassage(t.Context(), "corr_bad", input); err == nil || calls != 1 {
		t.Fatal("invalid binding reached transport")
	}
}

func TestBoxSourceClientCategoryFilters(t *testing.T) {
	requests := 0
	client := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.Query().Get("source_category") != "library" {
			t.Fatalf("category lost: %s", req.URL)
		}
		data := "[]"
		if strings.HasSuffix(req.URL.Path, "overview") {
			data = "{}"
		}
		return okEnvelope(data), nil
	})}}
	ctx := context.Background()
	if _, err := client.ListKnowledgeNotesRoots(ctx, "corr_box", knowledge.SourceRootFilter{SourceCategory: "library"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListKnowledgeNotesObjects(ctx, "corr_box", knowledge.KnowledgeObjectFilter{SourceCategory: "library"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetKnowledgeNotesOverview(ctx, "corr_box", knowledge.NotesOverviewInput{SourceCategory: "library"}); err != nil {
		t.Fatal(err)
	}
	if requests != 3 {
		t.Fatal("missing category request")
	}
}

func TestKnowledgeNotesRootsClientPaths(t *testing.T) {
	t.Parallel()

	requests := []struct {
		method string
		path   string
		body   string
		data   string
	}{
		{
			method: http.MethodGet,
			path:   "/v1/knowledge/notes/roots?include_inactive=true&node_key=main&root_kind=box_notes&status=active",
			data:   `[]`,
		},
		{
			method: http.MethodPost,
			path:   "/v1/knowledge/notes/roots/reconcile",
			body:   `"dry_run":true`,
			data:   `{"dry_run":true,"candidates":[],"source_roots":[],"applied":0}`,
		},
		{
			method: http.MethodGet,
			path:   "/v1/knowledge/notes/overview?include_inactive=true&node_key=main&project_id=project_test",
			data:   `{"generated_at":"2026-07-04T12:00:00Z","totals":{"root_count":1,"active_root_count":1,"object_count":2,"file_count":2,"directory_count":0,"size_bytes":128,"search_document_count":2},"projection":{"read_only":true},"index_health":{"search_document_count":2}}`,
		},
		{
			method: http.MethodPost,
			path:   "/v1/knowledge/notes/reprocess",
			body:   `"scope":"stale"`,
			data:   `{"scope":"stale","pipeline_key":"markdown_text_v1","queued":0,"skipped":0,"items":[]}`,
		},
		{
			method: http.MethodGet,
			path:   "/v1/knowledge/notes/embeddings/status",
			data:   `{"settings":{"enabled":false,"runtime_key":"ollama","model_key":"mxbai-embed-large","dimensions":1024},"queue":{"queued":0},"objects":{},"active_vectors":0,"generated_at":"2026-07-05T12:00:00Z"}`,
		},
		{
			method: http.MethodPost,
			path:   "/v1/knowledge/notes/embeddings/enable",
			data:   `{"settings":{"enabled":true,"runtime_key":"ollama","model_key":"mxbai-embed-large","dimensions":1024},"queued":2,"status":{"active_vectors":0}}`,
		},
		{
			method: http.MethodPost,
			path:   "/v1/knowledge/notes/embeddings/disable",
			data:   `{"settings":{"enabled":false,"runtime_key":"ollama","model_key":"mxbai-embed-large","dimensions":1024},"queued":0,"status":{"active_vectors":0}}`,
		},
		{
			method: http.MethodGet,
			path:   "/v1/knowledge/notes/projection/status",
			data:   `{"projection_root":"/tmp/loom-notes","exists":true,"manifest_path":"/tmp/loom-notes/.loom/manifest.json","read_only":true,"raw_writes_supported":false,"counts":{"entries":0},"generated_at":"2026-07-03T12:00:00Z"}`,
		},
		{
			method: http.MethodPost,
			path:   "/v1/knowledge/notes/projection/rebuild",
			body:   `"dry_run":true`,
			data:   `{"projection_root":"/tmp/loom-notes","dry_run":true,"manifest":{"schema_version":1,"projection_root":"/tmp/loom-notes","generated_at":"2026-07-03T12:00:00Z","read_only":true,"counts":{"entries":0},"entries":[]},"status":{"projection_root":"/tmp/loom-notes","exists":false,"manifest_path":"/tmp/loom-notes/.loom/manifest.json","counts":{"entries":0},"read_only":true,"raw_writes_supported":false,"generated_at":"2026-07-03T12:00:00Z"},"generated_at":"2026-07-03T12:00:00Z"}`,
		},
		{
			method: http.MethodPost,
			path:   "/v1/knowledge/notes/workers/indexer/run",
			body:   `"reason":"test run"`,
			data:   `{"worker":{"instance":{"worker_key":"main.knowledge_indexer"}},"run":{"worker_run_id":"worker_run_test"},"health":{"worker_health_id":"worker_health_test"},"checkpoints":[]}`,
		},
		{
			method: http.MethodPost,
			path:   "/v1/knowledge/notes/workers/embedder/run",
			body:   `"reason":"test embedding run"`,
			data:   `{"worker":{"instance":{"worker_key":"main.knowledge_embedder"}},"run":{"worker_run_id":"worker_run_embed"},"health":{"worker_health_id":"worker_health_test"},"checkpoints":[]}`,
		},
	}
	index := 0
	client := Client{
		BaseURL: "http://loom",
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if index >= len(requests) {
					t.Fatalf("unexpected request %s %s", req.Method, req.URL.String())
				}
				expected := requests[index]
				index++
				if req.Method != expected.method {
					t.Fatalf("request method = %q, want %q", req.Method, expected.method)
				}
				if req.URL.RequestURI() != expected.path {
					t.Fatalf("request path = %q, want %q", req.URL.RequestURI(), expected.path)
				}
				if expected.body != "" {
					payload := readKnowledgeRequestBody(t, req)
					if !strings.Contains(payload, expected.body) {
						t.Fatalf("request body = %s, want to contain %s", payload, expected.body)
					}
				}
				return okEnvelope(expected.data), nil
			}),
		},
	}

	if _, err := client.ListKnowledgeNotesRoots(context.Background(), "corr_test", knowledge.SourceRootFilter{RootKind: knowledge.RootKindBoxNotes, NodeKey: "main", Status: knowledge.SourceRootStatusActive, IncludeInactive: true}); err != nil {
		t.Fatalf("ListKnowledgeNotesRoots returned error: %v", err)
	}
	if _, err := client.ReconcileKnowledgeNotesRoots(context.Background(), "corr_test", knowledge.SourceRootReconcileInput{DryRun: true}); err != nil {
		t.Fatalf("ReconcileKnowledgeNotesRoots returned error: %v", err)
	}
	if _, err := client.GetKnowledgeNotesOverview(context.Background(), "corr_test", knowledge.NotesOverviewInput{NodeKey: "main", ProjectID: "project_test", IncludeInactive: true}); err != nil {
		t.Fatalf("GetKnowledgeNotesOverview returned error: %v", err)
	}
	if _, err := client.ReprocessKnowledgeNotes(context.Background(), "corr_test", knowledge.ReprocessInput{Scope: knowledge.ReprocessScopeStale}); err != nil {
		t.Fatalf("ReprocessKnowledgeNotes returned error: %v", err)
	}
	if _, err := client.GetKnowledgeNotesEmbeddingStatus(context.Background(), "corr_test"); err != nil {
		t.Fatalf("GetKnowledgeNotesEmbeddingStatus returned error: %v", err)
	}
	if _, err := client.EnableKnowledgeNotesEmbeddings(context.Background(), "corr_test"); err != nil {
		t.Fatalf("EnableKnowledgeNotesEmbeddings returned error: %v", err)
	}
	if _, err := client.DisableKnowledgeNotesEmbeddings(context.Background(), "corr_test"); err != nil {
		t.Fatalf("DisableKnowledgeNotesEmbeddings returned error: %v", err)
	}
	if _, err := client.GetKnowledgeNotesProjectionStatus(context.Background(), "corr_test"); err != nil {
		t.Fatalf("GetKnowledgeNotesProjectionStatus returned error: %v", err)
	}
	if _, err := client.RebuildKnowledgeNotesProjection(context.Background(), "corr_test", notesprojection.RebuildInput{DryRun: true}); err != nil {
		t.Fatalf("RebuildKnowledgeNotesProjection returned error: %v", err)
	}
	if _, err := client.RunKnowledgeNotesIndexer(context.Background(), "corr_test", workers.RunOnceInput{Reason: "test run"}); err != nil {
		t.Fatalf("RunKnowledgeNotesIndexer returned error: %v", err)
	}
	if _, err := client.RunKnowledgeNotesEmbedder(context.Background(), "corr_test", workers.RunOnceInput{Reason: "test embedding run"}); err != nil {
		t.Fatalf("RunKnowledgeNotesEmbedder returned error: %v", err)
	}
	if index != len(requests) {
		t.Fatalf("handled %d requests, want %d", index, len(requests))
	}
}

func TestKnowledgeNotesPipelineClientPaths(t *testing.T) {
	requests := []struct{ method, path string }{{http.MethodGet, "/v1/knowledge/notes/pipelines/status"}, {http.MethodGet, "/v1/knowledge/notes/pipelines?limit=25&status=waiting_heavy"}, {http.MethodGet, "/v1/knowledge/notes/pipelines/knowledge_pipeline_run_test"}, {http.MethodGet, "/v1/knowledge/notes/pipelines/failures"}, {http.MethodPost, "/v1/knowledge/notes/pipelines/knowledge_pipeline_run_test/retry"}, {http.MethodGet, "/v1/knowledge/notes/pipelines/policy"}, {http.MethodPost, "/v1/knowledge/notes/pipelines/policy"}, {http.MethodGet, "/v1/knowledge/notes/pipelines/backfill"}, {http.MethodPost, "/v1/knowledge/notes/pipelines/backfill"}, {http.MethodPost, "/v1/knowledge/notes/workers/coordinator/run"}, {http.MethodPost, "/v1/knowledge/notes/workers/heavy/run"}}
	index := 0
	client := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		expected := requests[index]
		index++
		if req.Method != expected.method || req.URL.RequestURI() != expected.path {
			t.Fatalf("request=%s %s want=%s %s", req.Method, req.URL.RequestURI(), expected.method, expected.path)
		}
		data := "{}"
		if strings.Contains(expected.path, "?limit") || strings.HasSuffix(expected.path, "/failures") {
			data = "[]"
		}
		return okEnvelope(data), nil
	})}}
	ctx := context.Background()
	_, _ = client.GetKnowledgeNotesPipelineStatus(ctx, "c")
	_, _ = client.ListKnowledgeNotesPipelines(ctx, "c", knowledge.PipelineListInput{Status: knowledge.FilePipelineStatusWaitingHeavy, Limit: 25})
	_, _ = client.GetKnowledgeNotesPipeline(ctx, "c", "knowledge_pipeline_run_test")
	_, _ = client.ListKnowledgeNotesPipelineFailures(ctx, "c")
	_, _ = client.RetryKnowledgeNotesPipeline(ctx, "c", "knowledge_pipeline_run_test", knowledge.PipelineRetryInput{})
	_, _ = client.GetKnowledgeNotesPipelinePolicy(ctx, "c")
	_, _ = client.UpdateKnowledgeNotesPipelinePolicy(ctx, "c", knowledge.PipelinePolicyUpdate{})
	_, _ = client.PlanKnowledgeNotesPipelineBackfill(ctx, "c")
	_, _ = client.ApplyKnowledgeNotesPipelineBackfill(ctx, "c", knowledge.PipelineBackfillInput{Confirm: true})
	_, _ = client.RunKnowledgeNotesCoordinator(ctx, "c", workers.RunOnceInput{})
	_, _ = client.RunKnowledgeNotesHeavy(ctx, "c", workers.RunOnceInput{})
	if index != len(requests) {
		t.Fatalf("requests=%d", index)
	}
}

func readKnowledgeRequestBody(t *testing.T, req *http.Request) string {
	t.Helper()
	payload, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	return string(payload)
}
