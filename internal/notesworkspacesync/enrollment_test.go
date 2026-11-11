package notesworkspacesync

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"loom.local/loom/internal/knowledge"
)

func TestEnrollmentAdmissionHoldPreservesSafePhase(t *testing.T) {
	err := &knowledge.NotesAdmissionError{Phase: "owner_acknowledgement", Err: knowledge.ErrNotesCustodyPaused}
	hold := enrollmentAdmissionHold("source", err)
	if hold.Phase != "owner_acknowledgement" || hold.Failure != "custody_unavailable" || !errors.Is(err, knowledge.ErrNotesCustodyPaused) {
		t.Fatalf("admission qualification lost: %+v", hold)
	}
	hold = enrollmentAdmissionHold("source", &knowledge.NotesAdmissionError{Phase: "database_connection", Err: context.DeadlineExceeded})
	if hold.Failure != "deadline_exceeded" || hold.Phase != "database_connection" {
		t.Fatalf("timeout qualification lost: %+v", hold)
	}
	hold = enrollmentAdmissionHold("source", &knowledge.NotesAdmissionError{Phase: "owner_acknowledgement", Reason: "source_scan_failed", Err: knowledge.ErrNotesCustodyPaused})
	if hold.Failure != "source_scan_failed" {
		t.Fatalf("specific owner diagnosis hidden: %+v", hold)
	}
}

func TestAutomaticEnrollmentPreservesLegacyAndRenamedSelections(t *testing.T) {
	cfg := runtimeFixture()
	first, err := mergeEnrollmentSelections(cfg, EnrollmentInventory{}, []enrollmentCandidate{{ID: "source-new", Root: "Projects/First Name/Notes"}})
	if err != nil || len(first) != 2 || !reflect.DeepEqual(first[0], cfg.Scopes[0]) {
		t.Fatalf("legacy adoption: %+v %v", first, err)
	}
	previous := EnrollmentInventory{Collections: []EnrollmentCollection{{RuntimeSelection: first[1], Status: "active"}}}
	again, err := mergeEnrollmentSelections(cfg, previous, []enrollmentCandidate{{ID: "source-new", Root: "Projects/Renamed/Notes"}})
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatalf("rename changed client identity: %+v %v", again, err)
	}
	if first[1].AdmissionPath != "" || first[1].Generation != "automatic_enrollment_v1" {
		t.Fatal("new/empty collection needs no probe file")
	}
}

func TestEnrollmentCandidateEligibility(t *testing.T) {
	project := "project"
	for _, rootStatus := range []string{knowledge.SourceRootStatusActive, knowledge.SourceRootStatusDisabled, knowledge.SourceRootStatusBlocked, "stale", "retired"} {
		for _, archives := range []bool{false, true} {
			for _, status := range []string{"active", "archived", "paused", "draft", ""} {
				root := knowledge.SourceRoot{ProjectID: &project, Status: rootStatus}
				want := status == "active" && rootStatus == knowledge.SourceRootStatusActive || status == "archived" && archives
				if got := enrollmentCandidateEligible(root, status, archives); got != want {
					t.Fatalf("project=%q root=%q archives=%v: eligible=%v want=%v", status, rootStatus, archives, got, want)
				}
			}
			if got := enrollmentCandidateEligible(knowledge.SourceRoot{Status: rootStatus}, "", archives); got != (rootStatus == knowledge.SourceRootStatusActive) {
				t.Fatalf("inactive Box root automatically enrolled: root=%q archives=%v", rootStatus, archives)
			}
		}
	}
}

func TestAutomaticEnrollmentInactiveDiscoveryPreservesConfiguredAndWithdrawn(t *testing.T) {
	cfg := runtimeFixture()
	project := "project"
	retired := RuntimeSelection{Scope: Scope{Workspace: cfg.Scopes[0].Workspace, Collection: "retired", SourceCollection: "retired-source", Generation: "old", Root: "Projects/Retired/Notes"}}
	previous := EnrollmentInventory{Collections: []EnrollmentCollection{{RuntimeSelection: retired, Status: "withdrawn"}}}
	var candidates []enrollmentCandidate
	for _, id := range []string{cfg.Scopes[0].SourceCollection, retired.SourceCollection} {
		root := knowledge.SourceRoot{NotesSourceRootID: id, ProjectID: &project, Status: knowledge.SourceRootStatusDisabled}
		if enrollmentCandidateEligible(root, "active", true) {
			candidates = append(candidates, enrollmentCandidate{ID: id, Root: retired.Root})
		}
	}
	selections, err := mergeEnrollmentSelections(cfg, previous, candidates)
	if err != nil || !reflect.DeepEqual(selections, cfg.Scopes) {
		t.Fatalf("inactive discovery removed configured holder or re-enrolled retired source: %+v %v", selections, err)
	}
	if previous.Collections[0].Status != "withdrawn" || !reflect.DeepEqual(previous.Collections[0].RuntimeSelection, retired) {
		t.Fatal("withdrawn history was changed")
	}
}

