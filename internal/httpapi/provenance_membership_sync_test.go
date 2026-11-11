package httpapi

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/requestctx"
)

type membershipProjectionTransport struct {
	*provenanceTransportStub
	input     provenance.ProjectProjectionMembershipInput
	selection boxregistry.ResolvedSelection
	calls     int
}

func (s *membershipProjectionTransport) SyncProjectProjectionMembership(ctx context.Context, input provenance.ProjectProjectionMembershipInput) (provenance.ProjectProjectionSyncReceipt, error) {
	s.input, s.calls = input, s.calls+1
	s.selection, _ = boxregistry.FromContext(ctx)
	return provenance.ProjectProjectionSyncReceipt{SchemaVersion: provenance.ProjectProjectionSchemaVersion, ProjectID: input.ProjectID, ProjectSnapshotID: "11111111-1111-4111-8111-111111111111"}, nil
}

func TestProvenanceMembershipOnlySyncDoesNotObserveSources(t *testing.T) {
	projectID := "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	db, query := newHTTPStorageFakeDB(t)
	t.Cleanup(func() { _ = db.Close() })
	query.expect("p.metadata->>'home_box_id'", func(args []driver.NamedValue) {
		if args[0].Value != projectID || args[1].Value != `["box_company"]` {
			t.Fatal(args)
		}
	}, registeredBoxProjectRow(projectID, "box_company", "archived"))
	transport := &membershipProjectionTransport{provenanceTransportStub: &provenanceTransportStub{}}
	idem := &provenanceIdempotencyStub{}
	resolve := func(_ context.Context, cid string) (requestctx.Context, error) {
		return requestctx.Context{ActorID: "actor_authenticated", OriginNodeID: "node_authenticated", CorrelationID: cid}, nil
	}
	// No Source/RepositoryState observer is configured: membership-only must
	// work from the registered project plus retained projection, not a reread.
	server := NewServer(Services{DB: db, Projects: projects.NewService(db), Provenance: transport, ProvenanceAuthorizer: &provenanceAuthorizerStub{}, Idempotency: idem, ProvenanceRequestResolver: resolve,
		ProjectRepos: ProjectRepositoryServices{Authorizer: &provenanceProjectReadAuthorizerStub{projectID: projectID}, RequestResolver: resolve}}, slog.Default())
	r := httptest.NewRequest(http.MethodPost, "/v1/provenance/projects/archived/sync?membership_only=true", strings.NewReader(`{"home_box_id":"box_personal"}`)).WithContext(navigationBoxContext("box_company"))
	r.Header.Set(idempotency.Header, "project-membership-only")
	out := httptest.NewRecorder()
	server.handleProvenanceProjectProjection(out, r)
	if out.Code != 200 || transport.calls != 1 || transport.input.ProjectID != projectID || transport.input.HomeBoxID != "box_company" || !transport.selection.Contains("box_company") || transport.repoSyncInput.Project.Project.ProjectID != "" {
		t.Fatal(out.Code, out.Body.String(), transport.input)
	}
	encoded, _ := json.Marshal(idem.beginInput.Request)
	if !strings.Contains(string(encoded), `"membership_only":"true"`) || !strings.Contains(string(encoded), `"home_box_id":"box_company"`) || strings.Contains(string(encoded), "box_personal") || !idem.completed {
		t.Fatal("repair mode/evidence not bound to idempotent request", string(encoded), idem.completed)
	}
	query.requireDone()
}

func TestProvenanceMembershipOnlyFlagIsExplicitAndBounded(t *testing.T) {
	for _, flag := range []string{"membership_only=1", "membership_only=", "membership_only=true&membership_only=false"} {
		server := NewServer(Services{}, slog.Default())
		out := httptest.NewRecorder()
		server.handleProvenanceProjectProjection(out, httptest.NewRequest(http.MethodPost, "/v1/provenance/projects/exact/sync?"+flag, nil))
		if out.Code != http.StatusBadRequest {
			t.Fatal(flag, out.Code, out.Body.String())
		}
	}
}
