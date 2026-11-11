package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"loom.local/loom/internal/notesworkspace"
)

func TestFilesNativeNoteTransportAndValidation(t *testing.T) {
	handler, _ := fileServerFixture(t)
	for _, tc := range []struct {
		method, target, body string
		local                bool
		status               int
	}{
		{"GET", "/v1/files/note?root=box&path=note.md", "", false, 403},
		{"POST", "/v1/files/note", `{"root":"box"}`, false, 403},
		{"GET", "/v1/files/watch?root=box&paths=[]", "", false, 403},
		{"POST", "/v1/files/tree-move", `{"operation_id":"desktop_tree_example"}`, false, 403},
		{"GET", "/v1/files/tree-move", "", true, 405},
		{"POST", "/v1/files/tree-move", `{"operation_id":"other"}`, true, 400},
		{"POST", "/v1/files/tree-move", `{"operation_id":"desktop_tree_example","unknown":true}`, true, 400},
		{"DELETE", "/v1/files/note", "", true, 405},
		{"POST", "/v1/files/note", `{"kind":"edit|rename","operation_id":"desktop_example"}`, true, 400},
		{"POST", "/v1/files/note", `{"kind":"edit","operation_id":"other"}`, true, 400},
		{"POST", "/v1/files/note", `{"kind":"edit","operation_id":"desktop_example","unknown":true}`, true, 400},
		{"GET", "/v1/files/watch?root=box&paths=[]", "", true, 400},
	} {
		r := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
		if tc.local {
			r = r.WithContext(context.WithValue(r.Context(), localFileTransportKey{}, true))
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.target, w.Code, w.Body.String())
		}
	}
}

func TestDesktopReceiptRequiresTheCompleteOriginalRequest(t *testing.T) {
	input := desktopMutation{Root: "box", Path: "Notes/old/note.md", OperationID: "desktop_saved", FileID: "file", BaseID: "base", Generation: "generation", Kind: "rename", Target: "Notes/new.md"}
	op := notesworkspace.Operation{State: notesworkspace.Accepted,
		File:    notesworkspace.File{CollectionID: "collection", ID: "file", RelativePath: "old/note.md"},
		Request: notesworkspace.Request{OperationID: input.OperationID, FileID: input.FileID, BaseID: input.BaseID, Generation: input.Generation, Kind: input.Kind, DestinationPath: "new.md"}}
	if !desktopReceiptMatches(input, op, "/box/Notes", "/box/Notes/old/note.md", "/box/Notes/new.md") {
		t.Fatal("exact terminal receipt cannot replay without its former parent")
	}
	for _, modify := range []func(*desktopMutation){
		func(v *desktopMutation) { v.Text = "different" },
		func(v *desktopMutation) { v.BaseID = "different" },
		func(v *desktopMutation) { v.FileID = "different" },
		func(v *desktopMutation) { v.Generation = "different" },
		func(v *desktopMutation) { v.Kind = "delete" },
		func(v *desktopMutation) { v.Directory = true },
		func(v *desktopMutation) { v.Root = "storage" },
	} {
		changed := input
		modify(&changed)
		if desktopReceiptMatches(changed, op, "/box/Notes", "/box/Notes/old/note.md", "/box/Notes/new.md") {
			t.Fatal("altered receipt request matched")
		}
	}
	if desktopReceiptMatches(input, op, "/box/Other", "/box/Notes/old/note.md", "/box/Notes/new.md") ||
		desktopReceiptMatches(input, op, "/box/Notes", "/box/Notes/other.md", "/box/Notes/new.md") ||
		desktopReceiptMatches(input, op, "/box/Notes", "/box/Notes/old/note.md", "/box/Notes/different.md") {
		t.Fatal("different logical source path matched")
	}
}
