package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/projectapply"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/requestctx"
)

type creationConnector struct {
	ProjectDeclarationService
	fail bool
	ids  []string
}

func (c *creationConnector) ConnectCreatedProject(_ context.Context, _ projectapply.Principal, id, root, owner string) (pc.DeclarationResult, error) {
	c.ids = append(c.ids, id)
	loaded, err := pc.LoadProject(root)
	if err != nil || loaded.Declaration.Project.ID != id || len(loaded.Declaration.Resources) != 0 || owner != "main" {
		return pc.DeclarationResult{}, errors.New("unexpected registration")
	}
	if c.fail {
		return pc.DeclarationResult{}, errors.New("registration unavailable")
	}
	return pc.DeclarationResult{SchemaVersion: pc.DeclarationResultSchemaV05, OperationID: ids.NewJobID(), State: pc.DeclarationOperationSucceeded}, nil
}

func TestProjectCreateBackendConnectionRetry(t *testing.T) {
	connector := &creationConnector{fail: true}
	server := httptest.NewServer(NewServer(Services{RuntimeConfig: config.Config{NodeID: "main"}, ProjectDeclaration: connector,
		ProjectDeclarationRequestResolver: func(context.Context, string) (requestctx.Context, error) {
			return requestctx.Context{ActorID: ids.NewActorID(), OriginNodeID: ids.NewNodeID()}, nil
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	defer server.Close()
	client, err := localclient.NewHTTP(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	input := pc.ScaffoldOptions{Name: "Connection", Slug: "connection", OwnerNode: "main", Mode: pc.ScaffoldModeDeclaration, Directory: t.TempDir()}
	first, err := client.ScaffoldProject(context.Background(), "test", input)
	if err == nil || first.Data.OK || first.Data.SourceState != "source_created" || first.Data.ContextState != "context_pending" || first.Data.ProjectID == "" {
		t.Fatalf("lost partial source: %+v %v", first, err)
	}
	state := filepath.Join(first.Data.ProjectRoot, ".project/STATE.md")
	if err := os.WriteFile(state, []byte("User's current work"), 0600); err != nil {
		t.Fatal(err)
	}
	connector.fail = false
	second, err := client.ScaffoldProject(context.Background(), "retry", input)
	if err != nil || !second.Data.OK || second.Data.ProjectID != first.Data.ProjectID || second.Data.ContextState != "registered_refresh_pending" || len(connector.ids) != 2 || connector.ids[0] != connector.ids[1] {
		t.Fatalf("retry: %+v %v", second, err)
	}
	raw, err := os.ReadFile(state)
	if err != nil || string(raw) != "User's current work" {
		t.Fatal("retry overwrote source")
	}
	input.DryRun = true
	if _, err := client.ScaffoldProject(context.Background(), "dry", input); err != nil || len(connector.ids) != 2 {
		t.Fatal("dry run registered identity")
	}
}
