package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestFilesAnnotationsUnavailableIsNotUnindexed(t *testing.T) {
	handler, _ := fileServerFixture(t)
	paths, _ := json.Marshal([]string{"note.md", "book.pdf", "vanished.md"})
	target := "/v1/files/annotations?root=box&paths=" + url.QueryEscape(string(paths))
	request := httptest.NewRequest("GET", target, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("accepted untrusted annotation request")
	}
	request = request.WithContext(context.WithValue(request.Context(), localFileTransportKey{}, true))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var result struct {
		Data struct {
			State   string           `json:"state"`
			Entries []fileAnnotation `json:"entries"`
		} `json:"data"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Data.State != "unavailable" || len(result.Data.Entries) != 3 || result.Data.Entries[0].Index != "unknown" || result.Data.Entries[2].Freshness != "source_missing" {
		t.Fatalf("invalid qualification: %d %s", response.Code, response.Body.String())
	}
	bad := httptest.NewRequest("GET", "/v1/files/annotations?root=box&paths=%5B%22..%2Fsecret%22%5D", nil)
	bad = bad.WithContext(context.WithValue(bad.Context(), localFileTransportKey{}, true))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, bad)
	if response.Code != 400 {
		t.Fatalf("unsafe path accepted: %d", response.Code)
	}
}
