package httpapi

import (
	"context"
	"testing"

	"loom.local/loom/internal/projectapply"
	pc "loom.local/loom/internal/projectcontracts"
)

type deploymentServiceStub struct {
	declarationServiceStub
	deployment pc.ProjectDeploymentRequest
}

func (s *deploymentServiceStub) Deploy(_ context.Context, p projectapply.Principal, r pc.ProjectDeploymentRequest) (pc.ProjectDeploymentResult, error) {
	s.calls++
	s.principal, s.deployment = p, r
	return pc.ProjectDeploymentResult{ProjectID: "project", NodeID: "main", Resource: r.Resource, State: "checked"}, nil
}

func TestProjectDeployHTTPUsesTrustedPrincipalAndPureCheck(t *testing.T) {
	s := &deploymentServiceStub{}
	h, principal := declarationTestHandler(s)
	w := declarationHTTP(t, h, "POST", "/v1/project-deploy", `{"project_ref":"housing","node_ref":"main","resource":"intake","check":true}`)
	if w.Code != 200 || s.calls != 1 || s.principal != principal || !s.deployment.Check || s.deployment.Resource != "intake" {
		t.Fatalf("%d %s %+v", w.Code, w.Body, s)
	}
}

func TestProjectDeployHTTPRejectsIdentityAndEffectInjection(t *testing.T) {
	for _, field := range []string{`"actor_id":"someone"`, `"effects":["reconcile"]`, `"command":"sh"`, `"credential_value":"not-accepted"`, `"check":false,"check":true`} {
		s := &deploymentServiceStub{}
		h, _ := declarationTestHandler(s)
		w := declarationHTTP(t, h, "POST", "/v1/project-deploy", `{"project_ref":"housing",`+field+`}`)
		if w.Code < 400 || s.calls != 0 {
			t.Fatalf("accepted %s: %d", field, w.Code)
		}
	}
}

func TestProjectApplicationTargetEffectIsExclusive(t *testing.T) {
	request := pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: "housing", Effects: []pc.DeclarationEffect{pc.ApplicationDeployEffect("intake")}}
	if err := validateDeclarationPlan(request); err != nil {
		t.Fatal(err)
	}
	request.Effects = append(request.Effects, pc.DeclarationReconcile)
	if err := validateDeclarationPlan(request); err == nil {
		t.Fatal("targeted deployment silently widened to full reconcile")
	}
}
