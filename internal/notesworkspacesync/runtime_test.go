package notesworkspacesync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/notesworkspace"
)

func runtimeFixture() RuntimeConfig {
	return RuntimeConfig{SchemaVersion: "notes_workspace.runtime.v1", Command: "/package/bin/notes-sync", Replica: "pilot", ReplicaDir: "/state/replica", Settings: "/private/settings.json",
		Scopes: []RuntimeSelection{{Scope: Scope{Workspace: "pilot", Collection: "personal", SourceCollection: "source-1", Generation: "enrollment-1", Root: "Personal"}, AdmissionPath: "welcome.md"}}}
}

func TestRuntimeConfiguration(t *testing.T) {
	if err := runtimeFixture().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*RuntimeConfig){
		"unbounded watch":      func(c *RuntimeConfig) { c.WatchSeconds = 61 },
		"negative watch":       func(c *RuntimeConfig) { c.WatchSeconds = -1 },
		"relative command":     func(c *RuntimeConfig) { c.Command = "notes-sync" },
		"missing admission":    func(c *RuntimeConfig) { c.Scopes[0].AdmissionPath = "" },
		"private admission":    func(c *RuntimeConfig) { c.Scopes[0].AdmissionPath = ".private/secret.md" },
		"reserved admission":   func(c *RuntimeConfig) { c.Scopes[0].AdmissionPath = "LOOM-Control-v1/test.md" },
		"escaping prefix":      func(c *RuntimeConfig) { c.Scopes[0].PathPrefix = "../other" },
		"invalid predecessor":  func(c *RuntimeConfig) { c.Scopes[0].PreviousGeneration = "bad generation" },
		"unselected admission": func(c *RuntimeConfig) { c.Scopes[0].PathPrefix = "Selected" },
		"overlapping roots": func(c *RuntimeConfig) {
			s := c.Scopes[0]
			s.SourceCollection = "source-2"
			s.Collection = "project"
			s.Root += "/Nested"
			c.Scopes = append(c.Scopes, s)
		},
		"duplicate source": func(c *RuntimeConfig) {
			s := c.Scopes[0]
			s.Collection = "project"
			s.Root = "Project"
			c.Scopes = append(c.Scopes, s)
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := runtimeFixture()
			change(&c)
			if c.Validate() == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "runtime.json")
	raw, _ := json.Marshal(runtimeFixture())
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeConfig(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"unknown":"secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeConfig(path); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe configuration error: %v", err)
	}
}

func TestRuntimeCollectionsAreNotPilotLimited(t *testing.T) {
	for _, count := range []int{8, 10, 16, 17, 64} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			cfg := runtimeFixture()
			cfg.Scopes = nil
			for i := 0; i < count; i++ {
				cfg.Scopes = append(cfg.Scopes, RuntimeSelection{Scope: Scope{
					Workspace: "pilot", Collection: fmt.Sprintf("collection-%d", i),
					SourceCollection: fmt.Sprintf("source-%d", i), Generation: "enrollment-1",
					Root: fmt.Sprintf("Projects/Project-%d/Notes", i),
				}, AdmissionPath: "welcome.md"})
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("%d collections: %v", count, err)
			}
		})
	}
}

func TestRuntimeConfigurationSizeBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.json")
	raw, _ := json.Marshal(runtimeFixture())
	raw = append(raw, []byte(strings.Repeat(" ", 65536-len(raw)))...)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeConfig(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeConfig(path); err == nil {
		t.Fatal("oversized configuration accepted")
	}
}

type selectedResolverTest struct{ called bool }

