package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
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
