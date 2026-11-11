package workspacefiles

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFilesShallowWatchAndWritableTargets(t *testing.T) {
	root := t.TempDir()
	s := Service{Roots: []Root{{Ref: "box", SourcePath: root}, {Ref: "storage", SourcePath: root, ReadOnly: true}}}
	defer s.Close()
	if err := os.Mkdir(filepath.Join(root, "notes"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WritableTarget(t.Context(), "storage", "notes/new.md"); err == nil {
		t.Fatal("storage mutation allowed")
	}
	if _, err := s.WritableTarget(t.Context(), "box", "../outside.md"); err == nil {
		t.Fatal("path escape allowed")
	}
	if target, err := s.WritableTarget(t.Context(), "box", "notes/new.md"); err != nil || target != filepath.Join(root, "notes/new.md") {
		t.Fatalf("target: %q %v", target, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan struct{})
	changed := make(chan []string, 2)
	done := make(chan error, 1)
	go func() {
		done <- s.Watch(ctx, "box", []string{"notes"}, func(paths []string, initial bool) error {
			if initial {
				close(ready)
			} else {
				changed <- paths
			}
			return nil
		})
	}()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not start")
	}
	select {
	case <-changed:
		t.Fatal("idle subscription emitted a change")
	case <-time.After(80 * time.Millisecond):
	}
	if err := os.WriteFile(filepath.Join(root, "notes/new.md"), []byte("source edit"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case paths := <-changed:
		if len(paths) != 1 || paths[0] != "notes" {
			t.Fatal(paths)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("source edit not notified")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watch did not cancel")
	}
}

func TestWatchRequestsRebindForReplacedDirectoryAndMissingAncestor(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			root := t.TempDir()
			s := Service{Roots: []Root{{Ref: "box", SourcePath: root}}}
			defer s.Close()
			if !missing {
				if err := os.Mkdir(filepath.Join(root, "notes"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			ready, changed := make(chan struct{}), make(chan struct{}, 1)
			done := make(chan error, 1)
			go func() {
				done <- s.Watch(t.Context(), "box", []string{"notes"}, func(_ []string, initial bool) error {
					if initial {
						close(ready)
					} else {
						changed <- struct{}{}
					}
					return nil
				})
			}()
			select {
			case <-ready:
			case <-time.After(time.Second):
				t.Fatal("watch did not start")
			}
			if missing {
				if err := os.Mkdir(filepath.Join(root, "notes"), 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Rename(filepath.Join(root, "notes"), filepath.Join(root, "old")); err != nil {
				t.Fatal(err)
			}
			select {
			case <-changed:
			case <-time.After(time.Second):
				t.Fatal("membership hint missing")
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("obsolete inode subscription did not end")
			}
		})
	}
}

func TestMissingWatchFolderKeepsUnrelatedLiveChanges(t *testing.T) {
	root := t.TempDir()
	s := Service{Roots: []Root{{Ref: "box", SourcePath: root}}}
	defer s.Close()
	if err := os.Mkdir(filepath.Join(root, "notes"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan struct{})
	changed := make(chan []string, 8)
	done := make(chan error, 1)
	go func() {
		done <- s.Watch(ctx, "box", []string{"retired/nested", "notes"}, func(paths []string, initial bool) error {
			if initial {
				close(ready)
			} else {
				changed <- paths
			}
			return nil
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("missing folder disabled the subscription: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not start")
	}
	if err := os.WriteFile(filepath.Join(root, "unrelated.md"), []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case paths := <-changed:
		t.Fatalf("unrelated ancestor child triggered work: %v", paths)
	case <-time.After(120 * time.Millisecond):
	}
	check := func(want string) {
		t.Helper()
		select {
		case paths := <-changed:
			if len(paths) != 1 || paths[0] != want {
				t.Fatalf("wrong watch scope: %v, want %s", paths, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no event for %s", want)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "notes/new.md"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	check("notes")
	if err := os.Mkdir(filepath.Join(root, "retired"), 0700); err != nil {
		t.Fatal(err)
	}
	check("retired/nested")
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watch did not cancel")
	}
}

func TestMissingWatchFallbackDoesNotBypassPathGuards(t *testing.T) {
	root := t.TempDir()
	s := Service{Roots: []Root{{Ref: "box", SourcePath: root}}}
	defer s.Close()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"../outside/missing", ".loom-notes-" + strings.Repeat("a", 64) + "/missing", "linked/missing"} {
		err := s.Watch(t.Context(), "box", []string{relative}, func([]string, bool) error {
			t.Fatal("invalid path became an admitted ancestor watch")
			return nil
		})
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("guard not retained for %s: %v", relative, err)
		}
	}
}

func TestFilesWatchDoesNotSilentlyTruncateLargerInventories(t *testing.T) {
	root := t.TempDir()
	s := Service{Roots: []Root{{Ref: "box", SourcePath: root}}}
	defer s.Close()
	paths := make([]string, 70)
	for i := range paths {
		paths[i] = fmt.Sprintf("folder-%d", i)
		if err := os.Mkdir(filepath.Join(root, paths[i]), 0700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := s.Watch(ctx, "box", paths, func(observed []string, initial bool) error {
		if !initial || len(observed) != 70 {
			t.Fatalf("incomplete watch: %d", len(observed))
		}
		cancel()
		return nil
	})
	if err != context.Canceled {
		t.Fatal(err)
	}
	if err := s.Watch(t.Context(), "box", make([]string, 4097), func([]string, bool) error { return nil }); err != ErrInvalidPath {
		t.Fatal("oversized inventory accepted", err)
	}
}
