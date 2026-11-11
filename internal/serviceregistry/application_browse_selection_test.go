package serviceregistry

import (
	"strings"
	"testing"
)

func TestApplicationBrowseProjectSelectionPrecedesPagination(t *testing.T) {
	f := applicationTestBrowse(t)
	f.accept(t)
	var record ApplicationBrowseRecord
	if err := f.runtime.Store.read("browse-"+f.request.Owner.Instance(), &record); err != nil {
		t.Fatal(err)
	}
	// Additional accepted-view metadata is enough to test catalog selection.
	// No grants, credentials or installation state are changed or reapplied.
	second := record
	second.Owner.Resource = "second-reader"
	if err := f.runtime.Store.write("browse-"+second.Owner.Instance(), second); err != nil {
		t.Fatal(err)
	}
	foreign := record
	foreign.Owner.ProjectID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAW"
	if err := f.runtime.Store.write("browse-"+foreign.Owner.Instance(), foreign); err != nil {
		t.Fatal(err)
	}
	q := ApplicationBrowseRead{Operation: "catalog", RestrictProjects: true, ProjectIDs: []string{f.request.Owner.ProjectID}, Limit: 1}
	first, _ := f.read(t, q)
	if len(first.Roots) != 1 || first.Roots[0].ProjectID != f.request.Owner.ProjectID || first.NextCursor != 1 {
		t.Fatalf("first selected page = %+v", first)
	}
	q.Cursor = first.NextCursor
	next, _ := f.read(t, q)
	if len(next.Roots) != 1 || next.Roots[0].ProjectID != f.request.Owner.ProjectID || next.Roots[0].Ref == first.Roots[0].Ref || next.NextCursor != 0 {
		t.Fatalf("foreign records consumed selected pagination: %+v", next)
	}
	q.Cursor = 2
	end, _ := f.read(t, q)
	if len(end.Roots) != 0 || end.NextCursor != 0 {
		t.Fatalf("selected cursor included foreign records: %+v", end)
	}
	legacy, _ := f.read(t, ApplicationBrowseRead{Operation: "catalog"})
	if len(legacy.Roots) != 3 {
		t.Fatalf("legacy unscoped catalog changed: %+v", legacy)
	}
	empty, _ := f.read(t, ApplicationBrowseRead{Operation: "catalog", RestrictProjects: true})
	if len(empty.Roots) != 0 || empty.NextCursor != 0 {
		t.Fatal("empty selection widened to all projects")
	}
}

func TestApplicationBrowseProjectSelectionGuardsDirectReads(t *testing.T) {
	f := applicationTestBrowse(t)
	receipt := f.accept(t)
	ref := applicationBrowseRootRef(f.request.Owner, "exports")
	foreign := "project_01ARZ3NDEKTSV4RRFFQ69G5FAW"
	for _, operation := range []string{"resolve", "open", "children"} {
		f.denied(t, f.peer, ApplicationBrowseRead{Operation: operation, Ref: ref, RestrictProjects: true, ProjectIDs: []string{foreign}}, "browse.project_outside_selection")
		f.denied(t, f.peer, ApplicationBrowseRead{Operation: operation, Ref: ref, ProjectID: foreign}, "browse.project_outside_selection")
		f.denied(t, f.peer, ApplicationBrowseRead{Operation: operation, Ref: ref, RestrictProjects: true}, "browse.project_outside_selection")
	}
	f.denied(t, f.peer, ApplicationBrowseRead{Operation: "catalog", ProjectID: foreign, RestrictProjects: true, ProjectIDs: []string{f.request.Owner.ProjectID}}, "browse.project_outside_selection")
	root, file := f.read(t, ApplicationBrowseRead{Operation: "open", Ref: ref, Path: "paper.pdf", Revision: receipt.Revision, ProjectID: f.request.Owner.ProjectID, RestrictProjects: true, ProjectIDs: []string{f.request.Owner.ProjectID}})
	if file == nil || root.Root == nil || root.Root.ProjectID != f.request.Owner.ProjectID {
		t.Fatal("selected project could not open its accepted view")
	}
	project, err := ApplicationBrowseProjectID(ref)
	if err != nil || project != f.request.Owner.ProjectID {
		t.Fatal(project, err)
	}
	if _, err := ApplicationBrowseProjectID(strings.Replace(ref, "appdata_", "box:", 1)); err == nil {
		t.Fatal("invalid dynamic reference parsed as a project")
	}
	f.denied(t, f.peer, ApplicationBrowseRead{Operation: "catalog", ProjectIDs: []string{foreign}}, "browse.project_selection_invalid")
	f.denied(t, f.peer, ApplicationBrowseRead{Operation: "catalog", RestrictProjects: true, ProjectIDs: []string{foreign, foreign}}, "browse.project_selection_invalid")
}
