package notesworkspacesync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type operatorNative struct{ *fakeNative }

func (n operatorNative) Call(ctx context.Context, req map[string]any, out any) error {
	if req["method"] != "payload.put" {
		return n.fakeNative.Call(ctx, req, out)
	}
	raw, _ := json.Marshal(Evidence{Status: "published", Revision: "1-payload"})
	return json.Unmarshal(raw, out)
}
func TestOperatorConflictResolutionUsesExistingWorkerAndSourceJournal(t *testing.T) {
	s, n, source, root, _, original := conflictFixture(t)
	s.Native = operatorNative{n}
	page, err := s.ListConflicts(t.Context(), "")
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("%+v %v", page, err)
	}
	view, err := s.ShowConflict(t.Context(), "offline")
	if err != nil {
		t.Fatal(err)
	}
	if view.Device != "device draft" || view.Source != "source edit" {
		t.Fatalf("%+v", view)
	}
	input := ResolveConflictInput{ID: "offline", Review: view.Review, Choice: "merge", Text: "reviewed", Confirm: true}
	pending, err := s.ResolveConflict(t.Context(), input)
	if err != nil || pending.Status != "pending" {
		t.Fatalf("%+v %v", pending, err)
	}
	body, _ := os.ReadFile(filepath.Join(root, "a.md"))
	if string(body) != "source edit" {
		t.Fatal("enqueue wrote source")
	}
	step(t, s)
	accepted, err := s.ResolveConflict(t.Context(), input)
	if err != nil || accepted.Ack == nil || accepted.Ack.Status != "applied" {
		t.Fatalf("%+v %v", accepted, err)
	}
	body, _ = os.ReadFile(filepath.Join(root, "a.md"))
	if string(body) != "reviewed" || len(source.ops) != 2 {
		t.Fatal("wrong source or duplicate operation")
	}
	old, _ := load[Join](t.Context(), s.Store.(*memoryRecords), "operation", "offline")
	if old.Ack.IntentDigest != original.Ack.IntentDigest || old.Ack.Status != "conflict" {
		t.Fatal("rewrote history")
	}
	page, err = s.ListConflicts(t.Context(), "")
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("settled conflict still listed: %+v %v", page, err)
	}
}
func TestOperatorConflictStaleReviewAndMissingConfirmation(t *testing.T) {
	s, _, source, root, _, _ := conflictFixture(t)
	view, err := s.ShowConflict(t.Context(), "offline")
	if err != nil {
		t.Fatal(err)
	}
	input := ResolveConflictInput{ID: "offline", Review: view.Review, Choice: "device"}
	if _, err = s.ResolveConflict(t.Context(), input); err == nil {
		t.Fatal("no confirmation")
	}
	input.Confirm = true
	if err = os.WriteFile(filepath.Join(root, "a.md"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveConflict(t.Context(), input); err == nil {
		t.Fatal("stale review accepted")
	}
	page, err := s.ListConflicts(t.Context(), "")
	if err != nil || len(page.Items) != 1 || page.Items[0].Status != "waiting_for_source_binding" {
		t.Fatalf("stale binding hid conflict: %+v %v", page, err)
	}
	if len(source.ops) != 1 {
		t.Fatal("stale review staged write")
	}
}
