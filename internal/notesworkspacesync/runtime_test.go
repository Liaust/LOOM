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

func TestSourceHintsExpireAndNoticeChangedIdentity(t *testing.T) {
	p := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(p, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Lstat(p)
	now := time.Now()
	h := sourceHint{info: info, verified: now}
	if !h.matches(info, now) || h.matches(info, now.Add(time.Minute)) {
		t.Fatal("hint lifetime")
	}
	if err := os.Rename(p, p+".retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	replacement, _ := os.Lstat(p)
	if h.matches(replacement, now) {
		t.Fatal("same-size replacement accepted as unchanged")
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
	if err != nil || more || !reflect.DeepEqual(got, []string{"Selected/a.md", "Selected/nested/b.md"}) {
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
	if !reflect.DeepEqual(all, []string{"a.md", "a/note.md", "image.png", "z.txt"}) {
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

func TestRuntimeSummaryDoesNotExposeCursor(t *testing.T) {
	raw, err := json.Marshal(RuntimeResult{Cursor: RuntimeCursor{Collection: "private-collection", After: "private-note.md", RetainedAfter: "private-operation"}})
	if err != nil || strings.Contains(string(raw), "private") {
		t.Fatalf("cursor exposed: %s, %v", raw, err)
	}
}
