package workspacefiles

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/notesworkspace"
)

func TestBoxWritableTargetsAndMutationIsolation(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	personal, company := filepath.Join(base, "personal"), filepath.Join(base, "company")
	for _, root := range []string{personal, company} {
		if err := os.MkdirAll(filepath.Join(root, "Notes"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	service := &Service{Roots: []Root{
		{Ref: "box", BoxID: "box_personal", SourcePath: personal, Node: "main"},
		{Ref: "box:box_company", BoxID: "box_company", SourceKind: "box", SourcePath: company, Node: "main"},
		{Ref: "storage", SourcePath: filepath.Join(base, "storage"), ReadOnly: true},
	}}
	for _, ref := range []string{"box", "box:box_company"} {
		if _, err := service.Create(t.Context(), CreateInput{Root: ref, Path: "Notes/same.md", Text: ref}); err != nil {
			t.Fatal(err)
		}
		if _, err := service.WritableTarget(t.Context(), ref, "Notes/new.md"); err != nil {
			t.Fatal(err)
		}
	}
	ctx := boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{BoxIDs: []string{"box_company"}})
	if _, err := service.Create(ctx, CreateInput{Root: "box", Path: "Notes/foreign.md"}); !errors.Is(err, fs.ErrPermission) {
		t.Fatal("foreign create admitted", err)
	}
	if _, err := service.WritableTarget(ctx, "box", "Notes/foreign.md"); !errors.Is(err, fs.ErrPermission) {
		t.Fatal("foreign write target admitted", err)
	}
	source, err := service.MutationSource(ctx, "", "")
	if err != nil || source.Path != company {
		t.Fatalf("company source = %#v: %v", source, err)
	}
	if _, err := service.MutationSource(ctx, personal, ""); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("foreign source admitted: %v", err)
	}
	resolver := MutationResolver{Files: service, Source: source}
	if err := resolver.WithSource(ctx, source.CollectionID, "Notes/same.md", func(notesworkspace.Source) error { return nil }); err != nil {
		t.Fatal(err)
	}
	foreign := boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{BoxIDs: []string{"box_personal"}})
	if err := resolver.WithSource(foreign, source.CollectionID, "Notes/same.md", func(notesworkspace.Source) error { t.Fatal("retargeted pending operation"); return nil }); !errors.Is(err, notesworkspace.ErrMembership) {
		t.Fatal(err)
	}
	if err := os.Symlink(personal, filepath.Join(company, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.WritableTarget(ctx, "box:box_company", "escape/foreign.md"); err == nil {
		t.Fatal("symlink escape admitted")
	}
	if _, err := service.Create(ctx, CreateInput{Root: "storage", Path: "new.md"}); !errors.Is(err, fs.ErrPermission) {
		t.Fatal(err)
	}
}
