package storagearchive

import (
	"encoding/json"
	"testing"

	"loom.local/loom/internal/boxregistry"
)

func TestMultiBoxArchiveRestoreKeepsOriginalBinding(t *testing.T) {
	f := newWorkspaceMoveFixture(t)
	f.roots.BoxID = "box_company"
	f.service.Roots = f.roots
	plan := f.plan(t)
	if plan.BoxID != f.roots.BoxID {
		t.Fatal("archive plan lost Box identity")
	}
	other := f.service
	other.Roots.BoxID = "box_other"
	if _, err := other.ApplyArchive(t.Context(), plan, plan.PlanDigest); err == nil || f.journal.exists {
		t.Fatal("foreign Box applied original plan")
	}
	archived, err := f.service.ApplyArchive(t.Context(), plan, plan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Manifest.BoxID != plan.BoxID || archived.Operation.BoxID != plan.BoxID {
		t.Fatal("archive receipt lost home Box")
	}
	catalog := boxregistry.Catalog{DefaultBox: "personal", Boxes: []boxregistry.Box{
		{ID: "box_personal", Key: "personal", RootRef: "box", Primary: true, Available: true, RootPath: "/not-the-original-box", ArchiveRoot: "/not-the-original-archive"},
		{ID: "box_company", Key: "company", RootRef: "box:box_company", Available: true, RootPath: f.roots.BoxRoot, ArchiveRoot: f.roots.StorageRoot},
	}}
	roots, err := WorkspaceRootsForManifest(catalog, *archived.Manifest)
	if err != nil || roots != f.roots {
		t.Fatalf("restore selected navigation default instead of home: %#v %v", roots, err)
	}
	restore := planRestore(t, f, plan.OperationID)
	if restore.BoxID != plan.BoxID {
		t.Fatal("restore plan changed Box")
	}
	restored, err := f.service.ApplyRestore(t.Context(), restore, restore.PlanDigest)
	if err != nil || restored.Manifest.BoxID != plan.BoxID {
		t.Fatalf("restore binding: %#v %v", restored.Manifest, err)
	}
	catalog.Boxes[1].Available = false
	if _, err := WorkspaceRootsForManifest(catalog, *archived.Manifest); err == nil {
		t.Fatal("missing home Box silently fell back")
	}
}

func TestMultiBoxHistoricalManifestAndPathTransferHold(t *testing.T) {
	f, _, _ := archivedRestoreFixture(t)
	inspection, err := f.service.InspectArchive(t.Context(), moveTestOperationID)
	if err != nil || inspection.Manifest == nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(inspection.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if _, exists := fields["box_id"]; exists {
		t.Fatal("historical manifest JSON changed")
	}
	primary := f.roots
	primary.BoxID, primary.Primary = "box_personal", true
	if err := ValidateWorkspaceArchiveManifestForRoots(*inspection.Manifest, primary); err != nil {
		t.Fatal("legacy Personal history rejected", err)
	}
	primary.Primary = false
	if err := ValidateWorkspaceArchiveManifestForRoots(*inspection.Manifest, primary); err == nil {
		t.Fatal("identity-free manifest was adopted by new Box")
	}
	service := WorkspacePathService{Roots: TrustedWorkspaceRoots{BoxID: "box_company", BoxRoot: f.roots.BoxRoot, StorageRoot: f.roots.StorageRoot}}
	from, err := pathOperationRef(service.Roots, WorkspaceRootBox, "Projects/source")
	if err != nil {
		t.Fatal(err)
	}
	to := from
	to.AbsolutePath = "/foreign-box/Projects/destination"
	to.RelativePath = "Projects/destination"
	p := WorkspacePathPlan{Schema: WorkspacePathSchema, Kind: "relocate_project", Move: RetainedRelocationPlan{BoxID: "box_company", Source: from, Destination: to}}
	if err := service.validateRefs(p); err == nil {
		t.Fatal("cross-Box transfer disguised as same-root rename")
	}
}

func TestMultiBoxPendingPersonalReviewRetainsOriginalHash(t *testing.T) {
	f := newWorkspaceMoveFixture(t)
	plan := f.plan(t)
	review, err := NewWorkspaceMovePlanReview(plan)
	if err != nil {
		t.Fatal(err)
	}
	f.service.Roots.BoxID, f.service.Roots.Primary = "box_personal", true
	replanned, err := f.service.ReplanReviewed(t.Context(), review)
	if err != nil || replanned.PlanDigest != plan.PlanDigest || replanned.BoxID != "" {
		t.Fatalf("pending Personal plan changed: %s %v", replanned.PlanDigest, err)
	}
	f.service.Roots.Primary = false
	if _, err := f.service.ReplanReviewed(t.Context(), review); err == nil {
		t.Fatal("Company adopted an identity-free Personal review")
	}
}
