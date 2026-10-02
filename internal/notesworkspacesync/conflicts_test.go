package notesworkspacesync

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func conflictFixture(t *testing.T) (*Service, *fakeNative, *sourceStore, string, Binding, Join) {
	t.Helper()
	s, n, source, root, b := fixture(t)
	i, p := operation(b, "offline", "device draft", "2-offline")
	n.content(b.Path, "2-offline", b.NativeRevision, "device draft")
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("source edit"), 0600); err != nil {
		t.Fatal(err)
	}
	n.add(i)
	n.add(p)
	step(t, s)
	original, err := load[Join](t.Context(), s.Store.(*memoryRecords), "operation", i.ID)
	if err != nil || original.Ack == nil || original.Ack.Status != "conflict" {
		t.Fatalf("missing conflict: %+v %v", original, err)
	}
	current := exportConflictCurrent(t, s, b)
	return s, n, source, root, current, original
}

func exportConflictCurrent(t *testing.T, s *Service, b Binding) Binding {
	t.Helper()
	r, err := s.Source.Read(t.Context(), "source", "a.md")
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Export(t.Context(), b.Collection, r.File.ID, r.Base.ID)
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func addResolution(n *fakeNative, b Binding, id, text string, ids ...string) {
	i, p := operation(b, id, text, "3-"+id)
	i.Resolves = ids
	raw, _ := json.Marshal(i)
	p.IntentDigest = digest(raw)
	n.content(b.Path, p.Revisions[0].Revision, b.NativeRevision, text)
	n.add(i)
	n.add(p)
}

func TestConflictResolutionPreservesHistoryAndReplays(t *testing.T) {
	for _, choice := range []string{"device draft", "source edit", "explicit combined result"} {
		t.Run(choice, func(t *testing.T) {
			s, n, source, root, current, original := conflictFixture(t)
			addResolution(n, current, "resolution", choice, "offline")
			step(t, s)
			r := s.Store.(*memoryRecords)
			result, _ := load[Join](t.Context(), r, "operation", "resolution")
			old, _ := load[Join](t.Context(), r, "operation", "offline")
			if result.Ack == nil || result.Ack.Status != "applied" || !reflect.DeepEqual(result.Ack.Resolves, []string{"offline"}) || old.ResolutionID != "resolution" {
				t.Fatalf("missing resolution receipt: %+v %+v", result, old)
			}
			old.ResolutionID = ""
			if !reflect.DeepEqual(old, original) {
				t.Fatal("original conflict or receipt was rewritten")
			}
			body, _ := os.ReadFile(filepath.Join(root, "a.md"))
			if string(body) != choice || len(source.ops) != 2 {
				t.Fatalf("source choice not applied: %s, %d operations", body, len(source.ops))
			}
			before := len(n.acks)
			restarted := *s
			step(t, &restarted)
			if len(n.acks) != before || len(source.ops) != 2 {
				t.Fatal("replay duplicated the resolution")
			}
		})
	}
}

func TestConflictResolutionStaleChoiceRequiresFreshReview(t *testing.T) {
	s, n, _, root, current, _ := conflictFixture(t)
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("newer source"), 0600); err != nil {
		t.Fatal(err)
	}
	addResolution(n, current, "stale", "device draft", "offline")
	step(t, s)
	r := s.Store.(*memoryRecords)
	stale, _ := load[Join](t.Context(), r, "operation", "stale")
	if stale.Ack == nil || stale.Ack.Status != "conflict" || len(stale.Ack.Resolves) != 0 {
		t.Fatalf("stale choice claimed completion: %+v", stale)
	}
	body, _ := os.ReadFile(filepath.Join(root, "a.md"))
	if string(body) != "newer source" {
		t.Fatal("stale choice overwrote a newer source")
	}
	current = exportConflictCurrent(t, s, current)
	addResolution(n, current, "reviewed", "reviewed merge", "offline", "stale")
	step(t, s)
	accepted, _ := load[Join](t.Context(), r, "operation", "reviewed")
	if accepted.Ack == nil || accepted.Ack.Status != "applied" || len(accepted.Ack.Resolves) != 2 {
		t.Fatalf("fresh review failed: %+v", accepted)
	}
	addResolution(n, current, "second", "unreviewed second writer", "offline")
	step(t, s)
	second, _ := load[Join](t.Context(), r, "operation", "second")
	if second.Reason != "conflict_already_resolved" {
		t.Fatalf("second writer was not rejected: %+v", second)
	}
}

