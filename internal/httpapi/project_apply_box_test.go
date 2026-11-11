package httpapi

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

func TestProjectDeclarationAcceptsOnlyPreviouslyValidatedBoxQuery(t *testing.T) {
	for _, tc := range []struct {
		method, path, body string
		bound, success     bool
	}{
		{"POST", "/v1/project-declaration-plans?box_id=box_company", `{"schema_version":"project.declaration_request.v0.5","project_ref":"owned"}`, true, true},
		{"GET", "/v1/projects/owned/declaration-status?box_id=box_company", "", true, true},
		{"GET", "/v1/projects/owned/declaration-status?box_id=box_company&node_ref=main", "", true, true},
		{"POST", "/v1/project-declaration-plans?box_id=box_company&unexpected=true", `{}`, true, false},
		{"GET", "/v1/projects/owned/declaration-status?box_id=box_company&unexpected=true", "", true, false},
		{"GET", "/v1/projects/owned/declaration-status?box_id=box_company", "", false, false},
		{"GET", "/v1/projects/owned/declaration-status?box_id=missing", "", true, false},
	} {
		service := &declarationServiceStub{}
		services := Services{ProjectDeclaration: service, ProjectDeclarationRequestResolver: func(context.Context, string) (requestctx.Context, error) {
			return requestctx.Context{ActorID: ids.NewActorID(), OriginNodeID: ids.NewNodeID()}, nil
		}}
		if tc.bound {
			catalog := operationalBoxes()
			services.Boxes = &catalog
		}
		handler := NewServer(services, slog.Default()).Handler()
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, r)
		if (out.Code == 200) != tc.success || (service.calls == 1) != tc.success {
			t.Fatalf("%s bound=%v status=%d calls=%d: %s", tc.path, tc.bound, out.Code, service.calls, out.Body.String())
		}
	}
}
