package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/response"
)

// The PostgreSQL exact-followup gate also exercises the actual search handler
// through TestKnowledgeBoxSourceSurfaceHelper; this freezes its additive wire.
func TestNotesSearchFollowupHTTPEnvelope(t *testing.T) {
	input := knowledge.NotesPassageInput{KnowledgeObjectID: ids.NewKnowledgeObjectID(), KnowledgeObjectVersionID: ids.NewKnowledgeObjectVersionID(), KnowledgeChunkID: ids.NewKnowledgeChunkID(), SourceHash: "sha256:" + strings.Repeat("a", 64), SourceLifecycle: knowledge.SourceLifecycleFilterArchived}
	for _, followup := range []*knowledge.NotesPassageInput{nil, &input} {
		data := knowledge.NotesSearchResultSet{ResultCount: 1, Results: []knowledge.NotesSearchResult{{KnowledgeObjectID: input.KnowledgeObjectID, KnowledgeObjectVersionID: input.KnowledgeObjectVersionID, KnowledgeChunkID: input.KnowledgeChunkID, PassageFollowup: followup}}}
		out := httptest.NewRecorder()
		response.WriteJSON(out, http.StatusOK, response.Success("notes-followup", data))
		var got struct {
			OK   bool                           `json:"ok"`
			Data knowledge.NotesSearchResultSet `json:"data"`
		}
		if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil || out.Code != 200 || !got.OK || got.Data.ResultCount != 1 {
			t.Fatalf("search envelope: %s %v", out.Body.String(), err)
		}
		if followup == nil {
			if strings.Contains(out.Body.String(), "passage_followup") {
				t.Fatal("old hit gained follow-up")
			}
		} else if p := got.Data.Results[0].PassageFollowup; p == nil || *p != input {
			t.Fatal("typed HTTP envelope lost tuple")
		}
		if strings.Contains(out.Body.String(), "loom notes passage") {
			t.Fatal("backend encoded human command")
		}
	}
}

func TestNotesArchiveLifecycleRequestFilters(t *testing.T) {
	for _, raw := range []string{"", "active", "archived", "all"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/knowledge/notes/roots?source_lifecycle="+raw, nil)
		want := knowledge.SourceLifecycleFilter(raw)
		if want == "" {
			want = knowledge.SourceLifecycleFilterActive
		}
		roots, err := sourceRootFilterFromRequest(req)
		if err != nil || roots.SourceLifecycle != want {
			t.Fatalf("roots: %+v %v", roots, err)
		}
		objects, err := knowledgeObjectFilterFromRequest(req)
		if err != nil || objects.SourceLifecycle != want {
			t.Fatalf("objects: %+v %v", objects, err)
		}
		overview, err := notesOverviewInputFromRequest(req)
		if err != nil || overview.SourceLifecycle != want {
			t.Fatalf("overview: %+v %v", overview, err)
		}
	}
	handler := NewServer(Services{}, slog.Default()).Handler()
	for _, suffix := range []string{"source_lifecycle=unknown", "source_lifecycle=Active", "source_lifecycle=%20all", "source_lifecycle=all&source_lifecycle=active", "source_lifecycle=%zz"} {
		for _, path := range []string{"roots", "overview", "objects", "objects/exact"} {
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/v1/knowledge/notes/"+path+"?"+suffix, nil))
			if out.Code != 400 || !strings.Contains(out.Body.String(), "knowledge.invalid_filter") {
				t.Fatalf("%s %s: %d %s", path, suffix, out.Code, out.Body.String())
			}
		}
	}
	query := url.Values{"object_id": {ids.NewKnowledgeObjectID()}, "version_id": {ids.NewKnowledgeObjectVersionID()}, "source_hash": {"sha256:" + strings.Repeat("a", 64)}}
	for _, suffix := range []string{"&source_lifecycle=wrong", "&source_lifecycle=all&source_lifecycle=archived", "&source_lifecycle=all&latest=true"} {
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/v1/knowledge/notes/passages/"+ids.NewKnowledgeChunkID()+"?"+query.Encode()+suffix, nil))
		if out.Code != 400 || !strings.Contains(out.Body.String(), "knowledge.invalid_passage") {
			t.Fatalf("passage: %d %s", out.Code, out.Body.String())
		}
	}
}

func TestKnowledgeNotesPassageRejectsIncompleteOrAmbiguousBinding(t *testing.T) {
	handler := NewServer(Services{}, slog.Default()).Handler()
	path := "/v1/knowledge/notes/passages/" + ids.NewKnowledgeChunkID()
	query := url.Values{"object_id": {ids.NewKnowledgeObjectID()}, "version_id": {ids.NewKnowledgeObjectVersionID()}, "source_hash": {"sha256:" + strings.Repeat("a", 64)}}
	for _, suffix := range []string{"", "?object_id=latest", "?" + query.Encode() + "&latest=true", "?" + query.Encode() + "&version_id=latest", "?" + query.Encode() + "&bad=%zz"} {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, path+suffix, nil))
		if res.Code != 400 || !strings.Contains(res.Body.String(), "knowledge.invalid_passage") {
			t.Fatalf("invalid binding %s: %d %s", suffix, res.Code, res.Body.String())
		}
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost, path+"?"+query.Encode(), nil))
	if res.Code != 405 {
		t.Fatal("non-read reached passage DB")
	}
}