type interruptedResolutionSource struct {
	Source
	interrupted bool
}

func (s *interruptedResolutionSource) Save(ctx context.Context, req SourceRequest) (Outcome, error) {
	result, err := s.Source.Save(ctx, req)
	if err == nil && !s.interrupted {
		s.interrupted = true
		return Outcome{}, errors.New("lost response after source commit")
	}
	return result, err
}

func TestConflictResolutionClaimSurvivesCommitReceiptGap(t *testing.T) {
	s, n, source, root, current, _ := conflictFixture(t)
	s.Source = &interruptedResolutionSource{Source: s.Source}
	addResolution(n, current, "afirst", "first choice", "offline")
	addResolution(n, current, "bother", "other choice", "offline")
	step(t, s)
	r := s.Store.(*memoryRecords)
	other, _ := load[Join](t.Context(), r, "operation", "bother")
	if other.Reason != "resolution_in_progress" {
		t.Fatalf("unfinished claim was bypassed: %+v", other)
	}
	step(t, s)
	first, _ := load[Join](t.Context(), r, "operation", "afirst")
	other, _ = load[Join](t.Context(), r, "operation", "bother")
	body, _ := os.ReadFile(filepath.Join(root, "a.md"))
	if first.Ack == nil || first.Ack.Status != "applied" || other.Reason != "conflict_already_resolved" || string(body) != "first choice" || len(source.ops) != 2 {
		t.Fatalf("claim recovery failed: first=%+v other=%+v bytes=%s", first, other, body)
	}
}

func TestConflictResolutionRejectsUnrelatedAndInvalidEvidence(t *testing.T) {
	for _, change := range []string{"missing", "file", "path", "scope", "accepted", "digest", "collision"} {
		t.Run(change, func(t *testing.T) {
			s, n, source, root, current, original := conflictFixture(t)
			r := s.Store.(*memoryRecords)
			id := "offline"
			var old Intent
			_ = json.Unmarshal([]byte(original.IntentRaw), &old)
			switch change {
			case "missing":
				id = "missing"
			case "file":
				original.SourceFileID = "different"
			case "path":
				old.Path = "Notes/other.md"
			case "scope":
				old.Collection = "different"
			case "accepted":
				original.Ack.Status = "applied"
			case "digest":
				original.Ack.IntentDigest = digest([]byte("different"))
			case "collision":
				original.Reason = "control_collision"
			}
			if change == "path" || change == "scope" {
				raw, _ := json.Marshal(old)
				original.IntentRaw = string(raw)
			}
			if err := r.Put(t.Context(), "operation", "offline", original); err != nil {
				t.Fatal(err)
			}
			addResolution(n, current, "invalid", "must not apply", id)
			step(t, s)
			result, _ := load[Join](t.Context(), r, "operation", "invalid")
			body, _ := os.ReadFile(filepath.Join(root, "a.md"))
			if result.Status != "held" || len(source.ops) != 1 || string(body) != "source edit" {
				t.Fatalf("invalid evidence mutated source: %+v", result)
			}
		})
	}
}

func TestResolutionProtocolBoundsAndOldEncoding(t *testing.T) {
	_, _, _, _, b := fixture(t)
	i, _ := operation(b, "choice", "text", "2-choice")
	raw, _ := json.Marshal(i)
	var old map[string]any
	_ = json.Unmarshal(raw, &old)
	if _, ok := old["resolves"]; ok {
		t.Fatal("legacy encoding changed")
	}
	for _, value := range []any{nil, []string{}, []string{"choice"}, []string{"x", "x"}, []string{"bad id"}, "x", make([]string, 33)} {
		v := clone(old)
		v["resolves"] = value
		raw, _ := json.Marshal(v)
		if _, err := parseIntent(raw); err == nil {
			t.Fatalf("accepted invalid refs: %#v", value)
		}
	}
	i.Resolves = []string{"offline"}
	raw, _ = json.Marshal(i)
	if _, err := parseIntent(raw); err != nil {
		t.Fatal(err)
	}
	i.Predecessor = "parent"
	raw, _ = json.Marshal(i)
	if _, err := parseIntent(raw); err == nil {
		t.Fatal("resolution silently rebased via predecessor")
	}
}
