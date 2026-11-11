package workspacehistory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/workspacefiles"
)

func TestHistoryInputBoundsAndCursor(t *testing.T) {
	now := time.Now().UTC()
	raw, _ := json.Marshal(cursor{At: now, ID: "desktop_one"})
	in, c, e := normalize(Input{Root: "box", Path: "Projects/One", After: base64.RawURLEncoding.EncodeToString(raw)})
	if e != nil || in.Limit != 100 || c.ID != "desktop_one" || in.To.Sub(in.From) > 91*24*time.Hour {
		t.Fatal(in, c, e)
	}
	for _, bad := range []Input{{Root: "box", Path: "../private"}, {Root: "box", Path: "x\\y"}, {Root: "box", Limit: 201}, {Root: "box", Kind: "arbitrary"}, {Root: "box", After: "invalid"}, {Root: "box", From: now, To: now.Add(-time.Hour)}} {
		if _, _, e = normalize(bad); e == nil {
			t.Fatal("accepted invalid input", bad)
		}
	}
	if inScope("Projects/One", "Projects/OneMore/note.md") || !inScope("Projects/One", "Projects/One/note.md") {
		t.Fatal("scope segment boundary")
	}
}

func TestBoxTimelineResolvesRegisteredRootsWithoutPersonalFallback(t *testing.T) {
	personal, company := t.TempDir(), t.TempDir()
	files := &workspacefiles.Service{Roots: []workspacefiles.Root{
		{Ref: "box", BoxID: "box_personal", SourcePath: personal},
		{Ref: "box:box_company", BoxID: "box_company", SourcePath: company, SourceKind: "box"},
		{Ref: "storage", SourcePath: t.TempDir(), ReadOnly: true},
	}}
	t.Cleanup(func() { _ = files.Close() })
	s := Service{Files: files}
	ctx := boxregistry.WithSelection(context.Background(), boxregistry.ResolvedSelection{BoxIDs: []string{"box_company"}})
	root, path, err := s.scope(ctx, Input{Root: "box:box_company"})
	if err != nil || root != company || path != company {
		t.Fatal(root, path, err)
	}
	for _, ref := range []string{"box", "storage", "box:missing"} {
		if _, _, err := s.scope(ctx, Input{Root: ref}); !errors.Is(err, fs.ErrPermission) {
			t.Fatal(ref, err)
		}
	}
	root, path, err = s.scope(context.Background(), Input{Root: "box", Path: "old.md"})
	if err != nil || root != personal || path != filepath.Join(personal, "old.md") {
		t.Fatal(root, path, err)
	}
	now := time.Now().UTC()
	raw, _ := json.Marshal(cursor{At: now, ID: "receipt", Root: "box"})
	_, err = s.List(ctx, Input{Root: "box:box_company", After: base64.RawURLEncoding.EncodeToString(raw)})
	if !errors.Is(err, workspacefiles.ErrInvalidCursor) {
		t.Fatal("cursor crossed Box roots", err)
	}
}
func TestHistoryQueryReadsMetadataAndGroupsFolderMoves(t *testing.T) {
	for _, required := range []string{"notes_workspace.path_history", "notes_workspace.tree_moves", "<>'tree_move'", "ORDER BY created_at DESC,operation_id DESC", "LIMIT $11"} {
		if !strings.Contains(historyQuery, required) {
			t.Fatal("missing", required)
		}
	}
	if strings.Contains(historyQuery, "->>'content'") || strings.Contains(historyQuery, "SELECT o.record") {
		t.Fatal("timeline reads full payloads")
	}
	if strings.Contains(historyQuery, "updated_at") {
		t.Fatal("mutable journal relocation time must not rewrite chronology")
	}
	if !strings.Contains(historyQuery, "o.record->'request'->>'box_id'") || !strings.Contains(historyQuery, "m.record->>'box_id'") {
		t.Fatal("timeline must retain recorded Box attribution without inventing a historical default")
	}
	if escaped("a_b%c") != "a\\_b\\%c" {
		t.Fatal("unsafe prefix matching")
	}
}
