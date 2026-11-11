package workspacefiles

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func filesFixture(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	service := &Service{Roots: []Root{{Ref: "box", Label: "Workspace", Node: "main", SourcePath: root}}}
	t.Cleanup(func() { _ = service.Close() })
	return service, root
}

func writeFile(t *testing.T, root, relative, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, relative)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, relative), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestFilesBrowseWithoutEnrollment(t *testing.T) {
	s, root := filesFixture(t)
	writeFile(t, root, "new/fresh.md", "not indexed or enrolled")
	writeFile(t, root, "new/drawing.canvas", `{"nodes":[]}`)
	writeFile(t, root, "new/catalog.base", "views: []")
	if err := os.MkdirAll(filepath.Join(root, "new/empty"), 0700); err != nil {
		t.Fatal(err)
	}
	page, err := s.List(context.Background(), ListInput{Root: "box", Path: "new"})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, item := range page.Entries {
		names[item.Name] = item.Kind
	}
	if !reflect.DeepEqual(names, map[string]string{"fresh.md": "file", "drawing.canvas": "file", "catalog.base": "file", "empty": "directory"}) {
		t.Fatalf("actual entries: %#v", names)
	}
	empty, err := s.List(context.Background(), ListInput{Root: "box", Path: "new/empty"})
	if err != nil || len(empty.Entries) != 0 || empty.HasMore {
		t.Fatalf("empty folder: %+v %v", empty, err)
	}
	file, metadata, err := s.Open(context.Background(), "box", "new/fresh.md")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	body, err := io.ReadAll(file)
	if err != nil || string(body) != "not indexed or enrolled" || !strings.HasPrefix(metadata.Revision, `W/"`) {
		t.Fatalf("original read: %q %+v %v", body, metadata, err)
	}
	if info, err := s.Stat(context.Background(), "box", "new/drawing.canvas"); err != nil || info.Kind != "file" {
		t.Fatalf("stat: %+v %v", info, err)
	}
}

func TestFilesPaginationReplaysAndDetectsChange(t *testing.T) {
	s, root := filesFixture(t)
	for i := 0; i < 13; i++ {
		writeFile(t, root, fmt.Sprintf("item-%02d.md", i), "body")
	}
	input := ListInput{Root: "box", Limit: 3}
	first, err := s.List(context.Background(), input)
	if err != nil || !first.HasMore || len(first.Entries) != 3 {
		t.Fatalf("first page: %+v %v", first, err)
	}
	input.Cursor = first.NextCursor
	second, err := s.List(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.List(context.Background(), input)
	if err != nil || !reflect.DeepEqual(second, replay) {
		t.Fatalf("page retry advanced: %+v %+v %v", second, replay, err)
	}
	seen := map[string]bool{}
	for _, page := range []Page{first, second} {
		for _, e := range page.Entries {
			seen[e.Name] = true
		}
	}
	for second.HasMore {
		input.Cursor = second.NextCursor
		second, err = s.List(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range second.Entries {
			if seen[e.Name] {
				t.Fatal("duplicate", e.Name)
			}
			seen[e.Name] = true
		}
	}
	if len(seen) != 13 {
		t.Fatalf("lost files: %d", len(seen))
	}
	input.Cursor = first.NextCursor
	if _, err := s.List(context.Background(), input); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("old page accepted: %v", err)
	}
	first, err = s.List(context.Background(), ListInput{Root: "box", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	changed := time.Now().Add(time.Second)
	if err := os.Chtimes(root, changed, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(context.Background(), ListInput{Root: "box", Limit: 3, Cursor: first.NextCursor}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("changed directory: %v", err)
	}
}

func TestFilesCursorExpiryRebindAndLeaseBound(t *testing.T) {
	s, root := filesFixture(t)
	writeFile(t, root, "a", "a")
	writeFile(t, root, "b", "b")
	now := time.Now()
	s.Now = func() time.Time { return now }
	s.LeaseTTL = time.Minute
	s.MaxLeases = 1
	first, err := s.List(context.Background(), ListInput{Root: "box", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(context.Background(), ListInput{Root: "box", Limit: 1}); err != nil {
		t.Fatal(err)
	}
	if len(s.leases) != 1 {
		t.Fatal("unbounded leases")
	}
	if _, err := s.List(context.Background(), ListInput{Root: "box", Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("evicted cursor: %v", err)
	}
	first, err = s.List(context.Background(), ListInput{Root: "box", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := s.List(context.Background(), ListInput{Root: "box", Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("expired cursor: %v", err)
	}
	first, err = s.List(context.Background(), ListInput{Root: "box", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root + "-old") })
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(context.Background(), ListInput{Root: "box", Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("rebound root: %v", err)
	}
}

func TestFilesConfinedPathsAndSpecialFiles(t *testing.T) {
	s, root := filesFixture(t)
	outside := t.TempDir()
	writeFile(t, outside, "private", "not authorized")
	writeFile(t, root, ".loom/state/token", "private runtime state")
	recovery := ".loom-notes-" + strings.Repeat("a", 64)
	writeFile(t, root, recovery+"/pending.json", "retained user draft")
	writeFile(t, root, "node_modules/rebuildable", "rebuild")
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".loom/state"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"../private", "/etc/passwd", "a/../private", `a\private`, "escape/private", "alias/token", ".loom/state/token", recovery + "/pending.json"} {
		file, _, err := s.Open(context.Background(), "box", relative)
		if err == nil {
			file.Close()
			t.Fatalf("unsafe path allowed: %s", relative)
		}
	}
	page, err := s.List(context.Background(), ListInput{Root: "box"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Entries {
		if item.Name == recovery {
			t.Fatal("recovery journal exposed as user content")
		}
		if item.Name == "node_modules" {
			t.Fatal("rebuildables should be hidden by default")
		}
	}
	page, err = s.List(context.Background(), ListInput{Root: "box", Path: ".loom", ShowIgnored: true})
	if err != nil || len(page.Entries) != 0 {
		t.Fatalf("mandatory runtime entries visible: %+v %v", page, err)
	}
	if _, err := s.List(context.Background(), ListInput{Root: "box", Path: "node_modules", ShowIgnored: true}); err != nil {
		t.Fatalf("explicit rebuildable browse: %v", err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Open(context.Background(), "box", "pipe"); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("special file was not rejected: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.List(cancelled, ListInput{Root: "box"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestFilesMissingCurrentNeverUsesArchive(t *testing.T) {
	s, root := filesFixture(t)
	archive := t.TempDir()
	writeFile(t, archive, "missing.md", "old archive version")
	s.Roots = append(s.Roots, Root{Ref: "archive", SourcePath: archive, ReadOnly: true, Verification: "not_checked"})
	if _, _, err := s.Open(context.Background(), "box", "missing.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing original: %v", err)
	}
	roots, err := s.ListRoots(context.Background())
	if err != nil || len(roots) != 2 || !roots[1].ReadOnly || roots[1].Verification != "not_checked" {
		t.Fatalf("qualified roots: %+v %v", roots, err)
	}
	if _, err := os.Stat(filepath.Join(root, "missing.md")); !os.IsNotExist(err) {
		t.Fatal("read created an original")
	}
}
