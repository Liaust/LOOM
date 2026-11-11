package storagearchive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspacePathMovePreservesDirectoryAndSupportsExactRecovery(t *testing.T) {
	for _, kind := range []string{"relocate_project", "retain_child"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			roots := TrustedWorkspaceRoots{BoxRoot: filepath.Join(root, "box"), StorageRoot: filepath.Join(root, "storage")}
			for _, directory := range []string{filepath.Join(roots.BoxRoot, "Projects/original/old/empty"), filepath.Join(roots.BoxRoot, "Projects/group"), filepath.Join(roots.StorageRoot, "Projects")} {
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			from, to, destRoot := "Projects/original", "Projects/group/new", WorkspaceRootBox
			if kind == "retain_child" {
				from, to, destRoot = "Projects/original/old", "Projects/old", WorkspaceRootStorage
			}
			source, err := pathOperationRef(roots, WorkspaceRootBox, from)
			if err != nil {
				t.Fatal(err)
			}
			destination, err := pathOperationRef(roots, destRoot, to)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(source.AbsolutePath, "note.md"), []byte("unchanged note"), 0640); err != nil {
				t.Fatal(err)
			}
			p := WorkspacePathPlan{Schema: WorkspacePathSchema, Kind: kind, Move: RetainedRelocationPlan{Source: source, Destination: destination}}
			service := WorkspacePathService{Roots: roots}
			if err = service.validateRefs(p); err != nil {
				t.Fatal(err)
			}
			p.Move.Inventory, err = inventoryRetainedSchema(context.Background(), roots, source, NoFollowInventorySchemaVersion)
			if err != nil {
				t.Fatal(err)
			}
			a, err := openHeldDirectoryChain(roots.BoxRoot, filepath.ToSlash(filepath.Dir(from)), nil)
			if err != nil {
				t.Fatal(err)
			}
			p.Move.SourceAncestors = a.evidence
			a.Close()
			b, err := openHeldDirectoryChain(pathOperationRoot(roots, destRoot), filepath.ToSlash(filepath.Dir(to)), nil)
			if err != nil {
				t.Fatal(err)
			}
			p.Move.DestinationAncestors = b.evidence
			b.Close()
			if _, err = proveRetainedUnmoved(context.Background(), roots, p.Move); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if _, err = moveRetained(context.Background(), roots, p.Move); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = os.Lstat(source.AbsolutePath); !os.IsNotExist(err) {
				t.Fatal("old source still exists")
			}
			data, err := os.ReadFile(filepath.Join(destination.AbsolutePath, "note.md"))
			if err != nil || string(data) != "unchanged note" {
				t.Fatal("content changed", err)
			}
			if _, err = proveRetainedUnmoved(context.Background(), roots, p.Move); err == nil {
				t.Fatal("completed move cancelled")
			}
		})
	}
}

func TestWorkspacePathDigestBindsDependenciesAndLargeJSONIntegers(t *testing.T) {
	a := WorkspacePathPlan{Schema: WorkspacePathSchema, Dependencies: "one"}
	b := a
	b.Dependencies = "two"
	if workspacePathDigest(a) == workspacePathDigest(b) {
		t.Fatal("dependencies not bound")
	}
	if jsonValue([]byte(`9007199254740992`)) == jsonValue([]byte(`9007199254740993`)) {
		t.Fatal("JSON precision lost")
	}
}

func TestWorkspacePathDigestSurvivesJSONBKeyOrder(t *testing.T) {
	p := WorkspacePathPlan{Schema: WorkspacePathSchema, ProjectSnapshot: json.RawMessage(`{"z":9007199254740993,"a":{"y":1,"x":2}}`), Notes: json.RawMessage(`{"z":[],"a":"kept"}`)}
	want := workspacePathDigest(p)
	p.ProjectSnapshot = json.RawMessage(`{"a":{"x":2,"y":1},"z":9007199254740993}`)
	p.Notes = json.RawMessage(`{"a":"kept","z":[]}`)
	if workspacePathDigest(p) != want {
		t.Fatal("stored JSONB value changed the review hash")
	}
}
