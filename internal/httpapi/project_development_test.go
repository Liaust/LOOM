package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/projectapply"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
)

type historicalDevelopmentObserver struct {
	source projectstate.ProvenanceProjectSource
}

func TestProjectDevelopmentDisclosesActualTruncatedFields(t *testing.T) {
	observer := historicalDevelopmentObserver{source: projectstate.ProvenanceProjectSource{Projection: projectstate.ProjectProjection{Development: projectstate.ProjectDevelopmentState{
		Posture: projectstate.ProjectDevelopmentReady, SourceDigest: "exact-source",
		Purpose: "A purpose ending in ...", CurrentFocus: strings.Repeat("字", 200),
		Progress: strings.Repeat("字", 201), Blockers: strings.Repeat("blocked ", 50), NextAction: "Read the source.",
	}}}}
	server := Server{services: Services{ProjectRepos: ProjectRepositoryServices{Provenance: observer}}}
	status := pc.DeclarationStatus{Target: pc.DeclarationTarget{ProjectID: "project"}}
	server.attachProjectDevelopment(t.Context(), &status)
	d := status.Development
	if !reflect.DeepEqual(d.TruncatedFields, []string{"progress", "blockers"}) || len([]rune(d.Progress)) != 203 || !utf8.ValidString(d.Progress) || d.Purpose != observer.source.Projection.Development.Purpose || d.CurrentFocus != observer.source.Projection.Development.CurrentFocus || d.SourceDigest != "exact-source" {
		t.Fatalf("truncation was hidden, guessed from punctuation or lost identity: %+v", d)
	}
}

func TestProjectDevelopmentConversationIndexIsBoundedSource(t *testing.T) {
	observer := historicalDevelopmentObserver{source: projectstate.ProvenanceProjectSource{Projection: projectstate.ProjectProjection{Development: projectstate.ProjectDevelopmentState{
		Posture: projectstate.ProjectDevelopmentReady, Documents: []projectstate.ProjectDevelopmentDocument{{Path: ".project/CONVERSATIONS.md", Posture: "present", Hash: "exact-hash", Excerpt: "Thread actual-id\n" + strings.Repeat("字", 600)}},
	}}}}
	server := Server{services: Services{ProjectRepos: ProjectRepositoryServices{Provenance: observer}}}
	status := pc.DeclarationStatus{Target: pc.DeclarationTarget{ProjectID: "project"}}
	server.attachProjectDevelopment(t.Context(), &status)
	index := status.Development.Conversations
	if index == nil || index.Hash != "exact-hash" || index.Path != ".project/CONVERSATIONS.md" || len(index.Excerpt) > 1200 || !index.Truncated || !utf8.ValidString(index.Excerpt) {
		t.Fatalf("index changed identity or exceeded bound: %+v", index)
	}
}

func (o historicalDevelopmentObserver) ObserveProjectForProvenance(context.Context, string) (projectstate.ProvenanceProjectSource, error) {
	return o.source, nil
}

func (o historicalDevelopmentObserver) ObserveProjectContextForProvenance(context.Context, string) (projectstate.ProvenanceProjectSource, error) {
	return o.source, nil
}

func TestProjectDevelopmentArchivedRequiresVerifiedCustody(t *testing.T) {
	observer := historicalDevelopmentObserver{source: projectstate.ProvenanceProjectSource{Projection: projectstate.ProjectProjection{Development: projectstate.ProjectDevelopmentState{Posture: projectstate.ProjectDevelopmentPartial, Purpose: "Retained project", MissingDocuments: []string{".project/STATE.md"}}}}}
	server := Server{services: Services{ProjectRepos: ProjectRepositoryServices{Provenance: observer}}}
	status := pc.DeclarationStatus{Lifecycle: "archived", Target: pc.DeclarationTarget{ProjectID: "project_archived"}}
	server.attachProjectDevelopment(t.Context(), &status)
	if status.Development == nil || status.Development.Posture != "unavailable" || status.Development.Purpose != "" || status.Development.Archive != nil {
		t.Fatal("unverified retained context exposed")
	}
	observer.source.Projection.Development.Archive = &projectstate.ArchivedSourceContext{OperationID: "operation", ManifestDigest: "digest", ArchivedAt: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)}
	server.services.ProjectRepos.Provenance = observer
	server.attachProjectDevelopment(t.Context(), &status)
	if status.Development.Posture != "partial" || status.Development.Purpose != "Retained project" || status.Development.Archive.OperationID != "operation" || len(status.Development.Archive.MissingDocuments) != 1 {
		t.Fatalf("historical evidence lost: %+v", status.Development)
	}
}

type creationConnector struct {
	ProjectDeclarationService
	fail bool
	ids  []string
}

func TestProjectCreatePendingRetainsOnlySafeTypedCause(t *testing.T) {
	for _, cause := range []error{&projectapply.Failure{Code: pc.DeclarationPlanStale, Cause: "registration_source_changed"}, errors.New("private diagnostic detail")} {
		writer := httptest.NewRecorder()
		Server{}.writeProjectCreatePending(writer, httptest.NewRequest("POST", "/", nil), pc.ScaffoldResult{}, "project.context_pending", "Source retained.", cause)
		var result response.ErrorEnvelope
		if err := json.Unmarshal(writer.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(writer.Body.String(), "private diagnostic detail") {
			t.Fatal("untyped private cause leaked")
		}
		if _, typed := cause.(*projectapply.Failure); typed && result.Error.Hint != "declaration.plan_stale: registration_source_changed" {
			t.Fatalf("typed cause was hidden: %+v", result.Error)
		}
	}
}

func (c *creationConnector) ConnectCreatedProject(_ context.Context, _ projectapply.Principal, id, root, owner string) (pc.DeclarationResult, error) {
	c.ids = append(c.ids, id)
	loaded, err := pc.LoadProject(root)
	if err != nil || loaded.Declaration.Project.ID != id || !pc.IsDefaultFilesystemDeclaration(*loaded.Declaration) || owner != "main" {
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