func TestAutomaticEnrollmentCollisionsAndOrder(t *testing.T) {
	cfg := runtimeFixture()
	for _, root := range []string{"Personal", "Personal/Nested", "../escape", "Projects/.private/Notes"} {
		if _, err := mergeEnrollmentSelections(cfg, EnrollmentInventory{}, []enrollmentCandidate{{ID: "new", Root: root}}); err == nil {
			t.Fatalf("accepted overlapping/unsafe display root %s", root)
		}
	}
	a, _ := mergeEnrollmentSelections(cfg, EnrollmentInventory{}, []enrollmentCandidate{{ID: "b", Root: "B"}, {ID: "a", Root: "A"}})
	b, _ := mergeEnrollmentSelections(cfg, EnrollmentInventory{}, []enrollmentCandidate{{ID: "a", Root: "A"}, {ID: "b", Root: "B"}})
	if !reflect.DeepEqual(a, b) {
		t.Fatal("store order changed collection identity")
	}
}

func TestAutomaticEnrollmentDisplayRoots(t *testing.T) {
	project := "project_1"
	for _, tc := range []struct{ kind, relative, name, want string }{
		{knowledge.RootKindBoxLibrary, "Library", "", "Library"},
		{knowledge.RootKindProjectNotes, "notes", "PsychoPoiesis", "Projects/PsychoPoiesis/Notes"},
		{knowledge.RootKindProjectMaterial, "docs", "Research", "Projects/Research/Documentation"},
		{knowledge.RootKindProjectMaterial, "drafts/research", "Research", "Projects/Research/drafts/research"},
	} {
		root := knowledge.SourceRoot{RootKind: tc.kind, RootRelativePath: tc.relative}
		if tc.name != "" {
			root.ProjectID = &project
		}
		got, err := enrollmentDisplayRoot(root, tc.name)
		if err != nil || got != tc.want {
			t.Fatalf("display root: %s %v", got, err)
		}
	}
	if _, err := enrollmentDisplayRoot(knowledge.SourceRoot{ProjectID: &project, RootRelativePath: "notes"}, "../escape"); err == nil {
		t.Fatal("accepted project name escape")
	}
}

func TestFilesystemFirstEnrollmentKeepsRealRelativeNames(t *testing.T) {
	project := "project_1"
	for _, relative := range []string{".", "notes", "docs", "research/chapter"} {
		root := knowledge.SourceRoot{ProjectID: &project, RootKind: knowledge.RootKindProjectMaterial, RootRelativePath: relative,
			Metadata: json.RawMessage(`{"registration_metadata":{"knowledge_source":{"schema_version":"project.contract.v0.6"}}}`)}
		want := "Projects/Research"
		if relative != "." {
			want += "/" + relative
		}
		got, err := enrollmentDisplayRoot(root, "Research")
		if err != nil || got != want {
			t.Fatalf("%s: %s %v", relative, got, err)
		}
		cfg := runtimeFixture()
		first, err := mergeEnrollmentSelections(cfg, EnrollmentInventory{}, []enrollmentCandidate{{ID: "source-files", Root: got, ProjectID: project}})
		if err != nil {
			t.Fatal(err)
		}
		previous := EnrollmentInventory{Collections: []EnrollmentCollection{{RuntimeSelection: first[len(first)-1]}}}
		again, err := mergeEnrollmentSelections(cfg, previous, []enrollmentCandidate{{ID: "source-files", Root: "Projects/Renamed/notes", ProjectID: project}})
		if err != nil || !reflect.DeepEqual(first, again) {
			t.Fatalf("existing alias changed: %+v %v", again, err)
		}
	}
}

func TestEnrollmentInventoryPublicationAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "enrollment.json")
	v := EnrollmentInventory{SchemaVersion: enrollmentSchema, Replica: "replica", NodeKey: "main", ConfigSHA256: enrollmentConfigHash(runtimeFixture()), Collections: []EnrollmentCollection{}}
	if err := writeEnrollment(path, v); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	got, err := readEnrollment(path)
	if err != nil || !reflect.DeepEqual(v, got) {
		t.Fatalf("inventory readback: %+v %v", got, err)
	}
	if err := writeEnrollment(path, v); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) || before.ModTime() != after.ModTime() || after.Mode().Perm() != 0600 {
		t.Fatal("unchanged enrollment rewrote inventory or exposed it")
	}
	bad := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(path, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := readEnrollment(bad); err == nil {
		t.Fatal("followed inventory symlink")
	}
}

func TestEnrollmentInventoryManagedReadAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "enrollment.json")
	v := EnrollmentInventory{SchemaVersion: enrollmentSchema, Replica: "replica", NodeKey: "main", Collections: []EnrollmentCollection{}}
	if err := writeEnrollment(path, v); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0600, 0640, 0650, 0644} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := readEnrollment(path); err != nil {
			t.Fatalf("managed read access %o rejected: %v", mode, err)
		}
		before, _ := os.Stat(path)
		if err := writeEnrollment(path, v); err != nil {
			t.Fatal(err)
		}
		after, _ := os.Stat(path)
		if !os.SameFile(before, after) || after.Mode().Perm() != mode {
			t.Fatal("unchanged replay rewrote managed ACL file")
		}
	}
	for _, mode := range []os.FileMode{0620, 0602, 0660, 0666} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := readEnrollment(path); err == nil {
			t.Fatalf("non-owner write %o accepted", mode)
		}
	}
	if err := os.Chmod(path, 0650); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, path+".link"); err != nil {
		t.Fatal(err)
	}
	if _, err := readEnrollment(path); err == nil {
		t.Fatal("accepted multiply linked inventory")
	}
}
