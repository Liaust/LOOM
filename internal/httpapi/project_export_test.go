package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectexport"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/workspacefiles"
)

func archivedExportFixture(t *testing.T) (Server, projects.ProjectRegistrationDetail, storagearchive.ProjectArchiveInspectResult, string) {
	t.Helper()
	storage := t.TempDir()
	scaffold, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{Name: "Export One", Slug: "one", OwnerNode: "main", Preset: projectcontracts.PresetMinimal, Directory: filepath.Join(storage, "Projects")})
	if err != nil {
		t.Fatal(err)
	}
	root := scaffold.ProjectRoot
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("a", 64)
	state := projects.ProjectPhysicalArchiveState{
		SchemaVersion: projects.ProjectPhysicalArchiveStateSchemaVersion, ProjectID: "project_one", ProjectSlug: "one", Status: "archived",
		Phase: projects.ProjectArchivePhaseComplete, MutationBlocked: true,
		OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV", PlanDigest: digest, WorkspacePlanDigest: digest,
		ActivePath: "/old/Box/Projects/one", ArchivePath: filepath.Join(storage, "archive/projects/one/project"),
		ActorID: "owner", Reason: "Owned export fixture", StartedAt: at, RuntimeDeactivatedAt: &at, WorkspaceCompletedAt: &at, ArchivedAt: &at,
		RuntimeQuiescenceDigest: digest, ArchiveManifestDigest: digest,
	}
	raw, err := json.Marshal(state)
	if err != nil || projects.ValidateProjectPhysicalArchiveState(state) != nil {
		t.Fatal("invalid owned archive state", err)
	}
	detail := projects.ProjectRegistrationDetail{Project: projects.ProjectDetail{Project: projects.Project{ProjectID: "project_one", Slug: "one", Status: "archived", ArchiveState: raw}}}
	inspection := storagearchive.ProjectArchiveInspectResult{Project: detail, Physical: &storagearchive.ProjectPhysicalArchiveInspection{
		ProjectID: "project_one", ArchiveOperationID: state.OperationID, OperationID: state.OperationID, PlanDigest: digest,
		Status: "archived", EvidenceStatus: "verified", CurrentPayload: &storagearchive.WorkspacePathRef{Root: storagearchive.WorkspaceRootStorage, RelativePath: "Projects/one", AbsolutePath: root},
	}}
	files := &workspacefiles.Service{Roots: []workspacefiles.Root{{Ref: "storage", SourcePath: storage, ReadOnly: true}}}
	t.Cleanup(func() { _ = files.Close() })
	server := NewServer(Services{Files: files}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.projectRegistrationStatus = func(context.Context, string) (projects.ProjectRegistrationDetail, error) { return detail, nil }
	server.projectArchiveInspection = func(context.Context, string) (storagearchive.ProjectArchiveInspectResult, error) {
		return inspection, nil
	}
	return server, detail, inspection, root
}

func TestProjectExportReadsVerifiedRelocatedArchive(t *testing.T) {
	server, _, _, root := archivedExportFixture(t)
	if got, err := server.resolveBackendProjectExportRoot(context.Background(), "project_one", ""); err != nil || got != root {
		t.Fatalf("current archive root=%s err=%v", got, err)
	}
	if _, err := server.resolveBackendProjectExportRoot(context.Background(), "project_one", "/old/Box/Projects/one"); err == nil {
		t.Fatal("historical requested root was accepted")
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	client, err := localclient.NewHTTP(httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "archive.tar")
	result, err := client.ExportProject(context.Background(), "corr_export", projectexport.Request{ProjectRef: "project_one", Mode: projectexport.ModePortable}, output, false)
	if err != nil || result.ArchiveChecksum == "" || result.ArchiveBytes == 0 {
		t.Fatalf("export=%+v err=%v", result, err)
	}
}

func TestProjectExportRefusesUnverifiedOrSubstitutedArchive(t *testing.T) {
	for _, change := range []string{"identity", "operation", "digest", "evidence", "active", "absolute", "escape", "symlink", "unavailable"} {
		t.Run(change, func(t *testing.T) {
			server, _, inspection, root := archivedExportFixture(t)
			p := inspection.Physical
			switch change {
			case "identity":
				p.ProjectID = "other"
			case "operation":
				p.ArchiveOperationID = "other"
			case "digest":
				p.PlanDigest = "sha256:" + strings.Repeat("b", 64)
			case "evidence":
				p.EvidenceStatus = "pending"
			case "active":
				p.Status = "restored"
			case "absolute":
				p.CurrentPayload.AbsolutePath = t.TempDir()
			case "escape":
				p.CurrentPayload.RelativePath = "../outside"
			case "symlink":
				moved := root + "-moved"
				if err := os.Rename(root, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, root); err != nil {
					t.Fatal(err)
				}
			case "unavailable":
				server.projectArchiveInspection = nil
			}
			if _, err := server.resolveBackendProjectExportRoot(context.Background(), "project_one", ""); err == nil {
				t.Fatal("unsafe archived export accepted")
			}
		})
	}
}
