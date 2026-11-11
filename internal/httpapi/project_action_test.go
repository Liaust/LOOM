package httpapi

import (
	"context"
	"testing"

	"loom.local/loom/internal/projectapply"
	pc "loom.local/loom/internal/projectcontracts"
)

type actionServiceStub struct {
	declarationServiceStub
	action pc.ProjectActionRequest
}

func (s *actionServiceStub) RunApplicationAction(_ context.Context, p projectapply.Principal, r pc.ProjectActionRequest) (pc.ProjectActionResult, error) {
	s.calls++
	s.principal, s.action = p, r
	return pc.ProjectActionResult{ProjectID: "project", Resource: r.Resource, Action: r.Action, State: "released", Finalized: true}, nil
}

func TestProjectActionHTTPAllowsReviewedReleaseWithoutIdentityInjection(t *testing.T) {
	s := &actionServiceStub{}
	h, principal := declarationTestHandler(s)
	w := declarationHTTP(t, h, "POST", "/v1/project-run-action", `{"project_ref":"housing","node_ref":"main","resource":"intake","action":"start_new","release_failed":true}`)
	if w.Code != 200 || s.calls != 1 || s.principal != principal || !s.action.ReleaseFailed || s.action.Action != "start_new" {
		t.Fatalf("%d %s %+v", w.Code, w.Body, s)
	}
	for _, field := range []string{`"actor_id":"someone"`, `"command":"sh"`, `"release_failed":true,"release_failed":false`} {
		s := &actionServiceStub{}
		h, _ := declarationTestHandler(s)
		w := declarationHTTP(t, h, "POST", "/v1/project-run-action", `{"project_ref":"housing","resource":"intake","action":"start_new",`+field+`}`)
		if w.Code < 400 || s.calls != 0 {
			t.Fatalf("accepted %s: %d", field, w.Code)
		}
	}
}