func TestRetainedEnrollmentObservationIsNotWriteAuthority(t *testing.T) {
	selection := runtimeFixture().Scopes[0]
	selection.PreviousGeneration = "previous"
	selection.PathPrefix = "Selected"
	s := selectedSources{PathResolver: generationResolver{generation: "current"}, Selections: []RuntimeSelection{selection}}
	for _, tc := range []struct {
		path, generation string
		allowed          bool
	}{
		{"Selected/a.md", "previous", true},
		{"Selected/a.md", "current", true},
		{"Selected/a.md", "unapproved", false},
		{"Other/a.md", "previous", false},
	} {
		called := false
		err := s.WithRetainedSource(t.Context(), "source-1", tc.path, tc.generation, func(src notesworkspace.Source) error {
			called = true
			if src.Generation != tc.generation {
				t.Fatal("wrong observation generation")
			}
			return nil
		})
		if called != tc.allowed || (err == nil) != tc.allowed {
			t.Fatalf("observation %+v: %v %v", tc, called, err)
		}
	}
	if err := s.WithSource(t.Context(), "source-1", "Selected/a.md", func(src notesworkspace.Source) error {
		if src.Generation != "current" {
			t.Fatal("old generation became write authority")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func (r *selectedResolverTest) WithPaths(ctx context.Context, id string, paths []string, fn func(notesworkspace.Source) error) error {
	r.called = true
	return fn(notesworkspace.Source{CollectionID: id})
}

func TestRuntimeFolderSelection(t *testing.T) {
	cfg := runtimeFixture()
	cfg.Scopes[0].PathPrefix = "Selected"
	cfg.Scopes[0].AdmissionPath = "Selected/welcome.md"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	resolver := &selectedResolverTest{}
	s := selectedSources{PathResolver: resolver, Selections: cfg.Scopes}
	for _, paths := range [][]string{{"other.md"}, {"Selected2/note.md"}, {"Selected/a.md", "Other/a.md"}} {
		resolver.called = false
		if err := s.WithPaths(t.Context(), "source-1", paths, func(notesworkspace.Source) error { return nil }); !errors.Is(err, notesworkspace.ErrMembership) || resolver.called {
			t.Fatalf("outside selection reached owner: %v %v", paths, err)
		}
	}
	if err := s.WithSource(t.Context(), "source-1", "Selected/a.md", func(notesworkspace.Source) error { return nil }); err != nil || !resolver.called {
		t.Fatalf("selected source refused: %v", err)
	}
	root := t.TempDir()
	for _, p := range []string{"Selected/a.md", "Selected/nested/b.md", "Selected2/c.md", "other.md"} {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("note"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	got, more, err := sourcePageWithin(t.Context(), root, "Selected", "", 32)
	if err != nil || more || !reflect.DeepEqual(got, []string{"Selected/", "Selected/a.md", "Selected/nested/", "Selected/nested/b.md"}) {
		t.Fatalf("selected enumeration: %v %v %v", got, more, err)
	}
	if err := os.Symlink(filepath.Join(root, "Selected"), filepath.Join(root, "Link")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sourcePageWithin(t.Context(), root, "Link/nested", "", 32); err == nil {
		t.Fatal("followed prefix symlink")
	}
}

func TestRuntimeSourcePagination(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"a/note.md", "a.md", "z.txt", ".private/secret.md", "credentials/secret.md", "private_no_index/secret.md", "LOOM-Control-v1/control.md", "image.png"} {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("example"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "z.txt"), filepath.Join(root, "link.md")); err != nil {
		t.Fatal(err)
	}
	var all []string
	after := ""
	for {
		page, more, err := sourcePage(t.Context(), root, after, 1)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page...)
		if !more {
			break
		}
		after = page[len(page)-1]
	}
	if !reflect.DeepEqual(all, []string{"a.md", "a/", "a/note.md", "image.png", "z.txt"}) {
		t.Fatalf("pages: %v", all)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := sourcePage(ctx, root, "", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if _, _, err := sourcePage(t.Context(), root, "", 0); err == nil {
		t.Fatal("zero page accepted")
	}
}

func TestRuntimeInventoryPageAcceptsWorkerBatch(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 129; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("note-%03d.md", i)), []byte("note"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	page, more, err := inventorySourcePage(t.Context(), root, "", "", 128)
	if err != nil || len(page) != 128 || !more {
		t.Fatalf("worker page refused: %d %t %v", len(page), more, err)
	}
	last, more, err := inventorySourcePage(t.Context(), root, "", page[len(page)-1], 128)
	if err != nil || len(last) != 1 || more {
		t.Fatalf("worker continuation refused: %v %t %v", last, more, err)
	}
	if _, _, err := inventorySourcePage(t.Context(), root, "", "", 129); err == nil {
		t.Fatal("unbounded page admitted")
	}
}

func TestRuntimeSummaryDoesNotExposeCursor(t *testing.T) {
	raw, err := json.Marshal(RuntimeResult{Cursor: RuntimeCursor{Collection: "private-collection", After: "private-note.md", RetainedAfter: "private-operation"}})
	if err != nil || strings.Contains(string(raw), "private") {
		t.Fatalf("cursor exposed: %s, %v", raw, err)
	}
}

func TestRuntimeSourcePagePrunesPastAndFutureBranches(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{"past/deep/old.md", "range.md", "range/a.md", "range/b.md", "range/sub/c.md", "z/deep/later.md"} {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("note"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var visited []string
	read := func(path string) ([]os.DirEntry, error) {
		relative, _ := filepath.Rel(root, path)
		visited = append(visited, filepath.ToSlash(relative))
		return os.ReadDir(path)
	}
	got, more, err := sourcePageWithReader(t.Context(), root, "", "past/deep/old.md", 2, read)
	if err != nil || !more || !reflect.DeepEqual(got, []string{"range.md", "range/"}) || !reflect.DeepEqual(visited, []string{".", "past", "past/deep", "range"}) {
		t.Fatalf("bounded initial page: paths=%v more=%v visited=%v err=%v", got, more, visited, err)
	}
	visited = nil
	got, more, err = sourcePageWithReader(t.Context(), root, "", "range/a.md", 1, read)
	if err != nil || !more || !reflect.DeepEqual(got, []string{"range/b.md"}) || !reflect.DeepEqual(visited, []string{".", "range"}) {
		t.Fatalf("bounded resumed page: paths=%v more=%v visited=%v err=%v", got, more, visited, err)
	}
	linked := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(root, linked); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sourcePage(t.Context(), linked, "", 2); !errors.Is(err, ErrHeld) {
		t.Fatalf("root symlink accepted: %v", err)
	}
}

func TestInventoryDeliveryTextAssetsAndRestart(t *testing.T) {
	i, scope, now := inventoryFixture(t)
	for n := 0; n < 140; n++ {
		inventoryWrite(t, scope, fmt.Sprintf("text-%02d.md", n), "text")
	}
	for n := 0; n < 20; n++ {
		inventoryWrite(t, scope, fmt.Sprintf("asset-%02d.png", n), "asset")
	}
	inventorySeed(t, i, scope, now, 100)
	var order []string
	publish := func(e InventoryEntry) (deliveryPublication, error) {
		order = append(order, e.Path)
		return deliveryPublication{Bytes: e.Size}, nil
	}
	first, err := deliverInventoryTurn(t.Context(), i, scope, DeliveryCursor{Generation: scope.Generation}, publish, func() time.Time { return now })
	if err != nil || first.TextExported != 128 || first.AssetExported != 16 || first.Cursor.TextSequence == 0 || first.Cursor.AssetSequence == 0 {
		t.Fatalf("turn %+v %v", first, err)
	}
	if order[0] != "text-00.md" || order[1] != "asset-00.png" {
		t.Fatal("asset not reserved", order[:2])
	}
	// Recompose from persisted inventory and summary cursor; acknowledged rows do
	// not replay even if the previous worker's checkpoint were entirely lost.
	i = SourceInventory{Store: i.Store}
	second, err := deliverInventoryTurn(t.Context(), i, scope, DeliveryCursor{}, publish, func() time.Time { return now })
	if err != nil || second.TextExported != 12 || second.AssetExported != 4 {
		t.Fatalf("restart %+v %v", second, err)
	}
	progress, err := i.Progress(t.Context(), scope, now)
	if err != nil || progress.Counts.TextPending != 0 || progress.Counts.AssetPending != 0 || progress.Counts.TextPresent != 140 || progress.Counts.AssetPresent != 20 {
		t.Fatal(progress, err)
	}
}

func TestInventoryDeliverySoftBudgetDedicatedAndWindow(t *testing.T) {
	i, scope, now := inventoryFixture(t)
	// Metadata-only sizes keep this focused fixture tiny; the transport callback
	// proves admission behavior without generating a large corpus or attachment.
	i.Observe = func(_ string, path string) (InventoryEntry, error) {
		e := InventoryEntry{Path: path, Kind: "asset", Present: true, Fingerprint: path, Size: deliverySoftBytes + 1}
		if textPath(path) {
			e.Kind = "text"
			e.Size = 10
		}
		return e, nil
	}
	for _, path := range []string{"text.md", "huge.png", "small.md"} {
		if err := i.Hint(t.Context(), scope, path, now); err != nil {
			t.Fatal(err)
		}
	}
	var paths []string
	publish := func(e InventoryEntry) (deliveryPublication, error) {
		paths = append(paths, e.Path)
		return deliveryPublication{Bytes: e.Size}, nil
	}
	first, err := deliverInventoryTurn(t.Context(), i, scope, DeliveryCursor{}, publish, func() time.Time { return now })
	if err != nil || first.AssetExported != 0 || first.TextExported != 2 || first.Pressure != "oversized_asset_dedicated" || first.Cursor.NextLane != "asset" {
		t.Fatal(first, err)
	}
	second, err := deliverInventoryTurn(t.Context(), i, scope, first.Cursor, publish, func() time.Time { return now })
	if err != nil || second.AssetExported != 1 || second.Bytes != deliverySoftBytes+1 || second.Cursor.NextLane != "text" {
		t.Fatal("large supported asset excluded", second, err)
	}
	if err := i.Hint(t.Context(), scope, "later.md", now); err != nil {
		t.Fatal(err)
	}
	if err := i.Hint(t.Context(), scope, "later.png", now); err != nil {
		t.Fatal(err)
	}
	clock := now
	slow := func(e InventoryEntry) (deliveryPublication, error) {
		clock = clock.Add(deliveryAdmissionWindow + time.Second)
		return deliveryPublication{Bytes: e.Size}, nil
	}
	third, err := deliverInventoryTurn(t.Context(), i, scope, DeliveryCursor{}, slow, func() time.Time { return clock })
	if err != nil || third.TextExported != 1 || third.AssetExported != 0 || third.Pressure != "admission_window" || third.Cursor.NextLane != "asset" {
		t.Fatal("admitted after window", third, err)
	}
}

func TestInventoryDeliveryInteractiveAgeAndRetry(t *testing.T) {
	i, scope, now := inventoryFixture(t)
	inventoryWrite(t, scope, "bulk.md", "bulk")
	inventorySeed(t, i, scope, now, 32)
	inventoryWrite(t, scope, "edit.md", "edit")
	if err := i.Hint(t.Context(), scope, "edit.md", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	page, err := i.DeliveryPage(t.Context(), scope, "text", now.Add(time.Second), 1)
	if err != nil || page.Entries[0].Path != "edit.md" {
		t.Fatal("edit queued behind bulk", page, err)
	}
	page, _ = i.DeliveryPage(t.Context(), scope, "text", now.Add(31*time.Second), 1)
	if page.Entries[0].Path != "bulk.md" {
		t.Fatal("old bulk starved", page)
	}
	calls := []string{}
	failed := func(e InventoryEntry) (deliveryPublication, error) {
		calls = append(calls, e.Path)
		if e.Path == "edit.md" {
			return deliveryPublication{Reason: "publication_pending"}, ErrHeld
		}
		return deliveryPublication{Bytes: e.Size}, nil
	}
	first, err := deliverInventoryTurn(t.Context(), i, scope, DeliveryCursor{}, failed, func() time.Time { return now.Add(time.Second) })
	if err != nil || first.Held != 1 || first.TextExported != 1 {
		t.Fatal("failure stopped lane", first, err)
	}
	calls = nil
	if _, err := deliverInventoryTurn(t.Context(), i, scope, first.Cursor, failed, func() time.Time { return now.Add(2 * time.Second) }); err != nil || len(calls) != 0 {
		t.Fatal("hot retry", calls, err)
	}
}

func TestInventoryCursorCompatibilityAndSummaryPrivacy(t *testing.T) {
	var old RuntimeCursor
	if err := json.Unmarshal([]byte(`{"collection":"c","after":"old.md","retained_after":"intent","enrollment_after":"file"}`), &old); err != nil || old.After != "old.md" || old.RetainedAfter != "intent" {
		t.Fatal(old, err)
	}
	old.Delivery = map[string]DeliveryCursor{"c": {Generation: "g", TextAfter: "private-text.md", AssetAfter: "private-asset.png", TextSequence: 4, AssetSequence: 7}}
	raw, err := json.Marshal(RuntimeResult{Cursor: old})
	if err != nil || strings.Contains(string(raw), "private") {
		t.Fatal("exposed delivery paths", string(raw), err)
	}
}

func TestInventoryPublicationKeepsUnknownAndExcludedDistinct(t *testing.T) {
	unknown := inventoryPublication(InventoryProgress{})
	if unknown.TextPublished != nil || unknown.AssetPublished != nil || unknown.Unknown["counters"] == "" || unknown.Unknown["totals"] == "" {
		t.Fatalf("unknown became zero: %+v", unknown)
	}
	partial := inventoryPublication(InventoryProgress{CountersKnown: true, Counts: InventoryCounts{TextPresent: 5, TextPending: 2}})
	if partial.TextPublished == nil || *partial.TextPublished != 3 || partial.TotalsKnown || partial.Unknown["totals"] == "" {
		t.Fatalf("observed counters became corpus total: %+v", partial)
	}
	bounded := inventoryPublication(InventoryProgress{CountersKnown: true, Counts: InventoryCounts{TextPending: 1}, TextReason: "waiting_outside_bounded_window"})
	if bounded.Unknown["text_wait"] == "" || bounded.Unknown["text_retry"] == "" {
		t.Fatalf("bounded lane miss became known zero: %+v", bounded)
	}
	excluded := inventoryPublication(InventoryProgress{CountersKnown: true, TotalsKnown: true, Counts: InventoryCounts{AssetPresent: 4, Excluded: 4}})
	if excluded.AssetPublished != nil || excluded.Unknown["published"] == "" {
		t.Fatalf("exclusion called delivered: %+v", excluded)
	}
}

func TestInventoryPublicationMappingUsesCheckpointGeneration(t *testing.T) {
	inventory := []InventoryProgress{{Collection: "canonical-source", Generation: "g"}, {Collection: "another-source", Generation: "other"}}
	if source, ok := inventorySourceForGeneration(inventory, "g"); !ok || source != "canonical-source" {
		t.Fatalf("native collection key confused with source: %s %t", source, ok)
	}
	if _, ok := inventorySourceForGeneration(inventory, "missing"); ok {
		t.Fatal("missing generation mapping invented")
	}
	inventory = append(inventory, InventoryProgress{Collection: "ambiguous-source", Generation: "g"})
	if _, ok := inventorySourceForGeneration(inventory, "g"); ok {
		t.Fatal("ambiguous source mapping guessed")
	}
}

func TestInventoryPublicationUnknownEncodesNull(t *testing.T) {
	raw, err := json.Marshal(inventoryPublication(InventoryProgress{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"counts":null`, `"text_published":null`, `"text_oldest_age_seconds":null`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("unknown encoded as zero: %s", raw)
		}
	}
	var readable InventoryPublication
	if err = json.Unmarshal(raw, &readable); err != nil || readable.CountersKnown {
		t.Fatalf("inventory reader shape failed: %+v %v", readable, err)
	}
}
