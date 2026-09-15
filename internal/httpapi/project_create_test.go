package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/projectcontracts"
)

func TestProjectCreateExistingScaffoldEndpoint(t *testing.T) {
	parent := t.TempDir()
	server := httptest.NewServer(NewServer(Services{RuntimeConfig: config.Config{NodeID: "fixture-node"}}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	defer server.Close()
	client, err := localclient.NewHTTP(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	input := projectcontracts.ScaffoldOptions{Name: "Endpoint create", Slug: "endpoint-create", Directory: parent, Mode: projectcontracts.ScaffoldModeDeclaration, DryRun: true}
	first, err := client.ScaffoldProject(context.Background(), "corr_create", input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Data.OwnerNode != "fixture-node" || first.Data.Mode != projectcontracts.ScaffoldModeDeclaration {
		t.Fatalf("normalization lost: %+v", first.Data)
	}
	if _, err := os.Lstat(filepath.Join(parent, input.Slug)); !os.IsNotExist(err) {
		t.Fatal("backend dry-run wrote source")
	}
	input.DryRun = false
	result, err := client.ScaffoldProject(context.Background(), "corr_create", input)
	if err == nil || result.Data.ContextState != "context_pending" {
		t.Fatalf("missing registration must retain source but report pending: %+v %v", result, err)
	}
	loaded, err := projectcontracts.LoadProject(result.Data.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Declaration == nil || len(loaded.Declaration.Resources) != 0 || result.Data.ProjectID != loaded.Contract.Project.ID {
		t.Fatal("endpoint lost declaration identity")
	}
	replay, err := client.ScaffoldProject(context.Background(), "corr_create", input)
	if err == nil || replay.Data.ContextState != "context_pending" || replay.Data.ProjectID != result.Data.ProjectID {
		t.Fatalf("replay: %v", err)
	}
	entries, err := os.ReadDir(result.Data.ProjectRoot)
	if err != nil || len(entries) != 5 {
		t.Fatalf("tree: %v %v", entries, err)
	}
}
