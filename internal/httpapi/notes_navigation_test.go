package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/knowledge"
)

func TestNotesNavigationRejectsAmbiguousCitation(t *testing.T) {
	handler := NewServer(Services{}, slog.Default()).Handler()
	path := "/v1/knowledge/notes/passages/" + ids.NewKnowledgeChunkID() + "/workspace"
	query := url.Values{"object_id": {ids.NewKnowledgeObjectID()}, "version_id": {ids.NewKnowledgeObjectVersionID()}, "source_hash": {"sha256:" + strings.Repeat("a", 64)}}
	for _, suffix := range []string{"", "?object_id=latest", "?" + query.Encode() + "&path=Personal/note.md", "?" + query.Encode() + "&version_id=latest", "?" + query.Encode() + "&source_lifecycle=all&source_lifecycle=active"} {
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, httptest.NewRequest(http.MethodGet, path+suffix, nil))
		if out.Code != 400 {
			t.Fatalf("%s: %d %s", suffix, out.Code, out.Body.String())
		}
	}
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, httptest.NewRequest(http.MethodPost, path+"?"+query.Encode(), nil))
	if out.Code != 405 {
		t.Fatal(out.Code)
	}
}

func TestNotesNavigationBoundBoxSelectorsPreserveExactArchivedCitation(t *testing.T) {
	path := "/v1/knowledge/notes/passages/" + ids.NewKnowledgeChunkID() + "/workspace"
	query := url.Values{"object_id": {ids.NewKnowledgeObjectID()}, "version_id": {ids.NewKnowledgeObjectVersionID()}, "source_hash": {"sha256:" + strings.Repeat("a", 64)}, "source_lifecycle": {"archived"}}
	ctx := boxregistry.WithSelection(context.Background(), boxregistry.ResolvedSelection{BoxIDs: []string{"box_company"}})
	for _, selector := range []string{"&box_id=box_company", "&box=company", "&all_boxes=true"} {
		r := httptest.NewRequest(http.MethodGet, path+"?"+query.Encode()+selector, nil)
		input, err := notesNavigationInput(ctx, r)
		if err != nil || input.KnowledgeObjectID != query.Get("object_id") || input.KnowledgeObjectVersionID != query.Get("version_id") || input.SourceLifecycle != knowledge.SourceLifecycleFilterArchived {
			t.Fatal(input, err)
		}
		if _, err := notesNavigationInput(context.Background(), r); err == nil {
			t.Fatal("unbound selector was silently discarded")
		}
	}
	for _, extra := range []string{"&path=Personal/note.md", "&version_id=latest", "&source_lifecycle=active"} {
		if _, err := notesNavigationInput(ctx, httptest.NewRequest(http.MethodGet, path+"?"+query.Encode()+"&box_id=box_company"+extra, nil)); err == nil {
			t.Fatal("citation was broadened", extra)
		}
	}
}
