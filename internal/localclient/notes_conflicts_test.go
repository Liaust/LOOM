package localclient

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"loom.local/loom/internal/notesworkspacesync"
)

func TestNotesConflictClientRoutes(t *testing.T) {
	calls := 0
	input := notesworkspacesync.ResolveConflictInput{ID: "conflict_1", Review: "sha256:" + strings.Repeat("a", 64), Choice: "merge", Text: "reviewed", Confirm: true}
	c := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/v1/knowledge/notes/conflicts" {
			t.Fatal(r.URL)
		}
		switch calls {
		case 1:
			if r.Method != "GET" || r.URL.Query().Get("after") != "previous" {
				t.Fatal(r.URL)
			}
			return okEnvelope(`{"items":[],"next":"next"}`), nil
		case 2:
			if r.Method != "GET" || r.URL.Query().Get("id") != input.ID {
				t.Fatal(r.URL)
			}
			return okEnvelope(`{"id":"conflict_1","source":"source","device":"draft"}`), nil
		default:
			var got notesworkspacesync.ResolveConflictInput
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got != input || r.Method != "POST" {
				t.Fatalf("%+v %v", got, err)
			}
			return okEnvelope(`{"id":"resolution_1","status":"pending"}`), nil
		}
	})}}
	page, err := c.ListNotesConflicts(t.Context(), "test", "previous")
	if err != nil || page.Data.Next != "next" {
		t.Fatalf("%+v %v", page, err)
	}
	view, err := c.ShowNotesConflict(t.Context(), "test", input.ID)
	if err != nil || view.Data.Device != "draft" {
		t.Fatalf("%+v %v", view, err)
	}
	result, err := c.ResolveNotesConflict(t.Context(), "test", input)
	if err != nil || result.Data.Status != "pending" {
		t.Fatalf("%+v %v", result, err)
	}
	input.Confirm = false
	if _, err = c.ResolveNotesConflict(t.Context(), "test", input); err == nil || calls != 3 {
		t.Fatal("invalid choice reached transport")
	}
}
