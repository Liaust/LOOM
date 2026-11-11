package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"loom.local/loom/internal/notesworkspace"
	"loom.local/loom/internal/response"
)

func TestMultiBoxUploadSavedIntentScopesEveryAction(t *testing.T) {
	s, _, _ := boxMutationFixture(t)
	db := installBoxReceiptStore(t, &s)
	intent := notesworkspace.UploadIntent{ID: "desktop_upload_saved_company", Root: companyFileRef, Path: "Notes/old/new.bin", Hash: "sha256:" + strings.Repeat("a", 64), Size: 1, Collision: "skip"}
	db.uploads[intent.ID] = notesworkspace.Upload{Intent: intent, Path: intent.Path, State: "skipped"}
	for _, defaultBox := range []string{"personal", "company"} {
		w := mutationRequest(t, s.Handler(), "GET", "/v1/files/uploads?action=status&id="+intent.ID, nil, defaultBox)
		var envelope response.Envelope[notesworkspace.Upload]
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || w.Code != 200 || envelope.Data.Intent != intent {
			t.Fatalf("saved upload followed mutable default %q: %d %s %v", defaultBox, w.Code, w.Body.String(), err)
		}
	}
	for _, action := range []struct{ method, name string }{{"GET", "status"}, {"PUT", "chunk"}, {"POST", "complete"}, {"POST", "cancel"}} {
		for _, query := range []string{"&box=personal", "&root=box"} {
			w := mutationRequest(t, s.Handler(), action.method, "/v1/files/uploads?action="+action.name+"&id="+intent.ID+query, nil, "")
			if w.Code != 409 || strings.Contains(w.Body.String(), intent.Path) {
				t.Fatalf("%s served or changed foreign upload: %d %s", action.name, w.Code, w.Body.String())
			}
		}
	}
	if w := mutationRequest(t, s.Handler(), "POST", "/v1/files/uploads?action=begin", intent, "personal"); w.Code != 200 {
		t.Fatalf("immutable begin replay: %d %s", w.Code, w.Body.String())
	}
	changed := intent
	changed.BoxID = "box_company"
	if w := mutationRequest(t, s.Handler(), "POST", "/v1/files/uploads?action=begin", changed, ""); w.Code != 409 {
		t.Fatal("begin silently rewrote optional saved BoxID", w.Code)
	}
	changed = intent
	changed.Root = "box"
	if w := mutationRequest(t, s.Handler(), "POST", "/v1/files/uploads?action=begin", changed, ""); w.Code != 409 {
		t.Fatal("Company upload replayed as Personal", w.Code)
	}
	changed = intent
	changed.BoxID = "box_personal"
	if w := mutationRequest(t, s.Handler(), "POST", "/v1/files/uploads?action=begin", changed, ""); w.Code != 400 {
		t.Fatal("mismatched upload BoxID accepted", w.Code)
	}
	s.services.Boxes.Boxes[1].Available = false
	for _, action := range []struct{ method, name string }{{"GET", "status"}, {"PUT", "chunk"}, {"POST", "complete"}, {"POST", "cancel"}} {
		w := mutationRequest(t, s.Handler(), action.method, "/v1/files/uploads?action="+action.name+"&id="+intent.ID, nil, "personal")
		if w.Code != 409 {
			t.Fatalf("%s admitted unavailable root: %d %s", action.name, w.Code, w.Body.String())
		}
	}
	if db.writes != 0 {
		t.Fatal("scope rejection reached upload lock/write", db.writes)
	}
}

func TestMultiBoxUploadOptionalIdentityRemainsImmutable(t *testing.T) {
	s, _, _ := boxMutationFixture(t)
	db := installBoxReceiptStore(t, &s)
	for _, intent := range []notesworkspace.UploadIntent{
		{ID: "desktop_upload_personal", Root: "box", Path: "Notes/old/file.bin", Hash: "sha256:" + strings.Repeat("a", 64), Collision: "skip"},
		{ID: "desktop_upload_company", Root: companyFileRef, BoxID: "box_company", Path: "Notes/old/file.bin", Hash: "sha256:" + strings.Repeat("a", 64), Collision: "skip"},
	} {
		db.uploads[intent.ID] = notesworkspace.Upload{Intent: intent, Path: intent.Path, State: "accepted"}
		w := mutationRequest(t, s.Handler(), "POST", "/v1/files/uploads?action=begin", intent, "company")
		var envelope response.Envelope[notesworkspace.Upload]
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || w.Code != 200 || envelope.Data.Intent != intent {
			t.Fatalf("optional identity changed: %d %s %v", w.Code, w.Body.String(), err)
		}
	}
	if db.writes != 0 {
		t.Fatal("begin replay wrote metadata", db.writes)
	}
}
