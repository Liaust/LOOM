package notesworkspacesync

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestStableReferenceRefusalWaitsForEvidenceOrSparseRepair(t *testing.T) {
	i, scope, now := inventoryFixture(t)
	entry := InventoryEntry{Path: "book.pdf", Kind: "asset", Present: true, Fingerprint: "same-bytes", Size: 1 << 30, Sequence: 7, Delivered: 2, WaitingSince: now, RetryAt: now}
	if err := i.Store.WithInventory(t.Context(), scope, func(r InventoryRecords) error { return r.Put(t.Context(), entry) }); err != nil {
		t.Fatal(err)
	}
	if err := i.Finish(t.Context(), scope, entry, "reference_base_diverged", now); err != nil {
		t.Fatal(err)
	}
	held := inventoryRow(t, i, scope, entry.Path)
	if !held.RetryAt.Equal(now.Add(layoutEvidenceRetry)) || held.Sequence != entry.Sequence || held.Delivered != entry.Delivered || held.Fingerprint != entry.Fingerprint {
		t.Fatal("stable refusal lost identity or sparse deadline", held)
	}
	page, err := i.DeliveryPage(t.Context(), scope, "asset", now.Add(time.Hour), 16)
	if err != nil || len(page.Entries) != 0 {
		t.Fatal("unchanged reference stayed runnable", page, err)
	}
	page, err = i.DeliveryPage(t.Context(), scope, "asset", now.Add(layoutEvidenceRetry), 16)
	if err != nil || len(page.Entries) != 1 {
		t.Fatal("sparse missed-event repair was lost", page, err)
	}
}

func TestReferenceEvidenceWakeRoutesExactNativeAndRecordPaths(t *testing.T) {
	i, scope, now := inventoryFixture(t)
	for _, path := range []string{"book.pdf", "other.pdf"} {
		entry := InventoryEntry{Path: path, Kind: "asset", Present: true, Fingerprint: path, Sequence: 3, Delivered: 1, Attempts: 8, Reason: "reference_base_diverged", RetryAt: now.Add(layoutEvidenceRetry), WaitingSince: now}
		if err := i.Store.WithInventory(t.Context(), scope, func(r InventoryRecords) error { return r.Put(t.Context(), entry) }); err != nil {
			t.Fatal(err)
		}
	}
	scopes := []Scope{{Collection: "native", SourceCollection: scope.Collection, Root: "Notes"}}
	selections := []RuntimeSelection{{Scope: scopes[0], LayoutFrom: "Previous"}}
	if got := referenceWakeScopes(scopes, selections, false); len(got) != 1 {
		t.Fatal("disabled layout acquired aliases", got)
	}
	scopes = referenceWakeScopes(scopes, selections, true)
	inventories := map[string]InventoryScope{"native": scope}
	before := inventoryRow(t, i, scope, "book.pdf")
	at := now.Add(time.Minute)
	if err := retryReferenceEvidence(t.Context(), i, inventories, scopes, []string{"Elsewhere/book.pdf"}, nil, at); err != nil {
		t.Fatal(err)
	}
	if got := inventoryRow(t, i, scope, "book.pdf"); got != before {
		t.Fatal("unrelated native path woke the reference", got)
	}
	if err := retryReferenceEvidence(t.Context(), i, inventories, scopes, []string{"Previous/book.pdf", "Previous/book.pdf"}, nil, at); err != nil {
		t.Fatal(err)
	}
	want := before
	want.RetryAt = at
	if got := inventoryRow(t, i, scope, "book.pdf"); got != want {
		t.Fatal("evidence wake rewrote source/history identity", got, want)
	}
	if got := inventoryRow(t, i, scope, "other.pdf"); !got.RetryAt.Equal(now.Add(layoutEvidenceRetry)) {
		t.Fatal("native change woke another reference", got)
	}
	if err := retryReferenceEvidence(t.Context(), i, inventories, scopes, nil, []referenceRetryHint{{scope.Collection, "other.pdf"}}, at); err != nil {
		t.Fatal(err)
	}
	if got := inventoryRow(t, i, scope, "other.pdf"); !got.RetryAt.Equal(at) {
		t.Fatal("exact receipt change did not wake reference", got)
	}
	// Current admission prefix still limits routing; hints confer no access.
	if err := i.Store.WithInventory(t.Context(), scope, func(r InventoryRecords) error {
		entry, err := r.Get(t.Context(), "other.pdf")
		if err != nil {
			return err
		}
		entry.RetryAt = now.Add(layoutEvidenceRetry)
		return r.Put(t.Context(), entry)
	}); err != nil {
		t.Fatal(err)
	}
	scope.Prefix = "allowed"
	inventories["native"] = scope
	if err := retryReferenceEvidence(t.Context(), i, inventories, scopes, []string{"Notes/other.pdf"}, nil, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := inventoryRow(t, i, scope, "other.pdf"); !got.RetryAt.Equal(now.Add(layoutEvidenceRetry)) {
		t.Fatal("out-of-selection path woke held reference", got)
	}
}

func TestReferenceDependencyHintsExcludeUnrelatedHolds(t *testing.T) {
	scope := Scope{Workspace: "w", Collection: "c", SourceCollection: "source", Generation: "g", Root: "Notes"}
	cache := &layoutDependencyCache{}
	cache.record(layoutDependencyKey(scope, "reference_file"), []string{"record:export:receipt", layoutPathKey("source", "book.pdf")}, "held", cache.version())
	cache.record(layoutDependencyKey(scope, "mapping"), []string{"record:export:unrelated", layoutPathKey("source", "other.pdf")}, "layout", cache.version())
	cache.invalidate("record:export:unrelated", false, false)
	if got := cache.referenceHints(); len(got) != 0 {
		t.Fatal("unrelated layout hint woke reference", got)
	}
	cache.invalidate("record:export:receipt", false, false)
	want := []referenceRetryHint{{"source", "book.pdf"}}
	if got := cache.referenceHints(); !reflect.DeepEqual(got, want) {
		t.Fatal("exact reference dependency hint missing", got)
	}
	cache.retainScopes(nil)
	if got := cache.referenceHints(); len(got) != 0 {
		t.Fatal("retired scope kept retrying", got)
	}
}

func TestNativeChangesCarryReferenceWakePathsWithoutSourceWrites(t *testing.T) {
	s, native, source, _, _ := fixture(t)
	before := len(source.ops)
	native.records = append(native.records,
		Evidence{ID: "book", Path: "Notes/book.pdf", Revision: "1-book", Kind: "file", Status: "available"},
		Evidence{ID: "chunk", Path: "Notes/chunk.pdf", Revision: "1-chunk", Kind: "system"},
		Evidence{ID: "text", Path: "Notes/new.md", Revision: "1-text", Kind: "file", Status: "available"})
	progress, err := s.Step(context.Background(), 128)
	if err != nil || !reflect.DeepEqual(progress.ReferencePaths, []string{"Notes/book.pdf"}) || len(source.ops) != before {
		t.Fatal("native wake lost exact path or created source work", progress, err)
	}
}
