package workspacenotes

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/workspacefiles"
)

func scopedNotes(t *testing.T) (Service, context.Context, string, string) {
	t.Helper()
	personal, company := t.TempDir(), t.TempDir()
	for _, root := range []string{personal, company} {
		if err := os.WriteFile(filepath.Join(root, "same.md"), []byte("same title"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	files := &workspacefiles.Service{Roots: []workspacefiles.Root{
		{Ref: "box", BoxID: "box_personal", SourcePath: personal, SourceKind: "box"},
		{Ref: "box:box_company", BoxID: "box_company", SourcePath: company, SourceKind: "box"},
	}}
	t.Cleanup(func() { _ = files.Close() })
	ctx := boxregistry.WithSelection(context.Background(), boxregistry.ResolvedSelection{BoxIDs: []string{"box_company"}})
	return Service{Files: files}, ctx, personal, company
}

func TestBoxMetadataRejectsForeignRootBeforeSQL(t *testing.T) {
	s, ctx, _, _ := scopedNotes(t)
	checks := []func() error{
		func() error { return s.Register(ctx, "box", nil) },
		func() error { _, e := s.TagFiles(ctx, "box", "tag"); return e },
		func() error { _, e := s.Complete(ctx, "box", "link", "same"); return e },
		func() error { _, e := s.Catalog(ctx, CatalogInput{Root: "box"}); return e },
		func() error { _, e := s.Observe(ctx, "box", "same.md"); return e },
		func() error { _, e := s.Seed(ctx, "box"); return e },
		func() error { return s.Retire(ctx, "box", "same.md") },
		func() error { return s.Move(ctx, "box", "same.md", "other.md") },
		func() error { _, e := s.LatestRefresh(ctx, "box"); return e },
		func() error { _, e := s.StartRefresh(ctx, "box", ""); return e },
	}
	for n, check := range checks {
		if err := check(); !errors.Is(err, fs.ErrPermission) {
			t.Fatal(n, err)
		}
	}
	if err := s.checkRoot(context.Background(), "box"); err != nil {
		t.Fatal("legacy caller changed", err)
	}
}

func TestBoxLinksKeepSameNamesAndRequireExplicitCrossBoxOpen(t *testing.T) {
	s, ctx, personal, _ := scopedNotes(t)
	items, err := s.Resolve(ctx, "box:box_company", "start.md", "same")
	if err != nil || len(items) != 1 || items[0].Root != "box:box_company" || items[0].Path != "same.md" {
		t.Fatal(items, err)
	}
	broad := boxregistry.WithSelection(ctx, boxregistry.ResolvedSelection{AllBoxes: true})
	foreign := filepath.Join(personal, "same.md")
	if _, err := s.Resolve(broad, "box:box_company", "start.md", foreign); !errors.Is(err, fs.ErrPermission) {
		t.Fatal("implicit absolute link crossed root", err)
	}
	if _, err := s.ResolveExplicit(ctx, "box:box_company", "start.md", foreign); !errors.Is(err, fs.ErrPermission) {
		t.Fatal("explicit open broadened selection", err)
	}
	items, err = s.ResolveExplicit(broad, "box:box_company", "start.md", foreign)
	if err != nil || len(items) != 1 || items[0].Root != "box" {
		t.Fatal(items, err)
	}
	archive := t.TempDir()
	if err := os.WriteFile(filepath.Join(archive, "same.md"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	s.Files.Roots = append(s.Files.Roots, workspacefiles.Root{Ref: "company_archive", SourcePath: archive, BoxID: "box_company", ReadOnly: true})
	items, err = s.Resolve(ctx, "box:box_company", "start.md", filepath.Join(archive, "same.md"))
	if err != nil || len(items) != 1 || items[0].Root != "company_archive" {
		t.Fatal("same-Box absolute link changed", items, err)
	}
}

func TestBoxDynamicMetadataRootRequiresVerifiedMembership(t *testing.T) {
	s, ctx, _, _ := scopedNotes(t)
	if err := s.checkRoot(ctx, "appdata_known"); !errors.Is(err, workspacefiles.ErrUnknownRoot) {
		t.Fatal(err)
	}
	s.RootBoxID = func(_ context.Context, ref string) (string, error) {
		if ref == "appdata_known" {
			return "box_company", nil
		}
		return "", nil
	}
	if err := s.checkRoot(ctx, "appdata_known"); err != nil {
		t.Fatal(err)
	}
	if err := s.checkRoot(ctx, "appdata_unknown"); !errors.Is(err, fs.ErrPermission) {
		t.Fatal("unknown root became selected Box", err)
	}
	if err := s.checkRoot(ctx, "box"); !errors.Is(err, fs.ErrPermission) {
		t.Fatal("resolver overrode registered foreign membership", err)
	}
}

func TestBoxMetadataRefreshBindsActualRegisteredRoot(t *testing.T) {
	s, ctx, _, _ := scopedNotes(t)
	company, err := s.refreshGeneration(ctx, "box:box_company")
	if err != nil || company == "" {
		t.Fatal(company, err)
	}
	personal, err := s.refreshGeneration(context.Background(), "box")
	if err != nil || personal == company {
		t.Fatal("refresh reused Personal source generation", personal, err)
	}
	queries := 0
	s.DB = sql.OpenDB(catalogConnector{t, func(string, []driver.NamedValue) (driver.Rows, error) {
		queries++
		raw, _ := json.Marshal(Refresh{ID: "foreign", Root: "box", Path: "private"})
		return &catalogRows{columns: []string{"record"}, values: [][]driver.Value{{raw}}}, nil
	}})
	t.Cleanup(func() { _ = s.DB.Close() })
	value, err := s.RefreshStatus(ctx, "foreign")
	if !errors.Is(err, fs.ErrPermission) || value.ID != "" || queries != 1 {
		t.Fatal(value, err, queries)
	}
}
