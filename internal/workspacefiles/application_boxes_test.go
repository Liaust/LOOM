package workspacefiles

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/projects"
	sr "loom.local/loom/internal/serviceregistry"
)

const (
	applicationPersonalProject      = "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	applicationCompanyProject       = "project_01ARZ3NDEKTSV4RRFFQ69G5FAW"
	applicationSecondCompanyProject = "project_01ARZ3NDEKTSV4RRFFQ69G5FAX"
	applicationUnknownProject       = "project_01ARZ3NDEKTSV4RRFFQ69G5FAY"
)

func applicationBoxRef(project string) string {
	raw, _ := json.Marshal(struct {
		Owner sr.ApplicationOwner `json:"owner"`
		Key   string              `json:"key"`
	}{Owner: sr.ApplicationOwner{ProjectID: project, NodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV", Resource: "reader"}, Key: "exports"})
	return "appdata_" + base64.RawURLEncoding.EncodeToString(raw)
}

type applicationBoxFiles struct {
	roots           map[string]sr.ApplicationBrowseRoot
	queries         []sr.ApplicationBrowseRead
	ignoreSelection bool
}

func (f *applicationBoxFiles) permits(q sr.ApplicationBrowseRead, project string) bool {
	if f.ignoreSelection {
		return true
	}
	if q.ProjectID != "" && q.ProjectID != project {
		return false
	}
	if !q.RestrictProjects {
		return true
	}
	for _, id := range q.ProjectIDs {
		if id == project {
			return true
		}
	}
	return false
}

func (f *applicationBoxFiles) ReadBrowse(ctx context.Context, q sr.ApplicationBrowseRead) (sr.ApplicationBrowseResult, error) {
	f.queries = append(f.queries, q)
	if q.Operation == "catalog" {
		refs := []string{}
		for ref, root := range f.roots {
			if f.permits(q, root.ProjectID) {
				refs = append(refs, ref)
			}
		}
		sort.Strings(refs)
		out := sr.ApplicationBrowseResult{Roots: []sr.ApplicationBrowseRoot{}}
		limit := q.Limit
		if limit == 0 {
			limit = 100
		}
		for i := q.Cursor; i < len(refs); i++ {
			if len(out.Roots) == limit {
				out.NextCursor = i
				break
			}
			out.Roots = append(out.Roots, f.roots[refs[i]])
		}
		return out, nil
	}
	root, ok := f.roots[q.Ref]
	if !ok || !f.permits(q, root.ProjectID) {
		return sr.ApplicationBrowseResult{}, fs.ErrPermission
	}
	out := sr.ApplicationBrowseResult{Root: &root}
	if q.Operation == "children" {
		for _, name := range q.Names {
			relative := filepath.Join(q.Path, name)
			info, err := os.Lstat(filepath.Join(root.SourcePath, relative))
			if err != nil {
				return out, err
			}
			out.Entries = append(out.Entries, sr.ApplicationBrowseEntry{Name: name, Path: filepath.ToSlash(relative), Kind: "file", Size: info.Size(), ModifiedAt: info.ModTime()})
		}
	}
	return out, nil
}

func (f *applicationBoxFiles) OpenBrowse(ctx context.Context, q sr.ApplicationBrowseRead) (*os.File, sr.ApplicationBrowseRoot, error) {
	f.queries = append(f.queries, q)
	root, ok := f.roots[q.Ref]
	if !ok || !f.permits(q, root.ProjectID) {
		return nil, sr.ApplicationBrowseRoot{}, fs.ErrPermission
	}
	if q.Revision != "" && q.Revision != root.Revision {
		return nil, root, ErrRootChanged
	}
	parent, err := os.OpenRoot(root.SourcePath)
	if err != nil {
		return nil, root, err
	}
	defer parent.Close()
	file, err := parent.Open(q.Path)
	return file, root, err
}

func applicationBoxFixture(t *testing.T) (*Service, *applicationBoxFiles, map[string]string, context.Context) {
	t.Helper()
	f := &applicationBoxFiles{roots: map[string]sr.ApplicationBrowseRoot{}}
	current := map[string]string{applicationPersonalProject: "box_personal", applicationCompanyProject: "box_company", applicationSecondCompanyProject: "box_company", applicationUnknownProject: ""}
	for project := range current {
		root := t.TempDir() // Allocation custody is unrelated to physical Box paths.
		for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
			if err := os.WriteFile(filepath.Join(root, name), []byte(project), 0600); err != nil {
				t.Fatal(err)
			}
		}
		ref := applicationBoxRef(project)
		f.roots[ref] = sr.ApplicationBrowseRoot{Ref: ref, ProjectID: project, Label: "Exports", SourcePath: root, Node: "main", Revision: "view-a", Format: "files", ReadOnly: true, Available: true}
	}
	s := &Service{Applications: f, ApplicationProjectBoxes: func(ctx context.Context, requested []string) (map[string]string, error) {
		out := map[string]string{}
		if requested == nil {
			for id, box := range current {
				out[id] = box
			}
		} else {
			for _, id := range requested {
				out[id] = current[id]
			}
		}
		return out, nil
	}}
	t.Cleanup(func() { _ = s.Close() })
	ctx := boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{BoxIDs: []string{"box_company"}})
	return s, f, current, ctx
}

func TestApplicationBoxCatalogFiltersBeforePagination(t *testing.T) {
	if projects.MaxApplicationProjectBoxes > sr.ApplicationBrowseMaxProjectIDs {
		t.Fatal("projects owner inventory exceeds native browse bound")
	}
	s, f, _, ctx := applicationBoxFixture(t)
	page, err := s.ApplicationRoots(ctx, "", 0, 1)
	if err != nil || len(page.Roots) != 1 || page.Roots[0].BoxID != "box_company" || page.NextCursor != 1 {
		t.Fatal(page, err)
	}
	next, err := s.ApplicationRoots(ctx, "", page.NextCursor, 1)
	if err != nil || len(next.Roots) != 1 || next.Roots[0].BoxID != "box_company" || next.Roots[0].Ref == page.Roots[0].Ref || next.NextCursor != 0 {
		t.Fatal(next, err)
	}
	for _, q := range f.queries {
		if !q.RestrictProjects || len(q.ProjectIDs) != 2 {
			t.Fatal("catalog was filtered after helper pagination", q)
		}
	}
	before := len(f.queries)
	if _, err := s.ApplicationRoots(ctx, applicationPersonalProject, 0, 1); !errors.Is(err, fs.ErrPermission) || len(f.queries) != before {
		t.Fatal("direct foreign project reached helper", err)
	}
	if _, err := s.ApplicationRoots(ctx, applicationUnknownProject, 0, 1); !errors.Is(err, ErrApplicationBoxOwnership) {
		t.Fatal("unknown project was relabelled", err)
	}
	emptyCtx := boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{BoxIDs: []string{"box_empty"}})
	empty, err := s.ApplicationRoots(emptyCtx, "", 0, 1)
	if err != nil || len(empty.Roots) != 0 || empty.NextCursor != 0 || !f.queries[len(f.queries)-1].RestrictProjects {
		t.Fatal("empty Box fell back to global catalog", empty, err)
	}
	f.ignoreSelection = true
	if _, err := s.ApplicationRoots(ctx, "", 0, 100); !errors.Is(err, ErrApplicationBoxOwnership) {
		t.Fatal("helper ignored scope without failing closed", err)
	}
}