// Invoked only by the disposable Box corpus test, never by a normal test run.
func TestKnowledgeBoxSourceSurfaceHelper(t *testing.T) {
	raw, socket := os.Getenv("LOOM_BOX_SURFACE_DB_URL"), os.Getenv("LOOM_BOX_SURFACE_SOCKET")
	if raw == "" && socket == "" {
		t.Skip("subprocess helper for the disposable Box corpus")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host != "" || !strings.HasPrefix(u.Query().Get("host"), "/tmp/") || !strings.HasPrefix(u.Path, "/box_sources_") || !strings.HasPrefix(socket, "/tmp/loom-box-api-") {
		t.Fatal("refused non-disposable surface endpoint")
	}
	db, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: NewServer(Services{DB: db, RuntimeConfig: config.Config{DataDir: filepath.Dir(socket)}}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler(), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	<-ctx.Done()
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(closeCtx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
}

func TestKnowledgeSourceCategoryValidation(t *testing.T) {
	server := NewServer(Services{}, slog.Default()).Handler()
	for _, endpoint := range []string{"roots", "objects", "overview"} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/knowledge/notes/"+endpoint+"?source_category=arbitrary", nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", endpoint, response.Code, response.Body.String())
		}
	}
}

func TestKnowledgeNotesOverviewRejectsNonGet(t *testing.T) {
	server := NewServer(Services{}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/knowledge/notes/overview", nil)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusMethodNotAllowed)
	}
}

func TestKnowledgeNotesEmbedderRunRejectsInvalidJSON(t *testing.T) {
	server := NewServer(Services{}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/knowledge/notes/workers/embedder/run", strings.NewReader(`{"unknown":true}`))
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusBadRequest)
	}
	if !strings.Contains(res.Body.String(), "request.invalid_json") {
		t.Fatalf("response body missing invalid json code: %s", res.Body.String())
	}
}

func TestKnowledgeNotesPipelinePolicyRejectsUnknownFields(t *testing.T) {
	server := NewServer(Services{}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/knowledge/notes/pipelines/policy", strings.NewReader(`{"unknown":true}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "request.invalid_json") {
		t.Fatalf("response=%d %s", res.Code, res.Body.String())
	}
}

func TestKnowledgeNotesPipelineStatusRejectsMutation(t *testing.T) {
	server := NewServer(Services{}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/knowledge/notes/pipelines/status", nil)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d", res.Code)
	}
}

func TestKnowledgeNotesPipelineBackfillRejectsUnknownFieldsAndMethods(t *testing.T) {
	server := NewServer(Services{}, slog.Default()).Handler()
	unknown := httptest.NewRequest(http.MethodPost, "/v1/knowledge/notes/pipelines/backfill", strings.NewReader(`{"unknown":true}`))
	unknownResult := httptest.NewRecorder()
	server.ServeHTTP(unknownResult, unknown)
	if unknownResult.Code != http.StatusBadRequest || !strings.Contains(unknownResult.Body.String(), "request.invalid_json") {
		t.Fatalf("unknown field response=%d %s", unknownResult.Code, unknownResult.Body.String())
	}
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/v1/knowledge/notes/pipelines/backfill", nil)
	deleteResult := httptest.NewRecorder()
	server.ServeHTTP(deleteResult, deleteRequest)
	if deleteResult.Code != http.StatusMethodNotAllowed {
		t.Fatalf("delete status=%d", deleteResult.Code)
	}
}

func TestKnowledgeNotesSearchAcceptsAbsoluteTimeFieldsAndValidatesDates(t *testing.T) {
	server := NewServer(Services{}, slog.Default()).Handler()

	accepted := httptest.NewRequest(http.MethodPost, "/v1/knowledge/notes/search", strings.NewReader(`{"query":"loom","after":"2026-01-01","before":"2026-02-01T00:00:00+01:00","sort":"newest"}`))
	acceptedResult := httptest.NewRecorder()
	server.ServeHTTP(acceptedResult, accepted)
	if strings.Contains(acceptedResult.Body.String(), "request.invalid_json") {
		t.Fatalf("absolute-time fields were rejected by request decoder: %s", acceptedResult.Body.String())
	}

	invalid := httptest.NewRequest(http.MethodPost, "/v1/knowledge/notes/search", strings.NewReader(`{"query":"loom","after":"01/02/2026"}`))
	invalidResult := httptest.NewRecorder()
	server.ServeHTTP(invalidResult, invalid)
	if invalidResult.Code != http.StatusBadRequest || !strings.Contains(invalidResult.Body.String(), "after value must use RFC3339") {
		t.Fatalf("invalid date response = %d %s", invalidResult.Code, invalidResult.Body.String())
	}

	empty := httptest.NewRequest(http.MethodPost, "/v1/knowledge/notes/search", strings.NewReader(`{"query":""}`))
	emptyResult := httptest.NewRecorder()
	server.ServeHTTP(emptyResult, empty)
	if emptyResult.Code != http.StatusBadRequest || !strings.Contains(emptyResult.Body.String(), "Pass a non-empty notes search query") || strings.Contains(emptyResult.Body.String(), "RFC3339") {
		t.Fatalf("empty-query response = %d %s", emptyResult.Code, emptyResult.Body.String())
	}
}
