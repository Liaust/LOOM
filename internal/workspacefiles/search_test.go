package workspacefiles

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFilenameSearchIndependentSafeAndPaged(t *testing.T) {
	s, root := filesFixture(t)
	for _, name := range []string{"a/START-HERE.md", "b/START-HERE.py", "c/START-HERE.pdf", "node_modules/START-HERE.md", ".git/START-HERE.md", ".loom/state/START-HERE.md"} {
		writeFile(t, root, name, "no content read")
	}
	outside := t.TempDir()
	writeFile(t, outside, "START-HERE.md", "private")
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	in := SearchInput{Root: "box", Query: "start-here", Limit: 1}
	seen := map[string]bool{}
	for i := 0; i < 10; i++ {
		page, err := s.Search(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Entries {
			seen[e.RelativePath] = true
		}
		if in.Cursor != "" {
			again, e := s.Search(context.Background(), in)
			if e != nil || !reflect.DeepEqual(again, page) {
				t.Fatalf("replay: %v %+v %+v", e, again, page)
			}
		}
		if !page.HasMore {
			if page.Coverage != "complete" {
				t.Fatal(page)
			}
			break
		}
		in.Cursor = page.NextCursor
	}
	if !reflect.DeepEqual(seen, map[string]bool{"a/START-HERE.md": true, "b/START-HERE.py": true, "c/START-HERE.pdf": true}) {
		t.Fatal(seen)
	}
	in.Query = "other"
	if _, err := s.Search(context.Background(), in); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal(err)
	}
}
func TestFilenameSearchRejectsTraversalAndCancelledRequests(t *testing.T) {
	s, _ := filesFixture(t)
	for _, in := range []SearchInput{{Root: "box", Path: "../private", Query: "file"}, {Root: "box", Query: ""}, {Root: "unknown", Query: "file"}, {Root: "box", Query: "file", Limit: 501}} {
		if _, err := s.Search(context.Background(), in); err == nil {
			t.Fatal(in)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Search(ctx, SearchInput{Root: "box", Query: "file"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