func TestApplicationBoxResolveOpenAndLeaseMembership(t *testing.T) {
	s, f, current, ctx := applicationBoxFixture(t)
	ref := applicationBoxRef(applicationCompanyProject)
	root, err := s.ApplicationRoot(ctx, ref)
	if err != nil || root.BoxID != "box_company" || root.ProjectID != applicationCompanyProject || !root.ReadOnly {
		t.Fatal(root, err)
	}
	file, _, err := s.Open(ctx, ref, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	before := len(f.queries)
	for _, foreign := range []string{applicationPersonalProject, applicationUnknownProject} {
		if _, err := s.ApplicationRoot(ctx, applicationBoxRef(foreign)); err == nil {
			t.Fatal("foreign/unknown resolve admitted")
		}
		if file, _, err := s.Open(ctx, applicationBoxRef(foreign), "a.txt"); err == nil || file != nil {
			t.Fatal("foreign/unknown descriptor returned")
		}
	}
	if len(f.queries) != before {
		t.Fatal("foreign/unknown direct read reached privileged helper")
	}
	page, err := s.List(ctx, ListInput{Root: ref, Limit: 1})
	if err != nil || !page.HasMore {
		t.Fatal(page, err)
	}
	for _, q := range f.queries {
		if !q.RestrictProjects || len(q.ProjectIDs) != 1 || q.ProjectIDs[0] != applicationCompanyProject {
			t.Fatal("resolve/open/lease/children lost bound project", q)
		}
	}
	current[applicationCompanyProject] = "box_personal"
	if _, err := s.List(ctx, ListInput{Root: ref, Limit: 1, Cursor: page.NextCursor}); err == nil {
		t.Fatal("cursor survived foreign Box reassociation")
	}
	if file, _, err := s.Open(ctx, ref, "a.txt"); err == nil || file != nil {
		t.Fatal("open used cached membership after reassociation")
	}
}

func TestApplicationBoxUnknownOwnershipFailsClosedLegacyUnchanged(t *testing.T) {
	s, f, _, ctx := applicationBoxFixture(t)
	s.ApplicationProjectBoxes = nil
	if _, err := s.ApplicationRoots(ctx, "", 0, 100); !errors.Is(err, ErrApplicationBoxOwnership) {
		t.Fatal("missing owner callback used global catalog", err)
	}
	if _, err := s.ApplicationRoot(ctx, applicationBoxRef(applicationCompanyProject)); !errors.Is(err, ErrApplicationBoxOwnership) {
		t.Fatal(err)
	}
	if file, _, err := s.Open(ctx, applicationBoxRef(applicationCompanyProject), "a.txt"); err == nil || file != nil {
		t.Fatal("missing owner callback returned content")
	}
	if len(f.queries) != 0 {
		t.Fatal("unknown ownership reached privileged helper")
	}
	legacy, err := s.ApplicationRoots(context.Background(), "", 0, 100)
	if err != nil || len(legacy.Roots) != 4 || f.queries[0].RestrictProjects || len(f.queries[0].ProjectIDs) != 0 {
		t.Fatal("legacy global catalog changed", legacy, err)
	}
	file, _, err := s.Open(context.Background(), applicationBoxRef(applicationUnknownProject), "a.txt")
	if err != nil {
		t.Fatal("legacy internal read changed", err)
	}
	file.Close()
}
