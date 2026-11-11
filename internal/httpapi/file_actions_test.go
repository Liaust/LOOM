package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/notesworkspace"
)

func TestFileActionsTransportAndStrictPayload(t *testing.T) {
	handler, _ := fileServerFixture(t)
	valid := `{"root":"box","path":"asset.bin","operation_id":"desktop_action_example","file_id":"file","base_id":"base","generation":"generation","kind":"rename","target":"moved.bin"}`
	for _, test := range []struct {
		method, url, body string
		local             bool
		status            int
	}{
		{"GET", "/v1/files/actions?root=box&path=asset.bin", "", false, 403},
		{"POST", "/v1/files/actions", valid, false, 403},
		{"PUT", "/v1/files/actions", "", true, 405},
		{"GET", "/v1/files/actions?root=storage&path=asset.bin", "", true, 403},
		{"POST", "/v1/files/actions", `{}`, true, 400},
		{"POST", "/v1/files/actions", strings.Replace(valid, `"rename"`, `"edit"`, 1), true, 400},
		{"POST", "/v1/files/actions", strings.Replace(valid, `"box"`, `"storage"`, 1), true, 400},
		{"POST", "/v1/files/actions", strings.Replace(valid, `"desktop_action_example"`, `"desktop_note"`, 1), true, 400},
		{"POST", "/v1/files/actions", strings.Replace(valid, `"target":"moved.bin"`, `"text":""`, 1), true, 400},
		{"POST", "/v1/files/actions", strings.Replace(valid, `"target":"moved.bin"`, `"content":"payload"`, 1), true, 400},
		{"POST", "/v1/files/actions", strings.Replace(valid, `"rename"`, `"delete"`, 1), true, 400},
		{"POST", "/v1/files/actions", valid + `{}`, true, 400},
		{"POST", "/v1/files/actions", `{"root":"` + strings.Repeat("x", 65536) + `"}`, true, 400},
		// A valid shape reaches source admission, not text or reference reading.
		{"POST", "/v1/files/actions", valid, true, 409},
	} {
		request := httptest.NewRequest(test.method, test.url, strings.NewReader(test.body))
		request.Header.Set("X-Loom-Local", "true")
		if test.local {
			request = request.WithContext(context.WithValue(request.Context(), localFileTransportKey{}, true))
		}
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, request)
		if result.Code != test.status {
			t.Fatalf("%s %s (%s): %d %s", test.method, test.url, test.body[:min(len(test.body), 200)], result.Code, result.Body.String())
		}
	}
}

func TestFileActionReceiptExactOriginalRequest(t *testing.T) {
	input := fileActionInput{Root: "box", Path: "old/asset.bin", OperationID: "desktop_action_saved", FileID: "file", BaseID: "base", Generation: "generation", Kind: "rename", Target: "new/asset.bin"}
	op := notesworkspace.Operation{State: notesworkspace.Accepted, File: notesworkspace.File{ID: "file", RelativePath: "old/asset.bin"}, Base: notesworkspace.Base{MetadataAction: &notesworkspace.MetadataActionIdentity{}}, Request: notesworkspace.Request{OperationID: input.OperationID, FileID: input.FileID, BaseID: input.BaseID, Generation: input.Generation, Kind: input.Kind, DestinationPath: input.Target}}
	matches := func(value fileActionInput) bool {
		target := ""
		if value.Target != "" {
			target = filepath.Join("/box", value.Target)
		}
		return fileActionReceiptMatches(value, op, "/box", filepath.Join("/box", value.Path), target)
	}
	if !matches(input) {
		t.Fatal("exact receipt needs a missing original parent")
	}
	for _, change := range []func(*fileActionInput){func(v *fileActionInput) { v.Root = "storage" }, func(v *fileActionInput) { v.Path = "other.bin" }, func(v *fileActionInput) { v.Target = "other.bin" }, func(v *fileActionInput) { v.OperationID = "other" }, func(v *fileActionInput) { v.FileID = "other" }, func(v *fileActionInput) { v.BaseID = "other" }, func(v *fileActionInput) { v.Generation = "other" }, func(v *fileActionInput) { v.Kind = "delete" }} {
		changed := input
		change(&changed)
		if matches(changed) {
			t.Fatal("altered request matched", changed)
		}
	}
	op.Base.MetadataAction = nil
	if matches(input) {
		t.Fatal("a text receipt became a metadata action")
	}
}

func TestFileActionCompactReceiptAndSnapshot(t *testing.T) {
	file := notesworkspace.File{ID: "file", RelativePath: "moved.bin", PathVersion: 1}
	op := notesworkspace.Operation{Request: notesworkspace.Request{OperationID: "desktop_action_saved", Generation: "generation"}, State: notesworkspace.Accepted, ResultFile: &file, ResultBaseID: "metadata-base"}
	result := metadataActionReceipt(op)
	if result.File.ID != "file" || result.Path != "moved.bin" || result.BaseID != "metadata-base" || result.Generation != "generation" {
		t.Fatal("accepted result metadata missing", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"text", "content", "base", "observed_content"} {
		if _, exists := fields[forbidden]; exists {
			t.Fatal("receipt includes payload", forbidden)
		}
	}
	op.State = notesworkspace.Held
	if held := metadataActionReceipt(op); held.File != nil || held.Path != "" || held.BaseID != "" {
		t.Fatal("held operation claimed resulting file placement", held)
	}
	file.Deleted = true
	op.State = notesworkspace.Accepted
	if deleted := metadataActionReceipt(op); deleted.File == nil || !deleted.File.Deleted || deleted.Path != "moved.bin" {
		t.Fatal("delete omitted retired file/path", deleted)
	}
	snapshot := fileActionSnapshot{File: file, Path: file.RelativePath, BaseID: "metadata-base", Generation: "generation", Revision: (notesworkspace.MetadataActionIdentity{Device: 1, Inode: 2}).Revision()}
	if !strings.HasPrefix(snapshot.Revision, "stat:") {
		t.Fatal("metadata revision mislabeled as a content hash")
	}
}
