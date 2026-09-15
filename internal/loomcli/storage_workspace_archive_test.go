package loomcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/storagearchive"
)

func TestStorageWorkspaceArchiveCommandsAreRegistered(t *testing.T) {
	root := NewRootCommand()
	for _, path := range [][]string{
		{"storage", "workspace", "plan"},
		{"storage", "workspace", "apply"},
		{"storage", "workspace", "inspect"},
		{"storage", "workspace", "recover"},
		{"storage", "workspace", "restore-plan"},
		{"storage", "workspace", "restore-apply"},
	} {
		command, _, err := root.Find(path)
		if err != nil || command == nil || command.Name() != path[len(path)-1] {
			t.Fatalf("command %v: %v", path, err)
		}
	}
}

func TestStorageWorkspaceMutationsRequireExplicitConfirmationAndDigest(t *testing.T) {
	for _, name := range []string{"apply", "restore-apply", "recover"} {
		t.Run(name, func(t *testing.T) {
			root := NewRootCommand()
			root.SetArgs([]string{"storage", "workspace", name, "operation-or-plan"})
			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), "--yes and --plan-digest are required") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestReadWorkspaceMovePlanReviewRejectsUnknownAndOversizedInput(t *testing.T) {
	directory := t.TempDir()
	unknown := filepath.Join(directory, "unknown.json")
	if err := os.WriteFile(unknown, []byte(`{"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkspaceMovePlanReview(unknown); err == nil {
		t.Fatal("accepted unknown plan fields")
	}
	oversized := filepath.Join(directory, "oversized.json")
	if err := os.WriteFile(oversized, make([]byte, storagearchive.MaximumWorkspaceSurfaceRequestBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkspaceMovePlanReview(oversized); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized error=%v", err)
	}
}

func TestReadWorkspaceMovePlanReviewRequiresExactTypedContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	review := storagearchive.WorkspaceMovePlanReview{
		SchemaVersion: storagearchive.WorkspaceMovePlanReviewSchemaVersion,
		OperationID:   "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FBD",
		OperationKind: storagearchive.WorkspaceOperationArchive,
		Kind:          storagearchive.WorkspaceKindTopic, ObjectID: "topic_object", Slug: "topic-one",
		Source:          storagearchive.WorkspacePathRef{Root: storagearchive.WorkspaceRootBox, RelativePath: "Topics/topic-one"},
		Destination:     storagearchive.WorkspacePathRef{Root: storagearchive.WorkspaceRootStorage, RelativePath: "archive/topics/topic-one/workspace"},
		InventoryDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ActorID:         "actor_workspace_cli", Reason: "reviewed archive",
		PlannedAt:  time.Unix(1_900_000_000, 0).UTC(),
		PlanDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	payload, _ := json.Marshal(review)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readWorkspaceMovePlanReview(path)
	if err != nil || got.PlanDigest != review.PlanDigest {
		t.Fatalf("review=%#v err=%v", got, err)
	}
}
